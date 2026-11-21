# shellcheck shell=bash
# agent の停止・復活・新規作成と「止めてよいか」の判定フラグ
# (agents / agent-new / agent-stop / agent-revive / agent-flag / agent-ops / agent-approve / agent-reject)
# bin/tb から source される。単体では実行しない。
#
# - 台帳は agents テーブル。herdr agent list と突き合わせて running / stopped を持つ(tb agents が毎回同期する)
# - 停止 = その agent の pane を閉じる(herdr に agent 単体の停止は無い)。止める前に session と cwd を台帳に残す
# - 復活 = 同じ名前で新しい workspace を作り、claude --resume <session> で同じ会話から再開する(--fresh なら新しい会話)
# - kind(claude / codex / gemini)ごとの違い(起動の引数・モデル指定・再開)は lib/kinds.sh のアダプタだけが知っている。
#   ここでは kind_* に聞き、対応していない項目は終了コード 12 で止める(黙って claude の引数を渡さない)
#   役割は SessionStart hook(tb role-context)が渡す。--role で決めた役割は台帳に残り、復活後も同じものが渡る
# - 判定フラグ killable = yes|no|unknown。付けるのは人か判定役(TB_SWEEPER_PATTERN、既定 sweeper*)か、grants で agent:flag を許された agent だけ。
#   yes を付けた後にその agent が新しい入力を受けたら stale(付け直すまで yes として扱わない)
# - 保護(config の protect): 一致した agent は killable が protected になり、tb agent-stop は --force でも止めない。
#   agent-shed の候補にも出さない。外すのは人が tb config edit protect で行う(再起動は止めずに戻るので対象外)
# - 人の端末からはそのまま実行する。agent の pane からは approvals の agent:<op>:<名前> を見て、ask なら L-<n> で人の承認待ち

AGENT_NAME_RE='^[a-z][a-z0-9_-]{0,31}$'   # herdr agent start の名前の規則
MODEL_RE='^[A-Za-z0-9._:-]{1,64}$'
ROLE_RE='^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'
SWEEPER_PAT=${TB_SWEEPER_PATTERN:-sweeper*}
AGENT_CWD=${TB_AGENT_CWD:-$TB_ROOT/..}
OPEN_TASK_STATUSES="'in_progress','review','blocked'"

valid_agent_name() { [[ $1 != human && $1 =~ $AGENT_NAME_RE ]] || usage "agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): $1"; }
valid_lid() { [[ $1 =~ ^L-[0-9]+$ ]] || usage "操作 ID は L-<数字>: $1"; }
agent_reg_row() { session_row agents name "$1"; }

# 今動いている agent の一覧(JSON 配列)を LIVE_AGENTS に入れる。取れなければ 1
LIVE_AGENTS="[]"
load_live() {
  local out
  out=$(herdr_call agent list 2>/dev/null) && LIVE_AGENTS=$(jq -c '[.result.agents[]? | select(type=="object")]' <<<"$out" 2>/dev/null) \
    || { LIVE_AGENTS="[]"; return 1; }
}
live_row() { jq -c --arg n "$1" '[.[] | select(.name==$n)][0] // empty' <<<"$LIVE_AGENTS"; }

# 台帳に登録する(新しい命のたびに判定フラグは付け直し)。$1=名前 $2=cwd $3=model $4=role $5=作った人 [$6=killable $7=理由 $8=kind(既定 claude)]
agent_register() {
  local name=$1 cwd=$2 model=$3 role=$4 by=$5 kill=${6:-unknown} why=${7:-} kind=${8:-$KIND_DEFAULT} ts; ts=$(now)
  sql "INSERT INTO main.agents (name, kind, herdr_session, cwd, model, role, state, killable, killable_reason, killable_by, killable_at, created_by, created_at, updated_at)
    VALUES ($(q "$name"), $(q "$kind"), $SQS, NULLIF($(q "$cwd"),''), NULLIF($(q "$model"),''), NULLIF($(q "$role"),''), 'running',
            $(q "$kill"), NULLIF($(q "$why"),''), CASE WHEN $(q "$kill")='unknown' THEN NULL ELSE $(q "$by") END,
            CASE WHEN $(q "$kill")='unknown' THEN NULL ELSE $(q "$ts") END, $(q "$by"), $(q "$ts"), $(q "$ts"))
    ON CONFLICT(herdr_session, name) DO UPDATE SET state='running', stopped_at=NULL, pane_id=NULL, kind=excluded.kind,
      cwd=COALESCE(excluded.cwd, cwd), model=COALESCE(excluded.model, model), role=COALESCE(excluded.role, role),
      killable=excluded.killable, killable_reason=excluded.killable_reason, killable_by=excluded.killable_by,
      killable_at=excluded.killable_at, killable_session=NULL, updated_at=excluded.updated_at;"
}

# herdr の一覧と台帳を突き合わせる。動いていれば running(pane / session / cwd を更新)、消えていれば stopped
agent_sync() {
  load_live || return 1
  local ts rows; ts=$(now)
  rows=$(jq -r '.[] | select((.name // "") != "") | [.name, (.pane_id // ""), (.agent_session.value // ""), (.foreground_cwd // .cwd // ""), (.agent // "")]
    | map(tostring | gsub("[\u001f\n]"; " ")) | join("\u001f")' <<<"$LIVE_AGENTS")
  local name pane sess cwd akind stmts="" names="''"
  while IFS=$'\x1f' read -r name pane sess cwd akind; do
    names+=",$(q "$name")"   # 台帳の規則に合わない名前(解析役 analyst-A<n> など)も「いる」ものとして扱い、勝手に stopped にしない
    [[ $name =~ $AGENT_NAME_RE ]] || continue
    kind_valid "$akind" || akind=""   # herdr が教える kind が既知のときだけ採用する。空・未知なら、既存の行は保持し、新規の行は既定にする
    # 個体が変わった判定は、ここ 1 か所: herdr が既知の kind を教え、台帳の kind と違うとき(同名の別個体が外部で起動された)。
    # そのとき旧い個体の持ち物(kind / model / session)をまとめて入れ替える。model は herdr から分からないので消し、
    # session は herdr が教えるものだけ(教えなければ消す)。同じ個体(kind が同じ・教えない・未知)は、従来どおり保持する
    chg="($(q "$akind")!='' AND $(q "$akind")!=kind)"
    stmts+="INSERT INTO main.agents (name, kind, herdr_session, pane_id, session_id, cwd, state, killable, created_by, created_at, updated_at)
      VALUES ($(q "$name"), $(q "${akind:-$KIND_DEFAULT}"), $SQS, NULLIF($(q "$pane"),''), NULLIF($(q "$sess"),''),
              COALESCE((SELECT cwd FROM main.agent_activity WHERE session_id=$(q "$sess") AND cwd IS NOT NULL), NULLIF($(q "$cwd"),'')),
              'running', 'unknown', 'herdr', $(q "$ts"), $(q "$ts"))
      ON CONFLICT(herdr_session, name) DO UPDATE SET state='running', stopped_at=NULL, pane_id=excluded.pane_id,
        kind=CASE WHEN $chg THEN $(q "$akind") ELSE kind END,
        model=CASE WHEN $chg THEN NULL ELSE model END,
        session_id=CASE WHEN $chg THEN excluded.session_id ELSE COALESCE(excluded.session_id, session_id) END,
        cwd=COALESCE(excluded.cwd, cwd), updated_at=$(q "$ts");"
  done <<<"$rows"
  sql "BEGIN; $stmts
    UPDATE main.agents SET state='stopped', stopped_at=$(q "$ts"), updated_at=$(q "$ts") WHERE herdr_session=$SQS AND state='running' AND name NOT IN ($names);
    COMMIT;"
}

# このセッションの台帳に無い名前が、別の herdr セッションの台帳にあるなら 1(OP_OUT にセッション違いのヒント)。
# 台帳はセッションごとに分かれているので、別のセッションの agent には触らない。ここで見るのはヒントのためだけ
other_session() {  # $1=名前
  local s; s=$(sql "SELECT herdr_session FROM main.agents WHERE name=$(q "$1") AND herdr_session!=$SQS
    AND NOT EXISTS (SELECT 1 FROM agents WHERE name=$(q "$1")) ORDER BY updated_at DESC LIMIT 1;")
  [[ -n $s ]] || return 1
  OP_OUT="$1 は herdr セッション '$s' の agent です。HERDR_SESSION=$s を付けて実行してください"
}

# protect の本文を検査する(tb config set / edit のときに呼ばれる)。問題があれば理由を出して 1
# 1 行 1 つ: <名前の glob> [= 理由] または role:<役割の glob> [= 理由]。# 以降はコメント
config_validate_protect() {  # $1=本文
  local line k n=0 bad=""
  while IFS= read -r line || [[ -n $line ]]; do
    n=$((n + 1))
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    k=$(trim "${line%%=*}")
    [[ ${k#role:} =~ ^[A-Za-z0-9*?._-]{1,64}$ ]] || bad+="${n} 行目: <名前の glob> か role:<役割の glob> の形ではありません: $k"$'\n'
  done <<<"$1"
  [[ -z $bad ]] && return 0
  printf '%s' "$bad" >&2; return 1
}

# 保護されていれば 0 で理由(書いていなければ一致した行の glob)を出す。されていなければ 1
protect_reason() {  # $1=agent 名
  config_has protect || return 1
  local line k v roles="" r rl=0; local -a have=()
  while IFS= read -r line || [[ -n $line ]]; do
    line=$(trim "${line%%#*}"); [[ -n $line ]] || continue
    k=$(trim "${line%%=*}"); v=""; [[ $line != *=* ]] || v=$(trim "${line#*=}")
    if [[ $k == role:* ]]; then
      if ((rl == 0)); then rl=1; roles=$(role_name_of "$1" 2>/dev/null) || roles=""; IFS=, read -ra have <<<"$roles"; fi
      for r in "${have[@]}"; do
        # shellcheck disable=SC2053  # glob として比べる
        [[ $r == ${k#role:} ]] && { printf '%s' "${v:-protect: $k}"; return 0; }
      done
    else
      # shellcheck disable=SC2053  # glob として比べる
      [[ $1 == $k ]] && { printf '%s' "${v:-protect: $k}"; return 0; }
    fi
  done < <(config_text protect)
  return 1
}

# 判定フラグの実効値: protected|yes|no|unknown|stale
killable_of() {
  protect_reason "$1" >/dev/null && { echo protected; return 0; }
  sql "SELECT CASE WHEN killable!='yes' THEN killable
      WHEN killable_session IS NOT NULL AND session_id IS NOT NULL AND killable_session!=session_id THEN 'stale'
      WHEN EXISTS (SELECT 1 FROM main.agent_activity aa WHERE (aa.session_id=agents.session_id OR (aa.pane_id=agents.pane_id AND aa.herdr_session=agents.herdr_session))
                   AND julianday(aa.last_prompt_at) > julianday(agents.killable_at)) THEN 'stale'
      ELSE 'yes' END FROM agents WHERE name=$(q "$1");"
}

# 実行してよいか。人なら 0。agent なら approvals の agent:<op>:<名前>: allow → 0、ask → L-<n> を積んで 20
authorize_op() {  # $1=op $2=対象 $3=引数の JSON
  requester
  [[ $REQ_BY == human ]] && return 0
  # 作成は、名前から推定した役割ではなく要求された役割(--role)で @<役割> を判定する
  if [[ $1 == new && -n $(jq -r '.role // ""' <<<"$3") ]]; then ROLE_HINT_NAME=$2; ROLE_HINT=$(jq -r '.role' <<<"$3"); fi
  approval_of "agent:$1:$2" "$REQ_BY"
  ROLE_HINT_NAME=""; ROLE_HINT=""
  [[ $APPROVAL_VALUE == allow ]] && return 0
  local id
  id=$(sql "INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at)
    VALUES ($SQS, $(q "$1"), $(q "$2"), $(q "$3"), $(q "$REQ_BY"), 'pending', $(q "approvals: $APPROVAL_RULE"), $(q "$(now)"));
    SELECT last_insert_rowid();")
  echo "L-$id pending (人の承認待ち: tb agent-approve L-$id)"
  return 20
}

# 実行した操作を残す(人の直接実行も、承認後の実行も)。$1=op $2=対象 $3=args $4=依頼者 $5=rc $6=結果 [$7=既存の id]
record_op() {
  local st=done; [[ $5 == 0 ]] || st=failed
  if [[ -n ${7:-} ]]; then
    sql "UPDATE main.agent_ops SET status='$st', result=$(q "$6"), decided_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$7;"
  else
    sql "INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at)
      VALUES ($SQS, $(q "$1"), $(q "$2"), $(q "$3"), $(q "$4"), '$st', $(q "$6"), $(q "$(now)"), $(q "$(now)"));"
  fi
}

# 起動直後の agent に最初の指示を送る(idle になるまで少し待つ)
# 起動直後は herdr が idle と言っても Claude の UI が未準備で、Enter が失われることがある。
# 準備完了(interactive_ready)を待ってから送り、送ったあと動き出したかを確かめる。
# 0=送って動き出した 10=送れていない、または送ったが処理を確認できない(FP_NOTE に理由。本文は再送しない)
FP_NOTE=""
first_prompt() {  # $1=名前 $2=本文 $3=依頼者
  local i sent=0
  FP_NOTE=""
  if ! herdr_wait_ready "$1"; then
    FP_NOTE="最初の指示は未送信(${TB_SPAWN_READY_SEC:-60} 秒待っても入力待ちになりません。tb send で送り直す)"
    echo "tb: $1 が入力待ちにならないため最初の指示を送っていません" >&2; return 10
  fi
  for i in 1 2 3 4 5; do
    if herdr_send "$1" "[from: tb / agent / $3] $(oneline "$2" 4000)" "agent:$1" agent 2>/dev/null; then sent=1; break; fi
    sleep "${TB_SPAWN_RETRY_SEC:-3}"
  done
  if ((! sent)); then FP_NOTE="最初の指示は未送信(tb send などで送り直す)"; return 10; fi
  if ! herdr_verify_prompt "$1"; then
    FP_NOTE="最初の指示は送ったが処理が始まったか確認できません(二重になるので再送せず、${1} の pane で Enter を押してください)"
    echo "tb: $1 に送ったが処理を確認できません。pane で Enter を押してください" >&2; return 10
  fi
  return 0
}

# kind が、要求された起動(モデル指定・会話の再開)に対応しているか。だめなら OP_OUT に理由を入れて 12
kind_check_launch() {  # $1=kind $2=モデル(空なら無指定) $3=再開する session(空なら新しい会話)
  kind_require "$1" launch || { OP_OUT=$KIND_ERR; return 12; }
  if [[ -n $2 ]]; then
    kind_require "$1" model "--model $2" || { OP_OUT=$KIND_ERR; return 12; }
    kind_model_valid "$1" "$2" || { OP_OUT="kind=$1 のモデル名として使えません item=model: $2"; return 12; }
  fi
  if [[ -n $3 ]]; then
    kind_require "$1" resume "session $3 で再開できない。新しい会話にするなら --fresh" || { OP_OUT=$KIND_ERR; return 12; }
  fi
  return 0
}

# ---------- 実体(引数は検査済み。結果を OP_OUT に入れて 0 / 1 / 11 / 12 を返す) ----------
OP_OUT=""
do_agent_new() {  # name cwd model role prompt killable by [spawned_by spawn_task](tb agent-spawn のときだけ) [kind]
  local name=$1 cwd=$2 model=$3 role=$4 prompt=$5 kill=$6 by=$7 sby=${8:-} stask=${9:-} kind=${10:-$KIND_DEFAULT}
  kind_check_launch "$kind" "$model" "" || return 12
  # 承認待ちの間に roles/<名前>.md が作られていたら、承認した役割と作成後の役割がずれるので作らない
  ! role_conflict "$name" "$role" || { OP_OUT=$ROLE_CONFLICT_WHY; return 1; }
  agent_sync || { OP_OUT="herdr agent list に失敗しました"; return 11; }
  [[ -z $(live_row "$name") ]] || { OP_OUT="$name はすでに動いています"; return 1; }
  [[ $(sql "SELECT state FROM agents WHERE name=$(q "$name");") != stopped ]] \
    || { OP_OUT="停止中の $name が台帳にあります。tb agent-revive $name で再開するか、別の名前にしてください"; return 1; }
  [[ -d $cwd ]] || { OP_OUT="ディレクトリがありません: $cwd"; return 1; }
  cwd=$(cd "$cwd" && pwd)
  # 先に台帳へ載せる(SessionStart hook が --role の役割を引けるように)
  local why=""; [[ $kill == unknown ]] || why="作成時に指定"
  agent_register "$name" "$cwd" "$model" "$role" "$by" "$kill" "$why" "$kind"
  local -a args=(); kind_launch_into args "$kind" "" "$(model_with_role_default "$model" "$kind" "$name" "$role")"
  if ! herdr_spawn "$kind" "$name" "$cwd" ${args[@]+"${args[@]}"}; then
    if ((SPAWN_KEPT)); then   # 人の操作待ち: pane は残っているので台帳にも残す(最初の指示は送らない)
      sql "UPDATE main.agents SET pane_id=$(q "$SPAWN_PANE"), spawned_by=NULLIF($(q "$sby"),''), spawn_task=NULLIF($(q "$stask"),'') WHERE herdr_session=$SQS AND name=$(q "$name");"
      OP_OUT="$name: $SPAWN_ERR${prompt:+。最初の指示は未送信(答えたあと tb send などで送る)}"; return 11
    fi
    sql "DELETE FROM main.agents WHERE herdr_session=$SQS AND name=$(q "$name");"
    OP_OUT="$name を起動できません: $SPAWN_ERR"; return 11
  fi
  sql "UPDATE main.agents SET pane_id=$(q "$SPAWN_PANE"), spawned_by=NULLIF($(q "$sby"),''), spawn_task=NULLIF($(q "$stask"),'') WHERE herdr_session=$SQS AND name=$(q "$name");"
  OP_OUT="$name started (pane $SPAWN_PANE, cwd $cwd${model:+, model $model}${role:+, role $role})"
  if [[ -n $prompt ]]; then first_prompt "$name" "$prompt" "$by" || OP_OUT+=" / $FP_NOTE"; fi
  return 0
}

do_agent_stop() {  # name force by
  local name=$1 force=$2 by=$3 row pane sess k open
  agent_sync || { OP_OUT="herdr agent list に失敗しました"; return 11; }
  row=$(live_row "$name")
  [[ -n $row ]] || { other_session "$name" && return 1; OP_OUT="$name は動いていません"; return 1; }
  [[ $by != "$name" ]] || { OP_OUT="自分自身は止められません"; return 1; }
  pane=$(jq -r '.pane_id // ""' <<<"$row"); sess=$(jq -r '.agent_session.value // ""' <<<"$row")
  [[ -n $pane ]] || { OP_OUT="$name の pane が分かりません"; return 1; }
  if k=$(protect_reason "$name"); then   # 保護は --force でも越えない
    OP_OUT="$name は保護されています($k)。止めるなら、人が tb config edit protect で外してから"
    return 1
  fi
  if [[ $force != 1 ]]; then
    k=$(killable_of "$name")
    open=$(sql "SELECT group_concat(id, ' ') FROM tasks WHERE owner_agent=$(q "$name") AND status IN ($OPEN_TASK_STATUSES);")
    if [[ $k != yes || -n $open ]]; then
      OP_OUT="$name は止めてよいと判定されていません(killable=$k${open:+、未完了タスク $open})。判定役に付けてもらうか(tb agent-flag)、人が --force で止めます"
      return 1
    fi
  fi
  agent_close_pane "$name" "$pane" "$sess" || return $?
  sess=$(sql "SELECT COALESCE(session_id,'') FROM agents WHERE name=$(q "$name");")
  OP_OUT="$name stopped${sess:+ (session ${sess}。tb agent-revive $name で同じ会話から再開できます)}"
}

# pane を閉じて台帳を stopped にする(session は残す)。do_agent_stop と do_agent_restart が共有。失敗は 11
agent_close_pane() {  # name pane session
  herdr_close_pane "$2" || { OP_OUT="pane $2 を閉じられません"; return 11; }
  sql "UPDATE main.agents SET state='stopped', stopped_at=$(q "$(now)"), updated_at=$(q "$(now)"), pane_id=NULL,
         session_id=COALESCE(NULLIF($(q "$3"),''), session_id) WHERE herdr_session=$SQS AND name=$(q "$1");"
}

do_agent_revive() {  # name fresh model prompt by
  local name=$1 fresh=$2 model=$3 prompt=$4 by=$5 row cwd sess kind
  agent_sync || { OP_OUT="herdr agent list に失敗しました"; return 11; }
  [[ -z $(live_row "$name") ]] || { OP_OUT="$name はすでに動いています"; return 1; }
  row=$(agent_reg_row "$name")
  [[ -n $row ]] || { other_session "$name" && return 1; OP_OUT="$name は台帳にありません(新しく作るなら tb agent-new)"; return 1; }
  cwd=$(jq -r '.cwd // ""' <<<"$row"); [[ -n $model ]] || model=$(jq -r '.model // ""' <<<"$row")
  local lmodel=$model   # 台帳に書くのは明示か台帳の値だけ(役割の既定は書かない)
  sess=""; [[ $fresh == 1 ]] || sess=$(jq -r '.session_id // ""' <<<"$row")
  kind=$(jq -r --arg d "$KIND_DEFAULT" '.kind // $d' <<<"$row")
  model=$(model_with_role_default "$model" "$kind" "$name")
  kind_check_launch "$kind" "$model" "$sess" || return 12
  if [[ -z $cwd || ! -d $cwd ]]; then
    # claude --resume は会話を作業ディレクトリごとに探すので、別の場所では同じ会話に戻れない
    [[ -z $sess ]] || { OP_OUT="$name の作業ディレクトリがありません(${cwd:-未記録})。--fresh なら $AGENT_CWD で新しい会話として起動します"; return 1; }
    cwd=$(cd "$AGENT_CWD" && pwd)
  fi
  local -a args=(); kind_launch_into args "$kind" "$sess" "$model"
  agent_register "$name" "$cwd" "$lmodel" "" "$by" unknown "" "$kind"
  if ! herdr_spawn "$kind" "$name" "$cwd" ${args[@]+"${args[@]}"}; then
    if ((SPAWN_KEPT)); then
      sql "UPDATE main.agents SET pane_id=$(q "$SPAWN_PANE") WHERE herdr_session=$SQS AND name=$(q "$name");"
      OP_OUT="$name: $SPAWN_ERR${prompt:+。最初の指示は未送信(答えたあと tb send などで送る)}"; return 11
    fi
    sql "UPDATE main.agents SET state='stopped', stopped_at=$(q "$(now)") WHERE herdr_session=$SQS AND name=$(q "$name");"
    OP_OUT="$name を起動できません: $SPAWN_ERR"; return 11
  fi
  sql "UPDATE main.agents SET pane_id=$(q "$SPAWN_PANE") WHERE herdr_session=$SQS AND name=$(q "$name");"
  local how="新しい会話"; [[ -z $sess ]] || how="resume $sess"
  OP_OUT="$name revived ($how, pane $SPAWN_PANE, cwd $cwd)"
  if [[ -n $prompt ]]; then first_prompt "$name" "$prompt" "$by" || OP_OUT+=" / $FP_NOTE"; fi
  return 0
}

# 再起動 = 停止 + 同じ会話での復活(MCP の接続を張り直す)。killable の判定は見ない代わりに、次の安全条件を全部満たすときだけ:
#   現在の herdr セッションで動いている / herdr の状態が idle か done / in_progress のタスクを持たない
#   (持つなら idle/done かつ --prompt に再開メモがあるときだけ) / 会話を再開できる(session と cwd が分かる) / 自分自身ではない
# 復活に失敗したら停止のまま残し、tb agent-revive の実行例を出す(勝手に新しい会話は作らない)。0 / 1 / 4 / 10(再開したが最初の指示を確認できない) / 11
RESTART_DEFAULT_PROMPT='MCP の接続を張り直すために再起動しました。直前の作業を続けてください。まず MCP のツールが使えることを確認し、まだ使えないなら tb comment で報告してください。'
do_agent_restart() {  # name prompt by
  local name=$1 prompt=$2 by=$3 row pane sess cwd status open t0 old_sess new_sess rev how stoprc=0 rr=0 cur
  t0=$(date +%s); FP_NOTE=""
  agent_sync || { OP_OUT="herdr agent list に失敗しました"; return 11; }
  row=$(live_row "$name")
  [[ -n $row ]] || { other_session "$name" && return 1; OP_OUT="$name は動いていません(再起動は動いている agent だけ。止まっているなら tb agent-revive $name)"; return 1; }
  [[ $by != "$name" ]] || { OP_OUT="自分自身は再起動できません"; return 1; }
  pane=$(jq -r '.pane_id // ""' <<<"$row"); old_sess=$(jq -r '.agent_session.value // ""' <<<"$row")
  status=$(jq -r '.agent_status // ""' <<<"$row")
  [[ -n $pane ]] || { OP_OUT="$name の pane が分かりません"; return 1; }
  case $status in idle|done) ;; *) OP_OUT="$name は ${status:-状態不明} のため再起動しません(idle か done のときだけ)"; return 1;; esac
  open=$(sql "SELECT group_concat(id, ' ') FROM tasks WHERE owner_agent=$(q "$name") AND status='in_progress';")
  if [[ -n $open ]] && blank "$prompt"; then
    OP_OUT="$name は in_progress のタスク($open)を持っています。再開メモを --prompt で渡したときだけ再起動できます"; return 1
  fi
  [[ -n $old_sess ]] || old_sess=$(sql "SELECT COALESCE(session_id,'') FROM agents WHERE name=$(q "$name");")
  cwd=$(sql "SELECT COALESCE(cwd,'') FROM agents WHERE name=$(q "$name");")
  # 閉じてから再開できないと困るので、再開できることを先に確かめる(だめなら何も止めない)
  kind_require "$(sql "SELECT COALESCE(kind,'$KIND_DEFAULT') FROM agents WHERE name=$(q "$name");")" resume "同じ会話に戻れないので止めません" || { OP_OUT=$KIND_ERR; return 12; }
  [[ -n $old_sess ]] || { OP_OUT="$name の session id が台帳にありません。同じ会話に戻れないので止めません"; return 1; }
  [[ -n $cwd && -d $cwd ]] || { OP_OUT="$name の作業ディレクトリがありません(${cwd:-未記録})。同じ会話に戻れないので止めません"; return 1; }
  blank "$prompt" && prompt=$RESTART_DEFAULT_PROMPT
  agent_close_pane "$name" "$pane" "$old_sess" || return $?
  agent_sync || true
  limits_refuse_running 1 >/dev/null 2>&1 || rr=$?
  if ((rr)); then
    OP_OUT="$name を停止しましたが、動作中の agent が上限のため復活していません。停止のままです。上限が空いたら: tb agent-revive $name [--fresh]"; return 4
  fi
  do_agent_revive "$name" 0 "" "$prompt" "$by" || rr=$?
  if ((rr)); then
    OP_OUT="$name を停止しました(pane $pane を閉じた)が、復活に失敗: ${OP_OUT}。停止のままです。自分で戻すなら: tb agent-revive $name [--fresh](--fresh は新しい会話になります)"
    return "$rr"
  fi
  rev=$OP_OUT
  agent_sync || true
  cur=$(live_row "$name"); new_sess=$(jq -r '.agent_session.value // ""' <<<"$cur")
  if [[ -z $new_sess ]]; then how="session unconfirmed (herdr reports none yet; was $old_sess)"
  elif [[ $new_sess == "$old_sess" ]]; then how="同じ会話に戻った(session $old_sess のまま)"
  else how="session CHANGED ($old_sess -> $new_sess; verify it is the same conversation)"; fi
  OP_OUT="$name restarted by $by: stop ok(pane $pane を閉じた) / revive ok(${rev#"$name revived "}) / $how / $(( $(date +%s) - t0 )) 秒"
  if [[ -n $FP_NOTE ]]; then OP_OUT+=" / $FP_NOTE"; return 10; fi
  return 0
}

# op と args(JSON)から実行する(直接実行と承認後の実行で共通)
run_op() {  # $1=op $2=対象 $3=args $4=依頼者
  local a=$3
  case $1 in
    new)    do_agent_new "$2" "$(jq -r '.cwd // ""' <<<"$a")" "$(jq -r '.model // ""' <<<"$a")" "$(jq -r '.role // ""' <<<"$a")" \
                         "$(jq -r '.prompt // ""' <<<"$a")" "$(jq -r '.killable // "unknown"' <<<"$a")" "$4" \
                         "$(jq -r '.spawn.parent // ""' <<<"$a")" "$(jq -r '.spawn.task // ""' <<<"$a")" "$(jq -r --arg d "$KIND_DEFAULT" '.kind // $d' <<<"$a")";;
    stop)   do_agent_stop "$2" "$(jq -r '.force // 0' <<<"$a")" "$4";;
    revive) if [[ $(jq -r '.model_set // 0' <<<"$a") == 1 ]]; then model_set_run "$2" "$a" "$4"; return; fi   # tb model-set(承認後)
            do_agent_revive "$2" "$(jq -r '.fresh // 0' <<<"$a")" "$(jq -r '.model // ""' <<<"$a")" "$(jq -r '.prompt // ""' <<<"$a")" "$4";;
    *) OP_OUT="不明な操作: $1"; return 1;;
  esac
}

# 承認を確かめて実行し、結果を出す。終了コード 0 / 1 / 11 / 20(承認待ち)
op_entry() {  # $1=op $2=対象 $3=args
  local r=0
  authorize_op "$1" "$2" "$3" || return $?
  run_op "$1" "$2" "$3" "$REQ_BY" || r=$?
  record_op "$1" "$2" "$3" "$REQ_BY" "$r" "$OP_OUT"
  if ((r)); then echo "tb: $OP_OUT" >&2; else echo "$OP_OUT"; fi
  return $r
}

# ---------- コマンド ----------
cmd_agent-new() {
  [[ $# -ge 1 && $1 != -* ]] || usage "agent-new <名前> [--cwd D] [--kind K] [--model M] [--role R] [--prompt P] [--killable yes|no]"
  local name=$1 cwd="" model="" role="" prompt="" kill=unknown kind=$KIND_DEFAULT; shift
  valid_agent_name "$name"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --cwd) need_arg "$@"; cwd=$2; shift 2;;
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --model) need_arg "$@"; model=$2; shift 2;;
      --role) need_arg "$@"; role=$2; shift 2;;
      --prompt) need_arg "$@"; prompt=$2; shift 2;;
      --killable) need_arg "$@"; kill=$2; shift 2;;
      *) usage "agent-new: 不明な引数 $1";;
    esac
  done
  kind_valid "$kind" || usage "agent-new: --kind は $KIND_ALL のどれか: $kind"
  [[ -z $model || $model =~ $MODEL_RE ]] || usage "agent-new: --model に使えない文字があります: $model"
  kind_check_launch "$kind" "$model" "" || { echo "tb: $OP_OUT" >&2; return 12; }   # 承認待ちに積む前に、その kind でできない依頼は止める
  if [[ -n $role ]]; then
    local rx; local -a rxs; IFS=, read -ra rxs <<<"$role"
    ((${#rxs[@]} >= 1 && ${#rxs[@]} <= 4)) || usage "agent-new: --role はカンマ区切りで 1〜4 個: $role"
    for rx in "${rxs[@]}"; do
      [[ $rx =~ $ROLE_RE && $rx != _* && $rx != README ]] && role_exists "$rx" || usage "agent-new: --role は定義のある役割名(tb role-list。カンマ区切りで複数可): $rx"
    done
    # roles/<名前>.md は台帳の役割より優先される。承認(要求された役割)と作成後の役割がずれないよう拒否する
    ! role_conflict "$name" "$role" || usage "agent-new: 役割 $name の定義があるため、この名前の役割は $name に固定です(--role $role は効きません)"
  fi
  [[ $kill == yes || $kill == no || $kill == unknown ]] || usage "agent-new: --killable は yes|no"
  if [[ $kill != unknown ]]; then   # 判定フラグを付けられるのは agent-flag と同じく判定役か人だけ
    requester
    # shellcheck disable=SC2053  # glob として比べる
    if [[ $REQ_BY != human && $REQ_BY != $SWEEPER_PAT ]]; then
      # 作る前の agent は台帳に無い。grants の @<役割> の対象は、要求された役割(--role)で比べる
      local grc=0; [[ -z $role ]] || { ROLE_HINT_NAME=$name; ROLE_HINT=$role; }
      grant_of "$REQ_BY" agent:flag "$name" || grc=$?
      ROLE_HINT_NAME=""; ROLE_HINT=""
      ((grc == 0)) || die "agent-new --killable を付けられるのは人、判定役($SWEEPER_PAT)、grants で agent:flag を許された agent だけです($REQ_BY)"
    fi
  fi
  [[ ${#prompt} -le 4000 ]] || usage "agent-new: --prompt は 4000 字まで"
  [[ -n $cwd ]] || cwd=$AGENT_CWD
  cwd=${cwd/#\~/$HOME}
  # 承認する側のカレントで解決されないよう、依頼時に絶対パスにする(存在の検査は実行時に do_agent_new が行う)
  [[ $cwd == /* ]] || cwd=$PWD/$cwd
  [[ ! -d $cwd ]] || cwd=$(cd "$cwd" && pwd)
  limits_refuse_running 1 || return 4   # 動作中が上限なら、承認待ちにも積まず拒否(max_running_agents)
  op_entry new "$name" "$(jq -nc --arg c "$cwd" --arg m "$model" --arg r "$role" --arg p "$prompt" --arg k "$kill" --arg kd "$kind" \
    '{cwd:$c} + (if $kd=="claude" then {} else {kind:$kd} end) + (if $m=="" then {} else {model:$m} end) + (if $r=="" then {} else {role:$r} end) + (if $p=="" then {} else {prompt:$p} end) + {killable:$k}')"
}

cmd_agent-stop() {
  [[ $# -ge 1 && $1 != -* ]] || usage "agent-stop <名前> [--force]"
  local name=$1 force=0; shift
  valid_agent_name "$name"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --force) force=1; shift;;
      *) usage "agent-stop: 不明な引数 $1";;
    esac
  done
  if ((force)); then requester; [[ $REQ_BY == human ]] || die "--force は人が自分の端末で使います(agent '$REQ_BY' の pane からは使えません)"; fi
  op_entry stop "$name" "$(jq -nc --argjson f "$force" '{force:$f}')"
}

cmd_agent-revive() {
  [[ $# -ge 1 && $1 != -* ]] || usage "agent-revive <名前> [--fresh] [--model M] [--prompt P]"
  local name=$1 fresh=0 model="" prompt=""; shift
  valid_agent_name "$name"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --fresh) fresh=1; shift;;
      --model) need_arg "$@"; model=$2; shift 2;;
      --prompt) need_arg "$@"; prompt=$2; shift 2;;
      *) usage "agent-revive: 不明な引数 $1";;
    esac
  done
  [[ -z $model || $model =~ $MODEL_RE ]] || usage "agent-revive: --model に使えない文字があります: $model"
  [[ ${#prompt} -le 4000 ]] || usage "agent-revive: --prompt は 4000 字まで"
  local row kd sess m
  row=$(agent_reg_row "$name")   # 台帳にあれば、その kind でできない依頼(再開・モデル指定)は承認待ちに積む前に止める
  if [[ -n $row ]]; then
    kd=$(jq -r --arg d "$KIND_DEFAULT" '.kind // $d' <<<"$row"); m=$model; [[ -n $m ]] || m=$(jq -r '.model // ""' <<<"$row")
    sess=""; ((fresh)) || sess=$(jq -r '.session_id // ""' <<<"$row")
    kind_check_launch "$kd" "$m" "$sess" || { echo "tb: $OP_OUT" >&2; return 12; }
  fi
  limits_refuse_running 1 || return 4   # 復活も動作中を 1 つ増やす
  op_entry revive "$name" "$(jq -nc --argjson f "$fresh" --arg m "$model" --arg p "$prompt" \
    '{fresh:$f} + (if $m=="" then {} else {model:$m} end) + (if $p=="" then {} else {prompt:$p} end)')"
}

# 再起動(MCP が切れた agent の復旧)。人はいつでも。agent の pane からは自分以外・指揮官以外で、
# agent:stop:<名前> と agent:revive:<名前> の両方が allow のときだけ(それ以外は何もせず拒否 = 終了コード 4。承認待ちには積まない)
# agent_ops には op=revive、args.restart=1 で 1 行だけ残す(CHECK 制約は変えない)
cmd_agent-restart() {
  [[ $# -ge 1 && $1 != -* ]] || usage "agent-restart <名前> [--prompt P]"
  local name=$1 prompt="" r=0 args; shift
  valid_agent_name "$name"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --prompt) need_arg "$@"; prompt=$2; shift 2;;
      *) usage "agent-restart: 不明な引数 $1(--force はありません)";;
    esac
  done
  [[ ${#prompt} -le 4000 ]] || usage "agent-restart: --prompt は 4000 字まで"
  args=$(jq -nc --arg p "$prompt" '{restart:1} + (if $p=="" then {} else {prompt:$p} end)')
  requester
  if [[ $REQ_BY != human ]]; then
    local why="" n s v
    if [[ $REQ_BY == "$name" ]]; then why="自分自身は再起動できません"
    else
      while IFS= read -r n; do [[ $n == "$name" ]] && why="指揮官($name)は agent から再起動できません(人が実行します)"; done < <(conductor_names)
    fi
    if [[ -z $why ]]; then
      approval_of "agent:stop:$name" "$REQ_BY"; s=$APPROVAL_VALUE
      approval_of "agent:revive:$name" "$REQ_BY"; v=$APPROVAL_VALUE
      [[ $s == allow && $v == allow ]] || why="approvals で agent:stop:$name と agent:revive:$name の両方が allow ではありません(stop=$s revive=$v)。人が実行するか、人が許可してください"
    fi
    if [[ -n $why ]]; then
      sql "INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at)
        VALUES ($SQS, 'revive', $(q "$name"), $(q "$args"), $(q "$REQ_BY"), 'rejected', $(q "restart refused: $why"), $(q "$(now)"), $(q "$(now)"));" >/dev/null
      echo "tb: $why" >&2; return 4
    fi
  fi
  limits_refuse_running 0 || return 4   # 動作中が既に上限を超えているときは触らない(再起動で数は増えない)
  do_agent_restart "$name" "$prompt" "$REQ_BY" || r=$?
  record_op revive "$name" "$args" "$REQ_BY" "$r" "restart: $OP_OUT"
  if ((r == 0)); then echo "$OP_OUT"; else echo "tb: $OP_OUT" >&2; fi
  return $r
}

# 判定役(または人)が「止めてよいか」を付ける
cmd_agent-flag() {
  [[ $# -ge 1 && $1 != -* ]] || usage "agent-flag <名前> --killable yes|no|unknown [--reason R]"
  local name=$1 kill="" why=""; shift
  valid_agent_name "$name"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --killable) need_arg "$@"; kill=$2; shift 2;;
      --reason) need_arg "$@"; why=$2; shift 2;;
      *) usage "agent-flag: 不明な引数 $1";;
    esac
  done
  [[ $kill == yes || $kill == no || $kill == unknown ]] || usage "agent-flag: --killable は yes|no|unknown"
  [[ $kill == unknown ]] || ! blank "$why" || usage "agent-flag: yes / no には --reason が必要です"
  requester
  if [[ $REQ_BY != human ]]; then
    # shellcheck disable=SC2053  # glob として比べる
    [[ $REQ_BY == $SWEEPER_PAT ]] || grant_of "$REQ_BY" agent:flag "$name" || die "判定フラグを付けられるのは人、判定役($SWEEPER_PAT)、grants で agent:flag を許された agent だけです($REQ_BY)"
    [[ $REQ_BY != "$name" ]] || die "自分自身の判定は付けられません"
  fi
  agent_sync || die "herdr agent list に失敗しました"
  [[ $(sql "SELECT state FROM agents WHERE name=$(q "$name");") == running ]] || die "$name は動いていません"
  local ts; ts=$(now)
  sql "UPDATE main.agents SET killable=$(q "$kill"), killable_reason=NULLIF($(q "$(oneline "$why" 300)"),''),
         killable_by=$(q "$REQ_BY"), killable_at=$(q "$ts"), killable_session=session_id, updated_at=$(q "$ts") WHERE herdr_session=$SQS AND name=$(q "$name");"
  echo "$name killable=$kill"
}

# 関係グラフ: taskboard/sql/agent_edges.sql の行を、主体 -動詞→ 対象 のエッジにする(Web UI の /agent-graph と同じ SQL)。
# project しか分からない端(lead の主体・report の宛先)は target_for で指揮官にする。自分への辺と端の欠けた行は捨て、同じ (動詞, 主体, 対象, status) はまとめる
EDGES_SQL=$TB_ROOT/sql/agent_edges.sql
agent_edges() {
  local rows projects map="{}" p
  # 共有の SQL は表の名前のまま書いてあるので、同じ名前の CTE(このセッションの行だけ)を前に置いて絞る(Web UI は一時ビューで同じことをする)
  rows=$(sql -json "$(grep -v '^--' "$EDGES_SQL");")   # 表の名前はこのセッションのビュー || return 1
  [[ -n $rows ]] || rows="[]"
  projects=$(jq -r '[.[] | .project // empty] | unique[]' <<<"$rows")
  while IFS= read -r p; do
    [[ -n $p ]] || continue
    map=$(jq -c --arg k "$p" --arg v "$(target_for "$p")" '. + {($k): $v}' <<<"$map")
  done <<<"$projects"
  jq -c --argjson m "$map" '
    map(.src = (.src // (if .project then $m[.project] else null end)) | .dst = (.dst // (if .project then $m[.project] else null end)))
    | map(select(.src != null and .dst != null and .src != "" and .dst != "" and .src != .dst))
    | group_by([.verb, .src, .dst, .status])
    | map({verb: .[0].verb, from: .[0].src, to: .[0].dst, status: .[0].status, n: (map(.n) | add), last_at: (map(.last_at // "") | max)})' <<<"$rows"
}

# --graph の文字の出力: ロールごとの agent と、直近 7 日の関係(拒否された試みには [denied])
AGENT_GRAPH_RENDER_JQ='
  (.nodes | group_by(.role // "-") | map("[\(.[0].role // "-")]", (.[] | "  \(.name)  \(.state)/\(.agent_status // "-")")) | .[]),
  "", "関係(直近 7 日。spawn と lead は今の状態):",
  (if (.edges | length) == 0 then "  (なし)" else
     (.edges | sort_by(.from, .to, .verb) | .[] | "  \(.from)  -\(.verb)\(if .n > 1 then " ×\(.n)" else "" end)→  \(.to)\(if .status == "denied" then "  [denied]" else "" end)") end)'

# 台帳 + 判定に使う材料(判定役はこれを読んで決める)。--group role はロールごとにまとめる。--graph は関係グラフ
cmd_agents() {
  local json=0 all=0 graph=0 group=""
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) json=1; shift;;
      --all) all=1; shift;;
      --graph) graph=1; shift;;
      --group) need_arg "$@"; [[ $2 == role ]] || usage "agents: --group は role だけ"; group=$2; shift 2;;
      *) usage "agents: 不明な引数 $1";;
    esac
  done
  agent_sync || echo "tb: herdr agent list に失敗しました(台帳の内容だけを出します)" >&2
  local where="1=1"; ((all)) || where="(a.state='running' OR a.stopped_at > strftime('%Y-%m-%dT%H:%M:%SZ','now','-7 days'))"
  local rows name k status
  rows=$(sql -json "SELECT a.name, a.state, a.pane_id, a.session_id, a.cwd, a.model, a.role, a.killable_reason, a.killable_by, a.killable_at,
      a.created_by, a.created_at, a.stopped_at, a.spawned_by, a.spawn_task,
      (SELECT COUNT(*) FROM tasks t WHERE t.owner_agent=a.name AND t.status IN ($OPEN_TASK_STATUSES)) AS open_tasks,
      (SELECT COUNT(*) FROM comments c LEFT JOIN tasks t ON t.id=c.task_id WHERE c.from_name=a.name AND c.kind IN ('question','blocked')
         AND c.resolved_at IS NULL AND COALESCE(t.status,'') NOT IN ('done','cancelled')) AS open_questions,
      aa.last_prompt_at, aa.last_stop_at,
      CASE WHEN aa.last_stop_at IS NOT NULL AND (aa.last_prompt_at IS NULL OR aa.last_stop_at >= aa.last_prompt_at)
           THEN CAST((julianday('now') - julianday(aa.last_stop_at)) * 1440 AS INTEGER) END AS idle_min
    FROM agents a LEFT JOIN main.agent_activity aa ON aa.session_id=a.session_id
    WHERE $where ORDER BY a.state, a.name;")
  [[ -n $rows ]] || rows="[]"
  # 実効の判定フラグと herdr の状態を足す
  local out="[]" r
  while IFS= read -r r; do
    [[ -n $r ]] || continue
    name=$(jq -r .name <<<"$r"); k=$(killable_of "$name")
    status=$(jq -r --arg n "$name" '[.[] | select(.name==$n)][0].agent_status // ""' <<<"$LIVE_AGENTS")
    load_role "$name" >/dev/null 2>&1 || true
    out=$(jq -c --argjson r "$r" --arg k "$k" --arg s "$status" --arg f "$ROLE_LABEL" \
      '. + [$r + {killable:$k, agent_status:(if $s=="" then null else $s end), role_file:(if $f=="" then null else $f end),
                  role_name:(if $f=="" then null else ($f | split(",") | map(sub("\\.md$"; "")) | join(",")) end)}]' <<<"$out")
  done < <(jq -c '.[]' <<<"$rows")
  if ((graph)); then
    local edges extra g n f
    edges=$(agent_edges) || die "関係を読めません(DB)"
    # 関係の端に居るが一覧に無い名前(止まって 7 日を過ぎた agent・cli など)も点として足す
    g=$(jq -c --argjson e "$edges" '{nodes: map({name, state, agent_status, role: .role_name}), edges: $e}' <<<"$out")
    extra=$(jq -r '(.nodes | map(.name)) as $have | [.edges[] | .from, .to] | unique | map(select(. as $x | $have | index($x) | not)) | .[]' <<<"$g")
    while IFS= read -r n; do
      [[ -n $n ]] || continue
      f=$(role_name_of "$n")
      g=$(jq -c --arg n "$n" --arg r "$f" '.nodes += [{name:$n, state:"-", agent_status:null, role:(if $r=="" then null else $r end)}]' <<<"$g")
    done <<<"$extra"
    if ((json)); then jq -c . <<<"$g"; else jq -r "$AGENT_GRAPH_RENDER_JQ" <<<"$g"; fi
    return 0
  fi
  if ((json)); then jq -c '.[]' <<<"$out"; return 0; fi
  if [[ $group == role ]]; then
    jq -r 'group_by(.role_name // "-") | .[] | "[\(.[0].role_name // "-")]", (.[] | "  " + ([.name, .state, (.agent_status // "-"), .killable,
       "tasks:\(.open_tasks)", "q:\(.open_questions)", "idle:\(.idle_min // "-")m", (.killable_reason // "")] | join("\t")))' <<<"$out"
    return 0
  fi
  jq -r '.[] | [.name, .state, (.agent_status // "-"), .killable, "tasks:\(.open_tasks)", "q:\(.open_questions)",
     "idle:\(.idle_min // "-")m", (.role_file // "-"), (.killable_reason // "")] | join("\t")' <<<"$out"
}

# agent の中身(pane の直近の出力)を読む。読むのは tb 経由に絞る(agent の設定で Bash(herdr agent read:*) を deny する。README)。
# 人と本人はそのまま。ほかの agent からは approvals の agent:read:<対象>(@<役割> も使える)が allow のときだけ。記録は read_log
# 終了コード: 0 / 1 読めない / 20 許可されていない(人の承認が要る)
cmd_peek() {
  [[ $# -ge 1 ]] || usage "peek <agent 名> [--lines N]"
  local name=$1 lines=40 row pane out rule=""; shift
  while [[ $# -gt 0 ]]; do
    case $1 in
      --lines) need_arg "$@"; [[ $2 =~ ^[0-9]{1,3}$ && $2 -ge 1 && $2 -le 200 ]] || usage "peek: --lines は 1〜200"; lines=$2; shift 2;;
      *) usage "peek: 不明な引数 $1";;
    esac
  done
  valid_agent_name "$name"
  requester
  if [[ $REQ_BY != human && $REQ_BY != "$name" ]]; then
    approval_of "agent:read:$name" "$REQ_BY"; rule=$APPROVAL_RULE
    if [[ $APPROVAL_VALUE != allow ]]; then
      sql "INSERT INTO main.read_log (herdr_session, reader, target, lines, status, rule, created_at) VALUES ($SQS, $(q "$REQ_BY"), $(q "$name"), $lines, 'denied', $(q "$rule"), $(q "$(now)"));"
      echo "tb: $REQ_BY は $name の中身を読めません($rule)。必要なら人が approvals に agent:read:$name か agent:read:@<役割> を allow で足すか、grants.conf に個別の許可を足す" >&2
      return 20
    fi
  fi
  if ! load_live; then
    sql "INSERT INTO main.read_log (herdr_session, reader, target, lines, status, rule, created_at) VALUES ($SQS, $(q "$REQ_BY"), $(q "$name"), $lines, 'failed', NULLIF($(q "$rule"),''), $(q "$(now)"));"
    die "herdr agent list に失敗しました"
  fi
  row=$(live_row "$name"); pane=$(jq -r '.pane_id // empty' <<<"$row")
  if [[ -z $pane ]]; then
    sql "INSERT INTO main.read_log (herdr_session, reader, target, lines, status, rule, created_at) VALUES ($SQS, $(q "$REQ_BY"), $(q "$name"), $lines, 'failed', NULLIF($(q "$rule"),''), $(q "$(now)"));"
    die "$name は herdr で動いていません"
  fi
  if ! out=$(herdr_call agent read "$pane" --source recent --lines "$lines" --format text 2>/dev/null); then
    sql "INSERT INTO main.read_log (herdr_session, reader, target, lines, status, rule, created_at) VALUES ($SQS, $(q "$REQ_BY"), $(q "$name"), $lines, 'failed', NULLIF($(q "$rule"),''), $(q "$(now)"));"
    die "herdr agent read に失敗しました($name)"
  fi
  sql "INSERT INTO main.read_log (herdr_session, reader, target, lines, status, rule, created_at) VALUES ($SQS, $(q "$REQ_BY"), $(q "$name"), $lines, 'ok', NULLIF($(q "$rule"),''), $(q "$(now)"));"
  printf '%s\n' "$out"
}

cmd_agent-ops() {
  local json=0 st=""
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) json=1; shift;;
      --status) need_arg "$@"; st=$2; shift 2;;
      *) usage "agent-ops: 不明な引数 $1";;
    esac
  done
  local where="1=1"; [[ -z $st ]] || where="status=$(q "$st")"
  if ((json)); then sql -json "SELECT * FROM agent_ops WHERE ($where) ORDER BY id DESC LIMIT 100;" | jq -c '(. // [])[] | .id = "L-\(.id)"'
  else sql -nullvalue - -separator $'\t' "SELECT 'L-'||id, op, target, requested_by, status, created_at, COALESCE(result,'') FROM agent_ops WHERE ($where) ORDER BY id DESC LIMIT 100;"; fi
}

# 承認待ちの操作の、承認を任せるときの操作名(grants.conf の approve:<これ> <依頼者>)。人だけが決めるものは空
agent_op_action() {  # $1=agent_ops の行(JSON)
  local op target args
  op=$(jq -r .op <<<"$1"); target=$(jq -r .target <<<"$1"); args=$(jq -r '.args // "{}"' <<<"$1")
  case $op in
    slash)  [[ $target != '*' ]] || return 0   # --all は人だけ
            printf 'agent:slash:%s:%s' "$(jq -r '.command // ""' <<<"$args")" "$target";;
    new)    if [[ -n $(jq -r '.spawn.parent // ""' <<<"$args") ]]; then
              spawn_frozen && return 0           # spawn-freeze の間は、spawn の承認は人だけ
              printf 'agent:spawn:%s' "$target"
            else printf 'agent:new:%s' "$target"; fi;;
    revive) if [[ $(jq -r '.model_set // 0' <<<"$args") == 1 ]]; then
              # tb model-set --all は人だけ
              [[ $(jq -r '.all // 0' <<<"$args") != 1 && $(jq -r '.result // ""' <<<"$1") != '--all'* ]] || return 0
              printf 'agent:model:%s' "$target"
            else printf 'agent:revive:%s' "$target"; fi;;
    *)      printf 'agent:%s:%s' "$op" "$target";;
  esac
}

# 承認を任された agent が実行する前に、承認待ちの行を 1 回だけ取る(2 体が同時に承認しても実行は 1 回)。
# 取った印は decided_at(結果の記録で上書きされる)。人は印があっても実行できる(途中で止まった行を人が片付けられるように)
op_claim() {  # $1=id
  [[ $APPROVER == human ]] && return 0
  [[ $(sql "UPDATE main.agent_ops SET decided_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$1 AND status='pending' AND decided_at IS NULL; SELECT changes();") == 1 ]] \
    || die "L-$1 はほかの承認者が処理中か、すでに決まっています"
}

# 承認待ちの操作を実行する(人か、grants.conf の approve:agent:… で許された agent)。--always なら agent:<op>:<対象> を allow に(人だけ)
cmd_agent-approve() {
  [[ $# -ge 1 ]] || usage "agent-approve L-<n> [--always]"
  valid_lid "$1"; local id=${1#L-} always=0 row op target args by r=0; shift
  [[ ${1:-} == --always ]] && always=1
  row=$(session_row agent_ops id "$id")
  [[ -n $row ]] || { approver_check "tb agent-approve" "" ""; die "操作が見つかりません: L-$id"; }
  approver_check "tb agent-approve" "$(agent_op_action "$row")" "$(jq -r .requested_by <<<"$row")"
  [[ $APPROVER == human ]] || ((always == 0)) || die "tb agent-approve: --always(今後は承認なしにする)は人だけが使えます"
  [[ $(jq -r .status <<<"$row") == pending ]] || die "L-$id は承認待ちではありません($(jq -r .status <<<"$row"))"
  if [[ $(jq -r .op <<<"$row") == slash ]]; then op_claim "$id"; slash_approve "$id" "$row" "$always"; return; fi   # tb slash(lib/slash.sh)
  op=$(jq -r .op <<<"$row"); target=$(jq -r .target <<<"$row"); args=$(jq -r .args <<<"$row"); by=$(jq -r .requested_by <<<"$row")
  local aop=$op; [[ $(jq -r '.model_set // 0' <<<"$args") == 1 ]] && aop=model   # tb model-set の承認待ち(記録は op=revive)
  if [[ $op == new && -n $(jq -r '.spawn.parent // ""' <<<"$args") ]]; then spawn_recheck "$id" "$args" || return 1
  elif [[ $op == new || $aop == revive ]]; then limits_refuse_running 1 || return 4   # 承認待ちの間に上限へ達していたら、承認待ちのまま残す
  fi
  if [[ $op == new ]] && role_conflict "$target" "$(jq -r '.role // ""' <<<"$args")"; then   # --always の規則を書く前に止める
    die "L-$id: $ROLE_CONFLICT_WHY(tb agent-reject L-$id で却下)"
  fi
  op_claim "$id"
  ((always)) && set_approval allow "agent:$aop:$target" >/dev/null && echo "agent:$aop:$target = allow(今後は承認なし)"
  run_op "$op" "$target" "$args" "$by" || r=$?
  record_op "$op" "$target" "$args" "$by" "$r" "$OP_OUT / $(approver_label)が承認" "$id"
  if ((r)); then echo "tb: L-$id: $OP_OUT" >&2; else echo "L-$id: $OP_OUT"; fi
  return $r
}

cmd_agent-reject() {
  [[ $# -eq 1 ]] || usage "agent-reject L-<n>"
  valid_lid "$1"; local id=${1#L-} row
  row=$(session_row agent_ops id "$id")
  [[ -n $row ]] || { approver_check "tb agent-reject" "" ""; die "L-$id は承認待ちではありません"; }
  approver_check "tb agent-reject" "$(agent_op_action "$row")" "$(jq -r .requested_by <<<"$row")"
  [[ $(jq -r .status <<<"$row") == pending ]] || die "L-$id は承認待ちではありません"
  sql "UPDATE main.agent_ops SET status='rejected', result=COALESCE(result,'')||$(q " / $(approver_label)が却下"), decided_at=$(q "$(now)") WHERE herdr_session=$SQS AND id=$id AND status='pending';"
  echo "L-$id rejected"
}

# ---------- 組織図(設定 org)を見る / 誰が誰に何をできるか ----------
# 組織図は全員が見られる(人も agent も。読むだけ)。上司の線は設定 org(lib/approvals.sh の org_manager_of)で決まり、
# できることは grants / delivery / approvals / protect の行が ~report などの関係を使って決める。
# tb org [--json]             台帳の agent と、設定 org に書かれた上司を木で出す
# tb org <agent 名> [--json]  その agent の上司・上の全員・直属の部下・同じ上司
# tb org --me [--json]        叩いた agent(pane から tb が決める。自己申告にしない)について同じもの
cmd_org() {
  local json=0 who="" n m rc
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json) json=1; shift;;
      --me) [[ -z $who ]] || usage "org: agent 名か --me のどちらか 1 つ"
            requester; [[ $REQ_BY != human ]] || usage "org: --me は agent の pane から使う(人の端末には上司がいない。tb org <agent 名> で見る)"
            who=$REQ_BY; shift;;
      -*) usage "org: 不明な引数 $1";;
      *) [[ -z $who ]] || usage "org: agent 名は 1 つだけ"; who=$1; shift;;
    esac
  done
  [[ -z $who ]] || valid_agent_name "$who"
  local -A mgr=() st=() seen=()
  local -a names=()
  while IFS=$'\t' read -r n m; do [[ -n $n ]] && { names+=("$n"); st[$n]=$m; }; done \
    < <(sql -separator $'\t' "SELECT name, state FROM agents ORDER BY name;")
  [[ -z $who || -n ${st[$who]:-} ]] || { names+=("$who"); st[$who]=-; }
  if config_has org; then
    for n in "${names[@]}"; do
      rc=0; m=$(org_manager_of "$n") || rc=$?
      if ((rc == 0)); then mgr[$n]=$m; elif ((rc == 2)); then mgr[$n]='?'; fi
    done
  fi
  # 上司として書かれているが台帳にいない名前も出す(上司の上司もたどる)
  local added=1
  while ((added)); do
    added=0
    for n in "${names[@]}"; do
      m=${mgr[$n]:-}
      if [[ -n $m && $m != '?' && -z ${st[$m]:-} ]]; then
        names+=("$m"); st[$m]=-; added=1
        rc=0; m=$(org_manager_of "$m") || rc=$?
        ((rc == 0)) && mgr[${names[-1]}]=$m
      fi
    done
  done
  if [[ -n $who ]]; then
    local chain reports="" peers="" x
    chain=$(org_chain "$who" 2>/dev/null) || chain='?'
    for x in "${names[@]}"; do
      [[ $x != "$who" ]] || continue
      [[ ${mgr[$x]:-} == "$who" ]] && reports+="$x "
      [[ -n ${mgr[$who]:-} && ${mgr[$who]} != '?' && ${mgr[$x]:-} == "${mgr[$who]}" ]] && peers+="$x "
    done
    if ((json)); then
      jq -nc --arg n "$who" --arg m "${mgr[$who]:-}" --arg c "$chain" --arg r "$reports" --arg p "$peers" --arg s "${st[$who]:--}" \
        '{name:$n, state:$s, manager:(if $m=="" then null else $m end), superiors:($c|split("\n")|map(select(.!=""))),
          reports:($r|split(" ")|map(select(.!=""))), peers:($p|split(" ")|map(select(.!="")))}'
      return 0
    fi
    [[ -n $reports ]] || reports=-; [[ -n $peers ]] || peers=-
    printf 'agent: %s\n上司: %s\n上の全員: %s\n直属の部下: %s\n同じ上司: %s\n' "$who" "${mgr[$who]:--}" \
      "$( [[ -n $chain ]] && tr '\n' ' ' <<<"$chain" | sed 's/ $//' || echo -)" "${reports% }" "${peers% }"
    return 0
  fi
  if ((json)); then
    for n in "${names[@]}"; do printf '%s\t%s\t%s\n' "$n" "${st[$n]}" "${mgr[$n]:-}"; done \
      | jq -Rsc --arg src "$(config_lookup org && printf '%s' "$CFG_SRC" || echo none)" \
          '{source:$src, agents:(split("\n")|map(select(.!="")|split("\t")|{name:.[0], state:.[1], manager:(if .[2]=="" then null else .[2] end)}))}'
    return 0
  fi
  if ! config_has org; then
    echo "組織図は未設定です(設定 org。人が tb config set org で書く。例: setup/examples/org.conf)。誰にも上司はいません"
    return 0
  fi
  echo "組織図(設定 org: $CFG_SRC)"
  _org_print() {  # $1=名前 $2=字下げ
    seen[$1]=1
    local c tag=""
    [[ ${st[$1]} == stopped ]] && tag="  (停止中)"
    [[ ${st[$1]} == - ]] && tag="  (台帳にいない)"
    printf '%s%s%s\n' "$2" "$1" "$tag"
    for c in "${names[@]}"; do
      [[ ${mgr[$c]:-} == "$1" && -z ${seen[$c]:-} ]] && _org_print "$c" "$2  "
    done
    return 0
  }
  ((${#names[@]})) || { echo "(台帳に agent がいません)"; return 0; }
  local -a sorted
  mapfile -t sorted < <(printf '%s\n' "${names[@]}" | sort -u)
  names=("${sorted[@]}")
  for n in "${names[@]}"; do
    m=${mgr[$n]:-}
    [[ -z $m || $m == '?' ]] && [[ -z ${seen[$n]:-} ]] && _org_print "$n" ""
  done
  local rest="" unk=""
  for n in "${names[@]}"; do
    [[ ${mgr[$n]:-} == '?' ]] && unk+="$n "
    [[ -z ${seen[$n]:-} ]] && rest+="$n "
  done
  [[ -z $unk ]] || echo "役割を確かめられず上司が決まらない: ${unk% }"
  [[ -z $rest ]] || echo "循環していて木にできない: ${rest% }"
  return 0
}

# tb can <主体> <操作> <対象>: 主体が対象にその操作をできるかを、実際の判定と同じ規則で 1 つに決めて出す(読むだけ。何もしない)
# 1 行目 allow|ask|hold|deny、続けて rule: 行。終了コード 0=allow 20=ask(人の承認待ちになる)21=hold(inbox に置かれる)4=deny
# 主体 human は人(pane のない端末)。操作: send:task / send:info / send:answer / agent:stop / role:change / agent:flag / そのほか approvals の操作名
# tb can <操作> <対象> は主体を叩いた agent(pane から決める。人の端末なら human)にする
cmd_can() {
  [[ $# -eq 2 || $# -eq 3 ]] || usage "can [<主体>] <操作> <対象>(例: tb can w1 send:task lead1、主体を省くと自分: tb can agent:stop w1)"
  if [[ $# -eq 2 ]]; then requester; set -- "$REQ_BY" "$@"; fi
  local who=$1 op=$2 target=$3 verdict=allow rule="" p
  [[ $who == human ]] || valid_agent_name "$who"
  valid_agent_name "$target"
  valid_action "$op"
  _can_out() { printf '%s\n' "$verdict"; printf 'rule: %s\n' "${@}"; case $verdict in allow) return 0;; ask) return 20;; hold) return 21;; *) return 4;; esac; }
  # tb role-set は、対象の名前の役割定義(roles/<名前>.md)があれば人でも止める(台帳のロールが効かない)
  if [[ $op == role:change ]] && role_exists "$target"; then verdict=deny; _can_out "役割 $target の定義があるため、台帳のロールは効かない(tb role-set は止まる)"; return; fi
  if [[ $who == human ]]; then
    if [[ $op == send:* ]]; then verdict=deny; _can_out "tb send は agent の pane から使う(人の端末には送信者がいない。人は herdr で直接話す)"; return; fi
    if [[ $op == agent:stop ]] && p=$(protect_reason "$target"); then verdict=deny; _can_out "protect: $p(人が設定 protect から外すまで止められない)"; return; fi
    _can_out "人の端末からはそのまま実行できる"; return
  fi
  if [[ $who == "$target" && ( $op == send:* || $op == role:change ) ]]; then verdict=deny; _can_out "自分自身には使えない"; return; fi
  if [[ $who == "$target" && $op == agent:read ]]; then _can_out "本人はそのまま読める(tb peek)"; return; fi
  case $op in
    send:task|send:info|send:answer)
      delivery_mode "$who" "$target" "${op#send:}"
      [[ $DELIVERY_MODE != block ]] || { verdict=deny; _can_out "$DELIVERY_RULE"; return; }
      # tb send と同じく、hold でも先に approvals を見る(ask なら承認待ち。承認のあとで inbox に置かれる)
      approval_of "$op:$target" "$who"
      if [[ $APPROVAL_VALUE != allow ]]; then verdict=ask
      elif [[ $DELIVERY_MODE == hold ]]; then verdict=hold; fi
      _can_out "$DELIVERY_RULE$([[ $DELIVERY_MODE == hold ]] && printf '(届けず宛先の inbox に置く)')" "approvals: $APPROVAL_RULE";;
    agent:stop)
      if p=$(protect_reason "$target"); then verdict=deny; _can_out "protect: $p"; return; fi
      approval_of "$op:$target" "$who"; [[ $APPROVAL_VALUE == allow ]] || verdict=ask
      _can_out "$APPROVAL_RULE";;
    agent:flag)
      # shellcheck disable=SC2053  # glob として比べる
      if [[ $who == $SWEEPER_PAT ]]; then _can_out "判定役($SWEEPER_PAT)"
      elif grant_of "$who" agent:flag "$target"; then _can_out "$GRANT_RULE"
      else verdict=deny; _can_out "判定役でなく、grants に agent:flag の行もない"; fi;;
    role:change)
      if grant_of "$who" role:change "$target"; then _can_out "$GRANT_RULE"
      elif name_matches "$who" "@$ROLE_ADMIN"; then
        approval_of role:change; [[ $APPROVAL_VALUE == allow ]] || verdict=ask
        _can_out "人事役(@$ROLE_ADMIN)/ approvals: $APPROVAL_RULE"
      else verdict=deny; _can_out "人事役(@$ROLE_ADMIN)でなく、grants に role:change の行もない"; fi;;
    approve:*)
      # 承認の委任(tb approve / agent-approve)。対象は依頼した agent。grants の行だけで決まる(approvals.conf は見ない)
      if [[ $who == "$target" ]]; then verdict=deny; _can_out "自分の依頼は承認できない"; return; fi
      local g=1; role_pin "$who" "$target"; grant_of "$who" "$op" "$target" && g=0; role_unpin
      if ((g == 0)); then _can_out "$GRANT_RULE(--always・--all 系・spawn-freeze 中は人だけ)"
      else verdict=deny; _can_out "grants に $op $target に当たる approve の行がない(承認は人だけ)"; fi;;
    *)
      approval_of "$op:$target" "$who"; [[ $APPROVAL_VALUE == allow ]] || verdict=ask
      _can_out "$APPROVAL_RULE";;
  esac
}
