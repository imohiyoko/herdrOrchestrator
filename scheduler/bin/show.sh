#!/bin/bash
# 使い方: show.sh <job>   実際に効く設定(conf + settings + SCHED_*)を JSON 1 行で出す。Web UI が使う
# interval_sec は間隔(秒)。interval_min は 60 で割り切れるときだけ数値で、そうでなければ null
# SCHED_IMPL=go なら Go 版(herdrctl sched show)に渡す(lib.sh の sched_go)
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go show "$@"
load_job "${1:-}"
days_to_nums >/dev/null
jq -nc --arg job "$JOB" --arg kind "$([ -n "${COMMAND:-}" ] && echo command || echo prompt)" --arg target "${TARGET:-}" \
  --argjson interval "${INTERVAL_MIN:-null}" --argjson interval_sec "$INTERVAL_SEC" --arg start "$START" --arg end "$END" --arg days "$DAYS" \
  --argjson enabled "$ENABLED" --arg tz "$TZ" --arg watcher "${WATCHER:-}" --arg owner "${OWNER:-}" --argjson gate "$([ -n "$GATE" ] && echo true || echo false)" \
  --argjson prules "$RULE_N" --argjson pcmd "$([ -n "$PROMPT_CMD" ] && echo true || echo false)" --argjson pvars "$VAR_N" \
  --argjson overridden "$([ "$SETTINGS_SRC" != none ] && echo true || echo false)" --arg source "$JOB_SRC" --arg ssource "$SETTINGS_SRC" \
  '{job:$job,kind:$kind,target:$target,interval_min:$interval,interval_sec:$interval_sec,start:$start,end:$end,days:$days,enabled:($enabled==1),tz:$tz,watcher:(if $watcher=="" then null else $watcher end),owner:(if $owner=="" then null else $owner end),gate:$gate,prompt_rules:$prules,prompt_cmd:$pcmd,prompt_vars:$pvars,overridden:$overridden,source:$source,settings_source:$ssource}'
