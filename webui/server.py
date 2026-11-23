#!/usr/bin/env python3
"""herdr Web UI。標準ライブラリだけ。127.0.0.1 限定、ログイン必須。

1 プロセスで全 herdr セッションの画面を返す(/s/<セッション名>/。ポートは既定 8800、使用中なら +1 ずつずらす)。

既定は Go 版(herdrctl webui serve)に替わる。WEBUI_IMPL=python なら Python 版(app/)のまま動く。
WEBUI_IMPL を書かず、Web UI を持つ herdrctl が見つからないときも Python 版で動く(標準エラーに知らせる)。
設定は環境変数(README 参照)。
"""
import os
import shutil
import subprocess
import sys


def find_herdrctl():
    """herdrctl の場所(WEBUI_HERDRCTL > TB_HERDRCTL > PATH)。"""
    return os.environ.get("WEBUI_HERDRCTL") or os.environ.get("TB_HERDRCTL") or shutil.which("herdrctl")


def has_webui(hc):
    """その herdrctl が Web UI(webui serve)を持つか。古い版は持たない。"""
    try:
        p = subprocess.run([hc, "help"], capture_output=True, text=True, timeout=10, stdin=subprocess.DEVNULL, errors="replace")
    except (OSError, subprocess.SubprocessError):
        return False
    return "webui serve" in (p.stdout + p.stderr)


def exec_go(hc):
    """Go 版(herdrctl webui serve)に替わる。static/ はこのディレクトリのものを読ませる。"""
    os.environ["WEBUI_ROOT"] = os.path.dirname(os.path.abspath(__file__))
    sys.stdout.flush()
    sys.stderr.flush()
    os.execv(hc, [hc, "webui", "serve"])


def choose():
    impl = os.environ.get("WEBUI_IMPL", "")
    if impl == "python":
        return
    hc = find_herdrctl()
    if impl == "go":
        if not hc:
            raise SystemExit("herdr-webui: WEBUI_IMPL=go だが herdrctl が見つかりません(WEBUI_HERDRCTL で場所を指定)")
        exec_go(hc)
    if hc and has_webui(hc):
        exec_go(hc)
    sys.stderr.write("herdr-webui: Web UI を持つ herdrctl が見つからないので Python 版で動きます"
                     "(herdrctl を入れ直すか、WEBUI_HERDRCTL で場所を指定)\n")


if __name__ == "__main__":
    choose()
    from app.handler import main
    main()
