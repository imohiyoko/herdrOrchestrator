# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 1 体に複数ロール(分野 × 立場): role-set のカンマ区切り / 役割の合成 / @a+b の照合
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_appr=${TB_APPROVALS-}; _saved_roles=${TB_ROLES-}
export TB_DB=$TMP/rmulti.db TB_APPROVALS=$TMP/rmulti-appr.conf TB_ROLES=$TMP/rmulti-roles
mkdir -p "$TB_ROLES"
for r in hr manager task worker; do echo "ROLE-$r" >"$TB_ROLES/$r.md"; done
echo "COMMON" >"$TB_ROLES/_common.md"
printf 'hrm* = hr,manager\nhrt* = hr, task\nworker* = worker\n' >"$TB_ROLES/map.conf"
printf 'role:change = allow\n* = ask\n' >"$TB_APPROVALS"
rm_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(rm_ hrm1),$(rm_ hrt1),$(rm_ worker1)]"
$TB agents >/dev/null 2>&1
eq "roles: map.conf gives several roles" "$($TB roles | cut -f1,2 | tr '\t\n' ' ')" "hrm1 hr.md,manager.md hrt1 hr.md,task.md worker1 worker.md "
ctx=$(HERDR_PANE_ID=whrm1:p1 $TB role-context --source startup --session-id s-hrm1 | jq -r .hookSpecificOutput.additionalContext)
eq "role-context: _common then each role file" "$(grep -cE '^(COMMON|ROLE-hr|ROLE-manager)$' <<<"$ctx") $(grep -c 'ROLE-task' <<<"$ctx")" "3 0"
printf 'send:task:@hr+task = allow\nagent:stop:@hr = allow\n* = ask\n' >"$TB_APPROVALS"
eq "approvals: @hr+task needs both roles" "$($TB approval send:task:hrt1 | head -1) $($TB approval send:task:hrm1 | head -1)" "allow ask"
eq "approvals: @hr matches any agent that has hr among its roles" "$($TB approval agent:stop:hrm1 | head -1) $($TB approval agent:stop:worker1 | head -1)" "allow ask"
printf 'role:change = allow\n* = ask\n' >"$TB_APPROVALS"
eq "role-set: a comma list" "$($TB role-set worker1 worker,manager | cut -d' ' -f1-5)" "worker1 role worker -> worker,manager"
eq "role-set: every role must exist" "$(rc $TB role-set worker1 worker,nosuch)" "2"
eq "role-set: an hr agent cannot grant hr inside a list" "$(HERDR_PANE_ID=whrm1:p1 rc $TB role-set worker1 task,hr)" "1"
eq "role-set: an agent with hr among several roles may change others" "$(HERDR_PANE_ID=whrm1:p1 $TB role-set hrt1 task | cut -d' ' -f1-5)" "hrt1 role hr,task -> task"
eq "agent-new: --role takes a comma list of existing roles" "$(rc $TB agent-new nw1 --cwd "$TMP" --role worker,nosuch) $($TB agent-new nw1 --cwd "$TMP" --role worker,manager | cut -d' ' -f1-2) $(sqlite3 "$TB_DB" "SELECT role FROM agents WHERE name='nw1';")" "2 nw1 started worker,manager"
eq "agents --group role: grouped by the role combination" "$($TB agents --group role | grep '^\[' | tr '\n' ' ')" "[hr,manager] [task] [worker,manager] "
export TB_APPROVALS=$_saved_appr
if [[ -n $_saved_roles ]]; then export TB_ROLES=$_saved_roles; else unset TB_ROLES; fi
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
