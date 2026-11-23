# tb の Go への移行(進み具合)

[ADR 0002](adr/0002-services-and-binary-distribution.md) の手順 3(コマンドを 1 つずつ移す)の進み具合と、残りの順番。サービスの名前は `.claude/skills/tb-command-naming` の一覧に合わせる。

## 決まり

- **1 PR で 1 サービス**。他の PR が bash 側を書き換えている最中のサービスは後に回す
- **移し終えた** = そのサービスのコマンドがすべて `herdrctl tb --commands` に出ていて、`taskboard/test/cases` のそのサービスのケースが **書き換えなしで** Go でも通った。出力・終了コード・エラーの文面は bash と同じにした。Go のユニットテストも付けた
- **同期**: CI の `tb` ジョブが全ケースを herdrctl で流す
- **bash を消した**: tb の全サービスが移ったので、`taskboard/lib/*.sh` を消した。`taskboard/bin/tb` は checkout の場所・DB・セッションを渡して `herdrctl tb` に置き換わるだけの shell(`help` だけは自分で `lib/help.txt` を出す。hook 用の `touch` / `role-context` は herdrctl が無くても何も出さず 0)。herdrctl は `TB_HERDRCTL` > PATH で、無い・古い(認可と呼び出し元の確かめが無い)と tb は止まる。bash の振る舞いを直接試していたテスト(リスク評価・DB の見張り)は Go のテストに移した(リスク評価は bash 版の出力を写した golden と突き合わせる)

## コマンドの名前

新しいコマンドは `.claude/skills/tb-command-naming` の形(`tb <サービス> <サブコマンド>`)の名前で作る。古い名前(`tb proxy-request` など)は別名として同じに動かす。role の文面・README・webui などの呼び出し元は、移す PR で新しい名前に直す。

## 進み具合

| 順 | サービス | コマンド | 状態 |
| --- | --- | --- | --- |
| – | ticket | `list` `show` `tree` `labels` `sprints` `board` `history` `events` `link` `unlink` `sprint-add` `sprint-start` `sprint-close` `claim` `done` `block` `cancel` `add` `update` `comment` `comments` `resolve` `reopen` と、新しい名前 `tb ticket move/status/add/list/show/update/claim/done/block/cancel/report/history/tree/link/unlink` | 済み |
| 1 | activity | `touch` `activity` | 済み |
| 2 | proxy | `proxy request/list/resolve/check`(古い名前 `proxy-*` も) | 済み |
| 2 | job | `job list/show/set/edit/unset/import/history`(古い名前 `sched-job` も) | 済み |
| 2 | handoff | `handoff write`(古い形 `handoff <id>` も) | 済み |
| 2 | whoami | `whoami`(呼び出し元の確かめと権限の門の前に答える。bin/tb はセッションの環境変数を書き換えずに渡す) | 済み |
| 2 | 土台の小さいもの | scheduler からの送信 `system-send`、旧形式の DB の取り込み `db-import`(写しは VACUUM INTO で作り、Go のスキーマ移行で最新にしてから取り込む) | 済み |
| 2 | 認可の表 | `policy`(`tb policy import [--apply]` で古い approvals / grants を policy の行に変換。checkout のファイルは状態ディレクトリへ退かす) | 済み |
| 3 | config | `config list/show/set/edit/unset/import/history`(9 種類の設定の検査も Go に) | 済み |
| 3 | proposal | `proposal add/list/show/resolve`(古い名前 `proposal --role ...` `proposals` `proposal-resolve` も) | 済み |
| 3 | notify | `notify status/on/off/test/dry-run/tail/ack/flush/events/hash/event/rules`(`pr-watch` は pr サービスとして移した) | 済み |
| 3 | approval | `approval check/allow/ask/approve/reject`(古い名前 `approval <操作>` `allow` `ask` `approve` `reject` も)。判定に使う役割(agent)・承認後の届け方(message)の部品も Go に | 済み |
| 3 | message | `message send/flush/outbox/inbox/read`(古い名前 `send` `flush` `outbox` `inbox` `inbox-read` も)。flush の前の台帳の同期(`agent_sync`)も `internal/agents` に | 済み(`report` / `pending` も報告コメントの配送と一緒に移した) |
| 3 | review / gate | `review pack/submit`(古い名前 `review-pack` `review-submit` も)と `gate evaluate/execute/ingest-review/ingest-events/reviewers`。設定と記録の置き場所は bash と同じく TB_ROOT の下に固定 | 済み |
| 3 | pr | `pr queue add/list/show/approve/reject/retry/run/scan` と `pr watch`(古い名前 `pr-queue <サブコマンド>` `pr-watch` も)。利用者が書く正規表現(`body_first_line`・公開前検査の規則と例外)は bash と同じ意味になるよう `grep -E` で評価する | 済み |
| 3 | alert / digest | `alert` / `alerts` / `alert-approve` / `alert-result` / `alert-dispatch` / `alert-close`(解析役の起動と結果の受け渡しも)と `digest` / `digests`。digest の scheduler の見張り(schedwatch.sh。ログの集計と schedoff / schedstale / schedfail / schedwait)も Go に。ジョブの実効設定は今までどおり scheduler の show.sh を呼んで読む | 済み |
| 4 | reminder | `reminder list/check/publish/scan/nudge`(古い名前 `reminders [--check / --publish]` `due-scan` `nudge` も)と `deadlines-import` | 済み |
| 4 | agent(一部) | `limits show/get/set/unset/mode/check/watcher-enabled`(spawn_* が無いときの spawn-policy の値も)、読むだけの `can` / `org` / `peek`、役割の `role-context` / `role-broadcast` / `role-show` / `role-list` / `role-write` / `role-edit` / `role-unset` / `role-set` / `role-log` / `roles`、文脈の圧縮 `compact-check`、スラッシュコマンドの送信 `slash`、`agent-ops` / `agent-flag` / `agent-kinds` / `agent-new`(`tb agent open`)/ `agent-stop` / `agent-revive` / `agent-restart` / `model-set`(`tb agent model set`)/ `agent-spawn` / `agent-approve` / `agent-reject`(スラッシュコマンドの承認も) / `spawn-freeze` / `spawn-unfreeze`、絞る候補の `agent-shed`、一覧の `agents`(`tb agent list`。`--json` / `--all` / `--graph` / `--group role` も) 。新しい名前 `tb agent <動詞>`(作るのは `tb agent open`)も足した。`tb agent` / `tb recipe` の一覧(引数なし・help)と不明な動詞の言い方も Go に | 済み |
| 5 | conv | `conv route/routes-set/draft/voice/voice-check/length/kinds/patterns/lint`、学習の `conv pair-add/pairs/pair-show/candidate-add/ledger/candidate-adopt/candidate-reject/scan/learn-purge`、段階の `conv stage/stage-set/stage-reset/stage-log/incident/gate`(古い名前 `conv-<サブコマンド>` も)。文字列の一般化と検査(sanitize / scan / ratio)は bash と同じく `lib/convtext.py` を呼ぶ | 済み |
| 5 | recipe | 部品の `parts` / `part-check` / `part-risk`、レシピの登録と読み取り `recipe-set` / `recipe-rm` / `recipe-list` / `recipe-show` / `recipe-history`(部品の表は `internal/recipe` と recipes.sh の両方に置き、`go test` が同じかを確かめる)、実行の `recipe-run` / `recipe-runner` / `recipe-queue` / `recipe-cancel`(判定・待ち行列・外の実行役)。新しい名前 `tb recipe <動詞>` と `tb recipe part list/check/risk` も足した | 済み(`tb recipe` だけ・help の使い方の表示は bash) |
| 6 | scheduler の読み込み | `herdrctl sched show`(scheduler/bin/show.sh が渡す)。ジョブ定義(conf)は bash として実行せず、決まった書き方(KEY=値。値は `'…'` / `"…"` / 引用なし)だけを読み、展開(`$` や `` ` ``)・コマンド・知らないキーは拒む。CI が scheduler のテストを herdrctl で流す | 済み |
| 6 | scheduler の判定・実行 | `herdrctl sched tick`(scheduler/bin/tick.sh が渡す)。曜日・時間帯・間隔・一時停止の判定、GATE・COMMAND(ロック・時間切れ)・PROMPT_CMD / PROMPT_RULES / PROMPT_VARS、herdr の状態の確かめと tb system-send での送信、ログの 1 行の形と理由の文言は tick.sh と同じ。時間切れはプロセスグループごと止める | 済み |
| 6 | scheduler の起動器 | `herdrctl sched jobs` / `dispatch` / `gen-plist` / `install`(jobs.sh・dispatch.sh・gen-plist.sh・install.sh が lib.sh の `sched_go` から渡す)。dispatch はジョブごとに `herdrctl sched tick` を別のプロセスで並べる。plist の中身・ラベル・メッセージは元の bash 版と同じ | 済み |
| 6 | scheduler の bash 版を消す | `scheduler/bin/*.sh` は環境をそろえて `herdrctl sched <サブコマンド>` に置き換わるだけ。herdrctl が無い・古いとエラー(exit 2)。展開($ や `)を含む定義のジョブは動かない(`setup/check.sh` の `job:<job>` が warn で知らせる) | 済み |
| 6 | webui | webui | 移行中: ログインまわりとサーバーの土台、画面の枠・緊急通知・送信・セッション一覧は Go(`herdrctl webui serve`、`WEBUI_IMPL=go`)。`go test ./internal/webui` が同じ DB で Python 版と HTML を突き合わせる。ほかの画面は順に移す |

サービスのコマンドより先に、ほかのサービスが使う部品だけを移すことがある(例: approval の PR で `internal/agents` の役割の引き方と `internal/message` の届け方)。そのサービスのコマンドはまだ bash のままで、移す PR でこの部品を使う。

土台(`internal/kernel`)は、移すサービスが使う分だけ足していく: store(DB)、env(既定の置き場所)、config(設定を引く・書く)、herdr(herdr アダプタ)、identity(人か agent か。報告者の名前 `resolve_label` も)、transport(herdr への送信 `herdr_send` と messages への記録)、util(`oneline` など)。

`touch` は hook から毎回呼ばれるので、herdrctl が無い・古いときは何も出さずに 0 で終わる(`bin/tb`)。
