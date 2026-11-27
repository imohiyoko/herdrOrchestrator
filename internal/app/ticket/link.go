package ticket

// リンク(ticket.link / ticket.unlink)とスプリント(ticket.sprint.add / start / close)。

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// LinkReq は ticket.link / ticket.unlink の入力。
type LinkReq struct {
	From string `api:"from"`
	Type string `api:"type"` // blocks / relates / duplicates
	To   string `api:"to"`
}

// LinkResult は ticket.link / ticket.unlink の結果。Added は link で足した(false なら既にあった)。
type LinkResult struct {
	Added   bool         `api:"added" json:"added"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *LinkResult) SetPending(p *app.Pending) { r.Pending = p }

// SprintAddReq は ticket.sprint.add の入力(開始・終了は YYYY-MM-DD か空)。
type SprintAddReq struct {
	Project string `api:"project"`
	Name    string `api:"name"`
	Start   string `api:"start,optional"`
	End     string `api:"end,optional"`
	Goal    string `api:"goal,optional"`
}

// SprintReq は ticket.sprint.start / ticket.sprint.close の入力。Carry は close で未完了のタスクを移す先(空ならバックログ)。
type SprintReq struct {
	ID    string `api:"id,path"`
	Carry string `api:"carry,optional"`
}

// SprintResult はスプリントの操作の結果。Moved は close で移したタスクの数。
type SprintResult struct {
	ID      string       `api:"id" json:"id"`
	Moved   int64        `api:"moved,optional" json:"moved,omitempty"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *SprintResult) SetPending(p *app.Pending) { r.Pending = p }

var reDue = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// ValidDate は YYYY-MM-DD の日付か(tb の validDue と同じ)。
func ValidDate(s string) bool {
	if !reDue.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// withWriter は DB を開いて履歴のトリガーを主体で作り直し、f を呼ぶ(通知の規則は rules のときだけ流す)。
func withWriter[T any](env *app.Env, who string, rules bool, f func(*writer) (T, error)) (T, error) {
	var zero T
	db, done, err := env.DB()
	if err != nil {
		return zero, internal(err)
	}
	defer done()
	w := &writer{db: db, st: tickets.LoadStatuses(db.DB, db.Session), now: env.NowString()}
	if err := events(env, db, who); err != nil {
		return zero, internal(err)
	}
	out, err := f(w)
	if err != nil {
		return zero, refused(err)
	}
	if rules {
		notify.TicketRules(db.DB, db.Session, w.st)
	}
	return out, nil
}

func linkOps(env *app.Env) []app.Op {
	linkGate := func(op, what string) app.Gate {
		return entryGate(op, what, func(v any) error {
			in := v.(LinkReq)
			for _, id := range []string{in.From, in.To} {
				if err := needID(id); err != nil {
					return err
				}
			}
			return nil
		})
	}
	link := app.Command(env, "ticket.link", linkGate("cmd:ticket:link", "tb link"),
		func(ctx context.Context, in LinkReq) (LinkResult, error) {
			by := who(ctx)
			return withWriter(env, by, true, func(w *writer) (LinkResult, error) {
				added, err := tickets.Link(w.db, in.From, in.Type, in.To, env.ActorOf(by), w.now)
				return LinkResult{Added: added}, err
			})
		},
		define.Doc("チケットをリンクする(tb link)"),
		define.Authz("cmd:ticket:link", define.Resource("ticket", "from")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/ticket-links"),
	)
	unlink := app.Command(env, "ticket.unlink", linkGate("cmd:ticket:unlink", "tb unlink"),
		func(ctx context.Context, in LinkReq) (LinkResult, error) {
			return withWriter(env, who(ctx), true, func(w *writer) (LinkResult, error) {
				return LinkResult{}, tickets.Unlink(w.db, in.From, in.Type, in.To)
			})
		},
		define.Doc("チケットのリンクを外す(tb unlink)"),
		define.Authz("cmd:ticket:unlink", define.Resource("ticket", "from")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/ticket-links/delete"),
	)
	sprintAdd := app.Command(env, "ticket.sprint.add",
		entryGate("cmd:ticket:sprint-add", "tb sprint-add", func(v any) error {
			in := v.(SprintAddReq)
			for _, d := range []string{in.Start, in.End} {
				if d != "" && !ValidDate(d) {
					return define.Errorf(define.CodeInvalidArgument, "期日は YYYY-MM-DD の日付: %s", d)
				}
			}
			return nil
		}),
		func(ctx context.Context, in SprintAddReq) (SprintResult, error) {
			return withWriter(env, who(ctx), false, func(w *writer) (SprintResult, error) {
				n, err := tickets.SprintAdd(w.db, tickets.SprintSpec{Project: in.Project, Name: in.Name, Start: in.Start, End: in.End, Goal: in.Goal}, w.now)
				return SprintResult{ID: "S-" + strconv.FormatInt(n, 10)}, err
			})
		},
		define.Doc("スプリントを作る(planned。tb sprint-add)"),
		define.Authz("cmd:ticket:sprint-add", define.Resource("sprint", "project")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/sprints"),
	)
	sprintGate := func(op, what string) app.Gate {
		return entryGate(op, what, func(v any) error {
			if err := tickets.NeedSprint(v.(SprintReq).ID); err != nil {
				return refused(err)
			}
			return nil
		})
	}
	sprintStart := app.Command(env, "ticket.sprint.start", sprintGate("cmd:ticket:sprint-start", "tb sprint-start"),
		func(ctx context.Context, in SprintReq) (SprintResult, error) {
			return withWriter(env, who(ctx), false, func(w *writer) (SprintResult, error) {
				return SprintResult{ID: in.ID}, tickets.SprintStart(w.db, in.ID, w.now)
			})
		},
		define.Doc("スプリントを始める(planned → active。tb sprint-start)"),
		define.Authz("cmd:ticket:sprint-start", define.Resource("sprint", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/sprints/{id}/start"),
	)
	sprintClose := app.Command(env, "ticket.sprint.close", sprintGate("cmd:ticket:sprint-close", "tb sprint-close"),
		func(ctx context.Context, in SprintReq) (SprintResult, error) {
			return withWriter(env, who(ctx), false, func(w *writer) (SprintResult, error) {
				n, err := tickets.SprintClose(w.db, w.st, in.ID, in.Carry, w.now)
				return SprintResult{ID: in.ID, Moved: n}, err
			})
		},
		define.Doc("スプリントを閉じ、未完了のタスクを移す(tb sprint-close)"),
		define.Authz("cmd:ticket:sprint-close", define.Resource("sprint", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/sprints/{id}/close"),
	)
	return []app.Op{link, unlink, sprintAdd, sprintStart, sprintClose}
}

