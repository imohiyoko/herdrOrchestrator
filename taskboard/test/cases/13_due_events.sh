# shellcheck shell=bash
# 期限のイベント(tb due-scan。ADR 0003 の段 3)。project は dv だけを使う
unset HERDR_PANE_ID
export TB_TODAY=2026-03-10
dy=$($TB add -p dv -t "昨日" --due 2026-03-09)
dt=$($TB add -p dv -t "今日" --due 2026-03-10)
dm=$($TB add -p dv -t "明日" --due 2026-03-11)
dw=$($TB add -p dv -t "来週" --due 2026-03-17)
dn=$($TB add -p dv -t "期日なし")
dd=$($TB add -p dv -t "済み" --due 2026-03-01); $TB done "$dd" -d o -m ok >/dev/null
dvk() { $TB events -p dv --kind "$1" --limit 100 --json | jq -r '[.task_id, .new, .actor, .ref] | join(" ")' | sort | tr '\n' '|'; }
eq "due-scan: counts" "$($TB due-scan -p dv)" "overdue 1 due_soon 2"
eq "due-scan: overdue event" "$(dvk overdue)" "$dy 2026-03-09 system day:2026-03-10|"
eq "due-scan: due_soon is today and tomorrow" "$(dvk due_soon)" "$dt 2026-03-10 system day:2026-03-10|$dm 2026-03-11 system day:2026-03-10|"
eq "due-scan: once per day" "$($TB due-scan -p dv)" "overdue 0 due_soon 0"
eq "due-scan: --within widens" "$($TB due-scan -p dv --within 7)" "overdue 0 due_soon 1"
eq "due-scan: next day again" "$(TB_TODAY=2026-03-11 $TB due-scan -p dv)" "overdue 2 due_soon 1"
eq "due-scan: no-due and done never" "$($TB events -p dv --limit 100 --json | jq -r 'select(.kind=="overdue" or .kind=="due_soon") | .task_id' | grep -cE "^($dn|$dd)$")" "0"
eq "due-scan: bad --within" "$(rc $TB due-scan --within -1)" "2"
eq "due-scan: bad project" "$(rc $TB due-scan -p "a b")" "2"
eq "due-scan: bad TB_TODAY" "$(TB_TODAY=2026-02-30 rc $TB due-scan -p dv)" "2"
eq "due-scan: shown in history" "$($TB history "$dy" | grep -c overdue)" "2"
unset TB_TODAY
