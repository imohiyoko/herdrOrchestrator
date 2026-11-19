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
repoindex/bin/repoindex query search|get|stale|freshness ...   # 人が確かめる用(serve と同じ中身)
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

- 上限: 1 回 20 件、1 件 1200 字、1 回の合計 8000 字(`ri/read.py`)。
- サーバーのログ(stderr)に本文は出さない。ツール名と件数だけ。
- このツールを持つのは **専用の読み取り役の agent 1 つだけ**(#106 / #109)。ほかの agent は、その agent に普段のメッセージで質問して答えをもらう。
  読み書きの道具(`init` / `repo-add` / `put` / `purge`)は、その agent に渡さない。

DB を読み取り専用で開いても、WAL の `-shm` には同じユーザーの読み書きが要る(作る側が `0600` で作る)。
読み取り側と更新側は同時に動かしてよい(WAL。読み取り側は更新前の版を読む)。

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

## 元に戻す

`repoindex purge --yes`(データのディレクトリごと消す)。ほかの部品は何も依存していない。モジュールごと外すなら、このディレクトリも消す。

## テスト

```
bash repoindex/tests/run.sh       # tests/run-all.sh repoindex と同じ
```

DB は一時ディレクトリ、リポジトリは一時の git リポジトリ。ネットワークとモデルは使わない。
