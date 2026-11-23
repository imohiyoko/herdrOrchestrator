#!/bin/bash
# 使い方: install.sh {install|uninstall|status} <job>
#         install.sh {install|uninstall|status} --dispatch   (gen-plist.sh --dispatch で作った 1 本)
# 中身は herdrctl sched install(lib.sh の sched_go)
set -eu
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sched_go install "$@"
