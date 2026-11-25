# shellcheck shell=bash
# tb ticket: 状態の移動(move。どの状態からどの状態へも)と、状態そのものの追加・削除(ticket status)。project は tk / tk2 だけを使う
a=$($TB add -p tk -t "動かす")
eq "ticket: move todo → review" "$($TB ticket move "$a" review)" "$a todo → review"
eq "ticket: same status is a no-op" "$($TB ticket move "$a" review)" "$a はすでに review です"
eq "ticket: unknown status is a usage error" "$(rc $TB ticket move "$a" nope)" "2"
eq "ticket: -d only for done" "$(rc $TB ticket move "$a" review -d /tmp/x)" "2"
eq "ticket: -m to open status → note" "$($TB ticket move "$a" blocked -m 待ち >/dev/null; $TB show "$a" | jq -r '.status+"/"+.note')" "blocked/待ち"
# done へ(-d / -m は任意)。完了報告の対象になり、done の通知が出る
eq "ticket: move to done without -d/-m" "$($TB ticket move "$a" done)" "$a blocked → done"
eq "ticket: done → pending report" "$($TB pending --ids | grep -c "^$a$")" "1"
eq "ticket: done event" "$($TB history "$a" | grep "$(printf '\tstatus\t')" | tail -1 | cut -f5,6 | tr '\t' ' ')" "blocked done"
# 再オープン(done → in_progress)。成果物・要約は残る
b=$($TB add -p tk -t "戻す"); $TB claim "$b" --owner w1 >/dev/null; $TB done "$b" -d /tmp/b.md -m 済み >/dev/null
eq "ticket: reopen done → in_progress" "$($TB ticket move "$b" in_progress)" "$b done → in_progress"
eq "ticket: reopened keeps deliverables/summary" "$($TB show "$b" | jq -r '[.status, .summary, (.deliverables | join(","))] | join(" ")')" "in_progress 済み /tmp/b.md"
eq "ticket: reopened is not pending" "$($TB pending --ids | grep -c "^$b$")" "0"
eq "ticket: done again with -d/-m replaces them" "$($TB ticket move "$b" done -d /tmp/c.md -m 再完了 >/dev/null; $TB show "$b" | jq -r '[.summary, (.deliverables | join(","))] | join(" ")')" "再完了 /tmp/c.md"
# cancelled へ・から
c=$($TB add -p tk -t "やめて戻す")
eq "ticket: move to cancelled → summary, not pending" "$($TB ticket move "$c" cancelled -m 不要 >/dev/null; $TB show "$c" | jq -r '.status+"/"+.summary+"/"+(.reported_at != null | tostring)')" "cancelled/不要/true"
eq "ticket: cancelled → todo, then claimable" "$($TB ticket move "$c" todo >/dev/null; rc $TB claim "$c" --owner w1)" "0"
# 先行チケットを戻すと、後続の claim はまた待つ
p1=$($TB add -p tk -t 先行); p2=$($TB add -p tk -t 後続); $TB link "$p1" blocks "$p2" >/dev/null
$TB ticket move "$p1" done >/dev/null
eq "ticket: closed blocker → claimable" "$($TB claim "$p2" --owner w1 >/dev/null 2>&1; $TB ticket move "$p2" todo >/dev/null; $TB ticket move "$p1" review >/dev/null; rc $TB claim "$p2" --owner w1)" "1"
# 従来のコマンドも tb ticket から
eq "ticket: passthrough list" "$($TB ticket list -p tk -s review | cut -f1)" "$p1"
eq "ticket: no subcommand → usage" "$(rc $TB ticket)" "2"
eq "ticket: unknown subcommand" "$(rc $TB ticket nope)" "2"
# アラートのチケットは専用のコマンドだけ
al=$(HERDR_PANE_ID= $TB alert --title 'tk のアラート' >/dev/null; sqlite3 "$TB_DB" "SELECT task_id FROM alerts ORDER BY id DESC LIMIT 1;")
eq "ticket: alert ticket guarded" "$(rc $TB ticket move "$al" done)" "1"

# --- 状態の追加・削除(設定 statuses に書く)
eq "ticket status: builtin list" "$($TB ticket status 2>/dev/null | cut -f1,2 | tr '\t' ':' | tr '\n' ' ')" "todo:todo in_progress:active review:review blocked:blocked done:done cancelled:cancelled "
eq "ticket status add" "$($TB ticket status add qa review -m 検証待ち --after review | cut -d'(' -f1)" "状態 qa を足しました"
eq "ticket status: added after review" "$($TB ticket status 2>/dev/null | cut -f1 | tr '\n' ' ')" "todo in_progress review qa blocked done cancelled "
eq "ticket status: config keeps it" "$($TB config show statuses 2>/dev/null | grep -c '^qa review = 検証待ち$')" "1"
eq "ticket status: json" "$($TB ticket status --json | jq -c 'select(.status=="qa") | [.category, .description, .tickets]')" '["review","検証待ち",0]'
eq "ticket status add: duplicate" "$(rc $TB ticket status add qa review)" "1"
eq "ticket status add: bad category" "$(rc $TB ticket status add x nope)" "2"
eq "ticket status add: human only" "$(HERDR_PANE_ID=wW:p1 rc $TB ticket status add zz todo)" "1"
d=$($TB add -p tk2 -t "qa へ"); $TB ticket move "$d" qa >/dev/null
eq "ticket status: count" "$($TB ticket status --json | jq -r 'select(.status=="qa") | .tickets')" "1"
eq "ticket status rm: refuses while tickets remain" "$(rc $TB ticket status rm qa)" "1"
eq "ticket status rm --to moves tickets" "$($TB ticket status rm qa --to review | head -1)" "状態 qa のチケット 1 件を review へ移しました"
eq "ticket status rm: moved" "$($TB show "$d" | jq -r .status)" "review"
eq "ticket status rm: gone" "$($TB ticket status 2>/dev/null | cut -f1 | grep -c '^qa$')" "0"
eq "ticket status rm: can't remove the last done" "$(rc $TB ticket status rm done --to review)" "1"
$TB config unset statuses --shared >/dev/null

# --- チケットを消す(ticket rm)。コメント・履歴・リンクも消え、相手の履歴には unlink が残る。子があれば一緒に指定する
r1=$($TB add -p tk -t 消す親); r2=$($TB add -p tk -t 消す子 --parent "$r1"); r3=$($TB add -p tk -t 残す)
$TB link "$r1" blocks "$r3" >/dev/null; sqlite3 "$TB_DB" "INSERT INTO comments (task_id, kind, text, from_name, from_pane, from_session, created_at, herdr_session) SELECT id, 'progress', '途中', 'w1', 'wW:p1', 's', created_at, herdr_session FROM tasks WHERE id='$r2';"
eq "ticket rm: refuses while a child remains" "$($TB ticket rm "$r1" 2>&1; echo "rc=$?")" "tb: $r1 には子チケットがあります: $r2(子も一緒に tb ticket rm するか、先に tb update で親を外す)
rc=1"
eq "ticket rm: nothing removed on refusal" "$($TB show "$r1" | jq -r .id)" "$r1"
eq "ticket rm: parent and child together" "$($TB ticket rm "$r1" "$r2")" "$r1 を消しました(コメント 0 件・履歴 3 件・リンク 1 件)
$r2 を消しました(コメント 1 件・履歴 1 件・リンク 0 件)"
eq "ticket rm: rows are gone" "$(sqlite3 "$TB_DB" "SELECT (SELECT COUNT(*) FROM tasks WHERE id IN ('$r1','$r2')) || ' ' || (SELECT COUNT(*) FROM comments WHERE task_id IN ('$r1','$r2')) || ' ' || (SELECT COUNT(*) FROM task_events WHERE task_id IN ('$r1','$r2')) || ' ' || (SELECT COUNT(*) FROM task_links WHERE '$r1' IN (from_id, to_id))")" "0 0 0 0"
eq "ticket rm: the other side keeps an unlink event" "$($TB history "$r3" | cut -f4,5 | tail -1)" "unlink	blocked_by $r1"
eq "ticket rm: missing ticket → 1" "$(rc $TB ticket rm "$r1")" "1"
eq "ticket rm: no id → 2" "$(rc $TB ticket rm)" "2"
eq "ticket rm: bad id → 2" "$(rc $TB ticket rm nope)" "2"
eq "ticket rm: alert ticket guarded" "$(rc $TB ticket rm "$al")" "1"
# agent からは ask(Q チケットになり、人が承認するまで消さない)
eq "ticket rm from an agent pane → ask" "$(FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s"}}]' HERDR_PANE_ID=wW:p1 rc $TB ticket rm "$r3") $($TB show "$r3" | jq -r .id)" "20 $r3"
