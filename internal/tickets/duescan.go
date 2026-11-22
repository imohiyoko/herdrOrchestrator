package tickets

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// DueScan は tasks.sh の due_scan_sql: 期限切れ(overdue)と期限間近(due_soon)のイベントを、1 日 1 回ずつ task_events に積む。
// closedSQL は閉じた状態の SQL の並び。project が空なら全部。積んだ件数を返す。
func DueScan(db *sql.DB, session, closedSQL, today string, within int, project string) (overdue, soon int64, err error) {
	ts, day := time.Now().UTC().Format("2006-01-02T15:04:05Z"), "day:"+today
	pw := ""
	if project != "" {
		pw = " AND t.project=" + store.Quote(project)
	}
	seen := func(kind string) string {
		return fmt.Sprintf("NOT EXISTS (SELECT 1 FROM main.task_events e WHERE e.herdr_session=%s AND e.task_id=t.id AND e.kind='%s' AND e.ref=%s)", store.Quote(session), kind, store.Quote(day))
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	r, err := conn.ExecContext(ctx, `INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
      SELECT ?, t.id, 'overdue', NULL, t.due, 'system', ?, ? FROM tasks t
      WHERE t.due IS NOT NULL AND t.status NOT IN (`+closedSQL+`) AND t.due < ?`+pw+` AND `+seen("overdue")+` ORDER BY t.due, t.id`, session, day, ts, today)
	if err != nil {
		return 0, 0, err
	}
	overdue, _ = r.RowsAffected()
	r, err = conn.ExecContext(ctx, fmt.Sprintf(`INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
      SELECT ?, t.id, 'due_soon', NULL, t.due, 'system', ?, ? FROM tasks t
      WHERE t.due IS NOT NULL AND t.status NOT IN (`+closedSQL+`) AND t.due >= ? AND t.due <= date(?, '+%d days')`+pw+` AND `+seen("due_soon")+` ORDER BY t.due, t.id`, within), session, day, ts, today, today)
	if err != nil {
		return 0, 0, err
	}
	soon, _ = r.RowsAffected()
	_, err = conn.ExecContext(ctx, "COMMIT")
	return overdue, soon, err
}
