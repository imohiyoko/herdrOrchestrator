# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態を使うものがある)
# --- add / 採番
a1=$($TB add -p alpha -t "first"); a2=$($TB add -p alpha -t "it's \"quoted\"; DROP TABLE tasks;--"); b1=$($TB add -p beta -t "b")
eq "id format/sequence" "$a1 $a2 $b1" "T-alpha-001 T-alpha-002 T-beta-001"
eq "title with quotes stored" "$($TB show $a2 | jq -r .title)" "it's \"quoted\"; DROP TABLE tasks;--"
eq "bad project rejected" "$(rc $TB add -p 'a-b' -t x)" "2"
eq "bad id rejected" "$(rc $TB show foo)" "2"
eq "missing task" "$(rc $TB show T-alpha-999)" "1"

# --- claim
eq "claim ok" "$(rc $TB claim $a1 --owner w1 --session s1)" "0"
eq "claim twice fails" "$(rc $TB claim $a1 --owner w2)" "1"
eq "owner kept" "$($TB show $a1 | jq -r '.owner_agent+"/"+.session_id+"/"+.status')" "w1/s1/in_progress"
eq "update status" "$(rc $TB update $a1 --status review)" "0"
eq "update to done rejected" "$(rc $TB update $a1 --status done)" "2"

# --- done の必須項目
eq "done without -d/-m" "$(rc $TB done $a1)" "2"
eq "done without -m" "$(rc $TB done $a1 -d /x/y)" "2"
eq "done without -d" "$(rc $TB done $a1 -m s)" "2"
eq "status unchanged after failed done" "$($TB show $a1 | jq -r .status)" "review"
eq "report before done fails" "$(rc $TB report $a1)" "1"

# --- report: idle → 送って reported_at を埋める(既定宛先 conductor)
$TB done $a1 -d /tmp/a.md -d https://example.invalid/pr/1 -m $'要約\n二行目' >/dev/null
eq "pending lists done+unreported" "$($TB pending --ids)" "$a1"
eq "pending shows default target" "$($TB pending | cut -f1,3)" "$(printf '%s\tconductor' "$a1")"
FAKE_STATUS=idle $TB report $a1 >/dev/null; r=$?
eq "report idle exit" "$r" "0"
eq "prompt message" "$(grep '^prompt' "$FAKE_LOG" | tail -1)" "$(printf 'prompt conductor\t[from: w1 / - / %s / done] 要約 二行目 / 成果物: /tmp/a.md, https://example.invalid/pr/1 (詳細: tb comments %s)' "$a1" "$a1")"
eq "reported_at set" "$($TB show $a1 | jq -r '.reported_at != null')" "true"
eq "pending empty" "$($TB pending --ids)" ""
n=$(prompts); $TB report $a1 >/dev/null
eq "re-report is idempotent (no resend)" "$(prompts)" "$n"

# --- report: working / blocked / unknown は送らず pending に残す
$TB done $a2 -d /tmp/b.md -m s2 >/dev/null
n=$(prompts)
eq "working → exit 10" "$(FAKE_STATUS=working rc $TB report $a2)" "10"
eq "blocked → exit 10" "$(FAKE_STATUS=blocked rc $TB report $a2)" "10"
eq "unknown → exit 10" "$(FAKE_STATUS=unknown rc $TB report $a2)" "10"
eq "no prompt sent while busy" "$(prompts)" "$n"
eq "reported_at still empty" "$($TB show $a2 | jq -r '.reported_at')" "null"
eq "still pending (as the done comment made by report)" "$($TB pending --ids) $(sqlite3 "$TB_DB" "SELECT task_id||':'||kind FROM comments WHERE id=2;")" "C-2 $a2:done"
eq "missing target → exit 11" "$(FAKE_STATUS=missing rc $TB report $a2)" "11"
eq "prompt failure → exit 11" "$(FAKE_PROMPT_RC=1 rc $TB report $a2)" "11"
eq "reported_at empty after failure" "$($TB show $a2 | jq -r '.reported_at')" "null"
eq "retry succeeds when idle" "$(FAKE_STATUS=done rc $TB report $a2)" "0"
eq "pending empty after retry" "$($TB pending --ids)" ""

# --- project ごとの宛先
printf '# c\nbeta = beta-conductor\n' >"$TB_CONFIG"
$TB done $b1 -d /tmp/c -m sb >/dev/null; $TB report $b1 >/dev/null
eq "project-specific target" "$(grep '^prompt' "$FAKE_LOG" | tail -1 | cut -f1)" "prompt beta-conductor"

# --- block / list / parent
b2=$($TB add -p beta -t child --parent $b1)
eq "parent stored" "$($TB show $b2 | jq -r .parent_id)" "$b1"
eq "bad parent rejected" "$(rc $TB add -p beta -t x --parent T-beta-999)" "1"
$TB block $b2 -m "wait" >/dev/null
eq "block" "$($TB list -p beta -s blocked | cut -f1,2)" "$(printf '%s\tblocked' "$b2")"
eq "done task can't be blocked" "$(rc $TB block $a1)" "1"

# --- claim の上書き防止: 担当が空か --owner と同じ todo だけ取れる
cg_state() { $TB show "$1" | jq -r '.status+"/"+(.owner_agent // "")'; }
c0=$($TB add -p cg -t "unowned")
eq "claim guard: unowned todo → ok" "$(rc $TB claim $c0 --owner w1)" "0"
c1=$($TB add -p cg -t "empty owner"); sqlite3 "$TB_DB" "UPDATE tasks SET owner_agent='' WHERE id='$c1';"
eq "claim guard: empty-string owner → ok" "$(rc $TB claim $c1 --owner w1)" "0"
c2=$($TB add -p cg -t "same owner" --owner w1)
eq "claim guard: assigned to the same agent → ok" "$(rc $TB claim $c2 --owner w1)" "0"
c3=$($TB add -p cg -t "other owner" --owner w1); cg_t0=$($TB show $c3 | jq -r .updated_at); cg_n0=$(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM task_events WHERE task_id='$c3';" 2>/dev/null || echo 0)
eq "claim guard: assigned to another agent → fails" "$(rc $TB claim $c3 --owner w2)" "1"
eq "claim guard: failed claim changes nothing" "$(cg_state $c3) $($TB show $c3 | jq -r .updated_at) $(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM task_events WHERE task_id='$c3';" 2>/dev/null || echo 0)" "todo/w1 $cg_t0 $cg_n0"
eq "claim guard: reason points to reassignment" "$($TB claim $c3 --owner w2 2>&1 | grep -c 'tb update .* --owner')" "1"
eq "claim guard: reassign with update, then claim" "$($TB update $c3 --owner w2 >/dev/null; rc $TB claim $c3 --owner w2) $(cg_state $c3)" "0 in_progress/w2"
eq "claim guard: status not todo keeps the old reason" "$($TB claim $c3 --owner w2 2>&1 | grep -c 'todo ではない')" "1"
# two claims run one after the other; only the first succeeds (the condition sits in the UPDATE WHERE, so it is atomic)
c4=$($TB add -p cg -t "second claim on the same todo")
cg_a=$(rc $TB claim $c4 --owner wa); cg_b=$(rc $TB claim $c4 --owner wb)
eq "claim guard: two sequential claims on the same unowned todo → only the first succeeds" "$cg_a $cg_b $(cg_state $c4)" "0 1 in_progress/wa"
c5=$($TB add -p cg -t "pool text case" --owner w9)
eq "claim guard: following a pool-style claim on an assigned todo fails and leaves it" "$(rc $TB claim $c5 --owner w1 --session s9) $(cg_state $c5)" "1 todo/w9"

# --- 状態を別名にした環境でも、担当ガードと理由の分け方は分類で効く(todo の分類 = backlog、active の分類 = doing)
_cg_saved=${TB_STATUSES-__unset__}
export TB_STATUSES=$TMP/claim-guard-statuses.conf
printf '%s\n' 'backlog todo' 'doing active' 'shipped done' 'dropped cancelled' >"$TB_STATUSES"
ca1=$($TB add -p cga -t "alias, other owner" --owner w1)
eq "claim guard (alias statuses): assigned to another agent → fails and keeps the alias state" "$(rc $TB claim $ca1 --owner w2) $(cg_state $ca1)" "1 backlog/w1"
eq "claim guard (alias statuses): the reason is the assignment, not the status" "$($TB claim $ca1 --owner w2 2>&1 | grep -c '割り当て済み')" "1"
eq "claim guard (alias statuses): unowned and same-owner claims pass" "$(rc $TB claim $($TB add -p cga -t "alias, unowned") --owner w1) $(rc $TB claim $($TB add -p cga -t "alias, same owner" --owner w1) --owner w1)" "0 0"
$TB claim $ca1 --owner w1 >/dev/null
eq "claim guard (alias statuses): a task already in the active class reports the status reason" "$($TB claim $ca1 --owner w1 2>&1 | grep -c '割り当て済み') $($TB claim $ca1 --owner w1 2>&1 | grep -c 'backlog')" "0 1"
if [[ $_cg_saved == __unset__ ]]; then unset TB_STATUSES; else export TB_STATUSES=$_cg_saved; fi
