# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- review-pack の「人への依頼の繰り返し」と「仕事の混在」
_saved_db=$TB_DB
export TB_DB=$TMP/review-gov.db
$TB list >/dev/null 2>&1   # DB を作る
rgsql() { sqlite3 "$TB_DB" "$@"; }
NOW="strftime('%Y-%m-%dT%H:%M:%SZ','now')"
rgsql "INSERT INTO outbox (from_name, from_pane, from_session, target, kind, text, status, reason, created_at) VALUES
  ('conductor','p','s','worker1','task','a','pending','approvals',$NOW), ('conductor','p','s','worker1','task','b','sent','x / 人が承認 t',$NOW),
  ('conductor','p','s','worker2','task','c','sent','allow',$NOW);
  INSERT INTO agent_ops (op, target, args, requested_by, status, created_at, decided_at) VALUES
  ('stop','w9','{}','conductor','done','2026-01-01T00:00:00Z',$NOW), ('stop','w9','{}','conductor','pending',$NOW,NULL),
  ('stop','w8','{}','conductor','done',$NOW,$NOW);
  INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at) VALUES
  ('T-x-001','question','鍵はどこ?','worker1','p','s',$NOW), ('T-x-001','question','鍵はどこ?','worker1','p','s',$NOW);
  INSERT INTO agents (name, state, created_by, created_at, updated_at, role) VALUES
  ('worker1','running','human',$NOW,$NOW,'worker'), ('worker2','running','human',$NOW,$NOW,'worker'), ('worker3','running','human',$NOW,$NOW,NULL);
  INSERT INTO tasks (id, project, title, owner_agent, status, created_at, updated_at) VALUES
  ('T-a-001','a','x','worker1','in_progress',$NOW,$NOW), ('T-b-001','b','y','worker1','review',$NOW,$NOW), ('T-a-002','a','z','worker2','in_progress',$NOW,$NOW);
  INSERT INTO role_changes (agent, old_role, new_role, changed_by, created_at) VALUES ('worker3','worker','watcher','hr1',$NOW);"
out=$($TB review-pack --since 2000-01-01T00:00:00Z)
sect() { awk -v h="$1" 'index($0, "## " h) == 1 {on=1; next} /^## / {on=0} on && NF' <<<"$out"; }
eq "review-pack: repeated asks to a human (2+ only)" "$(sect '人への依頼の繰り返し' | sort | tr '\n' ',')" \
  "conductor | agent:stop | w9 | 2,conductor | send:task | worker1 | 2,worker1 | question | 鍵はどこ? | 2,"
eq "review-pack: agents doing more than one kind of work" "$(sect '仕事の混在' | sort | tr '\n' ',')" "worker1 | worker | 2 | 0 | a,b,worker3 | - | - | 1 | -,"
export TB_DB=$_saved_db
