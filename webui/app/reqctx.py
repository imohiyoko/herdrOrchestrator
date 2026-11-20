"""リクエストごとの文脈: この画面がどの herdr セッションのものか。

セッションはプロセスの環境変数ではなくここから読む(DB の行の絞り込み・herdr と tb の呼び出し・URL の BASE)。
ハンドラがリクエストの入口で、URL の /s/<セッション名>/ のセッションを use() し、各所は name() / session() / base() で読む。
use() の外(テストやスクリプトから関数を直接呼ぶとき)は、プロセスの herdr セッションに落とす。
"""

import contextvars
import os
from contextlib import contextmanager

from .config import herdr_env

_cur = contextvars.ContextVar("herdr_webui_request", default=None)


def name():
    """URL の /s/<名前> の名前(= herdr セッション名)。"""
    v = _cur.get()
    return v[0] if v else herdr_env.session_name()


def session():
    """DB の行(herdr_session 列)と、herdr / tb に渡す herdr セッション名。"""
    v = _cur.get()
    return v[1] if v else herdr_env.session_name()


def process_session():
    """このプロセス(と同じ checkout の scheduler・tb)のセッション。URL では変わらない。"""
    return herdr_env.session_name()


def base():
    return f"/s/{name()}"


@contextmanager
def use(name_, session_):
    tok = _cur.set((name_, session_))
    try:
        yield
    finally:
        _cur.reset(tok)


def child_env(env=None):
    """herdr / tb の子プロセスに渡す環境。プロセスのセッションと違うときは HERDR_SESSION で明示する
    (socket のパスは外す: herdr はそちらを優先するため)。同じなら環境はそのまま。"""
    env = dict(os.environ if env is None else env)
    if session() == herdr_env.session_name():
        return env
    env.pop("HERDR_SOCKET_PATH", None)
    env["HERDR_SESSION"] = session()
    if env.get("HERDR_SESSION_FROM_FILE") != env["HERDR_SESSION"]:
        env.pop("HERDR_SESSION_FROM_FILE", None)
    return env
