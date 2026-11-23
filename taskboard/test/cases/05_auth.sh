# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 呼び出し元の確かめ(ADR 0007 段 1a): HERDR_PANE_ID を信じず、pane の中から呼ばれたかを herdr とプロセスの親子・端末で確かめる
# 疑似 herdr は、呼んだ tb の HERDR_PANE_ID の pane だけ、テストのプロセス(TB_TEST_SHELL_PID。tb の祖先)を pane の最初のプロセスとして返す
_au_saved=$(declare -p TB_DB FAKE_AGENTS TB_CONFIG TB_SYSTEM 2>/dev/null || true)   # 最後に元の宣言(未設定・export も)へ戻す
export TB_DB=$TMP/auth.db
unset FAKE_AGENTS TB_CONFIG TB_SYSTEM
au() { "$@" >/dev/null 2>"$TMP/auth.err"; echo $?; }   # 人だけのコマンド tb config unset で、人か agent か分からないかを見る
eq "auth: outside herdr, no HERDR_PANE_ID → human" "$(au env -u HERDR_PANE_ID $TB config unset projects)" "0"
eq "auth: a verified agent pane is the agent (human-only refused)" "$(HERDR_PANE_ID=wW:p1 au $TB config unset projects) $(grep -c "worker1" "$TMP/auth.err")" "1 1"
eq "auth: a claimed pane we are not inside → unknown (rc 3)" "$(FAKE_SHELL_PID=999999 HERDR_PANE_ID=wW:p1 au $TB list) $(grep -c '^tb: \[identity.unverified\] HERDR_PANE_ID=wW:p1 の pane の中から実行されていません' "$TMP/auth.err")" "3 1"
eq "auth: another agent's pane cannot be borrowed" "$(FAKE_SHELL_PID=999999 HERDR_PANE_ID=wW:p2 au $TB config unset projects)" "3"
eq "auth: herdr cannot answer → unknown (fail-closed)" "$(FAKE_PINFO_FAIL=1 HERDR_PANE_ID=wW:p1 au $TB list) $(grep -c '問い合わせられない' "$TMP/auth.err")" "3 1"
eq "auth: a pane that does not exist → unknown" "$(HERDR_PANE_ID=nope:p9 FAKE_SHELL_PID=999999 au $TB list)" "3"

# herdr の中(祖先に herdr のプロセスがいる)で HERDR_PANE_ID を消しても、人にはなれない: agent の pane を探して付け直す
mkdir -p "$TMP/auth-bin"; cp "$(command -v bash)" "$TMP/auth-bin/herdr"
in_herdr() { "$TMP/auth-bin/herdr" -c '"$@"; r=$?; exit $r' _ "$@"; }   # 名前が herdr のプロセスの子として動かす(exec で置き換わらないよう、あとに続きを置く)
eq "auth: inside herdr without HERDR_PANE_ID, our pane is an agent's → that agent" "$(FAKE_SHELL_PID=$TB_TEST_SHELL_PID in_herdr env -u HERDR_PANE_ID "$TB" config unset projects >/dev/null 2>"$TMP/auth.err"; echo $?) $(grep -c 'worker1' "$TMP/auth.err")" "1 1"
eq "auth: inside herdr without HERDR_PANE_ID, no agent's pane → human (a person's own pane)" "$(in_herdr env -u HERDR_PANE_ID "$TB" config unset projects >/dev/null 2>&1; echo $?)" "0"
eq "auth: inside herdr, herdr cannot answer → unknown" "$(FAKE_LIST_FAIL=1 in_herdr env -u HERDR_PANE_ID "$TB" list >/dev/null 2>&1; echo $?)" "3"
eq "auth: hooks (touch) are not stopped" "$(FAKE_SHELL_PID=999999 HERDR_PANE_ID=wW:p1 au $TB touch)" "0"
eq "auth: inside herdr, an unreadable agent list → unknown (not a person's pane)" "$(FAKE_AGENTS='"x"' in_herdr env -u HERDR_PANE_ID "$TB" list >/dev/null 2>&1; echo $?)" "3"
eq "auth: inside herdr, an empty agent list → human" "$(FAKE_AGENTS='[]' in_herdr env -u HERDR_PANE_ID "$TB" config unset projects >/dev/null 2>&1; echo $?)" "0"
# system:<名前>(scheduler など): herdr の外から TB_SYSTEM で名乗る。policy の system: の行だけを見る(当たらなければ人と同じ)
_au_pol=${TB_POLICY-}; export TB_POLICY=$TMP/auth.policy; : >"$TB_POLICY"
eq "auth: system without system: rows passes like a human (human-only command)" "$(TB_SYSTEM=scheduler au env -u HERDR_PANE_ID $TB config unset projects)" "0"
printf 'system:scheduler cmd:config:unset * = deny\nsystem:* cmd:config:list * = ask\n* cmd:ticket:list * = deny\n' >"$TB_POLICY"
eq "auth: a system: row restricts the scheduler (inner check)" "$(TB_SYSTEM=scheduler au env -u HERDR_PANE_ID $TB config unset projects) $(grep -c "system:scheduler" "$TMP/auth.err")" "1 1"
eq "auth: a system:* glob row restricts at the entry" "$(TB_SYSTEM=scheduler au env -u HERDR_PANE_ID $TB config)" "20"
eq "auth: rows for * (agents) do not apply to system" "$(TB_SYSTEM=scheduler au env -u HERDR_PANE_ID $TB list)" "0"
eq "auth: without TB_SYSTEM the person's terminal is not restricted by system: rows" "$(au env -u HERDR_PANE_ID -u TB_SYSTEM $TB config unset projects)" "0"
eq "auth: TB_SYSTEM inside an agent pane is ignored (still the agent)" "$(TB_SYSTEM=scheduler HERDR_PANE_ID=wW:p1 au $TB config unset projects) $(grep -c "worker1" "$TMP/auth.err")" "1 1"
eq "auth: a malformed TB_SYSTEM → unknown (rc 3)" "$(TB_SYSTEM='Bad name' au env -u HERDR_PANE_ID $TB list)" "3"
[[ -n $_au_pol ]] && export TB_POLICY=$_au_pol || unset TB_POLICY; unset _au_pol
eq "auth: config set accepts system: rows and refuses a malformed one" "$(printf 'system:scheduler cmd:ticket:show * = deny\n' | env -u HERDR_PANE_ID -u TB_SYSTEM $TB config set policy >/dev/null 2>&1; echo $?) $(printf 'system:Bad cmd:ticket:show * = deny\n' | env -u HERDR_PANE_ID -u TB_SYSTEM $TB config set policy 2>&1 >/dev/null | grep -c 'system の主体は')" "0 1"
env -u HERDR_PANE_ID $TB config unset policy >/dev/null 2>&1 || true
unset -f au in_herdr; unset TB_DB FAKE_AGENTS TB_CONFIG TB_SYSTEM; eval "$_au_saved"; unset _au_saved
