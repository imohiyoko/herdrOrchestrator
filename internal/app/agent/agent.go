// Package agent は agent のアプリケーション API(ADR 0008 の段 1。tb のサービス agent)。
//
// 操作の門は agent の操作の判定(policy の agent:<操作>:<名前>。approval.Judge)で、ask は agent_ops に積んで L-<n> を返す。
// 結果は agent_ops に決まった行として残す(tb の op_entry と同じ)。
package agent

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// 拒否の理由(tb は終了コードにする)。
const (
	// ReasonOpDenied は policy の agent:<操作> が deny(tb の終了コード 4)。
	ReasonOpDenied = "op_denied"
	// ReasonHerdrUnavailable は herdr を呼べない・pane を閉じられない(tb の終了コード 11)。
	ReasonHerdrUnavailable = "herdr_unavailable"
	// ReasonLimitExceeded は上限(max_running_agents など)を超える(tb の終了コード 4)。
	ReasonLimitExceeded = "limit_exceeded"
	// ReasonPromptUnconfirmed は再起動したが最初の指示の処理を確かめられない(tb の終了コード 10)。
	ReasonPromptUnconfirmed = "prompt_unconfirmed"
	// ReasonKindUnsupported は agent の種類(kind)が要求に対応していない(tb の終了コード 12)。
	ReasonKindUnsupported = "kind_unsupported"
)

// ExitReasons は拒否の理由 → tb の終了コード。
var ExitReasons = map[string]int{ReasonOpDenied: 4, ReasonLimitExceeded: 4, ReasonHerdrUnavailable: 11, ReasonPromptUnconfirmed: 10, ReasonKindUnsupported: 12, ReasonUseTB: 1}

// Result は操作の実行の結果(tb の opResult。Code は tb の終了コード、Out は文言)。
type Result struct {
	Code int
	Out  string
	Note string // 最初の指示を送れなかった・確かめられなかった理由(FP_NOTE)
	Pane string // 起動した pane(作成のときだけ)
	WS   string // 作った workspace(作成で workspace を作ったときだけ)
}

// Err は結果を操作のエラーにする(成功なら nil)。
func (r Result) Err() error {
	switch r.Code {
	case 0:
		return nil
	case 11:
		return &define.Error{Code: define.CodeUnavailable, Message: r.Out, Reasons: []string{ReasonHerdrUnavailable}}
	case 4:
		return &define.Error{Code: define.CodeFailedPrecondition, Message: r.Out, Reasons: []string{ReasonLimitExceeded}}
	case 10:
		return &define.Error{Code: define.CodeFailedPrecondition, Message: r.Out, Reasons: []string{ReasonPromptUnconfirmed}}
	case 12:
		return &define.Error{Code: define.CodeFailedPrecondition, Message: r.Out, Reasons: []string{ReasonKindUnsupported}}
	}
	return &define.Error{Code: define.CodeFailedPrecondition, Message: r.Out}
}

// opGate は agent の操作 op(stop・revive など)の門: 人は通す。agent は policy の agent:<op>:<名前> で、
// allow は通す、deny は却下の行を残して拒否、ask は承認待ち(agent_ops の pending の行、L-<n>)に積む。
// args は承認後の実行に使う引数(agent_ops.args の JSON)。pre は判定の前の確かめ(--force は人だけ、など)。
func opGate[In any](op string, target func(In) string, args func(In) string, pre func(who string, in In) error) app.Gate {
	return opGateRole(op, target, args, pre, nil)
}

// opGateRole は opGate に、policy の @<役割> を比べる役割(role。空なら台帳の役割)を足したもの。
// 作成(new)は、まだ台帳に無い agent を要求された役割(--role)で判定する。
func opGateRole[In any](op string, target func(In) string, args func(In) string, pre func(who string, in In) error, role func(In) string) app.Gate {
	return func(_ context.Context, env *app.Env, who string, v any) app.Decision {
		in := v.(In)
		if pre != nil {
			if err := pre(who, in); err != nil {
				return app.Decision{Err: err}
			}
		}
		if who == "human" {
			return app.Decision{}
		}
		db, done, err := env.DB()
		if err != nil {
			return app.Decision{Err: define.Errorf(define.CodeInternal, "%s", err.Error())}
		}
		defer done()
		t := target(in)
		j := approval.NewJudge(db.DB, db.Session)
		if role != nil {
			if r := role(in); r != "" {
				j.Roles.Hint(t, r)
			}
		}
		val, rule := j.Of("agent:"+op+":"+t, who)
		switch val {
		case "allow":
			return app.Decision{}
		case "deny":
			ts := env.NowString()
			_, _ = db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at) VALUES (?,?,?,?,?, 'rejected', ?, ?, ?)`,
				db.Session, op, t, args(in), who, "認可: "+rule, ts, ts)
			return app.Decision{Err: &define.Error{Code: define.CodePermissionDenied, Message: "agent:" + op + " " + t + " は実行できません(deny。" + rule + ")", Reasons: []string{ReasonOpDenied}}}
		}
		res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?,?,?,?,?, 'pending', ?, ?)`,
			db.Session, op, t, args(in), who, "認可: "+rule, env.NowString())
		if err != nil {
			return app.Decision{Err: define.Errorf(define.CodeInternal, "%s", err.Error())}
		}
		id, _ := res.LastInsertId()
		q := env.QOf(db.DB, "agent_ops", id)
		lid := "L-" + strconv.FormatInt(id, 10)
		return app.Decision{Pending: &app.Pending{Route: "L", ID: lid, Message: lid + " pending " + q + " (承認待ち: tb approval approve " + q + ")"}}
	}
}

// withDB は env の DB で f を呼ぶ。
func withDB[T any](env *app.Env, f func(*store.DB) (T, error)) (T, error) {
	var zero T
	db, done, err := env.DB()
	if err != nil {
		return zero, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	defer done()
	return f(db)
}

// by は操作の主体(perms の書き方)。門を通ったあとなので必ずある。
func by(ctx context.Context) string {
	p, _ := define.PrincipalFrom(ctx)
	return app.Who(p)
}

// Ops は agent の操作。
func Ops(env *app.Env) []app.Op {
	return []app.Op{newOp(env), listOp(env), stopOp(env), reviveOp(env), restartOp(env), parentOp(env, true), parentOp(env, false), spawnOp(env), decideOp(env, true), decideOp(env, false)}
}

// ValidName は agent 名として使えるか(英小文字で始まる 32 字以内。human は予約名)。
func ValidName(n string) bool { return n != "human" && agents.NameRE.MatchString(n) }

// validName は agent 名の検査(tb の validAgentName と同じ文言)。
func validName(n string) error {
	if n == "human" || !agents.NameRE.MatchString(n) {
		return define.Errorf(define.CodeInvalidArgument, "agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): %s", n)
	}
	return nil
}

// limitErr は RefuseRunning が書いた理由(「tb: …」の 1 行)を上限のエラーにする。
func limitErr(b *bytes.Buffer) error {
	msg := strings.TrimPrefix(strings.TrimRight(b.String(), "\n"), "tb: ")
	return &define.Error{Code: define.CodeFailedPrecondition, Message: msg, Reasons: []string{ReasonLimitExceeded}}
}
