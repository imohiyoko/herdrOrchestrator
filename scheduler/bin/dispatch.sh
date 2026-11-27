#!/bin/bash
# 使い方: dispatch.sh <job> [<job> ...] | dispatch.sh --all(毎分その時の DB のジョブをすべて)
# 毎分 1 回だけ launchd から起動し、指定したジョブの tick をこの 1 プロセスの中で並べて判定・実行する。
# ジョブごとに plist を置くと、ジョブの数だけ毎分 bash が起動し、Mac をスリープから起こす回数も増える。
# 1 本にまとめると起動は毎分 1 回になる。時間帯・間隔・一時停止の判定はジョブごとの tick が行う。
# 同じジョブをここと個別の plist(gen-plist.sh <job>)の両方に登録しない(二重に動く。setup/check.sh が warn を出す)。
# 環境変数(NOW / DRY_RUN / JOBS_DIR / LOG_DIR など)は各ジョブの tick にそのまま渡る。終了コードは 1 つでも失敗があれば 1

# 中身は herdrctl sched dispatch(lib.sh の sched_go)
set -u
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go dispatch "$@"
