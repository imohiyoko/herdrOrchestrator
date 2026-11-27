// Package message は agent 同士の送信のアプリケーション API(ADR 0008。tb のサービス message)。
// 中身は internal/message。送信者は主体から決める(tb の入口は herdr から引いたもの。Env.Reporter)。
package message

import (
	"context"
	"errors"
	"io"
	"strconv"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	msg "github.com/imohiyoko/herdrorchestrator/internal/message"
)

// SendReq は message.send の入力(tb message send)。
type SendReq struct {
	To      string `api:"to"`
	Text    string `api:"text"`
	Task    string `api:"task,optional"`
	Kind    string `api:"kind,optional"` // task(既定) / info / answer
	ReplyTo string `api:"reply_to,optional"`
}

// SendResult は送信の結果。Code は tb の終了コード(0 送信済み・保留 / 10 使用中 / 11 送信失敗 / 12 保留に失敗 / 4 拒否 / 20 承認待ち)。
// 0 以外でも失敗ではない(行は outbox に残る)。途中の知らせは Env.Warn に書く。
type SendResult struct {
	ID      string `api:"id" json:"id"`
	Code    int    `api:"code,optional" json:"code,omitempty"`
	Held    string `api:"held,optional" json:"held,omitempty"`
	Blocked string `api:"blocked,optional" json:"blocked,omitempty"`
	Q       string `api:"q,optional" json:"q,omitempty"`
}

// FlushReq は message.flush の入力(無し)。
type FlushReq struct{}

// FlushResult は message.flush の結果。Code は届けられなかった宛先の最大の終了コード(無ければ 0)。
type FlushResult struct {
	Code int `api:"code,optional" json:"code,omitempty"`
}

// ReasonNoSender は送信者を herdr から決められなかった(tb は理由を出して 3)。
const ReasonNoSender = "no_sender"

// Ops は送信の操作。
func Ops(env *app.Env) []app.Op {
	send := app.Command(env, "message.send", app.Entry("cmd:message:send", "tb message send"),
		func(ctx context.Context, in SendReq) (SendResult, error) {
			p, _ := define.PrincipalFrom(ctx)
			return send(env, app.Who(p), in)
		},
		define.Doc("agent に送る(tb message send。認可・経路の規則・承認待ち・保留は tb と同じ)"),
		define.Authz("cmd:message:send", define.Resource("agent", "to")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/messages"),
	)
	flush := app.Command(env, "message.flush", app.Entry("cmd:message:flush", "tb message flush"),
		func(_ context.Context, _ FlushReq) (FlushResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return FlushResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			w := env.Out
			if w == nil {
				w = io.Discard
			}
			code, err := msg.Flush(db, w, env.Warner(), func() {
				if f, ok := w.(interface{ Flush() error }); ok {
					_ = f.Flush()
				}
			})
			if err != nil {
				return FlushResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			return FlushResult{Code: code}, nil
		},
		define.Doc("積まれた送信(outbox の queued)を届ける(tb message flush)"),
		define.Authz("cmd:message:flush"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/messages/flush"),
	)
	return []app.Op{send, flush}
}

func send(env *app.Env, who string, in SendReq) (SendResult, error) {
	if in.Kind == "" {
		in.Kind = "task"
	}
	db, done, err := env.DB()
	if err != nil {
		return SendResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	defer done()
	s := msg.SendSpec{To: in.To, Text: in.Text, Task: in.Task, Kind: in.Kind, ReplyTo: in.ReplyTo}
	if err := msg.CheckSend(db.DB, s); err != nil {
		return SendResult{}, refused(err)
	}
	s.From, s.Pane, s.Session = env.ReporterOf(who)
	if s.From == "" {
		return SendResult{}, &define.Error{Code: define.CodeUnauthenticated, Message: "送信者を特定できません", Reasons: []string{ReasonNoSender}}
	}
	r, err := msg.Send(db, s, env.NowString(), env.Warner(), func(oid int64) string { return env.QOf(db.DB, "outbox", oid) })
	if err != nil {
		return SendResult{}, refused(err)
	}
	return SendResult{ID: "O-" + strconv.FormatInt(r.OID, 10), Code: r.Code, Held: r.Held, Blocked: r.Blocked, Q: r.Q}, nil
}

func refused(err error) error {
	var r *msg.SendRefused
	if errors.As(err, &r) {
		if r.Usage {
			return define.Errorf(define.CodeInvalidArgument, "%s", r.Msg)
		}
		return define.Errorf(define.CodeFailedPrecondition, "%s", r.Msg)
	}
	return define.Errorf(define.CodeInternal, "%s", err.Error())
}
