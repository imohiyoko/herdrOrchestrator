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
