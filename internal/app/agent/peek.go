package agent

import (
	"context"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// PeekReq は agent.peek の入力(tb peek)。Lines は 1〜200(0 なら 40)。
type PeekReq struct {
	Name  string `api:"name"`
	Lines int    `api:"lines,optional"`
}

// PeekResult は pane の直近の出力(末尾の改行は落とす)。
type PeekResult struct {
	Text string `api:"text" json:"text"`
}

// ValidPeek は agent.peek の入力の確かめ(tb は開く前に呼ぶ)。
func ValidPeek(in PeekReq) error {
	if in.Lines < 0 || in.Lines > 200 {
		return define.Errorf(define.CodeInvalidArgument, "peek: --lines は 1〜200")
	}
	if !agents.NameRE.MatchString(in.Name) {
		return define.Errorf(define.CodeInvalidArgument, "agent 名に使えない文字があります: %s", in.Name)
	}
	return nil
}

func peekOp(env *app.Env) app.Op {
	return app.Command(env, "agent.peek", app.Entry("cmd:agent:peek", "tb peek"),
		func(ctx context.Context, in PeekReq) (PeekResult, error) {
			if err := ValidPeek(in); err != nil {
				return PeekResult{}, err
			}
			who := by(ctx)
			if who == "" {
				return PeekResult{}, define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
			}
			if in.Lines == 0 {
				in.Lines = 40
			}
			return withDB(env, func(db *store.DB) (PeekResult, error) { return peek(env, db, who, in) })
		},
		define.Doc("agent の pane の直近の出力を読む(tb peek。人と本人はそのまま、ほかは policy の agent:read:<対象> が allow のときだけ。読んだ記録は read_log)"),
		define.Authz("cmd:agent:peek"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/peek"),
	)
}

func peek(env *app.Env, db *store.DB, who string, in PeekReq) (PeekResult, error) {
	name := in.Name
	logRead := func(status, rule string) {
		var r any
		if rule != "" || status == "denied" {
			r = rule
		}
		_, _ = db.Exec(`INSERT INTO main.read_log (herdr_session, reader, target, lines, status, rule, created_at) VALUES (?,?,?,?,?,?,?)`,
			db.Session, who, name, in.Lines, status, r, env.NowString())
	}
	rule := ""
	if who != "human" && who != name {
		var v string
		v, rule = approval.NewJudge(db.DB, db.Session).Of("agent:read:"+name, who)
		if v != "allow" {
			logRead("denied", rule)
			e := &define.Error{Code: define.CodePermissionDenied, Reasons: []string{app.ReasonApprovalRequired},
				Message: who + " は " + name + " の中身を読めません(" + rule + ")。必要なら人が policy に '<主体> agent:read " + name + " = allow'(対象は @<役割> や ~child なども書ける)を足す"}
			if v == "deny" {
				e.Reasons = []string{ReasonOpDenied}
			}
			return PeekResult{}, e
		}
	}
	as, err := herdr.Agents()
	if err != nil {
		logRead("failed", rule)
		return PeekResult{}, define.Errorf(define.CodeUnavailable, "herdr agent list に失敗しました")
	}
	pane := ""
	for _, a := range as {
		if a.Name == name {
			pane = a.PaneID
			break
		}
	}
	if pane == "" {
		logRead("failed", rule)
		return PeekResult{}, define.Errorf(define.CodeNotFound, "%s は herdr で動いていません", name)
	}
	out, err := herdr.Call("agent", "read", pane, "--source", "recent", "--lines", strconv.Itoa(in.Lines), "--format", "text")
	if err != nil {
		logRead("failed", rule)
		return PeekResult{}, define.Errorf(define.CodeUnavailable, "herdr agent read に失敗しました(%s)", name)
	}
	logRead("ok", rule)
	return PeekResult{Text: strings.TrimRight(string(out), "\n")}, nil
}
