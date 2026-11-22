# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- チケットの状態を動かすコマンド(claim / done / block / cancel)の使い方・文面・終了コード・履歴・通知(bash と Go で同じ。期待値は bash の出力から作った)。project は tsc / tsc2 だけを使う
_ts_saved=$(env | grep -E '^(FAKE_AGENTS|HERDR_PANE_ID|TB_STATUSES|TB_NOTIFY_ENABLED|TB_NOTIFY_DEDUPE_SEC)=' || true)
unset HERDR_PANE_ID
export FAKE_AGENTS='[{"pane_id":"w1:p1","name":"w1","agent_status":"idle","agent_session":{"value":"s1"}}]'
export TB_STATUSES=$TMP/ts-statuses.conf TB_NOTIFY_ENABLED=on TB_NOTIFY_DEDUPE_SEC=0; rm -f "$TB_STATUSES"
tserr() { "$@" 2>&1; echo "rc=$?"; }
tsmask() { sed -E -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:]{8}Z?/TS/g' -e 's/\bE-[0-9]+\b/E-N/g'; }
tsrow() { sqlite3 "$TB_DB" "SELECT status, IFNULL(owner_agent,'NULL'), IFNULL(session_id,'NULL'), IFNULL(account,'NULL'), deliverables, IFNULL(summary,'NULL'), IFNULL(note,'NULL'), reported_at IS NULL FROM tasks WHERE id='$1';"; }
a=$($TB add -p tsc -t A); b=$($TB add -p tsc -t B --owner w2); c=$($TB add -p tsc -t C); d=$($TB add -p tsc -t D)
$TB link "$a" blocks "$c" >/dev/null

# ---- claim
eq "claim: no args" "$(tserr $TB claim)" $'tb: claim <id> --owner <name> [--session <sid>] [--account <acct>]\n(tb help で使い方)\nrc=2'
eq "claim: bad id" "$(tserr $TB claim x --owner w1)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "claim: no owner" "$(tserr $TB claim "$a")" $'tb: claim: --owner が必要です\n(tb help で使い方)\nrc=2'
eq "claim: blank owner" "$(tserr $TB claim "$a" --owner ' ')" $'tb: claim: --owner が必要です\n(tb help で使い方)\nrc=2'
eq "claim: --owner needs a value" "$(tserr $TB claim "$a" --owner)" $'tb: --owner に値が必要です\n(tb help で使い方)\nrc=2'
eq "claim: unknown" "$(tserr $TB claim "$a" --owner w1 --bogus)" $'tb: claim: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "claim: missing" "$(tserr $TB claim T-tsc-999 --owner w1)" $'tb: タスクが見つかりません: T-tsc-999\nrc=1'
eq "claim: blocked by a" "$(tserr $TB claim "$c" --owner w1)" $'tb: claim できません(先に終わらせるタスクがあります: T-tsc-001(todo)): T-tsc-003\nrc=1'
eq "claim: assigned to someone else" "$(tserr $TB claim "$b" --owner w1)" $'tb: claim できません(w2 に割り当て済み。付け替えは tb update T-tsc-002 --owner <名前>。次の候補は tb list で探す): T-tsc-002\nrc=1'
eq "claim: own assignment" "$(tserr $TB claim "$b" --owner w2 --session s2 --account acct)" $'T-tsc-002 claimed by w2\nrc=0'
eq "claim: ok" "$(tserr $TB claim "$a" --owner w1 --session '' --account '')" $'T-tsc-001 claimed by w1\nrc=0'
eq "claim: not todo" "$(tserr $TB claim "$a" --owner w1)" $'tb: claim できません(status が todo ではない): T-tsc-001\nrc=1'
eq "claim: rows" "$(tsrow "$a"; tsrow "$b")" $'in_progress|w1|NULL|NULL|[]|NULL|NULL|1\nin_progress|w2|s2|acct|[]|NULL|NULL|1'

# ---- done
eq "done: no args" "$(tserr $TB done)" $'tb: done <id> -d <成果物パス> [-d ...] -m <要約>\n(tb help で使い方)\nrc=2'
eq "done: bad id" "$(tserr $TB done x -d f -m s)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "done: no deliverable" "$(tserr $TB done "$a" -m s)" $'tb: done: 成果物(-d)が必須です\n(tb help で使い方)\nrc=2'
eq "done: blank deliverable" "$(tserr $TB done "$a" -d ' ' -m s)" $'tb: done: 成果物(-d)が必須です\n(tb help で使い方)\nrc=2'
eq "done: no summary" "$(tserr $TB done "$a" -d f)" $'tb: done: 要約(-m)が必須です\n(tb help で使い方)\nrc=2'
eq "done: blank summary" "$(tserr $TB done "$a" -d f -m '  ')" $'tb: done: 要約(-m)が必須です\n(tb help で使い方)\nrc=2'
eq "done: -m needs a value" "$(tserr $TB done "$a" -d f -m)" $'tb: -m に値が必要です\n(tb help で使い方)\nrc=2'
eq "done: unknown" "$(tserr $TB done "$a" -d f -m s --bogus)" $'tb: done: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "done: missing" "$(tserr $TB done T-tsc-999 -d f -m s)" $'tb: タスクが見つかりません: T-tsc-999\nrc=1'
eq "done: ok" "$(tserr $TB done "$a" -d 'out/a b.md' -d ' ' --deliverable '<x>&"y"' -m "要約	タブ")" $'T-tsc-001 done (報告は tb digest でまとめて届く。すぐ届けるなら tb report T-tsc-001)\nrc=0'
eq "done: twice" "$(tserr $TB done "$a" -d f -m s)" $'tb: すでに done です: T-tsc-001\nrc=1'
eq "done: from todo" "$(tserr $TB done "$d" -d f -m s)" $'T-tsc-004 done (報告は tb digest でまとめて届く。すぐ届けるなら tb report T-tsc-004)\nrc=0'
eq "done: rows" "$(tsrow "$a"; tsrow "$d")" $'done|w1|NULL|NULL|["out/a b.md","<x>&\\"y\\""]|要約\tタブ|NULL|1\ndone|NULL|NULL|NULL|["f"]|s|NULL|1'
eq "done: notify" "$(sqlite3 "$TB_DB" "SELECT type, ref, text FROM notify_events WHERE ref LIKE 'T-tsc-%' ORDER BY id;")" $'done|T-tsc-001|[完了] T-tsc-001: 要約 タブ / 成果物: out/a b.md, ‹x›&"y"\ndone|T-tsc-004|[完了] T-tsc-004: s / 成果物: f'
eq "done: blocked task can be claimed now" "$(tserr $TB claim "$c" --owner w1)" $'T-tsc-003 claimed by w1\nrc=0'

# ---- block
eq "block: no args" "$(tserr $TB block)" $'tb: block <id> [-m <理由>]\n(tb help で使い方)\nrc=2'
eq "block: bad id" "$(tserr $TB block x)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "block: unknown" "$(tserr $TB block "$c" --bogus)" $'tb: block: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "block: -m needs a value" "$(tserr $TB block "$c" -m)" $'tb: -m に値が必要です\n(tb help で使い方)\nrc=2'
eq "block: missing" "$(tserr $TB block T-tsc-999)" $'tb: タスクが見つかりません: T-tsc-999\nrc=1'
eq "block: ok" "$(tserr $TB block "$c" -m "待ち")" $'T-tsc-003 blocked\nrc=0'
eq "block: again clears the reason" "$(tserr $TB block "$c" --message '')" $'T-tsc-003 blocked\nrc=0'
eq "block: done task" "$(tserr $TB block "$d")" $'T-tsc-004 blocked\nrc=0'
eq "block: rows" "$(tsrow "$c"; tsrow "$d")" $'blocked|w1|NULL|NULL|[]|NULL|NULL|1\nblocked|NULL|NULL|NULL|["f"]|s|NULL|1'

# ---- cancel
eq "cancel: no args" "$(tserr $TB cancel)" $'tb: cancel <id> [-m <理由>]\n(tb help で使い方)\nrc=2'
eq "cancel: bad id" "$(tserr $TB cancel x)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "cancel: unknown" "$(tserr $TB cancel "$b" --bogus)" $'tb: cancel: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "cancel: missing" "$(tserr $TB cancel T-tsc-999)" $'tb: タスクが見つかりません: T-tsc-999\nrc=1'
eq "cancel: ok" "$(tserr $TB cancel "$b" -m "やめた")" $'T-tsc-002 cancelled\nrc=0'
eq "cancel: twice" "$(tserr $TB cancel "$b")" $'tb: すでに cancelled です: T-tsc-002\nrc=1'
eq "cancel: done task" "$(tserr $TB cancel "$d")" $'T-tsc-004 cancelled\nrc=0'
eq "cancel: rows" "$(tsrow "$b"; tsrow "$d")" $'cancelled|w2|s2|acct|[]|やめた|NULL|0\ncancelled|NULL|NULL|NULL|["f"]|NULL|NULL|0'

# ---- pane からの変更(変えた者は herdr から引いた名前)
e=$($TB add -p tsc2 -t E)
eq "pane: claim" "$(HERDR_PANE_ID=w1:p1 tserr $TB claim "$e" --owner w1)" $'T-tsc2-001 claimed by w1\nrc=0'
eq "pane: unknown pane still writes" "$(HERDR_PANE_ID=w9:p9 tserr $TB block "$e")" $'T-tsc2-001 blocked\nrc=0'
eq "pane: done" "$(HERDR_PANE_ID=w1:p1 tserr $TB done "$e" -d f -m s)" $'T-tsc2-001 done (報告は tb digest でまとめて届く。すぐ届けるなら tb report T-tsc2-001)\nrc=0'
eq "history: actors" "$($TB history "$e" | tsmask)" $'E-N\tTS\tT-tsc2-001\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-tsc2-001\tstatus\ttodo\tin_progress\tw1\t-\nE-N\tTS\tT-tsc2-001\towner\t-\tw1\tw1\t-\nE-N\tTS\tT-tsc2-001\tstatus\tin_progress\tblocked\tpane:w9:p9\t-\nE-N\tTS\tT-tsc2-001\tstatus\tblocked\tdone\tw1\t-'
eq "history: b" "$($TB history "$b" | tsmask)" $'E-N\tTS\tT-tsc-002\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-tsc-002\tstatus\ttodo\tin_progress\thuman\t-\nE-N\tTS\tT-tsc-002\tstatus\tin_progress\tcancelled\thuman\t-'

# ---- 設定 statuses(書く状態は分類の最初の名前)
printf 'backlog todo\nqueued todo\ndoing active\nhold blocked\nshipped done\ndropped cancelled\n' >"$TB_STATUSES"
f=$($TB add -p tsc2 -t F); g=$($TB add -p tsc2 -t G)
sqlite3 "$TB_DB" "UPDATE tasks SET status='queued' WHERE id='$g';"
eq "statuses: claim from queued" "$(tserr $TB claim "$g" --owner w1)" $'T-tsc2-003 claimed by w1\nrc=0'
eq "statuses: claim not todo lists names" "$(tserr $TB claim "$g" --owner w1)" $'tb: claim できません(status が backlog / queued / todo ではない): T-tsc2-003\nrc=1'
eq "statuses: block" "$(tserr $TB block "$g")" $'T-tsc2-003 hold\nrc=0'
eq "statuses: done" "$(tserr $TB done "$g" -d f -m s)" $'T-tsc2-003 done (報告は tb digest でまとめて届く。すぐ届けるなら tb report T-tsc2-003)\nrc=0'
eq "statuses: done again (builtin done is done too)" "$(sqlite3 "$TB_DB" "UPDATE tasks SET status='done' WHERE id='$g';"; tserr $TB done "$g" -d f -m s)" $'tb: すでに done です: T-tsc2-003\nrc=1'
eq "statuses: cancel" "$(tserr $TB cancel "$f")" $'T-tsc2-002 dropped\nrc=0'
eq "statuses: cancel builtin cancelled" "$(sqlite3 "$TB_DB" "UPDATE tasks SET status='cancelled' WHERE id='$f';"; tserr $TB cancel "$f")" $'tb: すでに cancelled です: T-tsc2-002\nrc=1'
eq "statuses: rows" "$(tsrow "$f"; tsrow "$g")" $'cancelled|NULL|NULL|NULL|[]|NULL|NULL|0\ndone|w1|NULL|NULL|["f"]|s|NULL|1'
rm -f "$TB_STATUSES"

unset -f tserr tsmask tsrow; unset a b c d e f g FAKE_AGENTS TB_STATUSES TB_NOTIFY_ENABLED TB_NOTIFY_DEDUPE_SEC
[[ -z $_ts_saved ]] || while IFS= read -r _l; do export "$_l"; done <<<"$_ts_saved"
unset _ts_saved _l
