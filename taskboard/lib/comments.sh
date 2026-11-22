# shellcheck shell=bash
# worker → 指揮官の報告コメント(comment / comments)
# bin/tb から source される。単体では実行しない。

# ---------- comment / comments(worker → 指揮官の報告コメント) ----------
COMMENT_KINDS="done progress question blocked"
# すぐ送る kind。それ以外は保存だけして tb digest でまとめて送る
IMMEDIATE_KINDS=${TB_IMMEDIATE_KINDS:-question blocked}

deliver_comment() {  # $1=comments.id → 0 送信済み / 10 / 11
  local cid=$1 row delivered tid kind text name pane project target msg
  [[ $cid =~ ^[0-9]+$ ]] || usage "コメント ID は C-<数字>"
  row=$(session_row comments id "$cid")
  [[ -n $row ]] || die "コメントが見つかりません: C-$cid"
  delivered=$(jq -r '.delivered_at // ""' <<<"$row")
  if [[ -n $delivered ]]; then echo "C-$cid は配送済みです($delivered)"; return 0; fi
  tid=$(jq -r .task_id <<<"$row"); kind=$(jq -r .kind <<<"$row"); text=$(jq -r .text <<<"$row")
  name=$(jq -r .from_name <<<"$row"); pane=$(jq -r .from_pane <<<"$row")
  project=$(sql "SELECT project FROM tasks WHERE id=$(q "$tid");"); target=$(target_for "$project")
  # tb agent-spawn で立てた子なら、指揮官が親にも伝えられるよう親の名前を付ける(それ以外は従来のまま)
  local sby; sby=$(sql "SELECT COALESCE(spawned_by,'') FROM agents WHERE name=$(q "$name");")
  if [[ $sby =~ ^[A-Za-z0-9._-]{1,64}$ ]]; then sby="spawned_by: $sby "; else sby=""; fi
  local dels=""; [[ $kind == done ]] && dels=$(jq -r '.deliverables | fromjson | join(", ")' <<<"$row")
  msg="[from: $name / $pane / $tid / $kind] ${sby}$(oneline "$text" 200)${dels:+ / 成果物: $(oneline "$dels" 300)} (詳細: tb comments $tid)"
  herdr_send "$target" "$msg" "C-$cid" comment || return $?
  local ts; ts=$(now)
  sql "UPDATE main.comments SET delivered_at=$(q "$ts") WHERE herdr_session=$SQS AND id=$cid;"
  comment_marks_reported "$cid" "$ts"
  echo "C-$cid delivered to $target"
}

# done の報告コメントが届いたら、そのタスク(done のもの)を報告済みにする(tb report と digest の両方から)
comment_marks_reported() {  # $1=comments.id $2=時刻
  sql "UPDATE main.tasks SET reported_at=$(q "$2"), updated_at=$(q "$2")
         WHERE herdr_session=$SQS AND status IN ($ST_DONE_SQL) AND reported_at IS NULL
           AND id=(SELECT task_id FROM comments WHERE id=$1 AND kind='done');"
}

# 終了コード: 0 配送済み / 10 保存済み・宛先使用中で未配送 / 11 保存済み・送信失敗 / 3 報告者を特定できず未保存 / 2 使い方 / 1 その他
cmd_comment() {
  [[ $# -ge 1 ]] || usage "comment <task-id> --kind done|progress|question|blocked --text T [--deliverables a,b] [--json]"
  local tid=$1 kind="" text="" dels="" json=0; shift
  valid_id "$tid"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --text) need_arg "$@"; text=$2; shift 2;;
      --deliverables) need_arg "$@"; dels=$2; shift 2;;
      --json) json=1; shift;;
      --from|--from-*|--name|--pane|--session) usage "comment: 報告者は herdr から自動で決まります。$1 は指定できません";;
      *) usage "comment: 不明な引数 $1";;
    esac
  done
  [[ " $COMMENT_KINDS " == *" $kind "* ]] || usage "comment: --kind は $COMMENT_KINDS のどれか"
  blank "$text" && usage "comment: --text が必要です"
  local darr="[]"
  if [[ -n $dels ]]; then
    darr=$(jq -nc --arg d "$dels" '$d | split(",") | map(gsub("^\\s+|\\s+$";"")) | map(select(length>0))')
  fi
  if [[ $kind == done && $darr == "[]" ]]; then usage "comment: done は --deliverables(成果物)が必須です"; fi
  require_task "$tid" >/dev/null
  resolve_label || return 3
  local cid rc=0
  cid=$(sql "INSERT INTO main.comments (herdr_session, task_id, kind, text, deliverables, from_name, from_pane, from_session, created_at)
    VALUES ($SQS, $(q "$tid"), $(q "$kind"), $(q "$text"), $(q "$darr"), $(q "$LABEL_NAME"), $(q "$LABEL_PANE"), $(q "$LABEL_SESSION"), $(q "$(now)"));
    SELECT last_insert_rowid();")
  notify_hook notify_hook_comment "$cid"   # 外への通知の積み込み(off なら何もしない。失敗しても結果は変わらない)
  notify_rules_hook
  local state=queued
  if [[ " $IMMEDIATE_KINDS " == *" $kind "* ]]; then   # 人の判断待ちはすぐ送る。それ以外は tb digest でまとめて送る
    deliver_comment "$cid" >/dev/null || rc=$?   # 保存は済んでいる。配送の結果は終了コードで返す
    state=$([[ $rc == 0 ]] && echo delivered || echo saved-undelivered)
  fi
  if ((json)); then sql -json "SELECT * FROM comments WHERE id=$cid;" | jq -c '.[0] | .deliverables |= fromjson | .id = "C-\(.id)"'
  else echo "C-$cid $state"; fi
  return $rc
}

cmd_comments() {
  local tid="" kind="" und=0 open=0 json=0 where="1=1"
  if [[ ${1:-} == T-* ]]; then tid=$1; shift; valid_id "$tid"; fi
  while [[ $# -gt 0 ]]; do
    case $1 in
      --kind) need_arg "$@"; kind=$2; shift 2;;
      --undelivered) und=1; shift;;
      --open) open=1; shift;;
      --json) json=1; shift;;
      *) usage "comments: 不明な引数 $1";;
    esac
  done
  [[ -z $tid ]] || where+=" AND task_id=$(q "$tid")"
  if [[ -n $kind ]]; then [[ " $COMMENT_KINDS " == *" $kind "* ]] || usage "comments: --kind は $COMMENT_KINDS"; where+=" AND kind=$(q "$kind")"; fi
  ((und)) && where+=" AND delivered_at IS NULL"
  ((open)) && where+=" AND kind IN ('question','blocked') AND resolved_at IS NULL"   # 人の対応待ち
  if ((json)); then
    sql -json "SELECT * FROM comments WHERE ($where) ORDER BY id;" | jq -c '(. // [])[] | .deliverables |= fromjson | .id = "C-\(.id)"'
  else
    # needs-human: 未対応の question/blocked(人の判断・承認待ち)。resolved: 人が対応済みにしたもの
    sql -separator $'\t' "SELECT 'C-'||id, kind, CASE WHEN resolved_at IS NOT NULL THEN 'resolved'
           WHEN kind IN ('question','blocked') THEN 'needs-human' ELSE '-' END,
      CASE WHEN delivered_at IS NULL THEN 'undelivered' ELSE 'delivered' END, task_id, from_name,
      replace(replace(text, char(10), ' '), char(9), ' ') FROM comments WHERE ($where) ORDER BY id;"
  fi
}

# tb resolve C-<n> [-m メモ]: 人が question / blocked の報告を対応済みにする。agent の pane からは拒否(require_human)
# 終了コード 0 対応済みにした / すでに対応済み、1 拒否・見つからない・対象外の種別、2 使い方
cmd_resolve() {
  [[ $# -ge 1 ]] || usage "resolve <C-n> [-m <メモ>]"
  local ref=${1#C-} note="" row kind at; shift
  [[ $ref =~ ^[0-9]+$ ]] || usage "resolve: ID は C-<数字>: $ref"
  while [[ $# -gt 0 ]]; do
    case $1 in
      -m|--message) need_arg "$@"; note=$2; shift 2;;
      *) usage "resolve: 不明な引数 $1";;
    esac
  done
  ((${#note} <= 500)) || usage "resolve: メモは 500 字まで"
  require_human "tb resolve"
  row=$(sql -json "SELECT kind, resolved_at FROM comments WHERE id=$ref;" | jq -c '.[0] // empty')
  [[ -n $row ]] || die "報告が見つかりません: C-$ref"
  kind=$(jq -r .kind <<<"$row"); at=$(jq -r '.resolved_at // ""' <<<"$row")
  [[ $kind == question || $kind == blocked ]] || die "対応済みにできるのは question / blocked だけです(C-$ref は $kind)"
  if [[ -n $at ]]; then echo "C-$ref はすでに対応済みです($at)"; return 0; fi
  sql "UPDATE main.comments SET resolved_at=$(q "$(now)"), resolved_note=NULLIF($(q "$note"),'') WHERE herdr_session=$SQS AND id=$ref AND resolved_at IS NULL;"
  echo "C-$ref resolved"
}

# tb reopen C-<n> [-m 理由]: tb resolve の逆。誤って対応済みにした question / blocked の報告を未対応に戻す。人だけ(require_human。agent の pane からは拒否)
# 戻すと resolved_at / resolved_note を空にする。誰がいつ戻したかは task_events に 'comment_reopened'(old=対応済みだった時刻、new=-m の理由(省略可)、actor=変えた者、ref=C-<n>)で残す(スキーマ変更なし)
# 終了コード 0 戻した / すでに未対応で何もしなかった、1 拒否・見つからない・対象外の種別、2 使い方
cmd_reopen() {
  [[ $# -ge 1 ]] || usage "reopen <C-n> [-m <理由>]"
  local ref=${1#C-} note="" row kind at tid ev=""; shift
  [[ $ref =~ ^[0-9]+$ ]] || usage "reopen: ID は C-<数字>: $ref"
  while [[ $# -gt 0 ]]; do
    case $1 in
      -m|--message) need_arg "$@"; note=$2; shift 2;;
      *) usage "reopen: 不明な引数 $1";;
    esac
  done
  ((${#note} <= 500)) || usage "reopen: 理由は 500 字まで"
  require_human "tb reopen"
  row=$(sql -json "SELECT kind, resolved_at, task_id FROM comments WHERE id=$ref;" | jq -c '.[0] // empty')
  [[ -n $row ]] || die "報告が見つかりません: C-$ref"
  kind=$(jq -r .kind <<<"$row"); at=$(jq -r '.resolved_at // ""' <<<"$row"); tid=$(jq -r .task_id <<<"$row")
  [[ $kind == question || $kind == blocked ]] || die "未対応に戻せるのは question / blocked だけです(C-$ref は $kind)"
  if [[ -z $at ]]; then echo "C-$ref は対応済みではありません(未対応のまま)"; return 0; fi
  [[ -n ${SESSION_SETUP-} ]] || session_setup_load   # HAS_TASK_EVENTS は $(...) の外で決める
  if [[ ${HAS_TASK_EVENTS:-0} == 1 ]]; then
    ev="INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
          SELECT $SQS, $(q "$tid"), 'comment_reopened', $(q "$at"), NULLIF($(q "$note"),''), $(q "$EVENT_ACTOR"), 'C-$ref', $(q "$(now)") WHERE changes() > 0;"
  fi
  sql "BEGIN; UPDATE main.comments SET resolved_at=NULL, resolved_note=NULL WHERE herdr_session=$SQS AND id=$ref AND resolved_at IS NOT NULL; $ev COMMIT;"
  echo "C-$ref reopened"
}
