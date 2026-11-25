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

# --- ask で止まったコマンドも Q- に積む(引数と標準入力ごと)。承認されると依頼した agent に知らせ、依頼者が tb approval run で 1 回だけ実行する
printf 'worker1 cmd:config:set * = allow\nworker1 policy:send:* * = ask\nworker1 policy:send:* worker1 = deny\n' >"$TB_POLICY"
cp "$TB_POLICY" "$TMP/al.base"
eq "cmd queue: an ask stops with 20 and names the Q-" "$(printf '%s\n' "$(cat "$TMP/al.base")" 'worker2 send:task * = allow' | TB_PRIORITY=high HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"; echo $?) $(grep -c '承認待ち Q-[0-9]* に積みました' "$TMP/al.err")" "20 1"
cq=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: listed with the required permission, the requester and the priority" "$($TB approval list | cut -f1-5)" "$cq	high	policy:send:task	worker1	worker2"
eq "cmd queue: nothing was written yet" "$(grep -c '^worker2' "$TB_POLICY")" "0"
eq "cmd queue: the requester cannot approve its own request" "$(HERDR_PANE_ID=wW:p1 rc $TB approval approve "$cq" 2>/dev/null)" "1"
eq "cmd queue: an agent without approve: cannot approve" "$(HERDR_PANE_ID=wW:p2 $TB approval approve "$cq" 2>&1 >/dev/null | grep -c '承認する許可がありません')" "1"
eq "cmd queue: the requester cannot run it before approval" "$(HERDR_PANE_ID=wW:p1 $TB approval run "$cq" 2>&1 >/dev/null | grep -c 'まだ承認されていません')" "1"
eq "cmd queue: a person approves → not run yet, approved by human" "$($TB approval approve "$cq" >/dev/null 2>&1; echo $?) $(grep -c '^worker2 send:task' "$TB_POLICY") $(alsql "SELECT status||' '||decided_by FROM requests WHERE id=${cq#Q-};")" "0 0 approved human"
eq "cmd queue: the requester's pane gets the one-time command (not stopped by the default delivery rules)" "$(grep -c "^prompt worker1	\[tb approval / $cq / human\] $cq は human に承認されました。tb approval run $cq" "$FAKE_LOG") $(alsql "SELECT count(*) FROM messages WHERE kind='approval' AND ref='$cq' AND target='worker1' AND result='sent';") $(alsql "SELECT notified_at IS NOT NULL FROM requests WHERE id=${cq#Q-};")" "1 1 1"
eq "cmd queue: another agent cannot run someone else's request" "$(HERDR_PANE_ID=wW:p2 $TB approval run "$cq" 2>&1 >/dev/null | grep -c '依頼した agent だけ') $(grep -c '^worker2' "$TB_POLICY")" "1 0"
eq "cmd queue: the requester runs it → the command runs with the saved stdin" "$(HERDR_PANE_ID=wW:p1 $TB approval run "$cq" >/dev/null 2>&1; echo $?) $(grep -c '^worker2 send:task' "$TB_POLICY") $(alsql "SELECT status FROM requests WHERE id=${cq#Q-};")" "0 1 done"
eq "cmd queue: the run is one-time (a second run is refused)" "$(HERDR_PANE_ID=wW:p1 $TB approval run "$cq" 2>&1 >/dev/null | grep -c 'もう実行しました')" "1"
eq "cmd queue: an approved request cannot be approved again" "$($TB approval approve "$cq" 2>&1 >/dev/null | grep -c '承認待ちではありません')" "1"
eq "cmd queue: the run key is not stored in the clear" "$(alsql "SELECT count(*) FROM requests WHERE run_key IS NOT NULL;")" "0"
eq "cmd queue: the token cannot be reused by hand" "$(TB_APPROVED_REQUEST=$cq HERDR_PANE_ID=wW:p1 rc $TB config set policy </dev/null 2>/dev/null)" "1"
# agent の承認者: approve:<操作>:<対象> <依頼者> の行で決まる
# 承認者は承認の権限だけを持つ(config set も policy: も持たない)。実行は依頼者 worker1 が自分の権限で行う
cp "$TMP/al.base" "$TB_POLICY"; printf 'lead1 approve:policy:send:task:* worker1 = allow\n' >>"$TB_POLICY"
printf '%s\n' "$(cat "$TB_POLICY")" 'worker3 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"
cq2=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: an agent with only approve:<op>:<target> <requester> approves; the requester runs it" "$(HERDR_PANE_ID=wW:p2 $TB approval approve "$cq2" >/dev/null 2>&1; HERDR_PANE_ID=wW:p1 $TB approval run "$cq2" >/dev/null 2>&1; echo $?) $(grep -c '^worker3 send:task' "$TB_POLICY") $(alsql "SELECT decided_by FROM requests WHERE id=${cq2#Q-};")" "0 1 lead1"
cp "$TMP/al.base" "$TB_POLICY"
printf '%s\n' "$(cat "$TB_POLICY")" 'worker4 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"
cq3=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: reject → not run" "$($TB approval reject "$cq3") $(grep -c '^worker4' "$TB_POLICY") $(alsql "SELECT status FROM requests WHERE id=${cq3#Q-};")" "$cq3 rejected 0 rejected"
eq "cmd queue: reject → the requester is told" "$(grep -c "^prompt worker1	\[tb approval / $cq3 / human\] $cq3 は human に却下されました" "$FAKE_LOG")" "1"
eq "cmd queue: a rejected request cannot be run" "$(HERDR_PANE_ID=wW:p1 $TB approval run "$cq3" 2>&1 >/dev/null | grep -c '承認されていません')" "1"
printf '%s\n' "$(cat "$TB_POLICY")" 'worker5 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"
cq4=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "cmd queue: the requester is gone → not approved, stays pending, says to reject or revive" "$(FAKE_AGENTS='[{"pane_id":"wW:p2","name":"lead1","agent_session":{"value":"s2"}}]' $TB approval approve "$cq4" 2>&1 >/dev/null | grep -c "依頼者 worker1 が herdr にいないため承認しません") $(alsql "SELECT status||' '||(decided_by IS NULL) FROM requests WHERE id=${cq4#Q-};")" "1 pending 1"
eq "cmd queue: the requester is busy → approved, not run, the approver is told the notice did not go out" "$(FAKE_STATUS=working $TB approval approve "$cq4" 2>&1 >/dev/null | grep -c '知らせを届けられませんでした'; grep -c '^worker5' "$TB_POLICY") $(alsql "SELECT status||' '||(notified_at IS NULL) FROM requests WHERE id=${cq4#Q-};")" "1
0 approved 1"

# --- deny も Q- に残る。開けられるのは人だけ。人が承認すると、依頼者が 1 回だけ実行できる(承認者 lead1 の行は先に書いておく)
printf 'lead1 approve:policy:send:task:* worker1 = allow\n' >>"$TB_POLICY"
eq "deny: stops with 1 and is recorded as a denied Q-" "$(printf '%s\n' "$(cat "$TB_POLICY")" 'worker1 send:task * = allow' | HERDR_PANE_ID=wW:p1 $TB config set policy >/dev/null 2>"$TMP/al.err"; echo $?) $(grep -c '記録 Q-[0-9]*(例外は人が tb approval approve Q-[0-9]* で許可できます)' "$TMP/al.err")" "1 1"
dq=$(grep -o 'Q-[0-9]*' "$TMP/al.err" | head -1)
eq "deny: the record says who asked, what and which rule" "$(alsql "SELECT tier||' '||status||' '||requester||' '||op||' '||decided_by||' '||rule FROM requests WHERE id=${dq#Q-};")" "deny denied worker1 policy:send:task policy policy: worker1 policy:send:* worker1 = deny"
eq "deny: not in the pending list" "$($TB approval list | grep -c "^$dq	")" "0"
eq "deny: the requester cannot run it" "$(HERDR_PANE_ID=wW:p1 $TB approval run "$dq" 2>&1 >/dev/null | grep -c '承認されていません')" "1"
eq "deny: an agent with approve: still cannot open a deny" "$(HERDR_PANE_ID=wW:p2 rc $TB approval approve "$dq" 2>/dev/null) $(alsql "SELECT status FROM requests WHERE id=${dq#Q-};")" "1 denied"
eq "deny: a person approves the exception → the requester runs it once" "$($TB approval approve "$dq" >/dev/null 2>&1; HERDR_PANE_ID=wW:p1 $TB approval run "$dq" >/dev/null 2>&1; echo $?) $(grep -c '^worker1 send:task' "$TB_POLICY") $(alsql "SELECT status||' '||decided_by FROM requests WHERE id=${dq#Q-};")" "0 1 done human"
eq "deny: rejecting a deny record is refused (it is already refused)" "$($TB approval reject "$dq" 2>&1 >/dev/null | grep -c '承認待ちではありません')" "1"

# --- allow も Q- に残る(policy が許可。承認は要らない)。history で誰が依頼し、何が・誰が決めたかを見る
eq "allow: an allowed agent call is recorded as allowed by policy" "$(alsql "SELECT count(*) FROM requests WHERE tier='allow' AND status='allowed' AND requester='worker1' AND op='cmd:config:set' AND decided_by='policy';" | awk '{print ($1>0)}')" "1"
eq "allow: not in the pending list" "$($TB approval list)" "$(alsql "SELECT 'Q-'||id FROM requests WHERE 0;")"
eq "allow: a person's own calls are not recorded" "$(alsql "SELECT count(*) FROM requests WHERE requester='human';")" "0"
eq "history --mine: the requester sees its own records, newest first" "$(HERDR_PANE_ID=wW:p1 $TB approval history --mine --limit 1 | cut -f1-4,7)" "$(alsql "SELECT 'Q-'||id||char(9)||tier||char(9)||status||char(9)||requester||char(9)||decided_by FROM requests WHERE requester='worker1' ORDER BY id DESC LIMIT 1;")"
eq "history --by: shows ask, deny and allow with who decided" "$($TB approval history --by worker1 --limit 1000 | awk -F'\t' '{print $2" "$7}' | sort -u | tr '\n' ',')" "allow policy,ask human,ask lead1,deny human,deny policy,"
eq "history --json: rule and result are there" "$($TB approval history --json --limit 1000 | jq -r "select(.id==\"$dq\") | [.rule, (.result|startswith(\"exit 0\"))] | @tsv")" "policy: worker1 policy:send:* worker1 = deny	true"
eq "history --json: another agent does not see the requester's command output" "$(HERDR_PANE_ID=wW:p2 $TB approval history --json --limit 1000 | jq -r "select(.id==\"$dq\") | .result")" ""
eq "history --json: the requester sees its own command output" "$(HERDR_PANE_ID=wW:p1 $TB approval history --json --limit 1000 | jq -r "select(.id==\"$dq\") | (.result|startswith(\"exit 0\"))")" "true"
eq "allow: scheduler (system:) allows are not recorded" "$(TB_SYSTEM=sched $TB list >/dev/null 2>&1; alsql "SELECT count(*) FROM requests WHERE requester LIKE 'system:%';")" "0"
# --- 会話(tb conv)の文面は、allow / deny の記録にも残さない
printf 'worker1 cmd:conv:voice-check * = deny\n' >"$TB_POLICY"
cv=CANARYCONV$$
eq "conv: a denied voice-check stops (1) and is recorded" "$(printf -- '- %s\n' "$cv" | HERDR_PANE_ID=wW:p1 rc $TB conv-voice-check direct 2>/dev/null) $(alsql "SELECT count(*) FROM requests WHERE op='cmd:conv:voice-check' AND tier='deny';")" "1 1"
HERDR_PANE_ID=wW:p1 $TB conv-route --text "$cv" >/dev/null 2>&1
eq "conv: neither the args nor stdin are in the records (allow and deny)" "$(alsql "SELECT count(*) FROM requests WHERE detail LIKE '%$cv%' OR payload LIKE '%$cv%';") $(alsql "SELECT count(*) FROM requests WHERE requester='worker1' AND detail LIKE 'tb conv-%(会話の文面を含みうるため%';" | awk '{print ($1>=2)}')" "0 1"
cvq=Q-$(alsql "SELECT max(id) FROM requests WHERE op='cmd:conv:voice-check';")
eq "conv: approving a redacted record is refused (nothing to run)" "$($TB approval approve "$cvq" 2>&1 >/dev/null | grep -c '中身を残していません') $(alsql "SELECT status FROM requests WHERE id=${cvq#Q-};")" "1 denied"
: >"$TB_POLICY"

# --- 送信・agent の操作も、どの入口(Q- / O- / L- / tb agent-approve)で決めても、依頼者に 1 回だけ知らせる
alsql "INSERT INTO outbox (herdr_session, from_name, from_pane, from_session, target, kind, text, status, created_at) VALUES ('default','worker1','wW:p1','s1','lead1','task','direct path','pending','2026-01-01T00:00:05.000Z');
INSERT INTO agent_ops (herdr_session, op, target, args, requested_by, status, created_at) VALUES ('default','stop','w9','{}','worker1','pending','2026-01-01T00:00:06.000Z');"
oid=$(alsql "SELECT id FROM outbox WHERE text='direct path';"); oq2=Q-$(alsql "SELECT id FROM requests WHERE source='outbox' AND source_id=$oid;")
lid=$(alsql "SELECT id FROM agent_ops WHERE target='w9';"); lq2=Q-$(alsql "SELECT id FROM requests WHERE source='agent_ops' AND source_id=$lid;")
eq "notice: tb approve O-<n> stopped by the default delivery → only the existing block notice, no second notice" "$($TB approve "O-$oid" >/dev/null 2>&1; grep -c "tb approval / $oq2 /" "$FAKE_LOG") $(grep -c "^prompt worker1	\[tb: blocked / O-$oid\]" "$FAKE_LOG")" "0 1"
printf 'worker1 -> lead1 : task=now\n' >"$TMP/al.delivery"
alsql "INSERT INTO outbox (herdr_session, from_name, from_pane, from_session, target, kind, text, status, created_at) VALUES ('default','worker1','wW:p1','s1','lead1','task','direct path 2','pending','2026-01-01T00:00:07.000Z');"
oid=$(alsql "SELECT id FROM outbox WHERE text='direct path 2';"); oq2=Q-$(alsql "SELECT id FROM requests WHERE source='outbox' AND source_id=$oid;")
eq "notice: tb approve O-<n> (not via Q-) tells the requester once, with the real outcome" "$(TB_DELIVERY=$TMP/al.delivery $TB approve "O-$oid" >/dev/null 2>&1; grep -c "^prompt worker1	\[tb approval / $oq2 / human\] $oq2(送信 send:task lead1)は human に承認され、届けました" "$FAKE_LOG")" "1"
eq "notice: tb agent-reject L-<n> (not via Q-) tells the requester once" "$($TB agent-reject "L-$lid" >/dev/null 2>&1; grep -c "^prompt worker1	\[tb approval / $lq2 / human\] $lq2(agent の操作 agent:stop w9)は human に却下されました" "$FAKE_LOG")" "1"
eq "notice: deciding again does not tell again" "$(TB_DELIVERY=$TMP/al.delivery $TB approve "O-$oid" >/dev/null 2>&1; grep -c "tb approval / $oq2 /" "$FAKE_LOG")" "1"
eq "history: bad args" "$(rc $TB approval history --limit 0 2>/dev/null) $(rc $TB approval history --bogus 2>/dev/null)" "2 2"
unset -f alsql; unset oq lq cq cq2 cq3 cq4 dq cv cvq oid oq2 lid lq2 TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS; eval "$_al_saved"; unset _al_saved
