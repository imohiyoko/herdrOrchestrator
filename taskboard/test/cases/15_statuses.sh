# shellcheck shell=bash
# 状態を設定で差し替える(設定 statuses と system-labels、024 の CHECK 外し)。tb のコマンドは名前ではなく分類で判断する。
# project は st / st2 だけを使う。設定はファイル(TB_STATUSES / TB_SYSTEM_LABELS)で渡し、最後に戻す
_st_saved_env=${TB_STATUSES-__unset__}
export TB_STATUSES=$TMP/statuses.conf TB_TODAY=2026-04-10
rm -f "$TB_STATUSES"

# --- 設定が無ければ組み込みの 6 つ(今までどおり)
d=$($TB add -p st -t "既定")
eq "statuses: no config → new task is todo" "$($TB show "$d" | jq -r .status)" "todo"
eq "statuses: no config → usage lists builtin" "$($TB list -s nope 2>&1 | head -1)" "tb: status は todo in_progress review done blocked cancelled のどれか: nope"

# --- 書式の検査(誤りがあれば何も書かない)
eq "statuses: unknown category refused" "$(printf 'a todo\nb doing\nc done\n' | rc $TB config set statuses)" "1"
eq "statuses: missing active refused" "$(printf 'a todo\nc done\n' | rc $TB config set statuses)" "1"
eq "statuses: glob character refused" "$(printf 'a* todo\nb active\nc done\n' | rc $TB config set statuses)" "1"
eq "statuses: duplicate refused" "$(printf 'a todo\na active\nc done\n' | rc $TB config set statuses)" "1"
eq "statuses: three words refused" "$(printf 'a todo extra\nb active\nc done\n' | rc $TB config set statuses)" "1"
eq "statuses: nothing written on error" "$([[ -e $TB_STATUSES ]] && echo y || echo n)" "n"

# --- 名前を変え、足す。書いた順が一覧の順(ボードは分類の順)
printf '%s\n' '# 状態 分類 = 説明' 'backlog todo = 未着手' 'doing active = 作業中' 'shipped done = 完了' 'qa review = 確認待ち' \
  'waiting review' 'stuck blocked' 'dropped cancelled  # 取り消し' | $TB config set statuses >/dev/null
eq "statuses: config list shows key" "$($TB config list | grep -c '^statuses ')" "1"
eq "statuses: usage lists configured order" "$($TB list -s nope 2>&1 | head -1)" "tb: status は backlog doing shipped qa waiting stuck dropped のどれか: nope"
a=$($TB add -p st -t "流れ" --due 2026-04-01)
eq "statuses: add uses first todo-category status" "$($TB show "$a" | jq -r .status)" "backlog"
eq "statuses: removed builtin name can't be filtered" "$(rc $TB list -s todo)" "2"
eq "statuses: list -s with configured name" "$($TB list -p st -s backlog | cut -f1)" "$a"
eq "statuses: overdue includes open custom status" "$($TB list -p st --overdue | cut -f1)" "$a"
eq "statuses: claim from todo-category → first active" "$($TB claim "$a" --owner w1 >/dev/null; $TB show "$a" | jq -r .status)" "doing"
eq "statuses: claim twice fails (not todo-category)" "$(rc $TB claim "$a" --owner w2)" "1"
eq "statuses: update --status to added review status" "$($TB update "$a" --status waiting >/dev/null; $TB show "$a" | jq -r .status)" "waiting"
eq "statuses: update --status to done-category is a usage error" "$(rc $TB update "$a" --status shipped)" "2"
eq "statuses: update --status to cancelled-category is a usage error" "$(rc $TB update "$a" --status dropped)" "2"
eq "statuses: update --status to unknown is a usage error" "$(rc $TB update "$a" --status in_progress)" "2"
eq "statuses: block → first blocked" "$($TB block "$a" -m 待ち)" "$a stuck"
$TB update "$a" --status doing >/dev/null
eq "statuses: done → first done" "$($TB done "$a" -d /tmp/x -m ok >/dev/null; $TB show "$a" | jq -r .status)" "shipped"
eq "statuses: done task is pending report" "$($TB pending --ids | grep -c "^$a$")" "1"
eq "statuses: done twice says its name" "$($TB done "$a" -d /tmp/x -m ok 2>&1)" "tb: すでに shipped です: $a"
eq "statuses: closed (done-category) can't be updated" "$(rc $TB update "$a" --title x)" "1"
eq "statuses: closed not overdue" "$($TB list -p st --overdue | wc -l | tr -d ' ')" "0"
c=$($TB add -p st -t "やめる")
eq "statuses: cancel → first cancelled" "$($TB cancel "$c")" "$c dropped"
eq "statuses: cancelled not pending" "$($TB pending --ids | grep -c "^$c$")" "0"
# 先行チケット(blocks)が閉じた分類なら claim できる
b1=$($TB add -p st -t 先行); b2=$($TB add -p st -t 後続); $TB link "$b1" blocks "$b2" >/dev/null
eq "statuses: open blocker blocks claim" "$(rc $TB claim "$b2" --owner w1)" "1"
$TB cancel "$b1" >/dev/null
eq "statuses: closed blocker no longer blocks" "$(rc $TB claim "$b2" --owner w1)" "0"

# --- 組み込みの名前は設定から外しても組み込みの分類で数える(アラート・提案のトリガーが書く。外した状態のまま残った行)
o=$($TB add -p st2 -t "古い done" --due 2026-04-01)
sqlite3 "$TB_DB" "UPDATE tasks SET status='done' WHERE id='$o';"
eq "statuses: leftover builtin done is closed (not overdue)" "$($TB list -p st2 --overdue | wc -l | tr -d ' ')" "0"
eq "statuses: leftover builtin done can't be updated" "$(rc $TB update "$o" --title x)" "1"
o2=$($TB add -p st2 -t "古い in_progress"); sqlite3 "$TB_DB" "UPDATE tasks SET status='in_progress', owner_agent='w9' WHERE id='$o2';"
eq "statuses: leftover builtin in_progress counts as held (agents open_tasks)" \
  "$(sqlite3 "$TB_DB" "INSERT INTO agents (herdr_session, name, state, created_by, created_at, updated_at) VALUES ('default','w9','running','human','2026-04-10T00:00:00Z','2026-04-10T00:00:00Z');" 2>/dev/null; $TB agents --json 2>/dev/null | jq -r 'select(.name=="w9") | .open_tasks')" "1"

# --- ボード: 列は分類の順(同じ分類は書いた順)。設定に無い状態のチケットが残っていれば、その列も後ろに足す
s=$($TB sprint-add -p st2 -n s1); $TB sprint-start "$s" >/dev/null
$TB update "$o2" --sprint "$s" >/dev/null 2>&1 || sqlite3 "$TB_DB" "UPDATE tasks SET sprint_id=${s#S-} WHERE id='$o2';"
k=$($TB add -p st2 -t k --sprint "$s")
eq "statuses: board columns follow categories, leftovers appended" "$($TB board "$s" --json | jq -r '.columns | keys_unsorted | join(" ")')" "backlog doing qa waiting stuck shipped dropped in_progress"
eq "statuses: board card in configured column" "$($TB board "$s" --json | jq -r '.columns.backlog[0].id')" "$k"
eq "statuses: board text has column header" "$($TB board "$s" | grep -c '^== backlog ==$')" "1"
eq "statuses: sprint counts use categories" "$($TB sprints -p st2 | cut -f6)" "0/2"

# --- 設定が読めない本文(手で壊した)なら、警告して組み込みに戻る
printf 'broken\n' >"$TB_STATUSES"
eq "statuses: broken config → warning" "$($TB list -p st -s todo 2>&1 >/dev/null | grep -c '設定 statuses に不正な行')" "1"
eq "statuses: broken config → builtin statuses" "$(rc $TB list -p st -s todo)" "0"
rm -f "$TB_STATUSES"

# --- tb 自身が付けるラベル(system-labels)
export TB_SYSTEM_LABELS=$TMP/system-labels.conf; rm -f "$TB_SYSTEM_LABELS"
eq "system-labels: unknown key refused" "$(printf 'nope = x\n' | rc $TB config set system-labels)" "1"
eq "system-labels: team without {team} refused" "$(printf 'team = squad\n' | rc $TB config set system-labels)" "1"
eq "system-labels: bad label refused" "$(printf 'deadline = due date\n' | rc $TB config set system-labels)" "1"
eq "system-labels: ok" "$(printf 'deadline = 期限\nteam = squad/{team}\n' | rc $TB config set system-labels)" "0"
# 用意したラベル(設定 labels)があっても、tb 自身が付けるラベルは書かなくても付けられる(名前は system-labels のもの)
export TB_LABELS=$TMP/st-labels.conf; printf 'ui\n' >"$TB_LABELS"
eq "system-labels: configured deadline label is a system label" "$(rc $TB add -p st -t 期限つき -l 期限)" "0"
eq "system-labels: old deadline name is no longer a system label" "$(rc $TB add -p st -t 期限つき -l deadline)" "2"
unset TB_LABELS TB_SYSTEM_LABELS

if [[ $_st_saved_env == __unset__ ]]; then unset TB_STATUSES; else export TB_STATUSES=$_st_saved_env; fi
unset TB_TODAY _st_saved_env a b1 b2 c d k o o2 s
