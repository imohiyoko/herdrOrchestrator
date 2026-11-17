# shellcheck shell=bash
# 役割の配布(role-context / role-broadcast / roles)
# bin/tb から source される。単体では実行しない。

# ---------- roles(agent ごとの役割の配布。SessionStart hook と一括送信) ----------
# ロールは herdr セッション固有。roles/sessions/<セッション>/ があれば、そのセッションではそこだけを使う(共有の roles/ は見ない)。
# 無ければ共有の roles/。台帳の役割(agents.role)も、同じセッションの agent のものだけを使う
ROLES_BASE=${TB_ROLES:-$TB_ROOT/../roles}
ROLES_SESSION=$(herdr_session_name)
if [[ $ROLES_SESSION =~ ^[A-Za-z0-9_][A-Za-z0-9._-]*$ && -d $ROLES_BASE/sessions/$ROLES_SESSION ]]; then ROLES_DIR=$ROLES_BASE/sessions/$ROLES_SESSION
else ROLES_DIR=$ROLES_BASE; fi
ROLE_MAX=${TB_ROLE_MAX:-12000}
ROLE_FILE=""; ROLE_HASH=""; ROLE_TEXT=""

sha256() { if command -v sha256sum >/dev/null; then sha256sum | cut -c1-64; else shasum -a 256 | cut -c1-64; fi; }

# agent のロール(複数可)の役割ファイルを 1 行 1 パスで出す(無ければ何も出さない)。
# 優先: roles/<agent 名>.md(1 つ)> 台帳の role(tb role-set / agent-new --role。カンマ区切り)> roles/map.conf(値もカンマ区切り可)。
# 複数のロールは「分野 × 立場」(例 hr,manager)を表す。存在しない役割名は飛ばす
# $2 に台帳の role を渡すと、台帳を読まずにそれで決める(tb role-set が読んだ値から旧ロールを決めるため)。
# ROLE_STRICT=1(許可判定の経路)では台帳照会の失敗を 1 で返し、map.conf に進まない。それ以外(load_role など)は未登録扱い
role_files_for() {  # $1=agent 名 [$2=台帳の role]
  local name=$1 k v pat r found=""
  [[ $name =~ ^[A-Za-z0-9._-]{1,64}$ && $name != _* && $name != README ]] || return 0
  if [[ -f $ROLES_DIR/$name.md ]]; then printf '%s\n' "$ROLES_DIR/$name.md"; return 0; fi
  _role_list() {  # $1=カンマ区切り → 存在するものだけ出す
    local x out="" ; local -a xs
    IFS=, read -ra xs <<<"$1"
    for x in "${xs[@]}"; do
      x=$(trim "$x")
      [[ $x =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ && $x != README && -f $ROLES_DIR/$x.md ]] && out+="$ROLES_DIR/$x.md"$'\n'
    done
    printf '%s' "$out"
  }
  local reg row
  if [[ -n ${2+x} ]]; then reg=$2
  else
    row=$(sql -separator $'\x1f' "SELECT herdr_session, COALESCE(role,'') FROM agents WHERE name=$(q "$name");" 2>/dev/null) || { [[ -n ${ROLE_STRICT:-} ]] && return 1; row=""; }
    # 別の herdr セッションの agent には、このセッションの役割を付けない(ロールはセッション固有)
    if [[ -n $row && ${row%%$'\x1f'*} != "$ROLES_SESSION" ]]; then return 0; fi
    reg=${row#*$'\x1f'}
  fi
  if [[ -n $reg ]]; then found=$(_role_list "$reg"); [[ -z $found ]] || { printf '%s\n' "$found"; return 0; }; fi
  [[ -f $ROLES_DIR/map.conf ]] || return 0
  while IFS='=' read -r k v || [[ -n $k ]]; do
    pat=$(trim "${k%%#*}"); v=$(trim "${v%%#*}")
    [[ -n $pat && -n $v && $v =~ ^[A-Za-z0-9._,\ -]+$ ]] || continue
    # shellcheck disable=SC2053  # glob として比べる
    if [[ $name == $pat ]]; then found=$(_role_list "$v"); [[ -z $found ]] || printf '%s\n' "$found"; return 0; fi
  done <"$ROLES_DIR/map.conf"
}

# 先頭の役割ファイル(1 つだけを見る古い呼び出し元向け: gate / risk)
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
  while IFS= read -r f; do [[ -n $f ]] || continue; f=${f##*/}; out+="${out:+,}${f%.md}"; done <<<"$fs"
  printf '%s' "$out"
}

# 作成後の役割が、要求された(承認した)役割とずれるか。ずれるなら 0 で ROLE_CONFLICT_WHY に理由
#   roles/<名前>.md があり要求と違う(名前のファイルが優先される)/ 要求した roles/<役割>.md が無い(map.conf の役割に落ちる)
ROLE_CONFLICT_WHY=""
role_conflict() {  # $1=agent 名 $2=要求された役割(空なら無し)
  ROLE_CONFLICT_WHY=""
  [[ -n $2 ]] || return 1
  if [[ $2 != "$1" && -f $ROLES_DIR/$1.md ]]; then ROLE_CONFLICT_WHY="roles/$1.md があるため、$1 の役割は $2 になりません"; return 0; fi
  local x; local -a xs; IFS=, read -ra xs <<<"$2"
  for x in "${xs[@]}"; do
    x=$(trim "$x")
    if [[ ! -f $ROLES_DIR/$x.md ]]; then ROLE_CONFLICT_WHY="roles/$x.md が無いため、$1 の役割は $2 になりません"; return 0; fi
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

# ROLE_FILES(配列)/ ROLE_FILE(先頭)/ ROLE_LABEL(hr.md,manager.md)/ ROLE_HASH / ROLE_TEXT を埋める。渡すものが無ければ 1
# 複数のロールは _common.md のあとに順に足す
ROLE_FILES=(); ROLE_LABEL=""
load_role() {  # $1=agent 名
  local body="" f
  ROLE_FILES=(); ROLE_LABEL=""; ROLE_HASH=""; ROLE_TEXT=""
  while IFS= read -r f; do [[ -n $f ]] && ROLE_FILES+=("$f"); done < <(role_files_for "$1")
  ROLE_FILE=${ROLE_FILES[0]:-}
  for f in "${ROLE_FILES[@]}"; do ROLE_LABEL+="${ROLE_LABEL:+,}${f#"$ROLES_DIR"/}"; done
  [[ -f $ROLES_DIR/_common.md ]] && body+="$(cat "$ROLES_DIR/_common.md")"$'\n\n'
  for f in "${ROLE_FILES[@]}"; do body+="$(cat "$f")"$'\n\n'; done
  blank "$body" && return 1
  ROLE_HASH=$(printf '%s' "$body" | sha256)
  if (( ${#body} > ROLE_MAX )); then body="${body:0:ROLE_MAX}"$'\n\n(長すぎるため途中で切りました。全文は roles/ を読んでください)'; fi
  ROLE_TEXT="# あなたの役割(agent 名: $1。roles/ から自動で渡しています。版 ${ROLE_HASH:0:12})"$'\n\n'"$body"
}

last_briefed_hash() {  # $1=session_id
  sql "SELECT hash FROM role_briefings WHERE session_id=$(q "$1") ORDER BY id DESC LIMIT 1;"
}
record_briefing() {  # session agent file hash source via
  sql "INSERT INTO role_briefings (session_id, agent, role_file, hash, source, via, created_at)
    VALUES ($(q "$1"), $(q "$2"), NULLIF($(q "$3"),''), $(q "$4"), $(q "$5"), $(q "$6"), $(q "$(now)"));" >/dev/null
}

# 人か agent かの判定用: 一覧を取れなければ 1(「名前が無い」= 人の pane と区別する)
pane_owner_name() {  # $1=pane → 名前(無ければ空)
  local out
  out=$(herdr_call agent list 2>/dev/null) || return 1
  jq -r --arg p "$1" '[.result.agents[]? | select(.pane_id==$p)][0].name // empty' <<<"$out" 2>/dev/null || return 1
}

agent_name_by_pane() {  # $1=pane → 名前(無ければ空)
  herdr_call agent list 2>/dev/null | jq -r --arg p "$1" '[.result.agents[]? | select(.pane_id==$p)][0].name // empty' 2>/dev/null
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
  local only=" $* " list rc=0 name pane sess files msg r f
  list=$(herdr_call agent list 2>/dev/null) || die "herdr agent list に失敗しました"
  while IFS=$'\x1f' read -r pane name sess; do
    [[ -n $name ]] || continue
    [[ $# -eq 0 || $only == *" $name "* ]] || continue
    if ! load_role "$name"; then echo "skip $name (役割ファイルなし)"; continue; fi
    if [[ -n $sess && $(last_briefed_hash "$sess") == "$ROLE_HASH" ]]; then echo "skip $name (渡し済み ${ROLE_HASH:0:12})"; continue; fi
    files="$(cd "$ROLES_DIR" && pwd)/_common.md"
    for f in "${ROLE_FILES[@]}"; do files+=" $(cd "$(dirname "$f")" && pwd)/$(basename "$f")"; done
    msg="[role-update / tb] あなた($name)の役割が更新されました(版 ${ROLE_HASH:0:12})。次のファイルを読み、以後はこれに従ってください: $files"
    r=0; herdr_send "$name" "$msg" "role:$name" role 2>/dev/null || r=$?
    if ((r)); then echo "pending $name (宛先が使用中か送信失敗。あとで再実行)"; ((r > rc)) && rc=$r; continue; fi
    [[ -n $sess ]] && record_briefing "$sess" "$name" "$ROLE_LABEL" "$ROLE_HASH" broadcast broadcast
    echo "sent $name (${ROLE_HASH:0:12})"
  done < <(jq -r '.result.agents[]? | [.pane_id, (.name // ""), (.agent_session.value // "")] | map(tostring | gsub("[\u001f\n]"; " ")) | join("\u001f")' <<<"$list")
  return $rc
}

# ロールを変える(台帳 agents.role)。ロールは許可ルール(@<役割>)の主語・目的語なので、これは権限の変更でもある。
# 人の端末からはそのまま。agent からは人事役(@hr、TB_ROLE_ADMIN で変えられる)だけで、approvals の role:change が allow のときだけ。
# agent は自分のロールと、人事役のロールを付けることはできない(権限の自己拡大を防ぐ)。記録は role_changes に残る
ROLE_ADMIN=${TB_ROLE_ADMIN:-hr}
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
    [[ $x =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ && $x != README && -f $ROLES_DIR/$x.md ]] || usage "ロールは roles/<ロール>.md がある名前(カンマ区切りで複数可): $x"
  done
  requester
  if [[ $REQ_BY != human ]]; then
    name_matches "$REQ_BY" "@$ROLE_ADMIN" || die "ロールを変えられるのは人と人事役(@$ROLE_ADMIN)だけです($REQ_BY)"
    [[ $REQ_BY != "$name" ]] || die "自分のロールは変えられません"
    for x in "${xs[@]}"; do [[ $x != "$ROLE_ADMIN" ]] || die "人事役のロール($ROLE_ADMIN)を付けられるのは人だけです"; done
    approval_of "role:change"
    if [[ $APPROVAL_VALUE != allow ]]; then
      echo "tb: role:change は人の承認が要ります($APPROVAL_RULE)。人が自分の端末で実行してください: tb role-set $name $role" >&2
      return 20
    fi
  fi
  agent_sync >/dev/null 2>&1 || true
  local sess; sess=$(sql "SELECT herdr_session FROM agents WHERE name=$(q "$name");")
  [[ -n $sess ]] || die "台帳に無い agent です: $name(tb agents で確かめる)"
  [[ $sess == "$ROLES_SESSION" ]] || die "$name は herdr セッション '$sess' の agent です。ロールはセッション固有なので、HERDR_SESSION=$sess で実行してください"
  [[ ! -f $ROLES_DIR/$name.md ]] || die "roles/$name.md があるため、台帳のロールは効きません(そのファイルを人事役の手順で直す)"
  # 旧ロールは実効の役割(台帳の役割ファイルが無ければ map.conf)。台帳の値を先に読み、変更はその値が変わっていないときだけ行う
  # (読んでから書くまでに別の変更が入ったら、履歴の old_role がずれるので何もせずやり直しを促す)
  local reg n
  reg=$(sql "SELECT COALESCE(role,'') FROM agents WHERE name=$(q "$name");") || die "台帳を読めません"
  old=$(role_name_of "$name" "$reg")   # 読んだ台帳の値から実効の役割を決める(もう一度は読まない)
  n=$(sql "BEGIN IMMEDIATE;
    INSERT INTO role_changes (agent, old_role, new_role, changed_by, reason, created_at, herdr_session)
    SELECT $(q "$name"), NULLIF($(q "$old"),''), $(q "$role"), $(q "$REQ_BY"), NULLIF($(q "$(oneline "$why" 300)"),''), $(q "$(now)"), $(q "$ROLES_SESSION")
    WHERE (SELECT COALESCE(role,'') FROM agents WHERE name=$(q "$name"))=$(q "$reg");
    UPDATE agents SET role=$(q "$role"), updated_at=$(q "$(now)") WHERE name=$(q "$name") AND COALESCE(role,'')=$(q "$reg");
    SELECT changes();
    COMMIT;") || die "ロールを変えられませんでした(DB)"
  [[ $n == 1 ]] || die "同時に別のロール変更が入りました。もう一度実行してください"
  new=$(role_name_of "$name")
  echo "$name role ${old:--} -> $new (by $REQ_BY)。本人に配るには: tb role-broadcast $name"
}

# ロール変更の記録(このセッションのもの。セッションを記録する前の行も出す)
role_log_where() { printf "herdr_session IS NULL OR herdr_session=%s" "$(q "$ROLES_SESSION")"; }
cmd_role-log() {
  local json=0; [[ ${1:-} == --json ]] && json=1
  if ((json)); then sql -json "SELECT * FROM role_changes WHERE $(role_log_where) ORDER BY id DESC LIMIT 100;" | jq -c '(. // [])[]'
  else sql -nullvalue - -separator $'\t' "SELECT created_at, agent, COALESCE(old_role,'-')||' -> '||new_role, changed_by, COALESCE(reason,'') FROM role_changes WHERE $(role_log_where) ORDER BY id DESC LIMIT 100;"; fi
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
