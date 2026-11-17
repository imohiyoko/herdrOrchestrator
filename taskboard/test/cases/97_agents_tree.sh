# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb agents --tree(指揮系統の論理ツリー)と --json の parent / parent_via
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_conf=$TB_CONFIG
export TB_DB=$TMP/tree.db TB_CONFIG=$TMP/tree-projects.conf
printf 'projb=b-conductor\n' >"$TB_CONFIG"
trsql() { sqlite3 "$TB_DB" "$@"; }
ag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"%s","agent_session":{"value":"s-%s"}}' "$1" "$1" "${2:-idle}" "$1"; }
export FAKE_AGENTS="[$(ag conductor),$(ag b-conductor),$(ag worker1 working),$(ag worker2),$(ag worker3),$(ag watcher)]"
$TB agents >/dev/null 2>&1   # 台帳を作る
trsql "UPDATE agents SET spawned_by='worker1', spawn_task='T-a-001' WHERE name='worker3';"
tid=$($TB add -p projb -t 'b work' --owner worker2); $TB update "$tid" --status in_progress >/dev/null
parents=$($TB agents --json | jq -r '"\(.name)=\(.parent // "-")/\(.parent_via // "-")"' | sort | tr '\n' ' ')
eq "agents --json: parent by spawn > project conductor > default conductor > top" "$parents" \
  "b-conductor=-/- conductor=-/- watcher=-/- worker1=conductor/default worker2=b-conductor/project worker3=worker1/spawn "
exp=$(cat <<'TREE'
human
├─ b-conductor  running/idle  conductor  tasks:0 q:0
│  └─ worker2  running/idle  worker  tasks:1 q:0
├─ conductor  running/idle  conductor  tasks:0 q:0
│  └─ worker1  running/working  worker  tasks:0 q:0
│     └─ worker3  running/idle  worker  tasks:0 q:0  (spawn: T-a-001)
└─ watcher  running/idle  watcher  tasks:0 q:0
TREE
)
eq "agents --tree: human → conductor → worker → spawned child" "$($TB agents --tree)" "$exp"
# 親が一覧に居ない・循環する → 最上位
trsql "UPDATE agents SET spawned_by='worker3' WHERE name='worker1';"
eq "agents --tree: a spawn cycle falls back to the top" "$($TB agents --json | jq -r 'select(.name=="worker1" or .name=="worker3") | .parent // "-"' | sort | tr '\n' ' ')" "- - "
trsql "UPDATE agents SET spawned_by='gone' WHERE name='worker1'; UPDATE agents SET spawned_by=NULL WHERE name='worker3';"
eq "agents --tree: unknown parent → default rule" "$($TB agents --json | jq -r 'select(.name=="worker1") | "\(.parent)/\(.parent_via)"')" "conductor/default"
export TB_CONFIG=$_saved_conf
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
