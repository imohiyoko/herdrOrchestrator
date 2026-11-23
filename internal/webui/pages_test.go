package webui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES
 ('default', 'policy', '* send:info * = allow   # 既定どおり <b>
* send:task worker1 = allow
* send:task @lead = ask-human
* send:task @lead+hr = deny
* agent:* * = allow
* send:task ~child = deny
* send:task worker? = allow
worker2 send:task * = allow
human * * = allow
human send:[ab] * = deny
system:x * * = deny
* proxy:write * = deny
* bogus:op * = allow
* cmd:git * = allow
* setup:hooks * = ask-any
* agent:slash:compact * = allow
bad line
* a b c = maybe', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'role-map', 'worker* = lead  # 既定
qa? = hr,lead
bad', 'human', '2026-01-01T00:00:00Z'),
 ('*', 'role:lead', '# lead', 'human', '2026-01-01T00:00:00Z');
INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES
 ('default', 'sched-job:nightly', '# 夜のまとめを gamma に送る <b>。
#   token=supersecret123 を含む説明
# 注意: COMMAND は bash -c で動く

# ここは説明ではない
TARGET=gamma
PROMPT=''まとめてください ghp_abcdefghijklmnopqrstuvwxyz
Bearer abcdefghijklmnop x''
PROMPT_RULES=''hour=9 | 朝の本文
* | 既定''
SKIP_IF="working blocked"   # 注釈
INTERVAL_MIN=1440
START=03:00
END=03:00
DAYS=daily
TZ=Asia/Tokyo', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'sched-job:fine', 'COMMAND=''tb notify flush && tb digest; echo password: hunter22222''
COMMAND_TIMEOUT=30
INTERVAL_SEC=90
START=00:00
END=23:59
DAYS="Mon,Wed"
TZ=UTC
ENABLED=0', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'sched-settings:fine', 'INTERVAL_SEC=150', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'sched-job:pfile', 'TARGET=alpha
PROMPT_FILE=/etc/hostname
INTERVAL_MIN=30
WINDOW=09:00-18:00
DAYS=weekdays
TZ=Asia/Tokyo', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'sched-job:pin', 'TARGET=delta
PROMPT_FILE=@SCHED@/jobs/auditor.conf.example
INTERVAL_MIN=60
START=09:00
END=18:00
DAYS="sat sun"
TZ=Asia/Tokyo', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'sched-job:dupjob', 'TARGET=dup
PROMPT_FILE=x.md
INTERVAL_MIN=5
START=09:00
END=10:00
DAYS=weekends
TZ=Nowhere/Zone', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'sched-job:nobody', 'TARGET=nobody
PROMPT_CMD=''echo hi''
INTERVAL_MIN=10
START=09:00
END=10:00
DAYS=daily
TZ=UTC', 'human', '2026-01-01T00:00:00Z'),
 ('default', 'sched-job:badjob', 'TARGET=gamma
PROMPT=x
INTERVAL_MIN=10
START=09:00
END=10:00
DAYS=funday
TZ=UTC', 'human', '2026-01-01T00:00:00Z'),
 ('*', 'sched-job:shared1', 'COMMAND=''tb pr-watch''
INTERVAL_MIN=60
START=09:00
END=18:00
DAYS=daily
TZ=UTC', 'human', '2026-01-01T00:00:00Z'),
 ('other', 'sched-job:otherjob', 'COMMAND=x
INTERVAL_MIN=60
START=09:00
END=18:00
DAYS=daily
TZ=UTC', 'human', '2026-01-01T00:00:00Z');
INSERT INTO tasks (id, project, title, status, owner_agent, type, priority, labels, due, sprint_id, points, parent_id, reported_at, deliverables, created_at, updated_at, herdr_session) VALUES
 ('T-q-1', 'q', 'バグ <i>直す</i> ' || replace(hex(zeroblob(50)), '00', 'い'), 'todo', 'alpha', 'bug', 'highest', '["ui","x y",3]', '2026-03-01', 1, 3, 'T-p-1', '2026-03-01T00:00:00Z', '["/tmp/a","/tmp/b"]', '2026-02-01T10:20:30Z', '2026-03-01T00:00:00.123Z', 'default'),
 ('T-q-2', 'q', 'done task', 'done', NULL, 'story', 'low', '["ui"]', '2026-03-02', 1, 5, NULL, NULL, 'bad json', '2026-02-01T00:00:00Z', '2026-02-02 00:00:00', 'default'),
 ('T-q-3', 'q', 'legacy state', 'legacy', '', 'task', 'medium', '[]', '2026-03-06', 1, NULL, NULL, NULL, '[]', '2026-02-01T00:00:00Z', '2026-02-01T00:00:00Z', 'default'),
 ('T-q-4', 'q', 'stale wip', 'wip', 'beta', 'epic', 'high', '["backend"]', NULL, NULL, NULL, NULL, NULL, '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'default'),
 ('T-q-5', 'q', 'other sprint', 'todo', 'beta', 'subtask', 'lowest', '[]', '2026-04-01', 2, 1, 'T-q-1', NULL, '[]', '2026-02-01T00:00:00Z', '2026-03-03T00:00:00Z', 'default');
INSERT INTO sprints (id, project, name, goal, start_date, end_date, state, created_at, herdr_session) VALUES
 (1, 'q', 'Sprint <A>', 'goal <x>', '2026-03-01', '2026-03-14', 'active', '2026-02-01T00:00:00Z', 'default'),
 (2, 'q', 'Sprint B', NULL, NULL, NULL, 'planned', '2026-02-01T00:00:00Z', 'default'),
 (3, 'p', 'Old', NULL, '2026-01-01', NULL, 'closed', '2026-01-01T00:00:00Z', 'default'),
 (4, 'z', 'other session', NULL, NULL, NULL, 'active', '2026-01-01T00:00:00Z', 'other');
INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES
 ('default', 'labels', 'ui = 画面
backend # 裏側
ui
unused', 'human', '2026-01-01T00:00:00Z');
INSERT INTO approval_changes (pattern, value, created_at, herdr_session) VALUES
 ('send:task:worker1', 'allow', '2026-01-01T00:00:00Z', 'default'), ('send:info:*', 'ask', '2026-01-02T00:00:00Z', 'default'),
 ('send:task:worker1', 'ask', '2026-01-03T00:00:00Z', 'default'), ('agent:*', 'allow', '2026-01-04T00:00:00Z', 'default'),
 ('agent:*', 'ask', '2026-01-05T00:00:00Z', 'other');
INSERT INTO agents (herdr_session, name, kind, state, killable, created_by, created_at, updated_at) VALUES
 ('other', 'zeta', 'claude', 'running', 'no', 'human', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`

// docsFixtureSQL はドキュメント(/docs)とタスク詳細(/tasks/<id>)のための行。@DOCS@ は成果物を置いたディレクトリ。
const docsFixtureSQL = `
UPDATE agent_activity SET cwd = '@DOCS@/proj' WHERE session_id = 'sess-b';
INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at, deliverables, delivered_at, herdr_session) VALUES
 ('T-p-1', 'done', '成果 <b>', 'beta', 'p2', 'sess-b', '2026-01-05T00:00:00Z',
  '["notes.md", "sub", "https://example.com/pr/1?a=1&b=2", "missing.md", "../outside.md", "bin.dat", " notes.md ", "big.txt", "link-out", "empty.md"]', '2026-01-05T00:01:00Z', 'default'),
 ('T-p-1', 'progress', 'second', 'alpha', 'p1', 'nosess', '2026-01-04T00:00:00Z',
  '["rel.md", "/abs/none.md", "@DOCS@/proj/diagram.mmd", "@DOCS@/proj/.env", "~/homefile.md", "file://@DOCS@/proj/plain.txt", "@DOCS@/proj/sub/"]', NULL, 'default'),
 ('T-p-2', 'done', 'x', 'alpha', 'p1', 'sess-b', '2026-01-03T00:00:00Z', 'notjson', NULL, 'default'),
 ('T-p-2', 'done', 'x', 'alpha', 'p1', 'sess-b', '2026-01-03T00:00:00Z', '[1, true, null, 2.5, "notes.md", {"a": 1}]', NULL, 'default'),
 ('T-zz', 'done', 'no task', 'alpha', 'p1', 'sess-b', '2026-01-06T00:00:00Z', '["notes.md"]', NULL, 'default'),
 ('T-p-3', 'done', 'other session', 'alpha', 'p1', 'sess-b', '2026-01-06T00:00:00Z', '["notes.md"]', NULL, 'other');
UPDATE tasks SET deliverables = '["@DOCS@/proj/notes.md", "@DOCS@/proj/sub", "https://example.com/x"]', session_id = 'sess-b', account = 'acc <1>',
  description = '説明 <b>' || char(10) || '2 行目', refs = '[{"kind": "slack", "url": "https://slack.example/a"}, {"url": "javascript:alert(1)"}, "junk", {"kind": "pr"}]',
  summary = 'まとめ', note = 'メモ'
  WHERE id = 'T-q-1';
INSERT INTO task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at) VALUES
 ('default', 'T-q-1', 'created', NULL, 'todo', 'human', NULL, '2026-02-01T10:20:30Z'),
 ('default', 'T-q-1', 'status', 'todo', '<wip>', 'alpha', 'C-1', '2026-02-02T00:00:00Z'),
 ('other', 'T-q-1', 'status', 'x', 'y', 'zeta', NULL, '2026-02-02T00:00:00Z');
INSERT INTO task_links (from_id, link_type, to_id, created_by, created_at, herdr_session) VALUES
 ('T-q-1', 'blocks', 'T-q-2', 'human', '2026-02-01T00:00:00Z', 'default'),
 ('T-q-3', 'duplicates', 'T-q-1', 'human', '2026-02-01T00:00:00Z', 'default'),
 ('T-q-4', 'blocks', 'T-q-1', 'human', '2026-02-01T00:00:00Z', 'default'),
 ('T-q-1', 'relates', 'T-q-5', 'human', '2026-02-01T00:00:00Z', 'default');
UPDATE alert_details SET result_path = '@DOCS@/proj/report.md', result_report = '# DB のレポート' || char(10) || '- 本文', result_by = 'analyst'
  WHERE alert_no = 9;
UPDATE alert_details SET result_report = 'DB だけ **太字**' WHERE alert_no = 10;
`

// docsFiles は成果物のファイル(@DOCS@ からの相対パス → 中身)。
var docsFiles = map[string]string{
	"proj/notes.md": "# ノート <x>\n\n本文 **太字** と [リンク](https://example.com)。\n\n- a\n  - b\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n" +
		"```mermaid\ngraph TD; A-->B\n```\n",
	"proj/diagram.mmd":        "graph LR; X-->Y",
	"proj/.env":               "SECRET=1",
	"proj/plain.txt":          "壊れた \xe3\x81 と \xff\xfe と <tag>",
	"proj/bin.dat":            "a\x00b",
	"proj/empty.md":           "",
	"proj/report.md":          "# ファイルのレポート",
	"proj/sub/a.md":           "*a*",
	"proj/sub/b.txt":          "b",
	"proj/sub/.hidden":        "h",
	"proj/sub/nested/c.md":    "c",
	"proj/sub/node_modules/x": "x",
	"proj/sub/.git/config":    "x",
	"proj/sub/z dir/日本語.md":   "日本",
	"homefile.md":             "home",
	"extra/e.md":              "e",
}

const fakeHerdr = `#!/bin/sh
if [ "$1 $2" = "agent list" ]; then
cat <<'J'
{"result": {"agents": [
 {"name": "alpha", "pane_id": "p1", "agent_status": "blocked", "terminal_title": "Allow \u001b[31mrm</b>?"},
 {"name": "beta", "pane_id": "p2", "agent_status": "working", "agent_session": {"value": "sess-b"}},
 {"pane_id": "p/3 x", "agent_status": "blocked", "terminal_title_stripped": "", "terminal_title": ""},
 {"name": "", "pane_id": "p4", "agent_status": "idle"},
 {"name": "gamma", "pane_id": "p5", "agent_status": "idle", "workspace_id": "w1"},
 {"name": "delta", "pane_id": "p6", "agent_status": "done", "workspace_id": "w9"},
 {"name": "dup", "pane_id": "p7", "agent_status": "idle"}, {"name": "dup", "pane_id": "p8", "agent_status": "idle"},
 5]}}
J
exit 0
fi
if [ "$1 $2" = "workspace list" ]; then
echo '{"result": {"workspaces": [{"workspace_id": "w1", "label": "<WS1>"}, {"workspace_id": "w9"}]}}'
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
	schedDir, _ := filepath.Abs("../../scheduler")
	if _, err := db.Exec(strings.ReplaceAll(fixtureSQL, "@SCHED@", schedDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(strings.ReplaceAll(docsFixtureSQL, "@DOCS@", dir)); err != nil {
		t.Fatal(err)
	}
	for name, body := range docsFiles {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "proj", "big.txt"), []byte(strings.Repeat("あ", docMaxBytes/3+10)), 0o644)
	os.Symlink("/etc/hostname", filepath.Join(dir, "proj", "link-out"))
	os.Symlink("/etc/hostname", filepath.Join(dir, "proj", "sub", "out-link"))
	os.Symlink("../report.md", filepath.Join(dir, "proj", "sub", "in-link.md"))
	os.Symlink("nested", filepath.Join(dir, "proj", "sub", "dirlink"))
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
	os.MkdirAll(filepath.Join(dir, "jobs"), 0o755)
	os.WriteFile(filepath.Join(dir, "jobs", "unimported.conf"), []byte("COMMAND=x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "jobs", "nightly.conf"), []byte("COMMAND=x\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "roles"), 0o755)
	os.WriteFile(filepath.Join(dir, "roles", "hr.md"), []byte("# hr\n"), 0o644)
	webui, _ := filepath.Abs("../../webui")
	for k, v := range map[string]string{
		"WEBUI_TB_DB": dbp, "HERDR_BIN": herdr, "WEBUI_SCHED_LOGS": logs, "HERDR_SESSION": "default",
		"HERDR_SESSION_FILE": filepath.Join(dir, "none"), "HERDR_SOCKET_PATH": "", "HERDR_SESSION_FROM_FILE": "",
		"TB_STATUSES": "", "TB_CONFIG_DIR": filepath.Join(dir, "config"), "WEBUI_ROOT": webui,
		"XDG_STATE_HOME": filepath.Join(dir, "state"), "WEBUI_POLICY": "", "TB_POLICY": "", "TB_ROLES": filepath.Join(dir, "roles"),
		"WEBUI_SCHED_JOBS": filepath.Join(dir, "jobs"), "WEBUI_SCHED_SETTINGS": filepath.Join(dir, "settings"), "SCHED_FILE_FALLBACK": "",
		"SCHED_START": "", "SCHED_END": "", "SCHED_WINDOW": "", "SCHED_INTERVAL_MIN": "", "SCHED_INTERVAL_SEC": "", "HOME": dir, "WEBUI_STATE_DIR": filepath.Join(dir, "state"),
		"WEBUI_DOC_ROOTS": filepath.Join(dir, "extra") + ":relative:/tmp:" + filepath.Join(dir, "nope"),
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
from datetime import datetime, timezone
from app import views, reqctx, data, jobview
FIXED = datetime(2026, 3, 4, 12, 34, 56, tzinfo=timezone.utc)
views.next_runs = lambda info: jobview.next_runs(info, now=FIXED)
class FixedDT(datetime):
    @classmethod
    def now(cls, tz=None):
        return FIXED.astimezone(tz) if tz else FIXED.astimezone().replace(tzinfo=None)
views.datetime = FixedDT
out = {}
with reqctx.use("default", "default"), data.request_db():
    for u in json.loads(sys.argv[2]):
        p, _, query = u.partition("?")
        ctx = {"csrf": "X"}
        if p.startswith("/jobs/"):
            ctx["norefresh"] = True
            title, body = views.page_job(p[6:], "X", saved=parse_qs(query).get("saved") == ["1"])
        else:
            r = views.route(p, parse_qs(query), ctx)
            if r is None:
                out[u] = None
                continue
            title, body = r
        out[u] = [title, body, views.page(title, body, p, data.badge_count(), "<LOGOUT>", refresh=not ctx.get("norefresh"), scripts=ctx.get("scripts", ()))]
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
		"/inbox?resolved=%20done",
		"/perms", "/perms?op=send:task:worker1", "/perms?op=send:task:worker9", "/perms?op=send:task:qa1", "/perms?op=agent:new:x",
		"/perms?op=bad%20op", "/perms?op=setup:webui", "/perms?op=agent:slash:compact:w1", "/perms?kind=send&state=active&fallback=1",
		"/perms?allowed=1&kind=bogus", "/perms?state=unwired", "/perms?pat=send:task:*&val=allow", "/perms?pat=*&val=allow",
		"/perms?pat=send:info&val=ask", "/perms?pat=bad%20x&val=allow", "/perms?val=deny", "/perms?pat=proxy:write&val=allow",
		"/jobs", "/jobs/nightly", "/jobs/fine?saved=1", "/jobs/pfile", "/jobs/pin", "/jobs/dupjob", "/jobs/nobody", "/jobs/badjob",
		"/jobs/shared1", "/jobs/missing",
		"/tasks", "/tasks?view=all", "/tasks?view=closed&sort=due", "/tasks?status=legacy&status=todo&status=todo&status=nope",
		"/tasks?q=%25_&project=q&owner=~&type=bug&priority=highest&label=ui&due=overdue&days=5&unreported=1&stale=1&sort=priority",
		"/tasks?due=soon&days=3", "/tasks?due=soon&days=x&view=bad&type=no&priority=no&label=a%20b&owner=bad%20o&sort=nope",
		"/tasks?stale=1", "/tasks?owner=alpha&q=%E3%83%90%E3%82%B0", "/tasks?sort=id&view=open&label=x%20y",
		"/board", "/board?sprint=S-2", "/board?sprint=S-9&label=ui", "/board?sprint=bad&label=a%20b", "/board?project=p", "/board?label=backend&project=q",
		"/docs", "/docs?q=NOTES", "/docs?q=%CE%A3x", "/docs?q=analyst", "/docs?q=T-q-1", "/tasks/T-q-1", "/tasks/T-p-1", "/tasks/T-p-2",
		"/tasks/T-p-3", "/tasks/nope", "/tasks/T-a-9", "/tasks/T-q-1?resolved=done",
		"/docs/view", "/docs/view?ref=bogus", "/docs/view?ref=C-99/0", "/docs/view?ref=A-99", "/docs/view?ref=A-9", "/docs/view?ref=A-9&raw=1",
		"/docs/view?ref=A-10", "/docs/view?ref=A-10&raw=1", "/docs/view?ref=task:T-q-1/0", "/docs/view?ref=task:T-q-1/0&raw=1",
		"/docs/view?ref=task:T-q-1/1", "/docs/view?ref=task:T-q-1/1&sub=nested", "/docs/view?ref=task:T-q-1/1&sub=a.md",
		"/docs/view?ref=task:T-q-1/1&sub=../notes.md", "/docs/view?ref=task:T-q-1/1&sub=in-link.md", "/docs/view?ref=task:T-q-1/1&sub=out-link",
		"/docs/view?ref=task:T-q-1/1&sub=nested/c.md&raw=1", "/docs/view?ref=task:T-q-1/0&sub=x", "/docs/view?ref=task:T-q-1/2",
		"/docs/view?ref=task:T-q-1/3", "/docs/view?ref=task:nope/0", "/docs/view?ref=C-12/1", "/docs/view?ref=C-12/0", "/docs/view?ref=C-11/0"}
	for i := range 10 {
		paths = append(paths, fmt.Sprintf("/docs/view?ref=C-9/%d", i))
	}
	for i := range 7 {
		paths = append(paths, fmt.Sprintf("/docs/view?ref=C-10/%d", i))
	}
	home := os.Getenv("HOME")
	for _, x := range []string{home + "/proj/notes.md", home + "/proj/sub", "~/proj/diagram.mmd", "file://" + home + "/extra/e.md", "relative.md",
		"/etc/hostname", home + "/proj/../proj/plain.txt", home + "/extra", home, home + "/proj/.env", home + "/proj/sub/out-link", " " + home + "/extra/e.md "} {
		paths = append(paths, "/docs/view?"+pyURLEncode(kvs{{"path", x}}), "/docs/view?"+pyURLEncode(kvs{{"path", x}, {"sub", "a.md"}}))
	}
	paths = append(paths, "/docs/view?"+pyURLEncode(kvs{{"path", home + "/proj/notes.md"}, {"ref", "A-9"}}))
	nowFn = func() time.Time { return time.Date(2026, 3, 4, 12, 34, 56, 0, time.UTC) }
	defer func() { nowFn = time.Now }()
	t.Setenv("SCHED_HERDRCTL", buildHerdrctl(t))
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
		var title, body string
		if name, ok := strings.CutPrefix(p, "/jobs/"); ok {
			opts.norefresh = true
			saved := pyParseQS(query)["saved"]
			title, body = s.pageJob(q, name, "X", "", len(saved) == 1 && saved[0] == "1")
		} else if tt, bb, found, handled := s.routeDetail(q, p, query, "X", opts); handled {
			if !found {
				if want[u] != nil {
					t.Errorf("%s: Go は 404、Python は %v", u, want[u])
				}
				continue
			}
			title, body = tt, bb
		} else {
			title, body = ported[p](s, q, pageCtx{csrf: "X", opts: opts, qs: pyParseQS(query)})
		}
		w, ok := want[u].([]any)
		if !ok {
			t.Errorf("%s: Python は 404", u)
			continue
		}
		if title != w[0] {
			t.Errorf("%s title: %q != %q", p, title, w[0])
		}
		if body != w[1] {
			t.Errorf("%s body differs:\n%s", u, firstDiff(body, w[1].(string)))
		}
		if full := s.page(q, title, body, p, "<LOGOUT>", *opts); full != w[2] {
			t.Errorf("%s page differs:\n%s", u, firstDiff(full, w[2].(string)))
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
		"/s/default/notifications":         {200, "<h1>緊急通知</h1>"},
		"/s/default/outbox/":               {200, "<h1>送信 (outbox)</h1>"},
		"/s/default/sessions":              {200, `<a href="../other/">other</a>`},
		"/s/default/ledger?kind=send":      {200, `<option value="send" selected>send</option>`},
		"/s/default/nope":                  {404, "<h1>見つかりません</h1>"},
		"/s/default/tasks":                 {200, "<h1>タスク</h1>"},
		"/s/default/tasks/T-q-1":           {200, "<h1>タスク T-q-1</h1>"},
		"/s/default/tasks/nope":            {404, "<h1>見つかりません</h1>"},
		"/s/default/docs":                  {200, "<h1>ドキュメント</h1>"},
		"/s/default/docs/view?ref=C-9%2F0": {200, `<script src="/s/default/static/mermaid.min.js"`},
		"/s/default/docs/view?ref=x":       {404, "<h1>見つかりません</h1>"},
		"/s/default/jobs/x":                {404, "<h1>見つかりません</h1>"},
		"/s/default/agents/x":              {501, "Go 版の Web UI にまだありません"},
		"/s/default/api/notifications":     {200, `"poll_sec": 15}`},
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

var (
	herdrctlOnce sync.Once
	herdrctlBin  string
	herdrctlErr  error
)

// buildHerdrctl は Python 版の定期実行の画面が呼ぶ herdrctl(scheduler/bin/show.sh が渡す先)を作る。
// startEnv はテストが環境変数を変える前の環境。
var startEnv = os.Environ()

func buildHerdrctl(t *testing.T) string {
	herdrctlOnce.Do(func() {
		dir, err := os.MkdirTemp("", "herdrctl-")
		if err != nil {
			herdrctlErr = err
			return
		}
		herdrctlBin = filepath.Join(dir, "herdrctl")
		cmd := exec.Command("go", "build", "-o", herdrctlBin, "../../cmd/herdrctl")
		cmd.Env = startEnv // テストが HOME を変えたあとでも、いつものモジュールキャッシュを使う(一時ディレクトリに落とすと読み取り専用で消せない)
		out, err := cmd.CombinedOutput()
		if err != nil {
			herdrctlErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if herdrctlErr != nil {
		t.Skipf("herdrctl を作れない: %v", herdrctlErr)
	}
	return herdrctlBin
}

// firstDiff は最初に違う箇所の前後だけを見せる。
func firstDiff(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	a := max(0, i-200)
	return " go: …" + got[a:min(len(got), i+300)] + "\n py: …" + want[a:min(len(want), i+300)]
}

// 定期実行のスケジュールの保存・戻す(書き込みは本物の tb job。herdrctl を作って使う)。
func TestJobPost(t *testing.T) {
	pagesFixture(t)
	t.Setenv("TB_HERDRCTL", buildHerdrctl(t))
	ts, srv := testServer(t)
	srv.Auth.Setup("alice", "correcthorse12")
	ck, _ := srv.Auth.CreateSession()
	r := do(t, ts, "GET", "/s/default/jobs/nightly", ck, nil)
	if r.code != 200 || !strings.Contains(r.body, `action="/s/default/jobs/nightly"`) || strings.Contains(r.body, "herdr-refresh") ||
		!strings.Contains(r.body, "1440 分おき") {
		t.Fatalf("job: %d %s", r.code, r.body)
	}
	tok := csrf(t, r.body)
	post := func(job string, f url.Values) resp {
		f.Set("csrf", tok)
		return do(t, ts, "POST", "/s/default/jobs/"+job, ck, f)
	}
	save := url.Values{"action": {"save"}, "interval": {"2"}, "interval_unit": {"min"}, "start": {"08:00"}, "end": {"09:30"},
		"days": {"custom"}, "day_mon": {"1"}, "day_fri": {"1"}, "enabled": {"1"}}
	if r := post("nightly", save); r.code != 302 || r.loc != "/s/default/jobs/nightly?saved=1" {
		t.Fatalf("save: %d %s %s", r.code, r.loc, r.body)
	}
	r = do(t, ts, "GET", "/s/default/jobs/nightly?saved=1", ck, nil)
	for _, w := range []string{"保存しました", "2 分おき / 08:00-09:30 / Mon Fri", "Web UI で変更済み"} {
		if !strings.Contains(r.body, w) {
			t.Errorf("保存のあと %q が無い", w)
		}
	}
	bad := url.Values{"action": {"save"}, "interval": {"0"}, "interval_unit": {"min"}, "start": {"08:00"}, "end": {"09:30"}, "days": {"daily"}}
	if r := post("nightly", bad); r.code != 400 || !strings.Contains(r.body, "間隔は 1〜1440 分の整数で入力してください") || strings.Contains(r.body, "/logout") {
		t.Errorf("bad: %d %s", r.code, r.body)
	}
	if r := post("nightly", url.Values{"action": {"reset"}}); r.code != 302 {
		t.Errorf("reset: %d %s", r.code, r.body)
	}
	if r := do(t, ts, "GET", "/s/default/jobs/nightly", ck, nil); !strings.Contains(r.body, "1440 分おき") || strings.Contains(r.body, "Web UI で変更済み") {
		t.Errorf("reset のあと: %s", r.body)
	}
	if r := post("missing", url.Values{"action": {"reset"}}); r.code != 404 {
		t.Errorf("missing: %d", r.code)
	}
	if r := do(t, ts, "POST", "/s/default/jobs/nightly", "", url.Values{"csrf": {tok}, "action": {"reset"}}); r.code != 302 || r.loc != "/s/default/login" {
		t.Errorf("未ログイン: %d %s", r.code, r.loc)
	}
}
