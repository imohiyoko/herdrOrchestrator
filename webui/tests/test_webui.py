"""境界のテスト。herdr は疑似、DB・ログは一時ディレクトリ。実ポートは高位の小さな範囲だけ使い、必ず片付ける。"""
import atexit
import glob
import http.client
import json
import os
import random
import re
import shutil
import signal
import hashlib
import socket
import sqlite3
import subprocess
import sys
import tempfile
from datetime import datetime, timedelta, timezone
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SERVER = os.path.join(os.path.dirname(HERE), "server.py")
SCHEMA_DIR = os.path.join(os.path.dirname(os.path.dirname(HERE)), "taskboard", "schema")
TMP = tempfile.mkdtemp(prefix="webui-test-")
procs, socks = [], []
fails = []


def cleanup():
    for p in procs:
        if p.poll() is None:
            p.send_signal(signal.SIGTERM)
            try:
                p.wait(3)
            except subprocess.TimeoutExpired:
                p.kill()
    for s in socks:
        s.close()
    subprocess.run(["rm", "-rf", TMP])


atexit.register(cleanup)


def check(name, cond, detail=""):
    print(("ok   " if cond else "FAIL ") + name + (f"  [{detail}]" if not cond and detail else ""))
    if not cond:
        fails.append(name)


def occupy(port):
    s = socket.socket()
    s.bind(("127.0.0.1", port))
    s.listen(1)
    socks.append(s)
    return s


def free_range(n):
    for _ in range(200):
        base = random.randint(30000, 60000)
        try:
            ss = []
            for p in range(base, base + n):
                s = socket.socket()
                s.bind(("127.0.0.1", p))
                ss.append(s)
            for s in ss:
                s.close()
            return base
        except OSError:
            for s in ss:
                s.close()
    raise SystemExit("空きポート範囲が見つからない")


def make_env(name, **extra):
    d = os.path.join(TMP, name)
    os.makedirs(d, exist_ok=True)
    env = dict(os.environ)
    for k in ("TB_UI_PORT", "TB_INSTANCE", "HERDR_SESSION", "HERDR_SOCKET_PATH", "HERDR_SESSION_FROM_FILE", "TB_CONFIG_DIR"):
        env.pop(k, None)
    env.update(
        WEBUI_FAIL_DELAY="0", TB_PROMPT_VERIFY_SEC="0", HERDR_BIN=os.path.join(HERE, "fake", "herdr"), FAKE_HERDR_DIR=os.path.join(TMP, "fake"),
        WEBUI_RUN_DIR=os.path.join(d, "run"), WEBUI_TB_DB=os.path.join(d, "tasks.db"),
        WEBUI_SCHED_LOGS=os.path.join(d, "logs"), WEBUI_APPROVALS=os.path.join(d, "approvals.conf"),
        WEBUI_SCHED_JOBS=os.path.join(d, "jobs"), WEBUI_SCHED_SETTINGS=os.path.join(d, "settings"),
        WEBUI_STATE_DIR=os.path.join(d, "state"), WEBUI_AUTH_DIR=os.path.join(d, "run"),
        XDG_CONFIG_HOME=os.path.join(d, "xdg"), HERDR_SESSION_FILE=os.path.join(d, "no-herdr-session"))
    env.update(extra)
    env.setdefault("TB_UI_GATEWAY_PORT", str(free_range(1)))
    return env, d


def start(env):
    p = subprocess.Popen([sys.executable, SERVER], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    procs.append(p)
    return p


def wait_port(env, p, timeout=5):
    pf = os.path.join(env["WEBUI_RUN_DIR"], "port")
    end = time.time() + timeout
    while time.time() < end:
        if os.path.exists(pf):
            return int(open(pf).read())
        if p.poll() is not None:
            return None
        time.sleep(0.05)
    return None


def stop(p):
    p.send_signal(signal.SIGTERM)
    try:
        p.wait(3)
    except subprocess.TimeoutExpired:
        p.kill()


def mode(path):
    return oct(os.stat(path).st_mode & 0o777)


PORT_BASE = {}   # ポート → 既定で開くセッションの置き場所(テストが既定の default 以外を開くときに足す)


def base_for(port):
    """そのポートの Web UI で、既存の検査が見るセッションの置き場所(/s/<名前>)。既定は /s/default。"""
    return PORT_BASE.get(port, "/s/default")


def unbase(base, r):
    """ページは /s/<名前>/ の下にあるので、既存の検査はその下からの相対で見る(Location と HTML の絶対パスから base を外す)。
    BASE の付け外しそのものは「入口」の節で生のまま検査する。"""
    if not base:
        return r
    code, h, body = r
    if h.get("Location", "").startswith(base + "/"):
        h = dict(h, Location=h["Location"][len(base):])
    body = re.sub(r'(\s(?:href|action|src)=")' + re.escape(base) + "/", r"\1/", body)
    return code, h, body


def with_base(base, path):
    return base + path if base and path.startswith("/") and not path.startswith("/s/") else path


def req(port, method, path, cookie=None, host=None, raw=False):
    base = "" if raw else base_for(port)
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    h = {"Host": host or f"127.0.0.1:{port}"}
    if cookie:
        h["Cookie"] = cookie
    c.request(method, with_base(base, path), headers=h)
    r = c.getresponse()
    body = r.read().decode("utf-8", "replace")
    out = (r.status, dict(r.getheaders()), body)
    c.close()
    return unbase(base, out)


# ---- fixtures
os.makedirs(os.path.join(TMP, "fake"))
XSS = "<script>alert(1)</script>"
agents = {"id": "x", "result": {"agents": [
    {"name": "alpha", "agent": "claude", "agent_status": "working", "pane_id": "w1:p1", "workspace_id": "w1", "cwd": TMP,
     "agent_session": {"kind": "id", "value": "11111111-2222-3333-4444-555555555555"},
     "terminal_title_stripped": "title " + XSS},
    {"name": "<img src=x onerror=alert(2)>", "agent": "claude", "agent_status": "blocked", "pane_id": "w1:p2",
     "workspace_id": "w1"},
    {"agent": "claude", "agent_status": "idle", "pane_id": "w1:p3", "workspace_id": "w1"},
]}}
json.dump(agents, open(os.path.join(TMP, "fake", "list.json"), "w"))
open(os.path.join(TMP, "fake", "read.txt"), "w").write(
    "\x1b[31mred\x1b[0m \x1b]0;evil title\x07" + XSS + "\x00\x08 end\r\n")


open(os.path.join(TMP, "fake", "dialog.txt"), "w").write(
    "\x1b[1mDo you want to run?\x1b[0m\n> rm -rf /tmp/x " + XSS + "\n  1. Yes\n  2. No\r\n")


def session_fill(c, session="default"):
    """テストが herdr_session を書かずに足した行を、Web UI のセッション(既定 default)の行にする(テスト専用の便宜)。
    tb は必ず herdr_session を書く。Web UI はこのセッションの行だけを読む(app/data.py の session_views)"""
    for (t,) in c.execute("SELECT m.name FROM sqlite_master m WHERE m.type='table' AND EXISTS "
                          "(SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session')").fetchall():
        c.execute(f"CREATE TRIGGER IF NOT EXISTS fill_{t} AFTER INSERT ON {t} WHEN NEW.herdr_session='' "
                  f"BEGIN UPDATE {t} SET herdr_session='{session}' WHERE rowid=NEW.rowid; END")


def make_db(path):
    c = sqlite3.connect(path)
    # 本物のスキーマ(taskboard/schema/*.sql を tb と同じ順に流す)。手書きのスキーマは持たない
    for f in sorted(glob.glob(os.path.join(SCHEMA_DIR, "[0-9][0-9][0-9]_*.sql"))):
        c.executescript(open(f, encoding="utf-8").read().replace("@SESSION@", "'default'"))
        c.execute(f"PRAGMA user_version={int(os.path.basename(f)[:3])}")  # tb と同じく版を記録(tb が移行し直さない)
    session_fill(c)
    c.execute("INSERT INTO tasks VALUES ('T-1','pj','題 " + XSS + "','alpha','11111111-2222-3333-4444-555555555555',"
              "NULL,'in_progress',NULL,'[\"a.md\"]',NULL,NULL,NULL,'2026-11-06T00:00:00Z','2026-11-06T00:00:00Z','default')")
    c.execute("INSERT INTO comments (task_id,kind,text,from_name,from_pane,from_session,created_at) VALUES "
              "('T-1','question','質問 " + XSS + "','alpha','w1:p1','s','2026-11-06T00:00:00Z')")
    c.execute("INSERT INTO agent_activity VALUES ('11111111-2222-3333-4444-555555555555','w1:p1',NULL,NULL,NULL,NULL,"
              "'2026-11-06T00:00:00Z',NULL,'2026-11-06T00:00:00Z','default')")
    c.commit()
    c.close()


# ================= 待ち受け: 1 プロセスが全セッションを返す。ポートは TB_UI_GATEWAY_PORT(既定 8800)、使用中なら +1 ずつ
env, _ = make_env("listen")
p = start(env)
port = wait_port(env, p)
check("既定のポートで起動し run/port に書く", port == int(env["TB_UI_GATEWAY_PORT"]) and open(os.path.join(env["WEBUI_RUN_DIR"], "port")).read().strip() == str(port), f"port={port}")
GWJ = os.path.join(env["WEBUI_STATE_DIR"], "gateway.json")
check("待ち受けたポートを記録する(600)", os.path.exists(GWJ) and json.load(open(GWJ))["port"] == port and mode(GWJ) == "0o600")
ss_ = subprocess.run(["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN"], capture_output=True, text=True).stdout if port and shutil.which("lsof") else ""
check("bind はループバックだけ", (not ss_) or ("127.0.0.1:" in ss_ and "*:" not in ss_ and "0.0.0.0" not in ss_), ss_)
p2 = start(dict(env, TB_UI_GATEWAY_PORT=str(free_range(1))))
try:
    _rc2 = p2.wait(5)
except subprocess.TimeoutExpired:
    _rc2 = None
check("既に動いていれば 2 つ目は起動しない(ずれたポートで重ねて動かない)", _rc2 not in (None, 0) and "既にポート" in p2.stderr.read(), f"rc={_rc2}")
stop(p)
check("終了時に run/port と記録を消す", not os.path.exists(os.path.join(env["WEBUI_RUN_DIR"], "port")) and not os.path.exists(GWJ))
_sb = free_range(3)
occupy(_sb)
env, _ = make_env("listen-shift", TB_UI_GATEWAY_PORT=str(_sb))
p = start(env)
check("既定のポートが使用中なら次のポートで待ち受ける", wait_port(env, p) == _sb + 1)
stop(p)
env, _ = make_env("listen-bad", TB_UI_GATEWAY_PORT="80x")
p = start(env)
try:
    _rc2 = p.wait(5)
except subprocess.TimeoutExpired:
    _rc2 = None
check("数字でないポートでは起動しない", _rc2 not in (None, 0), f"rc={_rc2}")

# ================= 認証つき本体
USER, PW = "tester", "correct-horse-battery-9"
WRONG = "wrong-wrong-wrong-12"


class Client:
    def __init__(self, port):
        self.port, self.cookie = port, None

    def raw(self, method, path, data=None, origin="default", cookie="default", host=None, raw=False):
        base = "" if raw else base_for(self.port)
        path = with_base(base, path)
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        h = {"Host": host or f"127.0.0.1:{self.port}"}
        ck = self.cookie if cookie == "default" else cookie
        if ck:
            h["Cookie"] = ck
        body = None
        if data is not None:
            body = "&".join(f"{k}={urllib.parse.quote(str(v))}" for k, v in data.items())
            h["Content-Type"] = "application/x-www-form-urlencoded"
        if origin == "default":
            origin = f"http://127.0.0.1:{self.port}" if method == "POST" else None
        if origin:
            h["Origin"] = origin
        c.request(method, path, body=body, headers=h)
        r = c.getresponse()
        text = r.read().decode("utf-8", "replace")
        out = (r.status, dict(r.getheaders()), text)
        c.close()
        return unbase(base, out)

    def get(self, path, **kw):
        return self.raw("GET", path, **kw)

    def csrf(self, path):
        m = re.search(r'name=csrf value="([^"]+)"', self.get(path)[2])
        return m.group(1) if m else ""

    def login(self, user=USER, pw=PW, next_=None, csrf=None, **kw):
        csrf = csrf if csrf is not None else self.csrf("/login")
        d = {"csrf": csrf, "username": user, "password": pw}
        if next_ is not None:
            d["next"] = next_
        r = self.raw("POST", "/login", d, **kw)
        m = re.match(r"([^;]+)", r[1].get("Set-Cookie", ""))
        if r[0] == 302 and m:
            self.cookie = m.group(1)
        return r


NOTIFY_TAG = '<script src="/notify.js" defer></script>'


def no_js(b):
    return b.replace(NOTIFY_TAG, "")


def stop_out(p):
    stop(p)
    return (p.stdout.read() or "") + (p.stderr.read() or "")


import urllib.parse  # noqa: E402

base = free_range(2)
env, d = make_env("main", HERDR_WEBUI_RANGE=f"{base}-{base + 1}", WEBUI_LOCK_SEC="3")
RUN = env["WEBUI_RUN_DIR"]
make_db(env["WEBUI_TB_DB"])
os.makedirs(env["WEBUI_SCHED_LOGS"])
os.makedirs(env["WEBUI_SCHED_JOBS"])
open(os.path.join(env["WEBUI_SCHED_JOBS"], "job1.conf"), "w").write(
    "COMMAND='echo hi'\nWINDOW=09:00-21:00\nINTERVAL_MIN=10\nTZ=Asia/Tokyo\n")
open(os.path.join(env["WEBUI_SCHED_LOGS"], "job1.log"), "w").write(
    json.dumps({"ts": "2026-11-06T12:00:00+0900", "job": "job1", "action": "ran", "reason": "exit 0", "exit": 0}) + "\n")
_J = env["WEBUI_SCHED_JOBS"]
_REPO = os.path.dirname(os.path.dirname(HERE))
_OUTSIDE = os.path.join(TMP, "outside-prompt.txt")
open(_OUTSIDE, "w").write("OUTSIDE-SECRET-BODY")
_FAKE_TOK = "gh" + "p_" + "x" * 24   # 架空の値(分割して連結。ソースに token 形の文字列を置かない)
open(os.path.join(_J, "pj.conf"), "w").write(
    "TARGET=alpha\nPROMPT='roles/x.md を読んで <b>bold</b> $HOME ${FOO} \"quoted\" " + _FAKE_TOK + " を使う'\n"
    "WINDOW=00:00-23:59\nINTERVAL_MIN=10\nDAYS=daily\nSKIP_IF=\"working blocked\"\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "pcomment.conf"), "w").write("TARGET=alpha\nPROMPT='REAL-PROMPT'\n# PROMPT='ignored-line'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
os.makedirs(os.path.join(_J, ".ssh"), exist_ok=True)
open(os.path.join(_J, ".ssh", "key.txt"), "w").write("DENIED-KEY-BODY")
open(os.path.join(_J, ".env"), "w").write("DENIED-ENV-BODY")
os.symlink(os.path.join(_J, ".ssh", "key.txt"), os.path.join(_J, "link.txt"))
for _n, _p in (("pdeny1", os.path.join(_J, ".ssh", "key.txt")), ("pdeny2", os.path.join(_J, ".env")), ("pdeny3", os.path.join(_J, "link.txt"))):
    open(os.path.join(_J, _n + ".conf"), "w").write(f"TARGET=alpha\nPROMPT_FILE={_p}\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "ptb.conf"), "w").write(f"TARGET=alpha\nPROMPT_FILE={os.path.join(_REPO, 'taskboard', 'config', 'approvals-catalog.conf')}\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "pvar.conf"), "w").write("TARGET=alpha\nPROMPT_FILE=$HOME/x.txt\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "cmdsub.conf"), "w").write("COMMAND=$(echo hi; date) # comment\nWINDOW=00:00-23:59\nINTERVAL_SEC=120\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "cmdplain.conf"), "w").write("COMMAND=bin/tb-prwatch\nWINDOW=00:00-23:59\nINTERVAL_SEC=120\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "cmdbig.conf"), "w").write("COMMAND='echo hi'\n" + "# pad\n" * 12000 + "WINDOW=00:00-23:59\nINTERVAL_SEC=120\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "pmissing.conf"), "w").write("TARGET=no-such-agent\nPROMPT='hello'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "pmulti.conf"), "w").write("TARGET=alpha\nPROMPT='line1\n" + "\n".join(f"line{i}" for i in range(2, 30)) + "'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "pfin.conf"), "w").write(f"TARGET=alpha\nPROMPT_FILE={os.path.join(_REPO, 'scheduler', 'README.md')}\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "pfout.conf"), "w").write(f"TARGET=alpha\nPROMPT_FILE={_OUTSIDE}\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "pfrel.conf"), "w").write("TARGET=alpha\nPROMPT_FILE=../../etc/passwd\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "cmdx.conf"), "w").write("COMMAND='cd /x && TOKEN=supersecretvalue1 bin/tb <i>y</i> $(date)'\nCOMMAND_TIMEOUT=45\nWINDOW=00:00-23:59\nINTERVAL_SEC=120\nTZ=Asia/Tokyo\n")
# 権限(approvals.conf)の fixture。順位・警告・届かない行・エスケープの確認用
with open(env["WEBUI_APPROVALS"], "w") as f:
    f.write("# 先頭の注釈\nsend:info:* = allow   # 2026-11-01T00:00:00Z に人が変更\nsend:task:dd-* = allow\nsend:task:* = ask\n"
            "agent:* = allow   # 2026-11-02T00:00:00Z に人が追加\nrole:* = ask\nrole:change = allow\n"
            "x<b>bold</b>y = allow\n* = ask\n")
open(os.path.join(env["WEBUI_SCHED_LOGS"], "job2.log"), "w").write(
    json.dumps({"ts": "2026-11-06T11:00:00+0900", "job": "job2", "action": "ran", "reason": "exit 0"}) + "\n" +
    json.dumps({"ts": "2026-11-06T11:10:00+0900", "job": "job2", "action": "skipped", "reason": "busy"}) + "\n" +
    json.dumps({"ts": "2026-11-06T11:20:00+0900", "job": "job2", "action": "failed", "reason": "exit 3 " + XSS}) + "\n" +
    json.dumps({"ts": "2026-11-06T11:30:00+0900", "job": "job2", "action": "skipped", "reason": "window"}) + "\n")
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO proxy_requests (task_id,cmd,why,effect,status,created_at,updated_at) VALUES "
           "('T-1','git push " + XSS + "','push したい','external','pending-human','2026-11-06T00:00:00Z','2026-11-06T00:00:00Z')")
_c.execute("INSERT INTO proxy_requests (task_id,cmd,why,effect,status,created_at,updated_at) VALUES "
           "('T-1','ls','見る','read-only','done','2026-11-06T00:00:00Z','2026-11-06T00:00:00Z')")
_c.execute("INSERT INTO approval_changes (pattern,value,created_at) VALUES ('agent:*','allow','2026-11-02T00:00:00Z')")
# 送信履歴(messages)の fixture: send 5 件(O-7 付き、本文 2 行)、failed の proposal 150 件、report 110 件、scheduler / busy も
for i in range(5):
    _c.execute("INSERT INTO messages (kind,ref,target,body,result,created_at) VALUES ('send',?,'alpha',?,'sent','2026-11-05T01:00:00Z')",
               (f"O-{7 + i}", "一行目 " + XSS + f" s{i}\nSECOND-LINE-SECRET-{i}"))
for i in range(150):
    _c.execute("INSERT INTO messages (kind,ref,target,body,result,created_at) VALUES ('proposal','P-1','beta',?,'failed','2026-11-04T01:00:00Z')", (f"failbody-{i}",))
for i in range(110):
    _c.execute("INSERT INTO messages (kind,ref,target,body,result,created_at) VALUES ('report','T-1','gamma',?,'sent','2026-11-06T01:00:00Z')", (f"reportbody-{i}",))
_c.execute("INSERT INTO messages (kind,ref,target,body,result,created_at) VALUES ('scheduler','j1','alpha','sched-busy','busy','2026-11-03T01:00:00Z')")
_c.execute("INSERT INTO messages (kind,ref,target,body,result,created_at) VALUES ('send','O-99','alpha',?,'sent','2026-11-05T02:00:00Z')", ("\nSECOND-LINE-SECRET-LEAD",))

# タスク一覧の fixture(親子・担当・PJ・未報告・停滞・長い題・長い報告)
_now_iso = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
_t_ins = "INSERT INTO tasks VALUES (?,?,?,?,NULL,NULL,?,?,'[]',NULL,NULL,?,?,?,'default')"
for _row in [("T-2", "pj", "親 " + XSS, "beta", "todo", None, None, _now_iso, _now_iso),
             ("T-3", "pj2", "子", "alpha", "in_progress", "T-2", "2026-11-07T00:00:00Z", _now_iso, _now_iso),
             ("T-4", "pj2", "x" * 200 + "TITLETAIL", "alpha", "done", "T-2", "2026-11-07T00:00:00Z", _now_iso, _now_iso),
             ("T-5", "pj2", "古い", "gamma", "blocked", None, None, "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z")]:
    _c.execute(_t_ins, _row)
_cm = "INSERT INTO comments (task_id,kind,text,from_name,from_pane,from_session,created_at) VALUES (?,?,?,'alpha','w1:p1','s','2026-11-07T00:00:00Z')"
_c.execute(_cm, ("T-3", "progress", "進捗1")); _c.execute(_cm, ("T-3", "progress", "進捗2"))
_c.execute(_cm, ("T-3", "blocked", "解決済みの詰まり"))
_c.execute("UPDATE comments SET resolved_at='2026-11-08T00:00:00Z' WHERE text='解決済みの詰まり'")
_c.commit()
_c.close()
# 旧方式の run/token が残っていても無視される
os.makedirs(RUN, exist_ok=True)
open(os.path.join(RUN, "token"), "w").write("legacy-token-legacy-token-legacy-token-0000\n")
p = start(env)
port = wait_port(env, p)
check("起動して port が分かる", port is not None)
cl = Client(port)
PAGES = ["/", "/notifications", "/agents", "/agents/alpha", "/tasks", "/tasks/T-1", "/inbox", "/outbox", "/jobs", "/ledger", "/perms"]

# ---- 初回設定
for path in PAGES:
    r = cl.get(path)
    check(f"未設定: {path} は /setup へ 302", r[0] == 302 and r[1].get("Location") == "/setup", str(r[:2]))
check("未設定: /login は /setup へ", cl.get("/login")[1].get("Location") == "/setup")
check("run/token は作られない", open(os.path.join(RUN, "token")).read().startswith("legacy"))
r = cl.get("/setup")
check("GET /setup が 200 でフォーム", r[0] == 200 and "name=confirm" in r[2] and "<form" in r[2])
cs = cl.csrf("/setup")
check("Origin 不一致の /setup POST は拒否",
      cl.raw("POST", "/setup", {"csrf": cs, "username": USER, "password": PW, "confirm": PW}, origin="http://evil.example")[0] == 403)
check("Origin も Referer も無い POST は拒否",
      cl.raw("POST", "/setup", {"csrf": cs, "username": USER, "password": PW, "confirm": PW}, origin=None)[0] == 403)
check("CSRF トークン無しの /setup POST は拒否",
      cl.raw("POST", "/setup", {"username": USER, "password": PW, "confirm": PW})[0] == 403)
check("改ざん CSRF トークンは拒否",
      cl.raw("POST", "/setup", {"csrf": cs[:-1] + ("0" if cs[-1] != "0" else "1"), "username": USER, "password": PW,
                               "confirm": PW})[0] == 403)
check("パスワード 12 文字未満は拒否", cl.raw("POST", "/setup", {"csrf": cs, "username": USER, "password": "short-11-ch", "confirm": "short-11-ch"})[0] == 400)
check("確認入力の不一致は拒否", cl.raw("POST", "/setup", {"csrf": cs, "username": USER, "password": PW, "confirm": PW + "x"})[0] == 400)
check("拒否の間は auth.json が作られない", not os.path.exists(os.path.join(RUN, "auth.json")))
r = cl.raw("POST", "/setup", {"csrf": cs, "username": USER, "password": PW, "confirm": PW})
check("初回設定が成功(302 + Cookie)", r[0] == 302 and "Set-Cookie" in r[1], str(r[:2]))
ck = r[1].get("Set-Cookie", "")
check("Cookie が HttpOnly / SameSite=Strict / Path=/(ログインは全セッション共通)", "HttpOnly" in ck and "SameSite=Strict" in ck and "Path=/;" in ck, ck.split("=")[0])
check("Cookie の有効期間が 12 時間", "Max-Age=43200" in ck, ck.split(";")[-1])
check("auth.json が 0600", mode(os.path.join(RUN, "auth.json")) == "0o600", mode(os.path.join(RUN, "auth.json")))
check("sessions.json が 0600", mode(os.path.join(RUN, "sessions.json")) == "0o600")
raw_auth = open(os.path.join(RUN, "auth.json")).read()
check("auth.json に平文パスワードを含まない", PW not in raw_auth)
aj = json.load(open(os.path.join(RUN, "auth.json")))
hrec = [v for v in aj.values() if isinstance(v, dict)][0]
check("scrypt・salt 16 バイト以上・パラメータを記録",
      hrec.get("algo") == "scrypt" and len(hrec["salt"]) >= 32 and all(k in hrec for k in ("n", "r", "p")))
check("/setup は以後 404(GET)", cl.get("/setup")[0] == 404)
check("/setup は以後 404(POST)", cl.raw("POST", "/setup", {"csrf": cs, "username": "x", "password": PW, "confirm": PW})[0] == 404)

# ---- 未ログイン
anon = Client(port)
for path in PAGES:
    r = anon.get(path)
    check(f"未ログイン: {path} は /login へ 302", r[0] == 302 and r[1]["Location"].startswith("/login?next="), str(r[:2]))
check("next に元の URL を渡す", anon.get("/tasks?project=pj")[1]["Location"] == "/login?next=%2Ftasks%3Fproject%3Dpj")
check("未ログインの未知 URL も /login へ", anon.get("/nope")[0] == 302)
check("GET /login が 200", anon.get("/login")[0] == 200)
check("不正な Host は 403", anon.get("/login", host="evil.example")[0] == 403)
check("?token= の経路がない(Cookie も出ない)",
      "Set-Cookie" not in anon.get("/?token=" + open(os.path.join(RUN, "token")).read().strip())[1])
check("旧 run/token の値では入れない", anon.get("/agents?token=legacy-token-legacy-token-legacy-token-0000")[0] == 302)
for path in ["/login", "/"]:
    check(f"ログイン画面に token の導線がない({path})", "token=" not in anon.get(path)[2].lower().replace("csrf", ""))

# ---- ログイン
check("CSRF トークン無しの /login は拒否", anon.raw("POST", "/login", {"username": USER, "password": PW})[0] == 403)
check("Origin 不一致の /login は拒否",
      anon.login(origin="http://evil.example")[0] == 403 and anon.cookie is None)
r = anon.login(user=USER, pw=WRONG)
check("誤ったパスワードで拒否(401・Cookie なし)", r[0] == 401 and anon.cookie is None and "Set-Cookie" not in r[1])
msg_pw = re.search(r"class=err>([^<]*)", r[2]).group(1)
r2 = anon.login(user="nobody", pw=PW)
msg_user = re.search(r"class=err>([^<]*)", r2[2]).group(1)
check("ユーザー名違いとパスワード違いでメッセージが同じ", msg_pw == msg_user and r2[0] == 401, f"{msg_pw}|{msg_user}")
check("エラー画面に入力値を戻さない", WRONG not in r[2] and PW not in r2[2])
# ロックアウト(ここまでで 2 回失敗、LOCK_MAX=5)
for _ in range(3):
    r = anon.login(user=USER, pw=WRONG)
check("5 回失敗でロック(正しいパスワードでも 429)", anon.login()[0] == 429 and anon.cookie is None)
check("429 に Retry-After", "Retry-After" in anon.login()[1])
time.sleep(3.3)
r = anon.login(next_="/tasks")
check("ロック時間の経過後にログインできる(next へ)", r[0] == 302 and r[1]["Location"] == "/tasks" and anon.cookie, str(r[:2]))
for ext in ["http://evil.example/x", "//evil.example/x", "https://evil.example", "/\\evil.example", "javascript:alert(1)"]:
    c2 = Client(port)
    r = c2.login(next_=ext)
    check(f"next に外部 URL({ext})を渡しても無視", r[0] == 302 and r[1]["Location"] == "/", r[1].get("Location", ""))
check("未ログインの next も外部 URL は無視",
      "evil" not in Client(port).get("/login?next=http%3A%2F%2Fevil.example%2F")[2])

# ---- ログイン後のページ
cl = Client(port)
cl.login()
sess_cookie = cl.cookie
cookie_val = sess_cookie.split("=", 1)[1]
sj = open(os.path.join(RUN, "sessions.json")).read()
check("sessions.json に Cookie 値そのものを含まない(ハッシュのみ)", cookie_val not in sj and hashlib.sha256(cookie_val.encode()).hexdigest() in sj)
check("sessions.json に有効期限(約 12 時間後)", any(abs(v - time.time() - 43200) < 60 for v in json.load(open(os.path.join(RUN, "sessions.json"))).values()))
check("sessions.json が 0600", mode(os.path.join(RUN, "sessions.json")) == "0o600")
check("ログイン済みで /login は元のページへ", cl.get("/login?next=/agents")[1].get("Location") == "/agents")

for path in PAGES:
    s_, h, b = cl.get(path)
    check(f"GET {path} が 200", s_ == 200, str(s_))
    # 入力欄(対応済みにする)があるページは自動更新しない
    check(f"{path}: 共通ナビ・meta refresh・バッジ",
          (('http-equiv="refresh"' in b) != ('action="/resolve"' in b or path in ("/ledger", "/perms", "/tasks"))) and 'href="/ledger"' in b and 'class="badge"' in b, path)
    check(f"{path}: スクリプトは同一オリジンの notify.js だけ",
          re.findall(r"<script[^>]*>", b, re.I) == [NOTIFY_TAG[:-9]] and "<script" not in no_js(b).lower()
          and "<img" not in b.lower(), str(re.findall(r"<script[^>]*>", b, re.I)))
    check(f"{path}: 制御文字なし", not re.search("[\x00-\x08\x0b-\x1f\x1b]", b))
    check(f"{path}: ?token= の経路がない", "token=" not in b)

b = cl.get("/agents/alpha")[2]
check("エスケープされて表示", "&lt;script&gt;alert(1)&lt;/script&gt;" in b)
check("ANSI/OSC を除去(本文は残る)", "red" in b and "evil title" not in b and "\\x1b" not in b)
check("resume コマンドは出さず、使わないよう注意する", "claude --resume 1111" not in b and "claude --resume を実行しないでください" in b)
check("attach 欄にインラインのスクリプトがない", "onfocus" not in b)
check("名前に HTML を含む agent も安全", "&lt;img src=x onerror=alert(2)&gt;" in cl.get("/agents")[2])
check("pane_id フォールバックの URL", cl.get("/agents/w1%3Ap3")[0] == 200)
_ab = cl.get("/agents")[2]
check("agent 一覧にロール別と関係グラフへのリンク", '<h2 id="roles">ロール別</h2>' in _ab and 'href="/agent-graph"' in _ab and _ab.count('<a href="/agents/alpha">') >= 2)
_gs, _gh, _gb = cl.get("/agent-graph")
check("関係グラフ: mermaid と表、スクリプトは同一オリジンだけ", _gs == 200 and "<pre class=mermaid>flowchart LR" in _gb
      and re.findall(r"<script[^>]*>", _gb, re.I) == [NOTIFY_TAG[:-9], '<script src="/static/mermaid.min.js" defer>', '<script src="/docs.js" defer>'],
      str(re.findall(r"<script[^>]*>", _gb, re.I)))
_gm = _gb.split("<pre class=mermaid>")[1].split("</pre>")[0]
check("関係グラフ: 名前のない agent も pane_id で点になる", '[&quot;w1:p3&quot;]' in _gm, _gm[:300])
check("関係グラフ: HTML を含む名前もラベルでは安全な文字だけ", "&lt;" not in _gm and "_img_src_x_onerror_alert_2_" in _gm, _gm[:300])
check("未知の agent は 404", cl.get("/agents/nope")[0] == 404)
check("未知のタスクは 404", cl.get("/tasks/nope")[0] == 404)
check("未知の URL は 404", cl.get("/nope")[0] == 404)
check("パストラバーサル風も 404", cl.get("/agents/..%2F..%2Fetc")[0] == 404)
inbox = cl.get("/inbox")[2]
check("承認待ちバッジの件数(question 1 + pending-human 1 + blocked agent 1)", '<span class="badge">3</span>' in inbox)

# ---- 緊急通知(/notifications・/api/notifications・/notify.js)
r = cl.get("/notifications")
check("GET /notifications が 200", r[0] == 200, str(r[0]))
nb = r[2]
for label in ("質問", "代行依頼", "承認待ちの agent", "滞留", "定期実行の失敗"):
    check(f"/notifications に {label}", label in nb)
check("/notifications: 通知ボタンと状態表示", "id=nenable" in nb and "id=nstate" in nb)
check("/notifications: 外部由来の値はエスケープ", "exit 3 &lt;script&gt;" in nb and "<script>alert" not in nb)
check("ナビに緊急通知バッジ(5 件)", '<span id=nbadge class="badge">5</span>' in inbox, re.findall(r"<span id=nbadge[^<]*<", inbox))
r = cl.get("/api/notifications")
check("/api/notifications が 200 JSON", r[0] == 200 and r[1].get("Content-Type", "").startswith("application/json"), str(r[:2]))
items = json.loads(r[2])["items"] if r[0] == 200 else []
ids = [it["id"] for it in items]
check("通知 id(報告・代行依頼・blocked・滞留・ジョブ失敗)",
      ids[:2] == ["c:1", "p:1"] and "a:<img src=x onerror=alert(2)>" in ids and "s:alpha:2026-11-06T00:00:00Z" in ids
      and "j:job2:2026-11-06T11:20:00+0900" in ids and len(ids) == 5, str(ids))
check("成功したジョブ・完了した代行依頼は通知しない", not any(i.startswith("j:job1") or i == "p:2" for i in ids))
check("href はすべてサーバー内の絶対パス", all(it["href"].startswith("/") and not it["href"].startswith("//") for it in items))
check("blocked の通知は承認待ちの内容へ", any(it["href"].endswith("#approval") for it in items if it["kind"] == "agent_blocked"))
check("通知の値に制御文字なし", not any(re.search("[\x00-\x08\x0b-\x1f\x1b]", str(v)) for it in items for v in it.values()))
check("/api/notifications も Host 検査", cl.get("/api/notifications", host="evil.example")[0] == 403)
r = Client(port).get("/api/notifications")
check("未ログインの /api/notifications は 401(リダイレクトせず中身も出さない)",
      r[0] == 401 and "Location" not in r[1] and "質問" not in r[2] and "alpha" not in r[2], str(r[:2]))
check("無効な Cookie の /api/notifications も 401", Client(port).get("/api/notifications", cookie="herdr_webui_session=nope")[0] == 401)
check("POST /api/notifications は 405", cl.raw("POST", "/api/notifications", {"a": "b"})[0] == 405)
# 送信の承認待ち(tb send が approvals.conf で ask になったもの)
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.executescript("INSERT INTO outbox (from_name,from_pane,from_session,target,kind,text,status,created_at) VALUES ('conductor','w0:p1','s','w1','task','調べて <b>x</b>','pending','t');"
                 "INSERT INTO outbox (from_name,from_pane,from_session,target,kind,text,status,created_at) VALUES ('conductor','w0:p1','s','w1','task','済','sent','t');")
_c.commit(); _c.close()
time.sleep(3.2)  # 通知のキャッシュ切れを待つ
r = cl.get("/api/notifications")
_it = [it for it in json.loads(r[2])["items"] if it["id"].startswith("o:")] if r[0] == 200 else []
check("送信の承認待ちが通知に出る(承認コマンド付き)", [it["id"] for it in _it] == ["o:1"] and "tb approve O-1" in _it[0]["how"] and "tb approve" not in _it[0]["body"], str(_it))
check("送信の承認待ちは /notifications でエスケープ", "&lt;b&gt;x&lt;/b&gt;" in cl.get("/notifications")[2])
_nb = cl.get("/notifications")[2]
check("通知は 3 行(何が / 中身 / 対応)で、対応に承認コマンドが出る", "対応: 送ってよければ手元で tb approve O-1" in _nb)
_ib = cl.get("/inbox")[2]
check("報告・承認待ち: 承認待ちの節に、どこでどう承認するかが出る",
      "承認待ち(どこで・どう承認するか)" in _ib and "tb approve O-1" in _ib.split("要対応の報告")[0] and "手元で tb …" in _ib)
check("報告・承認待ち: 質問には答え方(tb send --kind answer)と対応済みの付け方が出る", "--kind answer" in _ib and "tb resolve C-" in _ib)
# outbox 一覧(読み取り専用。queued が 60 秒超なら強調)
_old = (datetime.now(timezone.utc) - timedelta(seconds=300)).strftime("%Y-%m-%dT%H:%M:%SZ")
_new = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO outbox (from_name,from_pane,from_session,target,kind,task_id,text,status,created_at) VALUES ('conductor','w0:p1','s','w9','info','T-1',?,'queued',?)", ("古い<script>alert(1)</script>" + "x" * 200, _old))
_c.execute("INSERT INTO outbox (from_name,from_pane,from_session,target,kind,text,status,created_at) VALUES ('conductor','w0:p1','s','w8','info','新しい','queued',?)", (_new,))
_c.commit(); _c.close()
ob = cl.get("/outbox")
check("/outbox は 200", ob[0] == 200)
check("outbox は pending/sent/queued を全部出す", all(x in ob[2] for x in (">pending<", ">sent<", "w8", "w9")))
check("outbox の本文はエスケープ", "<script>alert(1)" not in ob[2] and "&lt;script&gt;" in ob[2])
check("outbox の本文は先頭 80 文字まで", "x" * 80 not in ob[2] and "x" * 40 in ob[2])
check("queued が 60 秒超の行だけ強調", ob[2].count("<span class=warn>queued</span>") == 1, str(ob[2].count("<span class=warn>queued</span>")))
check("outbox は新しい順", ob[2].index("O-4") < ob[2].index("O-3") < ob[2].index("O-1"))
check("outbox に操作フォームなし", "<form action" not in ob[2].split("</nav>", 1)[1] and "approve</button" not in ob[2])
check("POST /outbox は 405", cl.raw("POST", "/outbox", {"a": "b"})[0] == 405)
check("未ログインの /outbox は /login へ", Client(port).get("/outbox")[0] == 302)
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.executescript("INSERT INTO alerts (fingerprint,source,first_seen,last_seen,severity,title,status,result_summary,analyzed_at) VALUES ('f1','watch','t0','t0','critical','API <i>5xx</i>','analyzed','設定漏れ','t1');"
                 "INSERT INTO alerts (fingerprint,source,first_seen,last_seen,severity,title,status,result_summary,analyzed_at) VALUES ('f2','watch','t0','t0','low','軽微','analyzed','誤報','t2');"
                 "INSERT INTO alerts (fingerprint,source,first_seen,title,status,body,last_seen) VALUES ('f3','watch','t0','ディスク','awaiting-approval','80%','t3');"
                 "INSERT INTO alerts (fingerprint,source,first_seen,last_seen,title,status) VALUES ('f4','watch','t0','t0','解析中','analyzing');")
_c.commit(); _c.close()
time.sleep(3.2)
_ids = [it["id"] for it in json.loads(cl.get("/api/notifications")[2])["items"] if it["id"][:2] in ("r:", "w:")]
check("アラート: critical/high の解析結果と起動待ちだけ通知", _ids == ["r:1:t1", "w:3"], str(_ids))
check("アラート: 題名はエスケープ", "API &lt;i&gt;5xx&lt;/i&gt;" in cl.get("/notifications")[2])
r = cl.get("/notify.js")
check("/notify.js が 200 の JavaScript", r[0] == 200 and r[1].get("Content-Type", "").startswith("text/javascript")
      and "Notification" in r[2] and "/api/notifications" in r[2], str(r[:2]))
check("/notify.js は innerHTML を使わない", "innerHTML" not in r[2] and "eval(" not in r[2])
check("未ログインの /notify.js は /login へ", Client(port).get("/notify.js")[0] == 302)
csp = r[1].get("Content-Security-Policy", "")
check("CSP は script-src 'self' / connect-src 'self' だけを足す(インラインスクリプト不可)",
      "default-src 'none'" in csp and "script-src 'self';" in csp and "connect-src 'self';" in csp
      and "unsafe-inline'; base-uri" in csp and csp.count("unsafe-inline") == 1, csp)
check("CORP same-origin", r[1].get("Cross-Origin-Resource-Policy") == "same-origin")
if subprocess.run(["which", "node"], capture_output=True).returncode == 0:
    jsf = os.path.join(TMP, "notify.js")
    open(jsf, "w").write(r[2])
    check("notify.js の構文(node --check)", subprocess.run(["node", "--check", jsf], capture_output=True).returncode == 0)

# ---- 送信履歴(/ledger)と権限(/perms): 読み取りだけ
L = cl.get("/ledger")
check("送信履歴: 200 で、旧 dispatch.jsonl の記述が無い", L[0] == 200 and "dispatch.jsonl" not in L[2].lower() and "<h1>送信履歴</h1>" in L[2])
check("送信履歴: 種別ごとの件数サマリ(failed 150 / report 110)", "<h2>種別ごとの件数</h2>" in L[2] and ">150<" in L[2] and ">110<" in L[2])
check("送信履歴: 既定は最大 100 件で failed を畳む", L[2].count("reportbody-") == 98 and "failbody-" not in L[2] and "畳んでいます" in L[2])
K = cl.get("/ledger?kind=send")
check("送信履歴: send は O-n を /outbox へリンクし、本文は 1 行目だけ",
      '<a href="/outbox">O-7</a>' in K[2] and "SECOND-LINE-SECRET" not in K[2] and "一行目" in K[2])
check("送信履歴: 本文の HTML はエスケープ", XSS not in K[2] and "&lt;script&gt;" in K[2])
F = cl.get("/ledger?result=failed")
check("送信履歴: result=failed で failed だけ(最大 100 件)", F[2].count("failbody-") == 100 and "reportbody-" not in F[2])
A = cl.get("/ledger?result=all&kind=proposal")
check("送信履歴: result=all で failed も出る", "failbody-" in A[2] and "畳んでいます" not in A[2] and "failbody-" not in cl.get("/ledger?kind=proposal")[2])
check("送信履歴: kind=send では他種別の failed を畳み件数に数えない", "畳んでいます" not in K[2])
_KP = cl.get("/ledger?kind=proposal")[2]
check("送信履歴: kind=proposal は failed 150 件を畳み件数に出す", "failed 150 件は畳んでいます" in _KP)
check("送信履歴: 全角数字の日付は不正", "YYYY-MM-DD" in cl.get("/ledger?from=%EF%BC%92%EF%BC%90%EF%BC%92%EF%BC%96-%EF%BC%90%EF%BC%91-%EF%BC%90%EF%BC%91")[2])

# ---- タスク一覧・詳細(読み取りのみ)
def _ids(path):
    h = cl.get(path)[2]
    t = h.split("</form>", 1)[1] if "</form>" in h else h
    return list(dict.fromkeys(re.findall(r'<tr><td><a href="/tasks/([A-Z]-[A-Za-z0-9_-]+)">', t)))
# 未解決の質問は要対応の件数に入るので、この区間だけテスト側で足して最後に消す
_cx = sqlite3.connect(env["WEBUI_TB_DB"])
_cx.execute(_cm, ("T-3", "question", "長い質問 " + "あ" * 3000 + "REPORT-TAIL"))
_cx.commit()
TL = cl.get("/tasks")
check("タスク: 状態の件数サマリ", all(x in TL[2] for x in ("todo 1", "in_progress 2", "review 0", "done 1", "blocked 1", "全部 5")), "")
check("タスク: 既定は 5 件すべて", sorted(_ids("/tasks")) == ["T-1", "T-2", "T-3", "T-4", "T-5"], str(_ids("/tasks")))
check("タスク: PJ で絞る(件数サマリも PJ 内)", sorted(_ids("/tasks?project=pj2")) == ["T-3", "T-4", "T-5"] and "in_progress 1" in cl.get("/tasks?project=pj2")[2])
check("タスク: 担当で絞る", sorted(_ids("/tasks?owner=alpha")) == ["T-1", "T-3", "T-4"])
check("タスク: 担当 + 状態", _ids("/tasks?owner=alpha&status=done") == ["T-4"])
check("タスク: 担当 + PJ + 状態", _ids("/tasks?owner=alpha&project=pj2&status=in_progress") == ["T-3"])
check("タスク: 未報告だけ", sorted(_ids("/tasks?unreported=1")) == ["T-1", "T-2", "T-5"])
check("タスク: 未報告 + 担当", _ids("/tasks?unreported=1&owner=alpha") == ["T-1"])
check("タスク: 停滞だけ(in_progress で更新が古いもの)", _ids("/tasks?stale=1") == ["T-1"])
_t1row = re.search(r'<tr><td><a href="/tasks/T-1">.*?</tr>', TL[2], re.S)
check("タスク: 停滞の目印は T-1 の行だけ", bool(_t1row) and "停滞 " in _t1row.group(0) and TL[2].count("停滞 ") == 1 and _ids("/tasks?stale=1&owner=beta") == [])
check("タスク: 状態の件数が停滞の絞り込みを反映しない旨の注記", "「停滞だけ」は反映しません" in TL[2])
check("タスク: 並び替え(更新が古い順 / ID 順)", _ids("/tasks?sort=oldest")[:2] == ["T-5", "T-1"] and _ids("/tasks?sort=id") == ["T-1", "T-2", "T-3", "T-4", "T-5"])
check("タスク: 親子の表示(親リンクと子の件数)", '<a href="/tasks/T-2">T-2</a>' in TL[2] and "子 2" in TL[2])
check("タスク: 報告の種別ごとの件数と未対応", "progress 2" in TL[2] and "question 1" in TL[2] and "未対応 1" in TL[2])
check("タスク: 題は先頭 80 文字だけ", "TITLETAIL" not in TL[2] and ("x" * 80) in TL[2] and ("x" * 100) not in TL[2])
check("タスク: 題の HTML はエスケープ", XSS not in TL[2] and "&lt;script&gt;" in TL[2])
check("タスク: 期限・優先度の列は無い", "期限" not in TL[2].split("<table>")[1].split("</tr>")[0] and "優先度" not in TL[2].split("<table>")[1].split("</tr>")[0])
for bad in ["status=zzz", "owner=a%20b%3Cx", "sort=zz", "project=%27%3B--", "unreported=%27"]:
    r = cl.get("/tasks?" + bad)
    check(f"タスク: 不正な絞り込み {bad} でも 200", r[0] == 200 and "'--" not in r[2], str(r[0]))
check("タスク: 自動更新しない(入力欄あり)", 'http-equiv="refresh"' not in TL[2])
check("タスク: POST は 405", cl.raw("POST", "/tasks", {"a": "b"})[0] == 405)
TD = cl.get("/tasks/T-3")[2]
check("タスク詳細: 親へのリンク・session・報告の件数", 'href="/tasks/T-2"' in TD and "progress 2" in TD and "未対応 1" in TD)
check("タスク詳細: 報告は先頭 1000 文字だけで、全文の読み方を案内する", "REPORT-TAIL" not in TD and "長い質問" in TD and "…" in TD and "tb comments T-3" in TD)
check("タスク詳細: 人が対応済みにした報告が分かる", "対応済み 2026-11-08T00:00:00Z" in TD)
T2 = cl.get("/tasks/T-2")[2]
check("タスク詳細: 子タスクの一覧", "<h2>子タスク</h2>" in T2 and 'href="/tasks/T-3"' in T2 and 'href="/tasks/T-4"' in T2)
check("タスク詳細: 停滞の目印(T-1)", "停滞: in_progress のまま" in cl.get("/tasks/T-1")[2])
_cx.execute("DELETE FROM comments WHERE text LIKE '長い質問%'")
_cx.commit()
_cx.close()
# 300 件を超えるとき: 件数を知らせ、blocked / in_progress / 未対応の報告を先に出す(後ろの PJ でも隠れない)
_cy = sqlite3.connect(env["WEBUI_TB_DB"])
for _i in range(320):
    _cy.execute(_t_ins, (f"D-{_i:03d}", "zz", f"done{_i}", "delta", "done", None, "2026-11-07T00:00:00Z", _now_iso, _now_iso))
_cy.execute(_t_ins, ("B-1", "zzz", "後ろの PJ の blocked", "delta", "blocked", None, None, _now_iso, _now_iso))
_cy.execute(_t_ins, ("P-1", "zzz", "後ろの PJ の in_progress", "delta", "in_progress", None, None, _now_iso, _now_iso))
_cy.execute(_cm, ("T-2", "question", "todo のタスクの未解決の質問"))
_cy.commit()
_BIG = cl.get("/tasks")[2]
check("タスク(300 件超): 「N 件のうち 300 件だけ表示」と明示する", "327 件のうち 300 件だけ表示しています" in _BIG, re.findall(r"\d+ 件のうち[^<]*", _BIG)[:1].__str__())
_big_ids = _ids("/tasks")
check("タスク(300 件超): 既定の並びは 300 行で、blocked・in_progress が後ろの PJ でも出る", len(_big_ids) == 300 and "B-1" in _big_ids and "P-1" in _big_ids, str(len(_big_ids)))
check("タスク(300 件超): 未解決の報告があるタスクが先頭、次に blocked → in_progress → done", set(_big_ids[:2]) == {"T-1", "T-2"} and _big_ids.index("B-1") < _big_ids.index("P-1") < _big_ids.index("D-000"), str(_big_ids[:6]))
check("タスク(300 件超): 従来の PJ 順だと切り詰めで隠れる(注記は出る)", "B-1" not in _ids("/tasks?sort=project") and "件のうち 300 件だけ表示" in cl.get("/tasks?sort=project")[2])
check("タスク(300 件超): 絞り込めば残りを見られる", _ids("/tasks?status=blocked&owner=delta") == ["B-1"] and "件のうち" not in cl.get("/tasks?status=blocked")[2])
check("タスク(300 件超): 上限未満なら注記は出ない", "件のうち" not in cl.get("/tasks?project=pj2")[2])
_cy.execute("DELETE FROM tasks WHERE id LIKE 'D-%' OR id IN ('B-1','P-1')")
_cy.execute("DELETE FROM comments WHERE text LIKE 'todo のタスクの未解決%'")
_cy.commit()
_cy.close()
for _pth in ["/tasks?owner=alpha", "/tasks/T-3", "/tasks?stale=1"]:
    _r = Client(port).get(_pth)
    check(f"未ログインの {_pth} は /login へ(本文なし)", _r[0] == 302 and _r[1].get("Location", "").startswith("/login") and "T-3" not in _r[2] and "TITLETAIL" not in _r[2])

check("送信履歴: kind で絞る", K[2].count("SECOND") == 0 and "一行目" in K[2] and "reportbody-" not in K[2])
T = cl.get("/ledger?target=alpha")
check("送信履歴: 宛先で絞る(busy も出る)", "sched-busy" in T[2] and "reportbody-" not in T[2])
D = cl.get("/ledger?from=2026-11-05&to=2026-11-05&result=all")
check("送信履歴: 期間で絞る", "一行目" in D[2] and "reportbody-" not in D[2] and "failbody-" not in D[2])
for bad in ["kind=zzz", "target=a%20b%3Cx", "from=bad", "to=2026-13-xx%27", "result=%27%3B--"]:
    r = cl.get("/ledger?" + bad)
    check(f"送信履歴: 不正な絞り込み {bad} でも 200(エラー表示。SQL に渡さない)", r[0] == 200 and "<b" in r[2] and "'--" not in r[2], str(r[0]))
check("送信履歴: POST は 405", cl.raw("POST", "/ledger", {"a": "b"})[0] == 405)
check("送信履歴: 自動更新しない(入力欄あり)", 'http-equiv="refresh"' not in L[2])

_AP = env["WEBUI_APPROVALS"]
_m0 = os.stat(_AP).st_mtime_ns
P = cl.get("/perms")
check("権限: 200", P[0] == 200 and "approvals.conf" in P[2])
rows = re.findall(r"<tr><td>(\d+)</td><td><code[^>]*>([^<]*)</code></td><td>(?:<b[^>]*>)?(allow|ask)", P[2])
check("権限: 上から順に順位・パターン・値", [(a, b, c) for a, b, c in rows][:4] == [("1", "send:info:*", "allow"), ("2", "send:task:dd-*", "allow"), ("3", "send:task:*", "ask"), ("4", "agent:*", "allow")], str(rows[:5]))
check("権限: 行末の注記と変更履歴を併記", "2026-11-01T00:00:00Z に人が変更" in P[2] and "履歴: 2026-11-02T00:00:00Z に allow" in P[2])
check("権限: 一致の例", "<code>send:info:worker1</code>" in P[2] and "<code>agent:new:worker1</code>" in P[2])
check("権限: * = ask に警告", "全体の既定" in P[2])
check("権限: 広い allow(agent:*)に警告", "広い allow" in P[2])
check("権限: 宛先を絞った allow はワイルドカード注意だけ", "ワイルドカードの allow" in P[2])
check("権限: 上の行で隠れる行を指摘", "先に一致するため" in P[2] and "上の 5 行目" in P[2])
check("権限: 変更履歴の節", "変更履歴(直近 50 件)" in P[2] and "誰が" in P[2])
check("権限: 行のパターンの HTML はエスケープ", "<b>bold</b>" not in P[2] and "&lt;b&gt;bold" in P[2])
check("権限: 変更ボタンも POST フォームも無い(ログアウトだけ)", P[2].count("method=post") == 1 and 'action="/logout"' in P[2] and "<button" in P[2])
S1 = cl.get("/perms?op=send:task:dd-x")[2]
check("権限: 検索 send:task:dd-x は 2 行目で allow", "2 行目" in S1 and "<b class=\"warn\">allow</b>" in S1)
check("権限: 検索 send:task:w1 は 3 行目で ask", "3 行目" in cl.get("/perms?op=send:task:w1")[2])
check("権限: 検索 zzz:q は * = ask(8 行目)", "8 行目" in cl.get("/perms?op=zzz:q")[2])
check("権限: 不正な操作名は拒否", "使えない文字" in cl.get("/perms?op=a%20b%3Bc")[2] and cl.get("/perms?op=a%20b")[0] == 200)
C = cl.get("/perms?pat=send:task:w1&val=allow")[2]
check("権限: tb allow のコマンド行を表示するだけ", "<pre>tb allow send:task:w1</pre>" in C and "自分の端末" in C and "先頭" in C)
check("権限: * への allow に強い警告", "全体を allow にします" in cl.get("/perms?pat=*&val=allow")[2])
check("権限: 値・パターンが不正ならコマンドを出さない", "<pre>tb" not in cl.get("/perms?pat=a%3Bb&val=allow")[2] and "<pre>tb" not in cl.get("/perms?pat=ab&val=rm")[2])
check("権限: シェルの特殊文字を含むパターンは出さない", "<pre>tb" not in cl.get("/perms?pat=%24%28id%29&val=ask")[2])
check("権限: approvals.conf は GET の前後で変わらない(mtime)", os.stat(_AP).st_mtime_ns == _m0)
check("権限: 先頭が - の操作名・パターンは不正", "使えない文字" in cl.get("/perms?op=-x")[2] and "<pre>tb" not in cl.get("/perms?pat=-x&val=allow")[2])
check("権限: POST は 405", cl.raw("POST", "/perms", {"a": "b"})[0] == 405)
check("権限: 自動更新しない", 'http-equiv="refresh"' not in P[2])
_un = Client(port)
for _pth in ["/ledger", "/perms", "/perms?op=send:task:x", "/ledger?result=all"]:
    _r = _un.get(_pth)
    check(f"未ログインの {_pth} は /login へ(本文なし)", _r[0] == 302 and _r[1].get("Location", "").startswith("/login") and "send:" not in _r[2] and "approvals" not in _r[2])
check("定期実行が出る", "job1" in cl.get("/jobs")[2])

# ---- 定期実行の詳細: 何が・誰に・いつ・承認は(読み取りのみ)
_L = cl.get("/jobs")[2]
check("ジョブ一覧: 型と宛先が分かる", "<b>agent</b> <code>alpha</code> に送る" in _L and "<b>コマンド</b>" in _L, _L[_L.find("<table"):][:600])
_pj = cl.get("/jobs/pj")
_pjh = _pj[2]
check("ジョブ詳細: 型(PROMPT 型)", _pj[0] == 200 and "PROMPT 型" in _pjh and "agent にプロンプトを送る" in _pjh)
check("ジョブ詳細: 宛先と今の状態(working)", "<code>alpha</code> 今:" in _pjh and "working" in _pjh and "workspace ws-label" in _pjh)
check("ジョブ詳細: working は送らない旨", "idle / done 以外のときは送らず" in _pjh)
check("ジョブ詳細: 本文が出る(展開せず、エスケープ)", "roles/x.md を読んで" in _pjh and "&lt;b&gt;bold&lt;/b&gt;" in _pjh and "<b>bold</b>" not in _pjh
      and "$HOME" in _pjh and "${FOO}" in _pjh)
check("ジョブ詳細: トークンらしい文字列はマスク", _FAKE_TOK[:12] not in _pjh and "[マスク]" in _pjh and "1 件をマスク" in _pjh)
check("ジョブ詳細: SKIP_IF の表示", "working blocked" in _pjh)
check("ジョブ詳細: 次回の予定が出る", re.search(r"次回の予定\(目安\)</th><td>\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} → ", _pjh) is not None)
check("ジョブ詳細: 承認の扱い(scheduler は approvals.conf を見ない)", "approvals.conf を見ず" in _pjh and "send:task:alpha" in _pjh)
check("ジョブ詳細: 副作用は推測せず「不明」", "分かりません(不明)" in _pjh)
check("ジョブ詳細: 送信履歴へのリンク", 'href="/ledger?kind=scheduler&amp;target=alpha"' in _pjh)
check("ジョブ詳細: 編集フォームは増えていない(保存と戻すの 2 つだけ)", _pjh.count('<form method=post action="/jobs/pj"') == 2 and "name=prompt" not in _pjh and "name=command" not in _pjh and "name=target" not in _pjh)
_pcm = cl.get("/jobs/pcomment")[2]
check("ジョブ詳細: コメントアウトされた PROMPT は読まない", "REAL-PROMPT" in _pcm and "ignored-line" not in _pcm)
for _n, _body in (("pdeny1", "DENIED-KEY-BODY"), ("pdeny2", "DENIED-ENV-BODY"), ("pdeny3", "DENIED-KEY-BODY")):
    _d = cl.get("/jobs/" + _n)[2]
    check(f"ジョブ詳細: PROMPT_FILE が認証情報の置き場({_n})なら中身を出さない", _body not in _d and "認証情報などの置き場なので、中身は表示しません" in _d, _d[:0])
_vv = cl.get("/jobs/pvar")[2]
check("ジョブ詳細: PROMPT_FILE=$HOME/x は相対扱いにせず「展開される記述」と明示", "シェルで展開・解釈される記述です" in _vv and "相対パスなので" not in _vv and "確定できず" in _vv)
_cs = cl.get("/jobs/cmdsub")[2]
check("ジョブ詳細: COMMAND=$(…) は行全体を出し、展開される記述と明示", "$(echo hi; date)" in _cs and "シェルで展開・解釈される記述です" in _cs and "# comment" not in _cs)
_cp = cl.get("/jobs/cmdplain")[2]
check("ジョブ詳細: 単純な引用符なしの値は警告なし", "bin/tb-prwatch" in _cp and "シェルで展開・解釈される記述です" not in _cp)
check("ジョブ詳細: 展開される記述でない PROMPT(単一引用符)は警告なし", "シェルで展開・解釈される記述です" not in cl.get("/jobs/pcomment")[2])
check("ジョブ詳細: conf が 64 KiB 超なら注意を出す", "64 KiB を超えている" in cl.get("/jobs/cmdbig")[2])
_tb = cl.get("/jobs/ptb")[2]
check("ジョブ詳細: PROMPT_FILE の範囲は scheduler/ 配下(taskboard の設定ファイルは読まない)", "external-write" not in _tb and "外なので、中身は表示しません" in _tb)
_pm = cl.get("/jobs/pmissing")[2]
check("ジョブ詳細: 宛先が存在しない警告", "herdr に見つかりません" in _pm and "class=warn" in _pm)
_pu = cl.get("/jobs/pmulti")[2]
check("ジョブ詳細: 長い本文は折りたたむ", "<details><summary>本文を開く" in _pu and "line29" in _pu)
_c = cl.get("/jobs/cmdx")[2]
check("ジョブ詳細: COMMAND 型はコマンド全文(展開せず・エスケープ・マスク)", "COMMAND 型" in _c and "bash -c" in _c and "$(date)" in _c and "&lt;i&gt;y&lt;/i&gt;" in _c
      and "supersecretvalue1" not in _c and "タイムアウト 45 秒" in _c)
check("ジョブ詳細: COMMAND 型は宛先を出さない・承認の対象外と書く", "誰に送られるか" not in _c and "approvals.conf の対象外" in _c)
_fi = cl.get("/jobs/pfin")[2]
check("ジョブ詳細: PROMPT_FILE(リポジトリ内)は中身を出す", "scheduler" in _fi and "ファイル <code>" in _fi and "表示しません" not in _fi)
_fo = cl.get("/jobs/pfout")[2]
check("ジョブ詳細: PROMPT_FILE(リポジトリ外)は中身を出さない", "OUTSIDE-SECRET-BODY" not in _fo and "外なので、中身は表示しません" in _fo)
_fr = cl.get("/jobs/pfrel")[2]
check("ジョブ詳細: PROMPT_FILE(相対パス)は中身を出さない", "root:" not in _fr and "相対パスなので" in _fr)
_odir = os.path.join(TMP, "other-jobs"); os.makedirs(_odir, exist_ok=True)
open(os.path.join(_odir, "foreign.conf"), "w").write("COMMAND='echo FOREIGN-JOB'\nWINDOW=09:00-21:00\nINTERVAL_MIN=10\nTZ=Asia/Tokyo\n")
check("ジョブ詳細: このプロセスの jobs の外(別インスタンス・別 checkout)のジョブは見せない", cl.get("/jobs/foreign")[0] == 404 and "FOREIGN-JOB" not in cl.get("/jobs")[2])
check("ジョブ詳細: 不正なジョブ名・トラバーサルは 404", cl.get("/jobs/..%2Fetc")[0] == 404 and cl.get("/jobs/nope")[0] == 404)
check("ジョブ詳細: POST は従来のスケジュール保存だけ(prompt は書けない)",
      cl.raw("POST", "/jobs/pj", {"csrf": cl.csrf("/jobs/pj"), "action": "save", "interval": "5", "interval_unit": "min", "start": "09:00", "end": "10:00", "days": "daily", "enabled": "1", "prompt": "HACK"})[0] == 302
      and "HACK" not in open(os.path.join(_J, "pj.conf")).read() and "HACK" not in cl.get("/jobs/pj")[2])
os.remove(os.path.join(env["WEBUI_SCHED_SETTINGS"], "pj.settings"))
# 次回の予定(時刻を固定した単体の確認)
sys.path.insert(0, os.path.dirname(HERE))
import app.jobview as _jv  # noqa: E402
from zoneinfo import ZoneInfo as _ZI  # noqa: E402
_inf = {"enabled": True, "tz": "Asia/Tokyo", "interval_sec": 1800, "start": "09:00", "end": "21:00", "days": "weekdays"}
check("次回: 土曜の昼は月曜 09:00 から", _jv.next_runs(_inf, datetime(2026, 11, 14, 10, 0, tzinfo=_ZI("Asia/Tokyo")), 2)[0] == ["2026-11-16 09:00:00", "2026-11-16 09:30:00"])
check("次回: 平日の 09:10 の次は 09:30", _jv.next_runs(_inf, datetime(2026, 11, 16, 9, 10, tzinfo=_ZI("Asia/Tokyo")), 1)[0] == ["2026-11-16 09:30:00"])
check("次回: 終了後は翌営業日", _jv.next_runs(_inf, datetime(2026, 11, 20, 22, 0, tzinfo=_ZI("Asia/Tokyo")), 1)[0] == ["2026-11-23 09:00:00"])
check("次回: 一時停止は予定なし", _jv.next_runs({**_inf, "enabled": False})[0] == [])
check("次回: 不正な TZ は「不明」", "不明" in _jv.next_runs({**_inf, "tz": "No/Such"})[1])
# conf の読み取り(CodeRabbit 指摘): 閉じ引用符の直後の # と、引用符内・継続行の中の代入
def _jd(body):
    os.environ["WEBUI_SCHED_JOBS"] = _J
    open(os.path.join(_J, "unit.conf"), "w").write(body)
    return _jv.job_defs("unit")
for _q in ("'", '"'):
    _d = _jd(f"PROMPT_FILE={_q}/repo/scheduler/body.txt{_q}#v2\n")
    check(f"conf: {_q} の閉じ引用符の直後の # はコメントにしない(連結として不完全)", "PROMPT_FILE" in _d["_complex"], str(_d))
    _d = _jd(f"PROMPT_FILE={_q}/repo/scheduler/body.txt{_q} # note\n")
    check(f"conf: {_q} の閉じ引用符のあとに空白を置いた # はコメント(完全)", "PROMPT_FILE" not in _d["_complex"] and _d["PROMPT_FILE"] == "/repo/scheduler/body.txt", str(_d))
_d = _jd("PROMPT='説明\nPROMPT_FILE=/repo/scheduler/body.txt\n終わり'\n")
check("conf: 複数行 PROMPT の中の PROMPT_FILE= は代入として読まない", "PROMPT_FILE" not in _d and "PROMPT_FILE=/repo/scheduler/body.txt" in _d["PROMPT"], str(_d))
_d = _jd('PROMPT="説明\nCOMMAND=rm -rf x\n終わり"\n')
check("conf: 二重引用符の複数行の中の COMMAND= も代入として読まない", "COMMAND" not in _d, str(_d))
_d = _jd("PROMPT=abc\\\nPROMPT_FILE=/x/y\nSKIP_IF=working\n")
check("conf: 行末 \\ の継続行の中の代入は読まず、引用符の外の次の代入は読む", "PROMPT_FILE" not in _d and _d["SKIP_IF"] == "working", str(_d))
_d = _jd("PROMPT='a'\nPROMPT_FILE=/repo/scheduler/b.txt\n")
check("conf: 引用符が閉じたあとの行の代入は読む", _d["PROMPT_FILE"] == "/repo/scheduler/b.txt", str(_d))
# 説明(conf の先頭コメント)と、COMMAND からの推定
_jd("# 1 行目の説明\n# 2 行目\n# 注意: COMMAND は bash -c でそのまま実行される\n# token=ghp_abcdefghijklmnopqrstuvwxyz0123\nCOMMAND='cd /x && bin/tb flush'\n# 後ろのコメントは説明に入れない\n")
_ab = _jv.job_about("unit")
check("説明: 先頭のコメントだけ、注意書きは省き、トークンはマスク", _ab[:2] == ["1 行目の説明", "2 行目"] and len(_ab) == 3 and "ghp_" not in _ab[2], str(_ab))
check("説明: 一覧の 1 行は先頭コメントの 1 行目", _jv.job_summary("unit", {"kind": "command"}) == "1 行目の説明")
_jd("# 鍵の例\n# -----BEGIN RSA PRIVATE KEY-----\n# MIIEowIBAAKCAQEAsecretbody1234567890\n# -----END RSA PRIVATE KEY-----\nCOMMAND=true\n")
_ab = _jv.job_about("unit")
check("説明: 行をまたぐ秘密鍵のブロックは中身ごと伏せる", "MIIEow" not in " ".join(_ab) and "[マスク]" in " ".join(_ab), str(_ab))
_jd("COMMAND='cd /x && HERDR_SESSION=default bin/tb notify flush; bin/tb digest'\n")
check("説明: コメントが無ければ tb のサブコマンドから推す(notify flush は flush と取り違えない)",
      [c for c, _ in _jv.command_summary(_jv.job_defs("unit")["COMMAND"])] == ["tb notify flush", "tb digest"]
      and "通知" in _jv.job_summary("unit", {"kind": "command"}))
os.remove(os.path.join(_J, "unit.conf"))

# ---- 権限カタログ × approvals.conf(読み取りのみ)
check("カタログ: 操作ごとの現在の権限の節", "操作ごとの現在の権限(カタログ × approvals.conf)" in P[2])
def _crow(html, pat):
    m = re.search(r"<tr><td>(?:<span class=note>)?<code>" + re.escape(pat) + r"</code>.*?</tr>", html, re.S)
    return m.group(0) if m else ""
_t = _crow(P[2], "send:task:*")
check("カタログ: send:task:* は 3 行目で ask(明示)、宛先を絞った allow は例外に出る",
      "3 行目" in _t and ">ask<" in _t and "明示" in _t and "send:task:dd-* = allow" in _t, _t[:300])
_n = _crow(P[2], "agent:new:*")
check("カタログ: agent:new:*(high)が agent:* = allow で allow なら警告", "<b class=warn>allow</b>" in _n and "リスク high" in _n and "4 行目" in _n, _n[:300])
check("カタログ: mid の操作が allow でも警告しない", "リスク mid" not in _crow(P[2], "agent:compact:*") and "<b class=warn>allow</b>" in _crow(P[2], "agent:compact:*"))
_pr = _crow(P[2], "proxy:read-only")
check("カタログ: 規則が無い操作は既定の ask に落ちる", "既定に落ちる" in _pr and ">ask<" in _pr and "* = ask" in _pr, _pr[:300])
check("カタログ: unwired(proxy:*)は薄く表示し、承認判定に渡らないと警告", "<span class=note><code>proxy:read-only</code>" in _pr and "承認判定に渡らない" in _pr and "unwired" in _pr, _pr[:300])
_U = cl.get("/perms?state=unwired")[2].split("ルール一覧")[0]
check("カタログ: 状態 unwired で絞る", all(x in _U for x in ("proxy:read-only", "proxy:write", "proxy:external")) and "agent:new:*" not in _U)
check("カタログ: role:change は conditional ではなく no", "<code>role:change</code>" in P[2] and "conditional" not in _crow(P[2], "role:change"))
_g = _crow(P[2], "gate:merge")
check("カタログ: reserved は薄く表示し、リスク階層・副作用・allow 候補・状態を出す",
      "<span class=note><code>gate:merge</code>" in _g and "human-only" in _g and "external-write" in _g and "reserved" in _g, _g[:300])
check("カタログ: 説明・副作用の列が出る", "宛先を絞った allow だけ" in P[2] and "local-write" in P[2])
check("カタログ: 未知の規則の欄(カタログに無い規則を出し、エスケープ)", "未知の規則(カタログに無い)" in P[2] and "x&lt;b&gt;bold&lt;/b&gt;y" in P[2].split("未知の規則(カタログに無い)")[1].split("変更履歴")[0])
check("カタログ: 既知の規則(send:task:dd-* など)は未知に出ない", "send:task:dd-*" not in P[2].split("未知の規則(カタログに無い)")[1].split("変更履歴")[0])
_K = cl.get("/perms?kind=gate")[2]
check("カタログ: 種別で絞る", "gate:merge" in _K.split("ルール一覧")[0] and "agent:new:*" not in _K.split("ルール一覧")[0])
_S = cl.get("/perms?state=reserved")[2].split("ルール一覧")[0]
check("カタログ: 状態で絞る", "gate:execute" in _S and "send:task:*" not in _S)
_F = cl.get("/perms?fallback=1")[2].split("ルール一覧")[0]
check("カタログ: 既定に落ちているものだけ", "proxy:read-only" in _F and "send:task:*" not in _F)
_A = cl.get("/perms?allowed=1")[2].split("ルール一覧")[0]
check("カタログ: allow になっているものだけ", "agent:new:*" in _A and "proxy:read-only" not in _A)
check("カタログ: 不正な絞り込み値は無視して 200", cl.get("/perms?kind=zzz&state=%27")[0] == 200)
check("カタログ: 場所は固定で env で差し替える口が無い", "WEBUI_CATALOG" not in open(os.path.join(os.path.dirname(SERVER), "app", "config.py"), encoding="utf-8").read())
check("カタログ: GET だけ(絞り込みも検索も method=post を増やさない)", cl.get("/perms?kind=gate")[2].count("method=post") == 1)

# カタログの読み込みと表示の単体テスト(perms.CATALOG を差し替える)。説明にタグ・制御文字・| を入れても壊れない
sys.path.insert(0, os.path.dirname(HERE))
import app.perms as _perms  # noqa: E402
import app.views as _views  # noqa: E402
_cf = os.path.join(TMP, "unit-catalog.conf")
open(_cf, "w", encoding="utf-8").write(
    "# 注釈\n"
    "send:task:*   | send | local-write | high | conditional | active | 説明 <script>alert(1)</script> \x01制御 | 縦線を含む説明\n"
    "send:info:*   | send | local-write | mid | yes | active | 正常な行\n"
    "agent:new:x   | role | local-write | mid | yes | active | 種別がパターンの先頭と違う\n"
    "setup:hooks   | setup | local-write | human-only | yes | active | human-only なのに allow 候補が yes\n"
    "role:change   | role | local-write | high | conditional | active | 対象の無い操作名が conditional\n"
    "send:info:*   | send | local-write | mid | yes | active | 重複\n"
    "gate:merge    | gate | external-write | human-only | no | bogus | 状態が不正\n"
    "gate:x | gate | none | low | no\n")
_perms.CATALOG = _cf
_cat, _cerr, _cbad = _perms.load_catalog()
check("カタログ単体: 正しい 2 行だけ読み、不正な行(種別・human-only・conditional・重複・状態・項目不足)を数える",
      [c["pattern"] for c in _cat] == ["send:task:*", "send:info:*"] and _cbad == 6 and not _cerr, str((len(_cat), _cbad)))
check("カタログ単体: 説明の | は残り、制御文字は除かれる", "縦線を含む説明" in _cat[0]["desc"] and "\x01" not in _cat[0]["desc"])
_html = _views._catalog_section({}, [], "", _cat, "", _cbad)
check("カタログ単体: 説明のタグはエスケープされ、不正な行のスキップ件数が出る", "<script>" not in _html and "&lt;script&gt;" in _html and "6 件" in _html)
_perms.CATALOG = os.path.join(TMP, "no-such-catalog.conf")
check("カタログ単体: ファイルが無ければエラー文を返す", _perms.load_catalog()[1] != "" and "読めません" in _views._catalog_section({}, [], "", *_perms.load_catalog()[:2], 0))

# conf を一時的に差し替えて境界を確かめる(読み取りだけの画面なので、戻せば元の表示に戻る)
_orig = open(_AP, encoding="utf-8").read()
open(_AP, "w", encoding="utf-8").write("send:task:[[:alpha:]]* = ask\nsend:task:* = allow\nrole:change = ask\n* = ask\n")
_B = cl.get("/perms")[2]
check("権限: [ を含む行に警告を出す", "[ を含むパターン" in _B)
_R = cl.get("/perms?op=send:task:w1")[2]
check("カタログ: [ を含む行が先頭なら、現在の値も判定不能(下の行で決めない)", "判定不能" in _B.split("ルール一覧")[0])
check("権限: [ の行に当たる操作は判定不能(下の allow へ進まない)", "判定不能" in _R and "<b class=\"warn\">allow</b>" not in _R and "→ <b>ask</b>" not in _R)
check("権限: [ の行より下の行の一致の例は出さない(決めつけない)", "<code>send:task:worker1</code>" not in _B)
check("権限: [ の行が先頭にあれば、どの操作もそこで止まる(下の行を使わない)", "判定不能" in cl.get("/perms?op=role:change")[2])
open(_AP, "w", encoding="utf-8").write("gate:merge = allow\nsend:task:* = ask\nsend:task:dd-* = allow\nproxy:write = allow\nproxy:read-only = allow\n* = ask\n")
_G = cl.get("/perms")[2]
check("カタログ: unwired の操作に当たる規則は「何も効かない」と警告", "何も効きません" in _G and _G.count("何も効きません") == 2)
check("カタログ: human-only の操作(gate:merge)が allow なら警告", "リスク human-only" in _crow(_G, "gate:merge"))
check("カタログ: allow にしない操作(proxy:write)が allow なら警告", "リスク high / allow 候補 no" in _crow(_G, "proxy:write"))
check("カタログ: 宛先を絞った allow(high / conditional)の例外は警告しない", "例外があります" not in _crow(_G, "send:task:*"))
open(_AP, "w", encoding="utf-8").write("# " + "x" * 262144 + "\nsend:task:* = allow\n* = ask\n")
_Big = cl.get("/perms?op=send:task:w1")
check("権限: 256 KiB 超の conf は全部読めないと出し、ask とも allow とも断定しない",
      _Big[0] == 200 and "全部は読めません" in _Big[2] and "全部は読めていない" in _Big[2] and "→ <b" not in _Big[2] and "<td>1</td>" not in _Big[2])
open(_AP, "w", encoding="utf-8").write(_orig)
check("カタログ: 上限超の conf では現在の値を断定しない", "現在の値は" in _Big[2] and "<b class=warn>allow</b>" not in _Big[2].split("ルール一覧")[0])
check("権限: conf を戻すと元の表示に戻る", "<td>1</td>" in cl.get("/perms")[2])


# ---- 定期実行の設定(Web UI から変更できる唯一の操作)
SET = os.path.join(env["WEBUI_SCHED_SETTINGS"], "job1.settings")
check("一覧に実際の間隔が出る", "10 分おき / 09:00-21:00 / daily" in cl.get("/jobs")[2])
r = cl.get("/jobs/job1")
check("設定フォームが出る(自動更新しない)", r[0] == 200 and 'name=interval ' in r[2] and 'name=interval_unit' in r[2] and 'http-equiv="refresh"' not in r[2])
check("未知のジョブは 404", cl.get("/jobs/nope")[0] == 404 and cl.raw("POST", "/jobs/nope", {"csrf": cl.csrf("/jobs/job1")})[0] == 404)
good = {"action": "save", "interval_min": "15", "start": "10:00", "end": "18:00", "days": "weekdays", "enabled": "1"}
check("CSRF 無しの設定 POST は拒否", cl.raw("POST", "/jobs/job1", good)[0] == 403 and not os.path.exists(SET))
check("Origin 不一致の設定 POST は拒否",
      cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/jobs/job1")}, origin="http://evil.example")[0] == 403)
check("未ログインの設定 POST は /login へ", Client(port).raw("POST", "/jobs/job1", good)[0] == 302 and not os.path.exists(SET))
check("ログイン画面の CSRF(anon)は使えない", cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/login")})[0] == 403)
r = cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/jobs/job1")})
check("保存すると 302 で戻る", r[0] == 302 and r[1].get("Location") == "/jobs/job1?saved=1", str(r[:2]))
body = open(SET).read() if os.path.exists(SET) else ""
check("settings に書かれる(0600)", "INTERVAL_MIN=15" in body and "DAYS=weekdays" in body and "ENABLED=1" in body
      and mode(SET) == "0o600", body)
check("一覧に反映", "15 分おき / 10:00-18:00 / weekdays" in cl.get("/jobs")[2])
for bad, label in ((dict(interval_min="0"), "間隔 0"), (dict(interval_min="5; rm"), "間隔に記号"),
                   (dict(start="25:00"), "開始が範囲外"), (dict(start="19:00"), "開始が終了より後"),
                   (dict(days="$(id)"), "曜日が不正"), (dict(days="custom"), "曜日を 1 つも選ばない"),
                   (dict(start="10:00\nCOMMAND=x"), "改行の注入")):
    r = cl.raw("POST", "/jobs/job1", {**good, **bad, "csrf": cl.csrf("/jobs/job1")})
    check(f"不正な入力は 400 で書かない: {label}", r[0] == 400 and open(SET).read() == body, str(r[0]))
r = cl.raw("POST", "/jobs/job1", {**good, "days": "custom", "day_mon": "1", "day_fri": "1", "enabled": "",
                                  "csrf": cl.csrf("/jobs/job1")})
check("曜日指定と一時停止", r[0] == 302 and "DAYS=Mon Fri" in open(SET).read() and "ENABLED=0" in open(SET).read())
check("一時停止が一覧に出る", "一時停止" in cl.get("/jobs")[2])
# 秒単位の間隔・時刻
r = cl.raw("POST", "/jobs/job1", {**good, "interval": "30", "interval_unit": "sec", "start": "10:00:15", "end": "10:30",
                                  "csrf": cl.csrf("/jobs/job1")})
sb = open(SET).read()
check("秒の間隔を保存", r[0] == 302 and "INTERVAL_SEC=30" in sb and "INTERVAL_MIN" not in sb and "START=10:00:15" in sb, sb)
check("一覧に秒の間隔", "30 秒おき / 10:00:15-10:30 / weekdays" in cl.get("/jobs")[2])
f = cl.get("/jobs/job1")[2]
check("フォームに秒の値と単位", 'value="30"' in f and "<option value=sec selected>" in f and 'value="10:00:15"' in f)
r = cl.raw("POST", "/jobs/job1", {**good, "interval": "2", "interval_unit": "min", "csrf": cl.csrf("/jobs/job1")})
check("分の間隔で保存し直すと INTERVAL_MIN", r[0] == 302 and "INTERVAL_MIN=2" in open(SET).read()
      and "INTERVAL_SEC" not in open(SET).read())
sb = open(SET).read()
for bad, label in ((dict(interval="86401", interval_unit="sec"), "秒の上限超え"), (dict(interval="5", interval_unit="hour"), "単位が不正"),
                   (dict(start="10:00:60"), "秒が範囲外"), (dict(start="18:00:30", end="18:00:10"), "開始が終了より後(秒)")):
    r = cl.raw("POST", "/jobs/job1", {**good, **bad, "csrf": cl.csrf("/jobs/job1")})
    check(f"不正な入力は 400 で書かない: {label}", r[0] == 400 and open(SET).read() == sb, str(r[0]))
r = cl.raw("POST", "/jobs/job1", {**good, "start": "18:00:30", "end": "18:00", "csrf": cl.csrf("/jobs/job1")})
check("終了 HH:MM はその分の 59 秒までなので 18:00:30-18:00 は可", r[0] == 302, str(r[0]))
r = cl.raw("POST", "/jobs/job1", {"action": "reset", "csrf": cl.csrf("/jobs/job1")})
check("conf の値に戻すと settings を消す", r[0] == 302 and not os.path.exists(SET) and "10 分おき" in cl.get("/jobs")[2])
check("tasks のフィルタ", "T-1" in cl.get("/tasks?project=pj&status=in_progress")[2] and "T-1" not in cl.get("/tasks?status=done")[2])
check("フィルタ値の XSS", "<script>" not in cl.get("/tasks?project=%3Cscript%3E")[2])

# 承認待ちの内容(表示とコマンドのみ)
bl = cl.get("/agents/%3Cimg%20src%3Dx%20onerror%3Dalert(2)%3E")
check("blocked の詳細に承認待ちの内容が出る", bl[0] == 200 and "承認待ちの内容" in bl[2] and "Do you want to run?" in bl[2])
check("ダイアログ内容がエスケープされ ANSI が除かれる",
      "rm -rf /tmp/x &lt;script&gt;alert(1)&lt;/script&gt;" in bl[2] and "[1m" not in bl[2] and "<script" not in no_js(bl[2]).lower())
check("手元で開くコマンド(readonly 入力)", "readonly" in bl[2] and "herdr agent attach &#x27;&lt;img" in bl[2])
check("場所(workspace ラベル / tab / pane)", "ws-label" in bl[2] and "w1:p2" in bl[2])
idle = cl.get("/agents/w1%3Ap3")
check("idle には承認待ちの内容が出ない", "承認待ちの内容" not in idle[2] and "Do you want to run?" not in idle[2])
check("名前なしは pane_id で attach", "herdr agent attach w1:p3" in idle[2])
check("フォームはログアウトだけ(承認・send-keys のボタンなし)",
      re.findall(r"<form[^>]*action=\"([^\"]+)\"", bl[2]) == ["/logout"] and bl[2].lower().count("<button") == 1)
check("inbox の blocked 行から詳細へリンク", "#approval" in inbox)

# ---- 報告を対応済みにする(/resolve。書き込みは tb resolve が行う)
ib = cl.get("/inbox")[2]
check("inbox に「対応済みにする」(C-1)", 'action="/resolve"' in ib and 'name=id value="1"' in ib)
check("タスク画面にも「対応済みにする」", 'action="/resolve"' in cl.get("/tasks/T-1")[2])
good = {"id": "1", "note": "決めた <b>x</b>", "back": "/inbox"}
check("CSRF 無しの /resolve は拒否", cl.raw("POST", "/resolve", good)[0] == 403)
check("Origin 不一致の /resolve は拒否", cl.raw("POST", "/resolve", {**good, "csrf": cl.csrf("/inbox")}, origin="http://evil.example")[0] == 403)
check("未ログインの /resolve は /login へ", Client(port).raw("POST", "/resolve", good)[0] == 302)
check("ログイン画面の CSRF(anon)は使えない", cl.raw("POST", "/resolve", {**good, "csrf": cl.csrf("/login")})[0] == 403)
check("id が数字でなければ 400", cl.raw("POST", "/resolve", {**good, "id": "1;x", "csrf": cl.csrf("/inbox")})[0] == 400)
r = cl.raw("POST", "/resolve", {**good, "csrf": cl.csrf("/inbox")})
check("対応済みにすると inbox へ戻る", r[0] == 302 and r[1].get("Location") == "/inbox?resolved=done", str(r[:2]))
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_row = _c.execute("SELECT resolved_at IS NOT NULL, resolved_note FROM comments WHERE id=1").fetchone()
_c.close()
check("tb resolve で DB に記録(メモも)", _row == (1, "決めた <b>x</b>"), str(_row))
time.sleep(3.2)  # needs_human のキャッシュ切れを待つ
ib = cl.get("/inbox?resolved=done")[2]
check("対応済みは要対応から消え、バッジが減る", '<span class="badge">2</span>' in ib and 'name=id value="1"' not in ib)
check("結果の表示と、メモのエスケープ", "対応済みにしました" in ib and "決めた &lt;b&gt;x&lt;/b&gt;" in cl.get("/tasks/T-1")[2])
check("緊急通知からも消える", "c:1" not in [it["id"] for it in json.loads(cl.get("/api/notifications")[2])["items"]])
r = cl.raw("POST", "/resolve", {**good, "back": "//evil.example/x", "csrf": cl.csrf("/inbox")})
check("2 回目は already、戻り先は自サイトだけ", r[0] == 302 and r[1].get("Location") == "/inbox?resolved=already", str(r[:2]))
r = cl.raw("POST", "/resolve", {"id": "999", "back": "/tasks/T-1", "csrf": cl.csrf("/inbox")})
check("存在しない報告は error", r[0] == 302 and r[1].get("Location") == "/tasks/T-1?resolved=error", str(r[:2]))

# ---- agent の停止・復活・新規作成(/agent-op /agent-new。書き込みは tb agent-* が行う)
FLOG = os.path.join(TMP, "fake", "log")


def flog():
    return open(FLOG).read() if os.path.exists(FLOG) else ""


def regdb(sql, args=()):
    c = sqlite3.connect(env["WEBUI_TB_DB"])
    try:
        r = c.execute(sql, args).fetchall()
        c.commit()
        return r
    finally:
        c.close()


ag = cl.get("/agents")[2]
check("agent 一覧に停止のボタンと新規作成へのリンク", 'action="/agent-op"' in ag and 'href="/agent-new"' in ag)
check("未判定・未完了タスクありは確認つきの強制停止", 'value="force-stop"' in ag and "name=confirm" in ag and 'value="stop"' not in ag)
good_op = {"op": "force-stop", "name": "alpha", "confirm": "1"}
check("CSRF 無しの /agent-op は拒否", cl.raw("POST", "/agent-op", good_op)[0] == 403)
check("Origin 不一致の /agent-op は拒否", cl.raw("POST", "/agent-op", {**good_op, "csrf": cl.csrf("/agents")}, origin="http://evil.example")[0] == 403)
check("未ログインの /agent-op は /login へ", Client(port).raw("POST", "/agent-op", good_op)[0] == 302)
check("名前が不正なら 400", cl.raw("POST", "/agent-op", {**good_op, "name": "a;b", "csrf": cl.csrf("/agents")})[0] == 400)
check("不明な op は 400", cl.raw("POST", "/agent-op", {**good_op, "op": "rm", "csrf": cl.csrf("/agents")})[0] == 400)
r = cl.raw("POST", "/agent-op", {"op": "stop", "name": "alpha", "csrf": cl.csrf("/agents")})
check("印の無い agent の通常停止は断る", r[0] == 302 and r[1].get("Location") == "/agents?op=refused" and "pane close" not in flog(), str(r[:2]))
r = cl.raw("POST", "/agent-op", {"op": "force-stop", "name": "alpha", "csrf": cl.csrf("/agents")})
check("強制停止は確認のチェックが要る", r[0] == 302 and r[1].get("Location") == "/agents?op=refused" and "pane close" not in flog())
r = cl.raw("POST", "/agent-op", {**good_op, "csrf": cl.csrf("/agents")})
check("強制停止で pane を閉じる", r[0] == 302 and r[1].get("Location") == "/agents?op=done" and "pane close w1:p1" in flog(), str(r[:2]) + flog())
check("台帳に会話が残る", regdb("SELECT state, session_id FROM agents WHERE name='alpha'") == [("stopped", "11111111-2222-3333-4444-555555555555")])
time.sleep(3.2)  # agent list のキャッシュ切れを待つ
ag = cl.get("/agents?op=done")[2]
check("結果の表示と、停止中の一覧・復活ボタン", "実行しました" in ag and "停止中の agent" in ag and 'value="revive"' in ag and 'value="revive-fresh"' in ag)
check("直近の操作に human の記録", "alpha" in ag and "human" in ag)
r = cl.raw("POST", "/agent-op", {"op": "revive", "name": "alpha", "csrf": cl.csrf("/agents")})
check("同じ会話で復活(claude --resume)", r[0] == 302 and r[1].get("Location") == "/agents?op=done"
      and "agent start alpha --kind claude --pane w9:p1 -- --resume 11111111-2222-3333-4444-555555555555" in flog(), str(r[:2]) + flog())
nw = cl.get("/agent-new")
check("新規作成フォーム(自動更新なし・役割の候補)", nw[0] == 200 and 'action="/agent-new"' in nw[2] and 'http-equiv="refresh"' not in nw[2]
      and '<option value="worker">' in nw[2] and '<option value="_common">' not in nw[2])
check("CSRF 無しの /agent-new は拒否", cl.raw("POST", "/agent-new", {"name": "newbie"})[0] == 403)
r = cl.raw("POST", "/agent-new", {"name": "Bad<b>", "csrf": cl.csrf("/agent-new")})
check("不正な名前はフォームに戻してエラー(エスケープ)", r[0] == 400 and "英小文字" in r[2] and "Bad&lt;b&gt;" in r[2] and "Bad<b>" not in r[2])
r = cl.raw("POST", "/agent-new", {"name": "newbie", "cwd": "relative/dir", "csrf": cl.csrf("/agent-new")})
check("相対パスの cwd は断る", r[0] == 400 and "絶対パス" in r[2])
r = cl.raw("POST", "/agent-new", {"name": "newbie", "cwd": "/no/such/dir", "csrf": cl.csrf("/agent-new")})
check("存在しない cwd は tb のエラーを見せる", r[0] == 400 and "ディレクトリがありません" in r[2], r[2][-400:])
r = cl.raw("POST", "/agent-new", {"name": "newbie", "cwd": TMP, "model": "haiku", "role": "worker", "killable": "yes",
                                  "prompt": "最初の指示", "csrf": cl.csrf("/agent-new")})
check("新しい agent を作る", r[0] == 302 and r[1].get("Location") == "/agents?op=done"
      and "--label newbie" in flog() and "agent start newbie --kind claude --pane w9:p1 -- --model haiku" in flog()
      and "agent prompt newbie [from: tb / agent / human] 最初の指示" in flog(), str(r[:2]) + flog())
check("台帳に役割と印", regdb("SELECT role, killable, created_by FROM agents WHERE name='newbie'") == [("worker", "yes", "human")])
regdb("INSERT INTO agent_ops (op, target, args, requested_by, status, created_at) VALUES ('new','helper','{\"cwd\":\"" + TMP + "\"}','conductor','pending','2026-11-10T00:00:00Z')")
lid = regdb("SELECT max(id) FROM agent_ops")[0][0]
time.sleep(3.2)
ag = cl.get("/agents")[2]
check("承認待ちの操作に承認・却下のボタン", f'name=id value="{lid}"' in ag and 'value="approve"' in ag and 'value="reject"' in ag)
check("緊急通知にも出る", f"l:{lid}" in [it["id"] for it in json.loads(cl.get("/api/notifications")[2])["items"]])
check("id が数字でなければ 400", cl.raw("POST", "/agent-op", {"op": "approve", "id": "1;x", "csrf": cl.csrf("/agents")})[0] == 400)
r = cl.raw("POST", "/agent-op", {"op": "approve", "id": str(lid), "csrf": cl.csrf("/agents")})
check("承認すると実行する", r[0] == 302 and r[1].get("Location") == "/agents?op=done" and "--label helper" in flog(), str(r[:2]))
check("記録は依頼者のまま done", regdb("SELECT status, requested_by FROM agent_ops WHERE id=?", (lid,)) == [("done", "conductor")])
r = cl.raw("POST", "/agent-op", {"op": "reject", "id": str(lid), "csrf": cl.csrf("/agents")})
check("済んだものは却下できない", r[0] == 302 and r[1].get("Location") == "/agents?op=refused", str(r[:2]))
os.remove(os.path.join(TMP, "fake", "closed"))
# ---- ドキュメント(agent が成果物として報告したローカルのファイル)
DOCS = os.path.join(d, "work")
os.makedirs(os.path.join(DOCS, "out", "sub"), exist_ok=True)
os.makedirs(os.path.join(d, "home", ".ssh"), exist_ok=True)
open(os.path.join(DOCS, "summary.md"), "w").write(
    "# まとめ " + XSS + "\n\n- 項目 **太字** と `code`\n- [リンク](https://example.com/a?b=1&c=2)\n- [危険](javascript:alert(1))\n\n"
    "| 列1 | 列2 |\n|---|---|\n| a | <b>b</b> |\n\n```\n<script>x</script>\n```\n")
open(os.path.join(DOCS, "out", "sub", "note.txt"), "w").write("内側のファイル " + XSS)
open(os.path.join(d, "outside.txt"), "w").write("外のファイル")
os.symlink(os.path.join(d, "outside.txt"), os.path.join(DOCS, "out", "link.txt"))
open(os.path.join(DOCS, ".env"), "w").write("SECRET=1")
open(os.path.join(d, "home", ".ssh", "id_ed25519"), "w").write("PRIVATE")
open(os.path.join(DOCS, "bin.dat"), "wb").write(b"\x00\x01\x02")
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO agent_activity (session_id,cwd,updated_at) VALUES ('sess-w',?, '2026-11-06T00:00:00Z')", (DOCS,))
_dl = json.dumps(["summary.md", os.path.join(DOCS, "out"), ".env", os.path.join(d, "home", ".ssh", "id_ed25519"),
                  "bin.dat", "gone.md", "https://example.com/pr/1", os.path.join(d, "outside.txt") + "/../" + "outside.txt"])
_c.execute("INSERT INTO comments (task_id,kind,text,deliverables,from_name,from_pane,from_session,created_at) VALUES "
           "('T-1','done','終わり',?,'alpha','w1:p1','sess-w','2026-11-07T00:00:00Z')", (_dl,))
_cid = _c.execute("SELECT max(id) FROM comments").fetchone()[0]
_c.execute("INSERT INTO alerts (fingerprint,title,source,status,result_summary,result_report,result_path,result_by,first_seen,last_seen,analyzed_at) "
           "VALUES ('f','障害 " + XSS + "','cli','analyzed','要約','## 原因\n\nDB の本文','/nonexistent/report.md','analyst','t','t','2026-11-08T00:00:00Z')")
_aid = _c.execute("SELECT max(id) FROM alerts").fetchone()[0]
_c.commit()
_c.close()
s_, _, b = cl.get("/docs")
check("GET /docs が 200 で一覧", s_ == 200 and "summary.md" in b and f"ref=C-{_cid}%2F0" in b and f"ref=A-{_aid}" in b, str(s_))
check("/docs: ナビにドキュメント・入力欄があるので自動更新しない", 'href="/docs"' in b and 'http-equiv="refresh"' not in b)
check("/docs: 状態(あり/フォルダ/なし/非表示/URL/DB に本文)",
      all(x in b for x in ("あり", "フォルダ", "なし", "非表示", "URL", "DB に本文")))
check("/docs: XSS がエスケープ", "<script" not in no_js(b).lower())
check("/docs?q= で絞り込み", "summary.md" in cl.get("/docs?q=summary")[2] and "summary.md" not in cl.get("/docs?q=zzzz")[2])
V = lambda ref, **kw: cl.get("/docs/view?" + urllib.parse.urlencode({"ref": ref, **kw}))  # noqa: E731
s_, _, b = V(f"C-{_cid}/0")
check("相対パスは agent の作業ディレクトリ基準で開ける", s_ == 200 and "<div class=doc>" in b, str(s_))
check("Markdown を整形(見出し・太字・code・表)",
      "<h2 class=md>まとめ &lt;script&gt;" in b and "<strong>太字</strong>" in b and "<code>code</code>" in b and "<th>列1</th>" in b)
check("Markdown: http(s) だけリンクにし、javascript: はリンクにしない",
      'href="https://example.com/a?b=1&amp;c=2"' in b and 'href="javascript' not in b.lower())
check("Markdown: 生 HTML とコードブロックはエスケープ", "&lt;b&gt;b&lt;/b&gt;" in b and "&lt;script&gt;x&lt;/script&gt;" in b
      and "<script" not in no_js(b).lower())
check("文書ページは自動更新しない", 'http-equiv="refresh"' not in b)
check("元のテキスト表示", "<pre># まとめ &lt;script&gt;" in V(f"C-{_cid}/0", raw="1")[2])
b = V(f"C-{_cid}/1")[2]
check("フォルダは中身の一覧(外を指すリンクは出さない)", "sub/note.txt" in b and "link.txt" not in b)
check("フォルダ内のファイルを開ける", "内側のファイル &lt;script&gt;" in V(f"C-{_cid}/1", sub="sub/note.txt")[2])
check("フォルダの外へは出られない(.. / 絶対パス / リンク)",
      all(V(f"C-{_cid}/1", sub=x)[0] == 404 for x in ("../summary.md", "/etc/passwd", "link.txt", "../../outside.txt")))
check(".env は開かない", "認証情報" in V(f"C-{_cid}/2")[2] and "SECRET" not in V(f"C-{_cid}/2")[2])
check(".ssh の鍵は開かない", "PRIVATE" not in V(f"C-{_cid}/3")[2] and "認証情報" in V(f"C-{_cid}/3")[2])
check("バイナリは表示しない", "テキストではない" in V(f"C-{_cid}/4")[2])
check("無いファイルは見つからないと出す", "見つかりません" in V(f"C-{_cid}/5")[2])
check("URL の成果物は外部リンク", 'href="https://example.com/pr/1"' in V(f"C-{_cid}/6")[2])
b = V(f"A-{_aid}")[2]
check("アラートの解析レポートは DB の本文を出す", "DB の本文" in b and "<h3 class=md>原因</h3>" in b and "&lt;script&gt;" in b)
check("登録されていない参照は 404", all(V(x)[0] == 404 for x in (f"C-{_cid}/99", "C-99999/0", "task:nope/0", "T-1/0", "A-99999", "/etc/passwd", "")))
# パスを指定して開く(agent の作業ディレクトリ配下だけ)
check("/docs に開ける範囲(agent の作業ディレクトリ)が出る", f"<code>{os.path.realpath(DOCS)}</code>" in cl.get("/docs")[2])
b = V("", path=os.path.join(DOCS, "summary.md"))[2]
check("作業ディレクトリ配下のパスは開ける(Markdown 整形)", "<div class=doc>" in b and "<strong>太字</strong>" in b)
check("パス指定でもフォルダの中を開ける", "内側のファイル" in V("", path=os.path.join(DOCS, "out"), sub="sub/note.txt")[2])
for bad in ("/etc/passwd", os.path.join(d, "outside.txt"), os.path.join(DOCS, "out", "link.txt"), os.path.join(DOCS, "..", "outside.txt"),
            "summary.md", "~"):
    b = V("", path=bad)[2]
    check(f"範囲外・相対は開けない: {bad}", "開けません" in b or "絶対パス" in b, bad)
check("パス指定でも .env は開かない", "SECRET" not in V("", path=os.path.join(DOCS, ".env"))[2])
# Mermaid(同梱の mermaid.js でブラウザが描く)
open(os.path.join(DOCS, "flow.md"), "w").write("# 図\n\n```mermaid\ngraph TD\n  A[" + XSS + "] --> B\n```\n")
open(os.path.join(DOCS, "seq.mmd"), "w").write("sequenceDiagram\n  A->>B: hi\n")
b = V("", path=os.path.join(DOCS, "flow.md"))[2]
check("```mermaid は pre.mermaid(中身はエスケープ)", "<pre class=mermaid>graph TD\n  A[&lt;script&gt;" in b)
check("Mermaid があるページだけ同梱 JS を読む", '<script src="/static/mermaid.min.js" defer>' in b and '<script src="/docs.js" defer>' in b
      and "/static/mermaid.min.js" not in V("", path=os.path.join(DOCS, "summary.md"))[2])
check(".mmd も図にする", "<pre class=mermaid>sequenceDiagram" in V("", path=os.path.join(DOCS, "seq.mmd"))[2])
s_, h, b = cl.get("/static/mermaid.min.js")
check("同梱の mermaid.min.js を同一オリジンから配る", s_ == 200 and h.get("Content-Type", "").startswith("text/javascript") and len(b) > 100000)
check("未ログインでは同梱 JS も読めない", Client(port).get("/static/mermaid.min.js")[0] == 302 and Client(port).get("/docs.js")[0] == 302)
check("static の他のファイルは出さない", cl.get("/static/mermaid.LICENSE")[0] == 404 and cl.get("/static/../app/docs.py")[0] == 404)
b = cl.get("/tasks/T-1")[2]
check("タスク画面の成果物が Web UI へのリンクになる", f'href="/docs/view?ref=C-{_cid}%2F0"' in b and 'href="/docs/view?ref=task%3AT-1%2F0"' in b)

# ---- 他のメソッド
for method in ("POST", "PUT", "DELETE", "PATCH"):
    for path in ("/inbox", "/agents/alpha", "/tasks"):
        check(f"{method} {path} は 405", cl.raw(method, path, {"a": "b"} if method == "POST" else None)[0] == 405)
check("未ログインでも他の POST は 405", Client(port).raw("POST", "/inbox", {"a": "b"})[0] == 405)
check("PUT /login も 405", cl.raw("PUT", "/login")[0] == 405)
check("GET /logout は 405", cl.get("/logout")[0] == 405)

# ---- サーバー再起動後もセッションが続く
out1 = stop_out(p)
p = start(env)
port2 = wait_port(env, p)
cl2 = Client(port2)
cl2.cookie = sess_cookie
check("再起動後も同じセッションで入れる", cl2.get("/inbox")[0] == 200)
check("再起動後は /setup が 404 のまま", cl2.get("/setup")[0] == 404)

# ---- 期限切れセッション
sess_path = os.path.join(RUN, "sessions.json")
d_ = json.load(open(sess_path))
h_ = hashlib.sha256(cookie_val.encode()).hexdigest()
d_[h_] = time.time() - 1
json.dump(d_, open(sess_path, "w"))
r = cl2.get("/inbox")
check("期限切れセッションは /login へ", r[0] == 302 and r[1]["Location"].startswith("/login"), str(r[:2]))
check("不明なセッション値は /login へ", Client(port2).get("/inbox", cookie="herdr_webui_session=nope")[0] == 302)

# ---- logout
cl3 = Client(port2)
cl3.login()
old = cl3.cookie
tok = cl3.csrf("/inbox")
check("CSRF 無しの logout は拒否(セッションは残る)", cl3.raw("POST", "/logout", {})[0] == 403 and cl3.get("/inbox")[0] == 200)
check("Origin 不一致の logout は拒否", cl3.raw("POST", "/logout", {"csrf": tok}, origin="http://evil.example")[0] == 403 and cl3.get("/inbox")[0] == 200)
tok = cl3.csrf("/inbox")
r = cl3.raw("POST", "/logout", {"csrf": tok})
check("logout は 302 /login で Cookie を消す", r[0] == 302 and r[1]["Location"] == "/login" and "Max-Age=0" in r[1].get("Set-Cookie", ""))
check("logout 後は同じ Cookie が無効", Client(port2).get("/inbox", cookie=old)[0] == 302)
check("logout でサーバー側のセッションも消える", hashlib.sha256(old.split("=", 1)[1].encode()).hexdigest() not in open(sess_path).read())


# ---- パスワード再設定(run/allow-reset)
NEWPW = "brand-new-password-77"
ALLOW = os.path.join(os.path.abspath(RUN), "allow-reset")
anon = Client(port2)
check("ログイン画面に「パスワードを忘れた」リンク", 'href="/reset"' in anon.get("/login")[2])
r = anon.get("/reset")
check("許可ファイルなしは入力欄を出さずコマンドを表示",
      r[0] == 200 and "name=confirm" not in r[2] and "touch " + ALLOW in r[2].replace("&#x27;", "'"), r[2][-400:])
lg = anon.csrf("/login")
before = open(os.path.join(RUN, "auth.json")).read()
r = anon.raw("POST", "/reset", {"csrf": lg, "password": NEWPW, "confirm": NEWPW})
check("許可ファイルなしの POST は拒否", r[0] == 403 and open(os.path.join(RUN, "auth.json")).read() == before)
check("古いパスワードのままログインできる", Client(port2).login()[0] == 302)

oldc = Client(port2)
oldc.login()
for _ in range(5):
    Client(port2).login(pw=WRONG)
check("(準備)ロックアウト中", Client(port2).login()[0] == 429)

open(ALLOW, "w").close()
r = anon.get("/reset")
check("許可ファイルありでフォームが出る", r[0] == 200 and "name=confirm" in r[2])
cs = anon.csrf("/reset")
base_ = {"csrf": cs, "password": NEWPW, "confirm": NEWPW}
check("12 文字未満は拒否(許可は残る)", anon.raw("POST", "/reset", {**base_, "password": "short-11-ch", "confirm": "short-11-ch"})[0] == 400 and os.path.exists(ALLOW))
check("確認入力の不一致は拒否", anon.raw("POST", "/reset", {**base_, "confirm": NEWPW + "x"})[0] == 400 and os.path.exists(ALLOW))
check("CSRF トークン不一致は拒否", anon.raw("POST", "/reset", {**base_, "csrf": cs[:-1] + ("0" if cs[-1] != "0" else "1")})[0] == 403)
check("CSRF トークン無しは拒否", anon.raw("POST", "/reset", {"password": NEWPW, "confirm": NEWPW})[0] == 403)
check("Origin 不一致は拒否", anon.raw("POST", "/reset", base_, origin="http://evil.example")[0] == 403)
check("Origin 無しは拒否", anon.raw("POST", "/reset", base_, origin=None)[0] == 403)
check("拒否された POST では許可ファイルは消えない", os.path.exists(ALLOW))
os.utime(ALLOW, (time.time() - 540, time.time() - 540))  # 9 分前: まだ有効
r = anon.raw("POST", "/reset", base_)
check("10 分以内なら成功(302 /login)", r[0] == 302 and r[1]["Location"] == "/login", str(r[:2]))
check("成功後に許可ファイルが消える", not os.path.exists(ALLOW))
check("auth.json は 0600 のまま", mode(os.path.join(RUN, "auth.json")) == "0o600")
check("auth.json に新パスワードの平文を含まない", NEWPW not in open(os.path.join(RUN, "auth.json")).read())
check("全セッションが無効化(sessions.json が空)", json.load(open(sess_path)) == {} and mode(sess_path) == "0o600")
check("古いセッションは使えない", oldc.get("/inbox")[0] == 302)
check("古いパスワードで拒否", Client(port2).login(pw=PW)[0] == 401)
nc = Client(port2)
check("新パスワードでログインできる(ロックアウトも解除)", nc.login(pw=NEWPW)[0] == 302 and nc.get("/inbox")[0] == 200)
check("再設定後に同じ許可は再利用できない", anon.raw("POST", "/reset", {**base_, "csrf": anon.csrf("/login")})[0] == 403)

open(ALLOW, "w").close()
os.utime(ALLOW, (time.time() - 660, time.time() - 660))  # 11 分前
r = anon.get("/reset")
check("期限切れはフォームを出さずコマンドを表示", "name=confirm" not in r[2] and "期限" in r[2] and "touch " in r[2])
r = anon.raw("POST", "/reset", {**base_, "csrf": anon.csrf("/login"), "password": NEWPW + "2", "confirm": NEWPW + "2"})
check("期限切れの POST は拒否(パスワード不変)", r[0] == 403 and Client(port2).login(pw=NEWPW)[0] == 302)
os.remove(ALLOW)

target = os.path.join(TMP, "real-allow")
open(target, "w").close()
os.symlink(target, ALLOW)
r = anon.get("/reset")
check("シンボリックリンクはフォームを出さない", "name=confirm" not in r[2] and "不正" in r[2])
r = anon.raw("POST", "/reset", {**base_, "csrf": anon.csrf("/login"), "password": NEWPW + "3", "confirm": NEWPW + "3"})
check("シンボリックリンクの POST は拒否", r[0] == 403 and Client(port2).login(pw=NEWPW)[0] == 302 and os.path.islink(ALLOW))
os.remove(ALLOW)

# 時刻の注入(待たない)
import importlib.util  # noqa: E402
spec = importlib.util.spec_from_file_location("webui_auth", os.path.join(os.path.dirname(HERE), "auth.py"))
am = importlib.util.module_from_spec(spec)
spec.loader.exec_module(am)
ut = os.path.join(TMP, "unit-run")
ua = am.Auth(ut)
open(ua.allow_path, "w").close()
t0 = os.stat(ua.allow_path).st_mtime
check("[注入] 9 分後は ok", ua.reset_state(t0 + 540) == "ok")
check("[注入] 10 分ちょうどは ok、10 分 1 秒後は expired", ua.reset_state(t0 + 600) == "ok" and ua.reset_state(t0 + 601) == "expired")
check("[注入] 作成より未来すぎる時刻は invalid", ua.reset_state(t0 - 3600) == "invalid")
check("[注入] 許可なしは missing", (os.remove(ua.allow_path) or ua.reset_state()) == "missing")

check("PUT /reset は 405", anon.raw("PUT", "/reset")[0] == 405)
check("GET /reset は設定済みで 200(情報のみ)", anon.get("/reset")[0] == 200)

# ---- herdr 失敗でも落ちない
cl4 = Client(port2)
cl4.login(pw=NEWPW)
open(os.path.join(TMP, "fake", "fail"), "w").close()
time.sleep(3.2)  # キャッシュ失効待ち
s_, h, b = cl4.get("/agents")
check("herdr 失敗でも 200 でエラー表示", s_ == 200 and "herdr が失敗しました" in b, str(s_))
os.remove(os.path.join(TMP, "fake", "fail"))
check("サーバーは生きている", p.poll() is None)

out2 = stop_out(p)
check("ログに再設定の成功と拒否(期限切れ・不正)が残る", "reset: ok" in out2 and "reset: denied (expired)" in out2 and "reset: denied (invalid)" in out2 and "reset: denied (missing)" in out2, out2[-600:])
leak = [x for x in (PW, NEWPW, WRONG, cookie_val, old.split("=", 1)[1], "legacy-token") if x in out1 + out2]
check("ログ(stdout/stderr)にパスワード・セッション値を出さない", not leak, f"{len(leak)} 件")
leak2 = [x for x in ("failbody-", "reportbody-", "SECOND-LINE-SECRET", "TITLETAIL", "REPORT-TAIL", "owner=alpha", "send:task:dd-x", "send:task:w1", "approvals.conf") if x in out1 + out2]
check("ログに送信履歴の本文・権限の検索語(URL のクエリ)を出さない", not leak2, str(leak2))
_src = open(os.path.join(os.path.dirname(SERVER), "app", "perms.py"), encoding="utf-8").read()
check("権限の画面は tb も他のコマンドも呼ばない(agent の pane から効く経路を作らない)",
      "subprocess" not in _src and "os.system" not in _src and '"w"' not in _src and "data.config_text(\"approvals\")" in _src)

# ================= DB 不在・ログ不在・壊れた DB
for name, prep in (("nodb", None), ("baddb", lambda e_: open(e_["WEBUI_TB_DB"], "w").write("not a sqlite file"))):
    base = free_range(1)
    env, d = make_env(name, HERDR_WEBUI_RANGE=f"{base}-{base}")
    if prep:
        prep(env)
    p = start(env)
    port = wait_port(env, p)
    check(f"{name}: 起動できる", port is not None)
    if port:
        c = Client(port)
        c.raw("POST", "/setup", {"csrf": c.csrf("/setup"), "username": USER, "password": PW, "confirm": PW})
        c.cookie = None
        c.login()
        for path in ["/", "/notifications", "/api/notifications", "/agents", "/tasks", "/tasks/T-1", "/inbox", "/jobs", "/ledger", "/perms"]:
            s_ = c.get(path)[0]
            check(f"{name}: GET {path}", s_ in (200, 404), str(s_))
        check(f"{name}: サーバー生存", p.poll() is None)
        if name == "nodb":
            check("DB 不在の表示", "タスク DB がありません" in c.get("/tasks")[2])
    stop(p)

# ================= ブラウザ相当の Origin(Referrer-Policy 起因の不具合の回帰)
# Referrer-Policy: no-referrer だと、ブラウザは同一オリジンの POST でも Origin: null にし Referer も付けない。
# → 全フォームが 403 になった。同一オリジンには Origin/Referer を付ける same-origin にする。
base = free_range(1)
env, d = make_env("origin0", HERDR_WEBUI_RANGE=f"{base}-{base}")
p = start(env)
port = wait_port(env, p)
c0 = Client(port)
r = c0.get("/setup")
check("Referrer-Policy は same-origin(no-referrer ではない)", r[1].get("Referrer-Policy") == "same-origin", r[1].get("Referrer-Policy"))
r = c0.get("/setup")
check("/setup の Referrer-Policy ヘッダ", r[1].get("Referrer-Policy") == "same-origin")
check("/setup の meta referrer も same-origin", "name=referrer content=same-origin" in r[2] and "no-referrer" not in r[2])
stop(p)

BAD = [("null", "Origin: null"), ("http://evil.example", "外部オリジン"), (None, "Origin も Referer も無い")]
for host in ("127.0.0.1", "localhost"):
    base = free_range(1)
    env, d = make_env("origin-" + host, HERDR_WEBUI_RANGE=f"{base}-{base}")
    RUN_ = env["WEBUI_RUN_DIR"]
    p = start(env)
    port = wait_port(env, p)
    good = f"http://{host}:{port}"
    c = Client(port)
    H = f"{host}:{port}"
    pw1, pw2 = "origin-test-password-1", "origin-test-password-2"

    def post(path, data, origin, cookie="default"):
        return c.raw("POST", path, data, origin=origin, cookie=cookie, host=H)

    def tok(path, cookie="default"):
        m = re.search(r'name=csrf value="([^"]+)"', c.raw("GET", path, host=H, cookie=cookie)[2])
        return m.group(1) if m else ""

    # /setup: 不正な Origin は 403、正しい Origin は成功
    for o, label in BAD:
        r = post("/setup", {"csrf": tok("/setup"), "username": "u", "password": pw1, "confirm": pw1}, o)
        check(f"[{host}] /setup: {label} は 403", r[0] == 403, str(r[0]))
    check(f"[{host}] 拒否の間は auth.json が作られない", not os.path.exists(os.path.join(RUN_, "auth.json")))
    r = post("/setup", {"csrf": tok("/setup"), "username": "u", "password": pw1, "confirm": pw1}, good)
    check(f"[{host}] /setup: Origin {good} で成功", r[0] == 302 and "Set-Cookie" in r[1], str(r[:2]))
    ck = re.match(r"([^;]+)", r[1]["Set-Cookie"]).group(1)

    # /logout
    for o, label in BAD:
        r = post("/logout", {"csrf": tok("/", ck)}, o, ck)
        check(f"[{host}] /logout: {label} は 403(セッションは残る)", r[0] == 403 and c.raw("GET", "/inbox", cookie=ck, host=H)[0] == 200, str(r[0]))
    r = post("/logout", {"csrf": tok("/", ck)}, good, ck)
    check(f"[{host}] /logout: Origin {good} で成功", r[0] == 302 and r[1]["Location"] == "/login", str(r[:2]))

    # /login
    for o, label in BAD:
        r = post("/login", {"csrf": tok("/login"), "username": "u", "password": pw1}, o)
        check(f"[{host}] /login: {label} は 403", r[0] == 403 and "Set-Cookie" not in r[1], str(r[0]))
    r = post("/login", {"csrf": tok("/login"), "username": "u", "password": pw1}, good)
    check(f"[{host}] /login: Origin {good} で成功", r[0] == 302 and "Set-Cookie" in r[1], str(r[:2]))
    ck = re.match(r"([^;]+)", r[1]["Set-Cookie"]).group(1)

    # /reset
    allow = os.path.join(os.path.abspath(RUN_), "allow-reset")
    open(allow, "w").close()
    for o, label in BAD:
        r = post("/reset", {"csrf": tok("/login"), "password": pw2, "confirm": pw2}, o)
        check(f"[{host}] /reset: {label} は 403(許可は残る)", r[0] == 403 and os.path.exists(allow), str(r[0]))
    r = post("/reset", {"csrf": tok("/login"), "password": pw2, "confirm": pw2}, good)
    check(f"[{host}] /reset: Origin {good} で成功", r[0] == 302 and r[1]["Location"] == "/login" and not os.path.exists(allow), str(r[:2]))
    # 成功後も、Origin も Referer も無い POST は 403 のまま
    r = c.raw("POST", "/login", {"csrf": tok("/login"), "username": "u", "password": pw2}, origin=None, host=H)
    check(f"[{host}] Origin も Referer も無い /login は 403(成功後も)", r[0] == 403)
    stop(p)

# ================= 複数セッション: 1 プロセスが /s/<セッション名>/ で全 herdr セッションの画面を返す(セッションはリクエストごと)
env, dm = make_env("multi", HERDR_SESSION="alpha", FAKE_HERDR_SESSION_LOG=os.path.join(TMP, "multi-herdr-sessions.log"))
make_db(env["WEBUI_TB_DB"])   # make_db の行は default
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO tasks VALUES ('T-9','pj','beta だけの題','w','sid-b',NULL,'in_progress',NULL,'[]',NULL,NULL,NULL,"
           "'2026-11-06T00:00:00Z','2026-11-06T00:00:00Z','beta')")
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES ('*', 'projects', 'x', 'h', '2026-11-06T00:00:00Z')")
_c.commit()
_c.close()
os.makedirs(os.path.join(env["XDG_CONFIG_HOME"], "herdr", "sessions", "gamma"))
pm = start(env)
portm = wait_port(env, pm)
check("multi: 起動できる", portm is not None)
mc = Client(portm)
r = mc.get("/", raw=True)
check("multi: / はこのプロセスの herdr セッション(HERDR_SESSION)へ", r[0] == 302 and r[1]["Location"] == "/s/alpha/", str(r[:2]))
r = mc.get("/s/alpha?x=1", raw=True)
check("multi: /s/alpha は /s/alpha/ へ(クエリを残す)", r[0] == 302 and r[1]["Location"] == "/s/alpha/?x=1", str(r[:2]))
r = mc.get("/s/alpha/", raw=True)
check("multi: 未設定なら /s/alpha/setup へ", r[0] == 302 and r[1]["Location"] == "/s/alpha/setup", str(r[:2]))
r = mc.get("/s/alpha/setup", raw=True)
check("multi: HTML のフォーム先はそのセッションの下", r[0] == 200 and 'action="/s/alpha/setup"' in r[2] and 'action="/setup"' not in r[2], r[2][:300])
for _bad, _why in (("/s/nope/", "知らない名前"), ("/s/alphabet/", "似た名前"), ("/s/Alpha/", "大文字違い(herdr の名前は区別する)"),
                   ("/s/*/", "共通の行の名前"), ("/s/%2e%2e/", "エンコードした .."), ("/s/-x/", "- 始まり"), ("/x", "/s/ の外"),
                   ("/s/" + "a" * 65 + "/", "長すぎる名前")):
    r = mc.get(_bad, raw=True)
    check(f"multi: {_why}は 404", r[0] == 404, f"{_bad} {r[0]}")
r = mc.get("/", raw=True, host="evil.example")
check("multi: Host が自分以外は 403", r[0] == 403, str(r[0]))
r = mc.raw("POST", "/s/alpha/setup", {"csrf": mc.csrf("/s/alpha/setup"), "username": USER, "password": PW, "confirm": PW},
           origin=f"http://evil.example:{portm}", raw=True)
check("multi: 外の Origin の POST は 403", r[0] == 403, str(r[0]))
r = mc.raw("POST", "/s/alpha/setup", {"csrf": mc.csrf("/s/alpha/setup"), "username": USER, "password": PW, "confirm": PW}, raw=True)
ck = r[1].get("Set-Cookie", "")
check("multi: 初回設定は通り、Cookie は全セッション共通(Path=/)", r[0] == 302 and r[1]["Location"] == "/s/alpha/"
      and "Path=/;" in ck and ck.startswith("herdr_webui_session="), str(r[:2]))
mc.cookie = re.match(r"([^;]+)", ck).group(1) if ck else None
r = mc.get("/s/alpha/", raw=True)
check("multi: リンク・スクリプトはそのセッションの下", r[0] == 200 and 'href="/s/alpha/inbox"' in r[2] and 'src="/s/alpha/notify.js"' in r[2]
      and 'href="/inbox"' not in r[2] and 'href="/s/alpha/sessions"' in r[2], r[2][:300])
check("multi: タイトルとヘッダにセッション名(default 以外は色の帯)", re.search(r"<title>[^<]*\[alpha\][^<]*</title>", r[2]) is not None
      and 'class="inst inst-warn"' in r[2] and ">alpha<" in r[2])
r = mc.get("/s/default/tasks", raw=True)
check("multi: default は帯なしで名前を出す・default の行だけ", r[0] == 200 and 'class="inst"' in r[2] and ">default<" in r[2]
      and "inst inst-warn" not in r[2] and "T-1" in r[2] and "beta だけの題" not in r[2], r[2][:300])
r = mc.get("/s/beta/tasks", raw=True)
check("multi: 同じログインのまま beta(DB に行のあるセッション)を開ける・beta の行だけ",
      r[0] == 200 and "beta だけの題" in r[2] and "T-1" not in r[2] and 'href="/s/beta/inbox"' in r[2], str(r[0]))
r = mc.get("/s/gamma/tasks", raw=True)
check("multi: herdr のセッションのディレクトリにあるだけの gamma も開ける", r[0] == 200 and ">gamma<" in r[2], str(r[0]))
r = mc.get("/s/alpha/notify.js", raw=True)
check("multi: notify.js はそのセッションの置き場所を知る(取得先・保存キー・通知のタグ)", r[0] == 200 and 'var BASE = "/s/alpha"' in r[2], r[2][:200])
r = mc.get("/s/beta/api/notifications", raw=True)
check("multi: 緊急通知の API もセッションごと", r[0] == 200 and r[1]["Content-Type"].startswith("application/json"), str(r[0]))
r = mc.get("/s/alpha/inbox", raw=True, cookie=None)
check("multi: 未ログインはそのセッションのログインへ(next はセッションの下のパス)",
      r[0] == 302 and r[1]["Location"] == "/s/alpha/login?next=%2Finbox", str(r[:2]))
_hl = os.path.join(TMP, "multi-herdr-sessions.log")
_lines = [x for x in open(_hl).read().split("\n") if x] if os.path.exists(_hl) else []
check("multi: herdr はその画面のセッション(HERDR_SESSION)として呼ぶ",
      {x.rsplit(" ", 1)[-1] for x in _lines if x.startswith("agent list")} >= {"beta", "default", "gamma"}, str(_lines[:8]))
r = mc.get("/s/beta/sessions", raw=True)
check("multi: セッション一覧に全セッション(相対リンク)、この画面は太字。ポート番号は出さない",
      r[0] == 200 and "<strong>beta</strong>(この画面)" in r[2] and 'href="../alpha/"' in r[2] and 'href="../default/"' in r[2]
      and 'href="../gamma/"' in r[2] and 'href="/s/beta/sessions" class=on' in r[2] and str(portm) not in r[2] and "data-api" not in r[2]
      and 'href="../*/"' not in r[2], r[2][-1500:])
r = mc.raw("POST", "/s/beta/logout", {"csrf": re.search(r'name=csrf value="([^"]+)"', mc.get("/s/beta/tasks", raw=True)[2]).group(1)}, raw=True)
check("multi: ログアウトはそのセッションのログインへ・Cookie も Path=/ で消す",
      r[0] == 302 and r[1]["Location"] == "/s/beta/login" and "Path=/;" in r[1].get("Set-Cookie", "") and "Max-Age=0" in r[1].get("Set-Cookie", ""), str(r[:2]))
check("multi: ログアウトすると、ほかのセッションの画面にも入れない", mc.get("/s/alpha/tasks", raw=True)[0] == 302)
r = mc.get("/s/alpha/login", raw=True, cookie=None)
check("multi: ログイン画面はそのセッションの下のフォーム", r[0] == 200 and 'action="/s/alpha/login"' in r[2] and 'href="/s/alpha/reset"' in r[2] and ">alpha<" in r[2])
stop(pm)

# ---- 表示にセッション名の生の HTML 特殊文字が出ない(多層防御の確認。URL の名前は SESSION_RE で絞っている)
sys.path.insert(0, os.path.dirname(HERE))
import app.views as _v2  # noqa: E402
import app.reqctx as _rc2  # noqa: E402
with _rc2.use("a<b>&\"x", "default"):
    check("セッション名はエスケープされて出る(ヘッダ)", "<b>" not in _v2.inst_label() and "&lt;b&gt;" in _v2.inst_label())
    check("セッション名はエスケープされて出る(タイトル)", "<b>" not in _v2.page("t", "", "/", 0))

# ---- セッションの一覧(app/sessions.py): DB の行・herdr のディレクトリ・default・プロセスのセッション。'*' や不正な名前は出さない
import app.sessions as _sess  # noqa: E402
_old = {k: os.environ.get(k) for k in ("WEBUI_TB_DB", "XDG_CONFIG_HOME", "HERDR_SESSION", "HERDR_SOCKET_PATH")}
try:
    os.environ["WEBUI_TB_DB"] = env["WEBUI_TB_DB"]
    os.environ["XDG_CONFIG_HOME"] = env["XDG_CONFIG_HOME"]
    os.environ["HERDR_SESSION"] = "zeta"
    os.environ.pop("HERDR_SOCKET_PATH", None)
    os.makedirs(os.path.join(env["XDG_CONFIG_HOME"], "herdr", "sessions", "bad name"), exist_ok=True)
    open(os.path.join(env["XDG_CONFIG_HOME"], "herdr", "sessions", "afile"), "w").write("")
    _sess.forget()
    check("sessions: 一覧(DB の beta と default、ディレクトリの gamma、プロセスの zeta)",
          _sess.known() == ["beta", "default", "gamma", "zeta"], str(_sess.known()))
    check("sessions: 一覧に無い名前・不正な名前は通さない", not _sess.is_known("nope") and not _sess.is_known("*") and not _sess.is_known("bad name")
          and not _sess.is_known("afile") and _sess.is_known("gamma"))
finally:
    for _k, _v in _old.items():
        if _v is None:
            os.environ.pop(_k, None)
        else:
            os.environ[_k] = _v
    _sess.forget()

# ---- /docs から run/ とログイン情報の置き場を読めない
import app.docs as _docs2  # noqa: E402
_rb = os.path.join(TMP, "docs-runbase")
for _sub in ("", "instance-other"):
    os.makedirs(os.path.join(_rb, _sub), exist_ok=True)
    open(os.path.join(_rb, _sub, "notes.txt"), "w").write("x")
_old = {k: os.environ.get(k) for k in ("WEBUI_RUN_BASE", "WEBUI_RUN_DIR", "WEBUI_STATE_DIR", "WEBUI_AUTH_DIR")}
try:
    os.environ["WEBUI_RUN_BASE"] = _rb
    os.environ.pop("WEBUI_RUN_DIR", None)
    check("docs: run/ 配下は開かない", _docs2.denied(os.path.realpath(os.path.join(_rb, "notes.txt"))) is not None)
    check("docs: 旧版の run/instance-<名>/ も開かない", _docs2.denied(os.path.realpath(os.path.join(_rb, "instance-other", "auth.json"))) is not None)
    # 実体で比べる: 綴り違い(大文字小文字を区別しないボリューム)・別名(symlink)でも、run の配下は開かない
    _alias = os.path.join(TMP, "docs-run-alias")
    os.symlink(_rb, _alias)
    check("docs: run への symlink 経由でも開かない", _docs2.denied(os.path.join(_alias, "instance-other", "auth.json")) is not None)
    check("docs: run への symlink 経由・存在しないファイルでも開かない", _docs2.denied(os.path.join(_alias, "instance-other", "nope.json")) is not None)
    _case = os.path.join(os.path.dirname(_rb), os.path.basename(_rb).upper())
    if os.path.exists(_case):   # 大文字小文字を区別しないボリューム(macOS の既定)
        check("docs: 綴りを変えた run(大文字小文字違い)でも開かない", _docs2.denied(os.path.join(_case, "instance-other", "auth.json")) is not None)
        _dd = os.path.join(TMP, "docs-case"); os.makedirs(os.path.join(_dd, ".ssh"), exist_ok=True)
        check("docs: 綴りを変えた .SSH も開かない", _docs2.denied(os.path.join(_dd, ".SSH", "x")) is not None)
    check("docs: 無関係な存在しないパスは開く判定(denied でない)", _docs2.denied(os.path.join(TMP, "no-such-dir", "a.md")) is None)
    check("docs: run の隣の別ディレクトリ(前方一致だけ同じ名前)は denied でない",
          _docs2.denied(os.path.join(os.path.dirname(_rb), os.path.basename(_rb) + "-x", "a.md")) is None)
    _ex = os.path.join(TMP, "docs-explicit-run"); os.makedirs(_ex, exist_ok=True)
    os.environ["WEBUI_RUN_DIR"] = _ex
    check("docs: WEBUI_RUN_DIR 明示でも、その場所は開かない", _docs2.denied(os.path.realpath(os.path.join(_ex, "auth.json"))) is not None)
    _sd = os.path.join(TMP, "docs-state"); os.makedirs(_sd, exist_ok=True)
    os.environ["WEBUI_STATE_DIR"] = _sd
    os.environ.pop("WEBUI_AUTH_DIR", None)
    check("docs: 共通のログイン情報・記録(ホームの置き場)は開かない",
          _docs2.denied(os.path.join(_sd, "auth.json")) is not None and _docs2.denied(os.path.join(_sd, "gateway.json")) is not None)
finally:
    for _k, _v in _old.items():
        if _v is None:
            os.environ.pop(_k, None)
        else:
            os.environ[_k] = _v

# ================= 関係グラフ(app/graph.py。tb agents --graph と同じ SQL とまとめ方)と規則の @<役割>
sys.path.insert(0, os.path.dirname(HERE))
import app.graph as _graph  # noqa: E402
import app.perms as _perms2  # noqa: E402
_rd = os.path.join(TMP, "graph-roles")
os.makedirs(_rd, exist_ok=True)
for _r in ("conductor", "worker", "watcher"):
    open(os.path.join(_rd, _r + ".md"), "w").write("x")
open(os.path.join(_rd, "map.conf"), "w").write("*-conductor = conductor\nworker* = worker\nwatch* = watcher\n")
_pc = os.path.join(TMP, "graph-projects.conf")
open(_pc, "w").write("# c\nprojb = b-conductor\n")
_old = {k: os.environ.get(k) for k in ("TB_ROLES", "TB_CONFIG")}
os.environ.update(TB_ROLES=_rd, TB_CONFIG=_pc)
check("graph: 役割は名前の .md > 台帳の role > map.conf", [_graph.role_of(n, r) for n, r in
      (("conductor", None), ("b-conductor", None), ("worker1", None), ("helper", "worker"), ("helper", None), ("_x", None))]
      == ["conductor", "conductor", "worker", "worker", None, None])
_rows = [{"verb": "lead", "src": None, "dst": "worker2", "project": "projb", "status": "ok", "n": 1, "last_at": "a"},
         {"verb": "lead", "src": None, "dst": "worker2", "project": "proja", "status": "ok", "n": 1, "last_at": "b"},
         {"verb": "send:task", "src": "conductor", "dst": "worker1", "project": None, "status": "ok", "n": 2, "last_at": "c"},
         {"verb": "report", "src": "worker2", "dst": None, "project": "projb", "status": "ok", "n": 1, "last_at": "d"},
         {"verb": "spawn", "src": "worker1", "dst": "worker1", "project": None, "status": "ok", "n": 1, "last_at": "e"}]
_es = _graph.edges(_rows)
check("graph: project の端は指揮官で埋め、自分への辺は捨てる(1 体に複数の上流)",
      [(x["from"], x["verb"], x["to"]) for x in _es] == [("b-conductor", "lead", "worker2"), ("conductor", "send:task", "worker1"),
                                                         ("conductor", "lead", "worker2"), ("worker2", "report", "b-conductor")])
_ns = [{"name": n, "role": _graph.role_of(n)} for n in ("conductor", "b-conductor", "worker1", "worker2", "<b>x</b>")]
_mm = _graph.mermaid(_ns, _es + [{"verb": "send:task", "from": "worker1", "to": "worker2", "status": "denied", "n": 1, "last_at": ""}])
check("graph: ロールごとの subgraph とラベルつきの辺(拒否は点線)", 'subgraph g0["conductor"]' in _mm and 'subgraph g2["役割なし"]' in _mm
      and "-- 依頼 x2 -->" in _mm and "-. 依頼 拒否 .->" in _mm and "<b>" not in _mm, _mm)
check("graph: 規則の @役割", [_graph.name_matches(n, p) for n, p in (("worker1", "@worker"), ("worker1", "@cond*"), ("x", "@worker"), ("x", "x"))]
      == [True, False, False, True])
open(os.path.join(_rd, "manager.md"), "w").write("x")
open(os.path.join(_rd, "hr.md"), "w").write("x")
check("graph: 複数ロール(台帳はカンマ区切り)と @a+b", _graph.role_of("helper", "hr, manager") == "hr,manager"
      and [_graph.name_matches("helper", p, "hr,manager") for p in ("@hr", "@hr+manager", "@hr+worker", "@manag*")] == [True, True, False, True])
_rules = [{"raw": "send:*:@worker", "value": "allow"}, {"raw": "*", "value": "ask"}]
check("perms: @役割 の行は文字列の一致だけで「届かない」としない",
      _perms2.shadowed_by([{"no": 1, "raw": "send:task:*r", "value": "allow"}, {"no": 2, "raw": "send:task:@worker", "value": "ask"}],
                          {"no": 2, "raw": "send:task:@worker", "value": "ask"}) is None)
import app.data as _data2  # noqa: E402
_qc = _data2.q_checked
_data2.q_checked = lambda *a, **k: ([], True)   # 台帳は読めるが行が無い
check("perms: 末尾 @役割 の行を役割で照合", [_perms2.decide(_rules, op)[0] for op in ("send:task:worker1", "send:task:conductor", "role:change")]
      == ["allow", "ask", "ask"])
_data2.q_checked = lambda *a, **k: ([], False)
check("perms: 台帳を読めないときは @役割 を map.conf で一致させない(名前の役割ファイルは使う)",
      [_perms2.decide(_rules, "send:task:worker1")[0],
       _perms2.decide([{"raw": "send:*:@conductor", "value": "allow"}, {"raw": "*", "value": "ask"}], "send:task:conductor")[0],
       _perms2.decide([{"raw": "send:task:@worker", "value": "ask"}, {"raw": "*", "value": "allow"}], "send:task:worker1")[0]]
      == ["ask", "allow", "ask"])
_data2.q_checked = _qc
os.makedirs(os.path.join(_rd, "sessions", "s2"), exist_ok=True)
open(os.path.join(_rd, "sessions", "s2", "auditor.md"), "w").write("a")
open(os.path.join(_rd, "sessions", "s2", "map.conf"), "w").write("worker* = auditor\n")
_hs = os.environ.get("HERDR_SESSION")


def _in_session(s, f):
    os.environ["HERDR_SESSION"] = s
    try:
        return f()
    finally:
        if _hs is None:
            os.environ.pop("HERDR_SESSION", None)
        else:
            os.environ["HERDR_SESSION"] = _hs


check("graph: ロールは herdr セッション固有(このセッションの roles/sessions/<s>/ があればそこだけ)",
      [_in_session("default", lambda: _graph.role_of("worker1")), _in_session("s2", lambda: _graph.role_of("worker1")),
       _in_session("s2", lambda: _graph.role_of("worker1", "worker")), _in_session("s9", lambda: _graph.role_of("worker1")),
       _in_session("s2", lambda: _graph.name_matches("worker1", "@auditor"))]
      == ["worker", "auditor", "auditor", "worker", True])
check("perms: 拡張 glob の行(!(...) など)は判定できないとして止まる(下の allow に進まない)",
      _perms2.decide([{"raw": "send:task:@!(conductor)", "value": "ask"}, {"raw": "*", "value": "allow"}], "send:task:worker1")[0] == "unknown")
for _k, _v in _old.items():
    if _v is None:
        os.environ.pop(_k, None)
    else:
        os.environ[_k] = _v

# ---- 待ち受けたポートの記録(app/registry.py): 形式どおりのものだけ、書いたプロセスが居なければ動いていない扱い
import app.registry as _reg  # noqa: E402
_os = os.environ.get("WEBUI_STATE_DIR")
os.environ["WEBUI_STATE_DIR"] = os.path.join(TMP, "reg-state")
os.makedirs(os.environ["WEBUI_STATE_DIR"], mode=0o700)
_gp = os.path.join(os.environ["WEBUI_STATE_DIR"], "gateway.json")
_res = []
for _d in ({"port": "80", "pid": os.getpid()}, {"port": True, "pid": os.getpid()}, {"port": 80, "pid": 0}, {"port": 80, "pid": -1},
           {"port": 80, "pid": 2 ** 22 + 12345}, [1]):
    json.dump(_d, open(_gp, "w"))
    _res.append(_reg.read_gateway())
check("registry: 形式違い・pid が 0 や負・居ないプロセスの記録は動いていない扱い", _res == [None] * 6, str(_res))
check("registry: pid が 0 や負なら kill しない", not _reg.pid_ok({"pid": 0}) and not _reg.pid_ok({"pid": -1}) and not _reg.pid_ok({}))
json.dump({"port": 80, "pid": 1}, open(_gp, "w"))
_reg.clear_gateway()
check("registry: 他のプロセスの記録は消さない", os.path.exists(_gp))
_reg.write_gateway(80)
check("registry: 自分の記録は 600 で書き、消せる", mode(_gp) == "0o600" and (_reg.clear_gateway() or not os.path.exists(_gp)))
if _os is None:
    os.environ.pop("WEBUI_STATE_DIR")
else:
    os.environ["WEBUI_STATE_DIR"] = _os

# ================= ログイン情報は複数のプロセスで共有する(失敗回数のロックも共有。渡り歩いて回数を稼げない)
import auth as _auth2  # noqa: E402
_ad = os.path.join(TMP, "shared-auth")
_a1, _a2 = _auth2.Auth(_ad), _auth2.Auth(_ad)
_a1.setup("u", "long-enough-pass-1")
_ol = os.environ.get("WEBUI_LOCK_MAX")
os.environ["WEBUI_LOCK_MAX"] = "3"
for _i in range(3):
    (_a1 if _i % 2 == 0 else _a2).attempt("u", "wrong-wrong-wrong")
check("auth: 失敗回数は置き場で共有(別のプロセスで数えてもロックする)", _a1.locked_for() > 0 and _a2.locked_for() > 0)
check("auth: ロックの記録は 0600", mode(os.path.join(_ad, "lockout.json")) == "0o600" and mode(_ad) == "0o700")
if _ol is None:
    os.environ.pop("WEBUI_LOCK_MAX")
else:
    os.environ["WEBUI_LOCK_MAX"] = _ol
_t1 = _a1.create_session()
_t2 = _a2.create_session()
check("auth: 別のプロセスで作ったセッションも有効(書き込みが互いを消さない)", _a2.session_hash(_t1) and _a1.session_hash(_t2))
_a2.destroy_session(_t1)
check("auth: 別のプロセスでのログアウトも効く", _a1.session_hash(_t1) is None and _a1.session_hash(_t2))

# ================= DB は全セッション共有。各セッションの Web UI は自分のセッションの行だけを読む(app/data.py の一時ビュー)
sys.path.insert(0, os.path.dirname(HERE))
from app import data as _data  # noqa: E402
check("data: 一時ビューを作る表は taskboard/sql/session_tables(tb と同じ一覧)",
      "tasks" in _data.session_tables() and "config" in _data.session_tables() and all(re.fullmatch(r"[a-z_]+", t) for t in _data.session_tables()))
_sdb = os.path.join(TMP, "sessions.db")
make_db(_sdb)
_c = sqlite3.connect(_sdb)
_c.execute("INSERT INTO tasks (id,project,title,status,created_at,updated_at,herdr_session) VALUES ('T-o-001','o','other','todo','t','t','other')")
_c.execute("INSERT INTO comments (task_id,kind,text,from_name,from_pane,from_session,created_at,herdr_session) "
           "VALUES ('T-o-001','question','other q','x','p','s','t','other')")
_c.commit(); _c.close()
_old_db, _old_hs = os.environ.get("WEBUI_TB_DB"), os.environ.get("HERDR_SESSION")
os.environ["WEBUI_TB_DB"] = _sdb
os.environ["HERDR_SESSION"] = "default"
_d_ids = [r["id"] for r in _data.q("SELECT id FROM tasks ORDER BY id")]
_d_cm = _data.q("SELECT COUNT(*) AS n FROM comments c JOIN tasks t ON t.id=c.task_id")[0]["n"]
os.environ["HERDR_SESSION"] = "other"
_o_ids = [r["id"] for r in _data.q("SELECT id FROM tasks ORDER BY id")]
check("sessions: default の Web UI に別のセッションの行は出ない", _d_ids == ["T-1"] and _d_cm == 1, f"{_d_ids} {_d_cm}")
check("sessions: other の Web UI は other の行だけ", _o_ids == ["T-o-001"], str(_o_ids))
check("sessions: 読み取り専用のまま(書き込めない)", not _data.q("INSERT INTO tasks (id,project,title,created_at,updated_at) VALUES ('x','x','x','t','t') RETURNING id"))
# セッションはリクエストの文脈(app/reqctx.py)から読む。プロセスの環境(ここでは other)より文脈が勝つ
from app import reqctx as _rc, herdr as _hd, handler as _hdl  # noqa: E402
with _rc.use("default", "default"):
    _ctx_ids = [r["id"] for r in _data.q("SELECT id FROM tasks ORDER BY id")]
    _ctx_base, _ctx_link = _rc.base(), _hdl.with_base('<a href="/tasks">')
    _ctx_env = _rc.child_env({"HERDR_SOCKET_PATH": "/h/.config/herdr/sessions/other/herdr.sock", "HERDR_PANE_ID": "p"})
check("reqctx: 文脈のセッションの行を読む(プロセスの環境は other)", _ctx_ids == ["T-1"], str(_ctx_ids))
check("reqctx: BASE とリンクは文脈の名前", (_ctx_base, _ctx_link) == ("/s/default", '<a href="/s/default/tasks">'), f"{_ctx_base} {_ctx_link}")
check("reqctx: 子プロセスには文脈のセッションを HERDR_SESSION で渡し、socket のパスは外す",
      _ctx_env.get("HERDR_SESSION") == "default" and "HERDR_SOCKET_PATH" not in _ctx_env and _ctx_env.get("HERDR_PANE_ID") == "p", str(_ctx_env))
check("reqctx: プロセスと同じセッションなら環境はそのまま", _rc.child_env({"X": "1"}) == {"X": "1"})
_calls = []
for _s in ("sa", "sb", "sa"):
    with _rc.use(_s, _s):
        _hd.cached("t-key", lambda s=_s: _calls.append(s) or s)
check("reqctx: herdr の結果キャッシュはセッションごと", _calls == ["sa", "sb"], str(_calls))
check("reqctx: 文脈の外はプロセスの設定に落ちる", _rc.session() == "other")
# 設定(config 表): このセッションの行 → 共通の行(*) → checkout のファイル。環境変数で明示したファイルが最優先
_c = sqlite3.connect(_sdb)
_c.execute("INSERT INTO config VALUES ('*','projects','pj = shared-cond','human','t'), ('sa','projects','pj = sa-cond','human','t'), ('*','approvals','send:* = allow','human','t')")
_c.commit(); _c.close()
from app import graph as _g3  # noqa: E402
_old_tc = os.environ.pop("TB_CONFIG", None)
_old_wa = os.environ.pop("WEBUI_APPROVALS", None)
try:
    _cfg_t = []
    for _s in ("sa", "sb"):
        with _rc.use(_s, _s):
            _cfg_t.append((_g3.target_for("pj"), _data.config_text("projects")[1]))
    check("config: projects はこのセッションの行、無ければ共通の行", _cfg_t == [("sa-cond", "db:sa"), ("shared-cond", "db:*")], str(_cfg_t))
    with _rc.use("sb", "sb"):
        _ap = _data.config_text("approvals")
        _pl = __import__("app.perms", fromlist=["x"]).conf_path_label()
    check("config: approvals も DB から(出どころの表示つき)", _ap[0] == "send:* = allow" and "全セッション共通" in _pl, f"{_ap} {_pl}")
    os.environ["TB_CONFIG"] = os.path.join(TMP, "explicit-projects.conf")
    open(os.environ["TB_CONFIG"], "w").write("pj = file-cond\n")
    with _rc.use("sa", "sa"):
        check("config: 環境変数で明示したファイルが DB より先", _g3.target_for("pj") == "file-cond")
    # 役割の定義も DB から(role:<名前> / role-map)。このセッションの行 > 共通の行 > roles/ のファイル
    _c = sqlite3.connect(_sdb)
    _c.execute("INSERT INTO config VALUES ('sa','role:zrole','z の役割','human','t'), ('*','role-map','z* = zrole','human','t')")
    _c.commit(); _c.close()
    _rl = []
    for _s in ("sa", "sb"):
        with _rc.use(_s, _s):
            _rl.append((_g3.role_of("z1"), "zrole" in _g3.role_names()))
    check("roles: DB の役割と対応(このセッションの行だけが効く)", _rl == [("zrole", True), (None, False)], str(_rl))
finally:
    for _k, _v in (("TB_CONFIG", _old_tc), ("WEBUI_APPROVALS", _old_wa)):
        if _v is None:
            os.environ.pop(_k, None)
        else:
            os.environ[_k] = _v
for _k, _v in (("WEBUI_TB_DB", _old_db), ("HERDR_SESSION", _old_hs)):
    if _v is None:
        os.environ.pop(_k, None)
    else:
        os.environ[_k] = _v

print()
print("FAILED: " + ", ".join(fails) if fails else "all passed")
sys.exit(1 if fails else 0)
