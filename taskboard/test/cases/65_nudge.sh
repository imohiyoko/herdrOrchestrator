# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- nudge(チケットを握ったまま休んでいる agent に「続けて」と送る。経路は tb send と同じ outbox + delivery)
_ng_nudge_set=${TB_NUDGE+x}; _ng_nudge=${TB_NUDGE-}   # 設定 nudge は、開始時に退避して専用の空設定にし、終了時に戻す(未設定なら未設定)
_ng_roles_set=${TB_ROLES+x}; _ng_roles=${TB_ROLES-}
_ng_saved=("${TB_CONFIG-}" "${TB_ROUTES-}" "${TB_APPROVALS-}" "${TB_DELIVERY-}" "${TB_PROTECT-}" "${HERDR_PANE_ID-}")
export TB_DB=$TMP/nudge.db TB_CONFIG=$TMP/nudge-projects.conf TB_PROTECT=$TMP/nudge-protect.conf
unset TB_ROUTES TB_DELIVERY TB_APPROVALS HERDR_PANE_ID
printf 'ng=coord\n' >"$TB_CONFIG"; : >"$TB_PROTECT"
export TB_NUDGE=$TMP/nudge-ignore.conf; : >"$TB_NUDGE"
: >"$FAKE_LOG"
TAB=$'\t'   # BSD grep has no -P, so the tab goes through a variable
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
row() { grep "^$1${TAB}" <<<"$out" | cut -f2-3 | tr '\t' ' '; }
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
eq "nudge: 休みが短い → skip" "$($TB nudge --dry-run | grep "^w1${TAB}" | grep -c '休んで 3 分')" "1"
rest s1 30
# 送る: 指揮役から tb send したのと同じ outbox + 文面のラベル
out=$($TB nudge)
eq "nudge: sent" "$(row w1)" "$t1 sent"
eq "nudge: 指揮役からの task として outbox に残る" "$(nsql "SELECT from_name||' '||from_pane||' '||kind||' '||task_id||' '||status FROM outbox WHERE target='w1';")" "coord nudge task $t1 sent"
eq "nudge: 文面のラベルと中身" "$(grep '^prompt w1' "$FAKE_LOG" | grep -c "\[from: coord / nudge / $t1 / task / O-[0-9]*\] \[催促\] ${t1}「本の作業」")" "1"
eq "nudge: 複数のときは文面でほかのチケットに触れる" "$(grep '^prompt w3' "$FAKE_LOG" | grep -c "ほかにも持っています($t3)")" "1"
eq "nudge: messages に kind=send で残る" "$(nsql "SELECT COUNT(*) FROM messages WHERE kind='send' AND target IN ('w1','w3') AND result='sent';")" "2"
# 同じ休みには二度送らない
eq "nudge: この休みで催促済み" "$($TB nudge | grep "^w1${TAB}" | grep -c '催促済み')" "1"
# 新しい休み(催促を受けて動き、また止まった)なら、また送る。NUDGE_MAX 回で止める
for i in 1 2; do nsql "UPDATE outbox SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','-60 minutes') WHERE target='w1';"; rest s1 30; $TB nudge >/dev/null; done
eq "nudge: 3 回送った" "$(nsql "SELECT COUNT(*) FROM outbox WHERE target='w1' AND from_pane='nudge' AND status='sent';")" "3"
nsql "UPDATE outbox SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','-60 minutes') WHERE target='w1';"; rest s1 30
eq "nudge: --max を超えたら止める" "$($TB nudge | grep "^w1${TAB}" | grep -c '催促 3 回で止めた')" "1"
# 人の返事待ち(未対応の question)
nsql "DELETE FROM outbox WHERE target='w1';"
HERDR_PANE_ID=w1:p1 FAKE_AGENTS="[$(ag w1:p1 w1 idle s1),$(ag wC:p1 coord working sc)]" $TB comment "$t1" --kind question --text '方針はどちら?' >/dev/null 2>&1
eq "nudge: 未対応の question → skip" "$($TB nudge --dry-run | grep "^w1${TAB}" | grep -c '人の返事待ち')" "1"
$TB resolve "$(nsql "SELECT 'C-'||id FROM comments WHERE task_id='$t1';")" >/dev/null
eq "nudge: 対応済みにしたら → would" "$(out=$($TB nudge --dry-run); row w1)" "$t1 would"
# 保護の設定(protect)は nudge の対象を変えない(固定の skip は無い。除外は設定 nudge の ignore)
printf 'w1 = 定期実行の受け口\n' >"$TB_PROTECT"
eq "nudge: protect だけでは除外されない" "$(out=$($TB nudge --dry-run); row w1)" "$t1 would"
# 移行: 設定 nudge が無いあいだは、以前の既定の除外(保護)を保つ。宛先を明示したら適用しない
eq "nudge: 設定 nudge が無い → 保護は以前どおり skip" "$(TB_NUDGE=$TMP/no-such-nudge.conf $TB nudge --dry-run | grep "^w1${TAB}" | cut -f3-4 | tr '\t' ' ')" "skip 保護(定期実行の受け口。設定 nudge が無いあいだの以前の除外)"
eq "nudge: 設定 nudge が無くても宛先を明示すれば対象" "$(TB_NUDGE=$TMP/no-such-nudge.conf $TB nudge w1 --dry-run | grep "^w1${TAB}" | cut -f3)" "would"
: >"$TB_PROTECT"
# 設定 nudge の ignore(agent 名の glob と @役割)。宛先を明示すると適用しない
printf '# 注釈\nignore w1   # 名前\nignore @no-such-role\n' >"$TB_NUDGE"
eq "nudge: ignore(名前)→ 走査しない" "$($TB nudge --dry-run | grep -c '^w1')" "0"
eq "nudge: 宛先を明示すると ignore を適用しない" "$($TB nudge w1 --dry-run | grep "^w1${TAB}" | cut -f3)" "would"
eq "nudge: 宛先の glob は一致したものだけ" "$($TB nudge 'w[12]' --dry-run | cut -f1 | tr '\n' ' ')" "w1 w2 "
printf 'ignore w?\n' >"$TB_NUDGE"
eq "nudge: ignore(glob)" "$($TB nudge --dry-run | grep -c '^w[0-9]')" "0"
printf 'ignore a b\n' >"$TB_NUDGE"
eq "nudge: 不正な ignore の行はエラー" "$(rc $TB nudge --dry-run)" "1"
: >"$TB_NUDGE"
# 不正な行は tb config set nudge の時点で断る(実行のたびに失敗する設定を書かない)
eq "config set nudge: 不正な行は書かない" "$(printf 'ignore a b\n' | rc $TB config set nudge) $(wc -c <"$TB_NUDGE" | tr -d ' ')" "1 0"
eq "config set nudge: ignore 以外の語も断る" "$(printf 'skip w1\n' | rc $TB config set nudge)" "1"
# 明示した宛先に当たる agent がいなければ、黙って終わらずに skip の行を出す
eq "nudge: 当たらない宛先 → skip の行" "$($TB nudge no-such-agent --dry-run | cut -f1-3 | tr '\t' ' ')" "no-such-agent - skip"
# 先行チケット待ち
tb0=$($TB add -p ng -t '先行'); $TB link "$tb0" blocks "$t1" >/dev/null
eq "nudge: 先行チケット待ち → skip" "$($TB nudge --dry-run | grep "^w1${TAB}" | grep -c '先行チケット')" "1"
$TB unlink "$tb0" blocks "$t1" >/dev/null
# delivery の規則: hold / block の宛先には催促しない(指揮役 → その agent の task で決まる)
export TB_DELIVERY=$TMP/nudge-delivery.conf
printf 'coord -> w1 : task=hold\ncoord -> *\n' >"$TB_DELIVERY"
eq "nudge: delivery hold → skip" "$($TB nudge --dry-run | grep "^w1${TAB}" | grep -c 'delivery が hold')" "1"
printf '! coord -> w1\ncoord -> *\n' >"$TB_DELIVERY"
eq "nudge: delivery block → skip" "$($TB nudge --dry-run | grep "^w1${TAB}" | grep -c 'delivery が block')" "1"
unset TB_DELIVERY
# 届かなかった催促は後で届けない
eq "nudge: 送信失敗 → 取り下げ(queued に残さない)" "$(FAKE_PROMPT_RC=1 $TB nudge | grep "^w1${TAB}" | grep -c '取り下げ') $(nsql "SELECT status FROM outbox WHERE target='w1' ORDER BY id DESC LIMIT 1;")" "1 denied"
# 送り手と同じ agent(既定の宛先が本人)は送らない。ほかの指揮役は対象
tc=$(claim coord '指揮役の仕事'); rest sc 30
eq "nudge: 指揮役も既定で対象(送り手が別なら would)" "$($TB nudge --dry-run | grep "^coord${TAB}" | cut -f3 | tr '\t' ' ')" "would"
eq "nudge: 設定 nudge が無い → 指揮役は以前どおり skip" "$(TB_NUDGE=$TMP/no-such-nudge.conf $TB nudge --dry-run | grep "^coord${TAB}" | cut -f3 | tr '\t' ' ')" "skip"
eq "nudge: 設定 nudge が無くても、空の設定を置けば指揮役も対象" "$(: >"$TMP/empty-nudge.conf"; TB_NUDGE=$TMP/empty-nudge.conf $TB nudge --dry-run | grep "^coord${TAB}" | cut -f3)" "would"
printf 'ng=conductor\n' >"$TB_CONFIG"
t7=$(claim conductor '既定の宛先自身の仕事'); rest sconductor 30
export FAKE_AGENTS="${FAKE_AGENTS%]},$(ag wX:p1 conductor idle sconductor)]"
eq "nudge: 送り手が本人と同じ → skip" "$($TB nudge --dry-run | grep "^conductor${TAB}" | cut -f3-4 | tr '\t' ' ')" "skip 送り手が本人と同じ"
printf 'ng=coord\n' >"$TB_CONFIG"
eq "nudge: --json" "$($TB nudge --dry-run --json | jq -r 'select(.agent=="w2") | .result')" "skip"
eq "nudge: tb list の列は変えない" "$($TB list | grep "^$t1${TAB}" | cut -f1-3 | tr '\t' ' ')" "$t1 in_progress w1"
# --pool: 担当なし todo を優先度順に 1 件(team ラベルは領域の役割 → 無ければ名前の接尾辞を除く)。epic・未完了の子を持つ親は除く
export FAKE_AGENTS="[$(ag wC:p1 coord idle sc),$(ag wm:p1 team-a-lead idle sm),$(ag wn:p1 solo idle sn)]"
rest sm 30; rest sn 30
pl=$($TB add -p ng -t '低' -l team:team-a --priority low); ph1=$($TB add -p ng -t '高・古い' -l team:team-a --priority high); sleep 1
ph2=$($TB add -p ng -t '高・新しい' -l team:team-a --priority high)
po=$($TB add -p ng -t '別 team' -l team:other --priority highest)
pe=$($TB add -p ng -t '親 epic' --type epic -l team:team-a --priority highest)
pp=$($TB add -p ng -t '子を持つ親' -l team:team-a --priority highest); $TB add -p ng -t '未完了の子' --parent "$pp" >/dev/null
out=$($TB nudge 'team-a-lead' --pool --dry-run)
eq "nudge --pool: 最も高い優先度の古い方を名指し" "$(row team-a-lead)" "$ph1 would"
# 役割 domain-<x> から team を決める。同じ回では、同じ候補を 2 人に案内しない
export TB_ROLES=$TMP/nudge-roles; mkdir -p "$TB_ROLES"; printf 'team-a 担当\n' >"$TB_ROLES/domain-team-a.md"
printf 'team-a-2 = domain-team-a\n' >"$TB_ROLES/map.conf"
export FAKE_AGENTS="${FAKE_AGENTS%]},$(ag wq:p1 team-a-2 idle sq)]"; rest sq 30
out=$($TB nudge 'team-a-*' --pool --dry-run)
eq "nudge --pool: 役割 domain-<x> から team を決め、同じ回では別の候補" "$(row team-a-2) / $(row team-a-lead)" "$ph1 would / $ph2 would"
# ignore @役割 は役割の glob で外す
printf 'ignore @domain-*\n' >"$TB_NUDGE"
eq "nudge: ignore @役割の glob → 走査しない" "$($TB nudge --pool --dry-run | grep -c '^team-a-2')" "0"
: >"$TB_NUDGE"
# 自分が担当の todo(割り当て)があれば、--pool で別の仕事を取りに行かせない
pa=$($TB add -p ng -t 'team-a-2 に割り当て' --owner team-a-2)
eq "nudge --pool: 担当の todo がある → skip" "$($TB nudge team-a-2 --pool --dry-run | grep "^team-a-2${TAB}" | cut -f3-4 | tr '\t' ' ')" "skip 担当のチケットがある(${pa}。--pool では取りに行かせない)"
$TB cancel "$pa" -m 'テストの片付け' >/dev/null 2>&1 || nsql "UPDATE tasks SET status='cancelled' WHERE id='$pa';"
eq "nudge --pool: 宛先なし・--pool なしの既定では取りに行かせない" "$($TB nudge --dry-run | grep -c '^team-a-lead')" "0"
eq "nudge --pool: team を決められない agent は skip" "$($TB nudge solo --pool --dry-run | grep "^solo${TAB}" | cut -f3-4 | tr '\t' ' ')" "skip team を決められない(--pool)"
# 本人から team が決まらなければ、組織図(設定 org)で上司をたどって決める(名前の決まりに頼らない)
printf 'solo -> team-a-lead\n' >"$TMP/nudge-org.conf"
eq "nudge --pool: 組織図の上司から team を決める" "$(out=$(TB_ORG=$TMP/nudge-org.conf $TB nudge solo --pool --dry-run); row solo)" "$ph1 would"
printf 'solo -> boss\nboss -> team-a-lead\n' >"$TMP/nudge-org.conf"
eq "nudge --pool: 組織図を上へたどる(上司の上司)" "$(out=$(TB_ORG=$TMP/nudge-org.conf $TB nudge solo --pool --dry-run); row solo)" "$ph1 would"
printf 'solo -> boss\n' >"$TMP/nudge-org.conf"
eq "nudge --pool: 上司のどれからも決まらなければ skip" "$(TB_ORG=$TMP/nudge-org.conf $TB nudge solo --pool --dry-run | grep "^solo${TAB}" | cut -f3-4 | tr '\t' ' ')" "skip team を決められない(--pool)"
$TB nudge 'team-a-lead' --pool >/dev/null
eq "nudge --pool: 取りまとめ役への送信は task・文面に候補" "$(nsql "SELECT kind||' '||task_id||' '||status FROM outbox WHERE target='team-a-lead';")" "task $ph1 sent"
eq "nudge --pool: 文面" "$(grep '^prompt team-a-lead' "$FAKE_LOG" | grep -c '優先度の高い順(highest→lowest、同順位は古い順)に 1 件 claim')" "1"
eq "nudge --pool: 文面の claim に本人の名前と session" "$(grep '^prompt team-a-lead' "$FAKE_LOG" | grep -c "tb claim $ph1 --owner team-a-lead --session sm")" "1"
eq "nudge --pool: 同じ休みでは二度送らない" "$($TB nudge 'team-a-lead' --pool --dry-run | grep "^team-a-lead${TAB}" | grep -c '催促済み')" "1"
# 握っているのが親(epic)だけなら催促しない(--pool なし)
pg=$($TB add -p ng -t '親だけ握る' --type epic --owner team-a-lead); $TB update "$pg" --status in_progress >/dev/null
nsql "DELETE FROM outbox WHERE target='team-a-lead';"; rest sm 30
eq "nudge: 親(epic)だけ握っている → 催促しない" "$($TB nudge 'team-a-lead' --dry-run | grep "^team-a-lead${TAB}" | cut -f3-4 | tr '\t' ' ')" "skip 握っているのは親(epic など)だけ"
# digest: 複数チケットを握っている agent を指揮役に知らせる(同じ組み合わせは一度だけ)
eq "digest: 複数チケットの観測" "$($TB digest --dry-run | grep -c "w3 観測 / 複数チケット] in_progress のチケットを 2 枚握っている($t3 $t4)")" "1"
$TB digest >/dev/null
eq "digest: 同じ組み合わせは二度出さない" "$($TB digest --dry-run | grep -c '複数チケット')" "0"
# 親(epic)と子を 1 枚ずつ握っているのは「複数チケット」に数えない(nudge と同じ数え方)
pc=$($TB add -p ng -t '親の下の子' --parent "$pg" --owner team-a-lead); $TB update "$pc" --status in_progress >/dev/null
eq "digest: 親と子は複数チケットに数えない" "$($TB digest --dry-run | grep -c 'team-a-lead 観測 / 複数チケット')" "0"
# 設定 messages で nudge.pool を差し替えられる({agent} {session} も使える)
eq "config set messages: nudge.pool を受け付ける" "$(printf 'nudge.pool = {task} {title} {agent} {session}\n' | rc $TB config set messages)" "0"
$TB config unset messages >/dev/null
export TB_CONFIG=${_ng_saved[0]} TB_ROUTES=${_ng_saved[1]} TB_APPROVALS=${_ng_saved[2]} TB_DELIVERY=${_ng_saved[3]} TB_PROTECT=${_ng_saved[4]} HERDR_PANE_ID=${_ng_saved[5]}
for _v in TB_CONFIG TB_ROUTES TB_APPROVALS TB_DELIVERY TB_PROTECT HERDR_PANE_ID; do [[ -n ${!_v} ]] || unset "$_v"; done
if [[ -n $_ng_nudge_set ]]; then export TB_NUDGE=$_ng_nudge; else unset TB_NUDGE; fi
if [[ -n $_ng_roles_set ]]; then export TB_ROLES=$_ng_roles; else unset TB_ROLES; fi
unset FAKE_AGENTS _ng_saved _v _ng_nudge _ng_nudge_set _ng_roles _ng_roles_set
