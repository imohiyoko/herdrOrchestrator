# 部品とレシピ(ADR 0005)

設計は [ADR 0005](../../docs/adr/0005-minimal-parts-recipes-and-registered-grants.md)。新しい使い道を、専用のコマンドを足す代わりに「部品の並び(レシピ)」で作れるようにする。
この文書は、実装した段の使い方と守りをまとめる。

## 段 1: 部品(コードに固定)

部品は、1 つの副作用だけを持つ小さな操作。一覧・引数の型・リスク階層は `lib/recipes.sh` の表(`PARTS_TABLE`)に固定している。
**設定・DB・環境変数では増やせず、緩められない**(部品を足すのはコードの変更で、PR になる)。

```bash
tb parts [--json]                          # 部品の一覧
tb part-check <部品> [<名前>=<値> ...]      # 引数の型を確かめ、リスク階層・人だけ・実行役・判定の操作名を出す(実行しない)
tb part-risk <部品> ...                     # 部品の並びのリスク階層(中の最大)
```

| 列 | 意味 |
|---|---|
| 副作用 | `none` / `local-write` / `external-send` / `external-write`(権限カタログと同じ語) |
| リスク階層 | `low` / `mid` / `high` / `human-only`。**人だけ = `human-only`**(別の欄を持たないので、食い違わない) |
| 実行役 | `inside`(依頼者の pane の中の tb が実行してよい)/ `outside`(pane の外の実行役が行う。段 3) |
| 判定の操作名 | 承認判定(`approvals` / `grants`)に渡す名前。`$<引数名>` は、型の検査を通った値に置き換わる(例: `agent:stop:$target` → `agent:stop:w1`) |
| 引数 | `<名前>:<型>`。`<名前>?:<型>` は省略できる |

引数の型:

| 型 | 受ける値 |
|---|---|
| `agent` | agent 名(英小文字で始まる 32 字以内。`human` は受けない) |
| `task` | `T-<project>-NNN` |
| `comment` | `C-<数字>` |
| `prq` | `PQ-<数字>` |
| `config-key` | 設定のキー(`tb config list` に出るもの) |
| `int:<最小>-<最大>` | その範囲の整数(境界を含む) |
| `enum:<値>/<値>/...` | 列挙した値のどれか |
| `text:<最大字数>` | 最大字数まで(多バイトも 1 字)。制御文字は改行とタブだけ |

リスク階層の計算は **最大**: 並びの中に 1 つでも高い部品があれば、並び全体がその階層になる。一覧にない部品が混ざった並びは、
計算の上では `human-only` とみなし(低く数えない)、`tb part-risk` は終了コード 1 で拒否する。

### 守り(段 1 でテストしていること)

`taskboard/test/cases/83_parts.sh` が確かめる。

- 人だけの部品(`agent-force-stop` / `comment-resolve` / `pr-queue-approve` / `config-set`)は、`approvals` を全部 `allow` にしても、環境変数で表を差し替えようとしても、人だけのまま
- 部品の表は `lib/recipes.sh` の中だけにあり、設定のキーにも無い(DB で増やせない)
- 部品の判定の操作名は、権限カタログ(`config/approvals-catalog.conf`)の `active` な項目に当たり、部品のリスク階層はカタログの階層より低くない
- 型に合わない値(範囲の外の整数、上限を超える文字列、制御文字、`human`、記号を含む agent 名など)は受け付けない

### 部品を足すとき

1. `lib/recipes.sh` の `PARTS_TABLE` に 1 行足す。副作用は 1 つだけにする
2. 判定の操作名があれば、権限カタログに載っている `active` な操作名にする。リスク階層はカタログの階層以上にする
3. 人の端末からだけ実行できるコマンドに当たる部品は `human-only` にする
4. `bash taskboard/test/run.sh 83_parts.sh` を流す
