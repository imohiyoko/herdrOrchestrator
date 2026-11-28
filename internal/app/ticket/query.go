package ticket

// 画面向けの読み取り(Web UI)。接続は呼び手の rowfmt.Q(__DONE_STATUSES__ などは Q が埋める)。

import "github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"

// RecentComments はコメントを新しい順に n 件まで(全部の列)。
func RecentComments(q rowfmt.Q, n int) []rowfmt.Row {
	r, _ := q("SELECT * FROM comments ORDER BY id DESC LIMIT ?", n)
	return r
}

// RecentDone は直近に done の分類になったチケットを n 件まで。
func RecentDone(q rowfmt.Q, n int) []rowfmt.Row {
	r, _ := q("SELECT id,project,title,owner_agent,updated_at FROM tasks WHERE status IN (__DONE_STATUSES__) ORDER BY updated_at DESC LIMIT ?", n)
	return r
}

// OpenByOwner は担当 owner の閉じていないチケット(新しく更新した順。全部の列)。
func OpenByOwner(q rowfmt.Q, owner any) []rowfmt.Row {
	r, _ := q("SELECT * FROM tasks WHERE owner_agent=? AND status NOT IN (__CLOSED_STATUSES__) ORDER BY updated_at DESC", owner)
	return r
}

// OpenBySession は Claude の session sid が持つ閉じていないチケット(新しく更新した順。全部の列)。
func OpenBySession(q rowfmt.Q, sid any) []rowfmt.Row {
	r, _ := q("SELECT * FROM tasks WHERE session_id=? AND status NOT IN (__CLOSED_STATUSES__) ORDER BY updated_at DESC", sid)
	return r
}

// Exists はチケット id があるか(読めなければ false)。
func Exists(q rowfmt.Q, id string) bool {
	r, _ := q("SELECT id FROM tasks WHERE id=?", id)
	return len(r) > 0
}

// StatusCounts は状態ごとの件数(列は status・n)。
func StatusCounts(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT status, COUNT(*) AS n FROM tasks GROUP BY status")
	return r
}

// Projects はチケットとスプリントにあるプロジェクトの名前の行(列は project。チケットの分が先、それぞれ名前の順。重なりは除かない)。
func Projects(q rowfmt.Q) []rowfmt.Row {
	var out []rowfmt.Row
	for _, sq := range []string{"SELECT DISTINCT project FROM tasks ORDER BY project", "SELECT DISTINCT project FROM sprints ORDER BY project"} {
		r, _ := q(sq)
		out = append(out, r...)
	}
	return out
}

const (
	commentDocCols = "SELECT id,task_id,kind,deliverables,from_name,from_session,created_at FROM comments"
	taskDocCols    = "SELECT id,owner_agent,session_id,deliverables,updated_at FROM tasks"
)

// CommentDoc はコメント id の成果物の行(無ければ nil)。
func CommentDoc(q rowfmt.Q, id int) rowfmt.Row {
	if r, _ := q(commentDocCols+" WHERE id=?", id); len(r) > 0 {
		return r[0]
	}
	return nil
}

// TaskDoc はチケット id の成果物の行(無ければ nil)。
func TaskDoc(q rowfmt.Q, id string) rowfmt.Row {
	if r, _ := q(taskDocCols+" WHERE id=?", id); len(r) > 0 {
		return r[0]
	}
	return nil
}

// CommentDocs は成果物のあるコメントを新しい順に n 件まで。
func CommentDocs(q rowfmt.Q, n int) []rowfmt.Row {
	r, _ := q(commentDocCols+" WHERE deliverables IS NOT NULL AND deliverables NOT IN ('','[]') ORDER BY id DESC LIMIT ?", n)
	return r
}

// TaskDocs は成果物のあるチケットを新しく更新した順に n 件まで。
func TaskDocs(q rowfmt.Q, n int) []rowfmt.Row {
	r, _ := q(taskDocCols+" WHERE deliverables IS NOT NULL AND deliverables NOT IN ('','[]') ORDER BY updated_at DESC LIMIT ?", n)
	return r
}

// Task はチケット id の行(全部の列。無ければ nil)。
func Task(q rowfmt.Q, id string) rowfmt.Row {
	if r, _ := q("SELECT * FROM tasks WHERE id=?", id); len(r) > 0 {
		return r[0]
	}
	return nil
}

// Children は親 id の子チケット(ID の順)。
func Children(q rowfmt.Q, id string) []rowfmt.Row {
	r, _ := q("SELECT id, title, status, owner_agent, updated_at FROM tasks WHERE parent_id=? ORDER BY id", id)
	return r
}

// Links はチケット id のリンク(列は rel・id・title・status。rel は id から見た関係)。
func Links(q rowfmt.Q, id string) []rowfmt.Row {
	r, _ := q("SELECT CASE WHEN l.from_id=? THEN l.link_type WHEN l.link_type='blocks' THEN 'blocked by'"+
		" WHEN l.link_type='duplicates' THEN 'duplicated by' ELSE 'relates' END AS rel,"+
		" x.id, x.title, x.status FROM task_links l JOIN tasks x ON x.id = CASE WHEN l.from_id=? THEN l.to_id ELSE l.from_id END"+
		" WHERE l.from_id=? OR l.to_id=? ORDER BY l.id", id, id, id, id)
	return r
}

// Events はチケット id の履歴を新しい順に n 件まで。
func Events(q rowfmt.Q, id string, n int) []rowfmt.Row {
	r, _ := q("SELECT id, kind, old, new, actor, ref, created_at FROM task_events WHERE task_id=? ORDER BY id DESC LIMIT ?", id, n)
	return r
}

// Comments はチケット id のコメント(新しい順。全部の列)。
func Comments(q rowfmt.Q, id string) []rowfmt.Row {
	r, _ := q("SELECT * FROM comments WHERE task_id=? ORDER BY id DESC", id)
	return r
}

// ProxyRequests はチケット id の代行依頼(新しい順。全部の列)。
func ProxyRequests(q rowfmt.Q, id string) []rowfmt.Row {
	r, _ := q("SELECT * FROM proxy_requests WHERE task_id=? ORDER BY id DESC", id)
	return r
}

// OpenQuestions は未対応の question / blocked の報告(閉じていないチケットのもの。task_status・task_title つき)を新しい順に 100 件まで。
// resolved が真なら対応済み(resolved_at のある)ものを除く(列の無い古い DB では偽で呼ぶ)。
func OpenQuestions(q rowfmt.Q, resolved bool) []rowfmt.Row {
	res := ""
	if resolved {
		res = " AND c.resolved_at IS NULL"
	}
	r, _ := q("SELECT c.*, t.status AS task_status, t.title AS task_title FROM comments c " +
		"LEFT JOIN tasks t ON t.id=c.task_id WHERE c.kind IN ('question','blocked') " +
		"AND COALESCE(t.status,'') NOT IN (__CLOSED_STATUSES__)" + res + " ORDER BY c.id DESC LIMIT 100")
	return r
}

// ProxyPendingHuman は人の判断待ちの代行依頼を新しい順に 100 件まで。
func ProxyPendingHuman(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT * FROM proxy_requests WHERE status='pending-human' ORDER BY id DESC LIMIT 100")
	return r
}
