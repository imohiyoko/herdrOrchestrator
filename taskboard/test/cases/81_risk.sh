# shellcheck shell=bash
# run.sh から番号順に source される。リスク評価(lib/risk.sh。第 1 段: 評価を出すだけ)
# lib/risk.sh を別のシェルで source して直接評価する。規則ファイルの差し替えはテストの中(RISK_RULES の代入)だけで行う。tb 本体は env で差し替えられない
RK=$TMP/risk; mkdir -p "$RK"
# rk <規則ファイル|-(なし)> <送信者の役割> <宛先の役割> <宛先の状態> <送信者のモデル> <本文> → 評価の全出力(環境変数 K があれば kind)
rk() {
  ( TB_ROOT=$HERE/..; source "$HERE/../lib/risk.sh"; source "$HERE/../lib/notify.sh" 2>/dev/null
    if [[ $1 == - ]]; then RISK_RULES=$RK/none.conf; else RISK_RULES=$1; fi
    RISK_IN_SENDER_ROLE=$2 RISK_IN_TARGET_ROLE=$3 RISK_IN_TARGET_STATE=$4 RISK_IN_SENDER_MODEL=$5 RISK_IN_TEXT=$6
    [[ -z ${K:-} ]] || RISK_IN_KIND=$K
    risk_eval; risk_format )
}
head1() { sed -n 1p | cut -d' ' -f1-2; }
reasons() { grep '^risk-reason:' | sed 's/^risk-reason: //'; }
OK=(conductor worker running opus)
HDR='risk: ask'

# --- 規則ファイルなし = 組み込みの既定
eq "risk: clean input → allow 0/2" "$(rk - "${OK[@]}" 'please read the docs' | sed -n '1p;2p' | cut -d' ' -f1-2 | tr '\n' '|')" "risk: allow|risk-score: 0|"
eq "risk: no reasons when nothing contributes" "$(rk - "${OK[@]}" 'hello' | reasons | wc -l | tr -d ' ')" "0"
# R-02(語ごとに 1 件)
for w in 'git push origin main' 'merge the PR' 'use --force here' 'then --approve it' 'git reset --hard HEAD' 'rm -rf build'; do
  eq "risk R-02: '$w' → ask" "$(rk - "${OK[@]}" "$w" | head1)|$(rk - "${OK[@]}" "$w" | reasons | cut -c1-9)" "risk: ask|R-02 +ask"
done
eq "risk R-02: names the word" "$(rk - "${OK[@]}" 'please Push it' | reasons)" "R-02 +ask 本文に破壊的・公開系の操作語(語: Push)"
eq "risk R-02: word boundary (pushed/emerge/mergeable do not hit)" "$(rk - "${OK[@]}" 'pushed emerge mergeable' | head1)" "risk: allow"
# R-06
eq "risk R-06: stopped target → +2 (= threshold → ask)" "$(rk - conductor worker stopped opus 'hi' | reasons)|$(rk - conductor worker stopped opus hi | head1)" "R-06 +2 宛先が停止中|risk: ask"
# [unknown]: 役割・状態は ask、モデルは 1 点
eq "risk unknown: sender.role → ask" "$(rk - '' worker running opus 'hi' | reasons)" "unknown:sender.role +ask sender.role が不明(設定 ask)"
eq "risk unknown: target.role → ask" "$(rk - conductor '' running opus 'hi' | head1)" "risk: ask"
eq "risk unknown: target.state → ask" "$(rk - conductor worker '' opus 'hi' | reasons)" "unknown:target.state +ask target.state が不明(設定 ask)"
eq "risk unknown: model alone = 1 point, still allow" "$(rk - conductor worker running '' 'hi' | head1)|$(rk - conductor worker running '' hi | reasons)" "risk: allow|unknown:sender.model +1 sender.model が不明(設定 risk:1)"
eq "risk unknown: model unknown (1) + stopped target (2) → score 3 ≥ threshold" "$(rk - conductor worker stopped '' 'hi' | sed -n 2p)" "risk-score: 3 / threshold 2"
eq "risk: unknown model + body word counted separately" "$(rk - conductor worker running '' 'git push' | reasons | cut -d' ' -f1-2 | tr '\n' '|')" "unknown:sender.model +1|R-02 +ask|"
# R-07: 入力欠損(未設定。空とは別)
miss() { ( TB_ROOT=$HERE/..; source "$HERE/../lib/risk.sh"; RISK_RULES=$RK/none.conf
    RISK_IN_SENDER_ROLE=a RISK_IN_TARGET_ROLE=b RISK_IN_TARGET_STATE=running RISK_IN_SENDER_MODEL=m RISK_IN_TEXT=t
    unset "$1"; risk_eval; risk_format ) ; }
for pair in RISK_IN_SENDER_ROLE:sender.role RISK_IN_TARGET_ROLE:target.role RISK_IN_TARGET_STATE:target.state RISK_IN_SENDER_MODEL:sender.model RISK_IN_TEXT:body; do
  eq "risk R-07 fail-closed: ${pair%%:*} missing → ask, reason names it" "$(miss "${pair%%:*}" | head1)|$(miss "${pair%%:*}" | reasons)" "risk: ask|R-07 +ask 評価不能(fail-closed): 入力が取れません: ${pair#*:}"
done
# --- 規則ファイル
cat >"$RK/good.conf" <<'C'
[threshold]
ask-at = 2
[patterns]
external = slack|curl|https?://
destructive = drop table
[rules]
R-04 | body.matches(external) and target.role in (worker) | risk:1 | 外部送信を含む依頼
R-05 | sender.role in (worker) and target.role in (worker) | risk:1 | 横の連絡
R-03 | body.secret | ask | 秘密らしき文字列
R-08 | kind in (info) | risk:1 | info 種別
C
eq "risk rules: R-04 matches (+1, below threshold → allow)" "$(rk "$RK/good.conf" "${OK[@]}" 'post to Slack' | reasons)|$(rk "$RK/good.conf" "${OK[@]}" 'post to Slack' | head1)" "R-04 +1 外部送信を含む依頼(語: Slack)|risk: allow"
eq "risk rules: R-04 does not match for non-worker target" "$(rk "$RK/good.conf" conductor hr running opus 'post to Slack' | reasons | wc -l | tr -d ' ')" "0"
eq "risk rules: R-04 + R-05 overlap reaches the threshold → ask" "$(rk "$RK/good.conf" worker worker running opus 'curl it' | head1)|$(rk "$RK/good.conf" worker worker running opus 'curl it' | sed -n 2p)" "risk: ask|risk-score: 2 / threshold 2"
eq "risk rules: R-05 alone" "$(rk "$RK/good.conf" worker worker running opus 'hello' | reasons)" "R-05 +1 横の連絡"
eq "risk rules: extended pattern adds words to destructive" "$(rk "$RK/good.conf" "${OK[@]}" 'DROP TABLE users' | reasons)" "R-02 +ask 本文に破壊的・公開系の操作語(語: DROP TABLE)"
FAKEKEY=AK"IA"$(printf '%s' ABCDEFGHIJKLMNOP)   # 実行時に組み立てる偽の値(ソースに鍵の形を置かない)
eq "risk rules: R-03 secret scan (kind name only, never the value)" "$(rk "$RK/good.conf" "${OK[@]}" "key $FAKEKEY end" | reasons | grep -cF "$FAKEKEY")|$(rk "$RK/good.conf" "${OK[@]}" "key $FAKEKEY end" | reasons | cut -c1-6)" "0|R-03 +"
eq "risk rules: kind condition matches kind=info (+1)" "$(K=info rk "$RK/good.conf" "${OK[@]}" hi | reasons)" "R-08 +1 info 種別"
eq "risk rules: kind condition does not match kind=task" "$(K=task rk "$RK/good.conf" "${OK[@]}" hi | reasons | wc -l | tr -d ' ')" "0"
eq "risk rules: kind condition does not match when kind is unset" "$(rk "$RK/good.conf" "${OK[@]}" hi | reasons | wc -l | tr -d ' ')" "0"
# 語境界は ASCII 限定: 日本語に隣接した語も検出する。どの locale でも同じ結果
for txt in 'mainにpushして' 'それをmergeして' 'pushしてください'; do
  r1=$(LC_ALL=C rk - "${OK[@]}" "$txt" | head1); r2=$(LC_ALL=en_US.UTF-8 rk - "${OK[@]}" "$txt" | head1); r3=$(LC_ALL=C.UTF-8 rk - "${OK[@]}" "$txt" | head1); r4=$(rk - "${OK[@]}" "$txt" | head1)
  eq "risk R-02: '$txt' → ask in every locale (C / en_US.UTF-8 / C.UTF-8 / inherited)" "$r1|$r2|$r3|$r4" "risk: ask|risk: ask|risk: ask|risk: ask"
done
eq "risk R-02: identical full output across locales" "$(LC_ALL=C rk - "${OK[@]}" 'mainにpushして' | md5)" "$(LC_ALL=en_US.UTF-8 rk - "${OK[@]}" 'mainにpushして' | md5)"
eq "risk R-02: ASCII neighbour still blocks the match (pushed)" "$(LC_ALL=en_US.UTF-8 rk - "${OK[@]}" 'pushedして' | head1)" "risk: allow"
# 決定論
a=$(rk "$RK/good.conf" worker worker stopped '' 'git push; curl http://x'); b=$(rk "$RK/good.conf" worker worker stopped '' 'git push; curl http://x')
eq "risk: same input twice → identical output" "$([[ $a == "$b" ]] && echo same)" "same"
eq "risk: reasons are listed in a fixed order (unknown, built-in R-02, R-06, file rules)" "$(grep '^risk-reason:' <<<"$a" | cut -d' ' -f2 | tr '\n' ' ')" "unknown:sender.model R-02 R-06 R-04 R-05 "
# --- 自然文の & $ ; ` ( ) ' " > < | で全件 ask にならない
nat='R&D costs $5 > $3 & more; see `foo` (a|b) "quoted" it'"'"'s <ok> 50% 〜 $(not run)'
eq "risk: natural-language & \$ etc. do not force ask" "$(rk - "${OK[@]}" "$nat" | head1)|$(rk - "${OK[@]}" "$nat" | reasons | wc -l | tr -d ' ')" "risk: allow|0"
eq "risk: \$(…) in the body is not executed" "$(rk - "${OK[@]}" "x \$(touch $RK/pwned) y" >/dev/null; [[ -e $RK/pwned ]] && echo executed || echo not-run)" "not-run"
# --- fail-closed: 規則ファイルの壊れ(R-07)
bad() { printf '%s\n' "$2" >"$RK/$1.conf"; rk "$RK/$1.conf" "${OK[@]}" 'hello' | reasons; }
eq "risk fail-closed: unknown section" "$(bad b1 '[nope]' | grep -c 'R-07 +ask 評価不能(fail-closed): L1: 未知の節')" "1"
eq "risk fail-closed: line before any section" "$(bad b2 'x = 1' | grep -c 'R-07 +ask 評価不能(fail-closed): L1: ')" "1"
eq "risk fail-closed: bad ask-at" "$(bad b3 $'[threshold]\nask-at = 0' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: unknown attribute in [unknown]" "$(bad b4 $'[unknown]\nfoo = ask' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: bad value in [unknown]" "$(bad b5 $'[unknown]\nsender.model = risk:x' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: invalid regex" "$(bad b6 $'[patterns]\nfoo = (' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: regex that matches the empty string (would hit everything)" "$(bad b7 $'[patterns]\nfoo = a*' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: rule with 3 fields" "$(bad b8 $'[rules]\nR-04 | body.secret | ask' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: unknown condition" "$(bad b9 $'[rules]\nR-04 | body.contains(x) | ask | y' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: undefined pattern name" "$(bad b10 $'[rules]\nR-04 | body.matches(nothing) | ask | y' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: points above threshold" "$(bad b11 $'[rules]\nR-04 | body.secret | risk:9 | y' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: duplicate rule id" "$(bad b12 $'[rules]\nR-04 | body.secret | ask | y\nR-04 | body.secret | ask | z' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: built-in rule id cannot be redefined" "$(bad b13 $'[rules]\nR-02 | body.secret | ask | y' | grep -c 'R-07 +ask')" "1"
eq "risk fail-closed: unreadable path (directory)" "$(mkdir -p "$RK/dir.conf"; rk "$RK/dir.conf" "${OK[@]}" hi | head1)" "risk: ask"
eq "risk fail-closed: value of a broken file is ask, with score line" "$(bad b1 '[nope]' >/dev/null; rk "$RK/b1.conf" "${OK[@]}" hi | sed -n 2p)" "risk-score: 0 / threshold 2"
# --- 緩め設定は読み込んで警告するだけ(第 3 段まで効かない)
cat >"$RK/loose.conf" <<'C'
[threshold]
ask-at = 5
[unknown]
target.role = allow
sender.model = allow
C
eq "risk loosen: target.role=allow is ignored (still ask)" "$(rk "$RK/loose.conf" conductor '' running opus hi | reasons)" "unknown:target.role +ask target.role が不明(設定 ask)"
eq "risk loosen: each loosened setting warns, in file order" "$(rk "$RK/loose.conf" "${OK[@]}" hi | grep '^risk-warn:' | sed 's/^risk-warn: 緩め設定は第 3 段まで無効: //' | tr '\n' '|')" "[threshold] ask-at = 5(既定 2 のまま)|[unknown] target.role = allow(既定 ask のまま)|[unknown] sender.model = allow(既定 risk:1 のまま)|"
eq "risk loosen: sender.model=allow ignored (still +1)" "$(rk "$RK/loose.conf" conductor worker running '' hi | reasons)" "unknown:sender.model +1 sender.model が不明(設定 risk:1)"
eq "risk loosen: threshold stays 2" "$(rk "$RK/loose.conf" "${OK[@]}" hi | sed -n 2p)" "risk-score: 0 / threshold 2"
# 厳しくする方向は効く
cat >"$RK/strict.conf" <<'C'
[threshold]
ask-at = 1
[unknown]
sender.model = ask
C
eq "risk strict: lowering the threshold and sender.model=ask take effect" "$(rk "$RK/strict.conf" conductor worker running '' hi | reasons)|$(rk "$RK/strict.conf" "${OK[@]}" hi | sed -n 2p)" "unknown:sender.model +ask sender.model が不明(設定 ask)|risk-score: 0 / threshold 1"

# --- tb への組み込み(判定は変えない)
export TB_DB=$TMP/risk.db TB_CONFIG=$TMP/send.conf TB_APPROVALS=$TMP/approvals-risk.conf
cp "$HERE/../config/approvals.conf.example" "$TB_APPROVALS"
export FAKE_AGENTS='[{"pane_id":"wC:p1","name":"conductor","agent_session":{"value":"sc"}},{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s1"}}]'
: >"$FAKE_LOG"
eq "approval: no extra args → output exactly as before (no risk lines)" "$($TB approval send:task:worker1; echo "rc=$?")" "$(printf 'ask\nrule: send:task:* = ask\nrc=20')"
out=$($TB approval send:info:worker1 --to worker1 --text 'git push now'); r=$?
eq "approval: with --to/--text → allow stays allow (rc 0), risk lines appended" "$r|$(sed -n '1,2p' <<<"$out" | tr '\n' '|')$(grep -c '^risk: ask' <<<"$out")|$(grep -c '^risk-reason: R-02 +ask' <<<"$out")" "0|allow|rule: send:info:* = allow|1|1"
# tb send: 結果(status・終了コード・配送)は評価に影響されず、reason に評価が追記される
HERDR_PANE_ID=wC:p1 $TB send worker1 --kind info --text 'git push it' >/dev/null 2>&1; r=$?
eq "send: R-02 hit but approvals allow → still sent (risk is not used)" "$r|$(sqlite3 "$TB_DB" "SELECT status FROM outbox ORDER BY id DESC LIMIT 1;")" "0|sent"
eq "send: reason carries the risk evaluation after the approvals rule" "$(sqlite3 "$TB_DB" "SELECT reason FROM outbox ORDER BY id DESC LIMIT 1;" | grep -c ' / approvals: send:info:\* = allow / risk: ask(参考。score 1/2) \[unknown:.*R-02 +ask ')" "1"
HERDR_PANE_ID=wC:p1 $TB send worker1 --kind info --text 'just reading' >/dev/null 2>&1
eq "send: clean body → no R-02 in the reason" "$(sqlite3 "$TB_DB" "SELECT reason FROM outbox ORDER BY id DESC LIMIT 1;" | grep -c 'risk: ask(参考。score 1/2) \[unknown:target.state +ask')" "1"
HERDR_PANE_ID=wC:p1 $TB send worker1 --text 'plain task' >/dev/null 2>&1; r=$?
eq "send: approvals ask stays pending (exit 20) whatever the risk says" "$r|$(sqlite3 "$TB_DB" "SELECT status FROM outbox ORDER BY id DESC LIMIT 1;")" "20|pending"
# 台帳に agent がいない宛先(役割は map.conf で引ける)でも評価は出る。宛先の状態が不明 → ask(参考)
eq "send: target state unknown in the ledger → reference ask + reason" "$(sqlite3 "$TB_DB" "SELECT reason FROM outbox ORDER BY id DESC LIMIT 1;" | grep -c 'unknown:target.state +ask')" "1"
sqlite3 "$TB_DB" "INSERT INTO agents (herdr_session, name,state,created_by,created_at,updated_at,model) VALUES ('default', 'worker1','stopped','tb','t','t','opus');"
HERDR_PANE_ID=wC:p1 $TB send worker1 --kind info --text 'stopped target' >/dev/null 2>&1
eq "send: stopped target → R-06 in the reason, delivery unaffected by risk" "$(sqlite3 "$TB_DB" "SELECT reason FROM outbox ORDER BY id DESC LIMIT 1;" | grep -c 'R-06 +2 宛先が停止中')" "1"
# 人の端末から(pane なし)の approval 評価: 送信者は human
eq "approval: human terminal (no pane) is evaluated as sender=human (no R-07, no unknown:sender)" "$(HERDR_PANE_ID= $TB approval send:info:worker1 --to worker1 --text hi | grep -cE 'unknown:sender|R-07')" "0"
eq "approval: arg error prints nothing on stdout" "$($TB approval send:info:worker1 --to worker1 2>/dev/null | wc -l | tr -d ' ')|$($TB approval send:info:worker1 --to 'a b' --text x 2>/dev/null | wc -l | tr -d ' ')|$($TB approval send:info:worker1 --to w --text x --kind zzz 2>/dev/null | wc -l | tr -d ' ')" "0|0|0"
eq "approval: --to without --text is a usage error" "$(rc $TB approval send:info:worker1 --to worker1)" "2"
# tb 本体は規則ファイルの場所を env で差し替えられない
eq "risk: TB_RISK_RULES has no effect (fixed path)" "$(printf '[nope]\n' >"$RK/env.conf"; TB_RISK_RULES=$RK/env.conf RISK_RULES=$RK/env.conf $TB approval send:info:worker1 --to worker1 --text hi | grep -c 'R-07')" "0"
unset FAKE_AGENTS TB_APPROVALS
