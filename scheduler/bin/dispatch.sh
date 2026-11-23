#!/bin/bash
# 使い方: dispatch.sh <job> [<job> ...]
# 毎分 1 回だけ launchd から起動し、指定したジョブの tick.sh をこの 1 プロセスの中で並べて判定・実行する。
# ジョブごとに plist を置くと、ジョブの数だけ毎分 bash が起動し、Mac をスリープから起こす回数も増える。
# 1 本にまとめると起動は毎分 1 回になる。各ジョブはサブシェル(fork のみ。bash を起動し直さない)で動くので、
# ジョブどうしの変数や exit は混ざらない。時間帯・間隔・一時停止の判定は従来どおり tick.sh が行う。
# 同じジョブをここと個別の plist(gen-plist.sh <job>)の両方に登録しない(二重に動く。setup/check.sh が warn を出す)。
# 環境変数(NOW / DRY_RUN / JOBS_DIR / LOG_DIR など)は tick.sh にそのまま渡る。終了コードは 1 つでも失敗があれば 1
set -u
if [ "${SCHED_IMPL:-}" != bash ]; then   # Go 版(herdrctl sched dispatch)に渡せるなら渡す(lib.sh の sched_go。渡せなければ下の bash 版)
  # shellcheck source=lib.sh
  source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
  sched_go dispatch "$@"
fi
TICK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/tick.sh"
[ $# -gt 0 ] || { echo "error: 使い方: dispatch.sh <job> [<job> ...]" >&2; exit 2; }
pids=()
for job in "$@"; do
  # tick.sh は 1 ジョブ = 1 プロセスを前提に書かれている(exit・trap・グローバル変数)。サブシェルで閉じ込める
  ( source "$TICK" "$job" ) &
  pids+=($!)
done
rc=0
for p in "${pids[@]}"; do wait "$p" || rc=1; done
exit $rc
