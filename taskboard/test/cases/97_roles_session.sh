# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- ロールは herdr セッション固有: roles/sessions/<セッション>/ と、台帳の役割は同じセッションの agent だけ
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_appr=${TB_POLICY_A-}; _saved_roles=${TB_ROLES-}; _saved_hs=${HERDR_SESSION-}; unset HERDR_SESSION
export TB_DB=$TMP/rsess.db TB_POLICY_A=$TMP/rsess-appr.conf TB_ROLES=$TMP/rsess-roles
mkdir -p "$TB_ROLES/sessions/s2"
echo "ROLE-worker" >"$TB_ROLES/worker.md"; echo "COMMON" >"$TB_ROLES/_common.md"
printf 'worker* = worker\n' >"$TB_ROLES/map.conf"
echo "S2-auditor" >"$TB_ROLES/sessions/s2/auditor.md"; echo "S2-COMMON" >"$TB_ROLES/sessions/s2/_common.md"
printf 'worker* = auditor\n' >"$TB_ROLES/sessions/s2/map.conf"
rs_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(rs_ worker1)]"
$TB agents >/dev/null 2>&1
printf '* agent:stop @worker = allow\n* agent:stop @auditor = allow\n* send:info * = ask-any\n* send:answer * = ask-any\n' >"$TB_POLICY_A"
eq "session roles: the shared roles/ in a session without its own directory" "$($TB approval agent:stop:worker1 | head -1) $($TB approval agent:stop:worker7 | sed -n 2p)" "allow rule: policy: * agent:stop @worker = allow"
eq "session roles: roles/sessions/s2/ replaces roles/ in s2 (map.conf there)" "$(HERDR_SESSION=s2 $TB approval agent:stop:worker7 | sed -n 2p)" "rule: policy: * agent:stop @auditor = allow"
# 台帳はセッションごと: default の worker1 は s2 の台帳に無いので、s2 では未登録の名前と同じく s2 の map.conf で決まる(default の台帳の役割は使わない)
eq "session roles: an agent of another session is unknown here (the session's map.conf decides)" "$(HERDR_SESSION=s2 $TB approval agent:stop:worker1 | sed -n 2p)" "rule: policy: * agent:stop @auditor = allow"
ctx=$(HERDR_SESSION=s2 HERDR_PANE_ID=wworker7:p1 FAKE_AGENTS="[$(rs_ worker7)]" $TB role-context --source startup --session-id s-w7 | jq -r .hookSpecificOutput.additionalContext)
eq "session roles: role-context hands the session's own files" "$(grep -cE '^(S2-COMMON|S2-auditor)$' <<<"$ctx") $(grep -cE 'ROLE-worker|^COMMON$' <<<"$ctx")" "2 0"
printf '* role:change * = allow\n* send:info * = ask-any\n* send:answer * = ask-any\n' >"$TB_POLICY_A"
eq "role-set: refuses an agent of another session" "$(HERDR_SESSION=s2 FAKE_AGENTS="[]" $TB role-set worker1 auditor 2>&1 | grep -c "セッション 'default'") $(HERDR_SESSION=s2 FAKE_AGENTS="[]" rc $TB role-set worker1 auditor)" "1 1"
eq "role-set: records the session" "$($TB role-set worker1 worker >/dev/null; sqlite3 "$TB_DB" "SELECT herdr_session FROM role_changes ORDER BY id DESC LIMIT 1;")" "default"
eq "role-log: only this session's changes" "$($TB role-log | wc -l | tr -d ' ') $(HERDR_SESSION=s2 FAKE_AGENTS="[]" $TB role-log | wc -l | tr -d ' ')" "1 0"
export TB_POLICY_A=$_saved_appr
if [[ -n $_saved_roles ]]; then export TB_ROLES=$_saved_roles; else unset TB_ROLES; fi
if [[ -n $_saved_hs ]]; then export HERDR_SESSION=$_saved_hs; fi
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
