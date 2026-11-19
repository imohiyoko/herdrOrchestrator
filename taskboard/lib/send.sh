# shellcheck shell=bash
# 指揮官 → worker の送信(send / flush / outbox)
# bin/tb から source される。単体では実行しない。

# ---------- send(指揮官 → worker の下り。agent は herdr agent prompt を直接打たず、これを使う) ----------
# 送信者は HERDR_PANE_ID + herdr agent list から決まる(自己申告にしない)。宛先は config/routes.conf のルールで許可されたものだけ。
# 宛先が使用中なら outbox に積み、tb flush(tb digest からも呼ばれる)で後から届ける。送ろうとしたものはすべて outbox と messages に残る。
ROUTES=${TB_ROUTES:-$TB_ROOT/config/routes.conf}
SEND_KINDS="task info answer"
SEND_MAX=${TB_SEND_MAX:-4000}
# 届け先の herdr セッションは、この tb のセッション(lib/db.sh の SESS)。outbox の行もそのセッションのものだけを見る

# 宛先が tb の台帳で停止中(agent-stop 済み、または herdr の一覧から消えた)か。
# 停止中の宛先には送らず queued のまま残す(revive 後の flush で届く)。agent_sync のあとでだけ使う
outbox_target_stopped() {  # $1=宛先
  [[ -n $(sql "SELECT 1 FROM agents WHERE name=$(q "$1") AND state='stopped' LIMIT 1;" 2>/dev/null) ]]
}

# 指揮官として扱う名前: 既定の宛先と projects.conf の値
conductor_names() {
  local k v
  printf '%s\n' "$DEFAULT_TARGET"
  config_has projects || return 0
  while IFS='=' read -r k v || [[ -n $k ]]; do
    k=${k//[[:space:]]/}; v=${v//[[:space:]]/}
    [[ -z $k || $k == \#* || -z $v ]] && continue
    printf '%s\n' "$v"
  done < <(config_text projects)
}

# route_allowed <from> <to>: 0 なら許可。理由は ROUTE_REASON に入る
# routes.conf の各行は "<送信者のパターン> -> <宛先のパターン>"(* と ? の glob。@<役割> なら役割で比べる)。最初に一致した行で決まる。
# 行頭に ! を付けると拒否。routes.conf が無いときは「指揮官 → 誰でも」だけを許可する
ROUTE_REASON=""
route_allowed() {
  # 送信者と宛先の役割は判定の間 1 回だけ引いて固定する(行ごとに読み直さない)
  local rc=0 pinned=0
  if [[ ${#ROLE_PIN_N[@]} -eq 0 ]]; then role_pin "$1" "$2"; pinned=1; fi
  _route_rules "$1" "$2" || rc=$?
  ((pinned == 0)) || role_unpin
  return $rc
}
_route_rules() {
  local from=$1 to=$2 line neg l r n
  if ! config_has routes; then
    while IFS= read -r n; do
      [[ $from == "$n" ]] && { ROUTE_REASON="default: conductor $n"; return 0; }
    done < <(conductor_names)
    ROUTE_REASON="default: $from は指揮官ではない(worker からの報告は tb comment を使う)"; return 1
  fi
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    neg=0; [[ $line == \!* ]] && { neg=1; line=$(trim "${line#!}"); }
    [[ $line == *"->"* ]] || continue
    l=$(trim "${line%%->*}"); r=$(trim "${line#*->}")
    local m1=0 m2=1
    name_matches "$from" "$l" || m1=$?
    if ((m1 == 0)); then m2=0; name_matches "$to" "$r" || m2=$?; fi
    if ((m1 == 2 || m2 == 2)); then ROUTE_REASON="routes.conf: ${line} の役割を確かめられません(台帳を読めない)"; return 1; fi
    if ((m1 == 0 && m2 == 0)); then
      if ((neg)); then ROUTE_REASON="routes.conf: !$l -> $r"; return 1; fi
      ROUTE_REASON="routes.conf: $l -> $r"; return 0
    fi
  done < <(config_text routes)
  ROUTE_REASON="routes.conf: $from -> $to に一致する行がない"; return 1
}

# ---------- delivery(今届ける / 保留する。config の delivery キー) ----------
# 各行は "<送信者のパターン> -> <宛先のパターン> : <種別>=<now|hold> ..."(パターンは routes と同じ。種別は task info answer、* は残り全部)
#   coordinator -> assistant : info=hold answer=hold task=now
#   @worker     -> assistant : *=hold
# 送信者と宛先が一致し、その種別(無ければ *)を書いた最初の行で決まる。どの行にも当たらなければ now(今までどおり)。
# キーが無ければ判定もしない(今までどおり)。役割を確かめられない行に当たったら hold に倒す(人と話している相手の入力を邪魔しない)。
# 判定の順は routes → approvals → delivery(outbox_deliver が届ける直前に決める。承認のあとや flush でも同じ)
DELIVERY_MODE=now DELIVERY_RULE=""
delivery_mode() {  # $1=送信者 $2=宛先 $3=種別 → DELIVERY_MODE / DELIVERY_RULE
  DELIVERY_MODE=now; DELIVERY_RULE="delivery: 設定なし"
  config_has delivery || return 0
  local rc=0 pinned=0
  if [[ ${#ROLE_PIN_N[@]} -eq 0 ]]; then role_pin "$1" "$2"; pinned=1; fi
  _delivery_rules "$@" || rc=$?
  ((pinned == 0)) || role_unpin
  return $rc
}
# 行の中から種別の値を引く。$1=指定の並び $2=種別 → 値(now|hold)を出す。書いていなければ 1
_delivery_pick() {
  local tok k v star="" toks
  read -ra toks <<<"${1//,/ }"   # glob を展開しない(*=hold がファイル名に化けない)
  for tok in "${toks[@]}"; do
    [[ $tok == *=* ]] || continue
    k=${tok%%=*}; v=${tok#*=}
    [[ $v == now || $v == hold ]] || continue
    [[ $k == "$2" ]] && { printf '%s' "$v"; return 0; }
    [[ $k == '*' && -z $star ]] && star=$v
  done
  [[ -n $star ]] || return 1
  printf '%s' "$star"
}
_delivery_rules() {
  local from=$1 to=$2 kind=$3 line pair spec l r v
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ $line == *"->"*:* ]] || continue
    pair=${line%%:*}; spec=$(trim "${line#*:}")
    l=$(trim "${pair%%->*}"); r=$(trim "${pair#*->}")
    v=$(_delivery_pick "$spec" "$kind") || continue
    local m1=0 m2=1
    name_matches "$from" "$l" || m1=$?
    if ((m1 == 0)); then m2=0; name_matches "$to" "$r" || m2=$?; fi
    if ((m1 == 2 || m2 == 2)); then DELIVERY_MODE=hold; DELIVERY_RULE="delivery: $line の役割を確かめられないため保留"; return 0; fi
    ((m1 == 0 && m2 == 0)) || continue
    DELIVERY_MODE=$v; DELIVERY_RULE="delivery: $l -> $r : $kind=$v"; return 0
  done < <(config_text delivery)
  DELIVERY_RULE="delivery: $from -> $to に一致する行がない(now)"
}

# 保留する: inbox に置き、outbox は sent(届け先 inbox)にする。1 つのトランザクション。失敗したら 12(outbox は queued のまま)
DELIVER_HELD=""
outbox_hold() {  # $1=outbox.id $2=送信者 $3=宛先 $4=種別 $5=task(空可) $6=本文 $7=規則 $8=記録用の 1 行
  local oid=$1 iid err
  if ! iid=$(sql "BEGIN IMMEDIATE;
      INSERT INTO main.inbox (herdr_session, outbox_id, recipient, from_name, kind, task_id, text, rule, created_at)
        SELECT $SQS, $oid, $(q "$3"), $(q "$2"), $(q "$4"), NULLIF($(q "$5"),''), $(q "$6"), $(q "$7"), $(q "$(now)")
        WHERE EXISTS (SELECT 1 FROM outbox WHERE id=$oid AND status='queued');
      UPDATE main.outbox SET status='sent', sent_at=$(q "$(now)"), reason=reason||' / '||$(q "$7")||' → inbox I-'||(SELECT id FROM inbox WHERE outbox_id=$oid)
        WHERE herdr_session=$SQS AND id=$oid AND status='queued' AND EXISTS (SELECT 1 FROM inbox WHERE outbox_id=$oid);
      SELECT 'I-'||id FROM inbox WHERE outbox_id=$oid;
      COMMIT;" 2>&1) || [[ $iid != I-* ]]; then
    [[ -n $iid ]] || iid="O-$oid は queued ではありません"
    err=${iid##*$'\n'}
    log_message hold "O-$oid" "$3" "$8" failed
    echo "tb: O-$oid を inbox に保留できませんでした(送っていません。queued のまま): $err" >&2
    return 12
  fi
  DELIVER_HELD=$iid
  log_message hold "O-$oid" "$3" "$8" sent
}

outbox_deliver() {  # $1=outbox.id → 0 送信済み・保留済み(DELIVER_HELD に I-<n>)/ 10 / 11 / 12 保留に失敗
  local oid=$1 row st from pane target kind tid text msg
  DELIVER_HELD=""
  row=$(session_row outbox id "$oid")
  [[ -n $row ]] || die "outbox が見つかりません: O-$oid"
  st=$(jq -r .status <<<"$row")
  [[ $st == queued ]] || { echo "O-$oid は $st です"; return 0; }
  from=$(jq -r .from_name <<<"$row"); pane=$(jq -r .from_pane <<<"$row"); target=$(jq -r .target <<<"$row")
  kind=$(jq -r .kind <<<"$row"); tid=$(jq -r '.task_id // "-"' <<<"$row"); text=$(jq -r .text <<<"$row")
  msg="[from: $from / $pane / $tid / $kind] $(oneline "$text" "$SEND_MAX")"
  delivery_mode "$from" "$target" "$kind"
  if [[ $DELIVERY_MODE == hold ]]; then
    outbox_hold "$oid" "$from" "$target" "$kind" "$(jq -r '.task_id // ""' <<<"$row")" "$text" "$DELIVERY_RULE" "$msg"; return $?
  fi
  limits_send_gate "$target" || { log_message send "O-$oid" "$target" "$msg" busy; return 10; }   # max_working_agents: 待たせる(queued のまま)
  herdr_send "$target" "$msg" "O-$oid" send || return $?
  sql "UPDATE main.outbox SET status='sent', sent_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$oid;"
}

# 終了コード: 0 送信済み(または delivery の設定で inbox に保留した)/ 10 宛先使用中で outbox に積んだ / 11 送信失敗で outbox に積んだ
#             12 保留しようとして inbox に書けなかった(outbox に積んだまま)/ 4 ルールで拒否(記録のみ)
#             20 人の承認待ち(approvals.conf が ask。人が tb approve O-<n>) / 3 送信者を特定できず未保存
cmd_send() {
  [[ $# -ge 1 ]] || usage "send <宛先 agent 名> --text T [--task T-..] [--kind task|info|answer] [--json]"
  local target=$1 text="" tid="" kind=task json=0; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --text) need_arg "$@"; text=$2; shift 2;;
      --task) need_arg "$@"; tid=$2; shift 2;;
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --json) json=1; shift;;
      --from|--from-*|--name|--pane|--session) usage "send: 送信者は herdr から自動で決まります。$1 は指定できません";;
      *) usage "send: 不明な引数 $1";;
    esac
  done
  [[ $target =~ ^[A-Za-z0-9._-]{1,64}$ ]] || usage "send: 宛先は agent 名(英数字と . _ -): $target"
  [[ " $SEND_KINDS " == *" $kind "* ]] || usage "send: --kind は $SEND_KINDS のどれか"
  blank "$text" && usage "send: --text が必要です"
  [[ -z $tid ]] || { valid_id "$tid"; require_task "$tid" >/dev/null; }
  resolve_label || return 3
  [[ $target != "$LABEL_NAME" ]] || usage "send: 自分自身には送れません"
  local names
  names=$(herdr_call agent list 2>/dev/null | jq -r '.result.agents[]?.name // empty' 2>/dev/null) || names=""
  grep -qxF -- "$target" <<<"$names" || die "send: 宛先の agent が herdr にいません: $target(保存しません)"
  local status=queued reason="" rc=0 oid
  DELIVER_HELD=""
  if ! route_allowed "$LABEL_NAME" "$target"; then status=denied; reason=$ROUTE_REASON
  else
    reason=$ROUTE_REASON
    approval_of "send:$kind:$target" "$LABEL_NAME"
    [[ $APPROVAL_VALUE == allow ]] || status=pending
    reason+=" / approvals: $APPROVAL_RULE"
    # リスク評価(第 1 段: 参考値を reason に残すだけ。status は変えない)
    risk_collect "$LABEL_NAME" "$target" "$kind" "$text"; risk_eval
    reason+=" / $(risk_oneline)"
  fi
  oid=$(sql "INSERT INTO main.outbox (herdr_session, from_name, from_pane, from_session, target, kind, task_id, text, status, reason, created_at)
    VALUES ($SQS, $(q "$LABEL_NAME"), $(q "$LABEL_PANE"), $(q "$LABEL_SESSION"), $(q "$target"), $(q "$kind"), NULLIF($(q "$tid"),''),
            $(q "$text"), $(q "$status"), $(q "$reason"), $(q "$(now)"));
    SELECT last_insert_rowid();")
  if [[ $status == denied ]]; then
    echo "tb: 送信を拒否しました($reason): O-$oid" >&2
    rc=4
  elif [[ $status == pending ]]; then
    echo "tb: 人の承認待ちです(approvals.conf: $APPROVAL_RULE)。人が tb approve O-$oid で届けます" >&2
    rc=20
  else
    outbox_deliver "$oid" >/dev/null || rc=$?
  fi
  if ((json)); then sql -json "SELECT * FROM outbox WHERE id=$oid;" | jq -c --arg held "$DELIVER_HELD" '.[0] | .id = "O-\(.id)" | if $held != "" then .inbox = $held else . end'
  elif ((rc == 0)) && [[ -n $DELIVER_HELD ]]; then echo "O-$oid held for $target ($DELIVER_HELD)"
  else
    case $rc in 0) echo "O-$oid sent to $target";; 4) echo "O-$oid denied";; 20) echo "O-$oid pending-approval";; *) echo "O-$oid queued";; esac
  fi
  return $rc
}

# 積まれた送信を届ける。宛先ごとに古い順で、使用中の宛先は次回に回す。
# 停止中の宛先には送らず飛ばす(毎回の失敗を記録しない。revive すれば次の flush で届く)。
# 停止中かは、最初に 1 回 herdr の一覧と台帳を突き合わせて決める
cmd_flush() {
  [[ $# -eq 0 ]] || usage "flush(引数なし)"
  local oid target rc=0 r busy=" " held=" " synced=0
  [[ -n $(sql "SELECT 1 FROM outbox WHERE status='queued' LIMIT 1;") ]] || return 0
  # 台帳を herdr の一覧と突き合わせる(台帳の stopped が古いままのことがある)。herdr に届かなければ判定せず、今までどおり送ってみる
  agent_sync >/dev/null 2>&1 && synced=1
  while IFS=$'\t' read -r oid target; do
    [[ -n $oid ]] || continue
    [[ $busy == *" $target "* || $held == *" $target "* ]] && continue   # 同じ宛先の後続は順序を保つため送らない
    if ((synced)) && outbox_target_stopped "$target"; then
      held+="$target "
      echo "tb: 宛先 '$target' は停止中のため送りません(queued のまま。tb agent-revive $target のあとの flush で届きます)" >&2
      continue
    fi
    r=0; outbox_deliver "$oid" || r=$?
    if ((r)); then busy+="$target "; ((r > rc)) && rc=$r; fi
  done < <(sql -separator $'\t' "SELECT id, target FROM outbox WHERE status='queued' ORDER BY id;")
  return $rc
}

cmd_outbox() {
  local st="" json=0 where="1=1"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --status) need_arg "$@"; st=$2; shift 2;;
      --json) json=1; shift;;
      *) usage "outbox: 不明な引数 $1";;
    esac
  done
  if [[ -n $st ]]; then [[ $st =~ ^(pending|queued|sent|denied)$ ]] || usage "outbox: --status は pending|queued|sent|denied"; where="status=$(q "$st")"; fi
  if ((json)); then sql -json "SELECT * FROM outbox WHERE ($where) ORDER BY id DESC LIMIT 100;" | jq -c '(. // [])[] | .id = "O-\(.id)"'
  else sql -nullvalue - -separator $'\t' "SELECT 'O-'||id, status, from_name, target, kind, COALESCE(task_id,'-'), created_at,
         substr(replace(replace(text, char(10), ' '), char(9), ' '),1,80) FROM outbox WHERE ($where) ORDER BY id DESC LIMIT 100;"
  fi
}

# ---------- inbox(保留された送信を、宛先の agent が自分で読む) ----------
# 読み手は HERDR_PANE_ID + herdr agent list から決まる(自己申告にしない)。自分宛ての行だけを見る(送信者もほかの agent も読めない)。
# 人と話している agent は、人に頼まれたとき(「確認して」など)だけ読む(roles/_common.md)
cmd_inbox() {
  local unread=0 json=0 where
  while [[ $# -gt 0 ]]; do
    case $1 in
      --unread) unread=1; shift;;
      --json) json=1; shift;;
      --name|--agent|--for|--pane|--session) usage "inbox: 読み手は herdr から自動で決まります。$1 は指定できません";;
      *) usage "inbox [--unread] [--json]";;
    esac
  done
  resolve_label || return 3
  where="recipient=$(q "$LABEL_NAME")"; ((unread)) && where+=" AND read_at IS NULL"
  if ((json)); then
    sql -json "SELECT * FROM inbox WHERE $where ORDER BY id;" | jq -c '(. // [])[] | .id = "I-\(.id)" | .outbox_id = "O-\(.outbox_id)"'
  else
    sql -nullvalue - -separator $'\t' "SELECT 'I-'||id, CASE WHEN read_at IS NULL THEN 'unread' ELSE 'read' END, from_name, kind, COALESCE(task_id,'-'), created_at,
         substr(replace(replace(text, char(10), ' '), char(9), ' '),1,200) FROM inbox WHERE $where ORDER BY id;"
  fi
}

# 1 件を全文で出し、既読にする(自分宛てでなければ「見つからない」。ほかの agent の行があるかも漏らさない)
cmd_inbox-read() {
  [[ $# -ge 1 && $1 =~ ^I-[0-9]+$ ]] || usage "inbox-read I-<n> [--json]"
  local iid=${1#I-} json=0 row; shift
  case ${1:-} in --json) json=1;; "") ;; *) usage "inbox-read I-<n> [--json]";; esac
  resolve_label || return 3
  row=$(sql -json "SELECT * FROM inbox WHERE id=$iid AND recipient=$(q "$LABEL_NAME");" | jq -c '.[0] // empty')
  [[ -n $row ]] || die "inbox に見つかりません: I-$iid"
  sql "UPDATE main.inbox SET read_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$iid AND recipient=$(q "$LABEL_NAME") AND read_at IS NULL;"
  if ((json)); then jq -c '.id = "I-\(.id)" | .outbox_id = "O-\(.outbox_id)"' <<<"$row"
  else
    jq -r '"I-\(.id)  from: \(.from_name) / \(.kind) / \(.task_id // "-") / \(.created_at)  (O-\(.outbox_id))\n\n\(.text)"' <<<"$row"
  fi
}
