package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func lifeCall(t *testing.T, env *app.Env, by port.Principal, op string, in any) (agent.LifeResult, *define.Error) {
	t.Helper()
	c, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	var res agent.LifeResult
	if err := c.Call(context.Background(), by, op, in, &res); err != nil {
		var e *define.Error
		if !errors.As(err, &e) {
			t.Fatalf("not a define.Error: %v", err)
		}
		return res, e
	}
	return res, nil
}

// agent.revive の門(承認待ち・入力の検査)と、agent.restart の門(承認待ちにせず、stop と revive の両方の allow が要る)。
func TestReviveRestartGate(t *testing.T) {
	t.Setenv("HERDR_BIN", "/nonexistent/herdr")
	env, _ := fixture(t, "w1 agent:revive w2 = ask\nw1 agent:stop w3 = allow\n")
	res, e := lifeCall(t, env, app.Agent("w1"), "agent.revive", agent.ReviveReq{Name: "w2", Fresh: true, Prompt: "続けて <b>"})
	if e != nil || res.Pending == nil || res.Pending.Route != "L" {
		t.Fatalf("revive ask: %+v %v", res, e)
	}
	db, done, _ := env.DB()
	var args string
	db.QueryRow(`SELECT args FROM main.agent_ops WHERE status='pending'`).Scan(&args)
	done()
	if args != `{"fresh":1,"prompt":"続けて <b>"}` {
		t.Errorf("承認後の実行に使う引数: %s", args)
	}
	if _, e := lifeCall(t, env, app.Human(), "agent.revive", agent.ReviveReq{Name: "w2", Model: "a b"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("モデル名: %v", e)
	}
	for _, c := range []struct {
		by   port.Principal
		name string
		msg  string
	}{
		{app.Agent("w1"), "w1", "自分自身は再起動できません"},
		{app.Agent("w1"), "w3", "policy で agent:stop と agent:revive(w3)の両方が allow ではありません(stop=allow revive=ask)。人が実行するか、人が許可してください"},
	} {
		if _, e := lifeCall(t, env, c.by, "agent.restart", agent.RestartReq{Name: c.name}); e == nil || e.Message != c.msg ||
			!slices.Contains(e.Reasons, agent.ReasonOpDenied) {
			t.Errorf("restart %s: %v", c.name, e)
		}
	}
	if _, e := lifeCall(t, env, app.Human(), "agent.restart", agent.RestartReq{Name: "w3"}); e == nil || !slices.Contains(e.Reasons, agent.ReasonHerdrUnavailable) {
		t.Errorf("人の restart は門を通る: %v", e)
	}
	want := []string{"pending w1", "rejected w1", "rejected w1", "failed human"}
	if got := opsRows(t, env); !slices.Equal(got, want) {
		t.Errorf("agent_ops: %q, want %q", got, want)
	}
}

// agent.new の門: 入力の検査は tb と同じ文言、policy の @<役割> は要求された役割で比べる、
// --killable は agent:flag を許された agent だけ。承認待ちの引数は tb agent-new と同じ形。
func TestAgentNewGate(t *testing.T) {
	t.Setenv("HERDR_BIN", "/nonexistent/herdr")
	roles := t.TempDir()
	if err := os.WriteFile(filepath.Join(roles, "reviewer.md"), []byte("review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TB_ROLES", roles)
	env, _ := fixture(t, "w1 agent:new @reviewer = allow\nw1 agent:new w9 = deny\n")
	cwd := t.TempDir()
	for _, c := range []struct {
		in   agent.NewReq
		code define.Code
		msg  string
	}{
		{agent.NewReq{Name: "Bad"}, define.CodeInvalidArgument, "agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): Bad"},
		{agent.NewReq{Name: "w2", Kind: "x"}, define.CodeInvalidArgument, "agent-new: --kind は " + strings.Join(agent.KindAll, " ") + " のどれか: x"},
		{agent.NewReq{Name: "w2", Cwd: "rel"}, define.CodeInvalidArgument, "agent-new: --cwd は絶対パスにしてください: rel"},
		{agent.NewReq{Name: "w2", Role: "nope"}, define.CodeInvalidArgument, "agent-new: --role は定義のある役割名(tb role-list。カンマ区切りで複数可): nope"},
		{agent.NewReq{Name: "w2", Role: "a,b,c,d,e"}, define.CodeInvalidArgument, "agent-new: --role はカンマ区切りで 1〜4 個: a,b,c,d,e"},
		{agent.NewReq{Name: "reviewer", Role: "reviewer,reviewer"}, define.CodeInvalidArgument, "agent-new: 役割 reviewer の定義があるため、この名前の役割は reviewer に固定です(--role reviewer,reviewer は効きません)"},
		{agent.NewReq{Name: "w2", Killable: "maybe"}, define.CodeInvalidArgument, "agent-new: --killable は yes|no"},
		{agent.NewReq{Name: "w2", Killable: "yes"}, define.CodeFailedPrecondition, "agent-new --killable を付けられるのは人と、policy で agent:flag を許された agent だけです(w1)"},
		{agent.NewReq{Name: "w9", Cwd: cwd}, define.CodePermissionDenied, ""},
	} {
		_, e := lifeCall(t, env, app.Agent("w1"), "agent.new", c.in)
		if e == nil || e.Code != c.code || (c.msg != "" && e.Message != c.msg) {
			t.Errorf("%+v: %v", c.in, e)
		}
	}
	// 役割の付いた依頼は @reviewer の allow で門を通る(herdr が無いので起動は 11 で失敗し、台帳から消える)
	if _, e := lifeCall(t, env, app.Agent("w1"), "agent.new", agent.NewReq{Name: "w3", Cwd: cwd, Role: "reviewer"}); e == nil || !slices.Contains(e.Reasons, agent.ReasonHerdrUnavailable) {
		t.Errorf("@reviewer の allow: %v", e)
	}
	res, e := lifeCall(t, env, app.Agent("w1"), "agent.new", agent.NewReq{Name: "w4", Cwd: cwd, Prompt: "やって <b>"})
	if e != nil || res.Pending == nil || res.Pending.Route != "L" {
		t.Fatalf("ask: %+v %v", res, e)
	}
	db, done, _ := env.DB()
	var args string
	var n int
	db.QueryRow(`SELECT args FROM main.agent_ops WHERE status='pending'`).Scan(&args)
	db.QueryRow(`SELECT count(*) FROM main.agents`).Scan(&n)
	done()
	var got struct{ Cwd, Prompt, Killable string }
	if err := json.Unmarshal([]byte(args), &got); err != nil || (got.Cwd != cwd && got.Cwd != agent.PhysDir(cwd)) || got.Prompt != "やって <b>" || got.Killable != "unknown" ||
		!strings.HasPrefix(args, `{"cwd":`) || !strings.HasSuffix(args, `"prompt":"やって <b>","killable":"unknown"}`) {
		t.Errorf("承認後の実行に使う引数: %s", args)
	}
	if n != 0 {
		t.Errorf("起動に失敗した agent が台帳に残る: %d", n)
	}
	want := []string{"rejected w1", "failed w1", "pending w1"}
	if got := opsRows(t, env); !slices.Equal(got, want) {
		t.Errorf("agent_ops: %q, want %q", got, want)
	}
}
