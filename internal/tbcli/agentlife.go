package tbcli

import (
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"

	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
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
	by, err := requester()
	if err != nil {
		return err
	}
	var res agent.LifeResult
	if err := callAppW(db, app.PrincipalOf(by), stderr, "agent.revive", agent.ReviveReq{Name: name, Fresh: fresh, Model: model, Prompt: prompt}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.Out)
	return nil
}

// ---------- 再起動(restart) ----------

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
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	var res agent.LifeResult
	if err := callAppW(db, app.PrincipalOf(by), stderr, "agent.restart", agent.RestartReq{Name: name, Prompt: prompt}, &res); err != nil {
		return err
	}
	fmt.Fprintln(w, res.Out)
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
