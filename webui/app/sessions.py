"""この Web UI が画面を返す herdr セッションの一覧。

1 つのプロセスが全セッションの画面を /s/<セッション名>/ で返す。URL に書けるのは、ここに挙がる名前だけ:
DB に行のあるセッション(agents / tasks / config の herdr_session)、herdr のセッションのディレクトリ
(~/.config/herdr/sessions/<名前>/)、default、このプロセスのセッション。
"""

import os
import sqlite3
import threading
import time
from urllib.parse import quote

from .config import SESSION_RE, TB_DB, herdr_env

TTL = 5.0
MAX = 200
# 文はそのまま書く(tests/test_webui_queries.py が本物のスキーマで確かめる)
_QUERIES = ("SELECT DISTINCT herdr_session FROM main.agents", "SELECT DISTINCT herdr_session FROM main.tasks",
            "SELECT DISTINCT herdr_session FROM main.config")
_lock = threading.Lock()
_cache = [0.0, None]


def herdr_sessions_dir():
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config")
    return os.path.join(base, "herdr", "sessions")


def _from_db():
    path = TB_DB()
    if not os.path.isfile(path):
        return set()
    out = set()
    try:
        c = sqlite3.connect(f"file:{quote(path)}?mode=ro", uri=True, timeout=2)
    except sqlite3.Error:
        return out
    try:
        for sql in _QUERIES:
            try:
                out.update(r[0] for r in c.execute(sql).fetchmany(MAX))
            except sqlite3.Error:   # 表・列が無い(古い DB)
                pass
    finally:
        c.close()
    return out


def _from_herdr():
    try:
        return {n for n in os.listdir(herdr_sessions_dir()) if os.path.isdir(os.path.join(herdr_sessions_dir(), n))}
    except OSError:
        return set()


def _collect():
    names = _from_db() | _from_herdr() | {"default", herdr_env.session_name()}
    return sorted(n for n in names if isinstance(n, str) and SESSION_RE.fullmatch(n))[:MAX]


def known():
    """名前の一覧(名前順)。数秒だけ覚える。"""
    now = time.monotonic()
    with _lock:
        if _cache[1] is not None and now - _cache[0] < TTL:
            return _cache[1]
    names = _collect()
    with _lock:
        _cache[0], _cache[1] = time.monotonic(), names
    return names


def forget():
    with _lock:
        _cache[0], _cache[1] = 0.0, None


def is_known(name):
    return bool(SESSION_RE.fullmatch(name or "")) and name in known()
