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

// Check は入力から、門で判定する操作名・対象・文面を作る。
// op は commands.tbl の cmd:<サービス>:<動詞>(場所が inner の行)か ops.tbl の操作名(agent:new:<名前> など)。
// target は対象(無ければ "")。what は tb の文面("tb resolve" など。止めたときの文言に入る)。
type Check[In any] func(in In) (op, target, what string)

// ReasonApprovalRequired は、ask だが承認待ちに積めなかったときの拒否の理由(tb は終了コード 20 にする)。
const ReasonApprovalRequired = "approval_required"

// guard はハンドラーの手前の門。主体(define.PrincipalFrom)と入力から操作名と対象を作り、policy を 3 つの効果で判定する:
// deny は permission_denied、ask はハンドラーを呼ばずに承認待ちに積んで Pending を持つ結果を返す、allow だけハンドラーを呼ぶ。
func guard[In, Out any](env *Env, check Check[In], h func(context.Context, In) (Out, error)) func(context.Context, In) (Out, error) {
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
		op, target, what := check(in)
		tier, rule, err := judge(env, who, op, target)
		if err != nil {
			return zero, define.Errorf(define.CodeInternal, "%s", err.Error())
		}
		switch err := perms.EnforceAs(who, op, target, tier, rule, what); {
		case err == nil:
			return h(ctx, in)
		default:
			var d *perms.Denied
			if !errors.As(err, &d) {
				return zero, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			if d.Code == 20 && d.Queued != "" {
				var out Out
				s, ok := any(&out).(PendingSetter)
				if !ok {
					return zero, define.Errorf(define.CodeInternal, "%s: 承認待ちを返せない出力です", op)
				}
				s.SetPending(&Pending{Route: "Q", ID: d.Queued, Message: d.Msg})
				return out, nil
			}
			e := &define.Error{Code: define.CodePermissionDenied, Message: d.Msg}
			if d.Code == 20 {
				e.Reasons = []string{ReasonApprovalRequired}
			}
			return zero, e
		}
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
