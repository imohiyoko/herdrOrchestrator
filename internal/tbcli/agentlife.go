package tbcli

import (
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"

	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// agent の止める・戻す(taskboard/lib/agents.sh の authorize_op / op_entry / do_agent_stop / cmd_agent-stop)。
// 作る(agent-new)・戻す(agent-revive)・再起動(agent-restart)も。承認(agent-approve / agent-reject)と tb agent-spawn の子の作成はまだ bash。
// 承認待ち(L-<n>)の行は bash と同じ形で積むので、承認は bash の tb agent-approve がそのまま実行できる。

// opResult は do_agent_* の結果(OP_OUT と終了コード)。
type opResult struct {
	code int
	out  string
	note string // 最初の指示を送れなかった・確かめられなかった理由(FP_NOTE)
}

// authorizeOp は authorize_op: 人なら true。agent なら policy の agent:<op> <名前> が allow のとき true、
// deny なら却下の行を残して 4、それ以外(ask-*)は L-<n> を積んで「承認待ち」を出し、終了コード 20 を返す。
func authorizeOp(db *store.DB, op, target, args, by string, w io.Writer) (bool, error) {
	if by == "human" {
		return true, nil
	}
	j := approval.NewJudge(db.DB, db.Session)
	if op == "new" { // 作成は、名前から推定した役割ではなく要求された役割(--role)で @<役割> を判定する
		var a struct {
			Role string `json:"role"`
		}
		if json.Unmarshal([]byte(args), &a) == nil && a.Role != "" {
			j.Roles.Hint(target, a.Role)
		}
	}
	v, rule := j.Of("agent:"+op+":"+target, by)
	if v == "allow" {
		return true, nil
	}
	if v == "deny" {
		ts := nowTS()
		_, _ = db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at) VALUES (?,?,?,?,?, 'rejected', ?, ?, ?)`,
			db.Session, op, target, args, by, "認可: "+rule, ts, ts)
		return false, &exit{4, "agent:" + op + " " + target + " は実行できません(deny。" + rule + ")"}
	}
	res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?,?,?,?,?, 'pending', ?, ?)`,
		db.Session, op, target, args, by, "認可: "+rule, nowTS())
	if err != nil {
		return false, err
	}
	id, _ := res.LastInsertId()
	q := pendingQ(db.DB, "agent_ops", id)
	fmt.Fprintf(w, "L-%d pending %s (承認待ち: tb approval approve %s)\n", id, q, q)
	return false, &exit{20, ""}
}

// opEntry は op_entry: 承認を確かめて実行し、記録して結果を出す。
func opEntry(db *store.DB, op, target, args, by string, w io.Writer, run func() opResult) error {
	if ok, err := authorizeOp(db, op, target, args, by, w); !ok {
		return err
	}
	r := run()
	recordOp(db, op, target, args, by, r.code, r.out)
	if r.code != 0 {
		return &exit{r.code, r.out}
	}
	fmt.Fprintln(w, r.out)
	return nil
}

// killableOf は killable_of: protected|yes|no|unknown|stale。
func killableOf(db *store.DB, name string) string { return agents.Killable(db.DB, db.Session, name) }

// liveOf は live_row: herdr の一覧から名前が一致する 1 件。
func liveOf(live []herdr.Agent, name string) (herdr.Agent, bool) { return agents.LiveOf(live, name) }

// closePane は agent_close_pane: pane を閉じて台帳を stopped にする(session は残す)。閉じられなければ 11。
func closePane(db *store.DB, name, pane, sess string) *opResult {
	if !agents.ClosePane(db.DB, db.Session, name, pane, sess) {
		return &opResult{code: 11, out: "pane " + pane + " を閉じられません"}
	}
	return nil
}

// doAgentStop は do_agent_stop(中身は app/agent.Stop。承認後の実行・recipe から呼ぶ)。
func doAgentStop(db *store.DB, st tickets.StatusSet, name string, force bool, by string) opResult {
	r := agent.Stop(db, st, name, force, by)
	return opResult{code: r.Code, out: r.Out}
}

// cmdAgentStop は tb agent-stop <名前> [--force]。中身はアプリケーション API の agent.stop(ADR 0008)。
func cmdAgentStop(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-stop <名前> [--force]")
	}
	name, force := args[0], false
	if err := validAgentName(name); err != nil {
		return err
	}
	for _, a := range args[1:] {
		if a != "--force" {
			return usage("agent-stop: 不明な引数 %s", a)
		}
		force = true
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	var res agent.StopResult
	if err := callAppAs(db, app.PrincipalOf(by), "agent.stop", agent.StopReq{Name: name, Force: force}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.Out)
	return nil
}

// ---------- 戻す(revive) ----------

var modelRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

const kindDefault = "claude"

func kindValid(k string) bool { return slices.Contains(kindAll, k) }

// kindRequire は kind_require: 対応していなければ理由(KIND_ERR)。
func kindRequire(kind, item, note string) (string, bool) {
	if !kindValid(kind) {
		return "kind=" + kind + " は未知の kind です(使えるのは: " + strings.Join(kindAll, " ") + ") item=kind", false
	}
	if kindSupports(kind, item) {
		return "", true
	}
	if note != "" {
		note = " (" + note + ")"
	}
	return "kind=" + kind + " は " + item + " に対応していません item=" + item + note + "。claude の引数を代わりに渡すことはしません(docs/agent-kinds.md)", false
}

// kindCheckLaunch は kind_check_launch: 要求された起動(モデル指定・会話の再開)に対応していなければ 12。
func kindCheckLaunch(kind, model, sess string) *opResult {
	if m, ok := kindRequire(kind, "launch", ""); !ok {
		return &opResult{code: 12, out: m}
	}
	if model != "" {
		if m, ok := kindRequire(kind, "model", "--model "+model); !ok {
			return &opResult{code: 12, out: m}
		}
		if kind != "claude" || !modelRE.MatchString(model) {
			return &opResult{code: 12, out: "kind=" + kind + " のモデル名として使えません item=model: " + model}
		}
	}
	if sess != "" {
		if m, ok := kindRequire(kind, "resume", "session "+sess+" で再開できない。新しい会話にするなら --fresh"); !ok {
			return &opResult{code: 12, out: m}
		}
	}
	return nil
}

// kindLaunchArgs は kind_launch_args: herdr agent start の -- 以降。
func kindLaunchArgs(kind, sess, model string) []string {
	var a []string
	if kind == "claude" {
		if sess != "" {
			a = append(a, "--resume", sess)
		}
		if model != "" {
			a = append(a, "--model", model)
		}
	}
	return a
}

// roleModelFor は role_model_for: 設定 role-models で、名前(name:<glob>)か役割の glob に当たるモデル。
// reg が空でなければ台帳の role の代わりに使う(role_name_of の第 2 引数)。
func roleModelFor(db *store.DB, name, reg string) string {
	body, _, ok := config.Lookup(db.DB, db.Session, "role-models")
	if !ok {
		return ""
	}
	r := agents.NewRoles(db.DB, db.Session)
	var have []string
	if reg != "" {
		have = r.FilesForReg(name, reg)
	} else if s, _ := r.NameOf(name); s != "" {
		have = strings.Split(s, ",")
	}
	for _, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		k, v, has := strings.Cut(line, "=")
		if !has {
			continue
		}
		k, v = config.Trim(k), config.Trim(v)
		if !modelRE.MatchString(v) {
			continue
		}
		if p, ok := strings.CutPrefix(k, "name:"); ok {
			if util.Glob(p, name) {
				return v
			}
			continue
		}
		for _, x := range have {
			if util.Glob(k, x) {
				return v
			}
		}
	}
	return ""
}

// modelWithRoleDefault は model_with_role_default: 明示が無ければ役割の既定モデル(kind がモデル指定に対応するときだけ)。
func modelWithRoleDefault(db *store.DB, model, kind, name, role string) string {
	if model != "" {
		return model
	}
	if !kindSupports(kind, "model") {
		return ""
	}
	return roleModelFor(db, name, role)
}

// agentRegister は agent_register: 台帳に登録する(新しい命のたびに判定フラグは付け直し)。
func agentRegister(db *store.DB, name, cwd, model, role, by, kill, why, kind string) error {
	ts := nowTS()
	var kby, kat any
	if kill != "unknown" {
		kby, kat = by, ts
	}
	_, err := db.Exec(`INSERT INTO main.agents (name, kind, herdr_session, cwd, model, role, state, killable, killable_reason, killable_by, killable_at, created_by, created_at, updated_at)
    VALUES (?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), 'running', ?, NULLIF(?,''), ?, ?, ?, ?, ?)
    ON CONFLICT(herdr_session, name) DO UPDATE SET state='running', stopped_at=NULL, pane_id=NULL, kind=excluded.kind,
      cwd=COALESCE(excluded.cwd, cwd), model=COALESCE(excluded.model, model), role=COALESCE(excluded.role, role),
      killable=excluded.killable, killable_reason=excluded.killable_reason, killable_by=excluded.killable_by,
      killable_at=excluded.killable_at, killable_session=NULL, updated_at=excluded.updated_at`,
		name, kind, db.Session, cwd, model, role, kill, why, kby, kat, by, ts, ts)
	return err
}

// firstPrompt は first_prompt: 起動直後の agent に最初の指示を送る。送れない・確かめられないときは理由(FP_NOTE)。
func firstPrompt(db *store.DB, name, prompt, by string, stderr io.Writer) string {
	if !transport.WaitReady(name) {
		fmt.Fprintf(stderr, "tb: %s が入力待ちにならないため最初の指示を送っていません\n", name)
		return "最初の指示は未送信(" + envOr("TB_SPAWN_READY_SEC", "60") + " 秒待っても入力待ちになりません。tb send で送り直す)"
	}
	sent := false
	for i := 0; i < 5; i++ {
		if transport.Send(db.DB, db.Session, name, "[from: tb / agent / "+by+"] "+util.Oneline(prompt, 4000), "agent:"+name, "agent", io.Discard) == transport.Sent {
			sent = true
			break
		}
		time.Sleep(time.Duration(secEnv("TB_SPAWN_RETRY_SEC", 3) * float64(time.Second)))
	}
	if !sent {
		return "最初の指示は未送信(tb send などで送り直す)"
	}
	if !transport.VerifyPrompt(name) {
		fmt.Fprintf(stderr, "tb: %s に送ったが処理を確認できません。pane で Enter を押してください\n", name)
		return "最初の指示は送ったが処理が始まったか確認できません(二重になるので再送せず、" + name + " の pane で Enter を押してください)"
	}
	return ""
}

func secEnv(k string, def float64) float64 {
	if f, err := strconv.ParseFloat(envOr(k, strconv.FormatFloat(def, 'f', -1, 64)), 64); err == nil {
		return f
	}
	return 0
}

// limitsRefuseRunning は limits_refuse_running <add>: 動作中の agent が add 増えると上限を超えるなら理由を stderr に出して 4。
func limitsRefuseRunning(db *store.DB, add int, stderr io.Writer) error {
	running, _, ok := limitsCounts(db)
	if !ok {
		return nil
	}
	max, lim := limits.Load(db.DB, db.Session).Max("max_running_agents")
	if limits.Exceeds(running+add, max, lim) {
		fmt.Fprintf(stderr, "tb: 動作中の agent が上限です(動作中 %d、上限 max_running_agents=%d)。何も作りません。 止める候補は tb agent-shed、上限を変えるのは人(tb limits set max_running_agents <数>)\n", running, max)
		return &exit{4, ""}
	}
	return nil
}

// agentCwd は AGENT_CWD(TB_AGENT_CWD、既定は TB_ROOT/..)。
func agentCwd() string {
	if d := os.Getenv("TB_AGENT_CWD"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("TB_ROOT"), "..")
}

func isDir(d string) bool {
	st, err := os.Stat(d)
	return err == nil && st.IsDir()
}

type regRow struct {
	ok                     bool
	cwd, model, sess, kind string
}

func agentRegRow(db *store.DB, name string) regRow {
	var r regRow
	var cwd, model, sess, kind sql.NullString
	if db.QueryRow(`SELECT cwd, model, session_id, kind FROM agents WHERE name=?`, name).Scan(&cwd, &model, &sess, &kind) == nil {
		r = regRow{true, cwd.String, model.String, sess.String, kind.String}
		if r.kind == "" {
			r.kind = kindDefault
		}
	}
	return r
}

// doAgentRevive は do_agent_revive。
func doAgentRevive(db *store.DB, name string, fresh bool, model, prompt, by string, stderr io.Writer) opResult {
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return opResult{code: 11, out: "herdr agent list に失敗しました"}
	}
	if _, ok := liveOf(live, name); ok {
		return opResult{code: 1, out: name + " はすでに動いています"}
	}
	row := agentRegRow(db, name)
	if !row.ok {
		if msg, other := otherSession(db, name); other {
			return opResult{code: 1, out: msg}
		}
		return opResult{code: 1, out: name + " は台帳にありません(新しく作るなら tb agent-new)"}
	}
	cwd := row.cwd
	if model == "" {
		model = row.model
	}
	lmodel := model // 台帳に書くのは明示か台帳の値だけ(役割の既定は書かない)
	sess := ""
	if !fresh {
		sess = row.sess
	}
	model = modelWithRoleDefault(db, model, row.kind, name, "")
	if r := kindCheckLaunch(row.kind, model, sess); r != nil {
		return *r
	}
	if cwd == "" || !isDir(cwd) {
		if sess != "" {
			c := cwd
			if c == "" {
				c = "未記録"
			}
			return opResult{code: 1, out: name + " の作業ディレクトリがありません(" + c + ")。--fresh なら " + agentCwd() + " で新しい会話として起動します"}
		}
		cwd = physDir(agentCwd())
	}
	if err := agentRegister(db, name, cwd, lmodel, "", by, "unknown", "", row.kind); err != nil {
		return opResult{code: 1, out: err.Error()}
	}
	sp, ok := transport.Spawn(env.TBDB(), row.kind, name, cwd, kindLaunchArgs(row.kind, sess, model))
	if !ok {
		if sp.Kept {
			_, _ = db.Exec(`UPDATE main.agents SET pane_id=? WHERE herdr_session=? AND name=?`, sp.Pane, db.Session, name)
			out := name + ": " + sp.Err
			if prompt != "" {
				out += "。最初の指示は未送信(答えたあと tb send などで送る)"
			}
			return opResult{code: 11, out: out}
		}
		_, _ = db.Exec(`UPDATE main.agents SET state='stopped', stopped_at=? WHERE herdr_session=? AND name=?`, nowTS(), db.Session, name)
		return opResult{code: 11, out: name + " を起動できません: " + sp.Err}
	}
	_, _ = db.Exec(`UPDATE main.agents SET pane_id=? WHERE herdr_session=? AND name=?`, sp.Pane, db.Session, name)
	how := "新しい会話"
	if sess != "" {
		how = "resume " + sess
	}
	out := name + " revived (" + how + ", pane " + sp.Pane + ", cwd " + cwd + ")"
	note := ""
	if prompt != "" {
		if note = firstPrompt(db, name, prompt, by, stderr); note != "" {
			out += " / " + note
		}
	}
	return opResult{code: 0, out: out, note: note}
}

// physDir は $(cd D && pwd): 絶対パス(シンボリックリンクは解かない。bash の pwd と同じ論理パス)。
func physDir(d string) string {
	if filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	wd := os.Getenv("PWD")
	if wd == "" {
		wd, _ = os.Getwd()
	}
	return filepath.Join(wd, d)
}

func cmdAgentRevive(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-revive <名前> [--fresh] [--model M] [--prompt P]")
	}
	name, fresh, model, prompt := args[0], false, "", ""
	if err := validAgentName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		switch a := args[i]; a {
		case "--fresh":
			fresh = true
		case "--model", "--prompt":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			if a == "--model" {
				model = args[i+1]
			} else {
				prompt = args[i+1]
			}
			i++
		default:
			return usage("agent-revive: 不明な引数 %s", a)
		}
	}
	if model != "" && !modelRE.MatchString(model) {
		return usage("agent-revive: --model に使えない文字があります: %s", model)
	}
	if utf8.RuneCountInString(prompt) > 4000 {
		return usage("agent-revive: --prompt は 4000 字まで")
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if row := agentRegRow(db, name); row.ok { // 台帳にあれば、その kind でできない依頼は承認待ちに積む前に止める
		m, sess := model, ""
		if m == "" {
			m = row.model
		}
		if !fresh {
			sess = row.sess
		}
		if r := kindCheckLaunch(row.kind, m, sess); r != nil {
			return &exit{12, r.out}
		}
	}
	if err := limitsRefuseRunning(db, 1, stderr); err != nil {
		return err
	}
	by, err := requester()
	if err != nil {
		return err
	}
	f := 0
	if fresh {
		f = 1
	}
	kv := []any{"fresh", f}
	if model != "" {
		kv = append(kv, "model", model)
	}
	if prompt != "" {
		kv = append(kv, "prompt", prompt)
	}
	return opEntry(db, "revive", name, jsonC(kv...), by, w, func() opResult { return doAgentRevive(db, name, fresh, model, prompt, by, stderr) })
}

// ---------- 再起動(restart) ----------

const restartDefaultPrompt = "MCP の接続を張り直すために再起動しました。直前の作業を続けてください。まず MCP のツールが使えることを確認し、まだ使えないなら tb comment で報告してください。"

// doAgentRestart は do_agent_restart: 停止 + 同じ会話での復活。安全条件を全部満たすときだけ。
// 0 / 1 / 4 / 10(再開したが最初の指示を確認できない) / 11 / 12
func doAgentRestart(db *store.DB, st tickets.StatusSet, name, prompt, by string, stderr io.Writer) opResult {
	t0 := time.Now()
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return opResult{code: 11, out: "herdr agent list に失敗しました"}
	}
	a, ok := liveOf(live, name)
	if !ok {
		if msg, other := otherSession(db, name); other {
			return opResult{code: 1, out: msg}
		}
		return opResult{code: 1, out: name + " は動いていません(再起動は動いている agent だけ。止まっているなら tb agent-revive " + name + ")"}
	}
	if by == name {
		return opResult{code: 1, out: "自分自身は再起動できません"}
	}
	pane, oldSess := a.PaneID, a.AgentSession.Value
	status, _ := a.AgentStatus.(string)
	if pane == "" {
		return opResult{code: 1, out: name + " の pane が分かりません"}
	}
	if status != "idle" && status != "done" {
		s := status
		if s == "" {
			s = "状態不明"
		}
		return opResult{code: 1, out: name + " は " + s + " のため再起動しません(idle か done のときだけ)"}
	}
	var open string
	_ = db.QueryRow(`SELECT COALESCE(group_concat(id, ' '),'') FROM tasks WHERE owner_agent=? AND status IN (`+st.SQLList("active")+`)`, name).Scan(&open)
	if open != "" && blank(prompt) {
		return opResult{code: 1, out: name + " は " + st.Main("active") + " のタスク(" + open + ")を持っています。再開メモを --prompt で渡したときだけ再起動できます"}
	}
	row := agentRegRow(db, name)
	if oldSess == "" {
		oldSess = row.sess
	}
	kind := row.kind // 台帳に無ければ空(bash と同じく未知の kind として止める)
	// 閉じてから再開できないと困るので、再開できることを先に確かめる(だめなら何も止めない)
	if m, ok := kindRequire(kind, "resume", "同じ会話に戻れないので止めません"); !ok {
		return opResult{code: 12, out: m}
	}
	if oldSess == "" {
		return opResult{code: 1, out: name + " の session id が台帳にありません。同じ会話に戻れないので止めません"}
	}
	if row.cwd == "" || !isDir(row.cwd) {
		c := row.cwd
		if c == "" {
			c = "未記録"
		}
		return opResult{code: 1, out: name + " の作業ディレクトリがありません(" + c + ")。同じ会話に戻れないので止めません"}
	}
	if blank(prompt) {
		prompt = restartDefaultPrompt
	}
	if r := closePane(db, name, pane, oldSess); r != nil {
		return *r
	}
	_, _ = agents.SyncLive(db.DB, db.Session)
	if limitsRefuseRunning(db, 1, io.Discard) != nil {
		return opResult{code: 4, out: name + " を停止しましたが、動作中の agent が上限のため復活していません。停止のままです。上限が空いたら: tb agent-revive " + name + " [--fresh]"}
	}
	rev := doAgentRevive(db, name, false, "", prompt, by, stderr)
	if rev.code != 0 {
		return opResult{code: rev.code, out: name + " を停止しました(pane " + pane + " を閉じた)が、復活に失敗: " + rev.out + "。停止のままです。自分で戻すなら: tb agent-revive " + name + " [--fresh](--fresh は新しい会話になります)"}
	}
	live, _ = agents.SyncLive(db.DB, db.Session)
	cur, _ := liveOf(live, name)
	how := ""
	switch ns := cur.AgentSession.Value; {
	case ns == "":
		how = "session unconfirmed (herdr reports none yet; was " + oldSess + ")"
	case ns == oldSess:
		how = "同じ会話に戻った(session " + oldSess + " のまま)"
	default:
		how = "session CHANGED (" + oldSess + " -> " + ns + "; verify it is the same conversation)"
	}
	out := fmt.Sprintf("%s restarted by %s: stop ok(pane %s を閉じた) / revive ok(%s) / %s / %d 秒", name, by, pane, strings.TrimPrefix(rev.out, name+" revived "), how, int(time.Since(t0).Seconds()))
	if rev.note != "" {
		return opResult{code: 10, out: out + " / " + rev.note}
	}
	return opResult{code: 0, out: out}
}

func cmdAgentRestart(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-restart <名前> [--prompt P]")
	}
	name, prompt := args[0], ""
	if err := validAgentName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		if args[i] != "--prompt" {
			return usage("agent-restart: 不明な引数 %s(--force はありません)", args[i])
		}
		if i+1 >= len(args) {
			return usage("%s に値が必要です", args[i])
		}
		prompt = args[i+1]
		i++
	}
	if utf8.RuneCountInString(prompt) > 4000 {
		return usage("agent-restart: --prompt は 4000 字まで")
	}
	kv := []any{"restart", 1}
	if prompt != "" {
		kv = append(kv, "prompt", prompt)
	}
	opArgs := jsonC(kv...)
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	if by != "human" {
		why := ""
		if by == name {
			why = "自分自身は再起動できません"
		} else {
			for _, n := range message.ConductorNames(db.DB, db.Session) {
				if n == name {
					why = "指揮官(" + name + ")は agent から再起動できません(人が実行します)"
				}
			}
		}
		if why == "" {
			j := approval.NewJudge(db.DB, db.Session)
			s, _ := j.Of("agent:stop:"+name, by)
			v, _ := j.Of("agent:revive:"+name, by)
			if s != "allow" || v != "allow" {
				why = "policy で agent:stop と agent:revive(" + name + ")の両方が allow ではありません(stop=" + s + " revive=" + v + ")。人が実行するか、人が許可してください"
			}
		}
		if why != "" {
			ts := nowTS()
			_, _ = db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at) VALUES (?, 'revive', ?, ?, ?, 'rejected', ?, ?, ?)`,
				db.Session, name, opArgs, by, "restart refused: "+why, ts, ts)
			return &exit{4, why}
		}
	}
	if err := limitsRefuseRunning(db, 0, stderr); err != nil { // 動作中が既に上限を超えているときは触らない(再起動で数は増えない)
		return err
	}
	r := doAgentRestart(db, st, name, prompt, by, stderr)
	recordOp(db, "revive", name, opArgs, by, r.code, "restart: "+r.out)
	if r.code != 0 {
		return &exit{r.code, r.out}
	}
	fmt.Fprintln(w, r.out)
	return nil
}

// ---------- 作る(open / agent-new) ----------

var reRoleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// roleConflict は role_conflict: 作成後の役割が、要求された(承認した)役割とずれるなら理由。
func roleConflict(r *agents.Roles, name, role string) (string, bool) {
	if role == "" {
		return "", false
	}
	if role != name && r.Exists(name) {
		return "役割 " + name + " の定義があるため、" + name + " の役割は " + role + " になりません", true
	}
	for _, x := range strings.Split(role, ",") {
		x = config.Trim(x)
		if !r.Exists(x) {
			return "役割 " + x + " の定義が無いため、" + name + " の役割は " + role + " になりません", true
		}
	}
	return "", false
}

// doAgentNew は do_agent_new(sby / stask は tb agent-spawn の子のときだけ)。
func doAgentNew(db *store.DB, name, cwd, model, role, prompt, kill, by, sby, stask, kind string, stderr io.Writer) opResult {
	if r := kindCheckLaunch(kind, model, ""); r != nil {
		return *r
	}
	// 承認待ちの間に roles/<名前>.md が作られていたら、承認した役割と作成後の役割がずれるので作らない
	if why, bad := roleConflict(agents.NewRoles(db.DB, db.Session), name, role); bad {
		return opResult{code: 1, out: why}
	}
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return opResult{code: 11, out: "herdr agent list に失敗しました"}
	}
	if _, ok := liveOf(live, name); ok {
		return opResult{code: 1, out: name + " はすでに動いています"}
	}
	var state string
	_ = db.QueryRow(`SELECT state FROM agents WHERE name=?`, name).Scan(&state)
	if state == "stopped" {
		return opResult{code: 1, out: "停止中の " + name + " が台帳にあります。tb agent-revive " + name + " で再開するか、別の名前にしてください"}
	}
	if !isDir(cwd) {
		return opResult{code: 1, out: "ディレクトリがありません: " + cwd}
	}
	cwd = physDir(cwd)
	// 先に台帳へ載せる(SessionStart hook が --role の役割を引けるように)
	why := ""
	if kill != "unknown" {
		why = "作成時に指定"
	}
	if err := agentRegister(db, name, cwd, model, role, by, kill, why, kind); err != nil {
		return opResult{code: 1, out: err.Error()}
	}
	sp, ok := transport.Spawn(env.TBDB(), kind, name, cwd, kindLaunchArgs(kind, "", modelWithRoleDefault(db, model, kind, name, role)))
	setPane := func() {
		_, _ = db.Exec(`UPDATE main.agents SET pane_id=?, spawned_by=NULLIF(?,''), spawn_task=NULLIF(?,'') WHERE herdr_session=? AND name=?`, sp.Pane, sby, stask, db.Session, name)
	}
	if !ok {
		if sp.Kept { // 人の操作待ち: pane は残っているので台帳にも残す(最初の指示は送らない)
			setPane()
			out := name + ": " + sp.Err
			if prompt != "" {
				out += "。最初の指示は未送信(答えたあと tb send などで送る)"
			}
			return opResult{code: 11, out: out}
		}
		_, _ = db.Exec(`DELETE FROM main.agents WHERE herdr_session=? AND name=?`, db.Session, name)
		return opResult{code: 11, out: name + " を起動できません: " + sp.Err}
	}
	setPane()
	out := name + " started (pane " + sp.Pane + ", cwd " + cwd
	if model != "" {
		out += ", model " + model
	}
	if role != "" {
		out += ", role " + role
	}
	out += ")"
	if prompt != "" {
		if note := firstPrompt(db, name, prompt, by, stderr); note != "" {
			out += " / " + note
		}
	}
	return opResult{code: 0, out: out}
}

func cmdAgentNew(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-new <名前> [--cwd D] [--kind K] [--model M] [--role R] [--prompt P] [--killable yes|no]")
	}
	name := args[0]
	cwd, model, role, prompt, kill, kind := "", "", "", "", "unknown", kindDefault
	if err := validAgentName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		var dst *string
		switch a {
		case "--cwd":
			dst = &cwd
		case "--kind":
			dst = &kind
		case "--model":
			dst = &model
		case "--role":
			dst = &role
		case "--prompt":
			dst = &prompt
		case "--killable":
			dst = &kill
		default:
			return usage("agent-new: 不明な引数 %s", a)
		}
		if i+1 >= len(args) {
			return usage("%s に値が必要です", a)
		}
		*dst = args[i+1]
		i++
	}
	if !kindValid(kind) {
		return usage("agent-new: --kind は %s のどれか: %s", strings.Join(kindAll, " "), kind)
	}
	if model != "" && !modelRE.MatchString(model) {
		return usage("agent-new: --model に使えない文字があります: %s", model)
	}
	if r := kindCheckLaunch(kind, model, ""); r != nil { // 承認待ちに積む前に、その kind でできない依頼は止める
		return &exit{12, r.out}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if role != "" {
		rs := strings.Split(role, ",")
		if len(rs) > 1 && rs[len(rs)-1] == "" { // IFS=, read -ra は末尾の空の欄を数えない
			rs = rs[:len(rs)-1]
		}
		if len(rs) < 1 || len(rs) > 4 {
			return usage("agent-new: --role はカンマ区切りで 1〜4 個: %s", role)
		}
		roles := agents.NewRoles(db.DB, db.Session)
		for _, x := range rs {
			if !reRoleName.MatchString(x) || x == "README" || !roles.Exists(x) {
				return usage("agent-new: --role は定義のある役割名(tb role-list。カンマ区切りで複数可): %s", x)
			}
		}
		// roles/<名前>.md は台帳の役割より優先される。承認(要求された役割)と作成後の役割がずれないよう拒否する
		if _, bad := roleConflict(roles, name, role); bad {
			return usage("agent-new: 役割 %s の定義があるため、この名前の役割は %s に固定です(--role %s は効きません)", name, name, role)
		}
	}
	if kill != "yes" && kill != "no" && kill != "unknown" {
		return usage("agent-new: --killable は yes|no")
	}
	if kill != "unknown" { // 判定フラグを付けられるのは agent-flag と同じく人か、policy で agent:flag を許された agent だけ
		by, err := requester()
		if err != nil {
			return err
		}
		if by != "human" {
			// 作る前の agent は台帳に無い。policy の @<役割> の対象は、要求された役割(--role)で比べる
			j := approval.NewJudge(db.DB, db.Session)
			if role != "" {
				j.Roles.Hint(name, role)
			}
			if _, ok := j.GrantOf(by, "agent:flag", name); !ok {
				return die("agent-new --killable を付けられるのは人と、policy で agent:flag を許された agent だけです(%s)", by)
			}
		}
	}
	if utf8.RuneCountInString(prompt) > 4000 {
		return usage("agent-new: --prompt は 4000 字まで")
	}
	if cwd == "" {
		cwd = agentCwd()
	}
	if strings.HasPrefix(cwd, "~") {
		cwd = os.Getenv("HOME") + cwd[1:]
	}
	// 承認する側のカレントで解決されないよう、依頼時に絶対パスにする(存在の検査は実行時に doAgentNew が行う)
	if !strings.HasPrefix(cwd, "/") {
		cwd = envOr("PWD", ".") + "/" + cwd
	}
	if isDir(cwd) {
		cwd = physDir(cwd)
	}
	if err := limitsRefuseRunning(db, 1, stderr); err != nil { // 動作中が上限なら、承認待ちにも積まず拒否(max_running_agents)
		return err
	}
	by, err := requester()
	if err != nil {
		return err
	}
	kv := []any{"cwd", cwd}
	if kind != "claude" {
		kv = append(kv, "kind", kind)
	}
	for _, p := range [][2]string{{"model", model}, {"role", role}, {"prompt", prompt}} {
		if p[1] != "" {
			kv = append(kv, p[0], p[1])
		}
	}
	kv = append(kv, "killable", kill)
	return opEntry(db, "new", name, jsonC(kv...), by, w, func() opResult { return doAgentNew(db, name, cwd, model, role, prompt, kill, by, "", "", kind, stderr) })
}
