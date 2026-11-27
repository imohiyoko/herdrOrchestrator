package ticket

// 状態の定義(設定 statuses)を足す・外す: ticket.status.add / ticket.status.rm(tb ticket status add / rm。人だけが既定)。

import (
	"context"
	"errors"
	"fmt"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ReasonQuiet は、理由を警告(Env.Warn)に出し終えたので、入口は文言を足さずに失敗で終わる(tb は 1)。
const ReasonQuiet = "already_reported"

// StatusAddReq は ticket.status.add の入力。
type StatusAddReq struct {
	Name     string `api:"name"`
	Category string `api:"category"`
	Desc     string `api:"desc,optional"`
	After    string `api:"after,optional"`
}

// StatusRmReq は ticket.status.rm の入力。To はその状態のチケットを移す先。
type StatusRmReq struct {
	Name string `api:"name,path"`
	To   string `api:"to,optional"`
}

// StatusDefResult は状態の定義を変えた結果。Source は書いた設定のところ。
// HadTickets は rm でその状態のチケットがあった(Moved はそのうち移した数。アラート・提案のチケットは移さない)。
type StatusDefResult struct {
	Source     string       `api:"source" json:"source"`
	HadTickets bool         `api:"had_tickets,optional" json:"had_tickets,omitempty"`
	Moved      int          `api:"moved,optional" json:"moved,omitempty"`
	Pending    *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *StatusDefResult) SetPending(p *app.Pending) { r.Pending = p }

func statusOps(env *app.Env) []app.Op {
	add := app.Command(env, "ticket.status.add", app.Cmd("cmd:ticket:status-add", "tb ticket status add"),
		func(ctx context.Context, in StatusAddReq) (StatusDefResult, error) {
			return withWriter(env, who(ctx), false, func(w *writer) (StatusDefResult, error) {
				body, err := tickets.StatusAddBody(w.db, w.st, in.Name, in.Category, in.Desc, in.After)
				if err != nil {
					return StatusDefResult{}, err
				}
				if err := statusWrite(env, w, body, "ticket status add "+in.Name, false); err != nil {
					return StatusDefResult{}, err
				}
				return StatusDefResult{Source: tickets.StatusSource(w.db)}, nil
			})
		},
		define.Doc("状態を足す(設定 statuses に書き足す。tb ticket status add)"),
		define.Authz("cmd:ticket:status-add", define.Resource("status", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/statuses"),
	)
	rm := app.Command(env, "ticket.status.rm", app.Cmd("cmd:ticket:status-rm", "tb ticket status rm"),
		func(ctx context.Context, in StatusRmReq) (StatusDefResult, error) {
			return withWriter(env, who(ctx), false, func(w *writer) (StatusDefResult, error) {
				body, move, err := tickets.StatusRmPlan(w.db, w.st, in.Name, in.To)
				var bad *tickets.BadStatuses
				if errors.As(err, &bad) {
					// tb は検査の文言だけを出して 1 で終わる(続く文は出さない)
					fmt.Fprint(env.Warner(), bad.Detail)
					return StatusDefResult{}, &define.Error{Code: define.CodeFailedPrecondition, Reasons: []string{ReasonQuiet}}
				}
				if err != nil {
					return StatusDefResult{}, err
				}
				res := StatusDefResult{HadTickets: move != nil}
				for _, id := range move {
					cur, changed, err := tickets.Move(w.db, w.st, id, in.To, "", false, nil, w.now)
					if err != nil {
						return StatusDefResult{}, err
					}
					if changed {
						if w.st.Is(in.To, "done") && !w.st.Is(cur, "done") {
							notify.TaskDone(w.db.DB, w.db.Session, id)
						}
						notify.TicketRules(w.db.DB, w.db.Session, w.st)
					}
					res.Moved++
				}
				if err := statusWrite(env, w, body, "ticket status rm "+in.Name, true); err != nil {
					return StatusDefResult{}, err
				}
				res.Source = tickets.StatusSource(w.db)
				return res, nil
			})
		},
		define.Doc("状態を外す(その状態のチケットは To へ移す。tb ticket status rm)"),
		define.Authz("cmd:ticket:status-rm", define.Resource("status", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/statuses/{name}/rm"),
	)
	return []app.Op{add, rm}
}

// statusWrite は tickets.StatusWrite の断りを入口の形にする(検査の文言は警告に出す)。
func statusWrite(env *app.Env, w *writer, body, how string, _ bool) error {
	err := tickets.StatusWrite(w.db, body, how)
	var bad *tickets.BadStatuses
	if errors.As(err, &bad) {
		fmt.Fprint(env.Warner(), bad.Detail)
		return define.Errorf(define.CodeFailedPrecondition, "%s", bad.Error())
	}
	var sl *config.ErrSymlink
	if errors.As(err, &sl) {
		return define.Errorf(define.CodeFailedPrecondition, "%s", sl.Error())
	}
	return err
}
