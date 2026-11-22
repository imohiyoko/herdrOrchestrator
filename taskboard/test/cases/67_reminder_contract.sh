# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb reminder(list / check / publish / scan / nudge)と古い名前の使い方・文面・終了コード(bash と Go で同じ。期待値は bash の出力から作った)
_rc_saved=$(env | grep -E '^(TB_DB|TB_CONFIG|TB_REPOS_FILE|TB_GH_BIN|FAKE_AGENTS|FAKE_LIST_FAIL|TB_NUDGE|HERDR_PANE_ID)=' || true)
unset FAKE_LIST_FAIL HERDR_PANE_ID
export TB_DB=$TMP/rcontract.db TB_CONFIG=$TMP/rc-projects.conf TB_REPOS_FILE=$TMP/rc-repos.md TB_GH_BIN=$TMP/rc-no-gh
export TB_REMINDERS_DEADLINES=$TMP/rc-deadlines.conf TB_TODAY=2026-03-10 TB_NUDGE=$TMP/rc-nudge.conf
export FAKE_AGENTS='[{"pane_id":"w1:p1","name":"w1","agent_status":"idle","agent_session":{"value":"s1"}}]'
printf 'rc=rc-conductor\n' >"$TB_CONFIG"; : >"$TB_NUDGE"; rm -f "$TB_REMINDERS_DEADLINES" "$TB_REPOS_FILE"
rcsql() { sqlite3 "$TB_DB" "$@"; }
rcerr() { "$@" 2>&1; echo "rc=$?"; }
rcts() { sed -E 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z/TS/g'; }
$TB agents >/dev/null 2>&1

# ---- 使い方と引数の誤り
eq "reminder: no subcommand → usage on stdout, rc 2" "$(rcerr $TB reminder)" $'tb reminder list [--json]          人が忘れているかもしれないものを優先度順に出す(何も書かない)\ntb reminder check                  出すものがあれば 0、無ければ 3(何も出さず、何も書かない)\ntb reminder publish [--json]       まとめを出し、前回の記録を更新する(同じ項目は「継続(N 日目)」の 1 行。1 日 1 回は全件)\ntb reminder scan [-p project] [--within N]   期限切れ・期限間近のタスクに履歴のイベントを 1 日 1 回ずつ残す\ntb reminder nudge [<宛先> ...] [--pool] [--dry-run] [--idle-min N] [--max N] [--json]   チケットを握ったまま休んでいる agent に催促する\n(古い名前 tb reminders [--check | --publish] / tb due-scan / tb nudge も同じに使える。期限ファイルの取り込みは tb deadlines-import のまま)\nrc=2'
eq "reminder help" "$(rcerr $TB reminder help)" $'tb reminder list [--json]          人が忘れているかもしれないものを優先度順に出す(何も書かない)\ntb reminder check                  出すものがあれば 0、無ければ 3(何も出さず、何も書かない)\ntb reminder publish [--json]       まとめを出し、前回の記録を更新する(同じ項目は「継続(N 日目)」の 1 行。1 日 1 回は全件)\ntb reminder scan [-p project] [--within N]   期限切れ・期限間近のタスクに履歴のイベントを 1 日 1 回ずつ残す\ntb reminder nudge [<宛先> ...] [--pool] [--dry-run] [--idle-min N] [--max N] [--json]   チケットを握ったまま休んでいる agent に催促する\n(古い名前 tb reminders [--check | --publish] / tb due-scan / tb nudge も同じに使える。期限ファイルの取り込みは tb deadlines-import のまま)\nrc=0'
eq "reminder: unknown subcommand" "$(rcerr $TB reminder bogus)" $'tb: reminder: 不明なサブコマンド bogus(list / check / publish / scan / nudge)\n(tb help で使い方)\nrc=2'
eq "reminder: empty subcommand" "$(rcerr $TB reminder '')" $'tb: reminder: 不明なサブコマンド (list / check / publish / scan / nudge)\n(tb help で使い方)\nrc=2'
eq "reminder list --check is not list" "$(rcerr $TB reminder list --check)" $'tb: reminder list: 不明な引数 --check(--json だけ)\n(tb help で使い方)\nrc=2'
eq "reminder publish --bogus" "$(rcerr $TB reminder publish --bogus)" $'tb: reminder publish: 不明な引数 --bogus(--json だけ)\n(tb help で使い方)\nrc=2'
eq "reminder check --json" "$(rcerr $TB reminder check --json)" $'tb: reminder check(引数なし)\n(tb help で使い方)\nrc=2'
eq "reminders --bogus" "$(rcerr $TB reminders --bogus)" $'tb: reminders: 不明な引数 --bogus(--check | --publish [--json] | --json)\n(tb help で使い方)\nrc=2'
eq "reminders: bad env" "$(TB_REMINDERS_DUE_DAYS=x rcerr $TB reminder list)" $'tb: TB_REMINDERS_STALE_HOURS / TB_REMINDERS_DUE_DAYS / TB_DIGEST_QUEUED_SEC は数字\n(tb help で使い方)\nrc=2'
eq "reminders: bad today" "$(TB_TODAY=2026-02-30 rcerr $TB reminder list)" $'tb: 期日は YYYY-MM-DD の日付: 2026-02-30\n(tb help で使い方)\nrc=2'
eq "scan: bad --within" "$(rcerr $TB reminder scan --within 1000)" $'tb: due-scan: --within は 0〜999 の日数: 1000\n(tb help で使い方)\nrc=2'
eq "scan: --within needs a value" "$(rcerr $TB reminder scan --within)" $'tb: --within に値が必要です\n(tb help で使い方)\nrc=2'
eq "scan: bad project" "$(rcerr $TB reminder scan -p 'a b')" $'tb: project は英数字と _ のみ: a b\n(tb help で使い方)\nrc=2'
eq "scan: unknown arg" "$(rcerr $TB due-scan --bogus)" $'tb: due-scan: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "scan: bad today" "$(TB_TODAY=x rcerr $TB reminder scan)" $'tb: 期日は YYYY-MM-DD の日付: x\n(tb help で使い方)\nrc=2'
eq "nudge: unknown option" "$(rcerr $TB reminder nudge --bogus)" $'tb: nudge [<宛先> ...] [--pool] [--dry-run] [--idle-min N] [--max N] [--json]\n(tb help で使い方)\nrc=2'
eq "nudge: bad --idle-min" "$(rcerr $TB nudge --dry-run --idle-min x)" $'tb: nudge: --idle-min / --max(TB_NUDGE_IDLE_MIN / TB_NUDGE_MAX)は数字\n(tb help で使い方)\nrc=2'
eq "nudge: --max needs a value" "$(rcerr $TB nudge --max)" $'tb: --max に値が必要です\n(tb help で使い方)\nrc=2'
eq "nudge: bad TB_NUDGE_MAX" "$(TB_NUDGE_MAX=-1 rcerr $TB nudge --dry-run)" $'tb: nudge: --idle-min / --max(TB_NUDGE_IDLE_MIN / TB_NUDGE_MAX)は数字\n(tb help で使い方)\nrc=2'

# ---- 空の DB
eq "check: nothing → 3, no output" "$(rcerr $TB reminder check)" $'rc=3'
eq "list: nothing" "$(rcerr $TB reminder list | rcts)" $'[reminders TS / 0 件 / 新規 0・変化 0・継続 0]\n忘れているかもしれないものはありません\nrc=0'
eq "list --json: nothing" "$(rcerr $TB reminder list --json)" $'{"full":false,"removed":[],"items":[]}\nrc=0'
eq "publish: nothing" "$(rcerr $TB reminder publish | rcts)" $'[reminders TS / 0 件 / 新規 0・変化 0・継続 0]\n忘れているかもしれないものはありません\nrc=0'
eq "publish: no full-day when empty" "$(rcsql "SELECT key FROM notify_seen WHERE key LIKE 'reminders:%' ORDER BY key;")" $'reminders:last\nreminders:state'
eq "scan: nothing" "$(rcerr $TB reminder scan)" $'overdue 0 due_soon 0\nrc=0'

# ---- 項目があるとき(期限 2・質問 1)
t1=$($TB add -p rc -t '期限切れ  の|作業' --due 2026-03-01)
t2=$($TB add -p rc -t '明日まで' --due 2026-03-11 --owner w1)
HERDR_PANE_ID=w1:p1 $TB comment "$t2" --kind question --text 'どうする?' >/dev/null
printf 'deadline = 2026-03-12 | 契約 の 更新\ndeadline = 2026-04-30 | 遠い\n' >"$TB_REMINDERS_DEADLINES"
eq "check: items → 0" "$(rcerr $TB reminder check)" $'rc=0'
eq "list: items" "$(rcerr $TB reminder list | rcts | sed "s/$t1/T1/g; s/$t2/T2/g")" $'[reminders TS / 4 件 / 新規 4・変化 0・継続 0]\n優先 | 何が | 誰の番 | 期限 | 参照 | 経過\n急ぎ | 期限: T1 期限切れ の 作業 (新規) | 人 | 2026-03-01 | T1 | 0m\n急ぎ | 期限: T2 明日まで (新規) | w1 | 2026-03-11 | T2 | 0m\n人の番 | 質問: T2 明日まで (新規) | 人 | - | C-1 | 0m\n待ち・予定 | 期限: 契約 の 更新 (新規) | 人 | 2026-03-12 | deadline 2026-03-12 | -\nrc=0'
eq "list --json: items" "$(rcerr $TB reminders --json | sed "s/$t1/T1/g; s/$t2/T2/g")" $'{"full":false,"removed":[],"items":[{"p":1,"key":"due:T1","what":"期限: T1 期限切れ の 作業","who":"人","due":"2026-03-01","ref":"T1","age":"0m","status":"new","day":1},{"p":1,"key":"due:T2","what":"期限: T2 明日まで","who":"w1","due":"2026-03-11","ref":"T2","age":"0m","status":"new","day":1},{"p":2,"key":"q:1","what":"質問: T2 明日まで","who":"人","due":"","ref":"C-1","age":"0m","status":"new","day":1},{"p":4,"key":"due:f:9c74cd27bd","what":"期限: 契約 の 更新","who":"人","due":"2026-03-12","ref":"deadline 2026-03-12","age":"","status":"new","day":1}]}\nrc=0'
eq "publish --json: full on the first run" "$(rcerr $TB reminder publish --json | sed "s/$t1/T1/g; s/$t2/T2/g")" $'{"full":true,"removed":[],"items":[{"p":1,"key":"due:T1","what":"期限: T1 期限切れ の 作業","who":"人","due":"2026-03-01","ref":"T1","age":"0m","status":"new","day":1},{"p":1,"key":"due:T2","what":"期限: T2 明日まで","who":"w1","due":"2026-03-11","ref":"T2","age":"0m","status":"new","day":1},{"p":2,"key":"q:1","what":"質問: T2 明日まで","who":"人","due":"","ref":"C-1","age":"0m","status":"new","day":1},{"p":4,"key":"due:f:9c74cd27bd","what":"期限: 契約 の 更新","who":"人","due":"2026-03-12","ref":"deadline 2026-03-12","age":"","status":"new","day":1}]}\nrc=0'
eq "publish: last row" "$(rcsql "SELECT value FROM notify_seen WHERE key='reminders:last';" | rcts | sed "s/$t1/T1/g; s/$t2/T2/g")" $'{"at":"TS","full":true,"removed":[],"text":"[reminders TS / 4 件 / 全件]\\n優先 | 何が | 誰の番 | 期限 | 参照 | 経過\\n急ぎ | 期限: T1 期限切れ の 作業 | 人 | 2026-03-01 | T1 | 0m\\n急ぎ | 期限: T2 明日まで | w1 | 2026-03-11 | T2 | 0m\\n人の番 | 質問: T2 明日まで | 人 | - | C-1 | 0m\\n待ち・予定 | 期限: 契約 の 更新 | 人 | 2026-03-12 | deadline 2026-03-12 | -","items":[{"p":1,"key":"due:T1","what":"期限: T1 期限切れ の 作業","who":"人","due":"2026-03-01","ref":"T1","age":"0m","status":"new","day":1},{"p":1,"key":"due:T2","what":"期限: T2 明日まで","who":"w1","due":"2026-03-11","ref":"T2","age":"0m","status":"new","day":1},{"p":2,"key":"q:1","what":"質問: T2 明日まで","who":"人","due":"","ref":"C-1","age":"0m","status":"new","day":1},{"p":4,"key":"due:f:9c74cd27bd","what":"期限: 契約 の 更新","who":"人","due":"2026-03-12","ref":"deadline 2026-03-12","age":"","status":"new","day":1}]}'
eq "publish: full-day" "$(rcsql "SELECT value FROM notify_seen WHERE key='reminders:full-day';")" $'2026-03-10'
eq "check: same day, nothing new → 3" "$(rcerr $TB reminders --check)" $'rc=3'
eq "publish: continuing" "$(rcerr $TB reminders --publish | rcts | sed "s/$t1/T1/g; s/$t2/T2/g")" $'[reminders TS / 4 件 / 新規 0・変化 0・継続 4]\n継続: T1(1 日目), T2(1 日目), C-1(1 日目), deadline 2026-03-12(1 日目)\nrc=0'
$TB done "$t1" -d x -m y >/dev/null 2>&1 || $TB update "$t1" --status done >/dev/null
eq "check: removed → 0" "$(rcerr $TB reminder check)" $'rc=0'
eq "publish: removed" "$(TB_TODAY=2026-03-11 rcerr $TB reminder publish | rcts | sed "s/$t1/T1/g; s/$t2/T2/g")" $'[reminders TS / 3 件 / 全件・解消 1]\n優先 | 何が | 誰の番 | 期限 | 参照 | 経過\n急ぎ | 期限: T2 明日まで (変化) | w1 | 2026-03-11 | T2 | 0m\n急ぎ | 期限: 契約 の 更新 (変化) | 人 | 2026-03-12 | deadline 2026-03-12 | -\n人の番 | 質問: T2 明日まで | 人 | - | C-1 | 0m\nrc=0'
eq "scan: events" "$(rcerr $TB reminder scan --within 3)" $'overdue 0 due_soon 1\nrc=0'
eq "scan: once a day" "$(rcerr $TB due-scan --within 3)" $'overdue 0 due_soon 0\nrc=0'

# ---- nudge
eq "nudge: dry-run, no live holder" "$(rcerr $TB reminder nudge --dry-run)" $'rc=0'
eq "nudge: targets that hit nobody" "$(rcerr $TB reminder nudge --dry-run nobody '@none' --json)" $'{"agent":"nobody","task":"-","result":"skip","reason":"宛先に当たる agent がいない(in_progress のチケットを握っていない・--pool なら herdr にいない)"}\n{"agent":"@none","task":"-","result":"skip","reason":"宛先に当たる agent がいない(in_progress のチケットを握っていない・--pool なら herdr にいない)"}\nrc=0'
eq "nudge: herdr list fails → 11" "$(FAKE_LIST_FAIL=1 rcerr $TB nudge --dry-run)" $'tb: nudge: herdr agent list に失敗しました(何も送っていません)\nrc=11'
printf 'bogus line\nwake done\n' >"$TB_NUDGE"
eq "nudge: broken config" "$(rcerr $TB nudge --dry-run)" $'1 行目: ignore <@役割の glob | agent 名の glob> か wake <分類 | 状態> ... の形ではありません: bogus line\n2 行目: 閉じた状態(done / cancelled の分類)は催促できません: done\ntb: nudge: 設定 nudge に不正な行があります(tb config edit nudge で直す)\nrc=1'
: >"$TB_NUDGE"

unset TB_REMINDERS_DEADLINES TB_TODAY TB_REPOS_FILE TB_GH_BIN TB_NUDGE
while IFS= read -r _l; do [[ -n $_l ]] && export "$_l"; done <<<"$_rc_saved"
unset _rc_saved _l
