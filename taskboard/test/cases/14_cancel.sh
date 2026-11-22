# shellcheck shell=bash
# 取り消し(023 の cancelled): tb cancel と、閉じた状態としての扱い(期限・先行チケット待ち・完了報告・スプリント)。project は cx / cx2 だけを使う
export TB_TODAY=2026-03-10
a=$($TB add -p cx -t "やめる" --due 2026-03-01)
b=$($TB add -p cx -t "後続")
$TB link "$a" blocks "$b" >/dev/null
eq "cancel: blocked by an open task" "$(rc $TB claim "$b" --owner w1)" "1"
eq "cancel: overdue before cancel" "$($TB list -p cx --overdue | cut -f1)" "$a"
eq "cancel: ok" "$($TB cancel "$a" -m "方針が変わった")" "$a cancelled"
eq "cancel: status / summary / reported" "$($TB show "$a" | jq -r '[.status, .summary, (.reported_at != null)] | join(" ")')" "cancelled 方針が変わった true"
eq "cancel: list -s cancelled" "$($TB list -p cx -s cancelled | cut -f1,2 | tr '\t' ' ')" "$a cancelled"
eq "cancel: not overdue" "$($TB list -p cx --overdue | wc -l | tr -d ' ')" "0"
eq "cancel: not due-within" "$($TB list -p cx --due-within 30 | wc -l | tr -d ' ')" "0"
eq "cancel: cancelled blocker no longer blocks claim" "$(rc $TB claim "$b" --owner w1)" "0"
eq "cancel: not in pending (not a completion)" "$($TB pending --ids | grep -c "$a")" "0"
eq "cancel: history has the status event" "$($TB history "$a" | grep "$(printf '\tstatus\t')" | cut -f5,6 | tr '\t' ' ')" "todo cancelled"

# --- 閉じた状態も特別扱いしない: 題などは変えられ、別の状態へも動かせる。二重の cancel と claim(todo の分類だけ)は断る
eq "cancel: twice rejected" "$(rc $TB cancel "$a")" "1"
eq "cancel: update of a cancelled task ok" "$(rc $TB update "$a" --title x)" "0"
eq "cancel: claim rejected (not todo-category)" "$(rc $TB claim "$a" --owner w2)" "1"
eq "cancel: update --status cancelled ok (reported_at filled)" "$($TB update "$b" --status cancelled >/dev/null; $TB show "$b" | jq -r '.status+"/"+(.reported_at != null | tostring)')" "cancelled/true"
eq "cancel: cancelled → done (pending report again)" "$($TB done "$b" -d /tmp/x -m ok >/dev/null; $TB pending --ids | grep -c "^$b$")" "1"
eq "cancel: done task can be cancelled (not reported)" "$($TB cancel "$b" >/dev/null; $TB pending --ids | grep -c "^$b$")" "0"
eq "cancel: unknown arg" "$(rc $TB cancel "$b" --nope)" "2"
eq "cancel: missing task" "$(rc $TB cancel T-cx-999)" "1"
r=$($TB add -p cx2 -t r); $TB cancel "$r" >/dev/null
eq "cancel: without reason → summary null" "$($TB show "$r" | jq -c .summary)" "null"

# --- スプリント: cancelled は分母に入れず、ボードの最後の列に出る
s=$($TB sprint-add -p cx -n "Sprint X")
c=$($TB add -p cx -t c --sprint "$s" --points 3); d=$($TB add -p cx -t d --sprint "$s" --points 5)
$TB cancel "$d" >/dev/null
eq "cancel: sprints counts exclude cancelled" "$($TB sprints -p cx | cut -f6,7 | tr '\t' ' ')" "0/1 0/3"
eq "cancel: board json has cancelled column" "$($TB board "$s" --json | jq -c '[.columns | keys_unsorted[-1], (.cancelled | map(.id))]')" "[\"cancelled\",[\"$d\"]]"
eq "cancel: board text has cancelled section last" "$($TB board "$s" | grep '^== ' | tail -1)" "== cancelled =="
eq "cancel: sprint-close carries only open tasks" "$($TB sprint-close "$s")" "$s closed(未完了 1 件を バックログ へ)"

# --- アラートのチケットは tb cancel では変えない
eq "cancel: alert ticket guarded" "$(HERDR_PANE_ID= $TB alert --title 'cx のアラート' >/dev/null; rc $TB cancel "$(sqlite3 "$TB_DB" "SELECT task_id FROM alerts ORDER BY id DESC LIMIT 1;")")" "1"
unset TB_TODAY
