package recipe

// レシピの読むだけの操作(tb recipe-list / recipe-show / recipe-history / recipe-queue)。結果は tb の出力そのもの。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/recipe"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// TextResult は読むだけの操作の結果(tb の出力そのもの)。
type TextResult struct {
	Text string `api:"text" json:"text"`
}

// ListReq は recipe.list の入力。
type ListReq struct {
	JSON bool `api:"json,optional"`
}

// HistoryReq は recipe.history の入力。Name が空なら全部。
type HistoryReq struct {
	Name string `api:"name,optional"`
	JSON bool   `api:"json,optional"`
}

// QueueReq は recipe.queue の入力。All でなければ queued と running だけ。
type QueueReq struct {
	All  bool `api:"all,optional"`
	JSON bool `api:"json,optional"`
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func readOps(env *app.Env) []app.Op {
	pass := func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} }
	text := func(f func(*store.DB) (string, error)) (TextResult, error) {
		return withDB(env, func(db *store.DB) (TextResult, error) {
			s, err := f(db)
			if err != nil {
				return TextResult{}, ierr(err)
			}
			return TextResult{Text: s}, nil
		})
	}
	list := app.Query(env, "recipe.list", pass,
		func(_ context.Context, in ListReq) (TextResult, error) {
			return text(func(db *store.DB) (string, error) { return listText(db, in.JSON) })
		},
		define.Doc("登録されたレシピの一覧(tb recipe-list。危険度は今のコードで検査し直したもの)"),
		define.Authz("cmd:recipe:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/recipes"),
	)
	show := app.Query(env, "recipe.show", pass,
		func(_ context.Context, in NameReq) (TextResult, error) {
			if err := validName(in.Name); err != nil {
				return TextResult{}, err
			}
			return text(func(db *store.DB) (string, error) { return showText(db, in.Name) })
		},
		define.Doc("レシピの中身と各段の部品(tb recipe-show)"),
		define.Authz("cmd:recipe:show", define.Resource("recipe", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/recipes/{name}"),
	)
	history := app.Query(env, "recipe.history", pass,
		func(_ context.Context, in HistoryReq) (TextResult, error) {
			if in.Name != "" {
				if err := validName(in.Name); err != nil {
					return TextResult{}, err
				}
			}
			return text(func(db *store.DB) (string, error) { return historyText(db, in) })
		},
		define.Doc("レシピの登録・書き換え・削除の履歴(tb recipe-history)"),
		define.Authz("cmd:recipe:history"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/recipe-changes"),
	)
	queue := app.Query(env, "recipe.queue", pass,
		func(_ context.Context, in QueueReq) (TextResult, error) {
			return text(func(db *store.DB) (string, error) { return queueText(db, in) })
		},
		define.Doc("レシピの実行の待ち行列 R-<n>(tb recipe-queue。All なら終わったものも)"),
		define.Authz("cmd:recipe:queue"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/recipe-runs"),
	)
	return []app.Op{list, show, history, queue}
}

func compact(rows []jv.Obj) string {
	var b strings.Builder
	for _, o := range rows {
		b.WriteString(jv.Compact(o) + "\n")
	}
	return b.String()
}

func tsv(db *store.DB, q string, args ...any) (string, error) {
	var b strings.Builder
	err := store.Each(db.DB, q, args, func(_ []string, vals []any) error {
		s := make([]string, len(vals))
		for i, v := range vals {
			s[i] = tickets.Text(v)
		}
		b.WriteString(strings.Join(s, "\t") + "\n")
		return nil
	})
	return b.String(), err
}

func listText(db *store.DB, js bool) (string, error) {
	rows, err := rowfmt.Objs(db.DB, `SELECT name, body, risk, steps, updated_by, updated_at FROM recipes ORDER BY name`)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, o := range rows {
		body, _ := o.Get("body").(string)
		risk, desc := "invalid", "(今のコードでは検査を通らない。実行しない)"
		if r, why := recipe.Parse(body); why == "" {
			risk, desc = r.Risk, r.Desc
		}
		if js {
			b.WriteString(jv.Compact(jv.Obj{}.Set("name", o.Get("name")).Set("risk", risk).Set("risk_at_set", o.Get("risk")).
				Set("steps", o.Get("steps")).Set("updated_by", o.Get("updated_by")).Set("updated_at", o.Get("updated_at")).Set("desc", desc)) + "\n")
			continue
		}
		if risk != tickets.Text(o.Get("risk")) {
			risk += "!"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\n", tickets.Text(o.Get("name")), risk, tickets.Text(o.Get("steps")),
			tickets.Text(o.Get("updated_by")), tickets.Text(o.Get("updated_at")), dash(desc))
	}
	return b.String(), nil
}

func showText(db *store.DB, name string) (string, error) {
	var body, by, at string
	if err := db.QueryRow(`SELECT body, updated_by, updated_at FROM recipes WHERE name=?`, name).Scan(&body, &by, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", define.Errorf(define.CodeNotFound, "レシピが登録されていません: %s", name)
		}
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\nop: recipe:%s:<対象>(既定の対象は依頼者自身)\nupdated: %s %s\n", name, name, by, at)
	if r, why := recipe.Parse(body); why == "" {
		fmt.Fprintf(&b, "risk: %s\n", r.Risk)
		for i, s := range r.Steps {
			p, _ := recipe.Lookup(recipe.StepPart(s))
			fmt.Fprintf(&b, "step %d: %s(%s / %s / %s / 操作名 %s)\n", i+1, s, p.Side, p.Risk, p.Runner, dash(p.Ops))
		}
	} else {
		b.WriteString("risk: invalid(今のコードでは検査を通らないため、実行しない)\n")
		for _, l := range strings.Split(why, "\n") {
			fmt.Fprintf(&b, "error: %s\n", l)
		}
	}
	fmt.Fprintf(&b, "---\n%s\n", body)
	return b.String(), nil
}

func historyText(db *store.DB, in HistoryReq) (string, error) {
	where, qa := "1=1", []any{}
	if in.Name != "" {
		where, qa = "name=?", []any{in.Name}
	}
	if in.JSON {
		rows, err := rowfmt.Objs(db.DB, `SELECT id, name, how, old_body, new_body, risk, changed_by, changed_at FROM recipe_changes WHERE `+where+` ORDER BY id`, qa...)
		return compact(rows), err
	}
	return tsv(db, `SELECT changed_at, name, how, COALESCE(risk,'-'), changed_by FROM recipe_changes WHERE `+where+` ORDER BY id`, qa...)
}

func queueText(db *store.DB, in QueueReq) (string, error) {
	where := "status IN ('queued','running')"
	if in.All {
		where = "1=1"
	}
	if in.JSON {
		rows, err := rowfmt.ObjsJSON(db.DB, []string{"plan"}, `SELECT 'R-'||id AS id, status, name, target, requester, via, task_id, plan, 'E-'||event_id AS event, created_at, started_at, finished_at, result FROM recipe_runs WHERE `+where+` ORDER BY recipe_runs.id`)
		return compact(rows), err
	}
	var b strings.Builder
	err := store.Each(db.DB, `SELECT 'R-'||id, status, name, target, requester, created_at, COALESCE(replace(result, char(10), ' '),'-') FROM recipe_runs WHERE `+where+` ORDER BY recipe_runs.id`, nil,
		func(_ []string, vals []any) error {
			s := make([]string, len(vals))
			for i, v := range vals {
				if v != nil {
					s[i] = jv.Scalar(rowfmt.Value(v))
				}
			}
			b.WriteString(strings.Join(s, "\t") + "\n")
			return nil
		})
	return b.String(), err
}
