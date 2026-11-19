# ADR 0003: 通知・期限・アラート・提案をチケットのイベントと規則に寄せる

- 状態: Accepted
- 日付: 2026-11-19

## 要旨

- **決定**: チケット(`tasks`)を「人や agent が追いかける事柄」の中心にし、チケットの変化を新しい表 `task_events` に 1 件ずつ残す。外への通知は、各機能が決め打ちで呼ぶのをやめ、イベントに対する**通知の規則**(設定 `notify-rules`。人だけが書く)で決める。例: 「ラベル `notify` が付いたら Slack」「優先度 highest の bug ができたら Slack と Web UI の緊急通知」「期限切れになったら Web UI」。
- **寄せるもの**: 報告コメント(既にチケットに付いている)と期限(`due`)はそのままイベントにする。アラート(A-)と提案(P-)は**元の表をやめてチケットに移す**(人の決定 2026-11-19)。チケットで表せない部分(解析役の起動の承認・指紋による重複のまとめ・解析結果、提案の配送)だけを、チケットに付く小さな表に残す。PR の監視は、PR の URL を外部リンクに持つチケットへイベントとして付ける。
- **寄せないもの**: 承認(送信 O-・agent 操作 L-・PR 作成 PQ-・代行依頼・解析役の起動)と、保留した送信(inbox)。承認は人の判断が安全の境界(ADR 0001 の L0)で、agent が書けるチケットの状態で代用すると境界が崩れる。inbox は仕事ではなくメッセージ。
- **規則がないときの動きは今と同じ**: 既定の規則は今の決め打ち(質問・ブロック・完了・アラート)をそのまま書き直したもの。`notify-rules` を書かなければ挙動は変わらない。

## 状況(Context)

チケットに種別・優先度・ラベル・外部リンク(#104)、期日(#119)が入り、スプリントとボード(#124)も進んでいる。一方で、人や agent に知らせる仕組みは機能ごとに別々に育った。

| 機能 | 置き場 | ID | 人に知らせる経路 | 今の決まり方 |
|---|---|---|---|---|
| タスク | `tasks` | T- | 完了の通知(notify の `done`)、指揮官へのまとめ(digest)、Web UI | `tb done` が決め打ちで通知を積む |
| 報告コメント | `comments` | C- | 質問・ブロック・完了を notify へ、Web UI の緊急通知 | `tb comment` が決め打ちで積む |
| アラート | `alerts` | A- | 登録と解析結果を notify へ、digest、Web UI(critical / high の結果、起動待ち) | `tb alert` / `tb alert-result` が決め打ちで積む |
| 提案 | `proposals` | P- | 設定役(人事)の agent へ直接送る | `tb proposal` が送る。届かなければ digest が再送 |
| PR の監視 | `notify_seen`(前回の状態) | owner/repo#n | 作成・approve・変更依頼・マージを notify へ | `tb pr-watch` が差分から積む。チケットとは結ばれていない |
| 承認待ち | `outbox`(pending)・`agent_ops`・`pr_queue`・`proxy_requests`・`alerts`(awaiting-approval) | O- L- PQ- など | notify の `approvals_pending`、Web UI の緊急通知 | 定期の `notify_periodic` と Web UI の集計 |
| 期限 | `tasks.due`(#119)、`deadlines.conf`(#116、未マージ) | — | `tb list --overdue`、Web UI の絞り込み、`tb reminders`(#116) | 見に行かないと分からない。期限切れになった瞬間の通知はない |
| 忘れているかも | `tb reminders`(#116、未マージ) | — | まとめを出す・Web UI(#122) | 各表を読むだけの集計 |
| 保留した送信 | `inbox`(#112、未マージ) | I- | `tb inbox`、Web UI(#122) | 設定 `delivery` |
| 観測(idle / stuck) | `observations` | — | digest | digest が決め打ちで観測 |

困っていること:

- **通知の条件を人が選べない**。種別ごとの届け先(`sinks_<種別>`)は選べるが、「このプロジェクトの bug だけ」「このラベルのものだけ」は書けない。新しい条件は毎回コードの変更になる。
- **チケットの履歴がない**。`tasks` は今の状態と `updated_at` だけで、「いつ誰が blocked にしたか」「いつラベルが付いたか」が残らない。通知も、期限切れの検出も、振り返りも、変化の記録がないので作りにくい。
- **同じ事柄が複数の場所に散る**。アラートの対応や提案の対応は、チケットにすると担当・優先度・期日・スプリント・ボードがそのまま使えるのに、今は別の表の別の状態で追っている。
- **PR とチケットが結ばれていない**。チケットの外部リンクに PR の URL を書いても、マージされたことはチケット側からは見えない。

人の要望(2026-11-19): チケット機能ができたので、通知などをチケットに統合できないか調べてほしい。理想で考えてよい。「このラベルが付いたら通知を出す」もありだと思う。

## 決定(Decision)

### 1. チケットのイベント(`task_events`)

チケットを変える操作は、同じトランザクションで 1 件のイベントを残す。

| 列 | 内容 |
|---|---|
| `id` | 連番(E-<n>) |
| `herdr_session` | セッション(他の表と同じく行で分ける) |
| `task_id` | T-… |
| `kind` | `created` / `status` / `label_added` / `label_removed` / `priority` / `owner` / `due` / `sprint` / `link` / `ref_added` / `comment` / `overdue` / `due_soon` / `pr_opened` / `pr_merged` など |
| `old` / `new` | 変わる前と後の値(文字列。ラベルならそのラベル、状態なら状態名) |
| `actor` | 変えた者。agent の pane からなら herdr から引いた agent 名(`tb comment` と同じ方法。引けなければ `pane:<id>`)、そうでなければ `human`(既存の `task_links.created_by` と同じ)、定期のジョブなら `system` |
| `created_at` | 時刻 |

- イベントは追記だけ。消さない、書き換えない。
- `tb history T-…`(と Web UI のタスク詳細)で時系列に見られる。
- 期限は「なった瞬間」を定期のジョブ(既存の `notify-flush` と同じ間隔)が見つけ、チケットごと 1 日 1 回だけ `overdue` / `due_soon` を残す。
- PR の監視は、PR の URL を `refs` に持つチケットがあれば、そのチケットに `pr_opened` / `pr_approved` / `pr_changes` / `pr_merged` を残す(チケットの状態は変えない。マージで done にするかは人か担当が決める)。チケットが無い PR は今と同じく notify だけ。

### 2. 通知の規則(`notify-rules`)

通知は「イベント × 条件 → 届け先」の規則で決める。規則は設定(`config` 表のキー `notify-rules`。`tb config` で人だけが書く。#013 と同じ)に置く。

```text
# <イベント> [条件 ...] -> <届け先 ...>
comment kind=question                   -> slack webui      # 今の既定と同じ
comment kind=blocked                    -> slack webui
status new=done                         -> slack
label_added label=notify                -> slack
created type=bug priority>=high         -> slack webui
overdue                                 -> webui conductor
pr_merged project=web                   -> slack
label_added label=urgent actor=human    -> slack webui      # 人(pane の外)が付けたときだけ
```

- **条件**: `type` / `priority`(`>=` で順序)/ `label`(持っている)/ `project` / `owner` / `kind` / `new` / `old` / `actor`(名前、`human`、`system`、`agent`)。すべて満たした行が当たる。当たった行の届け先を合わせる(重複は 1 回)。
- **届け先**: 今の notify の `file` / `stdout` / `slack` に、`webui`(Web UI の緊急通知と Chrome 通知に出す)と `conductor`(そのプロジェクトの指揮役への次のまとめ = digest に載せる)を足す。
- **誰が付けたラベルでも当たる**(人の決定 2026-11-19)。agent が付けたラベルでも通知は出る。絞りたい規則だけ `actor=human` を書く。
- **本文はイベントから決定論で作る**: `[ラベル notify] T-web-012 ログイン画面 (high / bug / 担当 worker-2)` のように、ID・題・種別・優先度・担当・イベントだけ。説明(`description`)やコメントの本文は、今の notify と同じ規則(先頭 2 行・長さの上限)でしか載せない。秘密らしい値の検出、伏せ字、頻度の上限、重複の抑え(`dedupe_sec`)は今の notify をそのまま通す。
- **既定の規則**: `notify-rules` が無いときは、今の決め打ちと同じ規則を使う(質問・ブロック・完了・アラートの登録と結果)。`tb notify rules` で、今効いている規則と出どころ(設定 / 既定)を出す。`tb notify rules test E-<n>` で、そのイベントにどの規則が当たるかを送らずに確かめる。
- `notify` の `enabled = off` のあいだは今と同じく何も外へ出さない(`webui` と `conductor` は内部なので、off でも出すかは段階 2 で決める。初めは off なら何も出さない)。

### 3. アラートと提案はチケットに移す(元の表をやめる)

人の決定(2026-11-19)により、`alerts` と `proposals` の表をやめ、チケットに移す。チケットが持てる項目(題・本文・状態・優先度・担当・ラベル・期日・スプリント・履歴)はチケットに置き、チケットで表せない部分だけを、チケットの ID を主キーにした小さな表に残す。

**アラート**

| 今の `alerts` | 移した先 |
|---|---|
| 題・本文 | チケットの題・説明(`description`) |
| 状態 `new` / `awaiting-approval` / `approved` / `queued` | チケットは `todo`。細かい段階は `alert_details.stage` |
| 状態 `analyzing` | チケットは `in_progress`、担当(`owner_agent`)は解析役 |
| 状態 `analyzed` | チケットは `review`(人が見て閉じる番) |
| 状態 `closed` | チケットは `done` |
| 緊急度(解析役が決める) | 優先度に写す(critical → highest、high → high、medium → medium、low → low。解析前は high)。元の値は `alert_details.severity` |
| 指紋・件数・初回 / 最終 | `alert_details`(`fingerprint`、`count`、`first_seen`、`last_seen`)。同じ指紋の未解決のチケットがあれば件数を増やすだけ(今と同じ) |
| 解析結果(要約・報告・パス・誰が・届けたか) | `alert_details`。要約はチケットのコメント(`--kind done`)にも残す |
| 報告元 | チケットの作成の履歴(`task_events` の `actor`)と `alert_details.source` |

- チケットは project `alert`(設定で変えられる)、種別 `bug`、ラベル `alert`。ID は `T-alert-<n>`。
- `A-<n>` は**別名として残す**(`alert_details.alert_no`)。`tb alerts A-3`、`tb alert-approve A-3`、通知の `ref`、役割の文面はそのまま使える。
- **人だけが通せる操作はチケットの状態で代用しない**。解析役の起動の承認は `alert_details.stage` を `tb alert-approve`(人だけ)が変える。閉じる(`done`)のは今の `tb alert-close` と同じく人だけで、agent が `tb done` / `tb update --status` で alert のチケットを閉じようとしたら止める。
- 既存の行は移行で移す(同じ番号を `alert_no` に入れる)。Web UI・digest・notify・limits の読み先をチケットに変える。

**提案**

| 今の `proposals` | 移した先 |
|---|---|
| 題・本文 | チケットの題・説明 |
| 対象の役割ファイル | ラベル `role:<ファイル名>`(全体なら `role:-`) |
| 状態 `open` / `applied` / `rejected` | `todo`(担当は設定役)/ `done`(要約に「適用」)/ `done`(要約に「見送り」とラベル `rejected`) |
| 提案者 | 作成の履歴の `actor` と `proposal_details.by_name` |
| 届けた時刻 | `proposal_details.delivered_at`(届かなければ digest が再送。今と同じ) |

- チケットは project `proposal`、種別 `task`、ラベル `proposal`。`P-<n>` は別名として残す(`proposal_details.proposal_no`)。

**実装(段 4a / 4b、スキーマ 019)**

- `alerts` / `proposals` は**表をやめ、同じ列の互換ビュー**にした(中身はチケットと `alert_details` / `proposal_details`。`id` は `A-<n>` / `P-<n>` の番号、`task_id` でチケットを指す)。
  digest・limits・notify・review・Web UI・関係グラフなどの読み手は SQL を変えずに読める。ビューも、このセッションの行だけの一時ビューで絞る(`taskboard/sql/session_views`)。
- 書き込みは今までどおり `main.alerts` / `main.proposals` に書き、スキーマの INSTEAD OF トリガーがチケットと小さな表へ振り分け、段階・緊急度をチケットの状態・優先度・担当に写す。
  写すのは変わった項目だけ(人がチケットで変えた題や優先度を、関係ない更新で戻さない)。振り分けをスキーマに置くので、bash の tb・Go・手で開いた sqlite3 のどれから書いても同じに写る。
- 案からの変更: project 名は `alert` / `proposal` に固定(トリガーが設定を読めないため)。解析結果の要約はコメントではなくチケットの要約(`summary`)に入れる。閉じた・適用した提案は完了報告(digest の完了の知らせ)に出さない(報告済みにする)。
  チケットの状態を変える一般のコマンド(`tb claim` / `update --status` / `done` / `block`)は、人の端末からでもアラート・提案のチケットには使えない(専用のコマンドを案内する)。

### 4. 寄せないもの

| もの | 寄せない理由 | 代わりにすること |
|---|---|---|
| 承認待ち(O- / L- / PQ- / 代行依頼 / 解析役の起動) | 承認は人だけが通せる操作で、ADR 0001 の L0 の土台。チケットの状態は agent も書けるので、チケットで承認を表すと境界が崩れる | 承認の表に `task_id` がある(`outbox` / `pr_queue` / `proxy_requests`)ので、チケット詳細から辿れるようにする。イベントは `approval_requested` / `approval_decided` をチケットに残す(状態は変えない) |
| 保留した送信(inbox、#112) | 仕事ではなくメッセージ。宛先の agent だけが読める設計で、チケットは誰でも読める | 今のまま |
| 観測(idle / stuck)と digest | agent の状態でチケットの状態ではない | digest は届け先 `conductor` として規則から使う側になる |
| 忘れているかも(#116) | 集計の画面・まとめで、置き場ではない | チケットに関わる材料(期限・停滞・質問)を `task_events` から読むようにする(#116 のマージ後) |

## 検討した代替案

| 案 | 利点 | 欠点 |
|---|---|---|
| A. 今のまま(機能ごとに決め打ち) | 追加のコストがない | 通知の条件が増えるたびにコード変更。チケットの履歴がない |
| B. 承認・inbox もチケットに移す | 置き場が 1 つで分かりやすい | 承認の境界が崩れる。`tasks` の状態の制約(5 状態)に、送信済みなどを押し込むことになる。並行する PR(#112 / #116 / #122)とぶつかる |
| B'. アラート・提案は元の表を正にし、チケットを自動で作って結ぶ(当初の推奨) | 変更が小さい | 同じ事柄が 2 か所にあり、同期の向きを覚える必要がある。人の決定で採らない |
| C. 通知の規則だけを足す(イベントの表は作らない) | 小さい | 期限切れ・PR のマージのように「チケットの操作でない変化」を規則に乗せられない。履歴が残らない |
| D. 外の仕組み(Jira など)へ移す | 機能が揃っている | 決定論の実行者・秘密の検出・セッションの分離など、この仕組みの前提を外に持ち出せない |

## 結果(Consequences)

良い点:

- 人が「何を知りたいか」を設定だけで決められる(ラベル・優先度・種別・プロジェクト・担当・期限)。
- チケットの履歴が残り、振り返り(いつ詰まったか、どれくらい review にいたか)ができる。
- アラートと提案に、担当・優先度・期日・スプリント・ボードがそのまま使える。
- PR のマージがチケット側から見える。

悪い点:

- チケットを変える全ての書き込みに、イベントの書き込みが 1 つ増える(同じトランザクション。件数は少ないので負荷は小さいと見ている。**推測**)。
- 規則の書き方を覚える必要がある(`tb notify rules test` で確かめられるようにする)。
- agent がラベルを付けられるので、ラベルで決まる通知は agent も引き起こせる(下の「人が決めること」)。

## 移行の段階(案)

| 段 | 内容 | スキーマ | 依存 |
|---|---|---|---|
| 1 | `task_events` と書き込み(作成・状態・ラベル・優先度・担当・期日・リンク・外部リンク・コメント)、`tb history`、Web UI のタスク詳細の履歴 | 新しい表 1 つ | なし |
| 2 | 通知の規則(`notify-rules`、既定の規則 = 今の挙動)、届け先 `webui` / `conductor`、`tb notify rules [test]`。今の決め打ちの hook を規則の評価に置き換える | なし(`config` 表を使う) | 1 |
| 3 | 期限のイベント(`overdue` / `due_soon`)を定期のジョブで残す | なし | 1、2 |
| 4a | アラートをチケットへ移す(`alert_details`、移行、`A-<n>` の別名、人だけが閉じる。読み手は互換ビュー `alerts` でそのまま) | `alert_details`、`alerts` の行を移して表を互換ビューにする | 1 |
| 4b | 提案をチケットへ移す(`proposal_details`、`P-<n>` の別名。互換ビュー `proposals`) | `proposal_details`、`proposals` の行を移して表を互換ビューにする | 1 |
| 4c | PR の監視を `refs` の URL でチケットに結ぶ | なし | 1 |
| 5 | `tb reminders`(#116)のチケットの材料を `task_events` から読む。`deadlines.conf` を担当なしのチケット + 期日に寄せる | なし | #116 のマージ、3 |

- スキーマの番号は、マージの時点の main の次の空き番号にする(#112 / #121 / #124 がそれぞれ 015 を使っているため)。
- 各段は「規則も設定もなければ今と同じ」を守る。
- ADR 0002(Go のバイナリへの移行)とは、表と設定の形で受け渡すので、どちらの実装にも載る。

## 人の決定(2026-11-19)

1. **ラベルで決まる通知は、誰が付けたラベルでも出す**。agent が付けたラベルでも当たる。本文は決定論で作り、秘密の検出・頻度の上限は必ず通る。絞りたい規則だけ `actor=human` を書く(pane の外かどうかは環境変数で決まるので、強い保証ではない)。
2. **アラートと提案はチケットに完全に移す**(上の 3)。人だけが通せる操作(解析役の起動の承認、アラートを閉じる)はチケットの状態で代用せず、別の表と人だけのコマンドに残す。

## 未確認・推測

- **推測**: イベントの書き込みによる遅れは、今の書き込みの待ち(最大 5 秒)に比べて無視できる。段 1 のテストで件数を増やして確かめる。
- **未確認**: Web UI の緊急通知(`urgent_items`)に規則由来の項目を足したとき、Chrome 通知の重複の抑え(id ごと)がそのまま効くか。段 2 で確かめる。

## 関連

- [ADR 0001](0001-enforce-agent-limits-by-environment.md): 承認をチケットに寄せない理由(L0)。
- 外への通知の設定: [`taskboard/config/notify.conf.example`](../../taskboard/config/notify.conf.example)。
- 並行する PR: #112(保留した送信と inbox)、#116(忘れているかも)、#122(Web UI に保留と忘れているかも)、#124(スプリントとボード)、#115(ADR 0002)。
