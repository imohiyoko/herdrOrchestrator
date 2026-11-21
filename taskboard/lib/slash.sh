# shellcheck shell=bash
# 許可リストにあるスラッシュコマンドを agent のセッションに送る(tb slash)。#94
# bin/tb から source される。単体では実行しない。
#
# - 送ってよいコマンドは config の slash(config/slash.conf。無ければ slash.conf.example)に載っているものだけ。
#   載っていない名前は送らない。方針 deny の行(/model など副作用が大きいもの)も送らない
# - 宛先: agent 名・@<役割>(@a+b も可)・--all。いまの herdr セッションの agent だけ。実行者自身と指揮官(conductor_names)は常に除く
# - 送るのは idle/done の agent だけ。working/blocked などは 1 行ずつ理由を出して飛ばす
# - 既定は dry-run。--apply のときだけ送る
# - 承認: 人の端末からは名前・@役割はそのまま送る。agent の pane からは approvals の agent:slash:<コマンド>:<宛先> を見て、
#   allow なら送り、ask なら宛先ごとに L-<n>(人の承認待ち。tb agent-approve)を積む。
#   --all は誰が実行しても 1 件の L-<n> を積むだけで、人が tb agent-approve するまで何も送らない
# - 送り方は tb compact-check と同じ(slash_deliver: herdr_send → 受け付けの確認)。確認の仕方はコマンドごと(busy / screen)
# - 送った結果は agent_ops(op=slash)に宛先ごとに残す。画面の出力は短い 1 行の状態だけ(本文は残さない)

SLASH_NAME_RE='^[a-z][a-z0-9_-]{0,31}(:[a-z0-9][a-z0-9_-]{0,31})?$'
SLASH_ARGS_RE='^[A-Za-z0-9 ._:/=@,+-]{0,200}$'
SLASH_ERR_RE_DEFAULT='([Uu]nknown (slash )?command|[Ee]rror:|[Ff]ailed)'

# 受け付けを確かめて送る(compact-check と共有)。0=送って受け付けを確認 1=送ったが確認できない 10=宛先が使用中 11=失敗
# $1=宛先 $2=本文 $3=ログ用 ID $4=種別 [$5=確認の仕方 busy(既定) | none]
slash_deliver() {
  local r=0
  herdr_send "$1" "$2" "$3" "$4" >/dev/null 2>&1 || r=$?
  ((r == 0)) || return $r
  [[ ${5:-busy} == busy ]] || return 0
  herdr_verify_prompt "$1" || return 1
}

# 許可リストの 1 行を探す。見つかれば 0 で SL_ARGS / SL_POLICY / SL_CHECK / SL_OK / SL_NOTE を入れる。無ければ 1。書式の誤りは 3(SL_ERR)
SL_ARGS=""; SL_POLICY=""; SL_CHECK=""; SL_OK=""; SL_NOTE=""; SL_ERR=""
slash_lookup() {  # $1=コマンド名
  local line n a p c o d
  config_has slash || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $(trim "$line") == \#* || -z $(trim "$line") ]] && continue
    IFS='|' read -r n a p c o d <<<"$line"
    n=$(trim "$n"); [[ $n == "$1" ]] || continue
    a=$(trim "$a"); p=$(trim "$p"); c=$(trim "$c"); o=$(trim "$o"); d=$(trim "${d%%#*}")
    [[ -n $p ]] || p=allow; [[ -n $c ]] || c=busy
    SL_ERR=""
    [[ $n =~ $SLASH_NAME_RE ]] || SL_ERR="名前に使えない文字があります"
    [[ $a =~ $SLASH_ARGS_RE ]] || SL_ERR="固定の引数に使えない文字があります(英数字と空白 . _ : / = @ , + - の 200 字以内)"
    [[ $p == allow || $p == deny ]] || SL_ERR="方針は allow か deny: $p"
    [[ $c == busy || $c == screen ]] || SL_ERR="確認は busy か screen: $c"
    [[ -z $SL_ERR ]] || return 3
    SL_ARGS=$a; SL_POLICY=$p; SL_CHECK=$c; SL_OK=$o; SL_NOTE=$d
    return 0
  done < <(config_text slash)
  return 1
}

# 送った後の画面を見る。0=問題なし 1=失敗(SLASH_WHY に短い理由)
SLASH_WHY=""
slash_screen_check() {  # $1=pane $2=成功の印(ERE。空なら見ない)
  local out bad
  SLASH_WHY=""
  sleep "${TB_SLASH_SCREEN_SEC:-2}"
  if ! out=$(herdr_call agent read "$1" --source recent --lines "${TB_SLASH_SCREEN_LINES:-15}" --format text 2>/dev/null); then
    SLASH_WHY="画面を読めない"; return 1
  fi
  bad=$(grep -E -m 1 -- "${TB_SLASH_ERROR_RE:-$SLASH_ERR_RE_DEFAULT}" <<<"$out" || true)
  if [[ -n $bad ]]; then SLASH_WHY="エラーの行: $(oneline "$bad" 120)"; return 1; fi
  if [[ -n $2 ]] && ! grep -Eq -- "$2" <<<"$out"; then SLASH_WHY="成功の印が画面に無い"; return 1; fi
  return 0
}

# 1 体に送って記録する。SLASH_LINE に 1 行の結果。0=送った 1=失敗 10=使用中で送らず
SLASH_LINE=""
slash_send_one() {  # $1=宛先 $2=コマンド名 $3=依頼者 [$4=承認待ちの id]
  local name=$1 cmd=$2 by=$3 lid=${4:-} text pane r=0 args msg
  text="/$cmd${SL_ARGS:+ $SL_ARGS}"
  args=$(jq -nc --arg c "$cmd" --arg a "$SL_ARGS" '{command:$c, args:$a}')
  # 承認待ちの行は、承認時の許可リストで実際に送る引数に書き換える(承認待ちの間に固定の引数が変わりうる)
  [[ -z $lid ]] || sql "UPDATE main.agent_ops SET args=$(q "$args") WHERE herdr_session=$SQS AND id=$lid;"
  pane=$(jq -r --arg n "$name" '[.[] | select(.name==$n)][0].pane_id // ""' <<<"$LIVE_AGENTS")
  if [[ $SL_CHECK == screen ]]; then slash_deliver "$name" "$text" "slash:$cmd:$name" slash none || r=$?
  else slash_deliver "$name" "$text" "slash:$cmd:$name" slash busy || r=$?; fi
  case $r in
    0) if [[ $SL_CHECK == screen || -n $SL_OK ]] && ! slash_screen_check "${pane:-$name}" "$SL_OK"; then r=1; msg="failed($SLASH_WHY)"
       else msg=sent; fi;;
    1) msg="failed(処理の開始を確認できない。pane を見てください)";;
    10) record_op slash "$name" "$args" "$by" 1 "$(oneline "$text: 送らず(送る直前に宛先が使用中)" 200)${lid:+ / $(approver_label)が承認}" "$lid"
        SLASH_LINE="$name skip(busy)"; return 10;;
    *) msg="failed(送信に失敗)"; r=1;;
  esac
  record_op slash "$name" "$args" "$by" "$r" "$(oneline "$text: $msg" 200)${lid:+ / $(approver_label)が承認}" "$lid"
  SLASH_LINE="$name $msg"
  return $r
}

# 宛先を名前の一覧(1 行 1 体。除く理由があれば "<名前>\t<理由>")にする。$1=--all なら 1、以降=名前か @役割
slash_resolve() {
  local all=$1 self=$2 t name st conductors seen=" " ck; shift 2
  conductors=" $(conductor_names | tr '\n' ' ') "
  local -a names=()
  while IFS= read -r name; do [[ -n $name ]] && names+=("$name"); done < <(jq -r '.[] | .name // empty' <<<"$LIVE_AGENTS")
  _sl_emit() {  # $1=名前
    [[ $seen != *" $1 "* ]] || return 0; seen+="$1 "
    if [[ $1 == "$self" || $conductors == *" $1 "* ]]; then printf '%s\t%s\n' "$1" "skip(excluded)"; return 0; fi
    st=$(jq -r --arg n "$1" '[.[] | select(.name==$n)][0].agent_status // ""' <<<"$LIVE_AGENTS")
    case $st in idle|done) ;; *) printf '%s\t%s\n' "$1" "skip(${st:-unknown})"; return 0;; esac
    ck=$(sql "SELECT COALESCE(kind,'$KIND_DEFAULT') FROM agents WHERE name=$(q "$1");")
    # 台帳に無い個体(名前が台帳の規則に合わない解析役など)は kind を確かめられないので送らない
    [[ -n $ck ]] || { printf '%s\t%s\n' "$1" "not-supported(kind unknown: not in the agents ledger)"; return 0; }
    kind_supports "$ck" slash || { printf '%s\t%s\n' "$1" "not-supported(kind=$ck)"; return 0; }
    printf '%s\t\n' "$1"
  }
  if ((all)); then
    for name in ${names[@]+"${names[@]}"}; do _sl_emit "$name"; done
    return 0
  fi
  for t in "$@"; do
    if [[ $t == @* ]]; then
      local hit=0 m
      for name in ${names[@]+"${names[@]}"}; do
        m=0; name_matches "$name" "$t" || m=$?
        ((m == 0)) && { hit=1; _sl_emit "$name"; }
      done
      ((hit)) || printf '%s\t%s\n' "$t" "skip(no agent with this role)"
    else
      if printf '%s\n' ${names[@]+"${names[@]}"} | grep -qxF -- "$t"; then _sl_emit "$t"
      else printf '%s\t%s\n' "$t" "skip(not found in herdr session '$SESS')"; fi
    fi
  done
}

# tb slash --list: 許可リストを出す
slash_list() {
  local line n a p c o d
  config_has slash || { echo "tb slash: 許可リストがありません(config/slash.conf)"; return 0; }
  printf 'name\targs\tpolicy\tcheck\tnote\n'
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $(trim "$line") == \#* || -z $(trim "$line") ]] && continue
    IFS='|' read -r n a p c o d <<<"$line"; n=$(trim "$n")
    if slash_lookup "$n"; then printf '/%s\t%s\t%s\t%s\t%s\n' "$n" "$SL_ARGS" "$SL_POLICY" "$SL_CHECK" "$SL_NOTE"
    else printf '/%s\t-\tinvalid\t-\t%s\n' "$n" "$SL_ERR"; fi
  done < <(config_text slash)
}

cmd_slash() {
  local cmd="" apply=0 all=0 r t name why rc=0 id
  local -a targets=()
  while [[ $# -gt 0 ]]; do
    case $1 in
      --list) [[ $# -eq 1 && -z $cmd ]] || usage "slash --list は単独で"; slash_list; return 0;;
      --apply) apply=1; shift;;
      --dry-run) apply=0; shift;;
      --all) all=1; shift;;
      -*) usage "slash: 不明な引数 $1";;
      *) if [[ -z $cmd ]]; then cmd=${1#/}; else targets+=("$1"); fi; shift;;
    esac
  done
  [[ -n $cmd ]] || usage "slash <コマンド名> <agent 名|@役割>... [--all] [--apply] / slash --list"
  [[ $cmd =~ $SLASH_NAME_RE ]] || usage "slash: コマンド名に使えない文字があります: $cmd"
  ((all)) && ((${#targets[@]} > 0)) && usage "slash: --all と宛先は一緒に指定できません"
  ((all || ${#targets[@]} > 0)) || usage "slash: 宛先(agent 名・@役割・--all)が必要です"
  for t in ${targets[@]+"${targets[@]}"}; do
    [[ $t =~ $AGENT_NAME_RE || $t =~ ^@[A-Za-z0-9._*?+-]{1,64}$ ]] || usage "slash: 宛先は agent 名か @<役割>: $t"
  done
  r=0; slash_lookup "$cmd" || r=$?
  case $r in
    0) ;;
    3) die "slash: 許可リストの /$cmd の行が正しくありません: $SL_ERR";;
    *) die "slash: /$cmd は許可リスト(config/slash.conf)にありません。送りません";;
  esac
  [[ $SL_POLICY == allow ]] || die "slash: /$cmd は許可リストで deny(既定で拒否)です。送りません${SL_NOTE:+($SL_NOTE)}"
  requester
  agent_sync || die "herdr agent list に失敗しました"
  # --all は誰が実行しても人の承認待ちにする(承認後に宛先を選び直す)
  if ((all)); then
    if ((! apply)); then
      echo "dry-run: --all は --apply で人の承認待ち(L-<n>)になり、承認後に次の宛先へ送ります"
      while IFS=$'\t' read -r name why; do echo "$name ${why:-would-send(after approval)}"; done < <(slash_resolve 1 "$REQ_BY")
      return 0
    fi
    id=$(sql "INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at)
      VALUES ($SQS, 'slash', '*', $(q "$(jq -nc --arg c "$cmd" --arg a "$SL_ARGS" '{command:$c, args:$a, all:1}')"), $(q "$REQ_BY"), 'pending', 'slash --all は常に人の承認', $(q "$(now)"));
      SELECT last_insert_rowid();")
    echo "L-$id pending (/$cmd を --all で送る。人の承認待ち: tb agent-approve L-$id)"
    return 20
  fi
  while IFS=$'\t' read -r name why; do
    [[ -n $name ]] || continue
    if [[ -n $why ]]; then echo "$name $why"; continue; fi
    if [[ $REQ_BY != human ]]; then
      approval_of "agent:slash:$cmd:$name" "$REQ_BY"
      if [[ $APPROVAL_VALUE != allow ]]; then
        if ((! apply)); then echo "$name would-ask(approval: agent:slash:$cmd:$name is ask)"; continue; fi
        id=$(sql "INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at)
          VALUES ($SQS, 'slash', $(q "$name"), $(q "$(jq -nc --arg c "$cmd" --arg a "$SL_ARGS" '{command:$c, args:$a}')"), $(q "$REQ_BY"), 'pending', $(q "approvals: $APPROVAL_RULE"), $(q "$(now)"));
          SELECT last_insert_rowid();")
        echo "$name pending L-$id (人の承認待ち: tb agent-approve L-$id)"
        ((rc == 0 || rc == 20)) && rc=20
        continue
      fi
    fi
    if ((! apply)); then echo "$name would-send(dry-run)"; continue; fi
    r=0; slash_send_one "$name" "$cmd" "$REQ_BY" || r=$?
    echo "$SLASH_LINE"
    ((r == 1)) && rc=11
  done < <(slash_resolve 0 "$REQ_BY" ${targets[@]+"${targets[@]}"})
  return $rc
}

# tb agent-approve から呼ぶ(op=slash の承認待ち)。$1=id $2=行の JSON $3=--always なら 1
slash_approve() {
  local id=$1 row=$2 always=$3 target args cmd by name why out="" r=0 rc=0 n=0
  target=$(jq -r .target <<<"$row"); args=$(jq -r .args <<<"$row"); by=$(jq -r .requested_by <<<"$row")
  cmd=$(jq -r '.command // ""' <<<"$args")
  ((always == 0)) || [[ $target != '*' ]] || die "L-$id: --all の承認に --always は使えません"
  op_claim "$id"   # 同時に承認しても送るのは 1 回(lib/agents.sh)
  # 承認待ちの間に許可リストが変わっていたら、今の行に従う(外された・deny になったら送らない)
  r=0; slash_lookup "$cmd" || r=$?
  if ((r)) || [[ $SL_POLICY != allow ]]; then
    record_op slash "$target" "$args" "$by" 1 "/$cmd は許可リストに無いか deny になったため送らない / $(approver_label)が承認" "$id"
    echo "tb: L-$id: /$cmd は許可リストに無いか deny です。送りません" >&2; return 1
  fi
  if ((always)); then
    set_approval allow "agent:slash:$cmd:$target" >/dev/null && echo "agent:slash:$cmd:$target = allow(今後は承認なし)"
  fi
  agent_sync || die "herdr agent list に失敗しました"
  if [[ $target == '*' ]]; then
    # 承認の行には要約を、送った宛先ごとの行は別に残す
    while IFS=$'\t' read -r name why; do
      [[ -n $name ]] || continue
      if [[ -n $why ]]; then echo "$name $why"; continue; fi
      r=0; slash_send_one "$name" "$cmd" "$by" || r=$?
      echo "$SLASH_LINE"; ((r == 0)) && n=$((n + 1)); ((r == 1)) && rc=11
    done < <(slash_resolve 1 "$by")
    record_op slash '*' "$args" "$by" "$((rc ? 1 : 0))" "/$cmd を --all で $n 体に送った / 人が承認" "$id"
    return $rc
  fi
  while IFS=$'\t' read -r name why; do
    [[ -n $name ]] || continue
    if [[ -n $why ]]; then
      record_op slash "$target" "$args" "$by" 1 "$why / $(approver_label)が承認" "$id"; echo "tb: L-$id: $name $why" >&2; return 1
    fi
    r=0; slash_send_one "$name" "$cmd" "$by" "$id" || r=$?
    out=$SLASH_LINE
  done < <(slash_resolve 0 "$by" "$target")
  echo "L-$id: $out"
  ((r == 0)) || return 11
}
