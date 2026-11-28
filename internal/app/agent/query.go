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

// CwdOf は Claude の session sid の作業ディレクトリの行(列は cwd。agent_activity)。
func CwdOf(q rowfmt.Q, sid any) []rowfmt.Row {
	r, _ := q("SELECT cwd FROM agent_activity WHERE session_id=?", sid)
	return r
}

// Cwds は agent の作業ディレクトリ(空でないもの。重なりなし。列は cwd)。
func Cwds(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT DISTINCT cwd FROM agent_activity WHERE cwd IS NOT NULL AND cwd != ''")
	return r
}

// OpRows は agent の操作の記録(L-<n>)を新しい順に limit 件まで。status が空でなければその状態だけ。
func OpRows(q rowfmt.Q, status string, limit int) []rowfmt.Row {
	if status != "" {
		r, _ := q("SELECT * FROM agent_ops WHERE status=? ORDER BY id DESC LIMIT ?", status, limit)
		return r
	}
	r, _ := q("SELECT * FROM agent_ops ORDER BY id DESC LIMIT ?", limit)
	return r
}

// ActivityBySession は Claude の session sid の活動の行(全部の列)。
func ActivityBySession(q rowfmt.Q, sid any) []rowfmt.Row {
	r, _ := q("SELECT * FROM agent_activity WHERE session_id=?", sid)
	return r
}

// ActivityByPane は pane の最新の活動の行(全部の列。1 行まで)。
func ActivityByPane(q rowfmt.Q, pane any) []rowfmt.Row {
	r, _ := q("SELECT * FROM agent_activity WHERE pane_id=? ORDER BY updated_at DESC LIMIT 1", pane)
	return r
}
