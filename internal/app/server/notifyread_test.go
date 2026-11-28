package server

import (
	"strings"
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appnotify "github.com/imohiyoko/herdrorchestrator/internal/app/notify"
)

func TestNotifyReadOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if r, e := callOp[appnotify.TextResult](t, env, app.Human(), "notify.status", appnotify.StatusReq{}); e != nil || !strings.HasPrefix(r.Text, "enabled: ") {
		t.Errorf("status: %q %v", r.Text, e)
	}
	if r, e := callOp[appnotify.TextResult](t, env, app.Human(), "notify.dryrun", appnotify.DryRunReq{}); e != nil || !strings.Contains(r.Text, "# dry-run:") {
		t.Errorf("dry-run: %q %v", r.Text, e)
	}
	if _, e := callOp[appnotify.TextResult](t, env, app.Human(), "notify.events", appnotify.EventsReq{Status: "A"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("events の status: %v", e)
	}
	if _, e := callOp[appnotify.TextResult](t, env, app.Human(), "notify.rules.test", appnotify.RulesTestReq{ID: "E-999999"}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("rules test の無いイベント: %v", e)
	}
}
