#!/bin/bash
# 使い方: jobs.sh   ジョブ名の一覧(DB の sched-job:<job> と jobs/*.conf の和。1 行 1 つ、名前順)。Web UI と tb digest が使う
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_job_names
