#!/bin/bash
# 使い方: bin/gen-gateway-plist.sh   -> generated/local.herdr-webui-gateway.plist を生成して lint する。
# 入口(gateway.py)は checkout・セッションをまたいで 1 つだけ動かす(ラベルは TB_INSTANCE によらず固定)。
# TB_UI_GATEWAY_PORT が設定されていれば plist に渡す(未設定なら 8800)。
# launchctl への登録はしない(人が行う。README の「入口」を参照)。
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LABEL="local.herdr-webui-gateway"
EXTRA_ENV=""
if [ -n "${TB_UI_GATEWAY_PORT:-}" ]; then
  { [[ $TB_UI_GATEWAY_PORT =~ ^[0-9]{1,5}$ ]] && ((10#$TB_UI_GATEWAY_PORT >= 1 && 10#$TB_UI_GATEWAY_PORT <= 65535)); } \
    || { echo "error: TB_UI_GATEWAY_PORT は 1〜65535 の数字: $TB_UI_GATEWAY_PORT" >&2; exit 2; }
  EXTRA_ENV="    <key>TB_UI_GATEWAY_PORT</key>
    <string>$((10#$TB_UI_GATEWAY_PORT))</string>
"
fi
OUT_DIR="${OUT_DIR:-$ROOT/generated}"
LOG_DIR="$ROOT/logs"
PY="${PYTHON3:-$(command -v python3)}"
mkdir -p "$OUT_DIR" "$LOG_DIR"
OUT="$OUT_DIR/$LABEL.plist"
cat >"$OUT" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$PY</string>
    <string>$ROOT/gateway.py</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$ROOT</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
$EXTRA_ENV  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>$LOG_DIR/launchd-webui-gateway.out</string>
  <key>StandardErrorPath</key>
  <string>$LOG_DIR/launchd-webui-gateway.err</string>
</dict>
</plist>
PLIST
if command -v plutil >/dev/null; then plutil -lint "$OUT" >/dev/null; fi   # macOS 以外(テスト)では lint を省く
echo "$OUT"
echo "登録は人が行う(このスクリプトは行わない)。手順は README の「入口」を参照"
