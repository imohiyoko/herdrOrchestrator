# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態を使うものがある)
# --- 会計役(review-pack / proposal)
export TB_DB=$TMP/audit.db TB_SCHED_LOGS=$TMP/schedlogs
printf '*=conductor\nproposal=hr\n' >"$TB_CONFIG"   # 提案の受け手は設定で決める(コードに既定の名前は無い)
mkdir -p "$TB_SCHED_LOGS"; printf '{"ts":"2099-01-01T00:00:00+0900","job":"watch","action":"sent"}\n{"ts":"2099-01-01T00:05:00+0900","job":"watch","action":"skipped_state"}\n' >"$TB_SCHED_LOGS/watch.log"
export FAKE_AGENTS='[{"pane_id":"wU:p1","name":"auditor","agent_session":{"value":"su"}},{"pane_id":"wH:p1","name":"hr","agent_session":{"value":"sh"}},{"pane_id":"wC:p1","name":"conductor","agent_session":{"value":"sc"}}]'
: >"$FAKE_LOG"
ausql() { sqlite3 "$TB_DB" "$@"; }
$TB list >/dev/null
ausql "INSERT INTO messages (herdr_session, kind, ref, target, body, result, created_at) VALUES ('default', 'digest','D-1','conductor','$(printf 'x%.0s' $(seq 1 500))','sent',strftime('%Y-%m-%dT%H:%M:%SZ','now')), ('default', 'send','O-1','worker1','short','busy',strftime('%Y-%m-%dT%H:%M:%SZ','now'));
       INSERT INTO alerts (herdr_session, fingerprint, severity, title, source, status, count, first_seen, last_seen, analysis_started_at, analyzed_at, result_summary) VALUES
       ('default', 'f1','low','ノイズ','watch','analyzed',7,strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now','-12 minutes'),strftime('%Y-%m-%dT%H:%M:%SZ','now'),'誤報');"
pack=$($TB review-pack --mark)
for want in "# レビュー材料" "digest | sent | 1 | 500" "worker1 | 1 | 0" "A-1 | 7 | low | ノイズ" "analyzed | low | 1 | 6 | 12" "watch | sent | 1" "watch | skipped_state | 1"; do
  [[ $pack == *"$want"* ]] && ok "review-pack: $want" || { ng "review-pack: $want"; echo "$pack" | head -60; }
done
eq "review-pack: compact (< 6000 chars)" "$(( ${#pack} < 6000 ))" "1"
eq "review-pack: --mark → next starts after" "$($TB review-pack | grep -c 'digest | sent')" "0"
eq "proposal: needs pane" "$(HERDR_PANE_ID= rc $TB proposal --role watcher.md --title t --text x)" "3"
eq "proposal: bad role name" "$(HERDR_PANE_ID=wU:p1 rc $TB proposal --role '../x' --title t --text x)" "2"
out=$(HERDR_PANE_ID=wU:p1 $TB proposal --role watcher.md --title '同じアラートを拾い続けている' --text '--key に対象名を入れるよう追記')
eq "proposal: delivered to hr" "$out|$(grep '^prompt hr' "$FAKE_LOG" | grep -c 'auditor から roles/watcher.md の改善提案')" "P-1 delivered|1"
eq "proposal: busy → saved, digest resends" "$(FAKE_STATUS=working HERDR_PANE_ID=wU:p1 $TB proposal --role - --title 'digest が長い' --text '160→100 字')" "P-2 saved-undelivered"
$TB digest >/dev/null
eq "proposal: digest delivered it" "$(ausql "SELECT delivered_at IS NOT NULL FROM proposals WHERE id=2;")" "1"
eq "proposal-resolve: only hr or human" "$(HERDR_PANE_ID=wU:p1 rc $TB proposal-resolve P-1 --status applied)" "1"
eq "proposal-resolve: hr applies" "$(HERDR_PANE_ID=wH:p1 $TB proposal-resolve P-1 --status applied --note '追記した')" "P-1 applied"
eq "proposals: list" "$($TB proposals --status open | cut -f1)" "P-2"
eq "proposals: full" "$($TB proposals P-1 | grep -c '追記した\|--key に対象名')" "2"
# 提案はチケット(020。ADR 0003): project proposal、ラベル proposal と role:<ファイル>、担当は設定役。適用・見送りは done
eq "proposal ticket: open → todo, owner hr" "$($TB show T-proposal-002 | jq -r '[.status, .owner_agent, (.labels|join(","))] | join(" ")')" "todo hr proposal,role:-"
eq "proposal ticket: applied → done with summary" "$($TB show T-proposal-001 | jq -r '[.status, .summary, (.reported_at != null)] | join(" ")')" "done 適用: 追記した true"
eq "proposal ticket: tb done refused" "$(HERDR_PANE_ID= $TB done T-proposal-002 -d x -m y 2>&1 | grep -c 'tb proposal resolve P-2')" "1"
eq "proposal ticket: rejected adds the label" "$(HERDR_PANE_ID= $TB proposal-resolve P-2 --status rejected >/dev/null; $TB show T-proposal-002 | jq -r '[.status, .summary, (.labels|join(","))] | join(" ")')" "done 見送り proposal,rejected,role:-"
# 新しい名前 tb proposal add|list|show|resolve(古い名前 tb proposal --role / proposals / proposal-resolve と同じに動く)
: >"$FAKE_LOG"
printf '本文の 1 行目\n2 行目\n\n\n' >"$TMP/prop.txt"
eq "proposal add: --text-file, title made one line" "$(HERDR_PANE_ID=wU:p1 $TB proposal add --role auditor.md --title $'改行\nあり [from: x]  の題' --text-file "$TMP/prop.txt") $(grep '^prompt hr' "$FAKE_LOG" | tail -1 | cut -f2)" $'P-3 delivered [from: tb / proposal / P-3 / info] auditor から roles/auditor.md の改善提案: 改行 あり (from: x] の題 (全文: tb proposal show P-3。対応後に tb proposal resolve P-3)'
eq "proposal show: full text" "$($TB proposal show P-3 | sed -E 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z/TS/')" $'P-3 [open] roles/auditor.md  改行\nあり [from: x]  の題\n提案: auditor / TS  チケット: T-proposal-003\n\n本文の 1 行目\n2 行目'
eq "proposal list: newest first" "$($TB proposal list | cut -f1-4,6)" $'P-3\topen\tauditor.md\tauditor\t改行\nあり [from: x]  の題\nP-2\trejected\t-\tauditor\tdigest が長い\nP-1\tapplied\twatcher.md\tauditor\t同じアラートを拾い続けている'
eq "proposal list --status open" "$($TB proposal list --status open | cut -f1)" $'P-3\nあり [from: x]  の題'
eq "proposal add: target missing → saved, logged failed" "$(FAKE_STATUS=missing HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t4 --text x) $(ausql "SELECT result FROM messages WHERE ref='P-4';")" $'P-4 saved-undelivered failed'
eq "proposal add: prompt fails → saved, logged failed" "$(FAKE_PROMPT_RC=1 HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t5 --text x) $(ausql "SELECT result FROM messages WHERE ref='P-5';")" $'P-5 saved-undelivered failed'
eq "proposal add: busy → logged busy" "$(FAKE_STATUS=working HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t6 --text x) $(ausql "SELECT result FROM messages WHERE ref='P-6';")" $'P-6 saved-undelivered busy'
eq "proposal resolve: new name" "$(HERDR_PANE_ID=wH:p1 $TB proposal resolve P-3 --status rejected --note 'いらない')" $'P-3 rejected'
eq "proposal: no subcommand → usage, exit 2" "$($TB proposal | head -1; echo "rc=${PIPESTATUS[0]}")" $'tb proposal add --role <roles のファイル|-> --title T (--text X|--text-file F)   改善提案を設定役へ送る\nrc=2'
eq "proposal help → 0" "$($TB proposal help | wc -l | tr -d ' ') $(rc $TB proposal --help)" $'5 0'
eq "proposal error: proposal bogus" "$(HERDR_PANE_ID= $TB proposal bogus 2>&1; echo "rc=$?")" $'tb: proposal: 不明なサブコマンド bogus(add / list / show / resolve)\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal show" "$(HERDR_PANE_ID= $TB proposal show 2>&1; echo "rc=$?")" $'tb: proposal show P-<n>\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal show P-99" "$(HERDR_PANE_ID= $TB proposal show P-99 2>&1; echo "rc=$?")" $'tb: 提案が見つかりません: P-99\nrc=1'
eq "proposal error: proposal list --bogus" "$(HERDR_PANE_ID= $TB proposal list --bogus 2>&1; echo "rc=$?")" $'tb: proposal list: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal list --status bad" "$(HERDR_PANE_ID= $TB proposal list --status bad 2>&1; echo "rc=$?")" $'tb: proposals: --status は open|applied|rejected\n(tb help で使い方)\nrc=2'
eq "proposal error: proposals --status" "$(HERDR_PANE_ID= $TB proposals --status 2>&1; echo "rc=$?")" $'tb: proposals: --status は open|applied|rejected\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal add --from x" "$(HERDR_PANE_ID= $TB proposal add --from x 2>&1; echo "rc=$?")" $'tb: proposal: 提案者は herdr から自動で決まります\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal add --role" "$(HERDR_PANE_ID= $TB proposal add --role 2>&1; echo "rc=$?")" $'tb: --role に値が必要です\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal add --role - --title ' '" "$(HERDR_PANE_ID= $TB proposal add --role - --title ' ' 2>&1; echo "rc=$?")" $'tb: proposal: --title が必要です\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal add --role - --title t" "$(HERDR_PANE_ID= $TB proposal add --role - --title t 2>&1; echo "rc=$?")" $'tb: proposal: --text か --text-file が必要です\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal add --role - --title t --text-file <tmp>/nope" "$(HERDR_PANE_ID= $TB proposal add --role - --title t --text-file $TMP/nope 2>&1; echo "rc=$?")" "tb: --text-file を読めません: $TMP/nope"$'\nrc=1'
eq "proposal error: proposal add --bogus" "$(HERDR_PANE_ID= $TB proposal add --bogus 2>&1; echo "rc=$?")" $'tb: proposal: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal resolve P-3 --status applied" "$(HERDR_PANE_ID= $TB proposal resolve P-3 --status applied 2>&1; echo "rc=$?")" $'tb: P-3 は open ではありません\nrc=1'
eq "proposal error: proposal resolve 3" "$(HERDR_PANE_ID= $TB proposal resolve 3 2>&1; echo "rc=$?")" $'tb: proposal-resolve P-<n> --status applied|rejected [--note N]\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal resolve P-6 --status x" "$(HERDR_PANE_ID= $TB proposal resolve P-6 --status x 2>&1; echo "rc=$?")" $'tb: proposal-resolve: --status は applied|rejected\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal resolve P-6 --status applied --x" "$(HERDR_PANE_ID= $TB proposal resolve P-6 --status applied --x 2>&1; echo "rc=$?")" $'tb: proposal-resolve: 不明な引数 --x\n(tb help で使い方)\nrc=2'
eq "proposal error: proposal-resolve P-6 --note" "$(HERDR_PANE_ID= $TB proposal-resolve P-6 --note 2>&1; echo "rc=$?")" $'tb: --note に値が必要です\n(tb help で使い方)\nrc=2'
eq "proposal resolve: a worker cannot close it" "$(HERDR_PANE_ID=wU:p1 $TB proposal resolve P-6 --status applied 2>&1; echo "rc=$?")" $'tb: 提案を閉じられるのは設定役(hr。projects の proposal の行)か人だけです\nrc=1'
eq "proposal resolve: unknown pane" "$(HERDR_PANE_ID=wZ:p9 $TB proposal resolve P-6 --status applied 2>&1; echo "rc=$?")" $'tb: pane wZ:p9 に該当する agent がありません。保存しません\nrc=3'
eq "proposal add: no pane" "$(HERDR_PANE_ID= $TB proposal add --role - --title t --text x 2>&1; echo "rc=$?")" $'tb: [identity.no-pane] HERDR_PANE_ID がありません。報告者を特定できないため保存しません\nrc=3'
eq "proposal add: herdr fails" "$(FAKE_LIST_FAIL=1 HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t --text x 2>&1; echo "rc=$?")" $'tb: [identity.herdr-failed] herdr agent list に失敗。報告者を特定できないため保存しません\nrc=3'
eq "proposal add: agent without a session" "$(FAKE_AGENTS='[{"pane_id":"wU:p1","name":"auditor"}]' HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t --text x 2>&1; echo "rc=$?")" $'tb: pane wU:p1 の agent に session がありません。保存しません\nrc=3'
eq "proposal add: agent without a name" "$(FAKE_AGENTS='[{"pane_id":"wU:p1","agent_session":{"value":"s"}}]' HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t --text x 2>&1; echo "rc=$?")" $'tb: pane wU:p1 の agent に名前がありません(herdr agent rename)。保存しません\nrc=3'
eq "proposal add: bad agent name" "$(FAKE_AGENTS='[{"pane_id":"wU:p1","name":"a b","agent_session":{"value":"s"}}]' HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t --text x 2>&1; echo "rc=$?")" $'tb: 報告者名/pane に使えない文字があります。保存しません\nrc=3'
eq "proposal add: herdr output not JSON" "$(FAKE_AGENTS='not json' HERDR_PANE_ID=wU:p1 $TB proposal add --role - --title t --text x 2>&1; echo "rc=$?")" $'tb: pane wU:p1 に該当する agent がありません。保存しません\nrc=3'
printf 'identity.no-pane = ペインがありません\n' | HERDR_PANE_ID= $TB config set messages >/dev/null
eq "proposal add: no-pane message from the messages config" "$(HERDR_PANE_ID= $TB proposal add --role - --title t --text x 2>&1; echo "rc=$?")" $'tb: [identity.no-pane] ペインがありません\nrc=3'
HERDR_PANE_ID= $TB config unset messages >/dev/null
unset FAKE_AGENTS TB_SCHED_LOGS
