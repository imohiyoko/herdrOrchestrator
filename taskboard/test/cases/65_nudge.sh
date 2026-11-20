# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- nudge(チケットを握ったまま休んでいる agent に「続けて」と送る。経路は tb send と同じ outbox + delivery)
_ng_saved=("${TB_CONFIG-}" "${TB_ROUTES-}" "${TB_APPROVALS-}" "${TB_DELIVERY-}" "${TB_PROTECT-}" "${HERDR_PANE_ID-}")
export TB_DB=$TMP/nudge.db TB_CONFIG=$TMP/nudge-projects.conf TB_PROTECT=$TMP/nudge-protect.conf
unset TB_ROUTES TB_DELIVERY TB_APPROVALS HERDR_PANE_ID
printf 'ng=coord\n' >"$TB_CONFIG"; : >"$TB_PROTECT"
: >"$FAKE_LOG"
nsql() { sqlite3 "$TB_DB" "$@"; }
ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"%s","agent_session":{"value":"%s"}}' "$1" "$2" "$3" "$4"; }
export FAKE_AGENTS="[$(ag wC:p1 coord idle sc),$(ag w1:p1 w1 idle s1),$(ag w2:p1 w2 working s2),$(ag w3:p1 w3 idle s3),$(ag w4:p1 w4 idle s4),$(ag w5:p1 w5 blocked s5)]"
# 休み: 最後の turn の終わりから何分たったか
rest() { nsql "INSERT OR REPLACE INTO agent_activity (herdr_session, session_id, last_prompt_at, last_stop_at, updated_at)
  VALUES ('default', '$1', strftime('%Y-%m-%dT%H:%M:%fZ','now','-$(($2 + 1)) minutes'), strftime('%Y-%m-%dT%H:%M:%fZ','now','-$2 minutes'), 'x');"; }
claim() { local t; t=$($TB add -p ng -t "$2" --owner "$1" ${3:+--priority $3}); $TB update "$t" --status in_progress >/dev/null; echo "$t"; }
t1=$(claim w1 '本の作業')
t2=$(claim w2 '作業中')
t3=$(claim w3 '低い方' low); t4=$(claim w3 '高い方' high)
t5=$(claim w5 '承認待ち')
t6=$($TB add -p ng -t 'まだ claim していない' --owner w4)
rest s1 30; rest s2 30; rest s3 30; rest s5 30
row() { grep -P "^$1\t" <<<"$out" | cut -f2-3 | tr '\t' ' '; }
out=$($TB nudge --dry-run)
eq "nudge: idle holder → would" "$(row w1)" "$t1 would"
eq "nudge: working → skip" "$(row w2)" "$t2 skip"
eq "nudge: blocked(人の操作待ち)→ skip" "$(row w5)" "$t5 skip"
eq "nudge: todo の割り当てだけでは握っていない" "$(grep -c '^w4' <<<"$out")" "0"
eq "nudge: 複数 → 優先度の高い 1 枚を名指し" "$(row w3)" "$t4 would"
eq "nudge: --dry-run は何も送らない" "$(prompts) $(nsql 'SELECT COUNT(*) FROM outbox;')" "0 0"
eq "nudge: agent の pane からは拒否(--dry-run 以外)" "$(HERDR_PANE_ID=w1:p1 rc $TB nudge)" "1"
eq "nudge: dry-run は agent の pane からも読める" "$(HERDR_PANE_ID=w1:p1 rc $TB nudge --dry-run)" "0"
# 活動記録に入力の時刻が無い(Stop だけ)
nsql "UPDATE agent_activity SET last_prompt_at=NULL WHERE session_id='s1';"
eq "nudge: last_prompt_at が無くても休みを数える" "$(out=$($TB nudge --dry-run); row w1)" "$t1 would"
# 休みが短い
rest s1 3
eq "nudge: 休みが短い → skip" "$($TB nudge --dry-run | grep -P '^w1\t' | grep -c '休んで 3 分')" "1"
rest s1 30
# 送る: 指揮役から tb send したのと同じ outbox + 文面のラベル
out=$($TB nudge)
eq "nudge: sent" "$(row w1)" "$t1 sent"
eq "nudge: 指揮役からの task として outbox に残る" "$(nsql "SELECT from_name||' '||from_pane||' '||kind||' '||task_id||' '||status FROM outbox WHERE target='w1';")" "coord nudge task $t1 sent"
eq "nudge: 文面のラベルと中身" "$(grep '^prompt w1' "$FAKE_LOG" | grep -c "\[from: coord / nudge / $t1 / task / O-[0-9]*\] \[催促\] $t1「本の作業」")" "1"
eq "nudge: 複数のときは文面でほかのチケットに触れる" "$(grep '^prompt w3' "$FAKE_LOG" | grep -c "ほかにも持っています($t3)")" "1"
eq "nudge: messages に kind=send で残る" "$(nsql "SELECT COUNT(*) FROM messages WHERE kind='send' AND target IN ('w1','w3') AND result='sent';")" "2"
# 同じ休みには二度送らない
eq "nudge: この休みで催促済み" "$($TB nudge | grep -P '^w1\t' | grep -c '催促済み')" "1"
# 新しい休み(催促を受けて動き、また止まった)なら、また送る。NUDGE_MAX 回で止める
for i in 1 2; do nsql "UPDATE outbox SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','-60 minutes') WHERE target='w1';"; rest s1 30; $TB nudge >/dev/null; done
eq "nudge: 3 回送った" "$(nsql "SELECT COUNT(*) FROM outbox WHERE target='w1' AND from_pane='nudge' AND status='sent';")" "3"
nsql "UPDATE outbox SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','-60 minutes') WHERE target='w1';"; rest s1 30
eq "nudge: --max を超えたら止める" "$($TB nudge | grep -P '^w1\t' | grep -c '催促 3 回で止めた')" "1"
# 人の返事待ち(未対応の question)
nsql "DELETE FROM outbox WHERE target='w1';"
HERDR_PANE_ID=w1:p1 FAKE_AGENTS="[$(ag w1:p1 w1 idle s1),$(ag wC:p1 coord working sc)]" $TB comment "$t1" --kind question --text '方針はどちら?' >/dev/null 2>&1
eq "nudge: 未対応の question → skip" "$($TB nudge --dry-run | grep -P '^w1\t' | grep -c '人の返事待ち')" "1"
$TB resolve "$(nsql "SELECT 'C-'||id FROM comments WHERE task_id='$t1';")" >/dev/null
eq "nudge: 対応済みにしたら → would" "$(out=$($TB nudge --dry-run); row w1)" "$t1 would"
# 保護
printf 'w1 = 定期実行の受け口\n' >"$TB_PROTECT"
eq "nudge: 保護 → skip" "$($TB nudge --dry-run | grep -P '^w1\t' | grep -c '保護(定期実行の受け口)')" "1"
: >"$TB_PROTECT"
# 先行チケット待ち
tb0=$($TB add -p ng -t '先行'); $TB link "$tb0" blocks "$t1" >/dev/null
eq "nudge: 先行チケット待ち → skip" "$($TB nudge --dry-run | grep -P '^w1\t' | grep -c '先行チケット')" "1"
$TB unlink "$tb0" blocks "$t1" >/dev/null
# delivery の規則: hold / block の宛先には催促しない(指揮役 → その agent の task で決まる)
export TB_DELIVERY=$TMP/nudge-delivery.conf
printf 'coord -> w1 : task=hold\ncoord -> *\n' >"$TB_DELIVERY"
eq "nudge: delivery hold → skip" "$($TB nudge --dry-run | grep -P '^w1\t' | grep -c 'delivery が hold')" "1"
printf '! coord -> w1\ncoord -> *\n' >"$TB_DELIVERY"
eq "nudge: delivery block → skip" "$($TB nudge --dry-run | grep -P '^w1\t' | grep -c 'delivery が block')" "1"
unset TB_DELIVERY
# 届かなかった催促は後で届けない
eq "nudge: 送信失敗 → 取り下げ(queued に残さない)" "$(FAKE_PROMPT_RC=1 $TB nudge | grep -P '^w1\t' | grep -c '取り下げ') $(nsql "SELECT status FROM outbox WHERE target='w1' ORDER BY id DESC LIMIT 1;")" "1 denied"
# 指揮役は催促しない
tc=$(claim coord '指揮役の仕事'); rest sc 30
eq "nudge: 指揮役 → skip" "$($TB nudge --dry-run | grep -P '^coord\t' | cut -f3-4 | tr '\t' ' ')" "skip 指揮役"
eq "nudge: --json" "$($TB nudge --dry-run --json | jq -r 'select(.agent=="w2") | .result')" "skip"
eq "nudge: tb list の列は変えない" "$($TB list | grep -P "^$t1\t" | cut -f1-3 | tr '\t' ' ')" "$t1 in_progress w1"
# digest: 複数チケットを握っている agent を指揮役に知らせる(同じ組み合わせは一度だけ)
eq "digest: 複数チケットの観測" "$($TB digest --dry-run | grep -c "w3 観測 / 複数チケット] in_progress のチケットを 2 枚握っている($t3 $t4)")" "1"
$TB digest >/dev/null
eq "digest: 同じ組み合わせは二度出さない" "$($TB digest --dry-run | grep -c '複数チケット')" "0"
export TB_CONFIG=${_ng_saved[0]} TB_ROUTES=${_ng_saved[1]} TB_APPROVALS=${_ng_saved[2]} TB_DELIVERY=${_ng_saved[3]} TB_PROTECT=${_ng_saved[4]} HERDR_PANE_ID=${_ng_saved[5]}
for _v in TB_CONFIG TB_ROUTES TB_APPROVALS TB_DELIVERY TB_PROTECT HERDR_PANE_ID; do [[ -n ${!_v} ]] || unset "$_v"; done
unset FAKE_AGENTS _ng_saved _v
