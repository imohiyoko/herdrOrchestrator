# shellcheck shell=bash
# 土台(ADR 0004 / ADR 0006): tb の全コマンドの権限段階(allow / ask-any / ask-human / only-human)。bin/tb から source される。単体では実行しない。
# 表は taskboard/perms/commands.tbl(コードの一部。場所は固定で、環境変数では差し替えない)。人が変えるのは設定 approvals の cmd: の行だけ。
# 使うのは core・config・identity だけ。入口(bin/tb の main → perm_gate)と、コマンドの中の require_human が呼ぶ。

PERM_TBL=$TB_ROOT/perms/commands.tbl
declare -gA PERM_OP=() PERM_DEF=() PERM_FLOOR=() PERM_WHERE=() PERM_OP_DEF=() PERM_OP_FLOOR=()
PERM_LOADED=0
PERM_TIER=""; PERM_RULE=""; PERM_PATH=""

perm_rank() {  # $1=段階 → 0..3(知らない値は 3 = only-human)
  case $1 in allow) echo 0;; ask-any) echo 1;; ask-human) echo 2;; *) echo 3;; esac
}

# 表を読む(1 回だけ)。読めなければ空のまま(どの道も表に無い = only-human)
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

# perm_tier <操作名>: 今効いている段階を PERM_TIER に、根拠を PERM_RULE に
# 既定と下限は表から。設定 approvals の cmd: で始まる行(上から最初に当たった行)が既定を置き換える。下限より緩い値は下限に上げる
perm_tier() {
  perm_load
  local op=$1 def floor line k v
  if [[ -z ${PERM_OP_DEF[$op]+x} ]]; then PERM_TIER=only-human; PERM_RULE="(表に無い操作: only-human)"; return 0; fi
  def=${PERM_OP_DEF[$op]}; floor=${PERM_OP_FLOOR[$op]}
  PERM_TIER=$def; PERM_RULE="(既定 $def)"
  [[ $floor != only-human ]] || { PERM_TIER=only-human; PERM_RULE="(下限 only-human)"; return 0; }   # 設定では変わらない(設定を読まない)
  local found=0; config_lookup approvals || found=$?
  # 設定を読めない(ファイルの読み取りや DB の問い合わせが失敗した)ときは、設定が無いのとは分けて only-human(fail-closed)
  if [[ -n $CFG_ERR ]]; then PERM_TIER=only-human; PERM_RULE="(設定 approvals を読めない: $CFG_ERR)"; return 0; fi
  if ((found == 0)); then
    while IFS= read -r line || [[ -n $line ]]; do
      line=$(trim "${line%%#*}"); [[ $line == cmd:*=* ]] || continue
      k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
      [[ $v == ask ]] && v=ask-any   # 既存の ask は ask-any の別名
      case $v in allow|ask-any|ask-human|only-human) ;; *) continue;; esac   # 段階の名前と完全に一致する値だけ
      # shellcheck disable=SC2053  # glob として比べる
      [[ $op == $k ]] || continue
      PERM_TIER=$v; PERM_RULE="approvals: $k = $v"; break
    done <<<"$CFG_BODY"
  fi
  if (($(perm_rank "$PERM_TIER") < $(perm_rank "$floor"))); then
    PERM_RULE+="(下限 $floor より緩いので $floor)"; PERM_TIER=$floor
  fi
}

# perm_enforce <操作名> <文面の名前>: PERM_TIER(perm_tier の結果)で止めるか決める。人の端末(pane なし・pane に agent がいない)なら常に通す
perm_enforce() {
  local op=$1 what=$2 pane=${HERDR_PANE_ID:-} n
  [[ $PERM_TIER != allow && -n $pane ]] || return 0
  n=$(pane_owner_name "$pane") || die "$what: herdr agent list に失敗したため、人の端末からの実行か確かめられません"
  [[ -n $n ]] || return 0
  case $PERM_TIER in
    ask-any|ask-human)
      echo "tb: $what は承認が要ります(${op} = ${PERM_TIER}。${PERM_RULE})。承認待ちの仕組みはまだ無いため実行しません。人が自分の端末で実行してください(agent '$n')" >&2
      exit 20;;
    *) die "$what は人が自分の端末で実行します(agent '$n' の pane からは実行できません)";;
  esac
}

# 入口(bin/tb の main): tb <コマンド> <引数...> の道を引き、場所が entry の行だけ確かめる。表に無いコマンドは only-human
perm_gate() {
  perm_path_of "$@" || { PERM_TIER=only-human; PERM_RULE="(表に無いコマンド)"; perm_enforce "cmd:?:$1" "tb $1"; return 0; }
  [[ ${PERM_WHERE[$PERM_PATH]} == entry ]] || return 0
  local op=${PERM_OP[$PERM_PATH]}
  perm_tier "$op"
  perm_enforce "$op" "tb ${PERM_PATH//./ }"
}

# 人の端末からの実行か(agent の pane からは承認・設定変更をさせない)。$1 = 文面の名前 "tb <語...>"
# どの段階にするかは表の行(文面の語の道)と設定 approvals の cmd: の行で決まる。表に無い文面は only-human(従来と同じ)
require_human() {
  local what=$1 words op
  read -ra words <<<"${what#tb }"
  if perm_path_of "${words[@]}"; then op=${PERM_OP[$PERM_PATH]}; perm_tier "$op"
  else op="cmd:?:${what#tb }"; PERM_TIER=only-human; PERM_RULE="(表に無い文面)"; fi
  perm_enforce "$op" "$what"
}
