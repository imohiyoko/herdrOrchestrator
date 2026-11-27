// Package approval は承認待ち(Q-<n>)を決める操作と、承認制の行を書く操作のアプリケーション API(ADR 0008。tb のサービス approval)。
// 中身は internal/requests。agent の操作の承認待ちは app/agent の Decide に渡す(承認待ちは 1 か所で見て決める。ADR 0007 の段 4)。
package approval

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

// DecideReq は approval.approve / approval.reject の入力。ID は Q-<n>(前の O-<n> / L-<n> も受ける)。
// Always は承認して、今後は承認なしにする(送信と agent の操作だけ。人だけ)。
type DecideReq struct {
	ID     string `api:"id,path"`
	Always bool   `api:"always,optional"`
}

// Result は決めた結果。Out / Err は tb が標準出力・標準エラーに出す行(Env.Out があればそちらへ流し、ここは空)。
// Exit は tb の終了コード(0 以外でも失敗ではない: 送信の承認で届けられなかった 11 / 12、承認して実行したコマンドの終了コード)。
type Result struct {
	ID   string `api:"id" json:"id"`
	Out  string `api:"out,optional" json:"out,omitempty"`
	Err  string `api:"err,optional" json:"err,omitempty"`
	Exit int    `api:"exit,optional" json:"exit,omitempty"`
}

// PatternReq は approval.allow / approval.ask の入力(操作のパターン。例 send:task:worker1、agent:new:*)。
type PatternReq struct {
	Pattern string `api:"pattern"`
}

// Ops は承認の操作。
func Ops(env *app.Env) []app.Op {
	return []app.Op{decideOp(env, true), decideOp(env, false), patternOp(env, "allow"), patternOp(env, "ask")}
}

func decideOp(env *app.Env, approve bool) app.Op {
	verb := "reject"
	if approve {
		verb = "approve"
	}
	return app.Command(env, "approval."+verb, app.Entry("cmd:approval:"+verb, "tb approval "+verb),
		func(ctx context.Context, in DecideReq) (Result, error) {
			p, _ := define.PrincipalFrom(ctx)
			return decide(ctx, env, app.Who(p), in, approve)
		},
		define.Doc("承認待ち Q-<n> を"+map[bool]string{true: "承認する(送信は届け、agent の操作は実行し、コマンドは依頼者に返すか依頼者として実行する)", false: "却下する"}[approve]+"(tb approval "+verb+")"),
		define.Authz("cmd:approval:"+verb, define.Resource("request", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/requests/{id}/"+verb),
	)
}

func decide(ctx context.Context, env *app.Env, who string, in DecideReq, approve bool) (Result, error) {
	if strings.HasPrefix(in.ID, "L-") {
		return agentDecide(ctx, env, who, in.ID, in.Always, approve)
	}
	db, done, err := env.DB()
	if err != nil {
		return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	var out, errb bytes.Buffer
	o := requests.IO{Out: &out, Err: &errb, ChildEnv: env.ChildEnv, Tb: env.TbCmd}
	if env.Out != nil {
		o.Out, o.Err = env.Out, env.Warner()
	}
	code, lid, err := requests.Decide(db, o, who, in.ID, approve, in.Always)
	done()
	if err != nil {
		return Result{}, refused(err)
	}
	if lid > 0 { // agent の操作の承認待ち
		return agentDecide(ctx, env, who, "L-"+strconv.FormatInt(lid, 10), in.Always, approve)
	}
	return Result{ID: in.ID, Out: out.String(), Err: errb.String(), Exit: code}, nil
}

// agentDecide は agent の操作の承認待ちを app/agent に渡す。tb agent-approve / reject の入口の行(cmd:agent:<動詞>)もここで判定する
// (policy で cmd:agent:approve だけを締めた行を、approval の入口から迂回させない)。
func agentDecide(ctx context.Context, env *app.Env, who, lid string, always, approve bool) (Result, error) {
	verb := "reject"
	if approve {
		verb = "approve"
	}
	if d := app.Cmd("cmd:agent:"+verb, "tb agent-"+verb)(ctx, env, who, nil); d.Err != nil {
		return Result{}, d.Err
	} else if d.Pending != nil {
		return Result{}, &define.Error{Code: define.CodePermissionDenied, Message: d.Pending.Message, Reasons: []string{app.ReasonApprovalRequired}}
	}
	r, err := agent.Decide(env, who, agent.DecideReq{ID: lid, Always: always}, approve)
	if err != nil {
		return Result{}, err
	}
	out := r.Out + "\n"
	if env.Out != nil {
		io.WriteString(env.Out, out)
		out = ""
	}
	return Result{ID: lid, Out: out}, nil
}

func patternOp(env *app.Env, val string) app.Op {
	return app.Command(env, "approval."+val, app.Cmd("cmd:approval:"+val, "tb "+val),
		func(ctx context.Context, in PatternReq) (Result, error) {
			p, _ := define.PrincipalFrom(ctx)
			who := app.Who(p)
			db, done, err := env.DB()
			if err != nil {
				return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			var out, errb bytes.Buffer
			w, ew := io.Writer(&out), io.Writer(&errb)
			if env.Out != nil {
				w, ew = env.Out, env.Warner()
			}
			check := func(old, new string) error {
				return permDenied(perms.RequirePolicyWriteAs(db.DB, db.Session, who, old, new, "tb "+val))
			}
			if err := requests.SetApprovalChecked(db, val, in.Pattern, w, ew, check); err != nil {
				return Result{}, refused(err)
			}
			return Result{ID: in.Pattern, Out: out.String(), Err: errb.String()}, nil
		},
		define.Doc(map[string]string{"allow": "今後は許可する(設定 policy に * <操作> <対象> = allow の行を書く。tb approval allow。人だけ)", "ask": "承認制に戻す(設定 policy に * <操作> <対象> = ask の行を書く。tb approval ask。人だけ)"}[val]),
		define.Authz("cmd:approval:"+val),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/approval/"+val),
	)
}

// permDenied は perms の断り(1 か 20)を API のエラーにする(20 は承認が要る)。
func permDenied(err error) error {
	var d *perms.Denied
	if !errors.As(err, &d) {
		return err
	}
	e := &define.Error{Code: define.CodePermissionDenied, Message: d.Msg}
	if d.Code == 20 {
		e.Reasons = []string{app.ReasonApprovalRequired}
	}
	return e
}

// refused は requests の断りを API のエラーにする(使い方の誤り = InvalidArgument、ほか = FailedPrecondition)。
func refused(err error) error {
	var e *define.Error
	if errors.As(err, &e) {
		return err
	}
	var r *requests.Refused
	if errors.As(err, &r) {
		if r.Usage {
			return define.Errorf(define.CodeInvalidArgument, "%s", r.Msg)
		}
		return define.Errorf(define.CodeFailedPrecondition, "%s", r.Msg)
	}
	return define.Errorf(define.CodeInternal, "%s", err.Error())
}

