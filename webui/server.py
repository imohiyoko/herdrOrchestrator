#!/usr/bin/env python3
"""herdr Web UI。標準ライブラリだけ。127.0.0.1 限定、ログイン必須。

1 プロセスで全 herdr セッションの画面を返す(/s/<セッション名>/。ポートは既定 8800、使用中なら +1 ずつずらす)。

サーバー側で HTML を組み立てる複数ページ構成(SPA ではない)。中身は app/ にある(app/__init__.py に構成)。
設定は環境変数(README 参照)。
"""
import os
import shutil
import sys


def exec_go():
    """WEBUI_IMPL=go なら Go 版(herdrctl webui serve)に替わる。移行中で、今はログインまわりだけ。
    herdrctl は WEBUI_HERDRCTL > TB_HERDRCTL > PATH。static/ はこのディレクトリのものを読ませる。"""
    hc = os.environ.get("WEBUI_HERDRCTL") or os.environ.get("TB_HERDRCTL") or shutil.which("herdrctl")
    if not hc:
        raise SystemExit("herdr-webui: WEBUI_IMPL=go だが herdrctl が見つかりません(WEBUI_HERDRCTL で場所を指定)")
    os.environ["WEBUI_ROOT"] = os.path.dirname(os.path.abspath(__file__))
    sys.stdout.flush()
    os.execv(hc, [hc, "webui", "serve"])


if __name__ == "__main__":
    if os.environ.get("WEBUI_IMPL") == "go":
        exec_go()
    from app.handler import main
    main()
