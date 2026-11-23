#!/bin/bash
# 境界のテスト。herdr は疑似。使う実ポートは高位の小範囲だけで、終了時に必ず片付ける。
# Python 版のあと、herdrctl があれば同じテストを Go 版(WEBUI_IMPL=go。herdrctl webui serve)にも流す。
set -u
cd "$(dirname "${BASH_SOURCE[0]}")/.."
python3 -m py_compile server.py auth.py app/*.py || exit 1
python3 tests/test_webui.py || exit 1
if [ -n "${WEBUI_HERDRCTL:-}${TB_HERDRCTL:-}" ] || command -v herdrctl >/dev/null; then
  echo "== Go 版(WEBUI_IMPL=go)"
  WEBUI_IMPL=go exec python3 tests/test_webui.py
fi
echo "herdrctl が無いので Go 版は飛ばしました(TB_HERDRCTL か PATH で渡すと流れる)"
