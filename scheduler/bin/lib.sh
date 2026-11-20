# shellcheck shell=bash
# 共通処理。tick.sh / gen-plist.sh / install.sh から source される。
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

SCHED_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
JOBS_DIR="${JOBS_DIR:-$SCHED_ROOT/jobs}"
LOG_DIR="${LOG_DIR:-$SCHED_ROOT/logs}"
# Web UI などが書く上書き設定(<job>.settings)の置き場
SETTINGS_DIR="${SCHED_SETTINGS_DIR:-$SCHED_ROOT/settings}"
# shellcheck source=../../lib/herdr-env.sh
source "$SCHED_ROOT/../lib/herdr-env.sh"
herdr_session_apply   # checkout の herdr-session ファイルでセッションを固定していれば、herdr と tb にも渡す
HERDR="${HERDR_BIN:-$HERDR_DEFAULT_BIN}"   # 上の PATH で /opt/homebrew/bin/herdr が先に見つかる
export HERDR_BIN_PATH="${HERDR_BIN_PATH:-$HERDR}"

die() { echo "error: $*" >&2; exit 2; }

# ジョブ定義・上書き設定の DB(taskboard と同じ DB の config 表)。キー: sched-job:<job>(conf の本文)/ sched-settings:<job>(settings の本文)。
# 読むのは DB だけ: このセッションの行 > 全セッション共通の行(*)。書くのは人だけ(tb sched-job)。ここは読むだけで、DB を書き込みで開かない。
# ファイル(jobs/<job>.conf・settings/<job>.settings)は DB へ取り込む前の置き場所で、SCHED_FILE_FALLBACK=1 のときだけ DB の後に読む
# (tb sched-job が書く前の検査と、テスト用)。既定では読まないので、取り込んでいない conf のジョブは動かない(tick.sh が failed を記録する)
SCHED_DB="${TB_DB:-$HERDR_DEFAULT_TB_DB}"
sched_files_ok() { [ "${SCHED_FILE_FALLBACK:-0}" = 1 ]; }
sched_db_body() {  # $1=キー。見つかれば本文を出して 0、無ければ 1
  command -v sqlite3 >/dev/null 2>&1 || return 1
  [ -f "$SCHED_DB" ] || return 1
  local k=${1//\'/\'\'} s out
  s=$(herdr_session_name); s=${s//\'/\'\'}
  out=$(sqlite3 -readonly -batch -cmd ".timeout 5000" "$SCHED_DB" \
    "SELECT 'x' || body FROM config WHERE key='$k' AND herdr_session IN ('$s', '*') ORDER BY herdr_session='*' LIMIT 1;" 2>/dev/null) || return 1
  [ -n "$out" ] || return 1
  printf '%s\n' "${out#x}"
}
# DB にあるジョブ名(1 行 1 つ)。sqlite3・DB・表が無ければ何も出さない
sched_db_jobs() {
  command -v sqlite3 >/dev/null 2>&1 || return 0
  [ -f "$SCHED_DB" ] || return 0
  local s; s=$(herdr_session_name); s=${s//\'/\'\'}
  sqlite3 -readonly -batch -cmd ".timeout 5000" "$SCHED_DB" \
    "SELECT DISTINCT substr(key, 11) FROM config WHERE key LIKE 'sched-job:%' AND herdr_session IN ('$s', '*');" 2>/dev/null \
    | grep -E '^[A-Za-z0-9][A-Za-z0-9_-]*$' || true
}
# ジョブ名の一覧(DB。SCHED_FILE_FALLBACK=1 ならファイルとの和。名前順)
sched_job_names() {
  { sched_db_jobs; local f; if sched_files_ok; then for f in "$JOBS_DIR"/*.conf; do [ -f "$f" ] && basename "$f" .conf; done; fi; } \
    | grep -E '^[A-Za-z0-9][A-Za-z0-9_-]*$' | sort -u
}
# DB に取り込んでいない jobs/*.conf のジョブ名(1 行 1 つ)。既定では動かないジョブ。setup/check.sh と tb sched-job list が知らせる
sched_unimported_jobs() {
  local db f j; db=$(sched_db_jobs)
  for f in "$JOBS_DIR"/*.conf; do
    [ -f "$f" ] || continue; j=$(basename "$f" .conf)
    grep -qxF -- "$j" <<<"$db" || echo "$j"
  done
}

# DB に定義が無いときの説明(tick.sh が failed の理由にも使う)
sched_missing_msg() {
  local m="ジョブ定義が DB に無い: sched-job:$1(セッション $(herdr_session_name))"
  if ! command -v sqlite3 >/dev/null 2>&1; then m="${m}。sqlite3 が無い"
  elif [ ! -f "$SCHED_DB" ]; then m="${m}。DB が無い: $SCHED_DB"; fi
  [ -f "$JOBS_DIR/$1.conf" ] && m="${m}。jobs/$1.conf は DB に取り込むまで使われない: taskboard/bin/tb sched-job import $1"
  printf '%s\n' "$m"
}

# 時刻 -> その日の 0 時からの秒数。"09:00" -> 32400、"09:00:30" -> 32430。
# 第 2 引数が end のとき、秒を省いた "HH:MM" はその分の 59 秒までを含む("21:00" -> 21:00:59)。
# 秒に対応する前は分単位で判定していたので、"21:00" を終端に書いた窓は 21:00 台を丸ごと含んでいた。その意味を変えないため
hms_to_sec() {
  [[ "$1" =~ ^([0-9]{1,2}):([0-9]{2})(:([0-9]{2}))?$ ]] || die "時刻の形式が不正: $1 (HH:MM か HH:MM:SS)"
  local h=$((10#${BASH_REMATCH[1]})) m=$((10#${BASH_REMATCH[2]})) s
  if [ -n "${BASH_REMATCH[3]}" ]; then s=$((10#${BASH_REMATCH[4]})); elif [ "${2:-}" = end ]; then s=59; else s=0; fi
  ((h < 24 && m < 60 && s < 60)) || die "時刻が範囲外: $1"
  echo $((h * 3600 + m * 60 + s))
}

# 間隔(秒)を表示用に。60 で割り切れれば分で出す
interval_label() { if ((INTERVAL_SEC % 60 == 0)); then echo "$((INTERVAL_SEC / 60)) 分"; else echo "$INTERVAL_SEC 秒"; fi; }

# INTERVAL_MIN / INTERVAL_SEC のどちらか一方から秒を得る。<場所> <分> <秒>。両方空なら空
interval_of() {
  [ -n "$2" ] && [ -n "$3" ] && die "$JOB: $1 に INTERVAL_MIN と INTERVAL_SEC を同時に書かない"
  if [ -n "$2" ]; then
    [[ "$2" =~ ^[0-9]+$ ]] && ((10#$2 > 0)) || die "$JOB: INTERVAL_MIN は正の整数"
    echo $((10#$2 * 60))
  elif [ -n "$3" ]; then
    [[ "$3" =~ ^[0-9]+$ ]] && ((10#$3 > 0)) || die "$JOB: INTERVAL_SEC は正の整数"
    echo $((10#$3))
  fi
}

# 上書き設定 $SETTINGS_DIR/<job>.settings を読む。bash として実行せず、決まったキーと値の形だけを受け付ける。
# キー: INTERVAL_MIN か INTERVAL_SEC / START / END / DAYS / ENABLED(0 で一時停止)。結果は S_<キー> に入る
# DB に sched-settings:<job> の行があれば、ファイルより先にそれを読む(書式は同じ)。どちらを読んだかは SETTINGS_SRC(db / file / none)
load_settings() {
  local f="$SETTINGS_DIR/$JOB.settings" line k v body
  S_INTERVAL_MIN=""; S_INTERVAL_SEC=""; S_START=""; S_END=""; S_DAYS=""; S_ENABLED=""; SETTINGS_SRC=none
  if body=$(sched_db_body "sched-settings:$JOB"); then
    SETTINGS_SRC=db; f="DB の sched-settings:$JOB"
  else
    sched_files_ok || return 0   # 既定は DB だけ。settings/<job>.settings は読まない
    [ -e "$f" ] || return 0
    [ -f "$f" ] && [ ! -L "$f" ] || die "$JOB: settings が通常ファイルではない: $f"
    body=$(cat "$f"); SETTINGS_SRC=file
  fi
  while IFS= read -r line || [ -n "$line" ]; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" =~ ^([A-Z_]+)=(.*)$ ]] || die "$JOB: settings の行が不正: $f"
    k=${BASH_REMATCH[1]}; v=${BASH_REMATCH[2]}
    case "$k" in
      INTERVAL_MIN) [[ "$v" =~ ^[0-9]{1,4}$ ]] || die "$JOB: settings の INTERVAL_MIN が不正"; S_INTERVAL_MIN=$v ;;
      INTERVAL_SEC) [[ "$v" =~ ^[0-9]{1,5}$ ]] || die "$JOB: settings の INTERVAL_SEC が不正"; S_INTERVAL_SEC=$v ;;
      START) [[ "$v" =~ ^[0-9]{2}:[0-9]{2}(:[0-9]{2})?$ ]] || die "$JOB: settings の START が不正"; S_START=$v ;;
      END) [[ "$v" =~ ^[0-9]{2}:[0-9]{2}(:[0-9]{2})?$ ]] || die "$JOB: settings の END が不正"; S_END=$v ;;
      DAYS) [[ "$v" =~ ^[A-Za-z][A-Za-z\ ,]{0,39}$ ]] || die "$JOB: settings の DAYS が不正"; S_DAYS=$v ;;
      ENABLED) [[ "$v" =~ ^[01]$ ]] || die "$JOB: settings の ENABLED は 0 か 1"; S_ENABLED=$v ;;
      *) die "$JOB: settings に使えないキー: $k" ;;
    esac
  done <<EOF_SETTINGS
$body
EOF_SETTINGS
  if [ -n "$S_START$S_END" ] && { [ -z "$S_START" ] || [ -z "$S_END" ]; }; then
    die "$JOB: settings の START と END は両方書く"
  fi
}

# ジョブ定義を読み込み、既定値を埋めて検証する。
# 優先順: 定義(sched-job:<job>)< 上書き設定(sched-settings:<job>)< 環境変数 SCHED_*
load_job() {
  JOB="${1:-}"
  [[ "$JOB" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || die "ジョブ名が不正: '$JOB'"
  JOB_CONF="$JOBS_DIR/$JOB.conf"
  # 定義: DB の sched-job:<job>(SCHED_FILE_FALLBACK=1 なら、無いときに jobs/<job>.conf)。どちらを読んだかは JOB_SRC(db / file)
  local job_body=""
  if job_body=$(sched_db_body "sched-job:$JOB"); then
    JOB_SRC=db; JOB_CONF="db:sched-job:$JOB"
  else
    sched_files_ok || die "$(sched_missing_msg "$JOB")"
    JOB_SRC=file
    [ -f "$JOB_CONF" ] || die "ジョブ定義がない: $JOB_CONF"
  fi
  unset TARGET PROMPT PROMPT_FILE PROMPT_RULES PROMPT_CMD PROMPT_CMD_TIMEOUT PROMPT_VARS PROMPT_VAR_TIMEOUT COMMAND COMMAND_TIMEOUT GATE GATE_TIMEOUT WINDOW START END INTERVAL_MIN INTERVAL_SEC DAYS SKIP_IF TZ ENABLED WATCHER
  if [ "$JOB_SRC" = db ]; then
    eval "$job_body"   # 本文は conf と同じ(bash の変数代入)。人が tb sched-job で書いたもの
  else
    # shellcheck disable=SC1090
    source "$JOB_CONF"
  fi
  # WINDOW(HH:MM[:SS]-HH:MM[:SS]) を START/END に分解。conf が START/END を直接持つならそちらを優先
  if [ -n "${WINDOW:-}" ] && [ -z "${START:-}${END:-}" ]; then
    [[ "$WINDOW" =~ ^([0-9:]+)-([0-9:]+)$ ]] || die "$JOB: WINDOW は HH:MM-HH:MM"
    START=${BASH_REMATCH[1]}; END=${BASH_REMATCH[2]}
  fi
  load_settings
  if [ -n "$S_START" ]; then START=$S_START; END=$S_END; fi
  # 間隔は秒で持つ。conf / settings / SCHED_* の各層で INTERVAL_MIN か INTERVAL_SEC のどちらかを書け、上の層が勝つ
  INTERVAL_SEC=$(interval_of conf "${INTERVAL_MIN:-}" "${INTERVAL_SEC:-}") || exit 2
  local iv
  iv=$(interval_of settings "$S_INTERVAL_MIN" "$S_INTERVAL_SEC") || exit 2
  INTERVAL_SEC=${iv:-$INTERVAL_SEC}
  DAYS=${S_DAYS:-${DAYS:-}}
  ENABLED=${S_ENABLED:-${ENABLED:-1}}
  [[ "$ENABLED" =~ ^[01]$ ]] || die "$JOB: ENABLED は 0 か 1"
  # WATCHER: 見張り系のジョブか(tb digest の観測が見る)。1 / 0。未設定なら空(TARGET と COMMAND から推定)
  [[ -z "${WATCHER:-}" || "$WATCHER" =~ ^[01]$ ]] || die "$JOB: WATCHER は 0 か 1"
  # 一時的な上書き: SCHED_WINDOW / SCHED_START / SCHED_END / SCHED_INTERVAL_MIN / SCHED_INTERVAL_SEC
  if [ -n "${SCHED_WINDOW:-}" ]; then
    [[ "$SCHED_WINDOW" =~ ^([0-9:]+)-([0-9:]+)$ ]] || die "SCHED_WINDOW は HH:MM-HH:MM"
    START=${BASH_REMATCH[1]}; END=${BASH_REMATCH[2]}
  fi
  START=${SCHED_START:-${START:-}}; END=${SCHED_END:-${END:-}}
  iv=$(interval_of "SCHED_*" "${SCHED_INTERVAL_MIN:-}" "${SCHED_INTERVAL_SEC:-}") || exit 2
  INTERVAL_SEC=${iv:-$INTERVAL_SEC}
  [ -n "$START" ] && [ -n "$END" ] || die "$JOB: 開始・終了時刻が未設定(WINDOW または START と END)"
  WINDOW="$START-$END"
  : "${DAYS:=daily}" "${SKIP_IF:=working blocked}" "${TZ:=Asia/Tokyo}"
  export TZ
  : "${COMMAND_TIMEOUT:=60}"
  [[ "$COMMAND_TIMEOUT" =~ ^[0-9]+$ ]] && ((COMMAND_TIMEOUT > 0)) || die "$JOB: COMMAND_TIMEOUT は正の整数(秒)"
  # GATE: 任意の発火条件(シェル 1 行)。終了コード 0 のときだけ発火する。conf にだけ書ける(settings・Web UI からは変えられない)
  : "${GATE:=}" "${GATE_TIMEOUT:=10}"
  [[ "$GATE_TIMEOUT" =~ ^[0-9]+$ ]] && ((GATE_TIMEOUT > 0)) || die "$JOB: GATE_TIMEOUT は正の整数(秒)"
  # PROMPT_RULES / PROMPT_CMD: 回ごとに本文を切り替える(プロンプト型だけ)。下の parse_prompt_rules と tick.sh の select_prompt
  # PROMPT_VARS: 本文の {{名前}} に差し込むコマンドの出力(検索結果など)。下の parse_prompt_vars と tick.sh の expand_prompt
  : "${PROMPT_RULES:=}" "${PROMPT_CMD:=}" "${PROMPT_CMD_TIMEOUT:=10}" "${PROMPT_VARS:=}" "${PROMPT_VAR_TIMEOUT:=30}"
  [[ "$PROMPT_CMD_TIMEOUT" =~ ^[0-9]+$ ]] && ((PROMPT_CMD_TIMEOUT > 0)) || die "$JOB: PROMPT_CMD_TIMEOUT は正の整数(秒)"
  [[ "$PROMPT_VAR_TIMEOUT" =~ ^[0-9]+$ ]] && ((PROMPT_VAR_TIMEOUT > 0)) || die "$JOB: PROMPT_VAR_TIMEOUT は正の整数(秒)"
  if [ -n "${COMMAND:-}" ]; then
    [ -z "${TARGET:-}${PROMPT:-}${PROMPT_FILE:-}$PROMPT_RULES$PROMPT_CMD$PROMPT_VARS" ] \
      || die "$JOB: COMMAND は TARGET/PROMPT/PROMPT_FILE/PROMPT_RULES/PROMPT_CMD/PROMPT_VARS と同時に指定できない"
    TARGET=""   # COMMAND 型は agent を使わない
  else
    [ -n "${TARGET:-}" ] || die "$JOB: TARGET が未設定(または COMMAND を指定)"
  fi
  [ -n "${WINDOW:-}" ] || die "$JOB: WINDOW が未設定"
  [ -n "${COMMAND:-}${PROMPT:-}${PROMPT_FILE:-}$PROMPT_RULES$PROMPT_CMD" ] || die "$JOB: PROMPT か PROMPT_FILE(または PROMPT_RULES / PROMPT_CMD)が必要"
  parse_prompt_rules
  parse_prompt_vars
  [ -n "$INTERVAL_SEC" ] || die "$JOB: 間隔が未設定(INTERVAL_MIN か INTERVAL_SEC)"
  # 分で割り切れるときだけ INTERVAL_MIN も入れる(表示・互換用)
  if ((INTERVAL_SEC % 60 == 0)); then INTERVAL_MIN=$((INTERVAL_SEC / 60)); else INTERVAL_MIN=""; fi
  [[ "$WINDOW" =~ ^([0-9:]+)-([0-9:]+)$ ]] || die "$JOB: WINDOW は HH:MM-HH:MM"
  # 窓はその日の 0 時からの秒(両端を含む)
  WIN_START=$(hms_to_sec "${BASH_REMATCH[1]}") || exit 2
  WIN_END=$(hms_to_sec "${BASH_REMATCH[2]}" end) || exit 2
  ((WIN_START <= WIN_END)) || die "$JOB: 日をまたぐ WINDOW は未対応"
}

# DAYS(引数があればそれ)を ISO 曜日番号(1=Mon..7=Sun)の空白区切りに展開
days_to_nums() {
  local out="" t
  for t in $(echo "${1:-$DAYS}" | tr ',A-Z' ' a-z'); do
    case "$t" in
      daily|everyday) out+=" 1 2 3 4 5 6 7" ;;
      weekdays) out+=" 1 2 3 4 5" ;;
      weekends) out+=" 6 7" ;;
      mon*) out+=" 1" ;; tue*) out+=" 2" ;; wed*) out+=" 3" ;; thu*) out+=" 4" ;;
      fri*) out+=" 5" ;; sat*) out+=" 6" ;; sun*) out+=" 7" ;;
      *) die "$JOB: DAYS の指定が不正: $t" ;;
    esac
  done
  echo "$out"
}

# ---- PROMPT_RULES: 回の時刻で本文を切り替える
# 1 行 1 ルール: <条件> | <本文>。上から見て最初に合った行の本文を送る。どれにも合わなければ PROMPT / PROMPT_FILE(無ければ送らない)
# 条件は空白区切りの key=値(すべて満たすと合う)。key: min / hour / sec / days。* だけの条件は常に合う
#   値: 数 / 範囲 a-b / 刻み */n / それらのカンマ区切り(例 min=00,30  hour=9-12  min=*/15)。days は DAYS と同じ語をカンマで(days=mon,wed / days=weekdays)
# 本文が file:<パス> なら、そのファイルの中身を送る(PROMPT_FILE と同じ)
# 結果は RULE_N(数)と RULE_COND_<i> / RULE_BODY_<i>(i は 1 から)。bash 3.2 でも動くよう配列を使わない
rule_spec_ok() {  # <key> <値>。値が正しければ 0
  local key=$1 spec=$2 max item a b
  case "$key" in
    min | sec) max=59 ;;
    hour) max=23 ;;
    days) (days_to_nums "$spec") >/dev/null 2>&1; return ;;
    *) return 1 ;;
  esac
  [ -n "$spec" ] || return 1
  [[ "$spec" =~ (^,|,,|,$) ]] && return 1
  local IFS=,
  for item in $spec; do
    if [ "$item" = '*' ]; then continue
    elif [[ "$item" =~ ^\*/([0-9]{1,2})$ ]]; then ((10#${BASH_REMATCH[1]} > 0)) || return 1
    elif [[ "$item" =~ ^([0-9]{1,2})-([0-9]{1,2})$ ]]; then
      a=$((10#${BASH_REMATCH[1]})); b=$((10#${BASH_REMATCH[2]}))
      ((a <= b && b <= max)) || return 1
    elif [[ "$item" =~ ^[0-9]{1,2}$ ]]; then ((10#$item <= max)) || return 1
    else return 1
    fi
  done
}
rule_spec_match() {  # <値の指定> <値(数)>。合えば 0
  local v=$((10#$2)) item n IFS=,
  for item in $1; do
    case "$item" in
      '*') return 0 ;;
      '*/'*) n=$((10#${item#*/})); ((v % n == 0)) && return 0 ;;
      *-*) ((v >= 10#${item%-*} && v <= 10#${item#*-})) && return 0 ;;
      *) ((v == 10#$item)) && return 0 ;;
    esac
  done
  return 1
}
parse_prompt_rules() {
  local line cond body tok n=0 k
  local -a toks
  RULE_N=0
  [ -n "$PROMPT_RULES" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line#"${line%%[![:space:]]*}"}
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" == *"|"* ]] || die "$JOB: PROMPT_RULES の行に | が無い(<条件> | <本文>): $line"
    cond=${line%%|*}; body=${line#*|}
    body=${body#"${body%%[![:space:]]*}"}
    [ -n "$body" ] || die "$JOB: PROMPT_RULES の本文が空: $line"
    read -r -a toks <<<"$cond"   # read -a はパス名展開をしない(* を含む値のため)
    ((${#toks[@]})) || die "$JOB: PROMPT_RULES の条件が空(常に合わせるなら *): $line"
    for tok in "${toks[@]}"; do
      [ "$tok" = '*' ] && continue
      [[ "$tok" =~ ^([a-z]+)=(.+)$ ]] || die "$JOB: PROMPT_RULES の条件が不正: $tok"
      k=${BASH_REMATCH[1]}
      rule_spec_ok "$k" "${BASH_REMATCH[2]}" || die "$JOB: PROMPT_RULES の条件が不正: $tok(key は min / hour / sec / days)"
    done
    n=$((n + 1))
    printf -v "RULE_COND_$n" '%s' "${toks[*]}"
    printf -v "RULE_BODY_$n" '%s' "$body"
  done <<EOF_RULES
$PROMPT_RULES
EOF_RULES
  RULE_N=$n
  ((RULE_N)) || die "$JOB: PROMPT_RULES にルールが無い"
}
# <i> <時> <分> <秒> <曜日 1-7>。ルール i が合えば 0
rule_match() {
  local c tok k v
  local -a toks
  eval "c=\$RULE_COND_$1"
  read -r -a toks <<<"$c"
  for tok in "${toks[@]}"; do
    [ "$tok" = '*' ] && continue
    k=${tok%%=*}; v=${tok#*=}
    case "$k" in
      hour) rule_spec_match "$v" "$2" || return 1 ;;
      min) rule_spec_match "$v" "$3" || return 1 ;;
      sec) rule_spec_match "$v" "$4" || return 1 ;;
      days) case " $(days_to_nums "$v") " in *" $5 "*) ;; *) return 1 ;; esac ;;
    esac
  done
  return 0
}

# ---- PROMPT_VARS: 本文の {{名前}} に差し込むコマンド。1 行 1 つ: <名前> | <コマンド(シェル 1 行)>
# 名前は英小文字で始まる英小文字・数字・_。組み込みの名前(job / target / at / date / time / hour / min / sec / dow)は使えない
# 結果は VAR_N と VAR_NAME_<i> / VAR_CMD_<i>(i は 1 から)
var_index() {  # <名前> → i を出して 0。無ければ 1
  local i n
  for ((i = 1; i <= VAR_N; i++)); do eval "n=\$VAR_NAME_$i"; [ "$n" = "$1" ] && { echo "$i"; return 0; }; done
  return 1
}
parse_prompt_vars() {
  local line name cmd n=0
  VAR_N=0
  [ -n "$PROMPT_VARS" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line#"${line%%[![:space:]]*}"}
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" == *"|"* ]] || die "$JOB: PROMPT_VARS の行に | が無い(<名前> | <コマンド>): $line"
    name=${line%%|*}; name=${name%"${name##*[![:space:]]}"}
    cmd=${line#*|}; cmd=${cmd#"${cmd%%[![:space:]]*}"}
    [[ "$name" =~ ^[a-z][a-z0-9_]*$ ]] || die "$JOB: PROMPT_VARS の名前が不正(英小文字・数字・_): $name"
    case "$name" in job | target | at | date | time | hour | min | sec | dow) die "$JOB: PROMPT_VARS の名前 $name は組み込みと重なる" ;; esac
    var_index "$name" >/dev/null && die "$JOB: PROMPT_VARS の名前が重複: $name"
    [ -n "$cmd" ] || die "$JOB: PROMPT_VARS のコマンドが空: $name"
    n=$((n + 1)); VAR_N=$n
    printf -v "VAR_NAME_$n" '%s' "$name"
    printf -v "VAR_CMD_$n" '%s' "$cmd"
  done <<EOF_VARS
$PROMPT_VARS
EOF_VARS
  ((VAR_N)) || die "$JOB: PROMPT_VARS に定義が無い"
}
