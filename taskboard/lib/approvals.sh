# shellcheck shell=bash
# 人の承認(approval / allow / ask / approve / reject)
# bin/tb から source される。単体では実行しない。

# ---------- コマンドの中の操作の判定(ADR 0007: 設定 policy と既定の表 perms/ops.tbl。lib/perms.sh の perm_authorize)----------
# 古い設定 approvals(操作 → ask/allow)と grants(主体・操作・対象の許可)は読まない。tb policy import で policy の行に移して消す
APPROVAL_RULE=""

valid_action() { [[ $1 =~ ^[A-Za-z0-9._:*?@-]{1,128}$ ]] || usage "操作名に使えない文字があります: $1"; }

# ---------- 主体と対象の関係(policy の対象 ~child など)----------
#   spawn の関係 ~child(子)/ ~descendant(子孫)/ ~parent(親)/ ~sibling(兄弟)、
#   組織図(設定 org)の関係 ~report(直属の部下)/ ~subordinate(配下)/ ~manager(上司)/ ~superior(上の全員)/ ~peer(同じ上司)
# 役割・台帳を確かめられない関係は 2(policy の判定では締める向きにだけ使う)
GRANT_RULE=""
grant_parent_of() {  # $1=agent 名 → spawned_by(同じセッションの agent だけ)。読めなければ 1
  sql "SELECT COALESCE(spawned_by,'') FROM agents WHERE name=$(q "$1");"
}
grant_relation() {  # $1=主体 $2=対象 $3=~関係 → 当たれば 0、当たらなければ 1、台帳(親・役割)を読めず確かめられなければ 2(delivery は hold、grants は使わない)
  local p x i
  case $3 in
    '~child')      p=$(grant_parent_of "$2") || return 2; [[ -n $p && $p == "$1" ]];;
    '~parent')     p=$(grant_parent_of "$1") || return 2; [[ -n $p && $p == "$2" ]];;
    '~sibling')    p=$(grant_parent_of "$1") || return 2; x=$(grant_parent_of "$2") || return 2; [[ -n $p && $p == "$x" && $1 != "$2" ]];;
    '~descendant') x=$2
                   for ((i = 0; i < 16; i++)); do   # 上へたどる(循環しても 16 段で止める)
                     x=$(grant_parent_of "$x") || return 2; [[ -n $x ]] || return 1
                     [[ $x == "$1" ]] && return 0
                   done; return 1;;
    '~report'|'~subordinate'|'~manager'|'~superior'|'~peer') org_relation "$1" "$2" "$3";;
    *) return 1;;
  esac
}
PERM_REL_MATCH=grant_relation   # 認可(lib/perms.sh)の対象 ~<関係> をこの関数で比べる

# ---------- 組織図(設定 org。上司の線)----------
# 1 行 1 つ: <部下のパターン> -> <上司>。agent ごとに、上から見て最初に当たった行で上司が 1 人に決まる(決定的)
#   部下のパターン  agent 名の glob か @<役割>(@a+b も可)
#   上司            agent 名、または ~parent(tb agent-spawn で立てた親。親がいなければその行は飛ばす)
# 自分自身を上司にする行は飛ばす。設定が無ければ誰にも上司はいない(中立。関係 ~report などはどれにも当たらない)
# 組織図そのものは全員が見られる(tb org)。できることを決めるのは policy(と、段 3 の残りの delivery / protect)の行
ORG_DEPTH=16   # 上へたどる段数の上限(循環しても止まる)
# org_manager_of <agent> → 上司の名前を出して 0。上司がいなければ 1、役割や親を確かめられない行に先に当たったら 2
org_manager_of() {
  local who=$1 line l r m
  config_has org || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ $line == *"->"* ]] || continue
    l=$(trim "${line%%->*}"); r=$(trim "${line#*->}")
    [[ -n $l && -n $r && $r != *[[:space:]]* ]] || continue
    m=0; name_matches "$who" "$l" || m=$?
    ((m == 2)) && return 2
    ((m == 0)) || continue
    if [[ $r == '~parent' ]]; then r=$(grant_parent_of "$who") || return 2; [[ -n $r ]] || continue; fi
    [[ $r != "$who" ]] || continue
    printf '%s' "$r"; return 0
  done < <(config_text org)
  return 1
}
# org_chain <agent>: 上司を上へ順に 1 行ずつ(最大 ORG_DEPTH 段。循環したら止める)。役割を確かめられなければ 2
org_chain() {
  local x=$1 seen=" $1 " i rc
  for ((i = 0; i < ORG_DEPTH; i++)); do
    rc=0; x=$(org_manager_of "$x") || rc=$?
    ((rc == 0)) || { ((rc == 2)) && return 2; return 0; }
    [[ $seen != *" $x "* ]] || return 0
    seen+="$x "; printf '%s\n' "$x"
  done
}
# org_relation <主体> <対象> <~関係>: 組織図の上で当たれば 0、当たらなければ 1、役割を確かめられなければ 2
#   ~report 対象の上司が主体(直属の部下)/ ~subordinate 対象の上をたどると主体(配下の全員)
#   ~manager 主体の上司が対象 / ~superior 主体の上をたどると対象 / ~peer 上司が同じ(自分は除く)
org_relation() {
  local a=$1 b=$2 x y rc=0 chain
  [[ $a != "$b" ]] || return 1
  case $3 in
    '~report')      x=$(org_manager_of "$b") || return $?; [[ $x == "$a" ]];;
    '~manager')     x=$(org_manager_of "$a") || return $?; [[ $x == "$b" ]];;
    '~subordinate') chain=$(org_chain "$b") || return $?; grep -qxF -- "$a" <<<"$chain";;
    '~superior')    chain=$(org_chain "$a") || return $?; grep -qxF -- "$b" <<<"$chain";;
    '~peer')        x=$(org_manager_of "$a") || return $?; y=$(org_manager_of "$b") || { rc=$?; ((rc == 2)) && return 2; return 1; }
                    [[ $x == "$y" ]];;
    *) return 1;;
  esac
}
# 設定 org の本文を検査する(tb config set / edit のときに呼ばれる)
config_validate_org() {  # $1=本文
  local line l r n=0 bad=""
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    if [[ $line != *"->"* ]]; then bad+="${n} 行目: <部下のパターン> -> <上司> の形ではありません: $line"$'\n'; continue; fi
    l=$(trim "${line%%->*}"); r=$(trim "${line#*->}")
    [[ $l =~ ^@?[A-Za-z0-9*?._+-]{1,64}$ ]] || bad+="${n} 行目: 部下は agent 名の glob か @<役割>: $l"$'\n'
    [[ $r == '~parent' || $r =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || bad+="${n} 行目: 上司は agent 名か ~parent(glob・@役割は書けない): $r"$'\n'
  done <<<"$1"
  [[ -z $bad ]] && return 0
  printf '%s' "$bad" >&2; return 1
}

# 古い権限の設定(approvals / grants)が残っていれば、判定のときに 1 回だけ警告する(読まない。黙って無視しない)
PERM_LEGACY_WARNED=0
perm_legacy_warn() {
  ((PERM_LEGACY_WARNED == 0)) || return 0
  PERM_LEGACY_WARNED=1
  local k left=""
  for k in approvals grants; do
    if config_lookup "$k"; then left+=" $k(${CFG_SRC})"; fi
  done
  [[ -z $left ]] || echo "tb: 古い権限の設定が残っています(もう読みません):${left}。人が tb policy import で設定 policy に移して消してください(ADR 0007)" >&2
}

# grant_of <主体> <操作> <対象>: policy の行が主体にその操作を許していれば(当たる行のいちばん厳しい効果が allow)0 で GRANT_RULE に行を入れる
# 既定の表は見ない(行で明示した許可だけ)。役割・関係を確かめられない行は許可に使わず、締める行(ask-* / deny)は許可を消す
grant_of() {
  local who=$1 op=$2 target=$3
  GRANT_RULE=""
  [[ -n $who && $who != human ]] || return 1
  perm_legacy_warn
  perm_judge "$who" "$op" "$target"
  [[ -z $PJ_ERR ]] && ((PJ_BEST == 0 && PJ_UNK <= 0)) || return 1
  GRANT_RULE=$PJ_BRULE
}

# ---------- 承認の委任(policy の <承認者> approve:<元の操作名> <依頼者> = allow)----------
# 人の承認待ち(O-<n> / L-<n>)を、許可された agent が自分の pane から承認・却下できるようにする。
#   例: @lead approve:send:task:* ~child = allow   子からの依頼の送信(宛先は問わない)を、lead が承認できる
#       reviewer1 approve:agent:stop:* w1 = allow  w1 が頼んだ agent の停止を、reviewer1 が承認できる
# 「誰からの」= 対象(依頼者)、「何の」= 操作(approve: のあとに元の操作名。その glob)。
# 自分の依頼は承認できない。依頼の操作が依頼者に ask-human なら人だけ。--always(規則の書き換え)・人だけが決める承認待ち(--all など)は人だけ
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
  local tier=""
  if [[ $by =~ $AGENT_NAME_RE ]]; then perm_split_act "$act"; perm_authorize "$by" "$PS_OP" "$PS_TARGET"; tier=$PERM_TIER; fi
  [[ $tier == ask-human ]] || grant_of "$REQ_BY" "approve:$act" "$by" || rc=$?
  ((pinned == 0)) || role_unpin
  [[ $tier != ask-human ]] || die "$cmd は人が自分の端末で実行します($by からの $act は ask-human のため、人だけが承認できます。$PERM_RULE)"
  ((rc == 0)) || die "$cmd は人が自分の端末で実行します(agent '$REQ_BY' には $by からの $act を承認する許可がありません。policy に '<承認者> approve:<操作> <依頼者> = allow' の行が要ります)"
  APPROVER=$REQ_BY; APPROVER_RULE=$GRANT_RULE
}
# 記録に残す承認者の書き方(人 / agent の名前と根拠)
approver_label() { if [[ $APPROVER == human ]]; then printf '人'; else printf 'agent %s(%s)' "$APPROVER" "$APPROVER_RULE"; fi; }

# approval_of <操作名:対象> [主体]: APPROVAL_VALUE に allow|ask|deny、効果を APPROVAL_TIER(allow / ask-any / ask-human / deny)、根拠を APPROVAL_RULE に入れる
# (サブシェルで呼ばない)。主体(実行する agent)が無い判定(scheduler からの自動の判定など)は、主体 * の行だけが当たる
APPROVAL_VALUE=ask; APPROVAL_TIER=ask-any
approval_of() {
  # 対象と主体の役割は判定の間 1 回だけ引いて固定する(行ごとに読み直さない)
  local pinned=0
  perm_legacy_warn
  if [[ $1 == *:* && ${#ROLE_PIN_N[@]} -eq 0 ]]; then role_pin "${1##*:}" ${2:+"$2"}; pinned=1; fi
  perm_split_act "$1"
  perm_authorize "${2:-}" "$PS_OP" "$PS_TARGET"
  ((pinned == 0)) || role_unpin
  APPROVAL_TIER=$PERM_TIER; APPROVAL_RULE=$PERM_RULE
  case $PERM_TIER in allow) APPROVAL_VALUE=allow;; deny) APPROVAL_VALUE=deny;; *) APPROVAL_VALUE=ask;; esac
}

# tb approval <サブコマンド>(サービス approval)。古い名前 tb approval <操作> / allow / ask / approve / reject も同じに動く
cmd_approval() {
  if [[ $# -eq 0 ]]; then approval_usage; exit 2; fi   # 空の操作名(tb approval '')は引数なしと分け、check で誤りにする
  case $1 in
    check|allow|ask|approve|reject) local sub=$1; shift; "cmd_approval_$sub" "$@";;
    help|-h|--help) approval_usage;;
    *) cmd_approval_check "$@";;   # 古い形: tb approval <操作>
  esac
}
approval_usage() {
  cat <<'USAGE'
tb approval check <操作> [--by A] [--to 宛先 --text 本文 [--kind K]]   1 行目 allow|ask|deny、2 行目 rule:(終了コード 0=allow 20=ask 4=deny)
tb approval allow <操作のパターン>    今後は許可する(人だけ。設定 policy に * <操作> <対象> = allow の行を書く)
tb approval ask <操作のパターン>      承認制に戻す(人だけ。設定 policy に * <操作> <対象> = ask-any の行を書く)
tb approval approve O-<n> [--always]  承認待ちの送信を届ける
tb approval reject O-<n>              承認待ちの送信を却下する
(古い名前 tb approval <操作> / tb allow / tb ask / tb approve / tb reject も同じに使える)
USAGE
}
cmd_approval_allow() { cmd_allow "$@"; }
cmd_approval_ask() { cmd_ask "$@"; }
cmd_approval_approve() { cmd_approve "$@"; }
cmd_approval_reject() { cmd_reject "$@"; }

# tb approval check <操作> [--to 宛先 --text 本文 [--kind K] [--task T-..]]: 1 行目 allow|ask|deny、2 行目 rule:。終了コード 0=allow / 20=ask / 4=deny
# --to と --text があるときだけ、続けて risk: 行(リスク評価。第 1 段は参考値で、1 行目と終了コードには影響しない)を出す
cmd_approval_check() {
  [[ $# -ge 1 ]] || usage "approval <操作>(例: send:task:worker1, role:change)[--by agent][--to 宛先 --text 本文 [--kind K] [--task T-..]]"
  local act=$1 to="" text="" kind=task have_text=0 by=""; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --by) need_arg "$@"; by=$2; shift 2;;   # その agent が実行するとして(主体の行も見る。無ければ主体 * の行だけ)
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
  case $APPROVAL_VALUE in allow) return 0;; deny) return 4;; *) return 20;; esac
}

# tb allow|ask <パターン>: 人が「今後は許可/承認制に戻す」を決める。設定 policy に「* <操作名> <対象> = allow|ask-any」の行を書く
# (主体・操作名・対象が同じ行があれば書き換え、無ければ末尾に足す)。policy はいちばん厳しい行が勝つので、
# もっと厳しい行が当たる操作は allow にしても変わらない(書いたあとに確かめて、そう言う)
# パターンの分け方: 対象の無い操作(role:change など)は対象 *、send:task:w1 → send:task と w1、agent:* のように語が足りなければ操作名 agent:* と対象 *
policy_row_of_pattern() {  # $1=古い形のパターン → PR_OP PR_TARGET PR_OPS(書く行の操作名。広い glob は中の操作に絞る)
  perm_split_act "$1"; PR_OP=$PS_OP; PR_TARGET=${PS_TARGET:-*}
  policy_ops_of "$PR_OP"; PR_OPS=("${PO_OPS[@]}")
}
# 古い approvals が見ていたのはコマンドの中の操作だけ。cmd:・approve:・agent:flag に当たりうる glob(* や agent:* など)を
# そのまま policy に書くと、前は当たらなかったコマンドや承認の委任まで許してしまうので、中の操作の族に分けて書く
POLICY_INNER_OPS=(send:task send:info send:answer agent:new agent:stop agent:revive agent:spawn agent:compact agent:model agent:read 'agent:slash:*' role:change alert:analyze proxy:read-only proxy:write proxy:external 'setup:*' 'recipe:*')
_po_reserved() {  # $1=操作名の glob が cmd: / approve: / agent:flag に当たりうる → 0
  local pre=${1%%[*?\[]*} r
  if [[ $pre == "$1" ]]; then [[ $1 == cmd:* || $1 == approve:* || $1 == agent:flag ]]; return; fi
  for r in cmd: approve: agent:flag; do [[ $r == "$pre"* || $pre == "$r"* ]] && return 0; done
  return 1
}
# 古い approvals の role:change は人事役(@hr)にだけ効いていた(ほかの agent は grants が要った)。policy では主体を人事役にする
policy_subject_of() { if [[ $1 == role:change ]]; then PO_SUBJ="@${TB_ROLE_ADMIN:-hr}"; else PO_SUBJ='*'; fi; }
policy_ops_of() {  # $1=操作名の glob → PO_OPS
  local f; PO_OPS=()
  if ! _po_reserved "$1"; then PO_OPS=("$1"); return 0; fi
  for f in "${POLICY_INNER_OPS[@]}"; do
    # shellcheck disable=SC2053  # glob として比べる
    [[ $f == $1 ]] && PO_OPS+=("$f")
  done
  return 0
}
set_approval() {  # $1=allow|ask $2=パターン
  local val=$1 pat=$2 eff body line l s o t rest op hit
  local -a done_=()
  valid_action "$pat"
  require_human "tb $val"
  [[ $val == allow ]] && eff=allow || eff=ask-any
  policy_row_of_pattern "$pat"
  ((${#PR_OPS[@]})) || die "tb $val: $pat はコマンドの中の操作に当たりません(コマンド cmd: や承認の委任 approve: は policy に直接書く: tb config edit policy)"
  config_lookup policy || true
  [[ -z $CFG_ERR ]] || die "設定 policy を読めないため書きません($CFG_ERR)"
  body=""
  while IFS= read -r line || [[ -n $line ]]; do
    l=$(trim "${line%%#*}"); read -r s o t rest <<<"${l%%=*}"
    hit=""
    if [[ $l == *=* && $t == "$PR_TARGET" && -z $rest ]]; then
      for op in "${PR_OPS[@]}"; do policy_subject_of "$op"; [[ $s == "$PO_SUBJ" && $o == "$op" ]] && hit=$op; done
    fi
    if [[ -z $hit ]]; then body+="$line"$'\n'; continue; fi
    [[ " ${done_[*]} " == *" $hit "* ]] && continue   # 同じ行が 2 つあれば 1 つにする
    policy_subject_of "$hit"
    body+="$PO_SUBJ $hit $PR_TARGET = $eff   # $(now) に人が変更(tb approval $val)"$'\n'; done_+=("$hit")
  done <<<"$CFG_BODY"
  body=${body%$'\n'}
  for op in "${PR_OPS[@]}"; do
    policy_subject_of "$op"
    [[ " ${done_[*]} " == *" $op "* ]] || body+="${body:+$'\n'}$PO_SUBJ $op $PR_TARGET = $eff   # $(now) に人が追加(tb approval $val)"
  done
  config_put policy "$body" "$val"
  sql "INSERT INTO main.approval_changes (herdr_session, pattern, value, created_at) VALUES ($SQS, $(q "$pat"), $(q "$val"), $(q "$(now)"));" >/dev/null
  for op in "${PR_OPS[@]}"; do policy_subject_of "$op"; echo "$PO_SUBJ $op $PR_TARGET = $eff"; done
  policy_subject_of "$PR_OP"
  if [[ $val == allow && $pat != *[*?]* && $PO_SUBJ == '*' ]]; then   # 書いた行より厳しい行が当たっていないか(主体を問わない判定で確かめる)
    config_forget policy; approval_of "$pat"
    [[ $APPROVAL_VALUE == allow ]] || echo "tb: 注意: もっと厳しい行が当たるため、$pat はまだ $APPROVAL_TIER です($APPROVAL_RULE)" >&2
  fi
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
  [[ $(sql "UPDATE main.outbox SET status='denied', reason=reason||$(q " / $(approver_label)が却下 $(now)"),
         reply_check=CASE WHEN reply_check='ok' THEN $(q "$(approver_label)が却下") ELSE reply_check END WHERE herdr_session=$SQS AND id=$oid AND status='pending'; SELECT changes();") == 1 ]] \
    || die "O-$oid は承認待ちではありません(ほかで決まりました)"
  echo "O-$oid rejected"
}

# ---------- tb policy(サービス policy。認可の表 policy の変換と確かめ。ADR 0007)----------
cmd_policy() {
  case ${1:-help} in
    import) shift; cmd_policy_import "$@";;
    help|-h|--help) policy_usage;;
    *) policy_usage >&2; exit 2;;
  esac
}
policy_usage() {
  cat <<'USAGE'
tb policy import [--apply]   古い設定 approvals と grants を設定 policy の行に変換する(人だけ)。
                             --apply が無ければ、足す行・変換しない行・確かめてほしい行を出すだけ。
                             --apply で policy に足し、古い設定を消す(checkout のファイルは退かす)
USAGE
}

# 2 つのパターン(glob か @<役割> か関係)が同じものに当たりうるか(分からなければ当たりうるとみなす)
_pi_overlap() {
  [[ $1 == "$2" ]] && return 0
  [[ $1 == *[*?@~]* || $2 == *[*?@~]* || $1 == self || $2 == self ]] && return 0
  return 1
}
# policy_convert <approvals の本文> <grants の本文>: PI_ROWS(足す行)、PI_SKIP(変換しない行)、PI_WARN(確かめてほしい行)に入れる
policy_convert() {
  local ab=$1 gb=$2 line l k v n=0 i j d s o t rest
  local -a ks=() ops=() ts=() vs=() ns=() emit=()
  local -a defallow=()
  PI_ROWS=""; PI_SKIP=""; PI_WARN=""
  perm_load
  for i in "${!PERM_OPS_PAT[@]}"; do [[ ${PERM_OPS_VAL[$i]} != allow ]] || defallow+=("${PERM_OPS_PAT[$i]}"); done
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1)); l=$(trim "${line%%#*}"); [[ -n $l ]] || continue
    if [[ $l != *=* ]]; then PI_SKIP+="approvals $n 行目: <操作> = <値> の形ではない: $l"$'\n'; continue; fi
    k=$(trim "${l%%=*}"); v=$(trim "${l#*=}")
    if [[ $k == cmd:* ]]; then PI_SKIP+="approvals $n 行目: cmd: の行は前から効いていない(コマンドの権限は policy に <主体> $k * = <効果> と書く): $l"$'\n'; continue; fi
    case $v in ask|allow) ;; *) PI_SKIP+="approvals $n 行目: 値は ask か allow だけ: $l"$'\n'; continue;; esac
    [[ $k =~ ^[A-Za-z0-9._:*?@-]{1,128}$ ]] || { PI_SKIP+="approvals $n 行目: 操作名に使えない文字がある: $l"$'\n'; continue; }
    policy_row_of_pattern "$k"
    ks+=("$k"); ops+=("$PR_OP"); ts+=("$PR_TARGET"); vs+=("$v"); ns+=("$n"); emit+=(0)
  done <<<"$ab"
  local cnt=${#ks[@]} cover need
  for ((i = 0; i < cnt; i++)); do
    if [[ ${vs[$i]} == allow ]]; then emit[i]=1; continue; fi
    need=0
    # (a) 既定が allow の操作を締める ask(上の行で全部が決まっていなければ)
    for d in "${defallow[@]}"; do
      # shellcheck disable=SC2053  # glob として比べる
      [[ $d == ${ops[$i]} ]] || continue
      cover=0
      for ((j = 0; j < i; j++)); do
        # shellcheck disable=SC2053
        [[ ${ts[$j]} == '*' && $d == ${ops[$j]} ]] && { cover=1; break; }
      done
      ((cover)) || need=1
    done
    # (b) 下に、同じものに当たりうる allow の行がある(上から最初の行で決まっていた ask を残す)
    for ((j = i + 1; j < cnt; j++)); do
      [[ ${vs[$j]} == allow ]] || continue
      _pi_overlap "${ops[$i]}" "${ops[$j]}" && _pi_overlap "${ts[$i]}" "${ts[$j]}" && { need=1; break; }
    done
    emit[i]=$need
  done
  for ((i = 0; i < cnt; i++)); do
    if ((emit[i] == 0)); then PI_SKIP+="approvals ${ns[$i]} 行目: 既定(perms/ops.tbl)と同じなので書かない: ${ks[$i]} = ${vs[$i]}"$'\n'; continue; fi
    v=${vs[$i]}; [[ $v == ask ]] && v=ask-any
    policy_ops_of "${ops[$i]}"
    ((${#PO_OPS[@]})) || { PI_SKIP+="approvals ${ns[$i]} 行目: コマンドの中の操作に当たらない(前から効いていない): ${ks[$i]} = ${vs[$i]}"$'\n'; continue; }
    for d in "${PO_OPS[@]}"; do policy_subject_of "$d"; PI_ROWS+="$PO_SUBJ $d ${ts[$i]} = $v   # approvals ${ns[$i]} 行目: ${ks[$i]} = ${vs[$i]}"$'\n'; done
    [[ ${vs[$i]} == ask ]] || continue
    for ((j = 0; j < i; j++)); do   # 上の allow の行が、下の ask の行に締められる(上から最初の行で決まっていたのが、厳しい方が勝つになる)
      [[ ${vs[$j]} == allow ]] || continue
      _pi_overlap "${ops[$i]}" "${ops[$j]}" && _pi_overlap "${ts[$i]}" "${ts[$j]}" || continue
      PI_WARN+="approvals ${ns[$j]} 行目 ${ks[$j]} = allow は、${ns[$i]} 行目 ${ks[$i]} = ask に当たる相手では ask-any になる(厳しい方が勝つ)"$'\n'
    done
  done
  n=0
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1)); l=$(trim "${line%%#*}"); [[ -n $l ]] || continue
    read -r s o t rest <<<"$l"
    if [[ -z $t || -n $rest ]]; then PI_SKIP+="grants $n 行目: <主体> <操作> <対象> の形ではない: $l"$'\n'; continue; fi
    PI_ROWS+="$s $o $t = allow   # grants $n 行目"$'\n'
    for ((i = 0; i < cnt; i++)); do   # grants は approvals より先に見ていた。締める行(ask-any)に当たる相手では許可にならない
      ((emit[i])) && [[ ${vs[$i]} == ask ]] || continue
      _pi_overlap "$o" "${ops[$i]}" && _pi_overlap "$t" "${ts[$i]}" || continue
      PI_WARN+="grants $n 行目 $s $o $t は、approvals ${ns[$i]} 行目 ${ks[$i]} = ask に当たる相手では許可にならない(厳しい方が勝つ)"$'\n'
    done
  done <<<"$gb"
  return 0
}

cmd_policy_import() {
  local apply=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --apply) apply=1; shift;;
      *) usage "policy import [--apply]";;
    esac
  done
  require_human "tb policy import"
  local k ab="" gb="" srcs=() src
  for k in approvals grants; do
    config_lookup "$k" || { [[ -z $CFG_ERR ]] || die "設定 $k を読めません($CFG_ERR)"; continue; }
    [[ -z $CFG_ERR ]] || die "設定 $k を読めません($CFG_ERR)"
    [[ $k == approvals ]] && ab=$CFG_BODY || gb=$CFG_BODY
    srcs+=("$k=$CFG_SRC")
  done
  if ((${#srcs[@]} == 0)); then echo "古い権限の設定(approvals / grants)はありません。変換するものはありません"; return 0; fi
  # 書く先: 古い設定が全セッション共通(DB の共通の行・checkout のファイル)なら policy も共通の行。このセッションの行ならこのセッションの行
  local shared="" sc
  for src in "${srcs[@]}"; do
    case ${src#*=} in db:\*|file:"$CONFIG_DIR"/*) sc=1;; db:*) sc=0;; *) sc="";; esac   # 環境変数のファイルは、書く先を決めない
    [[ -n $sc ]] || continue
    [[ -z $shared || $shared == "$sc" ]] || die "approvals と grants の置き場所が違います(${srcs[*]})。tb config で同じ置き場所にそろえてから、もう一度実行してください"
    shared=$sc
  done
  [[ -n $shared ]] || shared=0
  policy_convert "$ab" "$gb"
  echo "変換のもと: ${srcs[*]}"
  if [[ -n $PI_ROWS ]]; then echo "policy に足す行:"; printf '%s' "$PI_ROWS" | sed 's/^/  /'; else echo "policy に足す行: なし"; fi
  [[ -z $PI_SKIP ]] || { echo "変換しない行:"; printf '%s' "$PI_SKIP" | sed 's/^/  /'; }
  [[ -z $PI_WARN ]] || { echo "確かめてほしい行(厳しい方が勝つため、前より厳しくなりうる):"; printf '%s' "$PI_WARN" | sed 's/^/  /'; }
  if ((! apply)); then echo "(まだ何も書いていません。--apply で policy に足し、古い設定を消します)"; return 0; fi
  # policy の今の本文(書く先と同じ置き場所のもの)の末尾に足す
  local cur="" body
  config_lookup policy || true
  [[ -z $CFG_ERR ]] || die "設定 policy を読めないため書きません($CFG_ERR)"
  if [[ $CFG_SRC != none ]]; then
    case $CFG_SRC in
      file:*) cur=$CFG_BODY;;   # 環境変数 TB_POLICY のファイル(config_put もそこに書く)
      db:\*) ((shared)) || die "設定 policy は全セッション共通の行にあり、古い設定はこのセッションの行にあります。置き場所をそろえてから実行してください"; cur=$CFG_BODY;;
      db:*) ((! shared)) || die "設定 policy はこのセッションの行にあり、古い設定は全セッション共通です。置き場所をそろえてから実行してください"; cur=$CFG_BODY;;
    esac
  fi
  body=$cur
  [[ -z $PI_ROWS ]] || body+="${body:+$'\n'}# $(now) に tb policy import が approvals / grants から変換した行"$'\n'"${PI_ROWS%$'\n'}"
  if [[ $body != "$cur" ]]; then config_put policy "$body" import "$shared"; echo "policy に足しました"; fi
  # 古い設定を消す(DB の行は消す。checkout のファイルは退かす。環境変数のファイルは消さない)
  local dest="" f
  for src in "${srcs[@]}"; do
    k=${src%%=*}; src=${src#*=}
    case $src in
      db:\*) config_unset "$k" 1; echo "$k: 全セッション共通の行を消しました";;
      db:*) config_unset "$k"; echo "$k: このセッションの行を消しました";;
      file:"$CONFIG_DIR"/*)
        f=${src#file:}
        if [[ -z $dest ]]; then dest=$HERDR_STATE_DIR/config-imported/$(date +%Y%m%d-%H%M%S); mkdir -p "$dest" || die "退かす先を作れません: $dest"; fi
        mv "$f" "$dest/" || die "$f を退かせません(policy には足し済み)"
        echo "$k: $f を $dest/ へ退かしました";;
      *) echo "$k: ${src#file:} は環境変数で指定したファイルなので消しません。環境変数を外してください";;
    esac
  done
}
