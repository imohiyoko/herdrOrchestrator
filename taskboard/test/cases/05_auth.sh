# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 呼び出し元の確かめ(ADR 0007 段 1a): HERDR_PANE_ID を信じず、pane の中から呼ばれたかを herdr とプロセスの親子・端末で確かめる
# 疑似 herdr は、呼んだ tb の HERDR_PANE_ID の pane だけ、テストのプロセス(TB_TEST_SHELL_PID。tb の祖先)を pane の最初のプロセスとして返す
export TB_DB=$TMP/auth.db
unset FAKE_AGENTS; _au_tc=${TB_CONFIG-}; unset TB_CONFIG
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
unset -f au in_herdr; [[ -z $_au_tc ]] || export TB_CONFIG=$_au_tc; unset _au_tc
