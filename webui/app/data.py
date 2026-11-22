"""tasks.db とログの読み取り(読み取り専用)、緊急通知の集計。"""

import contextvars
import json
import os
import re
import sqlite3
import time
from contextlib import contextmanager
from fnmatch import fnmatchcase
from datetime import datetime, timedelta, timezone
from urllib.parse import quote

from . import reqctx
from .config import HERDR_ROOT, JOB_RE, SCHED_LOGS, STALL_MIN, TB_DB
from .sanitize import clean
from .herdr import agent_key, agent_session, cached, get_agents


_TABLE_RE = re.compile(r"[a-z_]{1,64}")


SESSION_TABLES = os.path.join(HERDR_ROOT, "taskboard", "sql", "session_tables")
SESSION_VIEWS = os.path.join(HERDR_ROOT, "taskboard", "sql", "session_views")


def _name_list(path):
    try:
        with open(path, encoding="utf-8") as f:
            return [t for t in (x.strip() for x in f) if t and not t.startswith("#") and _TABLE_RE.fullmatch(t)]
    except OSError:
        return []


def session_tables():
    """herdr_session 列を持つ表の一覧(taskboard/sql/session_tables。tb の sql() と同じものを読む)。"""
    return _name_list(SESSION_TABLES)


def session_compat_views():
    """互換ビューの一覧(taskboard/sql/session_views。020 の alerts / proposals。中身はチケットと小さな表)。"""
    return _name_list(SESSION_VIEWS)


def session_views(c, session):
    """1 つの DB を複数の herdr セッションで共有する(行は herdr_session 列で分ける。taskboard/schema/012)。
    一覧の表のうち DB に herdr_session 列つきで今あるものごとに、同じ名前の一時ビュー(このセッションの行だけ)を作る(tb の sql() と同じ)。
    一時ビューは main の表より先に名前解決されるので、画面側の SQL は表の名前のまま、このセッションの行だけを見る。
    全セッションを見る読み(共通の設定 '*' など)は main.<表> と書く。列が無い古い DB(tb の移行前)はビューを作らず、そのまま読む。"""
    lit = "'" + session.replace("'", "''") + "'"
    want = set(session_tables())
    for (t,) in c.execute("SELECT m.name FROM main.sqlite_master m WHERE m.type='table' AND EXISTS "
                          "(SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session')").fetchall():
        if t in want:
            c.execute(f"CREATE TEMP VIEW {t} AS SELECT * FROM main.{t} WHERE herdr_session={lit}")
    # 互換ビュー(main のビュー)も同じ名前の一時ビューで絞る。020 の前の DB では表なので上で扱われる
    want_v = set(session_compat_views())
    for (t,) in c.execute("SELECT m.name FROM main.sqlite_master m WHERE m.type='view' AND EXISTS "
                          "(SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session')").fetchall():
        if t in want_v:
            c.execute(f"CREATE TEMP VIEW {t} AS SELECT * FROM main.{t} WHERE herdr_session={lit}")


def db_conn():
    path = TB_DB()
    if not os.path.isfile(path):
        return None
    try:
        c = sqlite3.connect(f"file:{quote(path)}?mode=ro", uri=True, timeout=2)
        c.row_factory = sqlite3.Row
        session_views(c, reqctx.session())
        c.execute("PRAGMA query_only=ON")
        return c
    except sqlite3.Error:
        return None


# リクエストの間は接続を使い回す(1 画面で q() を何度も呼んでも、開く・一時ビューを作るのは 1 回)。
# 鍵は (DB のパス, セッション): 一時ビューはセッションごとなので、1 リクエストで複数のセッションを読む画面(/sessions)でも混ざらない。
# スレッドごとに別の文脈(ThreadingHTTPServer)なので、接続をスレッドの間で共有しない。request_db() の外では従来どおり毎回開いて閉じる
_req_conns = contextvars.ContextVar("herdr_webui_db", default=None)


@contextmanager
def request_db():
    conns = {}
    tok = _req_conns.set(conns)
    try:
        yield
    finally:
        _req_conns.reset(tok)
        for c in conns.values():
            if c is not None:
                c.close()


def _conn():
    """(接続, 使い終わったら閉じるか)。"""
    conns = _req_conns.get()
    if conns is None:
        return db_conn(), True
    key = (TB_DB(), reqctx.session())
    c = conns.get(key)
    if c is None:
        c = db_conn()
        if c is not None:
            conns[key] = c
    return c, False


def q_checked(sql, params=()):
    """q と同じだが、照会できたかを返す: (行, 成否)。許可判定で「該当なし」と「読めない」を区別するため。"""
    c, close = _conn()
    if c is None:
        return [], False
    try:
        if "_STATUSES__" in sql:   # 状態の一覧の置き場(__CLOSED_STATUSES__ など)を設定 statuses の値で埋める(stq)
            sql = stq(sql)
        return [dict(r) for r in c.execute(sql, params).fetchall()], True
    except sqlite3.Error:
        return [], False
    finally:
        if close:
            c.close()


def q(sql, params=()):
    """読み取り専用で実行。DB 不在・テーブル欠落・破損でも空リスト。"""
    return q_checked(sql, params)[0]


# ---- 設定(config 表。taskboard/schema/013)。tb の lib/config.sh の config_lookup と同じ順で引く:
# 環境変数で明示したファイル > DB のこのセッションの行 > DB の共通の行(*) > checkout の taskboard/config/<名前> > <名前>.example
CONFIG_KEYS = {"projects": (("TB_CONFIG",), "projects.conf"),
               "approvals": (("WEBUI_APPROVALS", "TB_APPROVALS"), "approvals.conf"),
               "protect": (("TB_PROTECT",), "protect.conf"),
               "labels": (("TB_LABELS",), "labels.conf"),
               "statuses": (("TB_STATUSES",), "statuses.conf")}
CONFIG_MAX = 262144


def _read_text(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read(CONFIG_MAX + 1)
    except OSError:
        return None


def config_text(key):
    """(本文, 出どころ)。出どころは file:<パス> / db:<セッション> / db:* / example:<パス> / none。どこにも無い・読めなければ本文は None。
    本文は CONFIG_MAX + 1 文字まで(それを超えたかで「大きすぎる」を見分けられる)。"""
    envs, name = CONFIG_KEYS[key]
    for ev in envs:
        path = os.environ.get(ev)
        if path:
            return _read_text(path), "file:" + path
    sess = reqctx.session()
    rows, _ok = q_checked("SELECT herdr_session AS s, substr(body, 1, ?) AS body FROM main.config WHERE key=? AND herdr_session IN (?, '*') "
                          "ORDER BY herdr_session='*' LIMIT 1", (CONFIG_MAX + 1, key, sess))
    if rows:
        return rows[0]["body"], "db:" + rows[0]["s"]
    cdir = os.environ.get("TB_CONFIG_DIR") or os.path.join(HERDR_ROOT, "taskboard", "config")
    for path, kind in ((os.path.join(cdir, name), "file:"), (os.path.join(cdir, name + ".example"), "example:")):
        text = _read_text(path)
        if text is not None:
            return text, kind + path
    return None, "none"


def tail_lines(path, n, maxbytes=262144):
    try:
        with open(path, "rb") as f:
            f.seek(0, os.SEEK_END)
            size = f.tell()
            f.seek(max(0, size - maxbytes))
            data = f.read()
    except OSError:
        return []
    lines = data.decode("utf-8", "replace").splitlines()
    if size > maxbytes and lines:
        lines = lines[1:]  # 途中から始まる行は捨てる
    return lines[-n:]


def parse_jsonl(lines):
    out = []
    for ln in lines:
        try:
            d = json.loads(ln)
            if isinstance(d, dict):
                out.append(d)
        except ValueError:
            continue
    return out


def has_resolution():
    """comments に resolved_at があるか(スキーマ 003 より前の DB では無い。tb を一度動かせば足される)。"""
    return any(r.get("name") == "resolved_at" for r in q("PRAGMA table_info(comments)"))


def needs_human():
    """承認待ち: 未対応の question/blocked の報告(未完了タスク)、pending-human の代行依頼、blocked の agent。"""
    comments = q(
        "SELECT c.*, t.status AS task_status, t.title AS task_title FROM comments c "
        "LEFT JOIN tasks t ON t.id=c.task_id WHERE c.kind IN ('question','blocked') "
        "AND COALESCE(t.status,'') NOT IN (__CLOSED_STATUSES__)" + (" AND c.resolved_at IS NULL" if has_resolution() else "") +
        " ORDER BY c.id DESC LIMIT 100")
    proxies = q("SELECT * FROM proxy_requests WHERE status='pending-human' ORDER BY id DESC LIMIT 100")
    agents, _ = get_agents()
    blocked = [a for a in agents if a.get("agent_status") == "blocked"]
    return comments, proxies, blocked


def badge_count():
    c, p, b = needs_human()
    return len(c) + len(p) + len(b)


def parse_ts(s):
    if not s:
        return None
    try:
        d = datetime.fromisoformat(str(s).replace("Z", "+00:00"))
        return d if d.tzinfo else d.replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def activity_for(a):
    sid = agent_session(a)
    rows = []
    if sid:
        rows = q("SELECT * FROM agent_activity WHERE session_id=?", (sid,))
    if not rows and a.get("pane_id"):
        rows = q("SELECT * FROM agent_activity WHERE pane_id=? ORDER BY updated_at DESC LIMIT 1", (a["pane_id"],))
    return rows[0] if rows else None


def stalled(a, act):
    if a.get("agent_status") != "working" or not act:
        return False
    p, s = parse_ts(act.get("last_prompt_at")), parse_ts(act.get("last_stop_at"))
    if not p or (s and s >= p):
        return False
    return datetime.now(timezone.utc) - p > timedelta(minutes=STALL_MIN)


def tasks_for_agent(a):
    name, sid = a.get("name"), agent_session(a)
    rows = []
    if name:
        rows += q("SELECT * FROM tasks WHERE owner_agent=? AND status NOT IN (__CLOSED_STATUSES__) ORDER BY updated_at DESC", (name,))
    if sid:
        rows += [r for r in q("SELECT * FROM tasks WHERE session_id=? AND status NOT IN (__CLOSED_STATUSES__) ORDER BY updated_at DESC", (sid,))
                 if r["id"] not in {x["id"] for x in rows}]
    return rows


# 止めてよいかの実効値(tb の killable_of と同じ判定): 判定後に新しい入力があった・会話が変わったら stale
KILLABLE_SQL = """SELECT a.*, CASE WHEN a.killable!='yes' THEN a.killable
      WHEN a.killable_session IS NOT NULL AND a.session_id IS NOT NULL AND a.killable_session!=a.session_id THEN 'stale'
      WHEN EXISTS (SELECT 1 FROM agent_activity aa WHERE (aa.session_id=a.session_id OR aa.pane_id=a.pane_id)
                   AND julianday(aa.last_prompt_at) > julianday(a.killable_at)) THEN 'stale'
      ELSE 'yes' END AS killable_now,
    (SELECT COUNT(*) FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (__HELD_STATUSES__)) AS open_tasks
    FROM agents a"""


def protect_rules():
    """config の protect(tb の lib/agents.sh の protect_reason と同じ書式)。[(パターン, 理由)]。
    1 行 1 つ: <名前の glob> [= 理由] / role:<役割の glob> [= 理由]。# 以降はコメント。無ければ空(何も保護しない)"""
    text, _src = config_text("protect")
    rules = []
    for line in (text or "").splitlines():
        line = line.split("#", 1)[0].strip()
        if not line:
            continue
        k, _sep, v = line.partition("=")
        k = k.strip()
        rules.append((k, v.strip() or "protect: " + k))
    return rules


# ---- チケットの状態(設定 statuses。tb の lib/core.sh の statuses_load と同じ読み方)----
# 1 行 1 つ「<状態> <分類> [= 説明]」。分類は STATUS_CATEGORIES のどれか。無い・不正なら組み込みの 6 つ。
# 組み込みの名前は、設定に書かなくても組み込みの分類として数える(アラート・提案のチケットは DB のトリガーがこの名前で書く)
STATUS_CATEGORIES = ("todo", "active", "review", "blocked", "done", "cancelled")
STATUS_BUILTIN = (("todo", "todo"), ("in_progress", "active"), ("review", "review"),
                  ("done", "done"), ("blocked", "blocked"), ("cancelled", "cancelled"))
_STATUS_BAD = re.compile(r"[,\"'`\\*?\[\]]")


def parse_statuses(text):
    """本文 → [(状態, 分類, 説明)](書いた順)。不正な行・必要な分類の欠けがあれば None(tb は書く前に断る)。"""
    out, seen = [], set()
    for line in (text or "").splitlines():
        line = line.split("#", 1)[0].strip()
        if not line:
            continue
        line, _sep, desc = line.partition("=")
        parts = line.split()
        if len(parts) != 2:
            return None
        k, c = parts
        if len(k) > 30 or _STATUS_BAD.search(k) or c not in STATUS_CATEGORIES or k in seen:
            return None
        if dict(STATUS_BUILTIN).get(k, c) != c:   # 組み込みの名前は組み込みの分類だけ(tb と同じ)
            return None
        seen.add(k)
        out.append((k, c, desc.strip()))
    cats = {c for _k, c, _d in out}
    if not out or not {"todo", "active", "done"} <= cats:
        return None
    return out


_STATUS_CACHE = {}
STATUS_CACHE_SEC = 2.0   # 1 画面の中で何度も引くので、セッションごとに短いあいだ覚えておく


def statuses():
    """(設定した状態 [(状態, 分類, 説明)], {状態: 分類}(組み込みの名前を含む))。"""
    key = (reqctx.session(), os.environ.get("TB_STATUSES", ""), os.environ.get("TB_CONFIG_DIR", ""), TB_DB())
    now = time.monotonic()
    hit = _STATUS_CACHE.get(key)
    if hit and now - hit[0] < STATUS_CACHE_SEC:
        return hit[1]
    val = _statuses_read()
    _STATUS_CACHE.clear()
    _STATUS_CACHE[key] = (now, val)
    return val


def _statuses_read():
    text, _src = config_text("statuses")
    rows = parse_statuses(text) if text is not None else None
    if rows is None:
        rows = [(k, c, "") for k, c in STATUS_BUILTIN]
    cat = {k: c for k, c, _d in rows}
    for k, c in STATUS_BUILTIN:
        cat.setdefault(k, c)
    return rows, cat


def status_names():
    """設定した状態の名前(書いた順)。絞り込みの選択肢と件数の並び。"""
    return [k for k, _c, _d in statuses()[0]]


def board_columns():
    """ボードの列: 分類の順、同じ分類は書いた順(tb board と同じ)。"""
    rows = statuses()[0]
    return [k for c0 in STATUS_CATEGORIES for k, c, _d in rows if c == c0]


def st_names(*cats):
    """その分類の状態(設定した順の後に組み込みの名前)。"""
    rows, cat = statuses()
    out = []
    for k in [k for k, _c, _d in rows] + [k for k, _c in STATUS_BUILTIN]:
        if k not in out and cat.get(k) in cats:
            out.append(k)
    return out


def st_sql(*cats):
    """SQL の IN (...) の中身('a','b')。当たる状態が無ければ空集合(NULL だと NOT IN が偽になる)。"""
    return ",".join("'" + k.replace("'", "''") + "'" for k in st_names(*cats)) or "SELECT NULL WHERE 0"


# SQL の中の状態の一覧の置き場。q / q_checked が実行の前に埋める(SQL を 1 つの文字列のまま書けるように。tests/test_webui_queries.py)
#   __CLOSED_STATUSES__ = 分類 done・cancelled、__HELD_STATUSES__ = active・review・blocked(担当が持っている)、
#   __<分類>_STATUSES__ = その分類だけ(__DONE_STATUSES__ など)。taskboard/sql/agent_edges.sql も同じ置き場を使う
STATUS_GROUPS = {"CLOSED": ("done", "cancelled"), "HELD": ("active", "review", "blocked")}
_ST_TOKEN = re.compile(r"__([A-Z]+)_STATUSES__")


def stq(sql):
    def one(m):
        g = m.group(1)
        cats = STATUS_GROUPS.get(g) or ((g.lower(),) if g.lower() in STATUS_CATEGORIES else None)
        if cats is None:
            raise ValueError("unknown status token: " + m.group(0))
        return st_sql(*cats)
    return _ST_TOKEN.sub(one, sql)


def st_main(cat):
    """その分類の代表の名前(設定でその分類の最初に書いたもの。無ければ組み込みの名前)。"""
    for k, c, _d in statuses()[0]:
        if c == cat:
            return k
    return next(k for k, c in STATUS_BUILTIN if c == cat)


def defined_labels():
    """config の labels(tb の lib/tasks.sh の defined_labels と同じ書式)。[(ラベル, 説明)] を書いた順に。
    1 行 1 つ: <ラベル> [= 説明]。# 以降はコメント。無ければ空(ラベルは自由に付けられる)"""
    text, _src = config_text("labels")
    out, seen = [], set()
    for line in (text or "").splitlines():
        line = line.split("#", 1)[0].strip()
        if not line:
            continue
        k, _sep, v = line.partition("=")
        k = k.strip()
        if k and k not in seen:
            seen.add(k)
            out.append((k, v.strip()))
    return out


def protect_reason(name, reg_role=None, rules=None):
    """保護されていれば理由(上から見て最初に一致した行)、されていなければ None。"""
    from . import graph
    for k, why in protect_rules() if rules is None else rules:
        if k.startswith("role:"):   # tb と同じく 1 つの役割の glob として、持っている役割のどれかと比べる
            hit = any(fnmatchcase(r, k[5:]) for r in graph.roles_of(name, reg_role))
        else:
            hit = fnmatchcase(name, k)
        if hit:
            return why
    return None


def _with_protect(rows):
    """保護された agent は killable_now を protected に、protect_reason に理由を入れる(tb の killable_of と同じ)。"""
    rules = protect_rules()
    if rules:
        for r in rows:
            why = protect_reason(r["name"], r.get("role"), rules)
            if why:
                r["killable_now"], r["protect_reason"] = "protected", why
    return rows


def registry_row(reg, name):
    """agent_registry() の行。台帳に行が無くても(古い DB・同期前)、protect に一致すれば保護中の行を返す。"""
    r = reg.get(name or "")
    if r is None and name:
        why = protect_reason(name)
        if why:
            r = {"name": name, "killable_now": "protected", "protect_reason": why}
    return r


def agent_registry():
    """tb の台帳(agents)。{名前: 行}。スキーマ 004 より前の DB では空。"""
    return {r["name"]: r for r in _with_protect(q(KILLABLE_SQL + " ORDER BY a.name"))}


def stopped_agents():
    return _with_protect(q(KILLABLE_SQL + " WHERE a.state='stopped' ORDER BY a.stopped_at DESC LIMIT 50"))


def agent_ops(status=None, limit=20):
    if status:
        return q("SELECT * FROM agent_ops WHERE status=? ORDER BY id DESC LIMIT ?", (status, limit))
    return q("SELECT * FROM agent_ops ORDER BY id DESC LIMIT ?", (limit,))


NOTIFY_POLL_SEC = 15
NOTIFY_MAX = 100


def _short(s, n=200):
    s = clean("" if s is None else s).strip()
    return s[:n] + ("…" if len(s) > n else "")


OUTBOX_LIST_MAX = 100
QUEUED_WARN_SEC = 60


def outbox_rows():
    """outbox の一覧(読み取り専用)。新しい順に最大 100 件。text は先頭 80 文字だけ。

    age_sec は created_at からの経過秒(解釈できなければ None)。stuck は queued のまま QUEUED_WARN_SEC 超。
    """
    now = datetime.now(timezone.utc)
    out = []
    for o in q("SELECT id, from_name, target, kind, task_id, status, text, created_at FROM outbox ORDER BY id DESC LIMIT ?",
               (OUTBOX_LIST_MAX,)):
        t = parse_ts(o.get("created_at"))
        age = max(0, int((now - t).total_seconds())) if t else None
        out.append({"id": o["id"], "from_name": clean(o["from_name"]), "target": clean(o["target"]), "kind": clean(o["kind"]),
                    "task_id": clean(o.get("task_id") or ""), "status": clean(o["status"]), "age_sec": age,
                    "text": _short(o["text"], 80),
                    "stuck": o["status"] == "queued" and age is not None and age > QUEUED_WARN_SEC})
    return out


def failed_jobs():
    """最後の実行(ran/failed/sent)が failed のジョブ。[(job, record)]。"""
    try:
        names = sorted(n[:-4] for n in os.listdir(SCHED_LOGS()) if n.endswith(".log") and JOB_RE.fullmatch(n[:-4]))
    except OSError:
        return []
    out = []
    for n in names:
        recs = parse_jsonl(tail_lines(os.path.join(SCHED_LOGS(), n + ".log"), 200))
        ran = [r for r in recs if r.get("action") in ("ran", "failed", "sent")]
        if ran and ran[-1].get("action") == "failed":
            out.append((n, ran[-1]))
    return out


WEBUI_RULE_HOURS = 24


def urgent_items():
    """10 分のまとめ送信を待たずに知らせるもの。各要素は通知 1 件(id は同じ事象なら同じ値)。

    値はすべて clean 済みのプレーンテキスト。href は常にこのサーバー内の絶対パス。
    """
    def load():
        comments, proxies, blocked = needs_human()
        items = []
        for c in comments:
            items.append({"id": f"c:{c['id']}", "kind": "comment_" + str(c["kind"]),
                          "label": "質問" if c["kind"] == "question" else "ブロック報告",
                          "title": _short(f"{c['task_id']} {c.get('task_title') or ''}", 80),
                          "body": _short(f"{c['from_name']}: {c['text']}"),
                          "how": _short((f"{c['from_name']} に答える: 手元で tb send {c['from_name']} --kind answer --task {c['task_id']} --text '答え'"
                                         if c["kind"] == "question" else
                                         f"止まっている原因を取り除くか、{c['from_name']} に指示を送る(手元で tb send {c['from_name']} --task {c['task_id']} --text '指示')")
                                        + f"。済んだら「報告・承認待ち」の「対応済みにする」(手元なら tb resolve C-{c['id']})", 300),
                          "href": "/tasks/" + quote(str(c["task_id"]), safe=""), "ts": clean(c.get("created_at") or "")})
        for r in proxies:
            items.append({"id": f"p:{r['id']}", "kind": "proxy_pending", "label": "代行依頼(人の判断待ち)",
                          "title": _short(f"{r['task_id']} {r.get('effect') or ''}", 80),
                          "body": _short(f"{r['cmd']} / 理由: {r['why']}"),
                          "how": _short(f"agent の代わりに人が実行するか決める。実行したら手元で tb proxy-resolve {r['id']} --status done --result '結果'、断るなら --status denied", 300),
                          "href": "/tasks/" + quote(str(r["task_id"]), safe=""), "ts": clean(r.get("updated_at") or "")})
        for al in q("SELECT * FROM alerts WHERE status='analyzed' AND severity IN ('critical','high') ORDER BY id DESC LIMIT 50"):
            items.append({"id": f"r:{al['id']}:{clean(al.get('analyzed_at'))}", "kind": "alert_result",
                          "label": f"アラート解析結果({clean(al['severity'])})",
                          "title": _short(f"A-{al['id']} {al['title']}", 80),
                          "body": _short(al.get("result_summary") or ""),
                          "how": f"全文は手元で tb alerts A-{al['id']}。対応したら tb alert-close A-{al['id']}",
                          "href": "/notifications", "ts": clean(al.get("analyzed_at") or "")})
        for al in q("SELECT * FROM alerts WHERE status='awaiting-approval' ORDER BY id DESC LIMIT 50"):
            items.append({"id": f"w:{al['id']}", "kind": "alert_approval", "label": "アラート(解析役の起動待ち)",
                          "title": _short(f"A-{al['id']} {al['title']}", 80),
                          "body": _short(al.get("body") or ""),
                          "how": f"解析役を起動してよければ手元で tb alert-approve A-{al['id']}(今後も自動なら --always)",
                          "href": "/notifications", "ts": clean(al.get("last_seen") or "")})
        for o in q("SELECT * FROM outbox WHERE status='pending' ORDER BY id DESC LIMIT 100"):
            items.append({"id": f"o:{o['id']}", "kind": "send_pending", "label": "送信の承認待ち",
                          "title": _short(f"O-{o['id']} {o['from_name']} → {o['target']} ({o['kind']})", 80),
                          "body": _short(o["text"]),
                          "how": f"送ってよければ手元で tb approve O-{o['id']}(今後も許可なら --always)、止めるなら tb reject O-{o['id']}",
                          "href": "/notifications", "ts": clean(o.get("created_at") or "")})
        for o in agent_ops("pending", NOTIFY_MAX):
            items.append({"id": f"l:{o['id']}", "kind": "agent_op_pending", "label": "agent 操作の承認待ち",
                          "title": _short(f"L-{o['id']} {o['requested_by']} → {o['op']} {o['target']}", 80),
                          "body": _short(o.get("args") or ""),
                          "how": f"Web UI の agent 一覧の「承認待ちの操作」で承認か却下(手元なら tb agent-approve L-{o['id']} / tb agent-reject L-{o['id']})",
                          "href": "/agents#ops", "ts": clean(o.get("created_at") or "")})
        agents, _ = get_agents()
        for a in blocked:
            k = agent_key(a)
            items.append({"id": "a:" + clean(k), "kind": "agent_blocked", "label": "承認待ちの agent",
                          "title": _short(k, 80), "body": _short(a.get("terminal_title_stripped") or a.get("terminal_title") or "人の対応待ち"),
                          "how": "agent の画面の「承認待ちの内容」で何を求めているか確かめ、その agent の herdr の pane で許可か拒否を選ぶ",
                          "href": f"/agents/{quote(k, safe='')}#approval", "ts": ""})
        for a in agents:
            act = activity_for(a)
            if stalled(a, act):
                k = agent_key(a)
                items.append({"id": f"s:{clean(k)}:{clean(act.get('last_prompt_at'))}", "kind": "agent_stalled",
                              "label": f"滞留({STALL_MIN} 分超)", "title": _short(k, 80),
                              "body": _short(f"working のまま。最終入力 {act.get('last_prompt_at')}"),
                              "how": "agent の画面で直近の出力を見て、止まっていれば pane で続きを指示する(要らなければ停止)",
                              "href": f"/agents/{quote(k, safe='')}", "ts": clean(act.get("last_prompt_at") or "")})
        # 通知の規則(taskboard/lib/notifyrules.sh)で届け先に webui を書いたチケットの通知。直近 WEBUI_RULE_HOURS 時間
        for ev in q("SELECT id, ref, text, created_at FROM notify_events WHERE type='ticket' AND (' ' || sinks || ' ') LIKE '% webui %'"
                    " AND julianday(created_at) >= julianday('now') - ? / 24.0 ORDER BY id DESC LIMIT 50", (WEBUI_RULE_HOURS,)):
            text = clean(ev.get("text") or "")
            m = re.search(r"\bT-[A-Za-z0-9_]+-[0-9]+\b", text)
            items.append({"id": f"n:{ev['id']}", "kind": "ticket_rule", "label": "チケット(通知の規則)",
                          "title": _short(text.replace("[チケット] ", "", 1), 80), "body": _short(text),
                          "how": "規則は手元で tb notify rules。チケットを開いて確かめる",
                          "href": ("/tasks/" + quote(m.group(0), safe="")) if m else "/tasks", "ts": clean(ev.get("created_at") or "")})
        rem = reminders_last()
        for it in (rem["items"] if rem else []):
            if it["p"] != 1:
                continue
            items.append({"id": f"rm:{it['key']}:{it['due']}", "kind": "reminder_due", "label": "期限(忘れているかも)",
                          "title": _short(it["what"], 80), "body": _short(f"期限 {it['due'] or '-'} / 誰の番: {it['who'] or '-'} / {it['ref']}"),
                          "how": "期限を過ぎた・今日か明日が期限。片付けるか、期限を見直す(一覧は「報告・承認待ち」の「忘れているかも」)",
                          "href": reminder_href(it) or "/inbox#reminders", "ts": it["due"]})
        for n, r in failed_jobs():
            items.append({"id": f"j:{n}:{clean(r.get('ts'))}", "kind": "job_failed", "label": "定期実行の失敗",
                          "title": n, "body": _short(r.get("reason") or "failed"),
                          "how": f"定期実行の画面で理由と設定を確かめる(記録は scheduler/logs/{n}.log)",
                          "href": "/jobs/" + quote(n), "ts": clean(r.get("ts") or "")})
        return items[:NOTIFY_MAX]
    return cached("urgent", load)


# ---- 保留した送信(inbox 表。tb send の delivery=hold)と、忘れているかもしれないもの(tb reminders --publish の結果)。読み取り専用
HELD_MAX = 100
HELD_TEXT_MAX = 20000
_REM_PRIO = {1: "急ぎ", 2: "人の番", 3: "停滞", 4: "待ち・予定"}
_REM_STATUS = ("new", "changed", "continuing")


def has_inbox():
    """inbox 表(受け手の列つき)があるか。保留の配送より前の DB では無い。"""
    return any(r.get("name") == "recipient" for r in q("PRAGMA table_info(inbox)"))


# inbox は別の変更で足す表。tests/test_webui_queries.py(本物のスキーマで SELECT を EXPLAIN する)が、
# 表の無いスキーマでも落ちないよう、SELECT の文字列を FROM の前で分けて書く(has_inbox() で確かめてから読む)。
_HELD_COLS = "SELECT id, outbox_id, from_name, recipient, kind, task_id, created_at, read_at"


def held_messages():
    """保留した送信の一覧。新しい順に最大 HELD_MAX 件。本文(text)は返さない(見るのは held_message で 1 件ずつ)。
    セッションは明示して絞る(inbox が一時ビューの一覧に無い DB でも、別のセッションの行を出さない)。"""
    if not has_inbox():
        return []
    rows = q(_HELD_COLS + " FROM main.inbox WHERE herdr_session=? ORDER BY id DESC LIMIT ?", (reqctx.session(), HELD_MAX))
    return [{k: (v if k in ("id", "outbox_id") else clean("" if v is None else v)) for k, v in r.items()} for r in rows]


def held_message(hid):
    """1 件の本文つき(このセッションの行だけ)。無ければ None。"""
    if not has_inbox():
        return None
    rows = q(_HELD_COLS + ", text FROM main.inbox WHERE herdr_session=? AND id=?", (reqctx.session(), hid))
    if not rows:
        return None
    r = rows[0]
    out = {k: (v if k in ("id", "outbox_id") else clean("" if v is None else v)) for k, v in r.items()}
    out["text"] = out["text"][:HELD_TEXT_MAX]
    return out


def reminders_last():
    """tb reminders --publish が残した最新のまとめ(notify_seen の reminders:last)。無い・壊れていれば None。
    値はすべて clean 済み。p は 1〜4、status は new / changed / continuing のどれか(それ以外の項目は捨てる)。"""
    rows = q("SELECT value, updated_at FROM notify_seen WHERE key='reminders:last'")
    if not rows:
        return None
    try:
        d = json.loads(rows[0]["value"])
    except (TypeError, ValueError):
        return None
    if not isinstance(d, dict) or not isinstance(d.get("items"), list):
        return None
    items = []
    for it in d["items"]:
        if not isinstance(it, dict) or it.get("p") not in _REM_PRIO or it.get("status") not in _REM_STATUS:
            continue
        day = it.get("day")
        items.append({"p": it["p"], "label": _REM_PRIO[it["p"]], "status": it["status"],
                      "day": day if isinstance(day, int) and day > 0 else 1,
                      **{k: _short(it.get(k), 120) for k in ("key", "what", "who", "due", "ref", "age")}})
    removed = d.get("removed") if isinstance(d.get("removed"), list) else []
    return {"at": _short(d.get("at"), 40), "full": d.get("full") is True, "removed": len(removed), "items": items}


def reminder_href(it):
    """項目の行き先(既存の画面へ。同じものをこの欄で繰り返さない)。無ければ None。"""
    k, ref = it.get("key", ""), it.get("ref", "")
    if k.startswith(("q:", "o:", "p:", "a:")):
        return "/inbox"
    if k.startswith("l:"):
        return "/agents#ops"
    if k.startswith("queue:"):
        return "/outbox"
    if k.startswith("inbox:") and ref.startswith("I-") and ref[2:].isdigit():
        return "/inbox?held=" + ref[2:]
    if k.startswith(("stall:", "due:T-")) and re.fullmatch(r"T-[A-Za-z0-9_-]{1,80}", ref):
        return "/tasks/" + quote(ref, safe="")
    return None


# ---- 送信履歴(messages 表。tb が送ろうとしたものすべて。読み取り専用)
LEDGER_MAX = 100
LEDGER_RESULTS = ("sent", "busy", "failed")


def ledger_kinds():
    return [r["kind"] for r in q("SELECT DISTINCT kind FROM messages ORDER BY kind")]


def _ledger_where(target, d_from, d_to, kind=None, result=None, hide_failed=False):
    w, a = [], []
    if target:
        w.append("target=?"); a.append(target)
    if d_from:
        w.append("substr(created_at,1,10)>=?"); a.append(d_from)
    if d_to:
        w.append("substr(created_at,1,10)<=?"); a.append(d_to)
    if kind:
        w.append("kind=?"); a.append(kind)
    if result:
        w.append("result=?"); a.append(result)
    elif hide_failed:
        w.append("result!='failed'")
    return (" WHERE " + " AND ".join(w)) if w else "", a


def ledger_summary(target, d_from, d_to):
    """種別 × 結果の件数。宛先と期間だけで絞る(種別・結果の絞り込みでは変えない)。"""
    where, a = _ledger_where(target, d_from, d_to)
    return q("SELECT kind, result, COUNT(*) AS n FROM messages" + where + " GROUP BY kind, result ORDER BY kind, result", a)


def ledger_rows(target, d_from, d_to, kind, result, include_failed=False):
    """新しい順に最大 LEDGER_MAX 件(result が空なら failed を除く)。本文は 1 行目の先頭 200 文字だけ返す。"""
    where, a = _ledger_where(target, d_from, d_to, kind, result, hide_failed=not result and not include_failed)
    out = []
    for r in q("SELECT id, kind, ref, target, body, result, created_at FROM messages" + where + " ORDER BY id DESC LIMIT ?",
               a + [LEDGER_MAX]):
        first = clean(r["body"] or "").split("\n", 1)[0].strip()
        out.append({"id": r["id"], "kind": clean(r["kind"]), "ref": clean(r.get("ref") or ""), "target": clean(r["target"]),
                    "result": clean(r["result"]), "created_at": clean(r["created_at"]),
                    "first": first[:200] + ("…" if len(first) > 200 else "")})
    return out


def approval_history(limit=50):
    return q("SELECT id, pattern, value, created_at FROM approval_changes ORDER BY id DESC LIMIT ?", (limit,))
