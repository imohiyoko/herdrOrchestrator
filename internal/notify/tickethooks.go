package notify

// チケットの書き込みのあとに流すもの(tb の notify_hook_task_done と notify_rules_hook)。
// tb の入口とアプリケーション API(internal/app/ticket)の両方が使う。

import (
	"database/sql"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// TaskDone は完了の通知を積む(失敗しても止めない)。成果物が文字列の配列でなければ積まない(jq の join と同じ)。
func TaskDone(db *sql.DB, session, id string) {
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
			return
		}
		parts[i] = s
	}
	New(db, session).Emit(Event{Type: "done", Ref: id, A: id, B: summary.String, C: strings.Join(parts, ", ")})
}

// TicketRules は通知の規則を流す(失敗しても止めない)。
func TicketRules(db *sql.DB, session string, st tickets.StatusSet) {
	q := New(db, session)
	q.ActiveSQL = st.SQLList("active", "review")
	_ = q.RunRules()
}
