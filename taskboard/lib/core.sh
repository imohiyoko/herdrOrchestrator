# shellcheck shell=bash
# 共通の小道具: エラー終了、入力の検査、タスク行の取得、project ごとの指示役
# bin/tb から source される。単体では実行しない。

DEFAULT_TARGET=conductor
# チケットの状態は設定 statuses で足したり外したりできる(下の「状態」)。コードは状態の名前ではなく分類で判断する。
# STATUSES / CLOSED_STATUSES は statuses_load が設定から埋める(ここは設定が無いときの値)
STATUSES="todo in_progress review done blocked cancelled"
CLOSED_STATUSES="done cancelled"   # 閉じた状態(cancelled = やらないと決めた。done と同じく、もう動かさない)
TYPES="epic story task bug subtask"
PRIORITIES="highest high medium low lowest"   # 上から順に高い
LINK_TYPES="blocks relates duplicates"

die()   { echo "tb: $*" >&2; exit 1; }
usage() { echo "tb: $*" >&2; echo "(tb help で使い方)" >&2; exit 2; }

need_tools() {
  command -v sqlite3 >/dev/null || die "sqlite3 が必要です"
  command -v jq >/dev/null || die "jq が必要です"
}

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

valid_id()      { [[ $1 =~ ^T-[A-Za-z0-9_]+-[0-9]{3,}$ ]] || usage "タスク ID は T-<project>-NNN 形式: $1"; }
valid_project() { [[ $1 =~ ^[A-Za-z0-9_]+$ ]] || usage "project は英数字と _ のみ: $1"; }
valid_status()  { statuses_load; [[ " $STATUSES " == *" $1 "* ]] || usage "status は $STATUSES のどれか: $1"; }
valid_type()     { [[ " $TYPES " == *" $1 "* ]] || usage "種別は $TYPES のどれか: $1"; }
valid_priority() { [[ " $PRIORITIES " == *" $1 "* ]] || usage "優先度は $PRIORITIES のどれか: $1"; }
# ラベルは 1〜40 文字。空白・カンマ・引用符は使えない(日本語は可)
valid_label()    { [[ ${#1} -ge 1 && ${#1} -le 40 && ! $1 =~ [[:space:],\"\'] ]] || usage "ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: $1"; }
# 外部リンク: <種類>=<URL> か URL だけ(種類は slack / github / link を URL から決める)。種類は英小文字・数字・_ - で 20 文字まで
valid_ref_kind() { [[ $1 =~ ^[a-z][a-z0-9_-]{0,19}$ ]] || usage "外部リンクの種類は英小文字で始まる 20 文字までの英数字・_ -: $1"; }
valid_ref_url()  { [[ $1 =~ ^https?://[^[:space:]\"\'\<\>]+$ && ${#1} -le 500 ]] || usage "外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): $1"; }
# 期日(015): YYYY-MM-DD の実在する日付だけ(2026-02-30 などは不可)
valid_due()      {
  [[ $1 =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] && [[ $(sqlite3 :memory: "SELECT date('$1') IS '$1';") == 1 ]] ||
    usage "期日は YYYY-MM-DD の日付: $1"
}
# 「今日」(期限切れ・期限間近の基準)。tb を動かす端末の地方時の日付。TB_TODAY で固定できる(テスト・境界の確認用)
today() {
  if [[ -n ${TB_TODAY:-} ]]; then valid_due "$TB_TODAY"; printf '%s' "$TB_TODAY"; else date +%Y-%m-%d; fi
}
valid_sprint()  { [[ $1 =~ ^S-[0-9]+$ ]] || usage "スプリント ID は S-<番号> 形式: $1"; }
valid_points()  { [[ $1 =~ ^[0-9]{1,4}$ ]] || usage "ポイントは 0〜9999 の整数: $1"; }
need_arg()      { [[ $# -ge 2 ]] || usage "$1 に値が必要です"; }
blank()         { [[ -z "${1//[[:space:]]/}" ]]; }

task_row() {  # $1=id → JSON 1 行(なければ空)
  session_row tasks id "$1"
}
require_task() { local r; r=$(task_row "$1"); [[ -n $r ]] || die "タスクが見つかりません: $1"; printf '%s' "$r"; }

# projects の行: <project>=<指示役>。* の行(*=<名前>)は既定の宛先で、書かれていない project と、
# project に属さない観測(digest・アラートの結果・scheduler の観測)の宛先になる。* の行も無ければ $DEFAULT_TARGET
target_for() {  # $1=project(* なら既定の宛先)→ 指示役の名前
  local t=$DEFAULT_TARGET star="" exact="" k v
  if config_has projects; then
    while IFS='=' read -r k v || [[ -n $k ]]; do
      k=${k//[[:space:]]/}; v=${v//[[:space:]]/}
      [[ -z $k || $k == \#* || -z $v ]] && continue
      [[ $k == '*' ]] && star=$v
      [[ $k == "$1" ]] && exact=$v
    done < <(config_text projects)
  fi
  [[ -n $star ]] && t=$star
  [[ -n $exact ]] && t=$exact
  printf '%s' "$t"
}
default_target() { target_for '*'; }

# ---------- 状態(設定 statuses) ----------
# チケットの状態の一覧と、それぞれの分類。tb のコマンドと内部の処理(nudge・digest・reminders・shed・spawn・Web UI など)は
# 状態の名前ではなく分類で判断するので、状態を足す・外す・名前を変えるのは設定だけでできる。
# 書式: 1 行 1 つ「<状態> <分類> [= 説明]」。# 以降はコメント。書いた順が一覧・ボードの列の順(ボードは分類の順にまとめる)。
# 分類(固定。コードの判断に使う):
#   todo      未着手。tb add の既定、tb claim の元、nudge --pool の候補
#   active    作業中。tb claim の先、nudge の催促、spawn の親、停滞の目印
#   review    確認待ち(担当は持ったまま、手は止まっている)
#   blocked   詰まり。tb block の先
#   done      完了(閉じた状態)。tb done の先、tb report できる
#   cancelled 取り消し(閉じた状態)。tb cancel の先
# tb のコマンドが状態を書くときは、その分類で最初に書いた状態を使う(tb claim → active の先頭、など)。
# todo / active / done の分類にはそれぞれ 1 つ以上要る。設定が無ければ下の組み込みの 6 つ(今までと同じ)。
# 組み込みの名前(todo など)は、設定に書かなくても組み込みの分類として数える(アラート・提案のチケットは DB のトリガーがこの名前で書く)。
# 設定から外した状態には新しく変えられない(tb update --status が断る)が、既にその状態のチケットは分類どおりに扱う。
STATUS_CATEGORIES="todo active review blocked done cancelled"
STATUS_BUILTIN=$'todo\ttodo\nin_progress\tactive\nreview\treview\ndone\tdone\nblocked\tblocked\ncancelled\tcancelled'
declare -A ST_CAT=() ST_DESC=()
ST_LOADED=0
# statuses_load より前(DB を開かないコマンドの道筋)に使われても止まらないよう、組み込みの値を入れておく
ST_BOARD="todo in_progress review blocked done cancelled"
ST_CLOSED_SQL="'done','cancelled'"; ST_HELD_SQL="'in_progress','review','blocked'"
ST_TODO_SQL="'todo'"; ST_ACTIVE_SQL="'in_progress'"; ST_DONE_SQL="'done'"; ST_CANCELLED_SQL="'cancelled'"

# char_len <文字列> → UTF-8 の文字数(ロケールに頼らない。C ロケールの ${#s} はバイト数になるので、続きのバイト 10xxxxxx を数えない)
char_len() { local LC_ALL=C s=$1; s=${s//[$'\x80'-$'\xbf']/}; printf '%s' "${#s}"; }
# 組み込みの名前を別の分類に書いたら断る(tb がその分類の状態を書くときの代わりに使うので、分類がずれると tb block が未着手にする、などになる)
statuses_builtin_cat() { case $1 in todo|review|done|blocked|cancelled) printf '%s' "$1";; in_progress) printf active;; esac; }
# statuses_parse <本文>: 正しければ ST_PARSED に「状態<TAB>分類<TAB>説明」の行を入れて 0。不正な行は stderr に出して 1
statuses_parse() {
  local line k c d b n=0 bad="" seen=" " cats=" " need
  ST_PARSED=""
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    d=""; if [[ $line == *=* ]]; then d=$(trim "${line#*=}"); line=$(trim "${line%%=*}"); fi
    k=${line%%[[:space:]]*}; c=$(trim "${line#"$k"}")
    if [[ -z $c || $c == *[[:space:]]* ]]; then bad+="${n} 行目: <状態> <分類> [= 説明] の形ではありません: $line"$'\n'; continue; fi
    if (( $(char_len "$k") > 30 )) || [[ $k =~ [],\"\'\`\\*?[] ]]; then bad+="${n} 行目: 状態は 1〜30 文字で、カンマ・引用符・* ? [ ] を含まない: $k"$'\n'; continue; fi
    [[ " $STATUS_CATEGORIES " == *" $c "* ]] || { bad+="${n} 行目: 分類は $STATUS_CATEGORIES のどれか: $c"$'\n'; continue; }
    b=$(statuses_builtin_cat "$k")
    [[ -z $b || $b == "$c" ]] || { bad+="${n} 行目: 組み込みの状態 $k の分類は $b だけです(別の分類にするなら別の名前にする): $c"$'\n'; continue; }
    [[ $seen != *" $k "* ]] || { bad+="${n} 行目: 状態が重複しています: $k"$'\n'; continue; }
    seen+="$k "; cats+="$c "
    ST_PARSED+="$k"$'\t'"$c"$'\t'"$d"$'\n'
  done <<<"$1"
  if [[ -z $bad ]]; then
    [[ -n $ST_PARSED ]] || bad+="状態が 1 つもありません(設定を消すなら tb config unset statuses)"$'\n'
    for need in todo active done; do
      [[ -z $ST_PARSED || $cats == *" $need "* ]] || bad+="分類 $need の状態が 1 つもありません"$'\n'
    done
  fi
  [[ -z $bad ]] && return 0
  printf '%s' "$bad" >&2; return 1
}
# tb config set / edit statuses の検査(不正なら書かない)
config_validate_statuses() { statuses_parse "$1"; }

# 設定を読み、STATUSES(設定した状態。書いた順)・ST_CAT(状態 → 分類。組み込みの名前を含む)・CLOSED_STATUSES などを埋める。
# 1 プロセスで 1 回だけ読む。設定が読めなければ(不正な本文)警告して組み込みの値を使う
statuses_load() {
  ((ST_LOADED)) && return 0
  ST_LOADED=1
  local body=$STATUS_BUILTIN k c d c2
  if config_has statuses 2>/dev/null; then
    if statuses_parse "$CFG_BODY" 2>/dev/null; then body=$ST_PARSED
    else echo "tb: 設定 statuses に不正な行があるので組み込みの状態を使います(tb config edit statuses で直す)" >&2; fi
  fi
  ST_CAT=(); ST_DESC=(); STATUSES=""
  while IFS=$'\t' read -r k c d; do [[ -n $k ]] || continue; ST_CAT[$k]=$c; ST_DESC[$k]=$d; STATUSES+="${STATUSES:+ }$k"; done <<<"$body"
  # 組み込みの名前は、設定に無ければ組み込みの分類で数える(トリガーが書く行・外した状態のまま残っている行)
  while IFS=$'\t' read -r k c; do [[ -n ${ST_CAT[$k]+x} ]] || ST_CAT[$k]=$c; done <<<"$STATUS_BUILTIN"
  CLOSED_STATUSES=$(st_names done cancelled)
  ST_BOARD=""   # ボードの列: 分類の順、同じ分類は書いた順
  for c2 in $STATUS_CATEGORIES; do for k in $STATUSES; do [[ ${ST_CAT[$k]} == "$c2" ]] && ST_BOARD+="${ST_BOARD:+ }$k"; done; done
  # SQL に埋める一覧(よく使う組)
  ST_CLOSED_SQL=$(st_sql done cancelled)
  ST_HELD_SQL=$(st_sql active review blocked)   # 担当が持っている(閉じていない・未着手でない)
  ST_TODO_SQL=$(st_sql todo); ST_ACTIVE_SQL=$(st_sql active); ST_DONE_SQL=$(st_sql done); ST_CANCELLED_SQL=$(st_sql cancelled)
}
# st_names <分類...> → その分類の状態(空白区切り。設定した順の後に、組み込みの名前)
st_names() {
  statuses_load
  local out="" k c
  for k in $STATUSES $(cut -f1 <<<"$STATUS_BUILTIN"); do
    [[ " $out " == *" $k "* ]] && continue
    for c; do [[ ${ST_CAT[$k]} == "$c" ]] && { out+="${out:+ }$k"; break; }; done
  done
  printf '%s' "$out"
}
# st_sql <分類...> → SQL の IN (...) の中身('a','b')。当たる状態が無ければ空集合(IN も NOT IN も正しく効く。NULL だと NOT IN が偽になる)
st_sql() {
  local k out=""
  for k in $(st_names "$@"); do out+="${out:+,}$(q "$k")"; done
  printf '%s' "${out:-SELECT NULL WHERE 0}"
}
# st_main <分類> → tb がその分類の状態を書くときの名前(設定でその分類の最初に書いたもの。無ければ組み込みの名前)
st_main() {
  statuses_load
  local k
  for k in $STATUSES; do [[ ${ST_CAT[$k]} == "$1" ]] && { printf '%s' "$k"; return 0; }; done
  awk -F'\t' -v c="$1" '$2 == c { print $1; exit }' <<<"$STATUS_BUILTIN"
}
# st_cat <状態> → 分類(知らない状態なら空)
st_cat() { statuses_load; printf '%s' "${ST_CAT[$1]:-}"; }
# st_is <状態> <分類...>: その状態がどれかの分類なら 0
st_is() { local s=$1 c x; shift; x=$(st_cat "$s"); for c; do [[ $x == "$c" ]] && return 0; done; return 1; }

# ---------- tb 自身が使うラベル(設定 system-labels) ----------
# tb が付けたり探したりするラベルの名前。書式: 1 行 1 つ「<用途> = <ラベル>」。# 以降はコメント。書かなかった用途は組み込みの名前。
#   deadline = deadline        tb deadlines-import が期限のチケットに付けるラベル
#   team     = team:{team}     tb nudge --pool が見る team のラベル({team} が team の名前に置き換わる。必ず含める)
# アラート・提案のチケットのラベル(alert / proposal / rejected / role:*)は DB のトリガーが付けるので固定。
declare -A SYSLABEL_BUILTIN=([deadline]=deadline [team]='team:{team}')
SYSLABELS_LOADED=0
declare -A SYSLABEL=()
system_labels_parse() {  # $1=本文 → SYSLABEL_PARSED(用途<TAB>ラベル の行)。不正な行は stderr に出して 1
  local line k v n=0 bad="" seen=" " chk
  SYSLABEL_PARSED=""
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    [[ $line == *=* ]] || { bad+="${n} 行目: <用途> = <ラベル> の形ではありません: $line"$'\n'; continue; }
    k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
    [[ -n ${SYSLABEL_BUILTIN[$k]+x} ]] || { bad+="${n} 行目: 知らない用途です: $k(使えるのは: $(printf '%s\n' "${!SYSLABEL_BUILTIN[@]}" | LC_ALL=C sort | paste -sd' ' -))"$'\n'; continue; }
    [[ $seen != *" $k "* ]] || { bad+="${n} 行目: 用途が重複しています: $k"$'\n'; continue; }
    chk=$v
    if [[ $k == team ]]; then
      [[ $v == *"{team}"* ]] || { bad+="${n} 行目: team のラベルには {team} を含める: $v"$'\n'; continue; }
      chk=${v//"{team}"/x}
    fi
    if [[ -z $chk ]] || (( $(char_len "$chk") > 40 )) || [[ $chk =~ [[:space:],\"\'] ]]; then bad+="${n} 行目: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: $v"$'\n'; continue; fi
    seen+="$k "; SYSLABEL_PARSED+="$k"$'\t'"$v"$'\n'
  done <<<"$1"
  [[ -z $bad ]] && return 0
  printf '%s' "$bad" >&2; return 1
}
config_validate_system_labels() { system_labels_parse "$1"; }
# system_label_of <用途> [team の名前] → ラベル
system_label_of() {
  if ((! SYSLABELS_LOADED)); then
    SYSLABELS_LOADED=1
    local k v
    SYSLABEL=(); for k in "${!SYSLABEL_BUILTIN[@]}"; do SYSLABEL[$k]=${SYSLABEL_BUILTIN[$k]}; done
    if config_has system-labels 2>/dev/null; then
      if system_labels_parse "$CFG_BODY" 2>/dev/null; then
        while IFS=$'\t' read -r k v; do [[ -n $k ]] && SYSLABEL[$k]=$v; done <<<"$SYSLABEL_PARSED"
      else echo "tb: 設定 system-labels に不正な行があるので組み込みのラベルを使います(tb config edit system-labels で直す)" >&2; fi
    fi
  fi
  local l=${SYSLABEL[$1]}
  [[ $1 != team ]] || l=${l//"{team}"/${2:-}}
  printf '%s' "$l"
}
