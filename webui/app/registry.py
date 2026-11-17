"""動いている Web UI インスタンスの一覧(ホームの REGISTRY_DIR に 1 インスタンス 1 ファイル)。

インスタンスは起動時に自分の名前・ポート・置き場所(base = /s/<名前>)を書き、終了時に消す。gateway と「セッション一覧」が読む。
同じユーザーだけが読み書きする(ディレクトリ 700・ファイル 600)。読むときも、自分の所有の通常ファイルで、
中身が形式どおりのものだけを使う(ポートは 127.0.0.1 にだけつなぐので、外へは向かない)。
"""

import json
import os
import re
import socket

from .config import INSTANCE_RE, REGISTRY_DIR

MAX_ENTRY = 4096


def _path(name):
    return os.path.join(REGISTRY_DIR(), name + ".json")


def register(name, port, base):
    d = REGISTRY_DIR()
    os.makedirs(d, mode=0o700, exist_ok=True)
    tmp = _path(name) + f".{os.getpid()}.tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump({"name": name, "port": port, "base": base, "pid": os.getpid()}, f)
    os.replace(tmp, _path(name))


def unregister(name):
    """自分が書いたもの(pid が同じ)だけ消す。"""
    e = read_entry(name)
    if e and e.get("pid") == os.getpid():
        try:
            os.remove(_path(name))
        except OSError:
            pass


def read_entry(name):
    if not INSTANCE_RE.fullmatch(name or ""):
        return None
    p = _path(name)
    try:
        st = os.lstat(p)
        if not os.path.isfile(p) or os.path.islink(p) or st.st_uid != os.getuid() or st.st_size > MAX_ENTRY:
            return None
        with open(p, encoding="utf-8") as f:
            d = json.load(f)
    except (OSError, ValueError):
        return None
    if not isinstance(d, dict) or d.get("name") != name:
        return None
    port, base = d.get("port"), d.get("base")
    if not isinstance(port, int) or isinstance(port, bool) or not 1 <= port <= 65535:
        return None
    if base != f"/s/{name}":   # 入口の後ろに置かない旧い形(base が空)は扱わない
        return None
    return {"name": name, "port": port, "base": base, "pid": d.get("pid") if isinstance(d.get("pid"), int) and not isinstance(d.get("pid"), bool) else None}


def entries():
    """[entry]。名前順。読めないものは飛ばす。"""
    try:
        names = sorted(f[:-5] for f in os.listdir(REGISTRY_DIR()) if f.endswith(".json"))
    except OSError:
        return []
    return [x for x in (read_entry(n) for n in names) if x]


def pid_ok(entry):
    """書いたプロセスが(自分のユーザーで)まだ動いているか。強制終了で消し損ねた古い行の、ポートの使い回しへ中継しないため。"""
    pid = entry.get("pid")
    if not pid or pid <= 0:
        return False
    try:
        os.kill(pid, 0)
        return True
    except OSError:   # 無い(ProcessLookupError)・別ユーザーのプロセス(PermissionError)
        return False


def _gateway_path():
    return os.path.join(os.path.dirname(REGISTRY_DIR()), "gateway.json")


def write_gateway(port):
    """入口が実際に待ち受けたポート(8800 が埋まっていればずれる)。インスタンスの起動ログと setup の確認が読む。"""
    d = os.path.dirname(_gateway_path())
    os.makedirs(d, mode=0o700, exist_ok=True)
    tmp = _gateway_path() + f".{os.getpid()}.tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump({"port": port, "pid": os.getpid()}, f)
    os.replace(tmp, _gateway_path())


def read_gateway(check_alive=True):
    """動いている入口 {port, pid} か None(check_alive=False なら、動いているかは見ずに記録を返す)。"""
    p = _gateway_path()
    try:
        st = os.lstat(p)
        if not os.path.isfile(p) or os.path.islink(p) or st.st_uid != os.getuid() or st.st_size > MAX_ENTRY:
            return None
        with open(p, encoding="utf-8") as f:
            d = json.load(f)
    except (OSError, ValueError):
        return None
    if not isinstance(d, dict):
        return None
    port, pid = d.get("port"), d.get("pid")
    if not isinstance(port, int) or isinstance(port, bool) or not 1 <= port <= 65535 or not isinstance(pid, int) or isinstance(pid, bool):
        return None
    e = {"port": port, "pid": pid}
    return e if not check_alive or alive(e) else None


def clear_gateway():
    """自分(この pid)が書いた記録だけ消す。"""
    g = read_gateway(check_alive=False)
    if g and g["pid"] == os.getpid():
        try:
            os.remove(_gateway_path())
        except OSError:
            pass


def alive(entry, timeout=0.3):
    """書いたプロセスが動いていて、そのポートで待ち受けているか。"""
    if not pid_ok(entry):
        return False
    try:
        with socket.create_connection(("127.0.0.1", entry["port"]), timeout=timeout):
            return True
    except OSError:
        return False


NAME_IN_BASE = re.compile(r"/s/([A-Za-z0-9][A-Za-z0-9._-]{0,63})(/.*)?")
