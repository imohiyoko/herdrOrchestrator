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

## 段 2: レシピの登録と汎用の入口

レシピは、名前と部品の並び。DB(`recipes` 表。schema 025)に登録し、変更の履歴は `recipe_changes` に残る。
**登録・変更・削除は人だけ**(agent の pane からは拒否)。

```bash
tb recipe-set <名前> [--file F]     # 登録・置き換え(人だけ。本文は省くと標準入力)
tb recipe-rm <名前>                 # 削除(人だけ。履歴は残る)
tb recipe-list [--json]             # 一覧(リスク階層は今のコードで計算し直す)
tb recipe-show <名前>               # 判定の操作名・リスク階層・step ごとの部品の性質
tb recipe-history [<名前>] [--json] # 登録の変更
tb recipe-run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]   # 汎用の入口
```

### 本文の書き方

```
# 架空の例: 自分を安全に再起動する
desc 自分を安全に再起動する
risk low                                   # 宣言(参考)。中の部品の最大より低ければ最大になる
arg note? text:500                         # 実行のときに note=<値> で渡す(? は省略可)
step agent-status  target=$requester
step wait-idle     target=$requester timeout=60
step agent-restart target=$requester prompt=$note
```

- 書けるのは `desc` / `risk` / `arg` / `step` だけ。条件分岐・ループ・シェルは書けない
- 値はリテラル(空白と `$` を含まない 1 語)か、`$requester`(依頼者)/ `$target`(対象)/ `$<宣言した引数>` の 1 語
- `$requester` / `$target` は `agent` 型の引数にだけ。宣言した引数の型は、渡す先の部品の引数と同じ種類にする(`text` なら `text`)
- 名前は英小文字で始まる英小文字・数字・`-` の 32 字以内。**`:` は使えない**(操作名を最後の `:` で分けるため)
- step は 16 個まで、arg は 8 個まで。登録のときに全部を検査し、1 つでも通らなければ登録しない(終了コード 2。理由を行ごとに出す)

### 判定

`tb recipe-run` は、次の順に判定する。**全部の step を判定してから実行する**ので、途中の step で止まるときは、前の読み取りの部品も実行しない。

1. **登録されていないレシピ名は実行しない**(終了コード 4。ask ではない)。登録の後でコードの部品が変わって検査を通らなくなったレシピも実行しない
2. **レシピの操作名** `recipe:<名前>:<対象>` を、既存の `approvals` / `grants` で判定する。人の端末は承認なし。ask なら 20(段 2 では承認待ちに積まない。人が自分の端末で実行するか、人が allow にする)
   - 既定の対象は依頼者自身。他者を対象にするときは `--target <agent 名>`。人の端末からは `--target` が要る
   - `approvals` は操作名の全体を照合する: `recipe:restart-self:* = allow`
   - `grants` は操作(対象を除く)を書く: `lead recipe:restart-self ~report`
3. **部品ごと**: 引数の型、人だけの部品(agent からは 4)、部品の判定の操作名(`agent:stop:<対象>` など。ask なら 20)。**レシピの許可は部品の許可の代わりにならない**
4. 段 2 で実行するのは、副作用 `none` で実行役 `inside` の部品だけ(`agent-status` / `wait-idle` / `task-show`)。ほかの部品は判定まで行い、実行しない(4。段 3 で外の実行役が行う)

実行の途中で部品が失敗したら、残りは続行しない(終了コード 1。済んだ step は巻き戻さない。人が見る)。`--dry-run` は判定だけを出し、何も記録しない。

### 記録

チケットの履歴(`task_events`。ADR 0003)に残す。`tb events --kind recipe_run` などで読める。`--task T-..` を付けると、そのチケットの `tb history` にも出る。

| kind | old | new | ref |
|---|---|---|---|
| `recipe_run` | - | `<名前> target=<対象>` | 判定の操作名 |
| `recipe_step` | `<番号> <部品>`(0 はレシピの操作名の判定) | 判定と根拠 / `ok` `failed` `未実行` と出力の 1 行 | `E-<recipe_run の番号>` |
| `recipe_end` | - | `done` か、止めた理由 | `E-<recipe_run の番号>` |

actor は依頼者(agent 名か `human`)。

### 守り(段 2 でテストしていること)

`taskboard/test/cases/84_recipes.sh` が確かめる。

- 登録・削除は agent の pane から断る。断ったら何も残らない
- 宣言 `risk` で下げられない。人だけの部品を含むレシピは `human-only`
- 未登録・検査を通らないレシピは、人の端末からでも実行しない
- レシピが allow でも、部品の判定が ask なら止まる。`approvals` と `grants` を全部許可しても、人だけの部品は agent から止まる
- `--target` と引数の値はシェルに渡らない(`$(...)` を含む値は型で落ち、何も実行されない)
- 途中で失敗したら残りの step を実行せず、どこで止まったかを記録する

## これから(段 3・段 4)

- 段 3: pane の外の短命の実行役(既存の定期ジョブの仕組み)と、副作用のある部品の実行。定期ジョブに持ち主(依頼者)を持たせる(それまでは、定期ジョブからのレシピの実行を許さない)
- 段 4: 最初のレシピ(agent の安全な再起動)を設定で作る
