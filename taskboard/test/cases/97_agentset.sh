# shellcheck shell=bash
# --- tb agentset: 定義(人だけが書く)、up(1 つの workspace に pane を分けて、親から順に)・status・down(子から順に)
# DB・定義の置き場・設定はこのケースのものだけを使う
_saved_env=$(declare -p HERDR_PANE_ID TB_DB TB_AGENTSETS TB_SPAWN_RETRY_SEC TB_AGENT_CWD FAKE_DYNAMIC FAKE_AGENTS 2>/dev/null)
unset HERDR_PANE_ID
export TB_DB=$TMP/agentset.db TB_AGENTSETS=$TMP/agentsets TB_SPAWN_RETRY_SEC=0 TB_AGENT_CWD=$TMP FAKE_DYNAMIC=1
as_ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"},"cwd":"%s"}' "$2" "$1" "$1" "$TMP"; }
as_sql() { sqlite3 "$TB_DB" "$@"; }
as_reset() { : >"$FAKE_LOG"; rm -f "$FAKE_LOG.closed" "$FAKE_LOG.started" "$FAKE_LOG.splits"; }
export FAKE_AGENTS="[$(as_ag conductor wC:p1)]"
as_reset
$TB agents >/dev/null

as_def="# 見張りのセット
cwd = $TMP
parent = conductor

[member lead]
role = -
prompt = 振り分ける
parent = ~set

[member rev]
count = 2
parent = lead
"
as_def=${as_def//role = -$'\n'/}

# ---- 定義: 人だけが書ける。読めない定義は置かない
eq "agentset: add from an agent pane is refused (deny)" "$(HERDR_PANE_ID=wC:p1 rc $TB agentset add rv <<<"$as_def") $(ls "$TB_AGENTSETS" 2>/dev/null | wc -l | tr -d ' ')" "1 0"
eq "agentset: a broken definition is not saved (usage)" "$(rc $TB agentset add rv <<<$'[member a]\nparent = a\n') $(ls "$TB_AGENTSETS" 2>/dev/null | wc -l | tr -d ' ')" "2 0"
eq "agentset: add / list / show" "$($TB agentset add rv <<<"$as_def") / $($TB agentset list) / $($TB agentset show rv | grep -c '^\[member')" "agentset rv を登録しました / rv / 2"

# ---- up: 1 体目は workspace(label = セット名)、残りはその pane を分けて置く。親は台帳の spawned_by
out=$($TB agentset up rv 2>&1; echo "rc=$?")
eq "agentset up: succeeds" "$(tail -1 <<<"$out")" "rc=0"
eq "agentset up: workspace once, then split right / down" \
  "$(grep -c "^workspace-create workspace create --cwd $TMP --label rv --no-focus" "$FAKE_LOG") $(grep -c '^pane-split wA:p1 --direction right' "$FAKE_LOG") $(grep -c '^pane-split wA:p1 --direction down' "$FAKE_LOG")" "1 1 1"
eq "agentset up: started in order, each in its own pane" \
  "$(grep '^start ' "$FAKE_LOG" | awk '{print $2" "$6}' | paste -sd, -)" "rv-lead wA:p1,rv-rev-1 wS:p1,rv-rev-2 wS:p2"
eq "agentset up: parents in the ledger" "$(as_sql "SELECT name||'<'||IFNULL(spawned_by,'-') FROM agents WHERE name LIKE 'rv-%' ORDER BY name;" | paste -sd, -)" "rv-lead<conductor,rv-rev-1<rv-lead,rv-rev-2<rv-lead"
eq "agentset up: first prompt sent to lead" "$(grep -c '^prompt rv-lead.*振り分ける' "$FAKE_LOG")" "1"
eq "agentset status" "$($TB agentset status rv | cut -f1-5 | paste -sd, -)" $'rv-lead\tlead\trunning\twA:p1\tconductor,rv-rev-1\trev\trunning\twS:p1\trv-lead,rv-rev-2\trev\trunning\twS:p2\trv-lead'
eq "agentset status --json" "$($TB agentset status rv --json | jq -r '.[0].name+" "+.[0].want_parent')" "rv-lead conductor"

# ---- もう一度 up しても何もしない
: >"$FAKE_LOG"
eq "agentset up: again does nothing" "$($TB agentset up rv) $(grep -c '^start\|^workspace-create\|^pane-split' "$FAKE_LOG")" "rv: すべて動いています(何もしません) 0"
eq "agentset up/down from an agent pane is refused" "$(HERDR_PANE_ID=wC:p1 rc $TB agentset up rv) $(HERDR_PANE_ID=wC:p1 rc $TB agentset down rv)" "1 1"

# ---- down: 子から順に止める。外の親は止めない。止めてよい(killable)と判定されていなければ、tb agent stop と同じく --force が要る
: >"$FAKE_LOG"
eq "agentset down: killable=unknown without --force → 1, nothing closed" "$(rc $TB agentset down rv) $(grep -c '^pane-close' "$FAKE_LOG")" "1 0"
: >"$FAKE_LOG"
out=$($TB agentset down rv --force 2>&1; echo "rc=$?")
eq "agentset down --force: succeeds" "$(tail -1 <<<"$out")" "rc=0"
eq "agentset down: children first, conductor untouched" "$(grep '^pane-close' "$FAKE_LOG" | awk '{print $2}' | paste -sd, -)" "wS:p2,wS:p1,wA:p1"
eq "agentset status after down" "$($TB agentset status rv | cut -f3 | sort -u)" "stopped"

# ---- 止めたあとの up は、同じ workspace の形で全員を戻す(revive)
: >"$FAKE_LOG"; rm -f "$FAKE_LOG.splits"
eq "agentset up after down: revives all" "$($TB agentset up rv | grep -c ' revived ') $(grep -c '^workspace-create.*--label rv' "$FAKE_LOG") $(grep -c '^pane-split' "$FAKE_LOG")" "3 1 2"

# ---- 外の親がいない・定義が無い
$TB agentset add solo <<<$'parent = ghost\n[member a]\nparent = ~set\n' >/dev/null
eq "agentset up: an unknown outside parent → 1, nothing started" "$(rc $TB agentset up solo) $(as_sql "SELECT COUNT(*) FROM agents WHERE name='solo-a';")" "1 0"
eq "agentset up: no definition → 1" "$(rc $TB agentset up nope)" "1"
eq "agentset rm" "$($TB agentset rm solo) $($TB agentset list | paste -sd, -)" "agentset solo を消しました rv"

as_reset
unset HERDR_PANE_ID TB_DB TB_AGENTSETS TB_SPAWN_RETRY_SEC TB_AGENT_CWD FAKE_DYNAMIC FAKE_AGENTS
eval "$_saved_env"
