#!/bin/bash
# 使い方: gen-plist.sh <job>   -> generated/local.herdr-sched-<job>.plist(TB_INSTANCE があれば local.herdr-<instance>-sched-<job>.plist)
#         gen-plist.sh --dispatch <job> [<job> ...]
#                              -> generated/local.herdr-sched-dispatch.plist(推奨。複数のジョブを毎分 1 回の起動にまとめる。bin/dispatch.sh)
# plist は毎分起動するだけ(StartCalendarInterval の空 dict = 毎分)。時間帯・間隔は tick.sh が実行時に判定するので、
# conf や settings を変えても作り直さなくてよい(--dispatch はジョブを足す・外すときだけ作り直す)。

# 中身は herdrctl sched gen-plist(lib.sh の sched_go)
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go gen-plist "$@"
