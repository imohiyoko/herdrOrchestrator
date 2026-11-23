# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 承認待ちを 1 か所に(ADR 0007 の段 4): 送信・agent の操作・ask で止まったコマンドが、どれも Q-<n> で tb approval list に並び、approve / reject で決める
_al_saved=$(declare -p TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS TB_PRIORITY 2>/dev/null || true)
unset HERDR_PANE_ID TB_PRIORITY
export TB_DB=$TMP/approval-list.db TB_POLICY=$TMP/approval-list.policy; : >"$TB_POLICY"
export FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s1"}},{"pane_id":"wW:p2","name":"lead1","agent_session":{"value":"s2"}}]'
$TB list >/dev/null 2>&1
alsql() { sqlite3 "$TB_DB" "$@"; }
eq "approval list: nothing pending → empty" "$($TB approval list)" ""
alsql "INSERT INTO outbox (herdr_session, from_name, from_pane, from_session, target, kind, text, status, created_at) VALUES
  ('default','w1','wW:p1','s-w1','lead1','task','please review','pending','2026-01-01T00:00:02.000Z'),
  ('default','w1','wW:p1','s-w1','lead1','info','done already','sent','2026-01-01T00:00:03.000Z');
INSERT INTO agent_ops (herdr_session, op, target, args, requested_by, status, created_at) VALUES
  ('default','stop','w2','{}','w1','pending','2026-01-01T00:00:01.000Z'),
  ('default','stop','w3','{}','w1','done','2026-01-01T00:00:00.000Z');"
oq=Q-$(alsql "SELECT id FROM requests WHERE source='outbox';"); lq=Q-$(alsql "SELECT id FROM requests WHERE source='agent_ops';")
eq "approval list: a pending send and a pending agent op each get one Q- (decided rows get none)" "$(alsql "SELECT count(*) FROM requests;")" "2"
eq "approval list: Q- with priority, required permission, requester, target" "$($TB approval list)" \
  "$oq	normal	send:task	w1	lead1	2026-01-01T00:00:02.000Z
$lq	normal	agent:stop	w1	w2	2026-01-01T00:00:01.000Z"
eq "approval list --json: one object per row with the detail" "$($TB approval list --json | jq -r '[.id, .detail] | join(" ")')" "$oq please review
$lq {}"
eq "approval priority: a person raises it → listed first" "$($TB approval priority "$lq" high) $($TB approval list | head -1 | cut -f1,2)" "$lq priority high $lq	high"
eq "approval priority: another agent cannot change it" "$(HERDR_PANE_ID=wW:p2 rc $TB approval priority "$lq" low 2>/dev/null)" "1"
printf 'worker1 cmd:agent:reject * = deny\n' >"$TB_POLICY"
eq "approval reject Q- (agent op): the agent-reject entry gate still applies" "$(HERDR_PANE_ID=wW:p1 $TB approval reject "$lq" >/dev/null 2>"$TMP/al.err"; echo $?) $(grep -c 'cmd:agent:reject = deny' "$TMP/al.err")" "1 1"
: >"$TB_POLICY"
eq "approval reject Q- (agent op): the person rejects it" "$($TB approval reject "$lq" >/dev/null; alsql "SELECT status FROM agent_ops WHERE target='w2';")" "rejected"
eq "approval reject Q- (send): the person rejects it" "$($TB approval reject "$oq" >/dev/null 2>&1; alsql "SELECT status FROM outbox WHERE text='please review';")" "denied"
eq "approval list: decided rows drop out" "$($TB approval list)" ""

# --- ask で止まったコマンドも Q- に積む(引数と標準入力ごと)。承認すると承認した側が実行する
printf 'worker1 cmd:config:set * = allow\nworker1 policy:send:* * = ask\nworker1 policy:send:* worker1 = deny\n' >"$TB_POLICY"
cp "$TB_POLICY" "$TMP/al.base"
eq "cmd queue: an ask stops with 20 and names the Q-" "$(printf '%s\n' "$(cat "$TMP/al.base")" 'worker2 send:task * = allow' | TB_PRIORITY=high HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"; echo $?) $(grep -c '承認待ち Q-[0-9]* に積みました' "$TMP/al.err")" "20 1"
cq=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: listed with the required permission, the requester and the priority" "$($TB approval list | cut -f1-5)" "$cq	high	policy:send:task	worker1	worker2"
eq "cmd queue: nothing was written yet" "$(grep -c '^worker2' "$TB_POLICY")" "0"
eq "cmd queue: the requester cannot approve its own request" "$(HERDR_PANE_ID=wW:p1 rc $TB approval approve "$cq" 2>/dev/null)" "1"
eq "cmd queue: an agent without approve: cannot approve" "$(HERDR_PANE_ID=wW:p2 $TB approval approve "$cq" 2>&1 >/dev/null | grep -c '承認する許可がありません')" "1"
eq "cmd queue: a person approves → the command runs with the saved stdin" "$($TB approval approve "$cq" >/dev/null 2>&1; echo $?) $(grep -c '^worker2 send:task' "$TB_POLICY") $(alsql "SELECT status FROM requests WHERE id=${cq#Q-};")" "0 1 done"
eq "cmd queue: an approved request runs once" "$($TB approval approve "$cq" 2>&1 >/dev/null | grep -c '承認待ちではありません')" "1"
eq "cmd queue: the run key is not stored in the clear" "$(alsql "SELECT count(*) FROM requests WHERE run_key IS NOT NULL;")" "0"
eq "cmd queue: the token cannot be reused by hand" "$(TB_APPROVED_REQUEST=$cq HERDR_PANE_ID=wW:p1 rc $TB config set policy </dev/null 2>/dev/null)" "1"
# agent の承認者: approve:<操作>:<対象> <依頼者> の行で決まる
# 承認者は承認の権限だけを持つ(config set も policy: も持たない)。実行は依頼者 worker1 の権限で機械的に行う
cp "$TMP/al.base" "$TB_POLICY"; printf 'lead1 approve:policy:send:task:* worker1 = allow\n' >>"$TB_POLICY"
printf '%s\n' "$(cat "$TB_POLICY")" 'worker3 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"
cq2=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: an agent with only approve:<op>:<target> <requester> approves; it runs as the requester" "$(HERDR_PANE_ID=wW:p2 $TB approval approve "$cq2" >/dev/null 2>&1; echo $?) $(grep -c '^worker3 send:task' "$TB_POLICY") $(alsql "SELECT decided_by FROM requests WHERE id=${cq2#Q-};")" "0 1 lead1"
cp "$TMP/al.base" "$TB_POLICY"
printf '%s\n' "$(cat "$TB_POLICY")" 'worker4 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"
cq3=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: reject → not run" "$($TB approval reject "$cq3") $(grep -c '^worker4' "$TB_POLICY") $(alsql "SELECT status FROM requests WHERE id=${cq3#Q-};")" "$cq3 rejected 0 rejected"
printf '%s\n' "$(cat "$TB_POLICY")" 'worker5 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"
cq4=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: the requester is gone → not run, failed" "$(FAKE_AGENTS='[{"pane_id":"wW:p2","name":"lead1","agent_session":{"value":"s2"}}]' $TB approval approve "$cq4" 2>&1 >/dev/null | grep -c 'もういない') $(grep -c '^worker5' "$TB_POLICY") $(alsql "SELECT status FROM requests WHERE id=${cq4#Q-};")" "1 0 failed"
eq "cmd queue: deny still stops without a Q-" "$(printf '%s\n' "$(cat "$TB_POLICY")" 'worker1 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"; echo $?) $(grep -c 'Q-' "$TMP/al.err")" "1 0"
unset -f alsql; unset oq lq cq cq2 cq3 cq4 TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS; eval "$_al_saved"; unset _al_saved
