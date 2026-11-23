#!/bin/bash
# 使い方: tick.sh <job>
#   NOW="YYYY-MM-DD HH:MM[:SS]"  時刻を差し替える(ジョブの TZ での現地時刻)。待たずに判定する
#                           秒まで書くとその 1 秒だけ、分までならその分(00〜59 秒)に入る回をすべて判定する
#   DRY_RUN=1               herdr を呼ばず、判定結果を stdout に JSON で出す(ログは書かない)
#   FAKE_STATE=<state>      DRY_RUN 時に TARGET の状態を仮定する(テスト用)
# launchd は毎分起動する。実行するかどうか(曜日・時間帯・間隔・一時停止)はここで判定するので、
# 間隔や時間帯を変えても plist を作り直す必要はない。対象外の回は(DRY_RUN 以外では)何も記録しない。
# 判定は秒単位。起動した分の中に刻みの回が複数ある(INTERVAL_SEC が 60 未満など)ときは、その時刻まで待って順に実行する。
# 中身は herdrctl sched tick(lib.sh の sched_go)
set -u
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go tick "$@"
