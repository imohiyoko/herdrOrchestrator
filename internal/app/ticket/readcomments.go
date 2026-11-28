package ticket

// 報告コメントと完了報告、状態の一覧の読むだけの操作(tb comments / pending / ticket status list)。Text は tb の出力そのまま。

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// CommentsReq は ticket.comments の入力。空の項目は絞らない。Open は人の対応待ち(未対応の question / blocked)。
type CommentsReq struct {
	ID          int64  `api:"id,optional"` // C-<n> の n(1 件だけ)
	Task        string `api:"task,optional"`
	Kind        string `api:"kind,optional"`
	Undelivered bool   `api:"undelivered,optional"`
	Open        bool   `api:"open,optional"`
	JSON        bool   `api:"json,optional"`
}

// PendingReq は ticket.pending の入力。Mode は tsv(既定)/ json / ids。
type PendingReq struct {
	Mode string `api:"mode,optional"`
}

// StatusListReq は ticket.status.list の入力。
type StatusListReq struct {
	JSON bool `api:"json,optional"`
}

// StatusListResult は状態の一覧。Source は設定 statuses の出どころ(tb は標準エラーに出す)。
type StatusListResult struct {
	Text   string `api:"text" json:"text"`
	Source string `api:"source" json:"source"`
}

// CommentKinds は報告コメントの種類。
var CommentKinds = []string{"done", "progress", "question", "blocked"}

func readCommentOps(env *app.Env) []app.Op {
	return []app.Op{
		readOp(env, "ticket.comments", "cmd:ticket:comments", "GET /v1/comments", "報告コメントの一覧(tb comments)", comments, define.Resource("ticket", "task")),
		readOp(env, "ticket.pending", "cmd:message:pending", "GET /v1/reports/pending", "未報告の完了と未配送のコメント(tb pending)", pending),
		statusListOp(env),
	}
}

// commentRows は tb comments --json の行(deliverables を JSON に、id を C-<n> に)。
func commentRows(b *strings.Builder, db *store.DB, where string, args ...any) error {
	rows, err := objs(db.DB, []string{"deliverables"}, "SELECT * FROM comments WHERE ("+where+") ORDER BY id", args...)
	if err != nil {
		return err
	}
	for _, r := range rows {
		r = r.Set("id", "C-"+jv.Scalar(r.Get("id")))
		b.WriteString(jv.Compact(r) + "\n")
	}
	return nil
}

func comments(db *store.DB, _ tickets.StatusSet, in CommentsReq) (string, error) {
	where, qa := "1=1", []any{}
	if in.ID != 0 {
		where += " AND id=?"
		qa = append(qa, in.ID)
	}
	if in.Task != "" {
		if !ReID.MatchString(in.Task) {
			return "", badArg("タスク ID は T-<project>-NNN 形式: %s", in.Task)
		}
		where += " AND task_id=?"
		qa = append(qa, in.Task)
	}
	if in.Undelivered {
		where += " AND delivered_at IS NULL"
	}
	if in.Open {
		where += " AND kind IN ('question','blocked') AND resolved_at IS NULL" // 人の対応待ち
	}
	if in.Kind != "" {
		if !slices.Contains(CommentKinds, in.Kind) {
			return "", badArg("comments: --kind は %s", strings.Join(CommentKinds, " "))
		}
		where += " AND kind=?"
		qa = append(qa, in.Kind)
	}
	var b strings.Builder
	if in.JSON {
		err := commentRows(&b, db, where, qa...)
		return b.String(), err
	}
	// needs-human: 未対応の question/blocked(人の判断・承認待ち)。resolved: 人が対応済みにしたもの
	err := tsv(&b, db.DB, `SELECT 'C-'||id, kind, CASE WHEN resolved_at IS NOT NULL THEN 'resolved'
		WHEN kind IN ('question','blocked') THEN 'needs-human' ELSE '-' END,
		CASE WHEN delivered_at IS NULL THEN 'undelivered' ELSE 'delivered' END, task_id, from_name,
		replace(replace(text, char(10), ' '), char(9), ' ') FROM comments WHERE (`+where+`) ORDER BY id`, qa...)
	return b.String(), err
}

func pending(db *store.DB, st tickets.StatusSet, in PendingReq) (string, error) {
	// 未配送の done コメントがあるタスクは C-<n> の行で出す(同じ報告を 2 回送らない)
	where := "status IN (" + st.SQLList("done") + ") AND (reported_at IS NULL OR reported_at='') AND NOT EXISTS (SELECT 1 FROM comments c WHERE c.task_id=tasks.id AND c.kind='done' AND c.delivered_at IS NULL)"
	var b strings.Builder
	switch in.Mode {
	case "ids":
		if err := tsv(&b, db.DB, "SELECT id FROM tasks WHERE ("+where+") ORDER BY updated_at, id"); err != nil {
			return "", err
		}
		err := tsv(&b, db.DB, "SELECT 'C-'||id FROM comments WHERE delivered_at IS NULL ORDER BY id")
		return b.String(), err
	case "json":
		rows, err := objs(db.DB, []string{"deliverables"}, "SELECT * FROM tasks WHERE ("+where+") ORDER BY updated_at, id")
		if err != nil {
			return "", err
		}
		compact(&b, rows)
		err = commentRows(&b, db, "delivered_at IS NULL")
		return b.String(), err
	case "", "tsv":
	default:
		return "", badArg("pending: mode は tsv / json / ids: %s", in.Mode)
	}
	// 宛先は行を読み終えてから引く(接続は 1 本なので、行を読んでいる間に別の問い合わせをすると止まる)
	var out [][3]string
	for _, q := range []string{
		"SELECT id, project, replace(replace(COALESCE(summary,''), char(10), ' '), char(9), ' ') FROM tasks WHERE (" + where + ") ORDER BY updated_at, id",
		// 未配送のコメント: ID は C-<n>(tb report C-<n> で再送)
		`SELECT 'C-'||c.id, COALESCE(t.project,''), '['||c.kind||'] '||substr(replace(replace(c.text, char(10), ' '), char(9), ' '),1,80)
		FROM comments c LEFT JOIN tasks t ON t.id=c.task_id WHERE c.delivered_at IS NULL ORDER BY c.id`,
	} {
		rows, err := db.DB.Query(q)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var a, p, s any
			if err := rows.Scan(&a, &p, &s); err != nil {
				rows.Close()
				return "", err
			}
			out = append(out, [3]string{tickets.Text(a), tickets.Text(p), tickets.Text(s)})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	for _, r := range out {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", r[0], r[1], message.TargetFor(db.DB, db.Session, r[1]), r[2])
	}
	return b.String(), nil
}

// statusDescs は statuses_parse の説明(<状態> <分類> = 説明)。本文が不正なら空(組み込みに戻るので説明も無い)。
func statusDescs(body string) map[string]string {
	out := map[string]string{}
	if _, _, ok := tickets.ParseStatuses(body); !ok {
		return out
	}
	for _, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		k, d, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name, _ := config.FirstWord(config.Trim(k))
		out[name] = config.Trim(d)
	}
	return out
}

func statusListOp(env *app.Env) app.Op {
	return app.Query(env, "ticket.status.list",
		func(_ context.Context, _ *app.Env, _ string, _ any) app.Decision { return app.Decision{} },
		func(_ context.Context, in StatusListReq) (StatusListResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return StatusListResult{}, internal(err)
			}
			defer done()
			return statusList(db, in.JSON)
		},
		define.Doc("チケットの状態の一覧(tb ticket status list。分類・件数・説明つき)"),
		define.Authz("cmd:ticket:status"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/ticket-statuses"),
	)
}

func statusList(db *store.DB, js bool) (StatusListResult, error) {
	st := tickets.LoadStatuses(db.DB, db.Session)
	counts := map[string]int64{}
	var order []string
	rows, err := db.DB.Query("SELECT status, COUNT(*) FROM tasks GROUP BY status")
	if err != nil {
		return StatusListResult{}, internal(err)
	}
	for rows.Next() {
		var k any
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return StatusListResult{}, internal(err)
		}
		counts[tickets.Text(k)] = n
		order = append(order, tickets.Text(k))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return StatusListResult{}, internal(err)
	}
	body, src, _ := config.Lookup(db.DB, db.Session, "statuses")
	descs := statusDescs(body)
	all := boardCols(st)
	for _, k := range order {
		if !slices.Contains(all, k) {
			all = append(all, k)
		}
	}
	var b strings.Builder
	for _, k := range all {
		c, n, d := st.Cat(k), counts[k], descs[k]
		configured := slices.Contains(st.Names, k)
		if js {
			b.WriteString(jv.Compact(jv.Obj{{K: "status", V: k}, {K: "category", V: c}, {K: "description", V: d}, {K: "tickets", V: jv.Number(fmt.Sprint(n))}, {K: "configured", V: configured}}) + "\n")
			continue
		}
		if c == "" {
			c = "-"
		}
		if d == "" && !configured {
			d = "(設定から外した状態)"
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\t%s\n", k, c, n, d)
	}
	return StatusListResult{Text: b.String(), Source: src}, nil
}
