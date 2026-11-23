#!/bin/bash
# 使い方: jobs.sh   ジョブ名の一覧(DB の sched-job:<job>。SCHED_FILE_FALLBACK=1 なら jobs/*.conf との和。1 行 1 つ、名前順)
# 中身は herdrctl sched jobs(lib.sh の sched_go)
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go jobs "$@"
