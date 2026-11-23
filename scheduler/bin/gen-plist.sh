#!/bin/bash
# 使い方: gen-plist.sh <job>   -> generated/local.herdr-sched-<job>.plist(TB_INSTANCE があれば local.herdr-<instance>-sched-<job>.plist)
#         gen-plist.sh --dispatch <job> [<job> ...]
#                              -> generated/local.herdr-sched-dispatch.plist(推奨。複数のジョブを毎分 1 回の起動にまとめる。bin/dispatch.sh)
# plist は毎分起動するだけ(StartCalendarInterval の空 dict = 毎分)。時間帯・間隔は tick.sh が実行時に判定するので、
# conf や settings を変えても作り直さなくてよい(--dispatch はジョブを足す・外すときだけ作り直す)。
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go gen-plist "$@"   # SCHED_IMPL=go なら Go 版に渡す
OUT_DIR="${OUT_DIR:-$SCHED_ROOT/generated}"

if [ "${1:-}" = --dispatch ]; then
  shift
  [ $# -gt 0 ] || die "使い方: gen-plist.sh --dispatch <job> [<job> ...]"
  for j in "$@"; do ( load_job "$j" ) || exit 2; done   # 定義の誤りはここで止める(サブシェル: 変数を持ち越さない)
  JOB=dispatch
  PROG="$SCHED_ROOT/bin/dispatch.sh"; ARGS="$*"; WHAT="ジョブ $ARGS"
else
  load_job "${1:-}"
  [ "$JOB" != dispatch ] || die "ジョブ名 dispatch は予約済み(--dispatch のラベルと重なる)"
  PROG="$SCHED_ROOT/bin/tick.sh"; ARGS="$JOB"; WHAT="間隔 $(interval_label) / $WINDOW / $DAYS"
fi

LABEL="$(herdr_label_prefix)sched-$JOB" || exit 2   # TB_INSTANCE 未設定なら従来の local.herdr-sched-<job>
SESS_XML=""
if [ -n "${HERDR_SESSION:-}" ]; then   # 設定されていれば、その checkout のセッションを tick.sh に見せる。未設定なら何も足さない
  herdr_plist_value_ok "$HERDR_SESSION" || die "HERDR_SESSION は英数字と . _ - だけ: $HERDR_SESSION"
  SESS_XML="    <key>HERDR_SESSION</key>
    <string>$HERDR_SESSION</string>
"
fi
mkdir -p "$OUT_DIR" "$LOG_DIR"
OUT="$OUT_DIR/$LABEL.plist"
{
cat <<HDR
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>$PROG</string>
$(for a in $ARGS; do printf '    <string>%s</string>\n' "$a"; done)
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
$SESS_XML  </dict>
  <key>StartCalendarInterval</key>
  <dict/>
HDR
cat <<FTR
  <key>StandardOutPath</key>
  <string>$LOG_DIR/launchd-$JOB.out</string>
  <key>StandardErrorPath</key>
  <string>$LOG_DIR/launchd-$JOB.err</string>
</dict>
</plist>
FTR
} >"$OUT"
if command -v plutil >/dev/null; then plutil -lint "$OUT" >/dev/null; fi   # macOS 以外(テスト)では lint を省く
echo "$OUT (毎分起動。$WHAT は tick.sh が判定)"
