package server

import (
	"strings"
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
)

func TestTicketReadOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if _, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.tree", ticket.TreeReq{}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("tree の引数なし: %v", e)
	}
	if _, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.tree", ticket.TreeReq{ID: "T-x-001"}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("tree の無いタスク: %v", e)
	}
	if _, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.events", ticket.EventsReq{Limit: 1000000}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("events の limit: %v", e)
	}
	if r, e := callOp[ticket.TextResult](t, env, app.Agent("w1"), "ticket.events", ticket.EventsReq{}); e != nil || r.Text != "" {
		t.Errorf("events の空: %q %v", r.Text, e)
	}
	if _, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.board", ticket.BoardReq{Project: "x"}); e == nil || e.Code != define.CodeFailedPrecondition {
		t.Errorf("board の active なし: %v", e)
	}
	if _, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.comments", ticket.CommentsReq{Kind: "nope"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("comments の kind: %v", e)
	}
	if _, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.comments", ticket.CommentsReq{ID: "C-x"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("comments の id: %v", e)
	}
	if r, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.comments", ticket.CommentsReq{ID: "C-1"}); e != nil || strings.Count(r.Text, "\n") != 1 || !strings.HasPrefix(r.Text, "C-1\t") {
		t.Errorf("comments の 1 件: %q %v", r.Text, e)
	}
	if _, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.pending", ticket.PendingReq{Mode: "xml"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("pending の mode: %v", e)
	}
	if r, e := callOp[ticket.TextResult](t, env, app.Human(), "ticket.pending", ticket.PendingReq{Mode: "ids"}); e != nil || !strings.Contains(r.Text, "C-1\n") {
		t.Errorf("pending の ids: %q %v", r.Text, e)
	}
	if r, e := callOp[ticket.StatusListResult](t, env, app.Human(), "ticket.status.list", ticket.StatusListReq{}); e != nil || r.Source == "" || !strings.Contains(r.Text, "todo") {
		t.Errorf("status list: %+v %v", r, e)
	}
}
