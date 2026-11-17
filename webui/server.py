#!/usr/bin/env python3
"""herdr Web UI。標準ライブラリだけ。127.0.0.1 限定、ログイン必須。

1 プロセスで全 herdr セッションの画面を返す(/s/<セッション名>/。ポートは既定 8800、使用中なら +1 ずつずらす)。

サーバー側で HTML を組み立てる複数ページ構成(SPA ではない)。中身は app/ にある(app/__init__.py に構成)。
設定は環境変数(README 参照)。
"""
from app.handler import main

if __name__ == "__main__":
    main()
