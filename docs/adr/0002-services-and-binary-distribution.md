# ADR 0002: 部品をサービスの境界で分け、OS ごとのバイナリで配布する

- 状態: Proposed
- 日付: 2026-11-19

## 要旨

- **決定(案)**: bash と Python の部品を、境界のはっきりした「サービス」に分け直し、Go で書いた 1 つのバイナリ `herdrctl`(仮称)に順に移す。サービスはまず**同じバイナリの中のモジュール**として分け(プロセスは分けない)、外から触る口(CLI の引数・終了コード・DB のスキーマ)を契約として固定する。
- **配布**: GitHub Actions で macOS(arm64 / amd64)・Windows(amd64 / arm64)・Linux(amd64 / arm64)向けにビルドし、タグを打つと GitHub Releases に置く(release CD)。
- **移行**: 一度に書き直さない。`tb` などの入口は残し、移し終えたコマンドから順にバイナリへ渡す(strangler fig)。今の bash のテストは、そのまま両方の実装に流す契約テストとして使う。
- **人が決めること**: 下の「人が決めること」の 4 点。

## 状況(Context)

- 今の実装は bash(taskboard の `tb` と lib/*.sh、scheduler、setup、herdr-up.sh)と Python(webui、要約の索引)。依存は bash・sqlite3・jq・python3・launchd。
- 人の希望(2026-11-19): マイクロサービス化を進めたい。将来は、shell でも使えるまま、Windows と macOS それぞれ向けのバイナリを配布して、バイナリで使うようにしたい。そのための release CD が要る。
- 事実(確認済み、2026-11-19、herdr.dev の Windows のページ): herdr は Windows でネイティブに動く(PowerShell でインストール、ConPTY のペイン、セッションの永続化)。ただし `herdr terminal attach` など一部は Windows で動かず、バイナリの署名もない。`herdr pane send` など CLI からの操作が Windows で同じに動くかは**未確認**。
- 事実: scheduler は launchd(macOS)専用。Windows では タスク スケジューラ、Linux では systemd timer か cron が要る。
- 事実: bash・jq・sqlite3 の CLI は Windows の標準にはない。Windows で動かすには、bash の部品を置き換えるか、Git Bash / WSL を前提にするしかない。

## 決定(Decision)

### 1. サービスの境界

今の `taskboard/lib/*.sh` は 30 本近くあり、DB を共有しながら互いに呼び合っている。まず責務で 7 つに分ける。

| サービス | 今のファイル | 持つもの | 外への口 |
|---|---|---|---|
| store(タスク表) | db, core, config, tasks, comments, dbimport, schema/ | tasks.db、スキーマの版上げ、セッションの分割 | 他のサービスだけが使う。直接 SQL を書くのは store だけにする |
| messaging(連絡) | send, transport, digest, notify, alerts, activity, handoff, compact | 送信・保留・まとめ送り・緊急通知・送信履歴 | `tb send` / `report` / `digest` / `notify` |
| agents(agent の管理) | agents, spawn, limits, models, roles, kinds, shed, whoami | agent の作成・停止・復活、役割、上限、モデル | `tb agent-*` / `role-*` / `model-*` |
| policy(承認・ゲート) | approvals, risk, gate, review, prqueue, proxy | 承認判定、ゲート、PR キュー | `tb approve` / `gate` / `pr-queue` |
| scheduler(定期実行) | scheduler/, schedjobs, schedwatch | ジョブの定義と起動、失敗率の監視 | `jobs/*.conf`、OS ごとの起動器(launchd / タスク スケジューラ / systemd) |
| webui | webui/ | 画面とログイン | HTTP(127.0.0.1) |
| 要約の索引(任意) | 索引のモジュール(README の表を参照) | 要約の索引(別 DB) | CLI と MCP |

- herdr とのやり取り(`herdr pane send` など)は「herdr アダプタ」1 か所に集め、テストではここを疑似 herdr に差し替える(今の fake-herdr と同じ考え)。
- 境界の規則: サービスは他のサービスの表を直接読まない。読むときは store の関数を通す。これで、後でプロセスを分けても中身を書き換えずに済む。

### 2. 1 つのバイナリか、別々のプロセスか

**まず 1 つのバイナリにまとめ、中をモジュールで分ける(モジュラーモノリス)。** 理由:

- 利用者は 1 人で、手元の 1 台で動く。ネットワーク越しに分けると、起動・認証・版の食い違いの面倒が増えるだけで、得るもの(別々のスケール、別々のデプロイ)がない。
- 配布するのが 1 ファイルで済む(`herdrctl tb ...`、`herdrctl webui`、`herdrctl scheduler tick`)。
- 境界を守っておけば、後で 1 つだけ常駐のサービスに切り出せる。切り出す候補は messaging(送信の保留と再送を常駐で回したい場合)と webui(今も常駐)。

### 3. 言語: Go

| 案 | 利点 | 欠点 |
|---|---|---|
| **Go(推奨)** | 1 回の `GOOS`/`GOARCH` 指定で全 OS 向けを作れる。cgo の要らない SQLite(`modernc.org/sqlite`)がある。標準ライブラリで HTTP サーバー・JSON が足りる。jq も sqlite3 の CLI も要らなくなる | bash より行数は増える。webui のテンプレートを書き直す手間 |
| Rust | 速く、型が強い。herdr も Rust | 全 OS 向けのビルドが Go より手間(リンカ、SQLite の C ライブラリ)。書き直しに時間がかかる |
| Python + PyInstaller | webui と要約の索引はそのまま使える | 起動が遅い、配布物が大きい、OS ごとにその OS の上でビルドが要る。bash の部分はどのみち書き直す |

### 4. 移行の手順(strangler fig)

1. **土台**: リポジトリに `go/`(または `cmd/herdrctl/`)を足し、`herdrctl version` だけのバイナリと release CD を作る。既存のファイルには触らない。
2. **store**: スキーマ(`taskboard/schema/*.sql`)は今の場所を正とし、バイナリに埋め込む(`go:embed`)。版上げ(`PRAGMA user_version`)を Go で行い、bash と同じ DB を読み書きできることを確かめる。
3. **コマンドを 1 つずつ移す**: `tb` の入口で「移し終えたコマンドならバイナリを呼ぶ」ようにする。読み取りのコマンド(`whoami`、`list` など)から始め、書き込みは後。今の `taskboard/test/cases/*.sh` を、`TB_IMPL=bash` と `TB_IMPL=go` の両方で流す。
4. **scheduler**: OS ごとの起動器(launchd の plist、タスク スケジューラ、systemd)をバイナリが作る。ジョブの定義(`jobs/*.conf`)の書き方は変えない。
5. **webui**: Go の `net/http` + `html/template` で書き直し、`herdrctl webui` で起動。今の Python のテストを HTTP 越しの契約テストとして流す。
6. **bash を薄くする**: 全コマンドが移ったら、`tb` は「バイナリを呼ぶだけの shell」になる。shell からも、バイナリ直接からも同じに使える。

- 並行して進んでいる issue(#89〜#113 など)の実装は bash のままでよい。移行は、その機能が落ち着いてから、テストごと移す。移行中に bash 側を書き換える PR とぶつからないよう、1 つの PR では 1 つのサービスだけを移す。

### 5. release CD(GitHub Actions)

- **CI(PR ごと)**: 今の `test.yml` に、Go のビルドとテストのジョブを足す。`ubuntu-latest` / `macos-latest` / `windows-latest` の matrix で `go test ./...` を流す(Windows で動くことを毎回確かめる)。
- **リリース(タグ `v*` を push したとき)**: GoReleaser で 6 つ(darwin・windows・linux × amd64・arm64)をビルドし、GitHub Releases に置く。付けるもの: `checksums.txt`、SBOM、ビルドの来歴の証明(`actions/attest-build-provenance`)。
- **版**: semver。タグは人が打つ(またはリリース用の PR をマージしたら打つ。release-please)。
- **権限**: リリースのワークフローだけが `contents: write` を持つ。ADR 0001 の L0 と同じく、公開の操作は決定論の実行者(このワークフロー)だけが行い、agent には公開の認証を渡さない。
- **配布の窓口**: まず GitHub Releases。後から Homebrew tap(macOS)と winget / Scoop(Windows)を足せる。

## 人が決めること

1. **Windows の対応の範囲**: herdr は Windows でネイティブに動くので、ネイティブ対応(推奨)にするか、WSL 前提にするか。ネイティブにするなら、scheduler の Windows 版(タスク スケジューラ)も作る。
2. **言語**: Go(推奨)か Rust か。
3. **署名**: macOS の公証(Apple Developer Program、年 99 USD)と Windows のコード署名(証明書か Azure Trusted Signing、有料)を最初からするか、まず署名なしで出して後から足すか(推奨: 後から)。署名なしだと、macOS は初回に「開発元を確認できない」警告、Windows は SmartScreen の警告が出る。
4. **バイナリの名前**: `herdrctl`(仮)でよいか。herdr 本体の `herdr` と紛れない名前にする。

## 検討した代替案

| 案 | 利点 | 欠点 |
|---|---|---|
| A. サービスごとに別プロセス・HTTP で通信(本来のマイクロサービス) | 個別に再起動・更新できる | 1 人・1 台では得るものが少ない。起動の順序、ポート、認証、版の食い違いの管理が増える |
| B. bash のまま、Windows は Git Bash / WSL 前提 | 書き直しがない | jq・sqlite3・bash の差(macOS の bash 3.2 など)に引き続き悩む。バイナリ配布にならない |
| C. 一度に全部書き直す | 早く終わるように見える | 並行して進む機能の実装とぶつかる。今のテストの資産を活かしにくい |

## 結果(Consequences)

良い点: 依存(bash・jq・sqlite3・python3)が 1 ファイルになる。Windows で動く。サービスの境界がコードで守られる。リリースが自動になる。

悪い点: 移行の間は bash と Go の 2 つの実装を持つ。webui の書き直しの手間。Go を読める必要がある。

## 未解決の問い

- `herdr pane send` などの CLI 操作が Windows で同じに動くか(未確認)。土台の段階で、Windows の CI か実機で確かめる。
- Windows のパスと権限(`0700` / `0600` 相当)をどう表すか(ACL)。
- 設定ファイル(`*.conf`)の置き場所を、OS ごとの標準(`%APPDATA%` など)に合わせるか。
