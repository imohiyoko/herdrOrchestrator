# ADR 0006: tb の全コマンドに共通の権限段階を割り当て、設定で変えられるようにする

- 状態: Proposed
- 日付: 2026-11-22

## 要旨

- **困りごと**: tb の権限の扱いが、コマンドごとにばらばら。約 140 のコマンドのうち、約 40 は `require_human`(人だけ。コードに固定で変えられない)、約 15 箇所は `approvals` / `grants` で ask / allow を決め、残りは何も確かめない。「この agent にはこのコマンドを使わせたい / 使わせたくない」「agent のレビューで通してよい」を、コマンドごとに後から決められない。
- **決定(案)**:
  1. **段階を 1 つの物差しにする**: `allow`(agent がすぐ実行)/ `ask-any`(別の agent か人のレビューが要る)/ `ask-human`(人の LGTM が要る)/ `only-human`(人の端末からだけ)。レビューを**与える**権限は、段階とは別の軸で、既存の `grants` の `approve:<操作>` で決める。
  2. **全コマンドに既定の段階をコードで持つ**。操作名は `cmd:<サービス>:<動詞>`(新しい名前の形。古い名前は同じ操作名に寄せる)。
  3. **変えるのは既存の `approvals` の行**(`cmd:ticket:add = ask-any` など)。判定の仕組みは増やさない(ADR 0005 と同じ考え)。
  4. **守り**: コマンドごとに、コードが持つ**下限**(これより緩められない段階)を置く。権限そのものを書き換えるコマンド(設定・承認規則・レシピの登録など)の下限は `only-human`。分からないときは厳しい側に倒す。
- **段階**: 既定値は今の振る舞いと同じにする(何も変わらない状態で入れる)。そのうえで、人が設定で緩めたり締めたりする。

## 状況(Context)

今の tb の権限の処理は、次の 4 種類が混ざっている(2026-11-22 の `main` を読んだ事実)。

| 種類 | 仕組み | 例 | 後から変えられるか |
|---|---|---|---|
| A. 人だけ | 各コマンドの中で `require_human`(pane が agent なら die) | `config set`、`approval allow/ask`、`resolve`、`pr-queue approve`、`limits set`、`recipe-set`、`spawn-freeze`、`notify on/off` など約 40 | **変えられない**(コード固定) |
| B. 操作ごとの ask / allow | `approval_of <操作名>` → `grants`(主体ごとの許可)→ `approvals`(glob で ask / allow) | `send:task:<宛先>`、`agent:stop:<名前>`、`agent:spawn:<子>`、`agent:read:<名前>`、`role:change`、`recipe:<名前>:<対象>` など約 15 箇所 | 変えられる(`tb approval allow/ask`、`grants`) |
| C. 承認の委任 | `grants` の `approve:<元の操作> <依頼者>` を持つ agent が、O-<n> / L-<n> を承認・却下できる | `@lead approve:send:task:* ~child` | 変えられる |
| D. 確かめない | 何もしない | `add`、`list`、`claim`、`done`、`comment`、`agents`、`org` など残りの大半 | 決められない(常に誰でも) |

ほかに、コマンド固有の方針がある(`spawn-policy`(子の数・役割)、`delivery`(届ける / 保留 / 止める)、`protect`(止めさせない agent)、`limits`(数の上限)、組織図 `org`(関係 `~report` など。組織図そのものは何も許さない)、ADR 0005 のレシピ(部品ごとのリスク階層と人だけの部品))。これらは「どの相手に / いくつまで」を決めるもので、本 ADR の「そのコマンドを agent が使えるか」とは別の層として残す。

困りごと:

- A は、緩めたいとき(例: 報告コメントの `resolve` を信頼できる agent に任せる)も、コードの変更になる。
- D は、締めたいとき(例: worker に `cancel` させない)に、手段が無い。
- B の `ask` は「人か、委任された agent が承認する」の意味で、「人の LGTM が必ず要る」と「agent のレビューでよい」を分けて書けない。
- どのコマンドがどの扱いかを、一覧で見られない。

## 決定(Decision)

### 1. 段階(4 つ。上ほど緩い)

| 段階 | agent の pane から実行したとき | 人の端末から |
|---|---|---|
| `allow` | すぐ実行する | 実行する |
| `ask-any` | 承認待ち(P-<n>)になる。**`approve:<操作>` を持つ別の agent か、人**が承認すると、依頼者が 1 回だけ実行できる | 実行する |
| `ask-human` | 承認待ち(P-<n>)になる。**人だけ**が承認できる(`approve:` の委任があっても agent は承認できない) | 実行する |
| `only-human` | 実行しない(承認待ちも作らない)。今の `require_human` と同じ文面で止める | 実行する |

- **レビューを与える権限**(ユーザーの言う「agent からレビューを与える権限」)は、段階の 1 つではなく、**承認者の側の許可**として既存の `grants` の `approve:<操作> <依頼者>` で決める。例: `@reviewer approve:cmd:ticket:cancel ~peer`。段階を `ask-any` にしても、この行が無ければ承認できるのは人だけになる(今の ask と同じ)。
- **既存の値との対応**: `allow` はそのまま。既存の `ask` は `ask-any` の別名として読む(今の ask は「人か、委任された agent が承認」なので、意味が変わらない)。新しく `ask-human` と `only-human` を足す。
- 段階は**コマンドの入口の門**で、中の操作の判定(B: `agent:stop:<名前>` など)を置き換えない。両方を通る必要がある。二重の承認にならないよう、中で操作の判定をするコマンドの既定は `allow`(下の 3)にする。

### 2. 操作名

```
cmd:<サービス>:<動詞>          例: cmd:ticket:add、cmd:config:set、cmd:agent:spawn
```

- サービス名は `.claude/skills/tb-command-naming` の一覧に合わせる。今の古い名前(`add`、`agent-spawn`、`conv-stage-set` など)は、**対応する新しい名前の操作名に寄せる**(例: `add` → `cmd:ticket:add`、`resolve` → `cmd:ticket:resolve`)。別名を通って段階を逃れることはできない。
- 対応表(古い名前 → 操作名、既定の段階、下限)はコードに 1 つだけ持ち、bash と Go(herdrctl)が同じ表を使う(表はテキストの 1 ファイルにし、両方が読む。Go 側は埋め込む)。
- 対象が決まるコマンド(`cmd:agent:stop` など)でも、門の操作名には対象を入れない。対象ごとの判定は中の B に任せる(門と中で役割を分ける)。

### 3. 既定値(コードに持つ)と下限

既定値は **今の振る舞いと同じ** にする。入れた時点では何も変わらない。

| 今の扱い | 既定の段階 | 下限(これより緩められない) |
|---|---|---|
| D. 確かめない(読み取り・チケットの作業) | `allow` | `allow` |
| B. 中で操作の判定をする(`message send`、`agent-stop`、`agent-spawn`、`peek` など) | `allow`(中の判定に任せる) | `allow` |
| A. 人だけ(下の「自己の権限に関わるもの」以外。例: `resolve`、`reopen`、`pr-queue approve`、`nudge`、`notify on/off`、`alert-close`) | `only-human` | `ask-human`(人が設定で `ask-human` までは緩められる) |
| A. 人だけのうち、**権限・設定そのものを書き換える**もの(`config set/edit/unset/import`、`approval allow/ask`、`recipe-set/rm`、`limits set/mode/unset`、`spawn-unfreeze`、`db-import`、`job set/edit/unset/import`、`role-unset`、`system-send`) | `only-human` | **`only-human`(固定。設定で変えられない)** |

- **締める**(例: `cmd:ticket:cancel = ask-human`)のは、どのコマンドでもいつでもできる。
- **緩める**のは下限まで。下限より緩い行は効かせず、`tb approval show` と `tb config set` の検査で警告する。
- 下限の表もコードに固定する(ADR 0005 の「人だけの部品はコードで固定する」と同じ)。
- `help`、`whoami`、`touch`、`role-context`(hook から呼ばれ、常に 0 で終わるもの)は門を通さない(`allow` 固定)。

### 4. 設定(既存の `approvals` に書く)

```
# 例
cmd:ticket:cancel  = ask-human   # worker に勝手に取り消させない
cmd:ticket:resolve = ask-any     # 下限が ask-human なので効かない(警告)
cmd:conv:*         = ask-any
send:task:*        = ask         # 既存の行(ask = ask-any)
```

- 新しい設定ファイルは作らない。上から最初に当たった行で決まる(今の approvals と同じ)。`cmd:*` の行が無ければ、コードの既定値。**`* = ask` のような全体の行は `cmd:` には効かせない**(既定値の表を飛ばして全部が承認待ちになるのを避ける)。`cmd:` で始まる行だけが門に効く。
- 書き換えるのは人だけ(`tb approval allow/ask` に `ask-any` / `ask-human` / `only-human` を書く口を足す。名前は `tb approval set <操作> <段階>` を推奨)。
- 一覧: `tb approval show [--by <agent>]` で、全コマンドの「操作名・既定・下限・今効いている段階と、その出どころの行」を出す(Web UI の /perms もこれを読む)。

### 5. 承認待ち(ask-any / ask-human)の扱い

- agent が `ask-any` / `ask-human` のコマンドを実行すると、**実行せずに** P-<n> を作り、終了コード 20(今の ask と同じ)で止める。記録するのは、依頼者・操作名・引数(argv)のハッシュと本文・時刻。
- 承認は `tb approval approve P-<n>`(却下は `reject`)。`ask-any` は人か `approve:<操作>` を持つ別の agent、`ask-human` は人だけ。自分の依頼は承認できない(今の approver_check と同じ)。
- 承認されたら、**依頼者が同じ引数でもう一度実行すると、1 回だけ通る**(依頼者・操作名・argv のハッシュが一致し、期限内で、未使用のときだけ)。承認者のプロセスで代わりに実行しない(実行者が入れ替わると、中の判定が承認者の権限で動いてしまうため)。承認したことは、既存の送信の仕組みで依頼者に知らせる。
- 期限は既定 24 時間(設定で短くできる。長くはできない)。
- **打ち直しのときに、もう一度判定する**: 承認を使うたびに、その時点の段階・下限・承認者の有効性・期限を読み直す。承認の後に段階が厳しくなった(例: `ask-any` → `ask-human`)、承認者の `approve:` の行が外れた、期限が短くなって切れた、のどれかなら、その承認は使えない(新しい P-<n> を作る)。`ask-human` と `only-human` の操作は、**人が承認した P-<n> でなければ、どの経路でも実行しない**。段階を `only-human` にした操作は、人の承認があっても agent からは実行しない。

### 6. 判定の場所

- **bash**: `taskboard/bin/tb` の `main` で、`cmd_<名前>` を呼ぶ直前(DB を開いた後)に門を 1 回通す。今は `TB_IMPL=go` のとき門より前に herdrctl へ渡すので、**門を herdrctl 側にも同じに置く**(`internal/approval` に `Gate`、`internal/tbcli` の入口で呼ぶ)。
- 2 語目(動詞)で分けるコマンドは、門が 2 語目まで読んで操作名を決める。動詞が分からないとき(使い方の誤り)は、門を通さずにコマンドに任せる(使い方の誤りは終了コード 2 のまま)。
- 各コマンドの中の `require_human` は、門に置き換えた後に外す(段 2)。外すまでは両方が効く(厳しい側)。
- fail-closed: 表・設定・台帳が読めない、pane の持ち主を確かめられないときは、`only-human` として扱う。

### 7. 記録

- 承認待ちの作成・承認・却下・使用は表(`perm_requests`)に残し、既存の `approval_changes` と同じく後から追える。段階の設定の変更は、`tb config` の履歴に残る。

## 検討した代替案

| 案 | 利点 | 欠点 |
|---|---|---|
| A. 今のまま(require_human を足していく) | 変更なし | 緩める・締めるのがコード変更。一覧が無い |
| B. 新しい設定ファイル `permissions` を作る | 見やすい | 判定の仕組みが 2 つになる(approvals と permissions のどちらが勝つか)。ADR 0005 の「判定の仕組みは増やさない」に反する |
| C. 段階を 5 つにし、「レビューを与える」も段階の 1 つにする | ユーザーの言い方にそのまま合う | 「このコマンドを使えるか」と「他人の依頼を承認できるか」は別の軸で、1 つの値では書けない(承認できる agent は依頼者ごとに違う)。既存の `approve:` の委任とも重なる |
| D. 承認者のプロセスで代わりに実行する | 依頼者がもう一度打たなくてよい | 中の判定・記録が承認者の名前で動き、権限昇格の経路になる |
| E. 下限を置かず、全部を設定で変えられる | 自由 | agent が設定を書ける状態になったとき(誤り・迂回)に、全部が緩む。`config set` 自体を緩めれば終わる |

## 結果(Consequences)

良い点:

- 全コマンドが同じ物差しで並び、`tb approval show` で一覧できる。
- 緩めるのも締めるのも設定の 1 行で済む。コードの変更は、コマンドを足すときの対応表の 1 行だけ。
- 「agent のレビューでよい」と「人の LGTM が要る」を書き分けられる。

悪い点・懸念:

- 門と中の判定の 2 層になる。既定を `allow` にしたコマンドでは中だけが効く、という読み方を覚える必要がある(`tb approval show` に「中で判定」と出す)。
- 承認後に依頼者が打ち直す手間がある(代わりに、実行者が入れ替わらない)。
- 古い名前と新しい名前の対応表の保守が要る。コマンドを足したのに表に無いと、テストで落とす(下の段 2)。
- 同じ OS ユーザーで動く agent が DB や設定を直接書く迂回は、本 ADR でも閉じない(ADR 0001 の、環境で強制する段で閉じる)。

## 移行の段階(案)

| 段 | 内容 | 他のスレッドとの関係 |
|---|---|---|
| 0 | この ADR | — |
| 1 | `approvals` の値に `ask-any` / `ask-human` / `only-human` を足す(`ask` = `ask-any`)。O-<n> / L-<n> の承認で、`ask-human` は agent に承認させない。bash と Go の両方 | agent-spawn の承認(grants・通知)を直す PR のマージ後に始める(同じ `approver_check` を触るため) |
| 2 | 対応表(全コマンド → 操作名・既定・下限)と門を、bash の `main` と herdrctl の入口に置く。既定は今と同じなので振る舞いは変わらない。`require_human` を門に置き換える。テスト: 全 `cmd_*` が表にある、下限より緩められない、別名で逃れられない、`TB_IMPL=go` でも同じ | Go 移行の PR とは、入口(`bin/tb` の `main`、`internal/tbcli` の `Dispatch`)だけが重なる。サービスを移す PR は、そのサービスの `require_human` を消さずにそのまま Go に移してよい(段 2 が後でまとめて外す) |
| 3 | 承認待ち P-<n>(ask-any / ask-human の門)、`tb approval set` / `show`、Web UI の /perms | — |

## 人が決めること

1. **段階の数と名前**: `allow` / `ask-any` / `ask-human` / `only-human` の 4 つにし、「レビューを与える権限」は `grants` の `approve:` に任せるか(推奨)。
2. **既存の `ask` の意味**: `ask-any` の別名にするか(推奨。今の振る舞いが変わらない)、`ask-human` にするか(委任していた承認が効かなくなる)。
3. **下限**: 上の 3 の表のとおり、権限・設定を書き換えるコマンドは `only-human` 固定、ほかの人だけのコマンドは `ask-human` まで緩められる、でよいか。
4. **承認後の実行**: 依頼者が打ち直す(推奨)か、承認者が代わりに実行するか。

## 確認できていないこと・推測

- コマンドの数(約 140)、`require_human` の箇所(約 40)、`approval_of` などの呼び出し(約 15)は、`taskboard/lib/*.sh` を grep で数えた概数。対応表を作る段 2 で正確に数える。
- 2 語目で分けるコマンド(`config`、`notify`、`job` など)の動詞ごとの既定は、段 2 で今の `require_human` の位置から 1 つずつ決める。
- P-<n> の「打ち直し」で、引数に時刻や乱数が入るコマンドがあるかは、確かめていない。

## 関連

- [ADR 0001](0001-enforce-agent-limits-by-environment.md): 制限は環境で強制する。本 ADR の門も、同じ OS ユーザーの迂回は防げない。
- [ADR 0002](0002-services-and-binary-distribution.md): サービスの境界。操作名の `<サービス>` はこれに合わせる。
- [ADR 0005](0005-minimal-parts-recipes-and-registered-grants.md): 判定の仕組みを増やさない、人だけのものはコードで固定する、の考えをコマンド全体に広げる。
- [docs/risk-based-approvals.md](../../taskboard/docs/risk-based-approvals.md): 送信のリスク評価。評価は ask を緩めない、の関係は本 ADR の段階にもそのまま当てはまる(評価は段階を厳しくする方向にだけ使う)。
