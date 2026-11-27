package server

import (
	"strings"
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/recipe"
)

const testRecipe = "risk high\nstep agent-status target=$target"

func TestRecipeSetRm(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	res, e := callOp[recipe.SetResult](t, env, app.Human(), "recipe.set", recipe.SetReq{Name: "r1", Body: testRecipe})
	if e != nil || !res.Added {
		t.Fatalf("set: %+v %v", res, e)
	}
	if res, e := callOp[recipe.SetResult](t, env, app.Human(), "recipe.set", recipe.SetReq{Name: "r1", Body: testRecipe}); e != nil || res.Added {
		t.Fatalf("update: %+v %v", res, e)
	}
	if got := q1(t, env, `SELECT group_concat(how||':'||changed_by, ' ') FROM recipe_changes WHERE name='r1'`); got != "add:human update:human" {
		t.Errorf("履歴: %q", got)
	}
	if _, e := callOp[recipe.SetResult](t, env, app.Human(), "recipe.set", recipe.SetReq{Name: "Bad", Body: testRecipe}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("名前の誤り: %v", e)
	}
	// agent は inner の行(既定 deny)で断られる
	if _, e := callOp[recipe.SetResult](t, env, app.Agent("w1"), "recipe.set", recipe.SetReq{Name: "r2", Body: testRecipe}); e == nil || e.Code != define.CodePermissionDenied {
		t.Errorf("agent の set: %v", e)
	}
	if _, e := callOp[recipe.RmResult](t, env, app.Agent("w1"), "recipe.rm", recipe.NameReq{Name: "r1"}); e == nil || e.Code != define.CodePermissionDenied {
		t.Errorf("agent の rm: %v", e)
	}
	if _, e := callOp[recipe.RmResult](t, env, app.Human(), "recipe.rm", recipe.NameReq{Name: "r1"}); e != nil {
		t.Fatalf("rm: %v", e)
	}
	if _, e := callOp[recipe.RmResult](t, env, app.Human(), "recipe.rm", recipe.NameReq{Name: "r1"}); e == nil || e.Code != define.CodeNotFound {
		t.Errorf("無いレシピ: %v", e)
	}
}

func TestRecipeCancel(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "")
	db, done, err := env.DB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO main.recipe_runs (id, herdr_session, name, body_sha, target, requester, via, task_id, plan, event_id, status, created_at)
		VALUES (1, 'default', 'r1', 'x', 't', 'w1', 'cli', 'T-p-001', '[]', 1, 'queued', '2026-01-01T00:00:00Z'),
		       (2, 'default', 'r1', 'x', 't', 'w1', 'cli', 'T-p-001', '[]', 1, 'queued', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	done()
	if _, e := callOp[recipe.CancelResult](t, env, app.Agent("w2"), "recipe.cancel", recipe.CancelReq{ID: "R-1"}); e == nil || e.Code != define.CodePermissionDenied || !strings.Contains(e.Message, "頼んだ agent(w1)") {
		t.Errorf("頼んでいない agent: %v", e)
	}
	if res, e := callOp[recipe.CancelResult](t, env, app.Agent("w1"), "recipe.cancel", recipe.CancelReq{ID: "R-1"}); e != nil || res.ID != "R-1" {
		t.Fatalf("頼んだ agent: %+v %v", res, e)
	}
	if got := q1(t, env, `SELECT status||' '||result FROM recipe_runs WHERE id=1`); got != "cancelled w1が取り消し" {
		t.Errorf("状態: %q", got)
	}
	if _, e := callOp[recipe.CancelResult](t, env, app.Agent("w1"), "recipe.cancel", recipe.CancelReq{ID: "R-1"}); e == nil || e.Code != define.CodeFailedPrecondition {
		t.Errorf("二度目: %v", e)
	}
	if _, e := callOp[recipe.CancelResult](t, env, app.Human(), "recipe.cancel", recipe.CancelReq{ID: "R-2"}); e != nil {
		t.Errorf("人: %v", e)
	}
	if got := q1(t, env, `SELECT actor FROM task_events WHERE kind='recipe_end' AND old='R-2'`); got != "human" {
		t.Errorf("記録の変えた者: %q", got)
	}
}

func TestRecipeRunGates(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, _ := fixture(t, "w1 cmd:recipe:runner * = ask\n")
	// 実行役を ask にした agent は、承認待ちか(積めないなら)拒否になる。内部の失敗にはしない
	res, e := callOp[recipe.RunnerResult](t, env, app.Agent("w1"), "recipe.runner", recipe.RunnerReq{})
	if (e == nil && res.Pending == nil) || (e != nil && e.Code == define.CodeInternal) {
		t.Fatalf("ask の runner: %+v %v", res, e)
	}
	if _, e := callOp[recipe.RunnerResult](t, env, app.Agent("w2"), "recipe.runner", recipe.RunnerReq{}); e == nil || e.Code != define.CodePermissionDenied {
		t.Errorf("既定の runner: %v", e)
	}
	if _, e := callOp[recipe.RunResult](t, env, app.Human(), "recipe.run", recipe.RunReq{Name: "r1"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("人の端末で --target なし: %v", e)
	}
	// 登録の無いレシピは実行せず 4
	run, e := callOp[recipe.RunResult](t, env, app.Human(), "recipe.run", recipe.RunReq{Name: "r1", Target: "w1"})
	if e != nil || run.Code != 4 || !strings.Contains(run.Err, "登録されていないレシピ") {
		t.Errorf("未登録: %+v %v", run, e)
	}
}
