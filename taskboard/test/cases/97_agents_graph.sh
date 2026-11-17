# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb agents --graph(関係グラフ)/ --group role / 規則の @<役割> 指定(routes.conf・approvals.conf・spawn-policy)
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_conf=$TB_CONFIG; _saved_appr=${TB_APPROVALS-}; _saved_routes=${TB_ROUTES-}
export TB_DB=$TMP/graph.db TB_CONFIG=$TMP/graph-projects.conf
printf 'projb=b-conductor\n' >"$TB_CONFIG"
grsql() { sqlite3 "$TB_DB" "$@"; }
ag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"%s","agent_session":{"value":"s-%s"}}' "$1" "$1" "${2:-idle}" "$1"; }
export FAKE_AGENTS="[$(ag conductor),$(ag b-conductor),$(ag worker1 working),$(ag worker2),$(ag worker3),$(ag watcher)]"
$TB agents >/dev/null 2>&1   # 台帳を作る
# 役割: 名前と map.conf から(worker3 は台帳の role で)
grsql "UPDATE agents SET spawned_by='worker1', spawn_task='T-a-001' WHERE name='worker3';"
tid=$($TB add -p projb -t 'b work' --owner worker2); $TB update "$tid" --status in_progress >/dev/null
tid2=$($TB add -p proja -t 'a work' --owner worker2); $TB update "$tid2" --status review >/dev/null
NOW="strftime('%Y-%m-%dT%H:%M:%SZ','now')"
grsql "INSERT INTO outbox (from_name, from_pane, from_session, target, kind, text, status, created_at) VALUES
  ('conductor','wconductor:p1','s','worker1','task','x','sent',$NOW), ('conductor','wconductor:p1','s','worker1','task','y','sent',$NOW),
  ('worker1','wworker1:p1','s','worker2','task','z','denied',$NOW), ('conductor','wconductor:p1','s','worker1','info','old','sent','2000-01-01T00:00:00Z');
  INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at) VALUES ('$tid','progress','p','worker2','wworker2:p1','s',$NOW);
  INSERT INTO alerts (fingerprint, title, source, status, analyst, first_seen, last_seen) VALUES ('f','t','watcher','analyzing','analyst-A1',$NOW,$NOW);"

edges=$($TB agents --graph --json | jq -r '.edges | sort_by(.from, .to, .verb) | .[] | "\(.from) \(.verb) \(.to) \(.status) \(.n)"' | tr '\n' ',')
eq "agents --graph: typed edges from spawn / lead / send / report / analyze (a node may have several upstreams)" "$edges" \
  "b-conductor lead worker2 ok 1,conductor send:task worker1 ok 2,conductor lead worker2 ok 1,watcher analyze analyst-A1 ok 1,worker1 send:task worker2 denied 1,worker1 spawn worker3 ok 1,worker2 report b-conductor ok 1,"
eq "agents --graph: a name only seen on an edge becomes a node with its role" "$($TB agents --graph --json | jq -r '.nodes[] | select(.name=="analyst-A1") | "\(.state) \(.role)"')" "- analyst"
exp=$(cat <<'G'
[analyst]
  analyst-A1  -/-
[conductor]
  b-conductor  running/idle
  conductor  running/idle
[watcher]
  watcher  running/idle
[worker]
  worker1  running/working
  worker2  running/idle
  worker3  running/idle

関係(直近 7 日。spawn と lead は今の状態):
  b-conductor  -lead→  worker2
  conductor  -send:task ×2→  worker1
  conductor  -lead→  worker2
  watcher  -analyze→  analyst-A1
  worker1  -send:task→  worker2  [denied]
  worker1  -spawn→  worker3
  worker2  -report→  b-conductor
G
)
eq "agents --graph: grouped by role, then the edges" "$($TB agents --graph)" "$exp"
eq "agents --group role: list grouped by role" "$($TB agents --group role | grep -v '^  ' | tr '\n' ' ')" "[conductor] [watcher] [worker] "
eq "agents --group: only role" "$(rc $TB agents --group kind)" "2"

# 規則の @<役割>
export TB_ROUTES=$TMP/graph-routes.conf
printf '!@worker -> @conductor\n@conductor -> @worker\nworker1 -> worker2\n' >"$TB_ROUTES"
HERDR_PANE_ID=wconductor:p1 $TB send worker2 --text hi --kind info >/dev/null 2>&1
eq "routes: @conductor -> @worker allows by role" "$(grsql "SELECT (status!='denied')||' '||reason FROM outbox WHERE text='hi';" | cut -d" " -f1-5)" "1 routes.conf: @conductor -> @worker"
HERDR_PANE_ID=wworker1:p1 $TB send b-conductor --text up --kind info >/dev/null 2>&1
eq "routes: !@worker -> @conductor denies by role" "$(grsql "SELECT status||' '||reason FROM outbox WHERE text='up';")" "denied routes.conf: !@worker -> @conductor"
printf '!@worker -> @conductor\n* -> *\n' >"$TB_ROUTES"
grsql "ALTER TABLE agents RENAME COLUMN role TO role_x;"
HERDR_PANE_ID=wworker1:p1 $TB send b-conductor --text up2 --kind info >/dev/null 2>&1
grsql "ALTER TABLE agents RENAME COLUMN role_x TO role;"
eq "routes: a ! rule whose role cannot be read stops (does not fall through to * -> *)" "$(grsql "SELECT status FROM outbox WHERE text='up2';") $(grsql "SELECT reason FROM outbox WHERE text='up2';" | grep -c '確かめられません')" "denied 1"
export TB_APPROVALS=$TMP/graph-appr.conf
printf 'send:info:@conductor = ask\nsend:*:@worker = allow\nagent:stop:@watch* = allow\n* = ask\n' >"$TB_APPROVALS"
eq "approvals: send:*:@worker matches by the target's role" "$($TB approval send:task:worker3 | tr '\n' ' ')" "allow rule: send:*:@worker = allow "
eq "approvals: role glob" "$($TB approval agent:stop:watcher | head -1) $($TB approval agent:stop:worker1 | head -1)" "allow ask"
eq "approvals: @role does not match a name without that role" "$($TB approval send:info:b-conductor | head -1) $($TB approval send:info:nobody | sed -n 2p)" "ask rule: * = ask"
eq "allow: @ is accepted in a pattern" "$(rc $TB allow 'send:answer:@worker') $(grep -c '^send:answer:@worker = allow' "$TB_APPROVALS")" "0 1"
export TB_CONFIG=$_saved_conf TB_APPROVALS=$_saved_appr
if [[ -n $_saved_routes ]]; then export TB_ROUTES=$_saved_routes; else unset TB_ROUTES; fi
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane

# --- tb role-set(ロール変更は人と人事役だけ)/ role-log / 関係グラフの role の辺
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
export TB_DB=$TMP/graph.db TB_APPROVALS=$TMP/graph-appr2.conf
printf '* = ask\n' >"$TB_APPROVALS"
export FAKE_AGENTS="[$(ag conductor),$(ag worker1),$(ag worker2),$(ag hr1),$(ag watcher)]"
$TB agents >/dev/null 2>&1
eq "role-set: unknown role → 2" "$(rc $TB role-set worker1 nosuch)" "2"
eq "role-set: not from a non-hr agent" "$(HERDR_PANE_ID=wworker1:p1 $TB role-set worker2 watcher 2>&1 | grep -c '人と人事役')" "1"
eq "role-set: hr needs role:change = allow (20)" "$(HERDR_PANE_ID=whr1:p1 rc $TB role-set worker2 watcher)" "20"
printf 'role:change = allow\n* = ask\n' >"$TB_APPROVALS"
eq "role-set: hr cannot change itself or grant hr" "$(HERDR_PANE_ID=whr1:p1 rc $TB role-set hr1 worker) $(HERDR_PANE_ID=whr1:p1 rc $TB role-set worker2 hr)" "1 1"
eq "role-set: hr with allow" "$(HERDR_PANE_ID=whr1:p1 $TB role-set worker2 watcher --reason 'ログ番に' | cut -d' ' -f1-5)" "worker2 role worker -> watcher"
eq "role-set: the new role is used by rules" "$($TB approval agent:stop:worker2 >/dev/null; printf 'agent:stop:@watcher = allow\n* = ask\n' >"$TB_APPROVALS"; $TB approval agent:stop:worker2 | head -1)" "allow"
eq "role-set: a human can grant hr" "$($TB role-set worker1 hr | cut -d' ' -f1-5)" "worker1 role worker -> hr"
eq "role-set: a name with its own roles file is refused" "$(rc $TB role-set watcher worker)" "1"
eq "role-log: newest first, with who and why" "$($TB role-log | cut -f2-5 | tr '\t\n' '| ')" "worker1|worker -> hr|human| worker2|worker -> watcher|hr1|ログ番に "
eq "agents --graph: hr's change is a role edge (a human's is not)" "$($TB agents --graph --json | jq -r '[.edges[] | select(.verb=="role") | "\(.from)>\(.to)"] | join(",")')" "hr1>worker2"
unset FAKE_AGENTS; export TB_APPROVALS=$_saved_appr HERDR_PANE_ID=$_saved_pane

# --- マージ前の修正: 作成の承認は要求した役割で / human は予約名 / 台帳を読めないときは @ に一致させない
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
export TB_DB=$TMP/graph.db TB_APPROVALS=$TMP/graph-appr3.conf TB_SPAWN_RETRY_SEC=0 TB_AGENT_CWD=$TMP
printf 'agent:new:@worker = allow\n* = ask\n' >"$TB_APPROVALS"
export FAKE_AGENTS="[$(ag conductor),$(ag worker1)]"
out=$(HERDR_PANE_ID=wconductor:p1 $TB agent-new worker9 --role watcher 2>&1; echo "rc=$?")
eq "agent-new: @worker approval does not cover a worker-named agent asking for another role" "$(tail -1 <<<"$out") $(grep -c 'pending' <<<"$out")" "rc=20 1"
rm -rf "$TMP/roles97"; cp -r "$HERE/../../roles" "$TMP/roles97"; printf 'x\n' >"$TMP/roles97/watcher2.md"
eq "agent-new: --role cannot differ from the name's own roles file" "$(TB_ROLES=$TMP/roles97 rc $TB agent-new watcher2 --role worker) $(TB_ROLES=$TMP/roles97 $TB agent-new watcher2 --role worker 2>&1 | grep -c 'roles/watcher2.md')" "2 1"
_lid=$(sqlite3 "$TB_DB" "SELECT 'L-'||id FROM agent_ops WHERE op='new' AND target='worker9' AND status='pending' ORDER BY id DESC LIMIT 1;")
cp "$TMP/roles97/watcher.md" "$TMP/roles97/worker9.md"
eq "agent-approve: a roles file made while pending stops it before --always is written" "$(TB_ROLES=$TMP/roles97 rc $TB agent-approve "$_lid" --always) $(grep -c 'worker9' "$TB_APPROVALS") $(sqlite3 "$TB_DB" "SELECT status FROM agent_ops WHERE 'L-'||id='$_lid';")" "1 0 pending"
eq "agent-new: human is reserved" "$(rc $TB agent-new human)" "2"
export FAKE_AGENTS="[$(ag conductor),$(ag human)]"
eq "requester: an agent named human is not treated as the human" "$(HERDR_PANE_ID=whuman:p1 rc $TB role-set conductor worker)" "1"
export FAKE_AGENTS="[$(ag conductor),$(ag worker1)]"
printf 'agent:stop:@worker = allow\n* = ask\n' >"$TB_APPROVALS"
eq "approvals: @worker matches a worker by map.conf" "$($TB approval agent:stop:worker3 | head -1)" "allow"
eq "approvals: @role does not match when the registry cannot be read" "$(sqlite3 "$TB_DB" "ALTER TABLE agents RENAME TO agents_x;"; $TB approval agent:stop:worker3 2>/dev/null | head -1; sqlite3 "$TB_DB" "ALTER TABLE agents_x RENAME TO agents;")" "ask"
eq "approvals: @role does not match when only the role lookup fails" "$(sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role TO role_x;"; $TB approval agent:stop:worker3 2>/dev/null | head -1; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role_x TO role;")" "ask"
printf 'agent:stop:@nosuch = ask\n* = allow\n' >"$TB_APPROVALS"
eq "approvals: a role rule that cannot be checked stops at ask (does not fall through to * = allow)" "$(sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role TO role_x;"; $TB approval agent:stop:worker3 2>/dev/null | head -1; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role_x TO role;") $($TB approval agent:stop:worker3 | head -1)" "ask allow"
eq "role-set: old_role is the effective role (registry role without a file falls back to map.conf)" "$(sqlite3 "$TB_DB" "UPDATE agents SET role='gone' WHERE name='worker1';"; $TB role-set worker1 watcher >/dev/null; sqlite3 "$TB_DB" "SELECT old_role FROM role_changes ORDER BY id DESC LIMIT 1;")" "worker"
eq "role-set: change and history are written together" "$($TB role-set worker1 watcher >/dev/null; sqlite3 "$TB_DB" "SELECT (SELECT role FROM agents WHERE name='worker1')||' '||(SELECT new_role FROM role_changes ORDER BY id DESC LIMIT 1);")" "watcher watcher"
export TB_APPROVALS=$_saved_appr
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
