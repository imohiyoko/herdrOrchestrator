---
name: tb-command-naming
description: tb に新しいコマンド(サブコマンド)を足す・名前を決める・help.txt に書くときに使う。新しいコマンドは `tb <サービス名> <サブコマンド>` の形にする決まりと、サービス名の一覧、bash と Go(herdrctl)での実装の置き方を扱う。
---

# tb のコマンドの名前の決まり

新しく足す tb のコマンドは **`tb <サービス名> <サブコマンド> [引数...]`** の形にする。

```bash
tb ticket move 12 --to review        # チケットの状態を動かす
tb ticket status add qa review       # チケットの状態(設定 statuses)を足す
tb team show backend
```

この決まりは **これから足すコマンド** のためのもの。今あるコマンド(`tb add`、`tb list`、`tb agent-spawn`、`tb recipe-run` など)はまだこの形になっていないが、**名前を変えない**(下の「今あるコマンド」)。

## なぜこの形か

- ADR 0002 / 0004 で、部品をサービス(モジュール)の境界で分け、そのまま Go のパッケージの境界にすると決めた。コマンドの 1 語目をサービス名にすると、コマンドを見ただけで「どのサービスの口か」「どのパッケージに実装するか」が分かる
- 今の tb は 1 段の名前(`agent-spawn`、`conv-stage-reset` など)が 150 近くあり、`tb help` で探しにくい。1 語目でまとめれば `tb ticket` だけで、そのサービスのコマンドの一覧を出せる
- Go に移すとき(ADR 0002 の手順 3)、`herdrctl tb --commands` に出すのはサービス名 1 語だけで済み、サービス単位で移せる

## 形の決まり

1. **1 語目はサービス名**。下の一覧から選ぶ。英小文字の 1 語、単数形(`tickets` ではなく `ticket`)
2. **2 語目は動詞**(サブコマンド)。サービスの中にさらに対象があるなら、`tb <サービス名> <対象> <動詞>` と 3 語にしてよい(`tb ticket status add`)。4 語以上にはしない
3. 語はスペースで区切る。新しいコマンドでは `ticket-move` のようなハイフンでつないだ 1 語にしない。1 語の中でどうしても 2 語が要るときだけハイフンを使う(`tb pr queue-add` より `tb pr queue add` を先に考える)
4. 動詞は次からなるべく選び、サービスごとに意味を変えない

   | 動詞 | 使いどころ |
   | --- | --- |
   | `add` / `rm` | 作る / 消す |
   | `list` / `show` | 一覧 / 1 件の詳細(`--json` を付けられるようにする) |
   | `set` / `unset` | 設定の値を書く / 外す |
   | `update` | 既存のものの項目を変える |
   | `move` | 状態・置き場所を動かす |
   | `run` | 実行する(レシピ、ジョブなど) |

   これで言えないときは、そのサービスで自然な動詞を使ってよい(`tb ticket claim`、`tb agent spawn`)。agent を作るのは `add` ではなく `tb agent open`(止めるのは `stop`。人の決定 2026-11-23)。同じ意味の動詞を増やさない(`delete` と `rm`、`ls` と `list` を混ぜない)
5. 引数・オプションの書き方、終了コード(使い方の誤り = 2、対象なし = 1 など)は今の tb と同じにする
6. `tb <サービス名>` だけ、または `tb <サービス名> help` で、そのサービスのサブコマンドの一覧を出す

## サービス名の一覧

ADR 0002(サービスの境界)と ADR 0004(3 つの層とモジュール)から決めた名前。**今の実装と 1 対 1 ではない**。ここに無いサービスのコマンドを足すときは、先にこの表に 1 行足し、境界が変わるなら ADR も直す。

| サービス名 | 層(ADR 0004) | 持つもの | 今のおおよそのファイル / コマンド |
| --- | --- | --- | --- |
| `ticket` | チケットの通り道 | チケットの作成・変更・状態・リンク・コメント・履歴・スプリント・ボード・状態の設定 | tasks, comments, core(statuses)/ `add` `list` `show` `claim` `done` `link` `sprint-*` `board` `history` |
| `team` | チケットの通り道 | チーム(受付・指揮役・メンバー・チームごとの上書き) | まだ無い(ADR 0004 の案では `team-set` `team-show`) |
| `notify` | チケットの通り道 | 通知の規則、イベントからの通知 | notify, notifyrules / `notify` |
| `message` | 機能 | agent への連絡・保留・inbox・送信の履歴 | send, transport(の外向き), digest / `message send/flush/outbox/inbox/read`(古い名前 `send` `flush` `outbox` `inbox` `inbox-read`)。`report` `pending` はまだ古い名前だけ |
| `digest` | 機能 | まとめ送り | digest / `digest` `digests` |
| `agent` | 機能 | agent の作成・停止・復活・上限・モデル・役割 | agents, spawn, limits, models, roles, shed / `agent list/open/stop/revive/restart/flag/ops/approve/reject/kinds/shed/spawn/can/org/peek/slash`、`agent freeze/unfreeze`(子 agent の自動承認)、`agent model set`、`agent compact check`、`agent role show/list/write/edit/unset/set/log/broadcast/status`(古い名前 `agents` `agent-*` `spawn-*` `can` `org` `peek` `slash` `model-set` `compact-check` `role-*` `roles`)。`limits` はまだ古い形のまま |
| `alert` | 機能 | アラート | alerts / `alert` `alert-*` `alerts` |
| `review` | 機能 | 会計役のレビュー材料・レビューの提出 | review, gate / `review pack/submit`(古い名前 `review-pack` `review-submit`) |
| `proposal` | 機能 | 会計役 → 設定役の改善提案(project proposal のチケット) | review / `proposal add/list/show/resolve`(古い名前 `proposal --role ...` `proposals` `proposal-resolve`) |
| `pr` | 機能 | PR キュー・PR の見張り | prqueue / `pr queue add/list/show/approve/reject/retry/run/scan` `pr watch`(古い名前 `pr-queue` `pr-watch`) |
| `gate` | 機能 | ゲート | gate / `gate` |
| `approval` | 土台(authz) | 承認・人だけの判定 | approvals, risk / `approval check/allow/ask/approve/reject`(古い名前 `approval <操作>` `allow` `ask` `approve` `reject`) |
| `recipe` | 機能 | レシピと部品(ADR 0005) | recipes / `recipe set/rm/list/show/history/run/runner/queue/cancel`、`recipe part list/check/risk`(古い名前 `recipe-*` `parts` `part-*`) |
| `conv` | 機能 | 会話の下書き・振り分け・学習 | conv, convlearn, convstage / `conv route/draft/voice/…` `conv pair-add/ledger/candidate-adopt/…` `conv stage/stage-set/gate/…`(古い名前 `conv-<サブコマンド>`) |
| `reminder` | 機能 | リマインダー・期限 | reminders / `reminder list/check/publish/scan/nudge`(古い名前 `reminders` `due-scan` `nudge`)。`deadlines-import` はまだ古い名前だけ |
| `job` | 機能 | scheduler のジョブ | schedjobs, schedwatch / `sched-job` |
| `proxy` | 機能 | 代理の依頼 | proxy / `proxy-*` |
| `activity` | 機能 | hook からの活動記録 | activity / `touch` `activity`(hook が呼ぶので名前は変えない) |
| `handoff` | 機能 | 人が貼る調査依頼の文面 | handoff / `handoff write`(古い形 `handoff <id>`) |
| `config` | 土台(store) | 設定の読み書き | config / `config` |
| `policy` | 土台(authz) | 認可の設定 policy の移し替え(古い approvals / grants から) | approvals / `policy import` |

迷ったら、そのコマンドが **どの表(または設定)を書くか** で決める。書く表を持つサービスがコマンドの持ち主になる(ADR 0002 の「直接 SQL を書くのは store だけ」「他のサービスの表を直接読まない」と同じ向き)。

## 実装の置き方

- **Go(herdrctl)**: tb の中身は herdrctl だけ(`taskboard/bin/tb` は `herdrctl tb` に置き換わる。bash の実装は消した)。`herdrctl tb ticket move ...` を受ける。`herdrctl tb --commands` にはサービス名(`ticket`)を出す。サービスのコマンドは、そのサービスのパッケージ(`internal/tickets` など、ADR 0004)に置く。動詞の表は `internal/tbcli/services.go`
- **認可の表**(ADR 0006 / 0007): 新しいコマンド・サブコマンドは、`taskboard/perms/commands.tbl` に行を足す(道 `<サービス名>.<動詞>`、操作名 `cmd:<サービス名>:<動詞>`、agent への既定 `allow` / `ask` / `deny`、下限 `allow`、確かめる場所)。古い名前を別名として残すなら、同じ操作名の行を足す。コマンドの中で `requireHuman("tb <語...>")` を使うなら、その語の道の行を `inner` にする。足し忘れると `03_perms.sh` と `internal/kernel/perms` のテストが落ちる
- **help.txt**: 新しいコマンドは `tb <サービス名> ...` の行として、そのサービスの近くに書く
- **テスト**: `testing` スキルに従う。ケースの名前は `NN_<機能>.sh` のまま(サービス名を入れてもよい)

## 今あるコマンド

- 今ある 1 段の名前(`tb add`、`tb agent-spawn` など)は、Go に移すときにサービスごとに新しい形へ直す(人の決定 2026-11-22。進み具合は `docs/go-migration.md`)。古い名前は別名として同じに動かし、help.txt の新しい名前の下に「  別名(...): tb <古い名前> ...」と書く。古い名前は bash を消す PR でまとめて外す
- Go に移す PR 以外で、今あるコマンドの名前だけを変える PR は出さない
- 今あるコマンドの隣に新しいコマンドを足すときも、新しい方はこの形にする(例: `tb agent-spawn` があっても、新しく足すのは `tb agent pool add` のようにする)。古い名前と新しい名前が並ぶのはしばらく許す
- 古い名前に、新しい形の別名(`tb ticket show` → `cmd_show`)を足すのはよい。足すなら古い名前も動いたまま残し、両方をテストで確かめる

## やってはいけないこと

- 新しいコマンドを 1 語目に動詞を置いて足す(`tb move-ticket`、`tb add-status`)
- 一覧に無いサービス名を、表に足さずに使う
- 同じ意味の動詞をサービスごとに変える
- Go に移すのと別に、既存コマンドの名前だけを変える
