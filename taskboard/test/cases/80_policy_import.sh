# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb policy import(古い approvals / grants を policy の行に変換する)と tb allow / ask の書き方
_pi_saved=("${TB_POLICY-}")
export TB_DB=$TMP/pimport.db TB_POLICY=$TMP/pimport-policy.conf TB_APPROVALS=$TMP/pimport-appr.conf TB_GRANTS=$TMP/pimport-grants.conf
: >"$TB_POLICY"
# 古い approvals の * はコマンドの中の操作だけに当たっていた。cmd: や approve:・agent:flag を許す行にしない
printf 'send:task:w1 = ask\n* = allow\n' >"$TB_APPROVALS"
printf 'lead send:task ~child\n' >"$TB_GRANTS"
out=$(env -u HERDR_PANE_ID $TB policy import 2>&1)
eq "import: dry run writes nothing" "$(wc -c <"$TB_POLICY" | tr -d ' ')" "0"
eq "import: the ask before the allow is kept" "$(grep -c '^  \* send:task w1 = ask' <<<"$out")" "1"
eq "import: '* = allow' becomes the inner op families, not '* * *'" \
  "$(grep -c '^  \* \* \* = allow' <<<"$out") $(grep -c '^  \* agent:read \* = allow' <<<"$out") $(grep -c '^  \* recipe:\* \* = allow' <<<"$out") $(grep -cE '^  \* (cmd|approve|agent:flag)' <<<"$out")" "0 1 1 0"
eq "import: grants become allow rows" "$(grep -c '^  lead send:task ~child = allow' <<<"$out")" "1"
eq "import: a grant with its own subject overrides a * ask row, so it is not flagged (ADR 0009)" "$(grep -c 'grants 1 行目 lead send:task ~child は' <<<"$out")" "0"
eq "import: an approvals cmd: row is skipped" "$(printf 'cmd:ticket:list = allow\n' >"$TB_APPROVALS"; env -u HERDR_PANE_ID $TB policy import | grep -c 'cmd: の行は前から効いていない')" "1"
eq "import: role:change is not given to a role name (a human writes who gets it)" "$(printf 'role:change = allow\n' >"$TB_APPROVALS"; env -u HERDR_PANE_ID $TB policy import 2>&1 | grep -c 'role:change は誰に許すかを決めて policy に直接書く') $(env -u HERDR_PANE_ID $TB policy import 2>&1 | grep -c '@hr')" "1 0"
eq "import: not from an agent pane" "$(FAKE_AGENTS='[{"pane_id":"wX:p1","name":"x1","agent_session":{"value":"sx"}}]' HERDR_PANE_ID=wX:p1 rc $TB policy import)" "1"
printf '* = allow\n' >"$TB_APPROVALS"
out=$(env -u HERDR_PANE_ID $TB policy import --apply 2>&1)
eq "import --apply: rows are written to policy" "$(grep -c '^\* send:task \* = allow' "$TB_POLICY") $(grep -c '^\* \* \*' "$TB_POLICY")" "1 0"
eq "import --apply: an env file is left in place" "$(grep -c 'TB_APPROVALS\|環境変数で指定したファイルなので消しません' <<<"$out") $([[ -f $TB_APPROVALS ]] && echo kept)" "2 kept"
eq "import --apply: the written policy is valid" "$(env -u HERDR_PANE_ID $TB config set policy <"$TB_POLICY" >/dev/null 2>&1; echo $?)" "0"
unset TB_APPROVALS TB_GRANTS
eq "import: nothing to convert" "$(env -u HERDR_PANE_ID $TB policy import)" "古い権限の設定(approvals / grants)はありません。変換するものはありません"

# tb allow / ask も同じ: 広い glob はコマンドの中の操作の族に分けて書く
: >"$TB_POLICY"
eq "allow '*': inner op families only" "$(env -u HERDR_PANE_ID $TB allow '*' | wc -l | tr -d ' ') $(grep -c '^\* \* \*' "$TB_POLICY") $(grep -cE '^\* (cmd|approve|agent:flag)' "$TB_POLICY")" "17 0 0"
eq "allow '*' again: rows are replaced, not added" "$(env -u HERDR_PANE_ID $TB ask '*' >/dev/null; grep -c '= ask' "$TB_POLICY") $(grep -c '= allow' "$TB_POLICY")" "17 0"
eq "allow: a cmd: pattern is refused" "$(env -u HERDR_PANE_ID $TB allow 'cmd:ticket:list' 2>&1 >/dev/null | grep -c 'コマンドの中の操作に当たりません'; env -u HERDR_PANE_ID $TB allow 'approve:send:task' >/dev/null 2>&1; echo $?)" "$(printf '1\n1')"
eq "allow: a narrow pattern is one row" "$(: >"$TB_POLICY"; env -u HERDR_PANE_ID $TB allow 'agent:stop:w1')" "* agent:stop w1 = allow"

export TB_POLICY=${_pi_saved[0]}; [[ -n $TB_POLICY ]] || unset TB_POLICY
unset _pi_saved
