package ticket

import (
	"context"
	"regexp"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// CountReq は ticket.count の入力。空の項目は絞らない。
type CountReq struct {
	Project string `api:"project,optional"`
	Label   string `api:"label,optional"`
	// Category は状態の分類(todo / active / review / blocked / done / cancelled。カンマで複数。空なら todo)。
	Category string `api:"category,optional"`
	// Unowned は担当(owner_agent)の無いものだけ。
	Unowned bool `api:"unowned,optional"`
}

// CountResult は ticket.count の結果。
type CountResult struct {
	Count int `api:"count" json:"count"`
}

var (
	reProject = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	cats      = map[string]bool{"todo": true, "active": true, "review": true, "blocked": true, "done": true, "cancelled": true}
)

func (in CountReq) categories() []string {
	if in.Category == "" {
		return []string{"todo"}
	}
	return strings.Split(in.Category, ",")
}

func countOp(env *app.Env) app.Op {
	return app.Query(env, "ticket.count",
		func(_ context.Context, _ *app.Env, _ string, v any) app.Decision {
			in := v.(CountReq)
			if in.Project != "" && !reProject.MatchString(in.Project) {
				return app.Decision{Err: define.Errorf(define.CodeInvalidArgument, "project は英数字と _ の 32 字まで: %s", in.Project)}
			}
			if len([]rune(in.Label)) > 64 {
				return app.Decision{Err: define.Errorf(define.CodeInvalidArgument, "label は 64 字まで")}
			}
			for _, c := range in.categories() {
				if !cats[c] {
					return app.Decision{Err: define.Errorf(define.CodeInvalidArgument, "category は todo / active / review / blocked / done / cancelled: %s", c)}
				}
			}
			return app.Decision{}
		},
		func(_ context.Context, in CountReq) (CountResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return CountResult{}, internal(err)
			}
			defer done()
			st := tickets.LoadStatuses(db.DB, db.Session)
			q := `SELECT COUNT(*) FROM tasks t WHERE t.status IN (` + st.SQLList(in.categories()...) + `)`
			var args []any
			if in.Project != "" {
				q += ` AND t.project = ?`
				args = append(args, in.Project)
			}
			if in.Label != "" {
				q += ` AND EXISTS (SELECT 1 FROM json_each(t.labels) j WHERE j.value = ?)`
				args = append(args, in.Label)
			}
			if in.Unowned {
				q += ` AND COALESCE(t.owner_agent, '') = ''`
			}
			var r CountResult
			if err := db.QueryRow(q, args...).Scan(&r.Count); err != nil {
				return CountResult{}, internal(err)
			}
			return r, nil
		},
		define.Doc("条件に合うチケットの数(既定は未着手。agent セットの自動スケールの条件に使う)"),
		define.Authz("cmd:ticket:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/tickets/count"),
	)
}
