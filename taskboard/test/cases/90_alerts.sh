# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態を使うものがある)
# --- alerts(アラート → 解析役)
export TB_DB=$TMP/alerts.db TB_CONFIG=$TMP/send.conf TB_POLICY_A=$TMP/appr-alert.conf TB_ANALYST_RETRY_SEC=0 TB_ANALYST_MAX=1 TB_ANALYST_CWD=$TMP
printf '* alert:analyze * = allow\n* send:info * = ask-any\n* send:answer * = ask-any\n' >"$TB_POLICY_A"
export FAKE_AGENTS='[{"pane_id":"wC:p1","name":"conductor","agent_session":{"value":"sc"}},{"pane_id":"wX:p1","name":"watch1","agent_session":{"value":"sx"}},{"pane_id":"wA:p1","name":"analyst-A1","agent_session":{"value":"sa1"}}]'
: >"$FAKE_LOG"
alsql() { sqlite3 "$TB_DB" "$@"; }
eq "alert: reporter cannot set severity" "$(HERDR_PANE_ID=wX:p1 rc $TB alert --severity high --title x)" "2"
eq "alert: --source rejected" "$(HERDR_PANE_ID=wX:p1 rc $TB alert --title x --source boss)" "2"
out=$(HERDR_PANE_ID=wX:p1 $TB alert --title 'API 5xx 増加' --body '直近 5 分で 5xx が 12%')
eq "alert: new → analyst started (alert:analyze = allow)" "$out" "A-1 new analyzing"
eq "alert: workspace + agent start" "$(grep -c '^workspace-create.*--label analyst-A1' "$FAKE_LOG") $(grep -c '^start analyst-A1' "$FAKE_LOG")" "1 1"
eq "alert: analyst got the task" "$(grep '^prompt analyst-A1' "$FAKE_LOG" | grep -c 'tb alerts A-1')" "1"
eq "alert: reporter from herdr" "$(alsql "SELECT source FROM alerts WHERE id=1;")" "watch1"
eq "alert: duplicate is merged, no severity yet" "$(HERDR_PANE_ID=wX:p1 $TB alert --title 'API 5xx 増加') $(alsql "SELECT count||' '||COALESCE(severity,'-') FROM alerts WHERE id=1;")" "A-1 duplicate analyzing 2 -"
printf '* alert:analyze * = ask-any\n' >"$TB_POLICY_A"
eq "alert: ask → waits for approval" "$(HERDR_PANE_ID=wX:p1 $TB alert --title 'ディスク 80%')" "A-2 new awaiting-approval"
eq "alert-approve: refused from an agent pane" "$(HERDR_PANE_ID=wX:p1 rc $TB alert-approve A-2)" "1"
eq "alert-approve: over the cap → queued" "$(HERDR_PANE_ID= $TB alert-approve A-2)" "A-2 queued"
eq "alert-result: other agent refused" "$(HERDR_PANE_ID=wX:p1 rc $TB alert-result A-1 --severity low --summary x)" "1"
eq "alert-result: severity required" "$(HERDR_PANE_ID=wA:p1 rc $TB alert-result A-1 --summary x)" "2"
printf '# 報告\n原因: デプロイ直後の設定漏れ\n' >"$TMP/report.md"
out=$(HERDR_PANE_ID=wA:p1 $TB alert-result A-1 --severity critical --summary '設定漏れが原因。ロールバック推奨' --report "$TMP/report.md")
eq "alert-result: analyst returns" "$out" "A-1 analyzed"
eq "alert-result: sent to conductor" "$(grep '^prompt conductor' "$FAKE_LOG" | tail -1 | grep -c '\[alert A-1 analyzed / critical\] API 5xx 増加 — 設定漏れが原因')" "1"
eq "alert-result: slot freed → queued one starts" "$(alsql "SELECT status||' '||analyst FROM alerts WHERE id=2;")" "analyzing analyst-A2"
eq "alerts: full text has result" "$($TB alerts A-1 | grep -c '解析結果\|デプロイ直後')" "2"
eq "alert: after analyzed, same title is new" "$(HERDR_PANE_ID=wX:p1 $TB alert --title 'API 5xx 増加' | cut -d' ' -f1-2)" "A-3 new"
# 結果が届かなかったら digest が回収
FAKE_STATUS=working HERDR_PANE_ID=wA:p1 $TB alert-result A-1 --severity critical --summary '再送テスト' >/dev/null 2>&1   # 上書き・宛先使用中
alsql "UPDATE alerts SET result_delivered_at=NULL WHERE id=1;"
$TB digest >/dev/null
msg=$(grep '^prompt conductor' "$FAKE_LOG" | tail -1)
eq "digest: undelivered alert result" "$([[ $msg == *'A-1 解析結果 / critical'* ]] && echo ok || echo "$msg")" "ok"
eq "digest: new alerts listed" "$([[ $msg == *'A-3 アラート / '* ]] && echo ok || echo "$msg")" "ok"
eq "digest: result marked delivered" "$(alsql "SELECT result_delivered_at IS NOT NULL FROM alerts WHERE id=1;")" "1"
printf '* alert:analyze * = allow\n' >"$TB_POLICY_A"
eq "alert: start failure → queued with note" "$(TB_ANALYST_MAX=5 FAKE_START_RC=1 HERDR_PANE_ID=wX:p1 $TB alert --title 'new one' | cut -d' ' -f3) $(alsql "SELECT note FROM alerts WHERE id=4;")" "queued agent を起動できない(exit 1)"
eq "alert-close: human" "$(HERDR_PANE_ID= $TB alert-close A-4)" "A-4 closed"
eq "alert: cli (no pane) allowed" "$(HERDR_PANE_ID= $TB alert --title 'from monitor script' | cut -d' ' -f2) $(alsql "SELECT source FROM alerts ORDER BY id DESC LIMIT 1;")" "new cli"
# アラートはチケット(020。ADR 0003): project alert・種別 bug・ラベル alert。段階と緊急度がチケットの状態・優先度に写る
eq "ticket: each alert is a ticket" "$(alsql "SELECT group_concat(id||':'||task_id, ' ') FROM (SELECT id, task_id FROM alerts ORDER BY id);")" "1:T-alert-001 2:T-alert-002 3:T-alert-003 4:T-alert-004 5:T-alert-005"
eq "ticket: analyzed → review, critical → highest, owner cleared" "$($TB show T-alert-001 | jq -r '[.status, .priority, .type, (.labels|join(",")), (.owner_agent // "-")] | join(" ")')" "review highest bug alert -"
eq "ticket: analyzing → in_progress, owner is the analyst" "$($TB show T-alert-002 | jq -r '[.status, .owner_agent] | join(" ")')" "in_progress analyst-A2"
eq "ticket: closed → done (and not reported as a finished task)" "$($TB show T-alert-004 | jq -r '[.status, (.reported_at != null)] | join(" ")')" "done true"
eq "ticket: summary of the result" "$($TB show T-alert-001 | jq -r .summary)" "再送テスト"
eq "ticket: alerts A-n shows the ticket" "$($TB alerts A-1 | grep -c 'チケット: T-alert-001')" "1"
eq "ticket: history records the stages" "$($TB history T-alert-002 --json | jq -r .kind | sort | tr '\n' ' ')" "created label_added owner status "
eq "ticket: tb done refused (close with alert-close)" "$(HERDR_PANE_ID= $TB done T-alert-001 -d x -m y 2>&1 | grep -c 'tb alert-approve / alert-result / alert-close A-1')" "1"
eq "ticket: tb update --status refused" "$(HERDR_PANE_ID= rc $TB update T-alert-003 --status blocked) $(HERDR_PANE_ID= rc $TB block T-alert-003) $(HERDR_PANE_ID= rc $TB claim T-alert-003 --owner w)" "1 1 1"
eq "ticket: other fields can change (label, priority)" "$(HERDR_PANE_ID= $TB update T-alert-003 --add-label infra --priority highest >/dev/null; $TB show T-alert-003 | jq -r '[.priority, (.labels|join(","))] | join(" ")')" "highest alert,infra"
eq "ticket: an unrelated alert update keeps the human's priority" "$(alsql "UPDATE alerts SET note='x' WHERE id=3;"; $TB show T-alert-003 | jq -r .priority)" "highest"
eq "ticket: alert-close → done" "$(HERDR_PANE_ID= $TB alert-close A-1 >/dev/null; $TB show T-alert-001 | jq -r .status)" "done"
eq "ticket: title edited on the ticket shows in alerts" "$(HERDR_PANE_ID= $TB update T-alert-003 --title '題を直した' >/dev/null; alsql "SELECT title FROM alerts WHERE id=3;")" "題を直した"
# scheduler 用
: >"$FAKE_LOG"
eq "system-send: from agent pane refused" "$(HERDR_PANE_ID=wX:p1 rc $TB system-send watch1 --text 'look' --job watch)" "1"
eq "system-send: sends and logs" "$(HERDR_PANE_ID= rc $TB system-send watch1 --text 'look around' --job watch) $(alsql "SELECT kind||':'||ref||':'||result FROM messages ORDER BY id DESC LIMIT 1;")" "0 scheduler:job:watch:sent"
eq "system-send: busy → 10" "$(FAKE_STATUS=working HERDR_PANE_ID= rc $TB system-send watch1 --text x --job watch)" "10"
unset FAKE_AGENTS TB_POLICY_A TB_ANALYST_RETRY_SEC TB_ANALYST_MAX TB_ANALYST_CWD
