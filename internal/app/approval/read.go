package approval

// 承認待ちと記録の読むだけの操作(tb approval list / history)。Text は tb の出力そのまま。

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	japproval "github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ListReq は approval.list の入力。Mine なら呼び手が決められるものだけ(人は全部)。
type ListReq struct {
	Mine bool `api:"mine,optional"`
	JSON bool `api:"json,optional"`
}

// HistoryReq は approval.history の入力。By は依頼者で絞る。Mine なら呼び手の依頼だけ。Limit は 1〜1000(0 なら 50)。
type HistoryReq struct {
	By    string `api:"by,optional"`
	Mine  bool   `api:"mine,optional"`
	Limit int    `api:"limit,optional"`
	JSON  bool   `api:"json,optional"`
}

// TextResult は読むだけの操作の結果。Text は tb が標準出力に出す文字列。
type TextResult struct {
	Text string `api:"text" json:"text"`
}

// reqPending は承認待ちの行(元の行の状態を見る)。
const reqPending = `((r.source='cmd' AND r.status='pending')
  OR (r.source='outbox' AND EXISTS (SELECT 1 FROM outbox o WHERE o.id=r.source_id AND o.status='pending'))
  OR (r.source='agent_ops' AND EXISTS (SELECT 1 FROM agent_ops a WHERE a.id=r.source_id AND a.status='pending')))`

func readOps(env *app.Env) []app.Op {
	pass := func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} }
	list := app.Query(env, "approval.list", pass,
		func(ctx context.Context, in ListReq) (TextResult, error) {
			return withRead(env, func(db *store.DB) (string, error) { return list(db, who(ctx), in) })
		},
		define.Doc("承認待ち Q-<n> の一覧(tb approval list。優先度の高い順、同じなら古い順)"),
		define.Authz("cmd:approval:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/requests"),
	)
	hist := app.Query(env, "approval.history", pass,
		func(ctx context.Context, in HistoryReq) (TextResult, error) {
			return withRead(env, func(db *store.DB) (string, error) { return history(db, who(ctx), in) })
		},
		define.Doc("allow / ask / deny の記録(tb approval history。新しい順。実行の結果は依頼者本人と人にだけ見せる)"),
		define.Authz("cmd:approval:history"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/requests/history"),
	)
	return []app.Op{list, hist}
}

func who(ctx context.Context) string {
	p, _ := define.PrincipalFrom(ctx)
	return app.Who(p)
}

func withRead(env *app.Env, f func(*store.DB) (string, error)) (TextResult, error) {
	db, done, err := env.DB()
	if err != nil {
		return TextResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	defer done()
	s, err := f(db)
	if err != nil {
		if _, ok := err.(*define.Error); !ok {
			err = define.Errorf(define.CodeInternal, "%s", err.Error())
		}
		return TextResult{}, err
	}
	return TextResult{Text: s}, nil
}

// rowsOf は行ごとの値(sqlite3 の値のまま)。
func rowsOf(db *sql.DB, q string, args ...any) ([]string, [][]any, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		out = append(out, vals)
	}
	return cols, out, rows.Err()
}

// jsonRows は sqlite3 -json の行(1 行 1 つ)。
func jsonRows(b *strings.Builder, db *sql.DB, q string, args ...any) error {
	cols, rows, err := rowsOf(db, q, args...)
	if err != nil {
		return err
	}
	for _, vals := range rows {
		o := jv.Obj{}
		for i, c := range cols {
			var v any
			switch x := vals[i].(type) {
			case nil:
			case int64, float64:
				v = jv.Number(tickets.Text(x))
			default:
				v = tickets.Text(x)
			}
			o = append(o, jv.KV{K: c, V: v})
		}
		b.WriteString(jv.Compact(o) + "\n")
	}
	return nil
}

// sprintRows は値を fmt.Sprint でタブ区切りに(dash なら空を - に)。
func sprintRows(b *strings.Builder, db *sql.DB, dash bool, q string, args ...any) error {
	_, rows, err := rowsOf(db, q, args...)
	if err != nil {
		return err
	}
	for _, vals := range rows {
		s := make([]string, len(vals))
		for i, v := range vals {
			if x, ok := v.([]byte); ok {
				v = string(x)
			}
			s[i] = fmt.Sprint(v)
			if dash && s[i] == "" {
				s[i] = "-"
			}
		}
		b.WriteString(strings.Join(s, "\t") + "\n")
	}
	return nil
}

func list(db *store.DB, who string, in ListReq) (string, error) {
	filter := ""
	if in.Mine {
		// 呼んだ agent が approve: で決められるものだけ(人は全部)
		_, rows, err := rowsOf(db.DB, `SELECT r.id FROM requests r WHERE `+reqPending)
		if err != nil {
			return "", err
		}
		// 行を読み終えてから 1 件ずつ見る(読みかけのまま同じ DB に問い合わせると、接続が 1 本のとき止まる)
		var ids []string
		j := japproval.NewJudge(db.DB, db.Session)
		for _, v := range rows {
			id, _ := strconv.ParseInt(fmt.Sprint(v[0]), 10, 64)
			if r, ok, _ := requests.Load(db.DB, id); ok && CanDecide(db, j, r, who) {
				ids = append(ids, strconv.FormatInt(id, 10))
			}
		}
		if len(ids) == 0 {
			ids = []string{"-1"}
		}
		filter = ` AND r.id IN (` + strings.Join(ids, ",") + `)`
	}
	q := `SELECT 'Q-'||r.id AS id, CASE r.priority WHEN 2 THEN 'high' WHEN 0 THEN 'low' ELSE 'normal' END AS priority,
	  r.op, r.requester AS requested_by, r.target, r.created_at, r.detail
	  FROM requests r WHERE ` + reqPending + filter + ` ORDER BY r.priority DESC, r.id`
	var b strings.Builder
	if in.JSON {
		err := jsonRows(&b, db.DB, q)
		return b.String(), err
	}
	err := sprintRows(&b, db.DB, false, `SELECT id, priority, op, requested_by, target, created_at FROM (`+q+`)`)
	return b.String(), err
}

func history(db *store.DB, viewer string, in HistoryReq) (string, error) {
	by, limit := in.By, in.Limit
	if in.Mine {
		by = viewer
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 1000 {
		return "", define.Errorf(define.CodeInvalidArgument, "--limit は 1〜1000")
	}
	// 送信・agent の操作は元の行の状態を見せる。実行の結果(出力の末尾)は、依頼者本人と人にだけ見せる(ほかの agent の出力を読ませない)
	q := `SELECT 'Q-'||r.id AS id, r.tier, CASE r.source
	    WHEN 'outbox' THEN COALESCE((SELECT o.status FROM outbox o WHERE o.id=r.source_id), r.status)
	    WHEN 'agent_ops' THEN COALESCE((SELECT a.status FROM agent_ops a WHERE a.id=r.source_id), r.status)
	    ELSE r.status END AS status,
	  r.requester AS requested_by, r.op, r.target, COALESCE(r.decided_by, '') AS decided_by, COALESCE(r.rule, '') AS rule,
	  r.created_at, COALESCE(r.decided_at, '') AS decided_at, r.detail, CASE WHEN ?='human' OR r.requester=? THEN COALESCE(r.result, '') ELSE '' END AS result
	  FROM requests r WHERE r.herdr_session=? AND (?='' OR r.requester=?) ORDER BY r.id DESC LIMIT ?`
	qa := []any{viewer, viewer, db.Session, by, by, limit}
	var b strings.Builder
	if in.JSON {
		err := jsonRows(&b, db.DB, q, qa...)
		return b.String(), err
	}
	err := sprintRows(&b, db.DB, true, `SELECT id, tier, status, requested_by, op, target, decided_by, created_at FROM (`+q+`)`, qa...)
	return b.String(), err
}
