package ticket

// チケットの状態の変更(ticket.move。tb ticket move)と削除(ticket.rm。tb ticket rm)。

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// MoveReq は ticket.move の入力。
type MoveReq struct {
	ID     string `api:"id,path"`
	Status string `api:"status"`
	// Message は SetMessage のときだけ書く(閉じた分類なら要約、ほかはメモ。空なら消す)。
	Message    string `api:"message,optional"`
	SetMessage bool   `api:"set_message,optional"`
	// Deliverables は done の分類へ動かすときの成果物(空の要素は捨てる)。
	Deliverables []string `api:"deliverables,optional"`
}

// MoveResult は ticket.move の結果。
type MoveResult struct {
	ID   string `api:"id" json:"id"`
	From string `api:"from" json:"from"`
	To   string `api:"to" json:"to"`
	// Changed は書き込んだ(false なら、すでにその状態でメモも成果物も無かった)。
	Changed bool         `api:"changed" json:"changed"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *MoveResult) SetPending(p *app.Pending) { r.Pending = p }

// RmReq は ticket.rm の入力。
type RmReq struct {
	IDs []string `api:"ids"`
	// IfCreated は、画面を開いたときのチケットか(作成時刻)を確かめる(HasIfCreated のときだけ。チケット 1 枚のときだけ使える)。
	IfCreated    string `api:"if_created,optional"`
	HasIfCreated bool   `api:"has_if_created,optional"`
}

// RmCount は 1 枚のチケットを消したときに一緒に消したものの件数。
type RmCount struct {
	ID       string `api:"id" json:"id"`
	Comments int64  `api:"comments" json:"comments"`
	Events   int64  `api:"events" json:"events"`
	Links    int64  `api:"links" json:"links"`
}

// RmResult は ticket.rm の結果(IDs の順)。
type RmResult struct {
	Removed []RmCount    `api:"removed" json:"removed"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *RmResult) SetPending(p *app.Pending) { r.Pending = p }

// needID は tb の need_id と同じ文言。
func needID(id string) error {
	if !ReID.MatchString(id) {
		return define.Errorf(define.CodeInvalidArgument, "タスク ID は T-<project>-NNN 形式: %s", id)
	}
	return nil
}

// entryGate は入口の段(Entry)の前に入力の形を確かめる門。
func entryGate(op, what string, check func(any) error) app.Gate {
	entry := app.Entry(op, what)
	return func(ctx context.Context, env *app.Env, who string, v any) app.Decision {
		if err := check(v); err != nil {
			return app.Decision{Err: err}
		}
		return entry(ctx, env, who, v)
	}
}

func moveOp(env *app.Env) app.Op {
	return app.Command(env, "ticket.move",
		entryGate("cmd:ticket:move", "tb ticket move", func(v any) error { return needID(v.(MoveReq).ID) }),
		func(ctx context.Context, in MoveReq) (MoveResult, error) { return move(env, who(ctx), in) },
		define.Doc("チケットの状態を変える(tb ticket move)"),
		define.Authz("cmd:ticket:move", define.Resource("ticket", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets/{id}/move"),
	)
}

func rmOp(env *app.Env) app.Op {
	return app.Command(env, "ticket.rm",
		entryGate("cmd:ticket:rm", "tb ticket rm", func(v any) error {
			in := v.(RmReq)
			if len(in.IDs) == 0 {
				return define.Errorf(define.CodeInvalidArgument, "ticket rm: 消すチケットがありません")
			}
			for _, id := range in.IDs {
				if err := needID(id); err != nil {
					return err
				}
			}
			if in.HasIfCreated && len(in.IDs) != 1 {
				return define.Errorf(define.CodeInvalidArgument, "ticket rm: --if-created はチケット 1 枚のときだけ使えます")
			}
			return nil
		}),
		func(ctx context.Context, in RmReq) (RmResult, error) { return remove(env, who(ctx), in) },
		define.Doc("チケットを消す(コメント・履歴・リンクも。tb ticket rm)"),
		define.Authz("cmd:ticket:rm", define.Resource("ticket", "ids")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets/rm"),
	)
}

func who(ctx context.Context) string {
	p, _ := define.PrincipalFrom(ctx)
	return app.Who(p)
}

// events は履歴のトリガーを主体で作り直す(入口のトリガーをそのまま使うときは何もしない)。
func events(env *app.Env, db *store.DB, who string) error {
	if a := env.EventActorOf(who); a != "" {
		return tickets.SetupEvents(db.DB, a)
	}
	return nil
}

func move(env *app.Env, who string, in MoveReq) (MoveResult, error) {
	db, done, err := env.DB()
	if err != nil {
		return MoveResult{}, internal(err)
	}
	defer done()
	st := tickets.LoadStatuses(db.DB, db.Session)
	if !slices.Contains(st.Names, in.Status) {
		return MoveResult{}, define.Errorf(define.CodeInvalidArgument, "status は %s のどれか: %s", strings.Join(st.Names, " "), in.Status)
	}
	dels := slices.DeleteFunc(slices.Clone(in.Deliverables), func(d string) bool { return strings.TrimSpace(d) == "" })
	if len(dels) > 0 && !st.Is(in.Status, "done") {
		return MoveResult{}, define.Errorf(define.CodeInvalidArgument, "ticket move: -d(成果物)は done の分類へ動かすときだけ使えます: %s", in.Status)
	}
	if err := events(env, db, who); err != nil {
		return MoveResult{}, internal(err)
	}
	cur, changed, err := tickets.Move(db, st, in.ID, in.Status, in.Message, in.SetMessage, dels, env.NowString())
	if err != nil {
		return MoveResult{}, refused(err)
	}
	if changed {
		if st.Is(in.Status, "done") && !st.Is(cur, "done") {
			notify.TaskDone(db.DB, db.Session, in.ID)
		}
		notify.TicketRules(db.DB, db.Session, st)
	}
	return MoveResult{ID: in.ID, From: cur, To: in.Status, Changed: changed}, nil
}

func remove(env *app.Env, who string, in RmReq) (RmResult, error) {
	var ids []string
	for _, id := range in.IDs {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	db, done, err := env.DB()
	if err != nil {
		return RmResult{}, internal(err)
	}
	defer done()
	if err := events(env, db, who); err != nil {
		return RmResult{}, internal(err)
	}
	n, err := tickets.Remove(db, ids, in.IfCreated, in.HasIfCreated, env.NowString())
	if err != nil {
		return RmResult{}, refused(err)
	}
	out := RmResult{Removed: make([]RmCount, len(ids))}
	for i, id := range ids {
		out.Removed[i] = RmCount{ID: id, Comments: n[i].Comments, Events: n[i].Events, Links: n[i].Links}
	}
	return out, nil
}

// refused は tickets の断り(Refused)を型のエラーにする(tb は文言を出して 1 で終わる)。
func refused(err error) error {
	var r *tickets.Refused
	if !errors.As(err, &r) {
		return internal(err)
	}
	code := define.CodeFailedPrecondition
	switch {
	case r.NotFound:
		code = define.CodeNotFound
	case r.Invalid:
		code = define.CodeInvalidArgument
	}
	return define.Errorf(code, "%s", r.Msg)
}
