#!/bin/bash
# tick.sh の判定テスト。DRY_RUN=1 + NOW 差し替えのみ。herdr は呼ばない。
# 使い方: tests/run.sh
# An agent pane running this suite would change some results; the cases do not depend on it.
unset HERDR_PANE_ID
cd "$(dirname "$0")/.." || exit 1
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
export JOBS_DIR="$TMP/jobs" LOG_DIR="$TMP/logs"; mkdir -p "$JOBS_DIR"
export TB_DB="$TMP/none.db"   # ジョブ定義の DB(config 表)は、下の「DB のジョブ定義」の節でだけ使う。本物の DB は読まない
# 判定のテストはジョブをファイルに置く(DB に取り込む前の置き場所。SCHED_FILE_FALLBACK=1 のときだけ読む)。
# 既定(DB だけ)の動きは末尾の「既定は DB だけ」の節で確かめる
export SCHED_FILE_FALLBACK=1

cat >"$JOBS_DIR/t.conf" <<'C'
TARGET=dummy
PROMPT='do the thing'
WINDOW=09:00-21:00
INTERVAL_MIN=5
C
cat >"$JOBS_DIR/wk.conf" <<'C'
TARGET=dummy
PROMPT='do the thing'
WINDOW=09:00-21:00
INTERVAL_MIN=5
DAYS="Mon Tue Wed Thu Fri"
C
cat >"$JOBS_DIR/skipidle.conf" <<'C'
TARGET=dummy
PROMPT='do the thing'
WINDOW=09:00-21:00
INTERVAL_MIN=5
SKIP_IF="working blocked done"
C
cat >"$JOBS_DIR/slash.conf" <<'C'
TARGET=dummy
PROMPT='/loop bad'
WINDOW=09:00-21:00
INTERVAL_MIN=5
C

pass=0; failn=0
# check <説明> <期待 action> <job> <NOW> [FAKE_STATE]
check() {
  local desc=$1 want=$2 job=$3 now=$4 st=${5:-} got
  got=$(DRY_RUN=1 NOW="$now" FAKE_STATE="$st" bash bin/tick.sh "$job" 2>&1 | jq -r .action 2>&1)
  if [ "$got" = "$want" ]; then pass=$((pass + 1)); printf 'ok   %-46s %s -> %s\n' "$desc" "$now" "$got"
  else failn=$((failn + 1)); printf 'FAIL %-46s %s want=%s got=%s\n' "$desc" "$now" "$want" "$got"; fi
}

ok() { if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "ok   $1"; else failn=$((failn + 1)); echo "FAIL $1 want=[$3] got=[$2]"; fi; }

THU="2026-11-05"; SAT="2026-11-07"; SUN="2026-11-08"; MON="2026-11-09"
# 窓の境界(木曜、状態 idle)
check "境界 08:55 は窓の外"        skipped_window t "$THU 08:55" idle
check "境界 09:00 は実行(始端を含む)" sent           t "$THU 09:00" idle
check "境界 20:55 は実行"          sent           t "$THU 20:55" idle
check "境界 21:00 は実行(終端を含む)" sent           t "$THU 21:00" idle
check "境界 21:05 は窓の外"        skipped_window t "$THU 21:05" idle
check "ISO 形式の NOW (T 区切り)"    sent           t "${THU}T12:00" idle
# DAYS
check "DAYS 平日: 木曜は実行"       sent           wk "$THU 12:00" idle
check "DAYS 平日: 土曜は外"        skipped_window wk "$SAT 12:00" idle
check "DAYS 平日: 日曜は外"        skipped_window wk "$SUN 12:00" idle
check "DAYS 平日: 月曜は実行"       sent           wk "$MON 12:00" idle
check "DAYS 既定(毎日): 日曜も実行"  sent           t  "$SUN 12:00" idle
# SKIP_IF / 状態
check "SKIP_IF 既定: working は skip" skipped_state t "$THU 12:00" working
check "SKIP_IF 既定: blocked は skip" skipped_state t "$THU 12:00" blocked
check "idle は送る"                sent           t "$THU 12:00" idle
check "done は送る"                sent           t "$THU 12:00" done
check "unknown は idle/done でないので skip" skipped_state t "$THU 12:00" unknown
check "SKIP_IF 上書き: done を skip"  skipped_state skipidle "$THU 12:00" done
check "SKIP_IF 上書き: idle は送る"   sent           skipidle "$THU 12:00" idle
check "窓の外は状態に関わらず外"      skipped_window t "$THU 22:00" idle
# 本文の安全装置
check "先頭が / の本文は failed"     failed         slash "$THU 12:00" idle

# DRY_RUN はログを書かない
[ ! -e "$LOG_DIR/t.log" ] && { pass=$((pass + 1)); echo "ok   DRY_RUN はログを書かない"; } \
  || { failn=$((failn + 1)); echo "FAIL DRY_RUN がログを書いた"; }

# gen-plist: 毎分起動(空の StartCalendarInterval)。時間帯・間隔は tick.sh が判定する
OUT_DIR="$TMP/gen" bash bin/gen-plist.sh t >/dev/null
P="$TMP/gen/local.herdr-sched-t.plist"
grep -A1 '<key>StartCalendarInterval</key>' "$P" | grep -q '<dict/>' && ! grep -q '<key>Hour</key>' "$P" \
  && { pass=$((pass + 1)); echo "ok   plist: 毎分起動(Hour/Minute を焼き込まない)"; } \
  || { failn=$((failn + 1)); echo "FAIL plist: 毎分起動になっていない"; }

# 間隔: 窓の始端から INTERVAL_MIN 刻みの回だけ実行する
check "間隔: 09:05 は実行(5 分刻み)"   sent             t "$THU 09:05" idle
check "間隔: 09:07 は刻みの外"          skipped_interval t "$THU 09:07" idle
NOW="$THU 09:07" bash bin/tick.sh t
ok_q() { [ ! -e "$LOG_DIR/t.log" ] && { pass=$((pass + 1)); echo "ok   $1"; } || { failn=$((failn + 1)); echo "FAIL $1"; }; }
ok_q "刻みの外は(DRY_RUN 以外でも)ログを書かない"
NOW="$THU 08:00" bash bin/tick.sh t; ok_q "窓の外は(DRY_RUN 以外でも)ログを書かない"

# START/END 形式と SCHED_* 上書き
cat >"$JOBS_DIR/se.conf" <<'C'
TARGET=dummy
PROMPT='do the thing'
START=10:00
END=12:00
INTERVAL_MIN=30
C
check "START/END 形式: 10:00 は実行"  sent           se "$THU 10:00" idle
check "START/END 形式: 12:30 は外"    skipped_window se "$THU 12:30" idle
export SCHED_START=08:00
check "SCHED_START 上書き: 08:00 は実行" sent         se "$THU 08:00" idle
check "SCHED_START 上書き: END は conf のまま" skipped_window se "$THU 12:30" idle
unset SCHED_START
export SCHED_WINDOW=13:00-14:00
check "SCHED_WINDOW 上書き: 13:30 は実行" sent        t "$THU 13:30" idle
check "SCHED_WINDOW 上書き: 09:00 は外"   skipped_window t "$THU 09:00" idle
unset SCHED_WINDOW
check "SCHED_INTERVAL_MIN=10: 09:10 は実行" sent t "$THU 09:10" idle
SCHED_INTERVAL_MIN=10 check "SCHED_INTERVAL_MIN=10: 09:05 は刻みの外" skipped_interval t "$THU 09:05" idle

# settings/<job>.settings(Web UI が書く上書き)
export SCHED_SETTINGS_DIR="$TMP/settings"; mkdir -p "$SCHED_SETTINGS_DIR"
printf 'INTERVAL_MIN=15\nSTART=10:00\nEND=11:00\nDAYS=weekdays\n' >"$SCHED_SETTINGS_DIR/t.settings"
check "settings: 間隔 15 分の 10:15 は実行"    sent             t "$THU 10:15" idle
check "settings: 10:05 は刻みの外"            skipped_interval t "$THU 10:05" idle
check "settings: 時間帯 10-11 の 09:00 は外"  skipped_window   t "$THU 09:00" idle
check "settings: weekdays の土曜は外"         skipped_window   t "2026-11-07 10:15" idle
SCHED_INTERVAL_MIN=30 check "SCHED_* は settings より優先" skipped_interval t "$THU 10:15" idle
printf 'ENABLED=0\n' >"$SCHED_SETTINGS_DIR/t.settings"
check "settings: ENABLED=0 は一時停止"        skipped_disabled t "$THU 09:00" idle
ok "show.sh: 実際に効く設定" "$(bash bin/show.sh t | jq -c '[.interval_min,.start,.end,.enabled,.overridden]')" '[5,"09:00","21:00",false,true]'
for bad in 'INTERVAL_MIN=5; rm -rf /' 'COMMAND=echo pwned' 'START=10:00' 'DAYS=$(id)' 'ENABLED=yes'; do
  printf '%s\n' "$bad" >"$SCHED_SETTINGS_DIR/t.settings"
  DRY_RUN=1 NOW="$THU 09:00" bash bin/tick.sh t >/dev/null 2>&1; ok "settings: 不正な行は拒否(exit 2): $bad" "$?" 2
done
# 先が実在するシンボリックリンクと、先が無いもの(OS によらず作れる)の両方を拒否する
: >"$TMP/sym-target"; ln -sf "$TMP/sym-target" "$SCHED_SETTINGS_DIR/sym.settings"; cp "$JOBS_DIR/t.conf" "$JOBS_DIR/sym.conf"
DRY_RUN=1 NOW="$THU 09:00" bash bin/tick.sh sym >/dev/null 2>&1; ok "settings: シンボリックリンクは拒否" "$?" 2
ln -sf "$TMP/no-such-file" "$SCHED_SETTINGS_DIR/sym.settings"
DRY_RUN=1 NOW="$THU 09:00" bash bin/tick.sh sym >/dev/null 2>&1; ok "settings: 先が無いシンボリックリンクも拒否" "$?" 2
rm -rf "$SCHED_SETTINGS_DIR"; unset SCHED_SETTINGS_DIR

# ---- 秒単位
cat >"$JOBS_DIR/sec.conf" <<'C'
TARGET=dummy
PROMPT='do the thing'
WINDOW=09:00:30-09:02
INTERVAL_SEC=20
C
# check の先頭行だけでなく全行の action を見る
acts() { DRY_RUN=1 NOW="$2" FAKE_STATE=idle bash bin/tick.sh "$1" 2>&1 | jq -r '"\(.at[11:]) \(.action)"' | tr '\n' ' '; }
check "秒: 始端 09:00:30 は実行"             sent             sec "$THU 09:00:30" idle
check "秒: 09:00:50 は実行(20 秒刻み)"       sent             sec "$THU 09:00:50" idle
check "秒: 09:00:40 は刻みの外"              skipped_interval sec "$THU 09:00:40" idle
check "秒: 09:00:29 は窓の外"                skipped_window   sec "$THU 09:00:29" idle
check "秒: 終端 HH:MM はその分の 59 秒まで含む" sent           sec "$THU 09:02:50" idle
SCHED_WINDOW=09:00:30-09:02:10 check "秒: 終端を秒まで書けばその秒まで" skipped_window sec "$THU 09:02:30" idle
SCHED_WINDOW=09:00:30-09:02:10 check "秒: 終端ちょうどは実行"     sent           sec "$THU 09:02:10" idle
check "秒: 09:03:00 は窓の外"                skipped_window   sec "$THU 09:03:00" idle
ok "秒: 分指定の NOW はその分の回をすべて判定" "$(acts sec "$THU 09:01")" "09:01:10 sent 09:01:30 sent 09:01:50 sent "
ok "秒: 窓が分の途中から始まる"                "$(acts sec "$THU 09:00")" "09:00:30 sent 09:00:50 sent "
ok "分単位の既存ジョブは 1 分に 1 回だけ"       "$(acts t "$THU 09:05")" "09:05:00 sent "
ok "ログの at は秒まで"                       "$(acts t "$THU 09:05:00")" "09:05:00 sent "
ok "分単位ジョブを秒まで指定: 09:05:30 は刻みの外" "$(acts t "$THU 09:05:30")" "09:05:30 skipped_interval "
ok "show.sh: 秒の間隔は interval_min が null" "$(bash bin/show.sh sec | jq -c '[.interval_min,.interval_sec,.start,.end]')" '[null,20,"09:00:30","09:02"]'
ok "show.sh: 分の間隔は両方出る" "$(bash bin/show.sh t | jq -c '[.interval_min,.interval_sec]')" '[5,300]'
# WATCHER(見張り系の明示): 未設定は null、1 / 0 はそのまま、それ以外は拒否(tick.sh も show.sh も同じ load_job)
ok "show.sh: WATCHER 未設定は null" "$(bash bin/show.sh t | jq -c .watcher)" 'null'
printf 'TARGET=dummy\nPROMPT=x\nWINDOW=09:00-10:00\nINTERVAL_MIN=5\nWATCHER=1\n' >"$JOBS_DIR/w1.conf"
printf 'TARGET=dummy\nPROMPT=x\nWINDOW=09:00-10:00\nINTERVAL_MIN=5\nWATCHER=0\n' >"$JOBS_DIR/w0.conf"
printf 'TARGET=dummy\nPROMPT=x\nWINDOW=09:00-10:00\nINTERVAL_MIN=5\nWATCHER=yes\n' >"$JOBS_DIR/wbad.conf"
ok "show.sh: WATCHER=1" "$(bash bin/show.sh w1 | jq -c .watcher)" '"1"'
ok "show.sh: WATCHER=0" "$(bash bin/show.sh w0 | jq -c .watcher)" '"0"'
ok "WATCHER=yes は拒否(show.sh)" "$(bash bin/show.sh wbad >/dev/null 2>&1; echo $?)" '2'
ok "WATCHER=yes は拒否(tick.sh)" "$(DRY_RUN=1 NOW="$THU 09:05" bash bin/tick.sh wbad >/dev/null 2>&1; echo $?)" '2'
SCHED_INTERVAL_SEC=30 check "SCHED_INTERVAL_SEC=30: 09:00:30 は実行" sent t "$THU 09:00:30" idle
SCHED_INTERVAL_SEC=30 check "SCHED_INTERVAL_SEC は conf の INTERVAL_MIN より優先" skipped_interval t "$THU 09:00:20" idle
printf 'TARGET=dummy\nPROMPT=x\nWINDOW=09:00-10:00\nINTERVAL_MIN=1\nINTERVAL_SEC=30\n' >"$JOBS_DIR/both_iv.conf"
DRY_RUN=1 NOW="$THU 09:00" bash bin/tick.sh both_iv >/dev/null 2>&1; ok "INTERVAL_MIN と INTERVAL_SEC の同時指定はエラー" "$?" 2
for bad in "WINDOW=09:00-10:00 INTERVAL_SEC=0" "WINDOW=09:00:60-10:00 INTERVAL_SEC=5" "WINDOW=09:00:5-10:00 INTERVAL_SEC=5"; do
  printf 'TARGET=dummy\nPROMPT=x\n%s\n' $bad >"$JOBS_DIR/badsec.conf"
  DRY_RUN=1 NOW="$THU 09:00" bash bin/tick.sh badsec >/dev/null 2>&1; ok "不正な秒指定はエラー: $bad" "$?" 2
done
export SCHED_SETTINGS_DIR="$TMP/settings"; mkdir -p "$SCHED_SETTINGS_DIR"
printf 'INTERVAL_SEC=15\nSTART=10:00:00\nEND=10:00:45\n' >"$SCHED_SETTINGS_DIR/t.settings"
ok "settings: INTERVAL_SEC と HH:MM:SS" "$(acts t "$THU 10:00")" "10:00:00 sent 10:00:15 sent 10:00:30 sent 10:00:45 sent "
printf 'INTERVAL_MIN=1\nINTERVAL_SEC=15\n' >"$SCHED_SETTINGS_DIR/t.settings"
DRY_RUN=1 NOW="$THU 10:00" bash bin/tick.sh t >/dev/null 2>&1; ok "settings: INTERVAL_MIN と INTERVAL_SEC の同時指定は拒否" "$?" 2
printf 'INTERVAL_SEC=15\n' >"$SCHED_SETTINGS_DIR/t.settings"
ok "settings の INTERVAL_SEC は conf の INTERVAL_MIN を上書き" "$(acts t "$THU 09:00" | wc -w | tr -d ' ')" 8
rm -rf "$SCHED_SETTINGS_DIR"; unset SCHED_SETTINGS_DIR
mkcmd0() { printf "COMMAND='echo hi'\nWINDOW=00:00-23:59\nINTERVAL_SEC=%s\n" "$2" >"$JOBS_DIR/$1.conf"; }
mkcmd0 csec 30
NOW="$THU 12:00" bash bin/tick.sh csec
ok "COMMAND 型 30 秒刻み: 1 分で 2 回実行" "$(jq -r '"\(.at[11:]) \(.action)"' "$LOG_DIR/csec.log" | tr '\n' ' ')" "12:00:00 ran 12:00:30 ran "

# ---- COMMAND 型
mkcmd() { printf '%s\nWINDOW=%s\nINTERVAL_MIN=2\n' "$2" "${3:-00:00-23:59}" >"$JOBS_DIR/$1.conf"; }
mkcmd both "TARGET=dummy
COMMAND='echo hi'"
mkcmd cmd "COMMAND='echo out-line; echo err-line >&2; exit 3'"
mkcmd cmdok "COMMAND='echo ok'" 09:00-10:00
mkcmd slow "COMMAND='sleep 5'
COMMAND_TIMEOUT=1"
mkcmd long "COMMAND='head -c 2000 /dev/zero | tr \"\\\\0\" x'"
mkcmd lock "COMMAND='sleep 2'"
last() { tail -1 "$LOG_DIR/$1.log"; }
bash bin/tick.sh both >/dev/null 2>&1; ok "COMMAND と TARGET の同時指定はエラー(exit 2)" "$?" 2
check "COMMAND 型 DRY_RUN は実行せず ran" ran cmd "$THU 12:00"
check "COMMAND 型 窓の外は skipped_window" skipped_window cmdok "$THU 08:55"
check "COMMAND 型 終日 23:58 は実行(2 分刻み)" ran cmd "$THU 23:58"
NOW="$THU 12:00" bash bin/tick.sh cmd
ok "stdout/stderr/exit を記録" "$(last cmd | jq -r '[.action,.exit,.stdout,.stderr]|join("|")' | tr '\n' '~')" "failed|3|out-line|err-line~"
NOW="$THU 12:00" bash bin/tick.sh long
ok "出力は 500 字に切り詰め" "$(last long | jq -r '.stdout|length')" 500
t0=$SECONDS; NOW="$THU 12:00" bash bin/tick.sh slow
ok "タイムアウトで failed / exit 124" "$(last slow | jq -r '[.action,.exit]|join("|")')" "failed|124"
ok "タイムアウトは 4 秒以内に打ち切る" "$([ $((SECONDS - t0)) -le 4 ] && echo y)" y
NOW="$THU 12:00" bash bin/tick.sh lock & sleep 0.5
NOW="$THU 12:00" bash bin/tick.sh lock; wait
ok "多重実行は skipped_locked" "$(jq -r .action "$LOG_DIR/lock.log" | sort | tr '\n' ' ')" "ran skipped_locked "
ok "実行後にロックが残らない" "$([ -e "$LOG_DIR/lock.lock.d" ] && echo left || echo none)" none
mkdir "$LOG_DIR/cmd.lock.d"; echo 999999 >"$LOG_DIR/cmd.lock.d/pid"
NOW="$THU 12:00" bash bin/tick.sh cmd; ok "stale lock は回収して実行" "$(last cmd | jq -r .action)" failed
mkcmd fast "COMMAND='echo quick'
COMMAND_TIMEOUT=97"
t0=$SECONDS; NOW="$THU 12:00" bash bin/tick.sh fast
ok "すぐ終わるコマンドは待たずに ran" "$(last fast | jq -r '[.action,.stdout]|join("|")')$([ $((SECONDS - t0)) -le 1 ] && echo y)" "ran|quicky"
ok "時間切れの見張り(sleep)が残らない" "$(pgrep -f '^sleep 97$' >/dev/null && echo left || echo none)" none

# ---- dispatch.sh: 複数のジョブを 1 回の起動で(各ジョブはサブシェル。判定と記録は tick.sh と同じ)
rm -f "$LOG_DIR/cmd.log" "$LOG_DIR/fast.log" "$LOG_DIR/cmdok.log"
NOW="$THU 12:00" bash bin/dispatch.sh cmd fast cmdok; drc=$?
ok "dispatch: 各ジョブが自分のログに記録(窓の外は記録しない)" "$(last cmd | jq -r .action) $(last fast | jq -r .action) $([ -e "$LOG_DIR/cmdok.log" ] && echo logged || echo quiet)" "failed ran quiet"
ok "dispatch: 1 つでも失敗があれば終了コード 1" "$drc" 1
ok "dispatch: ジョブの変数が混ざらない(cmd の stdout が fast に出ない)" "$(last fast | jq -r .stdout)" quick
NOW="$THU 12:00" bash bin/dispatch.sh fast; ok "dispatch: 全部成功なら 0" "$?" 0
bash bin/dispatch.sh >/dev/null 2>&1; ok "dispatch: 引数なしは使い方の誤り(exit 2)" "$?" 2
NOW="$THU 12:00" bash bin/dispatch.sh fast nosuchjob >/dev/null 2>&1; ok "dispatch: 無いジョブは失敗にし、ほかは動く" "$? $(last fast | jq -r .action)" "1 ran"
OUT_DIR="$TMP/gen" bash bin/gen-plist.sh --dispatch fast cmd >/dev/null
ok "gen-plist --dispatch: 1 本の plist に dispatch.sh とジョブ名" "$(grep -c '<string>' "$TMP/gen/local.herdr-sched-dispatch.plist") $(grep -c 'bin/dispatch.sh</string>' "$TMP/gen/local.herdr-sched-dispatch.plist")" "8 1"
OUT_DIR="$TMP/gen" bash bin/gen-plist.sh --dispatch fast nosuchjob >/dev/null 2>&1; ok "gen-plist --dispatch: 無いジョブは止める" "$?" 2
mkcmd dispatch "COMMAND='true'"; OUT_DIR="$TMP/gen" bash bin/gen-plist.sh dispatch >/dev/null 2>&1; ok "ジョブ名 dispatch は予約済み" "$?" 2; rm -f "$JOBS_DIR/dispatch.conf"

# ---- 発火条件(GATE)。順序: 窓・間隔・曜日 → ENABLED → GATE → SKIP_IF → ロック → 発火
gjob() { printf "TARGET=dummy\nPROMPT='do the thing'\nWINDOW=09:00-21:00\nINTERVAL_MIN=5\n%s\n" "$2" >"$JOBS_DIR/$1.conf"; }
gjob g0 "GATE='true'"
gjob g1 "GATE='exit 3'"
gjob gslow "GATE='sleep 5'
GATE_TIMEOUT=1"
gjob gmiss "GATE='/nonexistent/gate-cmd'"
gjob goff "GATE='touch $TMP/gate-ran'
ENABLED=0"
gjob gsecret "GATE='echo SECRET-OUT; echo SECRET-ERR >&2; exit 1'"
gjob gbad "GATE='true'
GATE_TIMEOUT=0"
check "GATE なし: 今と同じく発火"        sent         t      "$THU 12:00" idle
check "GATE が 0: 発火"                 sent         g0     "$THU 12:00" idle
check "GATE が 0 以外: 発火しない"       skipped_gate g1     "$THU 12:00" idle
check "GATE の時間切れ: 発火しない"      skipped_gate gslow  "$THU 12:00" idle
check "GATE の起動失敗: 発火しない"      skipped_gate gmiss  "$THU 12:00" idle
check "GATE が 0 のときだけ SKIP_IF を見る" skipped_state g0   "$THU 12:00" working
check "GATE が 0 以外なら SKIP_IF より先に止まる" skipped_gate g1 "$THU 12:00" working
check "窓の外では GATE を見ない"          skipped_window g1   "$THU 22:00" idle
ok "理由: exit / timeout / 起動失敗" "$(for j in g1 gslow gmiss; do DRY_RUN=1 NOW="$THU 12:00" FAKE_STATE=idle bash bin/tick.sh $j | jq -r .reason; done | tr '\n' '|')" "gate exit 3|gate timeout after 1s|gate could not start (exit 127)|"
DRY_RUN=1 NOW="$THU 12:00" bash bin/tick.sh goff >/dev/null
ok "ENABLED=0 なら GATE を実行しない" "$([ -e "$TMP/gate-ran" ] && echo ran || echo not)" not
rm -f "$LOG_DIR/gsecret.log"; NOW="$THU 12:00" bash bin/tick.sh gsecret
ok "GATE の出力はログに残さない" "$(jq -r .action "$LOG_DIR/gsecret.log") $(grep -c SECRET "$LOG_DIR/gsecret.log")" "skipped_gate 0"
bash bin/tick.sh gbad >/dev/null 2>&1; ok "GATE_TIMEOUT=0 はエラー(exit 2)" "$?" 2
ok "show.sh: gate の有無" "$(bash bin/show.sh g1 | jq -c .gate) $(bash bin/show.sh t | jq -c .gate)" "true false"
# コマンド型: GATE が 0 以外なら、コマンドもロックも触らない
printf "COMMAND='touch %s/gcmd-ran'\nWINDOW=00:00-23:59\nINTERVAL_MIN=2\nGATE='false'\n" "$TMP" >"$JOBS_DIR/gcmd.conf"
NOW="$THU 12:00" bash bin/tick.sh gcmd
ok "コマンド型: GATE で止めるとコマンドを実行しない" "$(jq -r .action "$LOG_DIR/gcmd.log") $([ -e "$TMP/gcmd-ran" ] && echo ran || echo not) $([ -e "$LOG_DIR/gcmd.lock.d" ] && echo locked || echo free)" "skipped_gate not free"
sed -i.bak "s/GATE='false'/GATE='true'/" "$JOBS_DIR/gcmd.conf"; rm -f "$JOBS_DIR/gcmd.conf.bak"
NOW="$THU 12:00" bash bin/tick.sh gcmd
ok "コマンド型: GATE が 0 なら実行" "$(tail -n1 "$LOG_DIR/gcmd.log" | jq -r .action) $([ -e "$TMP/gcmd-ran" ] && echo ran || echo not)" "ran ran"
rm -f "$JOBS_DIR"/g*.conf

# ---- 回ごとに本文を切り替える(PROMPT_RULES / PROMPT_CMD)と {{名前}} の差し込み(PROMPT_VARS)
pjob() { printf "TARGET=dummy\nWINDOW=09:00-21:00\nINTERVAL_MIN=5\n%s\n" "$2" >"$JOBS_DIR/$1.conf"; }
# dry <job> <NOW> [jq の式]: 1 行目の判定から取り出す(既定は action)
dry() { DRY_RUN=1 NOW="$2" FAKE_STATE=idle bash bin/tick.sh "$1" 2>&1 | head -n1 | jq -r "${3:-.action}" 2>&1; }
echo 'B の本文 {{hour}}時' >"$TMP/b.md"
pjob pr "PROMPT_RULES='
# 00 分は A、30 分は B(ファイル)、平日の 9-12 時の 15 分おきは C
min=00 | A の本文
min=30 | file:$TMP/b.md
days=weekdays hour=9-12 min=*/15 | C の本文
'
PROMPT='既定の本文'"
check "RULES: 00 分は A で送る"            sent  pr "$THU 12:00" idle
ok "RULES: 00 分の本文と出どころ"  "$(dry pr "$THU 12:00" '.prompt + " " + .text')" "rule:1 A の本文"
ok "RULES: 30 分は file: の中身(差し込み済み)" "$(dry pr "$THU 12:30" '.prompt + " " + .text')" "rule:2 B の本文 12時"
ok "RULES: 平日 9-12 時の 15 分" "$(dry pr "$THU 10:45" '.prompt + " " + .text')" "rule:3 C の本文"
ok "RULES: 土曜の 10:45 は既定" "$(dry pr "$SAT 10:45" '.prompt + " " + .text')" "default 既定の本文"
ok "RULES: どれにも合わなければ PROMPT" "$(dry pr "$THU 13:05" '.prompt')" "default"
pjob prnodef "PROMPT_RULES='min=00 | 毎時'"
check "RULES: 既定が無く合わなければ送らない" skipped_prompt prnodef "$THU 12:05" idle
check "RULES: 合えば送る"                    sent           prnodef "$THU 12:00" idle
ok "show.sh: prompt_rules の数" "$(bash bin/show.sh pr | jq -c '[.prompt_rules,.prompt_cmd,.prompt_vars]') $(bash bin/show.sh t | jq -c '[.prompt_rules,.prompt_cmd,.prompt_vars]')" "[3,false,0] [0,false,0]"
pjob pbad1 "PROMPT_RULES='min=60 | x'"
pjob pbad2 "PROMPT_RULES='minute=0 | x'"
pjob pbad3 "PROMPT_RULES='min=00 x'"
pjob pbad4 "PROMPT_RULES='days=funday | x'"
pjob pbad5 "PROMPT_RULES='min=1, | x'"
printf "COMMAND='true'\nWINDOW=09:00-21:00\nINTERVAL_MIN=5\nPROMPT_RULES='* | x'\n" >"$JOBS_DIR/pbad6.conf"
ok "RULES: 不正な定義は読み込みでエラー(exit 2)" "$(for j in pbad1 pbad2 pbad3 pbad4 pbad5 pbad6; do bash bin/show.sh $j >/dev/null 2>&1; printf '%s ' $?; done)" "2 2 2 2 2 2 "
pjob pslash "PROMPT_RULES='* | /loop bad'"
check "RULES: 先頭が / の本文は failed" failed pslash "$THU 12:00" idle
# PROMPT_CMD: 時刻は SCHED_* で渡る。0 で空なら RULES → PROMPT へ
pjob pc "PROMPT_CMD='[ \"\$SCHED_MIN\" = 00 ] && echo \"cmd \$SCHED_JOB \$SCHED_HOUR:\$SCHED_MIN dow=\$SCHED_DOW\"; exit 0'
PROMPT='既定'"
ok "CMD: 出力が本文" "$(dry pc "$THU 12:00" '.prompt + " " + .text')" "cmd cmd pc 12:00 dow=4"
ok "CMD: 空なら既定" "$(dry pc "$THU 12:05" '.prompt + " " + .text')" "default 既定"
pjob pcfail "PROMPT_CMD='exit 5'
PROMPT='既定'"
pjob pcslow "PROMPT_CMD='sleep 5'
PROMPT_CMD_TIMEOUT=1"
ok "CMD: 0 以外・時間切れは failed" "$(dry pcfail "$THU 12:00" '.action + ":" + .reason')|$(dry pcslow "$THU 12:00" '.action + ":" + .reason')" "failed:PROMPT_CMD exit 5|failed:PROMPT_CMD timeout after 1s"
# GATE / COMMAND にも SCHED_* が渡る
pjob pg "GATE='[ \"\$SCHED_MIN\" = 30 ]'
PROMPT='x'"
ok "SCHED_* は GATE にも渡る" "$(dry pg "$THU 12:30") $(dry pg "$THU 12:00")" "sent skipped_gate"
# PROMPT_VARS: 本文の {{名前}} にコマンドの出力を差し込む
pjob pv "PROMPT_VARS='
hits | printf \"%s\" \"結果{{at}}\"; touch $TMP/pv-hits
unused | touch $TMP/pv-unused
'
PROMPT='{{job}} {{date}} {{time}}: {{hits}} / {{hits}} / {{unknown}}'"
rm -f "$TMP/pv-hits" "$TMP/pv-unused"
ok "VARS: 差し込み(値の中の {{}} は展開しない・知らない名前は残す)" "$(dry pv "$THU 12:00" .text)" "pv $THU 12:00:00: 結果{{at}} / 結果{{at}} / {{unknown}}"
ok "VARS: 本文に無い名前のコマンドは実行しない" "$([ -e "$TMP/pv-hits" ] && echo ran || echo not) $([ -e "$TMP/pv-unused" ] && echo ran || echo not)" "ran not"
rm -f "$TMP/pv-hits"
DRY_RUN=1 NOW="$THU 12:00" FAKE_STATE=working bash bin/tick.sh pv >/dev/null
ok "VARS: 送らない状態ならコマンドを実行しない" "$([ -e "$TMP/pv-hits" ] && echo ran || echo not)" "not"
pjob pv1 "PROMPT_VARS='hits | grep -c nothing /dev/null'
PROMPT='見つかった: {{hits}}'"
ok "VARS: exit 1 はこの回を送らない" "$(dry pv1 "$THU 12:00" '.action + ":" + .reason')" "skipped_prompt:PROMPT_VARS hits: exit 1 (nothing to send)"
pjob pv2 "PROMPT_VARS='hits | exit 4'
PROMPT='{{hits}}'"
pjob pv3 "PROMPT_VARS='hits | sleep 5'
PROMPT_VAR_TIMEOUT=1
PROMPT='{{hits}}'"
ok "VARS: 0/1 以外・時間切れは failed" "$(dry pv2 "$THU 12:00" '.action + ":" + .reason')|$(dry pv3 "$THU 12:00" '.action + ":" + .reason')" "failed:PROMPT_VARS hits: exit 4|failed:PROMPT_VARS hits: timeout after 1s"
pjob pv4 "PROMPT_VARS='cmd | echo /evil'
PROMPT='{{cmd}}'"
check "VARS: 差し込み後に先頭が / なら failed" failed pv4 "$THU 12:00" idle
pjob pvbad1 "PROMPT_VARS='hour | true'
PROMPT=x"
pjob pvbad2 "PROMPT_VARS='a | true
a | true'
PROMPT=x"
pjob pvbad3 "PROMPT_VARS='Bad | true'
PROMPT=x"
ok "VARS: 不正な定義は読み込みでエラー(exit 2)" "$(for j in pvbad1 pvbad2 pvbad3; do bash bin/show.sh $j >/dev/null 2>&1; printf '%s ' $?; done)" "2 2 2 "
rm -f "$JOBS_DIR"/p*.conf

# ---- DB のジョブ定義(tb job。config 表の sched-job:<job> / sched-settings:<job>)
SJ="$TMP/sj"; mkdir -p "$SJ/jobs" "$SJ/settings" "$SJ/logs"
export TB_SCHED_DIR="$SJ" HERDR_STATE_DIR_SAVE="${XDG_STATE_HOME:-}" XDG_STATE_HOME="$TMP/state"
printf "TARGET=dummy\nPROMPT='do the thing'\nWINDOW=09:00-21:00\nINTERVAL_MIN=5\nDAYS=weekdays\n" >"$SJ/jobs/p1.conf"
printf "COMMAND='echo hi'\nWINDOW=00:00-23:59\nINTERVAL_MIN=2\nGATE='true'\n" >"$SJ/jobs/c1.conf"
printf "INTERVAL_MIN=10\nSTART=10:00\nEND=12:00\nDAYS=daily\nENABLED=1\n" >"$SJ/settings/p1.settings"
SJTB="bash $PWD/../taskboard/bin/tb"
sjrun() {  # <job> <NOW> → 判定の JSON(dry-run)。ts は時刻で変わるので落とす
  JOBS_DIR="$SJ/jobs" SCHED_SETTINGS_DIR="$SJ/settings" LOG_DIR="$SJ/logs" TB_DB="$TMP/sj.db" DRY_RUN=1 NOW="$2" FAKE_STATE=idle \
    bash bin/tick.sh "$1" 2>&1 | jq -c 'del(.ts)'
}
sjshow() { JOBS_DIR="$SJ/jobs" SCHED_SETTINGS_DIR="$SJ/settings" TB_DB="$TMP/sj.db" bash bin/show.sh "$1"; }
before=""; for j in p1 c1; do for t in "$THU 09:55" "$THU 10:00" "$THU 10:05" "$THU 10:10" "$SAT 11:00" "$THU 13:00"; do before+="$(sjrun $j "$t")|"; done; done
showb="$(sjshow p1 | jq -c 'del(.source, .settings_source)') $(sjshow c1 | jq -c 'del(.source, .settings_source)')"
ok "DB: 取り込み前はファイルを読む" "$(sjshow p1 | jq -r '.source + " " + .settings_source')" "file file"
TB_DB="$TMP/sj.db" $SJTB sched-job import --dry-run >/dev/null
ok "DB: import --dry-run は何も変えない" "$(ls "$SJ/jobs" | tr '\n' ' ')$(TB_DB="$TMP/sj.db" sqlite3 "$TMP/sj.db" "SELECT COUNT(*) FROM config WHERE key LIKE 'sched-%';")" "c1.conf p1.conf 0"
TB_DB="$TMP/sj.db" $SJTB sched-job import >/dev/null
ok "DB: import でファイルを退かし、DB の行になる" "$(ls "$SJ/jobs" "$SJ/settings" | grep -c conf) $(sjshow p1 | jq -r '.source + " " + .settings_source') $(sjshow c1 | jq -r '.source + " " + .settings_source')" "0 db db db none"
after=""; for j in p1 c1; do for t in "$THU 09:55" "$THU 10:00" "$THU 10:05" "$THU 10:10" "$SAT 11:00" "$THU 13:00"; do after+="$(sjrun $j "$t")|"; done; done
ok "DB: 取り込みの前と後で、判定の結果が同じ" "$after" "$before"
ok "DB: 取り込みの前と後で、実際に効く設定が同じ" "$(sjshow p1 | jq -c 'del(.source, .settings_source)') $(sjshow c1 | jq -c 'del(.source, .settings_source)')" "$showb"
ok "DB: 一覧は DB とファイルの和" "$(printf "COMMAND='true'\nWINDOW=00:00-23:59\nINTERVAL_MIN=5\n" >"$SJ/jobs/f1.conf"; JOBS_DIR="$SJ/jobs" TB_DB="$TMP/sj.db" bash bin/jobs.sh | tr '\n' ' ')" "c1 f1 p1 "
ok "DB: 実行も DB の定義で(ログを書く)" "$(JOBS_DIR="$SJ/jobs" SCHED_SETTINGS_DIR="$SJ/settings" LOG_DIR="$SJ/logs" TB_DB="$TMP/sj.db" NOW="$THU 12:00" bash bin/tick.sh c1; tail -1 "$SJ/logs/c1.log" | jq -r '.action + " " + .stdout')" "ran hi"
# DB の行がファイルより先。DB の無い・読めない環境ではファイル
printf "COMMAND='echo from-file'\nWINDOW=00:00-23:59\nINTERVAL_MIN=2\n" >"$SJ/jobs/c1.conf"
ok "DB: 同じ名前のファイルより DB の行が勝つ" "$(sjshow c1 | jq -r .source)" "db"
ok "DB: DB が無ければファイル" "$(JOBS_DIR="$SJ/jobs" TB_DB="$TMP/nothing.db" bash bin/show.sh c1 | jq -r .source)" "file"
rm -f "$SJ/jobs/c1.conf" "$SJ/jobs/f1.conf"
unset TB_SCHED_DIR; export XDG_STATE_HOME="$HERDR_STATE_DIR_SAVE"; [ -n "$XDG_STATE_HOME" ] || unset XDG_STATE_HOME

# ---- プロンプト送信は tb を通る(疑似 herdr と一時 DB。本物は呼ばない)
export FAKE_LOG="$TMP/herdr.log" TB_DB="$TMP/tb.db"; : >"$FAKE_LOG"
export FAKE_AGENTS='[{"pane_id":"wD:p1","name":"dummy","agent_status":"idle","agent_session":{"value":"sd"}}]'
NOW="2026-11-05 09:00" HERDR_BIN="$PWD/../taskboard/test/fake-herdr" bash bin/tick.sh t
ok "送信: tb 経由で sent" "$(last t | jq -r .action)" sent
ok "送信: 宛先にプロンプト" "$(grep -c $'^prompt dummy\tdo the thing' "$FAKE_LOG")" 1
ok "送信: messages に kind=scheduler" "$(sqlite3 "$TB_DB" "SELECT kind||':'||ref||':'||result FROM messages;")" "scheduler:job:t:sent"
printf "TARGET=dummy\nWINDOW=09:00-21:00\nINTERVAL_MIN=5\nPROMPT_RULES='min=00 | 毎時 {{n}}'\nPROMPT_VARS='n | echo 42'\n" >"$JOBS_DIR/psend.conf"
NOW="2026-11-05 10:00" HERDR_BIN="$PWD/../taskboard/test/fake-herdr" bash bin/tick.sh psend
ok "送信: 切り替え・差し込み後の本文を送り、ログに出どころ(本文は残さない)" "$(grep -c $'^prompt dummy\t毎時 42' "$FAKE_LOG") $(last psend | jq -r '.action + " " + .prompt + " " + (has("text")|tostring)')" "1 sent rule:1 false"
rm -f "$JOBS_DIR/psend.conf"
unset FAKE_LOG FAKE_AGENTS; export TB_DB="$TMP/none.db"

# ---- 既定は DB だけ: SCHED_FILE_FALLBACK が無ければ jobs/*.conf・settings/*.settings を読まない
unset SCHED_FILE_FALLBACK
D="$TMP/dbonly"; mkdir -p "$D/jobs" "$D/settings" "$D/logs"
printf "COMMAND='echo hi'\nWINDOW=00:00-23:59\nINTERVAL_MIN=2\n" >"$D/jobs/fonly.conf"
dbo() { JOBS_DIR="$D/jobs" SCHED_SETTINGS_DIR="$D/settings" LOG_DIR="$D/logs" TB_DB="$TMP/sj.db" "$@"; }
out=$(dbo bash bin/show.sh fonly 2>&1); rc=$?
ok "既定: DB に無いジョブは show.sh が読まない(取り込みを促す)" "$rc $(grep -c 'tb job import fonly' <<<"$out")" "2 1"
ok "既定: jobs.sh は DB のジョブだけ" "$(dbo bash bin/jobs.sh | grep -c '^fonly$')" "0"
out=$(dbo env NOW="$THU 10:00" bash bin/tick.sh fonly 2>/dev/null); rc=$?
ok "既定: tick.sh は取り込んでいないジョブを動かさず failed を記録" "$rc $(jq -r '.action + " " + (.reason | test("tb job import fonly") | tostring)' "$D/logs/fonly.log")" "2 failed true"
dbo env NOW="$THU 10:02" bash bin/tick.sh fonly 2>/dev/null
ok "既定: 同じ理由の failed は毎分は書き足さない" "$(wc -l <"$D/logs/fonly.log" | tr -d ' ')" "1"
ok "既定: DRY_RUN でも failed を出す" "$(dbo env DRY_RUN=1 NOW="$THU 10:00" bash bin/tick.sh fonly 2>/dev/null | jq -r .action)" "failed"
TB_DB="$TMP/sj.db" sqlite3 "$TMP/sj.db" "INSERT INTO config (herdr_session, key, body, updated_by, updated_at) VALUES ('default', 'sched-job:fonly', 'COMMAND=''echo hi''
WINDOW=00:00-23:59
INTERVAL_MIN=2', 'human', 'x');"
printf 'INTERVAL_MIN=30\n' >"$D/settings/fonly.settings"
ok "既定: DB に定義があれば動き、ファイルの settings は読まない" \
  "$(dbo env DRY_RUN=1 NOW="$THU 10:02" bash bin/tick.sh fonly 2>/dev/null | jq -r .action) $(dbo bash bin/show.sh fonly | jq -r '.settings_source + " " + (.interval_min|tostring)')" "ran none 2"

echo "---- pass=$pass fail=$failn"
[ "$failn" = 0 ]
