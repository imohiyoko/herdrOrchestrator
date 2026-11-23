# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 権限の変更を行ごとに絞る(ADR 0007): agent が policy を書くと、変わる行ごとに policy:<行の操作> 対象 <行の主体> を見る(既定 deny)
_pw_saved=$(declare -p TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS 2>/dev/null || true)
unset HERDR_PANE_ID
export TB_DB=$TMP/policy-write.db TB_POLICY=$TMP/policy-write.policy
export FAKE_AGENTS='[{"pane_id":"wW:p1","name":"pw1","agent_session":{"value":"s1"}},{"pane_id":"wW:p2","name":"pw2","agent_session":{"value":"s2"}}]'
$TB list >/dev/null 2>&1
pw_base='pw1 cmd:config:set * = allow
pw1 cmd:approval:allow * = allow
pw1 policy:send:* pw2 = allow'
printf '%s\n' "$pw_base" >"$TB_POLICY"
pwset() { printf '%s\n' "$pw_base" "$@" | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/pw.err"; echo $?; }
eq "policy write: the command alone is not enough to add a row for another agent" "$(pwset 'pw2 agent:stop * = allow') $(grep -c "policy:agent:stop を対象 pw2 に許す行がありません" "$TMP/pw.err") $(grep -c 'agent:stop' "$TB_POLICY")" "1 1 0"
eq "policy write: a row the scope covers is written" "$(pwset 'pw2 send:task * = allow') $(grep -c '^pw2 send:task' "$TB_POLICY")" "0 1"
eq "policy write: another subject is outside the scope" "$(pwset 'pw2 send:task * = allow' '* send:task * = allow') $(grep -c '^\* send:task' "$TB_POLICY")" "1 0"
eq "policy write: removing a row is a change too" "$(printf '%s\n' 'pw1 cmd:config:set * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>&1; echo $?) $(grep -c '^pw1 policy:' "$TB_POLICY")" "1 1"
eq "policy write: comments and order alone are not changes" "$(pwset '# note' 'pw2 send:task * = allow' >/dev/null; printf '%s\n' '# moved' 'pw2 send:task * = allow' "$pw_base" | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>&1; echo $?)" "0"
eq "policy write: a new kind of operation works with the same rule form (no list of kinds)" "$(printf '%s\n' "$pw_base" 'pw1 policy:brand-new:* self = allow' >"$TB_POLICY"; pwset 'pw1 policy:brand-new:* self = allow' 'pw1 brand-new:op * = allow')" "0"
printf '%s\n' "$pw_base" >"$TB_POLICY"
eq "policy write: tb approval allow is checked the same way (* subject needs policy:<op> *)" "$(HERDR_PANE_ID=wW:p1 $TB approval allow send:task >/dev/null 2>"$TMP/pw.err"; echo $?) $(grep -c "policy:send:task を対象 \\* に許す行がありません" "$TMP/pw.err")" "1 1"
eq "policy write: a person is never stopped" "$(printf 'pw2 agent:stop * = deny\n' | $TB config set policy >/dev/null 2>&1; echo $?)" "0"
eq "policy write: tb allow refuses a policy: pattern (write it in policy directly)" "$($TB approval allow 'policy:send:task' >/dev/null 2>&1; echo $?)" "1"
unset -f pwset; unset pw_base TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS; eval "$_pw_saved"; unset _pw_saved
