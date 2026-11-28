package server

import (
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appalert "github.com/imohiyoko/herdrorchestrator/internal/app/alert"
	appdigest "github.com/imohiyoko/herdrorchestrator/internal/app/digest"
)

func TestAlertDigestReadOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if _, e := callOp[appalert.TextResult](t, env, app.Human(), "alert.get", appalert.GetReq{ID: 1}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("alert.get の無い番号: %v", e)
	}
	if r, e := callOp[appalert.TextResult](t, env, app.Agent("w1"), "alert.list", appalert.ListReq{JSON: true}); e != nil || r.Text != "" {
		t.Errorf("alert.list の空: %q %v", r.Text, e)
	}
	if _, e := callOp[appdigest.ListResult](t, env, app.Human(), "digest.list", appdigest.ListReq{ID: 9}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("digest.list の無い番号: %v", e)
	}
	if r, e := callOp[appdigest.ListResult](t, env, app.Human(), "digest.list", appdigest.ListReq{}); e != nil || r.Text != "" {
		t.Errorf("digest.list の空: %q %v", r.Text, e)
	}
}
