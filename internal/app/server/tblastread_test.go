package server

import (
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appagent "github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	appreminder "github.com/imohiyoko/herdrorchestrator/internal/app/reminder"
	appticket "github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
)

func TestTicketListLimitsDeadlinesOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if _, e := callOp[appticket.TextResult](t, env, app.Human(), "ticket.list", appticket.ListReq{Sort: "nope"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("ticket.list の不明な並び: %v", e)
	}
	if _, e := callOp[appticket.TextResult](t, env, app.Human(), "ticket.list", appticket.ListReq{Overdue: true}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("ticket.list の基準日なし: %v", e)
	}
	if _, e := callOp[appticket.TextResult](t, env, app.Human(), "ticket.list", appticket.ListReq{DueWithin: "x", Today: "2026-01-01"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("ticket.list の不正な日数: %v", e)
	}
	in := appticket.ListReq{Types: "task", Labels: "a,b", Sprints: "backlog", DueWithin: "7", Overdue: true, HasDue: true, Today: "2026-01-01", Format: "json"}
	if r, e := callOp[appticket.TextResult](t, env, app.Agent("w1"), "ticket.list", in); e != nil || r.Text != "" {
		t.Errorf("ticket.list の空: %q %v", r.Text, e)
	}
	if r, e := callOp[appagent.LimitsShowResult](t, env, app.Human(), "agent.limits.show", appagent.LimitsShowReq{JSON: true}); e != nil || r.Text == "" {
		t.Errorf("agent.limits.show: %+v %v", r, e)
	}
	t.Setenv("TB_REMINDERS_DEADLINES", t.TempDir()+"/none.conf")
	if r, e := callOp[appreminder.DeadlinesResult](t, env, app.Human(), "reminder.deadlines", appreminder.DeadlinesReq{}); e != nil || !r.Missing {
		t.Errorf("reminder.deadlines のファイルなし: %+v %v", r, e)
	}
}
