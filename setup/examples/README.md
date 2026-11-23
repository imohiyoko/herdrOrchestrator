# setup/examples — 役割を使う構成の利用例

tb は役職(指揮官・人事・見張りなど)を前提にしない。既定で agent に渡るのは `roles/_common.md` だけで、
誰が何をするかは agent の名前・プロンプト・権限(設定 `policy`。届け方は `delivery`)・返す文面(messages)で決める。
ここにあるのは、役割を分けて動かす場合の一例。そのまま使っても、書き換えても、使わなくてもよい。

| 場所 | 中身 |
|---|---|
| `roles/<役割>.md` | 役割の文面(conductor 指揮官 / hr 人事 / worker / watcher 見張り / analyst 解析 / sweeper 判定 / auditor 会計 / drafter 下書き) |
| `roles/map.conf` | agent 名のパターン → 役割(例 `worker* = worker`) |
| `org.conf` | 組織図(誰の上司は誰か)の例。`tb config set org` で入れる。全員が `tb org` で見られる |
| `org-rules.conf` | 組織図の関係(`~report` / `~manager` など)を使う policy と delivery の行の例 |
| `statuses.conf` | チケットの状態の例(確認を 2 段にし、名前を日本語にする)。`tb config set statuses --shared` で入れる。書式は `taskboard/config/statuses.conf.example` |
| `presets/roles.conf` | 上の役割で workspace を並べるプリセット(`presets/<名前>.conf` にコピーして使う) |

入れ方(DB に書く。書く前に人の承認を取る)と、役割に合わせて変える設定(projects / org / policy / spawn-policy / role-models / messages)は
`setup/CLAUDE.md` の「2b. 役割の利用例」にある。
