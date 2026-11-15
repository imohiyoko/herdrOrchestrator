# merge / approve の判定の口(段 0)

`tb gate` は、PR の merge や approve を人以外が行ってよいかを、決まった規則だけで判定するコマンドです。
**段 0 は口だけを作る段階です。判定は参考値を出すだけで、何も実行せず、何も記録しません。**
merge も approve も、これまでどおり人が行います。

## コマンド

```
tb gate evaluate <chain-id> --action merge|approve [--pr <owner>/<repo>#<n>] [--fact '行']... [--facts-stdin]
tb gate execute  <chain-id> --action merge|approve
```

### evaluate(判定。読み取りだけ)

- LLM もネットワークも使いません。同じ入力と同じ設定からは、必ず同じ出力が出ます。
- 結果は `allow` か `ask` の 2 つです。`ask` は「人に回す」です。
- 入力は、記録済みの事実を定型の行で渡します(下の「入力の形」)。呼び出し側が点数や結論を渡す形はありません。
- 終了コード: `0` = allow / `20` = ask / `21` = 評価不能(ask と同じ扱い。理由に不足を列挙) / `2` = 引数の誤り。

出力の例(値は架空):

```
gate: ask
action: merge
chain: T-0000   pr: example-owner/example-repo#123   head: 3f9c2ab
mode: disabled   # 段 0 は参考値だけ。実行は常に無効
score: 3 / threshold(merge) 1
hard: none
reason: S-01 +1  変更の大きさ: 中
reason: P-01 +1  触るパス: 一般のコード
reason: K-01 +1  PR の種類: code
...
```

**段 1 より前は、`tb gate evaluate` の終了コードや出力を、何かの許可(実行の条件)として使わないでください。** 入力は呼び出し側が渡す値で、真正性を確かめていません。

`hard:` に行があるときは、点数に関係なく `ask` です(`score:` は参考)。寄与は 1 件 1 行で全部出します。

### execute(実行の口。段 0 では常に無効)

引数が正しければ、gate の設定・入力・環境変数に関係なく**終了コード 30** で終わります。gate の設定や入力は読まず、何も記録せず、何も実行しません。
ただし `tb` 共通の起動処理(必要なコマンドの確認と DB の初期化)は、`gate` の前に走ります。DB を開けないなど起動に失敗したときは、30 ではなく終了コード 1 になります(`evaluate` も同じで、21 でなく 1 になりえます)。
呼び出し側は「0 以外は許可ではない」として扱ってください。
引数の誤りは `2` です。実行の口を有効にするには、後の段で別の変更が要ります。

## 判定の規則

allow になるのは、**hard に 1 件も当たらない** かつ **点の合計がしきい値以下** の両方を満たすときだけです。
点数は、hard に当たらない範囲の細かい差にだけ効きます(点を足して許可に近づく形にはしません)。

### hard(常に人に回す)

| ID | 内容 |
|---|---|
| H-01 | 変更したパスがガバナンスのパス規則に当たる(改名は旧パスと新パスの両方を見る)。組み込みの対象は下の「組み込みの hard のパス」 |
| H-02 | 公開前検査が clean でない(検査を実行できなかった場合も) |
| H-03 | 有効なレビューに blocker または fail がある / head に対する有効な記録が無い / 独立した 2 系統の pass がそろっていない / テストが失敗 / テストの sha が head と違う |
| H-04 | 作者と実行の主体が同じ |
| H-05 | 許可リストに無いリポジトリ(リストが空のときも) |
| H-06 | ファイルの種別・モード(シンボリックリンクなど)の変更 |

### 点数(点が高いほど危険)

| ID | 入力 | 点 |
|---|---|---|
| S-01 | 変更の大きさ(変更行数 ÷ 全行数) | 10% 未満 0 / 30% 未満 +1 / 60% 未満 +2 / それ以上 +3 |
| P-01 | 触るパス | docs・tests のみ 0 / 一般のコード +1 / lib・config・scheduler 配下 +2 |
| K-01 | PR の種類(パスから決定論で分類) | docs のみ 0 / tests のみ 0 / code +1 / mixed +2 |
| T-01 | テスト | 通過(sha 一致)0 / 未実行 +2 |
| R-01 | レビュー(記録から読む) | 独立 2 系統以上で should なし 0 / should が残る +1(2 系統に満たないものは H-03 で hard) |
| A-01 | 作者の過去の差し戻し率 | low 0 / mid +1 / high +2 / unknown +1 |

しきい値は設定で決めます(`approve` と `merge` を別に持てます)。ただしコード内の上限(approve 2、merge 1)より緩い値は、警告を出して上限のままにします。厳しくする方向の値だけが効きます。

## 入力の形

1 行 1 事実。`--fact '行'` を繰り返すか、`--facts-stdin` で標準入力から渡します。空行と `#` で始まる行は無視します。

```
HEAD <sha>                                        現在の head の sha(7〜40 桁の小文字 16 進)
PR <owner>/<repo>#<n>                             対象の PR。--pr を付けた場合は一致が必要
TEST exit=<n> sha=<sha>                           テスト結果。未実行なら TEST none
PUBLIC-CHECK clean|dirty|unavailable              公開前検査の結果
FILE <A|M|D|T> <path>                             変更ファイル(T は種別・モードの変更)
FILE R <旧path> <新path>                          改名
SIZE <変更行数> <全行数>                            全行数は 1 以上
AUTHOR <id>                                       PR の作者
EXECUTOR <id>                                     実行する主体
AUTHOR-REJECT-RATE low|mid|high|unknown           作者の過去の差し戻し率
```

- `REVIEW` は入力では受け付けません(あると評価不能)。レビュー結果は、下の記録から読みます。
- 欠けている行、形の不正、重複、未知の行は、すべて**評価不能(21)**です。不足は理由に全部列挙します。
- パスは相対パスで、`..` を含められません。

段 0 は記録の置き場が無いため、これらの値は呼び出し側が渡します。**渡された値の真正性は、この段では確かめません。**
実際に自動化へ進む段では、記録済みの事実だけを入力にする作りへ変える前提です。

## 組み込みの hard のパス

基準は「**ガバナンス・権限・コードの取得や実行の入口**」です。判定を弱める変更と、別の経路でコードを取得・実行させる変更は、点数だけでは通しません。どの階層のパスにも効き(`**/`)、設定ファイルで外せません。

| 分類 | パス |
|---|---|
| 判定・承認の設定と実装、`tb` の入口、テスト基盤 | `.github`、`gate*.conf`、`gate-paths.conf`、`gate*.sh`、`approvals.conf`、`risk-rules.conf`、`bin/tb`、`run-all.sh`、`test/run.sh`、`tests/run.sh`、gate のテスト |
| レビュー必須の規則・レビューの設定 | `CODEOWNERS`(ルート・`docs/`・`.github/` のどこでも)、`.coderabbit.yaml` |
| 実行の入口(git・起動・CI) | `.githooks`、`.husky`、`.gitattributes`、`.gitmodules`、`.pre-commit-config.yaml`、`.envrc`、`.mcp.json`、`.devcontainer/`、`.circleci/`、`Jenkinsfile` |
| ビルド・実行の入口 | `Makefile`、`GNUmakefile`、`Dockerfile*`、`*.dockerfile`、`docker-compose*.yml`(`docs/` 配下でも) |
| 依存定義と lockfile、取得先の設定 | `package.json`、`package-lock.json`、`yarn.lock`、`pnpm-lock.yaml`、`bun.lockb`、`.npmrc`、`.yarnrc*`、`requirements*.txt`、`Pipfile`、`Pipfile.lock`、`poetry.lock`、`uv.lock`、`pyproject.toml`、`setup.py`、`setup.cfg`、`pip.conf`、`pip.ini`、`.pip/`、`.pypirc`、`go.mod`、`go.sum`、`go.work`、`go.work.sum`、`Cargo.toml`、`Cargo.lock`、`Gemfile`、`Gemfile.lock`、`composer.json`、`composer.lock`、`pom.xml`、`build.gradle*` |

人が足したいパスは `config/gate-paths.conf` に書きます(組み込みは外せません)。
glob は広めに当たります(`*` が `/` も越える)ので、`Dockerfile` で始まる名前(`docs/dockerfile-notes.md` など)も hard になります。人に回る側へ倒す作りです。

## レビュー結果の記録(`tb review-submit`)

```
tb review-submit <task-id> --lineage <系統> --sha <head の sha> --blocker <n> --should <n> --verdict pass|fail [--pr <o>/<r>#<n>] [--evidence <パス>]
```

- 記録先は固定の追記専用ファイル `data/gate-reviews.log`(`.gitignore` 済み。env では変えられません)。DB のスキーマは変えません。既存の行は書き換えません。
- 1 行の形: `ts=<UTC> task=<id> by=<送信者> pr=<o/r#n|-> REVIEW <系統> <sha> blocker=<n> should=<n> verdict=<pass|fail> evidence=<パス|->`
- **送信者は pane から tb が決めます**(自己申告の引数はありません)。pane が無い端末は `by=human` です。
- chain-id はタスク ID です(`tb gate evaluate` の chain-id と同じ)。タスクが存在することが条件です。
- 終了コード: `0` 記録した / `2` 使い方の誤り / `3` 送信者を特定できない / `4` 作者(タスクの owner)と同一、または作者を確かめられない / `5` 役割・系統が許されない / `1` その他。
- 職務分掌の最小の検査: agent の pane から記録できるのは、役割が `reviewer` のものだけです(`roles/` の固定の置き場と台帳で決まり、env では変えられません)。作者と同一の agent は拒否します。外部系統の `coderabbit` は agent が名乗れません(非 agent の経路から記録します)。
- `verdict=pass` で `blocker` が 1 以上の記録は作れません。

### 評価での読み方

- 記録の sha が評価対象の head と違うものは**無効**で、数えません(古いレビューは無効)。記録の `pr` が対象と違うものも無効です。
- 有効な記録の系統ごとに、同じ sha で fail または blocker が 1 件でもあれば、その系統は失敗(H-03)です。
- 独立した **2 系統の pass**(系統が違い、送信者も違う)がそろい、blocker が 0 のときだけ、レビューの条件を満たします。
- 記録ファイルが無い・読めない(symlink を含む)・形式違反の行が 1 行でもある場合は、評価不能(21)です。
- 出力には、どの系統が何を記録したかを `reason: REC ...` で全部出します(無効な記録は理由つき)。

### 前提と限界

- `reviewer` の役割ファイル(`roles/reviewer.md` と `map.conf` の対応)は、人が用意します。無い間、agent の pane からは記録できません(人の端末からは記録できます)。
- 人の端末と、非 agent の決定論の経路は、どちらも pane が無いので区別できません(どちらも `by=human`)。外部系統の取り込みは、次の一歩で別の送信者名を持たせます。
- タスクの owner は DB から読みます。DB の場所は `tb` 共通の設定で、この PR では変えません。
- 判定を呼ぶ側が記録の置き場を書き換えられないことは、OS の権限による分離(ファイルの所有者)が本筋で、段 1 の次で扱います。

## 設定

次の 2 つを人が書きます。置き場は固定で、環境変数で差し替えることはできません(テストの内部だけ例外)。`tb` に編集コマンドはありません。
実ファイルは `.gitignore` 済みで、`.example` をコピーして使います。

| ファイル | 内容 |
|---|---|
| `config/gate.conf` | `[mode]` `[repos]` `[pr-kinds]` `[threshold]` `[limits]`。例は `config/gate.conf.example` |
| `config/gate-paths.conf` | 常に人に回すパスの glob。例は `config/gate-paths.conf.example` |

- どちらかが無い・読めない・構文エラー・未知のキー・範囲外の値は、**評価不能(21)**です(fail-closed)。設定で緩めることはできません。
- パスの glob は、`*` が `/` も越えて一致し、大文字小文字を区別しません(広めに当たる = 人に回る側へ倒す)。`**/` で始めると根直下にも当たります。
- 判定・承認の設定そのもの(`.github`、gate の設定とその実装、承認とリスク評価の設定)と、`tb` の入口(`bin/tb`)、テスト基盤(`run-all.sh`、`test/run.sh`、`tests/run.sh`)、gate のテストは、パス規則ファイルに書かなくても常に hard です。判定を弱める変更を点数だけで通さないためです。
- 点数の分類(P-01)も、hard と同じく大文字小文字を区別しません(`Lib/db.sh` は `lib/db.sh` と同じ重さです)。
- `docs/` 配下で docs として数えるのは、拡張子が `.md` `.txt` `.rst` `.png` `.svg` `.jpg` `.jpeg` `.gif` のものだけです(大文字小文字は区別しません)。それ以外(拡張子なし、`.js` `.yml` `.sh` など)は一般のコードとして数えます。
- パスは入力の正規形に頼りません。空・`.`・`..` の階層、先頭の `/`、末尾の `/` を含むパスは不正で、評価不能(21)です(`./x`、`a//b`、`a/../b` など)。
- シンボリックリンクは文字列からは分かりません。新規のシンボリックリンクは、記録する側が `FILE T` で渡す前提です。
- 例の `gate-paths.conf.example` は一般的な出発点です。認証の設定ファイルや公開前検査の規則など、環境固有のパスは人が足してください。
- `[mode]` `[pr-kinds]` `[limits]` は、段 0 では検査するだけで、判定の結果には使いません。

## 段 1 への引き継ぎ(段 0 では決めていないこと)

- `[pr-kinds]` を判定に使うか(段 0 は検査するだけ)
- 起動や環境構築のスクリプトのディレクトリ(例: `setup/**`)を常に hard に入れるか(人が決める)
- 評価を実行する場所の固定(別の作業ツリーの `tb` を呼ぶと、そこの設定を読む)
- レビュー結果は記録から読む形にした(段 1a)。テスト結果・公開前検査・変更ファイルなども、呼び出し側が渡す値から記録済みの事実へ変える
- 外部系統(CodeRabbit など)の取り込み、判定の記録、影武者の集計

## 段 0 でやらないこと

- merge / approve の実行(`tb gate execute` は常に 30)
- 判定の記録、DB の変更(レビュー結果の記録は、DB ではなく追記専用のファイルです)
- 緊急停止、回数の上限、実行用の認証

これらは段 1 以降の別の変更で扱います。
