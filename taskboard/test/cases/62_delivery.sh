# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- delivery(今届ける / 保留する)と inbox(tb inbox / tb inbox-read)
_dl_saved=("${TB_CONFIG-}" "${TB_ROUTES-}" "${TB_APPROVALS-}")
export TB_DB=$TMP/delivery.db TB_CONFIG=$TMP/delivery-projects.conf TB_ROUTES=$TMP/delivery-routes.conf TB_APPROVALS=$TMP/delivery-approvals.conf
unset TB_DELIVERY
printf 'dl=coordinator\n' >"$TB_CONFIG"
printf 'coordinator -> *\n@worker -> *\nassistant -> *\n' >"$TB_ROUTES"
printf '* = allow\n' >"$TB_APPROVALS"
dsql() { sqlite3 "$TB_DB" "$@"; }
dag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(dag coordinator),$(dag assistant),$(dag worker1),$(dag worker2),$(dag other)]"
$TB agents >/dev/null 2>&1
dsql "UPDATE agents SET role='worker' WHERE name IN ('worker1','worker2');"
: >"$FAKE_LOG"
C() { HERDR_PANE_ID=wcoordinator:p1 $TB "$@"; }
A() { HERDR_PANE_ID=wassistant:p1 $TB "$@"; }
W() { HERDR_PANE_ID=wworker1:p1 $TB "$@"; }

# キーが無ければ今までどおり(全部その場で届く)
n=$(prompts)
eq "delivery: unset key → delivered now as before" "$(C send assistant --kind info --text hello; echo "rc=$?")" "$(printf 'O-1 sent to assistant\nrc=0')"
eq "delivery: unset key → typed into the input, nothing in inbox" "$(($(prompts) - n)) $(dsql "SELECT COUNT(*) FROM inbox;")" "1 0"

# 設定: 種別ごと・役割の glob
export TB_DELIVERY=$TMP/delivery.conf
printf '# rules\ncoordinator -> assistant : info=hold, answer=hold task=now\n@worker -> assistant : *=hold\n' >"$TB_DELIVERY"
n=$(prompts)
eq "delivery: info=hold → held, success" "$(C send assistant --kind info --text $'status\nline2'; echo "rc=$?")" "$(printf 'O-2 held for assistant (I-1)\nrc=0')"
eq "delivery: held message is not typed into any input" "$(prompts)" "$n"
eq "delivery: outbox row is sent with the inbox id in reason" "$(dsql "SELECT status, reason LIKE '%delivery: coordinator -> assistant : info=hold → inbox I-1' FROM outbox WHERE id=2;")" "sent|1"
eq "delivery: history records a distinct kind (hold)" "$(dsql "SELECT kind, ref, target, result FROM messages ORDER BY id DESC LIMIT 1;")" "hold|O-2|assistant|sent"
eq "delivery: task=now → delivered now" "$(C send assistant --text 'do it'; echo "rc=$?") $(($(prompts) - n))" "$(printf 'O-3 sent to assistant\nrc=0') 1"
eq "delivery: role glob (@worker *=hold) holds every kind" "$(W send assistant --kind task --text 'from worker' | tail -1) $(W send assistant --kind answer --text 'a' | tail -1)" "O-4 held for assistant (I-2) O-5 held for assistant (I-3)"
eq "delivery: no matching line → now" "$(W send coordinator --kind info --text up | tail -1)" "O-6 sent to coordinator"
eq "delivery: --json carries the inbox id" "$(C send assistant --kind answer --text j --json | jq -r '"\(.status) \(.inbox)"')" "sent I-4"
# 種別を書いていない行は飛ばして次の行を見る
printf 'coordinator -> assistant : info=hold\ncoordinator -> * : *=hold\n' >"$TB_DELIVERY"
eq "delivery: a line without the kind (and no *) is skipped" "$(C send assistant --text t | tail -1) $(C send worker2 --text t | tail -1)" "O-8 held for assistant (I-5) O-9 held for worker2 (I-6)"
printf 'coordinator -> assistant : info=hold answer=hold task=now\n@worker -> assistant : *=hold\n' >"$TB_DELIVERY"

# 承認待ち → 承認したあとで delivery が決まる
printf 'send:info:assistant = ask\n* = allow\n' >"$TB_APPROVALS"
eq "delivery: approvals before delivery (pending first)" "$(C send assistant --kind info --text wait 2>/dev/null | tail -1) $(dsql "SELECT COUNT(*) FROM inbox WHERE outbox_id=10;")" "O-10 pending-approval 0"
eq "delivery: approved message is held, not typed" "$(n=$(prompts); $TB approve O-10; echo "$(($(prompts) - n))")" "$(printf 'O-10 approved and held for assistant (I-7)\n0')"
printf '* = allow\n' >"$TB_APPROVALS"
# routes が先(拒否は保留しない)
eq "delivery: routes before delivery (denied is not held)" "$(HERDR_PANE_ID=wother:p1 rc $TB send assistant --kind info --text x) $(dsql "SELECT COUNT(*) FROM inbox WHERE outbox_id=(SELECT MAX(id) FROM outbox);")" "4 0"

# inbox: 自分宛てだけ
eq "inbox: the receiver lists its held messages" "$(A inbox | cut -f1,2,3,4 | tr '\t\n' ' ,')" "I-1 unread coordinator info,I-2 unread worker1 task,I-3 unread worker1 answer,I-4 unread coordinator answer,I-5 unread coordinator task,I-7 unread coordinator info,"
eq "inbox: body is one line in the list" "$(A inbox | head -1 | cut -f7)" "status line2"
eq "inbox: the sender cannot see the receiver's inbox" "$(C inbox | wc -l | tr -d ' ') $(W inbox --json | wc -l | tr -d ' ')" "0 0"
eq "inbox: another agent sees only its own" "$(HERDR_PANE_ID=wworker2:p1 $TB inbox | cut -f1 | tr '\n' ' ')" "I-6 "
eq "inbox-read: full body and marks read" "$(A inbox-read I-1 | tail -2 | tr '\n' '|') $(A inbox --unread | cut -f1 | head -1)" "status|line2| I-2"
eq "inbox-read: the sender cannot read it" "$(rc C inbox-read I-2) $(C inbox-read I-2 2>&1 >/dev/null)" "1 tb: inbox に見つかりません: I-2"
eq "inbox-read: another receiver's message is not found" "$(rc A inbox-read I-6) $(dsql "SELECT read_at IS NULL FROM inbox WHERE id=6;")" "1 1"
eq "inbox: --json and --unread" "$(A inbox --unread --json | jq -r '"\(.id) \(.outbox_id)"' | head -1)" "I-2 O-4"
eq "inbox: no pane → exit 3" "$(HERDR_PANE_ID= rc $TB inbox)" "3"
eq "inbox: identity flags are rejected" "$(rc A inbox --name assistant)" "2"
eq "inbox-read: bad id → usage" "$(rc A inbox-read 7)" "2"

# 保留に失敗したら送信者にエラー(黙って捨てない)。outbox は queued のまま、flush で再び保留を試す
dsql "CREATE TRIGGER dl_fail BEFORE INSERT ON inbox BEGIN SELECT RAISE(ABORT, 'disk full'); END;"
n=$(prompts)
out=$(C send assistant --kind info --text lost 2>&1); r=$?
eq "delivery: holding failure → exit 12, reported" "$r $(grep -c 'inbox に保留できませんでした' <<<"$out") $(tail -1 <<<"$out")" "12 1 O-12 queued"
eq "delivery: failed hold is kept queued, not typed, logged as failed" "$(dsql "SELECT status FROM outbox WHERE id=12;") $(($(prompts) - n)) $(dsql "SELECT kind||':'||result FROM messages ORDER BY id DESC LIMIT 1;")" "queued 0 hold:failed"
dsql "DROP TRIGGER dl_fail;"
eq "delivery: flush retries the hold" "$(rc $TB flush) $(dsql "SELECT status FROM outbox WHERE id=12;") $(dsql "SELECT COUNT(*) FROM inbox WHERE outbox_id=12;")" "0 sent 1"

# 役割を確かめられない(台帳を読めない)→ hold に倒す
printf 'coordinator -> @assistant-role : *=now\n' >"$TB_DELIVERY"
dsql "ALTER TABLE agents RENAME COLUMN role TO role_x;"
out=$(C send assistant --kind task --text r 2>/dev/null | tail -1)
dsql "ALTER TABLE agents RENAME COLUMN role_x TO role;"
eq "delivery: a role that cannot be read → held (never now)" "$out $(dsql "SELECT reason LIKE '%確かめられないため保留%' FROM outbox WHERE id=13;")" "O-13 held for assistant (I-9) 1"

# キーを消せば今までどおり(rollback)
unset TB_DELIVERY
eq "delivery: removing the key → delivered now again" "$(C send assistant --kind info --text back | tail -1)" "O-14 sent to assistant"
export TB_CONFIG=${_dl_saved[0]} TB_ROUTES=${_dl_saved[1]} TB_APPROVALS=${_dl_saved[2]}
[[ -n $TB_ROUTES ]] || unset TB_ROUTES; [[ -n $TB_APPROVALS ]] || unset TB_APPROVALS
unset FAKE_AGENTS _dl_saved
