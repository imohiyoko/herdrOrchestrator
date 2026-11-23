# shellcheck shell=bash
# run.sh から番号順に source される。権限カタログ(config/approvals-catalog.conf)と、コード・既定の approvals.conf のずれを防ぐ。
# 決定論(静的な grep と glob 照合だけ。tb は実行しない)。
# 検査は _cat_problems <カタログのファイル> にまとめ、問題を 1 行 1 件で出す(空 = 問題なし)。
# 最後に、カタログの行を 1 件ずつ消して、必ずどれかの検査が落ちること(抜けを検出できること)を確かめる。
CAT=$HERE/../config/approvals-catalog.conf
TBROOT=$HERE/..
REPO=$HERE/../..

# 空白を落とす(bash だけで)
_ct() { local s=$1; s="${s#"${s%%[![:space:]]*}"}"; printf '%s' "${s%"${s##*[![:space:]]}"}"; }

# 期待する操作名(動的に組み立てられる部分の一覧。コードの定義から読めるものは読む)
EXP_AGENT_OPS="new stop revive spawn compact model slash"   # run_op の case と spawn / compact / model-set / slash
EXP_SETUP_STEPS="hooks scheduler webui launchctl"       # setup/CLAUDE.md の手順
EXP_GATE_ACTIONS="merge approve execute"                # gate.sh の --action と execute
SEND_KINDS_CODE=$(sed -n 's/^SEND_KINDS="\(.*\)"$/\1/p' "$TBROOT/lib/send.sh")
EFFECTS_CODE=$(sed -n 's/^EFFECTS="\(.*\)"$/\1/p' "$TBROOT/lib/proxy.sh")

# コードが承認判定に渡す操作名(動的な部分は *)
_code_ops() {
  { grep -hoE '(approval_of|set_approval (allow|ask)) +"[^"]+"' "$TBROOT"/lib/*.sh "$TBROOT"/bin/tb | sed -E 's/^[^"]*"//; s/"$//'
    grep -hoE 'tb approval (check )?[A-Za-z0-9:<>*._-]+' "$REPO"/roles/*.md "$REPO"/setup/examples/roles/*.md "$REPO"/setup/CLAUDE.md | sed -E 's/^tb approval (check )?//; s/<[^>]*>/*/g' \
      | grep -vxE 'list|approve|reject|allow|ask|help'   # tb approval のサブコマンドは操作名ではない
  } | sed -E 's/\$\{[^}]*\}|\$[A-Za-z0-9_]+/*/g' | grep -E '^[a-z][A-Za-z0-9._:*?-]*$' | sort -u   # 「tb approval <操作>」のような説明文の記号は除く
}
CODE_OPS=$(_code_ops)

# カタログの検査。$1=ファイル。問題を 1 行 1 件で出す
_cat_problems() {
  local f=$1 line t f_pat f_kind f_side f_risk f_allow f_state f_desc why seen=" " p op n=0 i lit hit
  local -a pats=() states=()
  while IFS= read -r line || [[ -n $line ]]; do
    t=$(_ct "$line"); [[ -z $t || $t == \#* ]] && continue
    n=$((n+1))
    IFS='|' read -r f_pat f_kind f_side f_risk f_allow f_state f_desc <<<"$line"
    f_pat=$(_ct "$f_pat"); f_kind=$(_ct "$f_kind"); f_side=$(_ct "$f_side"); f_risk=$(_ct "$f_risk")
    f_allow=$(_ct "$f_allow"); f_state=$(_ct "$f_state"); f_desc=$(_ct "$f_desc")
    why=""
    [[ $f_pat =~ ^[A-Za-z0-9._:*?-]{1,128}$ ]] || why="パターンの文字集合"
    case $f_kind in send|agent|alert|proxy|role|gate|setup|recipe) ;; *) why="種別";; esac
    [[ ${f_pat%%:*} == "$f_kind" ]] || why="種別がパターンの先頭と違う"
    case $f_side in none|local-write|external-send|external-write) ;; *) why="副作用";; esac
    case $f_risk in low|mid|high|human-only) ;; *) why="リスク階層";; esac
    case $f_allow in yes|no|conditional) ;; *) why="allow 候補";; esac
    case $f_state in active|unwired|reserved) ;; *) why="状態";; esac
    [[ -n $f_desc ]] || why="説明が空"
    [[ $f_risk != human-only || $f_allow == no ]] || why="human-only なのに allow 候補が no でない"
    # conditional は、操作名に宛先・対象(3 つ目の部分)があるものだけ
    [[ $f_allow != conditional || $f_pat == *:*:* ]] || why="宛先・対象の無い操作名が conditional"
    [[ $seen != *" $f_pat "* ]] || why="パターンが重複"
    seen+="$f_pat "
    [[ -z $why ]] || echo "形式: $why: $f_pat"
    pats+=("$f_pat"); states+=("$f_state")
  done <"$f"
  ((n >= 15)) || echo "行が少なすぎる($n)"

  # 期待する操作名(具体的な 1 例)が、状態つきでカタログに載っている
  _need() {  # $1=操作の例 $2=期待する状態
    local j
    for j in "${!pats[@]}"; do
      # shellcheck disable=SC2053
      if [[ $1 == ${pats[$j]} ]]; then [[ ${states[$j]} == "$2" ]] || echo "状態が違う: $1(${states[$j]}、期待 $2)"; return 0; fi
    done
    echo "カタログに無い: $1"
  }
  for op in $EXP_AGENT_OPS; do _need "agent:$op:example" active; done
  for op in $EXP_SETUP_STEPS; do _need "setup:$op" active; done
  for op in $SEND_KINDS_CODE; do _need "send:$op:example" active; done
  for op in $EFFECTS_CODE; do _need "proxy:$op" unwired; done
  for op in $EXP_GATE_ACTIONS; do _need "gate:$op" reserved; done
  _need "role:change" active
  _need "alert:analyze" active
  _need "recipe:example:example" active

  # (a) コードが承認判定に渡す操作名が、カタログのどれかと重なる(双方向の glob)
  while IFS= read -r op; do
    [[ -n $op ]] || continue
    hit=0
    for p in "${pats[@]}"; do
      # shellcheck disable=SC2053
      if [[ $p == $op || $op == $p ]]; then hit=1; break; fi
    done
    ((hit)) || echo "コードの操作名がカタログに無い: $op"
  done <<<"$CODE_OPS"

  # 逆向き: カタログの項目がコードに実在する
  for i in "${!pats[@]}"; do
    p=${pats[$i]}
    case ${states[$i]} in
      active)
        if [[ $p == send:*:* ]]; then   # send:<kind>:* の kind は SEND_KINDS にある
          op=${p#send:}; op=${op%%:*}
          [[ " $SEND_KINDS_CODE " == *" $op "* ]] || echo "実在しない(send の kind): $p"
        else
          lit=${p%:\*}   # agent:new:* → agent:new
          grep -rqF -- "$lit" "$TBROOT"/lib "$TBROOT"/bin "$REPO"/roles "$REPO"/setup/examples "$REPO"/setup/CLAUDE.md || echo "実在しない(lib・roles・setup に出てこない): $p"
        fi;;
      unwired)   # 承認判定に渡っていないこと(渡るようになったら active にする)
        while IFS= read -r op; do
          [[ -n $op ]] || continue
          # shellcheck disable=SC2053
          if [[ $p == $op || $op == $p ]]; then echo "承認判定に渡っている(状態を active に): $p ← $op"; fi
        done <<<"$CODE_OPS"
        if [[ $p == proxy:* ]]; then [[ " $EFFECTS_CODE " == *" ${p#proxy:} "* ]] || echo "実在しない(proxy の effect): $p"; fi;;
      reserved)
        if [[ $p == gate:* ]]; then grep -qF -- "${p#gate:}" "$TBROOT/lib/gate.sh" || echo "実在しない(gate の操作): $p"; fi;;
    esac
  done
}

# --- カタログ本体の検査
problems=$(_cat_problems "$CAT")
eq "catalog: 形式・期待する操作名・コードとの対応(双方向)に問題が無い" "$problems" ""
[[ -z $problems ]] || printf '   %s\n' "$problems"
eq "catalog: コードから承認判定の操作名を集められている(grep が壊れていない)" "$(($(printf '%s\n' "$CODE_OPS" | grep -c .) >= 8))" "1"
for must in 'alert:analyze' 'agent:compact:*' 'agent:model:*' 'agent:spawn:*' 'role:change' 'setup:*'; do
  eq "catalog: 抽出に $must が含まれる" "$(printf '%s\n' "$CODE_OPS" | grep -cxF -- "$must")" "1"
done

# --- 変異テスト: カタログの行を 1 件ずつ消すと、必ず問題が出る(消した項目の抜けを検出できる)
tmpc=$(mktemp); undetected=""; nrec=0
while IFS= read -r l; do
  nrec=$((nrec+1))
  grep -vxF -- "$l" "$CAT" >"$tmpc"
  [[ -n $(_cat_problems "$tmpc") ]] || undetected+="$(_ct "${l%%|*}") "
done < <(grep -vE '^[[:space:]]*(#|$)' "$CAT")
rm -f "$tmpc"
eq "catalog: 行を 1 件消すと必ず検出される(変異テスト $nrec 件)" "${undetected:-none}" "none"
eq "catalog: 変異テストが全行を回している" "$((nrec >= 20))" "1"

# --- (c) 既定の approvals.conf の規則がカタログに載っている(未知の規則)
CAT_PATS=()
while IFS= read -r l; do CAT_PATS+=("$(_ct "${l%%|*}")"); done < <(grep -vE '^[[:space:]]*(#|$)' "$CAT")
_cat_covers() {  # $1=規則のパターン。カタログのどれかと重なるか
  local p
  for p in "${CAT_PATS[@]}"; do
    # shellcheck disable=SC2053
    if [[ $p == $1 || $1 == $p ]]; then return 0; fi
  done
  return 1
}
_conf_unknown() {  # stdin=conf の本文 → 未知の規則を空白区切りで出す
  local ln k v out=""
  while IFS= read -r ln || [[ -n $ln ]]; do
    ln=$(_ct "${ln%%#*}"); [[ $ln == *=* ]] || continue
    k=$(_ct "${ln%%=*}"); v=$(_ct "${ln#*=}")
    [[ $v == ask || $v == allow ]] || continue
    [[ $k == '*' ]] && continue   # 末尾の既定
    _cat_covers "$k" || out+="$k "
  done
  printf '%s' "$out"
}
# 既定の表(perms/ops.tbl)の操作がカタログに載っている。agent:flag と approve:* は既定 deny で、policy の allow の行で明示するもの(カタログの外)
ex=""
while read -r k; do _cat_covers "$k" || _cat_covers "$k:x" || ex+="$k "; done < <(sed 's/#.*//' "$TBROOT/perms/ops.tbl" | awk 'NF == 2 && $1 != "*" && $1 != "agent:flag" && $1 != "approve:*" {print $1}')
eq "catalog: 既定の表(perms/ops.tbl)の操作がすべてカタログに載っている" "$ex" ""
# 手元の approvals.conf(取り込み前に人が書いたもの)は、未知の規則を警告するだけ(テストは落とさない)
w=""; [[ ! -f $TBROOT/config/approvals.conf ]] || w=$(_conf_unknown <"$TBROOT/config/approvals.conf")
[[ -z $w ]] && ok "catalog: 手元の approvals.conf に未知の規則は無い" || echo "warn catalog: 手元の approvals.conf にカタログに無い規則: $w"
eq "catalog: 未知の規則の検出が働く(自己テスト)" "$(printf 'zzz:unknown = allow\nsend:task:dd-* = allow\n* = ask\n' | _conf_unknown)" "zzz:unknown "
