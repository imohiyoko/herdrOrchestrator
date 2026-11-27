package server

import (
	"slices"
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

func TestAgentPeekRefused(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if _, e := callOp[agent.PeekResult](t, env, app.Human(), "agent.peek", agent.PeekReq{Name: "w1", Lines: 201}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("--lines の誤り: %v", e)
	}
	// 既定の policy では、ほかの agent は読めない(承認待ちと同じ扱いで 20)。断ったことも read_log に残る
	_, e := callOp[agent.PeekResult](t, env, app.Agent("w2"), "agent.peek", agent.PeekReq{Name: "w1"})
	if e == nil || e.Code != define.CodePermissionDenied || !slices.Contains(e.Reasons, app.ReasonApprovalRequired) {
		t.Fatalf("他人の peek: %v", e)
	}
	if got := q1(t, env, `SELECT reader||':'||target||':'||lines||':'||status FROM read_log`); got != "w2:w1:40:denied" {
		t.Errorf("read_log: %q", got)
	}
}
