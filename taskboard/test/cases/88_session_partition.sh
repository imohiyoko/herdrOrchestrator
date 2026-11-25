# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 1 つの DB を herdr セッションで分ける(schema/012): 他のセッションの行は見えない・触れない。タスク ID は DB 全体で一意
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_hs=${HERDR_SESSION-}; unset HERDR_SESSION
export TB_DB=$TMP/partition.db TB_CONFIG=$TMP/partition.conf
printf 'iso=conductor\n' >"$TB_CONFIG"
psql() { sqlite3 "$TB_DB" "$@"; }
A() { HERDR_SESSION=sa "$TB" "$@"; }
B() { HERDR_SESSION=sb "$TB" "$@"; }

ta=$(A add -p iso -t 'task of a'); tb_=$(B add -p iso -t 'task of b')
eq "partition: task IDs are unique across sessions" "$ta $tb_" "T-iso-001 T-iso-002"
eq "partition: rows carry their session" "$(psql "SELECT id||'='||herdr_session FROM tasks ORDER BY id;" | tr '\n' ' ')" "T-iso-001=sa T-iso-002=sb "
eq "partition: list shows only the session's tasks" "$(A list | cut -f1) / $(B list | cut -f1)" "T-iso-001 / T-iso-002"
eq "partition: show of another session's task fails" "$(B show "$ta" >/dev/null 2>&1 && echo shown || echo refused)" "refused"
eq "partition: claim / update / done / block of another session's task fail and change nothing" \
  "$(B claim "$ta" --owner w >/dev/null 2>&1; echo -n $?; B update "$ta" --title x >/dev/null 2>&1; echo -n " $?"; B done "$ta" -d x.md -m s >/dev/null 2>&1; echo -n " $?"; B block "$ta" >/dev/null 2>&1; echo " $?") $(psql "SELECT status||'|'||title FROM tasks WHERE id='$ta';")" \
  "1 1 1 1 todo|task of a"
eq "partition: --parent must be a task of the same session" "$(B add -p iso -t child --parent "$ta" >/dev/null 2>&1 && echo added || echo refused)" "refused"
# 報告(done → pending → report)もセッションごと
A claim "$ta" --owner wa >/dev/null; A done "$ta" -d a.md -m 'a finished' >/dev/null
eq "partition: pending is per session" "$(A pending --ids | tr '\n' ' ')/$(B pending --ids | tr '\n' ' ')" "$ta /"
# コメント・送信の承認待ち・アラート・agent の台帳
FAKE_AGENTS='[{"pane_id":"wA:p1","name":"wa","agent_session":{"value":"sa1"}}]' HERDR_PANE_ID=wA:p1 A comment "$ta" --kind question --text 'q from a' >/dev/null 2>&1
eq "partition: comments are per session" "$(A comments --json | jq -r .id | tr '\n' ' ')/$(B comments --json | jq -r .id | tr '\n' ' ')" "C-1 /"
eq "partition: resolve of another session's comment fails" "$(B resolve C-1 >/dev/null 2>&1 && echo resolved || echo refused) $(psql "SELECT resolved_at IS NULL FROM comments WHERE id=1;")" "refused 1"
A alert --title 'disk full' >/dev/null 2>&1
eq "partition: alerts are per session" "$(A alerts --json | jq -r .id | tr '\n' ' ')/$(B alerts --json | jq -r .id | tr '\n' ' ')" "A-1 /"
export FAKE_AGENTS='[{"pane_id":"wA:p1","name":"same","agent_status":"idle","agent_session":{"value":"s-a"}}]'
A agents >/dev/null
export FAKE_AGENTS='[{"pane_id":"wB:p1","name":"same","agent_status":"idle","agent_session":{"value":"s-b"}}]'
B agents >/dev/null
eq "partition: the same agent name lives in each session's ledger" "$(psql "SELECT herdr_session||':'||session_id FROM agents WHERE name='same' ORDER BY herdr_session;" | tr '\n' ' ')" "sa:s-a sb:s-b "
export FAKE_AGENTS='[]'; A agents >/dev/null
eq "partition: a sync of one session never stops the other session's agent" "$(psql "SELECT herdr_session||':'||state FROM agents WHERE name='same' ORDER BY herdr_session;" | tr '\n' ' ')" "sa:stopped sb:running "
unset FAKE_AGENTS
# hook の活動記録: 書いたセッションが残る。一覧はセッションごと、session_id で 1 行を引くときは絞らない
HERDR_SESSION=sa "$TB" touch --event UserPromptSubmit --session-id act-a --cwd /x
HERDR_SESSION=sb "$TB" touch --event UserPromptSubmit --session-id act-b --cwd /y
eq "partition: touch records the session" "$(psql "SELECT session_id||'='||herdr_session FROM agent_activity ORDER BY session_id;" | tr '\n' ' ')" "act-a=sa act-b=sb "
eq "partition: activity lists only the session's rows" "$(A activity --json | jq -r .session_id | tr '\n' ' ')/$(B activity --json | jq -r .session_id | tr '\n' ' ')" "act-a /act-b "
# キーが主キーの表(tb_flags / notify_seen / observations)は、セッションとの組で一意
A spawn-freeze >/dev/null 2>&1
eq "partition: a flag set in one session is not set in the other" "$(psql "SELECT herdr_session||'='||value FROM tb_flags WHERE key='spawn_frozen' ORDER BY herdr_session;" | tr '\n' ' ')" "sa=1 "
psql "INSERT INTO tb_flags (herdr_session, key, value, set_by, set_at) VALUES ('sb','spawn_frozen','0','human','t');"
eq "partition: the same flag key per session" "$(psql "SELECT herdr_session||'='||value FROM tb_flags WHERE key='spawn_frozen' ORDER BY herdr_session;" | tr '\n' ' ')" "sa=1 sb=0 "

# --- 移行: 1 セッション = 1 DB だった行は、移行を流したセッションのものになる
export TB_DB=$TMP/partition-legacy.db; rm -f "$TB_DB"
for f in "$HERE"/../schema/0{01..11}_*.sql; do cat "$f"; echo "PRAGMA user_version=$((10#$(basename "$f" | cut -c1-3)));"; done | sqlite3 "$TB_DB"
psql "INSERT INTO tasks (id, project, title, created_at, updated_at) VALUES ('T-lg-001','lg','legacy','t','t');
  INSERT INTO agents (name, herdr_session, state, killable, created_by, created_at, updated_at) VALUES ('wl','ops','stopped','unknown','h','t','t'), ('wd','','stopped','unknown','h','t','t');
  INSERT INTO tb_flags (key, value, set_at) VALUES ('spawn_frozen','1','t');"
HERDR_SESSION=mig "$TB" list >/dev/null
eq "migrate: existing rows become rows of the session that ran the migration" "$(psql "SELECT herdr_session FROM tasks; SELECT herdr_session FROM tb_flags;" | tr '\n' ' ')" "mig mig "
eq "migrate: the ledger keeps each agent's recorded session (empty → default)" "$(psql "SELECT name||'='||herdr_session FROM agents ORDER BY name;" | tr '\n' ' ')" "wd=default wl=ops "

# --- tb db-import: 旧形式の DB を、共有の DB の 1 セッションとして取り込む
export TB_DB=$TMP/partition-shared.db; rm -f "$TB_DB"
OLD=$TMP/partition-old.db; rm -f "$OLD"
for f in "$HERE"/../schema/0{01..11}_*.sql; do cat "$f"; echo "PRAGMA user_version=$((10#$(basename "$f" | cut -c1-3)));"; done | sqlite3 "$OLD"
sqlite3 "$OLD" "INSERT INTO tasks (id, project, title, parent_id, created_at, updated_at) VALUES ('T-pj-001','pj','old 1',NULL,'t','t'), ('T-pj-002','pj','old 2','T-pj-001','t','t'), ('T-zz-001','zz','old only',NULL,'t','t');
  INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at) VALUES ('T-pj-002','question','old q','w','p','s','t');
  INSERT INTO messages (kind, ref, target, body, result, created_at) VALUES ('comment','C-1','c','b','sent','t'), ('task','T-pj-002','c','b','sent','t');
  INSERT INTO notify_events (type, ref, severity, text, hash, sinks, slack_status, created_at) VALUES ('comment','C-1','info','x','h','file','sent','t');
  INSERT INTO notify_sends (event_id, via, at) VALUES (1,'file','t');
  INSERT INTO agents (name, herdr_session, state, killable, created_by, created_at, updated_at, role, model) VALUES ('wk','imp','stopped','unknown','h','t','t','worker','opus');
  INSERT INTO alerts (fingerprint, title, source, status, count, first_seen, last_seen) VALUES ('fo','old alert','cli','new',1,'t','t');
  INSERT INTO messages (kind, ref, target, body, result, created_at) VALUES ('alert','A-1','c','b','sent','t');
  INSERT INTO proposals (role_file, title, text, status, by_name, created_at) VALUES ('-','old proposal','x','open','au','t');"
before=$(sqlite3 "$OLD" .dump | cksum)
HERDR_SESSION=main "$TB" add -p pj -t 'main 1' >/dev/null; HERDR_SESSION=main "$TB" add -p pj -t 'main 2' >/dev/null
printf '* alert:analyze * = ask\n' >"$TMP/sp-policy.conf"; TB_POLICY=$TMP/sp-policy.conf HERDR_SESSION=main "$TB" alert --title 'main alert' >/dev/null 2>&1
FAKE_AGENTS='[{"pane_id":"wM:p1","name":"wm","agent_session":{"value":"sm1"}}]' HERDR_PANE_ID=wM:p1 HERDR_SESSION=main "$TB" comment T-pj-001 --kind progress --text 'main c' >/dev/null 2>&1
export FAKE_AGENTS='[{"pane_id":"wK:p1","name":"wk","agent_status":"idle","agent_session":{"value":"s-new"}}]'
HERDR_SESSION=imp "$TB" agents >/dev/null; unset FAKE_AGENTS
out=$(HERDR_SESSION=imp "$TB" db-import "$OLD" --dry-run)
eq "db-import --dry-run: reports and writes nothing" "$(grep -c 'T-pj-001 -> T-pj-003' <<<"$out") $(psql "SELECT COUNT(*) FROM tasks WHERE project='pj';")" "1 2"
out=$(HERDR_SESSION=imp "$TB" db-import "$OLD"); r=$?
eq "db-import: exit 0" "$r" "0"
eq "db-import: colliding task IDs move to the next free numbers; the rest keep theirs" "$(psql "SELECT id||'='||herdr_session||'='||COALESCE(parent_id,'-') FROM tasks WHERE project NOT IN ('alert','proposal') ORDER BY id;" | tr '\n' ' ')" \
  "T-pj-001=main=- T-pj-002=main=- T-pj-003=imp=- T-pj-004=imp=T-pj-003 T-zz-001=imp=- "
eq "db-import: integer IDs are shifted after the shared DB's; references follow" \
  "$(psql "SELECT id||':'||task_id||':'||herdr_session FROM comments ORDER BY id; SELECT ref FROM messages ORDER BY id; SELECT id||':'||ref FROM notify_events; SELECT event_id FROM notify_sends;" | tr '\n' ' ')" \
  "1:T-pj-001:main 2:T-pj-004:imp C-2 T-pj-004 A-2 1:C-2 1 "
eq "db-import: alerts and proposals come as tickets; numbers and ticket IDs move after the shared DB's" \
  "$(psql "SELECT 'A-'||alert_no||'='||task_id||'='||herdr_session FROM alert_details ORDER BY alert_no; SELECT 'P-'||proposal_no||'='||task_id FROM proposal_details;" | tr '\n' ' ')" \
  "A-1=T-alert-001=main A-2=T-alert-002=imp P-1=T-proposal-001 "
eq "db-import: the imported alert reads through the view" "$(HERDR_SESSION=imp "$TB" alerts | cut -f1,7 | tr '\t\n' '  ')" "A-2 old alert "
eq "db-import: the session sees the imported rows (alerts and proposals as tickets)" "$(HERDR_SESSION=imp "$TB" list | cut -f1 | tr '\n' ' ')" "T-alert-002 T-pj-003 T-pj-004 T-proposal-001 T-zz-001 "
eq "db-import: an agent already in the ledger keeps its row; empty values come from the old DB" "$(psql "SELECT session_id||'|'||COALESCE(role,'-')||'|'||COALESCE(model,'-') FROM agents WHERE name='wk';")" "s-new|worker|opus"
eq "db-import: the old DB is only read" "$(sqlite3 "$OLD" .dump | cksum)" "$before"
eq "db-import: the same file twice is refused" "$(HERDR_SESSION=imp "$TB" db-import "$OLD" 2>&1 | grep -c '取り込み済み') $(psql "SELECT COUNT(*) FROM tasks WHERE project NOT IN ('alert','proposal');")" "1 5"
eq "db-import: a DB of the new format is refused" "$(HERDR_SESSION=x "$TB" db-import "$TMP/partition.db" 2>&1 | grep -c '旧形式')" "1"
eq "db-import: the shared DB itself is refused" "$(HERDR_SESSION=x "$TB" db-import "$TB_DB" 2>&1 | grep -c '同じ DB')" "1"
# 消したチケットの番号は、取り込みでも使い直さない
OLD2=$TMP/partition-old2.db; rm -f "$OLD2"
for f in "$HERE"/../schema/0{01..11}_*.sql; do cat "$f"; echo "PRAGMA user_version=$((10#$(basename "$f" | cut -c1-3)));"; done | sqlite3 "$OLD2"
sqlite3 "$OLD2" "INSERT INTO tasks (id, project, title, created_at, updated_at) VALUES ('T-qq-001','qq','old qq','t','t');"
q1=$(HERDR_SESSION=main "$TB" add -p qq -t 'main qq'); HERDR_SESSION=main "$TB" ticket rm "$q1" >/dev/null
HERDR_SESSION=imp2 "$TB" db-import "$OLD2" >/dev/null
eq "db-import: a removed ticket's number is not reused" "$(psql "SELECT id FROM tasks WHERE project='qq';")" "T-qq-002"
eq "db-import: not from an agent pane" "$(FAKE_AGENTS='[{"pane_id":"wK:p1","name":"wk"}]' HERDR_PANE_ID=wK:p1 HERDR_SESSION=imp rc "$TB" db-import "$OLD")" "1"

if [[ -n $_saved_hs ]]; then export HERDR_SESSION=$_saved_hs; fi
export HERDR_PANE_ID=$_saved_pane
