package tbcli

// チケットの状態を動かすコマンド(taskboard/lib/tasks.sh の claim / done / block / cancel)。

import (
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
)

// specialTicketGuard は special_ticket_guard: アラート・提案のチケットの状態は、それぞれのコマンドだけが変える。
// rowQuerier は *sql.DB と *sql.Tx(トランザクションの中でも確かめられるように)。
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func specialTicketGuard(db rowQuerier, id, op string) error {
	var w string
	err := db.QueryRow(`SELECT 'A-'||alert_no FROM alert_details WHERE task_id=?1 UNION ALL SELECT 'P-'||proposal_no FROM proposal_details WHERE task_id=?1 LIMIT 1`, id).Scan(&w)
	if err != nil {
		return nil // 行が無い・表が無い(古い DB)
	}
	switch {
	case strings.HasPrefix(w, "A-"):
		return die("%s はアラート %s のチケットです。状態は tb alert-approve / alert-result / alert-close %s で変えます(閉じるのは人だけ。%s では変えません)", id, w, w, op)
	case strings.HasPrefix(w, "P-"):
		return die("%s は提案 %s のチケットです。状態は tb proposal resolve %s で変えます(%s では変えません)", id, w, w, op)
	}
	return nil
}

// taskStatus は require_task のあとの .status。
func taskStatus(db *store.DB, id string) (string, error) {
	var st string
	err := db.QueryRow(`SELECT status FROM tasks WHERE id=?`, id).Scan(&st)
	if err == sql.ErrNoRows {
		return "", die("タスクが見つかりません: %s", id)
	}
	return st, err
}

// taskDoneHook は notify_hook notify_hook_task_done: 完了の通知を積む(失敗しても止めない)。
func taskDoneHook(db *store.DB, id string) {
	var summary sql.NullString
	var dels string
	if db.QueryRow(`SELECT summary, deliverables FROM tasks WHERE id=?`, id).Scan(&summary, &dels) != nil {
		return
	}
	vs, err := jv.DecodeAll([]byte(dels))
	if err != nil || len(vs) != 1 {
		return
	}
	arr, ok := vs[0].([]any)
	if !ok {
		return
	}
	parts := make([]string, len(arr))
	for i, v := range arr {
		s, ok := v.(string)
		if !ok {
			return // jq の join は文字列以外で失敗する
		}
		parts[i] = s
	}
	notify.New(db.DB, db.Session).Emit(notify.Event{Type: "done", Ref: id, A: id, B: summary.String, C: strings.Join(parts, ", ")})
}

// cmdClaim は tb claim <id> --owner <name> [--session <sid>] [--account <acct>]。
func cmdClaim(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("claim <id> --owner <name> [--session <sid>] [--account <acct>]")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	var owner, session, account string
	for i := 1; i < len(args); i++ {
		var p *string
		switch args[i] {
		case "--owner":
			p = &owner
		case "--session":
			p = &session
		case "--account":
			p = &account
		default:
			return usage("claim: 不明な引数 %s", args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		*p = v
		i++
	}
	if blank(owner) {
		return usage("claim: --owner が必要です")
	}
	if err := requireTask(db, id); err != nil {
		return err
	}
	if err := specialTicketGuard(db.DB, id, "tb claim"); err != nil {
		return err
	}
	closed := st.SQLList("done", "cancelled")
	var blockers sql.NullString
	if err := db.QueryRow(`SELECT group_concat(t.id||'('||t.status||')', ', ') FROM task_links l JOIN tasks t ON t.id=l.from_id
		WHERE l.to_id=? AND l.link_type='blocks' AND t.status NOT IN (`+closed+`)`, id).Scan(&blockers); err != nil {
		return err
	}
	if blockers.String != "" {
		return die("claim できません(先に終わらせるタスクがあります: %s): %s", blockers.String, id)
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	todo := st.SQLList("todo")
	r, err := db.Exec(`UPDATE main.tasks SET status=?1, owner_agent=?2,
		session_id=COALESCE(NULLIF(?3,''), session_id), account=COALESCE(NULLIF(?4,''), account), updated_at=?5
		WHERE herdr_session=?6 AND id=?7 AND status IN (`+todo+`)
		AND (owner_agent IS NULL OR owner_agent='' OR owner_agent=?2)`, st.Main("active"), owner, session, account, now(), db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		// 0 件の理由を分ける: todo の分類の状態のまま別の担当に割り当て済みか、状態が todo の分類ではないか
		var cur string
		_ = db.QueryRow(`SELECT COALESCE(owner_agent,'') FROM tasks WHERE herdr_session=? AND id=? AND status IN (`+todo+`)`, db.Session, id).Scan(&cur)
		if cur != "" {
			return die("claim できません(%s に割り当て済み。付け替えは tb update %s --owner <名前>。次の候補は tb list で探す): %s", cur, id, id)
		}
		return die("claim できません(status が %s ではない): %s", strings.Join(st.In("todo"), " / "), id)
	}
	rulesHook(db, st)
	fmt.Fprintf(w, "%s claimed by %s\n", id, owner)
	return nil
}

// cmdDone は tb done <id> -d <成果物> [-d ...] -m <要約>。
func cmdDone(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("done <id> -d <成果物パス> [-d ...] -m <要約>")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	var summary string
	var dels []any
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-d", "--deliverable":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			if !blank(v) {
				dels = append(dels, v)
			}
		case "-m", "--summary":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			summary = v
		default:
			return usage("done: 不明な引数 %s", args[i])
		}
		i++
	}
	if len(dels) == 0 {
		return usage("done: 成果物(-d)が必須です")
	}
	if blank(summary) {
		return usage("done: 要約(-m)が必須です")
	}
	cur, err := taskStatus(db, id)
	if err != nil {
		return err
	}
	if st.Is(cur, "done") {
		return die("すでに %s です: %s", cur, id)
	}
	if err := specialTicketGuard(db.DB, id, "tb done"); err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	r, err := db.Exec(`UPDATE main.tasks SET status=?, deliverables=?, summary=?, reported_at=NULL, updated_at=?
		WHERE herdr_session=? AND id=? AND status NOT IN (`+st.SQLList("done")+`)`, st.Main("done"), jv.Compact(dels), summary, now(), db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return die("done できません(並行して done になりました): %s", id)
	}
	taskDoneHook(db, id)
	rulesHook(db, st)
	fmt.Fprintf(w, "%s done (報告は tb digest でまとめて届く。すぐ届けるなら tb report %s)\n", id, id)
	return nil
}

// reasonArgs は block / cancel の [-m <理由>]。
func reasonArgs(name string, args []string) (string, error) {
	reason := ""
	for i := 1; i < len(args); i++ {
		if args[i] != "-m" && args[i] != "--message" {
			return "", usage("%s: 不明な引数 %s", name, args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return "", err
		}
		reason = v
		i++
	}
	return reason, nil
}

// cmdBlock は tb block <id> [-m <理由>]。
func cmdBlock(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("block <id> [-m <理由>]")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	reason, err := reasonArgs("block", args)
	if err != nil {
		return err
	}
	if _, err := taskStatus(db, id); err != nil {
		return err
	}
	if err := specialTicketGuard(db.DB, id, "tb block"); err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	r, err := db.Exec(`UPDATE main.tasks SET status=?, note=NULLIF(?,''), updated_at=? WHERE herdr_session=? AND id=?`,
		st.Main("blocked"), reason, now(), db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return die("block できません: %s", id)
	}
	rulesHook(db, st)
	fmt.Fprintf(w, "%s %s\n", id, st.Main("blocked"))
	return nil
}

// cmdCancel は tb cancel <id> [-m <理由>]: やらないと決めたタスクを閉じる(完了報告には出さない)。
func cmdCancel(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("cancel <id> [-m <理由>]")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	reason, err := reasonArgs("cancel", args)
	if err != nil {
		return err
	}
	cur, err := taskStatus(db, id)
	if err != nil {
		return err
	}
	if st.Is(cur, "cancelled") {
		return die("すでに %s です: %s", cur, id)
	}
	if err := specialTicketGuard(db.DB, id, "tb cancel"); err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	t := now()
	r, err := db.Exec(`UPDATE main.tasks SET status=?, summary=NULLIF(?,''), reported_at=?, updated_at=?
		WHERE herdr_session=? AND id=? AND status NOT IN (`+st.SQLList("cancelled")+`)`, st.Main("cancelled"), reason, t, t, db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return die("cancel できません(並行して取り消されました): %s", id)
	}
	rulesHook(db, st)
	fmt.Fprintf(w, "%s %s\n", id, st.Main("cancelled"))
	return nil
}
