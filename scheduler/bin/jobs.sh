#!/bin/bash
# 使い方: jobs.sh   ジョブ名の一覧(DB の sched-job:<job>。SCHED_FILE_FALLBACK=1 なら jobs/*.conf との和。1 行 1 つ、名前順)
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go jobs "$@"   # SCHED_IMPL=go なら Go 版に渡す
sched_job_names
