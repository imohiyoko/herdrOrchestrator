#!/bin/bash
# 境界のテスト。herdr は疑似。使う実ポートは高位の小範囲だけで、終了時に必ず片付ける。
# 既定の Go 版(herdrctl webui serve)と、WEBUI_IMPL=python の Python 版の両方に同じテストを流す。
# herdrctl が無ければ(server.py が Python 版に落ちるので)Python 版だけ。
set -u
cd "$(dirname "${BASH_SOURCE[0]}")/.."
python3 -m py_compile server.py auth.py app/*.py || exit 1
python3 tests/test_server_choose.py || exit 1
if [ -n "${WEBUI_HERDRCTL:-}${TB_HERDRCTL:-}" ] || command -v herdrctl >/dev/null; then
  echo "== Go 版(既定)"
  WEBUI_IMPL=go python3 tests/test_webui.py || exit 1
else
  echo "herdrctl が無いので Go 版は飛ばします(TB_HERDRCTL か PATH で渡すと流れる)"
fi
echo "== Python 版(WEBUI_IMPL=python)"
WEBUI_IMPL=python exec python3 tests/test_webui.py
