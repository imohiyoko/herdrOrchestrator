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

# ======================================================================
# レシピ(ADR 0005 の段 2)。部品の並びに名前を付けて DB(schema/025 の recipes)に登録する。
#   登録・変更・削除は人だけ(tb recipe-set / recipe-rm)。履歴は recipe_changes
#   実行の入口は 1 つ(tb recipe-run)。判定の操作名は recipe:<名前>:<対象>(approvals / grants の glob で ask / allow)。権限カタログの項目は recipe:*:* の 1 行
#   登録されていないレシピ名は、ask ではなく実行しない(fail-closed)
#   pane の外で行う部品(実行役 outside)を含む並びは、判定してから待ち行列(recipe_runs)に置き、外の実行役(tb recipe-runner)が実行する(段 3)
#
# 本文(1 行 1 つ。# から行末は注釈):
#   desc <説明>                説明(1 行、200 字まで)
#   risk <low|mid|high|human-only>   宣言(参考)。中の部品の最大より低ければ、最大になる(下げられない)
#   arg <名前>[?] <型>         実行のときに <名前>=<値> で渡す引数(? は省略可)。型は部品と同じ書き方
#   step <部品> [<名前>=<値> ...]   値はリテラル(空白と $ を含まない)か、$requester(依頼者)/ $target(対象)/ $<宣言した引数> の 1 語
#   部品の agent 型の引数に書けるのは $requester か $target だけ(部品が向く agent は、いつも依頼者か操作名の対象)。agent 型の arg は宣言できない
# 条件分岐・ループ・シェルは書けない。部品は直接呼び、シェルを経由しない
# ======================================================================
RECIPE_NAME_RE='^[a-z][a-z0-9-]{0,31}$'
RECIPE_MAX_STEPS=16
RECIPE_MAX_ARGS=8
RECIPE_MAX_BYTES=8000
RECIPE_POLL_SEC=${TB_RECIPE_POLL_SEC:-2}   # wait-idle の確かめる間隔(テストで短くする)

valid_recipe_name() { [[ $1 =~ $RECIPE_NAME_RE ]] || usage "レシピ名は英小文字で始まる英小文字・数字・- の 32 字以内(: は使えない): $1"; }

# 本文を読んで検査する。通れば 0 で RCP_* に入れる。通らなければ 1 で RCP_ERR(1 行 1 件)
declare -gA RCP_ARGT=()
RCP_DESC=""; RCP_DECL=""; RCP_RISK=""; RCP_ERR=""; RCP_ARGN=(); RCP_STEPS=()
recipe_parse() {  # $1=本文
  local body=$1 line n=0 kw rest nm t opt part kv k v spec pt err="" ok
  local -a toks parts=()
  RCP_DESC=""; RCP_DECL=""; RCP_RISK=""; RCP_ERR=""; RCP_ARGN=(); RCP_STEPS=(); RCP_ARGT=()
  parts_load
  ((${#body} <= RECIPE_MAX_BYTES)) || { RCP_ERR="本文が長すぎます(${RECIPE_MAX_BYTES} 字まで)"; return 1; }
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    kw=${line%%[[:space:]]*}; rest=""; [[ $line == *[[:space:]]* ]] && rest=$(trim "${line#*[[:space:]]}")
    case $kw in
      desc)
        [[ -z $RCP_DESC ]] || { err+="${n} 行目: desc が 2 回あります"$'\n'; continue; }
        [[ -n $rest && $(char_len "$rest") -le 200 ]] || { err+="${n} 行目: desc は 1〜200 字"$'\n'; continue; }
        RCP_DESC=$rest;;
      risk)
        [[ -z $RCP_DECL ]] || { err+="${n} 行目: risk が 2 回あります"$'\n'; continue; }
        [[ " $PART_RISK_LEVELS " == *" $rest "* && -n $rest ]] || { err+="${n} 行目: risk は $PART_RISK_LEVELS のどれか: $rest"$'\n'; continue; }
        RCP_DECL=$rest;;
      arg)
        read -ra toks <<<"$rest"
        ((${#toks[@]} == 2)) || { err+="${n} 行目: arg <名前>[?] <型>"$'\n'; continue; }
        nm=${toks[0]}; t=${toks[1]}; opt=""; [[ $nm == *\? ]] && opt="?"; nm=${nm%\?}
        [[ $nm =~ ^[a-z][a-z0-9_]{0,15}$ && $nm != requester && $nm != target ]] || { err+="${n} 行目: 引数の名前は英小文字で始まる 16 字まで(requester / target は予約): $nm"$'\n'; continue; }
        [[ -z ${RCP_ARGT[$nm]+x} ]] || { err+="${n} 行目: 引数 $nm が 2 回あります"$'\n'; continue; }
        part_type_ok "$t" || { err+="${n} 行目: 引数 $nm の型が不正です: $t"$'\n'; continue; }
        [[ $t != agent ]] || { err+="${n} 行目: agent 型の引数は宣言できません(対象は --target で渡し、step では \$target と書きます): $nm"$'\n'; continue; }
        RCP_ARGT[$nm]=$t; RCP_ARGN+=("$nm$opt");;
      step)
        read -ra toks <<<"$rest"
        ((${#toks[@]} >= 1)) || { err+="${n} 行目: step <部品> [<名前>=<値> ...]"$'\n'; continue; }
        part=${toks[0]}
        part_exists "$part" || { err+="${n} 行目: 部品の一覧にありません: $part(tb parts)"$'\n'; continue; }
        ok=1; local seen_k=" "
        for kv in "${toks[@]:1}"; do
          [[ $kv == *=* ]] || { err+="${n} 行目: 引数は <名前>=<値>: $kv"$'\n'; ok=0; continue; }
          k=${kv%%=*}; v=${kv#*=}; pt=""; local popt=0
          [[ $seen_k != *" $k "* ]] || { err+="${n} 行目: 引数 $k が 2 回あります"$'\n'; ok=0; continue; }
          seen_k+="$k "
          for spec in ${PART_ARGS[$part]}; do
            [[ ${spec%%:*} == "$k" ]] && pt=${spec#*:}
            [[ ${spec%%:*} == "$k?" ]] && { pt=${spec#*:}; popt=1; }
          done
          [[ -n $pt ]] || { err+="${n} 行目: 部品 $part に引数 $k はありません(使えるのは: ${PART_ARGS[$part]:-なし})"$'\n'; ok=0; continue; }
          # agent 型の引数は依頼者か対象だけ(操作名 recipe:<名前>:<対象> の対象と食い違わないように。他者を対象にするのは --target)
          if [[ $pt == agent && $v != '$requester' && $v != '$target' ]]; then
            err+="${n} 行目: $part の $k(agent 型)に書けるのは \$requester か \$target だけです: $v"$'\n'; ok=0; continue
          fi
          if [[ $v == \$* ]]; then
            case ${v#\$} in
              requester|target) [[ $pt == agent ]] || { err+="${n} 行目: $v は agent 型の引数にだけ使えます($part の $k は $pt)"$'\n'; ok=0; };;
              *) t=${RCP_ARGT[${v#\$}]:-}
                 if [[ -z $t ]]; then err+="${n} 行目: $v は宣言されていません(arg の行は step より前に書きます)"$'\n'; ok=0
                 elif [[ ${t%%:*} != "${pt%%:*}" ]]; then err+="${n} 行目: $v の型 $t は $part の $k の型 $pt と合いません"$'\n'; ok=0
                 elif ((popt == 0)) && [[ " ${RCP_ARGN[*]} " == *" ${v#\$}? "* ]]; then
                   err+="${n} 行目: 省略できる引数 $v を、省略できない $part の $k に渡せません"$'\n'; ok=0; fi;;
            esac
          else
            [[ $v != *\$* ]] || { err+="${n} 行目: 値の中に \$ は書けません(\$<名前> は 1 語だけ): $v"$'\n'; ok=0; continue; }
            part_value_ok "$pt" "$v" || { err+="${n} 行目: $part の引数 $k: $PART_ERR"$'\n'; ok=0; }
          fi
        done
        if ((ok)); then
          # 必須の引数が書かれているか(値は実行のときに決まるものも含む)
          for spec in ${PART_ARGS[$part]}; do
            nm=${spec%%:*}; [[ $nm == *\? ]] && continue
            [[ " ${toks[*]:1} " == *" $nm="* ]] || { err+="${n} 行目: 部品 $part の引数 $nm がありません"$'\n'; ok=0; }
          done
        fi
        ((ok)) || continue
        RCP_STEPS+=("$rest"); parts+=("$part");;
      *) err+="${n} 行目: 書けるのは desc / risk / arg / step だけです: $kw"$'\n';;
    esac
  done <<<"$body"
  ((${#RCP_STEPS[@]} >= 1 || ${#parts[@]} >= 1)) || [[ -n $err ]] || err+="step が 1 つもありません"$'\n'
  ((${#RCP_STEPS[@]} <= RECIPE_MAX_STEPS)) || err+="step は ${RECIPE_MAX_STEPS} 個まで"$'\n'
  ((${#RCP_ARGN[@]} <= RECIPE_MAX_ARGS)) || err+="arg は ${RECIPE_MAX_ARGS} 個まで"$'\n'
  if [[ -n $err ]]; then RCP_ERR=${err%$'\n'}; return 1; fi
  # リスク階層 = 中の部品の最大。宣言はそれより高いときだけ効く(下げられない)
  RCP_RISK=$(part_risk_max "${parts[@]}")
  if [[ -n $RCP_DECL ]] && (($(part_risk_rank "$RCP_DECL") > $(part_risk_rank "$RCP_RISK"))); then RCP_RISK=$RCP_DECL; fi
  return 0
}

recipe_row() { session_row recipes name "$1"; }   # このセッションの行(JSON。無ければ空)

# ---------- 登録(人だけ) ----------
# tb recipe-set <名前> [--file F]: 本文(省くと標準入力)を検査して登録する。同じ名前があれば置き換える
cmd_recipe-set() {
  [[ $# -ge 1 && $1 != -* ]] || usage "recipe-set <名前> [--file F](本文は省くと標準入力。書式は docs/recipes.md)"
  local name=$1 file="" body old how; shift
  valid_recipe_name "$name"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --file) need_arg "$@"; file=$2; shift 2;;
      *) usage "recipe-set: 不明な引数 $1";;
    esac
  done
  require_human "tb recipe-set"
  if [[ -n $file ]]; then [[ -f $file && -r $file ]] || die "recipe-set: ファイルを読めません: $file"; body=$(cat -- "$file")
  else body=$(cat); fi
  recipe_parse "$body" || { printf '%s\n' "$RCP_ERR" | sed 's/^/tb: recipe-set: /' >&2; echo "tb: recipe-set: 登録しませんでした" >&2; return 2; }
  old=$(sql "SELECT body FROM recipes WHERE name=$(q "$name");")
  how=add; [[ -z $(recipe_row "$name") ]] || how=update
  sql "BEGIN;
    INSERT INTO main.recipes (herdr_session, name, body, risk, steps, updated_by, updated_at)
      VALUES ($SQS, $(q "$name"), $(q "$body"), $(q "$RCP_RISK"), ${#RCP_STEPS[@]}, 'human', $(q "$(now)"))
      ON CONFLICT(herdr_session, name) DO UPDATE SET body=excluded.body, risk=excluded.risk, steps=excluded.steps,
        updated_by=excluded.updated_by, updated_at=excluded.updated_at;
    INSERT INTO main.recipe_changes (herdr_session, name, how, old_body, new_body, risk, changed_by, changed_at)
      VALUES ($SQS, $(q "$name"), '$how', $([[ $how == add ]] && echo NULL || q "$old"), $(q "$body"), $(q "$RCP_RISK"), 'human', $(q "$(now)"));
    COMMIT;" >/dev/null
  if [[ -n $RCP_DECL && $RCP_DECL != "$RCP_RISK" ]]; then
    echo "tb: recipe-set: 宣言 risk $RCP_DECL は、中の部品の最大 $RCP_RISK より低いので $RCP_RISK にしました" >&2
  fi
  echo "$name $([[ $how == add ]] && echo added || echo updated) (risk $RCP_RISK, steps ${#RCP_STEPS[@]}, op recipe:$name:<対象>)"
}

# tb recipe-rm <名前>: 登録を消す(人だけ。履歴は残る)
cmd_recipe-rm() {
  [[ $# -eq 1 ]] || usage "recipe-rm <名前>"
  local name=$1 old
  valid_recipe_name "$name"
  require_human "tb recipe-rm"
  [[ -n $(recipe_row "$name") ]] || die "レシピが登録されていません: $name"
  old=$(sql "SELECT body FROM recipes WHERE name=$(q "$name");")
  sql "BEGIN;
    DELETE FROM main.recipes WHERE herdr_session=$SQS AND name=$(q "$name");
    INSERT INTO main.recipe_changes (herdr_session, name, how, old_body, new_body, risk, changed_by, changed_at)
      VALUES ($SQS, $(q "$name"), 'rm', $(q "$old"), NULL, NULL, 'human', $(q "$(now)"));
    COMMIT;" >/dev/null
  echo "$name removed"
}

# ---------- 一覧・中身・履歴(読むだけ。だれでも) ----------
# tb recipe-list [--json]: 名前 リスク階層 step 数 更新者 更新時刻 説明。リスク階層は今のコードで計算し直した値(登録のときと違えば !)
cmd_recipe-list() {
  local json=0 row name body risk desc
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) json=1; shift;;
      *) usage "recipe-list: 不明な引数 $1";;
    esac
  done
  while IFS= read -r row; do
    [[ -n $row ]] || continue
    name=$(jq -r .name <<<"$row"); body=$(jq -r .body <<<"$row")
    if recipe_parse "$body"; then risk=$RCP_RISK; desc=$RCP_DESC; else risk=invalid; desc="(今のコードでは検査を通らない。実行しない)"; fi
    if ((json)); then
      jq -c --arg r "$risk" --arg d "$desc" '{name, risk:$r, risk_at_set:.risk, steps, updated_by, updated_at, desc:$d}' <<<"$row"
    else
      [[ $risk == "$(jq -r .risk <<<"$row")" ]] || risk+="!"
      printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$risk" "$(jq -r .steps <<<"$row")" "$(jq -r .updated_by <<<"$row")" "$(jq -r .updated_at <<<"$row")" "${desc:--}"
    fi
  done < <(sql -json "SELECT name, body, risk, steps, updated_by, updated_at FROM recipes ORDER BY name;" | jq -c '(. // [])[]')
}

# tb recipe-show <名前>: 本文と、今のコードで計算したリスク階層・step ごとの部品と判定の操作名
cmd_recipe-show() {
  [[ $# -eq 1 ]] || usage "recipe-show <名前>"
  local name=$1 row i s p
  valid_recipe_name "$name"
  row=$(recipe_row "$name"); [[ -n $row ]] || die "レシピが登録されていません: $name"
  printf 'name: %s\nop: recipe:%s:<対象>(既定の対象は依頼者自身)\nupdated: %s %s\n' "$name" "$name" "$(jq -r .updated_by <<<"$row")" "$(jq -r .updated_at <<<"$row")"
  if recipe_parse "$(jq -r .body <<<"$row")"; then
    printf 'risk: %s\n' "$RCP_RISK"
    for i in "${!RCP_STEPS[@]}"; do
      s=${RCP_STEPS[$i]}; p=${s%%[[:space:]]*}
      printf 'step %d: %s(%s / %s / %s / 操作名 %s)\n' "$((i + 1))" "$s" "${PART_SIDE[$p]}" "${PART_RISK[$p]}" "${PART_RUNNER[$p]}" "${PART_OPS[$p]:--}"
    done
  else
    printf 'risk: invalid(今のコードでは検査を通らないため、実行しない)\n'; printf '%s\n' "$RCP_ERR" | sed 's/^/error: /'
  fi
  echo "---"; jq -r .body <<<"$row"
}

# tb recipe-history [<名前>] [--json]: 登録の変更(時刻 名前 add|update|rm リスク階層 変えた者)
cmd_recipe-history() {
  local name="" json=0 where="1=1"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) json=1; shift;;
      -*) usage "recipe-history: 不明な引数 $1";;
      *) [[ -z $name ]] || usage "recipe-history [<名前>] [--json]"; valid_recipe_name "$1"; name=$1; shift;;
    esac
  done
  [[ -z $name ]] || where="name=$(q "$name")"
  if ((json)); then sql -json "SELECT id, name, how, old_body, new_body, risk, changed_by, changed_at FROM recipe_changes WHERE $where ORDER BY id;" | jq -c '(. // [])[]'
  else sql -separator $'\t' "SELECT changed_at, name, how, COALESCE(risk,'-'), changed_by FROM recipe_changes WHERE $where ORDER BY id;"; fi
}

# ---------- 実行の入口 ----------
# 記録(チケットの履歴 task_events。ADR 0003)。--task が無ければ task_id は空(どのチケットにも属さない)。actor は依頼者
RUN_TASK=""; RUN_EID=""; RUN_TARGET=""; RUN_VIA=""
recipe_event() {  # $1=kind $2=old $3=new $4=ref → 行の id を出す
  sql "INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
    VALUES ($SQS, $(q "$RUN_TASK"), $(q "$1"), $([[ -n $2 ]] && q "$2" || echo NULL), $(q "$3"), $(q "$REQ_BY"), $(q "$4"), $(q "$(now)"));
    SELECT last_insert_rowid();"
}

# 実行できる部品(段 3)。ここに無い部品は判定まで行い、実行しない(send-info / pr-queue-approve / config-set はまだ)
PART_EXEC="agent-status wait-idle task-show agent-stop agent-revive agent-restart agent-force-stop comment-resolve"

# 依頼者を決める。人の端末(pane なし)でも、定期ジョブ(scheduler が SCHED_JOB を渡す)から呼ばれたときは人として扱わない:
# ジョブに持ち主(OWNER。scheduler が SCHED_OWNER で渡す)があればその agent の権限で判定し、無ければ実行しない(守り 8)
recipe_requester() {
  RUN_VIA=""
  requester
  [[ $REQ_BY == human && -n ${SCHED_JOB:-} ]] || return 0
  if [[ -z ${SCHED_OWNER:-} ]]; then
    echo "tb: recipe-run: 定期ジョブ($SCHED_JOB)からは、ジョブに持ち主(OWNER=<agent 名>)があるときだけ実行します(人の権限では実行しない)" >&2; return 4
  fi
  part_value_ok agent "$SCHED_OWNER" || { echo "tb: recipe-run: ジョブ $SCHED_JOB の OWNER が agent 名ではありません: $SCHED_OWNER" >&2; return 4; }
  REQ_BY=$SCHED_OWNER; RUN_VIA="sched:$SCHED_JOB"
}

# agent 型の値が依頼者か対象か(登録の検査をすり抜けた行への備え)。$1=部品(PART_VAL に引数)
recipe_agents_ok() {
  local spec k
  for spec in ${PART_ARGS[$1]}; do
    [[ ${spec#*:} == agent ]] || continue
    k=${spec%%:*}; k=${k%\?}
    [[ -z ${PART_VAL[$k]+x} || ${PART_VAL[$k]} == "$REQ_BY" || ${PART_VAL[$k]} == "$RUN_TARGET" ]] && continue
    PART_ERR="$1 の $k(${PART_VAL[$k]})は依頼者でも対象でもありません"; return 1
  done
}

# 部品ごとの判定(依頼者 REQ_BY の権限。レシピの許可は部品の許可の代わりにならない)。$1=plan(JSON)
# 全部を判定し、1 行ずつ出す。JUDGE_RULES(step ごとの判定と根拠)。止まる step があれば JUDGE_STOP(理由)と JUDGE_RC(4 / 20)
JUDGE_STOP=""; JUDGE_RC=0; JUDGE_RULES=(); JUDGE_OUTSIDE=0
recipe_judge() {
  local plan=$1 i n part verdict rule k c
  local -a kvs
  JUDGE_STOP=""; JUDGE_RC=0; JUDGE_RULES=(); JUDGE_OUTSIDE=0
  for ((i = 0; i < $(jq length <<<"$plan"); i++)); do
    n=$((i + 1)); part=$(jq -r ".[$i].part" <<<"$plan")
    mapfile -t kvs < <(jq -r ".[$i].args | to_entries[] | \"\(.key)=\(.value)\"" <<<"$plan")
    verdict=allow; rule=""
    if ! part_args_check "$part" "${kvs[@]}"; then verdict=deny; rule=$PART_ERR; JUDGE_RC=4
    elif ! recipe_agents_ok "$part"; then verdict=deny; rule=$PART_ERR; JUDGE_RC=4
    elif part_human_only "$part" && [[ $REQ_BY != human ]]; then verdict=deny; rule="人だけの部品(コードで固定)"; JUDGE_RC=4
    elif [[ " $PART_EXEC " != *" $part "* ]]; then verdict=deny; rule="この部品の実行はまだありません(判定だけ)"; JUDGE_RC=4
    else
      # agent から指揮官を止める・戻すことはしない(tb agent-restart と同じ)
      if [[ $REQ_BY != human && -n ${PART_VAL[target]:-} && ( ${PART_OPS[$part]} == *agent:stop* || ${PART_OPS[$part]} == *agent:revive* ) ]]; then
        while IFS= read -r c; do
          [[ -n $c && $c == "${PART_VAL[target]}" ]] && { verdict=deny; rule="指揮官(${PART_VAL[target]})は agent から止めない・戻さない(人が実行します)"; JUDGE_RC=4; }
        done < <(conductor_names)
      fi
      if [[ $verdict == allow ]]; then
        while IFS= read -r k; do
          [[ -n $k ]] || continue
          if [[ $REQ_BY == human ]]; then rule+="${rule:+ / }$k: 人の端末"; continue; fi
          approval_of "$k" "$REQ_BY"
          rule+="${rule:+ / }$k: $APPROVAL_RULE"
          [[ $APPROVAL_VALUE == allow ]] || { verdict=ask; ((JUDGE_RC == 4)) || JUDGE_RC=20; }
        done < <(part_ops "$part")
        [[ -n $rule ]] || rule="判定の操作名なし(読み取り)"
      fi
      [[ ${PART_RUNNER[$part]} != outside ]] || JUDGE_OUTSIDE=1
    fi
    JUDGE_RULES+=("$verdict $rule")
    echo "step $n $part: $verdict($rule)"
    if [[ $verdict != allow ]]; then JUDGE_STOP="step $n ($part) で止めました: $verdict($rule)"; return 0; fi
  done
}

# 部品の実行。出力は PART_OUT、0=成功。$1=部品(PART_VAL に引数)$2=実行の記録に残す名前(recipe:R-<n> など)
PART_OUT=""
part_run() {
  local st out t0 r=0 args
  PART_OUT=""
  case $1 in
    agent-status)
      out=$(herdr_call agent get "${PART_VAL[target]}" 2>/dev/null) || { PART_OUT="${PART_VAL[target]} の状態を引けません"; return 1; }
      st=$(jq -r '.result.agent.agent_status // empty' <<<"$out" 2>/dev/null)
      [[ -n $st ]] || { PART_OUT="${PART_VAL[target]} の状態を引けません"; return 1; }
      PART_OUT="${PART_VAL[target]} $st";;
    wait-idle)
      t0=$SECONDS
      while :; do
        out=$(herdr_call agent get "${PART_VAL[target]}" 2>/dev/null) && st=$(jq -r '.result.agent.agent_status // empty' <<<"$out" 2>/dev/null) || st=""
        [[ $st == idle || $st == done ]] && { PART_OUT="${PART_VAL[target]} $st"; return 0; }
        ((SECONDS - t0 < 10#${PART_VAL[timeout]})) || { PART_OUT="${PART_VAL[target]} は ${PART_VAL[timeout]} 秒で idle になりませんでした(${st:-状態不明})"; return 1; }
        sleep "$RECIPE_POLL_SEC"
      done;;
    task-show)
      PART_OUT=$( (cmd_show "${PART_VAL[task]}") 2>&1) || return 1;;
    comment-resolve)
      PART_OUT=$( (cmd_resolve "${PART_VAL[comment]}") 2>&1) || return 1;;
    agent-stop|agent-force-stop)
      args=$(jq -nc --argjson f "$([[ $1 == agent-force-stop ]] && echo 1 || echo 0)" --arg r "$2" '{force:$f, recipe:$r}')
      do_agent_stop "${PART_VAL[target]}" "$([[ $1 == agent-force-stop ]] && echo 1 || echo 0)" "$2" || r=$?
      record_op stop "${PART_VAL[target]}" "$args" "$REQ_BY" "$r" "$2: $OP_OUT"; PART_OUT=$OP_OUT; return "$r";;
    agent-revive)
      args=$(jq -nc --arg r "$2" --arg p "${PART_VAL[prompt]:-}" '{fresh:0, recipe:$r} + (if $p=="" then {} else {prompt:$p} end)')
      limits_refuse_running 1 >/dev/null 2>&1 || { PART_OUT="動作中の agent が上限のため戻しません"; record_op revive "${PART_VAL[target]}" "$args" "$REQ_BY" 4 "$2: $PART_OUT"; return 4; }
      do_agent_revive "${PART_VAL[target]}" 0 "" "${PART_VAL[prompt]:-}" "$2" || r=$?
      record_op revive "${PART_VAL[target]}" "$args" "$REQ_BY" "$r" "$2: $OP_OUT"; PART_OUT=$OP_OUT; return "$r";;
    agent-restart)
      args=$(jq -nc --arg r "$2" --arg p "${PART_VAL[prompt]:-}" '{restart:1, recipe:$r} + (if $p=="" then {} else {prompt:$p} end)')
      limits_refuse_running 0 >/dev/null 2>&1 || { PART_OUT="動作中の agent が上限を超えているため触りません"; record_op revive "${PART_VAL[target]}" "$args" "$REQ_BY" 4 "$2: $PART_OUT"; return 4; }
      do_agent_restart "${PART_VAL[target]}" "${PART_VAL[prompt]:-}" "$2" || r=$?
      record_op revive "${PART_VAL[target]}" "$args" "$REQ_BY" "$r" "restart: $2: $OP_OUT"; PART_OUT=$OP_OUT; return "$r";;
    *) PART_OUT="この部品の実行はまだありません: $1"; return 1;;
  esac
}

# 並びどおりに実行する(シェルを経由せず部品を直接呼ぶ)。途中で失敗したら残りは続行しない(巻き戻さない)。$1=plan $2=記録の名前
# 0=全部済んだ / 1=途中の部品が失敗(EXEC_STOP に理由)
EXEC_STOP=""
recipe_exec() {
  local plan=$1 by=$2 i n part rc
  local -a kvs
  EXEC_STOP=""
  for ((i = 0; i < $(jq length <<<"$plan"); i++)); do
    n=$((i + 1)); part=$(jq -r ".[$i].part" <<<"$plan")
    mapfile -t kvs < <(jq -r ".[$i].args | to_entries[] | \"\(.key)=\(.value)\"" <<<"$plan")
    part_args_check "$part" "${kvs[@]}" || { EXEC_STOP="step $n の引数を確かめ直せません: $PART_ERR"; return 1; }
    rc=0; part_run "$part" "$by" || rc=$?
    recipe_event recipe_step "$n $part" "${JUDGE_RULES[$i]} / $([[ $rc == 0 ]] && echo ok || echo failed): $(oneline "$PART_OUT" 300)" "E-$RUN_EID" >/dev/null
    printf '%s\n' "$PART_OUT"
    if ((rc)); then EXEC_STOP="step $n ($part) が失敗しました: $(oneline "$PART_OUT" 300)。step $((n + 1)) 以降は実行していません(済んだ step は巻き戻しません)"; return 1; fi
  done
}

# tb recipe-run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]
# 終了コード 0=すべて済んだ、または外の実行役の待ち行列に置いた(R-<n> queued)/ 1=途中の部品が失敗した(残りは続行しない)/ 2=使い方 /
#            4=実行しない(未登録・検査を通らない・人だけの部品・まだ実行できない部品・持ち主の無い定期ジョブ)/
#            20=人の承認が要る(レシピか部品の判定が ask。承認待ちには積まない。人が自分の端末で実行するか、人が allow にする)
cmd_recipe-run() {
  [[ $# -ge 1 && $1 != -* ]] || usage "recipe-run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]"
  local name=$1 dry=0 kv k v row op i part plan step body rr=0
  local -a given=() tok
  declare -A bound=()
  RUN_TARGET=""; shift
  valid_recipe_name "$name"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --target) need_arg "$@"; RUN_TARGET=$2; shift 2;;
      --task) need_arg "$@"; valid_id "$2"; RUN_TASK=$2; shift 2;;
      --dry-run) dry=1; shift;;
      -*) usage "recipe-run: 不明な引数 $1";;
      *) given+=("$1"); shift;;
    esac
  done
  [[ -z $RUN_TASK ]] || require_task "$RUN_TASK" >/dev/null
  recipe_requester || rr=$?
  if ((rr)); then
    ((dry)) || { RUN_EID=$(recipe_event recipe_run "" "$name target=${RUN_TARGET:--} via=sched:${SCHED_JOB:-}" "recipe:$name:${RUN_TARGET:--}")
                 recipe_event recipe_end "" "定期ジョブの持ち主を確かめられないため実行しません" "E-$RUN_EID" >/dev/null; }
    return 4
  fi
  if [[ -z $RUN_TARGET ]]; then
    [[ $REQ_BY != human ]] || usage "recipe-run: 人の端末からは --target <agent 名> が要ります(既定の対象は依頼者自身)"
    RUN_TARGET=$REQ_BY
  fi
  part_value_ok agent "$RUN_TARGET" || usage "recipe-run: --target: $PART_ERR: $RUN_TARGET"
  op="recipe:$name:$RUN_TARGET"

  _rr_end() {  # $1=終了コード $2=理由(空 = done)
    ((dry)) || recipe_event recipe_end "" "${2:-done}" "E-$RUN_EID" >/dev/null
    [[ -z $2 ]] || echo "tb: recipe-run: $2" >&2
    return "$1"
  }
  ((dry)) || RUN_EID=$(recipe_event recipe_run "" "$name target=$RUN_TARGET${RUN_VIA:+ via=$RUN_VIA}" "$op")
  row=$(recipe_row "$name")
  [[ -n $row ]] || { _rr_end 4 "登録されていないレシピは実行しません: $name(登録は人が tb recipe-set)"; return; }
  body=$(jq -r .body <<<"$row")
  recipe_parse "$body" || { _rr_end 4 "レシピ $name は今のコードでは検査を通らないため実行しません: $(head -1 <<<"$RCP_ERR")"; return; }
  # 引数: 宣言したものだけ。宣言の型で確かめる
  for kv in "${given[@]}"; do
    [[ $kv == *=* ]] || { _rr_end 2 "引数は <名前>=<値>: $kv"; return; }
    k=${kv%%=*}; v=${kv#*=}
    [[ -n ${RCP_ARGT[$k]+x} ]] || { _rr_end 2 "レシピ $name に引数 $k はありません(宣言: ${RCP_ARGN[*]:-なし})"; return; }
    [[ -z ${bound[$k]+x} ]] || { _rr_end 2 "引数 $k が 2 回あります"; return; }
    part_value_ok "${RCP_ARGT[$k]}" "$v" || { _rr_end 2 "引数 $k: $PART_ERR"; return; }
    bound[$k]=$v
  done
  for k in "${RCP_ARGN[@]}"; do
    [[ $k == *\? ]] && continue
    [[ -n ${bound[$k]+x} ]] || { _rr_end 2 "引数 $k がありません"; return; }
  done
  # 並び(plan)を作る: 変数を値に置き換える。省略された引数は、その部品の引数も省く
  plan='[]'
  for step in "${RCP_STEPS[@]}"; do
    read -ra tok <<<"$step"; part=${tok[0]}
    local args='{}'
    for kv in "${tok[@]:1}"; do
      k=${kv%%=*}; v=${kv#*=}
      case $v in
        '$requester') [[ $REQ_BY != human ]] || { _rr_end 4 "人の端末からは \$requester を使うレシピ($name)は実行できません。agent の pane から実行します"; return; }
                      v=$REQ_BY;;
        '$target') v=$RUN_TARGET;;
        \$*) [[ -n ${bound[${v#\$}]+x} ]] || continue; v=${bound[${v#\$}]};;
      esac
      args=$(jq -c --arg k "$k" --arg v "$v" '. + {($k): $v}' <<<"$args")
    done
    plan=$(jq -c --arg p "$part" --argjson a "$args" '. + [{part:$p, args:$a}]' <<<"$plan")
  done

  # 判定 1: レシピの操作名。人の端末は承認なし。agent は approvals / grants(既存の判定のまま)
  local verdict rule
  if [[ $REQ_BY == human ]]; then rule="人の端末"; verdict=allow
  else approval_of "$op" "$REQ_BY"; rule=$APPROVAL_RULE; verdict=$APPROVAL_VALUE; fi
  echo "recipe $name target=$RUN_TARGET risk=$RCP_RISK: $verdict($rule)"
  if [[ $verdict != allow ]]; then
    ((dry)) || recipe_event recipe_step "0 $op" "$verdict $rule" "E-$RUN_EID" >/dev/null
    _rr_end 20 "$op は人の承認が要ります($rule)。人が自分の端末で実行するか、人が allow にします"; return
  fi
  # 判定 2: 部品ごと。全部を判定してから実行する
  recipe_judge "$plan"
  if ((dry)); then [[ -z $JUDGE_STOP ]] && return 0; echo "tb: recipe-run: $JUDGE_STOP" >&2; return "$JUDGE_RC"; fi
  if [[ -n $JUDGE_STOP ]]; then
    for i in "${!JUDGE_RULES[@]}"; do recipe_event recipe_step "$((i + 1)) $(jq -r ".[$i].part" <<<"$plan")" "${JUDGE_RULES[$i]} / 未実行" "E-$RUN_EID" >/dev/null; done
    _rr_end "$JUDGE_RC" "${JUDGE_STOP}。どの部品も実行していません"; return
  fi
  # pane の外で行う部品があれば、検査済みの並びだけを待ち行列に置く(外の実行役 tb recipe-runner が実行の時点で判定し直してから実行する)
  # 人の端末からの依頼は pane の外なので、その場で実行する(待ち行列に人の権限の依頼を置かない。実行役は人の依頼を受け付けない)
  if ((JUDGE_OUTSIDE)) && [[ $REQ_BY != human ]]; then
    local rid
    rid=$(sql "INSERT INTO main.recipe_runs (herdr_session, name, body_sha, target, requester, via, task_id, plan, event_id, status, created_at)
      VALUES ($SQS, $(q "$name"), $(q "$(printf '%s' "$body" | sha256)"), $(q "$RUN_TARGET"), $(q "$REQ_BY"), $([[ -n $RUN_VIA ]] && q "$RUN_VIA" || echo NULL),
              $(q "$RUN_TASK"), $(q "$plan"), $RUN_EID, 'queued', $(q "$(now)"));
      SELECT last_insert_rowid();")
    recipe_event recipe_step "0 queue" "R-$rid queued(pane の外の実行役が実行します)" "E-$RUN_EID" >/dev/null
    echo "R-$rid queued (外の実行役 tb recipe-runner が実行します。状態は tb recipe-queue)"
    return 0
  fi
  recipe_exec "$plan" "recipe:E-$RUN_EID" || { _rr_end 1 "$EXEC_STOP"; return; }
  _rr_end 0 ""
}

# ---------- 外の実行役(段 3) ----------
# tb recipe-runner [--max N]: 待ち行列(recipe_runs)の queued を古い順に取り出し、依頼者の権限で部品ごとに判定し直してから、並びどおりに実行する。
# 人の端末(pane なし)からだけ。定期ジョブ(人が登録したコマンド型。例 scheduler/jobs/recipe-runner.conf.example)が動かす短命のプロセスで、常駐しない。
# 並びの外のことはしない。レシピの本文が変わった・消えた、置いてから TB_RECIPE_QUEUE_TTL_SEC(既定 900)秒を過ぎた、判定が allow でない、のどれでも実行しない。
# 出力: 1 件ごとに R-<n> <状態>。終了コード 0(1 件ずつの失敗は状態と記録に残す)
RECIPE_QUEUE_TTL=${TB_RECIPE_QUEUE_TTL_SEC:-900}
RECIPE_RUNNING_MAX=${TB_RECIPE_RUNNING_MAX_SEC:-3600}
cmd_recipe-runner() {
  local max=1 n=0 id row cur
  while [[ $# -gt 0 ]]; do
    case $1 in
      --max) need_arg "$@"; [[ $2 =~ ^[0-9]{1,3}$ ]] && ((10#$2 >= 1)) || usage "recipe-runner: --max は 1〜999: $2"; max=$((10#$2)); shift 2;;
      *) usage "recipe-runner: 不明な引数 $1";;
    esac
  done
  require_human "tb recipe-runner"
  [[ $RECIPE_QUEUE_TTL =~ ^[0-9]{1,6}$ ]] || die "TB_RECIPE_QUEUE_TTL_SEC は秒の整数: $RECIPE_QUEUE_TTL"
  [[ $RECIPE_RUNNING_MAX =~ ^[0-9]{1,6}$ ]] || die "TB_RECIPE_RUNNING_MAX_SEC は秒の整数: $RECIPE_RUNNING_MAX"
  # 古すぎるものは実行しない(依頼した時点の状況と離れている)
  while IFS= read -r id; do
    [[ -n $id ]] || continue
    _runner_close "$id" expired "置いてから ${RECIPE_QUEUE_TTL} 秒を過ぎたため実行しません" queued
  done < <(sql "SELECT id FROM recipe_runs WHERE status='queued' AND (julianday('now') - julianday(created_at)) * 86400 > $((10#$RECIPE_QUEUE_TTL)) ORDER BY id;")
  # 実行役が途中で落ちた(running のまま長く残った)ものは failed にする。どこまで済んだかは recipe_step の記録で見る。巻き戻さない
  while IFS= read -r id; do
    [[ -n $id ]] || continue
    _runner_close "$id" failed "実行役が ${RECIPE_RUNNING_MAX} 秒たっても終わっていません(途中で止まった可能性。済んだ step は記録を見てください)" running
  done < <(sql "SELECT id FROM recipe_runs WHERE status='running' AND (julianday('now') - julianday(started_at)) * 86400 > $((10#$RECIPE_RUNNING_MAX)) ORDER BY id;")
  while ((n < max)); do
    id=$(sql "SELECT id FROM recipe_runs WHERE status='queued' ORDER BY id LIMIT 1;")
    [[ -n $id ]] || break
    # 取り出し(同時に 2 つの実行役が動いても、1 件を実行するのは 1 つだけ)
    [[ $(sql "UPDATE main.recipe_runs SET status='running', started_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id AND status='queued'; SELECT changes();") == 1 ]] || continue
    n=$((n + 1))
    row=$(session_row recipe_runs id "$id")
    _runner_one "$row"
    cur=$(sql "SELECT status FROM recipe_runs WHERE id=$id;")
    echo "R-$id $cur"
  done
  return 0
}
_runner_close() {  # $1=id $2=状態 $3=理由 $4=元の状態 [$5=記録の actor(既定は依頼者)] → recipe_end を残す
  local row eid actor
  row=$(session_row recipe_runs id "$1"); [[ -n $row ]] || return 0
  [[ $(sql "UPDATE main.recipe_runs SET status=$(q "$2"), result=$(q "$3"), finished_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$1 AND status=$(q "$4"); SELECT changes();") == 1 ]] || return 0
  eid=$(jq -r .event_id <<<"$row")
  REQ_BY=${5:-$(jq -r .requester <<<"$row")}; RUN_TASK=$(jq -r .task_id <<<"$row")
  sql "INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
    VALUES ($SQS, $(q "$RUN_TASK"), 'recipe_end', $(q "R-$1"), $(q "$([[ $2 == done ]] && echo done || echo "$2: $3")"), $(q "$REQ_BY"), $(q "E-$eid"), $(q "$(now)"));" >/dev/null
}
_runner_one() {  # $1=recipe_runs の行(JSON)
  local row=$1 id name plan body cur_sha
  id=$(jq -r .id <<<"$row"); name=$(jq -r .name <<<"$row"); plan=$(jq -c '.plan | fromjson' <<<"$row")
  REQ_BY=$(jq -r .requester <<<"$row"); RUN_TARGET=$(jq -r .target <<<"$row"); RUN_TASK=$(jq -r .task_id <<<"$row"); RUN_EID=$(jq -r .event_id <<<"$row")
  # 依頼者は記録された agent。人の依頼は待ち行列に置かない(recipe-run がその場で実行する)ので、human の行は実行しない(fail-closed)
  part_value_ok agent "$REQ_BY" || { _runner_close "$id" stopped "依頼者が agent 名ではありません(人の依頼は待ち行列では実行しない)" running; return; }
  part_value_ok agent "$RUN_TARGET" || { _runner_close "$id" stopped "対象が agent 名ではありません" running; return; }
  # レシピがまだ登録されていて、判定したときと同じ本文か
  body=$(sql "SELECT body FROM recipes WHERE name=$(q "$name");")
  [[ -n $body ]] || { _runner_close "$id" stopped "レシピ $name の登録が消えたため実行しません" running; return; }
  cur_sha=$(printf '%s' "$body" | sha256)
  [[ $cur_sha == "$(jq -r .body_sha <<<"$row")" ]] || { _runner_close "$id" stopped "レシピ $name の本文が変わったため実行しません(もう一度頼みます)" running; return; }
  recipe_parse "$body" || { _runner_close "$id" stopped "レシピ $name は今のコードでは検査を通りません" running; return; }
  # 実行の時点で判定し直す(レシピの操作名と、部品ごと)。設定が変わっていれば、それに従う
  approval_of "recipe:$name:$RUN_TARGET" "$REQ_BY"
  [[ $APPROVAL_VALUE == allow ]] || { _runner_close "$id" stopped "recipe:$name:$RUN_TARGET が実行の時点で allow ではありません($APPROVAL_RULE)" running; return; }
  recipe_judge "$plan" >/dev/null
  [[ -z $JUDGE_STOP ]] || { _runner_close "$id" stopped "実行の時点の判定で止めました: ${JUDGE_STOP#step }" running; return; }
  if recipe_exec "$plan" "recipe:R-$id" >/dev/null; then _runner_close "$id" done "" running
  else _runner_close "$id" failed "$EXEC_STOP" running; fi
}

# tb recipe-queue [--all] [--json]: 待ち行列(既定は queued と running。--all は全部)。R-<n> 状態 レシピ 対象 依頼者 置いた時刻 結果
cmd_recipe-queue() {
  local all=0 json=0 where="status IN ('queued','running')"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --all) all=1; shift;;
      --json) json=1; shift;;
      *) usage "recipe-queue: 不明な引数 $1";;
    esac
  done
  ((all == 0)) || where="1=1"
  if ((json)); then sql -json "SELECT 'R-'||id AS id, status, name, target, requester, via, task_id, plan, 'E-'||event_id AS event, created_at, started_at, finished_at, result FROM recipe_runs WHERE $where ORDER BY recipe_runs.id;" | jq -c '(. // [])[] | .plan |= fromjson'
  else sql -separator $'\t' "SELECT 'R-'||id, status, name, target, requester, created_at, COALESCE(replace(result, char(10), ' '),'-') FROM recipe_runs WHERE $where ORDER BY recipe_runs.id;"; fi
}

# tb recipe-cancel R-<n>: 待ち行列の queued を取り消す。人と、頼んだ agent 自身だけ
cmd_recipe-cancel() {
  [[ $# -eq 1 && $1 =~ ^R-([0-9]{1,18})$ ]] || usage "recipe-cancel R-<n>"
  local id=${BASH_REMATCH[1]} row by
  row=$(session_row recipe_runs id "$id"); [[ -n $row ]] || die "実行の依頼が見つかりません: R-$id"
  requester; by=$REQ_BY
  [[ $by == human || $by == "$(jq -r .requester <<<"$row")" ]] || die "R-$id を取り消せるのは人と、頼んだ agent($(jq -r .requester <<<"$row"))だけです"
  [[ $(jq -r .status <<<"$row") == queued ]] || die "R-$id は待ち行列にありません($(jq -r .status <<<"$row"))"
  _runner_close "$id" cancelled "$([[ $by == human ]] && echo 人 || echo "$by")が取り消し" queued "$by"
  [[ $(sql "SELECT status FROM recipe_runs WHERE id=$id;") == cancelled ]] || die "R-$id は待ち行列にありません(ほかで決まりました)"
  REQ_BY=$by
  echo "R-$id cancelled"
}
