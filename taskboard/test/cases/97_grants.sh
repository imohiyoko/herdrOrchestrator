# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- policy: 主体ごとの個別の許可と、spawn の関係による相対指定(~child / = allow ~descendant / ~parent / ~sibling)
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_appr=${TB_POLICY_A-}; _saved_roles=${TB_ROLES-}
export TB_DB=$TMP/grants.db TB_POLICY_A=$TMP/grants-appr.conf TB_ROLES=$TMP/grants-roles TB_POLICY_G=$TMP/grants.conf
mkdir -p "$TB_ROLES"; for r in manager worker; do echo "ROLE-$r" >"$TB_ROLES/$r.md"; done
printf 'mgr* = manager\nw* = worker\n' >"$TB_ROLES/map.conf"
printf '* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
gr_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(gr_ mgr1),$(gr_ w1),$(gr_ w2),$(gr_ w3),$(gr_ other1)]"
$TB agents >/dev/null 2>&1
sqlite3 "$TB_DB" "UPDATE agents SET spawned_by='mgr1' WHERE name IN ('w1','w3'); UPDATE agents SET spawned_by='w1' WHERE name='w2';"
printf '@manager agent:read ~descendant = allow\n@manager send:task ~child = allow\nw1 send:task ~parent = allow\nw1 agent:read ~sibling = allow\nother1 agent:stop @worker = allow\nbad line\n' >"$TB_POLICY_G"
ga() { $TB approval "$1" --by "$2" | tr '\n' ' '; }
eq "grants: ~descendant reaches a grandchild" "$(ga agent:read:w2 mgr1)" "allow rule: policy: @manager agent:read ~descendant = allow "
eq "grants: ~descendant does not reach an unrelated agent" "$(ga agent:read:other1 mgr1)" "ask rule: 既定 ask "
eq "grants: ~child is only the direct child" "$($TB approval send:task:w1 --by mgr1 | head -1) $($TB approval send:task:w2 --by mgr1 | head -1)" "allow ask"
eq "grants: ~parent / ~sibling" "$($TB approval send:task:mgr1 --by w1 | head -1) $($TB approval agent:read:w3 --by w1 | head -1) $($TB approval agent:read:w2 --by w1 | head -1)" "allow allow ask"
eq "grants: the subject must match (a worker is not a manager)" "$($TB approval agent:read:w2 --by w3 | head -1)" "ask"
eq "grants: a named subject with an @role target" "$($TB approval agent:stop:w2 --by other1 | head -1) $($TB approval agent:stop:mgr1 --by other1 | head -1)" "allow ask"
eq "grants: not used without a subject (the human's view of approvals.conf)" "$($TB approval agent:read:w2 | head -1)" "ask"
eq "grants: a role that cannot be read grants nothing" "$(sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role TO role_x;"; $TB approval agent:read:w2 --by mgr1 2>/dev/null | head -1; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role_x TO role;")" "ask"
eq "grants: tb peek uses them (manager reads a grandchild, not an unrelated agent)" "$(HERDR_PANE_ID=wmgr1:p1 rc $TB peek w2) $(HERDR_PANE_ID=wmgr1:p1 rc $TB peek other1)" "0 20"
eq "grants: the peek is logged with the grant as its rule" "$(sqlite3 "$TB_DB" "SELECT status||' '||rule FROM read_log WHERE target='w2' ORDER BY id DESC LIMIT 1;")" "ok policy: @manager agent:read ~descendant = allow"
export TB_POLICY_A=$_saved_appr; unset TB_POLICY_G
if [[ -n $_saved_roles ]]; then export TB_ROLES=$_saved_roles; else unset TB_ROLES; fi
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
