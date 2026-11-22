# shellcheck shell=bash
# scheduler のジョブ定義と上書き設定を DB(config 表)に置く(tb sched-job)。bin/tb から source される。単体では実行しない。
#
# キー: sched-job:<job>(jobs/<job>.conf の本文そのまま。bash の変数代入)/ sched-settings:<job>(settings/<job>.settings の本文)。
# scheduler(scheduler/bin/lib.sh)は DB だけを読む(このセッションの行 > 共通の行(*))。ファイル(jobs/*.conf・settings/*.settings)は
# 取り込む前の置き場所で、SCHED_FILE_FALLBACK=1 のときだけ DB の後に読む。取り込んでいない conf のジョブは動かない(tb sched-job import で取り込む)。
# 変更の履歴は config_changes(tb config と同じ仕組み)。書くのは人だけ。書く前に scheduler 自身(show.sh)で読めることを確かめる。
# tb sched-job import でファイルを DB に取り込み、取り込んだファイルは checkout から退かす($HERDR_STATE_DIR/sched-imported/<日時>/)。

SJ_DIR=${TB_SCHED_DIR:-$TB_ROOT/../scheduler}
SJ_SHOW=${TB_SCHED_SHOW:-$TB_ROOT/../scheduler/bin/show.sh}
SJ_JOB_RE='^[A-Za-z0-9][A-Za-z0-9_-]*$'

sj_valid_job() { [[ $1 =~ $SJ_JOB_RE && ${#1} -le 64 && $1 != dispatch ]] || usage "ジョブ名は英数字で始まり、英数字・_・- の 64 字以内(dispatch は予約名): $1"; }
sj_key() { if [[ $2 == 1 ]]; then echo "sched-settings:$1"; else echo "sched-job:$1"; fi; }   # $1=job $2=settings なら 1

sj_files_ok() { [[ ${SCHED_FILE_FALLBACK:-0} == 1 ]]; }
# 今の本文と出どころ。SJ_BODY / SJ_SRC(db:<セッション> / db:* / file:<パス> / none)。ファイルは scheduler と同じく SCHED_FILE_FALLBACK=1 のときだけ
sj_lookup() {  # $1=job $2=settings なら 1
  local r f
  SJ_BODY=""; SJ_SRC=none
  r=$(config_db_row "$(sj_key "$1" "$2")")
  if [[ -n $r ]]; then
    SJ_BODY=${r#?:}; if [[ ${r:0:1} == '*' ]]; then SJ_SRC="db:*"; else SJ_SRC="db:$SESS"; fi; return 0
  fi
  sj_files_ok || return 1
  if [[ $2 == 1 ]]; then f=$SJ_DIR/settings/$1.settings; else f=$SJ_DIR/jobs/$1.conf; fi
  [[ -f $f && ! -L $f ]] || return 1
  SJ_BODY=$(cat "$f"); SJ_SRC="file:$f"
}

# scheduler が読めるか(show.sh を一時のディレクトリで動かす。DB は見せない)。$1=job $2=conf の本文 $3=settings の本文(空なら無し)
sj_check() {
  local tmp out rc=0
  tmp=$(mktemp -d) || die "一時ディレクトリを作れません"
  mkdir -p "$tmp/jobs" "$tmp/settings" "$tmp/logs"
  printf '%s\n' "$2" >"$tmp/jobs/$1.conf"
  [[ -z $3 ]] || printf '%s\n' "$3" >"$tmp/settings/$1.settings"
  out=$(SCHED_FILE_FALLBACK=1 TB_DB=$tmp/none.db JOBS_DIR=$tmp/jobs LOG_DIR=$tmp/logs SCHED_SETTINGS_DIR=$tmp/settings \
        env -u SCHED_WINDOW -u SCHED_START -u SCHED_END -u SCHED_INTERVAL_MIN -u SCHED_INTERVAL_SEC bash "$SJ_SHOW" "$1" 2>&1) || rc=$?
  rm -rf "$tmp"
  ((rc == 0)) && return 0
  echo "tb: scheduler がこの本文を受け付けません: $(tail -n 1 <<<"$out")" >&2
  return 1
}

cmd_sched-job() {
  local sub=${1:-list}; shift || true
  case $sub in
    list) sj_list "$@";;
    show) sj_show "$@";;
    set|edit|unset) sj_change "$sub" "$@";;
    import) sj_import "$@";;
    history) sj_history "$@";;
    *) usage "sched-job [list | show <job> [--settings] | set <job> [--settings] [--shared] < 本文 | edit <job> [--settings] [--shared] | unset <job> [--settings] [--shared] | import [--dry-run] [--shared] [<job> ...] | history <job> [--settings]]";;
  esac
}

# tb job <サブコマンド>(.claude/skills/tb-command-naming の形)。古い名前 tb sched-job も同じに動く(サブコマンドが無ければ list)
cmd_job() {
  local sub=${1:-}; shift || true
  case $sub in
    list|show|set|edit|unset|import|history) cmd_sched-job "$sub" "$@";;
    ""|-h|--help|help) job_usage; [[ -n $sub ]] || exit 2;;
    *) usage "job: 不明なサブコマンド $sub(list / show / set / edit / unset / import / history)";;
  esac
}
job_usage() {
  cat <<'USAGE'
tb job list   ジョブの一覧と、定義・上書き設定の出どころ(db:<セッション> / db:* / file:<パス> / none)
tb job show <job> [--settings]   今の本文(出どころは標準エラーに 1 行)
tb job set <job> [--settings] [--shared] < 本文   書く(人だけ。scheduler が読めることを確かめてから)
tb job edit <job> [--settings] [--shared]   $VISUAL / $EDITOR で書き換える(人だけ)
tb job unset <job> [--settings] [--shared]   DB の行を消す(人だけ)
tb job import [--dry-run] [--shared] [<job> ...]   jobs/*.conf(と settings/*.settings)を DB に取り込み、ファイルを退かす(人だけ)
tb job history <job> [--settings]   変更の履歴
(古い名前 tb sched-job も同じに使える。tb sched-job だけなら list)
USAGE
}

# ジョブ名の一覧(DB とファイルの和)と、定義・設定の出どころ
sj_list() {
  [[ $# -eq 0 ]] || usage "sched-job list"
  local j names
  names=$( { sql "SELECT DISTINCT substr(key, 11) FROM main.config WHERE key LIKE 'sched-job:%' AND herdr_session IN ($SQS, '*');"
             for j in "$SJ_DIR"/jobs/*.conf; do [[ -f $j ]] && basename "$j" .conf; done; } | grep -E "$SJ_JOB_RE" | sort -u)
  printf 'セッション: %s\n' "$SESS"
  while IFS= read -r j; do
    [[ -n $j ]] || continue
    local js ss
    sj_lookup "$j" 0 || true; js=$SJ_SRC
    sj_lookup "$j" 1 || true; ss=$SJ_SRC
    # DB に無い conf は、scheduler が既定では読まない(動かない)。取り込みを促す
    [[ $js == none && -f $SJ_DIR/jobs/$j.conf ]] && js="未取り込み:$SJ_DIR/jobs/$j.conf(tb job import $j)"
    printf '%s\t%s\tsettings=%s\n' "$j" "$js" "$ss"
  done <<<"$names"
}

sj_args() {  # → SJ_JOB / SJ_SET / SJ_SHARED / SJ_DRY
  SJ_JOB=""; SJ_SET=0; SJ_SHARED=0; SJ_DRY=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --settings) SJ_SET=1; shift;;
      --shared) SJ_SHARED=1; shift;;
      -*) usage "sched-job: 不明な引数 $1";;
      *) [[ -z $SJ_JOB ]] || usage "sched-job: ジョブは 1 つ"; SJ_JOB=$1; shift;;
    esac
  done
  [[ -n $SJ_JOB ]] || usage "sched-job: ジョブ名が必要です"
  sj_valid_job "$SJ_JOB"
}

sj_show() {
  sj_args "$@"
  sj_lookup "$SJ_JOB" "$SJ_SET" || die "$(sj_key "$SJ_JOB" "$SJ_SET") は DB にありません$( [[ $SJ_SET == 0 && -f $SJ_DIR/jobs/$SJ_JOB.conf ]] && echo "(jobs/$SJ_JOB.conf は tb job import $SJ_JOB で取り込む)")"
  echo "# $(sj_key "$SJ_JOB" "$SJ_SET"): $SJ_SRC" >&2
  [[ -z $SJ_BODY ]] || printf '%s\n' "$SJ_BODY"
}

sj_change() {
  local sub=$1 body tmp conf settings key hs; shift
  sj_args "$@"
  require_human "tb job $sub"
  key=$(sj_key "$SJ_JOB" "$SJ_SET")
  case $sub in
    unset)
      hs=$SQS; ((SJ_SHARED)) && hs="'*'"
      local which="このセッションの行"; ((SJ_SHARED)) && which="共通の行"
      [[ -n $(sql "SELECT 1 FROM main.config WHERE herdr_session=$hs AND key=$(q "$key");") ]] || die "$key の DB の行はありません($which)"
      config_unset "$key" "$SJ_SHARED"
      sj_lookup "$SJ_JOB" "$SJ_SET" || true; echo "$key: $SJ_SRC"; return 0;;
    set)
      [[ ! -t 0 ]] || usage "sched-job set <job>: 本文は標準入力から渡す(例: tb job set $SJ_JOB < jobs/$SJ_JOB.conf)"
      body=$(cat);;
    edit)
      tmp=$(mktemp); { sj_lookup "$SJ_JOB" "$SJ_SET" && printf '%s\n' "$SJ_BODY"; } >"$tmp" || true
      "${VISUAL:-${EDITOR:-vi}}" "$tmp" || { rm -f "$tmp"; die "編集を中止しました"; }
      body=$(cat "$tmp"); rm -f "$tmp";;
  esac
  # scheduler が読めることを確かめてから書く(設定なら今の定義と組み合わせて)
  if ((SJ_SET)); then
    sj_lookup "$SJ_JOB" 0 || die "ジョブ $SJ_JOB の定義が DB にありません(先に tb job set $SJ_JOB か tb job import $SJ_JOB で置く)"
    conf=$SJ_BODY; settings=$body
  else
    conf=$body; settings=""; sj_lookup "$SJ_JOB" 1 && settings=$SJ_BODY
  fi
  [[ -n $conf ]] || die "本文が空です"
  sj_check "$SJ_JOB" "$conf" "$settings" || die "$key は書きませんでした"
  config_db_put "$key" "$body" "$sub" "$SJ_SHARED"
  sj_lookup "$SJ_JOB" "$SJ_SET" || true; echo "$key: $SJ_SRC"
}

# jobs/*.conf(と settings/*.settings)を DB に取り込み、取り込んだファイルを checkout から退かす。
# 既定はこのセッションの行(--shared なら共通の行)。既に行があるジョブは飛ばす。取り込みの前後で、scheduler の読む中身は変わらない
sj_import() {
  local dry=0 shared=0 j f sf hs dest="" n=0 conf settings
  local -a want=()
  while [[ $# -gt 0 ]]; do
    case $1 in
      --dry-run) dry=1; shift;;
      --shared) shared=1; shift;;
      -*) usage "sched-job import [--dry-run] [--shared] [<job> ...]";;
      *) sj_valid_job "$1"; want+=("$1"); shift;;
    esac
  done
  require_human "tb job import"
  hs=$SQS; ((shared)) && hs="'*'"
  if ((${#want[@]} == 0)); then
    for f in "$SJ_DIR"/jobs/*.conf; do [[ -f $f ]] && want+=("$(basename "$f" .conf)"); done
  fi
  for j in ${want[@]+"${want[@]}"}; do
    [[ $j =~ $SJ_JOB_RE && $j != dispatch ]] || { echo "$j: ジョブ名に使えない文字があるので飛ばします"; continue; }
    f=$SJ_DIR/jobs/$j.conf; sf=$SJ_DIR/settings/$j.settings
    [[ -f $f && ! -L $f ]] || { echo "$j: jobs/$j.conf がありません(通常のファイルのみ)"; continue; }
    if [[ -n $(sql "SELECT 1 FROM main.config WHERE herdr_session=$hs AND key=$(q "sched-job:$j");") ]]; then
      echo "$j: DB に行が既にあるので飛ばします(jobs/$j.conf はそのまま)"; continue
    fi
    [[ ! -L $sf ]] || { echo "$j: settings/$j.settings がシンボリックリンクなので飛ばします"; continue; }
    conf=$(cat "$f"); settings=""; [[ -f $sf ]] && settings=$(cat "$sf")
    sj_check "$j" "$conf" "$settings" || { echo "$j: scheduler が読めない定義なので飛ばします"; continue; }
    n=$((n + 1))
    if ((dry)); then echo "$j: jobs/$j.conf${settings:+ と settings/$j.settings} を取り込みます"; continue; fi
    if [[ -z $dest ]]; then
      dest=$HERDR_STATE_DIR/sched-imported/$(date +%Y%m%d-%H%M%S)
      mkdir -p "$dest/jobs" "$dest/settings" || die "退かす先を作れません: $dest"
    fi
    config_db_put "sched-job:$j" "$conf" import "$shared"
    [[ -z $settings && ! -f $sf ]] || config_db_put "sched-settings:$j" "$settings" import "$shared"
    mv "$f" "$dest/jobs/" || die "$f を退かせません(DB には取り込み済み)"
    [[ ! -f $sf ]] || mv "$sf" "$dest/settings/" || die "$sf を退かせません(DB には取り込み済み)"
    echo "$j: 取り込み、ファイルを $dest/ へ退かしました"
  done
  ((dry)) && echo "(dry-run: 何も書いていません)"
  ((n > 0)) || echo "取り込むジョブはありません"
  return 0
}

sj_history() {
  sj_args "$@"
  sql -separator $'\t' "SELECT id, herdr_session, how, changed_by, changed_at, CASE WHEN body IS NULL THEN '(消した)' ELSE length(body) || ' 文字' END
    FROM main.config_changes WHERE key=$(q "$(sj_key "$SJ_JOB" "$SJ_SET")") AND herdr_session IN ($SQS, '*') ORDER BY id DESC LIMIT 50;"
}
