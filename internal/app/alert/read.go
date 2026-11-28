package alert

// アラートの読むだけの操作(tb alerts と、ほかのコマンドが出す 1 件)。

import (
	"context"
	"database/sql"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// GetReq は alert.get の入力(A-<n> の n)。
type GetReq struct {
	ID int64 `api:"id,path"`
}

// ListReq は alert.list の入力。Status で絞る(空なら全部)。新しい順に 100 件。
type ListReq struct {
	Status string `api:"status,optional"`
	JSON   bool   `api:"json,optional"`
}

// TextResult は読むだけの操作の結果。alert.get は行の JSON(id は数のまま)、alert.list は tb alerts の出力。
type TextResult struct {
	Text string `api:"text" json:"text"`
}

func readOps(env *app.Env) []app.Op {
	pass := func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} }
	get := app.Query(env, "alert.get", pass,
		func(_ context.Context, in GetReq) (TextResult, error) {
			return withDB(env, func(db *store.DB) (TextResult, error) {
				rows, err := objs(db.DB, `SELECT * FROM alerts WHERE id=?`, in.ID)
				if err != nil {
					return TextResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				if len(rows) == 0 {
					return TextResult{}, define.Errorf(define.CodeNotFound, "アラートが見つかりません: A-%d", in.ID)
				}
				return TextResult{Text: jv.Compact(rows[0])}, nil
			})
		},
		define.Doc("アラート 1 件の行(JSON。id は数のまま)"),
		define.Authz("cmd:alert:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/alerts/{id}"),
	)
	list := app.Query(env, "alert.list", pass,
		func(_ context.Context, in ListReq) (TextResult, error) {
			return withDB(env, func(db *store.DB) (TextResult, error) {
				s, err := listText(db, in)
				if err != nil {
					return TextResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				return TextResult{Text: s}, nil
			})
		},
		define.Doc("アラートの一覧(tb alerts。新しい順に 100 件)"),
		define.Authz("cmd:alert:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/alerts"),
	)
	return []app.Op{get, list}
}

// objs は sqlite3 -json の行(列の順のまま)。
func objs(db *sql.DB, q string, args ...any) ([]jv.Obj, error) {
	var out []jv.Obj
	err := store.Each(db, q, args, func(cols []string, vals []any) error {
		o := jv.Obj{}
		for i, c := range cols {
			o = append(o, jv.KV{K: c, V: jvValue(vals[i])})
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func jvValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64, float64:
		return jv.Number(tickets.Text(x))
	default:
		return tickets.Text(v)
	}
}

func listText(db *store.DB, in ListReq) (string, error) {
	where, qa := "1=1", []any{}
	if in.Status != "" {
		where, qa = "status=?", []any{in.Status}
	}
	var b strings.Builder
	if in.JSON {
		rows, err := objs(db.DB, `SELECT * FROM alerts WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa...)
		if err != nil {
			return "", err
		}
		for _, o := range rows {
			b.WriteString(jv.Compact(o.Set("id", "A-"+jv.Scalar(o.Get("id")))) + "\n")
		}
		return b.String(), nil
	}
	err := store.Each(db.DB, `SELECT 'A-'||id, COALESCE(severity,'未解析'), status, count, COALESCE(analyst,'-'), last_seen, title FROM alerts WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa, func(_ []string, vals []any) error {
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				parts[i] = "-"
			} else {
				parts[i] = jv.Scalar(jvValue(v))
			}
		}
		b.WriteString(strings.Join(parts, "\t") + "\n")
		return nil
	})
	return b.String(), err
}
