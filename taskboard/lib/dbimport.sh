# shellcheck shell=bash
# 旧形式の DB(1 セッション = 1 DB。checkout ごとの taskboard/data/tasks.db)を、共有の DB の 1 セッションとして取り込む(db-import)
# bin/tb から source される。単体では実行しない。
#
# - 取り込むのは写し(.backup)。元の DB は読むだけで、書き換えも移動もしない
# - 写しを最新のスキーマまで移行し(012 で行の herdr_session が取り込み先のセッションになる)、1 つのトランザクションで共有の DB に足す
# - 整数の ID(C- / O- / A- / L- / N- / D- / P- / PQ- など)は、共有の DB の最大値の後ろへずらす(番号は DB 全体で一意)。
#   ずらした番号を指す列(notify_sends.event_id / notify_events.batch_id / observations.digest_id / pr_queue.depends_on と、messages / notify_events の ref)も直す
# - タスク ID(T-<project>-NNN)は DB 全体で一意。共有の DB に同じ ID があれば、取り込む側を次の空き番号に付け替え、参照(親・コメント・送信など)も直す
# - 名前やキーが主キーの表(agents / agent_activity / observations / tb_flags / notify_seen)は、共有の DB に既にある行を優先する。
#   agents だけは、共有の DB の行に無い値(role / model / cwd / session など)を旧 DB の値で埋める
# - 同じファイルを 2 回取り込まない(取り込んだ記録を tb_flags の db-import:<実体のパス> に残す)

# 整数の ID を持つ表と、画面やコマンドでの接頭辞(無いものは空)。列が id でなければ <表>.<列>
# アラート・提案は 020 でチケットへ移した: 番号は alert_details.alert_no / proposal_details.proposal_no(チケットは tasks と一緒に取り込む)
IMPORT_ID_TABLES="comments:C outbox:O alert_details.alert_no:A agent_ops:L notify_events:N digests:D proposal_details.proposal_no:P pr_queue:PQ
  proxy_requests: messages: reviews: read_log: role_changes: role_briefings: approval_changes: notify_sends:"
# タスク ID を持つ列(表.列)
IMPORT_TASK_REFS="tasks.parent_id comments.task_id proxy_requests.task_id outbox.task_id pr_queue.task_id agents.spawn_task observations.task_id
  alert_details.task_id proposal_details.task_id"

cmd_db-import() {
  local src="" session=$SESS dry=0
  while [[ $# -gt 0 ]]; do
    case $1 in
      --session) need_arg "$@"; session=$2; shift 2;;
      --dry-run) dry=1; shift;;
      -*) usage "db-import: 不明な引数 $1";;
      *) [[ -z $src ]] || usage "db-import: 旧 DB は 1 つだけ"; src=$1; shift;;
    esac
  done
  [[ -n $src ]] || usage "db-import <旧 DB のパス> [--session <名前>] [--dry-run]"
  [[ $session =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$ ]] || usage "db-import: セッション名は英数字で始まり、英数字と . _ - の 64 字以内: $session"
  require_human "tb db-import"
  [[ -f $src ]] || die "旧 DB がありません: $src"
  local real dreal
  real=$(cd "$(dirname "$src")" && printf '%s/%s' "$(pwd -P)" "$(basename "$src")")
  dreal=$(cd "$(dirname "$DB")" 2>/dev/null && printf '%s/%s' "$(pwd -P)" "$(basename "$DB")") || dreal=$DB
  [[ $real != "$dreal" ]] || die "取り込み先と同じ DB です: $src"
  local sv; sv=$(sqlite3 -readonly "$real" "PRAGMA user_version;" 2>/dev/null) || die "旧 DB を開けません: $src"
  ((sv < 12)) || die "取り込めるのは旧形式(セッションごとに分ける前、スキーマ v11 まで)の DB だけです(この DB は v$sv)"
  local mark="db-import:$real"
  [[ -z $(sql "SELECT 1 FROM main.tb_flags WHERE key=$(q "$mark") LIMIT 1;") ]] \
    || die "この DB は取り込み済みです: $real($(sql "SELECT herdr_session||' / '||value FROM main.tb_flags WHERE key=$(q "$mark") LIMIT 1;"))"

  # 写しを作り、取り込み先のセッションとして最新まで移行する(元の DB には触らない)
  local tmp copy; tmp=$(mktemp -d); copy=$tmp/src.db
  # shellcheck disable=SC2064  # tmp は今の値で消す(tb は 1 コマンド 1 プロセス)
  trap "rm -rf '$tmp'" EXIT
  sqlite3 "$real" ".backup '$copy'" || die "旧 DB の写しを作れません: $src"
  ( DB=$copy; SESS=$session; SQS=$(q "$session"); init_db ) || die "旧 DB の写しを移行できません"

  local isql; isql=$(import_sql "$copy" "$session" "$mark") || return 1
  local out
  if ((dry)); then
    out=$(sqlite3 -bail -batch -cmd ".timeout ${SQL_TIMEOUT:-5000}" "$copy" "$isql ROLLBACK;") || die "取り込みに失敗しました(何も変えていません)"
    echo "(dry-run: 何も書いていません)"
  else
    out=$(sqlite3 -bail -batch -cmd ".timeout ${SQL_TIMEOUT:-5000}" "$copy" "$isql COMMIT;") || die "取り込みに失敗しました(何も変えていません)"
  fi
  printf '%s\n' "$out"
  ((dry)) || echo "$real を herdr セッション '$session' の行として取り込みました。旧 DB はそのまま残しています(不要なら消す)"
}

# 取り込みの SQL(写しを main、共有の DB を dst として開く)。最後の COMMIT / ROLLBACK は呼び出し側が足す
import_sql() {  # $1=写し $2=セッション $3=取り込みの記録のキー
  local copy=$1 sqs; sqs=$(q "$2")
  local s t p col tbl idc
  s="ATTACH $(q "$DB") AS dst; BEGIN IMMEDIATE;"
  # 1. 整数の ID をずらす(負の値を経由して、途中で主キーがぶつからないようにする)
  s+=" CREATE TEMP TABLE _off (t TEXT PRIMARY KEY, n INTEGER NOT NULL);"
  for t in $IMPORT_ID_TABLES; do
    import_id_split "$t"
    s+=" INSERT INTO _off VALUES ('$tbl', COALESCE((SELECT MAX($idc) FROM dst.$tbl), 0));
         UPDATE main.$tbl SET $idc = -($idc + (SELECT n FROM _off WHERE t='$tbl')); UPDATE main.$tbl SET $idc = -$idc;"
  done
  s+=" UPDATE main.notify_sends SET event_id = event_id + (SELECT n FROM _off WHERE t='notify_events');
       UPDATE main.notify_events SET batch_id = batch_id + (SELECT n FROM _off WHERE t='notify_events') WHERE batch_id IS NOT NULL;
       UPDATE main.observations SET digest_id = digest_id + (SELECT n FROM _off WHERE t='digests') WHERE digest_id IS NOT NULL;
       UPDATE main.pr_queue SET depends_on = depends_on + (SELECT n FROM _off WHERE t='pr_queue') WHERE depends_on IS NOT NULL;"
  # ref の "<接頭辞>-<数字>" だけを直す(owner/repo#12 などはそのまま)
  for t in $IMPORT_ID_TABLES; do
    import_id_split "$t"; p=${t#*:}; [[ -n $p ]] || continue
    for col in messages.ref notify_events.ref; do
      s+=" UPDATE main.${col%%.*} SET ${col#*.} = '$p-' || (CAST(substr(${col#*.}, $((${#p} + 2))) AS INTEGER) + (SELECT n FROM _off WHERE t='$tbl'))
           WHERE ${col#*.} LIKE '$p-%' AND substr(${col#*.}, $((${#p} + 2))) != '' AND substr(${col#*.}, $((${#p} + 2))) NOT GLOB '*[^0-9]*';"
    done
  done
  # 2. タスク ID がぶつかるものを付け替える(同じ project の、両方の DB を通した次の番号)。参照も直す
  s+=" CREATE TEMP TABLE _ren (old TEXT PRIMARY KEY, new TEXT);
       CREATE TEMP TABLE _next (project TEXT PRIMARY KEY, n INTEGER);
       INSERT INTO _next SELECT project, MAX(CAST(substr(id, length(project) + 4) AS INTEGER))
         FROM (SELECT id, project FROM dst.tasks UNION ALL SELECT id, project FROM main.tasks) GROUP BY project;
       CREATE TEMP TABLE _clash AS SELECT m.id, m.project, ROW_NUMBER() OVER (PARTITION BY m.project ORDER BY m.id) AS k
         FROM main.tasks m WHERE EXISTS (SELECT 1 FROM dst.tasks d WHERE d.id = m.id);
       INSERT INTO _ren SELECT c.id, 'T-' || c.project || '-' || printf('%03d', (SELECT n FROM _next x WHERE x.project = c.project) + c.k) FROM _clash c;
       UPDATE main.tasks SET id = (SELECT new FROM _ren WHERE old = id) WHERE id IN (SELECT old FROM _ren);"
  for col in $IMPORT_TASK_REFS; do
    s+=" UPDATE main.${col%%.*} SET ${col#*.} = (SELECT new FROM _ren WHERE old = ${col#*.}) WHERE ${col#*.} IN (SELECT old FROM _ren);"
  done
  s+=" UPDATE main.messages SET ref = (SELECT new FROM _ren WHERE old = ref) WHERE ref IN (SELECT old FROM _ren);
       UPDATE main.notify_events SET ref = (SELECT new FROM _ren WHERE old = ref) WHERE ref IN (SELECT old FROM _ren);"
  # 3. 足す。行のセッションは写しの移行で取り込み先のものになっている
  local cols
  for t in tasks $IMPORT_ID_TABLES; do
    import_id_split "$t"; cols=$(import_cols "$tbl")
    s+=" INSERT INTO dst.$tbl ($cols) SELECT $cols FROM main.$tbl;"
  done
  for tbl in agent_activity observations tb_flags notify_seen; do
    cols=$(import_cols "$tbl")
    s+=" INSERT OR IGNORE INTO dst.$tbl ($cols) SELECT $cols FROM main.$tbl;"
  done
  cols=$(import_cols agents)
  s+=" INSERT INTO dst.agents ($cols) SELECT $cols FROM main.agents WHERE true
       ON CONFLICT (herdr_session, name) DO UPDATE SET role = COALESCE(role, excluded.role), model = COALESCE(model, excluded.model),
         cwd = COALESCE(cwd, excluded.cwd), session_id = COALESCE(session_id, excluded.session_id),
         spawned_by = COALESCE(spawned_by, excluded.spawned_by), spawn_task = COALESCE(spawn_task, excluded.spawn_task);"
  s+=" INSERT INTO dst.tb_flags (herdr_session, key, value, set_by, set_at) VALUES ($sqs, $(q "$3"), $(q "$(now)"), 'human', $(q "$(now)"));"
  # 4. 件数(ずらした番号の範囲と、付け替えたタスク)
  s+=" SELECT 'tasks: ' || (SELECT COUNT(*) FROM main.tasks) || ' 件(付け替え ' || (SELECT COUNT(*) FROM _ren) || ' 件)';
       SELECT '  ' || old || ' -> ' || new FROM _ren ORDER BY old;"
  for t in $IMPORT_ID_TABLES; do
    import_id_split "$t"
    s+=" SELECT '$tbl: ' || COUNT(*) || ' 件' || CASE WHEN COUNT(*) > 0 AND (SELECT n FROM _off WHERE t='$tbl') > 0
           THEN '(番号は +' || (SELECT n FROM _off WHERE t='$tbl') || ')' ELSE '' END FROM main.$tbl HAVING COUNT(*) > 0;"
  done
  s+=" SELECT 'agents: ' || (SELECT COUNT(*) FROM main.agents) || ' 件';"
  printf '%s' "$s"
}

# IMPORT_ID_TABLES の 1 つ(<表>[.<列>]:<接頭辞>)を tbl と idc(番号の列。既定 id)に分ける
import_id_split() { tbl=${1%%:*}; idc=id; if [[ $tbl == *.* ]]; then idc=${tbl#*.}; tbl=${tbl%%.*}; fi; }

# 表の列(共有の DB と写しは同じ版なので同じ列)。カンマ区切り
import_cols() { sql "SELECT group_concat(name, ', ') FROM pragma_table_info($(q "$1"));"; }
