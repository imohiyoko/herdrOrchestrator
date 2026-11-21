# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 承認の委任: grants.conf の approve:<元の操作> <依頼者> に当たる agent が、人の承認待ち(O-<n> / L-<n>)を承認・却下できる
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_appr=${TB_APPROVALS-}; _saved_roles=${TB_ROLES-}; _saved_db=$TB_DB _saved_dlv=${TB_DELIVERY-}
export TB_DB=$TMP/delegate.db TB_APPROVALS=$TMP/delegate-appr.conf TB_ROLES=$TMP/delegate-roles TB_GRANTS=$TMP/delegate-grants.conf TB_AGENT_CWD=$TMP
mkdir -p "$TB_ROLES"; for r in lead worker; do echo "ROLE-$r" >"$TB_ROLES/$r.md"; done
printf 'lead* = lead\nw* = worker\n' >"$TB_ROLES/map.conf"
printf '* = ask\n' >"$TB_APPROVALS"
export TB_DELIVERY=$TMP/delegate-delivery.conf; printf '* -> *\n' >"$TB_DELIVERY"
dg_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"},"cwd":"%s"}' "$1" "$1" "$1" "$TMP"; }
export FAKE_AGENTS="[$(dg_ lead1),$(dg_ w1),$(dg_ w2),$(dg_ rev1)]"
dsql() { sqlite3 "$TB_DB" "$@"; }
$TB agents >/dev/null 2>&1
dsql "UPDATE agents SET spawned_by='lead1' WHERE name='w1';"
printf '@lead approve:send:task:* ~child\nw* approve:send:* w*\nrev1 approve:agent:stop:* w1\nrev1 approve:agent:model:* *\nrev1 approve:agent:slash:* *\n' >"$TB_GRANTS"
as() { HERDR_PANE_ID=w$1:p1 "${@:2}"; }   # その agent の pane から実行する

# ---- 送信(O-<n>)
eq "send: task from w1 waits for approval" "$(as w1 rc $TB send w2 --text 'please do X' 2>/dev/null)" "20"
eq "approve: an agent without a matching grant is refused (still pending)" "$(as rev1 rc $TB approve O-1 2>/dev/null) $(dsql "SELECT status FROM outbox WHERE id=1;")" "1 pending"
eq "approve: the refusal names the missing grant" "$(as rev1 $TB approve O-1 2>&1 | grep -c "approve:<操作> <依頼者>")" "1"
eq "approve: the requester cannot approve its own ask (even with a grant)" "$(as w1 rc $TB approve O-1 2>/dev/null) $(dsql "SELECT status FROM outbox WHERE id=1;")" "1 pending"
eq "approve: --always is for the human only" "$(as lead1 rc $TB approve O-1 --always 2>/dev/null) $(dsql "SELECT status FROM outbox WHERE id=1;") $($TB approval send:task:w2 | head -1)" "1 pending ask"
eq "approve: the parent with @lead approve:send:task:* ~child delivers it" "$(as lead1 $TB approve O-1)" "O-1 approved and sent to w2"
eq "approve: delivered to the target" "$(grep '^prompt' "$FAKE_LOG" | tail -1 | grep -c 'please do X')" "1"
eq "approve: the approver and its rule are recorded" "$(dsql "SELECT reason LIKE '%agent lead1(grants.conf: @lead approve:send:task:* ~child)が承認%' FROM outbox WHERE id=1;")" "1"
eq "approve: twice → error" "$(as lead1 rc $TB approve O-1 2>/dev/null)" "1"
# 操作で絞る: lead の許可は send:task だけ(info は承認できない)
as w1 $TB send w2 --kind info --text fyi >/dev/null 2>&1
eq "approve: scoped by the operation (send:info is not granted to lead1)" "$(as lead1 rc $TB approve O-2 2>/dev/null) $(dsql "SELECT status FROM outbox WHERE id=2;")" "1 pending"
eq "approve: w2 may approve w1's ask (w* approve:send:* w*)" "$(as w2 $TB approve O-2)" "O-2 approved and sent to w2"
# 依頼者で絞る: lead1 の子ではない w2 からの依頼は承認できない
as w2 $TB send w1 --text 'from w2' >/dev/null 2>&1
eq "approve: scoped by the requester (w2 is not lead1's child)" "$(as lead1 rc $TB approve O-3 2>/dev/null) $(dsql "SELECT status FROM outbox WHERE id=3;")" "1 pending"
eq "reject: an agent with the grant rejects it, recorded" "$(as w1 $TB reject O-3) $(dsql "SELECT status, reason LIKE '%agent w1(grants.conf: w* approve:send:* w*)が却下%' FROM outbox WHERE id=3;")" "O-3 rejected denied|1"
as w1 $TB send w2 --text 'second' >/dev/null 2>&1
eq "reject: refused without a grant" "$(as rev1 rc $TB reject O-4 2>/dev/null) $(dsql "SELECT status FROM outbox WHERE id=4;")" "1 pending"
eq "approve: a missing O-<n> from an agent pane is refused as a human-only command" "$(as lead1 $TB approve O-99 2>&1 | grep -c '人が自分の端末で実行します')" "1"
eq "approve: the human still approves anything" "$($TB approve O-4)" "O-4 approved and sent to w2"
eq "approve: a role that cannot be read grants nothing" "$(as w1 $TB send w2 --text third >/dev/null 2>&1; dsql "ALTER TABLE agents RENAME COLUMN role TO role_x;"; as lead1 rc $TB approve O-5 2>/dev/null; dsql "ALTER TABLE agents RENAME COLUMN role_x TO role;")" "1"

eq "can: approve is decided by grants only (allow / deny / self)" "$($TB can lead1 approve:send:task:w2 w1 | head -1) $($TB can lead1 approve:send:info:w2 w1 | head -1) $(rc $TB can w1 approve:send:task:w2 w1)" "allow deny 4"

# ---- agent の操作(L-<n>)
dsql "UPDATE agents SET killable='yes', killable_at='2000-01-01T00:00:00.000Z';"
dsql "INSERT INTO agent_activity (herdr_session, session_id, pane_id, last_prompt_at, last_stop_at, cwd, updated_at) VALUES ('default','s-w2','ww2:p1','2000-01-01T00:00:00.000Z','2000-01-01T00:01:00.000Z','$TMP','x');"
out=$(as w1 $TB agent-stop w2 2>&1); lid=$(head -1 <<<"$out" | cut -d' ' -f1)
eq "agent-stop: w1 needs approval" "$(as w1 rc $TB agent-stop w2 >/dev/null 2>&1; dsql "SELECT COUNT(*) FROM agent_ops WHERE status='pending' AND op='stop';") ${lid%%-*}" "2 L"
eq "agent-approve: refused without a grant" "$(as lead1 rc $TB agent-approve "$lid" 2>/dev/null) $(dsql "SELECT status FROM agent_ops WHERE id=${lid#L-};")" "1 pending"
eq "agent-approve: --always is for the human only" "$(as rev1 rc $TB agent-approve "$lid" --always 2>/dev/null) $($TB approval agent:stop:w2 | head -1)" "1 ask"
dsql "UPDATE agent_ops SET decided_at='x' WHERE id=${lid#L-};"   # ほかの承認者が処理中
eq "agent-approve: a row another approver is running is not run twice" "$(as rev1 rc $TB agent-approve "$lid" 2>/dev/null) $(grep -c '^pane-close ww2:p1' "$FAKE_LOG")" "1 0"
dsql "UPDATE agent_ops SET decided_at=NULL WHERE id=${lid#L-};"
out=$(as rev1 $TB agent-approve "$lid" 2>&1)
eq "agent-approve: rev1 approve:agent:stop:* w1 runs it" "$(grep -c 'w2 stopped' <<<"$out") $(grep -c '^pane-close ww2:p1' "$FAKE_LOG")" "1 1"
eq "agent-approve: the approver is recorded" "$(dsql "SELECT status, result LIKE '%agent rev1(grants.conf: rev1 approve:agent:stop:* w1)が承認%' FROM agent_ops WHERE id=${lid#L-};")" "done|1"
lid2=$(dsql "SELECT 'L-'||id FROM agent_ops WHERE status='pending' AND op='stop' LIMIT 1;")
eq "agent-reject: by the granted agent, recorded" "$(as rev1 $TB agent-reject "$lid2") $(dsql "SELECT status, result LIKE '%agent rev1(grants.conf: rev1 approve:agent:stop:* w1)が却下%' FROM agent_ops WHERE id=${lid2#L-};")" "$lid2 rejected rejected|1"
# 人だけが決める承認待ち: model-set --all・slash --all・spawn-freeze 中の spawn
dsql "INSERT INTO agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES
  ('default','revive','w1','{\"model_set\":1,\"model\":\"haiku\",\"all\":1}','w2','pending','--all は人の承認が要ります','x'),
  ('default','slash','*','{\"command\":\"clear\",\"args\":\"\",\"all\":1}','w2','pending','slash --all は常に人の承認','x'),
  ('default','revive','w1','{\"model_set\":1,\"model\":\"haiku\"}','w2','pending','approvals: * = ask','x');"
ml=$(dsql "SELECT 'L-'||id FROM agent_ops WHERE op='revive' AND json_extract(args,'\$.all')=1;")
sl=$(dsql "SELECT 'L-'||id FROM agent_ops WHERE op='slash';")
m1=$(dsql "SELECT 'L-'||id FROM agent_ops WHERE op='revive' AND json_extract(args,'\$.all') IS NULL;")
eq "agent-approve: model-set --all and slash --all stay human-only" "$(as rev1 rc $TB agent-approve "$ml" 2>/dev/null) $(as rev1 rc $TB agent-reject "$sl" 2>/dev/null) $(dsql "SELECT COUNT(*) FROM agent_ops WHERE status='pending' AND id IN (${ml#L-},${sl#L-});")" "1 1 2"
eq "agent-reject: a single model-set can be delegated (approve:agent:model:*)" "$(as rev1 $TB agent-reject "$m1")" "$m1 rejected"
eq "model-set --all: the pending row is marked all:1" "$(HERDR_PANE_ID= $TB model-set --all sonnet >/dev/null 2>&1; dsql "SELECT COUNT(*) > 0 FROM agent_ops WHERE status='pending' AND json_extract(args,'\$.all')=1 AND id != ${ml#L-};")" "1"

export TB_APPROVALS=$_saved_appr TB_DB=$_saved_db; unset TB_GRANTS
if [[ -n $_saved_dlv ]]; then export TB_DELIVERY=$_saved_dlv; else unset TB_DELIVERY; fi
if [[ -n $_saved_roles ]]; then export TB_ROLES=$_saved_roles; else unset TB_ROLES; fi
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
