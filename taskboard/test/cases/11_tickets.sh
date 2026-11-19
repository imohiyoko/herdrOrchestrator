# shellcheck shell=bash
# チケットの構造(014): 種別・優先度・ラベル・説明・リンク・tree・labels。project は tk / tk2 だけを使う
e1=$($TB add -p tk -t "ログイン刷新" --type epic --priority high -l ui,auth)
s1=$($TB add -p tk -t "画面" --type story --parent "$e1" -l ui --desc $'受け入れ条件\nボタンが動く')
t1=$($TB add -p tk -t "API" --parent "$e1" --label auth --label "api" --priority highest)
st=$($TB add -p tk -t "テスト" --type subtask --parent "$s1")
d1=$($TB add -p tk -t "既定値")
eq "tickets: defaults are task/medium/no labels" "$($TB show "$d1" | jq -c '[.type,.priority,.labels,.description]')" '["task","medium",[],null]'
eq "tickets: fields stored" "$($TB show "$e1" | jq -c '[.type,.priority,.labels]')" '["epic","high",["auth","ui"]]'
eq "tickets: description keeps newlines" "$($TB show "$s1" | jq -r .description)" $'受け入れ条件\nボタンが動く'
eq "tickets: labels sorted/unique from repeat + comma" "$($TB show "$t1" | jq -c .labels)" '["api","auth"]'

# --- 不正な値と階層の規則(すべて使い方の誤り = 2)
eq "tickets: bad type" "$(rc $TB add -p tk -t x --type feature)" "2"
eq "tickets: bad priority" "$(rc $TB add -p tk -t x --priority urgent)" "2"
eq "tickets: label with space" "$(rc $TB add -p tk -t x -l 'a b')" "2"
eq "tickets: label with quote" "$(rc $TB add -p tk -t x -l "a'b")" "2"
eq "tickets: label too long" "$(rc $TB add -p tk -t x -l "$(printf 'a%.0s' {1..41})")" "2"
eq "tickets: japanese label ok" "$(rc $TB add -p tk2 -t x -l 認証)" "0"
eq "tickets: subtask needs parent" "$(rc $TB add -p tk -t x --type subtask)" "2"
eq "tickets: subtask not directly under epic" "$(rc $TB add -p tk -t x --type subtask --parent "$e1")" "2"
eq "tickets: subtask can't be a parent" "$(rc $TB add -p tk -t x --parent "$st")" "2"
eq "tickets: epic can't have a parent" "$(rc $TB add -p tk -t x --type epic --parent "$s1")" "2"
eq "tickets: rejected adds leave no rows" "$($TB list -p tk | wc -l | tr -d ' ')" "5"

# --- list: 既定の列は変えない / --long / 絞り込み / 並び
eq "tickets: default list columns unchanged" "$($TB list -p tk | head -1)" "$(printf '%s\ttodo\t-\tログイン刷新' "$e1")"
eq "tickets: --long columns" "$($TB list -p tk --long | head -1 | cut -f1-5,8,9)" "$(printf '%s\ttodo\tepic\thigh\t-\tauth,ui\tログイン刷新' "$e1")"
eq "tickets: --long has created/updated" "$($TB list -p tk --long | head -1 | cut -f6,7 | grep -cE '^[0-9T:Z-]+'$'\t''[0-9T:Z-]+$')" "1"
eq "tickets: filter by label" "$($TB list -p tk -l ui | cut -f1 | tr '\n' ' ')" "$e1 $s1 "
eq "tickets: labels AND" "$($TB list -p tk -l ui -l auth | cut -f1)" "$e1"
eq "tickets: labels AND (comma)" "$($TB list -p tk -l ui,auth | cut -f1)" "$e1"
eq "tickets: bad label in filter is an error" "$(rc $TB list -l 'a b')" "2"
eq "tickets: filter by type" "$($TB list -p tk --type subtask | cut -f1)" "$st"
eq "tickets: filter by priority" "$($TB list -p tk --priority highest | cut -f1)" "$t1"
eq "tickets: filter by parent" "$($TB list --parent "$e1" | cut -f1 | tr '\n' ' ')" "$s1 $t1 "
eq "tickets: sort by priority" "$($TB list -p tk --sort priority | head -2 | cut -f1 | tr '\n' ' ')" "$t1 $e1 "
eq "tickets: bad sort" "$(rc $TB list --sort zz)" "2"
eq "tickets: json labels are arrays" "$($TB list -p tk --json | jq -sc "map(select(.id==\"$e1\"))[0].labels")" '["auth","ui"]'

# --- update: 種別・優先度・説明・ラベルの付け外し
$TB update "$s1" --add-label frontend --remove-label ui --priority low >/dev/null
eq "tickets: add/remove label + priority" "$($TB show "$s1" | jq -c '[.labels,.priority]')" '[["frontend"],"low"]'
$TB update "$s1" --label a,b >/dev/null
eq "tickets: --label replaces" "$($TB show "$s1" | jq -c .labels)" '["a","b"]'
$TB update "$s1" --label '' >/dev/null
eq "tickets: --label '' clears" "$($TB show "$s1" | jq -c .labels)" '[]'
$TB update "$s1" --desc "" >/dev/null
eq "tickets: empty desc clears" "$($TB show "$s1" | jq -r .description)" "null"
eq "tickets: task with children can't become subtask" "$(rc $TB update "$s1" --type subtask)" "2"
eq "tickets: task with parent can't become epic" "$(rc $TB update "$s1" --type epic)" "2"
eq "tickets: parent cycle rejected" "$(rc $TB update "$e1" --type task --parent "$s1")" "2"
eq "tickets: type unchanged after rejected update" "$($TB show "$e1" | jq -r .type)" "epic"
eq "tickets: bad priority in update" "$(rc $TB update "$s1" --priority x)" "2"

# --- tree
eq "tickets: tree of an epic" "$($TB tree "$e1" | cut -f1)" "$(printf '%s\n  %s\n    %s\n  %s' "$e1" "$s1" "$st" "$t1")"
eq "tickets: tree -p lists roots" "$($TB tree -p tk | grep -c '^T-')" "2"
eq "tickets: tree needs id or project" "$(rc $TB tree)" "2"

# --- link: blocks は claim を止める
eq "tickets: link" "$($TB link "$t1" blocks "$s1")" "$t1 blocks $s1"
eq "tickets: link twice is idempotent" "$($TB link "$t1" blocks "$s1")" "$t1 blocks $s1(既にあります)"
eq "tickets: blocks cycle rejected" "$(rc $TB link "$s1" blocks "$t1")" "2"
eq "tickets: self link rejected" "$(rc $TB link "$s1" relates "$s1")" "2"
eq "tickets: bad link type" "$(rc $TB link "$s1" causes "$t1")" "2"
eq "tickets: link to missing task" "$(rc $TB link "$s1" relates T-tk-999)" "1"
$TB link "$d1" relates "$s1" >/dev/null
eq "tickets: show links (incoming)" "$($TB show "$s1" | jq -c '.links | {b: [.blocked_by[].id], r: [.relates[].id]}')" "{\"b\":[\"$t1\"],\"r\":[\"$d1\"]}"
eq "tickets: show links (outgoing)" "$($TB show "$t1" | jq -c '[.links.blocks[] | .id, .status]')" "[\"$s1\",\"todo\"]"
eq "tickets: blocked task can't be claimed" "$(rc $TB claim "$s1" --owner w1)" "1"
eq "tickets: still todo" "$($TB show "$s1" | jq -r .status)" "todo"
$TB claim "$t1" --owner w1 >/dev/null; $TB done "$t1" -d /tmp/x -m ok >/dev/null
eq "tickets: claim ok once blocker is done" "$(rc $TB claim "$s1" --owner w1)" "0"
eq "tickets: unlink" "$($TB unlink "$d1" relates "$s1")" "unlinked $d1 relates $s1"
eq "tickets: unlink missing" "$(rc $TB unlink "$d1" relates "$s1")" "1"
eq "tickets: link row has session + actor" "$(sqlite3 "$TB_DB" "SELECT herdr_session<>'' AND created_by='human' FROM task_links LIMIT 1;")" "1"

# --- labels
eq "tickets: labels with open/total counts" "$($TB labels -p tk)" "$(printf 'api\t0\t1\nauth\t1\t2\nui\t1\t1')"
eq "tickets: labels --json" "$($TB labels -p tk2 --json)" '{"label":"認証","open":1,"total":1}'
