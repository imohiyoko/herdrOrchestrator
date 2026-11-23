package tbcli

import (
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// agent の止める・戻す(taskboard/lib/agents.sh の authorize_op / op_entry / do_agent_stop / cmd_agent-stop)。
// 作る・戻す・再起動・承認(agent-new / agent-revive / agent-restart / agent-approve)はまだ bash。
// 承認待ち(L-<n>)の行は bash と同じ形で積むので、承認は bash の tb agent-approve がそのまま実行できる。

// opResult は do_agent_* の結果(OP_OUT と終了コード)。
type opResult struct {
	code int
	out  string
}

// authorizeOp は authorize_op: 人なら true。agent なら approvals の agent:<op>:<名前> が allow のとき true、
// それ以外は L-<n> を積んで「承認待ち」を出し、終了コード 20 を返す。
func authorizeOp(db *store.DB, op, target, args, by string, w io.Writer) (bool, error) {
	if by == "human" {
		return true, nil
	}
	v, rule := approval.NewJudge(db.DB, db.Session).Of("agent:"+op+":"+target, by)
	if v == "allow" {
		return true, nil
	}
	res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?,?,?,?,?, 'pending', ?, ?)`,
		db.Session, op, target, args, by, "approvals: "+rule, nowTS())
	if err != nil {
		return false, err
	}
	id, _ := res.LastInsertId()
	fmt.Fprintf(w, "L-%d pending (人の承認待ち: tb agent-approve L-%d)\n", id, id)
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
func killableOf(db *store.DB, name string) string {
	if _, ok := agents.NewRoles(db.DB, db.Session).ProtectReason(name); ok {
		return "protected"
	}
	var k string
	_ = db.QueryRow(`SELECT CASE WHEN killable!='yes' THEN killable
      WHEN killable_session IS NOT NULL AND session_id IS NOT NULL AND killable_session!=session_id THEN 'stale'
      WHEN EXISTS (SELECT 1 FROM main.agent_activity aa WHERE (aa.session_id=agents.session_id OR (aa.pane_id=agents.pane_id AND aa.herdr_session=agents.herdr_session))
                   AND julianday(aa.last_prompt_at) > julianday(agents.killable_at)) THEN 'stale'
      ELSE 'yes' END FROM agents WHERE name=?`, name).Scan(&k)
	return k
}

// liveOf は live_row: herdr の一覧から名前が一致する 1 件。
func liveOf(live []herdr.Agent, name string) (herdr.Agent, bool) {
	for _, a := range live {
		if a.Name == name {
			return a, true
		}
	}
	return herdr.Agent{}, false
}

// closePane は agent_close_pane: pane を閉じて台帳を stopped にする(session は残す)。閉じられなければ 11。
func closePane(db *store.DB, name, pane, sess string) *opResult {
	if _, err := herdr.Call("pane", "close", pane); err != nil {
		return &opResult{11, "pane " + pane + " を閉じられません"}
	}
	ts := nowTS()
	_, _ = db.Exec(`UPDATE main.agents SET state='stopped', stopped_at=?, updated_at=?, pane_id=NULL,
         session_id=COALESCE(NULLIF(?,''), session_id) WHERE herdr_session=? AND name=?`, ts, ts, sess, db.Session, name)
	return nil
}

// doAgentStop は do_agent_stop。
func doAgentStop(db *store.DB, st tickets.StatusSet, name string, force bool, by string) opResult {
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return opResult{11, "herdr agent list に失敗しました"}
	}
	a, ok := liveOf(live, name)
	if !ok {
		if msg, other := otherSession(db, name); other {
			return opResult{1, msg}
		}
		return opResult{1, name + " は動いていません"}
	}
	if by == name {
		return opResult{1, "自分自身は止められません"}
	}
	pane, sess := a.PaneID, a.AgentSession.Value
	if pane == "" {
		return opResult{1, name + " の pane が分かりません"}
	}
	if k, ok := agents.NewRoles(db.DB, db.Session).ProtectReason(name); ok { // 保護は --force でも越えない
		return opResult{1, name + " は保護されています(" + k + ")。止めるなら、人が tb config edit protect で外してから"}
	}
	if !force {
		k := killableOf(db, name)
		var open string
		_ = db.QueryRow(`SELECT COALESCE(group_concat(id, ' '),'') FROM tasks WHERE owner_agent=? AND status IN (`+st.SQLList("active", "review", "blocked")+`)`, name).Scan(&open)
		if k != "yes" || open != "" {
			extra := ""
			if open != "" {
				extra = "、未完了タスク " + open
			}
			return opResult{1, name + " は止めてよいと判定されていません(killable=" + k + extra + ")。判定役に付けてもらうか(tb agent-flag)、人が --force で止めます"}
		}
	}
	if r := closePane(db, name, pane, sess); r != nil {
		return *r
	}
	_ = db.QueryRow(`SELECT COALESCE(session_id,'') FROM agents WHERE name=?`, name).Scan(&sess)
	out := name + " stopped"
	if sess != "" {
		out += " (session " + sess + "。tb agent-revive " + name + " で同じ会話から再開できます)"
	}
	return opResult{0, out}
}

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
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	if force && by != "human" {
		return die("--force は人が自分の端末で使います(agent '%s' の pane からは使えません)", by)
	}
	f := 0
	if force {
		f = 1
	}
	return opEntry(db, "stop", name, fmt.Sprintf(`{"force":%d}`, f), by, w, func() opResult { return doAgentStop(db, st, name, force, by) })
}
