package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
)

// ticketFixture は fixture に、チケット T-p-001(in_progress)・T-p-002(親)・T-p-003(T-p-002 の子)を足したもの。
func ticketFixture(t *testing.T, policy string) *app.Env {
	t.Helper()
	env, _ := fixture(t, policy)
	db, done, err := env.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if _, err := db.Exec(`INSERT INTO main.tasks (id, project, title, status, parent_id, created_at, updated_at, herdr_session) VALUES
		('T-p-001', 'p', 'a', 'in_progress', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'default'),
		('T-p-002', 'p', '親', 'todo', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'default'),
		('T-p-003', 'p', '子', 'todo', 'T-p-002', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', 'default')`); err != nil {
		t.Fatal(err)
	}
	return env
}

func callOp[Out any](t *testing.T, env *app.Env, by port.Principal, op string, in any) (Out, *define.Error) {
	t.Helper()
	c, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	var res Out
	err = c.Call(context.Background(), by, op, in, &res)
	if err == nil {
		return res, nil
	}
	var e *define.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a define.Error: %v", err)
	}
	return res, e
}

func q1(t *testing.T, env *app.Env, query string, args ...any) string {
	t.Helper()
	db, done, err := env.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var s string
	if err := db.QueryRow(query, args...).Scan(&s); err != nil {
		return ""
	}
	return s
}

func TestTicketMove(t *testing.T) {
	env := ticketFixture(t, "")
	res, e := callOp[ticket.MoveResult](t, env, app.Human(), "ticket.move", ticket.MoveReq{ID: "T-p-001", Status: "done", Message: "済んだ", SetMessage: true, Deliverables: []string{"a.md", " "}})
	if e != nil || !res.Changed || res.From != "in_progress" || res.To != "done" {
		t.Fatalf("move: %+v %v", res, e)
	}
	if got := q1(t, env, `SELECT status||' '||summary||' '||deliverables FROM tasks WHERE id='T-p-001'`); got != `done 済んだ ["a.md"]` {
		t.Errorf("書いた中身: %q", got)
	}
	if got := q1(t, env, `SELECT actor FROM task_events WHERE task_id='T-p-001' AND kind='status' ORDER BY id DESC LIMIT 1`); got != "human" {
		t.Errorf("履歴の変えた者: %q", got)
	}
	if res, e := callOp[ticket.MoveResult](t, env, app.Human(), "ticket.move", ticket.MoveReq{ID: "T-p-001", Status: "done"}); e != nil || res.Changed {
		t.Errorf("同じ状態は変えない: %+v %v", res, e)
	}
	for _, c := range []struct {
		in   ticket.MoveReq
		code define.Code
		msg  string
	}{
		{ticket.MoveReq{ID: "T-1", Status: "done"}, define.CodeInvalidArgument, "タスク ID は T-<project>-NNN 形式: T-1"},
		{ticket.MoveReq{ID: "T-p-001", Status: "nope"}, define.CodeInvalidArgument, "status は "},
		{ticket.MoveReq{ID: "T-p-001", Status: "todo", Deliverables: []string{"x"}}, define.CodeInvalidArgument, "-d(成果物)は done の分類へ動かすときだけ"},
		{ticket.MoveReq{ID: "T-p-404", Status: "todo"}, define.CodeNotFound, "タスクが見つかりません: T-p-404"},
	} {
		if _, e := callOp[ticket.MoveResult](t, env, app.Human(), "ticket.move", c.in); e == nil || e.Code != c.code || !strings.Contains(e.Message, c.msg) {
			t.Errorf("%+v: %v", c.in, e)
		}
	}

	// agent の主体なら、履歴には agent の名前が残る
	if _, e := callOp[ticket.MoveResult](t, env, app.Agent("w1"), "ticket.move", ticket.MoveReq{ID: "T-p-002", Status: "in_progress"}); e != nil {
		t.Fatal(e)
	}
	if got := q1(t, env, `SELECT actor FROM task_events WHERE task_id='T-p-002' AND kind='status' ORDER BY id DESC LIMIT 1`); got != "w1" {
		t.Errorf("agent の履歴: %q", got)
	}
}

func TestTicketMovePolicy(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env := ticketFixture(t, "w1 cmd:ticket:move * = deny\n")
	if _, e := callOp[ticket.MoveResult](t, env, app.Agent("w1"), "ticket.move", ticket.MoveReq{ID: "T-p-001", Status: "todo"}); e == nil || e.Code != define.CodePermissionDenied {
		t.Fatalf("deny: %v", e)
	}
	if q1(t, env, `SELECT status FROM tasks WHERE id='T-p-001'`) != "in_progress" {
		t.Fatal("deny なのに書いた")
	}
}

func TestTicketRm(t *testing.T) {
	env := ticketFixture(t, "")
	if _, e := callOp[ticket.RmResult](t, env, app.Human(), "ticket.rm", ticket.RmReq{IDs: []string{"T-p-002"}}); e == nil || e.Code != define.CodeFailedPrecondition || !strings.Contains(e.Message, "子チケットがあります: T-p-003") {
		t.Fatalf("子があれば断る: %v", e)
	}
	if _, e := callOp[ticket.RmResult](t, env, app.Human(), "ticket.rm", ticket.RmReq{IDs: []string{"T-p-003"}, IfCreated: "2000-01-01T00:00:00Z", HasIfCreated: true}); e == nil || !strings.Contains(e.Message, "開き直して") {
		t.Fatalf("作成時刻が違えば断る: %v", e)
	}
	if _, e := callOp[ticket.RmResult](t, env, app.Human(), "ticket.rm", ticket.RmReq{IDs: []string{"T-p-002", "T-p-003"}, HasIfCreated: true}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Fatalf("--if-created は 1 枚だけ: %v", e)
	}
	res, e := callOp[ticket.RmResult](t, env, app.Human(), "ticket.rm", ticket.RmReq{IDs: []string{"T-p-003", "T-p-002", "T-p-003"}})
	if e != nil || len(res.Removed) != 2 || res.Removed[0].ID != "T-p-003" || res.Removed[1].ID != "T-p-002" {
		t.Fatalf("子と一緒に消す: %+v %v", res, e)
	}
	if q1(t, env, `SELECT COUNT(*) FROM tasks WHERE id IN ('T-p-002','T-p-003')`) != "0" {
		t.Error("消えていない")
	}
}

func TestTicketRmAgentAsk(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env := ticketFixture(t, "")
	var queued []string
	perms.Queue = func(op, target, rule, what string) (string, error) {
		queued = append(queued, op+" "+what)
		return "Q-9", nil
	}
	defer func() { perms.Queue = nil }()
	// ticket.rm は agent からは ask(commands.tbl の既定)。Web UI や将来の API から agent として呼んでも承認待ちになる
	res, e := callOp[ticket.RmResult](t, env, app.Agent("w1"), "ticket.rm", ticket.RmReq{IDs: []string{"T-p-001"}})
	if e != nil || res.Pending == nil || res.Pending.ID != "Q-9" || len(queued) != 1 || queued[0] != "cmd:ticket:rm tb ticket rm" {
		t.Fatalf("ask: %+v %v %v", res, e, queued)
	}
	if q1(t, env, `SELECT COUNT(*) FROM tasks WHERE id='T-p-001'`) != "1" {
		t.Fatal("ask なのに消した")
	}
	// tb の入口(入口の段を判定済み)からは二重に判定しない
	env.EntryChecked = true
	if res, e := callOp[ticket.RmResult](t, env, app.Agent("w1"), "ticket.rm", ticket.RmReq{IDs: []string{"T-p-001"}}); e != nil || res.Pending != nil || len(queued) != 1 {
		t.Fatalf("EntryChecked: %+v %v %v", res, e, queued)
	}
}
