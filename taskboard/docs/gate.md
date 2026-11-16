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

基準は「**ガバナンス・権限・コードの取得や実行の入口**」です(agent の振る舞いを変える指示・権限の設定を含みます)。判定を弱める変更と、別の経路でコードを取得・実行させる変更は、点数だけでは通しません。どの階層のパスにも効き(`**/`)、設定ファイルで外せません。

| 分類 | パス |
|---|---|
| 判定・承認の設定と実装、`tb` の入口、テスト基盤 | `.github`(どの階層でも)、`gate*.conf`、`gate-paths.conf`、`gate*.sh`、`approvals.conf`、`risk-rules.conf`、`bin/tb`、`run-all.sh`、`test/run.sh`、`tests/run.sh`、gate のテスト |
| agent への指示・権限・拡張の設定(どの階層でも) | `.claude.json`、`.agents/`、`AGENT.md`、`.aider.conf.*`、`.claude/`(hooks、commands、agents、skills、settings)、`CLAUDE.md`、`CLAUDE.local.md`、`AGENTS.md`、`GEMINI.md`、`copilot-instructions.md`、`.cursor/`、`.cursorrules`、`.windsurf/`、`.windsurfrules`、`.clinerules*`、`.roo/`、`.continue/`、`.codex/`、`.gemini/`、`mcp.json`、`.mcp.json`、`.vscode/tasks.json`、`.vscode/settings.json` |
| レビュー必須の規則・レビューの設定 | `CODEOWNERS`(ルート・`docs/`・`.github/` のどこでも)、`.coderabbit.yaml` |
| 実行の入口(git・起動・CI) | `.githooks`、`.husky`、`.gitattributes`、`.gitmodules`、`.pre-commit-config.yaml`、`.envrc`、`.mcp.json`、`.devcontainer/`、`.circleci/`、`Jenkinsfile`、`.gitlab-ci.yml`、`.gitlab/`、`.travis.yml`、`azure-pipelines*.y*ml`、`.azure-pipelines/`、`bitbucket-pipelines.yml`、`cloudbuild.y*ml`、`appveyor.yml`、`.drone.yml`、`buildspec*.y*ml`、`lefthook*.y*ml`、`.pre-commit-hooks.yaml`、`renovate.json`(`.json5`)、`.renovaterc*`、`.dependabot/` |
| ビルド・実行の入口 | `Makefile`、`GNUmakefile`、`Dockerfile*`、`*.dockerfile`、`docker-compose*.yml`(`docs/` 配下でも)、`justfile`、`Taskfile.y*ml`、`Procfile`、`tox.ini`、`noxfile.py`、`conftest.py`、`.bazelrc*`、`MODULE.bazel`、`WORKSPACE*`、`.tool-versions`、`mise.toml` と別名(`.mise.toml`、`mise.*.toml`、`.mise/`、`.config/mise/`) |
| 依存定義と lockfile、取得先の設定 | `package.json`、`package-lock.json`、`yarn.lock`、`pnpm-lock.yaml`、`bun.lockb`、`.npmrc`、`.yarnrc*`、`requirements*.txt`、`Pipfile`、`Pipfile.lock`、`poetry.lock`、`uv.lock`、`pyproject.toml`、`setup.py`、`setup.cfg`、`pip.conf`、`pip.ini`、`.pip/`、`.pypirc`、`go.mod`、`go.sum`、`go.work`、`go.work.sum`、`Cargo.toml`、`Cargo.lock`、`Gemfile`、`Gemfile.lock`、`composer.json`、`composer.lock`、`pom.xml`、`build.gradle*`、`Podfile*`、`Package.swift`、`Package.resolved`、`mix.exs`、`mix.lock`、`pubspec.*`、`build.sbt`、`deno.json(c)`、`deno.lock`、`environment.yml`、`flake.nix`、`flake.lock`、`Brewfile*`、`.terraform.lock.hcl`、`.cargo/config*`、`.bundle/config`、`.gemrc`、`bunfig.toml`、`.yarn/`、`pnpm-workspace.yaml`、`gradlew`、`mvnw`、`.mvn/`、`gradle.properties`、`settings.gradle*` |

**見送ったもの**(基準に当たらない。人が足したければ `config/gate-paths.conf` に書けます): `.nvmrc`、`.python-version`、`.ruby-version`(ランタイムのバージョンを 1 つ固定するだけで、スクリプトや取得先の定義を持たない)、`CMakeLists.txt`、Bazel の `BUILD*`(通常のコードで、機能の変更のたびに触る)、`tsconfig.json`・`.editorconfig`・`.prettierrc` などの整形・型の設定、名前が近い説明の文書(`docs/tox-notes.md` など)。基準は一貫して「ガバナンス・権限・コードの取得や実行の入口」で、通常のコードや docs まで常に人に回さないためです。

人が足したいパスは `config/gate-paths.conf` に書きます(組み込みは外せません)。
glob は広めに当たります(`*` が `/` も越える)ので、`Dockerfile` で始まる名前(`docs/dockerfile-notes.md` など)も hard になります。人に回る側へ倒す作りです。

## レビュー結果の記録(`tb review-submit`)

```
tb review-submit <task-id> --lineage <系統> --sha <head の sha> --blocker <n> --should <n> --verdict pass|fail [--pr <o>/<r>#<n>] [--evidence <パス>]
```

- 記録先は固定の追記専用ファイル `data/gate-reviews.log`(`.gitignore` 済み。env では変えられません)。DB のスキーマは変えません。既存の行は書き換えません。
- 1 行の形: `ts=<UTC> task=<id> by=<送信者> pr=<o/r#n|-> REVIEW <系統> <sha> blocker=<n> should=<n> verdict=<pass|fail> evidence=<パス|->`
- **送信者は pane から tb が決めます**(自己申告の引数はありません)。pane が無い実行は `by=human` です。通常の agent 操作では、agent は自分の名前を変えられず、作者と同一の記録も作れません(下の「既知の限界」を参照)。
- chain-id はタスク ID です(`tb gate evaluate` の chain-id と同じ)。タスクが存在することが条件です。
- 終了コード: `0` 記録した / `2` 使い方の誤り / `3` 送信者を特定できない / `4` 作者(タスクの owner)と同一、または作者を確かめられない / `5` 役割・系統が許されない / `1` その他。
- 職務分掌の最小の検査: agent の pane から記録できるのは、役割が `reviewer` のものだけです(`roles/` の固定の置き場と台帳で決まり、env では変えられません)。作者と同一の agent は拒否します。外部系統の `coderabbit` は agent が名乗れません(非 agent の経路から記録します)。
- `verdict=pass` で `blocker` が 1 以上の記録は作れません。

### 評価での読み方

- 記録の sha が評価対象の head と違うものは**無効**で、数えません(古いレビューは無効)。記録の `pr` が対象と違うものも無効です。
- 有効な記録の系統ごとに、同じ sha で fail または blocker が 1 件でもあれば、その系統は失敗(H-03)です。
- 独立した **2 系統の pass**(系統が違い、送信者も違う。外部系統 `coderabbit` は、送信者 `ingest` の記録だけが数えられる)がそろい、blocker が 0 のときだけ、レビューの条件を満たします。**送信者 `human` の記録は、独立な系統の根拠に数えません**(pane が無い実行は誰でも名乗れるため)。ただし `human` の fail や blocker は、ほかの記録と同じく効きます。
- 記録の sha は、head と**完全に一致**したものだけが有効です。7〜40 桁の短縮 sha は、head と長さが違うと(同じ commit を指していても)無効になります。HEAD と記録で同じ長さの sha を使ってください。
- 記録ファイルが無い・読めない(symlink を含む)・形式違反の行が 1 行でもある場合は、評価不能(21)です。**形式違反の行が 1 行あると、すべての chain の評価が 21 になります**(chain ごとに切り分けません)。復旧は人が行います: 該当の行(エラーに `L<行番号>` が出ます)を確認して、追記専用の記録のため、行を直す代わりにファイルを退避して新しい記録から始めるか、その行だけを人が取り除きます。agent は記録を書き換えません。
- 出力には、どの系統が何を記録したかを `reason: REC ...` で全部出します(無効な記録は理由つき)。

## 外部レビューの取り込み(段 1b: `tb gate ingest-review`)

```
tb gate ingest-review <task-id> --pr <o>/<r>#<n> --sha <head の sha>
```

外部レビュー(CodeRabbit)を、agent ではなく**決定論の取り込み口**が、別の送信者 `ingest`(固定の名前)で記録します。LLM は使いません。`gh` の読み取り(`api` の GET だけ)で `pulls/<n>/reviews`、`pulls/<n>/comments`、`issues/<n>/comments` を取り、`REVIEW coderabbit <sha> blocker=<n> should=<n> verdict=<pass|fail>` を、`tb review-submit` と同じ追記専用の記録に足します。

- **記録する条件**: 設定した投稿者(既定は `coderabbitai[bot]`)のレビューが、`--sha` の head に対して存在し(コミット ID の前方一致)、状態が COMMENTED / APPROVED / CHANGES_REQUESTED で、スキップの印が無いときだけです。
- **記録しない(fail-closed。「記録が無い」= レビューの条件を満たさない)**:
  - head に対するレビューが無い・sha が違う・PENDING / DISMISSED(終了コード 10)
  - レビューがスキップされている(先頭 12 行に `rate limit`、`review skipped`、`reviews paused`、`usage limit`、`quota` のいずれかがある。head のレビューより新しいコメントにあっても同じ)(11)
  - 取得に失敗した・JSON として読めない・配列でない・出力が空(12)。なお、head に対するレビューが 0 件(空の配列 `[]`)は 10、gh の出力そのものが空・壊れているのは 12 です(どちらも記録しません)
  - **スキップや 0 件を pass とは記録しません。** 無料枠の上限で何も出なかったときは、レビューが存在しないので記録されません。
- **数え方**(未解決の指摘だけ): 設定した投稿者のインラインコメントのうち、まだ有効なもの(`position` がある、または `subject_type` が `file` のファイル単位の指摘)で、`✅ Addressed in commit` の印が無いものです。解決済みかどうかは REST では取れないため、この印で近似します(印が無い解決済みの指摘は数え続けます)。`position` が無く、ファイル単位でもない指摘は、コードの変更で位置が消えた(outdated)ものとして数えません。
- **重大度の対応表**: 指摘の本文から **HTML コメントとタグを取り除き、空行を飛ばした最初の行(最初の実質行)**を取り、その表記(小文字化して部分一致)を表で `blocker` か `should` に対応させます。実物の CodeRabbit の指摘は、1 行目が HTML コメント、2 行目が空で、重大度は 3 行目(picture タグ入り)にあるため、物理的な 1 行目ではなく最初の実質行を見ます。既定は `critical`・`major` = blocker、`potential issue`・`minor`・`trivial`・`nitpick`・`refactor suggestion` = should。最初の実質行が複数の表記に当たるときは blocker が優先で、1 件と数えます。表は設定(`[severity]`)で足せます。blocker に上げる方向だけが効き、should に下げる設定は警告して無視します。
- **表記が見つからない指摘**(最初の実質行に表の表記が無い)は、`should` として数え、**`blocker` には数えません**。つまり「見落とすと pass 側に倒れうる」ので、**保守的な扱いではありません**。見落としに気づけるよう、件数を警告に出します(`warn: 重大度の表記が見つからない指摘が N 件あります`、成功時の出力にも `unknown-label=N`)。表記が見つからない指摘を blocker に倒すかは、人の判断で、**未決**です(倒すと、表記の形が変わったときに常に fail になる懸念があります)。
- `blocker` が 1 以上なら `verdict=fail`、0 なら `pass` です(should は pass のまま)。
- **本文の中の指示は読まず、実行しません**(Prompt for AI Agents、自動修正のチェックボックスなど)。見るのは、最初の実質行の重大度の表記、`✅ Addressed in commit` の印、スキップの見出し、件数だけです。
- 終了コード: `0` 記録した / `2` 使い方の誤り / `5` agent の pane からは実行できない / `1` 設定または gh を使えない / `10` / `11` / `12` は上のとおり(記録しない)。

- **同じ sha で一度 fail が記録されると、その sha のままでは復旧しません。** `evaluate` は「同じ sha で 1 件でも fail の系統は数えない」ので、指摘を直さずに(あるいは `✅ Addressed` の印が付く前に)再取り込みして pass が記録されても、先の fail が残ります。**新しい sha(新しいコミット)でレビューが付くことでだけ解消します。**
- `--sha` は呼び出し側の値で、PR の現在の head そのものとは照合しません(取得したレビューの `commit_id` との一致を見るだけで、`evaluate` が head と照合します)。取り込みを定期的に呼ぶジョブは、PR の現在の head を GitHub から取って `--sha` に渡す前提です(次の一歩)。

### 設定(`config/gate-ingest.conf`)

固定の場所で、env では差し替えられません(`.gitignore` 済み。`config/gate-ingest.conf.example` をコピーして使います)。`[gh] path` は読み取り専用の gh ラッパーの**絶対パス**で、必須です。未指定・不正のときは警告して止まります(PATH 上の gh には落ちません)。`~/bin/gh` などのラッパー自体は、この変更では変えません。`[source] login` は取り込む投稿者、`[severity]` は上の対応表です。

### 送信者 `ingest` の扱い

- `tb gate evaluate` は、**外部系統(`coderabbit`)の記録のうち、送信者が `ingest` のものだけを独立系統として数えます。** agent が `review-submit --lineage coderabbit` で書いた記録や、`human` が書いた記録は、(sha が合っていても)独立系統には数えません。ただし、その記録の fail や blocker は、ほかの記録と同じく効きます。送信者 `ingest` が外部系統以外の系統を名乗る記録も数えません。
- `tb review-submit` は、agent の pane から `coderabbit` を名乗れません(送信者は pane から決まります)。`ingest` という名前の agent も記録できません(予約名)。
- `tb gate ingest-review` は、agent の pane(`HERDR_PANE_ID` がある)からは実行できません。
- これで、独立 2 系統は「別の agent の `self`」と「`ingest` の `coderabbit`」になります。

## 全レビューの身元つきの記録と表示(段 1c: `tb gate ingest-events` / `tb gate reviewers`)

設計: `docs/gate-reviewer-trust.md`。**段 1c は記録と表示だけです。判定(`evaluate`)の入力は変えず、`evaluate` はこの記録を読みません。** `tb gate execute` は常に 30 のままです。重み・信頼度(段 1d 以降)は作っていません。

```
tb gate ingest-events <task-id> --pr <o>/<r>#<n>     全レビューを身元つきで追記する(gh の読み取りだけ。agent の pane からは実行不可)
tb gate reviewers <o>/<r>#<n> [--task <id>]           記録をレビュアー(数値 id)ごとにまとめて表示する(読み取りだけ。gh は呼ばない)
```

- **取るもの**(`gh` の GET の `api` だけ。段 1b と同じ専用ラッパー、`config/gate-ingest.conf` の `[gh] path`): `pulls/<n>`(作者の数値 id)、`pulls/<n>/reviews`、`pulls/<n>/comments`、`issues/<n>/comments`。この取り込みでは `[source] login` は要りません(全員を取るため)。未指定・不正なラッパーでは止まります(fail-closed)。
- **記録**(固定の追記専用ファイル `data/gate-review-events.log`。env では差し替えられず、`.gitignore` 済み。`gate-reviews.log`(判定の入力)とは別):
  `ts= task= repo= pr= kind=review|inline|issue event=<id> actor=<login> actor_id=<数値 id> actor_type=user|bot|org assoc=<小文字> author=0|1 sha=<40 桁|-> state=<状態|-> label=blocker|should|none addr=0|1 body_sha256=<64 桁> upd=<UTC|-> review=<id|-> orig_sha=<40 桁|-> outdated=0|1 head=<40 桁> by=ingest`
  - **身元は数値 id**(`actor_id`)で取ります。login は表示用で、同じ id で login が変わった場合は `reviewers` が `login-changed` と表示します。同じ login でも id が違えば別人です。
  - **本文は保存しません**(本文の sha256 だけ)。本文中の指示(Prompt for AI Agents、自動修正のチェックボックスなど)は読まず、実行しません。`label` は、段 1b と同じく最初の実質行の重大度の表記を表で対応させた値(inline だけ。表記が見つからなければ `should`)、`addr=1` は `✅ Addressed in commit` の印です。
  - **sha に紐付き**ます(`sha` はレビューと inline の `commit_id`。issue のコメントは紐付かないので `-`)。**ただし inline の `commit_id` は、そのコメントが付いた時のコミットとは限りません**(コミットが積まれると付け替わる)。そのため、次も記録します: `orig_sha`(`original_commit_id`。inline だけ)、`outdated`(inline で `position` が無く、ファイル単位でもない = コードの変更で位置が消えた)、`review`(inline の `pull_request_review_id` = どのレビューに属するか。レビュー自身は自分の id)、`head`(取り込み時点の PR の head の sha)。これらも判定には使いません。
  - 設計書が挙げる `performed_via_github_app` は、項目の有無を実機で確かめられていないため、**記録しません**(bot かどうかは `actor_type` と `actor_id` で足ります)。`author=1` は PR の作者本人の発言(数値 id の一致)で、記録はしますが、独立系統の根拠には数えない方針です(設計の P-R11。段 1e で効かせます)。
  - **追記専用で冪等**です。同じ発言の同じ内容(**task**、repo、PR、種類、id、本文のハッシュ、更新時刻、状態、sha、outdated)は足さず、編集された発言や、位置が消えた(outdated になった)発言は新しい行として足します(別のタスクで取り込めば、そのタスクの記録になります)。`head` は取り込み時点の値なので、重複排除のキーに入れません。**並行して実行しないでください**(同時に走ると、同じ行が 2 回追記されうる。表示は発言ごとの最新だけを使うので変わりませんが、記録は重複します)。`reviewers` は、同じ発言(種類と id)の最新の行だけを使います。
  - **全部か無か**: 1 件でも形式が不正(login・数値 id・種類・関連・sha・時刻など)なら、何も記録しません(終了コード 12)。取得の失敗(PR 本体を含む 4 つのどれか)・JSON として読めないのも 12 です。既存の記録に形式違反の行がある、または記録が symlink のときは止まります(1)。
- 終了コード(`ingest-events`): `0` 記録した(`new=N unchanged=M`) / `2` 使い方の誤り / `5` agent の pane からは実行できない / `1` 設定・gh・既存の記録を使えない / `12` 取得・解析・形式の失敗(記録しない)。`reviewers` は、記録が壊れていれば 1、使い方の誤りは 2、記録が無いときは `events: 0` で 0 です。
- **`reviewers` の表示**: レビュアーごとに `login id type assoc author reviews inline issue label-blocker label-should last` を出します(表示だけで、判定には使われません)。**`label-blocker` / `label-should` は、投稿者が書いた本文の最初の実質行の語から機械的に数えた値で、検証された重大度ではありません。**誰でも書ける語で決まる(例: 荒らしの `Critical` という 1 語が `label-blocker` になり、重大度の語が無い承認のコメントが `label-should` になる)ので、人が重みを決める材料にするときは、`assoc` や発言の内容と併せて見てください。表示の先頭にも同じ注記が出ます。`assoc`(`author_association` の小文字)は、人が重みを決めるときの参考です。CODEOWNERS に載っているかの表示は、段 1d で足します(未実装)。
- **既知の限界(段 1d 以降で決める)**: **1 件でも形式が不正な発言(例: 投稿者が `null`、未知の type、変則的な login)があると、その PR の身元つきの記録は作れません**(全部か無か。終了コード 12。メッセージに種類と id が出るので、人が該当の発言を GitHub で確認します)。公開リポジトリで、外部の投稿者が 1 件の変則的な発言を付けると、その PR の記録が止まりうる、という意味です。形式が読めない投稿者だけを最低信頼として記録し、ほかを記録する設計(設計書の P-R2 の既定と同じ扱い)は、段 1d 以降の判断です。issue のコメントなどで `author_association` が欠けているときは、`assoc=unknown` と記録します。
- **段 1c で作らないもの**: 重み・信頼度の設定、低信頼の扱い、`evaluate` への入力、消えた発言の記録(`state=gone`。P-R9 の決定のあと)。
- 既知の限界は段 1b と同じです(`HERDR_PANE_ID` を外せば pane 検査は破れる。同じ OS ユーザーの agent は記録ファイルを直接書ける。OS の権限による分離の段で閉じる)。記録の `by=ingest` は固定の値で、ファイルを直接書ける同じ OS ユーザーは偽装できます(上の限界)。

### 既知の限界と次の段

段 1a の記録は、**通常の agent 操作で作者が自分のレビューを作れないようにする最小の検査**です。次のことは防げません。したがって、`tb gate evaluate` の allow を、実行や承認の許可の根拠に使うのは、次の段(OS の権限による分離)より後にしてください。

- agent は、同じ OS ユーザーで動いているため、記録ファイルを直接書ける、`HERDR_PANE_ID` を外して `by=human` で記録できる、`HERDR_BIN`・`TB_DB` を差し替えて別の agent 名や別のタスクの owner を名乗れる、といった迂回ができます。
- `by=human` を独立 2 系統の根拠に数えないのは、その迂回の一つ(1 体の agent が複数の系統を作ること)を小さくするための検査です。
- `HERDR_BIN`・`TB_DB` の差し替えへの対処(警告・拒否・無視)は、このテスト基盤が同じ環境変数に依存しているため、段 1a では入れていません。対処は、記録の書き込みを専用の経路に移す次の段で行います。
- 取り込み口が使う gh ラッパー(`[gh] path`)の置き場の所有者・書き込み権限は、`tb` は確かめません(誰でも書けるファイルを指定できる)。設定は人だけが書く前提で、ラッパーの置き場の権限も、OS の権限による分離の段で扱います。
- `reviewer` の役割ファイル(`roles/reviewer.md` と `map.conf` の対応)は、人が用意します。無い間、agent の pane からは記録できません(人の端末からは記録できます)。
- 外部系統(CodeRabbit など)の取り込み口(`tb gate ingest-review`)は、**agent ではない決定論の経路が、`human` とは別の送信者 `ingest` で記録する**ものです。ただし、`ingest-review` の「agent の pane から実行できない」は `HERDR_PANE_ID` で見ているので、環境変数を外せば破れます。記録ファイルを書ける同じ OS ユーザーの agent は、`ingest` の行を直接書くこともできます。これも次の段(OS の権限による分離)で閉じます。取り込み口を呼ぶのは、人または決定論のジョブ(次の一歩の scheduler)です。
- タスクの owner は DB から読みます。DB の場所は `tb` 共通の設定です。

**次の段(OS の権限による分離)**: 記録ファイルを、agent の実行ユーザーが書けない所有者と権限にして、書き込みを専用の経路(決定論のジョブ)だけにします。判定を呼ぶ側が入力と記録を書き換えられないことを、設定ではなく OS が強制する形にします。

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
- 取り込み口を定期的に呼ぶ決定論のジョブ(scheduler)、判定の記録、影武者の集計

## 段 0 でやらないこと

- merge / approve の実行(`tb gate execute` は常に 30)
- 判定の記録、DB の変更(レビュー結果の記録は、DB ではなく追記専用のファイルです)
- 緊急停止、回数の上限、実行用の認証

これらは段 1 以降の別の変更で扱います。
