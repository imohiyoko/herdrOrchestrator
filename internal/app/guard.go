package app

import (
	"context"
	"errors"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
)

// Pending は承認待ちに積んだ結果(失敗ではない。ADR 0008 の決定 2)。
// Route は経路: "Q"(コマンドの ask。tb approval の Q-<n>)か "L"(agent の操作の待ち。agent_ops の L-<n>)。
// Message は tb が今出している文言(入口は経路ごとにそのまま見せる)。
type Pending struct {
	Route   string `api:"route" json:"route"`
	ID      string `api:"id" json:"id"`
	Message string `api:"message" json:"message"`
}

// PendingSetter は、Guard が承認待ちの結果を入れられる出力(出力の型のポインターに実装する)。
type PendingSetter interface{ SetPending(*Pending) }

// Decision は門の判定。ゼロ値は allow(ハンドラーを呼ぶ)。
// Pending があれば承認待ちに積んだ結果を返し、Err があればそれを返す(どちらもハンドラーは呼ばない)。
type Decision struct {
	Pending *Pending
	Err     error
}

// Gate は門。主体 who("human" / agent 名 / "system:<名前>")と入力(操作の In の値)で、ハンドラーを呼ぶか決める。
type Gate func(ctx context.Context, env *Env, who string, in any) Decision

// ReasonApprovalRequired は、ask だが承認待ちに積めなかったときの拒否の理由(tb は終了コード 20 にする)。
const ReasonApprovalRequired = "approval_required"

// Cmd は commands.tbl の場所が inner の行(cmd:<サービス>:<動詞>)の門。tb の require_human と同じ判定と文言。
// what は tb の文面("tb resolve" など。止めたときの文言に入る)。
func Cmd(op, what string) Gate {
	return func(_ context.Context, env *Env, who string, _ any) Decision {
		return enforce(env, who, op, "", what)
	}
}

// Entry は commands.tbl の場所が entry の行(cmd:<サービス>:<動詞>)の門。Authorizer は ask を通すだけなので、
// ask(承認待ちに積む)と deny の文言・記録はここで tb の入口と同じにする。
// tb の入口はコマンドを読む前に同じ行を判定している(perms.Gate)ので、Env.EntryChecked のときは二重に判定・記録しない。
func Entry(op, what string) Gate {
	return func(_ context.Context, env *Env, who string, _ any) Decision {
		if env != nil && env.EntryChecked {
			return Decision{}
		}
		return enforce(env, who, op, "", what)
	}
}

// enforce は主体 who の op を policy で判定し、perms.EnforceAs の結果を Decision にする。
func enforce(env *Env, who, op, target, what string) Decision {
	tier, rule, err := judge(env, who, op, target)
	if err != nil {
		return Decision{Err: define.Errorf(define.CodeInternal, "%s", err.Error())}
	}
	err = perms.EnforceAs(who, op, target, tier, rule, what)
	if err == nil {
		return Decision{}
	}
	var d *perms.Denied
	if !errors.As(err, &d) {
		return Decision{Err: define.Errorf(define.CodeInternal, "%s", err.Error())}
	}
	if d.Code == 20 && d.Queued != "" {
		return Decision{Pending: &Pending{Route: "Q", ID: d.Queued, Message: d.Msg}}
	}
	e := &define.Error{Code: define.CodePermissionDenied, Message: d.Msg}
	if d.Code == 20 {
		e.Reasons = []string{ReasonApprovalRequired}
	}
	return Decision{Err: e}
}

// guard はハンドラーの手前の門。主体(define.PrincipalFrom)と入力で gate を呼び、
// allow ならハンドラーを、承認待ちなら Pending を持つ結果を、拒否ならエラーを返す。
func guard[In, Out any](env *Env, gate Gate, h func(context.Context, In) (Out, error)) func(context.Context, In) (Out, error) {
	return func(ctx context.Context, in In) (Out, error) {
		var zero Out
		p, ok := define.PrincipalFrom(ctx)
		if !ok {
			return zero, &define.Error{Code: define.CodeUnauthenticated}
		}
		who := Who(p)
		if who == "" {
			return zero, &define.Error{Code: define.CodePermissionDenied, Reasons: []string{"principal_not_allowed"}}
		}
		d := gate(ctx, env, who, in)
		switch {
		case d.Err != nil:
			return zero, d.Err
		case d.Pending != nil:
			var out Out
			s, ok := any(&out).(PendingSetter)
			if !ok {
				return zero, define.Errorf(define.CodeInternal, "承認待ちを返せない出力です")
			}
			s.SetPending(d.Pending)
			return out, nil
		}
		return h(ctx, in)
	}
}

// judge は主体 who の op の効果。cmd: の操作は commands.tbl、それ以外は ops.tbl の既定に policy を重ねる。
func judge(env *Env, who, op, target string) (tier, rule string, err error) {
	if strings.HasPrefix(op, "cmd:") {
		return tierFor(env, who, op, target)
	}
	if who == humanID {
		return "allow", "(人は常に通る)", nil
	}
	if env == nil || env.DB == nil {
		return "deny", "(設定 policy を読めない: DB が無い)", nil
	}
	d, done, err := env.DB()
	if err != nil {
		return "", "", err
	}
	defer done()
	tier, rule = perms.Authorize(d.DB, d.Session, who, op, target, perms.Matcher{})
	return tier, rule, nil
}
