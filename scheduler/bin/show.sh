#!/bin/bash
# 使い方: show.sh <job>   実際に効く設定(conf + settings + SCHED_*)を JSON 1 行で出す。Web UI が使う
# interval_sec は間隔(秒)。interval_min は 60 で割り切れるときだけ数値で、そうでなければ null
# 中身は herdrctl sched show(lib.sh の sched_go)
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go show "$@"
