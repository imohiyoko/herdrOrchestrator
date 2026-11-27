package server

import (
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appapproval "github.com/imohiyoko/herdrorchestrator/internal/app/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

func TestApprovalPriority(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	db, done, err := env.DB()
	if err != nil {
		t.Fatal(err)
	}
	id, err := requests.InsertCmd(db.DB, requests.CmdRow{Session: "default", Requester: "w1", Op: "cmd:ticket:cancel", Target: "*", Priority: 1,
		Detail: "tb cancel", Payload: `{"args":[]}`, Status: "pending", Tier: "ask", CreatedAt: "2026-03-01T00:00:00Z"})
	done()
	if err != nil {
		t.Fatal(err)
	}
	q := requests.Request{ID: id}.QID()
	// 依頼者でも人でもない agent は変えられない
	if _, e := callOp[appapproval.PriorityResult](t, env, app.Agent("w2"), "approval.priority", appapproval.PriorityReq{ID: q, Priority: "high"}); e == nil || e.Code != define.CodePermissionDenied {
		t.Errorf("他人: %v", e)
	}
	if _, e := callOp[appapproval.PriorityResult](t, env, app.Human(), "approval.priority", appapproval.PriorityReq{ID: q, Priority: "urgent"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("知らない優先度: %v", e)
	}
	res, e := callOp[appapproval.PriorityResult](t, env, app.Agent("w1"), "approval.priority", appapproval.PriorityReq{ID: q, Priority: "high"})
	if e != nil || res.ID != q || res.Priority != "high" {
		t.Fatalf("依頼者: %+v %v", res, e)
	}
	if got := q1(t, env, `SELECT priority FROM requests WHERE id=?`, id); got != "2" {
		t.Errorf("priority: %q", got)
	}
}
