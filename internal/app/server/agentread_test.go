package server

import (
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appagent "github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

func TestAgentReadOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if _, e := callOp[appagent.ShedResult](t, env, app.Human(), "agent.shed", appagent.ShedReq{Count: -1}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("agent.shed の負の数: %v", e)
	}
	if r, e := callOp[appagent.OpsResult](t, env, app.Human(), "agent.ops", appagent.OpsReq{JSON: true}); e != nil || r.Text != "" {
		t.Errorf("agent.ops の空: %q %v", r.Text, e)
	}
	if r, e := callOp[appagent.LedgerResult](t, env, app.Human(), "agent.ledger", appagent.LedgerReq{All: true}); e != nil || r.Rows != "" {
		t.Errorf("agent.ledger の空: %q %v", r.Rows, e)
	}
	if r, e := callOp[appagent.ShedResult](t, env, app.Human(), "agent.shed", appagent.ShedReq{}); e != nil || len(r.Rows) != 0 {
		t.Errorf("agent.shed の空: %v %v", r.Rows, e)
	}
	t.Setenv("TB_ROOT", t.TempDir())
	if _, e := callOp[appagent.GraphResult](t, env, app.Human(), "agent.graph", appagent.GraphReq{}); e == nil || e.Message != "関係を読めません(DB)" {
		t.Errorf("agent.graph の SQL が無い: %v", e)
	}
	t.Setenv("TB_ROOT", "../../../taskboard")
	if r, e := callOp[appagent.GraphResult](t, env, app.Human(), "agent.graph", appagent.GraphReq{}); e != nil || len(r.Nodes) != 0 || len(r.Edges) != 0 {
		t.Errorf("agent.graph の空: %+v %v", r, e)
	}
}
