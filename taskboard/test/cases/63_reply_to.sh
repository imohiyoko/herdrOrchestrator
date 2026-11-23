# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb send --reply-to(受け手の自分の送信への返信だけを、DB で確かめてすぐ届ける)
_rp_saved=("${TB_CONFIG-}" "${TB_ROUTES-}" "${TB_POLICY_A-}")
export TB_DB=$TMP/reply.db TB_CONFIG=$TMP/reply-projects.conf TB_ROUTES=$TMP/reply-routes.conf TB_POLICY_A=$TMP/reply-approvals.conf
export TB_DELIVERY=$TMP/reply-delivery.conf
printf 'rp=coordinator\n' >"$TB_CONFIG"
printf 'coordinator -> *\n@worker -> *\nassistant -> *\n' >"$TB_ROUTES"
printf '* * * = allow\n' >"$TB_POLICY_A"
printf 'coordinator -> assistant : *=hold reply=now\n@worker -> assistant : *=hold reply=now\n' >"$TB_DELIVERY"
rsql() { sqlite3 "$TB_DB" "$@"; }
rag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(rag coordinator),$(rag assistant),$(rag worker1)]"
$TB agents >/dev/null 2>&1
rsql "UPDATE agents SET role='worker' WHERE name='worker1';"
: >"$FAKE_LOG"
RC() { HERDR_PANE_ID=wcoordinator:p1 $TB "$@"; }
RA() { HERDR_PANE_ID=wassistant:p1 $TB "$@"; }
RW() { HERDR_PANE_ID=wworker1:p1 $TB "$@"; }

# assistant が coordinator に聞く(O-1。assistant → coordinator は delivery の行が無いので今届く)
eq "reply: the question is delivered with its id in the label" "$(RA send coordinator --kind task --text '進み具合は?' | tail -1) $(grep '^prompt coordinator' "$FAKE_LOG" | tail -1 | grep -c '/ task / O-1\]')" "O-1 sent to coordinator 1"
eq "reply: without --reply-to the pair is held" "$(RC send assistant --kind answer --text 'status report' | tail -1)" "O-2 held for assistant (I-1)"
n=$(prompts)
out=$(RC send assistant --kind answer --reply-to O-1 --text '半分です' 2>&1); r=$?
eq "reply: a valid reply is delivered now although the pair holds" "$r $(tail -1 <<<"$out") $(($(prompts) - n))" "0 O-3 sent to assistant 1"
eq "reply: the label says which message it answers" "$(grep '^prompt assistant' "$FAKE_LOG" | tail -1 | cut -f2)" "[from: coordinator / wcoordinator:p1 / - / answer / O-3 re O-1] 半分です"
eq "reply: the link is recorded" "$(rsql "SELECT reply_to||' '||reply_check FROM outbox WHERE id=3;")" "1 ok"

# 2 回目の返信は通常の送信として扱う(保留)
out=$(RC send assistant --kind answer --reply-to O-1 --text 'もう一度' 2>&1); r=$?
eq "reply: a second reply to the same message is held, the sender is told why" "$r $(tail -1 <<<"$out") $(grep -c 'O-1 には返信済み(O-3)' <<<"$out")" "0 O-4 held for assistant (I-2) 1"
# 組が違う(worker1 は O-1 の宛先ではない)
out=$(RW send assistant --kind answer --reply-to O-1 --text 'わたしも' 2>&1)
eq "reply: a reply from someone the question was not sent to is held" "$(tail -1 <<<"$out") $(grep -c 'O-1 は assistant から worker1 への送信ではない' <<<"$out")" "O-5 held for assistant (I-3) 1"
# 向きが逆(coordinator が自分の送信 O-2 に「返信」と主張)
out=$(RC send assistant --kind answer --reply-to O-2 --text 'self' 2>&1)
eq "reply: the reversed pair is required (own message → held)" "$(tail -1 <<<"$out") $(grep -c 'O-2 は assistant から coordinator への送信ではない' <<<"$out")" "O-6 held for assistant (I-4) 1"
out=$(RC send assistant --kind answer --reply-to O-999 --text 'ghost' 2>&1)
eq "reply: a message that does not exist → held" "$(tail -1 <<<"$out") $(grep -c '元の送信 O-999 が見つからない' <<<"$out")" "O-7 held for assistant (I-5) 1"
eq "reply: --reply-to must be O-<n>" "$(rc RC send assistant --reply-to 1 --text x) $(rc RC send assistant --reply-to I-1 --text x)" "2 2"

# まだ届いていない(承認待ち)の送信への返信は認めない
printf '* send:task coordinator = ask\n* * * = allow\n' >"$TB_POLICY_A"
RA send coordinator --kind task --text 'pending q' >/dev/null 2>&1   # O-8 pending
out=$(RC send assistant --kind answer --reply-to O-8 --text 'early' 2>&1)
eq "reply: the original must have been delivered" "$(tail -1 <<<"$out") $(grep -c 'O-8 はまだ届いていない(pending)' <<<"$out")" "O-9 held for assistant (I-6) 1"
# 返信が承認待ちで却下されたら、返信済みに数えない
RA send coordinator --kind info --text 'q2' >/dev/null 2>&1   # O-10 sent(info は allow)
printf '* send:answer assistant = ask\n* * * = allow\n' >"$TB_POLICY_A"
eq "reply: a verified reply still waits for approval first" "$(RC send assistant --kind answer --reply-to O-10 --text r1 2>/dev/null | tail -1 | sed -E 's/Q-[0-9]+/Q-n/g')" "O-11 pending-approval Q-n"
$TB reject O-11 >/dev/null
printf '* * * = allow\n' >"$TB_POLICY_A"
eq "reply: a rejected reply does not count as answered" "$(RC send assistant --kind answer --reply-to O-10 --text r2 2>/dev/null | tail -1) $(rsql "SELECT reply_check FROM outbox WHERE id=11;")" "O-12 sent to assistant 人が却下"
# 承認されれば、届ける時点で返信として扱う
printf '* send:answer assistant = ask\n* * * = allow\n' >"$TB_POLICY_A"
RA send coordinator --kind info --text 'q3' >/dev/null 2>&1   # O-13
RC send assistant --kind answer --reply-to O-13 --text r3 >/dev/null 2>&1   # O-14 pending
eq "reply: an approved verified reply is delivered now" "$($TB approve O-14)" "O-14 approved and sent to assistant"
printf '* * * = allow\n' >"$TB_POLICY_A"

# 保留された送信(inbox)への返信も返信として届く(元の送信は sent)
eq "reply: replying to a held message is allowed" "$(RA send coordinator --kind answer --reply-to O-2 --text 'thanks' 2>/dev/null | tail -1) $(rsql "SELECT reply_check FROM outbox WHERE id=15;")" "O-15 sent to coordinator ok"

# reply= を書いていない組では、確かめた返信も種別の規則で決まる(* では今届けない)
printf 'coordinator -> assistant : *=hold\n' >"$TB_DELIVERY"
RA send coordinator --kind task --text 'q4' >/dev/null 2>&1   # O-16
eq "reply: without reply= the verified reply follows the kind rule" "$(RC send assistant --kind answer --reply-to O-16 --text r4 2>/dev/null | tail -1) $(rsql "SELECT reply_check FROM outbox WHERE id=17;")" "O-17 held for assistant (I-7) ok"
# reply=hold も明示できる
printf 'coordinator -> assistant : *=now reply=hold\n' >"$TB_DELIVERY"
RA send coordinator --kind task --text 'q5' >/dev/null 2>&1   # O-18
eq "reply: reply=hold holds verified replies" "$(RC send assistant --kind answer --reply-to O-18 --text r5 2>/dev/null | tail -1)" "O-19 held for assistant (I-8)"
# delivery が無ければ今までどおり(--reply-to があっても今届く)
unset TB_DELIVERY
eq "reply: without the delivery key everything is delivered now" "$(RC send assistant --kind answer --reply-to O-999 --text r6 2>/dev/null | tail -1)" "O-20 sent to assistant"
# 拒否された送信は返信とみなさない(主張の失敗は言わない。拒否のメッセージだけ)
printf '!worker1 -> coordinator\n* -> *\n' >"$TB_ROUTES"
RC send worker1 --kind task --text 'q6' >/dev/null 2>&1   # O-21
eq "reply: a denied reply is recorded as not ok" "$(RW send coordinator --reply-to O-21 --text x 2>&1 >/dev/null | grep -c 認めません) $(rsql "SELECT status||' '||reply_check FROM outbox WHERE id=22;")" "0 denied ルールで拒否された送信"
export TB_CONFIG=${_rp_saved[0]} TB_ROUTES=${_rp_saved[1]} TB_POLICY_A=${_rp_saved[2]}
[[ -n $TB_ROUTES ]] || unset TB_ROUTES; [[ -n $TB_POLICY_A ]] || unset TB_POLICY_A
unset FAKE_AGENTS _rp_saved
