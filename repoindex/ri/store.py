"""データの置き場所と DB の接続。

- データ(DB)はこのマシンのユーザーごとの決まった場所だけに置く。既定は ${XDG_STATE_HOME:-~/.local/state}/herdr-repoindex。
  REPOINDEX_DIR で変えられるが、git の作業ツリーの中(この checkout を含む)は拒否する(誤ってコミットされないように)。
- ディレクトリは 0700、DB は 0600。umask も 077 にしてから作る(-wal / -shm も他のユーザーから読めない)。
- 読み取りは mode=ro の URI + PRAGMA query_only で開く。書き込みは別の経路(このモジュールの CLI)だけ。
"""
import os
import sqlite3
import stat
import subprocess

MODULE_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HERDR_ROOT = os.path.dirname(MODULE_ROOT)
SCHEMA_DIR = os.path.join(MODULE_ROOT, "schema")
DB_NAME = "index.db"


class StoreError(Exception):
    """利用者に 1 行で返す失敗(スタックトレースは出さない)。"""


def data_dir():
    d = os.environ.get("REPOINDEX_DIR")
    if not d:
        base = os.environ.get("XDG_STATE_HOME") or os.path.join(os.path.expanduser("~"), ".local", "state")
        d = os.path.join(base, "herdr-repoindex")
    return os.path.abspath(d)


def db_path():
    return os.path.join(data_dir(), DB_NAME)


def _inside(child, parent):
    child, parent = os.path.realpath(child), os.path.realpath(parent)
    return child == parent or child.startswith(parent.rstrip(os.sep) + os.sep)


def _nearest_existing(p):
    while not os.path.exists(p):
        up = os.path.dirname(p)
        if up == p:
            break
        p = up
    return p


def check_location(d=None):
    """データの置き場所が git の作業ツリーの外にあるか。違反なら StoreError。"""
    d = d or data_dir()
    if _inside(d, HERDR_ROOT):
        raise StoreError(f"データの置き場所がこの checkout の中です(コミットされるおそれ): {d}")
    probe = _nearest_existing(d)
    try:
        r = subprocess.run(["git", "-C", probe, "rev-parse", "--is-inside-work-tree"],
                           capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.TimeoutExpired):
        return  # git が無ければ判定できない(checkout の中でないことは上で確かめた)
    if r.returncode == 0 and r.stdout.strip() == "true":
        raise StoreError(f"データの置き場所が git の作業ツリーの中です: {d}")


def check_permissions(d=None):
    """ディレクトリと DB が他のユーザーから読めないか。問題の一覧(空なら問題なし)。"""
    d = d or data_dir()
    probs = []
    for p in (d, os.path.join(d, DB_NAME), os.path.join(d, DB_NAME + "-wal"), os.path.join(d, DB_NAME + "-shm")):
        if not os.path.exists(p):
            continue
        mode = stat.S_IMODE(os.stat(p).st_mode)
        if mode & 0o077:
            probs.append(f"{p}: {oct(mode)}(グループ・他者の権限を外す)")
    return probs


def _migrate(con):
    have = con.execute("PRAGMA user_version").fetchone()[0]
    files = sorted(f for f in os.listdir(SCHEMA_DIR) if f[:3].isdigit() and f.endswith(".sql"))
    for f in files:
        n = int(f[:3])
        if n <= have:
            continue
        with open(os.path.join(SCHEMA_DIR, f), encoding="utf-8") as fh:
            sql = fh.read()
        con.executescript("BEGIN;" + sql + f";PRAGMA user_version={n};COMMIT;")


def schema_latest():
    return max((int(f[:3]) for f in os.listdir(SCHEMA_DIR) if f[:3].isdigit() and f.endswith(".sql")), default=0)


def connect_rw():
    """書き込み用。無ければ作る(場所と権限を確かめてから)。"""
    d = data_dir()
    check_location(d)
    old = os.umask(0o077)
    try:
        os.makedirs(d, mode=0o700, exist_ok=True)
        os.chmod(d, 0o700)
        p = db_path()
        con = sqlite3.connect(p, timeout=10)
        os.chmod(p, 0o600)
        con.execute("PRAGMA journal_mode=WAL")
        con.execute("PRAGMA foreign_keys=ON")
        con.execute("PRAGMA busy_timeout=10000")
        _migrate(con)
    finally:
        os.umask(old)
    return con


def connect_ro():
    """読み取り専用。DB が無ければ StoreError(読み取り側は何も作らない)。"""
    p = db_path()
    if not os.path.exists(p):
        raise StoreError("索引がまだありません(repoindex init で作る)")
    con = sqlite3.connect(f"file:{p}?mode=ro", uri=True, timeout=10)
    con.execute("PRAGMA query_only=ON")
    con.execute("PRAGMA busy_timeout=10000")
    have = con.execute("PRAGMA user_version").fetchone()[0]
    if have != schema_latest():
        con.close()
        raise StoreError(f"索引の版が古い({have} < {schema_latest()})。repoindex init で上げる")
    con.row_factory = sqlite3.Row
    return con
