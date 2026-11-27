# shellcheck shell=bash
# --- tb agentset: 定義(人だけが書く)、up(1 つの workspace に pane を分けて、親から順に)・status・down(子から順に)
# DB・定義の置き場・設定はこのケースのものだけを使う
_saved_env=$(declare -p HERDR_PANE_ID TB_DB TB_AGENTSETS TB_SPAWN_RETRY_SEC TB_AGENT_CWD FAKE_DYNAMIC FAKE_AGENTS 2>/dev/null)
unset HERDR_PANE_ID
export TB_DB=$TMP/agentset.db TB_AGENTSETS=$TMP/agentsets TB_SPAWN_RETRY_SEC=0 TB_AGENT_CWD=$TMP FAKE_DYNAMIC=1
as_ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"},"cwd":"%s"}' "$2" "$1" "$1" "$TMP"; }
as_sql() { sqlite3 "$TB_DB" "$@"; }
as_reset() { : >"$FAKE_LOG"; rm -f "$FAKE_LOG.closed" "$FAKE_LOG.started" "$FAKE_LOG.splits"; }
export FAKE_AGENTS="[$(as_ag conductor wC:p1)]"
as_reset
$TB agents >/dev/null

as_def="# 見張りのセット
cwd = $TMP
parent = conductor

[member lead]
role = -
prompt = 振り分ける
parent = ~set

[member rev]
count = 2
parent = lead
"
as_def=${as_def//role = -$'\n'/}

# ---- 定義: 人だけが書ける。読めない定義は置かない
eq "agentset: add from an agent pane is refused (deny)" "$(HERDR_PANE_ID=wC:p1 rc $TB agentset add rv <<<"$as_def") $(ls "$TB_AGENTSETS" 2>/dev/null | wc -l | tr -d ' ')" "1 0"
eq "agentset: a broken definition is not saved (usage)" "$(rc $TB agentset add rv <<<$'[member a]\nparent = a\n') $(ls "$TB_AGENTSETS" 2>/dev/null | wc -l | tr -d ' ')" "2 0"
eq "agentset: add / list / show" "$($TB agentset add rv <<<"$as_def") / $($TB agentset list) / $($TB agentset show rv | grep -c '^\[member')" "agentset rv を登録しました / rv / 2"

# ---- up: 1 体目は workspace(label = セット名)、残りはその pane を分けて置く。親は台帳の spawned_by
out=$($TB agentset up rv 2>&1; echo "rc=$?")
eq "agentset up: succeeds" "$(tail -1 <<<"$out")" "rc=0"
eq "agentset up: workspace once, then split right / down" \
  "$(grep -c "^workspace-create workspace create --cwd $TMP --label rv --no-focus" "$FAKE_LOG") $(grep -c '^pane-split wA:p1 --direction right' "$FAKE_LOG") $(grep -c '^pane-split wA:p1 --direction down' "$FAKE_LOG")" "1 1 1"
eq "agentset up: started in order, each in its own pane" \
  "$(grep '^start ' "$FAKE_LOG" | awk '{print $2" "$6}' | paste -sd, -)" "rv-lead wA:p1,rv-rev-1 wS:p1,rv-rev-2 wS:p2"
eq "agentset up: parents in the ledger" "$(as_sql "SELECT name||'<'||IFNULL(spawned_by,'-') FROM agents WHERE name LIKE 'rv-%' ORDER BY name;" | paste -sd, -)" "rv-lead<conductor,rv-rev-1<rv-lead,rv-rev-2<rv-lead"
eq "agentset up: first prompt sent to lead" "$(grep -c '^prompt rv-lead.*振り分ける' "$FAKE_LOG")" "1"
eq "agentset status" "$($TB agentset status rv | cut -f1-5 | paste -sd, -)" $'rv-lead\tlead\trunning\twA:p1\tconductor,rv-rev-1\trev\trunning\twS:p1\trv-lead,rv-rev-2\trev\trunning\twS:p2\trv-lead'
eq "agentset status --json" "$($TB agentset status rv --json | jq -r '.[0].name+" "+.[0].want_parent')" "rv-lead conductor"

# ---- もう一度 up しても何もしない
: >"$FAKE_LOG"
eq "agentset up: again does nothing" "$($TB agentset up rv) $(grep -c '^start\|^workspace-create\|^pane-split' "$FAKE_LOG")" "rv: すべて動いています(何もしません) 0"
eq "agentset up/down from an agent pane is refused" "$(HERDR_PANE_ID=wC:p1 rc $TB agentset up rv) $(HERDR_PANE_ID=wC:p1 rc $TB agentset down rv)" "1 1"

# ---- down: 子から順に止める。外の親は止めない。止めてよい(killable)と判定されていなければ、tb agent stop と同じく --force が要る
: >"$FAKE_LOG"
eq "agentset down: killable=unknown without --force → 1, nothing closed" "$(rc $TB agentset down rv) $(grep -c '^pane-close' "$FAKE_LOG")" "1 0"
: >"$FAKE_LOG"
out=$($TB agentset down rv --force 2>&1; echo "rc=$?")
eq "agentset down --force: succeeds" "$(tail -1 <<<"$out")" "rc=0"
eq "agentset down: children first, conductor untouched" "$(grep '^pane-close' "$FAKE_LOG" | awk '{print $2}' | paste -sd, -)" "wS:p2,wS:p1,wA:p1"
eq "agentset status after down" "$($TB agentset status rv | cut -f3 | sort -u)" "stopped"

# ---- 止めたあとの up は、同じ workspace の形で全員を戻す(revive)
: >"$FAKE_LOG"; rm -f "$FAKE_LOG.splits"
eq "agentset up after down: revives all" "$($TB agentset up rv | grep -c ' revived ') $(grep -c '^workspace-create.*--label rv' "$FAKE_LOG") $(grep -c '^pane-split' "$FAKE_LOG")" "3 1 2"

# ---- 外の親がいない・定義が無い
$TB agentset add solo <<<$'parent = ghost\n[member a]\nparent = ~set\n' >/dev/null
eq "agentset up: an unknown outside parent → 1, nothing started" "$(rc $TB agentset up solo) $(as_sql "SELECT COUNT(*) FROM agents WHERE name='solo-a';")" "1 0"
eq "agentset up: no definition → 1" "$(rc $TB agentset up nope)" "1"
eq "agentset rm" "$($TB agentset rm solo) $($TB agentset list | paste -sd, -)" "agentset solo を消しました rv"

# ---- scale: max を書いたメンバーは <セット>-<メンバー>-<N>。同じ workspace に pane を足し、減らすのは番号の大きい順に止められるものだけ
as_reset
$TB agentset add sc <<<$'cwd = '"$TMP"$'\n[member lead]\n[member w]\nmax = 3\nparent = lead\n' >/dev/null
$TB agentset up sc >/dev/null 2>&1
eq "agentset scale: up starts the count (1)" "$(as_sql "SELECT name FROM agents WHERE name LIKE 'sc-%' ORDER BY name;" | paste -sd, -)" "sc-lead,sc-w-1"
: >"$FAKE_LOG"
eq "agentset scale +2: two new agents, split from the set's panes" "$($TB agentset scale sc w +2 | cut -d' ' -f1-2 | paste -sd, -) $(grep -c '^pane-split' "$FAKE_LOG") $(grep -c '^workspace-create' "$FAKE_LOG")" "sc-w-2: sc-w-2,sc-w-3: sc-w-3 2 0"
eq "agentset scale: new agents are killable=yes with the member's parent" "$(as_sql "SELECT name||' '||killable||' '||spawned_by FROM agents WHERE name IN ('sc-w-2','sc-w-3') ORDER BY name;" | paste -sd, -)" "sc-w-2 yes sc-lead,sc-w-3 yes sc-lead"
eq "agentset scale: over max → 4 (limit), nothing started" "$(rc $TB agentset scale sc w +1) $(as_sql "SELECT COUNT(*) FROM agents WHERE name='sc-w-4';")" "4 0"
eq "agentset scale: a fixed member → 2" "$(rc $TB agentset scale sc lead +1)" "2"
eq "agentset scale: from an agent pane is refused" "$(HERDR_PANE_ID=wC:p1 rc $TB agentset scale sc w 1)" "1"
as_sql "INSERT INTO tasks (id, project, title, status, owner_agent, created_at, updated_at, herdr_session) VALUES ('T-sc-001','sc','busy','in_progress','sc-w-3','t','t','default');"
: >"$FAKE_LOG"
out=$($TB agentset scale sc w 1 2>&1; echo "rc=$?")
eq "agentset scale down: skips the busy one (open task) and the unflagged one, stops the idle one, then 1" \
  "$(grep -c '^sc-w-3: そのまま' <<<"$out") $(grep -c '^sc-w-2: sc-w-2 stopped' <<<"$out") $(grep -c '^sc-w-1: そのまま' <<<"$out") $(tail -1 <<<"$out") $(grep -c '^pane-close' "$FAKE_LOG")" "1 1 1 rc=1 1"
eq "agentset status shows the scaled agents" "$($TB agentset status sc | cut -f1,3 | paste -sd, -)" $'sc-lead\trunning,sc-w-1\trunning,sc-w-2\tstopped,sc-w-3\trunning'
: >"$FAKE_LOG"
eq "agentset scale +1: revives the stopped number first" "$($TB agentset scale sc w +1 | grep -c '^sc-w-2: .* revived') $(as_sql "SELECT killable FROM agents WHERE name='sc-w-2';")" "1 yes"
$TB agentset down sc --force >/dev/null 2>&1
eq "agentset down includes the scaled agents" "$(as_sql "SELECT COUNT(*) FROM agents WHERE name LIKE 'sc-%' AND state='running';")" "0"

# ---- job: run はタスクごとに新しい番号で 1 体(killable=yes・spawn_task)、reap は終わったタスクの agent を止める
as_reset
$TB agentset add jb <<<$'cwd = '"$TMP"$'\n[member lead]\n[member fix]\nmode = job\nparent = lead\nprompt = 直す\n' >/dev/null
$TB agentset up jb >/dev/null 2>&1
eq "agentset job: up starts no job agents" "$(as_sql "SELECT name FROM agents WHERE name LIKE 'jb-%' ORDER BY name;" | paste -sd, -)" "jb-lead"
as_sql "INSERT INTO tasks (id, project, title, status, created_at, updated_at, herdr_session) VALUES ('T-jb-001','jb','one','todo','t','t','default'),('T-jb-002','jb','two','done','t','t','default');"
: >"$FAKE_LOG"
eq "agentset run: starts <set>-<member>-1 split into the set's workspace" "$($TB agentset run jb fix --task T-jb-001 --brief 'see log' | cut -d' ' -f1) $(grep -c '^pane-split' "$FAKE_LOG")" "jb-fix-1: 1"
eq "agentset run: killable=yes, spawn_task, parent" "$(as_sql "SELECT killable||' '||spawn_task||' '||spawned_by FROM agents WHERE name='jb-fix-1';")" "yes T-jb-001 jb-lead"
eq "agentset run: the prompt names the task and the brief" "$(grep '^prompt jb-fix-1' "$FAKE_LOG" | grep -c 'tb claim T-jb-001.*see log')" "1"
eq "agentset run: the same task again → 1" "$(rc $TB agentset run jb fix --task T-jb-001)" "1"
eq "agentset run: a done task → 1" "$(rc $TB agentset run jb fix --task T-jb-002)" "1"
eq "agentset run: a missing task → 1" "$(rc $TB agentset run jb fix --task T-jb-404)" "1"
eq "agentset run: no --task → 2" "$(rc $TB agentset run jb fix)" "2"
eq "agentset run: a fixed member → 2" "$(rc $TB agentset run jb lead --task T-jb-001)" "2"
eq "agentset run: from an agent pane is refused" "$(HERDR_PANE_ID=wC:p1 rc $TB agentset run jb fix --task T-jb-001)" "1"
eq "agentset reap: nothing to stop while the task is open" "$($TB agentset reap jb) $(as_sql "SELECT state FROM agents WHERE name='jb-fix-1';")" "片付けるものはありません running"
as_sql "UPDATE tasks SET status='done' WHERE id='T-jb-001';"
eq "agentset reap: stops the agent whose task is done" "$($TB agentset reap jb | grep -c '^jb-fix-1: T-jb-001 は done') $(as_sql "SELECT state FROM agents WHERE name='jb-fix-1';")" "1 stopped"
$TB agentset down jb --force >/dev/null 2>&1

# ---- adopt: 動いている agent に親を足す(止めない)。on_parent_exit: reap が親の止まったメンバーを付け替える
as_reset
export FAKE_AGENTS="[$(as_ag conductor wC:p1),$(as_ag loner wL:p1)]"
$TB agents >/dev/null
$TB agentset add ad <<<$'cwd = '"$TMP"$'\nparent = conductor\non_parent_exit = set-parent\n[member lead]\n[member w]\nparent = lead\n[adopt]\nloner -> lead\n' >/dev/null
eq "agentset adopt: up adds the parent to the adopted agent" "$($TB agentset up ad | grep -c '^loner: 親に ad-lead を足しました') $(as_sql "SELECT parent FROM agent_parents WHERE child='loner';")" "1 ad-lead"
eq "agentset adopt: up again leaves it" "$($TB agentset up ad | grep -c '^loner: 親 ad-lead は付いています')" "1"
eq "agentset adopt: status lists the adopted agent" "$($TB agentset status ad | grep '^loner' | cut -f2,5)" $'~adopt\tad-lead'
$TB agent stop ad-lead --force >/dev/null
eq "agentset reap: on_parent_exit = set-parent adds the set's parent to the members whose parents all stopped" \
  "$($TB agentset reap ad | cut -d' ' -f1 | sort | paste -sd, -) $(as_sql "SELECT group_concat(child||'>'||parent, ',') FROM (SELECT * FROM agent_parents WHERE parent='conductor' AND child IN ('ad-w','loner') ORDER BY child);")" \
  "ad-w:,loner: ad-w>conductor,loner>conductor"
eq "agentset down: removes the adopted parent, keeps the adopted agent" "$($TB agentset down ad --force 2>&1 | grep -c '^loner: 親 ad-lead を外しました') $(as_sql "SELECT COUNT(*) FROM agent_parents WHERE child='loner' AND parent='ad-lead';") $(as_sql "SELECT state FROM agents WHERE name='loner';")" "1 0 running"

# ---- schedule: up が scheduler のジョブ agentset-<セット>-<メンバー> を書き、down で消す
as_reset
$TB agentset add sd <<<$'cwd = '"$TMP"$'\n[member lead]\nschedule = every 30m: 未処理を確かめる\n' >/dev/null
eq "agentset schedule: up writes the job" "$($TB agentset up sd | grep -c '^agentset-sd-lead: 定期の指示を書きました(30 分おき)') $($TB job show agentset-sd-lead 2>/dev/null | grep -E '^(TARGET|PROMPT|INTERVAL_MIN)=' | paste -sd' ' -)" "1 TARGET=sd-lead PROMPT='未処理を確かめる' INTERVAL_MIN=30"
eq "agentset schedule: up again keeps it" "$($TB agentset up sd | grep -c '^agentset-sd-lead: 定期の指示は同じです')" "1"
eq "agentset schedule: an unreadable value is refused at add" "$(rc $TB agentset add sd2 <<<$'[member a]\nschedule = every 5m: /compact\n')" "2"
eq "agentset schedule: down removes the job" "$($TB agentset down sd --force | grep -c '^agentset-sd-lead: 定期の指示を消しました') $(rc $TB job show agentset-sd-lead)" "1 1"

# ---- autoscale: 条件を 1 回評価して増やす・減らす(増やすのは max まで、減らすのは count まで)
as_reset
$TB agentset add au <<<$'cwd = '"$TMP"$'\n[member w]\ncount = 1\nmax = 2\nqueue = project:au\nscale_out = queue > 1\nscale_in = queue == 0 and idle >= 1\n' >/dev/null
$TB agentset up au >/dev/null 2>&1
as_sql "INSERT INTO tasks (id, project, title, status, owner_agent, created_at, updated_at, herdr_session) VALUES ('T-au-001','au','a','todo',NULL,'t','t','default'),('T-au-002','au','b','todo',NULL,'t','t','default'),('T-au-003','au','c','todo','x','t','t','default'),('T-zz-001','zz','d','todo',NULL,'t','t','default');"
eq "agentset autoscale --dry-run: shows the decision, changes nothing" \
  "$($TB agentset autoscale au --dry-run) $(as_sql "SELECT COUNT(*) FROM agents WHERE name LIKE 'au-%';")" \
  "au-w: queue=2 live=1 idle=1 busy=0 blocked=0: scale_out(queue > 1) → +1(--dry-run なので変えません) 1"
eq "agentset autoscale: scale_out adds one" "$($TB agentset autoscale au | grep -c '^au-w-2: ') $(as_sql "SELECT killable FROM agents WHERE name='au-w-2';")" "1 yes"
eq "agentset autoscale: at max stays" "$($TB agentset autoscale au | grep -c 'max(2)なのでそのまま')" "1"
as_sql "UPDATE tasks SET status='done' WHERE id IN ('T-au-001','T-au-002');"
eq "agentset autoscale: scale_in stops the highest number" "$($TB agentset autoscale au | grep -c '^au-w-2: au-w-2 stopped') $(as_sql "SELECT state FROM agents WHERE name='au-w-1';")" "1 running"
eq "agentset autoscale: at count stays" "$($TB agentset autoscale au | grep -c 'count(1)なのでそのまま')" "1"
eq "agentset autoscale: a set without conditions → 2" "$(rc $TB agentset autoscale sc)" "2"
eq "agentset autoscale: from an agent pane is refused" "$(HERDR_PANE_ID=wC:p1 rc $TB agentset autoscale au)" "1"
eq "agentset autoscale: a broken condition is refused at add" "$(rc $TB agentset add au2 <<<$'[member w]\nmax = 2\nscale_out = cpu > 1\n')" "2"
$TB agentset down au --force >/dev/null 2>&1

# ---- autoscale の for(続けて当たったら)と scale_cooldown。状態は定義の置き場所の .autoscale/<セット>.json
as_reset
$TB agentset add af <<<$'cwd = '"$TMP"$'\n[member w]\ncount = 1\nmax = 3\nqueue = project:af\nscale_out = queue > 0 for 10m\nscale_cooldown = 30m\n' >/dev/null
$TB agentset up af >/dev/null 2>&1
as_sql "INSERT INTO tasks (id, project, title, status, created_at, updated_at, herdr_session) VALUES ('T-af-001','af','a','todo','t','t','default');"
eq "agentset autoscale for: holds but not long enough" "$($TB agentset autoscale af | grep -c 'for 10 分まであと 10 分') $(jq -r '.members.w.since|length' "$TB_AGENTSETS/.autoscale/af.json")" "1 1"
_before=$(cat "$TB_AGENTSETS/.autoscale/af.json")
$TB agentset autoscale af --dry-run >/dev/null
eq "agentset autoscale --dry-run does not write the state" "$(cat "$TB_AGENTSETS/.autoscale/af.json")" "$_before"
_ago=$(sqlite3 :memory: "SELECT strftime('%Y-%m-%dT%H:%M:%SZ','now','-11 minutes');")
jq --arg t "$_ago" '.members.w.since |= map_values($t)' "$TB_AGENTSETS/.autoscale/af.json" >"$TMP/af.json" && mv "$TMP/af.json" "$TB_AGENTSETS/.autoscale/af.json"
eq "agentset autoscale for: after 10 minutes adds one" "$($TB agentset autoscale af | grep -c '^af-w-2: ') $(jq -r '(.members.w.last != null) and (.members.w.since|length == 0)' "$TB_AGENTSETS/.autoscale/af.json")" "1 true"
jq --arg t "$_ago" '.members.w.since = {"scale_out:queue > 0 for 10m": $t}' "$TB_AGENTSETS/.autoscale/af.json" >"$TMP/af.json" && mv "$TMP/af.json" "$TB_AGENTSETS/.autoscale/af.json"
eq "agentset autoscale cooldown: waits after a change" "$($TB agentset autoscale af | grep -c 'scale_cooldown(30 分)まであと 30 分') $(as_sql "SELECT COUNT(*) FROM agents WHERE name='af-w-3';")" "1 0"
$TB agentset down af --force >/dev/null 2>&1
$TB agentset rm af >/dev/null
eq "agentset rm removes the autoscale state" "$([ -e "$TB_AGENTSETS/.autoscale/af.json" ] && echo kept || echo gone)" "gone"

as_reset
unset HERDR_PANE_ID TB_DB TB_AGENTSETS TB_SPAWN_RETRY_SEC TB_AGENT_CWD FAKE_DYNAMIC FAKE_AGENTS
eval "$_saved_env"
