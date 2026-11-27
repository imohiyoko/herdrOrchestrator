// Package message は agent 同士の送信のアプリケーション API(ADR 0008。tb のサービス message)。
// 中身は internal/message。送信者は主体から決める(tb の入口は herdr から引いたもの。Env.Reporter)。
package message

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
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
	// Pending は、呼び出しそのもの(cmd:message:send)が承認待ちに積まれたとき(policy が ask)。
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *SendResult) SetPending(p *app.Pending) { r.Pending = p }

// FlushReq は message.flush の入力(無し)。
type FlushReq struct{}

// FlushResult は message.flush の結果。Code は届けられなかった宛先の最大の終了コード(無ければ 0)。
// Out は配送の途中の出力(Env.Out が無いときだけ。あればそちらへ流す)。
type FlushResult struct {
	Code    int          `api:"code,optional" json:"code,omitempty"`
	Out     string       `api:"out,optional" json:"out,omitempty"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *FlushResult) SetPending(p *app.Pending) { r.Pending = p }

// ReasonNoSender は送信者を herdr から決められなかった(tb は理由を出して 3)。
const ReasonNoSender = app.ReasonNoSender

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
			var buf bytes.Buffer
			w := env.Out
			if w == nil {
				w = &buf
			}
			code, err := msg.Flush(db, w, env.Warner(), func() {
				if f, ok := w.(interface{ Flush() error }); ok {
					_ = f.Flush()
				}
			})
			if err != nil {
				return FlushResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			return FlushResult{Code: code, Out: buf.String()}, nil
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
	if env.Reporter != nil {
		s.From, s.Pane, s.Session = env.Reporter(who)
	} else if s.From, s.Pane = agentPane(who); s.Pane == "" {
		s.From = "" // 送れるのは pane のいる agent だけ(人・system: の主体は送信者として記録しない)
	}
	if s.From == "" {
		return SendResult{}, &define.Error{Code: define.CodeUnauthenticated, Message: "送信者を特定できません", Reasons: []string{ReasonNoSender}}
	}
	r, err := msg.Send(db, s, env.NowString(), env.Warner(), func(oid int64) string { return env.QOf(db.DB, "outbox", oid) })
	if err != nil {
		return SendResult{}, refused(err)
	}
	return SendResult{ID: "O-" + strconv.FormatInt(r.OID, 10), Code: r.Code, Held: r.Held, Blocked: r.Blocked, Q: r.Q}, nil
}

// agentPane は主体 who が agent なら、その名前と herdr の pane(いなければ空)。
func agentPane(who string) (string, string) {
	if who == "" || who == "human" || strings.HasPrefix(who, "system:") {
		return "", ""
	}
	as, err := herdr.Agents()
	if err != nil {
		return who, ""
	}
	for _, a := range as {
		if a.Name == who {
			return who, a.PaneID
		}
	}
	return who, ""
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
