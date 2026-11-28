package server

import (
	"strings"
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
	db, done, err := env.DB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO main.inbox (herdr_session, outbox_id, recipient, from_name, kind, text, rule, created_at) VALUES ('default', 1, 'w2', 'w1', 'info', 'secret', 'r', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	done()
	if _, e := callOp[appmessage.TextResult](t, env, app.Agent("nobody"), "message.inbox.get", appmessage.InboxGetReq{ID: "I-1"}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("inbox.get の他人の行: %v", e)
	}
	if r, e := callOp[appmessage.TextResult](t, env, app.Agent("nobody"), "message.inbox", appmessage.InboxReq{}); e != nil || r.Text != "" {
		t.Errorf("inbox の他人の行: %q %v", r.Text, e)
	}
	if r, e := callOp[appmessage.TextResult](t, env, app.Agent("w2"), "message.inbox.get", appmessage.InboxGetReq{ID: "I-1"}); e != nil || !strings.HasSuffix(r.Text, "secret\n") {
		t.Errorf("inbox.get の自分の行: %q %v", r.Text, e)
	}
	if _, e := callOp[appmessage.TextResult](t, env, app.Agent("w2"), "message.inbox.get", appmessage.InboxGetReq{ID: "I-+1"}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("inbox.get の +1: %v", e)
	}
}
