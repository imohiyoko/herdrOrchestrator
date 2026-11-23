# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 承認待ちを 1 か所で(ADR 0007 の段 4): tb approval list に送信 O-<n> と agent の操作 L-<n> が並び、approve / reject はどちらも受ける
_al_saved=$(declare -p TB_DB TB_POLICY HERDR_PANE_ID 2>/dev/null || true)
unset HERDR_PANE_ID
export TB_DB=$TMP/approval-list.db TB_POLICY=$TMP/approval-list.policy; : >"$TB_POLICY"
$TB list >/dev/null 2>&1
alsql() { sqlite3 "$TB_DB" "$@"; }
eq "approval list: nothing pending → empty" "$($TB approval list)" ""
alsql "INSERT INTO outbox (herdr_session, from_name, from_pane, from_session, target, kind, text, status, created_at) VALUES
  ('default','w1','wW:p1','s-w1','lead1','task','please review','pending','2026-01-01T00:00:02.000Z'),
  ('default','w1','wW:p1','s-w1','lead1','info','done already','sent','2026-01-01T00:00:03.000Z'),
  ('other','w9','wX:p1','s-w9','lead9','task','another session','pending','2026-01-01T00:00:04.000Z');
INSERT INTO agent_ops (herdr_session, op, target, args, requested_by, status, created_at) VALUES
  ('default','stop','w2','{}','w1','pending','2026-01-01T00:00:01.000Z'),
  ('default','stop','w3','{}','w1','done','2026-01-01T00:00:00.000Z');"
oid=O-$(alsql "SELECT id FROM outbox WHERE text='please review';"); lid=L-$(alsql "SELECT id FROM agent_ops WHERE target='w2';")
eq "approval list: O- and L- together, oldest first, only pending rows of this session" "$($TB approval list)" \
  "$lid	agent:stop	w1	w2	2026-01-01T00:00:01.000Z
$oid	send:task	w1	lead1	2026-01-01T00:00:02.000Z"
eq "approval list --json: one object per row with the detail" "$($TB approval list --json | jq -r '[.id, .detail] | join(" ")')" "$lid {}
$oid please review"
eq "approval list: an unknown argument is a usage error" "$(rc $TB approval list --bogus 2>/dev/null)" "2"
printf 'worker1 cmd:agent:reject * = deny\n' >"$TB_POLICY"
eq "approval reject L-: the agent-reject entry gate still applies" "$(HERDR_PANE_ID=wW:p1 $TB approval reject "$lid" >/dev/null 2>"$TMP/al.err"; echo $?) $(grep -c 'cmd:agent:reject = deny' "$TMP/al.err") $(alsql "SELECT status FROM agent_ops WHERE id=${lid#L-};")" "1 1 pending"
: >"$TB_POLICY"
eq "approval reject L-: the person rejects an agent operation" "$($TB approval reject "$lid") $(alsql "SELECT status FROM agent_ops WHERE id=${lid#L-};")" "$lid rejected rejected"
eq "approval reject O-: the person rejects a send" "$($TB approval reject "$oid" >/dev/null 2>&1; alsql "SELECT status FROM outbox WHERE id=${oid#O-};")" "denied"
eq "approval list: decided rows drop out" "$($TB approval list)" ""
unset -f alsql; unset oid lid TB_DB TB_POLICY HERDR_PANE_ID; eval "$_al_saved"; unset _al_saved
