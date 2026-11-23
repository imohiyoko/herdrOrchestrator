---
name: testing
description: このリポジトリでテストを設計・追加・修正・実行するときに使う。tb(taskboard/test/cases)、Web UI、scheduler、setup、任意の部品、Go(herdrctl)と TB_IMPL=go の契約テストの置き場所・書き方・並列化の決まり・CI の組を扱う。
---

# テストの設計と実装

このリポジトリのテストは、部品ごとに別の仕組みで書かれている。どれも **本物の herdr・launchd・ネットワーク・利用者の `~` には一切触れない**。新しいテストもこの決まりを守る。

## まず決めること

1. **どの部品の振る舞いか** を決め、下の表のテストに足す。bash の `tb` を変えたら tb のケース、`webui/app/*.py` なら test_webui.py、という対応にする
2. **何を確かめるか**: 正常系だけでなく、境界(0 件・上限ちょうど・期日の当日)、失敗の終了コード(使い方の誤り=2、対象なし=1 など、そのコマンドの約束)、もう一度流しても変わらないこと(冪等)、秘密が DB・ログ・送信本文に出ないことを考える
3. **バグ修正では先に再現テストを書く**。直す前に FAIL し、直した後に ok になるのを確かめてからコミットする
4. **時刻・日付に頼らない**。`TB_TODAY`(tb の「今日」)、scheduler の `NOW`、固定の ISO 時刻を使う。`sleep` で待たない(待つ処理は `*_SEC` / `*_POLL_SEC` の環境変数で短くする)

## 部品とテストの対応

| 部品 | テスト | 1 つだけ流す |
| --- | --- | --- |
| tb(`taskboard/`) | `taskboard/test/cases/NN_*.sh` | `bash taskboard/test/run.sh 93_prqueue.sh` |
| Web UI(`webui/`) | `webui/tests/test_webui.py`(1 本のスクリプト) | `bash webui/tests/run.sh` |
| scheduler | `scheduler/tests/run.sh` | 同左 |
| setup | `setup/tests/run.sh`(疑似の repo・HOME・LaunchAgents、`setup/tests/fake/`) | 同左 |
| 任意の部品(core から呼ばない部品) | その部品の `tests/run.sh`(組の一覧は `bash tests/run-all.sh --list`) | 同左 |
| herdrctl(Go) | `internal/**/*_test.go`、`cmd/herdrctl/main_test.go` | `go test ./internal/kernel/store/` |
| 全体の小物(misc) | `tests/test_env.sh`(bash と Python の既定値の突き合わせ)、`tests/test_instance.sh`(複数 herdr セッションの切り分け)、`tests/test_webui_queries.py`(webui の SQL を本物のスキーマで EXPLAIN)、`taskboard/tools/deps.py --check`(lib の層の向き) | `bash tests/run-all.sh misc` |

すべて流すのは `bash tests/run-all.sh`(`-j N` で組を並列に)。必要なもの: bash 4.3 以上、sqlite3、jq、python3、go。

## tb のケース(`taskboard/test/cases/`)

`run.sh` はケースを **ユニットごとに別のプロセス・別の一時ディレクトリ**(`$TMP`)で並列に流す。ユニットは 1 ケースが基本。

### 新しいケースを足す

- 名前は `NN_<機能>.sh`。`NN` は `taskboard/lib/` の対応するファイルのおおむねの順。先頭は `# shellcheck shell=bash` と、何を確かめるかの 1 行
- `run.sh` が用意するもの: `$TB`(bin/tb)、`$TMP`、`$TB_DB` / `$TB_CONFIG` / `$TB_LIMITS`(`$TMP` の下)、`HERDR_BIN`=疑似 herdr、`$FAKE_LOG`(疑似 herdr が呼ばれた記録)、`$HERE`(test/)
- 判定の部品: `eq "<名前>" "<実際>" "<期待>"`、`rc <コマンド>`(終了コードを出す)、`ok` / `ng`、`prompts`(送信の数)。名前はケースの中で一意に、何が成り立つべきかを書く(例: `"claim twice fails"`)
- **前のケースの状態を使わない**。自分のケースで使う project 名(例: `-p tk`)、DB(`export TB_DB=$TMP/<名前>.db`)、設定ファイル(`$TMP/<名前>.conf`)を自分で用意する。先頭のコメントに「project は tk / tk2 だけを使う」のように書く
- 環境変数を変えたら、同じユニットの後続に漏らさないよう最後に戻す(`_saved_db=$TB_DB` … `export TB_DB=$_saved_db`)。`HERDR_PANE_ID` を外す・戻す型は `12_conv.sh` を見る
  - 「元は設定されていなかった」も戻す。`unset` や `export X=...` をする変数は、先頭で `_saved_env=$(declare -p A B C 2>/dev/null)` と保存し、最後に `unset A B C` してから `eval "$_saved_env"` で戻す(`84_recipe_runner.sh`。ケースは run.sh の最上位で source されるので大域に戻る。`declare -` を `declare -g` に置き換えると、export していない変数の `declare --` が壊れる)。`X=${X-}` で保存して `unset` で戻すだけだと、元の値が消える
  - ケースの中の関数(例: `rr_reset`)が `unset` する変数も、戻す対象に入れる
- ケースの中で `c`・`out` などの変数は自由に使ってよい(runner の変数は `__` 始まり)
- どのユニットも最後に `99_zzz_session_sweep.sh` が流れ、テスト中に書かれた **すべての行に `herdr_session` が入っているか** を見る。新しい表・INSERT はセッション列を埋める
- どうしても前のケースの変数や DB を使うなら、`taskboard/test/groups` に「名前: 前のケース 後のケース」と 1 行足す(今は tasks / alerts / gate の 3 組だけ。増やすと並列で縮まなくなるので最後の手段)

### 確かめ方

```bash
bash taskboard/test/run.sh 14_cancel.sh          # 足したケースだけ
bash taskboard/test/run.sh                       # tb 全部(並列、本数は -j N か TB_TEST_JOBS)
bash taskboard/test/run.sh --units               # ユニットの一覧(groups が効いているか)
```

足したケースは **単独で** 通ることを必ず確かめる。全体でだけ通るなら、前のケースの状態に頼っている。

### 疑似 herdr(`taskboard/test/fake-herdr`)

本物の代わりに呼ばれ、呼ばれ方を `$FAKE_LOG` に 1 行ずつ書く(`prompt <宛先>\t<本文>`、`get <名前>` など)。振る舞いは環境変数で変える: `FAKE_STATUS`(idle / working / blocked / unknown / missing)、`FAKE_PROMPT_RC`(送信の成否)、`FAKE_AGENTS`(agent 一覧の JSON)、`FAKE_READY_AFTER`、`FAKE_DYNAMIC` など。新しい herdr のコマンドを使うなら、ここに足す。

### よく漏れる観点

- **値に空白・改行・タブ・`=` を入れる**。bash で値を配列に読む所(`mapfile -t`、`read`)は改行で分かれる。text 型など改行を受ける値は、受け付ける所だけでなく、DB や JSON を通って別の所で読み直す所まで通す(`jq -j '... \u0000'` と `mapfile -d ''` のように、値に含まれない区切りを使う)
- **期限・時間切れは、取り出すたび・実行の時点で効くか**。最初に 1 回だけ見ていると、前の処理に時間がかかったあいだに期限を過ぎたものが通る。`sleep` で待たず、DB の時刻を `strftime('%Y-%m-%dT%H:%M:%fZ','now','-99.5 seconds')` のように今から少し前にずらし、前の処理の時間は短い `timeout` の部品で作る
- **疑似 herdr の状態は操作で変わる**。`FAKE_DYNAMIC=1` で agent を止める・戻すと pane が変わるので、後の節で同じ pane(`HERDR_PANE_ID=wW:p1` など)を使うなら、節の頭で `FAKE_AGENTS` と `$FAKE_LOG.*` を作り直す。作り直さないと、pane の agent が見つからず人の端末として扱われ、関係のない所で落ちる
- **人と agent の両方から**呼ぶ。権限の分かれ目(人だけ・依頼者だけ)は、通る側と止まる側の両方を確かめる

### 直す前に落ちることを確かめる

足したテストが、直す前のコードで FAIL することを確かめる。直したファイルだけを一時的に戻して流し、元に戻す:

```bash
cp taskboard/lib/recipes.sh /tmp/fixed.sh
git show HEAD:taskboard/lib/recipes.sh > taskboard/lib/recipes.sh
bash taskboard/test/run.sh 84_recipe_runner.sh   # 足したテストだけが FAIL する
cp /tmp/fixed.sh taskboard/lib/recipes.sh
```

### CI の組と weights

CI は `--shard 1of4` … `4of4` を別のジョブで流す。組は `taskboard/test/weights`(ケースごとの秒数)で自動で分ける。weights に無いケースは 10 秒とみなすので、**重いケースを足したら `bash taskboard/test/run.sh --timings` で weights を書き直してコミットする**(bash 5 以上)。1 ケースが 1 分を超えるなら、独立した節ごとにファイルを分けると CI が縮む。

### TB_IMPL=go の契約テスト

tb のケースは Go 版(herdrctl)の仕様書でもある。CI の `tb contract` ジョブは同じケースを `TB_IMPL=go` で流し、Go に移したコマンド(`herdrctl tb --commands`)は herdrctl が答える。

```bash
go build -o /tmp/herdrctl ./cmd/herdrctl
TB_IMPL=go TB_HERDRCTL=/tmp/herdrctl bash taskboard/test/run.sh
```

herdrctl を作らずに `TB_IMPL=go` で流すと、ほとんどのケースが「herdrctl が見つかりません」で落ちる(コードの問題ではない)。

`TB_IMPL` を付けずに流すと bash で流れる(run.sh が `TB_IMPL=bash` にする。手元の PATH に herdrctl があっても変わらない)。tb 自体は、未設定なら使える herdrctl があれば Go を使う。

コマンドを Go に移すときは、ケースを書き換えずにこれが通ること。bash 側の出力(列の順・空白・終了コード)を変えたら、Go 側も合わせる。

## Web UI(`webui/tests/test_webui.py`)

- 1 本のスクリプトを上から流す。節は `# ================= <題>` で区切る。判定は `check("<名前>", <条件>, <詳細>)`
- 環境は `make_env("<名前>", ...)` で節ごとに作る(DB・run・state・認証の置き場がすべて一時ディレクトリ)。サーバは `start(env)`、ポートは `wait_port`
- **ポートは決め打ちしない**。`free_range(n)` が高位の空きポートを選ぶ(並列に流しても衝突しない)
- DB の fixture は本物のスキーマ(`taskboard/schema/*.sql`)を流して作り、`herdr_session` を入れる
- webui が投げる SELECT は `tests/test_webui_queries.py` が本物のスキーマで確かめる。webui の SQL を変えたら misc も流す

## scheduler / setup / 任意の部品

- scheduler: `DRY_RUN=1` と `NOW` の差し替えで tick の判定だけを見る。ジョブはファイル(`JOBS_DIR`、`SCHED_FILE_FALLBACK=1`)か一時の DB に置く
- setup: `fresh` で全部そろった疑似環境を作り、そこから 1 つずつ壊して check.sh の判定を見る。PATH に置くコマンドを絞って「無いとき」を作る
- 任意の部品: その部品の README と `tests/` の共通部品に従う。core(taskboard・webui・scheduler・setup など)とこのスキルを含む core 側のファイルからは、任意の部品の名前を出さない(任意の部品のテストが `git grep` で確かめている)

## Go(herdrctl)

- `t.TempDir()` と `t.Setenv()` で隔離する。CI は ubuntu / macOS / Windows で `go vet ./...` と `go test ./...` を流すので、パスは `filepath`、改行や実行権限に OS の差が出ないようにする
- bash の tb と同じ結果になることの突き合わせ(`TestSameSchemaAsBashTB`)は `TB_PARITY=1` のときだけ(bash・sqlite3・jq が要る)

## やってはいけないこと

- テストを消す・飛ばす・期待値を実際の値に合わせて書き換えるだけで通す(落ちた理由をまず調べる)
- 落ちたら「たまたま」で片付ける。並列や単独で落ちるのは、順序・時刻・共有した状態に頼っている印。例: `tb history --json` は `ORDER BY id` が別名の文字列で並んでいて、イベントが 10 件を超えると E-10 が E-2 の前に出ていた。単独で流して見つかった
- 本物の設定(`taskboard/config/*.conf`)・`~/.local/state/herdr/tasks.db`・checkout の中にファイルを書く(一時ディレクトリだけ)

## PR を出す前に

変えた部品のテストを流し、tb なら足したケースを単独で、と全体を並列で流す。CI(`test-all` と `go-all`)が必須のチェック。
