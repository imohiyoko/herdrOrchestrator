# shellcheck shell=bash
# チケットの履歴(018 の task_events。ADR 0003 の段 1)。project は ev / ev2 だけを使う
unset HERDR_PANE_ID
evk() { $TB history "$1" --json | jq -r '[.kind, (.old // "-"), (.new // "-"), .actor] | join(" ")'; }

a=$($TB add -p ev -t "履歴" -l ui,auth --ref https://github.com/o/r/pull/1 --priority high)
eq "events: created + initial labels + refs" "$(evk "$a" | tr '\n' '|')" "created - todo human|label_added - auth human|label_added - ui human|ref_added - https://github.com/o/r/pull/1 human|"
eq "events: ids are E-n" "$($TB history "$a" --json | jq -r .id | head -1 | grep -cE '^E-[0-9]+$')" "1"

# 変えた項目だけが 1 件ずつ残る(updated_at だけの変化は残らない)
n0=$($TB history "$a" | wc -l | tr -d ' ')
$TB update "$a" --priority highest --title "履歴2" --add-label notify --remove-label ui --due 2026-12-01 --desc "秘密っぽい本文" >/dev/null
got=$($TB history "$a" --json | jq -r '[.kind, (.old // "-"), (.new // "-")] | join(" ")' | tail -n +$((n0+1)) | sort | tr '\n' '|')
eq "events: update writes one per changed field" "$got" "description - -|due - 2026-12-01|label_added - notify|label_removed ui -|priority high highest|title 履歴 履歴2|"
eq "events: description body is not copied" "$(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM task_events WHERE old LIKE '%秘密%' OR new LIKE '%秘密%';")" "0"
n1=$($TB history "$a" | wc -l | tr -d ' ')
$TB update "$a" --priority highest >/dev/null
eq "events: no-op update leaves nothing" "$($TB history "$a" | wc -l | tr -d ' ')" "$n1"
$TB update "$a" --label notify,auth >/dev/null
eq "events: relabel to same set leaves nothing" "$($TB history "$a" | wc -l | tr -d ' ')" "$n1"
$TB update "$a" --remove-ref https://github.com/o/r/pull/1 --add-ref pr=https://github.com/o/r/pull/2 >/dev/null
eq "events: ref add/remove" "$(evk "$a" | tail -2 | sort | tr '\n' '|')" "ref_added - https://github.com/o/r/pull/2 human|ref_removed https://github.com/o/r/pull/1 - human|"

# 状態: claim / update / block / done
$TB claim "$a" --owner w1 >/dev/null
eq "events: claim = status + owner" "$(evk "$a" | tail -2 | sort | tr '\n' '|')" "owner - w1 human|status todo in_progress human|"
$TB block "$a" -m why >/dev/null
eq "events: block" "$(evk "$a" | tail -1)" "status in_progress blocked human"
$TB update "$a" --status review >/dev/null
$TB done "$a" -d out.md -m ok >/dev/null
eq "events: done" "$(evk "$a" | tail -1)" "status review done human"
# 報告(reported_at)そのものは履歴に残さない。tb report は done の報告コメントを作るので、そのコメントだけが 1 件残る
n2=$($TB history "$a" | wc -l | tr -d ' ')
FAKE_STATUS=idle $TB report "$a" >/dev/null 2>&1
eq "events: report leaves only its done comment" "$(($($TB history "$a" | wc -l | tr -d ' ') - n2)) $($TB history "$a" | tail -1 | grep -c comment)" "1 1"

# リンク: 両方のタスクに向きつきで。消したときも
b=$($TB add -p ev -t b); c=$($TB add -p ev -t c)
$TB link "$b" blocks "$c" >/dev/null
eq "events: link from side" "$(evk "$b" | tail -1)" "link - blocks $c human"
eq "events: link to side" "$(evk "$c" | tail -1)" "link - blocked_by $b human"
$TB link "$b" blocks "$c" >/dev/null
eq "events: duplicate link leaves nothing" "$($TB history "$c" | grep -c link)" "1"
$TB unlink "$b" blocks "$c" >/dev/null
eq "events: unlink both sides" "$(evk "$b" | tail -1)|$(evk "$c" | tail -1)" "unlink blocks $c - human|unlink blocked_by $b - human"

# agent の pane からは herdr から引いた名前。コメントは報告者と C-<n>。本文は写さない
export HERDR_PANE_ID=wW:p1
$TB update "$b" --add-label x >/dev/null
eq "events: actor from herdr" "$(evk "$b" | tail -1)" "label_added - x worker1"
FAKE_STATUS=idle $TB comment "$b" --kind question --text "本文は写さない" >/dev/null 2>&1
eq "events: comment kind + ref" "$($TB history "$b" --json | jq -r 'select(.kind=="comment") | [.new, .actor, (.ref|test("^C-[0-9]+$")|tostring)] | join(" ")')" "question worker1 true"
eq "events: comment body not copied" "$(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM task_events WHERE new LIKE '%本文%';")" "0"
# 名前を引けない pane は pane:<id>(書き込みは止めない)
FAKE_AGENTS='[]' $TB update "$b" --add-label y >/dev/null; r=$?
eq "events: unresolved pane still writes" "$r" "0"
eq "events: unresolved pane actor" "$(evk "$b" | tail -1)" "label_added - y pane:wW:p1"
unset HERDR_PANE_ID

# 使い方の誤り・tb events
eq "events: history unknown task" "$(rc $TB history T-ev-999)" "1"
eq "events: history bad id" "$(rc $TB history nope)" "2"
eq "events: events bad since" "$(rc $TB events --since 3)" "2"
eq "events: events bad kind" "$(rc $TB events --kind "x';--")" "2"
eq "events: events bad limit" "$(rc $TB events --limit 0)" "2"
eq "events: events --kind + -p" "$($TB events -p ev --kind link --json | jq -r .task_id | tr '\n' ' ')" "$b $c "
last=$($TB events -p ev --limit 1 --json | jq -r .id)
eq "events: --limit 1 is newest" "$($TB events -p ev --json | jq -r .id | tail -1)" "$last"
$TB add -p ev2 -t new >/dev/null
eq "events: --since" "$($TB events --since "$last" --json | jq -r '[.task_id, .kind] | join(" ")' | grep -c '^T-ev2-001 created$')" "1"

# 別セッションからは見えない・書けない
eq "events: other session sees none" "$(HERDR_SESSION=other-ev $TB events -p ev | wc -l | tr -d ' ')" "0"
eq "events: other session cannot write rows of this one" "$(HERDR_SESSION=other-ev rc $TB update "$b" --add-label z)" "1"
eq "events: rows carry session" "$(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM task_events WHERE herdr_session='';")" "0"
