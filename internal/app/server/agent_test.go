package server

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

func stopCall(t *testing.T, env *app.Env, by port.Principal, in agent.StopReq) (agent.StopResult, *define.Error) {
	t.Helper()
	c, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	var res agent.StopResult
	if err := c.Call(context.Background(), by, "agent.stop", in, &res); err != nil {
		var e *define.Error
		if !errors.As(err, &e) {
			t.Fatalf("not a define.Error: %v", err)
		}
		return res, e
	}
	return res, nil
}

// opsRows は agent_ops の (status, requested_by) の並び。
func opsRows(t *testing.T, env *app.Env) []string {
	t.Helper()
	db, done, err := env.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	rows, err := db.Query(`SELECT status || ' ' || requested_by FROM main.agent_ops ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// agent.stop の門: agent は policy の agent:stop:<名前> で決まり、ask は L-<n>、deny は却下の行を残して拒否する。
// 結果(実行した・失敗した)も agent_ops に残す。herdr は呼べないようにしてある(呼んだら 11)。
func TestAgentStopGate(t *testing.T) {
	t.Setenv("HERDR_BIN", "/nonexistent/herdr")
	env, _ := fixture(t, "w1 agent:stop w2 = ask\nw1 agent:stop w3 = deny\nw1 agent:stop w4 = allow\n")

	res, e := stopCall(t, env, app.Agent("w1"), agent.StopReq{Name: "w2"})
	if e != nil || res.Pending == nil || res.Pending.Route != "L" || res.Pending.ID != "L-1" {
		t.Fatalf("ask: %+v %v", res, e)
	}
	if _, e := stopCall(t, env, app.Agent("w1"), agent.StopReq{Name: "w3"}); e == nil || e.Code != define.CodePermissionDenied ||
		!slices.Contains(e.Reasons, agent.ReasonOpDenied) {
		t.Fatalf("deny: %v", e)
	}
	if _, e := stopCall(t, env, app.Agent("w1"), agent.StopReq{Name: "w4"}); e == nil || !slices.Contains(e.Reasons, agent.ReasonHerdrUnavailable) {
		t.Fatalf("allow → 実行して herdr で失敗: %v", e)
	}
	if _, e := stopCall(t, env, app.Agent("w1"), agent.StopReq{Name: "w4", Force: true}); e == nil || e.Code != define.CodeFailedPrecondition ||
		e.Message != "--force は人が自分の端末で使います(agent 'w1' の pane からは使えません)" {
		t.Fatalf("agent の --force: %v", e)
	}
	if _, e := stopCall(t, env, app.Human(), agent.StopReq{Name: "Bad"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Fatalf("名前: %v", e)
	}
	if _, e := stopCall(t, env, app.Human(), agent.StopReq{Name: "w5", Force: true}); e == nil || !slices.Contains(e.Reasons, agent.ReasonHerdrUnavailable) {
		t.Fatalf("人は門を通る: %v", e)
	}
	want := []string{"pending w1", "rejected w1", "failed w1", "failed human"}
	if got := opsRows(t, env); !slices.Equal(got, want) {
		t.Errorf("agent_ops: %q, want %q", got, want)
	}
}

// agent 名 human は予約名(人と区別できない)なので、agent の主体にならない。
func TestAgentNamedHuman(t *testing.T) {
	if app.Who(app.Agent("human")) != "" || app.Who(app.Agent("system:x")) != "" {
		t.Error("agent 名 human / system: が主体になる")
	}
}
