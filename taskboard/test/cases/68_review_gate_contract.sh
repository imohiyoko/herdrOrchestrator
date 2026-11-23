# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb review(pack / submit)と tb gate の使い方・誤りの文面・終了コード(期待値は消した bash 版の出力から作った。今はこれが約束)
_rg_saved=$(env | grep -E '^(TB_DB|HERDR_PANE_ID|FAKE_AGENTS|TB_SCHED_LOGS)=' || true)
unset HERDR_PANE_ID
export TB_DB=$TMP/rgcontract.db TB_SCHED_LOGS=$TMP/rg-logs
rgerr() { "$@" 2>&1; echo "rc=$?"; }
rgts() { sed -E 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z/TS/g'; }
RGT=$($TB add -p rg -t 'review target')

# ---- tb review
eq "review: no subcommand → usage on stdout, rc 2" "$(rgerr $TB review)" $'tb review pack [--since ISO8601] [--mark]   会計役に渡すレビュー材料(集計と上位だけ)\ntb review submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]   レビュー結果を記録する\n(古い名前 tb review-pack / tb review-submit も同じに使える)\nrc=2'
eq "review help" "$(rgerr $TB review help)" $'tb review pack [--since ISO8601] [--mark]   会計役に渡すレビュー材料(集計と上位だけ)\ntb review submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]   レビュー結果を記録する\n(古い名前 tb review-pack / tb review-submit も同じに使える)\nrc=0'
eq "review: unknown subcommand" "$(rgerr $TB review bogus)" $'tb: review: 不明なサブコマンド bogus(pack / submit)\n(tb help で使い方)\nrc=2'
eq "review: empty subcommand" "$(rgerr $TB review '')" $'tb: review: 不明なサブコマンド (pack / submit)\n(tb help で使い方)\nrc=2'
eq "review pack: unknown arg" "$(rgerr $TB review pack --bogus)" $'tb: review-pack: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "review pack: --since needs a value" "$(rgerr $TB review pack --since)" $'tb: --since に値が必要です\n(tb help で使い方)\nrc=2'
eq "review pack: bad --since" "$(rgerr $TB review-pack --since 'x y')" $'tb: review-pack: --since は ISO 8601\n(tb help で使い方)\nrc=2'
eq "review pack: empty DB, no scheduler logs" "$(rgerr $TB review pack --since 2026-01-01T00:00:00Z | rgts)" $'# レビュー材料(TS 〜 TS)\n生ログではなく集計です。詳しく見たいものだけ tb のコマンドで引いてください(tb messages は無いので sqlite3 で messages テーブル)。\n\n## 送信(種類 | 結果 | 件数 | 平均文字数)\n\n## 送れなかった宛先(宛先 | busy | failed)\n\n## 長い送信(トークンを食うもの: 種類 | 宛先 | 文字数 | 先頭)\n\n## tb send(状態 | 件数)と拒否・承認待ちの例\n\n## 人の承認(承認された送信の操作 | 回数)— 何度も承認されているものは allow にする候補\n\n## 人への依頼の繰り返し(依頼元 | 種類 | 対象・内容 | 回数)— 同じ依頼を何度も人に出していないか(2 回以上だけ)\n\n## 仕事の混在(agent | ロール | project 数 | ロール変更 | project)— 1 体が複数種類の仕事をしていないか(該当だけ)\n\n## 報告(種類 | 件数)と、質問・ブロックの例\n\n## まとめ送信(回数 | 平均件数 | 平均文字数)\n0 | - | -\n\n## アラート(状態 | 緊急度 | 件数 | まとめた重複の合計 | 平均解析分)\n重複の多いアラート(見張り役の見回りが同じものを拾い続けていないか):\nlow と判定された解析(誤報・過剰な見回りの候補):\n\n## 観測(tb が見つけた滞留など: 種類 | 件数)\n\n## 役割の配布(agent | 回数)— compact が多い agent は文脈が大きすぎる候補\n\n## 定期実行(job | action | 件数)\n(scheduler のログなし)\n\n## 提案の状況(状態 | 件数)\nrc=0'
mkdir -p "$TB_SCHED_LOGS"
printf '%s\n' '{"ts":"2026-11-20T00:00:00Z","job":"a","action":"run"}' '{"ts":"2026-11-20T01:00:00Z","job":"a","action":"run"}' '{"ts":"2025-01-01T00:00:00Z","job":"old","action":"run"}' \
  '{"ts":"2026-11-21T00:00:00Z","job":"b  c","action":"skip"}' '{"ts":"2026-11-21T00:00:00Z","job":7}' 'not json' '{"ts":"2026-11-22T00:00:00Z","job":"after","action":"x"}' >"$TB_SCHED_LOGS/x.log"
$TB message send nobody --text hi >/dev/null 2>&1
eq "review pack: scheduler log counts, an unreadable line fails the pack (rc of jq)" "$(rgerr $TB review pack --since 2026-11-01T00:00:00Z | sed -n '/^## 定期実行/,/^## 提案/p')" $'## 定期実行(job | action | 件数)\n7 | null | 1\na | run | 2\nb c | skip | 1\nrc=5'
printf '%s\n' '{"ts":"2026-11-20T00:00:00Z","job":"a","action":"run"}' >"$TB_SCHED_LOGS/x.log"
eq "review pack: scheduler log counts" "$(rgerr $TB review pack --since 2026-11-01T00:00:00Z | sed -n '/^## 定期実行/,/^## 提案/p')" $'## 定期実行(job | action | 件数)\na | run | 1\n\n## 提案の状況(状態 | 件数)'
eq "review pack --mark records the range" "$(rgerr $TB review pack --mark >/dev/null; sqlite3 "$TB_DB" "SELECT COUNT(*) FROM reviews;")" $'1'
eq "review pack: default since is the last mark" "$(rgerr $TB review pack | head -1 | rgts)" $'# レビュー材料(TS 〜 TS)'
eq "review submit: no args" "$(rgerr $TB review submit)" $'tb: review-submit <task-id> --lineage L --sha S --blocker N --should N --verdict pass|fail [--pr o/r#n] [--evidence PATH]\n(tb help で使い方)\nrc=2'
eq "review submit: bad task id" "$(rgerr $TB review submit x)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "review submit: --sha needs a value" "$(rgerr $TB review-submit "$RGT" --sha)" $'tb: --sha に値が必要です\n(tb help で使い方)\nrc=2'
eq "review submit: unknown arg" "$(rgerr $TB review submit "$RGT" --by me)" $'tb: review-submit: 不明な引数 --by\n(tb help で使い方)\nrc=2'
eq "review submit: bad lineage" "$(rgerr $TB review submit "$RGT" --lineage X)" $'tb: --lineage は小文字英字で始まる英小文字・数字・- の 32 文字まで(例: self, coderabbit)\n(tb help で使い方)\nrc=2'
eq "review submit: pass with blockers" "$(rgerr $TB review submit "$RGT" --lineage self --sha 3f9c2ab --blocker 1 --should 0 --verdict pass)" $'tb: verdict=pass には blocker=0 が要ります\n(tb help で使い方)\nrc=2'
eq "review submit: unknown task" "$(rgerr $TB review submit T-rg-999 --lineage self --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)" $'tb: タスクが見つかりません: T-rg-999\nrc=1'
eq "review submit: reserved sender name" "$(FAKE_AGENTS='[{"pane_id":"wI:p1","name":"ingest"}]' HERDR_PANE_ID=wI:p1 rgerr $TB review submit "$RGT" --lineage self --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)" $'tb: 送信者名 ingest は取り込み口の予約名です。記録しません\nrc=5'
eq "review submit: pane without a name" "$(FAKE_AGENTS='[{"pane_id":"wI:p1"}]' HERDR_PANE_ID=wI:p1 rgerr $TB review submit "$RGT" --lineage self --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)" $'tb: pane wI:p1 の agent の名前を特定できないため記録しません\nrc=3'
eq "review submit: no owner" "$(FAKE_AGENTS='[{"pane_id":"wR:p1","name":"rev-a"}]' HERDR_PANE_ID=wR:p1 rgerr $TB review submit "$RGT" --lineage self --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)" $'tb: タスクの owner がいないため、作者と別かを確かめられません。記録しません\nrc=4'

# ---- tb gate
eq "gate: no subcommand" "$(rgerr $TB gate)" $'tb: 使い方: tb gate evaluate|execute <chain-id> --action merge|approve / tb gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <sha> / tb gate ingest-events <task-id> --pr <o>/<r>#<n> / tb gate reviewers <o>/<r>#<n> [--task <id>]\n(tb help で使い方)\nrc=2'
eq "gate: unknown subcommand" "$(rgerr $TB gate run)" $'tb: 使い方: tb gate evaluate|execute <chain-id> --action merge|approve / tb gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <sha> / tb gate ingest-events <task-id> --pr <o>/<r>#<n> / tb gate reviewers <o>/<r>#<n> [--task <id>]\n(tb help で使い方)\nrc=2'
eq "gate evaluate: --action needs a value" "$(rgerr $TB gate evaluate T-1 --action)" $'tb: --action に値がありません\n(tb help で使い方)\nrc=2'
eq "gate evaluate: two chain ids" "$(rgerr $TB gate evaluate a b --action merge)" $'tb: chain-id は 1 つだけです\n(tb help で使い方)\nrc=2'
eq "gate evaluate: unknown flag" "$(rgerr $TB gate evaluate T-1 --action merge --json)" $'tb: 不明な引数: --json\n(tb help で使い方)\nrc=2'
eq "gate evaluate: --pr needs a value" "$(rgerr $TB gate evaluate T-1 --action merge --pr)" $'tb: --pr は evaluate だけで、値が要ります\n(tb help で使い方)\nrc=2'
eq "gate execute: --fact is evaluate only" "$(rgerr $TB gate execute T-1 --action merge --fact x)" $'tb: --fact は evaluate だけで、値が要ります\n(tb help で使い方)\nrc=2'
eq "gate execute: --facts-stdin is evaluate only" "$(rgerr $TB gate execute T-1 --action merge --facts-stdin)" $'tb: --facts-stdin は evaluate だけです\n(tb help で使い方)\nrc=2'
eq "gate execute: always 30" "$(rgerr $TB gate execute T-1 --action approve)" $'gate: execute は無効です(段 0。何も実行せず、何も記録しません)\nrc=30'
eq "gate evaluate: facts from --fact and stdin, no config → 21" "$(printf 'HEAD 3f9c2ab\nWHATEVER x\n' | rgerr $TB gate evaluate T-1 --action merge --fact 'PR a/b#1' --facts-stdin)" $'gate: ask\naction: merge\nchain: T-1   pr: a/b#1   head: 3f9c2ab\nmode: -   # 段 0 は参考値だけ。実行は常に無効\nscore: - / threshold(merge) -\nhard: -\nreason: E-01 評価不能(fail-closed): 設定ファイルがありません(config/gate.conf)\nreason: E-01 評価不能(fail-closed): パス規則ファイルがありません(config/gate-paths.conf)\nreason: E-01 評価不能(fail-closed): 入力: 未知の行: WHATEVER\nreason: E-01 評価不能(fail-closed): 入力: TEST がありません(未実行は \'TEST none\')\nreason: E-01 評価不能(fail-closed): 入力: PUBLIC-CHECK がありません\nreason: E-01 評価不能(fail-closed): 入力: SIZE がありません\nreason: E-01 評価不能(fail-closed): 入力: AUTHOR がありません\nreason: E-01 評価不能(fail-closed): 入力: EXECUTOR がありません\nreason: E-01 評価不能(fail-closed): 入力: AUTHOR-REJECT-RATE がありません\nreason: E-01 評価不能(fail-closed): 入力: FILE がありません\nreason: E-01 評価不能(fail-closed): レビューの記録がありません(data/gate-reviews.log)\nrc=21'
eq "gate ingest-review: no args" "$(rgerr $TB gate ingest-review)" $'tb: gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <head の sha>\n(tb help で使い方)\nrc=2'
eq "gate ingest-review: bad task id" "$(rgerr $TB gate ingest-review x --pr a/b#1 --sha 3f9c2ab)" $'tb: タスク ID は T-<project>-NNN 形式: x\n(tb help で使い方)\nrc=2'
eq "gate ingest-review: unknown arg" "$(rgerr $TB gate ingest-review "$RGT" --pr a/b#1 --bogus)" $'tb: gate ingest-review: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "gate ingest-review: --pr needs a value" "$(rgerr $TB gate ingest-review "$RGT" --pr)" $'tb: --pr に値が必要です\n(tb help で使い方)\nrc=2'
eq "gate ingest-review: bad pr" "$(rgerr $TB gate ingest-review "$RGT" --pr a/b --sha 3f9c2ab)" $'tb: --pr は <owner>/<repo>#<n>\n(tb help で使い方)\nrc=2'
eq "gate ingest-review: bad sha" "$(rgerr $TB gate ingest-review "$RGT" --pr a/b#1 --sha XYZ)" $'tb: --sha は 7〜40 桁の小文字 16 進\n(tb help で使い方)\nrc=2'
eq "gate ingest-review: from an agent pane" "$(HERDR_PANE_ID=w1:p1 rgerr $TB gate ingest-review "$RGT" --pr a/b#1 --sha 3f9c2ab)" $'tb: gate ingest-review は agent の pane からは実行できません\nrc=5'
eq "gate ingest-review: unknown task" "$(rgerr $TB gate ingest-review T-rg-999 --pr a/b#1 --sha 3f9c2ab)" $'tb: タスクが見つかりません: T-rg-999\nrc=1'
eq "gate ingest-events: no args" "$(rgerr $TB gate ingest-events)" $'tb: gate ingest-events <task-id> --pr <o>/<r>#<n>\n(tb help で使い方)\nrc=2'
eq "gate ingest-events: --sha is not an option" "$(rgerr $TB gate ingest-events "$RGT" --pr a/b#1 --sha 3f9c2ab)" $'tb: gate ingest-events: 不明な引数 --sha\n(tb help で使い方)\nrc=2'
eq "gate ingest-events: bad pr" "$(rgerr $TB gate ingest-events "$RGT" --pr nope)" $'tb: --pr は <owner>/<repo>#<n>\n(tb help で使い方)\nrc=2'
eq "gate ingest-events: from an agent pane" "$(HERDR_PANE_ID=w1:p1 rgerr $TB gate ingest-events "$RGT" --pr a/b#1)" $'tb: gate ingest-events は agent の pane からは実行できません\nrc=5'
eq "gate reviewers: no args" "$(rgerr $TB gate reviewers)" $'tb: gate reviewers <o>/<r>#<n> [--task <id>]\n(tb help で使い方)\nrc=2'
eq "gate reviewers: bad pr" "$(rgerr $TB gate reviewers nope)" $'tb: 引数は <owner>/<repo>#<n>\n(tb help で使い方)\nrc=2'
eq "gate reviewers: unknown arg" "$(rgerr $TB gate reviewers a/b#1 --bogus)" $'tb: gate reviewers: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "gate reviewers: --task needs a value" "$(rgerr $TB gate reviewers a/b#1 --task)" $'tb: --task に値が必要です\n(tb help で使い方)\nrc=2'
eq "gate reviewers: bad --task" "$(rgerr $TB gate reviewers a/b#1 --task 'a b')" $'tb: --task の形が不正です\n(tb help で使い方)\nrc=2'

unset TB_SCHED_LOGS
while IFS= read -r _l; do [[ -n $_l ]] && export "$_l"; done <<<"$_rg_saved"
unset _rg_saved _l
