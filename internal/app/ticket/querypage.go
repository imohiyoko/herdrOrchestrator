package ticket

// 画面向けの読み取り(Web UI のタスクの一覧・ボード)。接続は呼び手の rowfmt.Q(__<分類>_STATUSES__ は Q が埋める)。

import (
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
)

func countOf(r []rowfmt.Row) (int64, bool) {
	if len(r) == 0 {
		return 0, false
	}
	n, _ := r[0]["n"].(int64)
	return n, true
}

// ReportCounts は報告の件数の行。open が偽なら(列は task_id・kind・n)種類ごと、真なら(列は task_id・n)未対応の question / blocked。
// taskID が nil なら全部のチケット。
func ReportCounts(q rowfmt.Q, taskID any, open bool) []rowfmt.Row {
	var a []any
	if open {
		w := ""
		if taskID != nil {
			w, a = " AND task_id=?", []any{taskID}
		}
		r, _ := q("SELECT task_id, COUNT(*) AS n FROM comments WHERE kind IN ('question','blocked') AND resolved_at IS NULL"+w+" GROUP BY task_id", a...)
		return r
	}
	w := ""
	if taskID != nil {
		w, a = " WHERE task_id=?", []any{taskID}
	}
	r, _ := q("SELECT task_id, kind, COUNT(*) AS n FROM comments"+w+" GROUP BY task_id, kind", a...)
	return r
}

// ListFilter はタスクの一覧の絞り込み。空の項目は絞らない。
// Keyword は ID か題に含まれる文字(LIKE。呼び手がエスケープして % で包む)。NoOwner は担当なし。
// Due は overdue(Today より前)か soon(Today から SoonTo まで)。どちらも閉じたものは除く。
// Statuses が空なら View(open / closed。空はすべて)で分類を絞る。StaleBefore があれば、作業中のままそれより前に更新したもの。
type ListFilter struct {
	Keyword, Project, Owner, Type, Priority, Label string
	NoOwner, Unreported                            bool
	Due, Today, SoonTo                             string
	Statuses                                       []string
	View, StaleBefore                              string
}

// base は状態とその分類以外の条件。
func (f ListFilter) base() ([]string, []any) {
	var w []string
	var p []any
	if f.Keyword != "" {
		w, p = append(w, `(id LIKE ? ESCAPE '\' OR title LIKE ? ESCAPE '\')`), append(p, f.Keyword, f.Keyword)
	}
	if f.Project != "" {
		w, p = append(w, "project=?"), append(p, f.Project)
	}
	if f.NoOwner {
		w = append(w, "(owner_agent IS NULL OR owner_agent='')")
	} else if f.Owner != "" {
		w, p = append(w, "owner_agent=?"), append(p, f.Owner)
	}
	if f.Unreported {
		w = append(w, "reported_at IS NULL")
	}
	if f.Type != "" {
		w, p = append(w, "type=?"), append(p, f.Type)
	}
	if f.Priority != "" {
		w, p = append(w, "priority=?"), append(p, f.Priority)
	}
	if f.Label != "" {
		w, p = append(w, "EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)"), append(p, f.Label)
	}
	switch f.Due {
	case "overdue":
		w, p = append(w, "due IS NOT NULL", "due<?", "status NOT IN (__CLOSED_STATUSES__)"), append(p, f.Today)
	case "soon":
		w, p = append(w, "due IS NOT NULL", "due>=?", "due<=?", "status NOT IN (__CLOSED_STATUSES__)"), append(p, f.Today, f.SoonTo)
	}
	return w, p
}

func where(w []string) string {
	if len(w) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w, " AND ")
}

func (f ListFilter) full() (string, []any) {
	w, p := f.base()
	switch {
	case len(f.Statuses) > 0:
		w = append(w, "status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Statuses)), ",")+")")
		for _, x := range f.Statuses {
			p = append(p, x)
		}
	case f.View == "open":
		w = append(w, "status NOT IN (__CLOSED_STATUSES__)")
	case f.View == "closed":
		w = append(w, "status IN (__CLOSED_STATUSES__)")
	}
	if f.StaleBefore != "" {
		w, p = append(w, "status IN (__ACTIVE_STATUSES__)", "updated_at<?"), append(p, f.StaleBefore)
	}
	return where(w), p
}

// StatusRank は状態の分類の並び(blocked → active → review → todo → ほか)。ORDER BY に使う。
const StatusRank = "CASE WHEN status IN (__BLOCKED_STATUSES__) THEN 1 WHEN status IN (__ACTIVE_STATUSES__) THEN 2" +
	" WHEN status IN (__REVIEW_STATUSES__) THEN 3 WHEN status IN (__TODO_STATUSES__) THEN 4 ELSE 5 END"

// DefaultOrder は一覧の既定の並び(未対応の質問のある閉じていないもの → 状態の分類 → PJ → 新しく更新した順)。
// openQ が偽(resolved_at の無い古い DB)なら質問では並べない。
func DefaultOrder(openQ bool) string {
	o := ""
	if openQ {
		o = "(EXISTS (SELECT 1 FROM comments c WHERE c.task_id=tasks.id AND c.kind IN ('question','blocked') AND c.resolved_at IS NULL" +
			" AND tasks.status NOT IN (__CLOSED_STATUSES__))) DESC, "
	}
	return o + StatusRank + ", project, updated_at DESC"
}

// ListRows は絞り込みに合うチケット(全部の列)を order の順に max 件までと、合う件数(数えられなければ ok=false)。
// order は呼び手の並び(ORDER BY の中身)。
func ListRows(q rowfmt.Q, f ListFilter, order string, max int) (rows []rowfmt.Row, total int64, ok bool) {
	wh, p := f.full()
	rows, _ = q("SELECT * FROM tasks"+wh+" ORDER BY "+order+" LIMIT ?", append(append([]any(nil), p...), max)...)
	r, _ := q("SELECT COUNT(*) AS n FROM tasks"+wh, p...)
	total, ok = countOf(r)
	return rows, total, ok
}

// ListStatusCounts は状態とその分類以外の絞り込みに合うチケットの、状態ごとの件数(列は status・n)。
func ListStatusCounts(q rowfmt.Q, f ListFilter) []rowfmt.Row {
	w, p := f.base()
	r, _ := q("SELECT status, COUNT(*) AS n FROM tasks"+where(w)+" GROUP BY status", p...)
	return r
}

// DueCounts は期限切れ(today より前)と、today から until までのチケットの数(閉じたものは除く)。
func DueCounts(q rowfmt.Q, today, until string) (over, soon int64) {
	r, _ := q("SELECT COUNT(*) AS n FROM tasks WHERE due IS NOT NULL AND due<? AND status NOT IN (__CLOSED_STATUSES__)", today)
	over, _ = countOf(r)
	r, _ = q("SELECT COUNT(*) AS n FROM tasks WHERE due IS NOT NULL AND due>=? AND due<=? AND status NOT IN (__CLOSED_STATUSES__)", today, until)
	soon, _ = countOf(r)
	return over, soon
}

// Distinct は値の一覧(名前の順の行)。what はチケットの status / project / owner / label(列は status / project / owner_agent / value)と、
// スプリントの sprint-project(列は project)。ほかの値なら nil。
func Distinct(q rowfmt.Q, what string) []rowfmt.Row {
	var s string
	switch what {
	case "status":
		s = "SELECT DISTINCT status FROM tasks ORDER BY status"
	case "project":
		s = "SELECT DISTINCT project FROM tasks ORDER BY project"
	case "owner":
		s = "SELECT DISTINCT owner_agent FROM tasks WHERE owner_agent IS NOT NULL AND owner_agent!='' ORDER BY owner_agent"
	case "label":
		s = "SELECT DISTINCT j.value FROM tasks, json_each(tasks.labels) j ORDER BY j.value"
	case "sprint-project":
		s = "SELECT DISTINCT project FROM sprints ORDER BY project"
	default:
		return nil
	}
	r, _ := q(s)
	return r
}

// ChildCounts は親ごとの子チケットの数(列は parent_id・n)。
func ChildCounts(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT parent_id, COUNT(*) AS n FROM tasks WHERE parent_id IS NOT NULL GROUP BY parent_id")
	return r
}

// HasSprintTable は DB にスプリントの表があるか。
func HasSprintTable(q rowfmt.Q) bool {
	r, _ := q("SELECT name FROM sqlite_master WHERE name='sprints'")
	return len(r) > 0
}

// SprintRows はスプリントと進み(列は sprints の全部と n・done_n・pts・done_pts)。project が空でなければその PJ だけ。
// 並びは active → planned → ほか、PJ、新しい順。
func SprintRows(q rowfmt.Q, project string) []rowfmt.Row {
	sq := "SELECT s.*, COALESCE(SUM(t.id IS NOT NULL AND t.status NOT IN (__CANCELLED_STATUSES__)),0) AS n, SUM(t.status IN (__DONE_STATUSES__)) AS done_n," +
		" COALESCE(SUM(CASE WHEN t.status NOT IN (__CANCELLED_STATUSES__) THEN t.points END),0) AS pts," +
		" COALESCE(SUM(CASE WHEN t.status IN (__DONE_STATUSES__) THEN t.points END),0) AS done_pts" +
		" FROM sprints s LEFT JOIN tasks t ON t.sprint_id=s.id"
	tail := " GROUP BY s.id ORDER BY CASE s.state WHEN 'active' THEN 1 WHEN 'planned' THEN 2 ELSE 3 END, s.project, s.id DESC"
	if project != "" {
		r, _ := q(sq+" WHERE s.project=?"+tail, project)
		return r
	}
	r, _ := q(sq + tail)
	return r
}

// SprintTasks はスプリント id のチケット(label が空でなければそのラベルのもの)を優先度の順に 300 件まで。
func SprintTasks(q rowfmt.Q, id int64, label string) []rowfmt.Row {
	r, _ := q("SELECT * FROM tasks WHERE sprint_id=? AND (?='' OR EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?))"+
		" ORDER BY CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END, id"+
		" LIMIT 300", id, label, label)
	return r
}
