# ADR 0007: 認証した主体に認可を与える 1 つのモデルにし、過去の権限の仕組みをなくす

- 状態: Accepted(2026-11-22。人が「全部推奨で進めて」と決定。段 2 から実装)
- 日付: 2026-11-22
- 置き換えるもの: ADR 0006 の「変えるのは approvals の cmd: の行」と、approvals / grants / routes / delivery の許可・拒否 / protect / spawn-policy の自動承認 / require_human / proxy check の allow 判定。ADR 0005 の「ask / allow は approvals と grants で決める」

## 要旨

- **困りごと**:
  1. **認証が無い**: 呼んだのが誰かは、環境変数 `HERDR_PANE_ID` を `herdr agent list` と突き合わせて決めている。agent が自分のシェルで `HERDR_PANE_ID` を消すと人の端末として扱われ、`only-human` も通る。別の agent の pane を入れれば、その agent になれる。
  2. **認可の仕組みが 8 つある**: `approvals`(操作 → ask/allow、cmd: の段階)、`grants`(主体・操作・対象)、`routes` と `delivery`(送信の許可・拒否)、`protect`(止めさせない)、`spawn-policy`(自動承認)、`require_human`、`proxy check`、承認待ち(O-<n> / L-<n>)。同じことを別の書き方で決めていて、どれが効くのかを人が追えない。
- **決定(案)**:
  1. **認証**: 呼び出し元を、自己申告(環境変数・引数)ではなく、**呼び出したプロセスそのもの**から決める。結果は「主体」(`human` / `agent:<名前>` / `system:<名前>` / `unknown`)の 1 つ。確かめられなければ `unknown` で、`unknown` には何も許さない。
  2. **認可**: 主体に対して、**1 つの表(設定 `policy`)だけ**で決める。1 行は `<主体> <操作> <対象> = <効果>`。効果は `allow` / `ask-any` / `ask-human` / `deny` の 4 つ。
  3. **入口は PR 197 の門をそのまま使う**。判定する関数の中身を「主体 × 操作 × 対象 → 効果」に替える。コマンドの中の判定(送信・停止・peek・spawn など)も、同じ関数を呼ぶ。
  4. **過去の設定は 1 回だけ変換し、消す**。互換で読み続けない。
- **段階**: 段 1(認証)→ 段 2(policy と判定)→ 段 3(変換と削除)→ 段 4(承認待ちを 1 つにする)。どの段も、入れた時点の振る舞いは今と同じにする(既定の表を今の振る舞いから作る)。

## 状況(Context)

### 今の認証

- `taskboard/lib/identity.sh` の `requester` と `pane_owner_name`、Go の `internal/kernel/identity`。`HERDR_PANE_ID` が無い・pane に agent がいない → 人。
- Web UI は別に ID・パスワードとセッションで人を認証している(`webui/auth.py`)。そのあと tb を人として呼ぶ。
- scheduler・hook など、人でも agent でもない呼び出し元は、今は人として扱われる(`HERDR_PANE_ID` が無いため)。

### 今の認可(PR 197 の時点)

| 仕組み | 決めること | 書き方 |
|---|---|---|
| `approvals` | 操作ごとの ask / allow、cmd: の段階 | `<操作の glob> = ask\|allow\|ask-any\|ask-human\|only-human` |
| `grants` | 誰が誰に何をしてよいか(approvals より先に見る) | `<主体> <操作> <対象>` |
| `routes` / `delivery` | 送信の経路の許可・拒否と、届け方(now / hold / block) | `<送信者> -> <宛先> : <種別>=<扱い>` |
| `protect` | 止めさせない agent | `<agent の glob> [= 理由]` |
| `spawn-policy` | agent-spawn の自動承認の条件 | `<キー> = <値>` |
| `require_human` / 権限表 | 人だけのコマンド | コードと `perms/commands.tbl` |
| `proxy check` | 代行の自動許可 | 組み込みの条件 |
| 承認待ち | ask になった操作の保留 | O-<n>(送信)、L-<n>(agent の操作)、(P-<n> は未実装) |

関係のデータ(`org` の組織図、spawn の親子、役割)は、`grants` と `delivery` の対象の書き方(`~child` `~report` `@worker` など)で使われる。

認可ではないもの: `limits`(数の上限)、`risk-rules`(リスクの表示。第 1 段は判定に使わない)、`gate`(merge の参考値)、`delivery` の hold(届け方)。

## 決定

### 1. 認証: 主体はプロセスから決める

- tb は、自分を呼んだプロセスを起点に、**herdr に「このプロセスはどの pane の中にいるか」を尋ねる**。herdr は pane ごとに、その pane のシェルのプロセスを知っている。呼び出し元の祖先をたどって、どの pane のシェルの子孫かで pane を決める。`HERDR_PANE_ID` は使わない(参考の表示にだけ使う)。
- 主体の決め方:
  - agent の pane の子孫 → `agent:<名前>`(名前と session は herdr から)
  - 人の pane(agent のいない pane)の子孫 → `human`
  - Web UI → ログインのセッションで `human`。Web UI は tb を呼ぶとき、自分の秘密(Web UI だけが読めるファイル)で署名した「人の操作」の印を渡す。tb はその印を確かめる
  - scheduler・hook → `system:<名前>`。起動の仕組みが渡す印で確かめる(印の作り方は Web UI と同じ)
  - どれにも当たらない(pane の外に出たプロセス、二重 fork で親を切ったプロセスなど)→ `unknown`
- **`unknown` には何も許さない**(既定の表で `allow` の読み取りも含めて止めるかは「人が決めること」2)。`HERDR_PANE_ID` を消しても、人の pane の子孫にはならないので `human` にはなれない。
- 確かめに失敗した(herdr に聞けない)ときも `unknown`。

### 2. 認可: 1 つの表 `policy`

```
# <主体> <操作> <対象> = <効果>
human        *                   *            = allow
@manager     cmd:ticket:*        *            = allow
@worker      cmd:ticket:resolve  *            = ask-human
*            send:task           ~child       = allow
@worker      send:*              ~manager     = allow
*            agent:stop          role:auditor = deny        # 旧 protect
*            agent:read          self         = allow
lead1        approve:agent:stop  ~report      = allow       # 旧 grants の approve:
```

- **主体**: `human` / agent 名の glob / `@<役割>` / `system:<名前>` / `*`。`unknown` は書けない(常に何も許さない)。
- **操作**: `cmd:<サービス>:<動詞>`(PR 197 の表の操作名)と、コマンドの中の操作(`send:<種別>`、`agent:stop`、`agent:read`、`agent:spawn`、`proxy:<effect>`、`recipe:<名前>` など)。承認する権限は `approve:<操作>`。
- **対象**: agent 名の glob / `@<役割>` / 関係(`self`、spawn の `~child` `~descendant` `~parent` `~sibling`、組織図の `~report` `~subordinate` `~manager` `~superior` `~peer`)/ `*`。対象の無い操作は `*`。
- **効果**: `allow`(すぐ実行)/ `ask-any`(別の agent か人の承認が要る)/ `ask-human`(人の承認が要る)/ `deny`(実行しない。承認でも通らない)。`only-human` は無くなる(`human ... = allow` と、ほかの主体の `deny` で書く)。
- **決め方**: 当たる行のうち、**いちばん厳しい効果**を採る(上から最初に当たった行、ではない)。こうすると、行の順番で抜け道ができない。`deny` が 1 行でも当たれば止まる。当たる行が無ければ、コードの既定の表(下)で決まる。
- **既定の表**: PR 197 の `perms/commands.tbl` の既定の列を、`human * * = allow` と「agent の既定」に分けたもの。コマンドの中の操作の既定(今の approvals の既定 ask、delivery の既定など)も、この表に移す。入れた時点の振る舞いは今と同じ。
- **下限**: PR 197 と同じく列だけ残し、今はすべて `allow`(人の決定)。
- **policy を書けるのは `human` だけ**。これは表の行ではなくコードで固定する(`policy` を緩める行を agent に書かせない)。
- **判定の確かめ方**: `tb can <主体> <操作> <対象>` が、効果と当たった行をすべて出す(今の `tb can` を置き換える)。

### 3. 入口

- PR 197 の門(bash の `perm_gate` / `require_human`、Go の `perms.Gate` / `perms.RequireHuman`)の中身を、`authenticate`(主体を決める)と `authorize <主体> <操作> <対象>` に替える。
- コマンドの中の判定(送信の経路と approvals、grants、protect の確かめ、spawn の自動承認、`proxy check`。`lib/send.sh` `lib/approvals.sh` `lib/agents.sh` などと Go の同じ部分)は、すべて `authorize` を呼ぶ形に置き換えて消す。
- テスト: 判定を呼ぶ箇所が `authorize` 以外に無いこと(PR 197 の「identity.RequireHuman を直接呼ばない」と同じ形の検査)。

### 4. 過去の設定の変換と削除

- `tb policy import`(人だけ)が、`approvals` / `grants` / `routes` / `delivery` の許可・拒否 / `protect` / `spawn-policy` の自動承認の部分を、`policy` の行に変換する。変換できない行は書かずに一覧で出す(人が直す)。
- 変換のあと、古いキーは読まない。残っていれば、tb は起動のたびに「古い権限の設定が残っている。tb policy import をして消す」と警告する(黙って無視しない)。
- `delivery` は届け方(now / hold)だけを残す。許可・拒否(block、`!` の行)は policy に移る。
- 承認待ちは 1 つにする(段 4): O-<n> / L-<n> / P-<n> を 1 つの承認待ちにし、承認できるのは `approve:<操作>` が `allow` の主体。承認は依頼者の再実行で 1 回だけ使え、使うときにもう一度判定する(ADR 0006 の決定のまま)。

## 代替案

| 案 | 良い点 | だめな点 |
|---|---|---|
| A. 今の仕組みを残し、認証だけ足す | 変更が小さい | 8 つの仕組みは残る。人が「どれが効いているか」を追えない |
| B. 認証は環境変数のまま、署名した印を pane の起動時に渡す | herdr を変えずに済む | 同じ OS ユーザーの agent は、ほかのプロセスの環境変数を読める(`/proc/<pid>/environ`)。印を盗めるので、なりすましを止められない |
| C. agent ごとに OS のユーザーを分ける(ADR 0001 の L3) | いちばん強い | herdr のセッションの運用と合うか分からない。今すぐはできない。この ADR の認証は、C を入れたときもそのまま使える(主体の決め方が同じ) |
| D. 上から最初に当たった行で決める(今の approvals と同じ) | 慣れている | 行の順で抜け道ができる。表を 1 つにすると行が増え、順番の誤りが起きやすい |

## 段階

| 段 | 内容 | 振る舞い |
|---|---|---|
| 1 | 認証: tb が「呼び出し元はその pane の中か」を herdr の pane process-info とプロセスの親子で確かめる(段 1a。herdr は変えない)。Web UI と scheduler の印 | 主体の決め方だけが変わる。`HERDR_PANE_ID` を消しても人にはなれない |
| 2 | `policy` の表と `authorize`。PR 197 の門の中身を替える | 既定の表で、今と同じ |
| 3 | `tb policy import` と、コマンドの中の判定の置き換え。古いキーと関数を消す | 変換した行で、今と同じ |
| 4 | 承認待ちを 1 つにする(ask-any / ask-human の承認) | ask-* が exit 20 で止まる代わりに、承認待ちになる |

段 1 が herdr の変更に依存するので、herdr 側が入るまでは、段 2・3 を先に進めてよい(そのあいだ認証は今のまま)。

### 段 3 の進み具合(2026-11-23)

- **段 3a(済み)**: `approvals` と `grants` を `policy` に統合した。コマンドの中の操作の既定は表 `taskboard/perms/ops.tbl`(herdrctl も同じ表を埋め込む)。`approval_of` / `grant_of` は policy の判定(当たる行のいちばん厳しい効果、無ければ既定の表)に替え、deny は送らず実行しない(終了コード 4)。古いキーはもう読まず、残っていれば一度だけ警告する。`tb policy import [--apply]` が古い行を変換する
  - 変換の決まり: approvals の行は主体 `*` の行にする。ただし `*` や `agent:*` のような広い glob は、`cmd:`・`approve:`・`agent:flag` に当たらないよう、中の操作の族ごとの行に分ける(前の approvals はコマンドと承認の委任を決めていなかったので、`* * * = allow` にすると権限が広がる)。`role:change` は人事役(`@hr`)の行にする(前は人事役にだけ効いていた)。grants の行は `= allow` を付けた行にする
  - 前は「上から最初に当たった行」だったので、厳しい方が勝つ policy では結果が変わる行がある(広い ask の下の狭い allow、ask に当たる grants)。import はそれを「確かめてほしい行」として出す。広い行で締めて狭い行で緩める書き方はできない(決めたことの 1 のとおり)
  - `tb approval allow` / `ask` は policy に「* <操作> <対象> = allow|ask-any」の行を書く(上と同じ分け方)
  - policy.conf と `perms/*.tbl` を gate の外せない保護対象に足した
### 段 1 の進み具合(2026-11-23)

- **人が決めたこと**: 別の OS ユーザーで動かす tb のサーバーは作らない(最低限で終える)。同じ OS ユーザーの agent による、わざとのなりすましは一部許容する
- **段 1a(済み)**: `HERDR_PANE_ID` を信じず、tb(と herdrctl)を呼んだプロセスが本当にその pane の中で動いているかを確かめる。herdr は変えない
  - 確かめ方: `herdr pane process-info --pane <pane>` が返す pane の最初のプロセス(`shell_pid`)が呼び出し元の祖先にいるか、同じ端末(tty)を使っているか(親が先に終わって祖先から外れても、端末は残る)
  - `HERDR_PANE_ID` が無くても、herdr の中(祖先か、同じ端末の持ち主の親に herdr のプロセスがいる)なら agent の pane を探し、見つかれば `HERDR_PANE_ID` を付け直す。`HERDR_PANE_ID` を消して人のふりをする、を止める。どの agent の pane でもなければ人の pane
  - 確かめられないとき(申告した pane の中にいない、herdr に聞けない)は何もしない(`identity.unverified`、終了コード 3)。hook(`touch`、`role-context`)は止めない
  - herdr の外(人の端末・scheduler・Web UI)は今までどおり人
- **許容すること(段 1a で止めないもの)**: 親子関係と端末の両方を切る実行(`setsid` と新しい端末)、`HERDR_BIN` などで偽の herdr を使わせる、tb を書き換えて実行する、DB に直接書く。どれも同じ OS ユーザーで動く限り tb の中では止められない。Windows(herdrctl)では ps が無いため確かめず、段 1a より前と同じく `HERDR_PANE_ID` を信じる。止めるには OS のユーザーを分ける(代替案 C)
- **これから**: scheduler・hook の主体 `system:<名前>` と、policy の `system:` の行

- **段 3b(これから)**: `routes` / `delivery` の許可・拒否、`protect`、spawn の方針の自動承認、proxy check を policy に移し、古い仕組みを消す

## 人が決めたこと(2026-11-22。すべて推奨どおり)

1. **決め方**: 当たる行のうち「いちばん厳しい効果」を採る。
2. **`unknown` の扱い**: 何も許さない(読み取りも止める)。
3. **scheduler・hook の主体**: `system:<名前>` を新しく作る。
4. **古い設定の扱い**: 1 回変換して消す(互換で読み続けない)。

## 確認できていないこと・推測

- **herdr が「プロセス → pane」を答えられるか**(確かめた): herdr は接続元の pid を OS から受け取れるが、ログに使うだけで答える口は無い。代わりに `pane process-info` が pane の最初のプロセスの pid を返すので、tb の側で親子を照らす(段 1a)。
- **同じ OS ユーザーの限界**: agent が人の pane のシェルにコマンドを打ち込める(herdr の send 系の口を使える)なら、人の pane の子孫としてコマンドを動かせる。tb の `send` は宛先が agent だけだが、herdr を直接呼ばれると止められない。これは C(OS のユーザーを分ける)まで残る。
- **DB と設定ファイル**: agent が DB や設定ファイルに直接書き込めるなら、tb を通さずに policy を書き換えられる。段 1 と同時に、DB と設定を agent から書けない場所に置く必要がある(ADR 0001 の L3 と同じ問題)。
