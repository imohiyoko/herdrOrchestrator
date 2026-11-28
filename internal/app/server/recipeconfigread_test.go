package server

import (
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appagent "github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	appconfig "github.com/imohiyoko/herdrorchestrator/internal/app/config"
	apprecipe "github.com/imohiyoko/herdrorchestrator/internal/app/recipe"
)

func TestRecipeConfigRoleReadOps(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	if _, e := callOp[apprecipe.TextResult](t, env, app.Human(), "recipe.show", apprecipe.NameReq{Name: "nope"}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("recipe.show の無い名前: %v", e)
	}
	if _, e := callOp[apprecipe.TextResult](t, env, app.Human(), "recipe.show", apprecipe.NameReq{Name: "Bad:x"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("recipe.show の不正な名前: %v", e)
	}
	for op, in := range map[string]any{"recipe.list": apprecipe.ListReq{}, "recipe.history": apprecipe.HistoryReq{JSON: true}, "recipe.queue": apprecipe.QueueReq{All: true}} {
		if r, e := callOp[apprecipe.TextResult](t, env, app.Agent("w1"), op, in); e != nil || r.Text != "" {
			t.Errorf("%s の空: %q %v", op, r.Text, e)
		}
	}
	if _, e := callOp[appconfig.HistoryResult](t, env, app.Human(), "config.history", appconfig.HistoryReq{Key: "nope"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("config.history の不明なキー: %v", e)
	}
	if r, e := callOp[appagent.RoleLogResult](t, env, app.Human(), "agent.role.log", appagent.RoleLogReq{}); e != nil || r.Text != "" {
		t.Errorf("agent.role.log の空: %q %v", r.Text, e)
	}
}
