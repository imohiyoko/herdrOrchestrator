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
  2. API(`internal/app/<サービス>`)は **型のある要求と結果** を受け渡し、**入口が確かめた主体だけを使い**、権限の判定・承認待ち・上限の検査・イベントの記録を必ず通す。出力・終了コード・環境変数(`HERDR_PANE_ID` など)は扱わない。
  3. Web UI は API を同じプロセスで呼ぶ(`runTB` をなくす)。読み込みも API の読み取り関数を通す。policy と役割の自前の実装は API の関数に置き換える。
  4. tb のコマンド・引数・出力・終了コードは変えない(既存のテストがそのまま通ることを各段の条件にする)。
  5. 操作は [imohiyoko/API](https://github.com/imohiyoko/API) の `define.Query` / `define.Command` で定義し、認証・認可は herdr のアダプタ(ADR 0007 の主体と policy)を差し込む。入口は imohiyoko/API のプロセス内の口(`Server.Invoke`)で呼び、主体の確かめと承認待ちは herdr のアダプタとハンドラーで扱う(決定 2a)。
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
│  - 型のある要求と結果、context、主体は入口が確かめて渡す       │
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
// 例(名前は段 0 で決める。定義の書き方は 2a の imohiyoko/API の形)
package agent

// フィールドの名前と api タグは段 0 で決める。API に出すフィールドには一意な api タグを付ける
type NewReq struct {
    Name     string        `api:"name"` // define.Resource("agent", "name") が指す
    Cwd      string        `api:"cwd,optional"`
    Kind     string        `api:"kind,optional"`
    Model    string        `api:"model,optional"`
    Roles    []string      `api:"roles,optional"`
    Prompt   string        `api:"prompt,optional"`
    Killable string        `api:"killable,optional"` // yes / no / unknown
    Pane     *PanePlacement `api:"pane,optional"`    // 段 1 の後で: 既存の workspace に置く(agent セット用)
}

type NewResult struct {
    Name      string   `api:"name"`
    Pane      string   `api:"pane,optional"`
    Workspace string   `api:"workspace,optional"`
    Pending   *Pending `api:"pending,optional"` // 承認待ちに積んだときだけ(L-<n> / Q-<n>)
}

// 操作は 2a の define.Command で定義する。主体は ctx(define.PrincipalFrom)から読む
```

- **主体は入口が確かめて渡す**: 主体は ADR 0007 の `human` / `agent:<名前>` / `system:<名前>` / `unknown`。確かめるのは入口の責任: tb は今の `kernel/identity`(プロセスと pane)、Web UI はログイン、scheduler は `system:sched`。入口は確かめた結果を `Invoke` のトークンとして渡し、herdr の `port.Authenticator` が主体に戻す(2a)。API の中では `define.PrincipalFrom(ctx)` で読み、環境変数から主体を決めない。
- **門は API の側**: ADR 0006・0007 の権限段階と policy の判定は、API の関数の最初で必ず通す。API の門(herdr の `port.Authorizer`)は、`port.CheckRequest` で渡される主体だけで判定し、pane や `perms.HumanPane()` から決めない。今の `perms.Gate` / `perms.EnforceTarget` は、pane が無く `identity.System` も空のプロセスを人として扱い policy を見ずに通す(perms.go の `HumanPane`)。そのまま API から呼ぶと、pane の無い Web UI のプロセスが agent を主体として呼んだときに agent の policy が効かない。そのため段 0 で、主体を引数で受け取る判定の関数(例: `perms.GateFor(by Principal, op, target)`)を足し、API はそれだけを呼ぶ。tb の入口は、今の `Gate` を「pane から主体を決めて `GateFor` を呼ぶ」薄い関数にする。Web UI が API を直接呼んでも、判定を迂回しない。
- **出力しない**: `io.Writer`・終了コード・`os.Exit` を使わない。エラーは型(`define.Errorf` の `*define.Error`。2a)。tb の入口が `Code` を今の終了コードに、`Message` を今の文言に写す(文言は今と同じにする)。
- **判定は 2 つの層で、`ask` はハンドラーの手前の門で扱う**: imohiyoko/API の認可の結果(`port.Decision`)は `Allowed` と拒否の理由(`Reasons`)だけで、`ask` を表せない。そこで判定を次の 2 つに分け、どちらも API の中で必ず通す。
    1. **入口の段(`cmd:<サービス>:<動詞>`)は `port.Authorizer`**: 各操作の `define.Authz` の操作名を、今 `taskboard/perms/commands.tbl` で入口(`entry`)の判定に使っている `cmd:` の名前にする(`agent.new` → `cmd:agent:new`)。herdr の Authorizer は policy の効果が `deny` なら拒否、`allow` と `ask` なら通す。これで Web UI など tb の入口(`perms.Gate`)を通らない呼び出しでも `cmd:` の行が効く。tb の入口の `Gate` は移した動詞では二重になるが、結果は同じなので、段 5 で入口からは外す。
    2. **操作の中の判定(`agent:new:<名前>` など)と `ask` はハンドラーの手前の門**: herdr の操作はすべて `app.Guard(判定の作り方, ハンドラー)` で包んで登録する。門は主体(`define.PrincipalFrom`)と入力から操作名と対象を作り、policy を 3 つの効果で判定する: `deny` は `*define.Error`(`permission_denied`)、`ask` はハンドラーを呼ばずに承認待ちに積み `Pending` を持つ結果を返す、`allow` だけがハンドラーを呼ぶ。入口の段(1)の `ask` も、この門が同じく承認待ちにする(Authorizer は `ask` を通すだけで、実行させない)。
    - `Guard` を使わずに登録した操作が無いことを、レジストリを走査するテストで CI に検査させる。
    - 段 0 で決めた分け方: commands.tbl の場所が `entry` の行は Authorizer が判定し(`deny` だけ拒否)、`inner` の行(今 `require_human` で確かめている `cmd:ticket:resolve` など)は Authorizer では通して `Guard` が判定する。止めたときの文言・記録(Q-<n>)・承認待ちを tb と同じにするため。
- **承認待ちは結果の 1 つ**: 「承認待ちに積んだ」は失敗ではなく `Pending` を持つ結果として返す。`Pending` は経路(agent の操作の待ち `L-<n>` か、コマンドの入口の `ask` の `Q-<n>` か)と番号を持ち、tb の入口は経路ごとに今の文言を再現する: agent の操作は今どおり終了コード 20 と `L-<n> pending …`、コマンドの入口の `ask` は今どおり `Q-<n>` を含む説明。2 つを共通の文言にまとめない。
- **環境は注入**: DB、セッション、herdr のアダプタ、時計は `app.Env`(または各サービスの構造体)で渡す。テストでは疑似の herdr と一時 DB を入れる。
- **読み取りも API**: 一覧・詳細は `app/<サービス>` の読み取り関数(`ticket.List(ctx, by, Filter)` など)。画面向けの集計(Web UI の一覧の JOIN など)も、その表を持つサービスの読み取り関数に置く。

### 2a. 操作の定義は imohiyoko/API で書く

人の提案(2026-11-26): API まわりは [imohiyoko/API](https://github.com/imohiyoko/API) を使うときれいに書けるはず。

imohiyoko/API は「操作を 1 回定義すると、公開範囲(internal / partner / public)とプロトコル(REST / gRPC など)ごとの口が作られる」ライブラリ(設計段階。M1〜M3.5 ができている。2026-11-26 の main dbb6305 で確認)。操作は `define.Query` / `define.Command` で、型のある入力と出力、認可の宣言(`define.Authz`)、公開の許可(`define.Expose`)を 1 か所に持つ。認証と認可は `port.Authenticator` / `port.Authorizer` に差し替えられる。口は REST(`Server.Handler`)、gRPC(`runtime/grpcx`)、プロセス内の口(`Server.Invoke`)で、どれも同じ共通の層(認証 → 主体の確認 → レートリミット → 入力 → 認可 → 実行 → 出力の絞り込み)を通る(imohiyoko/API の ADR 0007)。上の 2 の決まり(型のある要求と結果、主体は入口が確かめて文脈で渡す、門は操作の側、出力しない、エラーは型)と同じ形なので、`internal/app/<サービス>` の操作はこれで書く。

```go
// internal/app/agent
var New = define.Command[NewReq, NewResult]("agent.new",
    define.Doc("agent を作って起動する"),
    define.Authz("cmd:agent:new", define.Resource("agent", "name")), // 入口の段(commands.tbl の cmd: の名前)
    define.Expose(define.Internal, define.REST),                 // internal に出す。Invoke も Expose した公開範囲でだけ呼べる
).Handle(app.Guard(agentNewOp, func(ctx context.Context, in NewReq) (NewResult, error) {
    by := define.PrincipalFrom(ctx) // 主体は文脈から(環境変数から決めない)
    ... // ここに来るのは agent:new:<名前> が allow のときだけ。ask なら Guard が承認待ちにして返す
}))

// 入口(tb・Web UI・agent セット)。どれも同じ流れを通る
out, err := srv.Invoke(ctx, define.Internal, "agent.new", token, agent.NewReq{...})
// out は公開範囲で絞り込んだ値(map[string]any)。入口は NewResult に読み直して表示する
```

| imohiyoko/API の部品 | herdr での中身 |
|---|---|
| 操作(`define.Query` / `Command`) | `app/<サービス>` の各操作。名前は `<サービス>.<動詞>`(tb のサービス名に合わせる) |
| `define.Authz("<操作>", Resource(...))` | 入口の段の操作名(`cmd:agent:new`)。操作の中の判定(`agent:new:<名前>`)と `ask` は `app.Guard`(上の 2) |
| `port.Authorizer` | herdr のアダプタ: 入口の段 `cmd:<サービス>:<動詞>` を policy で判定する(主体は `port.CheckRequest` のものだけ。上の 2)。`deny` だけ拒否し、`allow` と `ask` は通す |
| `port.Authenticator` | herdr のアダプタ: Web UI のログインのセッション → `human`、agent の pane → `agent:<名前>`(ADR 0007 の確かめ方)、scheduler → `system:<名前>` |
| 公開範囲 | 当面は internal だけ(127.0.0.1 の Web UI・agent セット)。外へ出す口は作らない |
| `define.Errorf(Code…)` | 操作のエラー。tb の入口がコードを今の終了コードと文言に写す |
| マニフェスト・OpenAPI の生成 | Web UI・agent セットの契約として生成し、CI で差分を見る |

#### imohiyoko/API との分担(2026-11-26 の main dbb6305 で確認)

| 足りなかったもの | どこで解いたか / 解くか | 中身 |
|---|---|---|
| **プロセス内の呼び出し** | **imohiyoko/API で入った**([#11](https://github.com/imohiyoko/API/issues/11)、imohiyoko/API の ADR 0007、M3.5) | `Server.Invoke(ctx, 公開範囲, 操作の名前, トークン, 入力)`。公開範囲は必ず渡し、`Expose` していない公開範囲では `not_found`。入力は In 型の値か JSON。出力は公開範囲で絞り込んだ値(`map[string]any`)、エラーは開示のルールを通した `*define.Error`。tb の CLI・Web UI・agent セットはこれで呼ぶ。HTTP の口は立てない |
| **依存の分割** | **imohiyoko/API で入った**([#12](https://github.com/imohiyoko/API/issues/12)) | gRPC は `runtime/grpcx`、YAML は `manifest/yamlx` に分かれた。herdrctl は `define`・`manifest`・`runtime`・`port` だけを import し、grpc・protobuf・YAML を含めない。`cli`(契約の生成)は grpc・protobuf を含むので、herdrctl からは import せず、契約の生成は別の小さな main(`cmd/apigen` など)で行う |
| **主体はトークンからしか作れない**: herdr の主体は呼び出したプロセスと pane で決まる(ADR 0007) | **herdr のアダプタ** | `port.Authenticator` の実装が、`Invoke` に渡す「トークン」を主体に変える。tb は呼び出し元の確かめ(kernel/identity)の結果を短命のトークンにして渡し、Web UI はログインのセッション、scheduler は `system:<名前>` の印を渡す。imohiyoko/API は確かめ済みの主体を渡す口を作らない(imohiyoko/API の ADR 0007 の代替案)ので、確かめは必ずアダプタを通る |
| **承認待ち(`ask`)の形が無い** | **herdr の中** | 認可(`port.Authorizer`)は許可・拒否のまま(`ask` は通す)。`ask` は `app.Guard` がハンドラーを呼ばずに承認待ちに積み、`Pending`(`L-<n>` / `Q-<n>`)を結果として返す(上の 2) |

段 0 で確かめること:

- `Invoke` の出力は型ではなく `map[string]any` なので、tb の入口が `NewResult` などに読み直す小さな関数(JSON を通す)を置く。手間が大きければ、型のある結果を返す `Invoke` の総称版を imohiyoko/API に提案する。
- 匿名の呼び出しのレートリミットは、プロセス内の呼び出しをまとめて数える(`runtime.InvokeClient`)。herdr の操作は匿名にしない(`define.Anonymous` を使わない)ので影響しない。
- `go list -deps ./cmd/herdrctl` に grpc・protobuf・yaml が出ないことを CI で検査する。

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
| 0 | 雛形: imohiyoko/API への依存と herdr のアダプタ(Authenticator・Authorizer)、`internal/app`(Env・Pending)、依存の検査と許容リスト。imohiyoko/API の `Invoke` で 1 つの操作を呼ぶ(2a) | 新規 + CI | 検査が CI で動き、今の違反が許容リストに載る。1 つの操作を tb と Web UI の両方から呼べる |
| 1 | agent: 起動・停止・再開・再起動・spawn・承認・却下・一覧、親の付け替え(新規) | agentlife.go、spawn.go、agentapprove.go、agentlist.go → `app/agent`。Web UI の agentspage.go(`runTB` 3)、agents.go の読み込み | tbcli の agent の動詞が引数の読み取りと表示だけになる |
| 2 | ticket: 作成・編集・状態・コメント・リンク・削除・読み取り | ticket*.go → `app/ticket`。Web UI の edits.go(`runTB` 9)、tasks.go(`SELECT` 18) | 同上 |
| 3 | job・recipe・承認・policy の説明 | job.go、recipe*.go、approval.go、policy.go。Web UI の jobs.go・jobspage.go・approvals.go・perms.go・roles.go | Web UI の自前の照合が無くなる |
| 4 | 残り: notify、conv、pr、gate、proxy、digest、reminder など。`pr.Q` の `[]string` の形を要求の型にする | 各ファイル、Web UI の残りの読み込み | Web UI の `runTB` と `SELECT` が 0 |
| 5 | 許容リストを空にし、検査を固定する | CI | 許容リストが空 |

### agent セットとの関係

- agent セット(`internal/agentset`、`tb agentset`)は段 1 のあとに作る。セットは `app/agent` を呼ぶ入口の 1 つで、tb を別プロセスで呼ばない。
- 設計案の「公開コマンドを呼ぶ」は「公開の API を呼ぶ」に読み替える。本体に足す部品(既存の workspace に pane で置く起動、親の付け替え)は段 1 で `app/agent` に入れる。

## 段 0 で入れたもの(2026-11-27)

- `internal/app`: 主体の写し(`human` → user / `human`、agent → service_account / `agent:<名前>`、system → service_account / `system:<名前>`)、プロセス内の短命のトークン(`Tokens`。Authenticator)、`Authorizer`、`Guard`(`app.Command` / `app.Query` だけが操作を作り、`app.Registry` は Guard を通さない操作を断る)、`Pending`。
- `internal/kernel/perms`: 主体を引数で受け取る `TierFor` と `EnforceAs`(pane や `identity.System` を見ない)。今の `EnforceTarget` は pane から主体を決めて同じ中身を呼ぶ。
- 最初の操作 `ticket.resolve`(`internal/app/ticket`)。`tb resolve` は引数を読み、呼び出し元を主体にして `Invoke` で呼び、結果を今の文言と終了コードにする。Web UI の「対応済みにする」は `runTB` をやめ、同じ操作を同じプロセスで人として呼ぶ。
- 依存と層の検査(`cmd/herdrctl/layers_test.go`): herdrctl の依存に grpc・protobuf・YAML・imohiyoko/API の `cli` が無いこと、入口(tbcli・webui)の内部 import と SQL の数を許容リスト(`cmd/herdrctl/testdata/layers-allow.txt`)と比べる。増えても減っても落ち、減ったら `-update` で許容リストを減らす。

## 段 1 で入れたもの(2026-11-27)

- `internal/app/agent`: `agent.stop`(#280)、`agent.revive` / `agent.restart`(#282)、`agent.new`(#283)。門は policy の `agent:<操作>:<名前>` で、ask は `agent_ops` に積んで `L-<n>` を返す(`opGate`)。作成は、まだ台帳に無い agent を要求された役割で `@<役割>` と比べる(`opGateRole`)。tb の該当の動詞は引数を読んで操作を呼び、結果を今の文言と終了コードにする。Web UI の停止・復活・作成は、同じセッションなら同じプロセスで呼ぶ(ほかのセッションは herdr の呼び出し先がプロセスの環境で決まるので、まだ tb の子プロセス)。
- herdr の呼び出しに上限(`HERDR_CALL_TIMEOUT_SEC`、既定 60 秒)と、要求の期限(`herdr.CallContext`)を通した。期限を過ぎたら pane を閉じない・起動しない。
- agent セット向けに、`agent.new` に親(`Parent`。台帳の `spawned_by`)と置き場所(`Pane`: 既存の pane を分ける `Split` + `Direction`、か名前を付けた新しい workspace `Workspace`)を足した。どちらも人だけが指定できる(agent からの依頼は承認待ちを通るが、承認後の実行は置き場所を持たないため)。結果に起動した pane と workspace を返す。分けるのは `herdr pane split <pane> --direction right|down --cwd <dir> --no-focus`(herdr の SKILL.md で確かめた形)。
- 親は「立てた親」1 つだけを作成時に入れる。親を足す・外す(多対多。別スレッドで設計中)は、`spawned_by` を立てた親として残す形なので、ここと両立する。

### 段 1 の続き(2026-11-27)

- 規則つきの子の作成 `agent.spawn`(#293): spawn-policy の読み取り・規則の検査・名前・最初の指示・承認待ちへの積み方を `app/agent/spawn.go` に移した。承認待ちを指揮官に知らせる報告の配達は、Env の新しいフック `Deliver`(tb の入口が配達の関数を入れる)。フックの無い入口では知らせが届かないので、Web UI から spawn を呼ぶ段で配達を `app` の下に移す。
- 承認・却下 `agent.approve` / `agent.reject`(#295): 承認者の確かめ(人か、policy の approve:agent:… を許された依頼者以外の agent)、再検査、1 回だけ実行する印、実行と記録、Q-<n> の記録、依頼した agent への知らせ(`transport.Send`)まで API がする。`--always`(policy を書く)は、まだ tb の中。段 3 で policy の書き込みと一緒に移す。
- スラッシュコマンドとモデルの切り替えの承認も `agent.approve` でする(送る部品 `SlashLookup` / `SlashCtx` / `SlashApproved` と `ModelSetRun` を `app/agent` に移した)。`use_tb` で断るのはやめた。
- 親を足す・外す `agent.parent.add` / `agent.parent.rm`(#290): 表 `agent_parents`(030、`spawned_by` から埋める)。`spawned_by` は立てた親(上限・深さ・記録)のまま。`tb agent shed` の orphan は、親が 1 体以上いて全員止まったときだけ。承認・報告を「親のうち誰か」に回すのは別の設計(多対多の親)。
- job の書き込み `job.set` / `job.rm`(#292、段 3 の先取り): `tb job set` / `rm` と同じ権限と確かめ(scheduler が読めるか)。agent セットの schedule が使う。チケットの読み取り `ticket.get`(#289)も先に入れた。
- 自動スケールの条件に使う読み取り(#297): `ticket.count`(project・label・状態の分類・担当なしで絞った数)と、`agent.list` の `status`(herdr の agent_status)。
- job を今すぐ動かす `job.run`(#302、`POST /v1/jobs/{name}/run`、`tb job run`): 中身は `herdrctl sched tick --now <job>`。時刻は見ず、ENABLED・GATE・SKIP_IF・ロックは毎分の回と同じ。既定は人だけ(policy の `cmd:job:run` で agent にも許せる)。scheduler を通さず、API を呼べば job が動く。
- 組織図の読み取り `agent.org`(`GET /v1/org`、`tb org`): 台帳の agent と設定 org で決まる上司。`tb org` の計算をここへ寄せ、Web UI の agent 一覧の組織図と `/org` も同じプロセスで呼ぶ。
- まだ残るもの: 一覧(`tb agents` / `agent-ops`)の読み取りの全部と、Web UI の agents 画面を同じプロセスで呼ぶこと。agent セットが使う最小の読み取り `agent.list` は入れた(今の親 `Parents` も返す)。

### agent セット(`internal/agentset`、`tb agentset`)

- 入口の 1 つとして作った。`agent.list` / `agent.new` / `agent.revive` / `agent.stop` を呼ぶだけで、台帳や store を直接触らない(層の検査で入口として扱う)。
- 定義は DB の設定ではなくファイル(`TB_AGENTSETS` か、DB と同じ所の `agentsets/<セット>.conf`)。書くのは人だけ(`tb agentset add` は inner の require_human)。
- `up` は外の親と上限を先に確かめ、だめなら 1 体も立てない。親から順に、1 体目は workspace(label はセット名)、残りはその pane を分けて置く。動いているメンバーはそのまま、止まっているメンバーは同じ workspace に戻す(`agent.revive` にも置き場所を足した)。`down` は子から順に止め、外の親は止めない。止めてよい(killable)の判定は `tb agent stop` と同じで、判定の無いメンバーは `--force` が要る。
- 数を変える `scale`(#288): count が 2 以上か max(数か unlimited)を書いたメンバーは `<セット>-<メンバー>-<N>`。増やすときは止まっている番号から戻し、足りなければ同じ workspace に新しい番号で足す(killable=yes)。減らすときは番号の大きい順に、止めてよく未完了のタスクが無いものだけを止める。
- job 型(`mode = job`、#289): `tb agentset run <セット> <メンバー> --task T` がタスク 1 つに 1 体立て(spawn_task=T、killable=yes)、`tb agentset reap` がタスクの終わった(か消えた)agent を止める。定期の片付けは scheduler の COMMAND 型のジョブに `tb agentset reap <セット>` を書く。
- `[adopt]` と `on_parent_exit`(#291): `[adopt]` の `<agent> -> <親>` は動いている agent に up で親を足し、down で外す(止めない)。`on_parent_exit = keep | grandparent | set-parent | stop`(既定 keep)は、親が全員止まったメンバーと adopt の agent を reap で付け替えるか止める。
- 定期の指示 `schedule = every <N>m|<N>h: <本文>`(#292): up が scheduler のジョブ `agentset-<セット>-<メンバー>`(TARGET はその agent、終日)を書き、down で消す。
- 時刻で書く schedule(#304): `schedule = at YYYY-MM-DD HH:MM: <本文>`(1 回だけ)と `schedule = <曜日> HH:MM: <本文>`。
- 条件での自動スケール(#298・#301・#303): メンバーに `scale_out` / `scale_in`(`<値> <比べ方> <数>` を and でつなぐ。値は queue / live / idle / busy / blocked)、`queue`、`scale_step`、`scale_cooldown`、条件の後ろの `for <N>m` を書く。`tb agentset autoscale <セット>` が 1 回評価して `Scale` を呼ぶ(増やすのは max まで、減らすのは count まで)。状態(当たり始めた時刻・前に変えた時刻)は `agentsets/.autoscale/<セット>.json`。
- 自動スケールは job に乗せる(#303): 条件があれば up がジョブ `agentset-<セット>-autoscale`(`autoscale_every` ごと。既定 5m)を書く。時刻で数を決める `scale_at = <時刻>: <数>` は、行ごとにジョブ `agentset-<セット>-<メンバー>-at<N>` にする。down は全部消し、up は定義から外したジョブを消す。どの job も `tb job run` / `POST /v1/jobs/{name}/run` で今すぐ動かせる。
- scheduler の側(#299・#302): 1 回だけのジョブ `AT=`、呼ばれたときだけのジョブ `MANUAL=1`、DB のジョブをすべて毎分読み直す `dispatch.sh --all`(セットが足したジョブも、dispatch を登録し直さずに動く)。Web UI の上のバーは「job」(#300)。
- まだ入れていないもの: `panes_per_workspace`(1 つの workspace に入れる pane の数)、Web UI のセットごとの表示と job を今すぐ動かすボタン。定義に書いてあれば黙って無視せず断る。

## 段 2 で入れたもの(2026-11-27)

- `internal/app/ticket`: 状態の変更 `ticket.move`(`POST /v1/tickets/{id}/move`、`tb ticket move`)と削除 `ticket.rm`(`POST /v1/tickets/rm`、`tb ticket rm`)。中身(アラート・提案のチケットの守り、状態の分類が変わるときの列、並行した変更の検出、消すときの子・作成時刻の確かめ)は `internal/tickets`(write.go)に移し、完了の通知と通知の規則は `internal/notify`(tickethooks.go)に移した。tb の `ticket status rm`(外す状態のチケットを移す)も同じ `tickets.Move` を使う。
- 入口の段の門 `app.Entry`: commands.tbl の場所が entry の行(`cmd:ticket:move` / `cmd:ticket:rm`)で、ask は承認待ち(Q-<n>)に、deny は tb と同じ文言と記録にする。tb の入口は `perms.Gate` で判定済みなので、`Env.EntryChecked` を立てて二重に判定・記録しない。Web UI や将来の API から agent として呼べば、ここで判定される(`ticket.rm` は agent からは ask)。
- 履歴の「変えた者」は `Env.EventActor`: tb の入口は今のトリガー(pane から引いた名前か `pane:<id>`)のまま、Web UI などは主体から(agent は名前、ほかは `human`)。
- Web UI の「状態を変える」(/task-status)と「チケットを消す」(/task-rm)は `runTB` をやめ、同じ操作を同じプロセスで人として呼ぶ(`callEdit`)。断ったときは「断りました: <理由>」と出す。
- 状態を動かす `ticket.claim` / `ticket.done` / `ticket.block` / `ticket.cancel`(`POST /v1/tickets/{id}/<動詞>`、`tb claim` / `done` / `block` / `cancel`)。中身は `internal/tickets/state.go`。
- リンク `ticket.link` / `ticket.unlink` とスプリント `ticket.sprint.add` / `start` / `close`(中身は `internal/tickets/link.go`)。Web UI のボードのスプリントの操作も同じプロセスで呼ぶ。リンクの作った者(`created_by`)は `Env.ActorName`(tb は今の actor_name)。
- 作る・変える `ticket.add`(`POST /v1/tickets`)と `ticket.update`(`PATCH /v1/tickets/{id}`)。中身(ラベル・外部リンク・親子の規則・採番)は `internal/tickets/edit.go`。update の入力は tb の引数の順の変更の並び(`tickets.Change`)で、検査の順と文言も tb と同じ。
- 報告コメント `ticket.comment`(`POST /v1/tickets/{task_id}/comments`)と `ticket.reopen`(`POST /v1/comments/{id}/reopen`)。報告した者は `Env.Reporter`(tb は herdr から引いたもの)、すぐ届ける種類は `Env.ImmediateKinds`(tb は `TB_IMMEDIATE_KINDS`)。配送(`deliver_comment`)は `message.DeliverComment` に移し、tb の report / digest も同じものを使う。外への通知は `notify.Comment`。
- 状態の定義 `ticket.status.add` / `ticket.status.rm`(人だけが既定の inner の行)。中身は `internal/tickets/statusdef.go`、設定のファイルの書き込みは `config.WriteFile`。Web UI の状態の定義の画面も同じプロセスで呼ぶ。
- ジョブ `job.set` / `job.unset` / `job.rm`(`internal/app/job`)に tb の選択肢(`--settings` / `--shared`、rm の行が無ければ断る・ログを消す)を足し、tb の `job set` / `edit` / `unset` / `rm` は API を呼ぶだけにした。scheduler が本文を受け付けないときは書かずに理由(`Rejected`)を返す。置き場所は `Env.SchedDir` / `SchedShow` / `SchedLogs`(空なら tb と同じ既定。Web UI は自分の見ている所を入れる)。
- Web UI のジョブの上書き設定・定義の保存・ジョブを消す・agent の操作の承認と却下は `runTB` をやめ、同じプロセスで人として呼ぶ(agent の承認はほかのセッションだけ tb の子プロセスのまま。herdr の呼び先がプロセスの環境で決まるため)。
- 承認 `approval.approve` / `approval.reject`(`POST /v1/requests/{id}/<動詞>`、`tb approval approve|reject Q-<n>`、前の O-<n> も)と承認制の行 `approval.allow` / `approval.ask`。中身(送信の承認と配送、ask で止まったコマンドを依頼者に返すか依頼者として実行する、policy の行の書き込み)は `internal/requests` に移した。agent の操作の承認待ちは `agent.Decide` に渡し、`--always` も agent.approve で扱う(tb の中の承認の実装は無くなった)。tb agent-approve / reject の入口の行(`cmd:agent:<動詞>`)は approval の側でも判定する。途中の出力は `Env.Out` に流すか結果に集め、承認したコマンドを動かす子は `Env.ChildEnv` / `TbCmd`(Web UI はこのチェックアウトの tb)。policy の書き込みの確かめは主体で行う `perms.RequirePolicyWriteAs`。Web UI の承認の画面も同じプロセスで呼ぶ。
- tb はまだ `internal/requests` を直接読む(承認待ちの一覧・履歴・`approval run`・承認済みの子の確かめ)。層テストの許容リストに残している。
- 設定 `config.set` / `config.unset`(`internal/app/config`。`PUT` / `DELETE /v1/config/{key}`、`tb config set` / `edit` / `unset`)。キーごとの検査(`Validators`)と、policy を変える agent の行ごとの確かめも API の中。Web UI の policy の保存も同じプロセスで呼ぶ。これで Web UI の `runTB` は、herdr の呼び先をプロセスの環境で決めるためのほかのセッションの agent の操作と承認だけになった。
- 送信 `message.send`(`POST /v1/messages`)と `message.flush`(`internal/app/message`)。中身(入力の確かめ・受け付けの判定・outbox への書き込み・配送・届けたあとの読み直し)は `internal/message`(send.go)。送信者は `Env.Reporter`(tb は herdr から引いたもの。決められなければ理由 `no_sender`、tb は 3)。
- まだ残るもの: report / pending / comments の読み取りと、tasks.go の読み込み。outbox / inbox の一覧と inbox の既読。
- アラート `alert.raise` / `alert.dispatch` / `alert.approve` / `alert.result` / `alert.close`(`internal/app/alert`。`POST /v1/alerts`、`/v1/alerts/{id}/<動詞>`、`tb alert` / `alert-dispatch` / `alert-approve` / `alert-result` / `alert-close`)。指紋での重複の数え上げ・解析役の起動(承認・上限・`max_running_agents`)・結果の受け取りと指揮官への配送・解析役に止めてよい印を付けるのは API の中。報告者は `Env.Reporter`(tb は pane の中だけ herdr から引く)。承認と閉じるのは既定で人だけ(inner の行で判定する)。報告ファイルを読むのと一覧(`tb alerts`)は tb に残した。
- 役割 `agent.role.write` / `agent.role.edit` / `agent.role.unset` / `agent.role.set`(`internal/app/agent` の role.go。`PUT` / `DELETE /v1/roles/{name}`、`POST /v1/roles/{name}/edit`、`PUT /v1/agents/{name}/role`、`tb role-write` / `role-edit` / `role-unset` / `role-set`)。自分の持つ役割と role:change を許す役割は人だけが扱う確かめ、policy の role:change の判定(ask は理由 `approval_required` で tb は 20)、台帳のロールの書き換えと記録は API の中。人として扱うのは人だけ(system:* は agent と同じ確かめを受ける)。役割の配布(`role-broadcast` / `role-context`)と一覧は tb に残した。
- 通知 `notify.on` / `notify.off` / `notify.test` / `notify.flush` / `notify.ack` / `notify.event`(`internal/app/notify`。`POST /v1/notify/on|off|test|flush`、`POST /v1/notify/events`、`/v1/notify/events/{id}/ack`、`tb notify on|off|test|flush|ack|event`)。設定 notify の書き換え(`WriteConf`)、試しの通知、送り切り、ack の hash の確かめは API の中。ack は notifier agent か人だけ(呼び手は `Env.Reporter`)、ほかは既定で人だけ(commands.tbl の inner の行で判定し、policy で緩められる。アラートの承認・閉じる、役割の行を消すのも同じ)。状態・一覧・規則の表示と `notify rules run` は tb に残した。
- レシピ `recipe.set` / `recipe.rm` / `recipe.cancel`(`internal/app/recipe`。`PUT` / `DELETE /v1/recipes/{name}`、`POST /v1/recipe-runs/{id}/cancel`、`tb recipe set` / `rm` / `cancel`)。登録の書式の確かめ・履歴・実行の依頼を閉じる処理(`RunClose`。runner も使う)は API の中。登録と削除は既定で人だけ(inner の行)、取り消しは人と頼んだ agent だけ。実行(`recipe run` / `runner`)は tb のコマンドを部品として動かすので tb に残した。
- agent の判定フラグと作成の凍結 `agent.flag` / `agent.freeze` / `agent.unfreeze`(`internal/app/agent` の flag.go。`POST /v1/agents/{name}/flag`、`POST /v1/agents/spawn-freeze|spawn-unfreeze`、`tb agent-flag` / `spawn-freeze` / `spawn-unfreeze`)。tb の使われていなかった `opEntry` / `authorizeOp` は消した。
- リマインダー `reminder.publish` / `reminder.scan`(`internal/app/reminder`。`POST /v1/reminders/publish|scan`、`tb reminder publish` / `scan`(古い名前 `reminders --publish` / `due-scan`))。まとめを作って前回の記録を書く処理と、期限のイベントを残して通知の規則を回す処理は API の中。書かない `list` / `check` と `nudge` は tb に残した。
- スラッシュコマンドとモデルの切り替え `agent.slash` / `agent.model.set`(`internal/app/agent` の slashop.go / modelsetop.go。`POST /v1/agents/slash|model-set`、`tb slash` / `model-set`)。宛先の解決、agent からの判定(`agent:slash:<コマンド>:<宛先>` / `agent:model:<名前>`)、承認待ち(L-<n>)を積む処理、送信・切り替えは API の中。結果は宛先ごとの tb の 1 行と終了コードで返す。`slash --list` は tb に残した。
- まとめ送信 `digest.run`(`internal/app/digest`。`POST /v1/digests`、`tb digest`)。未配送の報告・未報告の done・アラート・通知・観測(idle / stuck / 複数チケット / queued / blocked / 子 agent の上限・取り残し)と scheduler の見張り(sched.go。もとの tbcli/schedwatch.go)を集めて宛先ごとに 1 通にまとめ、送れた項目に印を付けるのは API の中。結果は tb の行と終了コードで返す。まとめの一覧(`tb digests`)は tb に残した。
- 完了の報告 `ticket.report`(`POST /v1/tickets/{ref}/report`、`tb report <id> | C-<n>`)と受信の既読 `message.read`(`POST /v1/inbox/{id}/read`、`tb inbox-read` の印を付ける所)。報告コメントを作って届ける処理と、宛先の agent 自身だけが印を付ける確かめは API の中。受信の行を読んで出すのは tb に残した。
- 文脈の整理 `agent.compact.check`(`internal/app/agent` の compact.go。`POST /v1/agents/compact-check`、`tb compact-check`)。transcript の usage を読んで閾値・間隔・policy(`agent:compact:<名前>`)を見て /compact を送り、記録するのは API の中。tb の使われていなかった `recordOpID` は消した。
- 中身を読む `agent.peek`(`internal/app/agent` の peek.go。`POST /v1/agents/peek`、`tb peek`)。`agent:read:<対象>` の判定と herdr からの読み出し、`read_log` への記録は API の中。tb は引数を確かめて読んだ文字列を出すだけ。
- 承認待ちの優先度 `approval.priority`(`internal/app/approval` の pending.go。`POST /v1/requests/{id}/priority`、`tb approval priority`)。承認待ちを積んだときの Q-<n> の引き当て・優先度(`TB_PRIORITY`)・決められる agent への知らせ(`approval.PendingQ` / `NotifyApprovers` / `CanDecide`)も tb から API に移した(tb の入口が `Env.PendingQ` に入れる。Web UI は別の herdr セッションも扱い、要求の中で herdr を呼ばないよう、まだ入れない)。tb の入口が ask で止めたコマンドの行の書き込みと、承認済みのコマンドを 1 回だけ動かす印は `internal/requests`(`InsertCmd` / `ClaimRun`)に寄せた。tb の入口の門そのもの(`perms.Gate` の Queue / Record)は段 5 で入口から外すまで tb に残る。
- 旧形式の DB の取り込み `config.db.import`(`internal/app/config` の dbimport.go。`POST /v1/config/db-import`、`tb db-import`)。旧 DB の確かめ・写しの移行・1 つのトランザクションでの取り込みは API の中。門は commands.tbl の inner の行 `cmd:config:db-import`(既定は agent を断る。system 主体は `system:` の行に従い、合う行がなければ通す)を `app.Cmd` で判定する。tb は引数を読んで結果を出すだけ。
- レシピの実行 `recipe.run` / `recipe.runner`(`internal/app/recipe` の run.go。`POST /v1/recipes/{name}/run`、`POST /v1/recipe-runs/run`、`tb recipe run` / `runner`)。レシピと部品ごとの判定、待ち行列への積み込みと取り出し、部品の実行、履歴の記録は API の中。部品は tb のコマンドを呼ばず、API の中身(`agent.Stop` / `Revive` / `Restart`、`ticket.Show`、`ticket.ResolveComment`)を直接呼ぶ。部品 task-show のため、`tb show` の中身を `ticket.show`(`GET /v1/tickets/{id}/show`)に移した。
- チケットの読むだけの操作 `ticket.tree` / `ticket.labels` / `ticket.sprints` / `ticket.board` / `ticket.history` / `ticket.events`(`internal/app/ticket` の read.go。`GET /v1/tickets/tree`・`/v1/tickets/labels`・`/v1/sprints`・`/v1/sprints/board`・`/v1/tickets/{id}/history`・`/v1/ticket-events`)。結果の `text` は tb の出力そのまま。tb は引数を読んで出すだけ。
- 報告コメントと状態の読むだけの操作 `ticket.comments` / `ticket.pending` / `ticket.status.list`(`internal/app/ticket` の readcomments.go。`GET /v1/comments`・`/v1/reports/pending`・`/v1/ticket-statuses`)。tb comments / pending / ticket status list は引数を読んで出すだけ。
- 承認待ちと記録の読むだけの操作 `approval.list` / `approval.history`(`internal/app/approval` の read.go。`GET /v1/requests`・`/v1/requests/history`)。`--mine` と実行の結果を見せる相手は呼び手の主体で決める。tb は引数を読んで出すだけ。
- 通知の読むだけの操作 `notify.status` / `notify.dryrun` / `notify.events` / `notify.rules.test`(`internal/app/notify` の read.go)。行を tb の出力の形にする関数(sqlite3 の -separator と jq -c の形)は `internal/app/rowfmt` にまとめ、tb と API の両方が使う。
- 送信と受信の読むだけの操作 `message.outbox` / `message.inbox` / `message.inbox.get`(`internal/app/message` の list.go。`GET /v1/outbox`・`/v1/inbox`・`/v1/inbox/{id}`)。受信の一覧と 1 件は呼び手宛ての行だけ。tb inbox-read は 1 件を読んでから `message.read` で印を付ける。
- アラートとまとめの読むだけの操作 `alert.get` / `alert.list`(`internal/app/alert` の read.go。`GET /v1/alerts/{id}`・`/v1/alerts`)と `digest.list`(`internal/app/digest` の list.go。`GET /v1/digests`)。`alert.get` は行の JSON を返し、tb はそれを読んで前と同じ形で出す。

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

- パッケージの名前: `internal/app/<サービス>`(案)か `internal/service/<サービス>` か。
- Web UI の画面向けの集計(一覧の JOIN など)も API に置くか、`app/<サービス>/query` のように読み取り専用の場所を分けるか(案: 分ける。書き込みの関数と同じパッケージの別ファイル)。
- 段 1 を agent セットの段 1・2(親の付け替え、pane に置く起動)と一緒に進めてよいか(案: 一緒に。同じファイルを触るので)。

## 確認できていないこと・推測

- `taskboard/test/cases` の tb のテストは出力の文言まで比べているので、文言を写す表を作れば互換を保てる、と推測している。段 0 で数個の動詞を移して確かめる。
- Web UI の `runTB` の時間切れ(15 秒)に頼っている処理があるか(長い spawn など)は未確認。同じプロセスで呼ぶときは context の期限で同じ時間を与える。
