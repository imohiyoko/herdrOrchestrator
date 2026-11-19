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

# --- レビュー指摘の回帰
# subtask の子があるタスクは epic にできない(subtask が epic の直下になる)
ta=$($TB add -p tk3 -t a); tb_=$($TB add -p tk3 -t b --type subtask --parent "$ta")
eq "tickets: task with subtask child can't become epic" "$(rc $TB update "$ta" --type epic)" "2"
tc=$($TB add -p tk3 -t c); $TB add -p tk3 -t d --type story --parent "$tc" >/dev/null
eq "tickets: task with story child can become epic" "$(rc $TB update "$tc" --type epic)" "0"
# 長い親子(100 段超)でも循環を見つける
prev=$($TB add -p tk4 -t p0); first=$prev
for _i in $(seq 1 105); do prev=$($TB add -p tk4 -t "p$_i" --parent "$prev"); done
eq "tickets: cycle found through a 105-level chain" "$(rc $TB update "$first" --parent "$prev")" "2"
eq "tickets: root still has no parent" "$($TB show "$first" | jq -r .parent_id)" "null"
# ラベル * はパス名展開されない
tg=$($TB add -p tk5 -t glob -l '*')
eq "tickets: label * is matched literally" "$($TB list -p tk5 -l '*' | cut -f1)" "$tg"
# 付け外しは書く時点の labels に当てる
$TB update "$tg" --add-label x >/dev/null; $TB update "$tg" --add-label y --remove-label '*' >/dev/null
eq "tickets: add/remove applies to stored labels" "$($TB show "$tg" | jq -c .labels)" '["x","y"]'

# --- 並行する更新: 事前検査のあと書き込みの直前に別の更新が確定しても、規則を破らない
# PATH に置いた sqlite3 の包みが、tb の書き込みトランザクション(BEGIN IMMEDIATE)の直前に 1 度だけ RACE_SQL を確定させる
_real_sqlite=$(command -v sqlite3); mkdir -p "$TMP/race-bin"
cat >"$TMP/race-bin/sqlite3" <<WRAP
#!/usr/bin/env bash
if [[ -n \${RACE_SQL-} && ! -e \$RACE_MARK && "\$*" == *"BEGIN IMMEDIATE;"* ]]; then
  : >"\$RACE_MARK"; "$_real_sqlite" "\$TB_DB" "\$RACE_SQL"
fi
exec "$_real_sqlite" "\$@"
WRAP
chmod +x "$TMP/race-bin/sqlite3"
ra=$($TB add -p tk6 -t a); rb=$($TB add -p tk6 -t b)
eq "tickets(race): --type epic loses to a parent set in between" \
  "$(PATH="$TMP/race-bin:$PATH" RACE_MARK=$TMP/race1 RACE_SQL="UPDATE tasks SET parent_id='$rb' WHERE id='$ra'" rc $TB update "$ra" --type epic)" "2"
eq "tickets(race): no epic with a parent" "$($TB show "$ra" | jq -c '[.type,.parent_id]')" "[\"task\",\"$rb\"]"
rc_=$($TB add -p tk6 -t c --type story); rd=$($TB add -p tk6 -t d --type story)
eq "tickets(race): parent set loses to a subtask change in between" \
  "$(PATH="$TMP/race-bin:$PATH" RACE_MARK=$TMP/race2 RACE_SQL="UPDATE tasks SET type='subtask', parent_id='$rd' WHERE id='$rc_'" rc $TB update "$ra" --parent "$rc_")" "2"
eq "tickets(race): subtask did not become a parent" "$($TB show "$ra" | jq -r .parent_id)" "$rb"
re=$($TB add -p tk6 -t e); rf=$($TB add -p tk6 -t f)
eq "tickets(race): blocks cycle loses to the reverse link in between" \
  "$(PATH="$TMP/race-bin:$PATH" RACE_MARK=$TMP/race3 RACE_SQL="INSERT INTO task_links (from_id,link_type,to_id,created_by,created_at,herdr_session) VALUES ('$rf','blocks','$re','t','t',(SELECT herdr_session FROM tasks WHERE id='$re'))" rc $TB link "$re" blocks "$rf")" "2"
eq "tickets(race): only the reverse link exists" "$(sqlite3 "$TB_DB" "SELECT from_id||'>'||to_id FROM task_links WHERE from_id IN ('$re','$rf');")" "$rf>$re"
$TB update "$re" --add-label keep >/dev/null
PATH="$TMP/race-bin:$PATH" RACE_MARK=$TMP/race4 RACE_SQL="UPDATE tasks SET labels='[\"keep\",\"other\"]' WHERE id='$re'" $TB update "$re" --add-label mine >/dev/null
eq "tickets(race): concurrent label add is kept" "$($TB show "$re" | jq -c .labels)" '["keep","mine","other"]'

# --- 外部リンク(Slack・PR・ドキュメントの URL)
x1=$($TB add -p tk7 -t refs --ref https://acme.slack.com/archives/C1/p123 --ref pr=https://github.com/o/r/pull/1 --ref https://example.invalid/doc)
eq "tickets: refs stored with kinds" "$($TB show "$x1" | jq -c '[.refs[] | .kind]')" '["slack","pr","link"]'
eq "tickets: refs keep url" "$($TB show "$x1" | jq -r '.refs[0].url')" "https://acme.slack.com/archives/C1/p123"
eq "tickets: github url gets github kind" "$($TB show "$($TB add -p tk7 -t g --ref https://github.com/o/r/issues/2)" | jq -r '.refs[0].kind')" "github"
eq "tickets: bad ref url" "$(rc $TB add -p tk7 -t x --ref 'javascript:alert(1)')" "2"
eq "tickets: ref url with quote" "$(rc $TB add -p tk7 -t x --ref 'https://a/"b')" "2"
eq "tickets: bad ref kind" "$(rc $TB add -p tk7 -t x --ref 'Bad=https://a/b')" "2"
eq "tickets: duplicate ref url kept once" "$($TB show "$($TB add -p tk7 -t d --ref https://a/b --ref doc=https://a/b)" | jq -c '.refs')" '[{"kind":"link","url":"https://a/b"}]'
$TB update "$x1" --add-ref doc=https://example.invalid/doc --remove-ref https://github.com/o/r/pull/1 >/dev/null
eq "tickets: add-ref replaces kind, remove-ref by url" "$($TB show "$x1" | jq -c '[.refs[] | .kind]')" '["slack","doc"]'
eq "tickets: list --json refs are arrays" "$($TB list -p tk7 --json | jq -sc 'map(select(.id=="'"$x1"'"))[0].refs | length')" "2"
