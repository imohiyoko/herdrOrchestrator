package ticket

// チケットの状態を動かす操作: ticket.claim / ticket.done / ticket.block / ticket.cancel(tb claim / done / block / cancel)。

import (
	"context"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ClaimReq は ticket.claim の入力。
type ClaimReq struct {
	ID      string `api:"id,path"`
	Owner   string `api:"owner"`
	Session string `api:"session,optional"`
	Account string `api:"account,optional"`
}

// DoneReq は ticket.done の入力(成果物と要約は必須)。
type DoneReq struct {
	ID           string   `api:"id,path"`
	Deliverables []string `api:"deliverables"`
	Summary      string   `api:"summary"`
}

// ReasonReq は ticket.block / ticket.cancel の入力。
type ReasonReq struct {
	ID     string `api:"id,path"`
	Reason string `api:"reason,optional"`
}

// StateResult は状態を動かした結果。Status は動かした先の状態(claim なら active の分類の状態)。
type StateResult struct {
	ID      string       `api:"id" json:"id"`
	Status  string       `api:"status" json:"status"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *StateResult) SetPending(p *app.Pending) { r.Pending = p }

func blank(s string) bool { return strings.TrimFunc(s, config.IsSpace) == "" }

func stateOps(env *app.Env) []app.Op {
	claim := app.Command(env, "ticket.claim",
		entryGate("cmd:ticket:claim", "tb claim", func(v any) error {
			in := v.(ClaimReq)
			if err := needID(in.ID); err != nil {
				return err
			}
			if blank(in.Owner) {
				return define.Errorf(define.CodeInvalidArgument, "claim: --owner が必要です")
			}
			return nil
		}),
		func(ctx context.Context, in ClaimReq) (StateResult, error) {
			return change(env, who(ctx), in.ID, func(w *writer) error {
				return tickets.Claim(w.db, w.st, in.ID, in.Owner, in.Session, in.Account, w.now)
			}, "active")
		},
		define.Doc("チケットを引き受ける(tb claim)"),
		define.Authz("cmd:ticket:claim", define.Resource("ticket", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets/{id}/claim"),
	)
	done := app.Command(env, "ticket.done",
		entryGate("cmd:ticket:done", "tb done", func(v any) error {
			in := v.(DoneReq)
			if err := needID(in.ID); err != nil {
				return err
			}
			if len(nonBlank(in.Deliverables)) == 0 {
				return define.Errorf(define.CodeInvalidArgument, "done: 成果物(-d)が必須です")
			}
			if blank(in.Summary) {
				return define.Errorf(define.CodeInvalidArgument, "done: 要約(-m)が必須です")
			}
			return nil
		}),
		func(ctx context.Context, in DoneReq) (StateResult, error) {
			return change(env, who(ctx), in.ID, func(w *writer) error {
				if err := tickets.Done(w.db, w.st, in.ID, nonBlank(in.Deliverables), in.Summary, w.now); err != nil {
					return err
				}
				notify.TaskDone(w.db.DB, w.db.Session, in.ID)
				return nil
			}, "done")
		},
		define.Doc("チケットを完了にする(tb done)"),
		define.Authz("cmd:ticket:done", define.Resource("ticket", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets/{id}/done"),
	)
	reason := func(name, what, cat string, f func(*writer, ReasonReq) error) app.Op {
		return app.Command(env, "ticket."+name,
			entryGate("cmd:ticket:"+name, what, func(v any) error { return needID(v.(ReasonReq).ID) }),
			func(ctx context.Context, in ReasonReq) (StateResult, error) {
				return change(env, who(ctx), in.ID, func(w *writer) error { return f(w, in) }, cat)
			},
			define.Doc("チケットを "+cat+" の分類にする("+what+")"),
			define.Authz("cmd:ticket:"+name, define.Resource("ticket", "id")),
			define.Expose(define.Internal, define.REST),
			define.HTTP("POST /v1/tickets/{id}/"+name),
		)
	}
	block := reason("block", "tb block", "blocked", func(w *writer, in ReasonReq) error {
		return tickets.Block(w.db, w.st, in.ID, in.Reason, w.now)
	})
	cancel := reason("cancel", "tb cancel", "cancelled", func(w *writer, in ReasonReq) error {
		return tickets.Cancel(w.db, w.st, in.ID, in.Reason, w.now)
	})
	return []app.Op{claim, done, block, cancel}
}

func nonBlank(ss []string) []string {
	var out []string
	for _, s := range ss {
		if !blank(s) {
			out = append(out, s)
		}
	}
	return out
}
