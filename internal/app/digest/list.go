package digest

// まとめの一覧と 1 件(tb digests / tb digests D-<n>)。Text は tb の出力そのまま。

import (
	"context"
	"fmt"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ListReq は digest.list の入力。ID(D-<n> の n)があればその 1 件。無ければ新しい順に 50 件。
type ListReq struct {
	ID   int64 `api:"id,optional"`
	JSON bool  `api:"json,optional"`
}

// ListResult は tb digests の出力。
type ListResult struct {
	Text string `api:"text" json:"text"`
}

func listOp(env *app.Env) app.Op {
	return app.Query(env, "digest.list",
		func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} },
		func(_ context.Context, in ListReq) (ListResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return ListResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			s, err := listText(db, in)
			if err != nil {
				if _, ok := err.(*define.Error); !ok {
					err = define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				return ListResult{}, err
			}
			return ListResult{Text: s}, nil
		},
		define.Doc("まとめの一覧(tb digests。新しい順に 50 件)か 1 件の全文"),
		define.Authz("cmd:digest:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/digests"),
	)
}

// str は jq の文字列埋め込み \(.x) の値(null は "null")。
func str(v any) string {
	if v == nil {
		return "null"
	}
	return jv.Scalar(v)
}

func listText(db *store.DB, in ListReq) (string, error) {
	withDid := func(o jv.Obj) jv.Obj { return o.Set("id", "D-"+jv.Scalar(o.Get("id"))) }
	var b strings.Builder
	if in.ID != 0 {
		rows, err := rowfmt.Objs(db.DB, `SELECT * FROM digests WHERE id=?`, in.ID)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 {
			return "", define.Errorf(define.CodeNotFound, "digest が見つかりません: D-%d", in.ID)
		}
		o := rows[0]
		if in.JSON {
			b.WriteString(jv.Compact(withDid(o)) + "\n")
		} else {
			fmt.Fprintf(&b, "D-%s → %s (%s 件, %s)\n%s\n", str(o.Get("id")), str(o.Get("target")), str(o.Get("item_count")), str(o.Get("created_at")), str(o.Get("body")))
		}
		return b.String(), nil
	}
	if in.JSON {
		rows, err := rowfmt.Objs(db.DB, `SELECT * FROM digests ORDER BY id DESC LIMIT 50`)
		if err != nil {
			return "", err
		}
		for _, o := range rows {
			b.WriteString(jv.Compact(withDid(o)) + "\n")
		}
		return b.String(), nil
	}
	err := store.Each(db.DB, `SELECT 'D-'||id, target, item_count, created_at FROM digests ORDER BY id DESC LIMIT 50`, nil, func(_ []string, vals []any) error {
		r := make([]string, len(vals))
		for i, v := range vals {
			r[i] = tickets.Text(v)
		}
		b.WriteString(strings.Join(r, "\t") + "\n")
		return nil
	})
	return b.String(), err
}
