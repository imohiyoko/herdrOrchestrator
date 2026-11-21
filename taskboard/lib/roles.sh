# shellcheck shell=bash
# 役割の配布(role-context / role-broadcast / roles)
# bin/tb から source される。単体では実行しない。

# ---------- roles(agent ごとの役割の配布。SessionStart hook と一括送信) ----------
# ロールは herdr セッション固有。roles/sessions/<セッション>/ があれば、そのセッションではそこだけを使う(共有の roles/ は見ない)。
# 無ければ共有の roles/。台帳の役割(agents.role)も、同じセッションの agent のものだけを使う
ROLES_BASE=${TB_ROLES:-$TB_ROOT/../roles}
# このセッション(lib/db.sh の SESS)の roles/sessions/<セッション>/ があればそこだけ
if [[ $SESS =~ ^[A-Za-z0-9_][A-Za-z0-9._-]*$ && -d $ROLES_BASE/sessions/$SESS ]]; then ROLES_DIR=$ROLES_BASE/sessions/$SESS
else ROLES_DIR=$ROLES_BASE; fi
ROLE_MAX=${TB_ROLE_MAX:-12000}
ROLE_FILE=""; ROLE_HASH=""; ROLE_TEXT=""

# ---------- 役割の本文(DB の config 表。キーは role:<役割名> と role-map)----------
# 引く順: DB のこのセッションの行 > 共通の行(*) > ROLES_DIR のファイル(roles/sessions/<セッション>/ があればそこ、無ければ roles/)。
# 役割名は roles/<役割名>.md の名前(_common を含む)。@map は roles/map.conf(agent 名のパターン = 役割)
ROLE_KEY_RE='^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$'
role_key_of() {  # $1=役割名 | @map → DB のキー(使えない名前なら 1)
  if [[ $1 == @map ]]; then printf 'role-map'; return 0; fi
  [[ $1 =~ $ROLE_KEY_RE && $1 != README ]] || return 1
  printf 'role:%s' "$1"
}
_role_cache_var() { local v=${1//./_d_}; v=${v//-/_h_}; v=${v//@/_a_}; printf 'RLK_%s' "$v"; }
# RB_BODY(本文)と RB_SRC(db:<セッション> / db:* / file:<パス>)を入れる。無ければ 1。1 プロセスの中では覚えておく(書いたら role_forget)
role_lookup() {  # $1=役割名 | @map
  local n=$1 key file cv bv sv r
  key=$(role_key_of "$n") || return 1
  cv=$(_role_cache_var "$n"); bv=${cv}_B; sv=${cv}_S
  if [[ -n ${!sv:-} ]]; then RB_BODY=${!bv}; RB_SRC=${!sv}; [[ $RB_SRC != none ]]; return; fi
  if [[ $n == @map ]]; then file=map.conf; else file=$n.md; fi
  RB_BODY=""; RB_SRC=none
  r=$(config_db_row "$key")
  if [[ -n $r ]]; then
    RB_BODY=${r#?:}; if [[ ${r:0:1} == '*' ]]; then RB_SRC="db:*"; else RB_SRC="db:$SESS"; fi
  elif [[ -f $ROLES_DIR/$file ]]; then RB_BODY=$(cat "$ROLES_DIR/$file"); RB_SRC="file:$ROLES_DIR/$file"; fi
  printf -v "$bv" '%s' "$RB_BODY"; printf -v "$sv" '%s' "$RB_SRC"
  [[ $RB_SRC != none ]]
}
role_exists() { role_lookup "$1"; }
role_forget() { local cv; cv=$(_role_cache_var "$1"); unset "${cv}_B" "${cv}_S"; }
# 役割名の一覧(_ で始まるもの・README を除く)。DB の行とファイル
role_list() {
  local f
  {
    sql "SELECT substr(key, 6) FROM main.config WHERE key LIKE 'role:%' AND herdr_session IN ($SQS, '*');" 2>/dev/null || true
    for f in "$ROLES_DIR"/*.md; do [[ -f $f ]] && basename "$f" .md; done
  } | grep -Ev '^(_|README$)' | sort -u
}

# agent のロール(複数可)の役割名を 1 行 1 つで出す(無ければ何も出さない)。
# 優先: 役割 <agent 名>(1 つ)> 台帳の role(tb role-set / agent-new --role。カンマ区切り)> 役割の対応(map。値もカンマ区切り可)。
# 複数のロールは「分野 × 立場」(例 hr,manager)を表す。存在しない役割名は飛ばす
# $2 に台帳の role を渡すと、台帳を読まずにそれで決める(tb role-set が読んだ値から旧ロールを決めるため)。
# ROLE_STRICT=1(許可判定の経路)では台帳照会の失敗を 1 で返し、map に進まない。それ以外(load_role など)は未登録扱い
role_files_for() {  # $1=agent 名 [$2=台帳の role]
  local name=$1 k v pat found=""
  [[ $name =~ ^[A-Za-z0-9._-]{1,64}$ && $name != _* && $name != README ]] || return 0
  if role_exists "$name"; then printf '%s\n' "$name"; return 0; fi
  _role_list() {  # $1=カンマ区切り → 存在するものだけ出す
    local x out="" ; local -a xs
    IFS=, read -ra xs <<<"$1"
    for x in "${xs[@]}"; do
      x=$(trim "$x")
      [[ $x =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ && $x != README ]] && role_exists "$x" && out+="$x"$'\n'
    done
    printf '%s' "$out"
  }
  local reg
  if [[ -n ${2+x} ]]; then reg=$2
  else
    # 台帳はこのセッションの行だけ。別の herdr セッションの agent は台帳に無いものとして map で決める
    reg=$(sql "SELECT COALESCE(role,'') FROM agents WHERE name=$(q "$name");" 2>/dev/null) || { [[ -n ${ROLE_STRICT:-} ]] && return 1; reg=""; }
  fi
  if [[ -n $reg ]]; then found=$(_role_list "$reg"); [[ -z $found ]] || { printf '%s\n' "$found"; return 0; }; fi
  role_lookup @map || return 0
  while IFS='=' read -r k v || [[ -n $k ]]; do
    pat=$(trim "${k%%#*}"); v=$(trim "${v%%#*}")
    [[ -n $pat && -n $v && $v =~ ^[A-Za-z0-9._,\ -]+$ ]] || continue
    # shellcheck disable=SC2053  # glob として比べる
    if [[ $name == $pat ]]; then found=$(_role_list "$v"); [[ -z $found ]] || printf '%s\n' "$found"; return 0; fi
  done <<<"$RB_BODY"
}

# 先頭の役割名(1 つだけを見る古い呼び出し元向け: gate / risk。basename .md しても同じ名前)
role_file_for() {  # $1=agent 名 [$2=台帳の role]
  local f; f=$(role_files_for "$@") || return 1; printf '%s' "${f%%$'\n'*}"
}

# 役割名をカンマ区切りで(例 hr,manager)。無ければ空。$2 は role_files_for と同じ
# ROLE_HINT_NAME / ROLE_HINT: まだ台帳に無い agent(作成の承認判定)は、名前から推定せず要求された役割で判定する
ROLE_HINT_NAME=""; ROLE_HINT=""
role_name_of() {  # $1=agent 名 [$2=台帳の role]
  if [[ -n $ROLE_HINT_NAME && $1 == "$ROLE_HINT_NAME" ]]; then printf '%s' "$ROLE_HINT"; return 0; fi
  local i fs f out=""
  if [[ $# -eq 1 ]]; then   # 判定の間に固定した役割があればそれを使う(role_pin)
    for i in "${!ROLE_PIN_N[@]}"; do
      if [[ ${ROLE_PIN_N[i]} == "$1" ]]; then [[ ${ROLE_PIN_V[i]} == ok:* ]] || return 1; printf '%s' "${ROLE_PIN_V[i]#ok:}"; return 0; fi
    done
  fi
  fs=$(role_files_for "$@") || return 1
  while IFS= read -r f; do [[ -n $f ]] || continue; out+="${out:+,}$f"; done <<<"$fs"
  printf '%s' "$out"
}

# 作成後の役割が、要求された(承認した)役割とずれるか。ずれるなら 0 で ROLE_CONFLICT_WHY に理由
#   roles/<名前>.md があり要求と違う(名前のファイルが優先される)/ 要求した roles/<役割>.md が無い(map.conf の役割に落ちる)
ROLE_CONFLICT_WHY=""
role_conflict() {  # $1=agent 名 $2=要求された役割(空なら無し)
  ROLE_CONFLICT_WHY=""
  [[ -n $2 ]] || return 1
  if [[ $2 != "$1" ]] && role_exists "$1"; then ROLE_CONFLICT_WHY="役割 $1 の定義があるため、$1 の役割は $2 になりません"; return 0; fi
  local x; local -a xs; IFS=, read -ra xs <<<"$2"
  for x in "${xs[@]}"; do
    x=$(trim "$x")
    if ! role_exists "$x"; then ROLE_CONFLICT_WHY="役割 $x の定義が無いため、$1 の役割は $2 になりません"; return 0; fi
  done
  return 1
}

# 1 回の判定の中で役割を固定する(規則ごとに台帳を読み直すと、途中の変更で規則をすり抜けうる)。
# role_pin <名前>...: 役割を一度だけ引いて覚える(引けなければ「確かめられない」として覚える)。role_unpin で忘れる
ROLE_PIN_N=(); ROLE_PIN_V=()
role_pin() {
  local n i r
  for n in "$@"; do
    for i in "${!ROLE_PIN_N[@]}"; do [[ ${ROLE_PIN_N[i]} == "$n" ]] && continue 2; done
    if r=$(ROLE_STRICT=1 role_name_of "$n"); then ROLE_PIN_V+=("ok:$r"); else ROLE_PIN_V+=("fail:"); fi
    ROLE_PIN_N+=("$n")
  done
}
role_unpin() { ROLE_PIN_N=(); ROLE_PIN_V=(); }

# 規則のパターンと agent 名を比べる。一致すれば 0、しなければ 1
#   @<役割の glob>          その役割を(複数のうちどれかとして)持つ
#   @<glob>+<glob>...       全部を持つ(例 @hr+manager)
#   それ以外                名前の glob
# (routes.conf / approvals.conf の宛先 / spawn-policy の denied_parents が使う)
# 台帳を読めず役割を確かめられないときは 2(呼び出し側は、そこで許可側に倒さず止める)
name_matches() {  # $1=agent 名 $2=パターン
  if [[ $2 == @* ]]; then
    local roles want r ok
    local -a wants have
    roles=$(ROLE_STRICT=1 role_name_of "$1") || return 2
    [[ -n $roles ]] || return 1
    IFS=+ read -ra wants <<<"${2#@}"
    IFS=, read -ra have <<<"$roles"
    for want in "${wants[@]}"; do
      ok=1
      for r in "${have[@]}"; do
        # shellcheck disable=SC2053  # glob として比べる
        [[ $r == $want ]] && { ok=0; break; }
      done
      ((ok == 0)) || return 1
    done
    return 0
  else
    # shellcheck disable=SC2053
    [[ $1 == $2 ]]
  fi
}

# ROLE_FILES(配列。役割名)/ ROLE_FILE(先頭)/ ROLE_LABEL(hr.md,manager.md)/ ROLE_HASH / ROLE_TEXT を埋める。渡すものが無ければ 1
# 複数のロールは _common のあとに順に足す
ROLE_FILES=(); ROLE_LABEL=""
load_role() {  # $1=agent 名
  local body="" f
  ROLE_FILES=(); ROLE_LABEL=""; ROLE_HASH=""; ROLE_TEXT=""
  while IFS= read -r f; do [[ -n $f ]] && ROLE_FILES+=("$f"); done < <(role_files_for "$1")
  ROLE_FILE=${ROLE_FILES[0]:-}
  for f in "${ROLE_FILES[@]}"; do ROLE_LABEL+="${ROLE_LABEL:+,}$f.md"; done
  role_lookup _common && body+="$RB_BODY"$'\n\n'
  for f in "${ROLE_FILES[@]}"; do role_lookup "$f" && body+="$RB_BODY"$'\n\n'; done
  blank "$body" && return 1
  ROLE_HASH=$(printf '%s' "$body" | sha256)
  if (( ${#body} > ROLE_MAX )); then body="${body:0:ROLE_MAX}"$'\n\n(長すぎるため途中で切りました。全文は tb role-show で読んでください)'; fi
  ROLE_TEXT="# あなたの役割(agent 名: $1。tb が自動で渡しています。版 ${ROLE_HASH:0:12})"$'\n\n'"$body"
}

last_briefed_hash() {  # $1=session_id
  sql "SELECT hash FROM role_briefings WHERE session_id=$(q "$1") ORDER BY id DESC LIMIT 1;"
}
record_briefing() {  # session agent file hash source via
  sql "INSERT INTO main.role_briefings (herdr_session, session_id, agent, role_file, hash, source, via, created_at)
    VALUES ($SQS, $(q "$1"), $(q "$2"), NULLIF($(q "$3"),''), $(q "$4"), $(q "$5"), $(q "$6"), $(q "$(now)"));" >/dev/null
}

# SessionStart hook から呼ぶ(bin/tb-role-hook 経由)。常に終了コード 0。渡すときだけ stdout に hook 用の JSON を出す
cmd_role-context() {
  local src="" sid="" name
  while [[ $# -gt 0 ]]; do
    case $1 in
      --source) [[ $# -ge 2 ]] || return 0; src=$2; shift 2;;
      --session-id) [[ $# -ge 2 ]] || return 0; sid=$2; shift 2;;
      *) return 0;;
    esac
  done
  [[ $src =~ ^(startup|resume|clear|compact)$ && $sid =~ ^[A-Za-z0-9._-]{1,128}$ && -n ${HERDR_PANE_ID:-} ]] || return 0
  name=$(agent_name_by_pane "$HERDR_PANE_ID"); [[ -n $name ]] || return 0
  load_role "$name" || return 0
  # 同じ会話の再開で、同じ版を渡し済みなら渡さない(履歴に残っている)
  if [[ $src == resume && $(last_briefed_hash "$sid") == "$ROLE_HASH" ]]; then return 0; fi
  record_briefing "$sid" "$name" "$ROLE_LABEL" "$ROLE_HASH" "$src" hook || true
  jq -nc --arg c "$ROLE_TEXT" '{hookSpecificOutput:{hookEventName:"SessionStart",additionalContext:$c}}'
}

# 動いている agent に役割(の置き場所)を送る。同じセッションに同じ版を渡し済みなら飛ばす
# 終了コード: 0 / 10 使用中で送れなかった agent がある / 11 送信失敗がある
cmd_role-broadcast() {
  local only=" $* " list rc=0 name pane sess msg r
  list=$(herdr_call agent list 2>/dev/null) || die "herdr agent list に失敗しました"
  while IFS=$'\x1f' read -r pane name sess; do
    [[ -n $name ]] || continue
    [[ $# -eq 0 || $only == *" $name "* ]] || continue
    if ! load_role "$name"; then echo "skip $name (役割ファイルなし)"; continue; fi
    if [[ -n $sess && $(last_briefed_hash "$sess") == "$ROLE_HASH" ]]; then echo "skip $name (渡し済み ${ROLE_HASH:0:12})"; continue; fi
    msg="[role-update / tb] あなた($name)の役割が更新されました(版 ${ROLE_HASH:0:12}、$ROLE_LABEL)。tb role-show で読み直し、以後はこれに従ってください"
    r=0; herdr_send "$name" "$msg" "role:$name" role 2>/dev/null || r=$?
    if ((r)); then echo "pending $name (宛先が使用中か送信失敗。あとで再実行)"; ((r > rc)) && rc=$r; continue; fi
    [[ -n $sess ]] && record_briefing "$sess" "$name" "$ROLE_LABEL" "$ROLE_HASH" broadcast broadcast
    echo "sent $name (${ROLE_HASH:0:12})"
  done < <(jq -r '.result.agents[]? | [.pane_id, (.name // ""), (.agent_session.value // "")] | map(tostring | gsub("[\u001f\n]"; " ")) | join("\u001f")' <<<"$list")
  return $rc
}

# ロールを変える(台帳 agents.role)。ロールは許可ルール(@<役割>)の主語・目的語なので、これは権限の変更でもある。
# 人の端末からはそのまま。agent からは人事役(@hr、TB_ROLE_ADMIN で変えられる)で、grants に role:change の行があるか
# approvals の role:change が allow のときだけ。台帳のロール(tb role-set。対象は 1 体の agent)に限り、grants の role:change の行が
# あれば人事役でなくてもよい(役割の本文・map は同じ役割の全 agent に効くので、人事役に限ったまま)。
# agent は自分のロールと、人事役のロールを付けることはできない(権限の自己拡大を防ぐ)。記録は role_changes に残る
ROLE_ADMIN=${TB_ROLE_ADMIN:-hr}
# 人事役(agent)が役割を変えてよいか(人は呼ばない)。$1=対象(grants.conf の対象) $2=人に頼むときのコマンド
# $3=agent なら $1 は 1 体の agent で、grants に当たれば人事役でなくてよい(tb role-set)。だめなら die か 20
role_admin_check() {
  if [[ ${3:-} == agent ]] && grant_of "$REQ_BY" role:change "$1"; then APPROVAL_VALUE=allow; APPROVAL_RULE=$GRANT_RULE
  else
    name_matches "$REQ_BY" "@$ROLE_ADMIN" || die "ロールを変えられるのは人と人事役(@$ROLE_ADMIN)だけです($REQ_BY)"
    if grant_of "$REQ_BY" role:change "$1"; then APPROVAL_VALUE=allow; APPROVAL_RULE=$GRANT_RULE; else approval_of "role:change"; fi
  fi
  if [[ $APPROVAL_VALUE != allow ]]; then
    msg_err role.change.needs-approval 'role:change は人の承認が要ります({rule})。人が自分の端末で実行してください: {command}' rule="$APPROVAL_RULE" command="$2"
    return 20
  fi
}

# ---------- 役割の定義を読む・書く(DB の config 表)----------
# tb role-show [<agent 名>]: その agent に渡す役割の本文(省略時は自分の pane の agent)。agent はファイルではなくこれで読む
cmd_role-show() {
  local name=${1:-}
  [[ $# -le 1 ]] || usage "role-show [<agent 名>]"
  if [[ -z $name ]]; then
    [[ -n ${HERDR_PANE_ID:-} ]] || usage "role-show <agent 名>(agent の pane では省略できる)"
    name=$(agent_name_by_pane "$HERDR_PANE_ID"); [[ -n $name ]] || die "この pane の agent が分かりません"
  fi
  load_role "$name" || die "$name に渡す役割がありません"
  printf '%s\n' "$ROLE_TEXT"
}

# tb role-list: 役割名と出どころ(db:<セッション> / db:* / file:<パス>)。_common と map も出す
cmd_role-list() {
  [[ $# -eq 0 ]] || usage "role-list"
  local r
  for r in _common @map $(role_list); do
    role_lookup "$r" || continue
    printf '%s\t%s\n' "$r" "$RB_SRC"
  done
}

# tb role-write <役割名|@map> [--shared] < 本文 / tb role-edit <役割名|@map> [--shared]
# 書くのは DB のこのセッションの行(--shared なら全セッション共通の行)。人か、人事役(role:change が allow のとき)
# 人事役は、人事役の役割そのものと、map で人事役を付ける行は書けない(権限の自己拡大を防ぐ)
role_write_args() {  # → RW_NAME / RW_SHARED
  RW_NAME=""; RW_SHARED=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --shared) RW_SHARED=1; shift;;
      -*) usage "不明な引数: $1";;
      *) [[ -z $RW_NAME ]] || usage "役割名は 1 つ"; RW_NAME=$1; shift;;
    esac
  done
  [[ -n $RW_NAME ]] || usage "role-write|role-edit <役割名|@map> [--shared]"
  role_key_of "$RW_NAME" >/dev/null || usage "役割名は英数字・_ で始まり、英数字と . _ - の 64 字以内(または @map): $RW_NAME"
}
role_put() {  # $1=役割名|@map $2=本文 $3=how $4=shared
  local key x
  key=$(role_key_of "$1")
  requester
  if [[ $REQ_BY != human ]]; then
    [[ $1 != "$ROLE_ADMIN" ]] || die "人事役の役割($ROLE_ADMIN)を書き換えられるのは人だけです"
    if [[ $1 == @map ]]; then
      while IFS='=' read -r _ x || [[ -n ${x:-} ]]; do
        x=$(trim "${x%%#*}")
        [[ ",${x// /}," != *",$ROLE_ADMIN,"* ]] || die "人事役($ROLE_ADMIN)を付ける対応を書けるのは人だけです"
      done <<<"$2"
    fi
    local sh=""; (($4)) && sh=" --shared"
    role_admin_check "$1" "tb role-write $1$sh < 本文" || return $?
  fi
  blank "$2" && die "本文が空です(消すときは人が tb config で行を消す)"
  config_db_put "$key" "$2" "$3" "$4" "$REQ_BY"
  role_forget "$1"
  role_lookup "$1"; echo "$1: ${RB_SRC}。動いている agent に配るには: tb role-broadcast"
}
cmd_role-write() {
  role_write_args "$@"
  [[ ! -t 0 ]] || usage "role-write <役割名>: 本文は標準入力から渡す"
  local body; body=$(cat)
  role_put "$RW_NAME" "$body" role-write "$RW_SHARED"
}
cmd_role-edit() {
  role_write_args "$@"
  local tmp body; tmp=$(mktemp)
  if role_lookup "$RW_NAME"; then printf '%s\n' "$RB_BODY" >"$tmp"; fi
  "${VISUAL:-${EDITOR:-vi}}" "$tmp" || { rm -f "$tmp"; die "編集を中止しました"; }
  body=$(cat "$tmp"); rm -f "$tmp"
  role_put "$RW_NAME" "$body" role-edit "$RW_SHARED"
}
cmd_role-set() {
  [[ $# -ge 2 ]] || usage "role-set <agent 名> <ロール[,ロール...]> [--reason R]"
  local name=$1 role=$2 why="" old new; shift 2
  while [[ $# -gt 0 ]]; do
    case $1 in
      --reason) need_arg "$@"; why=$2; shift 2;;
      *) usage "role-set: 不明な引数 $1";;
    esac
  done
  [[ $name =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || usage "agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内: $name"
  local x; local -a xs
  IFS=, read -ra xs <<<"$role"
  ((${#xs[@]} >= 1 && ${#xs[@]} <= 4)) || usage "ロールはカンマ区切りで 1〜4 個: $role"
  for x in "${xs[@]}"; do
    [[ $x =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ && $x != README ]] && role_exists "$x" || usage "ロールは定義のある役割名(tb role-list。カンマ区切りで複数可): $x"
  done
  requester
  if [[ $REQ_BY != human ]]; then
    grant_of "$REQ_BY" role:change "$name" || name_matches "$REQ_BY" "@$ROLE_ADMIN" || die "ロールを変えられるのは人、人事役(@$ROLE_ADMIN)、grants で role:change を許された agent だけです($REQ_BY)"
    [[ $REQ_BY != "$name" ]] || die "自分のロールは変えられません"
    for x in "${xs[@]}"; do [[ $x != "$ROLE_ADMIN" ]] || die "人事役のロール($ROLE_ADMIN)を付けられるのは人だけです"; done
    role_admin_check "$name" "tb role-set $name $role" agent || return $?
  fi
  agent_sync >/dev/null 2>&1 || true
  if [[ -z $(sql "SELECT 1 FROM agents WHERE name=$(q "$name");") ]]; then
    other_session "$name" && die "$OP_OUT(ロールはセッション固有)"
    die "台帳に無い agent です: $name(tb agents で確かめる)"
  fi
  ! role_exists "$name" || die "役割 $name($RB_SRC)があるため、台帳のロールは効きません(その役割を人事役の手順で直す)"
  # 旧ロールは実効の役割(台帳の役割ファイルが無ければ map.conf)。台帳の値を先に読み、変更はその値が変わっていないときだけ行う
  # (読んでから書くまでに別の変更が入ったら、履歴の old_role がずれるので何もせずやり直しを促す)
  local reg n
  reg=$(sql "SELECT COALESCE(role,'') FROM agents WHERE name=$(q "$name");") || die "台帳を読めません"
  old=$(role_name_of "$name" "$reg")   # 読んだ台帳の値から実効の役割を決める(もう一度は読まない)
  n=$(sql "BEGIN IMMEDIATE;
    INSERT INTO main.role_changes (agent, old_role, new_role, changed_by, reason, created_at, herdr_session)
    SELECT $(q "$name"), NULLIF($(q "$old"),''), $(q "$role"), $(q "$REQ_BY"), NULLIF($(q "$(oneline "$why" 300)"),''), $(q "$(now)"), $SQS
    WHERE (SELECT COALESCE(role,'') FROM agents WHERE name=$(q "$name"))=$(q "$reg");
    UPDATE main.agents SET role=$(q "$role"), updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND name=$(q "$name") AND COALESCE(role,'')=$(q "$reg");
    SELECT changes();
    COMMIT;") || die "ロールを変えられませんでした(DB)"
  [[ $n == 1 ]] || die "同時に別のロール変更が入りました。もう一度実行してください"
  new=$(role_name_of "$name")
  echo "$name role ${old:--} -> $new (by $REQ_BY)。本人に配るには: tb role-broadcast $name"
}

# ロール変更の記録(このセッションのもの)
cmd_role-log() {
  local json=0; [[ ${1:-} == --json ]] && json=1
  if ((json)); then sql -json "SELECT * FROM role_changes ORDER BY id DESC LIMIT 100;" | jq -c '(. // [])[]'
  else sql -nullvalue - -separator $'\t' "SELECT created_at, agent, COALESCE(old_role,'-')||' -> '||new_role, changed_by, COALESCE(reason,'') FROM role_changes ORDER BY id DESC LIMIT 100;"; fi
}

# agent ごとの役割ファイルと、渡した版が最新か
cmd_roles() {
  local json=0 list name pane sess last at state
  [[ ${1:-} == --json ]] && json=1
  list=$(herdr_call agent list 2>/dev/null) || die "herdr agent list に失敗しました"
  while IFS=$'\x1f' read -r pane name sess; do
    [[ -n $name ]] || continue
    last=""; at=""
    if load_role "$name"; then
      if [[ -n $sess ]]; then
        last=$(last_briefed_hash "$sess")
        at=$(sql "SELECT created_at FROM role_briefings WHERE session_id=$(q "$sess") ORDER BY id DESC LIMIT 1;")
      fi
      if [[ -z $last ]]; then state=never; elif [[ $last == "$ROLE_HASH" ]]; then state=current; else state=outdated; fi
    else state=no-role; fi
    if ((json)); then
      jq -nc --arg n "$name" --arg p "$pane" --arg f "$ROLE_LABEL" --arg h "${ROLE_HASH:0:12}" --arg s "$state" --arg at "$at" \
        '{name:$n,pane:$p,role_file:$f,version:$h,state:$s,briefed_at:(if $at=="" then null else $at end)}'
    else
      printf '%s\t%s\t%s\t%s\t%s\n' "$name" "$ROLE_LABEL" "${ROLE_HASH:0:12}" "$state" "${at:--}"
    fi
  done < <(jq -r '.result.agents[]? | [.pane_id, (.name // ""), (.agent_session.value // "")] | map(tostring | gsub("[\u001f\n]"; " ")) | join("\u001f")' <<<"$list")
}
