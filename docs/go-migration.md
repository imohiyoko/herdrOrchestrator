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
| – | ticket(一部) | `list` | 済み |
| 1 | activity | `touch` `activity` | 済み |
| 2 | proxy | `proxy request/list/resolve/check`(古い名前 `proxy-*` も) | 済み |
| 2 | job | `job list/show/set/edit/unset/import/history`(古い名前 `sched-job` も) | 済み |
| 2 | handoff | `handoff write`(古い形 `handoff <id>` も) | 済み |
| 2 | whoami | `whoami` | まだ(`bin/tb` の入口を作り直すときに一緒に) |
| 3 | config | `config list/show/set/edit/unset/import/history`(9 種類の設定の検査も Go に) | 済み |
| 3 | approval / alert / review / gate / pr / digest / notify | | まだ |
| 4 | agent / reminder / message | | まだ(message は `send` / `nudge` の PR の後) |
| 5 | ticket(残り) / recipe / conv | | まだ(ticket は状態の再設計の後、recipe は ADR 0005 段 3 の後) |
| 6 | scheduler の起動器 / webui | | まだ(ADR 0002 の手順 4・5) |

土台(`internal/kernel`)は、移すサービスが使う分だけ足していく: store(DB)、env(既定の置き場所)、config(設定を引く・書く)、herdr(herdr アダプタ)、identity(人か agent か)。

`touch` は hook から毎回呼ばれるので、`TB_IMPL=go` でも herdrctl が無い・持たないときは何も出さずに bash で続ける(`bin/tb` の `tb_go_quiet`)。
