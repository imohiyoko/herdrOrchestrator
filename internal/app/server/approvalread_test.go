package server

import (
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appapproval "github.com/imohiyoko/herdrorchestrator/internal/app/approval"
)

func TestApprovalReadOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if r, e := callOp[appapproval.TextResult](t, env, app.Human(), "approval.list", appapproval.ListReq{}); e != nil || r.Text != "" {
		t.Errorf("list の空: %q %v", r.Text, e)
	}
	if r, e := callOp[appapproval.TextResult](t, env, app.Agent("w1"), "approval.list", appapproval.ListReq{Mine: true}); e != nil || r.Text != "" {
		t.Errorf("list --mine の空: %q %v", r.Text, e)
	}
	if _, e := callOp[appapproval.TextResult](t, env, app.Human(), "approval.history", appapproval.HistoryReq{Limit: 1001}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("history の limit: %v", e)
	}
	if _, e := callOp[appapproval.TextResult](t, env, app.Agent("w1"), "approval.history", appapproval.HistoryReq{Mine: true, JSON: true}); e != nil {
		t.Errorf("history --mine: %v", e)
	}
}
