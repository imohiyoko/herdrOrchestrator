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
export TB_APPROVALS=$TMP/graph-appr.conf
printf 'send:info:@conductor = ask\nsend:*:@worker = allow\nagent:stop:@watch* = allow\n* = ask\n' >"$TB_APPROVALS"
eq "approvals: send:*:@worker matches by the target's role" "$($TB approval send:task:worker3 | tr '\n' ' ')" "allow rule: send:*:@worker = allow "
eq "approvals: role glob" "$($TB approval agent:stop:watcher | head -1) $($TB approval agent:stop:worker1 | head -1)" "allow ask"
eq "approvals: @role does not match a name without that role" "$($TB approval send:info:b-conductor | head -1) $($TB approval send:info:nobody | sed -n 2p)" "ask rule: * = ask"
eq "allow: @ is accepted in a pattern" "$(rc $TB allow 'send:answer:@worker') $(grep -c '^send:answer:@worker = allow' "$TB_APPROVALS")" "0 1"
export TB_CONFIG=$_saved_conf TB_APPROVALS=$_saved_appr
if [[ -n $_saved_routes ]]; then export TB_ROUTES=$_saved_routes; else unset TB_ROUTES; fi
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
