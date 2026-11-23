# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- チケットを作る・変える(add / update)の使い方・文面・終了コード・保存する値・履歴(bash と Go で同じ。期待値は bash の出力から作った)。project は tec / tec2 だけを使う
# 並行の検査は 11_tickets.sh と 12_sprints.sh、用意したラベルは 11_labels_defined.sh
_te_saved=$(declare -p HERDR_PANE_ID TB_STATUSES TB_LABELS TB_SYSTEM_LABELS 2>/dev/null || true)
unset HERDR_PANE_ID
export TB_STATUSES=$TMP/te-statuses.conf TB_LABELS=$TMP/te-labels.conf TB_SYSTEM_LABELS=$TMP/te-syslabels.conf; rm -f "$TB_STATUSES" "$TB_LABELS" "$TB_SYSTEM_LABELS"
teerr() { "$@" 2>&1; echo "rc=$?"; }
temask() { sed -E -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:]{8}Z?/TS/g' -e 's/\bE-[0-9]+\b/E-N/g' -e 's/\bS-[0-9]+\b/S-N/g'; }
terow() { sqlite3 "$TB_DB" "SELECT id, status, title, type, priority, IFNULL(parent_id,'-'), IFNULL(owner_agent,'-'), IFNULL(session_id,'-'), IFNULL(account,'-'), labels, IFNULL(description,'-'), refs, IFNULL(due,'-'), IFNULL(sprint_id,'-') IS NOT '-', IFNULL(points,'-'), reported_at IS NULL FROM tasks WHERE id='$1';"; }

# ---- add: 使い方
eq "add: no args" "$(teerr $TB add)" $'tb: add: -p <project> が必要です\n(tb help で使い方)\nrc=2'
eq "add: no title" "$(teerr $TB add -p tec)" $'tb: add: -t <title> が必要です\n(tb help で使い方)\nrc=2'
eq "add: blank title" "$(teerr $TB add -p tec -t ' ')" $'tb: add: -t <title> が必要です\n(tb help で使い方)\nrc=2'
eq "add: bad project" "$(teerr $TB add -p 'a-b' -t x)" $'tb: project は英数字と _ のみ: a-b\n(tb help で使い方)\nrc=2'
eq "add: -t needs a value" "$(teerr $TB add -p tec -t)" $'tb: -t に値が必要です\n(tb help で使い方)\nrc=2'
eq "add: unknown" "$(teerr $TB add -p tec -t x --bogus)" $'tb: add: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "add: bad type" "$(teerr $TB add -p tec -t x --type feature)" $'tb: 種別は epic story task bug subtask のどれか: feature\n(tb help で使い方)\nrc=2'
eq "add: bad priority" "$(teerr $TB add -p tec -t x --priority urgent)" $'tb: 優先度は highest high medium low lowest のどれか: urgent\n(tb help で使い方)\nrc=2'
eq "add: bad points" "$(teerr $TB add -p tec -t x --points 10000)" $'tb: ポイントは 0〜9999 の整数: 10000\n(tb help で使い方)\nrc=2'
eq "add: bad due" "$(teerr $TB add -p tec -t x --due 2026-13-01)" $'tb: 期日は YYYY-MM-DD の日付: 2026-13-01\n(tb help で使い方)\nrc=2'
eq "add: bad label" "$(teerr $TB add -p tec -t x -l 'a"b')" $'tb: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: a"b\n(tb help で使い方)\nrc=2'
eq "add: long label" "$(teerr $TB add -p tec -t x -l "$(printf 'あ%.0s' {1..41})")" $'tb: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: あああああああああああああああああああああああああああああああああああああああああ\n(tb help で使い方)\nrc=2'
eq "add: bad ref kind" "$(teerr $TB add -p tec -t x --ref 'Bad=https://x.test')" $'tb: 外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): Bad=https://x.test\n(tb help で使い方)\nrc=2'
eq "add: bad ref url" "$(teerr $TB add -p tec -t x --ref 'ftp://x.test')" $'tb: 外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): ftp://x.test\n(tb help で使い方)\nrc=2'
eq "add: ref with space" "$(teerr $TB add -p tec -t x --ref 'https://x.test/a b')" $'tb: 外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): https://x.test/a b\n(tb help で使い方)\nrc=2'
eq "add: bad parent" "$(teerr $TB add -p tec -t x --parent x)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "add: missing parent" "$(teerr $TB add -p tec -t x --parent T-tec-999)" $'tb: タスクが見つかりません: T-tec-999\nrc=1'
eq "add: subtask needs a parent" "$(teerr $TB add -p tec -t x --type subtask)" $'tb: subtask には --parent が必要です\n(tb help で使い方)\nrc=2'
eq "add: bad sprint" "$(teerr $TB add -p tec -t x --sprint 3)" $'tb: スプリント ID は S-<番号> 形式: 3\n(tb help で使い方)\nrc=2'
eq "add: missing sprint" "$(teerr $TB add -p tec -t x --sprint S-999999)" $'tb: スプリントが見つかりません: S-999999\nrc=1'
eq "add: nothing written" "$(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM tasks WHERE project='tec';")" $'0'

# ---- add: 保存する値
sa=$($TB sprint-add -p tec -n s); so=$($TB sprint-add -p tec2 -n o); sc=$($TB sprint-add -p tec -n c); $TB sprint-close "$sc" >/dev/null
eq "add: plain" "$(teerr $TB add -p tec -t "題	タブ")" $'T-tec-001\nrc=0'
eq "add: full" "$(teerr $TB add --project tec --title E --type epic --priority highest -l 'b, a' -l a,,c --label ' ' \
  --desc $'説明\n2 行' --ref https://acme.slack.com/archives/C1/p1 --ref https://github.com/o/r/pull/1 --ref 'doc=https://x.test/d?a=1&b=2' \
  --ref https://x.test/e --ref https://github.com/o/r/pull/1 --ref ' ' --due 2026-12-01 --sprint "$sa" --points 0 --owner w1 --session s1 --account acct)" $'T-tec-002\nrc=0'
eq "add: story under epic" "$(teerr $TB add -p tec -t S --type story --parent T-tec-002 --due '' --points 5)" $'T-tec-003\nrc=0'
eq "add: subtask under story" "$(teerr $TB add -p tec -t ST --type subtask --parent T-tec-003)" $'T-tec-004\nrc=0'
eq "add: epic cannot have a parent" "$(teerr $TB add -p tec -t x --type epic --parent T-tec-002)" $'tb: epic は親を持てません\n(tb help で使い方)\nrc=2'
eq "add: subtask cannot be a parent" "$(teerr $TB add -p tec -t x --parent T-tec-004)" $'tb: subtask は親にできません: T-tec-004\n(tb help で使い方)\nrc=2'
eq "add: subtask not directly under epic" "$(teerr $TB add -p tec -t x --type subtask --parent T-tec-002)" $'tb: subtask の親は story / task / bug にする(epic の直下には置けない): T-tec-002\n(tb help で使い方)\nrc=2'
eq "add: other project's sprint" "$(teerr $TB add -p tec -t x --sprint "$so" | temask)" $'tb: スプリント S-N は project tec2 のものです(タスクは tec)\n(tb help で使い方)\nrc=2'
eq "add: closed sprint" "$(teerr $TB add -p tec -t x --sprint "$sc" | temask)" $'tb: 閉じたスプリントには入れられません: S-N\n(tb help で使い方)\nrc=2'
eq "add: numbering per project" "$(teerr $TB add -p tec2 -t X)" $'T-tec2-001\nrc=0'
eq "add: rows" "$(for i in 001 002 003 004; do terow "T-tec-$i"; done; terow T-tec2-001)" $'T-tec-001|todo|題\tタブ|task|medium|-|-|-|-|[]|-|[]|-|0|-|1\nT-tec-002|todo|E|epic|highest|-|w1|s1|acct|["a","b","c"]|説明\n2 行|[{"kind":"slack","url":"https://acme.slack.com/archives/C1/p1"},{"kind":"github","url":"https://github.com/o/r/pull/1"},{"kind":"doc","url":"https://x.test/d?a=1&b=2"},{"kind":"link","url":"https://x.test/e"}]|2026-12-01|1|0|1\nT-tec-003|todo|S|story|medium|T-tec-002|-|-|-|[]|-|[]|-|0|5|1\nT-tec-004|todo|ST|subtask|medium|T-tec-003|-|-|-|[]|-|[]|-|0|-|1\nT-tec2-001|todo|X|task|medium|-|-|-|-|[]|-|[]|-|0|-|1'
eq "add: history" "$($TB history T-tec-002 | temask)" $'E-N\tTS\tT-tec-002\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-tec-002\tlabel_added\t-\ta\thuman\t-\nE-N\tTS\tT-tec-002\tlabel_added\t-\tb\thuman\t-\nE-N\tTS\tT-tec-002\tlabel_added\t-\tc\thuman\t-\nE-N\tTS\tT-tec-002\tref_added\t-\thttps://acme.slack.com/archives/C1/p1\thuman\t-\nE-N\tTS\tT-tec-002\tref_added\t-\thttps://github.com/o/r/pull/1\thuman\t-\nE-N\tTS\tT-tec-002\tref_added\t-\thttps://x.test/d?a=1&b=2\thuman\t-\nE-N\tTS\tT-tec-002\tref_added\t-\thttps://x.test/e\thuman\t-'

# ---- update: 使い方
eq "update: no args" "$(teerr $TB update)" $'tb: update <id> [--status S] [--title T] [--owner N] [--session S] [--account A] [--parent ID] [--type T] [--priority P] [--desc D] [--due YYYY-MM-DD|\'\'] [--label L ...] [--add-label L] [--remove-label L]\n(tb help で使い方)\nrc=2'
eq "update: bad id" "$(teerr $TB update x --title y)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "update: nothing" "$(teerr $TB update T-tec-001)" $'tb: update: 変更する項目がありません\n(tb help で使い方)\nrc=2'
eq "update: unknown" "$(teerr $TB update T-tec-001 --bogus)" $'tb: update: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "update: needs a value" "$(teerr $TB update T-tec-001 --title)" $'tb: --title に値が必要です\n(tb help で使い方)\nrc=2'
eq "update: missing" "$(teerr $TB update T-tec-999 --title y)" $'tb: タスクが見つかりません: T-tec-999\nrc=1'
eq "update: bad status" "$(teerr $TB update T-tec-001 --status doing)" $'tb: status は todo in_progress review done blocked cancelled のどれか: doing\n(tb help で使い方)\nrc=2'
eq "update: blank title" "$(teerr $TB update T-tec-001 --title '')" $'tb: update: title が空です\n(tb help で使い方)\nrc=2'
eq "update: self parent" "$(teerr $TB update T-tec-001 --parent T-tec-001)" $'tb: update: 自分自身を parent にできません\n(tb help で使い方)\nrc=2'
eq "update: bad remove-ref" "$(teerr $TB update T-tec-001 --remove-ref x)" $'tb: 外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): x\n(tb help で使い方)\nrc=2'
eq "update: bad points" "$(teerr $TB update T-tec-001 --points x)" $'tb: ポイントは 0〜9999 の整数: x\n(tb help で使い方)\nrc=2'
eq "update: bad due" "$(teerr $TB update T-tec-001 --due 2026-02-30)" $'tb: 期日は YYYY-MM-DD の日付: 2026-02-30\n(tb help で使い方)\nrc=2'
eq "update: epic with a parent" "$(teerr $TB update T-tec-002 --parent T-tec-001)" $'tb: epic は親を持てません\n(tb help で使い方)\nrc=2'
eq "update: parent cycle" "$(teerr $TB update T-tec-001 --type story; teerr $TB update T-tec-002 --type story; teerr $TB update T-tec-002 --parent T-tec-003)" $'T-tec-001 updated\nrc=0\nT-tec-002 updated\nrc=0\ntb: 親子が循環します: T-tec-002 → T-tec-003\n(tb help で使い方)\nrc=2'
eq "update: subtask with children" "$(teerr $TB update T-tec-003 --type subtask)" $'tb: 子のあるタスクは subtask にできません: T-tec-003\n(tb help で使い方)\nrc=2'
eq "update: epic with subtask child" "$(teerr $TB update T-tec-003 --type epic)" $'tb: epic は親を持てません\n(tb help で使い方)\nrc=2'
eq "update: closed sprint" "$(teerr $TB update T-tec-001 --sprint "$sc" | temask)" $'tb: 閉じたスプリントには入れられません: S-N\n(tb help で使い方)\nrc=2'

# ---- update: 保存する値
eq "update: many" "$(teerr $TB update T-tec-001 --title 新しい題 --priority low --owner w2 --session s2 --account a2 --desc d --due 2026-11-30 \
  --label x,y --add-label z --remove-label y --add-ref https://x.test/1 --add-ref 'pr=https://github.com/o/r/pull/9' --sprint "$sa" --points 3)" $'T-tec-001 updated\nrc=0'
eq "update: clear" "$(teerr $TB update T-tec-001 --owner '' --session '' --account '' --desc '' --due '' --label '' --sprint '' --points '' --remove-ref https://x.test/1)" $'T-tec-001 updated\nrc=0'
eq "update: refs replace kind" "$(teerr $TB update T-tec-002 --add-ref 'gh=https://github.com/o/r/pull/1' --remove-ref https://x.test/e)" $'T-tec-002 updated\nrc=0'
eq "update: labels add only" "$(teerr $TB update T-tec-002 --add-label 'q,a' --remove-label nope)" $'T-tec-002 updated\nrc=0'
eq "update: --sprint - is no sprint" "$(teerr $TB update T-tec-002 --title E2 --sprint -)" $'T-tec-002 updated\nrc=0'
eq "update: only --sprint - is nothing" "$(teerr $TB update T-tec-002 --sprint -)" $'tb: update: 変更する項目がありません\n(tb help で使い方)\nrc=2'
eq "update: label with a newline keeps the first line" "$(teerr $TB update T-tec-002 --add-label $'nl1,nl2\nnl3'; $TB show T-tec-002 | jq -c .labels)" $'T-tec-002 updated\nrc=0\n["a","b","c","nl1","nl2","q"]'
eq "list: nbsp label is accepted like add" "$(teerr $TB add -p tec -t nb -l $'ops\u00a0ui'; teerr $TB list -p tec -l $'ops\u00a0ui')" $'T-tec-005\nrc=0\nT-tec-005\ttodo\t-\tnb\tmedium\nrc=0'
eq "update: rows" "$(terow T-tec-001; terow T-tec-002)" $'T-tec-001|todo|新しい題|story|low|-|-|-|-|[]|-|[{"kind":"pr","url":"https://github.com/o/r/pull/9"}]|-|0|-|1\nT-tec-002|todo|E2|story|highest|-|w1|s1|acct|["a","b","c","nl1","nl2","q"]|説明\n2 行|[{"kind":"slack","url":"https://acme.slack.com/archives/C1/p1"},{"kind":"doc","url":"https://x.test/d?a=1&b=2"},{"kind":"gh","url":"https://github.com/o/r/pull/1"}]|2026-12-01|1|0|1'
eq "update: history" "$($TB history T-tec-001 | temask)" $'E-N\tTS\tT-tec-001\tcreated\t-\ttodo\thuman\t-\nE-N\tTS\tT-tec-001\ttype\ttask\tstory\thuman\t-\nE-N\tTS\tT-tec-001\ttitle\t題\tタブ\t新しい題\thuman\t-\nE-N\tTS\tT-tec-001\tpriority\tmedium\tlow\thuman\t-\nE-N\tTS\tT-tec-001\towner\t-\tw2\thuman\t-\nE-N\tTS\tT-tec-001\tdue\t-\t2026-11-30\thuman\t-\nE-N\tTS\tT-tec-001\tpoints\t-\t3\thuman\t-\nE-N\tTS\tT-tec-001\tsprint\t-\tS-N\thuman\t-\nE-N\tTS\tT-tec-001\tdescription\t-\t-\thuman\t-\nE-N\tTS\tT-tec-001\tlabel_added\t-\tx\thuman\t-\nE-N\tTS\tT-tec-001\tlabel_added\t-\tz\thuman\t-\nE-N\tTS\tT-tec-001\tref_added\t-\thttps://x.test/1\thuman\t-\nE-N\tTS\tT-tec-001\tref_added\t-\thttps://github.com/o/r/pull/9\thuman\t-\nE-N\tTS\tT-tec-001\towner\tw2\t-\thuman\t-\nE-N\tTS\tT-tec-001\tdue\t2026-11-30\t-\thuman\t-\nE-N\tTS\tT-tec-001\tpoints\t3\t-\thuman\t-\nE-N\tTS\tT-tec-001\tsprint\tS-N\t-\thuman\t-\nE-N\tTS\tT-tec-001\tdescription\t-\t-\thuman\t-\nE-N\tTS\tT-tec-001\tlabel_removed\tx\t-\thuman\t-\nE-N\tTS\tT-tec-001\tlabel_removed\tz\t-\thuman\t-\nE-N\tTS\tT-tec-001\tref_removed\thttps://x.test/1\t-\thuman\t-'

# ---- update --status(分類に入ったときだけ完了報告の列を合わせる)
sqlite3 "$TB_DB" "UPDATE tasks SET reported_at='x' WHERE id='T-tec2-001';"
eq "status: done" "$(teerr $TB update T-tec2-001 --status done)" $'T-tec2-001 updated\nrc=0'
eq "status: done clears reported_at" "$(terow T-tec2-001)" $'T-tec2-001|done|X|task|medium|-|-|-|-|[]|-|[]|-|0|-|1'
sqlite3 "$TB_DB" "UPDATE tasks SET reported_at=NULL WHERE id='T-tec2-001';"
eq "status: cancelled" "$(teerr $TB update T-tec2-001 --status cancelled)" $'T-tec2-001 updated\nrc=0'
eq "status: cancelled fills reported_at" "$(sqlite3 "$TB_DB" "SELECT status, reported_at GLOB '????-??-??T??:??:??Z' FROM tasks WHERE id='T-tec2-001';")" $'cancelled|1'
eq "status: back to todo" "$(teerr $TB update T-tec2-001 --status todo)" $'T-tec2-001 updated\nrc=0'
eq "status: notify done once" "$(TB_NOTIFY_ENABLED=on TB_NOTIFY_DEDUPE_SEC=0 $TB update T-tec2-001 --status done >/dev/null; TB_NOTIFY_ENABLED=on TB_NOTIFY_DEDUPE_SEC=0 $TB update T-tec2-001 --status done >/dev/null; sqlite3 "$TB_DB" "SELECT type, ref FROM notify_events WHERE ref='T-tec2-001';")" $'done|T-tec2-001'

# ---- 設定 labels / system-labels / statuses
printf 'ui = 画面\napi\n' >"$TB_LABELS"; printf 'deadline = due-date\n' >"$TB_SYSTEM_LABELS"
eq "labels: undefined on add" "$(teerr $TB add -p tec2 -t L -l ui,zz,yy)" $'tb: 用意されていないラベルです: yy zz(使えるのは: ui api。一覧は tb labels --defined、足すのは人が tb config edit labels)\n(tb help で使い方)\nrc=2'
eq "labels: system labels ok" "$(teerr $TB add -p tec2 -t L -l ui,alert,role:x,due-date)" $'T-tec2-002\nrc=0'
eq "labels: builtin deadline is not system now" "$(teerr $TB add -p tec2 -t L -l deadline)" $'tb: 用意されていないラベルです: deadline(使えるのは: ui api。一覧は tb labels --defined、足すのは人が tb config edit labels)\n(tb help で使い方)\nrc=2'
eq "labels: update add undefined" "$(teerr $TB update T-tec2-002 --add-label nope)" $'tb: 用意されていないラベルです: nope(使えるのは: ui api。一覧は tb labels --defined、足すのは人が tb config edit labels)\n(tb help で使い方)\nrc=2'
eq "labels: remove undefined ok" "$(teerr $TB update T-tec2-002 --remove-label alert)" $'T-tec2-002 updated\nrc=0'
printf 'bad line\n' >"$TB_SYSTEM_LABELS"
eq "labels: broken system-labels" "$(teerr $TB add -p tec2 -t L -l deadline)" $'tb: 設定 system-labels に不正な行があるので組み込みのラベルを使います(tb config edit system-labels で直す)\nT-tec2-003\nrc=0'
rm -f "$TB_LABELS" "$TB_SYSTEM_LABELS"
printf 'backlog todo\ndoing active\nshipped done\n' >"$TB_STATUSES"
eq "statuses: add writes first todo" "$(teerr $TB add -p tec2 -t M)" $'T-tec2-004\nrc=0'
eq "statuses: update to configured" "$(teerr $TB update T-tec2-004 --status shipped)" $'T-tec2-004 updated\nrc=0'
eq "statuses: builtin name not listed" "$(teerr $TB update T-tec2-004 --status done)" $'tb: status は backlog doing shipped のどれか: done\n(tb help で使い方)\nrc=2'
eq "statuses: rows" "$(terow T-tec2-004)" $'T-tec2-004|shipped|M|task|medium|-|-|-|-|[]|-|[]|-|0|-|1'
rm -f "$TB_STATUSES"

unset -f teerr temask terow; unset sa so sc HERDR_PANE_ID TB_STATUSES TB_LABELS TB_SYSTEM_LABELS
eval "$_te_saved"; unset _te_saved
