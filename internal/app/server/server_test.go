package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// fixture は一時 DB(報告 C-1 question、C-2 progress、C-3 resolved の blocked、C-4 ほかのセッション)と policy のファイル。
func fixture(t *testing.T, policy string) (*app.Env, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TB_CONFIG_DIR", dir)
	pol := filepath.Join(dir, "policy")
	if err := os.WriteFile(pol, []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TB_POLICY", pol)
	dbp := filepath.Join(dir, "tasks.db")
	db, err := store.Open(dbp, "default", &store.Options{NoSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at, resolved_at, herdr_session) VALUES
		('T-p-001', 'question', 'q', 'w1', 'p1', 's', '2026-01-01T00:00:00Z', NULL, 'default'),
		('T-p-001', 'progress', 'p', 'w1', 'p1', 's', '2026-01-01T00:00:00Z', NULL, 'default'),
		('T-p-001', 'blocked', 'b', 'w1', 'p1', 's', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z', 'default'),
		('T-p-001', 'question', 'o', 'w1', 'p1', 's', '2026-01-01T00:00:00Z', NULL, 'other')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return &app.Env{DB: app.OpenStore(dbp, "default", nil), Now: func() string { return "2026-03-01T00:00:00Z" }}, dbp
}

func resolvedAt(t *testing.T, env *app.Env, id string) string {
	t.Helper()
	db, done, err := env.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var at, note string
	db.QueryRow(`SELECT IFNULL(resolved_at,''), IFNULL(resolved_note,'') FROM main.comments WHERE id=`+id).Scan(&at, &note)
	return strings.TrimSpace(at + " " + note)
}

func call(t *testing.T, env *app.Env, by port.Principal, in ticket.ResolveReq) (ticket.ResolveResult, *define.Error) {
	t.Helper()
	c, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	var res ticket.ResolveResult
	err = c.Call(context.Background(), by, "ticket.resolve", in, &res)
	if err == nil {
		return res, nil
	}
	var e *define.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a define.Error: %v", err)
	}
	return res, e
}

func TestResolveHuman(t *testing.T) {
	env, _ := fixture(t, "")
	res, e := call(t, env, app.Human(), ticket.ResolveReq{ID: "1", Note: "決めた"})
	if e != nil || res.Already || res.ID != "1" || res.Pending != nil {
		t.Fatalf("resolve: %+v %v", res, e)
	}
	if got := resolvedAt(t, env, "1"); got != "2026-03-01T00:00:00Z 決めた" {
		t.Errorf("DB: %q", got)
	}
	if res, e := call(t, env, app.Human(), ticket.ResolveReq{ID: "1"}); e != nil || !res.Already || res.ResolvedAt != "2026-03-01T00:00:00Z" {
		t.Errorf("2 回目: %+v %v", res, e)
	}
	for _, c := range []struct {
		id   string
		code define.Code
		msg  string
	}{
		{"2", define.CodeFailedPrecondition, "対応済みにできるのは question / blocked だけです(C-2 は progress)"},
		{"4", define.CodeNotFound, "報告が見つかりません: C-4"}, // ほかのセッション
		{"99", define.CodeNotFound, "報告が見つかりません: C-99"},
		{"1 OR 1=1", define.CodeInvalidArgument, "resolve: ID は C-<数字>: 1 OR 1=1"},
	} {
		if _, e := call(t, env, app.Human(), ticket.ResolveReq{ID: c.id}); e == nil || e.Code != c.code || e.Message != c.msg {
			t.Errorf("%s: %v", c.id, e)
		}
	}
	if _, e := call(t, env, app.Human(), ticket.ResolveReq{ID: "3", Note: strings.Repeat("あ", 501)}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("長いメモ: %v", e)
	}
}

// agent は既定(deny)で止まり、ask なら承認待ち(Pending)、allow なら実行する。判定は主体だけで決め、pane を見ない。
func TestResolveAgent(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "") // 人の端末のプロセスからでも、主体が agent なら policy が効く
	env, _ := fixture(t, "")
	if _, e := call(t, env, app.Agent("w1"), ticket.ResolveReq{ID: "1"}); e == nil || e.Code != define.CodePermissionDenied ||
		e.Message != "tb resolve は人が自分の端末で実行します(agent 'w1' の pane からは実行できません)" {
		t.Fatalf("deny: %v", e)
	}
	if resolvedAt(t, env, "1") != "" {
		t.Fatal("deny なのに書いた")
	}

	os.WriteFile(os.Getenv("TB_POLICY"), []byte("w1 cmd:ticket:resolve * = ask\n"), 0o644)
	var queued []string
	perms.Queue = func(op, target, rule, what string) (string, error) {
		queued = append(queued, op+" "+what)
		return "Q-7", nil
	}
	res, e := call(t, env, app.Agent("w1"), ticket.ResolveReq{ID: "1"})
	if e != nil || res.Pending == nil || res.Pending.Route != "Q" || res.Pending.ID != "Q-7" ||
		!strings.Contains(res.Pending.Message, "承認待ち Q-7 に積みました") || !slices.Equal(queued, []string{"cmd:ticket:resolve tb resolve"}) {
		t.Fatalf("ask: %+v %v %v", res, e, queued)
	}
	perms.Queue = func(string, string, string, string) (string, error) { return "", errors.New("full") }
	if _, e := call(t, env, app.Agent("w1"), ticket.ResolveReq{ID: "1"}); e == nil || !slices.Contains(e.Reasons, app.ReasonApprovalRequired) {
		t.Errorf("ask で積めない: %v", e)
	}
	perms.Queue = nil
	if resolvedAt(t, env, "1") != "" {
		t.Fatal("ask なのに書いた")
	}

	os.WriteFile(os.Getenv("TB_POLICY"), []byte("w1 cmd:ticket:resolve * = allow\n"), 0o644)
	if res, e := call(t, env, app.Agent("w1"), ticket.ResolveReq{ID: "1"}); e != nil || res.Pending != nil || res.Already {
		t.Fatalf("allow: %+v %v", res, e)
	}
	if _, e := call(t, env, app.Agent("w2"), ticket.ResolveReq{ID: "1"}); e == nil || e.Code != define.CodePermissionDenied {
		t.Errorf("ほかの agent は既定のまま: %v", e)
	}
}

func TestSystemPrincipal(t *testing.T) {
	env, _ := fixture(t, "")
	// system: の行が無ければ人と同じく通る(ADR 0007)
	if _, e := call(t, env, app.System("sched"), ticket.ResolveReq{ID: "1"}); e != nil {
		t.Fatalf("system: %v", e)
	}
	env, _ = fixture(t, "system:sched cmd:ticket:resolve * = deny\n")
	if _, e := call(t, env, app.System("system:sched"), ticket.ResolveReq{ID: "1"}); e == nil || e.Code != define.CodePermissionDenied {
		t.Errorf("system: の deny: %v", e)
	}
}

func TestUnknownPrincipal(t *testing.T) {
	env, _ := fixture(t, "")
	for _, p := range []port.Principal{{Type: port.PrincipalUser, ID: "alice"}, {Type: port.PrincipalServiceAccount, ID: "agent:"}, {Type: "robot", ID: "human"}} {
		if _, e := call(t, env, p, ticket.ResolveReq{ID: "1"}); e == nil || e.Code != define.CodePermissionDenied {
			t.Errorf("%v: %v", p, e)
		}
	}
}

func TestTokens(t *testing.T) {
	var tk app.Tokens
	if _, err := tk.Authenticate(context.Background(), ""); !errors.Is(err, port.ErrUnauthenticated) {
		t.Error("空のトークン")
	}
	tok, revoke := tk.Issue(app.Agent("w1"))
	p, err := tk.Authenticate(context.Background(), tok)
	if err != nil || app.Who(p) != "w1" {
		t.Fatalf("issue: %v %v", p, err)
	}
	revoke()
	if _, err := tk.Authenticate(context.Background(), tok); !errors.Is(err, port.ErrUnauthenticated) {
		t.Error("取り消したトークンが使える")
	}
}

func TestPrincipalMapping(t *testing.T) {
	for _, who := range []string{"human", "w1", "system:sched"} {
		if got := app.Who(app.PrincipalOf(who)); got != who {
			t.Errorf("%s → %s", who, got)
		}
	}
	if app.Human().Type != port.PrincipalUser || app.Agent("w1").Type != port.PrincipalServiceAccount {
		t.Error("主体の種類")
	}
}

// すべての操作が Guard を通す(ADR 0008)。Guard を通さない操作は登録できない。
func TestAllOpsGuarded(t *testing.T) {
	ops := Ops(&app.Env{})
	if len(ops) == 0 {
		t.Fatal("操作が無い")
	}
	reg, err := app.Registry(ops...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := reg.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Operations) != len(ops) {
		t.Errorf("登録した操作 %d / %d", len(m.Operations), len(ops))
	}
	for _, op := range m.Operations {
		if op.Authz == nil || !strings.HasPrefix(op.Authz.Action, "cmd:") {
			t.Errorf("%s: Authz は commands.tbl の cmd: の操作名", op.Name)
		} else if _, ok := perms.RowOf(op.Authz.Action); !ok {
			t.Errorf("%s: %s は commands.tbl に無い", op.Name, op.Authz.Action)
		}
		if op.Anonymous {
			t.Errorf("%s: 匿名の操作は作らない", op.Name)
		}
	}
	if _, err := app.Registry(app.Op{}); err == nil {
		t.Error("Guard を通さない操作を登録できた")
	}
}

// 入口の段(commands.tbl の場所が entry の行)は Authorizer が deny だけ拒む。inner の行は Guard に任せる。
func TestAuthorizer(t *testing.T) {
	env, _ := fixture(t, "w1 cmd:ticket:show * = deny\nw2 cmd:ticket:show * = ask\nw1 cmd:ticket:resolve * = deny\n")
	a := app.Authorizer{Env: env}
	for _, c := range []struct {
		who, op string
		ok      bool
	}{
		{"w1", "cmd:ticket:show", false},
		{"w2", "cmd:ticket:show", true}, // ask は通す(Guard が承認待ちにする)
		{"w3", "cmd:ticket:show", true},
		{"human", "cmd:ticket:show", true},
		{"w1", "cmd:ticket:resolve", true}, // inner は Guard が判定する
		{"w1", "cmd:nope:nope", false},
		{"human", "cmd:nope:nope", false},
	} {
		d, err := a.Check(context.Background(), port.CheckRequest{Principal: app.PrincipalOf(c.who), Action: c.op})
		if err != nil || d.Allowed != c.ok {
			t.Errorf("%s %s: %+v %v", c.who, c.op, d, err)
		}
	}
}
