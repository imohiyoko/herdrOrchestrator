# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb message(send / flush / outbox / inbox / read)の出力・文面・終了コードの細かいところ(期待値は消した bash 版の出力から作った。今はこれが約束)
_mc_vars="HERDR_PANE_ID TB_DB TB_POLICY_A TB_ROLES TB_DELIVERY TB_ROUTES TB_CONFIG TB_LIMITS FAKE_AGENTS FAKE_STATUS FAKE_LIST_FAIL FAKE_PROMPT_RC"
declare -A _mc_saved=()
for _v in $_mc_vars; do if [[ -v $_v ]]; then _mc_saved[$_v]=${!_v}; fi; done
unset HERDR_PANE_ID FAKE_STATUS FAKE_LIST_FAIL FAKE_PROMPT_RC TB_ROUTES TB_LIMITS
export TB_DB=$TMP/mcontract.db TB_POLICY_A=$TMP/mc-appr.conf TB_ROLES=$TMP/mc-roles TB_DELIVERY=$TMP/mc-delivery.conf TB_CONFIG=$TMP/mc-projects.conf
rm -f "$TB_DELIVERY" "$TB_CONFIG"
printf '* send:* * = allow\n' >"$TB_POLICY_A"
mkdir -p "$TB_ROLES"; echo "ROLE-worker" >"$TB_ROLES/worker.md"; printf 'w* = worker\n' >"$TB_ROLES/map.conf"
mc_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(mc_ conductor),$(mc_ w1),$(mc_ w2),$(mc_ w3)]"
mcsql() { sqlite3 "$TB_DB" "$@"; }
mcerr() { "$@" 2>&1; echo "rc=$?"; }
mcts() { sed -E 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z/TS/g'; }
as() { HERDR_PANE_ID=w$1:p1 "${@:2}"; }
$TB agents >/dev/null 2>&1
$TB add -p mc -t 'first' >/dev/null

# ---- 使い方と引数の誤り
eq "message: no subcommand → usage on stdout, rc 2" "$(mcerr $TB message)" $'tb message send <宛先 agent 名> --text T [--task T-..] [--kind task|info|answer] [--reply-to O-<n>] [--json]\ntb message flush                     積まれた送信(outbox の queued)を届ける\ntb message outbox [--status pending|queued|sent|denied] [--json]\ntb message inbox [--unread] [--json]  自分宛てに保留された送信の一覧\ntb message read I-<n> [--json]        1 件を全文で読み、既読にする\n(古い名前 tb send / tb flush / tb outbox / tb inbox / tb inbox-read も同じに使える)\nrc=2'
eq "message help" "$(mcerr $TB message help)" $'tb message send <宛先 agent 名> --text T [--task T-..] [--kind task|info|answer] [--reply-to O-<n>] [--json]\ntb message flush                     積まれた送信(outbox の queued)を届ける\ntb message outbox [--status pending|queued|sent|denied] [--json]\ntb message inbox [--unread] [--json]  自分宛てに保留された送信の一覧\ntb message read I-<n> [--json]        1 件を全文で読み、既読にする\n(古い名前 tb send / tb flush / tb outbox / tb inbox / tb inbox-read も同じに使える)\nrc=0'
eq "message: unknown subcommand" "$(mcerr $TB message bogus)" $'tb: message: 不明なサブコマンド bogus(send / flush / outbox / inbox / read)\n(tb help で使い方)\nrc=2'
eq "message: empty subcommand" "$(mcerr $TB message '')" $'tb: message: 不明なサブコマンド (send / flush / outbox / inbox / read)\n(tb help で使い方)\nrc=2'
eq "error: send" "$(mcerr $TB message send)" $'tb: send <宛先 agent 名> --text T [--task T-..] [--kind task|info|answer] [--reply-to O-<n>] [--json]\n(tb help で使い方)\nrc=2'
eq "error: send w1 --text" "$(mcerr $TB send w1 --text)" $'tb: --text に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: send w1 --kind" "$(mcerr $TB send w1 --kind)" $'tb: --kind に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: send w1 --task" "$(mcerr $TB send w1 --task)" $'tb: --task に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: send w1 --reply-to" "$(mcerr $TB send w1 --reply-to)" $'tb: --reply-to に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: send w1 --from x" "$(mcerr $TB send w1 --from x)" $'tb: send: 送信者は herdr から自動で決まります。--from は指定できません\n(tb help で使い方)\nrc=2'
eq "error: send w1 --from-pane x" "$(mcerr $TB send w1 --from-pane x)" $'tb: send: 送信者は herdr から自動で決まります。--from-pane は指定できません\n(tb help で使い方)\nrc=2'
eq "error: send w1 --session x" "$(mcerr $TB send w1 --session x)" $'tb: send: 送信者は herdr から自動で決まります。--session は指定できません\n(tb help で使い方)\nrc=2'
eq "error: send w1 --bogus" "$(mcerr $TB send w1 --bogus)" $'tb: send: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "error: send --text x (target is --text)" "$(mcerr $TB send --text x)" $'tb: send: 不明な引数 x\n(tb help で使い方)\nrc=2'
eq "error: send 'a b' --text x" "$(mcerr $TB send 'a b' --text x)" $'tb: send: 宛先は agent 名(英数字と . _ -): a b\n(tb help で使い方)\nrc=2'
eq "error: send w1 --text x --kind bogus" "$(mcerr $TB send w1 --text x --kind bogus)" $'tb: send: --kind は task info answer のどれか\n(tb help で使い方)\nrc=2'
eq "error: send w1 --text '  '" "$(mcerr $TB send w1 --text '  ')" $'tb: send: --text が必要です\n(tb help で使い方)\nrc=2'
eq "error: send w1 (no text)" "$(mcerr $TB send w1)" $'tb: send: --text が必要です\n(tb help で使い方)\nrc=2'
eq "error: send w1 --text x --reply-to 7" "$(mcerr $TB send w1 --text x --reply-to 7)" $'tb: send: --reply-to は O-<n>(返信する元の送信): 7\n(tb help で使い方)\nrc=2'
eq "error: send w1 --text x --reply-to O-1234567890123456789" "$(mcerr $TB send w1 --text x --reply-to O-1234567890123456789)" $'tb: send: --reply-to は O-<n>(返信する元の送信): O-1234567890123456789\n(tb help で使い方)\nrc=2'
eq "error: send w1 --text x --task T-1" "$(mcerr $TB send w1 --text x --task T-1)" $'tb: タスク ID は T-<project>-NNN 形式: T-1\n(tb help で使い方)\nrc=2'
eq "error: send w1 --text x --task T-mc-999" "$(mcerr $TB send w1 --text x --task T-mc-999)" $'tb: タスクが見つかりません: T-mc-999\nrc=1'
eq "error: send without a pane → rc 3" "$(mcerr $TB send w1 --text x)" $'tb: [identity.no-pane] HERDR_PANE_ID がありません。報告者を特定できないため保存しません\nrc=3'
eq "error: send to self" "$(mcerr as conductor $TB send conductor --text x)" $'tb: send: 自分自身には送れません\n(tb help で使い方)\nrc=2'
eq "error: send to an agent not in herdr" "$(mcerr as conductor $TB send ghost --text x)" $'tb: send: 宛先の agent が herdr にいません: ghost(保存しません)\nrc=1'
eq "error: send when herdr list fails" "$(FAKE_LIST_FAIL=1 mcerr as conductor $TB send w1 --text x)" $'tb: [identity.herdr-failed] herdr agent list に失敗。報告者を特定できないため保存しません\nrc=3'
eq "error: nothing saved for the errors" "$(mcsql "SELECT COUNT(*) FROM outbox;")" $'0'

# ---- 送る
eq "send: sent" "$(mcerr as conductor $TB message send w1 --text $'hello\nworld' --task T-mc-001)" $'O-1 sent to w1\nrc=0'
eq "send: reason and row" "$(mcsql "SELECT status, from_name, from_pane, from_session, target, kind, task_id, text, reason, reply_to IS NULL, reply_check IS NULL FROM outbox WHERE id=1;")" $'sent|conductor|wconductor:p1|s-conductor|w1|task|T-mc-001|hello\nworld|default: conductor conductor / 認可: policy: * send:* * = allow / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)]|1|1'
eq "send --json" "$(as conductor $TB send w2 --kind info --text j --json | mcts)" $'{"id":"O-2","from_name":"conductor","from_pane":"wconductor:p1","from_session":"s-conductor","target":"w2","kind":"info","task_id":null,"text":"j","status":"sent","reason":"default: conductor conductor / 認可: policy: * send:* * = allow / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)]","sent_at":"TS","created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null}'
eq "send: busy → queued rc 10" "$(FAKE_STATUS=working mcerr as conductor $TB send w1 --text busy)" $'tb: 宛先 \'w1\' が working のため送りません。未送信(pending に残す): O-3\nO-3 queued\nrc=10'
eq "send: prompt fails → queued rc 11" "$(FAKE_PROMPT_RC=1 mcerr as conductor $TB send w3 --text fail)" $'tb: 送信に失敗しました。未送信(pending に残す): O-4\n{"error":{"code":"x"}}\nO-4 queued\nrc=11'
eq "send: busy --json" "$(FAKE_STATUS=working as conductor $TB send w1 --text busy2 --json | mcts)" $'{"id":"O-5","from_name":"conductor","from_pane":"wconductor:p1","from_session":"s-conductor","target":"w1","kind":"task","task_id":null,"text":"busy2","status":"queued","reason":"default: conductor conductor / 認可: policy: * send:* * = allow / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)]","sent_at":null,"created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null}'
printf '* send:answer w2 = ask-any\n* send:* * = allow\n' >"$TB_POLICY_A"
eq "send: pending approval rc 20" "$(mcerr as conductor $TB send w2 --kind answer --text ask)" $'tb: 承認待ちです(認可: policy: * send:answer w2 = ask-any)。人(か approve: を許された agent)が tb approve O-6 で届けます\nO-6 pending-approval\nrc=20'
eq "send: pending --json" "$(as conductor $TB send w2 --kind answer --text ask2 --json 2>/dev/null | mcts)" $'{"id":"O-7","from_name":"conductor","from_pane":"wconductor:p1","from_session":"s-conductor","target":"w2","kind":"answer","task_id":null,"text":"ask2","status":"pending","reason":"default: conductor conductor / 認可: policy: * send:answer w2 = ask-any / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)]","sent_at":null,"created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null}'
printf '* send:* * = allow\n' >"$TB_POLICY_A"
eq "send: default deny for a non-conductor (no delivery)" "$(mcerr as w1 $TB send w2 --text hi)" $'tb: 送信を拒否しました(default: w1 は指揮官ではない(worker からの報告は tb comment、依頼はチケットを使う)): O-8\nO-8 denied\nrc=4'
eq "send: denied --json" "$(as w1 $TB send w2 --text hi --json 2>/dev/null | mcts)" $'{"id":"O-9","from_name":"w1","from_pane":"ww1:p1","from_session":"s-w1","target":"w2","kind":"task","task_id":null,"text":"hi","status":"denied","reason":"default: w1 は指揮官ではない(worker からの報告は tb comment、依頼はチケットを使う)","sent_at":null,"created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null}'
printf 'conductor -> w2 : info=hold task=block\nw1 -> conductor : *=now\n' >"$TB_DELIVERY"
eq "send: held" "$(mcerr as conductor $TB send w2 --kind info --text $'held\tone')" $'O-10 held for w2 (I-1)\nrc=0'
eq "send: held --json" "$(as conductor $TB send w2 --kind info --text held2 --json | mcts)" $'{"id":"O-11","from_name":"conductor","from_pane":"wconductor:p1","from_session":"s-conductor","target":"w2","kind":"info","task_id":null,"text":"held2","status":"sent","reason":"delivery: conductor -> w2 : info=hold / 認可: policy: * send:* * = allow / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)] / delivery: conductor -> w2 : info=hold → inbox I-2","sent_at":"TS","created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null,"inbox":"I-2"}'
eq "send: blocked" "$(mcerr as conductor $TB send w2 --text blocked)" $'tb: [tb: blocked / O-12] w2 への送信 O-12(task)は w2 の受信設定で止められました(delivery: conductor -> w2 : task=block)。w2 には届けていません。依頼や相談はチケットにしてください(tb add -p <project> -t <題> --desc <内容>。進み具合は tb comment で返ってきます)\nO-12 blocked by w2\nrc=4'
eq "send: blocked --json" "$(as conductor $TB send w2 --text blocked2 --json 2>/dev/null | mcts)" $'{"id":"O-13","from_name":"conductor","from_pane":"wconductor:p1","from_session":"s-conductor","target":"w2","kind":"task","task_id":null,"text":"blocked2","status":"denied","reason":"delivery: conductor -> w2 : task=block / delivery: conductor -> w2 : task=block → blocked","sent_at":null,"created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null,"blocked":true}'
eq "send: blocked row" "$(mcsql "SELECT status, reason FROM outbox WHERE text='blocked';")" $'denied|delivery: conductor -> w2 : task=block / delivery: conductor -> w2 : task=block → blocked'

# ---- 返信の主張
eq "reply-to: not found" "$(mcerr as w1 $TB send conductor --kind answer --text r --reply-to O-999)" $'tb: --reply-to O-999 を返信と認めませんでした(元の送信 O-999 が見つからない)。返信ではない送信として通常の規則(delivery)で扱います: O-14\nO-14 sent to conductor\nrc=0'
eq "reply-to: not to this sender" "$(mcerr as w1 $TB send conductor --kind answer --text r --reply-to O-2)" $'tb: --reply-to O-2 を返信と認めませんでした(O-2 は conductor から w1 への送信ではない)。返信ではない送信として通常の規則(delivery)で扱います: O-15\nO-15 sent to conductor\nrc=0'
eq "reply-to: not delivered yet" "$(mcerr as w1 $TB send conductor --kind answer --text r --reply-to O-3)" $'tb: --reply-to O-3 を返信と認めませんでした(O-3 はまだ届いていない(queued))。返信ではない送信として通常の規則(delivery)で扱います: O-16\nO-16 sent to conductor\nrc=0'
eq "reply-to: ok (leading zeros)" "$(mcerr as w1 $TB send conductor --kind answer --text r --reply-to O-001)" $'O-17 sent to conductor\nrc=0'
eq "reply-to: already answered" "$(mcerr as w1 $TB send conductor --kind answer --text r --reply-to O-1)" $'tb: --reply-to O-1 を返信と認めませんでした(O-1 には返信済み(O-17))。返信ではない送信として通常の規則(delivery)で扱います: O-18\nO-18 sent to conductor\nrc=0'
eq "reply-to: rows" "$(mcsql "SELECT id, reply_to, reply_check FROM outbox WHERE reply_to IS NOT NULL ORDER BY id;")" $'14|999|元の送信 O-999 が見つからない\n15|2|O-2 は conductor から w1 への送信ではない\n16|3|O-3 はまだ届いていない(queued)\n17|1|ok\n18|1|O-1 には返信済み(O-17)'

# ---- outbox
eq "outbox" "$(mcerr $TB message outbox | mcts)" $'O-18\tsent\tw1\tconductor\tanswer\t-\tTS\tr\nO-17\tsent\tw1\tconductor\tanswer\t-\tTS\tr\nO-16\tsent\tw1\tconductor\tanswer\t-\tTS\tr\nO-15\tsent\tw1\tconductor\tanswer\t-\tTS\tr\nO-14\tsent\tw1\tconductor\tanswer\t-\tTS\tr\nO-13\tdenied\tconductor\tw2\ttask\t-\tTS\tblocked2\nO-12\tdenied\tconductor\tw2\ttask\t-\tTS\tblocked\nO-11\tsent\tconductor\tw2\tinfo\t-\tTS\theld2\nO-10\tsent\tconductor\tw2\tinfo\t-\tTS\theld one\nO-9\tdenied\tw1\tw2\ttask\t-\tTS\thi\nO-8\tdenied\tw1\tw2\ttask\t-\tTS\thi\nO-7\tpending\tconductor\tw2\tanswer\t-\tTS\task2\nO-6\tpending\tconductor\tw2\tanswer\t-\tTS\task\nO-5\tqueued\tconductor\tw1\ttask\t-\tTS\tbusy2\nO-4\tqueued\tconductor\tw3\ttask\t-\tTS\tfail\nO-3\tqueued\tconductor\tw1\ttask\t-\tTS\tbusy\nO-2\tsent\tconductor\tw2\tinfo\t-\tTS\tj\nO-1\tsent\tconductor\tw1\ttask\tT-mc-001\tTS\thello world\nrc=0'
eq "outbox --status queued" "$(mcerr $TB outbox --status queued | mcts)" $'O-5\tqueued\tconductor\tw1\ttask\t-\tTS\tbusy2\nO-4\tqueued\tconductor\tw3\ttask\t-\tTS\tfail\nO-3\tqueued\tconductor\tw1\ttask\t-\tTS\tbusy\nrc=0'
eq "outbox --status pending --json" "$($TB outbox --status pending --json | mcts)" $'{"id":"O-7","from_name":"conductor","from_pane":"wconductor:p1","from_session":"s-conductor","target":"w2","kind":"answer","task_id":null,"text":"ask2","status":"pending","reason":"default: conductor conductor / 認可: policy: * send:answer w2 = ask-any / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)]","sent_at":null,"created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null}\n{"id":"O-6","from_name":"conductor","from_pane":"wconductor:p1","from_session":"s-conductor","target":"w2","kind":"answer","task_id":null,"text":"ask","status":"pending","reason":"default: conductor conductor / 認可: policy: * send:answer w2 = ask-any / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)]","sent_at":null,"created_at":"TS","herdr_session":"default","reply_to":null,"reply_check":null}'
eq "error: outbox --status bogus" "$(mcerr $TB outbox --status bogus)" $'tb: outbox: --status は pending|queued|sent|denied\n(tb help で使い方)\nrc=2'
eq "error: outbox --status" "$(mcerr $TB outbox --status)" $'tb: --status に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: outbox --bogus" "$(mcerr $TB outbox --bogus)" $'tb: outbox: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'

# ---- inbox / read
eq "inbox" "$(mcerr as w2 $TB message inbox | mcts)" $'I-1\tunread\tconductor\tinfo\t-\tTS\theld one\nI-2\tunread\tconductor\tinfo\t-\tTS\theld2\nrc=0'
eq "inbox --unread --json" "$(as w2 $TB inbox --unread --json | mcts)" $'{"id":"I-1","herdr_session":"default","outbox_id":"O-10","recipient":"w2","from_name":"conductor","kind":"info","task_id":null,"text":"held\\tone","rule":"delivery: conductor -> w2 : info=hold","created_at":"TS","read_at":null}\n{"id":"I-2","herdr_session":"default","outbox_id":"O-11","recipient":"w2","from_name":"conductor","kind":"info","task_id":null,"text":"held2","rule":"delivery: conductor -> w2 : info=hold","created_at":"TS","read_at":null}'
eq "inbox: someone else's" "$(mcerr as w1 $TB inbox)" $'rc=0'
eq "error: inbox --name w2" "$(mcerr as w2 $TB inbox --name w2)" $'tb: inbox: 読み手は herdr から自動で決まります。--name は指定できません\n(tb help で使い方)\nrc=2'
eq "error: inbox --bogus" "$(mcerr as w2 $TB inbox --bogus)" $'tb: inbox [--unread] [--json]\n(tb help で使い方)\nrc=2'
eq "error: inbox without a pane" "$(mcerr $TB inbox)" $'tb: [identity.no-pane] HERDR_PANE_ID がありません。報告者を特定できないため保存しません\nrc=3'
eq "read" "$(mcerr as w2 $TB message read I-1 | mcts)" $'I-1  from: conductor / info / - / TS  (O-10)\n\nheld\tone\nrc=0'
eq "read: marked read" "$(as w2 $TB inbox | cut -f1,2 | tr '\t\n' ' ,')" $'I-1 read,I-2 unread,'
eq "read --json (leading zeros)" "$(as w2 $TB inbox-read I-02 --json | mcts)" $'{"id":"I-2","herdr_session":"default","outbox_id":"O-11","recipient":"w2","from_name":"conductor","kind":"info","task_id":null,"text":"held2","rule":"delivery: conductor -> w2 : info=hold","created_at":"TS","read_at":null}'
eq "read: extra args after --json are ignored" "$(as w2 $TB inbox-read I-2 --json extra | jq -r .id)" $'I-2'
eq "error: read" "$(mcerr as w2 $TB message read)" $'tb: inbox-read I-<n> [--json]\n(tb help で使い方)\nrc=2'
eq "error: read 2" "$(mcerr as w2 $TB message read 2)" $'tb: inbox-read I-<n> [--json]\n(tb help で使い方)\nrc=2'
eq "error: read I-1 --bogus" "$(mcerr as w2 $TB inbox-read I-1 --bogus)" $'tb: inbox-read I-<n> [--json]\n(tb help で使い方)\nrc=2'
eq "error: read someone else's" "$(mcerr as w1 $TB inbox-read I-1)" $'tb: inbox に見つかりません: I-1\nrc=1'
eq "error: read a huge id" "$(mcerr as w2 $TB inbox-read I-99999999999999999999)" $'tb: inbox に見つかりません: I-99999999999999999999\nrc=1'
eq "error: read without a pane" "$(mcerr $TB inbox-read I-1)" $'tb: [identity.no-pane] HERDR_PANE_ID がありません。報告者を特定できないため保存しません\nrc=3'

# ---- flush
rm -f "$TB_DELIVERY"
eq "error: flush x" "$(mcerr $TB message flush x)" $'tb: flush(引数なし)\n(tb help で使い方)\nrc=2'
eq "flush: sends the queued in order (busy w1 → later ones wait)" "$(FAKE_STATUS=working mcerr $TB flush)" $'tb: 宛先 \'w1\' が working のため送りません。未送信(pending に残す): O-3\ntb: 宛先 \'w3\' が working のため送りません。未送信(pending に残す): O-4\nrc=10'
eq "flush: w3 stopped (not in herdr) → kept queued" "$(FAKE_AGENTS="[$(mc_ conductor),$(mc_ w1),$(mc_ w2)]" mcerr $TB flush)" $'tb: 宛先 \'w3\' は停止中のため送りません(queued のまま。tb agent-revive w3 のあとの flush で届きます)\nrc=0'
eq "flush: ledger after sync" "$(mcsql "SELECT name, state, stopped_at IS NOT NULL FROM agents ORDER BY name;")" $'conductor|running|0\nw1|running|0\nw2|running|0\nw3|stopped|1'
eq "flush: queue after" "$(mcsql "SELECT id, status FROM outbox WHERE status='queued' OR id IN (4,5,6) ORDER BY id;")" $'4|queued\n5|sent\n6|pending'
eq "flush: herdr list fails → still tries" "$(FAKE_LIST_FAIL=1 mcerr $TB flush)" $'rc=0'
eq "flush: nothing queued" "$(mcerr $TB flush)" $'rc=0'
# 同期: kind が変わった個体・cwd・数値の pane・オブジェクトでない要素・規則に合わない名前
mcsql "UPDATE agents SET model='opus', cwd='/old' WHERE name='w1';"
as conductor $TB send w1 --text again >/dev/null 2>&1
mcsql "UPDATE outbox SET status='queued' WHERE text='again';"
eq "flush: sync with kinds and odd entries" "$(FAKE_AGENTS='[{"pane_id":7,"name":"w1","agent":"codex","foreground_cwd":"/fg","agent_session":{"value":"s-new"}},{"pane_id":"wx:p1","name":"Bad_Name","agent":"claude"},"junk",{"name":"w2","agent":"bogus","cwd":"/c2"},{"name":"","pane_id":"z"}]' mcerr $TB flush)" $'rc=0'
eq "flush: ledger rows after sync" "$(mcsql "SELECT name, kind, COALESCE(model,'-'), COALESCE(pane_id,'-'), COALESCE(session_id,'-'), COALESCE(cwd,'-'), state FROM agents ORDER BY name;")" $'conductor|claude|-|wconductor:p1|s-conductor|-|stopped\nw1|codex|-|7|s-new|/fg|running\nw2|claude|-|-|s-w2|/c2|running\nw3|claude|-|ww3:p1|s-w3|-|stopped'

eq "error: send --kind with two words is a usage error" "$(mcerr as conductor $TB send w1 --text two --kind 'task info')" $'tb: send: --kind は task info answer のどれか\n(tb help で使い方)\nrc=2'
for _v in $_mc_vars; do if [[ -v _mc_saved[$_v] ]]; then export "$_v=${_mc_saved[$_v]}"; else unset "$_v"; fi; done
unset _mc_saved _mc_vars
