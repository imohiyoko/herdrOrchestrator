# shellcheck shell=bash
# 人の承認(approval / allow / ask / approve / reject)
# bin/tb から source される。単体では実行しない。

# ---------- approvals(人の承認が要るか。config/approvals.conf) ----------
APPROVALS=${TB_APPROVALS:-$TB_ROOT/config/approvals.conf}
APPROVAL_RULE=""

valid_action() { [[ $1 =~ ^[A-Za-z0-9._:*?-]{1,128}$ ]] || usage "操作名に使えない文字があります: $1"; }

# approval_of <操作>: APPROVAL_VALUE に ask|allow、根拠の行を APPROVAL_RULE に入れる(サブシェルで呼ばない)
APPROVAL_VALUE=ask
approval_of() {
  local act=$1 line k v
  APPROVAL_RULE="(一致なし: 既定 ask)"; APPROVAL_VALUE=ask
  if [[ -f $APPROVALS ]]; then
    while IFS= read -r line || [[ -n $line ]]; do
      line=$(trim "${line%%#*}"); [[ $line == *=* ]] || continue
      k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
      [[ $v == ask || $v == allow ]] || continue
      # shellcheck disable=SC2053  # glob として比べる
      if [[ $act == $k ]]; then APPROVAL_RULE="$k = $v"; APPROVAL_VALUE=$v; return 0; fi
    done <"$APPROVALS"
  fi
}

# 人の端末からの実行か(agent の pane からは承認・設定変更をさせない)
require_human() {
  local pane=${HERDR_PANE_ID:-} n
  [[ -n $pane ]] || return 0
  n=$(pane_owner_name "$pane") || die "$1: herdr agent list に失敗したため、人の端末からの実行か確かめられません"
  [[ -z $n ]] || die "$1 は人が自分の端末で実行します(agent '$n' の pane からは実行できません)"
}

# tb approval <操作> [--to 宛先 --text 本文 [--kind K] [--task T-..]]: 1 行目 ask|allow、2 行目 rule:。終了コード 0=allow / 20=ask
# --to と --text があるときだけ、続けて risk: 行(リスク評価。第 1 段は参考値で、1 行目と終了コードには影響しない)を出す
cmd_approval() {
  [[ $# -ge 1 ]] || usage "approval <操作>(例: send:task:worker1, role:change)[--to 宛先 --text 本文 [--kind K] [--task T-..]]"
  local act=$1 to="" text="" kind=task have_text=0; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --to) need_arg "$@"; to=$2; shift 2;;
      --text) need_arg "$@"; text=$2; have_text=1; shift 2;;
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --task) need_arg "$@"; shift 2;;   # 第 1 段では評価に使わない(将来用)
      *) usage "approval: 不明な引数 $1";;
    esac
  done
  valid_action "$act"
  approval_of "$act"
  printf '%s\nrule: %s\n' "$APPROVAL_VALUE" "$APPROVAL_RULE"
  if [[ -n $to ]] && ((have_text)); then
    [[ $to =~ ^[A-Za-z0-9._-]{1,64}$ ]] || usage "approval: --to は agent 名(英数字と . _ -)"
    [[ " $SEND_KINDS " == *" $kind "* ]] || usage "approval: --kind は $SEND_KINDS のどれか"
    local sender=""   # 人の端末(pane なし)= 空。agent の pane は resolve_label で決める(引けなければ ? = 評価不能)
    if [[ -n ${HERDR_PANE_ID:-} ]]; then resolve_label 2>/dev/null && sender=$LABEL_NAME || sender="?"; fi
    risk_collect "$sender" "$to" "$kind" "$text"; risk_eval
    risk_format
  elif [[ -n $to ]] || ((have_text)); then
    usage "approval: リスク評価には --to と --text の両方が要ります"
  fi   # 引数なしは従来の出力のまま(評価なし)
  [[ $APPROVAL_VALUE == allow ]] || return 20
}

# tb allow|ask <パターン>: 人が「今後は許可/承認制に戻す」を決める。同じパターンの行を書き換え、無ければ先頭に足す
set_approval() {  # $1=allow|ask $2=パターン
  local val=$1 pat=$2 tmp found=0 line k
  valid_action "$pat"
  require_human "tb $val"
  mkdir -p "$(dirname "$APPROVALS")"; touch "$APPROVALS"
  [[ -f $APPROVALS && ! -L $APPROVALS ]] || die "approvals.conf が通常ファイルではありません"
  tmp=$(mktemp "$APPROVALS.XXXXXX")
  {
    while IFS= read -r line || [[ -n $line ]]; do
      k=$(trim "${line%%#*}"); k=$(trim "${k%%=*}")
      if [[ $line != \#* && $line == *=* && $k == "$pat" ]]; then
        ((found)) || printf '%s = %s   # %s に人が変更\n' "$pat" "$val" "$(now)"
        found=1
      else
        printf '%s\n' "$line"
      fi
    done <"$APPROVALS"
  } >"$tmp"
  if ((!found)); then   # コメントの直後(最初のルールの前)に足す: 先に一致させるため
    awk -v rule="$pat = $val   # $(now) に人が追加" 'BEGIN{d=0} !d && $0 !~ /^#/ && $0 !~ /^[[:space:]]*$/ {print rule; d=1} {print} END{if(!d) print rule}' "$tmp" >"$tmp.2"
    mv "$tmp.2" "$tmp"
  fi
  mv "$tmp" "$APPROVALS"
  sql "INSERT INTO approval_changes (pattern, value, created_at) VALUES ($(q "$pat"), $(q "$val"), $(q "$(now)"));" >/dev/null
  echo "$pat = $val"
}
cmd_allow() { [[ $# -eq 1 ]] || usage "allow <操作のパターン>"; set_approval allow "$1"; }
cmd_ask()   { [[ $# -eq 1 ]] || usage "ask <操作のパターン>"; set_approval ask "$1"; }

# tb approve O-<n> [--always] / tb reject O-<n>: 人の承認待ちの送信を通す・止める
cmd_approve() {
  [[ $# -ge 1 && $1 =~ ^O-[0-9]+$ ]] || usage "approve O-<n> [--always]"
  local oid=${1#O-} always=0 row kind target; shift
  [[ ${1:-} == --always ]] && always=1
  require_human "tb approve"
  row=$(sql -json "SELECT * FROM outbox WHERE id=$oid;" | jq -c '.[0] // empty')
  [[ -n $row ]] || die "outbox が見つかりません: O-$oid"
  [[ $(jq -r .status <<<"$row") == pending ]] || die "O-$oid は承認待ちではありません($(jq -r .status <<<"$row"))"
  kind=$(jq -r .kind <<<"$row"); target=$(jq -r .target <<<"$row")
  sql "UPDATE outbox SET status='queued', reason=reason||' / 人が承認 $(now)' WHERE id=$oid;"
  ((always)) && set_approval allow "send:$kind:$target" >/dev/null && echo "send:$kind:$target = allow(今後は承認なし)"
  local r=0; outbox_deliver_default "$oid" >/dev/null || r=$?
  case $r in
    0) echo "O-$oid approved and sent to $target";;
    11) echo "tb: O-$oid は承認済みですが宛先 '$target' に届けられません(上の理由を見てください。queued のまま。直したら tb flush)" >&2
        echo "O-$oid approved but delivery failed (queued)"; return 11;;
    *) echo "O-$oid approved (宛先使用中のため queued。tb flush で届く)";;
  esac
  return 0
}
cmd_reject() {
  [[ $# -eq 1 && $1 =~ ^O-[0-9]+$ ]] || usage "reject O-<n>"
  local oid=${1#O-}
  require_human "tb reject"
  [[ $(sql "SELECT status FROM outbox WHERE id=$oid;") == pending ]] || die "O-$oid は承認待ちではありません"
  sql "UPDATE outbox SET status='denied', reason=reason||' / 人が却下 $(now)' WHERE id=$oid;"
  echo "O-$oid rejected"
}
