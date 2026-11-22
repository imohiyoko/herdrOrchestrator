package tbcli

// チケットの読むだけのコマンド(taskboard/lib/tasks.sh の show / tree / labels / sprints / board / history / events)。
// 出し方は bash と同じ(sqlite3 の -separator と -json を jq で整えたもの)。

import (
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/activity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	reEventID = regexp.MustCompile(`^E-([0-9]{1,18})$`)
	reKind    = regexp.MustCompile(`^[a-z_]{1,32}$`)
	reEvLimit = regexp.MustCompile(`^[0-9]{1,6}$`)
)

func needID(v string) error {
	if !reID.MatchString(v) {
		return usage("タスク ID は T-<project>-NNN 形式: %s", v)
	}
	return nil
}

func needProject(v string) error {
	if !reProject.MatchString(v) {
		return usage("project は英数字と _ のみ: %s", v)
	}
	return nil
}

func needSprint(v string) error {
	if !reSprint.MatchString(v) {
		return usage("スプリント ID は S-<番号> 形式: %s", v)
	}
	return nil
}

// argv は need_arg のある引数の読み方(値が無ければ「<名前> に値が必要です」)。
func argv(a []string, i int) (string, error) {
	if i+1 >= len(a) {
		return "", usage("%s に値が必要です", a[i])
	}
	return a[i+1], nil
}

// qrows は行ごとに cols と vals を渡す。
func qrows(db *sql.DB, q string, args []any, f func(cols []string, vals []any) error) error {
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := f(cols, vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

// sqlTSV は sql -separator $'\t'(NULL は空)。
func sqlTSV(db *sql.DB, w io.Writer, q string, args ...any) error {
	return qrows(db, q, args, func(_ []string, vals []any) error {
		s := make([]string, len(vals))
		for i, v := range vals {
			s[i] = tickets.Text(v)
		}
		fmt.Fprintln(w, strings.Join(s, "\t"))
		return nil
	})
}

// jvValue は sqlite3 -json の 1 つの値。
func jvValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		return jv.Number(tickets.Text(x))
	case float64:
		return jv.Number(tickets.Text(x))
	default:
		return tickets.Text(v)
	}
}

// sqlObjs は sql -json の行(列の順のまま)。fromjson の列は JSON として読む。
func sqlObjs(db *sql.DB, fromjson []string, q string, args ...any) ([]jv.Obj, error) {
	var out []jv.Obj
	err := qrows(db, q, args, func(cols []string, vals []any) error {
		o := jv.Obj{}
		for i, c := range cols {
			v := jvValue(vals[i])
			if slices.Contains(fromjson, c) {
				s, _ := v.(string)
				vs, err := jv.DecodeAll([]byte(s))
				if err != nil || len(vs) != 1 {
					return fmt.Errorf("jq: error: %s を JSON として読めません", c)
				}
				v = vs[0]
			}
			o = append(o, jv.KV{K: c, V: v})
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func compactRows(w io.Writer, os []jv.Obj) {
	for _, o := range os {
		fmt.Fprintln(w, jv.Compact(o))
	}
}

func requireTask(db *store.DB, id string) error {
	ok, err := tickets.Exists(db.DB, id)
	if err != nil {
		return err
	}
	if !ok {
		return die("タスクが見つかりません: %s", id)
	}
	return nil
}

var ticketJSONCols = []string{"deliverables", "labels", "refs"}

// cmdShow は tb show <id>: 1 件(担当 agent の最終活動とリンクつき)。
func cmdShow(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) != 1 {
		return usage("show <id>")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	rows, err := sqlObjs(db.DB, ticketJSONCols, "SELECT * FROM tasks WHERE id=?", id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return die("タスクが見つかりません: %s", id)
	}
	row := rows[0]
	var act any
	if sid, _ := row.Get("session_id").(string); sid != "" {
		as, err := sqlObjs(db.DB, nil, activity.SelectSQL("10")+" WHERE session_id=?", sid)
		if err != nil {
			return err
		}
		if len(as) > 0 {
			act = as[0]
		}
	}
	links, err := taskLinks(db.DB, id)
	if err != nil {
		return err
	}
	row = row.Set("activity", act).Set("links", links)
	fmt.Fprintln(w, jv.Pretty(row))
	return nil
}

// taskLinks は task_links_json: 向きつきのリンクを rel ごとに(rel の名前の順)。
func taskLinks(db *sql.DB, id string) (jv.Obj, error) {
	ls, err := sqlObjs(db, nil, `SELECT CASE WHEN l.from_id=?1 THEN l.link_type
                         WHEN l.link_type='blocks' THEN 'blocked_by'
                         WHEN l.link_type='duplicates' THEN 'duplicated_by' ELSE 'relates' END AS rel,
                    t.id, t.title, t.status
             FROM task_links l JOIN tasks t ON t.id = CASE WHEN l.from_id=?1 THEN l.to_id ELSE l.from_id END
             WHERE l.from_id=?1 OR l.to_id=?1 ORDER BY l.id`, id)
	if err != nil {
		return nil, err
	}
	groups := map[string][]any{}
	var rels []string
	for _, l := range ls {
		r := jv.Scalar(l.Get("rel"))
		if _, ok := groups[r]; !ok {
			rels = append(rels, r)
		}
		groups[r] = append(groups[r], l[1:])
	}
	slices.Sort(rels)
	out := jv.Obj{}
	for _, r := range rels {
		out = append(out, jv.KV{K: r, V: groups[r]})
	}
	return out, nil
}

// cmdTree は tb tree <id> | tree -p <project>。
func cmdTree(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	root, project := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--project":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			project = v
			i++
		case strings.HasPrefix(a, "-"):
			return usage("tree: 不明な引数 %s", a)
		default:
			if root != "" {
				return usage("tree <id> | tree -p <project>")
			}
			if err := needID(a); err != nil {
				return err
			}
			root = a
		}
	}
	if root == "" && project == "" {
		return usage("tree <id> | tree -p <project>")
	}
	seed, arg := "parent_id IS NULL AND project=?", project
	if root != "" {
		if err := requireTask(db, root); err != nil {
			return err
		}
		seed, arg = "id=?", root
	}
	return sqlTSV(db.DB, w, `WITH RECURSIVE t(id, d, path) AS (
      SELECT id, 0, id FROM tasks WHERE `+seed+`
      UNION ALL SELECT c.id, t.d+1, t.path||'/'||c.id FROM tasks c JOIN t ON c.parent_id=t.id WHERE t.d < 20)
    SELECT substr('                                        ', 1, t.d*2)||x.id, x.type, x.priority, x.status,
           COALESCE(x.owner_agent,'-'), replace(x.title, char(10), ' ')
    FROM t JOIN tasks x ON x.id=t.id ORDER BY t.path`, arg)
}

// definedLabels は defined_labels: 設定 labels の「ラベル, 説明」(書いた順)。設定が無ければ ok=false。
func definedLabels(db *store.DB) ([][2]string, bool) {
	body, _, ok := config.Lookup(db.DB, db.Session, "labels")
	if !ok {
		return nil, false
	}
	var out [][2]string
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = config.Trim(line)
		if line == "" {
			continue
		}
		k, v, has := strings.Cut(line, "=")
		k = config.Trim(k)
		if has {
			v = config.Trim(v)
		}
		out = append(out, [2]string{k, v})
	}
	return out, true
}

// cmdLabels は tb labels [-p project] [--defined] [--json]。
func cmdLabels(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	where, qa := "1=1", []any{}
	js, defined := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--project":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			where += " AND t.project=?"
			qa = append(qa, v)
			i++
		case "--defined":
			defined = true
		case "--json":
			js = true
		default:
			return usage("labels: 不明な引数 %s", args[i])
		}
	}
	closed := st.SQLList("done", "cancelled")
	var q string
	if defined {
		defs, ok := definedLabels(db)
		if !ok {
			return die("用意したラベルはありません(設定 labels が無いので、ラベルは自由に付けられます。用意するのは人: tb config edit labels)")
		}
		arr := []any{}
		for _, d := range defs {
			arr = append(arr, jv.Obj{{K: "label", V: d[0]}, {K: "description", V: d[1]}})
		}
		q = `WITH d AS (SELECT key AS ord, json_extract(value,'$.label') AS label, json_extract(value,'$.description') AS description FROM json_each(?))
              SELECT d.label, COALESCE(SUM(t.id IS NOT NULL AND t.status NOT IN (` + closed + `)), 0) AS open, COUNT(t.id) AS total, d.description
              FROM d LEFT JOIN (SELECT t.id, t.status, j.value AS lv FROM tasks t, json_each(t.labels) j WHERE (` + where + `)) t ON t.lv = d.label
              GROUP BY d.ord ORDER BY d.ord`
		qa = append([]any{jv.Compact(arr)}, qa...)
	} else {
		q = `SELECT j.value AS label, SUM(t.status NOT IN (` + closed + `)) AS open, COUNT(*) AS total
            FROM tasks t, json_each(t.labels) j WHERE (` + where + `) GROUP BY j.value ORDER BY j.value`
	}
	if js {
		os, err := sqlObjs(db.DB, nil, q, qa...)
		if err != nil {
			return err
		}
		compactRows(w, os)
		return nil
	}
	return sqlTSV(db.DB, w, q, qa...)
}

// cmdSprints は tb sprints [-p project] [--state S] [--json]。
func cmdSprints(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	where, qa := "1=1", []any{}
	js := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--project", "--state":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			if args[i] == "--state" {
				if !slices.Contains([]string{"planned", "active", "closed"}, v) {
					return usage("state は planned / active / closed のどれか: %s", v)
				}
				where += " AND s.state=?"
			} else {
				where += " AND s.project=?"
			}
			qa = append(qa, v)
			i++
		case "--json":
			js = true
		default:
			return usage("sprints: 不明な引数 %s", args[i])
		}
	}
	cancelled, done := st.SQLList("cancelled"), st.SQLList("done")
	q := `SELECT 'S-'||s.id AS id, s.state, s.project, s.name, s.start_date, s.end_date, s.goal,
              COALESCE(SUM(t.id IS NOT NULL AND t.status NOT IN (` + cancelled + `)),0) AS tasks, SUM(t.status IN (` + done + `)) AS done,
              COALESCE(SUM(CASE WHEN t.status NOT IN (` + cancelled + `) THEN t.points END),0) AS points, COALESCE(SUM(CASE WHEN t.status IN (` + done + `) THEN t.points END),0) AS done_points
            FROM sprints s LEFT JOIN tasks t ON t.sprint_id=s.id WHERE (` + where + `)
            GROUP BY s.id ORDER BY s.project, CASE s.state WHEN 'active' THEN 1 WHEN 'planned' THEN 2 ELSE 3 END, s.id`
	if js {
		os, err := sqlObjs(db.DB, nil, q, qa...)
		if err != nil {
			return err
		}
		for i, o := range os {
			if v := o.Get("done"); v == nil || v == false {
				os[i] = o.Set("done", jv.Number("0"))
			}
		}
		compactRows(w, os)
		return nil
	}
	return sqlTSV(db.DB, w, `SELECT id, state, project, name, COALESCE(start_date,'-')||'..'||COALESCE(end_date,'-'),
         COALESCE(done,0)||'/'||tasks, done_points||'/'||points FROM (`+q+`)`, qa...)
}

// boardCols は ST_BOARD: 分類の順、同じ分類は書いた順。
func boardCols(st tickets.StatusSet) []string {
	var out []string
	for _, c := range tickets.Categories {
		for _, k := range st.Names {
			if st.Cat(k) == c {
				out = append(out, k)
			}
		}
	}
	return out
}

// cmdBoard は tb board [S-<n> | -p <project>] [-l ラベル] [--json]。
func cmdBoard(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	sid, project, js := "", "", false
	where, qa := "", []any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--project":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			project = v
			i++
		case a == "-l" || a == "--label":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			ls, err := labels(v)
			if err != nil {
				return err
			}
			for _, l := range ls {
				where += " AND EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)"
				qa = append(qa, l)
			}
			i++
		case a == "--json":
			js = true
		case strings.HasPrefix(a, "S-"):
			if err := needSprint(a); err != nil {
				return err
			}
			sid = a
		default:
			return usage("board: 不明な引数 %s", a)
		}
	}
	if sid == "" {
		if project == "" {
			return usage("board S-<n> | board -p <project>")
		}
		_ = db.DB.QueryRow("SELECT 'S-'||id FROM sprints WHERE project=? AND state='active'", project).Scan(&sid)
		if sid == "" {
			return die("project %s に active なスプリントがありません(tb sprints -p %s)", project, project)
		}
	}
	srows, err := sqlObjs(db.DB, nil, "SELECT * FROM sprints WHERE id=?", strings.TrimPrefix(sid, "S-"))
	if err != nil {
		return err
	}
	if len(srows) == 0 {
		return die("スプリントが見つかりません: %s", sid)
	}
	row := srows[0]
	q := `SELECT id, status, type, priority, COALESCE(owner_agent,'-') AS owner, points, labels, title FROM tasks
            WHERE sprint_id=` + strings.TrimPrefix(sid, "S-") + where + ` ORDER BY ` + tickets.PriorityRank + `, id`
	cols := boardCols(st)
	err = qrows(db.DB, "SELECT DISTINCT status FROM ("+q+") ORDER BY status", qa, func(_ []string, v []any) error {
		if c := tickets.Text(v[0]); c != "" && !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if js {
		ts, err := sqlObjs(db.DB, []string{"labels"}, q, qa...)
		if err != nil {
			return err
		}
		sp := jv.Obj{}
		for _, e := range row.Set("id", sid) {
			if e.K != "herdr_session" {
				sp = append(sp, e)
			}
		}
		cs := jv.Obj{}
		for _, c := range cols {
			l := []any{}
			for _, t := range ts {
				if t.Get("status") == c {
					l = append(l, t)
				}
			}
			cs = cs.Set(c, l)
		}
		fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "sprint", V: sp}, {K: "columns", V: cs}}))
		return nil
	}
	or := func(k string) string {
		if v := row.Get(k); v != nil && v != false {
			return jv.Scalar(v)
		}
		return "-"
	}
	fmt.Fprintf(w, "%s %s [%s] %s..%s\n", sid, jv.Scalar(row.Get("name")), jv.Scalar(row.Get("state")), or("start_date"), or("end_date"))
	if g := row.Get("goal"); g != nil && g != false {
		fmt.Fprintf(w, "目標: %s\n", jv.Scalar(g))
	}
	for _, c := range cols {
		fmt.Fprintf(w, "\n== %s ==\n", c)
		if err := sqlTSV(db.DB, w, "SELECT id, priority, owner, COALESCE(points,'-'), replace(title, char(10), ' ') FROM ("+q+") WHERE status=?", append(append([]any{}, qa...), c)...); err != nil {
			return err
		}
	}
	return nil
}

const eventCols = "'E-'||id AS id, task_id, kind, old, new, actor, ref, created_at"

// eventsOut は events_out: このセッションの履歴(task_events には一時ビューが無いので herdr_session で絞る)。
func eventsOut(db *store.DB, w io.Writer, js bool, cond string, args ...any) error {
	where := "herdr_session=? AND (" + cond + ")"
	args = append([]any{db.Session}, args...)
	if js {
		os, err := sqlObjs(db.DB, nil, "SELECT "+eventCols+" FROM main.task_events WHERE "+where+" ORDER BY main.task_events.id", args...)
		if err != nil {
			return err
		}
		compactRows(w, os)
		return nil
	}
	return sqlTSV(db.DB, w, `SELECT 'E-'||id, created_at, task_id, kind, COALESCE(replace(old, char(10), ' '),'-'), COALESCE(replace(new, char(10), ' '),'-'), actor, COALESCE(ref,'-')
         FROM main.task_events WHERE `+where+" ORDER BY main.task_events.id", args...)
}

// cmdHistory は tb history <id> [--json]。
func cmdHistory(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	id, js := "", false
	for _, a := range args {
		switch {
		case a == "--json":
			js = true
		case strings.HasPrefix(a, "-"):
			return usage("history: 不明な引数 %s", a)
		default:
			if id != "" {
				return usage("history <id> [--json]")
			}
			if err := needID(a); err != nil {
				return err
			}
			id = a
		}
	}
	if id == "" {
		return usage("history <id> [--json]")
	}
	if err := requireTask(db, id); err != nil {
		return err
	}
	return eventsOut(db, w, js, "task_id=?", id)
}

// cmdEvents は tb events [--since E-n] [--kind K] [-p project] [--limit N] [--json]: 最新の N 件を古い順に。
func cmdEvents(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	where, qa := "1=1", []any{}
	limit, js := "50", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--since", "--kind", "-p", "--project", "--limit":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			i++
			switch a {
			case "--since":
				m := reEventID.FindStringSubmatch(v)
				if m == nil {
					return usage("events: --since は E-<数字>: %s", v)
				}
				where += " AND id > " + m[1]
			case "--kind":
				if !reKind.MatchString(v) {
					return usage("events: --kind が不正です: %s", v)
				}
				where += " AND kind=?"
				qa = append(qa, v)
			case "--limit":
				n := strings.TrimLeft(v, "0")
				if !reEvLimit.MatchString(v) || n == "" {
					return usage("events: --limit は 1〜999999: %s", v)
				}
				limit = n
			default:
				if err := needProject(v); err != nil {
					return err
				}
				where += " AND task_id LIKE ?"
				qa = append(qa, "T-"+v+"-%")
			}
		case "--json":
			js = true
		default:
			return usage("events: 不明な引数 %s", a)
		}
	}
	return eventsOut(db, w, js, "id IN (SELECT id FROM main.task_events WHERE herdr_session=? AND "+where+" ORDER BY id DESC LIMIT "+limit+")",
		append([]any{db.Session}, qa...)...)
}
