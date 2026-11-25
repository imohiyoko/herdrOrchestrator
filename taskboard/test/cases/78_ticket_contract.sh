# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb ticket(move / status と従来のコマンドへの振り分け)の使い方・文面・終了コード・保存内容(期待値は消した bash 版の出力から作った。今はこれが約束)。project は tkc だけを使う
_tk_saved=$(declare -p HERDR_PANE_ID TB_STATUSES TB_NOTIFY_ENABLED TB_NOTIFY_DEDUPE_SEC 2>/dev/null || true)
unset HERDR_PANE_ID
export TB_STATUSES=$TMP/tk-statuses.conf TB_NOTIFY_ENABLED=on TB_NOTIFY_DEDUPE_SEC=0; rm -f "$TB_STATUSES"
tkerr() { "$@" 2>&1; echo "rc=$?"; }
tkmask() { sed -E -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:]{8}Z?/TS/g'; }
tkrow() { sqlite3 "$TB_DB" "SELECT status, deliverables, IFNULL(summary,'NULL'), IFNULL(note,'NULL'), reported_at IS NULL FROM tasks WHERE id='$1';"; }
a=$($TB add -p tkc -t A); b=$($TB add -p tkc -t B); c=$($TB add -p tkc -t C)

# ---- 振り分け
eq "ticket: no args" "$(tkerr $TB ticket)" $'tb ticket move <id> <状態> [-m メモ] [-d 成果物 ...]   どの状態からどの状態へも(閉じた状態から戻すのも)。\n      -m は閉じた分類(done / cancelled)なら要約、それ以外ならメモ(note)に入る。-d は done の分類へ動かすときだけ\ntb ticket status [list] [--json]                      状態・分類・説明・チケットの件数\ntb ticket status add <状態> <分類> [-m 説明] [--after <状態>]   状態を足す(設定 statuses に書き足す。人だけ)\ntb ticket status rm <状態> [--to <状態>]              状態を外す(その状態のチケットが残っていれば --to で移すまで外さない。人だけ)\ntb ticket add|list|show|update|claim|done|block|cancel ...   従来の tb <それ> と同じ\nrc=2'
eq "ticket: help" "$(tkerr $TB ticket help)" $'tb ticket move <id> <状態> [-m メモ] [-d 成果物 ...]   どの状態からどの状態へも(閉じた状態から戻すのも)。\n      -m は閉じた分類(done / cancelled)なら要約、それ以外ならメモ(note)に入る。-d は done の分類へ動かすときだけ\ntb ticket status [list] [--json]                      状態・分類・説明・チケットの件数\ntb ticket status add <状態> <分類> [-m 説明] [--after <状態>]   状態を足す(設定 statuses に書き足す。人だけ)\ntb ticket status rm <状態> [--to <状態>]              状態を外す(その状態のチケットが残っていれば --to で移すまで外さない。人だけ)\ntb ticket add|list|show|update|claim|done|block|cancel ...   従来の tb <それ> と同じ\nrc=0'
eq "ticket: --help" "$(tkerr $TB ticket --help | tail -1)" $'rc=0'
eq "ticket: unknown" "$(tkerr $TB ticket bogus)" $'tb: ticket: 不明なサブコマンド bogus(move / status / add / list / show / update / claim / done / block / cancel)\n(tb help で使い方)\nrc=2'
eq "ticket: list" "$($TB ticket list -p tkc --json | jq -r '.id')" $'T-tkc-001\nT-tkc-002\nT-tkc-003'
eq "ticket: show" "$($TB ticket show "$a" | jq -r '.id + " " + .title')" $'T-tkc-001 A'
eq "ticket: update" "$(tkerr $TB ticket update "$c" --title C2)" $'T-tkc-003 updated\nrc=0'
eq "ticket: claim" "$(tkerr $TB ticket claim "$c" --owner w1)" $'T-tkc-003 claimed by w1\nrc=0'
eq "ticket: report not done" "$(tkerr $TB ticket report "$c")" $'tb: done ではないタスクは report できません(in_progress): T-tkc-003\nrc=1'
eq "ticket: link" "$(tkerr $TB ticket link "$a" blocks "$b")" $'T-tkc-001 blocks T-tkc-002\nrc=0'
eq "ticket: unlink" "$(tkerr $TB ticket unlink "$a" blocks "$b")" $'unlinked T-tkc-001 blocks T-tkc-002\nrc=0'
eq "ticket: tree" "$(tkerr $TB ticket tree "$a")" $'T-tkc-001\ttask\tmedium\ttodo\t-\tA\nrc=0'
eq "ticket: history" "$(tkerr $TB ticket history "$c" | cut -f2- | tkmask)" $'TS\tT-tkc-003\tcreated\t-\ttodo\thuman\t-\nTS\tT-tkc-003\ttitle\tC\tC2\thuman\t-\nTS\tT-tkc-003\tstatus\ttodo\tin_progress\thuman\t-\nTS\tT-tkc-003\towner\t-\tw1\thuman\t-\nrc=0'

# ---- move
eq "move: no args" "$(tkerr $TB ticket move)" $'tb: ticket move <id> <状態> [-m メモ] [-d 成果物 ...]\n(tb help で使い方)\nrc=2'
eq "move: one arg" "$(tkerr $TB ticket move "$a")" $'tb: ticket move <id> <状態> [-m メモ] [-d 成果物 ...]\n(tb help で使い方)\nrc=2'
eq "move: bad id" "$(tkerr $TB ticket move x done)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "move: unknown" "$(tkerr $TB ticket move "$a" review --bogus)" $'tb: ticket move: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "move: -m needs a value" "$(tkerr $TB ticket move "$a" review -m)" $'tb: -m に値が必要です\n(tb help で使い方)\nrc=2'
eq "move: bad status" "$(tkerr $TB ticket move "$a" nope)" $'tb: status は todo in_progress review done blocked cancelled のどれか: nope\n(tb help で使い方)\nrc=2'
eq "move: -d not done" "$(tkerr $TB ticket move "$a" review -d f)" $'tb: ticket move: -d(成果物)は done の分類へ動かすときだけ使えます: review\n(tb help で使い方)\nrc=2'
eq "move: missing" "$(tkerr $TB ticket move T-tkc-999 review)" $'tb: タスクが見つかりません: T-tkc-999\nrc=1'
eq "move: same" "$(tkerr $TB ticket move "$a" todo)" $'T-tkc-001 はすでに todo です\nrc=0'
eq "move: same with note" "$(tkerr $TB ticket move "$a" todo -m 'メモ')" $'T-tkc-001 todo → todo\nrc=0'
eq "move: to review" "$(tkerr $TB ticket move "$a" review --message '見て')" $'T-tkc-001 todo → review\nrc=0'
eq "move: to done" "$(tkerr $TB ticket move "$a" done -m '要約' -d 'out/a.md' -d ' ' --deliverable '<b>')" $'T-tkc-001 review → done\nrc=0'
eq "move: done row" "$(tkrow "$a")" $'done|["out/a.md","<b>"]|要約|見て|1'
eq "move: done notify" "$(sqlite3 "$TB_DB" "SELECT type, ref, text FROM notify_events WHERE ref='$a' ORDER BY id;")" $'done|T-tkc-001|[完了] T-tkc-001: 要約 / 成果物: out/a.md, ‹b›'
eq "move: done to done blank summary" "$(tkerr $TB ticket move "$a" done --summary '')" $'T-tkc-001 done → done\nrc=0'
eq "move: done to cancelled" "$(tkerr $TB ticket move "$a" cancelled)" $'T-tkc-001 done → cancelled\nrc=0'
eq "move: cancelled row" "$(tkrow "$a" | sed 's/|0$/|reported/')" $'cancelled|["out/a.md","<b>"]|NULL|見て|reported'
eq "move: back to todo" "$(tkerr $TB ticket move "$a" todo -m '')" $'T-tkc-001 cancelled → todo\nrc=0'
eq "move: todo row" "$(tkrow "$a")" $'todo|["out/a.md","<b>"]|NULL|NULL|0'
eq "move: history" "$($TB history "$a" | cut -f2- | tkmask)" $'TS\tT-tkc-001\tcreated\t-\ttodo\thuman\t-\nTS\tT-tkc-001\tlink\t-\tblocks T-tkc-002\thuman\t-\nTS\tT-tkc-001\tunlink\tblocks T-tkc-002\t-\thuman\t-\nTS\tT-tkc-001\tstatus\ttodo\treview\thuman\t-\nTS\tT-tkc-001\tstatus\treview\tdone\thuman\t-\nTS\tT-tkc-001\tstatus\tdone\tcancelled\thuman\t-\nTS\tT-tkc-001\tstatus\tcancelled\ttodo\thuman\t-'

# ---- status list
eq "status: default list" "$(tkerr $TB ticket status | grep -E '^(todo|in_progress|review|done|blocked|cancelled)	' | cut -f1,2,4; echo)" $'todo\ttodo\t\nin_progress\tactive\t\nreview\treview\t\nblocked\tblocked\t\ndone\tdone\t\ncancelled\tcancelled\t'
eq "status: --json goes to list" "$($TB ticket status --json | jq -c '{status,category,description,configured}')" $'{"status":"todo","category":"todo","description":"","configured":true}\n{"status":"in_progress","category":"active","description":"","configured":true}\n{"status":"review","category":"review","description":"","configured":true}\n{"status":"blocked","category":"blocked","description":"","configured":true}\n{"status":"done","category":"done","description":"","configured":true}\n{"status":"cancelled","category":"cancelled","description":"","configured":true}'
eq "status: list bad arg" "$(tkerr $TB ticket status list --bogus)" $'tb: ticket status list [--json]\n(tb help で使い方)\nrc=2'
eq "status: bad sub" "$(tkerr $TB ticket status bogus)" $'tb: ticket status [list [--json] | add <状態> <分類> [-m 説明] [--after <状態>] | rm <状態> [--to <状態>]]\n(tb help で使い方)\nrc=2'
eq "status: source" "$($TB ticket status 2>&1 >/dev/null | sed "s|$TMP|TMP|")" $'# 設定 statuses: none(状態 分類 件数 説明)'

# ---- status add
eq "add: no args" "$(tkerr $TB ticket status add)" $'tb: ticket status add <状態> <分類> [-m 説明] [--after <状態>]\n(tb help で使い方)\nrc=2'
eq "add: one arg" "$(tkerr $TB ticket status add x)" $'tb: ticket status add <状態> <分類> [-m 説明] [--after <状態>]\n(tb help で使い方)\nrc=2'
eq "add: unknown" "$(tkerr $TB ticket status add x todo --bogus)" $'tb: ticket status add: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "add: --after needs a value" "$(tkerr $TB ticket status add x todo --after)" $'tb: --after に値が必要です\n(tb help で使い方)\nrc=2'
eq "add: agent pane" "$(HERDR_PANE_ID=w1:p1 FAKE_AGENTS='[{"pane_id":"w1:p1","name":"w1","agent_status":"idle","agent_session":{"value":"s1"}}]' tkerr $TB ticket status add x todo)" $'tb: tb ticket status add は人が自分の端末で実行します(agent \'w1\' の pane からは実行できません)。記録 Q-1(例外は人が tb approval approve Q-1 で許可できます)\nrc=1'
eq "add: bad category" "$(tkerr $TB ticket status add x nope)" $'tb: ticket status add: 分類は todo active review blocked done cancelled のどれか: nope\n(tb help で使い方)\nrc=2'
eq "add: newline in desc" "$(tkerr $TB ticket status add x todo -m $'a\nb')" $'tb: ticket status add: 説明に改行と # は使えません\n(tb help で使い方)\nrc=2'
eq "add: # in desc" "$(tkerr $TB ticket status add x todo -m 'a#b')" $'tb: ticket status add: 説明に改行と # は使えません\n(tb help で使い方)\nrc=2'
eq "add: exists" "$(tkerr $TB ticket status add review review)" $'tb: 状態 review はすでにあります(分類 review)\nrc=1'
eq "add: bad after" "$(tkerr $TB ticket status add x todo --after nope)" $'tb: ticket status add: --after の状態がありません: nope\n(tb help で使い方)\nrc=2'
eq "add: bad name" "$(tkerr $TB ticket status add 'x*' todo)" $'7 行目: 状態は 1〜30 文字で、カンマ・引用符・* ? [ ] を含まない: x*\ntb: statuses の本文に問題があるため書きませんでした\nrc=1'
eq "add: appends" "$(tkerr $TB ticket status add backlog todo | sed "s|$TMP|TMP|")" $'状態 backlog を足しました(分類 todo。設定 statuses: file:TMP/tk-statuses.conf)\nrc=0'
eq "add: file body" "$(cat "$TB_STATUSES")" $'todo         todo\nin_progress  active\nreview       review\ndone         done\nblocked      blocked\ncancelled    cancelled\nbacklog todo'
eq "add: after" "$(tkerr $TB ticket status add qa review --description 'QA 待ち' --after review | sed "s|$TMP|TMP|")" $'状態 qa を足しました(分類 review。設定 statuses: file:TMP/tk-statuses.conf)\nrc=0'
eq "add: after body" "$(cat "$TB_STATUSES")" $'todo         todo\nin_progress  active\nreview       review\nqa review = QA 待ち\ndone         done\nblocked      blocked\ncancelled    cancelled\nbacklog todo'
eq "add: list shows desc" "$($TB ticket status list 2>/dev/null | grep -E '^(qa|backlog)	')" $'backlog\ttodo\t0\t\nqa\treview\t0\tQA 待ち'
eq "add: move to new" "$(tkerr $TB ticket move "$b" qa)" $'T-tkc-002 todo → qa\nrc=0'

# ---- status rm
eq "rm: no args" "$(tkerr $TB ticket status rm)" $'tb: ticket status rm <状態> [--to <状態>]\n(tb help で使い方)\nrc=2'
eq "rm: unknown" "$(tkerr $TB ticket status rm qa --bogus)" $'tb: ticket status rm: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "rm: agent pane" "$(HERDR_PANE_ID=w1:p1 FAKE_AGENTS='[{"pane_id":"w1:p1","name":"w1","agent_status":"idle","agent_session":{"value":"s1"}}]' tkerr $TB ticket status rm qa)" $'tb: tb ticket status rm は人が自分の端末で実行します(agent \'w1\' の pane からは実行できません)。記録 Q-2(例外は人が tb approval approve Q-2 で許可できます)\nrc=1'
eq "rm: not configured" "$(tkerr $TB ticket status rm nope)" $'tb: 状態 nope は設定にありません(tb ticket status で一覧)\nrc=1'
eq "rm: to itself" "$(tkerr $TB ticket status rm qa --to qa)" $'tb: ticket status rm: --to に外す状態そのものは指定できません\n(tb help で使い方)\nrc=2'
eq "rm: bad to" "$(tkerr $TB ticket status rm qa --to nope)" $'tb: status は todo in_progress review qa done blocked cancelled backlog のどれか: nope\n(tb help で使い方)\nrc=2'
eq "rm: tickets left" "$(tkerr $TB ticket status rm qa)" $'tb: 状態 qa のチケットが 1 件あります。--to <状態> で移してから外します(例: tb ticket status rm qa --to review)\nrc=1'
eq "rm: tickets left (todo cat)" "$($TB ticket move "$c" backlog >/dev/null; tkerr $TB ticket status rm backlog)" $'tb: 状態 backlog のチケットが 1 件あります。--to <状態> で移してから外します(例: tb ticket status rm backlog --to todo)\nrc=1'
eq "rm: breaks config" "$(printf 'todo todo\nin_progress active\ndone done\n' >"$TB_STATUSES"; tkerr $TB ticket status rm todo --to done)" $'分類 todo の状態が 1 つもありません\nrc=1'
$TB ticket status add qa review >/dev/null 2>&1; $TB ticket status add backlog todo >/dev/null 2>&1
$TB ticket move "$b" qa >/dev/null
eq "rm: moves tickets" "$(tkerr $TB ticket status rm qa --to in_progress | sed "s|$TMP|TMP|")" $'状態 qa のチケット 1 件を in_progress へ移しました\n状態 qa を外しました(設定 statuses: file:TMP/tk-statuses.conf)\nrc=0'
eq "rm: moved row" "$(tkrow "$b")" $'in_progress|[]|NULL|NULL|1'
eq "rm: body" "$(cat "$TB_STATUSES")" $'todo todo\nin_progress active\ndone done\nbacklog todo'
eq "rm: empty" "$($TB ticket move "$c" todo >/dev/null; tkerr $TB ticket status rm backlog | sed "s|$TMP|TMP|")" $'状態 backlog を外しました(設定 statuses: file:TMP/tk-statuses.conf)\nrc=0'
eq "rm: list shows removed" "$($TB ticket status list 2>/dev/null | grep -E '^(backlog|in_progress)	' | cut -f1,2,4)" $'in_progress\tactive\t'

# ---- 設定が DB の行のとき(共通の行に書く)
unset TB_STATUSES
eq "db: add writes the shared row" "$(tkerr $TB ticket status add tk_wait blocked -m '待ち')" $'状態 tk_wait を足しました(分類 blocked。設定 statuses: db:*)\nrc=0'
eq "db: row" "$(sqlite3 "$TB_DB" "SELECT herdr_session, body FROM config WHERE key='statuses';"; sqlite3 "$TB_DB" "SELECT herdr_session, changed_by, how FROM config_changes WHERE key='statuses' ORDER BY id DESC LIMIT 1;")" $'*|todo         todo\nin_progress  active\nreview       review\ndone         done\nblocked      blocked\ncancelled    cancelled\ntk_wait blocked = 待ち\n*|human|ticket status add tk_wait'
eq "db: rm" "$(tkerr $TB ticket status rm tk_wait)" $'状態 tk_wait を外しました(設定 statuses: db:*)\nrc=0'
eq "db: rm row" "$(sqlite3 "$TB_DB" "SELECT herdr_session, body FROM config WHERE key='statuses';")" $'*|todo         todo\nin_progress  active\nreview       review\ndone         done\nblocked      blocked\ncancelled    cancelled'
sqlite3 "$TB_DB" "DELETE FROM config WHERE key='statuses';"

unset HERDR_PANE_ID TB_STATUSES TB_NOTIFY_ENABLED TB_NOTIFY_DEDUPE_SEC
eval "$_tk_saved"
