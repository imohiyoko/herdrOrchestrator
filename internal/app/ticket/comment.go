package ticket

// 報告コメント(ticket.comment。tb comment)。

import (
	"context"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// CommentReq は ticket.comment の入力(Deliverables は tb の --deliverables と同じカンマ区切り)。
type CommentReq struct {
	TaskID       string `api:"task_id,path"`
	Kind         string `api:"kind"` // done / progress / question / blocked
	Text         string `api:"text"`
	Deliverables string `api:"deliverables,optional"`
}

// CommentResult は ticket.comment の結果。
// State は queued(まとめて届ける)/ delivered(すぐ届けた)/ saved-undelivered(すぐ届けようとして届かなかった。RC が 10 か 11)。
type CommentResult struct {
	ID      int64        `api:"id" json:"id"`
	State   string       `api:"state" json:"state"`
	RC      int          `api:"rc,optional" json:"rc,omitempty"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *CommentResult) SetPending(p *app.Pending) { r.Pending = p }

func commentOp(env *app.Env) app.Op {
	return app.Command(env, "ticket.comment",
		entryGate("cmd:ticket:comment", "tb comment", func(v any) error { return needID(v.(CommentReq).TaskID) }),
		func(ctx context.Context, in CommentReq) (CommentResult, error) {
			by := who(ctx)
			return withWriter(env, by, false, func(w *writer) (CommentResult, error) {
				sp := tickets.CommentSpec{TaskID: in.TaskID, Kind: in.Kind, Text: in.Text, Deliverables: in.Deliverables}
				dels, err := tickets.CheckComment(w.db, sp)
				if err != nil {
					return CommentResult{}, err
				}
				name, pane, session := env.ReporterOf(by)
				cid, err := tickets.AddComment(w.db, sp, dels, tickets.Reporter{Name: name, Pane: pane, Session: session}, w.now)
				if err != nil {
					return CommentResult{}, err
				}
				notify.Comment(w.db.DB, w.db.Session, cid)
				notify.TicketRules(w.db.DB, w.db.Session, w.st)
				res := CommentResult{ID: cid, State: "queued"}
				imm := env.ImmediateKinds
				if imm == "" {
					imm = "question blocked"
				}
				if strings.Contains(" "+imm+" ", " "+in.Kind+" ") { // 人の判断待ちはすぐ送る。それ以外は tb digest でまとめて送る
					d, err := message.DeliverComment(w.db, strconv.FormatInt(cid, 10), env.NowString(), env.Warner())
					if err != nil {
						return CommentResult{}, err
					}
					res.State, res.RC = "delivered", d.RC
					if d.RC != 0 {
						res.State = "saved-undelivered"
					}
				}
				return res, nil
			})
		},
		define.Doc("報告コメントを書く(question / blocked はすぐ届ける。tb comment)"),
		define.Authz("cmd:ticket:comment", define.Resource("ticket", "task_id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets/{task_id}/comments"),
	)
}
