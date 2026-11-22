# shellcheck shell=bash
# 土台(ADR 0004 / ADR 0006 / ADR 0007): tb の認可。主体 × 操作 × 対象 → 効果(allow / ask-any / ask-human / deny)。bin/tb から source される。単体では実行しない。
# 既定は表 taskboard/perms/commands.tbl(コードの一部。場所は固定で、環境変数では差し替えない)。人が変えるのは設定 policy の行だけ:
#   <主体> <操作> <対象> = <効果>    当たる行のうち、いちばん厳しい効果を採る(行の順番は関係ない)。当たる行が無ければ表の既定
# 人(human)は今は常に通る(人の行は効かない)。system:<名前> の主体は ADR 0007 の段 1(認証)で入る。
# 使うのは core・config・identity だけ。役割(@<役割>)の照合は、機能の層が PERM_NAME_MATCH に関数名を入れる(lib/roles.sh)。
# 入口(bin/tb の main → perm_gate)と、コマンドの中の require_human が呼ぶ。

PERM_TBL=$TB_ROOT/perms/commands.tbl
declare -gA PERM_OP=() PERM_DEF=() PERM_FLOOR=() PERM_WHERE=() PERM_OP_DEF=() PERM_OP_FLOOR=()
PERM_LOADED=0
PERM_TIER=""; PERM_RULE=""; PERM_PATH=""

perm_rank() {  # $1=効果 → 0..3(知らない値は 3 = deny)
  case $1 in allow) echo 0;; ask-any) echo 1;; ask-human) echo 2;; *) echo 3;; esac
}
perm_effect_of() { case $1 in 0) echo allow;; 1) echo ask-any;; 2) echo ask-human;; *) echo deny;; esac; }

# 名前とパターンの照合: 0 一致 / 1 不一致 / 2 確かめられない(役割を読めない)。役割は機能の層が入れる関数で比べる
PERM_NAME_MATCH=""
perm_name_match() {  # $1=名前 $2=パターン(名前の glob か @<役割>)
  [[ $2 == '*' ]] && return 0
  if [[ -n $PERM_NAME_MATCH ]]; then "$PERM_NAME_MATCH" "$1" "$2"; return; fi
  [[ $2 == @* ]] && return 2
  # shellcheck disable=SC2053  # glob として比べる
  [[ $1 == $2 ]]
}

# 呼び出し元(段 1 までは HERDR_PANE_ID と herdr agent list で決める)。PERM_WHO = human か agent 名
PERM_WHO=""
perm_who() {
  [[ -z $PERM_WHO ]] || return 0
  local pane=${HERDR_PANE_ID:-} n=""
  if [[ -n $pane ]]; then n=$(pane_owner_name "$pane") || return 1; fi
  PERM_WHO=${n:-human}
}

# 表を読む(1 回だけ)。読めなければ空のまま(どの道も表に無い = deny)
perm_load() {
  ((PERM_LOADED)) && return 0
  PERM_LOADED=1
  local line p o d f w rest
  [[ -r $PERM_TBL ]] || return 0
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    read -r p o d f w rest <<<"$line"
    [[ -n $w && -z $rest ]] || continue
    PERM_OP[$p]=$o; PERM_DEF[$p]=$d; PERM_FLOOR[$p]=$f; PERM_WHERE[$p]=$w
    PERM_OP_DEF[$o]=$d; PERM_OP_FLOOR[$o]=$f
  done <"$PERM_TBL"
}

# perm_path_of <語...>: 長いほうの道(3 語 → 2 語 → 1 語)で表に当たった道を PERM_PATH に。当たらなければ空
perm_path_of() {
  perm_load
  PERM_PATH=""
  local n p i
  for n in 3 2 1; do
    (($# >= n)) || continue
    p=$1
    for ((i = 2; i <= n; i++)); do p+=".${!i}"; done
    if [[ -n ${PERM_OP[$p]+x} ]]; then PERM_PATH=$p; return 0; fi
  done
  return 1
}

# perm_tier <操作名> [<対象>]: 今の呼び出し元に効く効果を PERM_TIER に、根拠を PERM_RULE に
# 既定と下限は表から。設定 policy の行のうち、主体・操作・対象が当たる行の、いちばん厳しい効果が既定を置き換える。下限より緩い値は下限に上げる。
# 役割を確かめられない行は、締める向きにだけ効かせる(緩める向きには使わない)。設定を読めないときは deny(fail-closed)
perm_tier() {
  perm_load
  local op=$1 target=${2:-*} def floor line l v s o t rest best=-1 unk=-1 r m brule="" urule=""
  if [[ -z ${PERM_OP_DEF[$op]+x} ]]; then PERM_TIER=deny; PERM_RULE="(表に無い操作: deny)"; return 0; fi
  def=${PERM_OP_DEF[$op]}; floor=${PERM_OP_FLOOR[$op]}
  PERM_TIER=$def; PERM_RULE="(既定 $def)"
  local found=0; config_lookup policy || found=$?
  if [[ -n $CFG_ERR ]]; then PERM_TIER=deny; PERM_RULE="(設定 policy を読めない: $CFG_ERR)"; return 0; fi
  if ((found == 0)); then
    while IFS= read -r line || [[ -n $line ]]; do
      l=$(trim "${line%%#*}"); [[ $l == *=* ]] || continue
      v=$(trim "${l#*=}"); read -r s o t rest <<<"${l%%=*}"
      [[ -n $t && -z $rest ]] || continue
      case $v in allow|ask-any|ask-human|deny) ;; *) continue;; esac   # 効果の名前と完全に一致する値だけ
      # shellcheck disable=SC2053  # glob として比べる
      [[ $op == $o ]] || continue
      [[ $s != human && $s != system:* ]] || continue   # 人は常に通る。system は段 1 から
      perm_who || die "herdr agent list に失敗したため、呼び出し元を確かめられません"
      [[ $PERM_WHO != human ]] || return 0                # 人の端末: 判定は perm_enforce が通す
      m=0; perm_name_match "$PERM_WHO" "$s" || m=$?
      ((m != 1)) || continue
      if [[ $target != '*' ]]; then r=0; perm_name_match "$target" "$t" || r=$?; ((r != 1)) || continue; ((r == 0)) || m=2
      fi
      r=$(perm_rank "$v")
      if ((m == 0)); then ((r > best)) && { best=$r; brule="policy: $s $o $t = $v"; }
      else ((r > unk)) && { unk=$r; urule="policy: $s $o $t = $v(役割を確かめられない)"; }; fi
    done <<<"$CFG_BODY"
  fi
  ((best < 0)) || { PERM_TIER=$(perm_effect_of "$best"); PERM_RULE=$brule; }
  if ((unk > $(perm_rank "$PERM_TIER"))); then PERM_TIER=$(perm_effect_of "$unk"); PERM_RULE=$urule; fi
  if (($(perm_rank "$PERM_TIER") < $(perm_rank "$floor"))); then
    PERM_RULE+="(下限 $floor より緩いので $floor)"; PERM_TIER=$floor
  fi
}

# perm_enforce <操作名> <文面の名前>: PERM_TIER(perm_tier の結果)で止めるか決める。人の端末(pane なし・pane に agent がいない)なら常に通す
perm_enforce() {
  local op=$1 what=$2
  [[ $PERM_TIER != allow && -n ${HERDR_PANE_ID:-} ]] || return 0
  perm_who || die "$what: herdr agent list に失敗したため、人の端末からの実行か確かめられません"
  [[ $PERM_WHO != human ]] || return 0
  case $PERM_TIER in
    ask-any|ask-human)
      echo "tb: $what は承認が要ります(${op} = ${PERM_TIER}。${PERM_RULE})。承認待ちの仕組みはまだ無いため実行しません。人が自分の端末で実行してください(agent '$PERM_WHO')" >&2
      exit 20;;
    *)
      [[ $PERM_RULE == policy:* ]] && die "$what は実行できません(${op} = deny。${PERM_RULE})(agent '$PERM_WHO')"
      die "$what は人が自分の端末で実行します(agent '$PERM_WHO' の pane からは実行できません)";;
  esac
}

# 入口(bin/tb の main): tb <コマンド> <引数...> の道を引き、場所が entry の行だけ確かめる。表に無いコマンドは deny
perm_gate() {
  [[ -n ${HERDR_PANE_ID:-} ]] || return 0   # 人の端末は常に通る(設定も読まない)
  perm_path_of "$@" || { PERM_TIER=deny; PERM_RULE="(表に無いコマンド)"; perm_enforce "cmd:?:$1" "tb $1"; return 0; }
  [[ ${PERM_WHERE[$PERM_PATH]} == entry ]] || return 0
  local op=${PERM_OP[$PERM_PATH]}
  perm_tier "$op"
  perm_enforce "$op" "tb ${PERM_PATH//./ }"
}

# コマンドの中の確かめ(既定は人だけ)。$1 = 文面の名前 "tb <語...>"
# どの効果にするかは表の行(文面の語の道)と設定 policy の行で決まる。表に無い文面は deny(従来と同じ)
require_human() {
  local what=$1 words op
  [[ -n ${HERDR_PANE_ID:-} ]] || return 0
  read -ra words <<<"${what#tb }"
  if perm_path_of "${words[@]}"; then op=${PERM_OP[$PERM_PATH]}; perm_tier "$op"
  else op="cmd:?:${what#tb }"; PERM_TIER=deny; PERM_RULE="(表に無い文面)"; fi
  perm_enforce "$op" "$what"
}

# 設定 policy の検査(tb config set / edit のとき): 形の崩れた行・知らない効果・まだ効かない主体は書かない
config_validate_policy() {  # $1=本文
  local line l v s o t rest n=0 bad=""
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1)); l=$(trim "${line%%#*}"); [[ -n $l ]] || continue
    if [[ $l != *=* ]]; then bad+="${n} 行目: <主体> <操作> <対象> = <効果> の形ではありません: $l"$'\n'; continue; fi
    v=$(trim "${l#*=}"); read -r s o t rest <<<"${l%%=*}"
    if [[ -z $t || -n $rest ]]; then bad+="${n} 行目: 主体・操作・対象の 3 つを空白で区切って書きます: $l"$'\n'; continue; fi
    case $v in allow|ask-any|ask-human|deny) ;; *) bad+="${n} 行目: 効果は allow / ask-any / ask-human / deny のどれかです: $v"$'\n'; continue;; esac
    case $s in human|system:*) bad+="${n} 行目: 主体 $s の行はまだ効きません(人は常に通る。system は ADR 0007 の段 1 から)"$'\n';; esac
  done <<<"$1"
  [[ -z $bad ]] && return 0
  printf '%s' "$bad" >&2; return 1
}
