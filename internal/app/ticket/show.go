package ticket

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/activity"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ShowResult は ticket.show の結果。Text は tb show の出力(チケットの行に、担当 agent の最終活動とリンクを足した JSON)。
type ShowResult struct {
	Text string `api:"text" json:"text"`
}

// JSONCols はチケットの行のうち JSON として読む列。
var JSONCols = []string{"deliverables", "labels", "refs"}

func showOp(env *app.Env) app.Op {
	return app.Query(env, "ticket.show",
		func(_ context.Context, _ *app.Env, _ string, v any) app.Decision {
			if id := v.(GetReq).ID; !ReID.MatchString(id) {
				return app.Decision{Err: define.Errorf(define.CodeInvalidArgument, "ID は T-<project>-<3 桁以上の番号>: %s", id)}
			}
			return app.Decision{}
		},
		func(_ context.Context, in GetReq) (ShowResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return ShowResult{}, internal(err)
			}
			defer done()
			s, err := Show(db, in.ID)
			return ShowResult{Text: s}, err
		},
		define.Doc("チケット 1 件の全部(tb show。担当 agent の最終活動とリンクつき)"),
		define.Authz("cmd:ticket:show", define.Resource("ticket", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/tickets/{id}/show"),
	)
}

// Show は tb show の出力(整形した JSON。末尾の改行なし)。無ければ NotFound。
func Show(db *store.DB, id string) (string, error) {
	rows, err := objs(db.DB, JSONCols, "SELECT * FROM tasks WHERE id=?", id)
	if err != nil {
		return "", internal(err)
	}
	if len(rows) == 0 {
		return "", define.Errorf(define.CodeNotFound, "タスクが見つかりません: %s", id)
	}
	row := rows[0]
	var act any
	if sid, _ := row.Get("session_id").(string); sid != "" {
		as, err := objs(db.DB, nil, activity.SelectSQL("10")+" WHERE session_id=?", sid)
		if err != nil {
			return "", internal(err)
		}
		if len(as) > 0 {
			act = as[0]
		}
	}
	links, err := links(db.DB, id)
	if err != nil {
		return "", internal(err)
	}
	return jv.Pretty(row.Set("activity", act).Set("links", links)), nil
}

// links は task_links_json: 向きつきのリンクを rel ごとに(rel の名前の順)。
func links(db *sql.DB, id string) (jv.Obj, error) {
	ls, err := objs(db, nil, `SELECT CASE WHEN l.from_id=?1 THEN l.link_type
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

// objs は sqlite3 -json の行(列の順のまま)。fromjson の列は JSON として読む(tb の sqlObjs と同じ)。
func objs(db *sql.DB, fromjson []string, q string, args ...any) ([]jv.Obj, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []jv.Obj
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
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
			if slices.Contains(fromjson, c) {
				s, _ := v.(string)
				vs, err := jv.DecodeAll([]byte(s))
				if err != nil || len(vs) != 1 {
					return nil, fmt.Errorf("jq: error: %s を JSON として読めません", c)
				}
				v = vs[0]
			}
			o = append(o, jv.KV{K: c, V: v})
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
