package agents

import (
	"database/sql"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
)

// agent の止める・戻すの部品(tb の agent-stop などと、アプリケーション API の app/agent が使う)。

func nowTS() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// LiveOf は live_row: herdr の一覧から名前が一致する 1 件。
func LiveOf(live []herdr.Agent, name string) (herdr.Agent, bool) {
	for _, a := range live {
		if a.Name == name {
			return a, true
		}
	}
	return herdr.Agent{}, false
}

// Killable は killable_of: protected|yes|no|unknown|stale。
func Killable(db *sql.DB, session, name string) string {
	if _, ok := NewRoles(db, session).ProtectReason(name); ok {
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

// ClosePane は agent_close_pane: pane を閉じて台帳を stopped にする(session は残す)。閉じられなければ false。
func ClosePane(db *sql.DB, session, name, pane, sess string) bool {
	if _, err := herdr.Call("pane", "close", pane); err != nil {
		return false
	}
	ts := nowTS()
	_, _ = db.Exec(`UPDATE main.agents SET state='stopped', stopped_at=?, updated_at=?, pane_id=NULL,
         session_id=COALESCE(NULLIF(?,''), session_id) WHERE herdr_session=? AND name=?`, ts, ts, sess, session, name)
	return true
}

// OtherSession は、name がこのセッションに無く、ほかの herdr セッションの agent なら案内の文言と true。
func OtherSession(db *sql.DB, session, name string) (string, bool) {
	var s string
	if db.QueryRow(`SELECT herdr_session FROM main.agents WHERE name=? AND herdr_session!=?
    AND NOT EXISTS (SELECT 1 FROM agents WHERE name=?) ORDER BY updated_at DESC LIMIT 1`, name, session, name).Scan(&s) != nil || s == "" {
		return "", false
	}
	return name + " は herdr セッション '" + s + "' の agent です。HERDR_SESSION=" + s + " を付けて実行してください", true
}

// RecordOp は record_op: agent の操作の結果を agent_ops に決まった行として残す(rc が 0 なら done、ほかは failed)。
func RecordOp(db *sql.DB, session, op, target, args, by string, rc int, result string) {
	st := "done"
	if rc != 0 {
		st = "failed"
	}
	ts := nowTS()
	_, _ = db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		session, op, target, args, by, st, result, ts, ts)
}
