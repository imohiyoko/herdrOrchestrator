# shellcheck shell=bash
# agent のモデル: 役割ごとの既定(config の role-models)と、名前・役割・全体を指定した切り替え(tb model-set)。
# bin/tb から source される。単体では実行しない。設計と検証の手順は taskboard/docs/model-switch.md
#
# モデルの決まり方(起動・復活のとき。上が勝つ):
#   1. 明示の --model(tb agent-new / agent-revive / model-set)
#   2. 台帳の model(明示の指定で書いた値。tb model-set <対象> --reset で消す)
#   3. 役割の既定(config の role-models。<役割の glob> = <モデル>。上から見て最初に一致した行)
#   4. agent の CLI 自身の既定
# 役割の既定は台帳に書かない(あとで role-models を変えても、台帳に値がある agent は変わらない)。
# 切り替えは、idle / done の agent を止めて、同じ会話を --model 付きで復活させる。ユーザー単位の設定(~/.claude/settings.json など)は触らない。
# Claude Code の会話中の /model は、選んだモデルをユーザー単位の既定として保存し、以後に起動するすべての agent に効いてしまうので使わない。

# role-models の本文を検査する(tb config set / edit のときに呼ばれる)。問題があれば理由を出して 1
config_validate_role_models() {  # $1=本文
  local line k v n=0 bad=""
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    if [[ $line != *=* ]]; then bad+="${n} 行目: <役割の glob> = <モデル> の形ではありません"$'\n'; continue; fi
    k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
    [[ $k =~ ^[A-Za-z0-9*?._-]{1,64}$ ]] || bad+="${n} 行目: 役割の glob に使えない文字があります: $k"$'\n'
    [[ $v =~ $MODEL_RE ]] || bad+="${n} 行目: モデル名に使えない文字があります: $v"$'\n'
  done <<<"$1"
  [[ -z $bad ]] && return 0
  printf '%s' "$bad" >&2; return 1
}

# 役割の既定モデル(無ければ空)。$1=agent 名 [$2=台帳の role]
role_model_for() {
  config_has role-models || return 0
  local roles line k v r; local -a have
  roles=$(role_name_of "$@" 2>/dev/null) || return 0
  [[ -n $roles ]] || return 0
  IFS=, read -ra have <<<"$roles"
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ $line == *=* ]] || continue
    k=$(trim "${line%%=*}"); v=$(trim "${line#*=}")
    [[ $v =~ $MODEL_RE ]] || continue
    for r in "${have[@]}"; do
      # shellcheck disable=SC2053  # glob として比べる
      [[ $r == $k ]] && { printf '%s' "$v"; return 0; }
    done
  done < <(config_text role-models)
}

# 起動・復活に使うモデル。明示・台帳の値が空なら役割の既定(その kind がモデル指定に対応しているときだけ)
# $1=明示または台帳の値 $2=kind $3=agent 名 [$4=台帳の role]
model_with_role_default() {
  if [[ -n $1 ]]; then printf '%s' "$1"; return 0; fi
  kind_supports "$2" model || return 0
  role_model_for "$3" ${4:+"$4"}
}

MODEL_EMPTY="{}"
MODEL_CACHE_NOTE="注意: モデルを変えると、その会話のプロンプトキャッシュは使えなくなります(切り替えた直後の 1 回は、会話の全体を読み直す分の費用がかかります)"

# 1 体を切り替える(承認は確かめ済み)。OP_OUT に 1 行の結果。0=changed 3=skipped 11=herdr の失敗 12=not supported
do_model_set() {  # $1=名前 $2=モデル $3=依頼者
  local name=$1 model=$2 by=$3 row reg kind status pane sess cwd old rr=0
  agent_sync || { OP_OUT="herdr agent list に失敗しました"; return 11; }   # 承認後の実行でも、今の状態で判断する
  reg=$(agent_reg_row "$name"); row=$(live_row "$name")
  [[ -n $reg || -n $row ]] || { other_session "$name" || OP_OUT="$name は台帳にありません"; return 3; }
  kind=$(jq -r --arg d "$KIND_DEFAULT" '.kind // $d' <<<"${reg:-$MODEL_EMPTY}")
  kind_model_switch "$kind" >/dev/null || { OP_OUT="not supported (kind=$kind はモデルの切り替えに対応していません)"; return 12; }
  kind_model_valid "$kind" "$model" || { OP_OUT="not supported (kind=$kind のモデル名として使えません: $model)"; return 12; }
  old=$(jq -r '.model // ""' <<<"${reg:-$MODEL_EMPTY}")
  if [[ -z $row ]]; then
    # 止まっている: 台帳の値だけを変える(次の tb agent-revive から効く)
    sql "UPDATE main.agents SET model=$(q "$model"), updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND name=$(q "$name");"
    OP_OUT="changed (${old:-既定} -> ${model}。停止中のため、次の tb agent-revive $name から効きます)"; return 0
  fi
  status=$(jq -r '.agent_status // ""' <<<"$row")
  case $status in idle|done) ;; *) OP_OUT="skipped (${status:-状態不明}。idle か done のときだけ切り替えます)"; return 3;; esac
  [[ $old != "$model" ]] || { OP_OUT="skipped (すでに $model)"; return 3; }
  pane=$(jq -r '.pane_id // ""' <<<"$row"); sess=$(jq -r '.agent_session.value // ""' <<<"$row")
  [[ -n $sess ]] || sess=$(jq -r '.session_id // ""' <<<"${reg:-$MODEL_EMPTY}")
  cwd=$(jq -r '.cwd // ""' <<<"${reg:-$MODEL_EMPTY}")
  [[ -n $pane ]] || { OP_OUT="skipped (pane が分かりません)"; return 3; }
  [[ -n $sess ]] || { OP_OUT="skipped (session id が台帳にありません。同じ会話に戻れないので止めません)"; return 3; }
  [[ -n $cwd && -d $cwd ]] || { OP_OUT="skipped (作業ディレクトリがありません: ${cwd:-未記録}。同じ会話に戻れないので止めません)"; return 3; }
  agent_close_pane "$name" "$pane" "$sess" || return $?
  agent_sync || true
  do_agent_revive "$name" 0 "$model" "" "$by" || rr=$?
  if ((rr)); then
    OP_OUT="$name を停止しましたが、$model での復活に失敗: ${OP_OUT}。停止のままです。戻すなら: tb agent-revive $name --model $model"
    return 11
  fi
  OP_OUT="changed (${old:-既定} -> ${model}、同じ会話で再開)"
}

# tb model-set <名前 | @役割 | --all> <モデル> [--dry-run] / tb model-set <名前 | @役割 | --all> --reset [--dry-run]
cmd_model-set() {
  local u="model-set <名前 | @役割 | --all> <モデル> [--dry-run] | model-set <名前 | @役割 | --all> --reset [--dry-run]"
  [[ $# -ge 2 ]] || usage "$u"
  local sel=$1 model="" reset=0 dry=0; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --reset) reset=1; shift;;
      --dry-run) dry=1; shift;;
      -*) usage "model-set: 不明な引数 $1";;
      *) [[ -z $model ]] || usage "$u"; model=$1; shift;;
    esac
  done
  ((reset)) && [[ -n $model ]] && usage "model-set: --reset とモデル名は同時に指定できません"
  ((reset)) || [[ -n $model ]] || usage "$u"
  [[ -z $model || $model =~ $MODEL_RE ]] || usage "model-set: モデル名に使えない文字があります: $model"
  local all=0
  case $sel in
    --all) all=1;;
    @*) [[ ${sel#@} =~ ^[A-Za-z0-9*?._+-]{1,64}$ ]] || usage "model-set: @<役割> に使えない文字があります: $sel";;
    *) valid_agent_name "$sel";;
  esac
  requester
  agent_sync || { echo "tb: herdr agent list に失敗しました" >&2; return 11; }
  # 対象を集める(このセッションの台帳と、今動いているもの)
  local names n m
  names=$( { sql "SELECT name FROM agents;"; jq -r '.[] | .name // empty' <<<"$LIVE_AGENTS"; } | grep -E "$AGENT_NAME_RE" | sort -u)
  local -a targets=()
  if [[ $sel == @* ]]; then
    while IFS= read -r n; do
      [[ -n $n ]] || continue
      m=0; name_matches "$n" "$sel" || m=$?
      ((m == 0)) && targets+=("$n")
      ((m == 2)) && { echo "tb: 台帳を読めず、$n の役割を確かめられません" >&2; return 11; }
    done <<<"$names"
  elif ((all)); then
    while IFS= read -r n; do [[ -n $n ]] && targets+=("$n"); done <<<"$names"
  else
    targets=("$sel")
  fi
  ((${#targets[@]})) || { echo "tb: 対象の agent がいません: $sel" >&2; return 1; }
  local conductors args rc=0 r line changed=0 skipped=0 unsup=0 failed=0 pending=0 lid
  conductors=" $(conductor_names | tr '\n' ' ') "
  for n in "${targets[@]}"; do
    if [[ $n == "$REQ_BY" ]]; then echo "$n skipped (自分自身は切り替えません)"; skipped=$((skipped + 1)); continue; fi
    if [[ $conductors == *" $n "* ]]; then echo "$n skipped (指揮官は切り替えません。人が tb agent-revive $n --model M で行う)"; skipped=$((skipped + 1)); continue; fi
    if ((reset)); then
      args=$(jq -nc '{model_set:1, reset:1}')
    else
      args=$(jq -nc --arg m "$model" '{model_set:1, model:$m}')
    fi
    # 先に、承認に回す前に分かる「対応していない」「今は切り替えない」を見る(--dry-run の計画もここで出す)
    r=0; line=$(model_set_precheck "$n" "$model" "$reset") || r=$?
    if ((r)); then
      echo "$n $line"
      case $r in 12) unsup=$((unsup + 1));; *) skipped=$((skipped + 1));; esac
      continue
    fi
    # --all は承認待ちにだけ積む(自動では実行しない)。agent の pane からは agent:model:<名前> が allow のときだけ実行
    local hold=0 why=""
    if ((all)); then hold=1; why="--all は人の承認が要ります"
    elif [[ $REQ_BY != human ]]; then
      approval_of "agent:model:$n" "$REQ_BY"
      [[ $APPROVAL_VALUE == allow ]] || { hold=1; why="approvals: $APPROVAL_RULE"; }
    fi
    if ((dry)); then
      if ((hold)); then echo "$n would hold for approval ($line / $why)"; else echo "$n would $line"; fi
      continue
    fi
    if ((hold)); then
      lid=$(sql "INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at)
        VALUES ($SQS, 'revive', $(q "$n"), $(q "$args"), $(q "$REQ_BY"), 'pending', $(q "$why"), $(q "$(now)")); SELECT last_insert_rowid();")
      echo "$n pending L-$lid (人の承認待ち: tb agent-approve L-$lid)"; pending=$((pending + 1)); continue
    fi
    r=0; model_set_run "$n" "$args" "$REQ_BY" || r=$?
    record_op revive "$n" "$args" "$REQ_BY" "$r" "model-set: $OP_OUT"
    echo "$n $OP_OUT"
    case $r in 0) changed=$((changed + 1));; 3) skipped=$((skipped + 1));; 12) unsup=$((unsup + 1));; *) failed=$((failed + 1));; esac
  done
  ((dry)) && echo "(dry-run: 何も変えていません)"
  ((changed > 0 && ! reset)) && echo "$MODEL_CACHE_NOTE"
  if ((failed)); then rc=11; elif ((pending)); then rc=20; elif ((unsup)); then rc=12
  elif ((changed == 0 && skipped > 0 && ${#targets[@]} == 1)); then rc=1; fi
  return $rc
}

# 承認の前に分かる結果。成功なら 0 で計画("change <旧> -> <新>" など)を出す。だめなら 3(skipped)か 12(not supported)で理由を出す
model_set_precheck() {  # $1=名前 $2=モデル $3=reset
  local name=$1 model=$2 reset=$3 reg row kind old status
  reg=$(agent_reg_row "$name"); row=$(live_row "$name")
  if [[ -z $reg && -z $row ]]; then
    if other_session "$name"; then echo "skipped ($OP_OUT)"; else echo "skipped (台帳にありません)"; fi
    return 3
  fi
  kind=$(jq -r --arg d "$KIND_DEFAULT" '.kind // $d' <<<"${reg:-$MODEL_EMPTY}")
  old=$(jq -r '.model // ""' <<<"${reg:-$MODEL_EMPTY}")
  if ((reset)); then
    [[ -n $old ]] || { echo "skipped (台帳にモデルがありません)"; return 3; }
    echo "reset ($old -> 役割の既定 $(role_model_for "$name" || true))"; return 0
  fi
  kind_model_switch "$kind" >/dev/null || { echo "not supported (kind=$kind はモデルの切り替えに対応していません)"; return 12; }
  kind_model_valid "$kind" "$model" || { echo "not supported (kind=$kind のモデル名として使えません: $model)"; return 12; }
  [[ $old != "$model" ]] || { echo "skipped (すでに $model)"; return 3; }
  if [[ -n $row ]]; then
    status=$(jq -r '.agent_status // ""' <<<"$row")
    case $status in idle|done) ;; *) echo "skipped (${status:-状態不明}。idle か done のときだけ切り替えます)"; return 3;; esac
    echo "change (${old:-既定} -> ${model}、止めて同じ会話で再開)"
  else
    echo "change (${old:-既定} -> ${model}、停止中なので台帳だけ)"
  fi
}

# 承認後にも使う実行の本体。$1=名前 $2=args $3=依頼者。OP_OUT に結果
model_set_run() {
  local name=$1 a=$2 by=$3
  if [[ $(jq -r '.reset // 0' <<<"$a") == 1 ]]; then
    local d
    sql "UPDATE main.agents SET model=NULL, updated_at=$(q "$(now)") WHERE herdr_session=$SQS AND name=$(q "$name");"
    d=$(role_model_for "$name" || true)
    OP_OUT="reset (台帳のモデルを消しました。次の起動・復活から ${d:+役割の既定 $d}${d:-agent の CLI の既定} を使います)"; return 0
  fi
  do_model_set "$name" "$(jq -r '.model // ""' <<<"$a")" "$by"
}
