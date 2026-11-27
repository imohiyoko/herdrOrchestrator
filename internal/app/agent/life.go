package agent

// agent の起動の部品と、戻す(revive)・再起動(restart)の中身(tb の agents.sh の do_agent_revive / do_agent_restart)。
// 承認後の実行・recipe・tb agent-new などからも呼ぶ。警告は stderr に書く(tb は標準エラー、API の操作は結果の Warnings)。

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// KindAll は KIND_ALL。
var KindAll = []string{"claude", "codex", "gemini"}

func nowTS() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func blank(s string) bool { return strings.TrimFunc(s, config.IsSpace) == "" }

// ---------- 戻す(revive) ----------

var ModelRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

const KindDefault = "claude"

func KindValid(k string) bool { return slices.Contains(KindAll, k) }

// KindRequire は kind_require: 対応していなければ理由(KIND_ERR)。
func KindRequire(kind, item, note string) (string, bool) {
	if !KindValid(kind) {
		return "kind=" + kind + " は未知の kind です(使えるのは: " + strings.Join(KindAll, " ") + ") item=kind", false
	}
	if KindSupports(kind, item) {
		return "", true
	}
	if note != "" {
		note = " (" + note + ")"
	}
	return "kind=" + kind + " は " + item + " に対応していません item=" + item + note + "。claude の引数を代わりに渡すことはしません(docs/agent-kinds.md)", false
}

// CheckLaunch は kind_check_launch: 要求された起動(モデル指定・会話の再開)に対応していなければ 12。
func CheckLaunch(kind, model, sess string) *Result {
	if m, ok := KindRequire(kind, "launch", ""); !ok {
		return &Result{Code: 12, Out: m}
	}
	if model != "" {
		if m, ok := KindRequire(kind, "model", "--model "+model); !ok {
			return &Result{Code: 12, Out: m}
		}
		if kind != "claude" || !ModelRE.MatchString(model) {
			return &Result{Code: 12, Out: "kind=" + kind + " のモデル名として使えません item=model: " + model}
		}
	}
	if sess != "" {
		if m, ok := KindRequire(kind, "resume", "session "+sess+" で再開できない。新しい会話にするなら --fresh"); !ok {
			return &Result{Code: 12, Out: m}
		}
	}
	return nil
}

// LaunchArgs は kind_launch_args: herdr agent start の -- 以降。
func LaunchArgs(kind, sess, model string) []string {
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

// RoleModelFor は role_model_for: 設定 role-models で、名前(name:<glob>)か役割の glob に当たるモデル。
// reg が空でなければ台帳の role の代わりに使う(role_name_of の第 2 引数)。
func RoleModelFor(db *store.DB, name, reg string) string {
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
		if !ModelRE.MatchString(v) {
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

// ModelWithRoleDefault は model_with_role_default: 明示が無ければ役割の既定モデル(kind がモデル指定に対応するときだけ)。
func ModelWithRoleDefault(db *store.DB, model, kind, name, role string) string {
	if model != "" {
		return model
	}
	if !KindSupports(kind, "model") {
		return ""
	}
	return RoleModelFor(db, name, role)
}

// Register は agent_register: 台帳に登録する(新しい命のたびに判定フラグは付け直し)。
func Register(db *store.DB, name, cwd, model, role, by, kill, why, kind string) error {
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

// FirstPrompt は first_prompt: 起動直後の agent に最初の指示を送る。送れない・確かめられないときは理由(FP_NOTE)。
func FirstPrompt(db *store.DB, name, prompt, by string, stderr io.Writer) string {
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
		time.Sleep(time.Duration(SecEnv("TB_SPAWN_RETRY_SEC", 3) * float64(time.Second)))
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

func SecEnv(k string, def float64) float64 {
	if f, err := strconv.ParseFloat(envOr(k, strconv.FormatFloat(def, 'f', -1, 64)), 64); err == nil {
		return f
	}
	return 0
}

// RefuseRunning は limits_refuse_running <add>: 動作中の agent が add 増えると上限を超えるなら理由を stderr に出して 4。
func RefuseRunning(db *store.DB, add int, stderr io.Writer) bool {
	running, _, ok := Counts(db)
	if !ok {
		return false
	}
	max, lim := limits.Load(db.DB, db.Session).Max("max_running_agents")
	if limits.Exceeds(running+add, max, lim) {
		fmt.Fprintf(stderr, "tb: 動作中の agent が上限です(動作中 %d、上限 max_running_agents=%d)。何も作りません。 止める候補は tb agent-shed、上限を変えるのは人(tb limits set max_running_agents <数>)\n", running, max)
		return true
	}
	return false
}

// DefaultCwd は AGENT_CWD(TB_AGENT_CWD、既定は TB_ROOT/..)。
func DefaultCwd() string {
	if d := os.Getenv("TB_AGENT_CWD"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("TB_ROOT"), "..")
}

func IsDir(d string) bool {
	st, err := os.Stat(d)
	return err == nil && st.IsDir()
}

// RegRow は台帳の行のうち起動に使う項目(Ok は行があるか)。
type RegRow struct {
	Ok                     bool
	Cwd, Model, Sess, Kind string
}

func RegRowOf(db *store.DB, name string) RegRow {
	var r RegRow
	var cwd, model, sess, kind sql.NullString
	if db.QueryRow(`SELECT cwd, model, session_id, kind FROM agents WHERE name=?`, name).Scan(&cwd, &model, &sess, &kind) == nil {
		r = RegRow{true, cwd.String, model.String, sess.String, kind.String}
		if r.Kind == "" {
			r.Kind = KindDefault
		}
	}
	return r
}

// Revive は do_agent_revive。
func Revive(db *store.DB, name string, fresh bool, model, prompt, by string, stderr io.Writer) Result {
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return Result{Code: 11, Out: "herdr agent list に失敗しました"}
	}
	if _, ok := agents.LiveOf(live, name); ok {
		return Result{Code: 1, Out: name + " はすでに動いています"}
	}
	row := RegRowOf(db, name)
	if !row.Ok {
		if msg, other := agents.OtherSession(db.DB, db.Session, name); other {
			return Result{Code: 1, Out: msg}
		}
		return Result{Code: 1, Out: name + " は台帳にありません(新しく作るなら tb agent-new)"}
	}
	cwd := row.Cwd
	if model == "" {
		model = row.Model
	}
	lmodel := model // 台帳に書くのは明示か台帳の値だけ(役割の既定は書かない)
	sess := ""
	if !fresh {
		sess = row.Sess
	}
	model = ModelWithRoleDefault(db, model, row.Kind, name, "")
	if r := CheckLaunch(row.Kind, model, sess); r != nil {
		return *r
	}
	if cwd == "" || !IsDir(cwd) {
		if sess != "" {
			c := cwd
			if c == "" {
				c = "未記録"
			}
			return Result{Code: 1, Out: name + " の作業ディレクトリがありません(" + c + ")。--fresh なら " + DefaultCwd() + " で新しい会話として起動します"}
		}
		cwd = PhysDir(DefaultCwd())
	}
	if err := Register(db, name, cwd, lmodel, "", by, "unknown", "", row.Kind); err != nil {
		return Result{Code: 1, Out: err.Error()}
	}
	sp, ok := transport.Spawn(db.Path, row.Kind, name, cwd, LaunchArgs(row.Kind, sess, model))
	if !ok {
		if sp.Kept {
			_, _ = db.Exec(`UPDATE main.agents SET pane_id=? WHERE herdr_session=? AND name=?`, sp.Pane, db.Session, name)
			out := name + ": " + sp.Err
			if prompt != "" {
				out += "。最初の指示は未送信(答えたあと tb send などで送る)"
			}
			return Result{Code: 11, Out: out}
		}
		_, _ = db.Exec(`UPDATE main.agents SET state='stopped', stopped_at=? WHERE herdr_session=? AND name=?`, nowTS(), db.Session, name)
		return Result{Code: 11, Out: name + " を起動できません: " + sp.Err}
	}
	_, _ = db.Exec(`UPDATE main.agents SET pane_id=? WHERE herdr_session=? AND name=?`, sp.Pane, db.Session, name)
	how := "新しい会話"
	if sess != "" {
		how = "resume " + sess
	}
	out := name + " revived (" + how + ", pane " + sp.Pane + ", cwd " + cwd + ")"
	note := ""
	if prompt != "" {
		if note = FirstPrompt(db, name, prompt, by, stderr); note != "" {
			out += " / " + note
		}
	}
	return Result{Code: 0, Out: out, Note: note}
}

// PhysDir は $(cd D && pwd): 絶対パス(シンボリックリンクは解かない。bash の pwd と同じ論理パス)。
func PhysDir(d string) string {
	if filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	wd := os.Getenv("PWD")
	if wd == "" {
		wd, _ = os.Getwd()
	}
	return filepath.Join(wd, d)
}

// ---------- 再起動(restart) ----------

const RestartDefaultPrompt = "MCP の接続を張り直すために再起動しました。直前の作業を続けてください。まず MCP のツールが使えることを確認し、まだ使えないなら tb comment で報告してください。"

// Restart は do_agent_restart: 停止 + 同じ会話での復活。安全条件を全部満たすときだけ。
// 0 / 1 / 4 / 10(再開したが最初の指示を確認できない) / 11 / 12
func Restart(db *store.DB, st tickets.StatusSet, name, prompt, by string, stderr io.Writer) Result {
	t0 := time.Now()
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return Result{Code: 11, Out: "herdr agent list に失敗しました"}
	}
	a, ok := agents.LiveOf(live, name)
	if !ok {
		if msg, other := agents.OtherSession(db.DB, db.Session, name); other {
			return Result{Code: 1, Out: msg}
		}
		return Result{Code: 1, Out: name + " は動いていません(再起動は動いている agent だけ。止まっているなら tb agent-revive " + name + ")"}
	}
	if by == name {
		return Result{Code: 1, Out: "自分自身は再起動できません"}
	}
	pane, oldSess := a.PaneID, a.AgentSession.Value
	status, _ := a.AgentStatus.(string)
	if pane == "" {
		return Result{Code: 1, Out: name + " の pane が分かりません"}
	}
	if status != "idle" && status != "done" {
		s := status
		if s == "" {
			s = "状態不明"
		}
		return Result{Code: 1, Out: name + " は " + s + " のため再起動しません(idle か done のときだけ)"}
	}
	var open string
	_ = db.QueryRow(`SELECT COALESCE(group_concat(id, ' '),'') FROM tasks WHERE owner_agent=? AND status IN (`+st.SQLList("active")+`)`, name).Scan(&open)
	if open != "" && blank(prompt) {
		return Result{Code: 1, Out: name + " は " + st.Main("active") + " のタスク(" + open + ")を持っています。再開メモを --prompt で渡したときだけ再起動できます"}
	}
	row := RegRowOf(db, name)
	if oldSess == "" {
		oldSess = row.Sess
	}
	kind := row.Kind // 台帳に無ければ空(bash と同じく未知の kind として止める)
	// 閉じてから再開できないと困るので、再開できることを先に確かめる(だめなら何も止めない)
	if m, ok := KindRequire(kind, "resume", "同じ会話に戻れないので止めません"); !ok {
		return Result{Code: 12, Out: m}
	}
	if oldSess == "" {
		return Result{Code: 1, Out: name + " の session id が台帳にありません。同じ会話に戻れないので止めません"}
	}
	if row.Cwd == "" || !IsDir(row.Cwd) {
		c := row.Cwd
		if c == "" {
			c = "未記録"
		}
		return Result{Code: 1, Out: name + " の作業ディレクトリがありません(" + c + ")。同じ会話に戻れないので止めません"}
	}
	if blank(prompt) {
		prompt = RestartDefaultPrompt
	}
	if !agents.ClosePane(db.DB, db.Session, name, pane, oldSess) {
		return Result{Code: 11, Out: "pane " + pane + " を閉じられません"}
	}
	_, _ = agents.SyncLive(db.DB, db.Session)
	if RefuseRunning(db, 1, io.Discard) {
		return Result{Code: 4, Out: name + " を停止しましたが、動作中の agent が上限のため復活していません。停止のままです。上限が空いたら: tb agent-revive " + name + " [--fresh]"}
	}
	rev := Revive(db, name, false, "", prompt, by, stderr)
	if rev.Code != 0 {
		return Result{Code: rev.Code, Out: name + " を停止しました(pane " + pane + " を閉じた)が、復活に失敗: " + rev.Out + "。停止のままです。自分で戻すなら: tb agent-revive " + name + " [--fresh](--fresh は新しい会話になります)"}
	}
	live, _ = agents.SyncLive(db.DB, db.Session)
	cur, _ := agents.LiveOf(live, name)
	how := ""
	switch ns := cur.AgentSession.Value; {
	case ns == "":
		how = "session unconfirmed (herdr reports none yet; was " + oldSess + ")"
	case ns == oldSess:
		how = "同じ会話に戻った(session " + oldSess + " のまま)"
	default:
		how = "session CHANGED (" + oldSess + " -> " + ns + "; verify it is the same conversation)"
	}
	out := fmt.Sprintf("%s restarted by %s: stop ok(pane %s を閉じた) / revive ok(%s) / %s / %d 秒", name, by, pane, strings.TrimPrefix(rev.Out, name+" revived "), how, int(time.Since(t0).Seconds()))
	if rev.Note != "" {
		return Result{Code: 10, Out: out + " / " + rev.Note}
	}
	return Result{Code: 0, Out: out}
}

// 同期に失敗したら ok=false(台帳の値で数える)。台帳を数えられなくても ok=false。
func Counts(db *store.DB) (running, working int, ok bool) {
	live, err := agents.SyncLive(db.DB, db.Session)
	if e := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE state='running'`).Scan(&running); e != nil {
		return 0, 0, false
	}
	for _, a := range live {
		if s, _ := a.AgentStatus.(string); s == "working" && agents.NameRE.MatchString(a.Name) {
			working++
		}
	}
	return running, working, err == nil
}

// KindSupports は kind_supports <kind> <項目>。
func KindSupports(kind, item string) bool {
	switch {
	case kind == "claude":
		return true
	case (kind == "codex" || kind == "gemini") && item == "launch":
		return true
	}
	return false
}
