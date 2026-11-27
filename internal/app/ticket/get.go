package ticket

import (
	"context"
	"database/sql"
	"regexp"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// GetReq は ticket.get の入力。
type GetReq struct {
	ID string `api:"id,path"` // T-<project>-<番号>
}

// GetResult は ticket.get の結果(チケットの状態の要点)。
type GetResult struct {
	ID     string `api:"id" json:"id"`
	Title  string `api:"title" json:"title"`
	Status string `api:"status" json:"status"`
	// Category は状態の分類(todo / active / review / done / blocked / cancelled。設定 statuses による)。
	Category string `api:"category" json:"category"`
	Owner    string `api:"owner,optional" json:"owner,omitempty"`
	// Closed は終わった(done か cancelled の分類)。
	Closed bool `api:"closed" json:"closed"`
}

// ReID はチケットの ID(tb の reID と同じ)。
var ReID = regexp.MustCompile(`^T-[A-Za-z0-9_]+-[0-9]{3,}$`)

func getOp(env *app.Env) app.Op {
	return app.Query(env, "ticket.get",
		func(_ context.Context, _ *app.Env, _ string, v any) app.Decision {
			if id := v.(GetReq).ID; !ReID.MatchString(id) {
				return app.Decision{Err: define.Errorf(define.CodeInvalidArgument, "チケットの ID は T-<project>-<番号>: %s", id)}
			}
			return app.Decision{}
		},
		func(_ context.Context, in GetReq) (GetResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return GetResult{}, internal(err)
			}
			defer done()
			r := GetResult{ID: in.ID}
			var owner sql.NullString
			err = db.QueryRow(`SELECT title, status, owner_agent FROM tasks WHERE id=?`, in.ID).Scan(&r.Title, &r.Status, &owner)
			if err == sql.ErrNoRows {
				return GetResult{}, define.Errorf(define.CodeNotFound, "チケットが見つかりません: %s", in.ID)
			}
			if err != nil {
				return GetResult{}, internal(err)
			}
			st := tickets.LoadStatuses(db.DB, db.Session)
			r.Owner, r.Category, r.Closed = owner.String, st.Cat(r.Status), st.Is(r.Status, "done", "cancelled")
			return r, nil
		},
		define.Doc("チケットの状態の要点(tb show の読み取り)"),
		define.Authz("cmd:ticket:show", define.Resource("ticket", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/tickets/{id}"),
	)
}
