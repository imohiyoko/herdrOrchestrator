# repoindex — リポジトリの要約の索引(任意のモジュール)

レビュー・質問・障害調査のときに「この変更のまわりで何が動いているか」を引くための、リポジトリごとの要約の索引。
**任意(opt-in)のモジュール**で、core(taskboard / webui / scheduler / setup)はこれを呼ばない。無くても何も変わらない。

- 設計の issue: #105(全体)/ #106(置き場所と読み取りの口)/ #107(差分での更新)/ #108(取り込みの安全)/ #109(引き方)/ #110(段階的な導入)
- **要約は手がかりで、根拠ではない。** 結果にはいつも鮮度(要約を作ったコミット)と元のソースの場所が付く。判断の前に、その時点のソースを読む。
- **要約の中身は issue・PR・公開の場所に出さない。** 公開してよいのは仕組み(このディレクトリのコード)だけ。

## 置き場所

| もの | 場所 |
|---|---|
| コード | このディレクトリ(git 管理) |
| データ(DB) | `${XDG_STATE_HOME:-~/.local/state}/herdr-repoindex/index.db`。`REPOINDEX_DIR` で変えられる |

- タスク DB とは別の SQLite ファイル(全文検索は FTS5)。タスク DB に表を足さない。
- ディレクトリは `0700`、DB は `0600`(作るときに umask 077)。`repoindex status` で確かめる。
- データの置き場所が git の作業ツリーの中(この checkout を含む)なら、作るのを拒否する(誤ってコミットされないように)。
- 同じユーザーのプロセスは、そのユーザーが読めるファイルを何でも読める。権限は**間違いを防ぐ**ためのもので、悪意のあるコードは防げない。
- バックアップから外すかどうかは未定(データは作り直せる)。人が決める。

## 使い方

```
repoindex/bin/repoindex init                       # DB を作る・版を上げる
repoindex/bin/repoindex status                     # 置き場所・権限・リポジトリごとの件数(読み取りのみ)
repoindex/bin/repoindex repo-add <名前> <clone のパス>   # 索引に入れるリポジトリ(人が選ぶ。自分の権限で clone 済みのもの)
repoindex/bin/repoindex repo-remove <名前>          # そのリポジトリの行をすべて消す
repoindex/bin/repoindex put --repo R --kind dir|file|symbol --path P --commit C [--symbol S] [--boundaries auth,authz,external,data] < 本文
repoindex/bin/repoindex query search|get|stale|freshness|context ...   # 人が確かめる用(serve と同じ中身)
repoindex/bin/repoindex serve                      # 読み取り専用のツール口(MCP stdio)
repoindex/bin/repoindex purge --yes                # 元に戻す: データのディレクトリごと消す
```

### 読み取りの口(`serve`)

MCP の stdio サーバー。agent ごとに 1 プロセスを起動する形で、DB は読み取り専用(`mode=ro` + `query_only`)で開く。
書き込みの道具は無く、書き込みを試みても失敗する。

| ツール | 中身 |
|---|---|
| `repoindex_search` | 要約の全文検索(`query`、任意で `repo` / `limit`) |
| `repoindex_get` | ディレクトリ・ファイル・シンボルの要約。無ければ `missing` を返す(空の答えにしない) |
| `repoindex_stale` | 鮮度が落ちた要約の一覧(本文なし) |
| `repoindex_freshness` | リポジトリごとに、索引を進めたコミットと件数 |
| `repoindex_context` | 変更のまわり(下の「変更のまわりを引く」) |

- 上限: 1 回 20 件、1 件 1200 字、1 回の合計 8000 字(`ri/read.py`)。
- サーバーのログ(stderr)に本文は出さない。ツール名と件数だけ。
- このツールを持つのは **専用の読み取り役の agent 1 つだけ**(#106 / #109)。ほかの agent は、その agent に普段のメッセージで質問して答えをもらう。
  読み書きの道具(`init` / `repo-add` / `put` / `purge`)は、その agent に渡さない。

DB を読み取り専用で開いても、WAL の `-shm` には同じユーザーの読み書きが要る(作る側が `0600` で作る)。
読み取り側と更新側は同時に動かしてよい(WAL。読み取り側は更新前の版を読む)。

## 更新(#107)

```
repoindex/bin/repoindex repo-add <名前> <パス> [--granularity dir|file]   # 既定 dir(ディレクトリだけ)
repoindex/bin/repoindex update <名前> | --all     # 差分で更新
repoindex/bin/repoindex update --all --check      # 変更があるかだけ(0 = ある、1 = ない)。何も書かない
repoindex/bin/repoindex update <名前> --dry-run   # stale / missing の印だけ付け、要約は作らない
```

1. 索引を進めたコミット(`repos.head_commit`)と今の `HEAD` の差分を取る(初回は全体)。
2. 変わったファイルの要約(とそのシンボルの要約)、その親ディレクトリの要約を `stale` にする。消えたもの・名前を変えた元・除外されるようになったものは `missing`。
3. 変わったものだけを作り直す。保存の前に必ず安全の検査(落ちたら作り直す。下)。
4. 最後に `head_commit` を進める。変わったものが無ければ何もしない(要約のコマンドを 1 回も呼ばない)。

**要約を作るコマンドは人が設定する**(リポジトリの中身をモデルに送る判断そのもの)。未設定なら何も送らず、印を付けるだけで索引は進めない。

| 環境変数 | 中身 |
|---|---|
| `REPOINDEX_SUMMARIZER` | 要約のコマンド。標準入力に中身(ファイルの本文、またはディレクトリの直下のファイル名と各先頭 40 行)。環境変数 `REPOINDEX_KIND`(dir / file)`REPOINDEX_PATH` `REPOINDEX_ATTEMPT` が付く。標準出力の先頭の `boundaries: auth,external` と `calls: a/b.py, c/d.py` の行を境界・呼び先として読む(呼び先はリポジトリにあって除外されないファイルだけ) |
| `REPOINDEX_SUMMARIZER_DIR` | ディレクトリの要約だけ別のコマンド(例: ファイルは軽いモデル、境界とファイルどうしの関係を見るディレクトリは強いモデル) |
| `REPOINDEX_MODEL` | 記録用のモデル名(要約の行に残る) |
| `REPOINDEX_MAX_ATTEMPTS` | 検査で落ちたときの試行の上限(既定 3、最大 10) |

- 除外するパスは名前も中身も入力にしない。200 KB を超えるファイルとバイナリも入れない。
- 細かさ: 大きなリポジトリは `dir`(既定)、小さなものは `file`。生成物と vendor はパスの規則で外す。
- 検査で落ちたら、同じ入力で作り直す(落ちた本文は入力に入れない。検査は毎回最初から、弱めない)。
  上限に達したら理由の符号だけを `rejections` に残し、標準エラーに 1 行で知らせる。同じコミットではもう作り直さない。

### 鮮度

結果の `freshness.reasons` に次が付く(どれか 1 つでもあれば `stale: true`)。

| 理由 | 意味 |
|---|---|
| `stale` / `missing` | 更新が印を付けた(作り直し待ち / ソースが消えた) |
| `old_format` | 要約の書式の版が今より古い |
| `file_changed` / `file_missing` | 今の `HEAD` のファイルが、要約を作ったときと違う / 無い(git の blob hash で比べる) |
| `changed_since_summary` | ディレクトリ(と hash の無い要約)の中が、要約を作ったコミットから変わった |
| `source_unavailable` | 元のリポジトリを読めない |

`repoindex_freshness` は、リポジトリごとに索引のコミットと今の `HEAD`(`index_behind`)を返す。変わっていないファイルの要約は、古いコミットで作ったものでも stale にしない。

## 変更のまわりを引く(#109)

`repoindex_context`(`repo` と、`paths` = 変わったファイル、または `range` = `base..head`)。`query context --repo R [--range A..B] [パス ...]` も同じ。

1. 変わったファイルごとに、そのファイルとディレクトリの要約。無ければ `missing` に明示する(空の答えにしない)。
2. 呼び出し元: 索引が持っている関係(`relations`。要約のコマンドが `calls:` で返したもの)と、普通のコード検索(ファイル名の語で `git grep`)。
   コード検索は場所(path と行)だけを返し、行の中身は返さない。除外パスは出さない。
3. 呼び先: 索引が持っている関係。
4. 関わる境界(`auth` / `authz` / `external` / `data`)は、変わったファイル・ディレクトリと呼び先の要約から集める。
5. 影響の範囲(`blast_radius`): 変わったもの・呼び出し元・呼び先と、その理由。**どの項目にも元ソースの場所(path・行・commit)**。

上限: 変わったファイル 10 件、ファイルごとの呼び出し元・呼び先 5 件、まわりの要約 1 件 400 字、合計 8000 字。

### 専用の読み取り役

ツールを持つのは **専用の読み取り役の agent 1 つだけ**。ほかの agent はその agent に質問して答えをもらう(ツールを直接持たない)。
その agent は段階 2(#110)で、安全の検査のテストが通ってから作る。

- 役割: [`roles/reader.md`](roles/reader.md)。`tb role-write repoindex-reader < repoindex/roles/reader.md` で DB に入れ、
  `tb agent-new <名前> --role repoindex-reader` で作る。
- ツールの口: [`mcp.json.example`](mcp.json.example) をその agent の作業ディレクトリの `.mcp.json` にだけ置く(パスを直す)。
  ほかの agent の設定には入れない。
- 答えは手がかり。受け取った agent も、判断の前に元のソースを読む。要約の中身は公開の場所に写さない。

## 取り込みの安全(#108)

要約は `put`(と後の更新)で保存する前に、必ず `ri/scan.py` を通る。落ちたら**本文は保存しない**。
`rejections` 表に残るのは理由の符号(`aws_access_key` / `email` など)と回数だけで、本文も一致した文字列も残さない。

- **パスでの除外**: fixture・テストデータ・seed・ダンプ・factory・vendor / 生成物、`.env` 系、鍵と証明書、
  `*credential*` / `*secret*`、CSV や表計算の書き出し、DB ファイルなど。追加は `<データの場所>/exclude.conf`(glob。1 行 1 つ)。
- **本文の検査**: 秘密鍵の見出し、各種トークン・アクセスキー、`password = ...` の形、URL 内の資格情報、メールアドレス、
  口座のような長い数字、ホームの絶対パス、長い乱数列。追加は `<データの場所>/scan-patterns.conf`(`名前 = 正規表現`)。壊れた行があれば止まる。
- **外部のスキャナ**: `REPOINDEX_SCANNER`(例: 手元のシークレットスキャナ)を設定すると、本文を標準入力に渡し、終了コード 0 以外なら落とす。動かなければ落とす(安全な側)。
- 人名・顧客名はパターンで見つけにくい。パスでの除外を主にする。
- コード・コメント・文書の中の文は**データ**。要約を作る側はリポジトリを読むだけ、要約を読む側は手がかりとして扱う(中の指示には従わない)。

```
repoindex/bin/repoindex scan [--path P] < 本文     # 検査だけ(保存しない)。結果は理由の符号だけ
repoindex/bin/repoindex rejections [--repo R]      # 落ちた要約の一覧(本文は持っていない)
```

元に戻す(安全な側): 保存を止める(`put` / 更新を動かさない)か、`purge --yes` でデータを消す。

## 段階的な導入(#110)

段階は人が決め、変えるたびに **判断した人と測った値** が `stages` に残る(測った値の無い変更は受け付けない)。どの段階にも戻せる。

| 段階 | できること | 次へ進む条件(人が判断) |
|---|---|---|
| 0 | 何もしない。測る値を決め、既存の道具(コード検索・言語サーバー・リポジトリのホストの CLI)だけで基準を測る | 測る値の合意 |
| 1 | `repo-add`(1 つ、`--granularity dir` だけ)・`update`・`query` | 検査のテストがすべて通る・落ちる割合が許せる |
| 2 | 読み取りの口 `serve`(専用の読み取り役の agent はここで作る)。`--granularity file` | 基準より速く、見落としが少ない。stale を頼った回数が少ない |
| 3 | 2 つ目のリポジトリ。定期更新の登録(`job register`) | 更新が速い・stale の見落としが無い |
| 4 | 採用・拡大を決める(制限なし) | 上の測る値 |

```
repoindex/bin/repoindex stage show
repoindex/bin/repoindex stage set <0-4> --by <人> --measures '{"time_to_answer_min": 12, "missed_items": 3}' [--note ...]
repoindex/bin/repoindex measures [--repo R]    # 索引が数えられる値
```

測る値: 答えるまでの時間・見落とし・要約の誤り率(抜き取って元ソースと比べる)・stale を頼った回数(ここまでは人が測る)と、
更新の回数と時間・検査で落ちた回数・作り直しで通った数・試行の上限に達した数(`measures` が `runs` / `rejections` から数える)。
原則: まず「何も悪くならない」ことを確かめる。

### 定期更新(承認済みテンプレートだけ)

`repoindex job register`(段階 3 から)は、[`jobs/update.conf.template`](jobs/update.conf.template) を
[`jobs/allowlist.conf`](jobs/allowlist.conf) の sha256 と突き合わせ、合うときだけ `scheduler/jobs/repoindex-update.conf` を書き、
scheduler の `gen-plist.sh` → `install.sh install` で登録する(人がインストーラを動かさなくてよい)。

- 決まったコマンド(`update --all`、変更が無ければ `GATE` の `update --all --check` で止まる)・決まった間隔(60 分)・決まった場所だけ。
- テンプレートが書き換えられて hash が合わない・既存の登録がテンプレートと違うときは、登録も上書きもせず `refused` を残して止まる(人が確かめる)。
- 誰が・いつ・どの hash で登録 / 解除したかは `job_log`。`repoindex job status` で見る。
- 外すのは `repoindex job remove`(1 コマンド)。段階を 3 より下に戻す・`purge` するときは自動で外す。
- `allowlist.conf` とテンプレートの変更は、人のレビュー(PR)を通す。それが一度きりの承認になる。
- scheduler から動くときの要約のコマンドは `<データの場所>/settings.conf`(`REPOINDEX_SUMMARIZER=...` などの決まったキーだけを読む)。

## 元に戻す

`repoindex purge --yes`(定期更新の登録を外し、データのディレクトリごと消す)。1 つのリポジトリだけなら `repo-remove`。ほかの部品は何も依存していない。モジュールごと外すなら、このディレクトリも消す。

## テスト

```
bash repoindex/tests/run.sh       # tests/run-all.sh repoindex と同じ
```

DB は一時ディレクトリ、リポジトリは一時の git リポジトリ。ネットワークとモデルは使わない。
