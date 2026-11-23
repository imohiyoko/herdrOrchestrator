package webui

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// 移し終えた画面が Python 版(webui/app/views.py)と同じ HTML を返すかを、同じ DB・同じ疑似 herdr・同じログで確かめる。

const fixtureSQL = `
INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES
 ('default', 'statuses', 'todo todo
wip active = 作業中
done done
shelved cancelled # 棚上げ', 'human', '2026-01-01T00:00:00Z');
INSERT INTO tasks (id, project, title, status, created_at, updated_at, herdr_session) VALUES
 ('T-p-1', 'p', '<b>open</b> task', 'wip', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'default'),
 ('T-p-2', 'p', 'shelved task', 'shelved', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'default'),
 ('T-p-3', 'p', 'other session task', 'todo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'other'),
 ('T-a-9', 'p', 'alert task', 'todo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'default'),
 ('T-a-10', 'p', 'alert task 2', 'todo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'default');
INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at, herdr_session) VALUES
 ('T-p-1', 'question', 'どうする? <script>x</script> ` + "\x1b[31m" + `red', 'alpha', 'p1', 's', '2026-01-02T00:00:00Z', 'default'),
 ('T-p-1', 'blocked', '止まった ' || replace(hex(zeroblob(250)), '00', 'あ'), 'beta', 'p2', 's', '2026-01-02 01:00:00', 'default'),
 ('T-p-2', 'question', 'closed task question', 'alpha', 'p1', 's', '2026-01-02T00:00:00Z', 'default'),
 ('T-x-1', 'question', 'no task row', 'gamma', 'p9', 's', '2026-01-03T00:00:00+09:00', 'default'),
 ('T-p-3', 'question', 'other session', 'alpha', 'p1', 's', '2026-01-02T00:00:00Z', 'other');
INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at, resolved_at, herdr_session) VALUES
 ('T-p-1', 'question', 'resolved', 'alpha', 'p1', 's', '2026-01-02T00:00:00Z', '2026-01-02T01:00:00Z', 'default');
INSERT INTO proxy_requests (task_id, cmd, why, effect, status, created_at, updated_at, herdr_session) VALUES
 ('T-p-1', 'git push origin main', 'CI を回す & 確かめる', 'external', 'pending-human', '2026-01-01T00:00:00Z', '2026-01-01T02:00:00Z', 'default'),
 ('T-p-1', 'ls', 'done one', 'read-only', 'done', '2026-01-01T00:00:00Z', '2026-01-01T02:00:00Z', 'default');
INSERT INTO alert_details (task_id, alert_no, herdr_session, fingerprint, severity, source, stage, result_summary, first_seen, last_seen, analyzed_at) VALUES
 ('T-a-9', 9, 'default', 'fp9', 'high', 'log', 'analyzed', 'ディスクが <full>', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T03:00:00Z'),
 ('T-a-10', 10, 'default', 'fp10', 'low', 'log', 'awaiting-approval', NULL, '2026-01-01T00:00:00Z', '2026-01-01T04:00:00Z', NULL);
INSERT INTO outbox (from_name, from_pane, from_session, target, kind, task_id, text, status, created_at, herdr_session) VALUES
 ('alpha', 'p1', 's', 'beta', 'task', 'T-p-1', 'please "review" <this>', 'pending', '2026-01-01T00:00:00Z', 'default'),
 ('alpha', 'p1', 's', 'beta', 'info', NULL, 'queued later', 'queued', '2999-01-01T00:00:00Z', 'default'),
 ('alpha', 'p1', 's', 'beta', 'info', NULL, 'queued old ' || replace(hex(zeroblob(100)), '00', 'い'), 'queued', '2026-01-01T00:00:00Z', 'default'),
 ('alpha', 'p1', 's', 'beta', 'info', '', 'sent', 'sent', 'not a time', 'default');
INSERT INTO agent_ops (op, target, args, requested_by, status, created_at, herdr_session) VALUES
 ('stop', 'beta', '{"why":"idle"}', 'alpha', 'pending', '2026-01-01T00:00:00Z', 'default'),
 ('stop', 'beta', '{}', 'alpha', 'done', '2026-01-01T00:00:00Z', 'default');
INSERT INTO agent_activity (session_id, pane_id, last_prompt_at, last_stop_at, updated_at, herdr_session) VALUES
 ('sess-b', 'p2', '2026-01-01T00:00:00Z', NULL, '2026-01-01T00:00:00Z', 'default');
INSERT INTO notify_events (type, ref, text, hash, sinks, created_at, herdr_session) VALUES
 ('ticket', 'T-p-12', '[チケット] T-p-12 が開いた(担当 alpha)', 'h1', 'slack webui', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), 'default'),
 ('ticket', 'x', 'チケットT-a-1 と _T-b-2 と T-c-3x', 'h2', 'webui', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), 'default'),
 ('ticket', 'x', 'slack only T-d-4', 'h3', 'slack', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), 'default'),
 ('ticket', 'x', 'old T-e-5', 'h4', 'webui', '2020-01-01T00:00:00Z', 'default');
INSERT INTO notify_seen (herdr_session, key, value, updated_at) VALUES
 ('default', 'reminders:last', '{"at": "2026-01-01T09:00", "items": [
   {"p": 1, "status": "new", "key": "due:T-p-1", "what": "締め切り <a>", "who": "alpha", "due": "2026-01-02", "ref": "T-p-1"},
   {"p": true, "status": "changed", "key": "inbox:1", "what": "保留", "ref": "I-7"},
   {"p": 1.0, "status": "continuing", "key": "x:1", "what": "行き先なし", "due": null, "ref": "R"},
   {"p": 2, "status": "new", "key": "q:1", "what": "二番目"},
   {"p": 1, "status": "gone", "key": "bad"},
   "junk"], "removed": [1, 2]}', '2026-01-01T00:00:00Z');
INSERT INTO messages (kind, ref, target, body, result, created_at, herdr_session) VALUES
 ('send', 'O-1', 'beta', 'first line
second line', 'sent', '2026-01-01T00:00:00Z', 'default'),
 ('send', 'O-x', 'beta', '  <b>bold</b> ' || replace(hex(zeroblob(250)), '00', 'う'), 'failed', '2026-01-02T00:00:00Z', 'default'),
 ('report', 'T-p-1', 'alpha', 'report body', 'busy', '2026-01-02T10:00:00Z', 'default'),
 ('report', NULL, 'beta', 'report 2', 'failed', '2026-01-03T00:00:00Z', 'default'),
 ('報 告', 'C-1', 'alpha', '', 'sent', '2026-01-03T00:00:00Z', 'default'),
 ('send', 'O-2', 'other', 'other session', 'sent', '2026-01-01T00:00:00Z', 'other');
INSERT INTO inbox (herdr_session, outbox_id, recipient, from_name, kind, task_id, text, rule, created_at, read_at) VALUES
 ('default', 1, 'beta', 'alpha', 'info', 'T-p-1', 'held <body>
line 2', 'r1', '2026-01-01T00:00:00Z', NULL),
 ('default', 2, 'beta', 'alpha', 'task', NULL, 'read one', 'r1', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z'),
 ('other', 3, 'zeta', 'alpha', 'info', NULL, 'other session held', 'r1', '2026-01-01T00:00:00Z', NULL);
INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at, resolved_at, resolved_note, herdr_session) VALUES
 ('T-p-1', 'blocked', 'resolved with note', 'alpha', 'p1', 's', '2026-01-02T00:00:00Z', '2026-01-02T02:00:00Z', 'メモ <i>', 'default'),
 ('T-p-1', 'progress', 'just info', 'alpha', 'p1', 's', '2026-01-02T00:00:00Z', NULL, NULL, 'default');
INSERT INTO agents (herdr_session, name, kind, state, killable, created_by, created_at, updated_at) VALUES
 ('other', 'zeta', 'claude', 'running', 'no', 'human', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`

const fakeHerdr = `#!/bin/sh
if [ "$1 $2" = "agent list" ]; then
cat <<'J'
{"result": {"agents": [
 {"name": "alpha", "pane_id": "p1", "agent_status": "blocked", "terminal_title": "Allow \u001b[31mrm</b>?"},
 {"name": "beta", "pane_id": "p2", "agent_status": "working", "agent_session": {"value": "sess-b"}},
 {"pane_id": "p/3 x", "agent_status": "blocked", "terminal_title_stripped": "", "terminal_title": ""},
 {"name": "", "pane_id": "p4", "agent_status": "idle"},
 5]}}
J
exit 0
fi
echo "unknown" >&2; exit 3
`

func pagesFixture(t *testing.T) string {
	if runtime.GOOS == "windows" {
		t.Skip("疑似 herdr が sh")
	}
	dir := t.TempDir()
	dbp := filepath.Join(dir, "tasks.db")
	db, err := store.Open(dbp, "default", &store.Options{NoSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fixtureSQL); err != nil {
		t.Fatal(err)
	}
	db.Close()
	herdr := filepath.Join(dir, "herdr")
	if err := os.WriteFile(herdr, []byte(fakeHerdr), 0o755); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(dir, "logs")
	os.MkdirAll(logs, 0o755)
	os.WriteFile(filepath.Join(logs, "nightly.log"), []byte(`{"ts":"2026-01-01T00:00:00+0900","job":"nightly","action":"ran"}
not json
{"ts":"2026-01-02T00:00:00+0900","job":"nightly","action":"failed","reason":"exit 3 <boom>"}
{"ts":"2026-01-02T00:01:00+0900","job":"nightly","action":"skipped"}
`), 0o644)
	os.WriteFile(filepath.Join(logs, "fine.log"), []byte(`{"action":"failed"}`+"\n"+`{"action":"sent"}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(logs, "noreason.log"), []byte(`{"action":"failed","ts":null}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(logs, "bad name.log"), []byte(`{"action":"failed"}`+"\n"), 0o644)
	webui, _ := filepath.Abs("../../webui")
	for k, v := range map[string]string{
		"WEBUI_TB_DB": dbp, "HERDR_BIN": herdr, "WEBUI_SCHED_LOGS": logs, "HERDR_SESSION": "default",
		"HERDR_SESSION_FILE": filepath.Join(dir, "none"), "HERDR_SOCKET_PATH": "", "HERDR_SESSION_FROM_FILE": "",
		"TB_STATUSES": "", "TB_CONFIG_DIR": filepath.Join(dir, "config"), "WEBUI_ROOT": webui,
		"XDG_STATE_HOME": filepath.Join(dir, "state"), "HOME": dir, "WEBUI_STATE_DIR": filepath.Join(dir, "state"),
	} {
		t.Setenv(k, v)
	}
	ForgetSessions()
	cacheMu.Lock()
	cacheM = map[string]cacheEntry{}
	cacheMu.Unlock()
	return webui
}

const pyRender = `
import json, sys
sys.path.insert(0, sys.argv[1])
from urllib.parse import parse_qs
from app import views, reqctx, data
out = {}
with reqctx.use("default", "default"), data.request_db():
    for u in json.loads(sys.argv[2]):
        p, _, query = u.partition("?")
        ctx = {"csrf": "X"}
        title, body = views.route(p, parse_qs(query), ctx)
        out[u] = [title, body, views.page(title, body, p, data.badge_count(), "<LOGOUT>", refresh=not ctx.get("norefresh"))]
    out["api"] = json.dumps({"items": data.urgent_items(), "poll_sec": data.NOTIFY_POLL_SEC}, ensure_ascii=False)
print(json.dumps(out))
`

func TestPagesMatchPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 が無い")
	}
	webui := pagesFixture(t)
	paths := []string{"/notifications", "/outbox", "/sessions", "/ledger",
		"/ledger?result=all", "/ledger?result=failed&kind=send", "/ledger?kind=report&target=beta&from=2026-01-02",
		"/ledger?target=bad%20name&from=2026/01/01&to=x&kind=nope&result=weird", "/ledger?to=2026-01-02&target=&target=alpha",
		"/ledger?kind=%E5%A0%B1+%E5%91%8A", "/ledger?result=busy&to=2026-01-01",
		"/inbox", "/inbox?held=1", "/inbox?held=3", "/inbox?held=x1", "/inbox?held=999&resolved=done", "/inbox?resolved=already",
		"/inbox?resolved=%20done"}
	pj, _ := json.Marshal(paths)
	c := exec.Command(py, "-c", pyRender, webui, string(pj))
	raw, err := c.Output()
	if err != nil {
		t.Fatalf("python: %v", err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	s := &Server{Log: os.Stderr}
	q := &rq{name: "default"}
	defer func() { q.v.close() }() // q.v は途中で作られるので、閉じるときの値を見る
	for _, u := range paths {
		p, query, _ := strings.Cut(u, "?")
		opts := &pageOpts{}
		title, body := ported[p](s, q, pageCtx{csrf: "X", opts: opts, qs: pyParseQS(query)})
		w := want[u].([]any)
		if title != w[0] {
			t.Errorf("%s title: %q != %q", p, title, w[0])
		}
		if body != w[1] {
			t.Errorf("%s body differs:\n go: %s\n py: %s", p, body, w[1])
		}
		if full := s.page(q, title, body, p, "<LOGOUT>", *opts); full != w[2] {
			t.Errorf("%s page differs:\n go: %s\n py: %s", p, full, w[2])
		}
	}
	if got := notificationsJSON(q.data().urgentItems()); got != want["api"] {
		t.Errorf("api differs:\n go: %s\n py: %s", got, want["api"])
	}
	// 中身の確かめ(Python と同じであるだけでなく、思ったものが出ているか)
	items := q.data().urgentItems()
	kinds := map[string]int{}
	for _, it := range items {
		kinds[it.Kind]++
	}
	wantKinds := map[string]int{"comment_question": 2, "comment_blocked": 1, "proxy_pending": 1, "alert_result": 1, "alert_approval": 1,
		"send_pending": 1, "agent_op_pending": 1, "agent_blocked": 2, "agent_stalled": 1, "ticket_rule": 2, "reminder_due": 3, "job_failed": 2}
	for k, n := range wantKinds {
		if kinds[k] != n {
			t.Errorf("%s: %d 件(want %d)", k, kinds[k], n)
		}
	}
	if len(kinds) != len(wantKinds) {
		t.Errorf("kinds: %v", kinds)
	}
	if n := q.data().badgeCount(); n != 6 { // 報告 3(question 2・blocked 1)+ 代行 1 + blocked の agent 2
		t.Errorf("badge=%d", n)
	}
}

func TestTicketRef(t *testing.T) {
	for in, want := range map[string]string{
		"[チケット] T-p-12 が開いた": "T-p-12", "チケットT-a-1 と _T-b-2 と T-c-3x": "", "(T-x_y-7)": "T-x_y-7",
		"T-a-1-2": "T-a-1", "aT-b-1 T-c-2": "T-c-2", "T--1": "", "T-a-": "",
	} {
		if got := ticketRef(in); got != want {
			t.Errorf("%q: %q (want %q)", in, got, want)
		}
	}
}

func TestPagesHTTP(t *testing.T) {
	pagesFixture(t)
	ts, srv := testServer(t)
	if _, err := srv.Auth.Setup("alice", "correcthorse12"); err != nil {
		t.Fatal(err)
	}
	ck, err := srv.Auth.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]struct {
		code int
		has  string
	}{
		"/s/default/notifications":     {200, "<h1>緊急通知</h1>"},
		"/s/default/outbox/":           {200, "<h1>送信 (outbox)</h1>"},
		"/s/default/sessions":          {200, `<a href="../other/">other</a>`},
		"/s/default/ledger?kind=send":  {200, `<option value="send" selected>send</option>`},
		"/s/default/nope":              {404, "<h1>見つかりません</h1>"},
		"/s/default/tasks":             {501, "Go 版の Web UI にまだありません"},
		"/s/default/jobs/x":            {501, "Go 版の Web UI にまだありません"},
		"/s/default/api/notifications": {200, `"poll_sec": 15}`},
	} {
		r := do(t, ts, "GET", path, ck, nil)
		if r.code != want.code || !strings.Contains(r.body, want.has) {
			t.Errorf("%s: %d %s", path, r.code, r.body[:min(len(r.body), 300)])
		}
	}
	r := do(t, ts, "GET", "/s/default/notifications", ck, nil)
	if !strings.Contains(r.body, `href="/s/default/tasks/T-p-1"`) || strings.Contains(r.body, "<script>x") {
		t.Errorf("リンクの付け替えかエスケープ: %s", r.body)
	}
}

func TestResolvePost(t *testing.T) {
	pagesFixture(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	hc := filepath.Join(dir, "herdrctl")
	os.WriteFile(hc, []byte(`#!/bin/sh
[ "$1 $2" = "tb --features" ] && { echo "policy-ops auth"; exit 0; }
echo "$* | db=$TB_DB pane=${HERDR_PANE_ID-unset}" >>"`+log+`"
case "$3" in C-2) echo "C-2 はすでに対応済みです";; C-3) echo "tb: だめ" >&2; exit 1;; esac
exit 0
`), 0o755)
	t.Setenv("TB_HERDRCTL", hc)
	t.Setenv("HERDR_PANE_ID", "%9")
	ts, srv := testServer(t)
	srv.Auth.Setup("alice", "correcthorse12")
	ck, _ := srv.Auth.CreateSession()
	r := do(t, ts, "GET", "/s/default/inbox", ck, nil)
	if r.code != 200 || !strings.Contains(r.body, `action="/s/default/resolve"`) || strings.Contains(r.body, "herdr-refresh") {
		t.Fatalf("inbox: %d", r.code)
	}
	tok := csrf(t, r.body)
	post := func(f url.Values) resp { f.Set("csrf", tok); return do(t, ts, "POST", "/s/default/resolve", ck, f) }
	for _, c := range []struct {
		form url.Values
		code int
		loc  string
	}{
		{url.Values{"id": {"1"}, "back": {"/inbox"}, "note": {"  メモ  "}}, 302, "/s/default/inbox?resolved=done"},
		{url.Values{"id": {"2"}, "back": {"/tasks/T-p-1"}}, 302, "/s/default/tasks/T-p-1?resolved=already"},
		{url.Values{"id": {"3"}, "back": {"https://evil/"}}, 302, "/s/default/inbox?resolved=error"},
		{url.Values{"id": {"x"}}, 400, ""},
	} {
		if r := post(c.form); r.code != c.code || r.loc != c.loc {
			t.Errorf("%v: %d %s", c.form, r.code, r.loc)
		}
	}
	got, _ := os.ReadFile(log)
	want := "tb resolve C-1 -m メモ | db=" + os.Getenv("WEBUI_TB_DB") + " pane=unset\n" +
		"tb resolve C-2 | db=" + os.Getenv("WEBUI_TB_DB") + " pane=unset\n" +
		"tb resolve C-3 | db=" + os.Getenv("WEBUI_TB_DB") + " pane=unset\n"
	if string(got) != want {
		t.Errorf("tb に渡したもの:\n%s\nwant:\n%s", got, want)
	}
	if r := do(t, ts, "POST", "/s/default/resolve", "", url.Values{"csrf": {tok}, "id": {"1"}}); r.code != 302 || r.loc != "/s/default/login" {
		t.Errorf("未ログイン: %d %s", r.code, r.loc)
	}
}
