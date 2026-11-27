# ADR 0010: 使われていない agent を眠らせ、用があるときに起こす

- 状態: Proposed(2026-11-27。人が「protect と always-on を分ける」を選んだ)
- 日付: 2026-11-27
- 関係するもの: ADR 0001(環境で強制する)、ADR 0003(チケットのイベントと規則)、ADR 0007(主体と 1 つの policy)、ADR 0008(アプリケーション API。agent.stop / agent.revive は段 1 で `internal/app/agent` に移った)

## 要旨

- **困りごと**: 1 日に 2〜3 回しか声がかからない agent(保護している指揮役・相談役を含む)も、ずっと pane を持って動いている。動作中の数は `max_running_agents` を食い、メモリも使う。一方、止めると今は「誰かが `tb agent-revive` するまで届かない」ので、止められない。
- **決定(案)**:
  1. 停止とは別に **眠り(sleep)** を足す。眠りは「pane を閉じるが、会話(session)・役割・親子・作業ディレクトリ・置き場所を台帳に残し、用があれば仕組みが起こしてよい」状態。台帳の `state` に `sleeping` を足し、`stopped`(人や判定役が止めた)と分ける。
  2. **起こすのは 1 か所**: 眠っている agent への届け物(`tb send` / 返信 / nudge / 承認の結果 / scheduler のプロンプト)は、どれも「届ける直前」に同じ関数 `agent.Wake` を通す。起こしたあと、届け物を **最初の指示** として渡す(届け方の規則 delivery で hold / block になるものは起こさない)。
  3. **眠らせるのも 1 か所**: scheduler の job(`agent-sleep`)が、条件(下記)を満たす agent を眠らせる。agent の判断や `--force` では眠らせない。
  4. **protect の意味を分ける**: protect は「消さない・会話を捨てない(`--fresh` にしない)・人以外が止めない」にする。眠りは会話を捨てないので protect とぶつからない。眠らせたくない agent は新しい設定 `always-on` に書く。
  5. 起こすのは **会話の再開(resume)だけ**。再開できなければ新しい会話にはせず、人に知らせて届け物を outbox に残す。
- **段階**: 段 A(台帳と Wake / Sleep の操作)→ 段 B(送信・nudge・scheduler を Wake に通す)→ 段 C(job と Web UI)。ADR 0008 の段 1(agent)の後に、`internal/app/agent` の操作として足す。

## 状況(Context)

人の発言(2026-11-27): 待機している agent(削除保護しているものを含めて)を一度殺して、必要になったら叩き起こす運用をしてよいのでは。1 日に 2〜3 回しか叩かれないものは、いったん殺しておいて必要になったら起こす。

## 分析(main 71da126、2026-11-27)

### 1. 止める・戻すの部品はもう揃っている

- `agents.ClosePane` は pane を閉じて台帳を `stopped` にするが、`session_id`・`cwd`・`model`・`role`・`spawned_by` は消さない。
- `agent.revive`(`internal/app/agent/revive.go`、`life.go`)は台帳から kind・cwd・model・session を読み、`--resume <session>` で同じ会話に戻し、`--prompt` を最初の指示として送る(`FirstPrompt`)。cwd が無ければ止まる。
- `agent.restart` は「停止 + 同じ会話での復活」を安全条件つきで行う。

つまり **眠り = stop、起こす = revive(resume + 最初の指示)** で、新しく書く中身は少ない。足りないのは「誰が、いつ、どの権限で」だけ。

### 2. 今、止まっている agent に届けようとすると

| 経路 | 今の動き(止まっている宛先) |
|---|---|
| `tb send` → `message.Deliver` → `transport.Send` | `herdr agent get` が `agent_not_found` → 未送信で outbox に残る。`tb flush` まで届かない |
| nudge(`reminder/nudge.go`) | 「herdr にいない」で skip |
| scheduler のプロンプト job(`sched/tick.go` の `agentState`) | `TARGET が一意に解決できない` で失敗 |
| チケットの割り当て・claim | 担当名が入るだけ。知らせは nudge 任せ |
| 承認の結果 | 依頼者への送信(上と同じく outbox に残る) |

どの経路も最後は「宛先が herdr にいない」で止まる。**ここに 1 つの関数を挟めば全部の経路で起こせる**。scheduler のプロンプト job は、状態を `herdr agent list` で直接見てから `tb system-send` を呼ぶ(outbox を作らない)。状態を見る前に Wake を通すように直す。

### 3. 眠らせるとぶつかるもの

- **agent-shed の orphan**: 「親が running でない子」を止める候補にする。親が眠っただけで子が候補になってしまう。`sleeping` を `running` と同じに数える必要がある(多対多の親の設計でも同じ。親が眠っていることは「親がいない」ではない)。
- **protect**: 今は「--force でも止めない」。眠りを protect が止めると、いちばん眠らせたい指揮役・相談役が眠れない。
- **killable の stale 判定**: `killable_session` と `session_id` を比べる。resume で session_id が変わる kind では、起こすたびに判定が無効になる(claude の `--resume` が同じ ID を保つかは要確認)。
- **max_running_agents**: 起こすと 1 つ増える。上限なら起こせない。

### 4. 起こすコスト(推測を含む)

- **時間**: プロセスの起動・MCP の接続・会話の読み込みで、数秒〜数十秒(要計測)。届け物はその分遅れる。1 日 2〜3 回の相手なら問題にならない。
- **トークン**: 長く待っていた agent は、プロンプトキャッシュの期限(既定で数分)をとうに過ぎている。動かしたままでも次の 1 回目は会話全体を読み直すので、**眠らせても増えない**(推測。同じ会話を resume したときの読み直しが、動いたままの 1 回目と同じ量かは要確認)。
- **失うもの**: pane の置き場所(tab・並び)、MCP の一時的な状態、画面のスクロール。置き場所は `agent_activity` の `tab_id` / `workspace_id` と、PR #285 の置き場所の指定で戻せる。

## 決定(案)

### 1. 台帳

- `agents.state` に `sleeping` と `waking` を足す(`running` / `sleeping` / `waking` / `stopped`)。`sleeping` の意味は「pane は無いが、仕組みが起こしてよい」。`waking` は「起こしている途中で、まだ届けられない」。
- `agent_ops.op` に `sleep` / `wake` を足す。誰が・何の届け物で起こしたか(`args` に outbox の ID など)を残す。
- 眠らせるときに置き場所(tab・workspace)を `agents` に残す。
- agent ごとの **借用(lease)** を 1 行で持つ(表 `agent_leases`: 名前・持ち主・期限)。届け物・眠り・起こすの 3 つは、状態の確認から外への操作(送信、pane を閉じる、起動)が終わるまでを、この借用の中で行う。取るのは条件つきの INSERT / UPDATE(空いているか、期限が切れているときだけ)。

### 1a. 借用の決まり

- 借用を持つ操作の ctx の期限(ADR 0008 で herdr の呼び出しまで通した期限)は、借用の期限より短くする。期限を過ぎた持ち主は、herdr を呼ぶ前に止まる。
- herdr の子プロセスを期限で止めても、herdr の側で処理が続いている恐れがある(pane を閉じたかどうか分からない、など)。**結果が分からないまま借用が切れたときは、自動で引き継がない**。台帳の状態を `stopped` にし、`agent_ops` に「結果不明」を残して人に知らせる。届け物は outbox に残す。
- 借用を取れなかった届け物は outbox に queued で残す(`tb flush` で再送)。取れなかった眠りは、その回は取りやめる。

### 2. 眠らせる条件(job `agent-sleep`、既定は 5 分ごと)

全部満たすものだけ(借用を取ったあと、pane を閉じる直前に 1〜6 をすべてもう一度確かめる。判定のときから 1 つでも変わっていたら閉じず、次の周期で判定し直す。チケットの割り当てや承認の依頼は借用を使わずに書かれるので、確かめ直しは閉じる直前の 1 回にまとめる):

1. herdr の状態が `idle` か `done`。
2. 最後のプロンプト(`agent_activity.last_prompt_at` と `last_stop_at` の新しいほう)から `sleep_after` 分(設定 limits、既定 120)たっている。
3. 担当の `active` / `review` / `blocked` のチケットが無い。承認待ちの依頼(agent_ops・approvals の pending)を出していない。
4. `always-on` に当たらない。
5. kind が resume に対応している(`KindRequire(kind, "resume")`)。会話の session が台帳にある。
6. 自分の子(`spawned_by`)が動いていない(子からの報告を受ける親は起きておく)。

### 3. 起こす(`agent.Wake`)

- 呼ぶ場所: `transport.Send` の手前(`message.Deliver`・nudge・承認の結果・`tb flush` が通る)と、scheduler のプロンプト job。scheduler は `herdr agent list` で状態を見て、idle / done でなければ飛ばすので、**状態を見る前に** Wake を呼ぶ(宛先が sleeping なら、権限と delivery の判定を通してから起こし、起きたあとで状態を見る)。
- 起こすのは `state='sleeping'` の agent だけ。`stopped` は今までどおり起こさない(人や判定役が止めたものを勝手に戻さない)。
- 順番: delivery の判定(block / hold なら起こさない)→ 借用を取る → **枠の確保**(下)→ 台帳を `sleeping → waking` → `revive`(resume のみ)→ 届け物を最初の指示として送る → 台帳を `running`、outbox を sent にする → 借用を返す。
- **枠の確保**: `max_running_agents` の検査と、台帳を `waking` にする UPDATE を、1 つのトランザクション(SQLite の `BEGIN IMMEDIATE`。DB 全体で直列になる)で行う。`running` と `waking` を数えて上限なら確保せず、届け物を outbox に残す。別の agent への同時の Wake でも上限を越えない。**ふつうの起動(`agent.revive`・`agent.new`・`agent.spawn`)も、実際に起動する直前に同じ枠の確保を通す**ようにする(今は申請のときに数えるだけで、`ReviveContext` は数えずに running にしている)。これで、どの経路から起動しても上限を越えない。
- 同じ agent に同時に 2 つ届いたら、借用を取れたほうだけが起こす。もう一方と、`waking` の宛先への届け物は、送らずに outbox に queued で残す。起動に成功したら flush が古い順に届ける。
- 上限・借用を取れないときの扱いは経路ごとに今の決まりに合わせる:

| 経路 | 届かなかったとき |
|---|---|
| `tb send` / 返信 / 承認の結果 | outbox に queued で残し、`tb flush` で再送 |
| nudge | 今のまま取り下げる(`denied`)。古い催促を後から届けない |
| scheduler のプロンプト job | その回を飛ばす(`skipped_state`)。次の回に任せる |

### 4. 権限

- `agent:sleep:<名前>` と `agent:wake:<名前>` を操作名に足す(ADR 0007 の policy)。既定は、仕組み(scheduler の主体)と人に allow、agent からは deny。
- 起こすのは「届け物を送ってよい相手」に対してだけ起きる。送信の権限(`send:<種類>:<宛先>`)を通った届け物だけが起こせるので、起こすこと自体に別の承認は求めない。
- 眠りは revive の承認待ち(opGate)を通らない。人が承認して始めた agent を仕組みが一時的に寝かせて戻すだけで、新しい agent を作らないため。

### 5. protect と always-on

| 設定 | 意味 | 眠り |
|---|---|---|
| protect(変更) | 消さない・`--fresh` で会話を捨てない・人以外は stop しない | してよい |
| always-on(新規) | ずっと起きている(眠らせない) | しない |

既定の always-on は空。指揮役を眠らせたくない人は `role:conductor` などを書く。移行: 設定 `always-on` がまだ無いあいだは、今の protect の agent を always-on とみなす(何も変えなければ今の動きのまま)。

### 6. 起こせなかったとき

- cwd が無い・session が見つからない・起動に失敗: 新しい会話にはしない。`state` を `stopped` にし(`waking` から戻す)、届け物は outbox に残し、人に知らせる(notify)。
- 上限: outbox に残すだけ(失敗にしない)。

### 7. 見せ方

- `tb agents` と Web UI で `sleeping` / `waking` を別に見せる(最後に起きた時刻、起こされた回数)。
- `tb agent-sleep <名前>` / `tb agent-wake <名前>`(人が手で)。名前は `tb agent <サブコマンド>` の決まりに合わせて実装時に決める。

## 段階

- 段 A: 台帳(`sleeping` / `waking`、op の追加、借用の表)、`agent.sleep` / `agent.wake` の操作(中身は Stop / Revive を呼ぶ)、agent-shed の orphan で `sleeping` を親が居ると数える。
- 段 B: `message.Deliver`・nudge・scheduler を Wake に通す。2 重起動の防止。
- 段 C: job `agent-sleep`、`always-on` の設定と移行、Web UI。

各段で、眠りを使わない(job を入れない)なら今の動きが変わらないことを条件にする。

## 他の作業との関係

- **agent セット(ADR 0008 の上の入口)**: セットの起動で「起動するが最初は眠らせておく」を選べると、ワンコマンドで全員を立てても常駐は必要なものだけになる。セットは `agent.new` のあとに `agent.sleep` を呼ぶだけでよい。
- **多対多の親**: 「親が居るか」の判定は `running` か `sleeping` で行う。子の報告が眠っている親に届くと、親が起きる。

## 検討した代替案

- **ずっと動かしたまま(今のまま)**: 何も壊れないが、上限とメモリを食う。待ちの多い役割が増えるほど効く。
- **stop を流用し、止まっている agent はすべて自動で起こす**: 人や判定役が意図して止めたものまで戻ってしまう。`sleeping` を分ける。
- **起こすときに新しい会話で立て直す**: 役割の文書は読み直せるが、それまでの会話(何を頼まれ、何を返したか)を失う。protect の意味に反する。
- **agent が自分で眠る(`tb agent-sleep` を自分の pane から)**: 自分を止める操作は pane の中からはできない(ADR 0005 の状況と同じ)。仕組み側の job にする。

## 結果(Consequences)

- 良い: 待ちの多い agent が上限とメモリを使わない。保護している agent も会話を保ったまま眠れる。届け物の経路が 1 か所にまとまる。
- 悪い: 起きるまでの数秒〜数十秒、届け物が遅れる。起動の失敗という新しい失敗の形が増える(人に知らせる)。

## 人が決めること

1. ~~protect の意味を「消さない・会話を捨てない」に変え、眠らせない指定は always-on に分けてよいか~~ → 決定(2026-11-27): 分ける。
2. `sleep_after` の既定(推奨: 120 分)。
3. 指揮役(設定 projects の宛先)を既定で always-on にするか(推奨: しない。移行のあいだは protect の agent が always-on とみなされる)。

## 確認できていないこと・推測

- claude の `--resume` が同じ session ID を保つか(killable の stale 判定と、台帳の session の更新に効く)。
- 起きるまでの時間(MCP の接続を含む)。借用の期限はこれより長くする必要がある。
- herdr が子プロセスを止められたあとも pane close / agent start を続けるか(続けるなら「結果不明」は人に回す、という今の決まりで足りる)。
- resume したときの会話の読み直しの量が、動いたままの 1 回目と同じか。
- codex / gemini の resume の対応(`KindSupports`)。対応していない kind は眠らせない。
