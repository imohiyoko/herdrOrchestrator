# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 報告コメントのコマンド(comment / comments / resolve / reopen / report / pending)の使い方・文面・終了コード・保存内容(bash と Go で同じ。期待値は bash の出力から作った)。project は tcm / tcm2 だけを使う
_tc_saved=$(declare -p HERDR_PANE_ID TB_IMMEDIATE_KINDS FAKE_STATUS 2>/dev/null || true)
export HERDR_PANE_ID=wW:p1 FAKE_STATUS=idle; unset TB_IMMEDIATE_KINDS
tcerr() { "$@" 2>&1; echo "rc=$?"; }
tcmask() { sed -E -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:]{8}Z?/TS/g' -e 's/\bC-[0-9]+\b/C-N/g'; }
tcrow() { sqlite3 "$TB_DB" "SELECT task_id, kind, text, deliverables, from_name, from_pane, from_session, delivered_at IS NULL, resolved_at IS NULL, IFNULL(resolved_note,'NULL') FROM comments WHERE id=${1#C-};"; }
a=$($TB add -p tcm -t A); b=$($TB add -p tcm -t B); c=$($TB add -p tcm2 -t C)

# ---- comment
eq "comment: no args" "$(tcerr $TB comment)" $'tb: comment <task-id> --kind done|progress|question|blocked --text T [--deliverables a,b] [--json]\n(tb help で使い方)\nrc=2'
eq "comment: bad id" "$(tcerr $TB comment x --kind progress --text t)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "comment: unknown" "$(tcerr $TB comment "$a" --bogus)" $'tb: comment: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "comment: --from refused" "$(tcerr $TB comment "$a" --from me)" $'tb: comment: 報告者は herdr から自動で決まります。--from は指定できません\n(tb help で使い方)\nrc=2'
eq "comment: --from-pane refused" "$(tcerr $TB comment "$a" --from-pane p)" $'tb: comment: 報告者は herdr から自動で決まります。--from-pane は指定できません\n(tb help で使い方)\nrc=2'
eq "comment: --session refused" "$(tcerr $TB comment "$a" --session s)" $'tb: comment: 報告者は herdr から自動で決まります。--session は指定できません\n(tb help で使い方)\nrc=2'
eq "comment: --kind needs a value" "$(tcerr $TB comment "$a" --kind)" $'tb: --kind に値が必要です\n(tb help で使い方)\nrc=2'
eq "comment: bad kind" "$(tcerr $TB comment "$a" --kind info --text t)" $'tb: comment: --kind は done progress question blocked のどれか\n(tb help で使い方)\nrc=2'
eq "comment: no kind" "$(tcerr $TB comment "$a" --text t)" $'tb: comment: --kind は done progress question blocked のどれか\n(tb help で使い方)\nrc=2'
eq "comment: no text" "$(tcerr $TB comment "$a" --kind progress)" $'tb: comment: --text が必要です\n(tb help で使い方)\nrc=2'
eq "comment: blank text" "$(tcerr $TB comment "$a" --kind progress --text '  ')" $'tb: comment: --text が必要です\n(tb help で使い方)\nrc=2'
eq "comment: done without deliverables" "$(tcerr $TB comment "$a" --kind done --text t)" $'tb: comment: done は --deliverables(成果物)が必須です\n(tb help で使い方)\nrc=2'
eq "comment: done blank deliverables" "$(tcerr $TB comment "$a" --kind done --text t --deliverables ' , ')" $'tb: comment: done は --deliverables(成果物)が必須です\n(tb help で使い方)\nrc=2'
eq "comment: missing task" "$(tcerr $TB comment T-tcm-999 --kind progress --text t)" $'tb: タスクが見つかりません: T-tcm-999\nrc=1'
eq "comment: no pane" "$(HERDR_PANE_ID= tcerr $TB comment "$a" --kind progress --text t)" $'tb: [identity.no-pane] HERDR_PANE_ID がありません。報告者を特定できないため保存しません\nrc=3'
eq "comment: unknown pane" "$(HERDR_PANE_ID=wW:p9 tcerr $TB comment "$a" --kind progress --text t)" $'tb: pane wW:p9 に該当する agent がありません。保存しません\nrc=3'
eq "comment: progress queued" "$(tcerr $TB comment "$a" --kind progress --text '途中' | tcmask)" $'C-N queued\nrc=0'
eq "comment: done queued" "$(tcerr $TB comment "$a" --kind done --text '終わり' --deliverables ' out/a.md ,, <b>&"c" ' | tcmask)" $'C-N queued\nrc=0'
eq "comment: question delivered" "$(tcerr $TB comment "$a" --kind question --text $'どちら\nですか' | tcmask)" $'C-N delivered\nrc=0'
eq "comment: blocked busy" "$(FAKE_STATUS=working tcerr $TB comment "$b" --kind blocked --text '止まった' | tcmask)" $'tb: 宛先 \'conductor\' が working のため送りません。未送信(pending に残す): C-N\nC-N saved-undelivered\nrc=10'
eq "comment: send fails" "$(FAKE_PROMPT_RC=1 tcerr $TB comment "$b" --kind question --text '送れない' | tcmask)" $'tb: 送信に失敗しました。未送信(pending に残す): C-N\n{"error":{"code":"x"}}\nC-N saved-undelivered\nrc=11'
eq "comment: immediate kinds from env" "$(TB_IMMEDIATE_KINDS=progress tcerr $TB comment "$c" --kind progress --text 'すぐ' | tcmask)" $'C-N delivered\nrc=0'
eq "comment: --json" "$($TB comment "$c" --kind progress --text 'json' --json | jq -c 'del(.created_at,.id)')" $'{"task_id":"T-tcm2-001","kind":"progress","text":"json","deliverables":[],"from_name":"worker1","from_pane":"wW:p1","from_session":"sess-w1","delivered_at":null,"resolved_at":null,"resolved_note":null,"herdr_session":"default"}'
eq "comment: rows" "$(sqlite3 "$TB_DB" "SELECT id FROM comments WHERE task_id LIKE 'T-tcm%' ORDER BY id;" | while read -r i; do tcrow "$i"; done)" $'T-tcm-001|progress|途中|[]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm-001|done|終わり|["out/a.md","<b>&\\"c\\""]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm-001|question|どちら\nですか|[]|worker1|wW:p1|sess-w1|0|1|NULL\nT-tcm-002|blocked|止まった|[]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm-002|question|送れない|[]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm2-001|progress|すぐ|[]|worker1|wW:p1|sess-w1|0|1|NULL\nT-tcm2-001|progress|json|[]|worker1|wW:p1|sess-w1|1|1|NULL'
eq "comment: done marks the task reported?" "$(sqlite3 "$TB_DB" "SELECT id, status, reported_at IS NULL FROM tasks WHERE id LIKE 'T-tcm%' ORDER BY id;")" $'T-tcm-001|todo|1\nT-tcm-002|todo|1\nT-tcm2-001|todo|1'
eq "comment: notify" "$(sqlite3 "$TB_DB" "SELECT type, ref, text FROM notify_events WHERE ref LIKE 'T-tcm%' OR text LIKE '%T-tcm%' ORDER BY id;" | tcmask)" $''

# ---- comments
eq "comments: unknown" "$(tcerr $TB comments --bogus)" $'tb: comments: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "comments: bad kind" "$(tcerr $TB comments "$a" --kind info)" $'tb: comments: --kind は done progress question blocked\n(tb help で使い方)\nrc=2'
eq "comments: task" "$(tcerr $TB comments "$a" | tcmask)" $'C-N\tprogress\t-\tundelivered\tT-tcm-001\tworker1\t途中\nC-N\tdone\t-\tundelivered\tT-tcm-001\tworker1\t終わり\nC-N\tquestion\tneeds-human\tdelivered\tT-tcm-001\tworker1\tどちら ですか\nrc=0'
eq "comments: task kind" "$(tcerr $TB comments "$a" --kind done | tcmask)" $'C-N\tdone\t-\tundelivered\tT-tcm-001\tworker1\t終わり\nrc=0'
eq "comments: empty kind is no filter" "$(tcerr $TB comments "$a" --kind '' | cut -f2)" $'progress\ndone\nquestion\nrc=0'
eq "comments: last kind wins" "$(tcerr $TB comments "$a" --kind info --kind done | cut -f2)" $'done\nrc=0'
eq "comments: last kind is checked" "$(tcerr $TB comments "$a" --kind done --kind info)" $'tb: comments: --kind は done progress question blocked\n(tb help で使い方)\nrc=2'
eq "comments: open" "$(tcerr $TB comments --open | grep -E 'tcm|rc=' | tcmask)" $'C-N\tquestion\tneeds-human\tdelivered\tT-tcm-001\tworker1\tどちら ですか\nC-N\tblocked\tneeds-human\tundelivered\tT-tcm-002\tworker1\t止まった\nC-N\tquestion\tneeds-human\tundelivered\tT-tcm-002\tworker1\t送れない\nrc=0'
eq "comments: json" "$($TB comments "$b" --json | jq -c 'del(.created_at,.id,.delivered_at)')" $'{"task_id":"T-tcm-002","kind":"blocked","text":"止まった","deliverables":[],"from_name":"worker1","from_pane":"wW:p1","from_session":"sess-w1","resolved_at":null,"resolved_note":null,"herdr_session":"default"}\n{"task_id":"T-tcm-002","kind":"question","text":"送れない","deliverables":[],"from_name":"worker1","from_pane":"wW:p1","from_session":"sess-w1","resolved_at":null,"resolved_note":null,"herdr_session":"default"}'
eq "comments: missing task" "$(tcerr $TB comments T-tcm-999)" $'rc=0'

q=$(HERDR_PANE_ID= $TB comments "$a" --kind question --json | jq -r .id | tail -1)
p=$(HERDR_PANE_ID= $TB comments "$a" --kind progress --json | jq -r .id | tail -1)
# ---- resolve
eq "resolve: no args" "$(HERDR_PANE_ID= tcerr $TB resolve)" $'tb: resolve <C-n> [-m <メモ>]\n(tb help で使い方)\nrc=2'
eq "resolve: agent pane" "$(tcerr $TB resolve "$q")" $'tb: tb resolve は人が自分の端末で実行します(agent \'worker1\' の pane からは実行できません)\nrc=1'
eq "resolve: bad id" "$(HERDR_PANE_ID= tcerr $TB resolve X-1)" $'tb: resolve: ID は C-<数字>: X-1\n(tb help で使い方)\nrc=2'
eq "resolve: unknown" "$(HERDR_PANE_ID= tcerr $TB resolve "$q" --bogus)" $'tb: resolve: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "resolve: -m needs a value" "$(HERDR_PANE_ID= tcerr $TB resolve "$q" -m)" $'tb: -m に値が必要です\n(tb help で使い方)\nrc=2'
eq "resolve: note too long" "$(HERDR_PANE_ID= tcerr $TB resolve "$q" -m "$(printf 'あ%.0s' $(seq 1 501))")" $'tb: resolve: メモは 500 字まで\n(tb help で使い方)\nrc=2'
eq "resolve: missing" "$(HERDR_PANE_ID= tcerr $TB resolve C-99999)" $'tb: 報告が見つかりません: C-99999\nrc=1'
eq "resolve: progress" "$(HERDR_PANE_ID= tcerr $TB resolve "$p" | tcmask)" $'tb: 対応済みにできるのは question / blocked だけです(C-N は progress)\nrc=1'
eq "resolve: ok" "$(HERDR_PANE_ID= tcerr $TB resolve "$q" -m '決めた' | tcmask)" $'C-N resolved\nrc=0'
eq "resolve: twice" "$(HERDR_PANE_ID= tcerr $TB resolve "$q" | tcmask)" $'C-N はすでに対応済みです(TS)\nrc=0'
eq "resolve: row" "$(tcrow "$q")" $'T-tcm-001|question|どちら\nですか|[]|worker1|wW:p1|sess-w1|0|0|決めた'

# ---- reopen
eq "reopen: no args" "$(HERDR_PANE_ID= tcerr $TB reopen)" $'tb: reopen <C-n> [-m <理由>]\n(tb help で使い方)\nrc=2'
eq "reopen: agent pane" "$(tcerr $TB reopen "$q")" $'tb: tb reopen は人が自分の端末で実行します(agent \'worker1\' の pane からは実行できません)\nrc=1'
eq "reopen: bad id" "$(HERDR_PANE_ID= tcerr $TB reopen X-1)" $'tb: reopen: ID は C-<数字>: X-1\n(tb help で使い方)\nrc=2'
eq "reopen: missing" "$(HERDR_PANE_ID= tcerr $TB reopen C-99999)" $'tb: 報告が見つかりません: C-99999\nrc=1'
eq "reopen: progress" "$(HERDR_PANE_ID= tcerr $TB reopen "$p" | tcmask)" $'tb: 未対応に戻せるのは question / blocked だけです(C-N は progress)\nrc=1'
eq "reopen: ok" "$(HERDR_PANE_ID= tcerr $TB reopen "$q" -m '誤り' | tcmask)" $'C-N reopened\nrc=0'
eq "reopen: again" "$(HERDR_PANE_ID= tcerr $TB reopen "$q" | tcmask)" $'C-N は対応済みではありません(未対応のまま)\nrc=0'
eq "reopen: row and event" "$(tcrow "$q"; sqlite3 "$TB_DB" "SELECT kind, actor, IFNULL(old,'NULL'), IFNULL(new,'NULL') FROM task_events WHERE ref='$q';" | tcmask)" $'T-tcm-001|question|どちら\nですか|[]|worker1|wW:p1|sess-w1|0|1|NULL\ncomment|worker1|NULL|question\ncomment_reopened|human|TS|誤り'

# ---- pending / report
eq "pending: unknown" "$(tcerr $TB pending --bogus)" $'tb: pending: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "pending: tsv" "$(tcerr $TB pending | grep -E 'tcm|rc=' | tcmask)" $'C-N\ttcm\tconductor\t[progress] 途中\nC-N\ttcm\tconductor\t[done] 終わり\nC-N\ttcm\tconductor\t[blocked] 止まった\nC-N\ttcm\tconductor\t[question] 送れない\nC-N\ttcm2\tconductor\t[progress] json\nrc=0'
eq "pending: ids exit 0" "$(rc $TB pending --ids)" "0"
eq "pending: ids" "$(diff <($TB pending --ids | grep -Fx -f <(sqlite3 "$TB_DB" "SELECT 'C-'||id FROM comments WHERE task_id LIKE 'T-tcm%' UNION SELECT id FROM tasks WHERE id LIKE 'T-tcm%';")) <(sqlite3 "$TB_DB" "SELECT 'C-'||id FROM comments WHERE task_id LIKE 'T-tcm%' AND text IN ('途中','終わり','止まった','送れない','json') ORDER BY id;") && echo same)" $'same'
eq "report: no args" "$(tcerr $TB report)" $'tb: report <id>\n(tb help で使い方)\nrc=2'
eq "report: bad id" "$(tcerr $TB report x)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "report: missing task" "$(tcerr $TB report T-tcm-999)" $'tb: タスクが見つかりません: T-tcm-999\nrc=1'
eq "report: missing comment" "$(tcerr $TB report C-99999)" $'tb: コメントが見つかりません: C-99999\nrc=1'
eq "report: not done" "$(tcerr $TB report "$b" | tcmask)" $'tb: done ではないタスクは report できません(todo): T-tcm-002\nrc=1'
$TB done "$b" -d out/b.md -m 'B 要約' >/dev/null
eq "report: busy" "$(FAKE_STATUS=working tcerr $TB report "$b" | tcmask)" $'tb: 宛先 \'conductor\' が working のため送りません。未送信(pending に残す): C-N\nrc=10'
eq "report: ok" "$(tcerr $TB report "$b" | tcmask)" $'T-tcm-002 reported to conductor (C-N)\nrc=0'
eq "report: twice" "$(tcerr $TB report "$b" | tcmask)" $'T-tcm-002 は報告済みです(TS)\nrc=0'
eq "report: comment redeliver" "$(tcerr $TB report "$(sqlite3 "$TB_DB" "SELECT 'C-'||min(id) FROM comments WHERE task_id='$b' AND delivered_at IS NULL;")" | tcmask)" $'C-N delivered to conductor\nrc=0'
eq "report: comment already delivered" "$(tcerr $TB report "$q" | tcmask)" $'C-N は配送済みです(TS)\nrc=0'
eq "pending: after" "$(tcerr $TB pending | grep -E 'tcm|rc=' | tcmask)" $'C-N\ttcm\tconductor\t[progress] 途中\nC-N\ttcm\tconductor\t[done] 終わり\nC-N\ttcm\tconductor\t[question] 送れない\nC-N\ttcm2\tconductor\t[progress] json\nrc=0'
eq "report: rows" "$(sqlite3 "$TB_DB" "SELECT id, reported_at IS NULL FROM tasks WHERE id LIKE 'T-tcm%' ORDER BY id;"; sqlite3 "$TB_DB" "SELECT id FROM comments WHERE task_id LIKE 'T-tcm%' ORDER BY id;" | while read -r i; do tcrow "$i"; done)" $'T-tcm-001|1\nT-tcm-002|0\nT-tcm2-001|1\nT-tcm-001|progress|途中|[]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm-001|done|終わり|["out/a.md","<b>&\\"c\\""]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm-001|question|どちら\nですか|[]|worker1|wW:p1|sess-w1|0|1|NULL\nT-tcm-002|blocked|止まった|[]|worker1|wW:p1|sess-w1|0|1|NULL\nT-tcm-002|question|送れない|[]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm2-001|progress|すぐ|[]|worker1|wW:p1|sess-w1|0|1|NULL\nT-tcm2-001|progress|json|[]|worker1|wW:p1|sess-w1|1|1|NULL\nT-tcm-002|done|B 要約|["out/b.md"]|-|-|-|0|1|NULL'
eq "comment: immediate kinds split on spaces only" "$(TB_IMMEDIATE_KINDS=$'progress\tquestion' tcerr $TB comment "$c" --kind progress --text 'タブ' | tcmask)" $'C-N queued\nrc=0'
eq "reopen: old is the cleared time" "$(HERDR_PANE_ID= $TB resolve "$q" >/dev/null; t=$(sqlite3 "$TB_DB" "SELECT resolved_at FROM comments WHERE id=${q#C-};"); HERDR_PANE_ID= $TB reopen "$q" >/dev/null; sqlite3 "$TB_DB" "SELECT old='$t' FROM task_events WHERE ref='$q' AND kind='comment_reopened' ORDER BY id DESC LIMIT 1;")" $'1'

unset HERDR_PANE_ID TB_IMMEDIATE_KINDS FAKE_STATUS
eval "$_tc_saved"
