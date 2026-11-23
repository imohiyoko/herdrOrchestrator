#!/bin/bash
# 境界のテスト(Web UI の中身は Go の herdrctl webui serve。bin/serve.sh から動かす)。herdr は疑似。
# 使う実ポートは高位の小範囲だけで、終了時に必ず片付ける。herdrctl は WEBUI_HERDRCTL > TB_HERDRCTL > PATH。
# 部品ごとの確かめは Go のテスト(go test ./internal/webui)。
set -u
cd "$(dirname "${BASH_SOURCE[0]}")/.."
python3 -m py_compile tests/test_webui.py || exit 1
bash -n bin/serve.sh bin/gen-plist.sh || exit 1
# serve.sh: herdrctl が無い・古いときは起動せずに理由を出す
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
printf '#!/bin/sh\necho "  version  版"\n' >"$tmp/old"; chmod +x "$tmp/old"
out=$(WEBUI_HERDRCTL=$tmp/old bash bin/serve.sh 2>&1); rc=$?
[[ $rc == 2 && $out == *"webui serve"*"ありません"* ]] || { echo "FAIL: serve.sh 古い herdrctl: rc=$rc $out"; exit 1; }
out=$(WEBUI_HERDRCTL=$tmp/none bash bin/serve.sh 2>&1); rc=$?
[[ $rc == 2 && $out == *"見つかりません"* ]] || { echo "FAIL: serve.sh herdrctl なし: rc=$rc $out"; exit 1; }
echo "ok - serve.sh は herdrctl が無い・古いと起動しない"
exec python3 tests/test_webui.py
