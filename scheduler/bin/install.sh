#!/bin/bash
# 使い方: install.sh {install|uninstall|status} <job>
#         install.sh {install|uninstall|status} --dispatch   (gen-plist.sh --dispatch で作った 1 本)
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cmd="${1:-}"
if [ "${2:-}" = --dispatch ]; then JOB=dispatch; else load_job "${2:-}"; fi
LABEL="$(herdr_label_prefix)sched-$JOB" || exit 2   # TB_INSTANCE 未設定なら従来のラベル
SRC="$SCHED_ROOT/generated/$LABEL.plist"
DST="$HOME/Library/LaunchAgents/$LABEL.plist"
DOMAIN="gui/$(id -u)"
case "$cmd" in
  install)
    [ -f "$SRC" ] || die "$SRC がない。先に bin/gen-plist.sh $([ "$JOB" = dispatch ] && echo '--dispatch <job> ...' || echo "$JOB")"
    cp "$SRC" "$DST"
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true   # 再登録を許す
    launchctl bootstrap "$DOMAIN" "$DST"
    echo "installed: $LABEL" ;;
  uninstall)
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || echo "(未登録だった)"
    rm -f "$DST"
    echo "uninstalled: $LABEL" ;;
  status)
    launchctl print "$DOMAIN/$LABEL" 2>&1 | head -30 || true ;;
  *) die "使い方: install.sh {install|uninstall|status} <job> | --dispatch" ;;
esac
