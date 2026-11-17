"""部品で共通の既定値(lib/herdr-env.sh と同じ内容)。片方を変えたらもう片方も変える。"""
import os
import re

HERDR_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_BIN = "herdr"
DEFAULT_TB_DB = os.path.join(HERDR_ROOT, "taskboard", "data", "tasks.db")
SCHEMA_DIR = os.path.join(HERDR_ROOT, "taskboard", "schema")


def herdr_bin():
    return os.environ.get("HERDR_BIN") or DEFAULT_BIN


def tb_db():
    """tb と同じ解決順: TB_DB → 既定。webui はさらに手前で WEBUI_TB_DB を見る。"""
    return os.environ.get("TB_DB") or DEFAULT_TB_DB


def schema_version():
    """taskboard/schema にある最新の版(NNN_*.sql の NNN の最大)。"""
    nums = [int(f[:3]) for f in os.listdir(SCHEMA_DIR) if f[:3].isdigit() and f.endswith(".sql")]
    return max(nums, default=0)


_SESSION_RE = re.compile(r"[A-Za-z0-9_][A-Za-z0-9._-]{0,63}")
_SOCK_NAMED = re.compile(r".*/sessions/([A-Za-z0-9_][A-Za-z0-9._-]*)/herdr\.sock")
_SOCK_DEFAULT = re.compile(r".*/herdr/herdr\.sock")


def session_file():
    """checkout ごとにセッション名を固定するファイル(lib/herdr-env.sh の herdr_session_file と同じ)。"""
    return os.environ.get("HERDR_SESSION_FILE") or os.path.join(HERDR_ROOT, "herdr-session")


def session_file_value():
    """ファイルの 1 行目の名前(# 以降と前後の空白は無視)。無い・不正なら None。"""
    try:
        with open(session_file(), encoding="utf-8", errors="replace") as f:
            v = f.readline()
    except OSError:
        return None
    v = v.split("#", 1)[0].strip()
    return v if _SESSION_RE.fullmatch(v) else None


def session_name():
    """HERDR_SOCKET_PATH > HERDR_SESSION > herdr-session ファイル > default(lib/herdr-env.sh と同じ)。"""
    p = os.environ.get("HERDR_SOCKET_PATH") or ""
    m = _SOCK_NAMED.fullmatch(p)
    if m:
        return m.group(1)
    if _SOCK_DEFAULT.fullmatch(p):
        return "default"
    return os.environ.get("HERDR_SESSION") or session_file_value() or "default"


def apply_session():
    """ファイルで決まったときだけ HERDR_SESSION に入れる(子プロセスの herdr / tb に渡すため)。"""
    p = os.environ.get("HERDR_SOCKET_PATH") or ""
    if _SOCK_NAMED.fullmatch(p) or _SOCK_DEFAULT.fullmatch(p) or os.environ.get("HERDR_SESSION"):
        return
    v = session_file_value()
    if v:
        os.environ["HERDR_SESSION"] = v
        os.environ["HERDR_SESSION_FROM_FILE"] = v
