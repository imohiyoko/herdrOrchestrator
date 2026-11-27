# shellcheck shell=bash
# --- tb agent parent add / rm(親を複数持つ。agent_parents)と、親が全員止まったときだけ orphan(tb agent-shed)
# DB はこのケースのものだけを使う
_saved_env=$(declare -p HERDR_PANE_ID TB_DB TB_POLICY_A FAKE_AGENTS 2>/dev/null)
unset HERDR_PANE_ID
export TB_DB=$TMP/parents.db TB_POLICY_A=$TMP/parents-policy.conf
: >"$TB_POLICY_A"
pa_ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"},"cwd":"%s"}' "$2" "$1" "$1" "$TMP"; }
pa_sql() { sqlite3 "$TB_DB" "$@"; }
export FAKE_AGENTS="[$(pa_ag pa-lead wA:p1),$(pa_ag pa-sub wA:p2),$(pa_ag pa-kid wA:p3)]"
: >"$FAKE_LOG"
$TB agents >/dev/null
pa_sql "INSERT INTO agents (name, herdr_session, state, killable, created_by, created_at, updated_at) VALUES
  ('pa-lead','default','running','unknown','tb','t','t'),('pa-sub','default','running','unknown','tb','t','t'),('pa-kid','default','running','unknown','tb','t','t')
  ON CONFLICT DO NOTHING;"

eq "agent parent add: the first parent" "$($TB agent parent add pa-kid pa-lead)" "pa-kid の親: pa-lead"
eq "agent parent add: a second parent" "$($TB agent parent add pa-kid pa-sub)" "pa-kid の親: pa-lead pa-sub"
eq "agent parent add: again is a no-op" "$($TB agent parent add pa-kid pa-sub)" "pa-kid の親: pa-lead pa-sub(変わりません)"
eq "agent parent add: a cycle → 1" "$(rc $TB agent parent add pa-lead pa-kid) $(pa_sql "SELECT COUNT(*) FROM agent_parents WHERE child='pa-lead';")" "1 0"
eq "agent parent add: self → 2" "$(rc $TB agent parent add pa-kid pa-kid)" "2"
eq "agent parent add: an unknown parent → 1" "$(rc $TB agent parent add pa-kid pa-ghost)" "1"
eq "agent parent add: an unknown child → 1" "$(rc $TB agent parent add pa-ghost pa-lead)" "1"
eq "agent parent add: from an agent pane without policy → 4 (denied)" "$(HERDR_PANE_ID=wA:p1 rc $TB agent parent add pa-sub pa-lead)" "4"
printf '* agent:parent pa-sub = allow\n' >"$TB_POLICY_A"
eq "agent parent add: an agent allowed by policy" "$(HERDR_PANE_ID=wA:p1 $TB agent parent add pa-sub pa-lead) $(pa_sql "SELECT added_by FROM agent_parents WHERE child='pa-sub';")" "pa-sub の親: pa-lead pa-lead"
: >"$TB_POLICY_A"

# ---- shed: 親が 1 体でも動いていれば orphan にしない。全員止まったら orphan
pa_sql "UPDATE agents SET state='stopped' WHERE name='pa-lead';"
export FAKE_AGENTS="[$(pa_ag pa-sub wA:p2),$(pa_ag pa-kid wA:p3)]"
eq "agent-shed: one running parent left → not an orphan" "$($TB agent-shed | grep -c 'orphan.*pa-kid')" "0"
export FAKE_AGENTS="[$(pa_ag pa-kid wA:p3)]"
pa_sql "UPDATE agents SET state='stopped' WHERE name='pa-sub';"
eq "agent-shed: all parents stopped → orphan" "$($TB agent-shed | grep -c '^orphan *pa-kid')" "1"
eq "agent parent rm" "$($TB agent parent rm pa-kid pa-lead)" "pa-kid の親: pa-sub"
eq "agent parent rm: not a parent is a no-op" "$($TB agent parent rm pa-kid pa-lead)" "pa-kid の親: pa-sub(変わりません)"
$TB agent parent rm pa-kid pa-sub >/dev/null
eq "agent-shed: no parents left → a root, not an orphan" "$($TB agent-shed | grep -c 'pa-kid')" "0"
eq "agent parent rm: usage → 2" "$(rc $TB agent parent rm pa-kid)" "2"

unset HERDR_PANE_ID TB_DB TB_POLICY_A FAKE_AGENTS
eval "$_saved_env"
