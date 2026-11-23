#!/bin/bash
# 使い方: show.sh <job>   実際に効く設定(conf + settings + SCHED_*)を JSON 1 行で出す。Web UI が使う
# interval_sec は間隔(秒)。interval_min は 60 で割り切れるときだけ数値で、そうでなければ null
# SCHED_IMPL=go なら Go 版(herdrctl sched show。conf は決まった書き方だけを読み、展開は拒む)に渡す。herdrctl は SCHED_HERDRCTL > TB_HERDRCTL > PATH
set -eu
_hc=${SCHED_HERDRCTL:-${TB_HERDRCTL:-}}; [ -n "$_hc" ] || _hc=$(command -v herdrctl 2>/dev/null) || _hc=""
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
if [ "${SCHED_IMPL:-bash}" = go ]; then
  [ -n "$_hc" ] && command -v "$_hc" >/dev/null 2>&1 || die "SCHED_IMPL=go ですが herdrctl が見つかりません"
  SCHED_ROOT=$SCHED_ROOT JOBS_DIR=$JOBS_DIR SCHED_SETTINGS_DIR=$SETTINGS_DIR TB_DB=$SCHED_DB exec "$_hc" sched show "$@"
fi
load_job "${1:-}"
days_to_nums >/dev/null
jq -nc --arg job "$JOB" --arg kind "$([ -n "${COMMAND:-}" ] && echo command || echo prompt)" --arg target "${TARGET:-}" \
  --argjson interval "${INTERVAL_MIN:-null}" --argjson interval_sec "$INTERVAL_SEC" --arg start "$START" --arg end "$END" --arg days "$DAYS" \
  --argjson enabled "$ENABLED" --arg tz "$TZ" --arg watcher "${WATCHER:-}" --arg owner "${OWNER:-}" --argjson gate "$([ -n "$GATE" ] && echo true || echo false)" \
  --argjson prules "$RULE_N" --argjson pcmd "$([ -n "$PROMPT_CMD" ] && echo true || echo false)" --argjson pvars "$VAR_N" \
  --argjson overridden "$([ "$SETTINGS_SRC" != none ] && echo true || echo false)" --arg source "$JOB_SRC" --arg ssource "$SETTINGS_SRC" \
  '{job:$job,kind:$kind,target:$target,interval_min:$interval,interval_sec:$interval_sec,start:$start,end:$end,days:$days,enabled:($enabled==1),tz:$tz,watcher:(if $watcher=="" then null else $watcher end),owner:(if $owner=="" then null else $owner end),gate:$gate,prompt_rules:$prules,prompt_cmd:$pcmd,prompt_vars:$pvars,overridden:$overridden,source:$source,settings_source:$ssource}'
