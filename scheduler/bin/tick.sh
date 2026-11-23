#!/bin/bash
# 使い方: tick.sh <job>
#   NOW="YYYY-MM-DD HH:MM[:SS]"  時刻を差し替える(ジョブの TZ での現地時刻)。待たずに判定する
#                           秒まで書くとその 1 秒だけ、分までならその分(00〜59 秒)に入る回をすべて判定する
#   DRY_RUN=1               herdr を呼ばず、判定結果を stdout に JSON で出す(ログは書かない)
#   FAKE_STATE=<state>      DRY_RUN 時に TARGET の状態を仮定する(テスト用)
# launchd は毎分起動する。実行するかどうか(曜日・時間帯・間隔・一時停止)はここで判定するので、
# 間隔や時間帯を変えても plist を作り直す必要はない。対象外の回は(DRY_RUN 以外では)何も記録しない。
# 判定は秒単位。起動した分の中に刻みの回が複数ある(INTERVAL_SEC が 60 未満など)ときは、その時刻まで待って順に実行する。
set -u
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# 定義が DB に無いジョブ(jobs/*.conf を取り込んでいないなど)は動かさず、failed を記録する(Web UI の一覧と logs/<job>.log で見える)。
# 毎分起動されるので、同じ理由が最後の記録なら書き足さない
if [[ "${1:-}" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] && ! sched_files_ok && ! sched_db_body "sched-job:$1" >/dev/null; then
  _why=$(sched_missing_msg "$1")
  _line=$(jq -nc --arg ts "$(date '+%Y-%m-%dT%H:%M:%S%z')" --arg job "$1" --arg reason "$_why" '{ts:$ts,job:$job,action:"failed",reason:$reason}')
  if [ "${DRY_RUN:-0}" = 1 ]; then echo "$_line"
  else
    mkdir -p "$LOG_DIR"
    [ "$(tail -n 1 "$LOG_DIR/$1.log" 2>/dev/null | jq -r 'select(.action=="failed") | .reason' 2>/dev/null)" = "$_why" ] \
      || echo "$_line" >>"$LOG_DIR/$1.log"
  fi
  die "$_why"
fi
load_job "${1:-}"

DRY_RUN="${DRY_RUN:-0}"
[ "$DRY_RUN" = 1 ] || mkdir -p "$LOG_DIR"
PANE=""; STATE=""; EXITC=""; OUT_S=""; ERR_S=""; PSRC=""; DRY_TEXT=""

# 時刻の変換(BSD date / GNU date の両方)
if date -d @0 +%s >/dev/null 2>&1; then
  to_epoch() { date -d "$1" +%s 2>/dev/null; }
  fmt_epoch() { date -d "@$1" "+$2"; }
else
  to_epoch() { date -j -f '%Y-%m-%d %H:%M:%S' "$1" +%s 2>/dev/null; }
  fmt_epoch() { date -r "$1" "+$2"; }
fi

# 判定する範囲 [SLOT_LO, SLOT_HI](epoch 秒)。通常は今の分の 00〜59 秒
FAKED=0
if [ -n "${NOW:-}" ]; then
  FAKED=1; n="${NOW/T/ }"
  if [[ "$n" =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2}\ [0-9]{2}:[0-9]{2}:[0-9]{2}) ]]; then
    NOW_E=$(to_epoch "${BASH_REMATCH[1]}") || die "NOW を解釈できない: $NOW"
    SLOT_LO=$NOW_E; SLOT_HI=$NOW_E
  elif [[ "$n" =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2}\ [0-9]{2}:[0-9]{2})$ ]]; then
    NOW_E=$(to_epoch "${BASH_REMATCH[1]}:00") || die "NOW を解釈できない: $NOW"
    SLOT_LO=$NOW_E; SLOT_HI=$((NOW_E + 59))
  else
    die "NOW を解釈できない: $NOW (YYYY-MM-DD HH:MM[:SS])"
  fi
  [ -n "$NOW_E" ] || die "NOW を解釈できない: $NOW"
else
  NOW_E=$(date +%s)
  SLOT_LO=$((NOW_E - NOW_E % 60)); SLOT_HI=$((SLOT_LO + 59))
fi
read -r DAY dow HH MM SS <<<"$(fmt_epoch "$SLOT_LO" '%Y-%m-%d %u %H %M %S')"
SOD_LO=$((10#$HH * 3600 + 10#$MM * 60 + 10#$SS)); SOD_HI=$((SOD_LO + SLOT_HI - SLOT_LO))
AT="$DAY $HH:$MM:$SS"
sod_label() { printf '%02d:%02d:%02d' $(($1 / 3600)) $(($1 % 3600 / 60)) $(($1 % 60)); }

emit() { # action reason
  local line
  line=$(jq -nc --arg ts "$(date '+%Y-%m-%dT%H:%M:%S%z')" --arg job "$JOB" --arg action "$1" \
    --arg reason "$2" --arg at "$AT" --arg target "$TARGET" --arg pane "$PANE" --arg state "$STATE" \
    --arg exitc "$EXITC" --arg out "$OUT_S" --arg err "$ERR_S" --arg psrc "$PSRC" --arg text "$DRY_TEXT" \
    --argjson dry "$([ "$DRY_RUN" = 1 ] && echo true || echo false)" \
    '{ts:$ts,job:$job,action:$action,reason:$reason,at:$at}
     + (if $target!="" then {target:$target} else {} end)
     + (if $pane!="" then {pane:$pane} else {} end)
     + (if $state!="" then {state:$state} else {} end)
     + (if $psrc!="" then {prompt:$psrc} else {} end)
     + (if $exitc!="" then {exit:($exitc|tonumber),stdout:$out,stderr:$err} else {} end)
     + (if $dry and $text!="" then {text:$text} else {} end)
     + (if $dry then {dry_run:true} else {} end)')
  if [ "$DRY_RUN" = 1 ]; then echo "$line"; else echo "$line" >>"$LOG_DIR/$JOB.log"; fi
}
fail() { emit failed "$1"; return 1; }

# 対象外の回: DRY_RUN では理由を出し、通常は記録せずに終わる(毎分起動のためログを埋めない)
quiet_skip() { [ "$DRY_RUN" = 1 ] && emit "$1" "$2"; exit 0; }

# 1. 曜日・時間帯・間隔 -> この範囲で実行する回(その日の 0 時からの秒)
case " $(days_to_nums) " in *" $dow "*) ;; *) quiet_skip skipped_window "day $dow not in DAYS ($DAYS)" ;; esac
lo=$SOD_LO; hi=$SOD_HI
((lo < WIN_START)) && lo=$WIN_START
((hi > WIN_END)) && hi=$WIN_END
if ((lo > hi)); then
  quiet_skip skipped_window "$(sod_label "$SOD_LO") outside $WINDOW"
fi
DUE=()
first=$((WIN_START + (lo - WIN_START + INTERVAL_SEC - 1) / INTERVAL_SEC * INTERVAL_SEC))
for ((p = first; p <= hi; p += INTERVAL_SEC)); do DUE+=($((SLOT_LO + p - SOD_LO))); done
((${#DUE[@]})) || quiet_skip skipped_interval "$(sod_label "$SOD_LO") not on the $(interval_label)-grid from $START"
[ "$ENABLED" = 1 ] || { AT=$(fmt_epoch "${DUE[0]}" '%Y-%m-%d %H:%M:%S'); emit skipped_disabled "paused (ENABLED=0)"; exit 0; }

# 実際に起動された回: 過ぎてしまった回は最後の 1 回にまとめる(launchd の起動遅れ・スリープ復帰で取りこぼさず、まとめて何度も送らない)
if ((!FAKED)); then
  past=""; future=()
  for p in "${DUE[@]}"; do if ((p <= NOW_E)); then past=$p; else future+=("$p"); fi; done
  DUE=(${past:+"$past"} ${future[@]+"${future[@]}"})   # macOS の bash 3.2 + set -u では空配列の展開に +"" が要る
fi

kill_tree() { local c; for c in $(pgrep -P "$1" 2>/dev/null); do kill_tree "$c"; done; kill -KILL "$1" 2>/dev/null; }
LOCK="$LOG_DIR/$JOB.lock.d"; OUTF=""; ERRF=""; HAVE_LOCK=0
cleanup() { ((HAVE_LOCK)) && rm -rf "$LOCK"; rm -f "$OUTF" "$ERRF"; HAVE_LOCK=0; }
trap cleanup EXIT

# COMMAND 型: agent の状態を見ずにコマンドを実行する
run_command() {
  if [ "$DRY_RUN" = 1 ]; then emit ran "dry-run: would run command (timeout ${COMMAND_TIMEOUT}s)"; return 0; fi
  if ! mkdir "$LOCK" 2>/dev/null; then
    local lpid; lpid=$(cat "$LOCK/pid" 2>/dev/null)
    if [ -n "$lpid" ] && kill -0 "$lpid" 2>/dev/null; then emit skipped_locked "previous run still active (pid $lpid)"; return 0; fi
    rm -rf "$LOCK"   # 異常終了で残った stale lock
    mkdir "$LOCK" 2>/dev/null || { emit skipped_locked "lock race"; return 0; }
  fi
  HAVE_LOCK=1
  echo "${BASHPID:-$$}" >"$LOCK/pid"   # dispatch.sh のサブシェルでも自分の pid(bash 3.2 は BASHPID が無く、dispatch.sh の pid になる。どちらも実行中は生きている)
  OUTF=$(mktemp); ERRF=$(mktemp)
  /bin/bash -c "$COMMAND" >"$OUTF" 2>"$ERRF" </dev/null &
  local cpid=$! wpid timed_out=0 rc=0
  # 終わるのを wait で待つ(0.1 秒ごとの sleep で見回らない。macOS の bash 3.2 では sleep が毎回別のプロセスになる)。
  # 時間切れは見張りのプロセスが印のファイルを作ってから止める
  ( sleep "$COMMAND_TIMEOUT"; : >"$OUTF.timeout"; kill_tree "$cpid" ) </dev/null >/dev/null 2>&1 &
  wpid=$!
  wait "$cpid" 2>/dev/null; EXITC=$?
  kill_tree "$wpid" 2>/dev/null; wait "$wpid" 2>/dev/null
  [ -e "$OUTF.timeout" ] && timed_out=1
  rm -f "$OUTF.timeout"
  OUT_S=$(head -c 500 "$OUTF" | tr -d '\000'); ERR_S=$(head -c 500 "$ERRF" | tr -d '\000')
  cleanup
  if ((timed_out)); then EXITC=124; emit failed "timeout after ${COMMAND_TIMEOUT}s"; return 1; fi
  if [ "$EXITC" = 0 ]; then emit ran "exit 0"; else emit failed "exit $EXITC"; rc=1; fi
  return $rc
}

# 発火条件(GATE): 終了コード 0 のときだけ 0 を返す。0 以外・時間切れ・起動失敗は skipped_gate を記録して 1。
# GATE の stdout / stderr は捨てる(ログにも送信にも使わない)。判定の順序: 窓・間隔・曜日 → ENABLED → GATE → SKIP_IF → ロック → 発火
run_gate() {
  [ -n "$GATE" ] || return 0
  local gpid wpid gexit mark
  mark=$(mktemp 2>/dev/null) || { emit skipped_gate "gate could not start (mktemp)"; return 1; }
  rm -f "$mark"
  /bin/bash -c "$GATE" >/dev/null 2>&1 </dev/null &
  gpid=$!
  ( sleep "$GATE_TIMEOUT"; : >"$mark"; kill_tree "$gpid" ) </dev/null >/dev/null 2>&1 &
  wpid=$!
  wait "$gpid" 2>/dev/null; gexit=$?
  kill_tree "$wpid" 2>/dev/null; wait "$wpid" 2>/dev/null
  if [ -e "$mark" ]; then rm -f "$mark"; emit skipped_gate "gate timeout after ${GATE_TIMEOUT}s"; return 1; fi
  case $gexit in
    0) return 0 ;;
    126 | 127) emit skipped_gate "gate could not start (exit $gexit)"; return 1 ;;
    *) emit skipped_gate "gate exit $gexit"; return 1 ;;
  esac
}

# PROMPT_CMD: 本文を出すコマンド。終了コード 0 で stdout が空でなければそれが本文。0 で空なら次(PROMPT_RULES → PROMPT)へ。
# 0 以外・時間切れ・起動失敗は failed。stderr は捨てる。stdout は 64 KiB まで。SCHED_* の環境変数でこの回の時刻を渡す
run_prompt_cmd() {  # 本文を PTEXT に入れる。0=本文あり / 1=次へ / 2=失敗(記録済み)
  local rc
  run_capture "$PROMPT_CMD" "$PROMPT_CMD_TIMEOUT" 65536; rc=$?
  PTEXT=$COUT
  if ((CTIMEOUT)); then fail "PROMPT_CMD timeout after ${PROMPT_CMD_TIMEOUT}s"; return 2; fi
  case $rc in
    0) [ -n "$PTEXT" ] && return 0; return 1 ;;
    126 | 127) fail "PROMPT_CMD could not start (exit $rc)"; return 2 ;;
    *) fail "PROMPT_CMD exit $rc"; return 2 ;;
  esac
}

# この回の本文を選ぶ: PROMPT_CMD → PROMPT_RULES(最初に合った行)→ PROMPT_FILE / PROMPT。
# TEXT に本文、PSRC に出どころ(cmd / rule:<i> / default)。0=本文あり / 1=どれも無い / 2=失敗(記録済み)
select_prompt() {
  local i b f=""
  TEXT=""; PSRC=""
  if [ -n "$PROMPT_CMD" ]; then
    run_prompt_cmd; case $? in 0) TEXT=$PTEXT; PSRC=cmd; return 0 ;; 2) return 2 ;; esac
  fi
  for ((i = 1; i <= RULE_N; i++)); do
    rule_match "$i" "$SCHED_HOUR" "$SCHED_MIN" "$SCHED_SEC" "$SCHED_DOW" || continue
    eval "b=\$RULE_BODY_$i"; PSRC="rule:$i"
    case "$b" in file:*) f=${b#file:} ;; *) TEXT=$b; return 0 ;; esac
    break
  done
  if [ -z "$PSRC" ]; then
    if [ -n "${PROMPT_FILE:-}" ]; then f=$PROMPT_FILE; PSRC=default
    elif [ -n "${PROMPT:-}" ]; then TEXT=$PROMPT; PSRC=default; return 0
    else return 1
    fi
  fi
  [ -r "$f" ] || { fail "$([ "$PSRC" = default ] && echo PROMPT_FILE || echo "$PSRC の file:")を読めない: $f"; return 2; }
  TEXT="$(cat "$f")"
}

# コマンドを時間制限つきで実行し、stdout(NUL を除き <上限> バイトまで)を COUT に入れる。stderr は捨てる。
# <コマンド> <秒> <上限バイト>。戻り値はコマンドの終了コード。時間切れは 124 + CTIMEOUT=1
run_capture() {
  local cpid wpid cexit mark outf
  COUT=""; CTIMEOUT=0
  mark=$(mktemp 2>/dev/null) && outf=$(mktemp 2>/dev/null) || return 126
  rm -f "$mark"
  /bin/bash -c "$1" >"$outf" 2>/dev/null </dev/null &
  cpid=$!
  ( sleep "$2"; : >"$mark"; kill_tree "$cpid" ) </dev/null >/dev/null 2>&1 &
  wpid=$!
  wait "$cpid" 2>/dev/null; cexit=$?
  kill_tree "$wpid" 2>/dev/null; wait "$wpid" 2>/dev/null
  COUT=$(head -c "$3" "$outf" | tr -d '\000'); rm -f "$outf"
  if [ -e "$mark" ]; then rm -f "$mark"; CTIMEOUT=1; return 124; fi
  return "$cexit"
}

# 本文の {{名前}} を差し込む。名前は組み込み(job / target / at / date / time / hour / min / sec / dow)か PROMPT_VARS の名前。
# 知らない名前の {{...}} はそのまま残す。差し込んだ値の中の {{...}} は展開しない(1 回だけ)。
# PROMPT_VARS のコマンド: 終了コード 0 → stdout を差し込む(空も可)/ 1 → この回は送らない(skipped_prompt。grep の「該当なし」と同じ意味)/
# それ以外・時間切れ・起動失敗 → failed。同じ名前は 1 回だけ実行する。0=差し込み済み / 1=送らない(記録済み)/ 2=失敗(記録済み)
expand_prompt() {
  local re='\{\{([a-z][a-z0-9_]*)\}\}' rest out m name val i rc
  local done_names=" "
  [[ "$TEXT" == *"{{"* ]] || return 0
  rest=$TEXT; out=""
  while [[ "$rest" =~ $re ]]; do
    m=${BASH_REMATCH[0]}; name=${BASH_REMATCH[1]}
    out+=${rest%%"$m"*}; rest=${rest#*"$m"}
    case "$name" in
      job) val=$JOB ;; target) val=$TARGET ;; at) val=$SCHED_AT ;; date) val=${SCHED_AT%% *} ;; time) val=${SCHED_AT#* } ;;
      hour) val=$SCHED_HOUR ;; min) val=$SCHED_MIN ;; sec) val=$SCHED_SEC ;; dow) val=$SCHED_DOW ;;
      *)
        i=$(var_index "$name") || { out+=$m; continue; }   # 知らない名前はそのまま
        if [[ "$done_names" == *" $name "* ]]; then
          eval "val=\$VAR_VAL_$i"
        else
          local vcmd; eval "vcmd=\$VAR_CMD_$i"
          run_capture "$vcmd" "$PROMPT_VAR_TIMEOUT" 16384; rc=$?
          if ((CTIMEOUT)); then fail "PROMPT_VARS $name: timeout after ${PROMPT_VAR_TIMEOUT}s"; return 2; fi
          case $rc in
            0) ;;
            1) emit skipped_prompt "PROMPT_VARS $name: exit 1 (nothing to send)"; return 1 ;;
            126 | 127) fail "PROMPT_VARS $name: could not start (exit $rc)"; return 2 ;;
            *) fail "PROMPT_VARS $name: exit $rc"; return 2 ;;
          esac
          val=$COUT; printf -v "VAR_VAL_$i" '%s' "$val"; done_names+="$name "
        fi ;;
    esac
    out+=$val
  done
  TEXT=$out$rest
}

# プロンプト型: agent が idle / done のときだけ本文を送る
run_prompt() {
  local TEXT out sel s rc
  # 本文(送信前に検証)。PROMPT_RULES / PROMPT_CMD があれば、この回の時刻で選ぶ
  select_prompt; case $? in
    1) emit skipped_prompt "no prompt for this run (PROMPT_CMD empty / no PROMPT_RULES matched / no PROMPT)"; return 0 ;;
    2) return 1 ;;
  esac
  [ -n "$TEXT" ] || { fail "本文が空"; return 1; }
  case "$TEXT" in /* | -*) fail "本文の先頭が / か - (スラッシュコマンド/オプションと解釈される)"; return 1 ;; esac

  # 状態取得
  if [ "$DRY_RUN" = 1 ]; then
    STATE="${FAKE_STATE:-}"
  else
    out=$("$HERDR" agent list 2>&1) || { fail "herdr agent list 失敗: ${out:0:300}"; return 1; }
    sel=$(jq -r --arg n "$TARGET" '[.result.agents[]? | select(.name==$n)]
          | if length==1 then "\(.[0].pane_id) \(.[0].agent_status)" else "count=\(length)" end' <<<"$out" 2>&1) \
      || { fail "agent list の解析失敗: ${sel:0:300}"; return 1; }
    case "$sel" in count=*) fail "TARGET '$TARGET' が一意に解決できない ($sel)"; return 1 ;; esac
    PANE="${sel%% *}"; STATE="${sel#* }"
  fi

  for s in $SKIP_IF; do
    [ "$STATE" = "$s" ] && { emit skipped_state "state=$STATE matches SKIP_IF"; return 0; }
  done

  # idle / done のときだけ送る(DRY_RUN で状態を仮定しないときは見ない)
  if [ "$DRY_RUN" != 1 ] || [ -n "$STATE" ]; then
    case "$STATE" in
      idle | done) ;;
      *) emit skipped_state "state=$STATE is not idle/done"; return 0 ;;
    esac
  fi

  # {{名前}} の差し込み(送ると決まってから。PROMPT_VARS のコマンドは本文に出てくるものだけ実行する)
  expand_prompt; case $? in
    1) return 0 ;;   # skipped_prompt は記録済み
    2) return 1 ;;
  esac
  [ -n "$TEXT" ] || { fail "差し込み後の本文が空"; return 1; }
  case "$TEXT" in /* | -*) fail "差し込み後の本文の先頭が / か - (スラッシュコマンド/オプションと解釈される)"; return 1 ;; esac

  if [ "$DRY_RUN" = 1 ]; then
    DRY_TEXT=$TEXT
    if [ -z "$STATE" ]; then emit sent "dry-run: state not checked, would send"; else emit sent "dry-run: would send to $TARGET"; fi
    DRY_TEXT=""; return 0
  fi

  # 送信は tb を通す(送ったものは taskboard の messages に kind=scheduler で残る)
  TB="${TB_BIN:-$SCHED_ROOT/../taskboard/bin/tb}"
  out=$(env -u HERDR_PANE_ID HERDR_BIN="$HERDR" bash "$TB" system-send "$TARGET" --text "$TEXT" --job "$JOB" 2>&1); rc=$?
  case $rc in
    0) emit sent "prompt submitted (via tb)" ;;
    10) emit skipped_state "target became busy before sending (tb)" ;;
    *) fail "tb system-send 失敗(exit $rc): ${out:0:300}"; return 1 ;;
  esac
}

# 2. 刻みの回ごとに実行(未来の回はその時刻まで待つ)
RC=0; n=0
for p in "${DUE[@]}"; do
  if ((!FAKED)); then
    cur=$(date +%s)
    ((n > 0 && cur > SLOT_HI)) && break   # 前の回が長引いて分をまたいだら、残りは次の起動に任せる
    ((p > cur)) && sleep $((p - cur))
  fi
  n=$((n + 1))
  AT=$(fmt_epoch "$p" '%Y-%m-%d %H:%M:%S'); PANE=""; STATE=""; EXITC=""; OUT_S=""; ERR_S=""; PSRC=""
  # この回の時刻を GATE / COMMAND / PROMPT_CMD に渡す(ジョブの TZ の現地時刻。曜日は 1=Mon..7=Sun)
  read -r SCHED_HOUR SCHED_MIN SCHED_SEC SCHED_DOW <<<"$(fmt_epoch "$p" '%H %M %S %u')"
  # SCHED_OWNER: ジョブの持ち主(OWNER。無ければ空)。tb recipe-run は SCHED_JOB があれば人として扱わず、この agent の権限で判定する
  # TB_SYSTEM: tb の主体を system:scheduler にする(ADR 0007。policy の system: の行で締められる)
  export TB_SYSTEM=scheduler SCHED_JOB=$JOB SCHED_AT=$AT SCHED_HOUR SCHED_MIN SCHED_SEC SCHED_DOW SCHED_OWNER="${OWNER:-}"
  run_gate || continue
  if [ -n "${COMMAND:-}" ]; then run_command || RC=1; else run_prompt || RC=1; fi
done
exit $RC
