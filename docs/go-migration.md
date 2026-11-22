# tb の Go への移行(進み具合)

[ADR 0002](adr/0002-services-and-binary-distribution.md) の手順 3(コマンドを 1 つずつ移す)の進み具合と、残りの順番。サービスの名前は `.claude/skills/tb-command-naming` の一覧に合わせる。

## 決まり

- **1 PR で 1 サービス**。他の PR が bash 側を書き換えている最中のサービスは後に回す
- **移し終えた** = そのサービスのコマンドがすべて `herdrctl tb --commands` に出ていて、`taskboard/test/cases` のそのサービスのケースが **書き換えなしで** `TB_IMPL=go` でも通る。出力・終了コード・エラーの文面は bash と同じ。Go のユニットテストも付ける
- **同期**: CI の `tb contract` ジョブが全ケースを `TB_IMPL=go` で流す。移したコマンドを bash 側だけ変えるとこのジョブが落ちる。ケースで確かめていない振る舞いは、移す PR でケースを足して埋める
- **bash を消す時期**: tb の全サービスが移ったら、まず既定を `TB_IMPL=go` にする PR を出す。setup が herdrctl を必ず入れるようになり、しばらく様子を見てから `taskboard/lib/*.sh` を消し、`tb` を herdrctl を呼ぶだけの shell にする

## 進み具合

| 順 | サービス | コマンド | 状態 |
| --- | --- | --- | --- |
| – | ticket(一部) | `list` | 済み |
| 1 | activity | `touch` `activity` | 済み |
| 2 | proxy / whoami / handoff / job | `proxy-*` `whoami` `handoff` `sched-job` | まだ |
| 3 | config / approval / alert / review / gate / pr / digest / notify | | まだ |
| 4 | agent / reminder / message | | まだ(message は `send` / `nudge` の PR の後) |
| 5 | ticket(残り) / recipe / conv | | まだ(ticket は状態の再設計の後、recipe は ADR 0005 段 3 の後) |
| 6 | scheduler の起動器 / webui | | まだ(ADR 0002 の手順 4・5) |

`touch` は hook から毎回呼ばれるので、`TB_IMPL=go` でも herdrctl が無い・持たないときは何も出さずに bash で続ける(`bin/tb` の `tb_go_quiet`)。
