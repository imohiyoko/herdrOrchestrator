# shellcheck shell=bash
# リスク評価(第 1 段: 評価を出すだけ。allow / ask の最終判定には使わない)
# approvals.sh から source される。単体でも source できる(テスト用。依存は bash だけ。sql などは risk_collect だけが使う)。
# 設計: docs/risk-based-approvals.md。決定論(時刻・乱数・LLM を使わない)。理由は寄与を 1 件ずつ列挙する。
#
# 入力(呼ぶ側が代入する。未設定 = 取れなかった = 評価不能 R-07 で ask。空文字 = 取れたが不明 = [unknown] の設定で決まる):
#   RISK_IN_SENDER_ROLE RISK_IN_TARGET_ROLE RISK_IN_TARGET_STATE RISK_IN_SENDER_MODEL RISK_IN_TEXT  (RISK_IN_KIND は任意)
# 出力: RISK_VALUE(allow|ask)  RISK_SCORE  RISK_THRESHOLD  RISK_LINES(寄与 1 件 1 行の配列)  RISK_WARNS(警告の配列)
#
# 規則ファイルの場所は固定。env では差し替えられない(agent が自分の許可ファイルで評価させる迂回を作らない)。
# テストは、この lib を source したシェルの中で RISK_RULES を代入して差し替える(tb 本体は別プロセスなので env は効かない)。
RISK_RULES="${TB_ROOT:-.}/config/risk-rules.conf"

RISK_DEFAULT_ASK_AT=2
RISK_DEFAULT_WORDS='push|merge|--force|--approve|reset --hard|rm -rf'

_risk_trim() { local s=$1; s="${s#"${s%%[![:space:]]*}"}"; printf '%s' "${s%"${s##*[![:space:]]}"}"; }

# 値の強さ: ask=999 > risk:N=N > allow=0。ファイルの値が既定より弱ければ「緩め」
_risk_level() {
  case $1 in
    ask) echo 999;; allow) echo 0;;
    risk:*) echo "${1#risk:}";;
  esac
}
_risk_valid_value() { [[ $1 == ask || $1 == allow || $1 =~ ^risk:[0-9]+$ ]]; }

# 設定の読み込み。成功で 0。失敗は RISK_ERR に理由を入れて 1(呼ぶ側は評価不能として ask にする)
RISK_ERR=""
_risk_load() {
  RISK_ERR=""; RISK_WARNS=()
  RISK_ASK_AT=$RISK_DEFAULT_ASK_AT
  RISK_U_sender_role=ask; RISK_U_target_role=ask; RISK_U_target_state=ask; RISK_U_sender_model=risk:1
  RISK_PAT_NAMES=" destructive "; RISK_PAT_destructive=$RISK_DEFAULT_WORDS
  RISK_R_ID=(R-02 R-06)
  RISK_R_COND=("body.matches(destructive)" "target.state in (stopped)")
  RISK_R_RES=(ask risk:2)
  RISK_R_WHY=("本文に破壊的・公開系の操作語" "宛先が停止中")
  local f=$RISK_RULES
  [[ -e $f ]] || return 0                       # ファイルなし: コード内の既定だけで動く(既定は緩くない)
  [[ -f $f && -r $f ]] || { RISK_ERR="規則ファイルを読めません"; return 1; }
  local sec="" line n=0 k v key id cond res why lv dv file_ask_at="" attr
  local -a seen=() u_attrs=() u_vals=()
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n+1)); line=$(_risk_trim "$line")
    [[ -z $line || $line == \#* ]] && continue
    if [[ $line =~ ^\[([a-z]+)\]$ ]]; then
      sec=${BASH_REMATCH[1]}
      [[ $sec == threshold || $sec == unknown || $sec == patterns || $sec == rules ]] || { RISK_ERR="L$n: 未知の節 [$sec]"; return 1; }
      continue
    fi
    case $sec in
      threshold)
        [[ $line == *=* ]] || { RISK_ERR="L$n: 'キー = 値' の形ではありません"; return 1; }
        k=$(_risk_trim "${line%%=*}"); v=$(_risk_trim "${line#*=}")
        [[ $k == ask-at ]] || { RISK_ERR="L$n: [threshold] で使えるのは ask-at だけです"; return 1; }
        [[ $v =~ ^[0-9]+$ && $v -ge 1 && $v -le 99 ]] || { RISK_ERR="L$n: ask-at は 1 以上の整数です"; return 1; }
        file_ask_at=$v;;
      unknown)
        [[ $line == *=* ]] || { RISK_ERR="L$n: 'キー = 値' の形ではありません"; return 1; }
        k=$(_risk_trim "${line%%=*}"); v=$(_risk_trim "${line#*=}")
        case $k in sender.role|target.role|target.state|sender.model) ;; *) RISK_ERR="L$n: [unknown] に未知の属性 $k"; return 1;; esac
        _risk_valid_value "$v" || { RISK_ERR="L$n: 値は ask | allow | risk:<整数> です: $v"; return 1; }
        u_attrs+=("$k"); u_vals+=("$v");;
      patterns)
        [[ $line == *=* ]] || { RISK_ERR="L$n: '名前 = 正規表現' の形ではありません"; return 1; }
        k=$(_risk_trim "${line%%=*}"); v=$(_risk_trim "${line#*=}")
        [[ $k =~ ^[a-z][a-z0-9-]*$ ]] || { RISK_ERR="L$n: パターン名は小文字英数字と - です: $k"; return 1; }
        [[ -n $v ]] || { RISK_ERR="L$n: 空のパターンです"; return 1; }
        _risk_ere_ok "$v" || { RISK_ERR="L$n: 正規表現として不正、または空文字列に一致します($k)"; return 1; }
        key=${k//-/_}
        if [[ $RISK_PAT_NAMES == *" $k "* ]]; then   # 既存の名前には「足す」だけ(語を減らす=緩めることはできない)
          eval "RISK_PAT_$key=\"\$RISK_PAT_$key|\$v\""
        else
          RISK_PAT_NAMES+="$k "; eval "RISK_PAT_$key=\$v"
        fi;;
      rules)
        IFS='|' read -r id cond res why <<<"$line"
        id=$(_risk_trim "$id"); cond=$(_risk_trim "$cond"); res=$(_risk_trim "$res"); why=$(_risk_trim "${why:-}")
        [[ $id =~ ^R-[0-9]+$ ]] || { RISK_ERR="L$n: 規則 ID は R-<数字> です: $id"; return 1; }
        [[ $id != R-02 && $id != R-06 && $id != R-07 ]] || { RISK_ERR="L$n: $id は組み込みの規則で、ファイルでは定義できません"; return 1; }
        [[ " ${seen[*]:-} " != *" $id "* ]] || { RISK_ERR="L$n: 規則 ID が重複しています: $id"; return 1; }
        seen+=("$id")
        [[ -n $cond && -n $why ]] || { RISK_ERR="L$n: '<ID> | <条件> | <ask|risk:N> | <理由>' の 4 欄が要ります"; return 1; }
        [[ $res == ask || $res =~ ^risk:[0-9]+$ ]] || { RISK_ERR="L$n: 結果は ask か risk:<整数> です: $res"; return 1; }
        RISK_R_ID+=("$id"); RISK_R_COND+=("$cond"); RISK_R_RES+=("$res"); RISK_R_WHY+=("$why");;
      *) RISK_ERR="L$n: 節([threshold] など)の前に行があります"; return 1;;
    esac
  done <"$f"
  # しきい値: 下げる(厳しくする)のは有効。上げる(緩める)のは第 3 段まで無効で警告だけ
  if [[ -n $file_ask_at ]]; then
    if ((file_ask_at < RISK_ASK_AT)); then RISK_ASK_AT=$file_ask_at
    elif ((file_ask_at > RISK_ASK_AT)); then RISK_WARNS+=("緩め設定は第 3 段まで無効: [threshold] ask-at = $file_ask_at(既定 $RISK_ASK_AT のまま)"); fi
  fi
  local i
  for ((i = 0; i < ${#u_attrs[@]}; i++)); do
    attr=${u_attrs[i]}; key=RISK_U_${attr//./_}; v=${u_vals[i]}
    lv=$(_risk_level "$v"); dv=$(_risk_level "${!key}")
    if ((lv < dv)); then RISK_WARNS+=("緩め設定は第 3 段まで無効: [unknown] $attr = $v(既定 ${!key} のまま)")
    else printf -v "$key" '%s' "$v"; fi
  done
  # 点は 0..しきい値(規則ファイルの規則だけ。組み込みは固定)
  for ((i = 2; i < ${#RISK_R_ID[@]}; i++)); do
    res=${RISK_R_RES[i]}
    if [[ $res == risk:* && ${res#risk:} -gt $RISK_ASK_AT ]]; then RISK_ERR="${RISK_R_ID[i]}: 点がしきい値($RISK_ASK_AT)を超えています"; return 1; fi
    _risk_cond_ok "${RISK_R_COND[i]}" || { RISK_ERR="${RISK_R_ID[i]}: 条件を解釈できません: ${RISK_R_COND[i]}"; return 1; }
  done
  _risk_u_points_ok || return 1
  return 0
}

# [unknown] の risk:N も範囲 0..しきい値
_risk_u_points_ok() {
  local a v
  for a in sender_role target_role target_state sender_model; do
    v=RISK_U_$a; v=${!v}
    if [[ $v == risk:* && ${v#risk:} -gt $RISK_ASK_AT ]]; then RISK_ERR="[unknown] $a の点がしきい値($RISK_ASK_AT)を超えています"; return 1; fi
  done
}

# 正規表現として正しく、空文字列に一致しないこと(一致すると全件に当たる)
_risk_ere_ok() {
  local rc=0 re=$1
  [[ "x" =~ $re ]] 2>/dev/null; rc=$?; [[ $rc -ne 2 ]] || return 1
  [[ "" =~ $re ]] && return 1
  return 0
}

# 条件の文法検査(評価はしない)。名前付きパターンの存在も確かめる
_risk_cond_ok() {
  local cond=$1 t name
  local -a terms
  IFS=$'\x01' read -r -a terms <<<"${cond// and /$'\x01'}"
  ((${#terms[@]})) || return 1
  for t in "${terms[@]}"; do
    t=$(_risk_trim "$t")
    if [[ $t =~ ^body\.matches\(([a-z][a-z0-9-]*)\)$ ]]; then
      name=${BASH_REMATCH[1]}; [[ $RISK_PAT_NAMES == *" $name "* ]] || return 1
    elif [[ $t == body.secret ]]; then :
    elif [[ $t =~ ^(sender\.role|target\.role|target\.state|kind)\ +in\ +\(([A-Za-z0-9._,\ -]+)\)$ ]]; then :
    else return 1; fi
  done
}

# 条件 1 つ(複数は and)の評価。0=一致。RISK_HIT に補足(語・種類名)
_risk_cond_match() {
  local LC_ALL=C   # 語境界は ASCII 限定。locale に依存させない(日本語などに隣接した語も検出する)
  local cond=$1 t name attr list v w re had=0 hit=""
  local wc=_0123456789abcdefghijklm"nopqrstuvwxyz"ABCDEFGHIJKLM"NOPQRSTUVWXYZ"   # 範囲指定(a-z)は locale で変わりうるので列挙する
  local -a terms
  RISK_HIT=""
  IFS=$'\x01' read -r -a terms <<<"${cond// and /$'\x01'}"
  for t in "${terms[@]}"; do
    t=$(_risk_trim "$t")
    if [[ $t =~ ^body\.matches\(([a-z][a-z0-9-]*)\)$ ]]; then
      name=${BASH_REMATCH[1]}; v=RISK_PAT_${name//-/_}
      re="(^|[^$wc])(${!v})([^$wc]|\$)"
      shopt -q nocasematch && had=1
      shopt -s nocasematch
      if [[ $RISK_IN_TEXT =~ $re ]]; then w=${BASH_REMATCH[2]}; else w=""; fi
      ((had)) || shopt -u nocasematch
      [[ -n $w ]] || return 1
      hit+="${hit:+, }語: $w"
    elif [[ $t == body.secret ]]; then
      declare -F notify_secret_scan >/dev/null || return 1
      notify_secret_scan "$RISK_IN_TEXT" || return 1
      hit+="${hit:+, }種類: $NOTIFY_HIT_KINDS"
    elif [[ $t =~ ^(sender\.role|target\.role|target\.state|kind)\ +in\ +\(([A-Za-z0-9._,\ -]+)\)$ ]]; then
      attr=${BASH_REMATCH[1]}; list=${BASH_REMATCH[2]// /}
      case $attr in
        sender.role) v=${RISK_IN_SENDER_ROLE:-};; target.role) v=${RISK_IN_TARGET_ROLE:-};;
        target.state) v=${RISK_IN_TARGET_STATE:-};; kind) v=${RISK_IN_KIND:-};;
      esac
      [[ -n $v && ",$list," == *",$v,"* ]] || return 1   # 不明な属性を参照する条件は「一致しない」(不明は [unknown] が 1 回だけ数える)
    else return 1; fi
  done
  RISK_HIT=$hit
}

# 評価。常に 0 で返り、結果は RISK_* に入る(入力欠損・設定の壊れは評価不能 R-07 として ask)
risk_eval() {
  RISK_VALUE=ask; RISK_SCORE=0; RISK_THRESHOLD=$RISK_DEFAULT_ASK_AT; RISK_LINES=(); RISK_WARNS=()
  local missing="" ask=0 a v pts i
  [[ -n ${RISK_IN_SENDER_ROLE+x} ]] || missing+=" sender.role"
  [[ -n ${RISK_IN_TARGET_ROLE+x} ]] || missing+=" target.role"
  [[ -n ${RISK_IN_TARGET_STATE+x} ]] || missing+=" target.state"
  [[ -n ${RISK_IN_SENDER_MODEL+x} ]] || missing+=" sender.model"
  [[ -n ${RISK_IN_TEXT+x} ]] || missing+=" body"
  if [[ -n $missing ]]; then RISK_LINES+=("R-07 +ask 評価不能(fail-closed): 入力が取れません:${missing}"); return 0; fi
  if ! _risk_load; then
    RISK_LINES+=("R-07 +ask 評価不能(fail-closed): ${RISK_ERR}"); return 0
  fi
  RISK_THRESHOLD=$RISK_ASK_AT
  # 1) 不明な属性([unknown]。0 点でも寄与の行を出す)
  for a in sender.role target.role target.state sender.model; do
    case $a in
      sender.role) v=$RISK_IN_SENDER_ROLE;; target.role) v=$RISK_IN_TARGET_ROLE;;
      target.state) v=$RISK_IN_TARGET_STATE;; sender.model) v=$RISK_IN_SENDER_MODEL;;
    esac
    [[ -z $v ]] || continue
    v=RISK_U_${a//./_}; v=${!v}
    case $v in
      ask) ask=1; RISK_LINES+=("unknown:$a +ask $a が不明(設定 ask)");;
      allow) RISK_LINES+=("unknown:$a +0 $a が不明(設定 allow)");;
      risk:*) pts=${v#risk:}; RISK_SCORE=$((RISK_SCORE + pts)); RISK_LINES+=("unknown:$a +$pts $a が不明(設定 $v)");;
    esac
  done
  # 2) 規則(組み込みの R-02 R-06、続いてファイルの規則を上から)
  for ((i = 0; i < ${#RISK_R_ID[@]}; i++)); do
    _risk_cond_match "${RISK_R_COND[i]}" || continue
    v=${RISK_R_RES[i]}; w=${RISK_HIT:+(${RISK_HIT})}
    if [[ $v == ask ]]; then ask=1; RISK_LINES+=("${RISK_R_ID[i]} +ask ${RISK_R_WHY[i]}${w}")
    else pts=${v#risk:}; RISK_SCORE=$((RISK_SCORE + pts)); RISK_LINES+=("${RISK_R_ID[i]} +$pts ${RISK_R_WHY[i]}${w}"); fi
  done
  if ((ask || RISK_SCORE >= RISK_THRESHOLD)); then RISK_VALUE=ask; else RISK_VALUE=allow; fi
  return 0
}

# 入力の収集。risk_collect <送信者名 | 空=人 | ?=特定できない> <宛先> <kind> <本文>
# 取れなかった入力は未設定のまま(= R-07)。取れたが空なら空(= [unknown])
risk_collect() {
  local sender=$1 target=$2 kind=$3 text=$4 r
  unset RISK_IN_SENDER_ROLE RISK_IN_TARGET_ROLE RISK_IN_TARGET_STATE RISK_IN_SENDER_MODEL RISK_IN_TEXT RISK_IN_KIND
  RISK_IN_TEXT=$text; RISK_IN_KIND=$kind
  if [[ -z $sender ]]; then RISK_IN_SENDER_ROLE=human; RISK_IN_SENDER_MODEL=human
  elif [[ $sender != "?" ]]; then
    r=$(role_file_for "$sender" 2>/dev/null) && RISK_IN_SENDER_ROLE=$(basename "${r:-}" .md)
    RISK_IN_SENDER_MODEL=$(sql "SELECT COALESCE(model,'') FROM agents WHERE herdr_session=$SQS AND name=$(q "$sender");" 2>/dev/null) || unset RISK_IN_SENDER_MODEL
  fi
  r=$(role_file_for "$target" 2>/dev/null) && RISK_IN_TARGET_ROLE=$(basename "${r:-}" .md)
  RISK_IN_TARGET_STATE=$(sql "SELECT COALESCE(state,'') FROM agents WHERE herdr_session=$SQS AND name=$(q "$target");" 2>/dev/null) || unset RISK_IN_TARGET_STATE
  return 0
}

# 表示。approval 用(複数行)と、outbox.reason に足す 1 行
risk_format() {
  local l w
  printf 'risk: %s   # 第 1 段の参考値。最終判定には使われない\n' "$RISK_VALUE"
  printf 'risk-score: %s / threshold %s\n' "$RISK_SCORE" "$RISK_THRESHOLD"
  for l in "${RISK_LINES[@]}"; do printf 'risk-reason: %s\n' "$l"; done
  for w in "${RISK_WARNS[@]:-}"; do [[ -n $w ]] && printf 'risk-warn: %s\n' "$w"; done
  return 0
}
risk_oneline() {
  local out="risk: $RISK_VALUE(参考。score $RISK_SCORE/$RISK_THRESHOLD)" l w sep=" ["
  for l in "${RISK_LINES[@]}"; do out+="$sep$l"; sep="; "; done
  [[ $sep == "; " ]] && out+="]"
  for w in "${RISK_WARNS[@]:-}"; do [[ -n $w ]] && out+=" {警告: $w}"; done
  printf '%s' "${out//$'\n'/ }"
}
