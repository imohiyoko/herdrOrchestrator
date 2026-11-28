package server

import (
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appmessage "github.com/imohiyoko/herdrorchestrator/internal/app/message"
)

func TestMessageReadOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if _, e := callOp[appmessage.TextResult](t, env, app.Human(), "message.outbox", appmessage.OutboxReq{Status: "x"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("outbox の status: %v", e)
	}
	if _, e := callOp[appmessage.TextResult](t, env, app.Human(), "message.outbox", appmessage.OutboxReq{JSON: true}); e != nil {
		t.Errorf("outbox: %v", e)
	}
	if r, e := callOp[appmessage.TextResult](t, env, app.Agent("nobody"), "message.inbox", appmessage.InboxReq{}); e != nil || r.Text != "" {
		t.Errorf("inbox の空: %q %v", r.Text, e)
	}
	if _, e := callOp[appmessage.TextResult](t, env, app.Agent("nobody"), "message.inbox.get", appmessage.InboxGetReq{ID: "I-1"}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("inbox.get の他人の行: %v", e)
	}
}
