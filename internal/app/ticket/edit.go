package ticket

// チケットを作る(ticket.add。tb add)・変える(ticket.update。tb update)。

import (
	"context"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// AddReq は ticket.add の入力。Type と Priority は空なら task / medium。Labels はそれぞれカンマ区切りでもよい。
type AddReq struct {
	Project  string   `api:"project"`
	Title    string   `api:"title"`
	Type     string   `api:"type,optional"`
	Priority string   `api:"priority,optional"`
	Desc     string   `api:"desc,optional"`
	Due      string   `api:"due,optional"`
	Sprint   string   `api:"sprint,optional"`
	Points   string   `api:"points,optional"`
	Parent   string   `api:"parent,optional"`
	Owner    string   `api:"owner,optional"`
	Session  string   `api:"session,optional"`
	Account  string   `api:"account,optional"`
	Labels   []string `api:"labels,optional"`
	Refs     []string `api:"refs,optional"`
}

// AddResult は ticket.add の結果。
type AddResult struct {
	ID      string       `api:"id" json:"id"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *AddResult) SetPending(p *app.Pending) { r.Pending = p }

// UpdateReq は ticket.update の入力。Changes は tb update の引数の順(tickets.UpdateFields)。
type UpdateReq struct {
	ID      string           `api:"id,path"`
	Changes []tickets.Change `api:"changes"`
}

// UpdateResult は ticket.update の結果。
type UpdateResult struct {
	ID      string       `api:"id" json:"id"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *UpdateResult) SetPending(p *app.Pending) { r.Pending = p }

func editOps(env *app.Env) []app.Op {
	add := app.Command(env, "ticket.add",
		entryGate("cmd:ticket:add", "tb add", func(any) error { return nil }),
		func(ctx context.Context, in AddReq) (AddResult, error) {
			return withWriter(env, who(ctx), true, func(w *writer) (AddResult, error) {
				id, err := tickets.Add(w.db, w.st, tickets.AddSpec{Project: in.Project, Title: in.Title, Type: in.Type, Priority: in.Priority,
					Desc: in.Desc, Due: in.Due, Sprint: in.Sprint, Points: in.Points, Parent: in.Parent, Owner: in.Owner,
					Session: in.Session, Account: in.Account, Labels: in.Labels, Refs: in.Refs}, env.Warner(), w.now)
				return AddResult{ID: id}, err
			})
		},
		define.Doc("チケットを作る(tb add)"),
		define.Authz("cmd:ticket:add", define.Resource("ticket", "project")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets"),
	)
	update := app.Command(env, "ticket.update",
		entryGate("cmd:ticket:update", "tb update", func(v any) error { return needID(v.(UpdateReq).ID) }),
		func(ctx context.Context, in UpdateReq) (UpdateResult, error) {
			return withWriter(env, who(ctx), true, func(w *writer) (UpdateResult, error) {
				u, err := tickets.Update(w.db, w.st, in.ID, in.Changes, env.Warner(), w.now)
				if err == nil && u.Status != "" && w.st.Is(u.Status, "done") && !w.st.Is(u.From, "done") {
					notify.TaskDone(w.db.DB, w.db.Session, in.ID)
				}
				return UpdateResult{ID: in.ID}, err
			})
		},
		define.Doc("チケットを変える(tb update)"),
		define.Authz("cmd:ticket:update", define.Resource("ticket", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("PATCH /v1/tickets/{id}"),
	)
	return []app.Op{add, update}
}
