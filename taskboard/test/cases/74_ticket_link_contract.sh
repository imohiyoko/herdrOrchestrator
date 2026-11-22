# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- チケットのリンクとスプリントの書き込み(link / unlink / sprint-add / sprint-start / sprint-close)の使い方・文面・終了コード・履歴(bash と Go で同じ。期待値は bash の出力から作った)。project は tlc / tlc2 だけを使う
# 並行の検査は 11_tickets.sh と 12_sprints.sh(どちらも TB_IMPL=go で Go を通る)
_tl_saved=$(env | grep -E '^(FAKE_AGENTS|HERDR_PANE_ID)=' || true)
unset HERDR_PANE_ID
export FAKE_AGENTS='[{"pane_id":"w1:p1","name":"w1","agent_status":"idle","agent_session":{"value":"s1"}}]'
tlerr() { "$@" 2>&1; echo "rc=$?"; }
tlmask() { sed -E -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:]{8}Z?/TS/g' -e 's/\bE-[0-9]+\b/E-N/g'; }
tlsp() { sed -E "s/\bS-$s1\b/S-1st/g; s/\bS-$s2\b/S-2nd/g; s/\bS-$s3\b/S-3rd/g; s/\bS-$s4\b/S-4th/g"; }
a=$($TB add -p tlc -t A); b=$($TB add -p tlc -t B); c=$($TB add -p tlc -t C); x=$($TB add -p tlc2 -t X)

# ---- link / unlink
eq "link: no args" "$(tlerr $TB link)" $'tb: link <from-id> blocks|relates|duplicates <to-id>\n(tb help で使い方)\nrc=2'
eq "link: two args" "$(tlerr $TB link "$a" blocks)" $'tb: link <from-id> blocks|relates|duplicates <to-id>\n(tb help で使い方)\nrc=2'
eq "link: bad from" "$(tlerr $TB link x blocks "$b")" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "link: bad to" "$(tlerr $TB link "$a" blocks y)" $'tb: タスク ID は T-<project>-NNN 形式: y\n(tb help で使い方)\nrc=2'
eq "link: bad type" "$(tlerr $TB link "$a" parent "$b")" $'tb: リンクの種類は blocks relates duplicates のどれか: parent\n(tb help で使い方)\nrc=2'
eq "link: self" "$(tlerr $TB link "$a" blocks "$a")" $'tb: 自分自身とはリンクできません: T-tlc-001\n(tb help で使い方)\nrc=2'
eq "link: missing from" "$(tlerr $TB link T-tlc-999 blocks "$b")" $'tb: タスクが見つかりません: T-tlc-999\nrc=1'
eq "link: missing to" "$(tlerr $TB link "$a" blocks T-tlc-999)" $'tb: タスクが見つかりません: T-tlc-999\nrc=1'
eq "link: blocks" "$(tlerr $TB link "$a" blocks "$b")" $'T-tlc-001 blocks T-tlc-002\nrc=0'
eq "link: again" "$(tlerr $TB link "$a" blocks "$b")" $'T-tlc-001 blocks T-tlc-002(既にあります)\nrc=0'
eq "link: chain" "$(tlerr $TB link "$b" blocks "$c")" $'T-tlc-002 blocks T-tlc-003\nrc=0'
eq "link: cycle" "$(tlerr $TB link "$c" blocks "$a")" $'tb: blocks が循環します: T-tlc-003 → T-tlc-001\n(tb help で使い方)\nrc=2'
eq "link: relates back is no cycle" "$(tlerr $TB link "$c" relates "$a")" $'T-tlc-003 relates T-tlc-001\nrc=0'
eq "link: duplicates across projects" "$(tlerr $TB link "$x" duplicates "$a")" $'T-tlc2-001 duplicates T-tlc-001\nrc=0'
eq "link: from a pane" "$(HERDR_PANE_ID=w1:p1 tlerr $TB link "$b" relates "$a")" $'T-tlc-002 relates T-tlc-001\nrc=0'
eq "link: from an unknown pane" "$(HERDR_PANE_ID=w9:p9 tlerr $TB link "$c" duplicates "$b")" $'tb: pane w9:p9 に該当する agent がありません。保存しません\nrc=3'
eq "unlink: usage" "$(tlerr $TB unlink "$a" blocks)" $'tb: unlink <from-id> blocks|relates|duplicates <to-id>\n(tb help で使い方)\nrc=2'
eq "unlink: bad type" "$(tlerr $TB unlink "$a" bogus "$b")" $'tb: リンクの種類は blocks relates duplicates のどれか: bogus\n(tb help で使い方)\nrc=2'
eq "unlink: none" "$(tlerr $TB unlink "$b" blocks "$a")" $'tb: そのリンクはありません: T-tlc-002 blocks T-tlc-001\nrc=1'
eq "unlink: ok" "$(tlerr $TB unlink "$a" blocks "$b")" $'unlinked T-tlc-001 blocks T-tlc-002\nrc=0'
eq "unlink: from a pane" "$(HERDR_PANE_ID=w1:p1 tlerr $TB unlink "$c" relates "$a")" $'unlinked T-tlc-003 relates T-tlc-001\nrc=0'
eq "unlink: again" "$(tlerr $TB unlink "$a" blocks "$b")" $'tb: そのリンクはありません: T-tlc-001 blocks T-tlc-002\nrc=1'
eq "link: history" "$($TB history "$a" | tlmask)" $'E-N\tTS\tT-tlc-001\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\tblocks T-tlc-002\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\trelates T-tlc-003\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\tduplicated_by T-tlc2-001\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\trelates T-tlc-002\tw1\t-\nE-N\tTS\tT-tlc-001\tunlink\tblocks T-tlc-002\t-\thuman\t-\nE-N\tTS\tT-tlc-001\tunlink\trelates T-tlc-003\t-\tw1\t-'
eq "link: history --json" "$($TB history "$b" --json | tlmask)" $'{"id":"E-N","task_id":"T-tlc-002","kind":"created","old":null,"new":"todo","actor":"human","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-tlc-002","kind":"link","old":null,"new":"blocked_by T-tlc-001","actor":"human","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-tlc-002","kind":"link","old":null,"new":"blocks T-tlc-003","actor":"human","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-tlc-002","kind":"link","old":null,"new":"relates T-tlc-001","actor":"w1","ref":null,"created_at":"TS"}\n{"id":"E-N","task_id":"T-tlc-002","kind":"unlink","old":"blocked_by T-tlc-001","new":null,"actor":"human","ref":null,"created_at":"TS"}'
eq "link: show links" "$($TB show "$a" | jq -c .links)" $'{"duplicated_by":[{"id":"T-tlc2-001","title":"X","status":"todo"}],"relates":[{"id":"T-tlc-002","title":"B","status":"todo"}]}'
eq "link: rows" "$(sqlite3 "$TB_DB" "SELECT from_id, link_type, to_id, created_by FROM task_links WHERE from_id LIKE 'T-tlc%' ORDER BY id;")" $'T-tlc-002|blocks|T-tlc-003|human\nT-tlc2-001|duplicates|T-tlc-001|human\nT-tlc-002|relates|T-tlc-001|w1'

# ---- sprint-add
eq "sprint-add: no args" "$(tlerr $TB sprint-add)" $'tb: sprint-add: -p <project> が必要です\n(tb help で使い方)\nrc=2'
eq "sprint-add: no name" "$(tlerr $TB sprint-add -p tlc)" $'tb: sprint-add: -n <名前> が必要です\n(tb help で使い方)\nrc=2'
eq "sprint-add: blank name" "$(tlerr $TB sprint-add -p tlc -n '  ')" $'tb: sprint-add: -n <名前> が必要です\n(tb help で使い方)\nrc=2'
eq "sprint-add: bad project" "$(tlerr $TB sprint-add -p 'a b' -n x)" $'tb: project は英数字と _ のみ: a b\n(tb help で使い方)\nrc=2'
eq "sprint-add: -n needs a value" "$(tlerr $TB sprint-add -p tlc -n)" $'tb: -n に値が必要です\n(tb help で使い方)\nrc=2'
eq "sprint-add: bad start" "$(tlerr $TB sprint-add -p tlc -n x --start 2026-02-30)" $'tb: 期日は YYYY-MM-DD の日付: 2026-02-30\n(tb help で使い方)\nrc=2'
eq "sprint-add: bad end" "$(tlerr $TB sprint-add -p tlc -n x --end 11/30)" $'tb: 期日は YYYY-MM-DD の日付: 11/30\n(tb help で使い方)\nrc=2'
eq "sprint-add: end before start" "$(tlerr $TB sprint-add -p tlc -n x --start 2026-11-10 --end 2026-11-01)" $'tb: sprint-add: --end は --start より後にする\n(tb help で使い方)\nrc=2'
eq "sprint-add: unknown" "$(tlerr $TB sprint-add -p tlc -n x --bogus)" $'tb: sprint-add: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
s1=$($TB sprint-add -p tlc -n "一番目" --start 2026-11-01 --end 2026-11-01 --goal "目標")
s2=$($TB sprint-add --name 二番目 --project tlc --goal '')
s3=$($TB sprint-add -p tlc -n 三番目)
s4=$($TB sprint-add -p tlc2 -n other)
s1=${s1#S-} s2=${s2#S-} s3=${s3#S-} s4=${s4#S-}
eq "sprint-add: rows" "$(sqlite3 "$TB_DB" "SELECT project, name, IFNULL(goal,'NULL'), IFNULL(start_date,'NULL'), IFNULL(end_date,'NULL'), state FROM sprints WHERE id IN ($s1,$s2,$s3,$s4) ORDER BY id;")" $'tlc|一番目|目標|2026-11-01|2026-11-01|planned\ntlc|二番目|NULL|NULL|NULL|planned\ntlc|三番目|NULL|NULL|NULL|planned\ntlc2|other|NULL|NULL|NULL|planned'

# ---- sprint-start
eq "sprint-start: no args" "$(tlerr $TB sprint-start)" $'tb: sprint-start S-<n>\n(tb help で使い方)\nrc=2'
eq "sprint-start: two args" "$(tlerr $TB sprint-start "S-$s1" "S-$s2")" $'tb: sprint-start S-<n>\n(tb help で使い方)\nrc=2'
eq "sprint-start: bad id" "$(tlerr $TB sprint-start 1)" $'tb: スプリント ID は S-<番号> 形式: 1\n(tb help で使い方)\nrc=2'
eq "sprint-start: missing" "$(tlerr $TB sprint-start S-999999)" $'tb: スプリントが見つかりません: S-999999\nrc=1'
eq "sprint-start: ok" "$(tlerr $TB sprint-start "S-$s1" | tlsp)" $'S-1st active\nrc=0'
eq "sprint-start: again" "$(tlerr $TB sprint-start "S-$s1" | tlsp)" $'tb: planned のスプリントだけ始められます(active): S-1st\nrc=1'
eq "sprint-start: second active" "$(tlerr $TB sprint-start "S-$s2" | tlsp)" $'tb: project tlc には active なスプリントがあります(先に tb sprint-close)\nrc=1'
eq "sprint-start: other project" "$(tlerr $TB sprint-start "S-$s4" | tlsp)" $'S-4th active\nrc=0'

# ---- sprint-close
$TB update "$a" --sprint "S-$s1" --points 2 >/dev/null; $TB update "$b" --sprint "S-$s1" >/dev/null; $TB update "$c" --sprint "S-$s1" >/dev/null
$TB claim "$b" --owner w1 >/dev/null; $TB done "$b" -d x -m y >/dev/null; $TB cancel "$c" >/dev/null
eq "sprint-close: no args" "$(tlerr $TB sprint-close)" $'tb: sprint-close S-<n> [--carry S-<m>]\n(tb help で使い方)\nrc=2'
eq "sprint-close: bad id" "$(tlerr $TB sprint-close x)" $'tb: スプリント ID は S-<番号> 形式: x\n(tb help で使い方)\nrc=2'
eq "sprint-close: unknown" "$(tlerr $TB sprint-close "S-$s1" --bogus)" $'tb: sprint-close: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "sprint-close: --carry needs a value" "$(tlerr $TB sprint-close "S-$s1" --carry)" $'tb: --carry に値が必要です\n(tb help で使い方)\nrc=2'
eq "sprint-close: carry to itself" "$(tlerr $TB sprint-close "S-$s1" --carry "S-$s1" | tlsp)" $'tb: sprint-close: 同じスプリントには移せません\n(tb help で使い方)\nrc=2'
eq "sprint-close: carry bad id" "$(tlerr $TB sprint-close "S-$s1" --carry x | tlsp)" $'tb: スプリント ID は S-<番号> 形式: x\n(tb help で使い方)\nrc=2'
eq "sprint-close: carry missing" "$(tlerr $TB sprint-close "S-$s1" --carry S-999999 | tlsp)" $'tb: スプリントが見つかりません: S-999999\nrc=1'
eq "sprint-close: carry other project" "$(tlerr $TB sprint-close "S-$s1" --carry "S-$s4" | tlsp)" $'tb: スプリント S-4th は project tlc2 のものです(タスクは tlc)\n(tb help で使い方)\nrc=2'
eq "sprint-close: carry" "$(tlerr $TB sprint-close "S-$s1" --carry "S-$s2" | tlsp)" $'S-1st closed(未完了 1 件を S-2nd へ)\nrc=0'
eq "sprint-close: twice" "$(tlerr $TB sprint-close "S-$s1" | tlsp)" $'tb: すでに閉じています: S-1st\nrc=1'
eq "sprint-close: carry into closed" "$(tlerr $TB sprint-close "S-$s3" --carry "S-$s1" | tlsp)" $'tb: 閉じたスプリントには入れられません: S-1st\n(tb help で使い方)\nrc=2'
eq "sprint-close: backlog" "$(tlerr $TB sprint-close "S-$s2" | tlsp)" $'S-2nd closed(未完了 1 件を バックログ へ)\nrc=0'
eq "sprint-close: empty" "$(tlerr $TB sprint-close "S-$s3" | tlsp)" $'S-3rd closed(未完了 0 件を バックログ へ)\nrc=0'
eq "sprint-close: sprints" "$($TB sprints -p tlc | tlsp)" $'S-1st\tclosed\ttlc\t一番目\t2026-11-01..2026-11-01\t1/1\t0/0\nS-2nd\tclosed\ttlc\t二番目\t-..-\t0/0\t0/0\nS-3rd\tclosed\ttlc\t三番目\t-..-\t0/0\t0/0'
eq "sprint-close: task history" "$($TB history "$a" | tlmask | tlsp)" $'E-N\tTS\tT-tlc-001\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\tblocks T-tlc-002\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\trelates T-tlc-003\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\tduplicated_by T-tlc2-001\thuman\t-\nE-N\tTS\tT-tlc-001\tlink\t-\trelates T-tlc-002\tw1\t-\nE-N\tTS\tT-tlc-001\tunlink\tblocks T-tlc-002\t-\thuman\t-\nE-N\tTS\tT-tlc-001\tunlink\trelates T-tlc-003\t-\tw1\t-\nE-N\tTS\tT-tlc-001\tpoints\t-\t2\thuman\t-\nE-N\tTS\tT-tlc-001\tsprint\t-\tS-1st\thuman\t-\nE-N\tTS\tT-tlc-001\tsprint\tS-1st\tS-2nd\thuman\t-\nE-N\tTS\tT-tlc-001\tsprint\tS-2nd\t-\thuman\t-'
eq "sprint-close: done stays" "$($TB history "$b" --json | jq -r 'select(.kind=="sprint") | .new' | tlsp)" $'S-1st'

unset -f tlerr tlmask tlsp; unset a b c x s1 s2 s3 s4 FAKE_AGENTS
[[ -z $_tl_saved ]] || while IFS= read -r _l; do export "$_l"; done <<<"$_tl_saved"
unset _tl_saved _l
