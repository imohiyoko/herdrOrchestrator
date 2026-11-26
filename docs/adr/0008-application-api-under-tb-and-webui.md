# ADR 0008: tb と Web UI の下にアプリケーション API の層を置き、入口を薄くする

- 状態: Proposed
- 日付: 2026-11-26
- 関係するもの: ADR 0002(サービスの境界)、ADR 0004(3 つの層)、ADR 0007(主体と 1 つの policy)

## 要旨

- **困りごと**: 依存の向きは正しい(`internal/tbcli` を import するのは `cmd/herdrctl` だけ)。しかし tb の下に、Go から型のある形で呼べる「アプリケーション API」が無い。業務の中身(agent の起動・停止・spawn、チケットの書き込み、承認待ち)は `internal/tbcli` の CLI の関数の中にあり、引数は `[]string`、結果は `io.Writer` への文字列と終了コード。そのため:
  1. Web UI は書き込みのたびに `tb` を別プロセスで起動し、出力の文字列と終了コードだけを読む(`runTB` 21 か所)。
  2. Web UI は読み込みで DB を直接読む(`SELECT` 85 か所)。tb と Web UI が同じ表の構造に別々に依存している。
  3. Web UI は policy の照合と役割の解決を自前で書き直している(`webui/perms.go`、`webui/roles.go`。コメントに「tb の … と同じ」)。
  4. これから作る入口(agent セット、scheduler からの自動スケール、将来の HTTP API)も、同じく tb を別プロセスで呼ぶしかない。
- **決定(案)**:
  1. **入口 / アプリケーション API / ドメイン・土台** の 3 段にする。入口(tb の CLI、Web UI、scheduler、agent セット)は引数・画面・時刻を読んで API を呼び、結果を表示するだけにする。
  2. API(`internal/app/<サービス>`)は **型のある要求と結果** を受け渡し、**主体を引数で受け取り**、権限の判定・承認待ち・上限の検査・イベントの記録を必ず通す。出力・終了コード・環境変数(`HERDR_PANE_ID` など)は扱わない。
  3. Web UI は API を同じプロセスで呼ぶ(`runTB` をなくす)。読み込みも API の読み取り関数を通す。policy と役割の自前の実装は API の関数に置き換える。
  4. tb のコマンド・引数・出力・終了コードは変えない(既存のテストがそのまま通ることを各段の条件にする)。
  5. 操作は [imohiyoko/API](https://github.com/imohiyoko/API) の `define.Query` / `define.Command` で定義し、認証・認可は herdr のアダプタ(ADR 0007 の主体と policy)を差し込む。imohiyoko/API にはプロセス内の呼び出しと依存の分割を足し、主体の確かめと承認待ちは herdr のアダプタとハンドラーで扱う(決定 2a)。
- **段階**: 段 0(雛形と依存の検査)→ 段 1(agent)→ 段 2(チケット)→ 段 3(job・recipe・承認)→ 段 4(残り)→ 段 5(許容リストを空にして固定)。agent セットは段 1 のあとに、API を呼ぶ入口として作る。

## 状況(Context)

人の指摘(2026-11-26): agent セットは本筋のツール(SDK)の上に乗るものなので、密に繋がりすぎないほうがよい。そもそも tb コマンドとアプリケーション API はちゃんと疎結合になっているのか。

agent セットの設計案では、セットは「tb の公開コマンドを順に呼ぶ外側の層」とした。調べると、tb の外から呼べる公開の形はコマンド(別プロセス)しか無かった。

## 分析(main 2a83930、2026-11-26)

数え方: `go list` の import と、`_test.go` を除いたファイルの grep。数は目安。

### 1. 依存の向き(できている)

| パッケージ | import している内部パッケージ |
|---|---|
| `cmd/herdrctl` | tbcli, webui, sched, kernel/env, kernel/store, version |
| `internal/tbcli` | ほぼ全部(activity, agents, approval, conv, gate, gh, jobs, limits, message, notify, pr, proposal, proxy, recipe, reminder, review, tickets, kernel/*) |
| `internal/webui` | kernel/env, kernel/herdr, kernel/store, sched だけ |
| ドメインのパッケージ | kernel/* と、互いの一部(message → approval → notify など)。tbcli・webui は import しない |

入口(tbcli・webui)がドメインを使い、ドメインは入口を知らない。この向きは保つ。

### 2. tbcli が入口と業務を兼ねている

| 項目 | 値 |
|---|---|
| 行数 / ファイル数 | 18,052 行 / 50 ファイル |
| 表への書き込み(INSERT / UPDATE / DELETE) | 118 か所、26 ファイル |
| 環境変数の読み込み(`os.Getenv` / `envOr`) | 87 か所 |
| 主体を関数の中で決める(`requester()`) | 18 か所 |
| DB を開く CLI の入口(`openCmd`) | 79 か所 |

- 例: `doAgentNew`(agentlife.go)は役割の検査、herdr の起動、台帳の更新、最初の指示を 1 つの関数で行い、結果を `opResult{code, out string}` で返す。`cmdAgentSpawn`(spawn.go)は主体を `requester()` で環境変数から読み、検査の結果を文言で返す。
- どちらも業務としては再利用したいが、Go から呼ぶには `[]string` を組み立てて文字列を読むしかない。
- ドメインのパッケージにも CLI の形が残っている: `pr.Q.Add(args []string)` など(pr/cmd.go)。

### 3. Web UI の書き込みは別プロセスの tb

| ファイル | `runTB` / `runTBInput` |
|---|---|
| edits.go | 9 |
| jobs.go | 4 |
| agentspage.go | 3 |
| inbox.go | 3 |
| approvals.go | 1 |
| jobspage.go | 1 |

- 1 回ごとに bash と herdrctl を起動する。時間切れは 15 秒(spawn は別の値)。
- 結果は終了コードと出力の文字列だけ。失敗の理由を画面で分けるには文言を読むしかない。
- 良い点: 書き込みが必ず tb の門(ADR 0006・0007)を通る。この性質は API に移しても保つ(決定 2)。

### 4. Web UI の読み込みは DB の直読み

| ファイル | `SELECT` |
|---|---|
| tasks.go | 18 |
| notify.go | 14 |
| approvals.go | 10 |
| docs.go | 8 |
| docspage.go | 6 |
| agents.go / data.go | 5 / 5 |
| ほか 12 ファイル | 1〜4 |

表では tasks 22、comments 8、config 6、agent_ops 6 など。ADR 0002・0004 の「SQL を書くのは store(その表を持つ機能)だけ」に反している。

### 5. 同じ規則を 2 か所で書いている

| Web UI 側 | tb 側 | 内容 |
|---|---|---|
| webui/perms.go | kernel/perms、approval | policy の読み取りと照合(「tb の perm_authorize と同じ考え」) |
| webui/roles.go | agents.Roles | 役割の解決(「tb の role_files_for / name_matches と同じ」) |
| webui/agents.go の edges | tbcli の agent_edges | agent の関係の組み立て |

片方だけ直すと、画面の表示と実際の判定がずれる。

## 決定(案)

### 1. 3 段

```
┌────────────────────────────────────────────────────────────────┐
│ 入口(adapters)                                              │
│  tb の CLI(internal/tbcli) / Web UI(internal/webui)        │
│  scheduler(internal/sched) / agent セット / 将来の HTTP API  │
│  - 引数・フォーム・時刻を読む → 主体を確かめる → API を呼ぶ   │
│  - 結果を文言・JSON・画面・終了コードにする                    │
├────────────────────────────────────────────────────────────────┤
│ アプリケーション API(internal/app/<サービス>)               │
│  agent / ticket / job / recipe / approval / notify / ...       │
│  - 型のある要求と結果、context、主体は引数                     │
│  - 権限の判定・承認待ち・上限・イベントの記録を必ず通す        │
├────────────────────────────────────────────────────────────────┤
│ ドメインと土台(ADR 0004 のチケットの通り道と土台)           │
│  tickets / agents / approval / limits / ... / kernel/*         │
└────────────────────────────────────────────────────────────────┘
```

- サービスの分け方は tb のサービス名(`tb <サービス> <動詞>`)に合わせる。`tb agent new` → `app/agent.New`。
- 入口は API だけを呼ぶ。ドメインや store を直接呼ばない(読み込みも)。
- API は入口を import しない。API どうしは呼んでよいが、輪にしない。

### 2. API の決まり

```go
// 例(名前は段 0 で決める)
package agent

type NewReq struct {
    Name, Cwd, Kind, Model string
    Roles    []string
    Prompt   string
    Killable Tri
    Pane     PanePlacement // 段 1 の後で: 既存の workspace に置く(agent セット用)
}

type NewResult struct {
    Name, Pane, Workspace string
    Pending *app.PendingID // 承認待ちに積んだときだけ(L-<n> / Q-<n>)
}

func New(ctx context.Context, by app.Principal, r NewReq) (NewResult, error)
```

- **主体は引数**: `app.Principal`(ADR 0007 の `human` / `agent:<名前>` / `system:<名前>` / `unknown`)。確かめるのは入口の責任: tb は今の `kernel/identity`(プロセスと pane)、Web UI はログイン、scheduler は `system:sched`。API の中で環境変数から主体を決めない。
- **門は API の側**: ADR 0006・0007 の権限段階と policy の判定は、API の関数の最初で必ず通す。API の門は主体を `app.Principal` の引数で受け取り、pane や `perms.HumanPane()` から決めない。今の `perms.Gate` / `perms.EnforceTarget` は、pane が無く `identity.System` も空のプロセスを人として扱い policy を見ずに通す(perms.go の `HumanPane`)。そのまま API から呼ぶと、pane の無い Web UI のプロセスが agent を主体として呼んだときに agent の policy が効かない。そのため段 0 で、主体を引数で受け取る判定の関数(例: `perms.GateFor(by Principal, op, target)`)を足し、API はそれだけを呼ぶ。tb の入口は、今の `Gate` を「pane から主体を決めて `GateFor` を呼ぶ」薄い関数にする。Web UI が API を直接呼んでも、判定を迂回しない。
- **出力しない**: `io.Writer`・終了コード・`os.Exit` を使わない。エラーは型(`app.Error{Code, Msg, Hint}`)。tb の入口が `Code` を今の終了コードに、`Msg` を今の文言に写す(文言は今と同じにする)。
- **承認待ちは結果の 1 つ**: 「承認待ちに積んだ」は失敗ではなく `Pending` を持つ結果として返す。`Pending` は経路(agent の操作の待ち `L-<n>` か、コマンドの入口の `ask` の `Q-<n>` か)と番号を持ち、tb の入口は経路ごとに今の文言を再現する: agent の操作は今どおり終了コード 20 と `L-<n> pending …`、コマンドの入口の `ask` は今どおり `Q-<n>` を含む説明。2 つを共通の文言にまとめない。
- **環境は注入**: DB、セッション、herdr のアダプタ、時計は `app.Env`(または各サービスの構造体)で渡す。テストでは疑似の herdr と一時 DB を入れる。
- **読み取りも API**: 一覧・詳細は `app/<サービス>` の読み取り関数(`ticket.List(ctx, by, Filter)` など)。画面向けの集計(Web UI の一覧の JOIN など)も、その表を持つサービスの読み取り関数に置く。

### 2a. 操作の定義は imohiyoko/API で書く

人の提案(2026-11-26): API まわりは [imohiyoko/API](https://github.com/imohiyoko/API) を使うときれいに書けるはず。

imohiyoko/API は「操作を 1 回定義すると、公開範囲(internal / partner / public)とプロトコル(REST / gRPC など)ごとの口が作られる」ライブラリ(設計段階。M1・M2 の大半ができている。2026-11-26 の main で確認)。操作は `define.Query` / `define.Command` で、型のある入力と出力、認可の宣言(`define.Authz`)、公開の許可(`define.Expose`)を 1 か所に持つ。認証と認可は `port.Authenticator` / `port.Authorizer` に差し替えられる。上の 2 の決まり(型のある要求と結果、主体は文脈、門は操作の側、出力しない、エラーは型)とほぼ同じ形なので、`internal/app/<サービス>` の操作はこれで書く。

```go
// internal/app/agent
var New = define.Command[NewReq, NewResult]("agent.new",
    define.Doc("agent を作って起動する"),
    define.Authz("agent:new", define.Resource("agent", "Name")), // policy の操作名と対象
    define.Expose(define.Internal, define.REST),                 // Web UI・agent セット向け(127.0.0.1)
).Handle(func(ctx context.Context, in NewReq) (NewResult, error) {
    by := define.PrincipalFrom(ctx) // 主体は文脈から(環境変数から決めない)
    ...
})
```

| imohiyoko/API の部品 | herdr での中身 |
|---|---|
| 操作(`define.Query` / `Command`) | `app/<サービス>` の各操作。名前は `<サービス>.<動詞>`(tb のサービス名に合わせる) |
| `define.Authz("<操作>", Resource(...))` | 設定 policy の操作名と対象(ADR 0007)。`agent:new:<名前>` など |
| `port.Authorizer` | herdr のアダプタ: `perms` / `approval` の判定(主体を引数で受け取る `GateFor`。上の 2)。`ask` は承認待ちに積んだ結果として返す |
| `port.Authenticator` | herdr のアダプタ: Web UI のログインのセッション → `human`、agent の pane → `agent:<名前>`(ADR 0007 の確かめ方)、scheduler → `system:<名前>` |
| 公開範囲 | 当面は internal だけ(127.0.0.1 の Web UI・agent セット)。外へ出す口は作らない |
| `define.Errorf(Code…)` | `app.Error` の代わり。tb の入口がコードを今の終了コードと文言に写す |
| マニフェスト・OpenAPI の生成 | Web UI・agent セットの契約として生成し、CI で差分を見る |

### 3. Web UI

- `runTB` / `runTBInput` を API の呼び出しに置き換える。主体はログインした人(`human`)。今 `HERDR_PANE_ID` を外して人として呼んでいるのと同じ意味になる。
- `SELECT` を API の読み取り関数に置き換える。
- `webui/perms.go` と `webui/roles.go` の照合は、API(`app/approval.Explain` など)を呼ぶ形にする。画面の説明と実際の判定が同じ関数になる。

### 4. 依存の検査(CI)

ADR 0004 の依存の検査に次を足す。今ある違反は許容リストに入れ、減る方向にだけ動かす。

- `internal/tbcli`・`internal/webui`・入口のパッケージは、`internal/app/...` と `kernel/env`・`kernel/identity`(主体の確かめ)以外の内部パッケージを import しない。
- `internal/app/...` は `internal/tbcli`・`internal/webui` を import しない。
- `internal/tbcli`・`internal/webui` のファイルに SQL(`SELECT` / `INSERT` / `UPDATE` / `DELETE`)を書かない。

### 5. 互換

- tb のコマンド名・引数・出力・終了コードは変えない。taskboard のテスト(`taskboard/test/cases`)と Go のテストが、各段の PR でそのまま通ることを条件にする。
- Web UI の画面と URL は変えない。

## 段階

各段は 1〜数 PR。どの段も、入れた時点の振る舞いは今と同じ。

| 段 | 中身 | 主に動かすもの | 終わりの条件 |
|---|---|---|---|
| 0 | 雛形: imohiyoko/API への依存と herdr のアダプタ(Authenticator・Authorizer)、`internal/app`(Env・Pending)、依存の検査と許容リスト。imohiyoko/API にプロセス内の呼び出しを足す(2a) | 新規 + CI + imohiyoko/API | 検査が CI で動き、今の違反が許容リストに載る。1 つの操作を tb と Web UI の両方から呼べる |
| 1 | agent: 起動・停止・再開・再起動・spawn・承認・却下・一覧、親の付け替え(新規) | agentlife.go、spawn.go、agentapprove.go、agentlist.go → `app/agent`。Web UI の agentspage.go(`runTB` 3)、agents.go の読み込み | tbcli の agent の動詞が引数の読み取りと表示だけになる |
| 2 | ticket: 作成・編集・状態・コメント・リンク・削除・読み取り | ticket*.go → `app/ticket`。Web UI の edits.go(`runTB` 9)、tasks.go(`SELECT` 18) | 同上 |
| 3 | job・recipe・承認・policy の説明 | job.go、recipe*.go、approval.go、policy.go。Web UI の jobs.go・jobspage.go・approvals.go・perms.go・roles.go | Web UI の自前の照合が無くなる |
| 4 | 残り: notify、conv、pr、gate、proxy、digest、reminder など。`pr.Q` の `[]string` の形を要求の型にする | 各ファイル、Web UI の残りの読み込み | Web UI の `runTB` と `SELECT` が 0 |
| 5 | 許容リストを空にし、検査を固定する | CI | 許容リストが空 |

### agent セットとの関係

- agent セット(`internal/agentset`、`tb agentset`)は段 1 のあとに作る。セットは `app/agent` を呼ぶ入口の 1 つで、tb を別プロセスで呼ばない。
- 設計案の「公開コマンドを呼ぶ」は「公開の API を呼ぶ」に読み替える。本体に足す部品(既存の workspace に pane で置く起動、親の付け替え)は段 1 で `app/agent` に入れる。

## 検討した代替案

| 案 | 採らない理由 |
|---|---|
| tb に `--json` の出力を足し、Web UI とセットはそれを読む | 別プロセスのまま。型にならず、JSON の形が第 2 の公開の形になる。tb の一部のコマンドには既に `--json` があるので、それは人とスクリプト向けに残す |
| tb を常駐の HTTP / JSON のサーバーにし、Web UI とセットはそれを呼ぶ | 常駐が増える(ADR 0002 は 1 バイナリを選んだ)。API の層があれば、後から HTTP を薄くかぶせられる |
| Web UI を tbcli の関数を直接呼ぶ形にする | CLI の形(`[]string`、文字列の出力)に Web UI が縛られる。tbcli が両方の入口の事情を持つ |
| 何もしない | 規則の二重の実装と、表の構造への二重の依存が残る。agent セットも別プロセスで作ることになる |

## 結果(Consequences)

- 良い: Web UI・scheduler・agent セットが同じ関数で同じ判定を受ける。画面に型のある失敗の理由を出せる。表の構造を変えるときに直す所が API の中だけになる。tbcli は引数の読み取りと表示だけになり、テストを API の単位で書ける。
- 悪い: 移す量が大きい(tbcli 18,000 行、Web UI の読み込み 85 か所)。段の途中は、移した動詞と移していない動詞が混ざる。API の型が増え、名前の決まりを保つ手間がかかる。
- 中立: tb の使い方と出力は変わらないので、人と agent には見えない。

## 人が決めること

- imohiyoko/API にプロセス内の呼び出しと依存の分割を足してよいか(案: 足す。どちらもほかの利用者にも役に立つ)。
- パッケージの名前: `internal/app/<サービス>`(案)か `internal/service/<サービス>` か。
- Web UI の画面向けの集計(一覧の JOIN など)も API に置くか、`app/<サービス>/query` のように読み取り専用の場所を分けるか(案: 分ける。書き込みの関数と同じパッケージの別ファイル)。
- 段 1 を agent セットの段 1・2(親の付け替え、pane に置く起動)と一緒に進めてよいか(案: 一緒に。同じファイルを触るので)。

## 確認できていないこと・推測

- `taskboard/test/cases` の tb のテストは出力の文言まで比べているので、文言を写す表を作れば互換を保てる、と推測している。段 0 で数個の動詞を移して確かめる。
- Web UI の `runTB` の時間切れ(15 秒)に頼っている処理があるか(長い spawn など)は未確認。同じプロセスで呼ぶときは context の期限で同じ時間を与える。
