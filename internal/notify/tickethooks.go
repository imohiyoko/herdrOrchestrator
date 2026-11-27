package notify

// チケットの書き込みのあとに流すもの(tb の notify_hook_task_done と notify_rules_hook)。
// tb の入口とアプリケーション API(internal/app/ticket)の両方が使う。

import (
	"database/sql"
	"strconv"

	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// TaskDone は完了の通知を積む(失敗しても止めない)。成果物が文字列の配列でなければ積まない(jq の join と同じ)。
func TaskDone(db *sql.DB, session, id string) {
	var summary sql.NullString
	var dels string
	if db.QueryRow(`SELECT summary, deliverables FROM tasks WHERE id=?`, id).Scan(&summary, &dels) != nil {
		return
	}
	d, ok := tickets.JoinDeliverables(dels)
	if !ok {
		return
	}
	New(db, session).Emit(Event{Type: "done", Ref: id, A: id, B: summary.String, C: d})
}

// Comment は notify_hook_comment: 報告コメント cid の外への通知を積む(失敗しても止めない)。
func Comment(db *sql.DB, session string, cid int64) {
	var tid, kind, text, name, dels string
	if db.QueryRow(`SELECT task_id, kind, text, from_name, deliverables FROM comments WHERE id=?`, cid).Scan(&tid, &kind, &text, &name, &dels) != nil {
		return
	}
	q := New(db, session)
	switch kind {
	case "question", "blocked":
		q.Emit(Event{Type: kind, Ref: "C-" + strconv.FormatInt(cid, 10), A: tid, B: name, C: text})
	case "done":
		d, ok := tickets.JoinDeliverables(dels)
		if !ok {
			return
		}
		q.Emit(Event{Type: "done", Ref: tid, A: tid, B: text, C: d})
	}
}

// TicketRules は通知の規則を流す(失敗しても止めない)。
func TicketRules(db *sql.DB, session string, st tickets.StatusSet) {
	q := New(db, session)
	q.ActiveSQL = st.SQLList("active", "review")
	_ = q.RunRules()
}
