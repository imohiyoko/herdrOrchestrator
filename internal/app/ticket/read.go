package ticket

// チケットの読むだけの操作(tb tree / labels / sprints / board / history / events)。Text は tb の出力そのまま(行ごとに改行つき)。
// 出し方は bash と同じ(sqlite3 の -separator と -json を jq で整えたもの)。

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// TextResult は読むだけの操作の結果。Text は tb が標準出力に出す文字列。
type TextResult struct {
	Text string `api:"text" json:"text"`
}

// TreeReq は ticket.tree の入力。ID か Project のどちらか(両方なら ID)。
type TreeReq struct {
	ID      string `api:"id,optional"`
	Project string `api:"project,optional"`
}

// LabelsReq は ticket.labels の入力。Defined なら設定 labels で用意したラベルだけ(説明つき)。
type LabelsReq struct {
	Project string `api:"project,optional"`
	Defined bool   `api:"defined,optional"`
	JSON    bool   `api:"json,optional"`
}

// SprintsReq は ticket.sprints の入力。State は planned / active / closed。
type SprintsReq struct {
	Project string `api:"project,optional"`
	State   string `api:"state,optional"`
	JSON    bool   `api:"json,optional"`
}

// BoardReq は ticket.board の入力。Sprint(S-<n>)が空なら Project の active なスプリント。Labels はカンマ区切り(全部を持つもの)。
type BoardReq struct {
	Sprint  string `api:"sprint,optional"`
	Project string `api:"project,optional"`
	Labels  string `api:"labels,optional"`
	JSON    bool   `api:"json,optional"`
}

// HistoryReq は ticket.history の入力。
type HistoryReq struct {
	ID   string `api:"id,path"`
	JSON bool   `api:"json,optional"`
}

// EventsReq は ticket.events の入力。Since は E-<n>(その後ろ)。Limit は 1〜999999(0 なら 50)。
type EventsReq struct {
	Since   string `api:"since,optional"`
	Kind    string `api:"kind,optional"`
	Project string `api:"project,optional"`
	Limit   int    `api:"limit,optional"`
	JSON    bool   `api:"json,optional"`
}

var (
	reSprint  = regexp.MustCompile(`^S-[0-9]+$`)
	reEventID = regexp.MustCompile(`^E-([0-9]{1,18})$`)
	reKind    = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

func badArg(format string, a ...any) error {
	return define.Errorf(define.CodeInvalidArgument, format, a...)
}

func readOps(env *app.Env) []app.Op {
	return []app.Op{
		readOp(env, "ticket.tree", "cmd:ticket:tree", "GET /v1/tickets/tree", "チケットの木(tb tree)", tree, define.Resource("ticket", "id")),
		readOp(env, "ticket.labels", "cmd:ticket:labels", "GET /v1/tickets/labels", "ラベルごとの数(tb labels)", labelsText),
		readOp(env, "ticket.sprints", "cmd:ticket:sprints", "GET /v1/sprints", "スプリントの一覧と進み(tb sprints)", sprints),
		readOp(env, "ticket.board", "cmd:ticket:board", "GET /v1/sprints/board", "スプリントのボード(tb board)", board),
		readOp(env, "ticket.history", "cmd:ticket:history", "GET /v1/tickets/{id}/history", "チケット 1 件の履歴(tb history)", history, define.Resource("ticket", "id")),
		readOp(env, "ticket.events", "cmd:ticket:events", "GET /v1/ticket-events", "チケットの履歴の最新 N 件を古い順に(tb events)", eventsText),
	}
}

// readOp は読むだけの操作(門は通すだけ。入力は f の中で確かめる)。res は Authz の対象(ticket の ID など)。
func readOp[In any](env *app.Env, name, authz, route, doc string, f func(*store.DB, tickets.StatusSet, In) (string, error), res ...define.ResourceOption) app.Op {
	return app.Query(env, name,
		func(_ context.Context, _ *app.Env, _ string, _ any) app.Decision { return app.Decision{} },
		func(_ context.Context, in In) (TextResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return TextResult{}, internal(err)
			}
			defer done()
			s, err := f(db, tickets.LoadStatuses(db.DB, db.Session), in)
			if err != nil {
				if _, ok := err.(*define.Error); !ok {
					err = internal(err)
				}
				return TextResult{}, err
			}
			return TextResult{Text: s}, nil
		},
		define.Doc(doc),
		define.Authz(authz, res...),
		define.Expose(define.Internal, define.REST),
		define.HTTP(route),
	)
}

// tsv は sql -separator $'\t'(NULL は空)。
func tsv(b *strings.Builder, db *sql.DB, q string, args ...any) error {
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
		s := make([]string, len(vals))
		for i, v := range vals {
			s[i] = tickets.Text(v)
		}
		b.WriteString(strings.Join(s, "\t") + "\n")
	}
	return rows.Err()
}

func compact(b *strings.Builder, os []jv.Obj) {
	for _, o := range os {
		b.WriteString(jv.Compact(o) + "\n")
	}
}

func requireTask(db *store.DB, id string) error {
	ok, err := tickets.Exists(db.DB, id)
	if err != nil {
		return err
	}
	if !ok {
		return define.Errorf(define.CodeNotFound, "タスクが見つかりません: %s", id)
	}
	return nil
}

func tree(db *store.DB, _ tickets.StatusSet, in TreeReq) (string, error) {
	if in.ID == "" && in.Project == "" {
		return "", badArg("tree <id> | tree -p <project>")
	}
	seed, arg := "parent_id IS NULL AND project=?", in.Project
	if in.ID != "" {
		if !ReID.MatchString(in.ID) {
			return "", badArg("タスク ID は T-<project>-NNN 形式: %s", in.ID)
		}
		if err := requireTask(db, in.ID); err != nil {
			return "", err
		}
		seed, arg = "id=?", in.ID
	}
	var b strings.Builder
	err := tsv(&b, db.DB, `WITH RECURSIVE t(id, d, path) AS (
      SELECT id, 0, id FROM tasks WHERE `+seed+`
      UNION ALL SELECT c.id, t.d+1, t.path||'/'||c.id FROM tasks c JOIN t ON c.parent_id=t.id WHERE t.d < 20)
    SELECT substr('                                        ', 1, t.d*2)||x.id, x.type, x.priority, x.status,
           COALESCE(x.owner_agent,'-'), replace(x.title, char(10), ' ')
    FROM t JOIN tasks x ON x.id=t.id ORDER BY t.path`, arg)
	return b.String(), err
}

func labelsText(db *store.DB, st tickets.StatusSet, in LabelsReq) (string, error) {
	where, qa := "1=1", []any{}
	if in.Project != "" {
		where += " AND t.project=?"
		qa = append(qa, in.Project)
	}
	closed := st.SQLList("done", "cancelled")
	var q string
	if in.Defined {
		defs, ok := tickets.DefinedLabels(db)
		if !ok {
			return "", define.Errorf(define.CodeFailedPrecondition, "用意したラベルはありません(設定 labels が無いので、ラベルは自由に付けられます。用意するのは人: tb config edit labels)")
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
	var b strings.Builder
	if in.JSON {
		os, err := objs(db.DB, nil, q, qa...)
		if err != nil {
			return "", err
		}
		compact(&b, os)
		return b.String(), nil
	}
	err := tsv(&b, db.DB, q, qa...)
	return b.String(), err
}

func sprints(db *store.DB, st tickets.StatusSet, in SprintsReq) (string, error) {
	where, qa := "1=1", []any{}
	if in.Project != "" {
		where += " AND s.project=?"
		qa = append(qa, in.Project)
	}
	if in.State != "" {
		if !slices.Contains([]string{"planned", "active", "closed"}, in.State) {
			return "", badArg("state は planned / active / closed のどれか: %s", in.State)
		}
		where += " AND s.state=?"
		qa = append(qa, in.State)
	}
	cancelled, done := st.SQLList("cancelled"), st.SQLList("done")
	q := `SELECT 'S-'||s.id AS id, s.state, s.project, s.name, s.start_date, s.end_date, s.goal,
              COALESCE(SUM(t.id IS NOT NULL AND t.status NOT IN (` + cancelled + `)),0) AS tasks, SUM(t.status IN (` + done + `)) AS done,
              COALESCE(SUM(CASE WHEN t.status NOT IN (` + cancelled + `) THEN t.points END),0) AS points, COALESCE(SUM(CASE WHEN t.status IN (` + done + `) THEN t.points END),0) AS done_points
            FROM sprints s LEFT JOIN tasks t ON t.sprint_id=s.id WHERE (` + where + `)
            GROUP BY s.id ORDER BY s.project, CASE s.state WHEN 'active' THEN 1 WHEN 'planned' THEN 2 ELSE 3 END, s.id`
	var b strings.Builder
	if in.JSON {
		os, err := objs(db.DB, nil, q, qa...)
		if err != nil {
			return "", err
		}
		for i, o := range os {
			if v := o.Get("done"); v == nil || v == false {
				os[i] = o.Set("done", jv.Number("0"))
			}
		}
		compact(&b, os)
		return b.String(), nil
	}
	err := tsv(&b, db.DB, `SELECT id, state, project, name, COALESCE(start_date,'-')||'..'||COALESCE(end_date,'-'),
         COALESCE(done,0)||'/'||tasks, done_points||'/'||points FROM (`+q+`)`, qa...)
	return b.String(), err
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

func board(db *store.DB, st tickets.StatusSet, in BoardReq) (string, error) {
	sid := in.Sprint
	where, qa := "", []any{}
	if in.Labels != "" {
		for _, l := range strings.Split(in.Labels, ",") {
			if l = strings.TrimSpace(l); l == "" {
				continue
			}
			where += " AND EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)"
			qa = append(qa, l)
		}
	}
	if sid != "" && !reSprint.MatchString(sid) {
		return "", badArg("スプリント ID は S-<番号> 形式: %s", sid)
	}
	if sid == "" {
		if in.Project == "" {
			return "", badArg("board S-<n> | board -p <project>")
		}
		_ = db.DB.QueryRow("SELECT 'S-'||id FROM sprints WHERE project=? AND state='active'", in.Project).Scan(&sid)
		if sid == "" {
			return "", define.Errorf(define.CodeFailedPrecondition, "project %s に active なスプリントがありません(tb sprints -p %s)", in.Project, in.Project)
		}
	}
	srows, err := objs(db.DB, nil, "SELECT * FROM sprints WHERE id=?", strings.TrimPrefix(sid, "S-"))
	if err != nil {
		return "", err
	}
	if len(srows) == 0 {
		return "", define.Errorf(define.CodeNotFound, "スプリントが見つかりません: %s", sid)
	}
	row := srows[0]
	q := `SELECT id, status, type, priority, COALESCE(owner_agent,'-') AS owner, points, labels, title FROM tasks
            WHERE sprint_id=` + strings.TrimPrefix(sid, "S-") + where + ` ORDER BY ` + tickets.PriorityRank + `, id`
	cols := boardCols(st)
	ds, err := objs(db.DB, nil, "SELECT DISTINCT status FROM ("+q+") ORDER BY status", qa...)
	if err != nil {
		return "", err
	}
	for _, d := range ds {
		if c, _ := d.Get("status").(string); c != "" && !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	var b strings.Builder
	if in.JSON {
		ts, err := objs(db.DB, []string{"labels"}, q, qa...)
		if err != nil {
			return "", err
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
		b.WriteString(jv.Compact(jv.Obj{{K: "sprint", V: sp}, {K: "columns", V: cs}}) + "\n")
		return b.String(), nil
	}
	or := func(k string) string {
		if v := row.Get(k); v != nil && v != false {
			return jv.Scalar(v)
		}
		return "-"
	}
	fmt.Fprintf(&b, "%s %s [%s] %s..%s\n", sid, jv.Scalar(row.Get("name")), jv.Scalar(row.Get("state")), or("start_date"), or("end_date"))
	if g := row.Get("goal"); g != nil && g != false {
		fmt.Fprintf(&b, "目標: %s\n", jv.Scalar(g))
	}
	for _, c := range cols {
		fmt.Fprintf(&b, "\n== %s ==\n", c)
		if err := tsv(&b, db.DB, "SELECT id, priority, owner, COALESCE(points,'-'), replace(title, char(10), ' ') FROM ("+q+") WHERE status=?", append(append([]any{}, qa...), c)...); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

const eventCols = "'E-'||id AS id, task_id, kind, old, new, actor, ref, created_at"

// eventsOut は events_out: このセッションの履歴(task_events には一時ビューが無いので herdr_session で絞る)。
func eventsOut(db *store.DB, js bool, cond string, args ...any) (string, error) {
	where := "herdr_session=? AND (" + cond + ")"
	args = append([]any{db.Session}, args...)
	var b strings.Builder
	if js {
		os, err := objs(db.DB, nil, "SELECT "+eventCols+" FROM main.task_events WHERE "+where+" ORDER BY main.task_events.id", args...)
		if err != nil {
			return "", err
		}
		compact(&b, os)
		return b.String(), nil
	}
	err := tsv(&b, db.DB, `SELECT 'E-'||id, created_at, task_id, kind, COALESCE(replace(old, char(10), ' '),'-'), COALESCE(replace(new, char(10), ' '),'-'), actor, COALESCE(ref,'-')
         FROM main.task_events WHERE `+where+" ORDER BY main.task_events.id", args...)
	return b.String(), err
}

func history(db *store.DB, _ tickets.StatusSet, in HistoryReq) (string, error) {
	if !ReID.MatchString(in.ID) {
		return "", badArg("タスク ID は T-<project>-NNN 形式: %s", in.ID)
	}
	if err := requireTask(db, in.ID); err != nil {
		return "", err
	}
	return eventsOut(db, in.JSON, "task_id=?", in.ID)
}

func eventsText(db *store.DB, _ tickets.StatusSet, in EventsReq) (string, error) {
	where, qa := "1=1", []any{}
	if in.Since != "" {
		m := reEventID.FindStringSubmatch(in.Since)
		if m == nil {
			return "", badArg("events: --since は E-<数字>: %s", in.Since)
		}
		where += " AND id > " + m[1]
	}
	if in.Kind != "" {
		if !reKind.MatchString(in.Kind) {
			return "", badArg("events: --kind が不正です: %s", in.Kind)
		}
		where += " AND kind=?"
		qa = append(qa, in.Kind)
	}
	if in.Project != "" {
		if !reProject.MatchString(in.Project) {
			return "", badArg("project は英数字と _ のみ: %s", in.Project)
		}
		where += " AND task_id LIKE ?"
		qa = append(qa, "T-"+in.Project+"-%")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 999999 {
		return "", badArg("events: --limit は 1〜999999: %d", in.Limit)
	}
	return eventsOut(db, in.JSON, "id IN (SELECT id FROM main.task_events WHERE herdr_session=? AND "+where+" ORDER BY id DESC LIMIT "+strconv.Itoa(limit)+")",
		append([]any{db.Session}, qa...)...)
}
