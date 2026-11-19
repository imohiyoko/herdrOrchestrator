# shellcheck shell=bash
# スプリントとボード(017)。project は sp / sp2 だけを使う
s1=$($TB sprint-add -p sp -n "Sprint 1" --start 2026-11-17 --end 2026-11-30 --goal "ログイン刷新")
s2=$($TB sprint-add -p sp -n "Sprint 2")
eq "sprints: ids" "$s1 $s2" "$(sqlite3 "$TB_DB" "SELECT 'S-'||MIN(id)||' S-'||MAX(id) FROM sprints WHERE project='sp';")"
eq "sprints: bad date" "$(rc $TB sprint-add -p sp -n x --start 2026-02-30)" "2"
eq "sprints: end before start" "$(rc $TB sprint-add -p sp -n x --start 2026-11-10 --end 2026-11-01)" "2"
eq "sprints: name required" "$(rc $TB sprint-add -p sp)" "2"
ox=$($TB sprint-add -p sp2 -n other)
a=$($TB add -p sp -t A --sprint "$s1" --points 3 -l ui); b=$($TB add -p sp -t B --sprint "$s1" --points 5 --priority high)
c=$($TB add -p sp -t C); d=$($TB add -p sp -t D)
eq "sprints: task stored with sprint/points" "$($TB show "$a" | jq -c '[.sprint_id, .points]')" "[${s1#S-},3]"
eq "sprints: other project's sprint rejected" "$(rc $TB add -p sp -t x --sprint "$ox")" "2"
eq "sprints: missing sprint" "$(rc $TB add -p sp -t x --sprint S-99999)" "1"
eq "sprints: bad points" "$(rc $TB add -p sp -t x --points -1)" "2"
$TB update "$c" --sprint "$s1" --points 2 >/dev/null
eq "sprints: update moves into sprint" "$($TB list --sprint "$s1" | cut -f1 | tr '\n' ' ')" "$a $b $c "
eq "sprints: backlog" "$($TB list -p sp --sprint backlog | cut -f1)" "$d"
eq "sprints: --long has sprint and points" "$($TB list -p sp --long | head -1 | cut -f11,12)" "$(printf '%s\t3' "$s1")"

# --- 開始は project に 1 つだけ
eq "sprints: start" "$($TB sprint-start "$s1")" "$s1 active"
eq "sprints: second active rejected" "$(rc $TB sprint-start "$s2")" "1"
eq "sprints: other project can be active" "$(rc $TB sprint-start "$ox")" "0"
eq "sprints: start twice rejected" "$(rc $TB sprint-start "$s1")" "1"

# --- ボード
$TB claim "$a" --owner w1 >/dev/null; $TB done "$a" -d /tmp/x -m ok >/dev/null
eq "board: active sprint of project" "$($TB board -p sp | head -1)" "$s1 Sprint 1 [active] 2026-11-17..2026-11-30"
eq "board: todo column priority order" "$($TB board -p sp | sed -n '/== todo ==/,/== in_progress ==/p' | grep -c '^T-')" "2"
eq "board: first todo is the high one" "$($TB board -p sp | sed -n '/== todo ==/{n;p;}' | cut -f1)" "$b"
eq "board: json columns" "$($TB board "$s1" --json | jq -c '.columns | map_values(length)')" '{"todo":2,"in_progress":0,"review":0,"blocked":0,"done":1}'
eq "board: label filter" "$($TB board "$s1" -l ui --json | jq -c '[.columns[][] | .id]')" "[\"$a\"]"
eq "board: no active sprint" "$(rc $TB board -p nosuch)" "1"
eq "sprints: list counts" "$($TB sprints -p sp | head -1 | cut -f1,2,6,7)" "$(printf '%s\tactive\t1/3\t3/10' "$s1")"

# --- 閉じる: 未完了は --carry 先(無ければバックログ)へ
eq "sprints: close with carry" "$($TB sprint-close "$s1" --carry "$s2")" "$s1 closed(未完了 2 件を $s2 へ)"
eq "sprints: done task stays in closed sprint" "$($TB show "$a" | jq -r .sprint_id)" "${s1#S-}"
eq "sprints: carried tasks" "$($TB list --sprint "$s2" | cut -f1 | tr '\n' ' ')" "$b $c "
eq "sprints: closed sprint can't take tasks" "$(rc $TB update "$d" --sprint "$s1")" "2"
eq "sprints: close twice" "$(rc $TB sprint-close "$s1")" "1"
eq "sprints: carry to another project rejected" "$(rc $TB sprint-close "$s2" --carry "$ox")" "2"
$TB sprint-start "$s2" >/dev/null
eq "sprints: close to backlog" "$($TB sprint-close "$s2")" "$s2 closed(未完了 2 件を バックログ へ)"
eq "sprints: backlog after close" "$($TB list -p sp --sprint backlog | cut -f1 | tr '\n' ' ')" "$b $c $d "
$TB update "$d" --sprint "" >/dev/null
eq "sprints: --sprint '' keeps backlog" "$($TB show "$d" | jq -r .sprint_id)" "null"
eq "sprints: json" "$($TB sprints -p sp --state closed --json | jq -sc 'map(.id)')" "[\"$s1\",\"$s2\"]"
# 書く時点でスプリントが閉じていれば入れない(PATH の sqlite3 の包みで、書き込みの直前に閉じる)
s3=$($TB sprint-add -p sp -n "Sprint 3")
_real_sqlite=$(command -v sqlite3); mkdir -p "$TMP/race-bin2"
cat >"$TMP/race-bin2/sqlite3" <<WRAP
#!/usr/bin/env bash
if [[ -n \${RACE_SQL-} && ! -e \$RACE_MARK && "\$*" == *"BEGIN IMMEDIATE;"* ]]; then
  : >"\$RACE_MARK"; "$_real_sqlite" "\$TB_DB" "\$RACE_SQL"
fi
exec "$_real_sqlite" "\$@"
WRAP
chmod +x "$TMP/race-bin2/sqlite3"
eq "sprints(race): sprint closed in between" \
  "$(PATH="$TMP/race-bin2:$PATH" RACE_MARK=$TMP/srace1 RACE_SQL="UPDATE sprints SET state='closed' WHERE id=${s3#S-}" rc $TB update "$d" --sprint "$s3")" "2"
eq "sprints(race): task still in backlog" "$($TB show "$d" | jq -r .sprint_id)" "null"
eq "sprints(race): add into a sprint closed in between" \
  "$(PATH="$TMP/race-bin2:$PATH" RACE_MARK=$TMP/srace2 RACE_SQL="UPDATE sprints SET state='closed' WHERE id=$($TB sprint-add -p sp -n s4 | cut -c3-)" rc $TB add -p sp -t late --sprint "S-$(sqlite3 "$TB_DB" "SELECT MAX(id) FROM sprints;")")" "2"
eq "sprints(race): no task added" "$($TB list -p sp | grep -c late)" "0"
