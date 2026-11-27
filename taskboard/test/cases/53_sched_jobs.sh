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
# 読めないファイルは取り込まない(上書き設定の ENABLED=0 などを落とさない)
if [[ $(id -u) -eq 0 ]]; then
  echo "skip sched-job import: unreadable files(root はファイルの権限を無視して読めるため確かめられない)"
else
  printf '%s\n' "$J1" >"$TB_SCHED_DIR/jobs/j4.conf"; printf 'ENABLED=0\n' >"$TB_SCHED_DIR/settings/j4.settings"; chmod 000 "$TB_SCHED_DIR/settings/j4.settings"
  eq "sched-job import: unreadable settings → refused, nothing written" "$(XDG_STATE_HOME=$TMP/sjstate SJB sched-job import j4 2>&1 >/dev/null; echo "rc=$?") $(SJB sched-job list | grep -c '^j4	db')" "tb: 読めません: $TB_SCHED_DIR/settings/j4.settings
rc=1 0"
  chmod 000 "$TB_SCHED_DIR/jobs/j4.conf"
  eq "sched-job import: unreadable conf → refused" "$(XDG_STATE_HOME=$TMP/sjstate SJB sched-job import j4 2>&1 >/dev/null; echo "rc=$?")" "tb: 読めません: $TB_SCHED_DIR/jobs/j4.conf
rc=1"
  eq "sched-job show: unreadable file is not an empty body" "$(SCHED_FILE_FALLBACK=1 SJB sched-job show j4 >/dev/null 2>&1; echo $?)" "1"
  chmod 644 "$TB_SCHED_DIR/jobs/j4.conf" "$TB_SCHED_DIR/settings/j4.settings"; rm -f "$TB_SCHED_DIR/jobs/j4.conf" "$TB_SCHED_DIR/settings/j4.settings"
fi
# tb digest の見張り(schedwatch)も DB のジョブを見る
printf 'ENABLED=0\n' | SJB sched-job set j3 --settings >/dev/null
NOWE=1800000000
for m in 40 35 30 25 20 15 10 5; do jq -nc --argjson e $((NOWE - m * 60 + 32400)) '{ts:($e|strftime("%Y-%m-%dT%H:%M:%S+0900")),job:"j3",action:"skipped_disabled",reason:"t"}' >>"$TB_SCHED_DIR/logs/j3.log"; done
eq "schedwatch: sees a job defined only in the DB" "$(HERDR_SESSION=sjb TB_SCHEDWATCH_NOW=$NOWE TB_SCHEDWATCH_CONF=$TMP/none.conf "$TB" digest --dry-run | grep -c 'ジョブ j3 が ENABLED=0 のまま 40 分')" "1"
# 既定(SCHED_FILE_FALLBACK 無し)は DB だけ: 取り込んでいない conf は「未取り込み」と出し、settings を書く先の定義としても使わない
printf '%s\n' "$J1" >"$TB_SCHED_DIR/jobs/j9.conf"
eq "default: list marks an unimported conf" "$(SCHED_FILE_FALLBACK=0 SJB sched-job list | grep '^j9' | cut -f2 | cut -d: -f1)" "未取り込み"
eq "default: show points to import" "$(SCHED_FILE_FALLBACK=0 SJB sched-job show j9 2>&1 | grep -c 'tb job import j9')" "1"
eq "default: --settings needs a definition in the DB" "$(printf 'ENABLED=0\n' | SCHED_FILE_FALLBACK=0 rc SJB sched-job set j9 --settings)" "1"
eq "default: set still checks the body with the scheduler" "$(printf 'TARGET=x\nWINDOW=bad\nINTERVAL_MIN=5\nPROMPT=y\n' | SCHED_FILE_FALLBACK=0 rc SJB sched-job set j9) $(printf '%s\n' "$J1" | SCHED_FILE_FALLBACK=0 rc SJB sched-job set j9)" "1 0"
rm -f "$TB_SCHED_DIR/jobs/j9.conf"
# 新しい名前 tb job <サブコマンド>(古い名前 tb sched-job と同じに動く)
eq "job list: same as sched-job list" "$(SJA job list)" "$(SJA sched-job list)"
eq "job show: body and source on stderr" "$(SJA job show j1 2>&1 | head -1)" "# sched-job:j1: db:*"
eq "job history: same as sched-job history" "$(SJA job history j1)" "$(SJA sched-job history j1)"
eq "job: no subcommand → usage, exit 2" "$(SJA job 2>/dev/null | head -1 | cut -d' ' -f1-3; echo "rc=${PIPESTATUS[0]}")" $'tb job list\nrc=2'
eq "sched-job: no subcommand → list" "$(SJA sched-job | head -1)" "セッション: sja"
eq "job: unknown subcommand → 2" "$(rc SJA job bogus)" "2"
eq "job unset: no row message" "$(SJA job unset j1 2>&1)" "tb: sched-job:j1 の DB の行はありません(このセッションの行)"
eq "job set: refused body message" "$(printf 'TARGET=x\nWINDOW=bad\nINTERVAL_MIN=5\nPROMPT=y\n' | SJA job set j1 2>&1 | tail -1)" "tb: sched-job:j1 は書きませんでした"
eq "job set from an agent pane: message" "$(printf '%s\n' "$J1" | FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s"}}]' HERDR_PANE_ID=wW:p1 SJA job set j1 2>&1)" "tb: tb job set は人が自分の端末で実行します(agent 'worker1' の pane からは実行できません)。記録 Q-3(例外は人が tb approval approve Q-3 で許可できます)"
eq "job set: herdr fails → refused" "$(printf '%s\n' "$J1" | FAKE_AGENTS='not json' HERDR_PANE_ID=wW:p1 rc SJA job set j1)" "1"
# job rm: 定義と上書き設定の行を消す。どのセッションにも定義が残らなければログも消す
printf '%s\n' "$J1" | SJA job set jr >/dev/null; printf 'ENABLED=0\n' | SJA job set jr --settings >/dev/null
printf '%s\n' "$J1" | SJA job set jr --shared >/dev/null
printf '{}\n' >"$TB_SCHED_DIR/logs/jr.log"
eq "job rm: this session's rows, shared row still runs" "$(SJA job rm jr)" "jr: sched-job:jr と sched-settings:jr を消しました(このセッション sja の行)
jr: まだ db:* の定義で動きます(共通の行は tb job rm jr --shared で消す)"
eq "job rm: log kept while a definition remains" "$(ls "$TB_SCHED_DIR/logs" | grep -c '^jr.log$')" "1"
eq "job rm --shared: removes the log too" "$(SJA job rm jr --shared)" "jr: sched-job:jr を消しました(共通の行)
jr: ログ $TB_SCHED_DIR/logs/jr.log を消しました"
eq "job rm: gone from the list, history kept" "$(SJA job list | grep -c '^jr') $(SJA job history jr | cut -f2,3 | tr '\t\n' ': ')" "0 *:unset sja:unset *:set sja:set "
eq "job rm: no rows → 1" "$(rc SJA job rm jr)" "1"
eq "job rm --settings → 2" "$(rc SJA job rm jr --settings)" "2"
printf '%s\n' "$J1" | SJA job set jr >/dev/null; printf '{}\n' >"$TB_SCHED_DIR/logs/jr.log"
eq "job rm --keep-log" "$(SJA job rm jr --keep-log >/dev/null; ls "$TB_SCHED_DIR/logs" | grep -c '^jr.log$')" "1"
rm -f "$TB_SCHED_DIR/logs/jr.log"
eq "sched-job rm: not a legacy verb → 2" "$(rc SJA sched-job rm j1)" "2"
printf '%s\n' "$J1" | SJA job set jr >/dev/null
eq "job rm from an agent pane → refused" "$(FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s"}}]' HERDR_PANE_ID=wW:p1 rc SJA job rm jr) $(SJA job list | grep -c '^jr')" "1 1"
# job run: 時刻に関係なく今すぐ 1 回(API の job.run)。MANUAL=1 は毎分の判定では動かない
printf '%s\n' "COMMAND='echo hi-now'" 'MANUAL=1' | SJA job set jm >/dev/null
out=$(SJA job run jm; echo "rc=$?")
eq "job run: runs now, prints the record and stdout" "$out" $'jm: ran(exit 0)\nhi-now\nrc=0'
eq "job run: the log line has now: true" "$(tail -1 "$TB_SCHED_DIR/logs/jm.log" | jq -c '[.action,.now]')" '["ran",true]'
eq "job run: MANUAL=1 does not run on the minute" "$(HERDR_SESSION=sja DRY_RUN=1 NOW='2026-11-05 12:00' bash "$HERE/../../scheduler/bin/tick.sh" jm | jq -r .action)" "skipped_manual"
printf '%s\n' "COMMAND='echo no; exit 3'" 'WINDOW=09:00-09:01' 'INTERVAL_MIN=1' | SJA job set jf >/dev/null
eq "job run: a repeating job runs outside its window; failure → 1" "$(SJA job run jf | head -1; echo "rc=${PIPESTATUS[0]}")" $'jf: failed(exit 3)\nrc=1'
printf '%s\n' "COMMAND='echo x'" 'MANUAL=1' 'ENABLED=0' | SJA job set jp >/dev/null
eq "job run: a paused job is not run → 1" "$(SJA job run jp | cut -d'(' -f1; echo "rc=${PIPESTATUS[0]}")" $'jp: skipped_disabled\nrc=1'
eq "job run: no such job → 1" "$(rc SJA job run nosuch)" "1"
eq "job run: usage → 2" "$(rc SJA job run)" "2"
eq "job run from an agent pane → refused" "$(FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s"}}]' HERDR_PANE_ID=wW:p1 rc SJA job run jm) $(grep -c . "$TB_SCHED_DIR/logs/jm.log")" "1 1"
eq "job: MANUAL with WINDOW is refused" "$(printf '%s\n' "COMMAND='echo x'" 'MANUAL=1' 'WINDOW=09:00-10:00' | rc SJA job set jbad)" "1"
unset TB_SCHED_DIR; export HERDR_PANE_ID=$_saved_pane
