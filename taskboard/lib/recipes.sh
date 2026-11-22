# shellcheck shell=bash
# 部品(ADR 0005 の段 1)。bin/tb から source される。単体では実行しない。
#
# 部品は、1 つの副作用だけを持つ小さな操作。レシピ(部品の並び。段 2 から DB に登録)は、ここにある部品だけを使える。
# 部品の一覧・引数の型・リスク階層・人だけかどうかは、このファイルに固定する。設定・DB・環境変数では増やせず、緩められない。
#   - リスク階層は部品ごとに low / mid / high / human-only。レシピのリスク階層は、中の部品の最大(part_risk_max)。宣言で下げられない
#   - 人だけ(human-only)は、リスク階層が human-only の部品。別の欄を持たない(リスク階層と食い違わない)
#   - 判定の操作名は、承認判定(approvals / grants)に渡す名前。$<引数名> は、型の検査を通った値に置き換える。無ければ -
#   - 実行役は inside(依頼者の pane の中の tb が実行してよい)か outside(pane の外の実行役が行う。段 3)
# 部品を足したら、approvals-catalog.conf と、taskboard/test/cases/83_parts.sh の期待も見直す(操作名がカタログにあり、
# 部品のリスク階層がカタログの階層より低くないことをテストが確かめる)。

PART_RISK_LEVELS="low mid high human-only"   # 低い順
PART_SIDES="none local-write external-send external-write"
PART_RUNNERS="inside outside"

# 名前 | 副作用 | リスク階層 | 実行役 | 判定の操作名(空白区切り。無ければ -) | 引数(空白区切り。名前?:型 は省略可) | 説明
#   型: agent(agent 名)/ task(T-<project>-NNN)/ comment(C-<n>)/ prq(PQ-<n>)/ config-key(設定のキー)/
#       int:<最小>-<最大> / enum:<値>/<値>/... / text:<最大字数>(制御文字は改行とタブだけ)
# shellcheck disable=SC2016  # $<引数名> は展開しない(part_ops が置き換える)
PARTS_TABLE='
agent-status     | none           | low        | inside  | -                                       | target:agent                   | agent の状態(idle / working など)を読む
wait-idle        | none           | low        | inside  | -                                       | target:agent timeout:int:1-600 | agent が idle か done になるまで待つ(秒の上限つき。過ぎたら失敗)
task-show        | none           | low        | inside  | -                                       | task:task                      | チケットを読む(tb show と同じ出力)
send-info        | local-write    | mid        | inside  | send:info:$to                           | to:agent text:text:2000        | 情報共有を送る(tb send --kind info と同じ判定)
agent-stop       | local-write    | mid        | outside | agent:stop:$target                      | target:agent                   | agent を止める(tb agent-stop と同じ判定)
agent-revive     | local-write    | mid        | outside | agent:revive:$target                    | target:agent prompt?:text:4000 | 止めた agent を同じ会話から戻す(tb agent-revive と同じ判定)
agent-restart    | local-write    | mid        | outside | agent:stop:$target agent:revive:$target | target:agent prompt?:text:4000 | 止めて同じ会話で戻す。stop と revive の両方の判定が要る。自分自身の再起動は pane の外の実行役が行う
agent-force-stop | local-write    | human-only | outside | -                                       | target:agent                   | agent を強制で止める(tb agent-stop --force)
comment-resolve  | local-write    | human-only | inside  | -                                       | comment:comment                | 報告(question / blocked)を対応済みにする(tb resolve)
pr-queue-approve | external-write | human-only | outside | -                                       | id:prq                         | PR キューの依頼を承認する(tb pr-queue approve)
config-set       | local-write    | human-only | inside  | -                                       | key:config-key                 | 設定を書き換える(tb config set)
'

declare -gA PART_SIDE=() PART_RISK=() PART_RUNNER=() PART_OPS=() PART_ARGS=() PART_DESC=()
PART_NAMES=""   # 表の順
PART_ERR=""

# 表を読む。表の誤りはコードの誤りなので、使う前に止める(fail-closed)
parts_load() {
  [[ -z $PART_NAMES ]] || return 0
  local line n s r x o a d spec t
  while IFS= read -r line; do
    [[ -n ${line//[[:space:]]/} ]] || continue
    IFS='|' read -r n s r x o a d <<<"$line"
    n=$(trim "$n"); s=$(trim "$s"); r=$(trim "$r"); x=$(trim "$x"); o=$(trim "$o"); a=$(trim "$a"); d=$(trim "$d")
    [[ $n =~ ^[a-z][a-z0-9-]{0,31}$ && -z ${PART_SIDE[$n]+x} ]] || die "部品の表の誤り(名前): $n"
    [[ " $PART_SIDES " == *" $s "* && " $PART_RISK_LEVELS " == *" $r "* && " $PART_RUNNERS " == *" $x "* && -n $d ]] \
      || die "部品の表の誤り(副作用・リスク階層・実行役・説明): $n"
    for spec in $a; do
      t=${spec#*:}
      if ! [[ $spec == *:* && ${spec%%:*} =~ ^[a-z][a-z0-9_]{0,15}\??$ ]] || ! part_type_ok "$t"; then die "部品の表の誤り(引数): $n $spec"; fi
    done
    PART_SIDE[$n]=$s; PART_RISK[$n]=$r; PART_RUNNER[$n]=$x; PART_ARGS[$n]=$a; PART_DESC[$n]=$d
    [[ $o == - ]] && o=""
    PART_OPS[$n]=$o
    PART_NAMES+="${PART_NAMES:+ }$n"
  done <<<"$PARTS_TABLE"
}

part_exists() { parts_load; [[ -n $1 && -n ${PART_SIDE[$1]+x} ]]; }
part_human_only() { parts_load; [[ ${PART_RISK[$1]:-} == human-only ]]; }

# 型の書き方が正しいか(表と、段 2 のレシピの宣言で使う)
part_type_ok() {
  case $1 in
    agent|task|comment|prq|config-key) return 0;;
    int:*) [[ ${1#int:} =~ ^([0-9]{1,6})-([0-9]{1,6})$ ]] && ((10#${BASH_REMATCH[1]} <= 10#${BASH_REMATCH[2]}));;
    enum:*) [[ ${1#enum:} =~ ^[a-z0-9_-]{1,32}(/[a-z0-9_-]{1,32}){0,15}$ ]];;
    text:*) [[ ${1#text:} =~ ^[0-9]{1,5}$ ]] && ((10#${1#text:} >= 1 && 10#${1#text:} <= 10000));;
    *) return 1;;
  esac
}

# 値が型に合うか。合わなければ 1 で PART_ERR に理由
part_value_ok() {  # $1=型 $2=値
  local t=$1 v=$2 lo hi
  PART_ERR=""
  case $t in
    agent) [[ $v != human && $v =~ $AGENT_NAME_RE ]] || PART_ERR="agent 名ではありません";;
    task) [[ $v =~ ^T-[A-Za-z0-9_]+-[0-9]{3,}$ ]] || PART_ERR="タスク ID(T-<project>-NNN)ではありません";;
    comment) [[ $v =~ ^C-[0-9]{1,18}$ ]] || PART_ERR="報告の ID(C-<数字>)ではありません";;
    prq) [[ $v =~ ^PQ-[0-9]{1,18}$ ]] || PART_ERR="PR キューの ID(PQ-<数字>)ではありません";;
    config-key) [[ $v =~ ^[a-z][a-z-]{0,31}$ && " $CONFIG_KEYS " == *" $v "* ]] || PART_ERR="設定のキーではありません";;
    int:*) lo=${t#int:}; hi=${lo#*-}; lo=${lo%-*}
           [[ $v =~ ^[0-9]{1,6}$ ]] && ((10#$v >= 10#$lo && 10#$v <= 10#$hi)) || PART_ERR="${lo}〜${hi} の整数ではありません";;
    enum:*) [[ "/${t#enum:}/" == *"/$v/"* && -n $v && $v != */* ]] || PART_ERR="${t#enum:} のどれかではありません";;
    text:*) if (($(char_len "$v") > 10#${t#text:})); then PART_ERR="${t#text:} 字を超えています"
            elif [[ $v =~ [$'\x01'-$'\x08'$'\x0b'-$'\x1f'$'\x7f'] ]]; then PART_ERR="制御文字(改行とタブのほか)を含みます"; fi;;
    *) PART_ERR="不明な型 $t";;
  esac
  [[ -z $PART_ERR ]]
}

# 部品の引数を確かめる。$1=部品、続けて <名前>=<値> を並べる。足りない・余分・型違いは 1(PART_ERR に理由)
# 通ったら PART_VAL(連想配列。名前 → 値)に入れる
declare -gA PART_VAL=()
part_args_check() {
  local part=$1 kv k v spec nm opt t; shift
  PART_ERR=""; PART_VAL=()
  part_exists "$part" || { PART_ERR="部品の一覧にありません: $part"; return 1; }
  declare -A seen=()
  for kv in "$@"; do
    [[ $kv == *=* ]] || { PART_ERR="引数は <名前>=<値>: $kv"; return 1; }
    k=${kv%%=*}; v=${kv#*=}
    [[ -z ${seen[$k]+x} ]] || { PART_ERR="引数 $k が 2 回あります"; return 1; }
    seen[$k]=1
    t=""
    for spec in ${PART_ARGS[$part]}; do nm=${spec%%:*}; [[ ${nm%\?} == "$k" ]] && t=${spec#*:}; done
    [[ -n $t ]] || { PART_ERR="部品 $part に引数 $k はありません(使えるのは: ${PART_ARGS[$part]:-なし})"; return 1; }
    part_value_ok "$t" "$v" || { PART_ERR="$part の引数 $k: $PART_ERR"; return 1; }
    PART_VAL[$k]=$v
  done
  for spec in ${PART_ARGS[$part]}; do
    nm=${spec%%:*}; opt=0; [[ $nm == *\? ]] && opt=1; nm=${nm%\?}
    ((opt)) || [[ -n ${seen[$nm]+x} ]] || { PART_ERR="部品 $part の引数 $nm がありません"; return 1; }
  done
}

# 判定の操作名(part_args_check の後に呼ぶ)。1 行 1 つ。$<名前> は PART_VAL の値に置き換える
part_ops() {  # $1=部品
  local op out nm
  for op in ${PART_OPS[$1]:-}; do
    [[ $op =~ ^([a-z]+:[a-z]+):\$([a-z][a-z0-9_]*)$ ]] || die "部品の表の誤り($1 の操作名は <種別>:<操作>:\$<引数>: $op)"
    nm=${BASH_REMATCH[2]}
    [[ -n ${PART_VAL[$nm]:-} ]] || die "部品の表の誤り($1 の操作名 $op の \$$nm に値がありません)"
    out="${BASH_REMATCH[1]}:${PART_VAL[$nm]}"
    [[ $out =~ ^[A-Za-z0-9._:@-]{1,128}$ ]] || die "部品の操作名に使えない文字があります: $out"
    printf '%s\n' "$out"
  done
}

# リスク階層の順位(low=0 … human-only=3)。知らない値は human-only とみなす(低く数えない)
part_risk_rank() { case $1 in low) echo 0;; mid) echo 1;; high) echo 2;; *) echo 3;; esac; }
# 部品の並びのリスク階層 = 最大。$@=部品の名前。一覧にない部品があれば human-only(fail-closed)
part_risk_max() {
  local p r=0 x
  parts_load
  for p; do
    if part_exists "$p"; then x=$(part_risk_rank "${PART_RISK[$p]}"); else x=3; fi
    ((x > r)) && r=$x
  done
  local -a lv; read -ra lv <<<"$PART_RISK_LEVELS"; printf '%s' "${lv[$r]}"
}

# ---------- コマンド ----------
# tb parts [--json]: 部品の一覧(名前 副作用 リスク階層 人だけ 実行役 判定の操作名 引数 説明)。読むだけ
cmd_parts() {
  local json=0 p
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) json=1; shift;;
      *) usage "parts: 不明な引数 $1";;
    esac
  done
  parts_load
  for p in $PART_NAMES; do
    if ((json)); then
      jq -nc --arg n "$p" --arg s "${PART_SIDE[$p]}" --arg r "${PART_RISK[$p]}" --arg x "${PART_RUNNER[$p]}" \
        --arg o "${PART_OPS[$p]}" --arg a "${PART_ARGS[$p]}" --arg d "${PART_DESC[$p]}" \
        '{name:$n, side:$s, risk:$r, human_only:($r=="human-only"), runner:$x,
          ops:($o | split(" ") | map(select(. != ""))), args:($a | split(" ") | map(select(. != ""))), desc:$d}'
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$p" "${PART_SIDE[$p]}" "${PART_RISK[$p]}" \
        "$([[ ${PART_RISK[$p]} == human-only ]] && echo yes || echo no)" "${PART_RUNNER[$p]}" \
        "${PART_OPS[$p]:--}" "${PART_ARGS[$p]:--}" "${PART_DESC[$p]}"
    fi
  done
}

# tb part-check <部品> [<名前>=<値> ...]: 引数の型を確かめ、リスク階層・人だけ・実行役・判定の操作名を出す。読むだけ(実行しない)
# 終了コード 0=通った / 1=部品の一覧にない / 2=使い方・引数の誤り
cmd_part-check() {
  [[ $# -ge 1 && $1 != -* ]] || usage "part-check <部品> [<名前>=<値> ...](部品の一覧は tb parts)"
  local part=$1; shift
  part_exists "$part" || die "部品の一覧にありません: $part(tb parts)"
  part_args_check "$part" "$@" || usage "part-check: $PART_ERR"
  printf 'part: %s\nrisk: %s\nhuman_only: %s\nrunner: %s\n' "$part" "${PART_RISK[$part]}" \
    "$([[ ${PART_RISK[$part]} == human-only ]] && echo yes || echo no)" "${PART_RUNNER[$part]}"
  part_ops "$part" | sed 's/^/op: /'
}

# tb part-risk <部品> ...: 部品の並びのリスク階層(中の部品の最大)。読むだけ。一覧にない部品があれば終了コード 1(並びとして使えない)
cmd_part-risk() {
  [[ $# -ge 1 ]] || usage "part-risk <部品> ...(部品の並びのリスク階層 = 最大)"
  local p
  for p; do part_exists "$p" || die "部品の一覧にありません: $p(tb parts)"; done
  part_risk_max "$@"; echo
}
