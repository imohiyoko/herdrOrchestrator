# shellcheck shell=bash
# tb herdr(workspace / pane を見る・閉じる)。agent が動いている所は誰が実行しても閉じない(agent は tb agent stop)。agent からの close は既定 allow
# DB は herdr.db、設定は $TMP/hd.* だけを使う
_hd_saved=$(declare -p TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS FAKE_WORKSPACES FAKE_WS_CLOSE_RC FAKE_CLOSE_RC FAKE_PANES FAKE_FG FAKE_FG_PANE FAKE_BG_PANE FAKE_PINFO_BAD_PANE FAKE_PANE_LIST_FAIL 2>/dev/null || true)
unset HERDR_PANE_ID FAKE_WS_CLOSE_RC FAKE_CLOSE_RC FAKE_FG FAKE_FG_PANE FAKE_BG_PANE FAKE_PINFO_BAD_PANE FAKE_PANE_LIST_FAIL
export TB_DB=$TMP/herdr.db TB_POLICY=$TMP/hd.policy
: >"$TB_POLICY"
hd_ag() { printf '{"pane_id":"%s","name":"%s","workspace_id":"%s","agent_session":{"value":"s-%s"}}' "$2" "$1" "$3" "$2"; }
HD_A1=$(hd_ag dev1 wA:p1 wA)
HD_ANON='{"pane_id":"wB:p1","workspace_id":"wB","agent_session":{"value":"s-x"}}'
export FAKE_AGENTS="[$HD_A1,$HD_ANON]"
export FAKE_PANES='[{"pane_id":"wA:p1","workspace_id":"wA"},{"pane_id":"wB:p1","workspace_id":"wB"},{"pane_id":"wC:p1","workspace_id":"wC"},{"pane_id":"wC:p2","workspace_id":"wC"}]'
export FAKE_WORKSPACES='[{"workspace_id":"wA","label":"dev1"},{"workspace_id":"wB","label":"scratch"},{"workspace_id":"wC","label":"empty"}]'
$TB list >/dev/null 2>&1
: >"$FAKE_LOG"
hd_closed() { grep -c "^$1 $2\$" "$FAKE_LOG"; }

# ---- 使い方
eq "herdr: no subcommand → help, rc 2" "$($TB herdr 2>/dev/null | head -1; rc $TB herdr)" "$(printf 'tb herdr workspace list [--json]   herdr の workspace と、その中の agent\n2')"
eq "herdr: unknown verb → 2" "$(rc $TB herdr workspace kill wC 2>/dev/null) $(rc $TB herdr pane 2>/dev/null) $(rc $TB herdr nope 2>/dev/null)" "2 2 2"
eq "herdr close: missing id / extra args → 2" "$(rc $TB herdr pane close) $(rc $TB herdr workspace close wC --yes)" "2 2"

# ---- 一覧
eq "herdr workspace list: id, label and the agents in it" "$($TB herdr workspace list)" "$(printf 'wA\tdev1\tagents: dev1\nwB\tscratch\tagents: (名前なし。pane wB:p1)\nwC\tempty\tagents: -')"
eq "herdr workspace list --json" "$($TB herdr workspace list --json | jq -c '[.workspace_id, (.agents|length)]' | tr -d '\n')" '["wA",1]["wB",1]["wC",0]'

# ---- 人: agent の居ない所は閉じる
eq "herdr workspace close: empty workspace is closed" "$($TB herdr workspace close wC) $(hd_closed workspace-close wC)" "workspace wC closed 1"
eq "herdr workspace close: unknown workspace → 1, herdr not called" "$(rc $TB herdr workspace close wZ 2>/dev/null) $(hd_closed workspace-close wZ)" "1 0"
eq "herdr pane close: a pane without an agent is closed" "$($TB herdr pane close wC:p1) $(hd_closed pane-close wC:p1)" "pane wC:p1 closed 1"

# ---- agent を守る(名前の有無に関わらず、agent が動いている所は閉じない。--force は無い)
eq "herdr close: --force is not an option" "$(rc $TB herdr workspace close wA --force) $(rc $TB herdr pane close wB:p1 --force)" "2 2"
eq "herdr workspace close: a workspace with a named agent is refused" "$(rc $TB herdr workspace close wA 2>/dev/null) $(hd_closed workspace-close wA)" "1 0"
eq "herdr workspace close: the refusal points to tb agent stop" "$($TB herdr workspace close wA 2>&1 | grep -c 'tb agent stop')" "1"
eq "herdr pane close: a pane with a named agent is refused" "$(rc $TB herdr pane close wA:p1 2>/dev/null) $(hd_closed pane-close wA:p1)" "1 0"
eq "herdr close: an unnamed agent also blocks (pane and workspace)" "$(rc $TB herdr pane close wB:p1) $(rc $TB herdr workspace close wB) $(hd_closed pane-close wB:p1) $(hd_closed workspace-close wB)" "1 1 0 0"
eq "herdr pane close: the ledger is synced first (a stopped agent's old pane can be closed)" "$(FAKE_AGENTS="[$HD_ANON]" $TB herdr pane close wA:p1) $(sqlite3 "$TB_DB" "SELECT state FROM agents WHERE name='dev1';")" "pane wA:p1 closed stopped"
$TB list >/dev/null 2>&1
eq "herdr pane close: a running agent in the ledger blocks even if herdr does not list that pane" \
  "$(sqlite3 "$TB_DB" "UPDATE agents SET state='running', pane_id='wC:p7' WHERE name='dev1';"; FAKE_AGENTS="[$HD_A1]" rc $TB herdr pane close wC:p7) $(hd_closed pane-close wC:p7)" "0 1"
$TB list >/dev/null 2>&1
eq "herdr workspace close: an agent without workspace_id makes it refuse (cannot tell)" "$(FAKE_AGENTS='[{"pane_id":"wQ:p1","name":"q1","agent_session":{"value":"s"}}]' rc $TB herdr workspace close wC)" "1"
eq "herdr pane close: an agent without pane_id makes it refuse (cannot tell)" "$(FAKE_AGENTS='[{"name":"q1","workspace_id":"wQ","agent_session":{"value":"s"}}]' rc $TB herdr pane close wC:p8) $(hd_closed pane-close wC:p8)" "1 0"
eq "herdr close: agent list failure → 11, nothing closed" "$(FAKE_LIST_FAIL=1 rc $TB herdr workspace close wC) $(FAKE_LIST_FAIL=1 rc $TB herdr pane close wC:p9) $(hd_closed pane-close wC:p9)" "11 11 0"
eq "herdr close: an agent list without .result.agents is not taken as 'no agents'" "$(FAKE_LIST_BAD=1 rc $TB herdr pane close wC:p9) $(FAKE_LIST_BAD=1 rc $TB herdr workspace close wC) $(hd_closed pane-close wC:p9)" "11 11 0"
eq "herdr close: herdr close failure → 11" "$(FAKE_WS_CLOSE_RC=1 rc $TB herdr workspace close wC) $(FAKE_CLOSE_RC=1 rc $TB herdr pane close wC:p2)" "11 11"

# ---- agent 以外のプロセスも中断しない(前面のプロセス・シェルの下の子)
eq "herdr pane close: a foreground process (not an agent) blocks" "$(FAKE_FG_PANE=wC:p5 FAKE_FG='[{"pid":4242,"name":"vim"}]' rc $TB herdr pane close wC:p5) $(hd_closed pane-close wC:p5)" "1 0"
eq "herdr pane close: the refusal names the process" "$(FAKE_FG_PANE=wC:p5 FAKE_FG='[{"pid":4242,"name":"vim"}]' $TB herdr pane close wC:p5 2>&1 | grep -c '4242 vim')" "1"
eq "herdr pane close: the shell itself in the foreground does not block" "$(FAKE_FG_PANE=wC:p5 FAKE_FG='[{"pid":999999,"name":"zsh"}]' $TB herdr pane close wC:p5)" "pane wC:p5 closed"
eq "herdr pane close: a child under the pane's shell (background job) blocks" "$(FAKE_BG_PANE=wC:p6 rc $TB herdr pane close wC:p6) $(hd_closed pane-close wC:p6)" "1 0"
eq "herdr pane close: unreadable process info → refuses" "$(FAKE_PINFO_BAD_PANE=wC:p6 rc $TB herdr pane close wC:p6) $(FAKE_PINFO_FAIL=1 rc $TB herdr pane close wC:p6) $(hd_closed pane-close wC:p6)" "1 11 0"
eq "herdr workspace close: a busy pane anywhere in it blocks" "$(: >"$FAKE_LOG"; FAKE_FG_PANE=wC:p2 FAKE_FG='[4243]' rc $TB herdr workspace close wC) $(FAKE_BG_PANE=wC:p1 rc $TB herdr workspace close wC) $(hd_closed workspace-close wC)" "1 1 0"
eq "herdr workspace close: every pane is checked" "$(: >"$FAKE_LOG"; $TB herdr workspace close wC >/dev/null; grep -c '^pane-list --workspace wC$' "$FAKE_LOG")" "1"
eq "herdr workspace close: pane list failure → 11, nothing closed" "$(: >"$FAKE_LOG"; FAKE_PANE_LIST_FAIL=1 rc $TB herdr workspace close wC) $(hd_closed workspace-close wC)" "11 0"

# ---- agent から: 既定は allow(agent が居ないことの確かめは同じ)。自分の pane は閉じない。policy で締められる
: >"$FAKE_LOG"
eq "herdr agent: workspace list is allowed" "$(HERDR_PANE_ID=wA:p1 rc $TB herdr workspace list)" "0"
eq "herdr agent: closes a pane without an agent (default allow)" "$(HERDR_PANE_ID=wA:p1 $TB herdr pane close wC:p3) $(hd_closed pane-close wC:p3)" "pane wC:p3 closed 1"
eq "herdr agent: still refuses where an agent runs" "$(HERDR_PANE_ID=wA:p1 rc $TB herdr pane close wB:p1) $(hd_closed pane-close wB:p1)" "1 0"
eq "herdr agent: its own pane / workspace is not closed" "$(HERDR_PANE_ID=wA:p1 rc $TB herdr pane close wA:p1) $(HERDR_PANE_ID=wA:p1 rc $TB herdr workspace close wA) $(hd_closed pane-close wA:p1) $(hd_closed workspace-close wA)" "1 1 0 0"
eq "herdr agent: closes an empty workspace" "$(HERDR_PANE_ID=wA:p1 $TB herdr workspace close wC)" "workspace wC closed"
printf 'dev1 cmd:herdr:pane-close * = ask\n' >"$TB_POLICY"
eq "herdr agent: policy ask → queued as a Q-, nothing closed" "$(HERDR_PANE_ID=wA:p1 $TB herdr pane close wC:p4 >/dev/null 2>"$TMP/hd.err"; echo $?) $(grep -c '承認待ち Q-[0-9]* に積みました' "$TMP/hd.err") $(hd_closed pane-close wC:p4)" "20 1 0"
hq=$(grep -o 'Q-[0-9]*' "$TMP/hd.err" | head -1)
eq "herdr agent: approved → the requester runs it once" "$($TB approval approve "$hq" >/dev/null 2>&1; HERDR_PANE_ID=wA:p1 $TB approval run "$hq" 2>/dev/null | tail -1) $(hd_closed pane-close wC:p4)" "pane wC:p4 closed 1"

unset TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS FAKE_WORKSPACES FAKE_WS_CLOSE_RC FAKE_CLOSE_RC FAKE_PANES FAKE_FG FAKE_FG_PANE FAKE_BG_PANE FAKE_PINFO_BAD_PANE FAKE_PANE_LIST_FAIL
eval "$_hd_saved"
