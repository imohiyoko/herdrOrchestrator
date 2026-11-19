# shellcheck shell=bash
# 期日(015): --due の検査・list --overdue / --due-within の境界・期日なしは出ない。project は du / du2 だけを使う
export TB_TODAY=2026-03-10
y=$($TB add -p du -t "昨日" --due 2026-03-09)
t=$($TB add -p du -t "今日" --due 2026-03-10)
m=$($TB add -p du -t "明日" --due 2026-03-11)
w=$($TB add -p du -t "来週" --due 2026-03-17)
n=$($TB add -p du -t "期日なし")
eq "due: stored" "$($TB show "$y" | jq -r .due)" "2026-03-09"
eq "due: none is null" "$($TB show "$n" | jq -c .due)" "null"
eq "due: empty --due on add means none" "$($TB show "$($TB add -p du2 -t e --due '')" | jq -c .due)" "null"

# --- 形式の検査(使い方の誤り = 2、行は残らない)
eq "due: bad format slash" "$(rc $TB add -p du2 -t x --due 2026/03/10)" "2"
eq "due: bad format short" "$(rc $TB add -p du2 -t x --due 2026-3-10)" "2"
eq "due: impossible date" "$(rc $TB add -p du2 -t x --due 2026-02-30)" "2"
eq "due: datetime rejected" "$(rc $TB add -p du2 -t x --due 2026-03-10T00:00:00Z)" "2"
eq "due: injection rejected" "$(rc $TB add -p du2 -t x --due "2026-03-10');DROP TABLE tasks;--")" "2"
eq "due: leap day ok" "$(rc $TB add -p du2 -t x --due 2028-02-29)" "0"
eq "due: rejected adds leave no rows" "$($TB list -p du2 | wc -l | tr -d ' ')" "2"
eq "due: update bad date" "$(rc $TB update "$n" --due 2026-13-01)" "2"
eq "due: schema check stops raw bad value" "$(sqlite3 "$TB_DB" "UPDATE tasks SET due='2026-02-30' WHERE id='$n';" 2>/dev/null; echo $?)" "19"

# --- 期限切れ / 期限間近(境界: 昨日・今日・明日)
eq "due: overdue is before today only" "$($TB list -p du --overdue | cut -f1 | tr '\n' ' ')" "$y "
eq "due: within 0 is today only" "$($TB list -p du --due-within 0 | cut -f1 | tr '\n' ' ')" "$t "
eq "due: within 1 is today and tomorrow" "$($TB list -p du --due-within 1 | cut -f1 | tr '\n' ' ')" "$t $m "
eq "due: within 7 includes the 7th day" "$($TB list -p du --due-within 7 | cut -f1 | tr '\n' ' ')" "$t $m $w "
eq "due: within 6 excludes the 7th day" "$($TB list -p du --due-within 6 | cut -f1 | tr '\n' ' ')" "$t $m "
eq "due: has-due" "$($TB list -p du --has-due | wc -l | tr -d ' ')" "4"
eq "due: bad --due-within" "$(rc $TB list --due-within -1)" "2"
eq "due: bad --due-within word" "$(rc $TB list --due-within soon)" "2"
eq "due: bad TB_TODAY" "$(TB_TODAY=2026-02-30 rc $TB list --overdue)" "2"
# 日が変われば同じタスクが期限切れになる(今日を翌日にずらす)
eq "due: today becomes overdue tomorrow" "$(TB_TODAY=2026-03-11 $TB list -p du --overdue | cut -f1 | tr '\n' ' ')" "$y $t "
# 月・年をまたぐ
$TB add -p du2 -t ny --due 2027-01-01 >/dev/null
eq "due: within crosses year end" "$(TB_TODAY=2026-12-31 $TB list -p du2 --due-within 1 | cut -f2- | cut -f3)" "ny"

# --- 期日なしは期限切れにならない / done は出ない
eq "due: no-due never overdue (far future today)" "$(TB_TODAY=9999-12-31 $TB list -p du --overdue | grep -c "^$n"$'\t')" "0"
$TB done "$y" -d out.md -m ok >/dev/null
eq "due: done not overdue" "$($TB list -p du --overdue | wc -l | tr -d ' ')" "0"

# --- update: 変える・外す
$TB update "$n" --due 2026-03-12 >/dev/null
eq "due: update sets" "$($TB show "$n" | jq -r .due)" "2026-03-12"
$TB update "$n" --due '' >/dev/null
eq "due: update '' clears" "$($TB show "$n" | jq -c .due)" "null"

# --- 表示: --long の最後の列 / 並び / json
eq "due: --long last column" "$($TB list -p du --long | grep "^$m"$'\t' | cut -f9,10)" "$(printf '明日\t2026-03-11')"
eq "due: --long no-due is -" "$($TB list -p du --long | grep "^$n"$'\t' | cut -f10)" "-"
eq "due: sort by due (none last)" "$($TB list -p du --sort due | cut -f1 | tr '\n' ' ')" "$y $t $m $w $n "
eq "due: json has due" "$($TB list -p du --json | jq -sr "map(select(.id==\"$w\"))[0].due")" "2026-03-17"
unset TB_TODAY
eq "due: today defaults to local date" "$(rc $TB list --overdue)" "0"
