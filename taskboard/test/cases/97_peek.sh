# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb peek(agent の中身を読む権限)と read_log / 関係グラフの read の辺
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_appr=${TB_APPROVALS-}
export TB_DB=$TMP/peek.db TB_APPROVALS=$TMP/peek-appr.conf
printf 'read:@worker = allow\n* = ask\n' >"$TB_APPROVALS"
pk() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(pk conductor),$(pk worker1),$(pk sweeper)]"
: >"$FAKE_LOG"
eq "peek: a human reads anyone" "$($TB peek conductor --lines 5)" "screen of wconductor:p1"
eq "peek: reads through herdr with the line count" "$(grep -c $'^read wconductor:p1\t--source recent --lines 5 --format text' "$FAKE_LOG")" "1"
eq "peek: an agent reads itself" "$(HERDR_PANE_ID=wsweeper:p1 $TB peek sweeper)" "screen of wsweeper:p1"
eq "peek: read:@worker allows reading a worker" "$(HERDR_PANE_ID=wsweeper:p1 $TB peek worker1)" "screen of wworker1:p1"
eq "peek: others are refused (20) without reading" "$(HERDR_PANE_ID=wsweeper:p1 rc $TB peek conductor) $(grep -c '^read wconductor' "$FAKE_LOG")" "20 1"
eq "peek: --lines is bounded" "$(rc $TB peek worker1 --lines 999)" "2"
eq "peek: not running → 1" "$(rc $TB peek nobody)" "1"
eq "peek: read_log keeps who read whom" "$(sqlite3 "$TB_DB" "SELECT reader||'>'||target||':'||status FROM read_log ORDER BY id;" | tr '\n' ' ')" \
  "human>conductor:ok sweeper>sweeper:ok sweeper>worker1:ok sweeper>conductor:denied human>nobody:failed "
eq "agents --graph: agent-to-agent reads are edges (denied marked)" "$($TB agents --graph --json | jq -r '[.edges[] | select(.verb=="read") | "\(.from)>\(.to):\(.status)"] | sort | join(",")')" "sweeper>conductor:denied,sweeper>worker1:ok"
export TB_APPROVALS=$_saved_appr
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
