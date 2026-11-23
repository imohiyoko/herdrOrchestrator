package webui

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// tasks.go はタスク一覧(/tasks)とボード(/board)。読み取りのみ(views.page_tasks / page_board)。

type taskSort struct{ key, label, order string }

var (
	taskSorts = []taskSort{
		{"", "優先順(未対応の報告 → 詰まり → 作業中 → 確認待ち → 未着手 → 閉じたもの)", ""},
		{"project", "PJ ごと・更新が新しい順", "project, updated_at DESC"},
		{"oldest", "更新が古い順", "updated_at ASC"},
		{"created", "作成が新しい順", "created_at DESC"}, {"id", "タスク ID 順", "id ASC"},
		{"due", "期日が近い順(期日なしは最後)", "due IS NULL, due, updated_at DESC"},
		{"priority", "優先度が高い順(同じなら更新が新しい順)",
			"CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END, updated_at DESC"},
	}
	taskTypes      = []string{"epic", "story", "task", "bug", "subtask"}
	taskPriorities = []string{"highest", "high", "medium", "low", "lowest"}
	reLabel        = regexp.MustCompile(`^[^` + pySpaceCls + `,"']{1,40}$`)
	reOwner        = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reShortTS      = regexp.MustCompile(`^(\p{Nd}{4}-\p{Nd}{2}-\p{Nd}{2})T(\p{Nd}{2}:\p{Nd}{2})(:\p{Nd}{2}(\.\p{Nd}+)?)?Z?$`)
	reSprint       = regexp.MustCompile(`^S-[0-9]{1,9}$`)
	reRefURL       = regexp.MustCompile(`^https?://[^` + pySpaceCls + `"'<>]{1,500}$`)
	taskViews      = [][2]string{{"open", "未完了"}, {"closed", "閉じたもの"}, {"all", "すべて"}}
	dueViews       = [][2]string{{"overdue", "期限切れ"}, {"soon", "期限間近"}}
)

const (
	dueSoonDays  = 3
	dueDaysMax   = 365
	taskListMax  = 300
	titleMax     = 80
	reportMax    = 1000
	ownerNone    = "~" // 担当の絞り込みの「未割り当て」
	kwMax        = 100
	taskStaleHrs = 24
)

func lookup2(xs [][2]string, k string) (string, bool) {
	for _, x := range xs {
		if x[0] == k {
			return x[1], true
		}
	}
	return "", false
}

func sortOf(k string) (taskSort, bool) {
	for _, s := range taskSorts {
		if s.key == k {
			return s, true
		}
	}
	return taskSort{}, false
}

// head は _head: 先頭 n 文字だけ(エスケープ前)。
func head(x any, n int) string {
	s := ""
	if x != nil {
		s = pyStr(x)
	}
	s = Clean(s)
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func runesN(s string, n int) string { return runesUpTo(s, n) }

func (v *sv) statusRank() string {
	return "CASE WHEN status IN (__BLOCKED_STATUSES__) THEN 1 WHEN status IN (__ACTIVE_STATUSES__) THEN 2" +
		" WHEN status IN (__REVIEW_STATUSES__) THEN 3 WHEN status IN (__TODO_STATUSES__) THEN 4 ELSE 5 END"
}

// taskStale は作業中(分類 active)のまま 24 時間を超えて更新がない経過時間。
func (v *sv) taskStale(t row, now time.Time) (int, bool) {
	if !contains(v.stNames("active"), pyStr(t["status"])) || t["status"] == nil {
		return 0, false
	}
	u, ok := parseTS(t["updated_at"])
	if !ok {
		return 0, false
	}
	h := now.Sub(u).Hours()
	if h > taskStaleHrs {
		return int(h), true
	}
	return 0, false
}

func todayStr() string { return nowFn().Local().Format("2006-01-02") }

func addDays(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

func (v *sv) dueState(t row, today string, days int) string {
	d, ok := t["due"].(string)
	if !ok || d == "" || contains(v.stNames("done", "cancelled"), pyStr(t["status"])) {
		return ""
	}
	if d < today {
		return "overdue"
	}
	if d <= addDays(today, days) {
		return "soon"
	}
	return ""
}

func (v *sv) dueHTML(t row, today string) string {
	if !truthy(t["due"]) {
		return "-"
	}
	switch v.dueState(t, today, dueSoonDays) {
	case "overdue":
		return `<span class=err>` + ev(t["due"]) + ` 期限切れ</span>`
	case "soon":
		return `<span class=warn>` + ev(t["due"]) + ` 間近</span>`
	}
	return ev(t["due"])
}

type counts map[string]int64

// reportCounts は {task_id: {kind: 件数, "open": 未対応の question / blocked}}。
func (v *sv) reportCounts(taskID any) map[string]counts {
	out := map[string]counts{}
	w, a := "", []any{}
	if taskID != nil {
		w, a = " WHERE task_id=?", []any{taskID}
	}
	add := func(tid any, k string, n any) {
		key := pyStr(tid)
		if out[key] == nil {
			out[key] = counts{}
		}
		c, _ := n.(int64)
		out[key][k] = c
	}
	for _, r := range v.q("SELECT task_id, kind, COUNT(*) AS n FROM comments"+w+" GROUP BY task_id, kind", a...) {
		add(r["task_id"], pyStr(r["kind"]), r["n"])
	}
	if v.hasResolution() {
		w2 := ""
		if taskID != nil {
			w2 = " AND task_id=?"
		}
		for _, r := range v.q("SELECT task_id, COUNT(*) AS n FROM comments WHERE kind IN ('question','blocked') AND resolved_at IS NULL"+w2+
			" GROUP BY task_id", a...) {
			add(r["task_id"], "open", r["n"])
		}
	}
	return out
}

func countsLabel(c counts) string {
	if len(c) == 0 {
		return `<span class=note>(報告なし)</span>`
	}
	var ps []string
	for _, k := range []string{"done", "progress", "question", "blocked"} {
		if c[k] != 0 {
			ps = append(ps, fmt.Sprintf("%s %d", k, c[k]))
		}
	}
	out := E(strings.Join(ps, " / "))
	if c["open"] != 0 {
		out += fmt.Sprintf(` <span class=warn>(未対応 %d・完了済みのタスクを含む)</span>`, c["open"])
	}
	return out
}

func shortTS(ts any) string {
	s := ""
	if ts != nil {
		s = pyStr(ts)
	}
	if m := reShortTS.FindStringSubmatch(s); m != nil {
		return `<span title="` + E(s) + `">` + E(m[1]) + ` ` + E(m[2]) + `</span>`
	}
	return E(s)
}

func (v *sv) hasColumn(table, col string) bool {
	for _, r := range v.q("PRAGMA table_info(" + table + ")") {
		if r["name"] == col {
			return true
		}
	}
	return false
}

func priorityHTML(p any) string {
	if !truthy(p) {
		return "-"
	}
	if p == "highest" || p == "high" {
		return `<span class=warn>` + ev(p) + `</span>`
	}
	return ev(p)
}

// safeJSONList は json.loads(s or "[]") がリストならそれ、ほかは空。
func safeJSONList(x any) []any {
	s := "[]"
	if truthy(x) {
		s = pyStr(x)
	}
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil || d.More() {
		return nil
	}
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func labelsHTML(raw any, href func(string) string) string {
	var labs []string
	for _, x := range safeJSONList(raw) {
		if s, ok := x.(string); ok {
			labs = append(labs, s)
		}
	}
	if len(labs) == 0 {
		return "-"
	}
	var out []string
	for _, x := range labs {
		if href == nil {
			out = append(out, "<code>"+E(x)+"</code>")
		} else {
			out = append(out, `<a href="`+E(href(x))+`"><code>`+E(x)+`</code></a>`)
		}
	}
	return strings.Join(out, " ")
}

// definedLabels は config の labels(1 行 1 つ: <ラベル> [= 説明])。
func (v *sv) definedLabels() []string {
	text, _, _ := v.configText("labels")
	var out []string
	seen := map[string]bool{}
	for _, line := range pySplitlines(text) {
		line, _, _ = strings.Cut(line, "#")
		line = pyStrip(line)
		if line == "" {
			continue
		}
		k, _, _ := strings.Cut(line, "=")
		k = pyStrip(k)
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func (v *sv) statusNames() []string {
	var out []string
	for _, r := range v.statuses().rows {
		out = append(out, r.name)
	}
	return out
}

// boardColumns はボードの列: 分類の順、同じ分類は書いた順。
func (v *sv) boardColumns() []string {
	var out []string
	for _, c := range statusCategories {
		for _, r := range v.statuses().rows {
			if r.cat == c {
				out = append(out, r.name)
			}
		}
	}
	return out
}

func (v *sv) stMain(cat string) string {
	for _, r := range v.statuses().rows {
		if r.cat == cat {
			return r.name
		}
	}
	for _, b := range statusBuiltin {
		if b[1] == cat {
			return b[0]
		}
	}
	return ""
}

func likeEsc(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
}

func optsHTML(vals []string, cur string, labels map[string]string) string {
	o := `<option value="">(すべて)</option>`
	for _, x := range vals {
		l := x
		if s, ok := labels[x]; ok {
			l = s
		}
		o += `<option value="` + E(x) + `"` + sel(x == cur, " selected") + `>` + E(l) + `</option>`
	}
	return o
}

func intOf(x any) int64 {
	switch t := x.(type) {
	case int64:
		return t
	case float64:
		return int64(math.Trunc(t))
	case bool:
		if t {
			return 1
		}
	case string:
		n, _ := strconv.ParseInt(strings.TrimFunc(t, pyIsSpace), 10, 64)
		return n
	}
	return 0
}

func (s *Server) pageTasks(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true // 入力欄があるので自動更新しない
	c.opts.scripts = []string{"/filter.js"}
	v := q.data()
	proj, owner, view := runesN(c.one("project"), 100), c.one("owner"), c.one("view")
	kw := runesN(c.one("q"), kwMax)
	var sts []string
	for _, x := range c.qs["status"] {
		if x = pyStrip(x); x != "" && !contains(sts, x) {
			sts = append(sts, x)
		}
	}
	unrep, stale := c.one("unreported") == "1", c.one("stale") == "1"
	srt := c.one("sort")
	ttype, prio, label := c.one("type"), c.one("priority"), c.one("label")
	dview, daysS := c.one("due"), c.one("days")
	tf, hd := v.hasColumn("tasks", "labels"), v.hasColumn("tasks", "due")
	var errs []string
	days := dueSoonDays
	if _, ok := lookup2(taskViews, view); view != "" && !ok {
		errs = append(errs, "表示は open(未完了)/ closed(閉じたもの)/ all(すべて)のどれかです")
		view = ""
	}
	if _, ok := lookup2(dueViews, dview); dview != "" && !ok {
		errs = append(errs, "期日の絞り込みは overdue(期限切れ)/ soon(期限間近)のどちらかです")
		dview = ""
	}
	if daysS != "" {
		if n, err := strconv.Atoi(daysS); err == nil && isDecimal(daysS) && len(daysS) <= 3 && n <= dueDaysMax {
			days = n
		} else {
			errs = append(errs, fmt.Sprintf("日数は 0〜%d の整数です", dueDaysMax))
			daysS = ""
		}
	}
	if !hd {
		dview, daysS = "", ""
		if srt == "due" {
			srt = ""
		}
	}
	if ttype != "" && !contains(taskTypes, ttype) {
		errs = append(errs, "種別は "+strings.Join(taskTypes, " / ")+" のどれかです")
		ttype = ""
	}
	if prio != "" && !contains(taskPriorities, prio) {
		errs = append(errs, "優先度は "+strings.Join(taskPriorities, " / ")+" のどれかです")
		prio = ""
	}
	if label != "" && !reLabel.MatchString(label) {
		errs = append(errs, "ラベルは 40 文字までで、空白・カンマ・引用符を含みません")
		label = ""
	}
	if !tf {
		ttype, prio, label = "", "", ""
		if srt == "priority" {
			srt = ""
		}
	}
	taskStatuses := v.statusNames()
	var present []string
	for _, r := range v.q("SELECT DISTINCT status FROM tasks ORDER BY status") {
		present = append(present, pyStr(r["status"]))
	}
	var keep, bad []string
	for _, x := range sts {
		if !contains(taskStatuses, x) && !contains(present, x) {
			bad = append(bad, x)
		} else {
			keep = append(keep, x)
		}
	}
	if len(bad) > 0 {
		errs = append(errs, "状態は "+strings.Join(taskStatuses, " / ")+" のどれかです")
		sts = keep
	}
	if owner != "" && owner != ownerNone && !reOwner.MatchString(owner) {
		errs = append(errs, "担当は英数字と . _ - だけです")
		owner = ""
	}
	if _, ok := sortOf(srt); !ok {
		srt = ""
	}
	eview := ""
	if len(sts) == 0 {
		eview = orStr(view, "open")
	}
	var baseW []string
	var baseP []any
	if kw != "" {
		lk := likeEsc(kw)
		baseW = append(baseW, `(id LIKE ? ESCAPE '\' OR title LIKE ? ESCAPE '\')`)
		baseP = append(baseP, lk, lk)
	}
	if proj != "" {
		baseW, baseP = append(baseW, "project=?"), append(baseP, proj)
	}
	if owner == ownerNone {
		baseW = append(baseW, "(owner_agent IS NULL OR owner_agent='')")
	} else if owner != "" {
		baseW, baseP = append(baseW, "owner_agent=?"), append(baseP, owner)
	}
	if unrep {
		baseW = append(baseW, "reported_at IS NULL")
	}
	if ttype != "" {
		baseW, baseP = append(baseW, "type=?"), append(baseP, ttype)
	}
	if prio != "" {
		baseW, baseP = append(baseW, "priority=?"), append(baseP, prio)
	}
	if label != "" {
		baseW, baseP = append(baseW, "EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)"), append(baseP, label)
	}
	today := todayStr()
	switch dview {
	case "overdue":
		baseW = append(baseW, "due IS NOT NULL", "due<?", "status NOT IN (__CLOSED_STATUSES__)")
		baseP = append(baseP, today)
	case "soon":
		baseW = append(baseW, "due IS NOT NULL", "due>=?", "due<=?", "status NOT IN (__CLOSED_STATUSES__)")
		baseP = append(baseP, today, addDays(today, days))
	}
	where := append([]string(nil), baseW...)
	params := append([]any(nil), baseP...)
	switch {
	case len(sts) > 0:
		where = append(where, "status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(sts)), ",")+")")
		for _, x := range sts {
			params = append(params, x)
		}
	case eview == "open":
		where = append(where, "status NOT IN (__CLOSED_STATUSES__)")
	case eview == "closed":
		where = append(where, "status IN (__CLOSED_STATUSES__)")
	}
	if stale {
		cutoff := nowFn().UTC().Add(-taskStaleHrs * time.Hour).Format("2006-01-02T15:04:05Z")
		where = append(where, "status IN (__ACTIVE_STATUSES__)", "updated_at<?")
		params = append(params, cutoff)
	}
	wh := ""
	if len(where) > 0 {
		wh = " WHERE " + strings.Join(where, " AND ")
	}
	so, _ := sortOf(srt)
	order := so.order
	if order == "" {
		openQ := ""
		if v.hasResolution() {
			openQ = "(EXISTS (SELECT 1 FROM comments c WHERE c.task_id=tasks.id AND c.kind IN ('question','blocked') AND c.resolved_at IS NULL" +
				" AND tasks.status NOT IN (__CLOSED_STATUSES__))) DESC, "
		}
		order = openQ + v.statusRank() + ", project, updated_at DESC"
	}
	rows := v.q("SELECT * FROM tasks"+wh+" ORDER BY "+order+" LIMIT "+strconv.Itoa(taskListMax), params...)
	total := int64(len(rows))
	if r := v.q("SELECT COUNT(*) AS n FROM tasks"+wh, params...); len(r) > 0 {
		total = intOf(r[0]["n"])
	}
	bw := ""
	if len(baseW) > 0 {
		bw = " WHERE " + strings.Join(baseW, " AND ")
	}
	summ := map[string]int64{}
	var summKeys []string
	for _, r := range v.q("SELECT status, COUNT(*) AS n FROM tasks"+bw+" GROUP BY status", baseP...) {
		k := pyStr(r["status"])
		summ[k] = intOf(r["n"])
		summKeys = append(summKeys, k)
	}
	closedSet := v.stNames("done", "cancelled")
	var nAll, nClosed int64
	for k, n := range summ {
		nAll += n
		if contains(closedSet, k) {
			nClosed += n
		}
	}
	vcount := map[string]int64{"open": nAll - nClosed, "closed": nClosed, "all": nAll}
	var projects, owners, labels []string
	for _, r := range v.q("SELECT DISTINCT project FROM tasks ORDER BY project") {
		projects = append(projects, pyStr(r["project"]))
	}
	for _, r := range v.q("SELECT DISTINCT owner_agent FROM tasks WHERE owner_agent IS NOT NULL AND owner_agent!='' ORDER BY owner_agent") {
		owners = append(owners, pyStr(r["owner_agent"]))
	}
	kids := map[string]int64{}
	for _, r := range v.q("SELECT parent_id, COUNT(*) AS n FROM tasks WHERE parent_id IS NOT NULL GROUP BY parent_id") {
		kids[pyStr(r["parent_id"])] = intOf(r["n"])
	}
	if tf {
		labels = v.definedLabels() // 用意したラベルは、まだ使われていなくても選べるように先に並べる
		for _, r := range v.q("SELECT DISTINCT j.value FROM tasks, json_each(tasks.labels) j ORDER BY j.value") {
			if x := pyStr(r["value"]); !contains(labels, x) {
				labels = append(labels, x)
			}
		}
	}
	rc := v.reportCounts(nil)
	b2 := func(x bool) string { return ifs(x, "1") }
	type cur struct {
		q, project, owner, view string
		status                  []string
		unreported, stale       string
		sort, typ, priority     string
		label, due, days        string
	}
	cu := cur{kw, proj, owner, view, sts, b2(unrep), b2(stale), srt, ttype, prio, label, dview, daysS}
	href := func(f func(*cur)) string {
		d := cu
		d.status = append([]string(nil), cu.status...)
		f(&d)
		if d.view == "open" {
			d.view = "" // 既定と同じなので URL に出さない
		}
		var kv [][2]string
		addKV := func(k, x string) {
			if x != "" {
				kv = append(kv, [2]string{k, x})
			}
		}
		addKV("q", d.q)
		addKV("project", d.project)
		addKV("owner", d.owner)
		addKV("view", d.view)
		for _, x := range d.status {
			kv = append(kv, [2]string{"status", x})
		}
		addKV("unreported", d.unreported)
		addKV("stale", d.stale)
		addKV("sort", d.sort)
		addKV("type", d.typ)
		addKV("priority", d.priority)
		addKV("label", d.label)
		addKV("due", d.due)
		addKV("days", d.days)
		if len(kv) == 0 {
			return "/tasks"
		}
		return "/tasks?" + pyURLEncode(kv)
	}
	orderSt := v.boardColumns()
	for _, x := range taskStatuses {
		if !contains(orderSt, x) {
			orderSt = append(orderSt, x)
		}
	}
	var extra []string
	for _, x := range append(append([]string(nil), summKeys...), present...) {
		if !contains(orderSt, x) && !contains(extra, x) {
			extra = append(extra, x)
		}
	}
	sort.Strings(extra)
	orderSt = append(orderSt, extra...)
	body := ""
	if len(errs) > 0 {
		body = errHTML(strings.Join(errs, "。"))
	}
	closedLabel := E(strings.Join(closedSet, " / "))
	if hd {
		var nOver, nSoon int64
		if r := v.q("SELECT COUNT(*) AS n FROM tasks WHERE due IS NOT NULL AND due<? AND status NOT IN (__CLOSED_STATUSES__)", today); len(r) > 0 {
			nOver = intOf(r[0]["n"])
		}
		if r := v.q("SELECT COUNT(*) AS n FROM tasks WHERE due IS NOT NULL AND due>=? AND due<=? AND status NOT IN (__CLOSED_STATUSES__)",
			today, addDays(today, dueSoonDays)); len(r) > 0 {
			nSoon = intOf(r[0]["n"])
		}
		if nOver != 0 || nSoon != 0 {
			cls := "note"
			if nOver != 0 {
				cls = "err"
			}
			body += fmt.Sprintf(`<p><a href="/tasks?due=overdue&amp;sort=due"><span class=%s>期限切れ %d</span></a> / `+
				`<a href="/tasks?due=soon&amp;sort=due">期限間近(%d 日以内)%d</a>`+
				` <span class=note>(今日 = %s、閉じたもの(%s)は数えない)</span></p>`, cls, nOver, dueSoonDays, nSoon, E(today), closedLabel)
		}
	}
	if v.conn() == nil {
		body += `<p class=err>タスク DB がありません(または開けません)</p>`
	}
	var tabs strings.Builder
	for _, tv := range taskViews {
		k := tv[0]
		tabs.WriteString(`<a class="pill` + sel(eview == k, " on") + `" href="` + E(href(func(d *cur) { d.view, d.status = k, nil })) + `">` +
			E(tv[1]) + ` ` + strconv.FormatInt(vcount[k], 10) + `</a>`)
	}
	var cells strings.Builder
	for _, x := range orderSt {
		if _, ok := summ[x]; !ok && !contains(taskStatuses, x) {
			continue
		}
		x := x
		cells.WriteString(`<a class="pill` + sel(len(sts) == 1 && sts[0] == x, " on") + `" href="` + E(href(func(d *cur) { d.status, d.view = []string{x}, "" })) + `">` +
			E(x) + ` ` + strconv.FormatInt(summ[x], 10) + `</a>`)
	}
	body += `<div class=tabs>` + tabs.String() + `<span class=sep></span>` + cells.String() + `</div>` +
		`<p class=note>件数はキーワード・PJ・担当などの絞り込みを反映し、状態・表示と「停滞だけ」は反映しません。状態を足す・外すのは <a href="/statuses">状態の定義</a>。</p>`
	var ropts strings.Builder
	for _, so := range taskSorts {
		if (tf || so.key != "priority") && (hd || so.key != "due") {
			ropts.WriteString(`<option value="` + so.key + `"` + sel(so.key == srt, " selected") + `>` + E(so.label) + `</option>`)
		}
	}
	f := []string{`<label>キーワード <input type=search name=q value="` + E(kw) + `" maxlength=` + strconv.Itoa(kwMax) + ` placeholder="ID・題" size=18></label>`,
		`<label>PJ <select name=project>` + optsHTML(projects, proj, nil) + `</select></label>`,
		`<label>担当 <select name=owner>` + optsHTML(append([]string{ownerNone}, owners...), owner, map[string]string{ownerNone: "(未割り当て)"}) + `</select></label>`}
	if tf {
		f = append(f, `<label>種別 <select name=type>`+optsHTML(taskTypes, ttype, nil)+`</select></label>`,
			`<label>優先度 <select name=priority>`+optsHTML(taskPriorities, prio, nil)+`</select></label>`,
			`<label>ラベル <select name=label>`+optsHTML(labels, label, nil)+`</select></label>`)
	}
	if hd {
		f = append(f, `<label>期日 <select name=due>`+optsHTML([]string{"overdue", "soon"}, dview, map[string]string{"overdue": "期限切れ", "soon": "期限間近"})+`</select></label>`,
			`<label>間近の日数 <input name=days size=3 value="`+E(daysS)+`" placeholder="`+strconv.Itoa(dueSoonDays)+`"></label>`)
	}
	f = append(f, `<label>並び <select name=sort>`+ropts.String()+`</select></label>`)
	var stbox strings.Builder
	for _, x := range orderSt {
		stbox.WriteString(`<label class=chk><input type=checkbox name=status value="` + E(x) + `"` + sel(contains(sts, x), " checked") + `> ` + E(x) + `</label>`)
	}
	body += `<form id=tfilter method=get action="/tasks" class=filters>` +
		`<input type=hidden name=view value="` + E(view) + `">` +
		`<div class=frow>` + strings.Join(f, "") + `</div>` +
		`<div class=frow><span class=flabel>状態</span>` + stbox.String() + `<span class=note>(選ばなければ上の表示に従う)</span></div>` +
		`<div class=frow><label class=chk><input type=checkbox name=unreported value=1` + sel(unrep, " checked") + `> 未報告だけ</label>` +
		`<label class=chk><input type=checkbox name=stale value=1` + sel(stale, " checked") + `> 停滞だけ(` + strconv.Itoa(taskStaleHrs) + ` 時間超の ` + E(v.stMain("active")) + `)</label>` +
		`<button type=submit>絞り込む</button> <a href="/tasks">解除</a></div></form>`
	type chip struct{ t, h string }
	var chips []chip
	if kw != "" {
		chips = append(chips, chip{"キーワード: " + kw, href(func(d *cur) { d.q = "" })})
	}
	if proj != "" {
		chips = append(chips, chip{"PJ: " + proj, href(func(d *cur) { d.project = "" })})
	}
	if owner != "" {
		o := owner
		if owner == ownerNone {
			o = "(未割り当て)"
		}
		chips = append(chips, chip{"担当: " + o, href(func(d *cur) { d.owner = "" })})
	}
	for _, x := range sts {
		x := x
		chips = append(chips, chip{"状態: " + x, href(func(d *cur) {
			var r []string
			for _, y := range sts {
				if y != x {
					r = append(r, y)
				}
			}
			d.status = r
		})})
	}
	if eview != "" && eview != "open" {
		l, _ := lookup2(taskViews, eview)
		chips = append(chips, chip{"表示: " + l, href(func(d *cur) { d.view = "" })})
	}
	if ttype != "" {
		chips = append(chips, chip{"種別: " + ttype, href(func(d *cur) { d.typ = "" })})
	}
	if prio != "" {
		chips = append(chips, chip{"優先度: " + prio, href(func(d *cur) { d.priority = "" })})
	}
	if label != "" {
		chips = append(chips, chip{"ラベル: " + label, href(func(d *cur) { d.label = "" })})
	}
	if dview != "" {
		l, _ := lookup2(dueViews, dview)
		if dview == "soon" {
			l += fmt.Sprintf("(%d 日以内)", days)
		}
		chips = append(chips, chip{"期日: " + l, href(func(d *cur) { d.due, d.days = "", "" })})
	}
	if unrep {
		chips = append(chips, chip{"未報告だけ", href(func(d *cur) { d.unreported = "" })})
	}
	if stale {
		chips = append(chips, chip{"停滞だけ", href(func(d *cur) { d.stale = "" })})
	}
	shown := "未完了"
	if eview != "open" {
		if len(sts) > 0 {
			shown = "状態で選んだもの"
		} else {
			shown, _ = lookup2(taskViews, eview)
		}
	}
	var ch strings.Builder
	for _, x := range chips {
		ch.WriteString(`<a class=chip href="` + E(x.h) + `" title="この条件を外す">` + E(x.t) + ` ×</a>`)
	}
	body += `<p class=chips><b>` + strconv.FormatInt(total, 10) + ` 件</b> <span class=note>(` + E(shown) + `)</span> ` + ch.String() +
		ifs(len(chips) > 0, ` <a href="/tasks">すべて解除</a>`) + "</p>"
	now := nowFn().UTC()
	var out [][]string
	labelHref := func(x string) string { return href(func(d *cur) { d.label = x }) }
	for _, t := range rows {
		mark := ""
		if sh, ok := v.taskStale(t, now); ok {
			mark = fmt.Sprintf(`<span class=warn>停滞 %d 時間</span>`, sh)
		}
		par := ""
		if truthy(t["parent_id"]) {
			par = tlink(t["parent_id"])
		}
		kid := ""
		if n := kids[pyStr(t["id"])]; n != 0 {
			kid = fmt.Sprintf("子 %d", n)
		}
		var pk []string
		for _, x := range []string{par, E(kid)} {
			if x != "" {
				pk = append(pk, x)
			}
		}
		rep := "-"
		if truthy(t["reported_at"]) {
			rep = "○"
		}
		typ := "-"
		if truthy(t["type"]) {
			typ = pyStr(t["type"])
		}
		out = append(out, []string{tlink(t["id"]), ev(t["project"]), E(typ), priorityHTML(t["priority"]),
			v.dueHTML(t, today), E(head(t["title"], titleMax)), labelsHTML(t["labels"], labelHref), ev(t["status"]), E(orStr(truthyStr(t["owner_agent"]), "-")),
			orStr(strings.Join(pk, " "), "-"), countsLabel(rc[pyStr(t["id"])]),
			strconv.Itoa(len(safeJSONList(t["deliverables"]))), rep, shortTS(t["created_at"]), shortTS(t["updated_at"]), mark})
	}
	if total > int64(len(rows)) {
		body += fmt.Sprintf(`<p class=err>%d 件のうち %d 件だけ表示しています(上限 %d 件)。`, total, len(rows), taskListMax) +
			`既定の並びは未対応の報告・詰まり・作業中を先に出します。キーワード・PJ・担当・状態などで絞り込むと残りを見られます。</p>`
	}
	body += `<div class=tscroll>` + table([]string{"タスク", "PJ", "種別", "優先度", "期日", "題(先頭 80 文字)", "ラベル", "状態", "担当", "親 / 子",
		"報告(種別ごとの件数)", "成果物", "報告済み", "作成", "更新", "目印"}, out) + "</div>"
	body += fmt.Sprintf(`<p class=note>最大 %d 件。既定は未完了(閉じたもの = %s を除く)だけ。`, taskListMax, closedLabel) +
		"「報告済み」は tb report で指揮官に届いたもの。未対応は人がまだ「対応済み」にしていない question / blocked。" +
		"ラベルを押すとそのラベルで絞り込みます。期日は tb add / update --due で付けます(期日なしは期限切れになりません)。" +
		"題は先頭だけ、本文と session_id は詳細画面で見られます。</p>"
	return "タスク", body
}

func (v *sv) hasSprints() bool {
	return len(v.q("SELECT name FROM sqlite_master WHERE name='sprints'")) > 0 && v.hasColumn("tasks", "sprint_id")
}

func (s *Server) pageBoard(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true // 入力欄があるので自動更新しない
	v := q.data()
	if !v.hasSprints() {
		return "ボード", doneNotice(c) + editErr(c) + `<p class=note>スプリントの表がまだありません。下のフォームで作るか、手元で <code>tb sprints</code> を一度実行すると作られます。</p>` +
			v.sprintForm(c)
	}
	proj, sid, label := runesN(c.one("project"), 100), c.one("sprint"), c.one("label")
	var errs []string
	if sid != "" && !reSprint.MatchString(sid) {
		errs = append(errs, "スプリントは S-<番号> です")
		sid = ""
	}
	if label != "" && !reLabel.MatchString(label) {
		errs = append(errs, "ラベルは 40 文字までで、空白・カンマ・引用符を含みません")
		label = ""
	}
	sq := "SELECT s.*, COALESCE(SUM(t.id IS NOT NULL AND t.status NOT IN (__CANCELLED_STATUSES__)),0) AS n, SUM(t.status IN (__DONE_STATUSES__)) AS done_n," +
		" COALESCE(SUM(CASE WHEN t.status NOT IN (__CANCELLED_STATUSES__) THEN t.points END),0) AS pts," +
		" COALESCE(SUM(CASE WHEN t.status IN (__DONE_STATUSES__) THEN t.points END),0) AS done_pts" +
		" FROM sprints s LEFT JOIN tasks t ON t.sprint_id=s.id" + ifs(proj != "", " WHERE s.project=?") +
		" GROUP BY s.id ORDER BY CASE s.state WHEN 'active' THEN 1 WHEN 'planned' THEN 2 ELSE 3 END, s.project, s.id DESC"
	var sprints []row
	if proj != "" {
		sprints = v.q(sq, proj)
	} else {
		sprints = v.q(sq)
	}
	var cur row
	if sid != "" {
		for _, x := range sprints {
			if "S-"+pyStr(x["id"]) == sid {
				cur = x
				break
			}
		}
		if cur == nil {
			errs = append(errs, "スプリント "+sid+" が見つかりません")
			sid = ""
		}
	}
	if cur == nil {
		for _, x := range sprints {
			if x["state"] == "active" {
				cur = x
				break
			}
		}
	}
	body := doneNotice(c) + editErr(c)
	if len(errs) > 0 {
		body += errHTML(strings.Join(errs, "。"))
	}
	var projects []string
	for _, r := range v.q("SELECT DISTINCT project FROM sprints ORDER BY project") {
		projects = append(projects, pyStr(r["project"]))
	}
	popts := `<option value="">(すべて)</option>`
	for _, p := range projects {
		popts += `<option value="` + E(p) + `"` + sel(p == proj, " selected") + `>` + E(p) + `</option>`
	}
	sopts := ""
	for _, x := range sprints {
		id := strconv.FormatInt(intOf(x["id"]), 10)
		sopts += `<option value="S-` + id + `"` + sel(cur != nil && intOf(x["id"]) == intOf(cur["id"]), " selected") + `>` +
			`S-` + id + ` ` + ev(x["project"]) + ` / ` + ev(x["name"]) + ` [` + ev(x["state"]) + `]</option>`
	}
	body += `<form method=get action="/board"><p>` +
		`<label>PJ <select name=project>` + popts + `</select></label> ` +
		`<label>スプリント <select name=sprint><option value="">(active なもの)</option>` + sopts + `</select></label> ` +
		`<label>ラベル <input type=text name=label value="` + E(label) + `" maxlength=40 size=12></label> ` +
		`<button type=submit>表示</button> <a href="/board">解除</a></p></form>`
	dash := func(x any) string {
		if truthy(x) {
			return ev(x)
		}
		return "-"
	}
	if cur == nil {
		body += "<p>表示するスプリントがありません(active なものが無いか、まだ作っていません)。手元で <code>tb sprint-add</code> / <code>tb sprint-start</code>。</p>"
	} else {
		cid := intOf(cur["id"])
		cs := strconv.FormatInt(cid, 10)
		body += `<h2>S-` + cs + ` ` + ev(cur["name"]) + ` <span class=note>[` + ev(cur["state"]) + `] ` + dash(cur["start_date"]) + ` 〜 ` + dash(cur["end_date"]) +
			`・` + ev(cur["project"]) + `</span></h2>`
		if truthy(cur["goal"]) {
			body += "<p>目標: " + ev(cur["goal"]) + "</p>"
		}
		body += fmt.Sprintf(`<p>完了 %d / %d 件・ポイント %d / %d</p>`, intOf(cur["done_n"]), intOf(cur["n"]), intOf(cur["done_pts"]), intOf(cur["pts"]))
		rows := v.q("SELECT * FROM tasks WHERE sprint_id=? AND (?='' OR EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?))"+
			" ORDER BY CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END, id"+
			" LIMIT 300", cid, label, label)
		card := func(t row) string {
			pts := ""
			if t["points"] != nil {
				pts = fmt.Sprintf(` <span class=note>%dpt</span>`, intOf(t["points"]))
			}
			typ := ""
			if truthy(t["type"]) {
				typ = pyStr(t["type"])
			}
			return `<div class=card>` + tlink(t["id"]) + ` ` + priorityHTML(t["priority"]) + ` <span class=note>` + E(typ) + `</span>` + pts + `<br>` +
				E(head(t["title"], titleMax)) + `<br><span class=note>` + E(orStr(truthyStr(t["owner_agent"]), "-")) + `</span> ` +
				labelsHTML(t["labels"], func(l string) string {
					return "/board?" + pyURLEncode([][2]string{{"sprint", "S-" + cs}, {"label", l}})
				}) + `</div>`
		}
		bcols := v.boardColumns()
		var extra []string
		for _, t := range rows {
			if st := pyStr(t["status"]); !contains(bcols, st) && !contains(extra, st) {
				extra = append(extra, st)
			}
		}
		sort.Strings(extra)
		bcols = append(bcols, extra...)
		var heads []string
		var cells []string
		for _, col := range bcols {
			var cs []string
			for _, t := range rows {
				if pyStr(t["status"]) == col {
					cs = append(cs, card(t))
				}
			}
			heads = append(heads, E(col)+" ("+strconv.Itoa(len(cs))+")")
			cells = append(cells, orStr(strings.Join(cs, ""), "-"))
		}
		body += table(heads, [][]string{cells})
		body += "<p class=note>カードの状態はタスク画面の「状態を変える」で変えられます。スプリントへの出し入れは手元で " +
			"<code>tb update &lt;id&gt; --sprint S-n</code>。</p>"
	}
	var srows [][]string
	for _, x := range sprints {
		id := strconv.FormatInt(intOf(x["id"]), 10)
		srows = append(srows, []string{`<a href="/board?sprint=S-` + id + `">S-` + id + `</a>`, ev(x["project"]), ev(x["name"]), ev(x["state"]),
			dash(x["start_date"]) + ` 〜 ` + dash(x["end_date"]), fmt.Sprintf("%d / %d", intOf(x["done_n"]), intOf(x["n"])),
			fmt.Sprintf("%d / %d", intOf(x["done_pts"]), intOf(x["pts"])), sprintOps(x, sprints, c.csrf)})
	}
	body += "<h2>スプリント一覧</h2>" + table([]string{"スプリント", "PJ", "名前", "状態", "期間", "完了 / 件数", "ポイント", "操作"}, srows)
	return "ボード", body + v.sprintForm(c)
}
