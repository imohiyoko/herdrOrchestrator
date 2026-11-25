# shellcheck shell=bash
# tb herdr(workspace / pane を見る・閉じる)。agent の居る所は閉じない(agent は tb agent stop)。agent からの close は既定 ask(Q-<n>)
# DB は herdr.db、設定は $TMP/hd.* だけを使う
_hd_saved=$(declare -p TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS FAKE_WORKSPACES FAKE_WS_CLOSE_RC FAKE_CLOSE_RC 2>/dev/null || true)
unset HERDR_PANE_ID FAKE_WS_CLOSE_RC FAKE_CLOSE_RC
export TB_DB=$TMP/herdr.db TB_POLICY=$TMP/hd.policy
: >"$TB_POLICY"
hd_ag() { printf '{"pane_id":"%s","name":"%s","workspace_id":"%s","agent_session":{"value":"s-%s"}}' "$2" "$1" "$3" "$2"; }
HD_A1=$(hd_ag dev1 wA:p1 wA)
HD_ANON='{"pane_id":"wB:p1","workspace_id":"wB","agent_session":{"value":"s-x"}}'
export FAKE_AGENTS="[$HD_A1,$HD_ANON]"
export FAKE_WORKSPACES='[{"workspace_id":"wA","label":"dev1"},{"workspace_id":"wB","label":"scratch"},{"workspace_id":"wC","label":"empty"}]'
$TB list >/dev/null 2>&1
: >"$FAKE_LOG"
hd_closed() { grep -c "^$1 $2\$" "$FAKE_LOG"; }

# ---- 使い方
eq "herdr: no subcommand → help, rc 2" "$($TB herdr 2>/dev/null | head -1; rc $TB herdr)" "$(printf 'tb herdr workspace list [--json]   herdr の workspace と、その中の agent\n2')"
eq "herdr: unknown verb → 2" "$(rc $TB herdr workspace kill wC 2>/dev/null) $(rc $TB herdr pane 2>/dev/null) $(rc $TB herdr nope 2>/dev/null)" "2 2 2"
eq "herdr close: missing id / unknown flag → 2" "$(rc $TB herdr pane close 2>/dev/null) $(rc $TB herdr workspace close wC --yes 2>/dev/null)" "2 2"

# ---- 一覧
eq "herdr workspace list: id, label and the agents in it" "$($TB herdr workspace list)" "$(printf 'wA\tdev1\tagents: dev1\nwB\tscratch\tagents: (名前なし。pane wB:p1)\nwC\tempty\tagents: -')"
eq "herdr workspace list --json" "$($TB herdr workspace list --json | jq -c '[.workspace_id, (.agents|length)]' | tr -d '\n')" '["wA",1]["wB",1]["wC",0]'

# ---- 人: agent の居ない所は閉じる
eq "herdr workspace close: empty workspace is closed" "$($TB herdr workspace close wC) $(hd_closed workspace-close wC)" "workspace wC closed 1"
eq "herdr workspace close: unknown workspace → 1, herdr not called" "$(rc $TB herdr workspace close wZ 2>/dev/null) $(hd_closed workspace-close wZ)" "1 0"
eq "herdr pane close: a pane without an agent is closed" "$($TB herdr pane close wC:p1) $(hd_closed pane-close wC:p1)" "pane wC:p1 closed 1"

# ---- agent を守る
eq "herdr workspace close: a workspace with a named agent is refused even with --force" "$(rc $TB herdr workspace close wA 2>/dev/null) $(rc $TB herdr workspace close wA --force 2>/dev/null) $(hd_closed workspace-close wA)" "1 1 0"
eq "herdr workspace close: the refusal points to tb agent stop" "$($TB herdr workspace close wA 2>&1 | grep -c 'tb agent stop')" "1"
eq "herdr pane close: a pane with a named agent is refused even with --force" "$(rc $TB herdr pane close wA:p1 2>/dev/null) $(rc $TB herdr pane close wA:p1 --force 2>/dev/null) $(hd_closed pane-close wA:p1)" "1 1 0"
eq "herdr pane close: an unnamed agent needs --force" "$(rc $TB herdr pane close wB:p1 2>/dev/null) $(hd_closed pane-close wB:p1) $($TB herdr pane close wB:p1 --force) $(hd_closed pane-close wB:p1)" "1 0 pane wB:p1 closed 1"
eq "herdr workspace close: an unnamed agent needs --force" "$(rc $TB herdr workspace close wB 2>/dev/null) $($TB herdr workspace close wB --force) $(hd_closed workspace-close wB)" "1 workspace wB closed 1"
# 台帳で動作中の agent の pane は、herdr の一覧から外れていても守る(ここでは一覧の同期で stopped になるので、同期の後に running に戻らない限り閉じられる)
eq "herdr pane close: the ledger is synced first (a stopped agent's old pane can be closed)" "$(FAKE_AGENTS="[$HD_ANON]" $TB herdr pane close wA:p1) $(sqlite3 "$TB_DB" "SELECT state FROM agents WHERE name='dev1';")" "pane wA:p1 closed stopped"
$TB list >/dev/null 2>&1
eq "herdr workspace close: an agent without workspace_id makes it refuse (cannot tell)" "$(FAKE_AGENTS='[{"pane_id":"wQ:p1","name":"q1","agent_session":{"value":"s"}}]' rc $TB herdr workspace close wC 2>/dev/null)" "1"
eq "herdr close: agent list failure → 11, nothing closed" "$(FAKE_LIST_FAIL=1 rc $TB herdr workspace close wC 2>/dev/null) $(FAKE_LIST_FAIL=1 rc $TB herdr pane close wC:p9 2>/dev/null) $(hd_closed pane-close wC:p9)" "11 11 0"
eq "herdr close: herdr close failure → 11" "$(FAKE_WS_CLOSE_RC=1 rc $TB herdr workspace close wC 2>/dev/null) $(FAKE_CLOSE_RC=1 rc $TB herdr pane close wC:p2 2>/dev/null)" "11 11"

# ---- agent から: 既定は ask(承認待ち)。--force は人だけ。自分の pane は閉じない
eq "herdr agent: workspace list is allowed" "$(HERDR_PANE_ID=wA:p1 rc $TB herdr workspace list)" "0"
: >"$FAKE_LOG"
eq "herdr agent: pane close asks (20) and queues a Q-, nothing closed" "$(HERDR_PANE_ID=wA:p1 $TB herdr pane close wC:p3 >/dev/null 2>"$TMP/hd.err"; echo $?) $(grep -c '承認待ち Q-[0-9]* に積みました' "$TMP/hd.err") $(hd_closed pane-close wC:p3)" "20 1 0"
hq=$(grep -o 'Q-[0-9]*' "$TMP/hd.err" | head -1)
eq "herdr agent: approved → the requester runs it once" "$($TB approval approve "$hq" >/dev/null 2>&1; HERDR_PANE_ID=wA:p1 $TB approval run "$hq" 2>/dev/null | tail -1) $(hd_closed pane-close wC:p3)" "pane wC:p3 closed 1"
printf 'dev1 cmd:herdr:pane-close * = allow\ndev1 cmd:herdr:workspace-close * = allow\n' >"$TB_POLICY"
eq "herdr agent: --force is for people only" "$(HERDR_PANE_ID=wA:p1 rc $TB herdr pane close wB:p1 --force 2>/dev/null) $(hd_closed pane-close wB:p1)" "1 0"
eq "herdr agent: its own pane / workspace is not closed" "$(HERDR_PANE_ID=wA:p1 rc $TB herdr pane close wA:p1 2>/dev/null) $(HERDR_PANE_ID=wA:p1 rc $TB herdr workspace close wA 2>/dev/null) $(hd_closed pane-close wA:p1) $(hd_closed workspace-close wA)" "1 1 0 0"
eq "herdr agent: allowed by policy → closes an empty workspace" "$(HERDR_PANE_ID=wA:p1 $TB herdr workspace close wC) " "workspace wC closed "

unset TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS FAKE_WORKSPACES FAKE_WS_CLOSE_RC FAKE_CLOSE_RC
eval "$_hd_saved"
