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
SERVER = os.path.join(os.path.dirname(HERE), "bin", "serve.sh")   # 中身は herdrctl webui serve
REPO = os.path.dirname(os.path.dirname(HERE))
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
        WEBUI_SCHED_LOGS=os.path.join(d, "logs"), WEBUI_POLICY=os.path.join(d, "policy.conf"),
        WEBUI_SCHED_JOBS=os.path.join(d, "jobs"), WEBUI_SCHED_SETTINGS=os.path.join(d, "settings"),
        WEBUI_STATE_DIR=os.path.join(d, "state"), WEBUI_AUTH_DIR=os.path.join(d, "run"),
        XDG_CONFIG_HOME=os.path.join(d, "xdg"), HERDR_SESSION_FILE=os.path.join(d, "no-herdr-session"),
        TB_PROTECT=os.path.join(d, "protect.conf"), TB_LABELS=os.path.join(d, "labels.conf"),
        SCHED_FILE_FALLBACK="1")   # ジョブはファイル(jobs/*.conf)に置いて試す。既定(DB だけ)は「既定は DB だけ」のところで確かめる
    # 役割の既定(roles/)は _common.md だけ。setup の利用例(setup/examples/roles)を入れた状態で試す
    rd = os.path.join(d, "roles")
    if not os.path.isdir(rd):
        shutil.copytree(os.path.join(HERE, "..", "..", "setup", "examples", "roles"), rd)
        shutil.copy(os.path.join(HERE, "..", "..", "roles", "_common.md"), rd)
    env["TB_ROLES"] = rd
    env.update(extra)
    env.setdefault("TB_UI_GATEWAY_PORT", str(free_range(1)))
    return env, d


def start(env):
    p = subprocess.Popen(["bash", SERVER], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
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
    c.execute("INSERT INTO tasks (id,project,title,owner_agent,session_id,account,status,parent_id,deliverables,summary,note,reported_at,created_at,updated_at,herdr_session) VALUES ('T-1','pj','題 " + XSS + "','alpha','11111111-2222-3333-4444-555555555555',"
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
REFRESH_TAG = '<script src="/refresh.js" defer></script>'
FILTER_TAG = '<script src="/filter.js" defer></script>'   # タスク一覧の絞り込み(選んだらすぐ反映)


def no_js(b):
    return b.replace(NOTIFY_TAG, "").replace(REFRESH_TAG, "")


def script_tags(b):
    """<script> の開始タグの一覧。自動更新の /refresh.js は除く(自動更新の有無は別に確かめる)。"""
    return [t for t in re.findall(r"<script[^>]*>", b, re.I) if t != REFRESH_TAG[:-9]]


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
open(os.path.join(_J, "pman.conf"), "w").write("COMMAND='echo m'\nMANUAL=1\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "ponce.conf"), "w").write("COMMAND='echo once'\nAT=\"2099-01-02 03:04\"\nTZ=Asia/Tokyo\n")
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
open(os.path.join(_J, "prules.conf"), "w").write("TARGET=alpha\nPROMPT_RULES='\nmin=00 | 毎時の<b>本文</b>\nmin=30 | 半の本文\n'\nPROMPT_VARS='hits | gh search issues --token=supersecretvalue2'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
open(os.path.join(_J, "cmdx.conf"), "w").write("COMMAND='cd /x && TOKEN=supersecretvalue1 bin/tb <i>y</i> $(date)'\nCOMMAND_TIMEOUT=45\nWINDOW=00:00-23:59\nINTERVAL_SEC=120\nTZ=Asia/Tokyo\n")
# 権限(設定 policy)の fixture。番号・警告・厳しい行が重なる行・エスケープの確認用
with open(env["WEBUI_POLICY"], "w") as f:
    f.write("# 先頭の注釈\n* send:info * = allow   # 2026-11-01T00:00:00Z に人が変更\n* send:task dd-* = allow\n* send:task * = ask\n"
            "* agent:* * = allow   # 2026-11-02T00:00:00Z に人が追加\n* role:* * = ask\n* role:change * = allow\n"
            "* x<b>bold</b>y * = allow\n@lead send:task ~child = allow\n")
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
_t_ins = "INSERT INTO tasks (id,project,title,owner_agent,session_id,account,status,parent_id,deliverables,summary,note,reported_at,created_at,updated_at,herdr_session) VALUES (?,?,?,?,NULL,NULL,?,?,'[]',NULL,NULL,?,?,?,'default')"
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
    check(f"{path}: スクリプトは同一オリジンの notify.js だけ(タスク一覧は絞り込みの filter.js も)",
          script_tags(b) == [NOTIFY_TAG[:-9]] + (['<script src="/filter.js" defer>'] if path == "/tasks" else []) and "<script" not in no_js(b).replace(FILTER_TAG, "").lower()
          and "<img" not in b.lower(), str(re.findall(r"<script[^>]*>", b, re.I)))
    check(f"{path}: 自動更新は refresh.js(meta refresh は noscript の中だけ)",
          ('http-equiv="refresh"' in b) == (REFRESH_TAG in b) == ('<meta name=herdr-refresh content="30">' in b)
          and ('http-equiv="refresh"' not in b or '<noscript><meta http-equiv="refresh" content="30"></noscript>' in b), path)
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
      and script_tags(_gb) == [NOTIFY_TAG[:-9], '<script src="/static/mermaid.min.js" defer>', '<script src="/docs.js" defer>'],
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
for label in ("質問", "代行依頼", "承認待ちの agent", "滞留", "job の失敗"):
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
# 送信の承認待ち(tb send が policy で ask になったもの)。承認待ちは 1 種類(Q-<n>。requests 表。トリガーが outbox の pending から作る)
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.executescript("INSERT INTO outbox (from_name,from_pane,from_session,target,kind,text,status,created_at) VALUES ('conductor','w0:p1','s','w1','task','調べて <b>x</b>','pending','t');"
                 "INSERT INTO outbox (from_name,from_pane,from_session,target,kind,text,status,created_at) VALUES ('conductor','w0:p1','s','w1','task','済','sent','t');")
_c.commit(); _c.close()
time.sleep(3.2)  # 通知のキャッシュ切れを待つ
r = cl.get("/api/notifications")
_it = [it for it in json.loads(r[2])["items"] if it["id"].startswith("q:") and "send:task w1" in it["title"]] if r[0] == 200 else []
_qid = _it[0]["id"][2:] if len(_it) == 1 else "?"
check("送信の承認待ちが Q-<n> で通知に出る(承認コマンド付き)", len(_it) == 1 and _it[0]["kind"] == "approval_pending" and _it[0]["title"].startswith(f"Q-{_qid} conductor")
      and f"tb approval approve Q-{_qid}" in _it[0]["how"] and "--always" in _it[0]["how"] and f"tb approval reject Q-{_qid}" in _it[0]["how"]
      and "tb approval approve" not in _it[0]["body"], str(_it))
check("送信の承認待ちは /notifications でエスケープ", "&lt;b&gt;x&lt;/b&gt;" in cl.get("/notifications")[2])
_nb = cl.get("/notifications")[2]
check("通知は 3 行(何が / 中身 / 対応)で、対応に承認コマンドが出る", f"対応: 承認するなら手元で tb approval approve Q-{_qid}" in _nb)
_ib = cl.get("/inbox")[2]
check("報告・承認待ち: 承認待ちの節に、どこでどう承認するかが出る",
      "承認待ち(どこで・どう承認するか)" in _ib and f"tb approval approve Q-{_qid}" in _ib.split("要対応の報告")[0] and "手元で tb …" in _ib)
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
# 報告・承認待ち: 保留した送信(inbox)と「忘れているかも」(tb reminders の結果)。どちらも読むだけ、データがあるときだけ出す
_ib = cl.get("/inbox")[2]
check("inbox: データが無ければ保留・忘れているかもの欄は出ない", "保留した送信" not in _ib and "忘れているかも" not in _ib)
check("inbox: 保留が無いときも本文の指定は 200", cl.get("/inbox?held=1")[0] == 200)
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.executescript("""INSERT INTO inbox (herdr_session,outbox_id,recipient,from_name,kind,task_id,text,rule,created_at,read_at) VALUES
  ('default',7,'assistant','conductor','info','T-1','HELD-BODY <script>alert(9)</script>','r','2026-11-06T01:00:00Z',NULL),
  ('default',8,'assistant','<b>bob</b>','answer',NULL,'READ-BODY','r','2026-11-06T02:00:00Z','2026-11-06T03:00:00Z'),
  ('other',9,'assistant','conductor','info',NULL,'OTHER-BODY','r','2026-11-06T04:00:00Z',NULL);""")
_rem = {"at": "2026-11-06T05:00:00Z", "full": False, "removed": ["o:9"], "text": "x", "items": [
    {"p": 1, "key": "due:T-1", "what": "期限: T-1 <i>ship</i>", "who": "alpha", "due": "2026-11-05", "ref": "T-1", "age": "1d", "status": "changed", "day": 2},
    {"p": 2, "key": "q:1", "what": "質問: T-1", "who": "人", "due": "", "ref": "C-1", "age": "2h", "status": "continuing", "day": 3},
    {"p": 2, "key": "pr:org/repo#7", "what": "レビュー待ちの PR: add", "who": "レビュー担当", "due": "", "ref": "org/repo#7", "age": "1d", "status": "new", "day": 1},
    {"p": 4, "key": "inbox:1", "what": "未読の保留メッセージ", "who": "assistant", "due": "", "ref": "I-1", "age": "1h", "status": "new", "day": 1},
    {"p": 9, "key": "bad", "what": "壊れた項目", "status": "new"}]}
_c.execute("INSERT INTO notify_seen (herdr_session,key,value,updated_at) VALUES ('default','reminders:last',?, 't')", (json.dumps(_rem),))
_c.execute("INSERT INTO notify_seen (herdr_session,key,value,updated_at) VALUES ('other','reminders:last',?, 't')",
           (json.dumps({"at": "x", "items": [{"p": 1, "key": "due:x", "what": "OTHER-SESSION", "status": "new"}]}),))
_c.commit(); _c.close()
time.sleep(3.2)
_ib = cl.get("/inbox")[2]
_rm = _ib.split('<h2 id="reminders">')[1].split("</p>")[0] if '<h2 id="reminders">' in _ib else ""
check("inbox: 忘れているかもの欄が出る(優先度・期限・前回から)", "忘れているかも" in _rm and "急ぎ" in _rm and "2026-11-05" in _rm and "継続(3 日目)" in _rm
      and "変化" in _rm and "解消 1 件" in _rm, _rm[:400])
check("inbox: 忘れているかもの値はエスケープ、壊れた項目は捨てる", "&lt;i&gt;ship&lt;/i&gt;" in _rm and "<i>ship" not in _rm and "壊れた項目" not in _rm)
check("inbox: 項目は既存の画面へリンク(質問は /inbox、タスク、保留は本文、PR はリンクなし)",
      '<a href="/inbox">C-1</a>' in _rm and '<a href="/tasks/T-1">T-1</a>' in _rm and '<a href="/inbox?held=1">I-1</a>' in _rm
      and "<td>org/repo#7</td>" in _rm, _rm[:600])
check("inbox: 別のセッションのまとめは出ない", "OTHER-SESSION" not in _ib)
_hd = _ib.split('<h3 id="held">')[1].split("</p>")[0] if '<h3 id="held">' in _ib else ""
check("inbox: 保留した送信の一覧(このセッションの行だけ、未読・既読)", "I-1" in _hd and "I-2" in _hd and "I-3" not in _hd
      and "未読" in _hd and "既読 2026-11-06T03:00:00Z" in _hd and "&lt;b&gt;bob&lt;/b&gt;" in _hd, _hd[:500])
check("inbox: 一覧に本文は出さない", not any(x in _ib for x in ("HELD-BODY", "READ-BODY", "OTHER-BODY")))
_hb = cl.get("/inbox?held=1")[2]
check("inbox: 本文は明示して開いたときだけ(エスケープ)", "保留した送信 I-1 の本文" in _hb and "HELD-BODY &lt;script&gt;" in _hb and "<script>alert(9)" not in _hb
      and "READ-BODY" not in _hb)
check("inbox: 別のセッションの本文は開けない", "OTHER-BODY" not in cl.get("/inbox?held=3")[2] and "見つかりません" in cl.get("/inbox?held=3")[2])
check("inbox: 不正な held は無視(見つからない)", all(cl.get("/inbox?held=" + v)[0] == 200 for v in ("x", "-1", "1%27")) and "見つかりません" in cl.get("/inbox?held=abc")[2])
check("inbox: 欄に操作フォームを置かない", "<form" not in _rm and "<form" not in _hd)
_it = [it for it in json.loads(cl.get("/api/notifications")[2])["items"] if it["id"].startswith("rm:")]
check("通知: 急ぎ(期限切れ・今日・明日)の項目だけ緊急通知に出る", [it["id"] for it in _it] == ["rm:due:T-1:2026-11-05"] and _it[0]["href"] == "/tasks/T-1", str(_it))
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.executescript("DELETE FROM inbox; DELETE FROM notify_seen WHERE key='reminders:last';")
_c.commit(); _c.close()
time.sleep(3.2)
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
# 通知の規則(notifyrules.sh)で届け先に webui を書いたチケットの通知: 直近 24 時間・webui を含むもの・このセッションだけ
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_ne = "INSERT INTO notify_events (herdr_session,type,ref,text,hash,sinks,created_at) VALUES (?,?,?,?,'h',?,?)"
_now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
_c.executemany(_ne, [("default", "ticket", "E-1", "[チケット] T-web-012 ログイン <b>x</b> — ラベル +notify / high / bug / 担当 -", "file webui", _now),
                     ("default", "ticket", "E-2", "[チケット] T-web-013 slack だけ", "file slack", _now),
                     ("default", "ticket", "E-3", "[チケット] T-web-014 古い", "webui", "2000-01-01T00:00:00Z"),
                     ("other", "ticket", "E-4", "[チケット] T-web-015 別セッション", "webui", _now),
                     ("default", "question", "C-1", "[質問] webui の文字があっても種別違い", "webui", _now)])
_c.commit(); _c.close()
time.sleep(3.2)
_it = [it for it in json.loads(cl.get("/api/notifications")[2])["items"] if it["id"].startswith("n:")]
check("規則の通知: webui を含む直近のチケットの通知だけ", [it["href"] for it in _it] == ["/tasks/T-web-012"] and _it[0]["kind"] == "ticket_rule", str(_it))
check("規則の通知: /notifications でエスケープ", "ログイン &lt;b&gt;x&lt;/b&gt;" in cl.get("/notifications")[2])
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("DELETE FROM notify_events WHERE type IN ('ticket','question')")
_c.commit(); _c.close()
time.sleep(3.2)
r = cl.get("/notify.js")
check("/notify.js が 200 の JavaScript", r[0] == 200 and r[1].get("Content-Type", "").startswith("text/javascript")
      and "Notification" in r[2] and "/api/notifications" in r[2], str(r[:2]))
check("/notify.js は innerHTML を使わない", "innerHTML" not in r[2] and "eval(" not in r[2])
check("未ログインの /notify.js は /login へ", Client(port).get("/notify.js")[0] == 302)
# アラートはチケット(020): project alert のチケットとして一覧に出る。解析中は担当が解析役、解析済みは review、優先度は緊急度から
_at = cl.get("/tasks?project=alert")[2]
check("アラート: チケットとして一覧に出る(T-alert-NNN)", sorted(set(re.findall(r'href="/tasks/(T-alert-\d+)"', _at))) == ["T-alert-001", "T-alert-002", "T-alert-003", "T-alert-004"], str(re.findall(r'href="/tasks/(T-alert-\d+)"', _at)))
_ac = sqlite3.connect(env["WEBUI_TB_DB"])
check("アラート: 段階と緊急度がチケットの状態・優先度に写る",
      _ac.execute("SELECT group_concat(status||'/'||priority, ' ') FROM (SELECT t.status, t.priority FROM alert_details d JOIN tasks t ON t.id=d.task_id ORDER BY d.alert_no)").fetchone()[0]
      == "review/highest review/low todo/high in_progress/high")
# 後のタスク一覧の件数を変えないよう、ここで片付ける(ビューの DELETE はチケットも消す)
_ac.execute("DELETE FROM alerts"); _ac.commit()
check("アラート: ビューを消すとチケットも消える", _ac.execute("SELECT COUNT(*) FROM tasks WHERE project='alert'").fetchone()[0] == 0)
_ac.close()
rf = cl.get("/refresh.js")
check("/refresh.js が 200 の JavaScript。見ていないタブでは読み直さず、読み直しは GET",
      rf[0] == 200 and rf[1].get("Content-Type", "").startswith("text/javascript") and "document.hidden" in rf[2]
      and "visibilitychange" in rf[2] and "location.replace(location.href)" in rf[2] and "reload(" not in rf[2]
      and "innerHTML" not in rf[2] and "eval(" not in rf[2], str(rf[:2]))
check("未ログインの /refresh.js は /login へ", Client(port).get("/refresh.js")[0] == 302)
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
check("タスク: 状態の件数サマリ", all(x in TL[2] for x in ("todo 1", "in_progress 2", "review 0", "done 1", "blocked 1", "すべて 5", "未完了 4", "閉じたもの 1")), "")
check("タスク: 既定は未完了だけ(done の T-4 は出ない)", sorted(_ids("/tasks")) == ["T-1", "T-2", "T-3", "T-5"], str(_ids("/tasks")))
check("タスク: view=all で 5 件すべて", sorted(_ids("/tasks?view=all")) == ["T-1", "T-2", "T-3", "T-4", "T-5"])
check("タスク: view=closed で閉じたものだけ", _ids("/tasks?view=closed") == ["T-4"])
check("タスク: 状態の複数選択", sorted(_ids("/tasks?status=done&status=blocked")) == ["T-4", "T-5"])
check("タスク: 状態を選ぶと表示より優先", _ids("/tasks?view=closed&status=blocked") == ["T-5"])
check("タスク: キーワード(ID・題の部分一致)", _ids("/tasks?q=%E5%8F%A4%E3%81%84") == ["T-5"] and _ids("/tasks?q=T-3") == ["T-3"]
      and _ids("/tasks?q=%25") == [] and _ids("/tasks?view=all&q=TITLETAIL") == ["T-4"])
check("タスク: 担当の未割り当て(~)。担当済みだけなら空", _ids("/tasks?owner=~") == [])
_cx.execute(_t_ins, ("T-6", "pj", "担当なし", None, "todo", None, None, _now_iso, _now_iso))
_cx.execute(_t_ins, ("T-7", "pj", "担当が空", "", "todo", None, None, _now_iso, _now_iso))
_cx.commit()
check("タスク: 担当の未割り当て(~)は NULL と空文字の両方", sorted(_ids("/tasks?owner=~")) == ["T-6", "T-7"], str(_ids("/tasks?owner=~")))
_cx.execute("DELETE FROM tasks WHERE id IN ('T-6','T-7')")
_cx.commit()
_tc = cl.get("/tasks?q=T-3&status=in_progress&owner=alpha")[2]
check("タスク: 今の条件のチップ(外すリンク)", 'class=chip' in _tc and 'href="/tasks?owner=alpha&amp;status=in_progress"' in _tc
      and "キーワード: T-3 ×" in _tc and 'src="/filter.js"' in _tc)
check("タスク: 不正な表示は 200 でエラー表示", "class=err" in cl.get("/tasks?view=zz")[2])
_fj = cl.get("/filter.js")
check("/filter.js が 200 の JavaScript", _fj[0] == 200 and _fj[1].get("Content-Type", "").startswith("text/javascript") and "tfilter" in _fj[2]
      and "innerHTML" not in _fj[2])
check("タスク: PJ で絞る(件数サマリも PJ 内)", sorted(_ids("/tasks?project=pj2&view=all")) == ["T-3", "T-4", "T-5"] and "in_progress 1" in cl.get("/tasks?project=pj2")[2])
check("タスク: 担当で絞る", sorted(_ids("/tasks?owner=alpha")) == ["T-1", "T-3"] and sorted(_ids("/tasks?owner=alpha&view=all")) == ["T-1", "T-3", "T-4"])
check("タスク: 担当 + 状態", _ids("/tasks?owner=alpha&status=done") == ["T-4"])
check("タスク: 担当 + PJ + 状態", _ids("/tasks?owner=alpha&project=pj2&status=in_progress") == ["T-3"])
check("タスク: 未報告だけ", sorted(_ids("/tasks?unreported=1")) == ["T-1", "T-2", "T-5"])
check("タスク: 未報告 + 担当", _ids("/tasks?unreported=1&owner=alpha") == ["T-1"])
check("タスク: 停滞だけ(in_progress で更新が古いもの)", _ids("/tasks?stale=1") == ["T-1"])
_t1row = re.search(r'<tr><td><a href="/tasks/T-1">.*?</tr>', TL[2], re.S)
check("タスク: 停滞の目印は T-1 の行だけ", bool(_t1row) and "停滞 " in _t1row.group(0) and TL[2].count("停滞 ") == 1 and _ids("/tasks?stale=1&owner=beta") == [])
check("タスク: 状態の件数が停滞の絞り込みを反映しない旨の注記", "「停滞だけ」は反映しません" in TL[2])
check("タスク: 並び替え(更新が古い順 / ID 順)", _ids("/tasks?sort=oldest")[:2] == ["T-5", "T-1"] and _ids("/tasks?sort=id&view=all") == ["T-1", "T-2", "T-3", "T-4", "T-5"])
check("タスク: 親子の表示(親リンクと子の件数)", '<a href="/tasks/T-2">T-2</a>' in TL[2] and "子 2" in TL[2])
check("タスク: 報告の種別ごとの件数と未対応", "progress 2" in TL[2] and "question 1" in TL[2] and "未対応 1" in TL[2])
_TLa = cl.get("/tasks?view=all")[2]
check("タスク: 題は先頭 80 文字だけ", "TITLETAIL" not in _TLa and ("x" * 80) in _TLa and ("x" * 100) not in _TLa)
check("タスク: 題の HTML はエスケープ", XSS not in TL[2] and "&lt;script&gt;" in TL[2])
_th = TL[2].split("</form>", 1)[1].split("</tr>")[0]
check("タスク: 種別・優先度・期日・ラベル・作成の列", all(x in _th for x in ("種別", "優先度", "期日", "ラベル", "作成", "更新")))
# 期日(015): 期限切れ / 期限間近の絞り込みと境界(昨日・今日・明日)。期日なし・done は出ない
_today = datetime.now()
_d = lambda n: (_today + timedelta(days=n)).strftime("%Y-%m-%d")
_cx.execute("UPDATE tasks SET due=? WHERE id='T-1'", (_d(-1),))
_cx.execute("UPDATE tasks SET due=? WHERE id='T-2'", (_d(0),))
_cx.execute("UPDATE tasks SET due=? WHERE id='T-3'", (_d(1),))
_cx.execute("UPDATE tasks SET due=? WHERE id='T-4'", (_d(-5),))   # done なので期限切れにならない
_cx.commit()
check("タスク: 期限切れ(昨日だけ。done と期日なしは出ない)", _ids("/tasks?due=overdue") == ["T-1"], str(_ids("/tasks?due=overdue")))
check("タスク: 期限間近 0 日は今日だけ", _ids("/tasks?due=soon&days=0") == ["T-2"], str(_ids("/tasks?due=soon&days=0")))
check("タスク: 期限間近 1 日は今日と明日", sorted(_ids("/tasks?due=soon&days=1")) == ["T-2", "T-3"])
check("タスク: 期限間近の既定(3 日)", sorted(_ids("/tasks?due=soon")) == ["T-2", "T-3"])
check("タスク: 期日の近い順(期日なしは最後)", _ids("/tasks?sort=due&view=all") == ["T-4", "T-1", "T-2", "T-3", "T-5"], str(_ids("/tasks?sort=due&view=all")))
_TLd = cl.get("/tasks?view=all")[2]
_t1row = re.search(r'<tr><td><a href="/tasks/T-1">.*?</tr>', _TLd, re.S).group(0)
_t4row = re.search(r'<tr><td><a href="/tasks/T-4">.*?</tr>', _TLd, re.S).group(0)
check("タスク: 期限切れの目印と件数", "期限切れ</span>" in _t1row and "期限切れ" not in _t4row and "期限切れ 1</span>" in _TLd and "期限間近(3 日以内)2" in _TLd)
check("タスク詳細: 期日", "<th>期日</th><td><span class=warn>" + _d(1) + " 間近" in cl.get("/tasks/T-3")[2])
for bad in ["due=zz", "due=soon&days=-1", "due=soon&days=9999", "days=%27%3B--"]:
    r = cl.get("/tasks?" + bad)
    check(f"タスク: 不正な期日の絞り込み {bad} でも 200 でエラー表示", r[0] == 200 and "class=err" in r[2] and "'--" not in r[2], str(r[0]))
_cx.execute("UPDATE tasks SET due=NULL")
_cx.commit()
check("タスク: 期日が無ければ期限の件数は出ない", "期限切れ 0" not in cl.get("/tasks")[2] and _ids("/tasks?due=overdue") == [])
# チケットの構造(014): 種別・優先度・ラベルで絞る、ラベルは押すと絞り込み、優先度順
_cx.execute("UPDATE tasks SET type='epic', priority='high', labels='[\"ui\",\"auth\"]' WHERE id='T-2'")
_cx.execute("UPDATE tasks SET type='story', priority='highest', labels='[\"ui\"]' WHERE id='T-3'")
_cx.execute("INSERT INTO task_links (from_id, link_type, to_id, created_by, created_at, herdr_session) VALUES ('T-5','blocks','T-3','human','t','default')")
_cx.commit()
check("タスク: 種別で絞る", _ids("/tasks?type=epic") == ["T-2"])
check("タスク: 優先度で絞る", _ids("/tasks?priority=highest") == ["T-3"])
check("タスク: ラベルで絞る", sorted(_ids("/tasks?label=ui")) == ["T-2", "T-3"] and _ids("/tasks?label=auth") == ["T-2"])
check("タスク: ラベル + 担当", _ids("/tasks?label=ui&owner=alpha") == ["T-3"])
check("タスク: ラベルは絞り込みのリンク", 'href="/tasks?label=ui"' in cl.get("/tasks")[2])
check("タスク: 優先度順", _ids("/tasks?sort=priority")[:2] == ["T-3", "T-2"])
# 用意したラベル(設定 labels)は、まだ使われていなくても絞り込みの選択肢に先に出る
with open(env["TB_LABELS"], "w") as _f:
    _f.write("# 用意したラベル\nunused-yet = まだ誰も付けていない\nui\n")
_tl = cl.get("/tasks")[2]
check("タスク: 用意したラベルが選択肢に(書いた順に先、使われているものはその後)",
      '<option value="unused-yet">' in _tl and _tl.index('value="unused-yet"') < _tl.index('<option value="ui">') < _tl.index('<option value="auth">')
      and _tl.count('<option value="ui">') == 1)
os.remove(env["TB_LABELS"])
check("タスク: 設定を消せば使われているラベルだけ", 'value="unused-yet"' not in cl.get("/tasks")[2])
for bad in ["type=zz", "priority=x", "label=a%20b", "label=%27%3B--"]:
    r = cl.get("/tasks?" + bad)
    check(f"タスク: 不正な絞り込み {bad} でも 200 でエラー表示", r[0] == 200 and "class=err" in r[2] and "'--" not in r[2], str(r[0]))
_TD3 = cl.get("/tasks/T-3")[2]
check("タスク詳細: 種別・優先度・ラベル・リンク", "<th>種別</th><td>story</td>" in _TD3 and 'href="/tasks?label=ui"' in _TD3
      and "<h2>リンク</h2>" in _TD3 and "blocked by" in _TD3 and 'href="/tasks/T-5"' in _TD3)
check("タスク詳細: リンクは相手側にも出る", "<h2>リンク</h2>" in cl.get("/tasks/T-5")[2] and "blocks" in cl.get("/tasks/T-5")[2])
_cx.execute("UPDATE tasks SET refs=? WHERE id='T-3'", (json.dumps([{"kind": "slack", "url": "https://acme.slack.com/archives/C1/p1"},
                                                                  {"kind": "x", "url": "javascript:alert(1)"}]),))
_cx.commit()
_TD3 = cl.get("/tasks/T-3")[2]
check("タスク詳細: 外部リンク(http(s) だけをリンクに)", 'href="https://acme.slack.com/archives/C1/p1"' in _TD3 and 'rel="noopener noreferrer"' in _TD3
      and 'href="javascript' not in _TD3 and "javascript:alert(1)" in _TD3)
# スプリントとボード(019)
_cx.execute("INSERT INTO sprints (id, project, name, goal, start_date, end_date, state, created_at, herdr_session) VALUES "
            "(7, 'pj2', '第1スプリント " + XSS + "', 'ゴール', '2026-11-17', '2026-11-30', 'active', 't', 'default')")
_cx.execute("INSERT INTO sprints (id, project, name, state, created_at, herdr_session) VALUES (8, 'pj2', '次', 'planned', 't', 'default')")
_cx.execute("UPDATE tasks SET sprint_id=7, points=3 WHERE id='T-3'")
_cx.execute("UPDATE tasks SET sprint_id=7, points=5 WHERE id='T-4'")
_cx.execute("UPDATE tasks SET sprint_id=8 WHERE id='T-5'")
_cx.commit()
B = cl.get("/board")
check("ボード: 200・ナビに出る・自動更新しない", B[0] == 200 and 'href="/board"' in B[2] and 'http-equiv="refresh"' not in B[2])
check("ボード: 既定は active なスプリント(S-7)で、状態の列にカード", "<h2>S-7 " in B[2] and "in_progress (1)" in B[2] and "done (1)" in B[2]
      and 'href="/tasks/T-3"' in B[2] and 'href="/tasks/T-5"' not in B[2].split("<h2>スプリント一覧</h2>")[0])
check("ボード: 完了とポイントの集計", "完了 1 / 2 件・ポイント 5 / 8" in B[2])
check("ボード: スプリント名はエスケープ", XSS not in B[2] and "&lt;script&gt;" in B[2])
B8 = cl.get("/board?sprint=S-8")[2]
check("ボード: スプリントを選ぶ", "<h2>S-8 " in B8 and 'href="/tasks/T-5"' in B8.split("<h2>スプリント一覧</h2>")[0])
check("ボード: ラベルで絞る", 'href="/tasks/T-3"' in cl.get("/board?sprint=S-7&label=ui")[2] and
      'href="/tasks/T-4"' not in cl.get("/board?sprint=S-7&label=ui")[2].split("<h2>スプリント一覧</h2>")[0])
for bad in ["sprint=zz", "sprint=S-999", "label=a%20b", "project=%27%3B--"]:
    r = cl.get("/board?" + bad)
    check(f"ボード: 不正な指定 {bad} でも 200", r[0] == 200 and "'--" not in r[2], str(r[0]))
check("タスク詳細: スプリントとポイント", 'href="/board?sprint=S-7"' in cl.get("/tasks/T-3")[2] and "バックログ" in cl.get("/tasks/T-1")[2])
check("ボード: POST は 405", cl.raw("POST", "/board", {"a": "b"})[0] == 405)
_cx.execute("UPDATE tasks SET sprint_id=NULL, points=NULL")
_cx.execute("DELETE FROM sprints")
# 履歴(018 の task_events): 新しい順、エスケープ、別セッションの行は出ない、行が無ければ欄も出ない
check("タスク詳細: 履歴が無ければ欄は出ない", "<h2>履歴</h2>" not in _TD3)
_cx.executemany("INSERT INTO task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at) VALUES (?,?,?,?,?,?,?,?)",
                [("default", "T-3", "created", None, "todo", "human", None, "2026-01-01T00:00:00Z"),
                 ("default", "T-3", "label_added", None, "<b>x</b>", "worker1", None, "2026-01-01T00:00:01Z"),
                 ("other", "T-3", "status", "todo", "zz-other-session", "human", None, "2026-01-01T00:00:02Z")])
_cx.commit()
_TD3 = cl.get("/tasks/T-3")[2]
check("タスク詳細: 履歴(新しい順・エスケープ)", "<h2>履歴</h2>" in _TD3 and "&lt;b&gt;x&lt;/b&gt;" in _TD3 and "<b>x</b>" not in _TD3
      and _TD3.index("label_added") < _TD3.index("created</td>"))
check("タスク詳細: 別セッションの履歴は出ない", "zz-other-session" not in _TD3)
_cx.execute("DELETE FROM task_events")
_cx.execute("UPDATE tasks SET type='task', priority='medium', labels='[]', refs='[]'")
_cx.execute("DELETE FROM task_links")
_cx.commit()
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
_BIG = cl.get("/tasks?view=all")[2]
check("タスク(300 件超): 「N 件のうち 300 件だけ表示」と明示する", "327 件のうち 300 件だけ表示しています" in _BIG, re.findall(r"\d+ 件のうち[^<]*", _BIG)[:1].__str__())
_big_ids = _ids("/tasks?view=all")
check("タスク(300 件超): 既定の並びは 300 行で、blocked・in_progress が後ろの PJ でも出る", len(_big_ids) == 300 and "B-1" in _big_ids and "P-1" in _big_ids, str(len(_big_ids)))
check("タスク(300 件超): 未解決の報告があるタスクが先頭、次に blocked → in_progress → done", set(_big_ids[:2]) == {"T-1", "T-2"} and _big_ids.index("B-1") < _big_ids.index("P-1") < _big_ids.index("D-000"), str(_big_ids[:6]))
check("タスク(300 件超): 従来の PJ 順だと切り詰めで隠れる(注記は出る)", "B-1" not in _ids("/tasks?sort=project&view=all") and "件のうち 300 件だけ表示" in cl.get("/tasks?sort=project&view=all")[2])
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

_AP = env["WEBUI_POLICY"]
_m0 = os.stat(_AP).st_mtime_ns
P = cl.get("/perms")
check("権限: 200", P[0] == 200 and "policy" in P[2] and "approvals.conf" not in P[2])
rows = re.findall(r"<tr><td>(\d+)</td><td><code[^>]*>([^<]*)</code></td><td>(?:<b[^>]*>)?(allow|ask|ask|deny)", P[2])
check("権限: 番号・主体 操作 対象・効果", [(a, b, c) for a, b, c in rows][:4] == [("1", "* send:info *", "allow"), ("2", "* send:task dd-*", "allow"), ("3", "* send:task *", "ask"), ("4", "* agent:* *", "allow")], str(rows[:5]))
check("権限: 行末の注記と変更履歴を併記", "2026-11-01T00:00:00Z に人が変更" in P[2] and "履歴: 2026-11-02T00:00:00Z に allow" in P[2])
check("権限: 一致の例", "<code>send:info:worker1</code>" in P[2] and "<code>agent:new:worker1</code>" in P[2])
check("権限: 広い allow(agent:*)に警告", "広い allow" in P[2])
check("権限: 対象を絞らない allow はワイルドカード注意", "ワイルドカードの allow" in P[2])
check("権限: もっと厳しい行が重なる allow を指摘(厳しい方が勝つ)", "もっと厳しい 3 行目" in P[2] and "もっと厳しい 5 行目" in P[2])
check("権限: 変更履歴の節", "変更履歴(直近 50 件)" in P[2] and "誰が" in P[2])
check("権限: 行のパターンの HTML はエスケープ", "<b>bold</b>" not in P[2] and "&lt;b&gt;bold" in P[2])
check("権限: 変更ボタンも POST フォームも無い(ログアウトだけ)", P[2].count("method=post") == 1 and 'action="/logout"' in P[2] and "<button" in P[2])
S1 = cl.get("/perms?op=send:task:dd-x")[2]
check("権限: 検索 send:task:dd-x は厳しい 3 行目で ask(2 行目の allow より勝つ)", "3 行目" in S1 and "<b class=\"\">ask</b>" in S1, S1[S1.find("→"):S1.find("→") + 200])
check("権限: 検索 send:task:w1 は 3 行目で ask", "3 行目" in cl.get("/perms?op=send:task:w1")[2])
check("権限: 検索 send:info:w1 は 1 行目で allow", "1 行目" in cl.get("/perms?op=send:info:w1")[2])
check("権限: 検索 zzz:q は当たる行が無く既定の表", "既定の表" in cl.get("/perms?op=zzz:q")[2] and "<b>ask</b>" in cl.get("/perms?op=zzz:q")[2])
check("権限: 主体を絞った行(@lead)は主体 * の判定に使わない", "9 行目" not in cl.get("/perms?op=send:task:w1")[2].split("ルール一覧")[0])
check("権限: 不正な操作名は拒否", "使えない文字" in cl.get("/perms?op=a%20b%3Bc")[2] and cl.get("/perms?op=a%20b")[0] == 200)
C = cl.get("/perms?pat=send:task:w1&val=allow")[2]
check("権限: tb approval allow のコマンド行を表示するだけ", "<pre>tb approval allow send:task:w1</pre>" in C and "自分の端末" in C and "* send:task w1 = allow" in C)
check("権限: もっと厳しい行が重なるなら、allow にならないと言う", "もっと厳しい 3 行目" in C.split("変更したいとき")[1])
check("権限: * への allow に強い警告", "全体を allow にします" in cl.get("/perms?pat=*&val=allow")[2])
check("権限: 値・パターンが不正ならコマンドを出さない", "<pre>tb" not in cl.get("/perms?pat=a%3Bb&val=allow")[2] and "<pre>tb" not in cl.get("/perms?pat=ab&val=rm")[2])
check("権限: シェルの特殊文字を含むパターンは出さない", "<pre>tb" not in cl.get("/perms?pat=%24%28id%29&val=ask")[2])
check("権限: policy は GET の前後で変わらない(mtime)", os.stat(_AP).st_mtime_ns == _m0)
check("権限: 先頭が - の操作名・パターンは不正", "使えない文字" in cl.get("/perms?op=-x")[2] and "<pre>tb" not in cl.get("/perms?pat=-x&val=allow")[2])
check("権限: POST は 405", cl.raw("POST", "/perms", {"a": "b"})[0] == 405)
check("権限: WEBUI_POLICY で表示用のファイルを指定していれば policy は編集しない", "WEBUI_POLICY で表示用" in cl.get("/perms")[2] and 'action="/policy"' not in cl.get("/perms")[2])
check("権限: 自動更新しない", 'http-equiv="refresh"' not in P[2])
_un = Client(port)
for _pth in ["/ledger", "/perms", "/perms?op=send:task:x", "/ledger?result=all"]:
    _r = _un.get(_pth)
    check(f"未ログインの {_pth} は /login へ(本文なし)", _r[0] == 302 and _r[1].get("Location", "").startswith("/login") and "send:" not in _r[2] and "approvals" not in _r[2])
check("job が出る", "job1" in cl.get("/jobs")[2])
check("上のバーは job", '<a href="/jobs">job</a>' in cl.get("/ledger")[2])
check("呼ばれたときだけの job: 一覧と詳細", "呼ばれたときだけ(tb job run)" in cl.get("/jobs")[2] and "/v1/jobs/pman/run" in cl.get("/jobs/pman")[2])
check("1 回だけの job: 一覧に日時", "1 回だけ 2099-01-02 03:04" in cl.get("/jobs")[2])
_po = cl.get("/jobs/ponce")[2]
check("1 回だけの job: 詳細は日時と次回、間隔のフォームは出さない", "1 回だけ <b>2099-01-02 03:04</b>" in _po and "2099-01-02 03:04:00" in _po and "name=interval " not in _po, _po[:400])

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
check("ジョブ詳細: 承認の扱い(scheduler は policy を見ない)", "policy を見ず" in _pjh and "send:task:alpha" in _pjh)
check("ジョブ詳細: 副作用は推測せず「不明」", "分かりません(不明)" in _pjh)
check("ジョブ詳細: 送信履歴へのリンク", 'href="/ledger?kind=scheduler&amp;target=alpha"' in _pjh)
check("ジョブ詳細: 編集フォームは増えていない(保存・戻す・今すぐ流す・消すの 4 つだけ)", _pjh.count('<form method=post action="/jobs/pj"') == 4 and "name=prompt" not in _pjh and "name=command" not in _pjh and "name=target" not in _pjh)
_pcm = cl.get("/jobs/pcomment")[2]
check("ジョブ詳細: コメントアウトされた PROMPT は読まない", "REAL-PROMPT" in _pcm and "ignored-line" not in _pcm)
for _n, _body in (("pdeny1", "DENIED-KEY-BODY"), ("pdeny2", "DENIED-ENV-BODY"), ("pdeny3", "DENIED-KEY-BODY")):
    _d = cl.get("/jobs/" + _n)[2]
    check(f"ジョブ詳細: PROMPT_FILE が認証情報の置き場({_n})なら中身を出さない", _body not in _d and "認証情報などの置き場なので、中身は表示しません" in _d, _d[:0])
_vv = cl.get("/jobs/pvar")[2]
# 展開($ や `)を含む conf は scheduler(herdrctl sched)が読まない(動かない)。詳細はその理由を出す
check("ジョブ詳細: PROMPT_FILE=$HOME/x の conf は scheduler が読めない理由を出す(フォームは消すものだけ)", "展開($ や ` や ~)は使えない" in _vv and "相対パスなので" not in _vv and _vv.count('action="/jobs/pvar"') == 1 and "value=rm>" in _vv and "value=save>" not in _vv)
_cs = cl.get("/jobs/cmdsub")[2]
check("ジョブ詳細: COMMAND=$(…) の conf は scheduler が読めない理由を出す(コマンドは実行しない。フォームは消すものだけ)", "展開($ や ` や ~)は使えない" in _cs and _cs.count('action="/jobs/cmdsub"') == 1 and "value=rm>" in _cs and "value=save>" not in _cs)
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
check("ジョブ詳細: COMMAND 型は宛先を出さない・承認の対象外と書く", "誰に送られるか" not in _c and "policy の対象外" in _c)
_pr = cl.get("/jobs/prules")[2]
check("ジョブ詳細: PROMPT_RULES / PROMPT_VARS を見せる(エスケープ・マスク・既定なし)", "時刻での切り替え" in _pr and "&lt;b&gt;本文&lt;/b&gt;" in _pr and "半の本文" in _pr
      and "差し込み" in _pr and "supersecretvalue2" not in _pr and "既定の本文はありません" in _pr)
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
_c = sqlite3.connect(env["WEBUI_TB_DB"]); _c.execute("DELETE FROM config WHERE key='sched-settings:pj'"); _c.commit(); _c.close()
# ---- 権限カタログ × policy(読み取りのみ)
check("カタログ: 操作ごとの現在の権限の節", "操作ごとの現在の権限(カタログ × policy)" in P[2])
def _crow(html, pat):
    m = re.search(r"<tr><td>(?:<span class=note>)?<code>" + re.escape(pat) + r"</code>.*?</tr>", html, re.S)
    return m.group(0) if m else ""
_t = _crow(P[2], "send:task:*")
check("カタログ: send:task:* は 3 行目で ask(明示)、主体・宛先を絞った行は例外に出る",
      "3 行目" in _t and ">ask<" in _t and "明示" in _t and "* send:task dd-* = allow" in _t and "@lead send:task ~child = allow" in _t, _t[:400])
_n = _crow(P[2], "agent:new:*")
check("カタログ: agent:new:*(high)が agent:* = allow で allow なら警告", "<b class=warn>allow</b>" in _n and "リスク high" in _n and "4 行目" in _n, _n[:300])
check("カタログ: mid の操作が allow でも警告しない", "リスク mid" not in _crow(P[2], "agent:compact:*") and "<b class=warn>allow</b>" in _crow(P[2], "agent:compact:*"))
_pr = _crow(P[2], "proxy:read-only")
check("カタログ: 規則が無い操作は既定の表に落ちる(proxy:read-only は allow)", "既定に落ちる" in _pr and "perms/ops.tbl の allow" in _pr, _pr[:300])
check("カタログ: unwired(proxy:*)は薄く表示し、承認判定に渡らないと警告", "<span class=note><code>proxy:read-only</code>" in _pr and "承認判定に渡らない" in _pr and "unwired" in _pr, _pr[:300])
_U = cl.get("/perms?state=unwired")[2].split("ルール一覧")[0]
check("カタログ: 状態 unwired で絞る", all(x in _U for x in ("proxy:read-only", "proxy:write", "proxy:external")) and "agent:new:*" not in _U)
check("カタログ: role:change は conditional ではなく no", "<code>role:change</code>" in P[2] and "conditional" not in _crow(P[2], "role:change"))
_g = _crow(P[2], "gate:merge")
check("カタログ: reserved は薄く表示し、リスク階層・副作用・allow 候補・状態を出す",
      "<span class=note><code>gate:merge</code>" in _g and "human-only" in _g and "external-write" in _g and "reserved" in _g, _g[:300])
check("カタログ: 説明・副作用の列が出る", "宛先を絞った allow だけ" in P[2] and "local-write" in P[2])
check("カタログ: 未知の規則の欄(カタログに無い規則を出し、エスケープ)", "未知の規則(カタログに無い)" in P[2] and "x&lt;b&gt;bold&lt;/b&gt;y" in P[2].split("未知の規則(カタログに無い)")[1].split("変更履歴")[0])
check("カタログ: 既知の規則(send:task dd-* など)は未知に出ない", "send:task dd-*" not in P[2].split("未知の規則(カタログに無い)")[1].split("変更履歴")[0])
_K = cl.get("/perms?kind=gate")[2]
check("カタログ: 種別で絞る", "gate:merge" in _K.split("ルール一覧")[0] and "agent:new:*" not in _K.split("ルール一覧")[0])
_S = cl.get("/perms?state=reserved")[2].split("ルール一覧")[0]
check("カタログ: 状態で絞る", "gate:execute" in _S and "send:task:*" not in _S)
_F = cl.get("/perms?fallback=1")[2].split("ルール一覧")[0]
check("カタログ: 既定に落ちているものだけ", "proxy:read-only" in _F and "send:task:*" not in _F)
_A = cl.get("/perms?allowed=1")[2].split("ルール一覧")[0]
check("カタログ: allow になっているものだけ", "agent:new:*" in _A and "setup:hooks" not in _A and "<code>send:task:*</code>" not in _A)
check("カタログ: 不正な絞り込み値は無視して 200", cl.get("/perms?kind=zzz&state=%27")[0] == 200)
check("カタログ: 場所は固定で env で差し替える口が無い", "WEBUI_CATALOG" not in open(os.path.join(REPO, "internal", "webui", "perms.go"), encoding="utf-8").read())
check("カタログ: GET だけ(絞り込みも検索も method=post を増やさない)", cl.get("/perms?kind=gate")[2].count("method=post") == 1)

# conf を一時的に差し替えて境界を確かめる(読み取りだけの画面なので、戻せば元の表示に戻る)
_orig = open(_AP, encoding="utf-8").read()
open(_AP, "w", encoding="utf-8").write("* send:task [[:alpha:]]* = ask\n* send:task * = allow\n* role:change * = ask\n")
_B = cl.get("/perms")[2]
check("権限: [ を含む行に警告を出す", "[ を含むパターン" in _B)
_R = cl.get("/perms?op=send:task:w1")[2]
check("カタログ: [ を含む行が先頭なら、現在の値も判定不能(下の行で決めない)", "判定不能" in _B.split("ルール一覧")[0])
check("権限: [ の行に当たる操作は判定不能(allow と決めつけない)", "判定不能" in _R and "<b class=\"warn\">allow</b>" not in _R and "→ <b>ask</b>" not in _R)
check("権限: [ の行より下の行の一致の例は出さない(決めつけない)", "<code>send:task:worker1</code>" not in _B)
check("権限: [ の行が先頭にあれば、どの操作もそこで止まる(下の行を使わない)", "判定不能" in cl.get("/perms?op=role:change")[2])
open(_AP, "w", encoding="utf-8").write("* gate:merge * = allow\n* send:task * = ask\n* send:task dd-* = allow\n* proxy:write * = allow\n* proxy:read-only * = allow\n")
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
SET = os.path.join(env["WEBUI_SCHED_SETTINGS"], "job1.settings")   # DB に移す前の置き場所。Web UI はもう書かない


def dbset(job="job1"):
    """DB の sched-settings:<job>(このセッションの行)の本文。無ければ ""。"""
    c = sqlite3.connect(env["WEBUI_TB_DB"])
    try:
        r = c.execute("SELECT body FROM config WHERE key=? AND herdr_session='default'", ("sched-settings:" + job,)).fetchone()
    finally:
        c.close()
    return r[0] if r else ""


check("一覧に実際の間隔が出る", "10 分おき / 09:00-21:00 / daily" in cl.get("/jobs")[2])
r = cl.get("/jobs/job1")
check("設定フォームが出る(自動更新しない)", r[0] == 200 and 'name=interval ' in r[2] and 'name=interval_unit' in r[2] and 'http-equiv="refresh"' not in r[2])
check("未知のジョブは 404", cl.get("/jobs/nope")[0] == 404 and cl.raw("POST", "/jobs/nope", {"csrf": cl.csrf("/jobs/job1")})[0] == 404)
good = {"action": "save", "interval_min": "15", "start": "10:00", "end": "18:00", "days": "weekdays", "enabled": "1"}
check("CSRF 無しの設定 POST は拒否", cl.raw("POST", "/jobs/job1", good)[0] == 403 and not dbset())
check("Origin 不一致の設定 POST は拒否",
      cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/jobs/job1")}, origin="http://evil.example")[0] == 403)
check("未ログインの設定 POST は /login へ", Client(port).raw("POST", "/jobs/job1", good)[0] == 302 and not dbset())
check("ログイン画面の CSRF(anon)は使えない", cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/login")})[0] == 403)
r = cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/jobs/job1")})
check("保存すると 302 で戻る", r[0] == 302 and r[1].get("Location") == "/jobs/job1?saved=1", str(r[:2]))
body = dbset()
check("上書き設定は DB に書かれる(ファイルは作らない)", "INTERVAL_MIN=15" in body and "DAYS=weekdays" in body and "ENABLED=1" in body
      and not os.path.exists(SET), body)
check("一覧に反映", "15 分おき / 10:00-18:00 / weekdays" in cl.get("/jobs")[2])
for bad, label in ((dict(interval_min="0"), "間隔 0"), (dict(interval_min="5; rm"), "間隔に記号"),
                   (dict(start="25:00"), "開始が範囲外"), (dict(start="19:00"), "開始が終了より後"),
                   (dict(days="$(id)"), "曜日が不正"), (dict(days="custom"), "曜日を 1 つも選ばない"),
                   (dict(start="10:00\nCOMMAND=x"), "改行の注入")):
    r = cl.raw("POST", "/jobs/job1", {**good, **bad, "csrf": cl.csrf("/jobs/job1")})
    check(f"不正な入力は 400 で書かない: {label}", r[0] == 400 and dbset() == body, str(r[0]))
r = cl.raw("POST", "/jobs/job1", {**good, "days": "custom", "day_mon": "1", "day_fri": "1", "enabled": "",
                                  "csrf": cl.csrf("/jobs/job1")})
check("曜日指定と一時停止", r[0] == 302 and "DAYS=Mon Fri" in dbset() and "ENABLED=0" in dbset())
check("一時停止が一覧に出る", "一時停止" in cl.get("/jobs")[2])
# 秒単位の間隔・時刻
r = cl.raw("POST", "/jobs/job1", {**good, "interval": "30", "interval_unit": "sec", "start": "10:00:15", "end": "10:30",
                                  "csrf": cl.csrf("/jobs/job1")})
sb = dbset()
check("秒の間隔を保存", r[0] == 302 and "INTERVAL_SEC=30" in sb and "INTERVAL_MIN" not in sb and "START=10:00:15" in sb, sb)
check("一覧に秒の間隔", "30 秒おき / 10:00:15-10:30 / weekdays" in cl.get("/jobs")[2])
f = cl.get("/jobs/job1")[2]
check("フォームに秒の値と単位", 'value="30"' in f and "<option value=sec selected>" in f and 'value="10:00:15"' in f)
r = cl.raw("POST", "/jobs/job1", {**good, "interval": "2", "interval_unit": "min", "csrf": cl.csrf("/jobs/job1")})
check("分の間隔で保存し直すと INTERVAL_MIN", r[0] == 302 and "INTERVAL_MIN=2" in dbset()
      and "INTERVAL_SEC" not in dbset())
sb = dbset()
for bad, label in ((dict(interval="86401", interval_unit="sec"), "秒の上限超え"), (dict(interval="5", interval_unit="hour"), "単位が不正"),
                   (dict(start="10:00:60"), "秒が範囲外"), (dict(start="18:00:30", end="18:00:10"), "開始が終了より後(秒)")):
    r = cl.raw("POST", "/jobs/job1", {**good, **bad, "csrf": cl.csrf("/jobs/job1")})
    check(f"不正な入力は 400 で書かない: {label}", r[0] == 400 and dbset() == sb, str(r[0]))
r = cl.raw("POST", "/jobs/job1", {**good, "start": "18:00:30", "end": "18:00", "csrf": cl.csrf("/jobs/job1")})
check("終了 HH:MM はその分の 59 秒までなので 18:00:30-18:00 は可", r[0] == 302, str(r[0]))
r = cl.raw("POST", "/jobs/job1", {"action": "reset", "csrf": cl.csrf("/jobs/job1")})
os.makedirs(os.path.dirname(SET), exist_ok=True)
open(SET, "w").write("INTERVAL_MIN=50\n"); os.chmod(SET, 0o600)   # DB に移す前に Web UI が書いたファイルが残っている
r = cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/jobs/job1")})
check("残ったファイルの settings は保存で消え、DB の値が効く", r[0] == 302 and not os.path.exists(SET) and "INTERVAL_MIN=15" in dbset()
      and "15 分おき / 10:00-18:00" in cl.get("/jobs")[2], str(r[:2]))
open(SET, "w").write("INTERVAL_MIN=50\n"); os.chmod(SET, 0o600)
r = cl.raw("POST", "/jobs/job1", {"action": "reset", "csrf": cl.csrf("/jobs/job1")})
check("conf の値に戻すと DB の上書き設定を消す", r[0] == 302 and not dbset() and not os.path.exists(SET) and "10 分おき" in cl.get("/jobs")[2])
# DB にある定義(tb job。config 表の sched-job:<job>)。一覧・詳細に出て、上書き設定は DB に書く(履歴つき)
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_sess = "default"   # このプロセスのセッション(HERDR_SESSION も herdr-session ファイルも無い)
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES (?, 'sched-job:dbjob', ?, 'human', 'x')",
           (_sess, "# DB-ABOUT 説明\nCOMMAND='echo db'\nWINDOW=09:00-21:00\nINTERVAL_MIN=20\nTZ=Asia/Tokyo\n"))
_c.commit(); _c.close()
check("DB の定義: 一覧に出る(実際の間隔)", "dbjob" in cl.get("/jobs")[2] and "20 分おき / 09:00-21:00 / daily" in cl.get("/jobs")[2])
_dj = cl.get("/jobs/dbjob")
check("DB の定義: 詳細に説明とコマンドが出る", _dj[0] == 200 and "DB-ABOUT 説明" in _dj[2] and "echo db" in _dj[2])
r = cl.raw("POST", "/jobs/dbjob", {**good, "csrf": cl.csrf("/jobs/dbjob")})
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_sb = _c.execute("SELECT body FROM config WHERE key='sched-settings:dbjob'").fetchall()
_hist = _c.execute("SELECT how FROM config_changes WHERE key='sched-settings:dbjob'").fetchall()
_c.close()
check("DB の定義: 上書き設定は DB に(ファイルは作らない)、履歴が残る", r[0] == 302 and _sb and "INTERVAL_MIN=15" in _sb[0][0]
      and not os.path.exists(os.path.join(env["WEBUI_SCHED_SETTINGS"], "dbjob.settings")) and _hist == [("set",)], str((r[:2], _sb, _hist)))
check("DB の定義: 一覧に反映", "15 分おき / 10:00-18:00 / weekdays" in cl.get("/jobs")[2])
r = cl.raw("POST", "/jobs/dbjob", {**good, "start": "25:00", "csrf": cl.csrf("/jobs/dbjob")})
check("DB の定義: 不正な入力は 400 で書かない", r[0] == 400)
r = cl.raw("POST", "/jobs/dbjob", {"action": "reset", "csrf": cl.csrf("/jobs/dbjob")})
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_left = _c.execute("SELECT COUNT(*) FROM config WHERE key='sched-settings:dbjob'").fetchone()[0]
_c.close()
check("DB の定義: 戻すと DB の上書き設定を消す", r[0] == 302 and _left == 0 and "20 分おき" in cl.get("/jobs")[2])
# 今すぐ流す(tb job run)。待たずに戻り、結果は scheduler のログに残る
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES (?, 'sched-job:runjob', ?, 'human', 'x')",
           (_sess, "COMMAND='echo run-now'\nMANUAL=1\n"))
_c.commit(); _c.close()
_runlog = os.path.join(env["WEBUI_SCHED_LOGS"], "runjob.log")
_rp = cl.get("/jobs/runjob")[2]
check("今すぐ流す: 詳細にボタン", "value=run>" in _rp and "今すぐ 1 回流す" in _rp)
check("今すぐ流す: CSRF 無しは拒否", cl.raw("POST", "/jobs/runjob", {"action": "run"})[0] == 403)
r = cl.raw("POST", "/jobs/runjob", {"action": "run", "csrf": cl.csrf("/jobs/runjob")})
check("今すぐ流す: 待たずに詳細へ戻り、動かし始めたと知らせる", r[0] == 302 and r[1].get("Location") == "/jobs/runjob?done=job-run", str(r[:2]))
check("今すぐ流す: 戻った画面に知らせ", "ジョブを動かし始めました" in cl.get("/jobs/runjob?done=job-run")[2])
for _ in range(100):
    if os.path.exists(_runlog) and '"action":"ran"' in open(_runlog).read().replace(" ", ""):
        break
    time.sleep(0.1)
check("今すぐ流す: scheduler のログに ran が残る", os.path.exists(_runlog) and '"action":"ran"' in open(_runlog).read().replace(" ", ""),
      open(_runlog).read() if os.path.exists(_runlog) else "no log")
# ジョブを消す(tb job rm)。印を付けないと消さない。消すと定義・上書き設定の行とログが消え、一覧から外れる(履歴は残る)
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES (?, 'sched-job:rmjob', ?, 'human', 'x')",
           (_sess, "COMMAND='echo rm'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\n"))
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES (?, 'sched-settings:rmjob', 'ENABLED=0', 'human', 'x')", (_sess,))
_c.commit(); _c.close()
# 組織図(設定 org。agent 一覧の中の節と /org。上司は tb org と同じ agent.org で引く)
_ab = cl.get("/agents")[2]
check("組織図: 未設定なら agent 一覧に未設定の案内(図へのリンクは出さない)", '<h2 id="org">組織図</h2>' in _ab and "組織図は未設定です" in _ab
      and 'href="/org"' not in _ab and 'href="/org"' not in cl.get("/")[2])
_c = sqlite3.connect(env["WEBUI_TB_DB"])
for _n in ("orgboss", "orgw1", "orgw2"):
    _c.execute("INSERT OR IGNORE INTO agents (herdr_session, name, state, created_by, created_at, updated_at) VALUES (?, ?, 'stopped', 'human', 'x', 'x')", (_sess, _n))
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES (?, 'org', ?, 'human', 'x')",
           (_sess, "orgw* -> orgboss\n"))
_c.commit(); _c.close()
_ab = cl.get("/agents")[2]
_og = _ab.split('<h2 id="org">')[1].split('<h2 id="roles">')[0]
check("組織図: agent 一覧に上司の下へ部下が入った木と図へのリンク",
      re.search(r'<li><a href="/agents/orgboss">orgboss</a>[^<]*(<span[^>]*>[^<]*</span>)?<ul><li><a href="/agents/orgw1">orgw1</a>.*?</li><li><a href="/agents/orgw2">', _og) is not None
      and 'href="/org"' in _og, _og[:800])
_os, _oh, _ob = cl.get("/org")
_om = _ob.split("<pre class=mermaid>")[1].split("</pre>")[0] if "<pre class=mermaid>" in _ob else ""
check("組織図: /org は mermaid の図(上司 → 部下)と木、スクリプトは同一オリジンだけ", _os == 200 and _om.startswith("flowchart TD")
      and re.search(r"n\d+ --&gt; n\d+", _om) is not None and "orgw1" in _om
      and script_tags(_ob) == [NOTIFY_TAG[:-9], '<script src="/static/mermaid.min.js" defer>', '<script src="/docs.js" defer>'], _om[:300])
_c = sqlite3.connect(env["WEBUI_TB_DB"]); _c.execute("DELETE FROM config WHERE key='org'")
_c.execute("DELETE FROM agents WHERE name LIKE 'org%'"); _c.commit(); _c.close()
_rmlog = os.path.join(env["WEBUI_SCHED_LOGS"], "rmjob.log")
open(_rmlog, "w").write('{"ts":"2026-11-01T10:00:00+0900","job":"rmjob","action":"ran"}\n')
def _rmrows():
    c = sqlite3.connect(env["WEBUI_TB_DB"])
    try:
        return c.execute("SELECT key FROM config WHERE key IN ('sched-job:rmjob', 'sched-settings:rmjob') ORDER BY key").fetchall()
    finally:
        c.close()
_rj = cl.get("/jobs/rmjob")[2]
check("ジョブを消す: 詳細に消すフォーム(印が要る)", 'value=rm>' in _rj and "name=confirm value=1 required" in _rj)
r = cl.raw("POST", "/jobs/rmjob", {"action": "rm", "csrf": cl.csrf("/jobs/rmjob")})
check("ジョブを消す: 印が無ければ 400 で消さない", r[0] == 400 and "元に戻せないことを確かめた" in r[2] and len(_rmrows()) == 2 and os.path.exists(_rmlog))
check("ジョブを消す: CSRF 無しは拒否", cl.raw("POST", "/jobs/rmjob", {"action": "rm", "confirm": "1"})[0] == 403 and len(_rmrows()) == 2)
r = cl.raw("POST", "/jobs/rmjob", {"action": "rm", "confirm": "1", "csrf": cl.csrf("/jobs/rmjob")})
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_rh = _c.execute("SELECT key, how FROM config_changes WHERE key LIKE '%:rmjob' ORDER BY id").fetchall()
_c.close()
check("ジョブを消す: tb job rm で行とログを消し、一覧へ戻る(履歴は残る)", r[0] == 302 and r[1].get("Location") == "/jobs?done=job-rm"
      and _rmrows() == [] and not os.path.exists(_rmlog) and _rh == [("sched-job:rmjob", "unset"), ("sched-settings:rmjob", "unset")], str((r[:2], _rmrows(), _rh)))
_jl = cl.get("/jobs?done=job-rm")[2]
check("ジョブを消す: 一覧から外れ、消したと知らせる", "rmjob" not in _jl and "ジョブを消しました" in _jl)
check("ジョブを消す: 消したジョブは 404", cl.get("/jobs/rmjob")[0] == 404)
# 定義はファイル(job1)でも、DB に settings の行があると scheduler は DB を読む。Web UI も DB に書く(ファイルに書くと隠れて効かない)
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES (?, 'sched-settings:job1', ?, 'human', 'x')",
           (_sess, "INTERVAL_MIN=40\n"))
_c.commit(); _c.close()
check("ファイルの定義 + DB の settings: 一覧は DB の値", "40 分おき" in cl.get("/jobs")[2])
check("ファイルの定義 + DB の settings: 保存先は DB と出る", "DB の <code>sched-settings:job1</code>" in cl.get("/jobs/job1")[2])
r = cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/jobs/job1")})
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_sb = _c.execute("SELECT body FROM config WHERE key='sched-settings:job1'").fetchall()
_c.close()
check("ファイルの定義 + DB の settings: 保存は DB に書いて効く(ファイルは作らない)",
      r[0] == 302 and _sb and "INTERVAL_MIN=15" in _sb[0][0] and not os.path.exists(SET)
      and "15 分おき / 10:00-18:00 / weekdays" in cl.get("/jobs")[2], str((r[:2], _sb)))
# DB の行を消すとファイルの settings が効き出すので、戻すときは残ったファイルも消す
open(SET, "w").write("INTERVAL_MIN=50\n"); os.chmod(SET, 0o600)
r = cl.raw("POST", "/jobs/job1", {"action": "reset", "csrf": cl.csrf("/jobs/job1")})
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_left = _c.execute("SELECT COUNT(*) FROM config WHERE key='sched-settings:job1'").fetchone()[0]
_c.close()
check("ファイルの定義 + DB の settings: 戻すと DB の行と残ったファイルを消し、conf の値に戻る",
      r[0] == 302 and _left == 0 and not os.path.exists(SET) and "10 分おき / 09:00-21:00 / daily" in cl.get("/jobs")[2], str(r[:2]))
# 共通の行(*)だけがあるとき: 保存はこのセッションの行に書いて効く。戻すは共通の行を消さず、戻らないと伝える
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES ('*', 'sched-settings:job1', ?, 'human', 'x')",
           ("INTERVAL_MIN=45\n",))
_c.commit(); _c.close()
r = cl.raw("POST", "/jobs/job1", {**good, "csrf": cl.csrf("/jobs/job1")})
check("共通の settings の行: 保存はこのセッションの行に書いて効く", r[0] == 302 and "15 分おき / 10:00-18:00" in cl.get("/jobs")[2], str(r[:2]))
r = cl.raw("POST", "/jobs/job1", {"action": "reset", "csrf": cl.csrf("/jobs/job1")})
r2 = cl.raw("POST", "/jobs/job1", {"action": "reset", "csrf": cl.csrf("/jobs/job1")})
check("共通の settings の行: 戻すとこのセッションの行だけ消え、共通の行は残して 400 で知らせる",
      r[0] == 400 and "共通の行" in r[2] and r2[0] == 400 and "共通の行" in r2[2] and "45 分おき" in cl.get("/jobs")[2], str((r[0], r2[0])))
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_c.execute("DELETE FROM config WHERE key='sched-settings:job1'"); _c.commit(); _c.close()
# 定義が DB、settings がファイルに残っている: 戻すとファイルも消す(消さないとファイルの値が効いたまま)
_DBSET = os.path.join(env["WEBUI_SCHED_SETTINGS"], "dbjob.settings")
open(_DBSET, "w").write("INTERVAL_MIN=55\n"); os.chmod(_DBSET, 0o600)
check("DB の定義 + ファイルの settings: ファイルの値が効いている", "55 分おき" in cl.get("/jobs")[2])
r = cl.raw("POST", "/jobs/dbjob", {"action": "reset", "csrf": cl.csrf("/jobs/dbjob")})
check("DB の定義 + ファイルの settings: 戻すとファイルも消して conf の値に戻る",
      r[0] == 302 and not os.path.exists(_DBSET) and "20 分おき" in cl.get("/jobs")[2], str(r[:2]))
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
_c = sqlite3.connect(env["WEBUI_TB_DB"])
_nq = _c.execute("SELECT COUNT(*) FROM requests r WHERE herdr_session='default' AND ((r.source='cmd' AND r.status='pending') OR (r.source='outbox' AND EXISTS "
                 "(SELECT 1 FROM outbox o WHERE o.id=r.source_id AND o.status='pending')) OR (r.source='agent_ops' AND EXISTS "
                 "(SELECT 1 FROM agent_ops a WHERE a.id=r.source_id AND a.status='pending')))").fetchone()[0]
_c.close()
check("対応済みは要対応から消え、バッジが減る(バッジは報告・代行・blocked の agent と承認待ち Q-<n> の合計)",
      f'<span class="badge">{2 + _nq}</span>' in ib and 'name=id value="1"' not in ib, f"承認待ち {_nq}")
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
with open(env["TB_PROTECT"], "w") as _f:
    _f.write("# 保護\nalp* = 定期実行の受け口\n")
ag = cl.get("/agents")[2]
check("protect に一致した agent は保護中で、停止ボタンを出さない", "保護中" in ag and "定期実行の受け口" in ag and "protect から外してから" in ag)
r = cl.raw("POST", "/agent-op", {**good_op, "csrf": cl.csrf("/agents")})
check("保護中は強制停止も tb が断る", r[0] == 302 and r[1].get("Location") == "/agents?op=refused" and "pane close" not in flog(), str(r[:2]) + flog())
with open(env["TB_PROTECT"], "w") as _f:
    _f.write("role:nosuch\n")
with open(env["TB_PROTECT"], "w") as _f:
    _f.write("role:a+b\n")
check("role: は 1 つの役割の glob(+ で複数役割の同時一致にしない。tb と同じ)", "保護中" not in cl.get("/agents")[2])
regdb("UPDATE agents SET name='alpha-moved' WHERE name='alpha'")
with open(env["TB_PROTECT"], "w") as _f:
    _f.write("alp* = 台帳に無い\n")
ag = cl.get("/agents")[2]
check("台帳に行が無い agent も protect に一致すれば保護中", "台帳に無い" in ag and "protect から外してから" in ag)
regdb("UPDATE agents SET name='alpha' WHERE name='alpha-moved'")
with open(env["TB_PROTECT"], "w") as _f:
    _f.write("role:nosuch\n")
check("一致しなければ従来どおり", 'value="force-stop"' in cl.get("/agents")[2] and "保護中" not in cl.get("/agents")[2])
os.remove(env["TB_PROTECT"])
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
_ql = [it for it in json.loads(cl.get("/api/notifications")[2])["items"] if it["kind"] == "approval_pending" and "agent:new helper" in it["title"]]
check("緊急通知にも Q-<n> で出る(agent 一覧へのリンク)", len(_ql) == 1 and _ql[0]["href"].endswith("/agents#ops") and _ql[0]["title"].startswith("Q-"), str(_ql))
check("承認待ちの操作の id は Q-<n>(元の L-<n> も添える)", _ql and f'{_ql[0]["title"].split()[0]}<br><span class=note>L-{lid}</span>' in ag)
check("id が数字でなければ 400", cl.raw("POST", "/agent-op", {"op": "approve", "id": "1;x", "csrf": cl.csrf("/agents")})[0] == 400)
r = cl.raw("POST", "/agent-op", {"op": "approve", "id": str(lid), "csrf": cl.csrf("/agents")})
check("承認すると実行する", r[0] == 302 and r[1].get("Location") == "/agents?op=done" and "--label helper" in flog(), str(r[:2]))
check("記録は依頼者のまま done", regdb("SELECT status, requested_by FROM agent_ops WHERE id=?", (lid,)) == [("done", "conductor")])
r = cl.raw("POST", "/agent-op", {"op": "reject", "id": str(lid), "csrf": cl.csrf("/agents")})
check("済んだものは却下できない", r[0] == 302 and r[1].get("Location") == "/agents?op=refused", str(r[:2]))
os.remove(os.path.join(TMP, "fake", "closed"))
# ---- Q チケットの承認・却下と deny の例外(/approvals。書き込みは本物の tb approval を人として呼ぶ)
def _req(status, tier, requester, target):
    regdb("INSERT INTO requests (herdr_session, source, requester, op, target, detail, payload, status, tier, created_at) VALUES "
          "('default','cmd',?, 'cmd:ticket:cancel', ?, 'tb ticket cancel', '{\"args\":[\"ticket\",\"cancel\",\"T-1\"]}', ?, ?, '2026-11-10T00:00:00Z')",
          (requester, target, status, tier))
    return regdb("SELECT max(id) FROM requests")[0][0]
_qa, _qr, _qd, _qg = _req("pending", "ask", "alpha", "T-1"), _req("pending", "ask", "alpha", "T-2"), _req("denied", "deny", "alpha", "T-3"), _req("pending", "ask", "ghost", "T-4")
ap = cl.get("/approvals?tier=all")[2]
check("Q チケット画面に承認・却下・例外のボタン(戻り先はいまの絞り込み)", re.search(f'name=id value="{_qa}"><input type=hidden name=back value="/approvals\\?tier=all"><input type=hidden name=op value=approve>', ap)
      and re.search(f'name=id value="{_qd}"><input type=hidden name=back value="[^"]*"><input type=hidden name=op value=exception>', ap) and 'action="/approval-op"' in ap)
_tok = cl.csrf("/approvals")
r = cl.raw("POST", "/approval-op", {"op": "approve", "id": str(_qa), "back": "/approvals?tier=all", "csrf": _tok})
check("承認すると依頼者に知らせ、記録は human が approved(押した画面の絞り込みのまま戻る)", r[0] == 302 and r[1].get("Location") == f"/approvals?tier=all&decided=Q-{_qa}&done=approve"
      and regdb("SELECT status, decided_by FROM requests WHERE id=?", (_qa,)) == [("approved", "human")]
      and f"agent prompt alpha [tb approval / Q-{_qa} / human] Q-{_qa} は human に承認されました。tb approval run Q-{_qa} で 1 回だけ実行できます" in flog(),
      str(r[:2]) + str([x for x in flog().splitlines() if "Q-" in x]))
r = cl.raw("POST", "/approval-op", {"op": "reject", "id": str(_qr), "csrf": _tok})
check("却下すると rejected", r[0] == 302 and regdb("SELECT status, decided_by FROM requests WHERE id=?", (_qr,)) == [("rejected", "human")], str(r[:2]))
r = cl.raw("POST", "/approval-op", {"op": "exception", "id": str(_qd), "csrf": _tok})
check("例外の許可は確認のチェックが要る", r[0] == 400 and "確認のチェック" in r[2] and regdb("SELECT status FROM requests WHERE id=?", (_qd,)) == [("denied",)])
r = cl.raw("POST", "/approval-op", {"op": "exception", "id": str(_qd), "confirm": "1", "csrf": _tok})
check("deny を例外として許可すると approved", r[0] == 302 and r[1].get("Location") == f"/approvals?decided=Q-{_qd}&done=exception"
      and regdb("SELECT status, decided_by FROM requests WHERE id=?", (_qd,)) == [("approved", "human")], str(r[:2]) + str(re.findall(r"<p class=err>.*?</p>", r[2])))
r = cl.raw("POST", "/approval-op", {"op": "approve", "id": str(_qg), "csrf": _tok})
check("依頼した agent がいなければ tb が断り、理由を出す", r[0] == 400 and "tb が断りました" in r[2]
      and regdb("SELECT status FROM requests WHERE id=?", (_qg,)) == [("pending",)], r[2][-400:])
r = cl.raw("POST", "/approval-op", {"op": "reject", "id": str(_qa), "csrf": _tok})
check("決まったものは決め直せない", r[0] == 400 and "承認待ちではありません(approved)" in r[2])
regdb("INSERT INTO outbox (from_name,from_pane,from_session,target,kind,text,status,created_at) VALUES ('conductor','w0:p1','s','alpha','info','見て','pending','2026-11-10T00:00:00Z')")
_qo = regdb("SELECT id FROM requests WHERE source='outbox' AND source_id=(SELECT max(id) FROM outbox)")[0][0]
r = cl.raw("POST", "/approval-op", {"op": "approve", "id": str(_qo), "csrf": _tok})
_ap = cl.get(r[1].get("Location", "/approvals"))[2] if r[0] == 302 else r[2]
check("送信の承認も Q の記録に human が approved で残り、届いたかは別に出る", r[0] == 302
      and regdb("SELECT status, decided_by FROM requests WHERE id=?", (_qo,)) == [("approved", "human")]
      and f"Q-{_qo}: 承認しました" in _ap and re.search(f"Q-{_qo}</a></td><td>ask</td><td>approved<br><span class=note>送信: (sent|denied)", _ap), str(regdb("SELECT status, reason FROM outbox ORDER BY id DESC LIMIT 1")) + str(re.findall(r"<p class=note>Q-[^<]*</p>", _ap)) + str(re.findall(f"Q-{_qo}</a>.{{0,300}}", _ap)))
check("CSRF 無しの /approval-op は拒否", cl.raw("POST", "/approval-op", {"op": "approve", "id": str(_qg)})[0] == 403)
# 承認して今後は承認なし(tb approval approve Q-<n> --always)。送信と agent の操作だけに出し、確認のチェックが要る
regdb("INSERT INTO outbox (from_name,from_pane,from_session,target,kind,text,status,created_at) VALUES ('conductor','w0:p1','s','alpha','answer','いつも','pending','2026-11-10T00:00:00Z')")
_qw = regdb("SELECT id FROM requests WHERE source='outbox' AND source_id=(SELECT max(id) FROM outbox)")[0][0]
ap = cl.get("/approvals?tier=all")[2]
check("今後は承認なしのボタンは送信に出て、コマンドの承認待ちには出ない",
      re.search(f'name=id value="{_qw}"><input type=hidden name=back value="[^"]*"><input type=hidden name=op value=always>', ap)
      and not re.search(f'name=id value="{_qg}"><input type=hidden name=back value="[^"]*"><input type=hidden name=op value=always>', ap))
r = cl.raw("POST", "/approval-op", {"op": "always", "id": str(_qw), "csrf": _tok})
check("今後は承認なしは確認のチェックが要る", r[0] == 400 and "確認のチェック" in r[2] and regdb("SELECT status FROM requests WHERE id=?", (_qw,)) == [("pending",)])
r = cl.raw("POST", "/approval-op", {"op": "always", "id": str(_qw), "confirm": "1", "csrf": _tok})
_pol = "\n".join(b for (b,) in regdb("SELECT body FROM config WHERE key='policy'"))
check("承認して policy に allow を書く", r[0] == 302 and r[1].get("Location") == f"/approvals?decided=Q-{_qw}&done=always"
      and regdb("SELECT status, decided_by FROM requests WHERE id=?", (_qw,)) == [("approved", "human")]
      and re.search(r"^\* send:answer alpha = allow", _pol, re.M), str(r[:2]) + _pol + str(re.findall(r"<p class=err>.*?</p>", r[2])))
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
_src = "".join(open(os.path.join(REPO, "internal", "webui", f), encoding="utf-8").read() for f in ("perms.go", "permspage.go"))
check("権限の画面は tb も他のコマンドも呼ばない(agent の pane から効く経路を作らない)",
      "os/exec" not in _src and "WriteFile" not in _src and "OpenFile" not in _src and 'configText("policy")' in _src)

# ================= Web UI からの編集(チケットの状態・状態の定義・スプリント・policy・ジョブの定義。書き込みは tb が行う)
base = free_range(1)
env, d = make_env("edits", HERDR_WEBUI_RANGE=f"{base}-{base}")
env.pop("WEBUI_POLICY")   # 表示用の policy ファイルを指定しない(tb と同じものを読み書きする)
env.pop("TB_POLICY", None)
make_db(env["WEBUI_TB_DB"])
_c = sqlite3.connect(env["WEBUI_TB_DB"])   # tb の形の ID(T-<project>-NNN)のタスク
_c.execute("INSERT INTO tasks (id, project, title, status, created_at, updated_at, herdr_session) VALUES "
           "('T-pj-001', 'pj', '編集する', 'in_progress', '2026-11-06T00:00:00Z', '2026-11-06T00:00:00Z', 'default')")
_c.commit(); _c.close()
os.makedirs(env["WEBUI_SCHED_JOBS"])
os.makedirs(env["WEBUI_SCHED_LOGS"])
p = start(env)
port = wait_port(env, p)
check("edits: 起動できる", port is not None)
ec = Client(port)
ec.raw("POST", "/setup", {"csrf": ec.csrf("/setup"), "username": USER, "password": PW, "confirm": PW})
ec.cookie = None
ec.login()


def edb(sql, args=()):
    c = sqlite3.connect(env["WEBUI_TB_DB"])
    try:
        r = c.execute(sql, args).fetchall()
        c.commit()
        return r
    finally:
        c.close()


# ---- 状態の定義(/statuses。tb ticket status add / rm)
sp = ec.get("/statuses")
check("状態の定義: 一覧と足すフォーム(自動更新なし)", sp[0] == 200 and 'action="/statuses"' in sp[2] and ">in_progress</a>" in sp[2]
      and 'http-equiv="refresh"' not in sp[2])
check("状態の定義: CSRF 無しは拒否", ec.raw("POST", "/statuses", {"action": "add", "name": "qa", "cat": "review"})[0] == 403)
check("状態の定義: 未ログインは /login へ", Client(port).raw("POST", "/statuses", {"action": "add", "name": "qa", "cat": "review"})[0] == 302)
r = ec.raw("POST", "/statuses", {"action": "add", "name": "q a", "cat": "review", "csrf": ec.csrf("/statuses")})
check("状態の定義: 空白を含む名前は 400(書かない)", r[0] == 400 and "30 文字まで" in r[2] and not edb("SELECT 1 FROM config WHERE key='statuses'"))
r = ec.raw("POST", "/statuses", {"action": "add", "name": "qa", "cat": "review", "desc": "検証 <b>中</b>", "after": "review",
                                 "csrf": ec.csrf("/statuses")})
_st = edb("SELECT body FROM config WHERE key='statuses'")   # tb は今の出どころに合わせて書く(ファイルや共通の行から読んでいれば共通の行)
check("状態の定義: 足すと設定 statuses に書く(tb ticket status add)", r[0] == 302 and r[1].get("Location") == "/statuses?done=status-add"
      and _st and "qa review" in _st[0][0], str((r[:2], _st)))
check("状態の定義: 足した状態が一覧に出る(説明はエスケープ)", "検証 &lt;b&gt;中&lt;/b&gt;" in ec.get("/statuses?done=status-add")[2]
      and "状態を足しました" in ec.get("/statuses?done=status-add")[2])
r = ec.raw("POST", "/statuses", {"action": "add", "name": "qa", "cat": "review", "csrf": ec.csrf("/statuses")})
check("状態の定義: 同じ名前は tb が断り、理由を見せる", r[0] == 400 and "tb が断りました" in r[2], r[2][-300:])

# ---- チケットの状態(/task-status。tb ticket move)
tp = ec.get("/tasks/T-pj-001")
check("タスク画面に状態の変更フォーム(足した状態も選べる。自動更新なし)", 'action="/task-status"' in tp[2] and '<option value="qa">' in tp[2]
      and 'http-equiv="refresh"' not in tp[2])
check("チケットの状態: CSRF 無しは拒否", ec.raw("POST", "/task-status", {"id": "T-pj-001", "status": "qa"})[0] == 403)
r = ec.raw("POST", "/task-status", {"id": "T-1", "status": "review", "csrf": ec.csrf("/tasks/T-1")})
check("チケットの状態: tb が断れば理由を見せる(tb の形でない ID)", r[0] == 400 and "T-&lt;project&gt;-NNN" in r[2], r[2][-300:])
check("チケットの状態: 無いタスクは 404", ec.raw("POST", "/task-status", {"id": "T-404", "status": "qa", "csrf": ec.csrf("/tasks/T-pj-001")})[0] == 404)
r = ec.raw("POST", "/task-status", {"id": "T-pj-001", "status": "nope", "csrf": ec.csrf("/tasks/T-pj-001")})
check("チケットの状態: 定義に無い状態は 400", r[0] == 400 and "設定 statuses" in r[2] and edb("SELECT status FROM tasks WHERE id='T-pj-001'") == [("in_progress",)])
r = ec.raw("POST", "/task-status", {"id": "T-pj-001", "status": "qa", "memo": "見てもらう", "csrf": ec.csrf("/tasks/T-pj-001")})
check("チケットの状態: tb ticket move で変える(人の操作として記録)", r[0] == 302 and r[1].get("Location") == "/tasks/T-pj-001?done=status"
      and edb("SELECT status FROM tasks WHERE id='T-pj-001'") == [("qa",)]
      and edb("SELECT actor FROM task_events WHERE task_id='T-pj-001' AND kind='status' ORDER BY id DESC LIMIT 1") == [("human",)],
      str((r[0], re.findall(r"<p class=err>(.*?)</p>", r[2], re.S), edb("SELECT kind, actor FROM task_events WHERE task_id='T-pj-001'"))))
check("チケットの状態: 変えたと知らせる", "状態を変えました" in ec.get("/tasks/T-pj-001?done=status")[2])
r = ec.raw("POST", "/statuses", {"action": "rm", "name": "qa", "csrf": ec.csrf("/statuses")})
check("状態の定義: チケットが残る状態は、移し先なしでは外せない", r[0] == 400 and "--to" in r[2] and "qa review" in edb("SELECT body FROM config WHERE key='statuses'")[0][0])
r = ec.raw("POST", "/statuses", {"action": "rm", "name": "qa", "to": "in_progress", "csrf": ec.csrf("/statuses")})
check("状態の定義: 移し先を選ぶと移してから外す", r[0] == 302 and edb("SELECT status FROM tasks WHERE id='T-pj-001'") == [("in_progress",)]
      and "qa" not in edb("SELECT body FROM config WHERE key='statuses'")[0][0], str((r[0], re.findall(r"<p class=err>(.*?)</p>", r[2], re.S))))

# ---- チケットを消す(/task-rm。tb ticket rm)
edb("INSERT INTO tasks (id, project, title, status, parent_id, created_at, updated_at, herdr_session) VALUES "
    "('T-pj-002', 'pj', '親', 'done', NULL, '2026-11-06T00:00:00Z', '2026-11-06T00:00:00Z', 'default'), "
    "('T-pj-003', 'pj', '子', 'done', 'T-pj-002', '2026-11-06T00:00:00Z', '2026-11-06T00:00:00Z', 'default')")
edb("INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at, herdr_session) VALUES "
    "('T-pj-003', 'progress', 'x', 'w', 'p', 's', '2026-11-06T00:00:00Z', 'default')")
tp = ec.get("/tasks/T-pj-003")[2]
check("チケットを消す: タスク画面に消すフォーム(印が要る・作成時刻を持つ)", 'action="/task-rm"' in tp and "name=confirm value=1 required" in tp
      and 'name=created value="2026-11-06T00:00:00Z"' in tp)
check("チケットを消す: CSRF 無しは拒否", ec.raw("POST", "/task-rm", {"id": "T-pj-003", "confirm": "1"})[0] == 403)
check("チケットを消す: 未ログインは /login へ", Client(port).raw("POST", "/task-rm", {"id": "T-pj-003", "confirm": "1"})[0] == 302)
check("チケットを消す: 無いタスクは 404", ec.raw("POST", "/task-rm", {"id": "T-404", "confirm": "1", "csrf": ec.csrf("/tasks/T-pj-003")})[0] == 404)
r = ec.raw("POST", "/task-rm", {"id": "T-pj-003", "csrf": ec.csrf("/tasks/T-pj-003")})
check("チケットを消す: 印が無ければ 400 で消さない", r[0] == 400 and "元に戻せないことを確かめた" in r[2] and edb("SELECT 1 FROM tasks WHERE id='T-pj-003'"))
r = ec.raw("POST", "/task-rm", {"id": "T-pj-002", "confirm": "1", "created": "2026-11-06T00:00:00Z", "csrf": ec.csrf("/tasks/T-pj-002")})
check("チケットを消す: 子があれば tb が断り、理由を見せる", r[0] == 400 and "子チケット" in r[2] and edb("SELECT 1 FROM tasks WHERE id='T-pj-002'"), r[2][-300:])
r = ec.raw("POST", "/task-rm", {"id": "T-pj-003", "confirm": "1", "created": "2000-01-01T00:00:00Z", "csrf": ec.csrf("/tasks/T-pj-003")})
check("チケットを消す: 画面を開いたときのチケットでなければ(ID がまた使われた)消さない", r[0] == 400 and "開き直して" in r[2] and edb("SELECT 1 FROM tasks WHERE id='T-pj-003'"), r[2][-300:])
r = ec.raw("POST", "/task-rm", {"id": "T-pj-003", "confirm": "1", "created": "2026-11-06T00:00:00Z", "csrf": ec.csrf("/tasks/T-pj-003")})
check("チケットを消す: tb ticket rm で消し、一覧へ戻る(コメントも消える)", r[0] == 302 and r[1].get("Location") == "/tasks?done=task-rm"
      and not edb("SELECT 1 FROM tasks WHERE id='T-pj-003'") and not edb("SELECT 1 FROM comments WHERE task_id='T-pj-003'"), str(r[:2]))
check("チケットを消す: 消したと知らせ、画面は 404", "チケットを消しました" in ec.get("/tasks?done=task-rm")[2] and ec.get("/tasks/T-pj-003")[0] == 404)

# ---- スプリント(/sprint-op。tb sprint-add / sprint-start / sprint-close)
bp = ec.get("/board")
check("ボードにスプリントを作るフォーム(PJ の候補)", 'action="/sprint-op"' in bp[2] and '<option value="pj">' in bp[2])
r = ec.raw("POST", "/sprint-op", {"op": "add", "project": "p j", "name": "x", "csrf": ec.csrf("/board")})
check("スプリント: PJ が不正なら 400", r[0] == 400 and "英数字" in r[2] and not edb("SELECT 1 FROM sprints"))
r = ec.raw("POST", "/sprint-op", {"op": "add", "project": "pj", "name": "S1", "start": "2026-12-01", "end": "2026-11-01", "csrf": ec.csrf("/board")})
check("スプリント: 終了が開始より前なら tb が断る", r[0] == 400 and "tb が断りました" in r[2] and not edb("SELECT 1 FROM sprints"), r[2][-300:])
r = ec.raw("POST", "/sprint-op", {"op": "add", "project": "pj", "name": "第 1 <b>", "start": "2026-12-01", "end": "2026-12-14", "goal": "出す",
                                  "csrf": ec.csrf("/board")})
_sp = edb("SELECT id, project, name, state, goal, herdr_session FROM sprints")
check("スプリント: 作る(planned)", r[0] == 302 and len(_sp) == 1 and _sp[0][1:] == ("pj", "第 1 <b>", "planned", "出す", "default")
      and r[1].get("Location") == f"/board?sprint=S-{_sp[0][0]}&done=sprint-add", str((r[:2], _sp)))
_sid = f"S-{_sp[0][0]}"
bp = ec.get(f"/board?sprint={_sid}")[2]
check("スプリント: 一覧に始めるボタン(名前はエスケープ)", "value=start" in bp and "第 1 &lt;b&gt;" in bp)
r = ec.raw("POST", "/sprint-op", {"op": "start", "sprint": _sid, "csrf": ec.csrf("/board")})
check("スプリント: 始める(active)", r[0] == 302 and edb("SELECT state FROM sprints") == [("active",)], str(r[:2]))
r = ec.raw("POST", "/sprint-op", {"op": "start", "sprint": _sid, "csrf": ec.csrf("/board")})
check("スプリント: active をもう一度始めるのは tb が断る", r[0] == 400 and "planned" in r[2])
r = ec.raw("POST", "/sprint-op", {"op": "close", "sprint": _sid, "carry": "", "csrf": ec.csrf("/board")})
check("スプリント: 閉じる", r[0] == 302 and edb("SELECT state FROM sprints") == [("closed",)], str(r[:2]))
check("スプリント: 不正な ID は 400", ec.raw("POST", "/sprint-op", {"op": "start", "sprint": "S-x", "csrf": ec.csrf("/board")})[0] == 400)

# ---- policy(/policy。tb config set policy。ログインのパスワードが要る)
pp = ec.get("/perms")[2]
_pb = re.search(r'name=base value="([^"]+)"', pp)
check("権限画面に policy の編集フォーム(パスワード欄)", 'action="/policy"' in pp and "type=password name=password" in pp and _pb)
_good_pol = "# Web UI から\nworker1 send:task:* worker2 = allow\n"
_pol = {"body": _good_pol, "base": _pb.group(1) if _pb else ""}
check("policy: CSRF 無しは拒否", ec.raw("POST", "/policy", {**_pol, "password": PW})[0] == 403)
check("policy: 未ログインは /login へ", Client(port).raw("POST", "/policy", {**_pol, "password": PW})[0] == 302)
r = ec.raw("POST", "/policy", {**_pol, "password": WRONG, "csrf": ec.csrf("/perms")})
check("policy: パスワードが違えば書かない(入力は残す)", r[0] == 400 and "パスワードが違います" in r[2] and "send:task:* worker2" in r[2]
      and not edb("SELECT 1 FROM config WHERE key='policy'"))
r = ec.raw("POST", "/policy", {**_pol, "body": "worker1 send:task:* = maybe\n", "password": PW, "csrf": ec.csrf("/perms")})
check("policy: tb の検査に通らない本文は書かない", r[0] == 400 and "tb が断りました" in r[2] and not edb("SELECT 1 FROM config WHERE key='policy'"), r[2][-400:])
r = ec.raw("POST", "/policy", {**_pol, "password": PW, "csrf": ec.csrf("/perms")})
_pr = edb("SELECT body FROM config WHERE key='policy' AND herdr_session='default'")
check("policy: 保存すると設定 policy のこのセッションの行に書く(履歴つき)", r[0] == 302 and r[1].get("Location") == "/perms?done=policy"
      and _pr == [(_good_pol.rstrip("\n"),)] and edb("SELECT how FROM config_changes WHERE key='policy'") == [("set",)], str((r[:2], _pr)))
check("policy: 書いた行が一覧に出る", "send:task:*" in ec.get("/perms?done=policy")[2] and "policy を保存しました" in ec.get("/perms?done=policy")[2])
r = ec.raw("POST", "/policy", {**_pol, "body": "* send:task:* * = allow\n", "password": PW, "csrf": ec.csrf("/perms")})
check("policy: 編集を始めたあとで変わっていれば書かない", r[0] == 400 and "開き直して" in r[2] and edb("SELECT body FROM config WHERE key='policy'") == [(_good_pol.rstrip("\n"),)],
      str((r[0], re.findall(r"<p class=err>(.*?)</p>", r[2], re.S))))

# ---- ジョブの定義(/jobs/<job> の action=conf。tb job set)
_conf = "TARGET=alpha\nPROMPT='朝のまとめを出してください'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n"
edb("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES ('default', 'sched-job:ej', ?, 'human', 'x')", (_conf,))
jp = ec.get("/jobs/ej")[2]
_jb = re.search(r'name=action value=conf><input type=hidden name=base value="([^"]+)"', jp)
check("ジョブの定義: 本文の編集欄(今の本文)", _jb and "<textarea name=conf" in jp and "朝のまとめを出してください" in jp)
_jf = {"action": "conf", "base": _jb.group(1) if _jb else ""}
r = ec.raw("POST", "/jobs/ej", {**_jf, "conf": "echo hi\n", "csrf": ec.csrf("/jobs/ej")})
check("ジョブの定義: scheduler が読めない本文は書かない(理由を見せる)", r[0] == 400 and "tb が断りました" in r[2]
      and edb("SELECT body FROM config WHERE key='sched-job:ej'") == [(_conf,)], r[2][-400:])
_new = _conf.replace("朝のまとめ", "夕方のまとめ")
r = ec.raw("POST", "/jobs/ej", {**_jf, "conf": _new.replace("\n", "\r\n"), "csrf": ec.csrf("/jobs/ej")})
check("ジョブの定義: 保存すると tb job set で書く(改行は LF、履歴つき)", r[0] == 302 and r[1].get("Location") == "/jobs/ej?saved=1&done=conf"
      and edb("SELECT body FROM config WHERE key='sched-job:ej'") == [(_new.rstrip("\n"),)]
      and edb("SELECT how FROM config_changes WHERE key='sched-job:ej'") == [("set",)], str(r[:2]))
check("ジョブの定義: 送られる本文に反映", "夕方のまとめ" in ec.get("/jobs/ej?saved=1&done=conf")[2] and "ジョブの定義を保存しました" in ec.get("/jobs/ej?saved=1&done=conf")[2])
r = ec.raw("POST", "/jobs/ej", {**_jf, "conf": _conf, "csrf": ec.csrf("/jobs/ej")})
check("ジョブの定義: 編集を始めたあとで変わっていれば書かない", r[0] == 400 and "開き直して" in r[2]
      and edb("SELECT body FROM config WHERE key='sched-job:ej'") == [(_new.rstrip("\n"),)], str((r[0], re.findall(r"<p class=err>(.*?)</p>", r[2], re.S))))
_tok = "gh" + "p_" + "y" * 24
edb("INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES ('default', 'sched-job:tj', ?, 'human', 'x')",
    ("COMMAND='curl -H " + _tok + "'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n",))
tj = ec.get("/jobs/tj")[2]
check("ジョブの定義: トークンらしい文字列があれば本文を出さず編集もしない", "<textarea" not in tj and _tok not in tj and "tb job edit tj" in tj)
open(os.path.join(env["WEBUI_SCHED_JOBS"], "fj.conf"), "w").write("COMMAND='echo f'\nWINDOW=09:00-21:00\nINTERVAL_MIN=30\nTZ=Asia/Tokyo\n")
check("ジョブの定義: ファイルだけの定義は取り込みを案内する", "tb job import fj" in ec.get("/jobs/fj")[2] and "<textarea" not in ec.get("/jobs/fj")[2])
check("edits: サーバー生存", p.poll() is None)
stop(p)

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
_c.execute("INSERT INTO tasks (id,project,title,owner_agent,session_id,account,status,parent_id,deliverables,summary,note,reported_at,created_at,updated_at,herdr_session) VALUES ('T-9','pj','beta だけの題','w','sid-b',NULL,'in_progress',NULL,'[]',NULL,NULL,NULL,"
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

print()
print("FAILED: " + ", ".join(fails) if fails else "all passed")
sys.exit(1 if fails else 0)
