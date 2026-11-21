# shellcheck shell=bash
# 人の承認(approval / allow / ask / approve / reject)
# bin/tb から source される。単体では実行しない。

# ---------- approvals(人の承認が要るか。config/approvals.conf) ----------
APPROVALS=${TB_APPROVALS:-$TB_ROOT/config/approvals.conf}
APPROVAL_RULE=""

valid_action() { [[ $1 =~ ^[A-Za-z0-9._:*?@-]{1,128}$ ]] || usage "操作名に使えない文字があります: $1"; }

# ---------- grants.conf(主体ごとの個別の許可。相対指定 ~child など)----------
# 1 行 1 つ: <主体> <操作> <対象>。どれかの行に当たれば allow(approvals.conf より先に見る)。当たらなければ approvals.conf が決める
#   主体   agent 名の glob か @<役割>(@a+b も可)
#   操作   操作名の glob(対象の手前まで。例 agent:read / send:task / agent:stop / role:change)
#   対象   agent 名の glob か @<役割>、または spawn の関係で ~child(子)/ ~descendant(子孫)/ ~parent(親)/ ~sibling(兄弟)
# 許可を足すだけで、拒否(routes.conf の ! など)を外すことはない。役割を確かめられない行は使わない(許可側に倒さない)
GRANTS=${TB_GRANTS:-$TB_ROOT/config/grants.conf}
GRANT_RULE=""
grant_parent_of() {  # $1=agent 名 → spawned_by(同じセッションの agent だけ)。読めなければ 1
  sql "SELECT COALESCE(spawned_by,'') FROM agents WHERE name=$(q "$1");"
}
grant_relation() {  # $1=主体 $2=対象 $3=~関係 → 当たれば 0
  local p x i
  case $3 in
    '~child')      p=$(grant_parent_of "$2") || return 1; [[ -n $p && $p == "$1" ]];;
    '~parent')     p=$(grant_parent_of "$1") || return 1; [[ -n $p && $p == "$2" ]];;
    '~sibling')    p=$(grant_parent_of "$1") || return 1; x=$(grant_parent_of "$2") || return 1; [[ -n $p && $p == "$x" && $1 != "$2" ]];;
    '~descendant') x=$2
                   for ((i = 0; i < 16; i++)); do   # 上へたどる(循環しても 16 段で止める)
                     x=$(grant_parent_of "$x") || return 1; [[ -n $x ]] || return 1
                     [[ $x == "$1" ]] && return 0
                   done; return 1;;
    *) return 1;;
  esac
}
# grant_of <主体> <操作> <対象>: 当たれば 0 で GRANT_RULE に行を入れる
grant_of() {
  local who=$1 op=$2 target=$3 line s o t rest m
  GRANT_RULE=""
  [[ -n $who && $who != human ]] && config_has grants || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    read -r s o t rest <<<"$line"
    [[ -n $t && -z $rest ]] || continue
    # shellcheck disable=SC2053  # glob として比べる
    [[ $op == $o ]] || continue
    m=0; name_matches "$who" "$s" || m=$?; ((m == 0)) || continue
    if [[ $t == '~'* ]]; then grant_relation "$who" "$target" "$t" || continue
    else m=0; name_matches "$target" "$t" || m=$?; ((m == 0)) || continue; fi
    GRANT_RULE="grants.conf: $s $o $t"; return 0
  done < <(config_text grants)
  return 1
}

# ---------- 承認の委任(grants.conf の approve:<元の操作> <依頼者>)----------
# 人の承認待ち(O-<n> / L-<n>)を、許可された agent が自分の pane から承認・却下できるようにする。
#   例: @lead approve:send:task:* ~child   子からの依頼の送信(宛先は問わない)を、lead が承認できる
#       reviewer1 approve:agent:stop:* w1  w1 が頼んだ agent の停止を、reviewer1 が承認できる
# 「誰からの」= 対象(依頼者)、「何の」= 操作(approve: のあとに元の操作名。その glob)。
# 自分の依頼は承認できない。--always(規則の書き換え)・人だけが決める承認待ち(--all など)は人だけ
APPROVER=""; APPROVER_RULE=""
# approver_check <コマンド名> <元の操作名(空 = 人だけ)> <依頼者>: 人なら APPROVER=human。許可された agent なら APPROVER=<名前>。どちらでもなければ die
approver_check() {
  local cmd=$1 act=$2 by=$3 rc=0 pinned=0
  APPROVER=""; APPROVER_RULE=""
  requester
  if [[ $REQ_BY == human ]]; then APPROVER=human; return 0; fi
  [[ -n $act ]] || die "$cmd: この承認待ちは人が自分の端末で実行します(agent '$REQ_BY' の pane からは承認できません)"
  [[ $REQ_BY != "$by" ]] || die "$cmd: 自分の依頼は承認できません(agent '$REQ_BY')。人が自分の端末で実行します"
  if ((${#ROLE_PIN_N[@]} == 0)); then role_pin "$by" "$REQ_BY"; pinned=1; fi   # 判定の間、役割を固定する
  grant_of "$REQ_BY" "approve:$act" "$by" || rc=$?
  ((pinned == 0)) || role_unpin
  ((rc == 0)) || die "$cmd は人が自分の端末で実行します(agent '$REQ_BY' には $by からの $act を承認する許可がありません。grants.conf に '<承認者> approve:<操作> <依頼者>' の行が要ります)"
  APPROVER=$REQ_BY; APPROVER_RULE=$GRANT_RULE
}
# 記録に残す承認者の書き方(人 / agent の名前と根拠)
approver_label() { if [[ $APPROVER == human ]]; then printf '人'; else printf 'agent %s(%s)' "$APPROVER" "$APPROVER_RULE"; fi; }

# approval_of <操作> [主体]: APPROVAL_VALUE に ask|allow、根拠の行を APPROVAL_RULE に入れる(サブシェルで呼ばない)
# 主体(実行する agent)を渡すと、先に grants.conf を見る(操作の末尾を対象とみなす)
APPROVAL_VALUE=ask
approval_of() {
  # 対象の役割は判定の間 1 回だけ引いて固定する(規則ごとに読み直さない)
  local pinned=0
  if [[ $1 == *:* && ${#ROLE_PIN_N[@]} -eq 0 ]]; then role_pin "${1##*:}" ${2:+"$2"}; pinned=1; fi   # 主体(grants.conf)も固定
  _approval_rules "$1" "${2:-}"
  ((pinned == 0)) || role_unpin
}
_approval_rules() {
  local act=$1 line k v
  APPROVAL_RULE="(一致なし: 既定 ask)"; APPROVAL_VALUE=ask
  if [[ -n ${2:-} && $act == *:* ]] && grant_of "$2" "${act%:*}" "${act##*:}"; then APPROVAL_RULE=$GRANT_RULE; APPROVAL_VALUE=allow; return 0; fi
  if config_has approvals; then
    while IFS= read -r line || [[ -n $line ]]; do
      line=$(trim "${line%%#*}"); [[ $line == *=* ]] || continue
      k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
      [[ $v == ask || $v == allow ]] || continue
      if [[ ${k##*:} == @* ]]; then
        # 末尾が @<役割>(例: send:task:@worker)なら、操作の末尾の名前を役割で比べる
        [[ $act == *:* ]] || continue
        # shellcheck disable=SC2053  # glob として比べる
        [[ ${act%:*} == ${k%:*} ]] || continue
        local m=0; name_matches "${act##*:}" "${k##*:}" || m=$?
        case $m in
          0) APPROVAL_RULE="$k = $v"; APPROVAL_VALUE=$v; return 0;;
          2) APPROVAL_RULE="$k(台帳を読めず役割を確かめられないため ask)"; APPROVAL_VALUE=ask; return 0;;   # 下の行の allow に進まない
        esac
        continue
      fi
      # shellcheck disable=SC2053  # glob として比べる
      if [[ $act == $k ]]; then APPROVAL_RULE="$k = $v"; APPROVAL_VALUE=$v; return 0; fi
    done < <(config_text approvals)
  fi
}

# tb approval <操作> [--to 宛先 --text 本文 [--kind K] [--task T-..]]: 1 行目 ask|allow、2 行目 rule:。終了コード 0=allow / 20=ask
# --to と --text があるときだけ、続けて risk: 行(リスク評価。第 1 段は参考値で、1 行目と終了コードには影響しない)を出す
cmd_approval() {
  [[ $# -ge 1 ]] || usage "approval <操作>(例: send:task:worker1, role:change)[--by agent][--to 宛先 --text 本文 [--kind K] [--task T-..]]"
  local act=$1 to="" text="" kind=task have_text=0 by=""; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --by) need_arg "$@"; by=$2; shift 2;;   # その agent が実行するとして(grants.conf も見る)
      --to) need_arg "$@"; to=$2; shift 2;;
      --text) need_arg "$@"; text=$2; have_text=1; shift 2;;
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --task) need_arg "$@"; shift 2;;   # 第 1 段では評価に使わない(将来用)
      *) usage "approval: 不明な引数 $1";;
    esac
  done
  valid_action "$act"
  [[ -z $by || $by =~ $AGENT_NAME_RE ]] || usage "approval: --by は agent 名: $by"
  # 引数の検査は出力の前に済ませる(エラーのとき 1〜2 行目を先に出さない)
  if [[ -n $to ]] && ((have_text)); then
    [[ $to =~ ^[A-Za-z0-9._-]{1,64}$ ]] || usage "approval: --to は agent 名(英数字と . _ -)"
    [[ " $SEND_KINDS " == *" $kind "* ]] || usage "approval: --kind は $SEND_KINDS のどれか"
  elif [[ -n $to ]] || ((have_text)); then
    usage "approval: リスク評価には --to と --text の両方が要ります"
  fi
  approval_of "$act" "$by"
  printf '%s\nrule: %s\n' "$APPROVAL_VALUE" "$APPROVAL_RULE"
  if [[ -n $to ]] && ((have_text)); then
    # 送信者の判定(pane なし = 人)は既存の require_human と同じ前提
    local sender=""   # 人の端末(pane なし)= 空。agent の pane は resolve_label で決める(引けなければ ? = 評価不能)
    if [[ -n ${HERDR_PANE_ID:-} ]]; then resolve_label 2>/dev/null && sender=$LABEL_NAME || sender="?"; fi
    risk_collect "$sender" "$to" "$kind" "$text"; risk_eval
    risk_format
  fi   # 引数なしは従来の出力のまま(評価なし)
  [[ $APPROVAL_VALUE == allow ]] || return 20
}

# tb allow|ask <パターン>: 人が「今後は許可/承認制に戻す」を決める。同じパターンの行を書き換え、無ければ先頭に足す
set_approval() {  # $1=allow|ask $2=パターン
  local val=$1 pat=$2 body found=0 line k
  valid_action "$pat"
  require_human "tb $val"
  body=""
  while IFS= read -r line || [[ -n $line ]]; do
    k=$(trim "${line%%#*}"); k=$(trim "${k%%=*}")
    if [[ $line != \#* && $line == *=* && $k == "$pat" ]]; then
      ((found)) || body+="$pat = $val   # $(now) に人が変更"$'\n'
      found=1
    else
      body+="$line"$'\n'
    fi
  done < <(config_text approvals)
  body=${body%$'\n'}
  if ((!found)); then   # コメントの直後(最初のルールの前)に足す: 先に一致させるため
    if [[ -z $body ]]; then body="$pat = $val   # $(now) に人が追加"
    else body=$(awk -v rule="$pat = $val   # $(now) に人が追加" 'BEGIN{d=0} !d && $0 !~ /^#/ && $0 !~ /^[[:space:]]*$/ {print rule; d=1} {print} END{if(!d) print rule}' <<<"$body"); fi
  fi
  config_put approvals "$body" "$val"
  sql "INSERT INTO main.approval_changes (herdr_session, pattern, value, created_at) VALUES ($SQS, $(q "$pat"), $(q "$val"), $(q "$(now)"));" >/dev/null
  echo "$pat = $val"
}
cmd_allow() { [[ $# -eq 1 ]] || usage "allow <操作のパターン>"; set_approval allow "$1"; }
cmd_ask()   { [[ $# -eq 1 ]] || usage "ask <操作のパターン>"; set_approval ask "$1"; }

# tb approve O-<n> [--always] / tb reject O-<n>: 人の承認待ちの送信を通す・止める
cmd_approve() {
  [[ $# -ge 1 && $1 =~ ^O-[0-9]+$ ]] || usage "approve O-<n> [--always]"
  local oid=${1#O-} always=0 row kind target; shift
  [[ ${1:-} == --always ]] && always=1
  row=$(session_row outbox id "$oid")
  [[ -n $row ]] || { approver_check "tb approve" "" ""; die "outbox が見つかりません: O-$oid"; }   # agent には、あるかどうかも先に言わない
  kind=$(jq -r .kind <<<"$row"); target=$(jq -r .target <<<"$row")
  approver_check "tb approve" "send:$kind:$target" "$(jq -r .from_name <<<"$row")"
  [[ $APPROVER == human ]] || ((always == 0)) || die "tb approve: --always(今後は承認なしにする)は人だけが使えます"
  [[ $(jq -r .status <<<"$row") == pending ]] || die "O-$oid は承認待ちではありません($(jq -r .status <<<"$row"))"
  # 承認待ちのままの行だけを変える(同時に 2 人が承認しても、届けるのは 1 回)
  [[ $(sql "UPDATE main.outbox SET status='queued', reason=reason||$(q " / $(approver_label)が承認 $(now)") WHERE herdr_session=$SQS AND id=$oid AND status='pending'; SELECT changes();") == 1 ]] \
    || die "O-$oid は承認待ちではありません(ほかで決まりました)"
  ((always)) && set_approval allow "send:$kind:$target" >/dev/null && echo "send:$kind:$target = allow(今後は承認なし)"
  local r=0; outbox_deliver "$oid" >/dev/null || r=$?
  case $r in
    0) if [[ -n $DELIVER_BLOCKED ]]; then echo "O-$oid approved but blocked by $target"
       elif [[ -n $DELIVER_HELD ]]; then echo "O-$oid approved and held for $target ($DELIVER_HELD)"; else echo "O-$oid approved and sent to $target"; fi;;
    12) echo "O-$oid approved but holding failed (queued)"; return 12;;
    11) echo "tb: O-$oid は承認済みですが宛先 '$target' に届けられません(上の理由を見てください。queued のまま。直したら tb flush)" >&2
        echo "O-$oid approved but delivery failed (queued)"; return 11;;
    *) echo "O-$oid approved (宛先使用中のため queued。tb flush で届く)";;
  esac
  return 0
}
cmd_reject() {
  [[ $# -eq 1 && $1 =~ ^O-[0-9]+$ ]] || usage "reject O-<n>"
  local oid=${1#O-} row
  row=$(session_row outbox id "$oid")
  [[ -n $row ]] || { approver_check "tb reject" "" ""; die "O-$oid は承認待ちではありません"; }
  approver_check "tb reject" "send:$(jq -r .kind <<<"$row"):$(jq -r .target <<<"$row")" "$(jq -r .from_name <<<"$row")"
  [[ $(jq -r .status <<<"$row") == pending ]] || die "O-$oid は承認待ちではありません"
  # 却下した返信は「返信済み」に数えない(元の送信にもう一度返信できる)
  sql "UPDATE main.outbox SET status='denied', reason=reason||$(q " / $(approver_label)が却下 $(now)"),
         reply_check=CASE WHEN reply_check='ok' THEN $(q "$(approver_label)が却下") ELSE reply_check END WHERE herdr_session=$SQS AND id=$oid AND status='pending';"
  echo "O-$oid rejected"
}
