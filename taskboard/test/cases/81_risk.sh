# shellcheck shell=bash
# run.sh から番号順に source される。リスク評価(第 1 段: 評価を出すだけ)の tb への組み込み。
# 評価そのもの(規則ファイル・語・fail-closed)は internal/approval の risk_cases_test.go と risk_test.go(消した bash 版の出力を写した golden)
RK=$TMP/risk; mkdir -p "$RK"
# --- tb への組み込み(判定は変えない)
export TB_DB=$TMP/risk.db TB_CONFIG=$TMP/send.conf TB_POLICY_A=$TMP/approvals-risk.conf
printf '*=conductor\n' >"$TB_CONFIG"
: >"$TB_POLICY_A"   # 既定の表(perms/ops.tbl)で決まる
export FAKE_AGENTS='[{"pane_id":"wC:p1","name":"conductor","agent_session":{"value":"sc"}},{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s1"}}]'
: >"$FAKE_LOG"
eq "approval: no extra args → output exactly as before (no risk lines)" "$($TB approval send:task:worker1; echo "rc=$?")" "$(printf 'ask\nrule: 既定 ask\nrc=20')"
out=$($TB approval send:info:worker1 --to worker1 --text 'git push now'); r=$?
eq "approval: with --to/--text → allow stays allow (rc 0), risk lines appended" "$r|$(sed -n '1,2p' <<<"$out" | tr '\n' '|')$(grep -c '^risk: ask' <<<"$out")|$(grep -c '^risk-reason: R-02 +ask' <<<"$out")" "0|allow|rule: 既定 allow|1|1"
# tb send: 結果(status・終了コード・配送)は評価に影響されず、reason に評価が追記される
HERDR_PANE_ID=wC:p1 $TB send worker1 --kind info --text 'git push it' >/dev/null 2>&1; r=$?
eq "send: R-02 hit but approvals allow → still sent (risk is not used)" "$r|$(sqlite3 "$TB_DB" "SELECT status FROM outbox ORDER BY id DESC LIMIT 1;")" "0|sent"
eq "send: reason carries the risk evaluation after the policy rule" "$(sqlite3 "$TB_DB" "SELECT reason FROM outbox ORDER BY id DESC LIMIT 1;" | grep -c ' / 認可: 既定 allow / risk: ask(参考。score 1/2) \[unknown:.*R-02 +ask ')" "1"
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
unset FAKE_AGENTS TB_POLICY_A
