# tb の Go への移行(進み具合)

[ADR 0002](adr/0002-services-and-binary-distribution.md) の手順 3(コマンドを 1 つずつ移す)の進み具合と、残りの順番。サービスの名前は `.claude/skills/tb-command-naming` の一覧に合わせる。

## 決まり

- **1 PR で 1 サービス**。他の PR が bash 側を書き換えている最中のサービスは後に回す
- **移し終えた** = そのサービスのコマンドがすべて `herdrctl tb --commands` に出ていて、`taskboard/test/cases` のそのサービスのケースが **書き換えなしで** `TB_IMPL=go` でも通る。出力・終了コード・エラーの文面は bash と同じ。Go のユニットテストも付ける
- **同期**: CI の `tb contract` ジョブが全ケースを `TB_IMPL=go` で流す。移したコマンドを bash 側だけ変えるとこのジョブが落ちる。ケースで確かめていない振る舞いは、移す PR でケースを足して埋める
- **bash を消す時期**: tb の全サービスが移ったら、まず既定を `TB_IMPL=go` にする PR を出す。setup が herdrctl を必ず入れるようになり、しばらく様子を見てから `taskboard/lib/*.sh` を消し、`tb` を herdrctl を呼ぶだけの shell にする

## コマンドの名前

Go に移すサービスは、`.claude/skills/tb-command-naming` の形(`tb <サービス> <サブコマンド>`)の新しい名前で作る。bash にも同じ名前の `cmd_<サービス>` を足し、古い名前(`tb proxy-request` など)は別名として同じに動かす。role の文面・README・webui などの呼び出し元は、移す PR で新しい名前に直す。

## 進み具合

| 順 | サービス | コマンド | 状態 |
| --- | --- | --- | --- |
| – | ticket | `list` `show` `tree` `labels` `sprints` `board` `history` `events` `link` `unlink` `sprint-add` `sprint-start` `sprint-close` `claim` `done` `block` `cancel` `add` `update` `comment` `comments` `resolve` `reopen` と、新しい名前 `tb ticket move/status/add/list/show/update/claim/done/block/cancel/report/history/tree/link/unlink` | 済み |
| 1 | activity | `touch` `activity` | 済み |
| 2 | proxy | `proxy request/list/resolve/check`(古い名前 `proxy-*` も) | 済み |
| 2 | job | `job list/show/set/edit/unset/import/history`(古い名前 `sched-job` も) | 済み |
| 2 | handoff | `handoff write`(古い形 `handoff <id>` も) | 済み |
| 2 | whoami | `whoami` | まだ(`bin/tb` の入口を作り直すときに一緒に) |
| 3 | config | `config list/show/set/edit/unset/import/history`(9 種類の設定の検査も Go に) | 済み |
| 3 | proposal | `proposal add/list/show/resolve`(古い名前 `proposal --role ...` `proposals` `proposal-resolve` も) | 済み |
| 3 | notify | `notify status/on/off/test/dry-run/tail/ack/flush/events/hash/event/rules`(`pr-watch` は pr サービスとして移した) | 済み |
| 3 | approval | `approval check/allow/ask/approve/reject`(古い名前 `approval <操作>` `allow` `ask` `approve` `reject` も)。判定に使う役割(agent)・承認後の届け方(message)の部品も Go に | 済み |
| 3 | message | `message send/flush/outbox/inbox/read`(古い名前 `send` `flush` `outbox` `inbox` `inbox-read` も)。flush の前の台帳の同期(`agent_sync`)も `internal/agents` に | 済み(`report` / `pending` も報告コメントの配送と一緒に移した) |
| 3 | review / gate | `review pack/submit`(古い名前 `review-pack` `review-submit` も)と `gate evaluate/execute/ingest-review/ingest-events/reviewers`。設定と記録の置き場所は bash と同じく TB_ROOT の下に固定 | 済み |
| 3 | pr | `pr queue add/list/show/approve/reject/retry/run/scan` と `pr watch`(古い名前 `pr-queue <サブコマンド>` `pr-watch` も)。利用者が書く正規表現(`body_first_line`・公開前検査の規則と例外)は bash と同じ意味になるよう `grep -E` で評価する | 済み |
| 3 | alert / digest | | まだ(alert は解析役の起動が agent に依存するので agent の後) |
| 4 | reminder | `reminder list/check/publish/scan/nudge`(古い名前 `reminders [--check / --publish]` `due-scan` `nudge` も)と `deadlines-import` | 済み |
| 4 | agent(一部) | `limits show/get/set/unset/mode/check/watcher-enabled`(spawn_* が無いときの spawn-policy の値も)、読むだけの `can` / `org` / `peek`、役割の `role-context` / `role-broadcast` / `role-show` / `role-list` / `role-write` / `role-edit` / `role-unset` / `role-set` / `role-log` / `roles`、文脈の圧縮 `compact-check`、スラッシュコマンドの送信 `slash`(承認待ちの承認は `agent-approve` に残る) | 済み(残りの agent のコマンドは続きの PR) |
| 5 | conv | `conv route/routes-set/draft/voice/voice-check/length/kinds/patterns/lint`、学習の `conv pair-add/pairs/pair-show/candidate-add/ledger/candidate-adopt/candidate-reject/scan/learn-purge`、段階の `conv stage/stage-set/stage-reset/stage-log/incident/gate`(古い名前 `conv-<サブコマンド>` も)。文字列の一般化と検査(sanitize / scan / ratio)は bash と同じく `lib/convtext.py` を呼ぶ | 済み |
| 5 | recipe | | まだ(ADR 0005 段 3 の後) |
| 6 | scheduler の起動器 / webui | | まだ(ADR 0002 の手順 4・5) |

サービスのコマンドより先に、ほかのサービスが使う部品だけを移すことがある(例: approval の PR で `internal/agents` の役割の引き方と `internal/message` の届け方)。そのサービスのコマンドはまだ bash のままで、移す PR でこの部品を使う。

土台(`internal/kernel`)は、移すサービスが使う分だけ足していく: store(DB)、env(既定の置き場所)、config(設定を引く・書く)、herdr(herdr アダプタ)、identity(人か agent か。報告者の名前 `resolve_label` も)、transport(herdr への送信 `herdr_send` と messages への記録)、util(`oneline` など)。

`touch` は hook から毎回呼ばれるので、`TB_IMPL=go` でも herdrctl が無い・持たないときは何も出さずに bash で続ける(`bin/tb` の `tb_go_quiet`)。
