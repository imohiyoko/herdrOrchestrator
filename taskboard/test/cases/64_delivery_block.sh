# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- delivery の block(宛先の手前で止め、送信者に「チケットにして」と返す)
_bl_saved=("${TB_CONFIG-}" "${TB_ROUTES-}" "${TB_APPROVALS-}")
export TB_DB=$TMP/block.db TB_CONFIG=$TMP/block-projects.conf TB_ROUTES=$TMP/block-routes.conf TB_APPROVALS=$TMP/block-approvals.conf
export TB_DELIVERY=$TMP/block-delivery.conf
printf 'bl=coordinator\n' >"$TB_CONFIG"
printf 'coordinator -> *\n@worker -> *\nreviewer -> *\n' >"$TB_ROUTES"
printf '* = allow\n' >"$TB_APPROVALS"
bsql() { sqlite3 "$TB_DB" "$@"; }
bag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(bag coordinator),$(bag reviewer),$(bag worker1),$(bag worker2)]"
$TB agents >/dev/null 2>&1
bsql "UPDATE agents SET role='worker' WHERE name IN ('worker1','worker2');"
: >"$FAKE_LOG"
BC() { HERDR_PANE_ID=wcoordinator:p1 $TB "$@"; }
BW() { HERDR_PANE_ID=wworker1:p1 $TB "$@"; }

# 役割は書かない: 止める相手も止める種別も設定で決める
printf '@worker -> reviewer : *=block reply=now\n* -> worker2 : task=block\n' >"$TB_DELIVERY"
n=$(prompts)
out=$(BW send reviewer --text 'please review my PR' 2>"$TMP/block.err"); r=$?
eq "block: exit 4, stdout says blocked" "$r $out" "4 O-1 blocked by reviewer"
eq "block: nothing typed into any input" "$(($(prompts) - n))" "0"
eq "block: the sender is told to file a ticket (stderr)" "$(grep -c 'O-1(task)は reviewer の受信設定で止められました' "$TMP/block.err") $(grep -c 'チケットにしてください(tb add' "$TMP/block.err")" "1 1"
eq "block: outbox is denied with the rule, nothing in inbox" "$(bsql "SELECT status, reason LIKE '%delivery: @worker -> reviewer : task=block → blocked' FROM outbox WHERE id=1;") $(bsql "SELECT COUNT(*) FROM inbox;")" "denied|1 0"
eq "block: history records kind block" "$(bsql "SELECT kind, ref, target FROM messages ORDER BY id DESC LIMIT 1;")" "block|O-1|reviewer"
eq "block: --json marks blocked" "$(BW send reviewer --kind info --text fyi --json 2>/dev/null | jq -r '"\(.status) \(.blocked)"')" "denied true"
eq "block: per kind (task=block, info passes)" "$(BC send worker2 --text t 2>/dev/null | tail -1) $(BC send worker2 --kind info --text i | tail -1)" "O-3 blocked by worker2 O-4 sent to worker2"
eq "block: other senders are not blocked" "$(BC send reviewer --text ok | tail -1)" "O-5 sent to reviewer"
# 確かめた返信は reply= で通せる(相手が聞いたことへの答え)
HERDR_PANE_ID=wreviewer:p1 $TB send worker1 --kind info --text 'question for you' >/dev/null
eq "block: a verified reply passes with reply=now" "$(BW send reviewer --kind answer --reply-to O-6 --text yes | tail -1)" "O-7 sent to reviewer"

# 承認のあとで止めたら、送信者の pane に知らせる(tb send はもう終わっている)
printf 'send:task:reviewer = ask\n* = allow\n' >"$TB_APPROVALS"
eq "block: pending first" "$(BW send reviewer --text later 2>/dev/null | tail -1)" "O-8 pending-approval"
n=$(prompts)
eq "block: approve → blocked, notice typed into the sender" "$($TB approve O-8) $(($(prompts) - n)) $(grep -c "^prompt worker1	\[tb: blocked / O-8\]" "$FAKE_LOG")" "O-8 approved but blocked by reviewer 1 1"
eq "block: approve → not typed into the target" "$(grep -c '^prompt reviewer	.*later' "$FAKE_LOG")" "0"
printf '* = allow\n' >"$TB_APPROVALS"

# flush: queued の送信も同じ規則で止め、同じ宛先の後続も続けて判定する
printf '' >"$TB_DELIVERY"
FAKE_STATUS=working BC send worker2 --text q1 >/dev/null 2>&1
FAKE_STATUS=working BC send worker2 --kind info --text q2 >/dev/null 2>&1
printf '* -> worker2 : task=block\n' >"$TB_DELIVERY"
n=$(prompts)
eq "block: flush blocks then delivers the next" "$(rc $TB flush) $(bsql "SELECT group_concat(status) FROM (SELECT status FROM outbox WHERE id IN (9,10) ORDER BY id);")" "0 denied,sent"
eq "block: flush notifies the sender and sends the info" "$(($(prompts) - n)) $(grep -c "^prompt coordinator	\[tb: blocked / O-9\]" "$FAKE_LOG")" "2 1"

# 受け付けと届ける間に別の flush が同じ行を止めたら、送信者にも「止めた」と返す(sent と言わない)
bsql "CREATE TRIGGER bl_race AFTER INSERT ON outbox WHEN NEW.text='race' BEGIN
  UPDATE outbox SET status='denied', reason=reason||' / delivery: * -> worker2 : info=block → blocked' WHERE id=NEW.id; END;"
out=$(BC send worker2 --kind info --text race 2>"$TMP/block-race.err"); r=$?
bsql "DROP TRIGGER bl_race;"
eq "block: a row blocked by a concurrent flush is reported as blocked" "$r $out $(grep -c 'info=block' "$TMP/block-race.err")" "4 O-11 blocked by worker2 1"

# キーを消せば今までどおり
unset TB_DELIVERY
eq "block: removing the key → delivered again" "$(BW send reviewer --text back | tail -1)" "O-12 sent to reviewer"
export TB_CONFIG=${_bl_saved[0]} TB_ROUTES=${_bl_saved[1]} TB_APPROVALS=${_bl_saved[2]}
[[ -n $TB_ROUTES ]] || unset TB_ROUTES; [[ -n $TB_APPROVALS ]] || unset TB_APPROVALS
unset FAKE_AGENTS _bl_saved
