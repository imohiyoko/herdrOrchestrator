# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態を使うものがある)
# --- 代行依頼(許可リストはテスト用の一時ファイル。本物は書き換えない)
export TB_ALLOWLIST=$TMP/allowlist.txt
printf '# comment\n\ngh pr view\ngh pr diff\ngit log\ngit status\nherdr agent list\n' >"$TB_ALLOWLIST"
pr()    { $TB proxy-request "$a1" --why w --expect e "$@"; }          # → "<id> <status>"
verdict() { local id; id=$(pr --cmd "$1" --effect "${2:-read-only}" | cut -d' ' -f1); $TB proxy-check "$id"; }
first()   { head -1; }

eq "proxy: register ok" "$(pr --cmd 'gh pr view 12' --effect read-only)" "1 open"
eq "proxy: effect required" "$(rc $TB proxy-request $a1 --cmd 'git log' --why w)" "2"
eq "proxy: effect invalid rejected" "$(rc $TB proxy-request $a1 --cmd 'git log' --why w --effect readonly)" "2"
eq "proxy: unknown task rejected" "$(rc $TB proxy-request T-nope-001 --cmd 'git log' --why w --effect write)" "1"
eq "proxy: no row on rejection" "$($TB proxy-list --json | wc -l | tr -d ' ')" "1"

# 許可リスト内 / 外
eq "allow: exact entry"            "$(verdict 'git status' | first)" "allow"
eq "allow: with args"              "$(verdict 'gh pr view 12 --json title' | first)" "allow"
eq "human: not in allowlist"       "$(verdict 'gh pr merge 12' | first)" "human"
eq "human: word boundary (git logx)" "$(verdict 'git logx' | first)" "human"
eq "human: prefix only partially (gh pr)" "$(verdict 'gh pr' | first)" "human"
eq "human: declared write even if listed" "$(verdict 'git status' write | first)" "human"
eq "human: declared external even if listed" "$(verdict 'git log' external | first)" "human"
eq "no mismatch for plain human" "$(verdict 'curl https://example.invalid' | grep -c '^mismatch')" "0"

# メタ文字・危険フラグ: 先頭一致していても必ず human、read-only 申告なら mismatch
printf 'git diff\n' >>"$TB_ALLOWLIST"
for c in 'git log > /tmp/x' 'git log | head' 'git log; rm x' 'git log && rm x' 'git log || rm x' \
         'git log $(id)' 'git log `id`' 'git log < /etc/hosts' 'git log &' 'git log $HOME' \
         $'git log\nrm x' 'gh pr view 1 -X POST' 'gh pr view 1 -XDELETE' 'gh pr view 1 --request PUT' \
         'gh pr view 1 --method=patch' 'gh pr view 1 --approve' 'gh pr view 1 --merge' \
         'gh pr view 1 --body x' 'gh pr view 1 --body-file f' 'gh pr view 1 --add-label x' 'git diff --output=f'; do
  out=$(verdict "$c")
  eq "meta → human: ${c//$'\n'/\\n}" "$(first <<<"$out")" "human"
  eq "meta + read-only → mismatch: ${c//$'\n'/\\n}" "$(grep -c '^mismatch' <<<"$out")" "1"
done
out=$(verdict "git log > $TMP/side-effect" write)
eq "meta + declared write → human, no mismatch" "$(first <<<"$out") $(grep -c '^mismatch' <<<"$out")" "human 0"
eq "cmd is never executed (no side effect)" "$([[ -e $TMP/side-effect ]] && echo exists || echo none)" "none"

# 許可リストが読めないときは安全側
id=$(pr --cmd 'git status' --effect read-only | cut -d' ' -f1)
eq "missing allowlist → human" "$(TB_ALLOWLIST=$TMP/none.txt $TB proxy-check $id | first)" "human"

# resolve / list / denied の再登録
pid=$(pr --cmd 'gh pr diff 7' --effect read-only | cut -d' ' -f1)
eq "resolve bad status" "$(rc $TB proxy-resolve $pid --status open)" "2"
eq "resolve pending-human" "$($TB proxy-resolve $pid --status pending-human --result 'wait')" "$pid pending-human"
eq "list --status open excludes it" "$($TB proxy-list --status open | cut -f1 | grep -c "^$pid\$")" "0"
eq "resolve denied" "$($TB proxy-resolve $pid --status denied --result 'no')" "$pid denied"
eq "denied is final" "$(rc $TB proxy-resolve $pid --status done)" "1"
eq "list --status denied" "$($TB proxy-list --status denied | cut -f1)" "$pid"
eq "re-register of denied cmd → denied at once" "$(pr --cmd 'gh pr diff 7' --effect read-only 2>/dev/null | cut -d' ' -f2)" "denied"
eq "re-register records result" "$($TB proxy-list --json | jq -r 'select(.cmd=="gh pr diff 7" and .id!='"$pid"') | .result' | head -1 | grep -c "拒否済み")" "1"
eq "similar but different cmd stays open" "$(pr --cmd 'gh pr diff 8' --effect read-only | cut -d' ' -f2)" "open"
eq "denied request checks as human" "$($TB proxy-check "$pid" | first)" "human"
did=$($TB proxy-list --json | jq -r 'select(.cmd=="gh pr view 12") | .id' | head -1)
eq "resolve done with file path stored as-is" "$($TB proxy-resolve $did --status done --result /tmp/out.txt)" "$did done"
eq "result stored" "$($TB proxy-list --json | jq -r "select(.id==$did) | .result")" "/tmp/out.txt"

# 出力の形(Go 版と突き合わせる)
eq "list: tsv columns" "$($TB proxy-list --status denied | head -1)" "$pid	denied	read-only	$a1	gh pr diff 7"
eq "list: tab/newline in cmd flattened" "$(pr --cmd $'git log\t-1\nx' --effect write >/dev/null; $TB proxy-list | tail -1 | cut -f5)" "git log -1 x"
eq "re-register of denied: message" "$(pr --cmd 'gh pr diff 7' --effect read-only 2>&1 >/dev/null)" "tb: 同一 cmd が人に拒否済み(依頼 $pid)。同じ依頼は出し直さず、別の方法を提案すること"
eq "check: allow output" "$(verdict 'git status')" $'allow\nmatch: git status'
eq "check: human output" "$(verdict 'git log | head')" $'human\nreason: 危険な構文を含む: パイプ/||\nmismatch: 申告は read-only だが、コマンドに パイプ/|| を含む'
eq "check: several reasons" "$(verdict 'curl -X POST x > y' write)" $'human\nreason: 危険な構文を含む: リダイレクト(>)、書き込み系の HTTP メソッド\nreason: 申告 effect が write\nreason: 許可リストにない'
eq "check: unreadable allowlist reason" "$(TB_ALLOWLIST=$TMP/none.txt $TB proxy-check $id | tail -1)" "reason: 許可リストを読めない($TMP/none.txt)。安全側で human"
eq "request: --cmd missing message" "$($TB proxy-request $a1 --why w --effect write 2>&1 | head -1)" "tb: proxy-request: --cmd が必要です"
eq "request: blank --why" "$(rc $TB proxy-request $a1 --cmd 'git log' --why ' ' --effect write)" "2"
eq "request: cmd trimmed" "$(id2=$(pr --cmd '  git log -2  ' --effect write | cut -d' ' -f1); $TB proxy-list --json | jq -r "select(.id==$id2) | .cmd")" "git log -2"
eq "resolve: unknown id" "$($TB proxy-resolve 99999 --status done 2>&1; echo "rc=$?")" $'tb: 代行依頼が見つかりません: 99999\nrc=1'
eq "resolve: final message" "$($TB proxy-resolve $pid --status done 2>&1)" "tb: 解決済み(denied)の依頼は変更できません: $pid"
eq "check: bad id" "$(rc $TB proxy-check x)" "2"
eq "check: unknown id" "$(rc $TB proxy-check 99999)" "1"
eq "list: bad status" "$(rc $TB proxy-list --status nope)" "2"

# 新しい名前(tb proxy <サブコマンド>)は古い名前と同じに動く
eq "proxy request: same as proxy-request" "$($TB proxy request $a1 --cmd 'git status' --why w --effect read-only | cut -d' ' -f2)" "open"
nid=$($TB proxy list --json | jq -r 'select(.cmd=="git status") | .id' | tail -1)
eq "proxy check: same output" "$($TB proxy check $nid)" "$($TB proxy-check $nid)"
eq "proxy list: same output" "$($TB proxy list --status open)" "$($TB proxy-list --status open)"
eq "proxy resolve" "$($TB proxy resolve $nid --status done)" "$nid done"
eq "proxy: no subcommand → usage on stdout, exit 2" "$($TB proxy 2>/dev/null | head -1; echo "rc=${PIPESTATUS[0]}")" $'tb proxy request <task-id> --cmd C --why W --effect read-only|write|external [--expect E]   → "<id> <status>"\nrc=2'
eq "proxy help: exit 0, lists subcommands" "$($TB proxy help | grep -c '^tb proxy ')" "4"
eq "proxy: unknown subcommand" "$($TB proxy bogus 2>&1; echo "rc=$?")" $'tb: proxy: 不明なサブコマンド bogus(request / list / resolve / check)\n(tb help で使い方)\nrc=2'
