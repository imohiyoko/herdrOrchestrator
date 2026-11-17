"""待ち受けたポートの記録(STATE_DIR/gateway.json)。

Web UI は 1 プロセスで、既定 8800(使用中なら +1 ずつずらす)で待ち受ける。実際に待ち受けたポートをここに書き、終了時に消す。
setup の確認(setup/check.sh の webui:port)と、二重起動の検出が読む。
同じユーザーだけが読み書きする(ディレクトリ 700・ファイル 600)。読むときも、自分の所有の通常ファイルで、中身が形式どおりのものだけを使う。
"""

import json
import os
import socket

from .config import STATE_DIR

MAX_ENTRY = 4096


def _gateway_path():
    return os.path.join(STATE_DIR(), "gateway.json")


def write_gateway(port):
    d = os.path.dirname(_gateway_path())
    os.makedirs(d, mode=0o700, exist_ok=True)
    tmp = _gateway_path() + f".{os.getpid()}.tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump({"port": port, "pid": os.getpid()}, f)
    os.replace(tmp, _gateway_path())


def read_gateway(check_alive=True):
    """動いている Web UI {port, pid} か None(check_alive=False なら、動いているかは見ずに記録を返す)。"""
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


def pid_ok(entry):
    """書いたプロセスが(自分のユーザーで)まだ動いているか。強制終了で消し損ねた記録を「動いている」と見ないため。"""
    pid = entry.get("pid")
    if not pid or pid <= 0:   # 0 や負の数は kill がプロセスグループに向く
        return False
    try:
        os.kill(pid, 0)
        return True
    except OSError:   # 無い(ProcessLookupError)・別ユーザーのプロセス(PermissionError)
        return False


def alive(entry, timeout=0.3):
    """書いたプロセスが動いていて、そのポートで待ち受けているか。"""
    if not pid_ok(entry):
        return False
    try:
        with socket.create_connection(("127.0.0.1", entry["port"]), timeout=timeout):
            return True
    except OSError:
        return False
