package agent

// 画面向けの読み取り(Web UI)。接続は呼び手の rowfmt.Q。

import "github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"

// LedgerRow は台帳の 1 体(全部の列)。無ければ ok=true で nil、読めなければ ok=false。
func LedgerRow(q rowfmt.Q, name string) (rowfmt.Row, bool) {
	r, ok := q("SELECT * FROM agents WHERE name=?", name)
	if !ok || len(r) == 0 {
		return nil, ok
	}
	return r[0], true
}

// LedgerRole は台帳の役割の行(role の列だけ)。読めなければ ok=false。
func LedgerRole(q rowfmt.Q, name string) ([]rowfmt.Row, bool) {
	return q("SELECT role FROM agents WHERE name=?", name)
}

// killableSQL は台帳の行に、今の止めてよいか(killable_now。stale を含む)と開いているタスクの数を足したもの。
const killableSQL = `SELECT a.*, CASE WHEN a.killable!='yes' THEN a.killable
      WHEN a.killable_session IS NOT NULL AND a.session_id IS NOT NULL AND a.killable_session!=a.session_id THEN 'stale'
      WHEN EXISTS (SELECT 1 FROM agent_activity aa WHERE (aa.session_id=a.session_id OR aa.pane_id=a.pane_id)
                   AND julianday(aa.last_prompt_at) > julianday(a.killable_at)) THEN 'stale'
      ELSE 'yes' END AS killable_now,
    (SELECT COUNT(*) FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (__HELD_STATUSES__)) AS open_tasks
    FROM agents a`

// LedgerAll は台帳の全員(名前の順。killable_now と open_tasks つき)。
func LedgerAll(q rowfmt.Q) []rowfmt.Row {
	r, _ := q(killableSQL + " ORDER BY a.name")
	return r
}

// LedgerStopped は止まった agent を新しい順に 50 体まで(killable_now と open_tasks つき)。
func LedgerStopped(q rowfmt.Q) []rowfmt.Row {
	r, _ := q(killableSQL + " WHERE a.state='stopped' ORDER BY a.stopped_at DESC LIMIT 50")
	return r
}
