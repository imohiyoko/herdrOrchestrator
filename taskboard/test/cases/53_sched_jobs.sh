# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb sched-job: scheduler のジョブ定義・上書き設定を DB(config 表)に置く。履歴・検査・取り込み・セッションごとの定義
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
export TB_DB=$TMP/schedjobs.db TB_SCHED_DIR=$TMP/sjd
mkdir -p "$TB_SCHED_DIR/jobs" "$TB_SCHED_DIR/settings" "$TB_SCHED_DIR/logs"
sjsql() { sqlite3 "$TB_DB" "$@"; }
SJA() { HERDR_SESSION=sja "$TB" "$@"; }
SJB() { HERDR_SESSION=sjb "$TB" "$@"; }
J1=$'TARGET=dummy\nPROMPT=\'look\'\nWINDOW=09:00-21:00\nINTERVAL_MIN=5'
printf '%s\n' "$J1" >"$TB_SCHED_DIR/jobs/j1.conf"
eq "sched-job list: file job" "$(SJA sched-job list | grep '^j1' | cut -f2-)" "file:$TB_SCHED_DIR/jobs/j1.conf	settings=none"
# set: このセッションの行に書く。ほかのセッションはファイルのまま
printf '%s\nINTERVAL_MIN=7\n' "$J1" | SJA sched-job set j1 >/dev/null
eq "sched-job set: this session's row" "$(SJA sched-job list | grep '^j1' | cut -f2) $(SJB sched-job list | grep '^j1' | cut -f2 | cut -d: -f1)" "db:sja file"
eq "sched-job show: body from the DB" "$(SJA sched-job show j1 2>/dev/null | tail -1)" "INTERVAL_MIN=7"
eq "sched-job: the scheduler reads the session's own definition" "$(HERDR_SESSION=sja JOBS_DIR=$TB_SCHED_DIR/jobs bash "$HERE/../../scheduler/bin/show.sh" j1 | jq -c '[.interval_min,.source]') $(HERDR_SESSION=sjb JOBS_DIR=$TB_SCHED_DIR/jobs bash "$HERE/../../scheduler/bin/show.sh" j1 | jq -c '[.interval_min,.source]')" '[7,"db"] [5,"file"]'
# --shared: 共通の行。このセッションの行があればそちらが先
printf '%s\nINTERVAL_MIN=9\n' "$J1" | SJA sched-job set j1 --shared >/dev/null
eq "sched-job set --shared: shared row, session row wins" "$(SJA sched-job list | grep '^j1' | cut -f2) $(SJB sched-job list | grep '^j1' | cut -f2)" "db:sja db:*"
# 検査: scheduler が読めない本文は書かない
eq "sched-job set: invalid body refused, nothing written" "$(printf 'TARGET=x\nWINDOW=bad\nINTERVAL_MIN=5\nPROMPT=y\n' | rc SJA sched-job set j1) $(SJA sched-job show j1 2>/dev/null | tail -1)" "1 INTERVAL_MIN=7"
eq "sched-job set: empty body refused" "$(: | rc SJA sched-job set j2)" "1"
eq "sched-job set: bad job name → 2" "$(printf 'x\n' | rc SJA sched-job set 'a b')" "2"
eq "sched-job set: dispatch is reserved → 2" "$(printf 'x\n' | rc SJA sched-job set dispatch)" "2"
# 上書き設定も DB に(今の定義と組み合わせて検査する)
printf 'INTERVAL_MIN=15\nSTART=10:00\nEND=11:00\nDAYS=daily\nENABLED=0\n' | SJA sched-job set j1 --settings >/dev/null
eq "sched-job set --settings: the scheduler uses it" "$(HERDR_SESSION=sja JOBS_DIR=$TB_SCHED_DIR/jobs bash "$HERE/../../scheduler/bin/show.sh" j1 | jq -c '[.interval_min,.start,.enabled,.settings_source]')" '[15,"10:00",false,"db"]'
eq "sched-job set --settings: unknown key refused" "$(printf 'COMMAND=rm\n' | rc SJA sched-job set j1 --settings)" "1"
eq "sched-job set --settings: needs a definition" "$(printf 'ENABLED=0\n' | rc SJA sched-job set nojob --settings)" "1"
# 履歴(config_changes)
eq "sched-job history: changes recorded" "$(SJA sched-job history j1 | cut -f2,3 | tr '\t\n' ': ')" "*:set sja:set "
eq "sched-job history --settings" "$(SJA sched-job history j1 --settings | cut -f2,3 | tr '\t\n' ': ')" "sja:set "
# unset: このセッションの行を消すと、共通の行に戻る
SJA sched-job unset j1 >/dev/null
eq "sched-job unset: falls back to the shared row, history kept" "$(SJA sched-job list | grep '^j1' | cut -f2) $(SJA sched-job history j1 | head -1 | cut -f3)" "db:* unset"
eq "sched-job unset: no row → 1" "$(rc SJA sched-job unset j1)" "1"
# 人だけ: agent の pane からは書けない
export FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s"}}]'
eq "sched-job set from an agent pane → refused" "$(printf '%s\n' "$J1" | HERDR_PANE_ID=wW:p1 rc SJA sched-job set j1)" "1"
eq "sched-job import from an agent pane → refused" "$(HERDR_PANE_ID=wW:p1 rc SJA sched-job import)" "1"
unset FAKE_AGENTS
# import: ファイルを取り込んで退かす。既に行があるジョブは飛ばす
printf 'COMMAND=%s\nWINDOW=00:00-23:59\nINTERVAL_MIN=2\n' "'echo hi'" >"$TB_SCHED_DIR/jobs/j3.conf"
printf 'ENABLED=0\n' >"$TB_SCHED_DIR/settings/j3.settings"
out=$(XDG_STATE_HOME=$TMP/sjstate SJB sched-job import --dry-run)
eq "sched-job import --dry-run: plan only" "$(grep -c '^j3: jobs/j3.conf と settings/j3.settings を取り込みます' <<<"$out") $(grep -c '^j1: jobs/j1.conf を取り込みます' <<<"$out") $(ls "$TB_SCHED_DIR/jobs" | wc -l | tr -d ' ')" "1 1 2"
out=$(XDG_STATE_HOME=$TMP/sjstate SJB sched-job import j3)
eq "sched-job import <job>: imported and moved away" "$(SJB sched-job list | grep '^j3' | cut -f2-) $(ls "$TB_SCHED_DIR/jobs") $(ls "$TMP"/sjstate/herdr/sched-imported/*/jobs/)" "db:sjb	settings=db:sjb j1.conf j3.conf"
eq "sched-job import: history says import" "$(SJB sched-job history j3 | cut -f3)" "import"
printf 'COMMAND=%s\nWINDOW=00:00-23:59\nINTERVAL_MIN=2\n' "'echo hi'" >"$TB_SCHED_DIR/jobs/j3.conf"
eq "sched-job import: existing row is skipped, file kept" "$(XDG_STATE_HOME=$TMP/sjstate SJB sched-job import j3 | grep -c '既にある') $(ls "$TB_SCHED_DIR/jobs" | grep -c j3)" "1 1"
rm -f "$TB_SCHED_DIR/jobs/j3.conf"
# tb digest の見張り(schedwatch)も DB のジョブを見る
printf 'ENABLED=0\n' | SJB sched-job set j3 --settings >/dev/null
NOWE=1800000000
for m in 40 35 30 25 20 15 10 5; do jq -nc --argjson e $((NOWE - m * 60 + 32400)) '{ts:($e|strftime("%Y-%m-%dT%H:%M:%S+0900")),job:"j3",action:"skipped_disabled",reason:"t"}' >>"$TB_SCHED_DIR/logs/j3.log"; done
eq "schedwatch: sees a job defined only in the DB" "$(HERDR_SESSION=sjb TB_SCHEDWATCH_NOW=$NOWE TB_SCHEDWATCH_CONF=$TMP/none.conf "$TB" digest --dry-run | grep -c 'ジョブ j3 が ENABLED=0 のまま 40 分')" "1"
unset TB_SCHED_DIR; export HERDR_PANE_ID=$_saved_pane
