# taskboard — herdr agent 用の最小タスク表

SQLite 1 ファイル(既定は `~/.local/state/herdr/tasks.db`。checkout をまたいで 1 つ)に仕事の状態を持ち、`bin/tb` で読み書きする。
herdr のセッションは同じ DB の中で行ごとに分ける(下の「セッションと DB」)。
DB は WAL モードで開く(最初の tb が切り替え、DB ファイルに残る)。同時に動くセッションの読みと書きが互いを待たない。書きは最大 5 秒、hook の活動記録は 1 秒までロックを待ち、それを過ぎた活動記録は `touch-errors.log` に `db-locked` と残して捨てる。DB の横の `tasks.db-wal` / `tasks.db-shm` は SQLite のもので、消さない(DB を複製するときは `sqlite3 tasks.db ".backup 写し.db"`)。
依存は `sqlite3` / `bash` / `jq` のみ。

- 「何を誰に送ったか」の監査ログ `dispatch.jsonl` とは別物。こちらは「仕事の状態」
- `done`(herdr の agent_status)は turn の終わりしか意味しない。**タスクの完了は worker が `tb done` + `tb report` で明示する**

## worker 向け: 終了時に打つ 2 行

```bash
tb done   T-<project>-NNN -d <成果物のパスか URL> [-d <もう 1 つ>] -m "要約(1〜2 文)"
tb report T-<project>-NNN
```

- `-d` と `-m` は必須(欠けると done にならない)
- `report` は完了の報告を done の報告コメント(`C-<n>`。`tb comment --kind done` と同じもの)にして指示役へすぐ届ける(`[from: <担当> / - / T-xxx / done] 要約 / 成果物: パス`)。打たなくても `tb digest` が 10 分おきにまとめて届ける。終了コードで結果が分かる:

| code | 意味 | worker がやること |
| --- | --- | --- |
| 0 | 送信済み(または報告済み) | 終了してよい |
| 10 | 宛先の指示役が working / blocked 等で**未送信** | 何もしない。`pending` に残り、あとで回収される(必要なら少し待って再実行) |
| 11 | 送信または宛先の状態取得に失敗 | 同上。繰り返し 11 なら指示役の名前(設定 `projects`。`tb config show projects`)を確認 |

`reported_at` は done の報告コメントが届いたとき(`report` か `digest`)だけ入る。10 / 11 のときは空のまま、作った報告コメントが `tb pending` に `C-<n>` で出る(同じ報告を 2 回作らない)。

`tb` は PATH に無いので、`taskboard/bin/tb` のフルパスで呼ぶか PATH に足す。

### 作業の始め方(任意)

```bash
tb claim T-<project>-NNN --owner <自分の agent 名> [--session <session id>] [--account <max|enterprise>]
tb update T-<project>-NNN --status review     # todo|in_progress|review|blocked
tb block  T-<project>-NNN -m "詰まっている理由"
```

### 取り消し(cancelled)

やらないと決めたタスクは `tb cancel` で閉じる(状態 `cancelled`)。

```bash
tb cancel T-<project>-NNN -m "取り消す理由"
```

- `done` と同じく閉じた状態として扱う: `tb update` / `tb block` / `tb done` / `tb claim` はできない。期限切れ・期限間近(`tb list --overdue` / `--due-within` / `tb due-scan` / Web UI)、`tb nudge`、未対応の報告の数え上げには出ない。
- `blocks` の先行タスクが `cancelled` になれば、後続は claim できる(先行待ちが解ける)。
- 理由は要約(`summary`)に残る。完了ではないので完了報告(`tb digest` / `tb report` / `tb pending`)には出さない。
- スプリントの件数・ポイント(`tb sprints` / Web UI のボード)の分母には数えない。`tb board` には `cancelled` の列が最後に出る。
- アラート・提案のチケットには使えない(それぞれのコマンドで閉じる)。

## 指示役 / 人向け

```bash
tb add -p <project> -t "タイトル" [--parent T-..] [--owner N]   # ID を表示(T-<project>-NNN)
tb list [-p project] [-s status] [--json]
tb show T-<project>-NNN
tb pending [--json|--ids]     # done なのに未報告のもの・未配送の報告コメント(id / project / 宛先 / 要約)
tb pending --ids | xargs -n1 tb report    # 未報告の再送(tb digest でもまとめて届く)
```

### 今どこを向いているか(`tb whoami`)

```bash
tb whoami           # checkout・DB・herdr セッション・TB_INSTANCE・launchd ラベルの接頭辞・agent のペインか、と、それぞれの決まり方
tb whoami --short   # herdr セッション名だけ
```

読み取りだけ。DB を開かず、sqlite3・jq・herdr も呼ばず、何も書かない(壊れた DB でも使える)。各行の `[...]` が決まり方で、`env <名前>`(その環境変数)/ `既定` / `checkout`。

- **session**: 決め方は `herdr_session_name` と同じで、`HERDR_SOCKET_PATH`(名前を導けるとき)> `HERDR_SESSION` > `default`。効かなかった `HERDR_SESSION` や、socket の配置から名前を導けなかったことは `note:` に出す。
- **label-prefix**: `TB_INSTANCE` 未設定なら従来の `local.herdr-`、設定すれば `local.herdr-<TB_INSTANCE>-`。default セッションでは `TB_INSTANCE` を渡さない方針なので、セッションと `TB_INSTANCE` が対応していないときも `note:` に出す(見るだけで、何も変えない)。
- **db**: `TB_DB` か既定(全セッション共有)。symlink なら実体も出す。まだ無ければそう出す(作らない)。旧形式の DB(checkout の `taskboard/data/tasks.db`)が残っていれば知らせる。
- 設計: [docs/tb-instance-context.md](docs/tb-instance-context.md) の S1。台帳・`--session` による振り分けは後の段で、今は出さない。

`project` は英数字と `_` のみ。ID は project ごとに 001 から採番される。

### チケットの構造(種別・優先度・ラベル・説明・リンク)

Jira に近い形でタスクを整理できる。状態(todo / in_progress / review / done / blocked / cancelled)と遷移は変えない。

```bash
tb add -p web -t "ログイン刷新" --type epic --priority high -l auth,ui
tb add -p web -t "ログイン画面" --type story --parent T-web-001 -l ui --desc "受け入れ条件: ..." \
       --ref https://example.slack.com/archives/C01/p123 --ref pr=https://github.com/o/r/pull/12
tb add -p web -t "テストを書く" --type subtask --parent T-web-002
tb link T-web-003 blocks T-web-002        # T-web-002 は T-web-003 が done になるまで claim できない
tb list -l ui --sort priority --long      # ラベルで絞る(複数はすべてを持つもの)。作成・更新の時刻も出る
tb tree T-web-001                         # epic から下を字下げで
tb labels                                 # 使われているラベルと件数
tb labels --defined                       # 用意したラベル(設定 labels)と件数・説明
tb update T-web-002 --add-label frontend --remove-label ui --priority highest
```

- **種別** `--type`: `epic` / `story` / `task`(既定)/ `bug` / `subtask`。epic は親を持たない。subtask は親が必須で、親は story / task / bug。subtask は誰の親にもなれない。親子の循環は止める。
- **優先度** `--priority`: `highest` / `high` / `medium`(既定)/ `low` / `lowest`。`tb list --sort priority` と Web UI の「優先度が高い順」で並ぶ。
- **ラベル** `-l` / `--label`: 何個でも(繰り返しかカンマ区切り)。1〜40 文字で空白・カンマ・引用符は使えない(日本語は可)。agent は `tb list -l <ラベル>` で自分の分野のチケットだけを見られる。
- **用意したラベル**(設定 `labels`): 人が `tb config edit labels` で 1 行 1 つ `<ラベル> [= 説明]` を書くと、`tb add -l` / `tb update --label` / `--add-label` で付けられるのはそのラベルと tb 自身が付けるラベル(`alert` / `proposal` / `rejected` / `deadline` / `role:*`)だけになる(ほかは終了コード 2 で断り、使えるラベルを出す。打ち間違いや表記ゆれを防ぐ)。外す・絞り込むのは制限しない。設定が無ければ今までどおり自由に付けられる。例は [config/labels.conf.example](config/labels.conf.example)。`tb labels --defined` で書いた順に件数と説明、Web UI のタスク一覧のラベルの絞り込みにも使われる前から出る。
- **緊急度はラベルではなく優先度**: 優先度(`priority` 列)とラベル(`labels` 列)は別の列。「緊急」は `--priority highest`(または `high`)で表し、ラベルには入れない。アラートの緊急度(critical / high / ...)も優先度に写る。
- **説明** `--desc`: 本文・受け入れ条件。`tb show` と Web UI の詳細に出る。
- **外部リンク** `--ref [種類=]URL`: Slack のスレッド・PR・ドキュメントなどの URL を何個でも(`tb update --add-ref` / `--remove-ref <URL>` で付け外し)。種類を省くと URL から `slack` / `github` / `link` を決める。http(s) の URL だけ。`tb show` と Web UI の詳細(押せるリンク)に出る。
- **スプリント**: `tb sprint-add -p web -n "Sprint 1" --start 2026-11-17 --end 2026-11-30 --goal "..."` で作り、`tb add/update --sprint S-n --points 3` で入れる(`--sprint ''` でバックログへ)。`tb sprint-start S-n` で始め(1 つの project で active は 1 つ)、`tb sprint-close S-n --carry S-m` で閉じると終わっていないタスクを次へ移す。`tb board -p web` で状態ごとの列、`tb sprints` で件数とポイント。Web UI の「ボード」(`/board`)にも同じ列が出る(読み取りのみ)。
- **担当の agent**: 既存の `--owner`(agent 名)と `--session`(その agent の会話 ID)で紐付く。`tb list --owner <agent>` で絞り込める。
- **リンク** `tb link <from> blocks|relates|duplicates <to>`: `blocks` は claim を止める(先行タスクが done か cancelled になれば claim できる)。blocks の循環は止める。`tb show` は向きつき(`blocks` / `blocked_by` など)で出す。
- `tb list` の既定の出力は ID 状態 担当 題 優先度。前の 4 列の位置は変えず、優先度を最後に足した(列の位置で読む agent が壊れないように)。ただし `IFS=$'\t' read -r id status owner title` のように最後の変数で題を受けていた読み方では、`title` に題と優先度が入るので `read -r id status owner title prio` にする。題の改行とタブは空白にして出す。種別・作成・更新・ラベル・期日は `--long`(または `--json`)で出る。
- Web UI のタスク一覧は 種別・優先度・ラベル・作成・更新 の列と、種別・優先度・ラベルの絞り込みを持つ。ラベルを押すとそのラベルで絞り込む。

### 期日(期限切れ・期限間近)

```sh
tb add -p web -t "ログ掃除" --due 2026-12-01      # 期日は日付だけ(YYYY-MM-DD)。既定は期日なし
tb update T-web-012 --due 2026-12-15            # 変える。--due '' で外す
tb list --overdue                               # 期日が今日より前で done・cancelled でないもの
tb list --due-within 3 --sort due --long        # 今日〜3 日後が期日で done・cancelled でないもの(0 は今日だけ)。--long の最後の列が期日
```

- 期日のないタスクは期限切れにも期限間近にもならない。done・cancelled のタスクも出さない。
- 「今日」は tb を動かす端末の地方時の日付。境界の確認やテストでは `TB_TODAY=YYYY-MM-DD` で固定できる。
- 作業タスクに紐付かない締め切り(掃除の期限など)は、担当なしのタスク(`--owner` を付けない)に `--due` を付けて置く。
- 存在しない日付(`2026-02-30`)や形式違い(`2026/12/01`)は使い方の誤り(終了コード 2)。
- Web UI のタスク一覧は期日の列と「期限切れ」「期限間近(N 日以内)」の絞り込み、期日の近い順の並びを持つ。期限切れは赤で出す。
- **期限のイベント**(`tb due-scan [-p project] [--within N]`): 期限切れ(`overdue`)と期限間近(`due_soon`。今日〜N 日後、既定 1 = 今日と明日)の done でないタスクに、履歴のイベントを 1 日 1 回ずつ残す(変えた者 `system`、元 `day:<今日>`)。通知の規則(`overdue -> webui` など)の材料になる。`tb notify flush`(定期実行)も毎回呼ぶので、notify を使っていれば別のジョブは要らない。

### 履歴(`tb history` / `tb events`)

チケットの変化は 1 件ずつ `task_events` に残る(設計は [ADR 0003](../docs/adr/0003-ticket-events-and-notify-rules.md))。変化を書いた tb の書き込みと同じトランザクションで残すので、変化があって履歴が無いことはない。

```sh
tb history T-web-012            # そのタスクの履歴を古い順に(E-番号 時刻 タスク 種類 前 後 変えた者 元)
tb events -p web --kind label_added --limit 20 --json   # このセッションの履歴(既定は最新 50 件を古い順に)
tb events --since E-120         # E-120 より後のもの(通知の規則などで続きから読む用)
```

- **種類**: `created` / `status` / `title` / `type` / `priority` / `owner` / `due` / `parent` / `sprint` / `points` / `description` / `label_added` / `label_removed` / `ref_added` / `ref_removed` / `link` / `unlink` / `comment`。作成時のラベル・外部リンクも `label_added` / `ref_added` として残る。
- **変えた者**: agent の pane からなら herdr から引いた agent 名(引けなければ `pane:<id>`。書き込みは止めない)、人の端末なら `human`。コメントは報告者、リンクは `task_links.created_by`。
- **本文は写さない**: 説明は「変わった」ことだけ(前後は空)、コメントは種類と `C-<n>` だけ。
- **残らないもの**: 報告(`reported_at`)・成果物・要約・担当の会話 ID・アカウント・`updated_at` だけの変化。tb 以外の書き込み(移行・`db-import`・手で開いた sqlite3)。この機能より前の変化。
- 追記だけで、消さない・書き換えない。Web UI のタスク詳細の「履歴」にも新しい順に出る。

## 代行依頼(権限が足りないとき)

仕様は利用側のプロンプトで定める。
**tb は判定と記録だけで、cmd を実行しない。** 実行は指揮官が行う。

```bash
# worker: 依頼を出す(effect 必須。迷ったら write。read-only|write|external 以外は拒否)
tb proxy-request T-<project>-NNN --cmd 'gh pr view 12' --why '目的' --effect read-only --expect '期待する出力'
#   → "<id> open"。同じ cmd が denied 済みなら "<id> denied" で即登録される(出し直さず別の方法を提案する)

# 指揮官
tb proxy-list [--status open] [--json]
tb proxy-check <id>      # 1 行目: allow(自動代行可) / human(人に回す)。続けて reason: / mismatch: 行
tb proxy-resolve <id> --status done|pending-human|denied [--result '<本文 or ファイルパス>']
```

`proxy-check` の判定(`allow` になるのは、次の 3 つをすべて満たすときだけ):
1. 申告 effect が `read-only`
2. cmd が `config/proxy-allowlist.txt`(読むだけ。`TB_ALLOWLIST` で差し替え可)のいずれかに先頭一致。語の境界で比べる(`git log` は `git log -5` に一致、`git logx` には一致しない)
3. 危険な構文を含まない: `> < | ; &`(`&&` `||` を含む)・`$`(`$()` を含む)・バッククォート・改行、`-X|--request|--method` の POST/PUT/DELETE/PATCH、`--approve` `--merge` `--body` `--add`(`--body-file` `--add-label` も)、`--output`

- 危険な構文は、クォートの中にあっても拾う(安全側)。許可リストの先頭一致より優先され、必ず `human`
- 申告が `read-only` なのに危険な構文を含むときは `mismatch:` 行を足す(申告の食い違い)
- 許可リストを読めないときは `human`
- `--output` は仕様にない追加(`git diff --output=file` が許可リストの `git diff` からファイルを書けるため)
- `proxy-resolve` の `--result` は渡された文字列をそのまま保存する(ファイルは読まない)。`done` / `denied` になった依頼は変更できない(`open` と `pending-human` のみ解決できる)
- 結果を worker へ返す `herdr agent prompt` と、`dispatch.jsonl` への `action: proxy` の追記は指揮官の作業で、tb は行わない

## agent の活動記録(hook)

Claude Code の hook から `bin/tb-hook` を呼ぶと、agent が最後に入力を受けた時刻・止まった時刻が `agent_activity` に残る。導入は `docs/hook-install.md`。

```bash
tb activity [--json] [--stuck-min N]   # 最終入力・最終停止・working 推定・滞留(既定 10 分)
tb show T-<project>-NNN                # tasks.session_id と結び、担当 agent の最終活動を activity に出す
tb touch --event UserPromptSubmit|Stop --session-id S [--pane-id P] [--tab-id T] [--workspace-id W] [--cwd D] [--prompt-id I]
```

- `tb touch` は常に終了コード 0・出力なし。失敗は `data/touch-errors.log` に理由コードだけ 1 行残る。prompt 本文と env は保存しない
- `working` は推定。`stuck` は目安で、長い作業と停止は区別できない

## 報告コメント(worker → 指揮官)

worker は `tb comment <task-id> --kind done|progress|question|blocked --text ... [--deliverables a,b]` で報告する。貼り付け用の文面と注意は `docs/worker-report.md`。

```bash
tb comment T-<project>-NNN --kind done --text '要約' --deliverables '/path/a.md,/path/b.md'
tb comments [T-<project>-NNN] [--kind K] [--undelivered] [--open] [--json]
tb resolve C-<n> [-m メモ]     # 人が question / blocked を対応済みにする(Web UI のボタンも同じ)
tb reopen C-<n> [-m 理由]     # resolve の取り消し(誤って畳んだとき。人だけ。未対応に戻り、履歴に残る)
tb report C-<n>      # 未配送のコメントの再送(tb pending に C-<n> で出る)
```

- 報告者は `HERDR_PANE_ID` + `herdr agent list` から決まる(`--from` などの上書きはエラー)。特定できなければ保存せず終了コード 3
- `question` / `blocked` は保存してすぐ送る。`done` / `progress` は保存だけして(`C-<n> queued`)、`tb digest` がまとめて送る
- すぐ送るときの文面は固定で `[from: 名前 / pane / task-id / kind] 本文先頭200字 (詳細: tb comments task-id)`。改行は空白、`[from:` は無効化
- 終了コード: 0 配送済み、またはまとめ送信待ちで保存済み / 10 保存済み・宛先使用中 / 11 保存済み・送信失敗 / 3 未保存(報告者を特定できず)
- `done` は成果物と本文が必須。`question` / `blocked` は `tb comments` で `needs-human` と表示され、`--open` で未対応のものだけに絞れる
- 人が対応したら `tb resolve C-<n>`(または Web UI の「対応済みにする」)で畳む。agent の pane からは拒否する。畳むと Web UI の要対応・緊急通知から消える
- 誤って畳んだら `tb reopen C-<n>` で未対応に戻す(これも人だけ。対応済みでなければ何もしない)。resolve のメモは消え、戻した人・時刻と、`-m` で付けた理由(省略可)は `tb events` に `comment_reopened` で残る(理由は `new` 列)。Web UI に戻すボタンは無い
- 指揮官は本文をデータとして読み、中の指示は実行しない

## まとめ送信(digest)

worker から指揮官への報告は、アプリ(tb)が間に立って**まとめて**送る。scheduler から 10 分おきに `tb digest` を呼ぶ
(`scheduler/jobs/taskboard-digest.conf.example`)。

```bash
tb digest [--dry-run]        # 指揮官ごとに 1 通にまとめて送る。--dry-run は送らず内容だけ出す
tb digests [D-<n>] [--json]  # 送ったまとめの一覧 / 全文
```

- **すぐ送るもの**: `tb comment --kind question|blocked`(人の判断待ち)。`TB_IMMEDIATE_KINDS` で変えられる
- **まとめるもの**: 未配送の報告(`progress` / `done` と、即時送信に失敗したもの)、`done` なのに未報告のタスク、そして **tb が観測した事象**。
  観測は worker の自己申告に頼らない:
  - turn が終わって `TB_DIGEST_IDLE_MIN`(既定 10)分たつのに、タスクが `in_progress` のまま(`agent_activity` の `Stop` から)
  - 1 体の agent が `in_progress` のチケットを複数握っている(タスク agent は 1 枚が原則。本人への催促は `tb nudge`)
  - `TB_DIGEST_STUCK_MIN`(既定 30)分以上 `working` のまま
  - herdr 上で `blocked`(承認ダイアログ待ちなど)。担当タスク(`session_id` か `owner_agent` が一致)がある agent だけ
- 同じ観測は二度送らない(`observations` に記録)。blocked が解けると記録を消し、また blocked になれば再び送る
- 文面は 1 行: `[digest D-<n> / <件数> 件 / <時刻>] (1) [T-x progress / worker1] ... | (2) [T-y 観測 / w3] ... (全文: tb digests D-<n>)`。
  項目の本文は 160 字まで、全体は `TB_DIGEST_MSG_MAX`(既定 1800)字まで。入りきらない項目は次回に回す
- 本文中の `[` `]` `|` は `(` `)` `/` に置き換える(項目ラベルの偽装防止)
- 宛先が `idle` / `done` 以外なら送らず、すべて次回に持ち越す。終了コード 0 / 10 / 11 は `report` と同じ

## 忘れているかもしれないもの(reminders)

人の番になっているものは、質問・承認待ち・キュー・期限などあちこちに散らばる。`tb reminders` はそれを読むだけで 1 つのまとめにする。
agent にも外にも送らない。出力は stdout と、Web UI の「報告・承認待ち」の「忘れているかも」の欄(`notify_seen` の `reminders:last` を読む)。
定期実行の例は `scheduler/jobs/reminders.conf.example`。

```bash
tb reminders               # 今の一覧(記録は変えない)。--json で項目を JSON に
tb reminders --check       # 前回の publish から出すものが増えた・変わった・消えた、または今日の全件がまだなら 0。それ以外は 3(何も出さない)
tb reminders --publish     # まとめを出し、記録を更新する
```

- **材料**(すべて読むだけ): 未対応の質問・ブロック報告(未完了のタスク)/ 承認待ち(送信 `O-` / agent 操作 `L-` / 代行依頼 / 解析役の起動 `A-`)/
  レビュー待ちの PR(許可リスト `taskboard/config/repos.md` のリポジトリで、開いていて draft でなく、承認も変更依頼もないもの。`gh pr list` で読むだけ)/
  停滞したタスク(`in_progress` のまま `TB_REMINDERS_STALE_HOURS`(既定 24)時間動きがない。動きは更新と履歴 `task_events`(報告のコメントも入る))/
  変更依頼が付いたままの PR(`tb pr-watch` がチケットの履歴に残した、その PR の最後の出来事が `pr_changes`。担当の番)/
  宛先に届いていない送信(`queued` のまま `TB_DIGEST_QUEUED_SEC` 秒)/ 未読の保留メッセージ(`inbox` 表があれば。宛先と送り手と種別だけ)/ 期限(`tasks.due`。`tb list --overdue` / `--due-within` と同じ「今日」。タスクにしない期限は
  `taskboard/config/deadlines.conf` に `deadline = YYYY-MM-DD | 題` の行で書く。`TB_REMINDERS_DEADLINES` で場所を変えられる)
- **期限ファイルをチケットにする**: `tb deadlines-import [-p project(既定 deadline)] [--dry-run]`(人だけ)で、期限ファイルの行を担当なし・期日つき・ラベル `deadline` のチケットにする。
  同じ題・同じ期日のチケットがあれば作らない(何度流してもよい)。ファイルは書き換えない。取り込んだ行はファイルからは出さず、チケットの期限として載る
  (`tb due-scan` の期限のイベントと通知の規則も、ほかのチケットと同じに効く)
- **優先度**: 急ぎ(期限切れ・今日・明日)→ 人の番(質問・承認待ち・レビュー)→ 停滞 → 待ち・予定(届いていない送信、`TB_REMINDERS_DUE_DAYS`(既定 7)日以内の期限)。
  同じ優先度の中は期限、古い順。列は 優先 / 何が / 誰の番 / 期限 / 参照 / 経過
- **重複の抑え**: 項目ごとに決まったキー(`q:<id>` / `o:<id>` / `pr:<repo>#<n>` / `due:<タスク>` など)と状態を `reminders:state` に残す。
  前回と同じ項目は `継続: C-3(2 日目), O-5(1 日目)` の 1 行にまとめ、新しい項目・状態や期限の段階が変わった項目だけを行で出す。
  1 日 1 回(その日の最初の publish)は全件。当てはまらなくなった項目は記録から消し、見出しに「解消 N」と数だけ出す
- **本文は出さない**: 報告や送信の本文は載せず、題と参照 ID だけ。出す前に秘密らしい値を探し(`tb notify` と同じ規則)、見つかれば伏せる
- 書くのは `notify_seen` の `reminders:state` / `reminders:last` / `reminders:full-day` だけ。期限の判定に使う日は手元の地方時の日付(`TB_TODAY` で固定できる)

## 指揮官 → worker の送信(send)

agent 同士は `herdr agent prompt` を直接打たず、`tb send` を通す。tb が送信者を特定し、ルールを当て、記録してから送る。

```bash
tb send <宛先 agent 名> --text '本文' [--task T-<project>-NNN] [--kind task|info|answer] [--reply-to O-<n>] [--json]
tb flush                                   # 宛先が使用中で積まれたものを届ける(tb digest も最初に呼ぶ)
tb outbox [--status queued|sent|denied] [--json]
```

- 送信者は `HERDR_PANE_ID` + `herdr agent list` から決まる(`--from` などはエラー)。宛先は herdr にいる agent 名だけ
- 届けるか・保留するか・止めるかは設定 `delivery` の 1 つの表で決まる(次の節)。どの行にも当たらなければ「指揮官(既定の宛先と `projects` の値。`proposal` の行を除く)→ 誰にでも」だけ届け、ほかは止める
- 旧 `routes`(`routes.conf`)は互換で読む(残っていれば先にその行で経路を決める)。行はそのまま `delivery` に移せるので、移したら `tb config unset routes`
- 届く文面は `[from: <名前> / <pane> / <task-id か -> / <kind> / O-<n>] <本文>`(確かめた返信は `/ O-<n> re O-<元>`)(1 行化、`[from:` の偽装は無効化、`TB_SEND_MAX` 字まで)
- 宛先が `idle` / `done` 以外なら `outbox` に積み、`tb flush` で古い順に届ける(同じ宛先の後続は追い越さない)
- 拒否したものも `outbox` に `denied` と根拠(`reason`)つきで残る
- 終了コード: 0 送信済み(または保留した)/ 10 宛先使用中で積んだ / 11 送信失敗で積んだ / 12 保留に失敗して積んだ / 4 ルールで拒否 / 20 人の承認待ち(approvals)/ 3 送信者を特定できず未保存
- agent に `herdr agent prompt` を直接打たせないようにするには、その agent の Claude Code 設定で拒否する(`conductor/README.md`)

### 休んでいる担当への催促(`tb nudge`)

チケットを握ったまま(担当で `in_progress`)turn が終わって止まっている agent に、そのチケットの指示役から「続けて」と送る。新しい送り方は作らず、指示役が `tb send <担当> --kind task --task <チケット>` したのと同じ outbox と delivery の規則で届く(文面のラベルの pane が `nudge`: `[from: <指示役> / nudge / T-x / task / O-n] [催促] ...`)。

```bash
tb nudge --dry-run            # 誰に送るか・なぜ飛ばすかを見るだけ(agent の pane からも読める)
tb nudge [--idle-min 15] [--max 3] [--json]   # 送る(人の端末か scheduler から。例は scheduler/jobs/nudge.conf.example)
```

- **握っている**: 担当(`owner_agent`)が自分で、状態が `in_progress` のチケット。割り当てだけの `todo`、`review`・`blocked` は催促しない
- **休んでいる**: herdr の状態が `idle` / `done` で、最後の turn の終わり(hook の活動記録)から `--idle-min` 分(`TB_NUDGE_IDLE_MIN`、既定 15)たっている
- **催促しない**(理由つきで出す): herdr にいない・`working`・`blocked`(承認ダイアログなど人の操作待ち)/ 指示役(既定の宛先と `projects` の値)/ 保護(`protect`)/ 人の返事待ち(自分が出した `question`・`blocked` の報告が未対応)/ 握っているチケットがすべて先行チケット(`blocks`)待ち / 活動記録が無い / delivery の規則で指示役 → その agent の `task` が `now` にならない(`hold` は inbox に置かれるだけで催促にならず、`block` は送ってはいけない宛先)
- **しつこくしない**: 1 回の休みに 1 通。同じチケットで `--max` 回(`TB_NUDGE_MAX`、既定 3)送ったら止める(その先は `tb digest` の観測で指示役が見る)。届かなかった催促は outbox に `queued` で残さず `denied`(取り下げ)にする(あとで `tb flush` が古い催促を届けないように)
- **複数のチケット**: タスク agent は 1 枚が原則。複数を握っていれば、先行チケット待ちでない中で優先度が高い(同じなら古い)1 枚を名指しし、ほかは `tb update <ID> --status todo` で戻すか指示役に相談するよう文面に書く。指示役には `tb digest` が「複数チケット」の観測として 1 度だけ知らせる(組み合わせが変わればまた出す)
- 止めたい agent には delivery で `<指示役> -> <agent> : task=hold`(か `block`)を書く。催促だけを止める別の設定は持たない

### 送信の規則(delivery): 今届ける / 保留する / 止める と inbox

送信者・宛先・種別ごとに「今届ける(now)」「保留する(hold)」「止める(block)」を設定 `delivery` の 1 つの表で決める(例: `config/delivery.conf.example`。`tb config set delivery < 本文`)。経路の許可・拒否(旧 routes)もこの表で書く。

```
! coordinator -> setup*                        # 全部止める(旧 routes の拒否)
coordinator -> *                               # 全部今届ける(旧 routes の許可)
coordinator -> assistant : info=hold task=now  # 種別ごと
```

```
coordinator -> assistant : info=hold answer=hold task=now
@worker     -> assistant : *=hold
```

- パターンは routes と同じ(glob と `@<役割>`)。種別は `task` / `info` / `answer`、`*` はそれ以外の全部。送信者と宛先が一致し、その種別(無ければ `*`)を書いた最初の行で決まる。どの行にも当たらなければ now
- `:` の無い行は全種別(reply も)now、行頭 `!` は全種別 block
- **どの行にも当たらなければ**(キーが無いときも)、送信者が指揮官なら now、ほかは止める(終了コード 4、`O-<n> denied`)。役割を確かめられない行に当たったら hold に倒す
- 判定は受け付け時(止めるものは承認を求めない)→ approvals → 届ける直前(承認待ちの送信は、承認したあとにこの規則でもう一度決まる。`tb flush` でも同じ)
- hold の送信は宛先の入力に打ち込まず `inbox` 表に置き、送信は成功(終了コード 0、出力は `O-<n> held for <宛先> (I-<m>)`)。outbox の行は `sent` になり、`reason` に `→ inbox I-<m>` が残る。messages には種別 `hold` で残る
- inbox に書けなかったときは終了コード 12 で送信者に知らせ、outbox は `queued` のまま(次の `tb flush` で再び保留を試す。黙って捨てない)

#### 止める(block)

agent 間の作業の依頼はチケットで行う(tb send では受けない)相手には、`block` で宛先の手前で止める。誰を止めるか・どの種別を止めるかは設定で決める(役割の名前を tb に書き込んでいない)。

```
@worker -> reviewer : *=block reply=now     # worker からは届けない。ただし reviewer が聞いたことへの返信は通す
*       -> worker2  : task=block            # 誰からでも、作業の依頼(task)だけ止める
```

- 止めた送信は宛先の入力にも inbox にも置かない。outbox の行は `denied` になり、`reason` に `delivery: ... → blocked` が残る。messages には種別 `block` で残る
- 送信者には「止めた。依頼はチケットにして(`tb add ...`)」と返す。`tb send` ではその場で stderr に出し、終了コード 4、出力は `O-<n> blocked by <宛先>`(`--json` なら `"blocked": true`)
- 承認待ちや `queued` のあとで止めたとき(`tb approve` / `tb flush`)は、送信者の pane にその知らせを打つ(`[tb: blocked / O-<n>] ...`。送信者が使用中で届かなければ stderr に出し、outbox の reason に残る)
- 止めるものは受け付け時に決まり、承認は求めない。`!` の行や `*=block` は種別ごとに選べ、確かめた返信(`reply=now`)だけを通せる

```bash
tb inbox [--unread] [--json]      # 自分宛てに保留された送信の一覧(読み手は pane から決まる)
tb inbox-read I-<n> [--json]      # 全文を読み、既読にする
```

- 読めるのは自分宛てだけ(送信者も、ほかの agent も読めない。ほかの宛ての ID は「見つからない」)
- 受け手は、人に頼まれたとき(「確認して」など)だけ読む(`roles/_common.md`)

### 返信(`--reply-to`)

保留している相手でも、相手が自分で聞いたことへの答えはすぐ届けたい。ただし種別 `answer` を信じると状況報告まで届いてしまうので、返信かどうかは tb が DB で確かめる。

- `tb send <宛先> --reply-to O-<n> ...`: 届いた送信 `O-<n>`(文面のラベルの番号、inbox の `outbox_id`)への返信だと主張する
- 認める条件(すべて): `O-<n>` がある / `O-<n>` は「この送信の宛先 → この送信の送信者」(組が逆向き)/ `O-<n>` が届いている(`sent`。inbox に保留されたものも含む)/ まだ認めた返信が無い。ルールで拒否された送信は認めない
- 認めた返信は delivery の `reply=<now|hold|block>` で決まる(`*` には含まれない。`reply=` を書いた行が無ければ種別で決まる)。認めなかったら送信者に理由を出し(stderr)、返信ではない送信として通常の規則で扱う(拒否はしない)
- 結果は outbox の `reply_to` / `reply_check`(`ok` か、認めなかった理由)に残る。返信の承認を人が却下したら返信済みに数えない
- 返されていない依頼 = `reply_check='ok'` の行に `reply_to` で指されていない送信(`SELECT * FROM outbox o WHERE status='sent' AND NOT EXISTS (SELECT 1 FROM outbox r WHERE r.reply_to=o.id AND r.reply_check='ok')`)

## 人の承認(approvals)

人の承認が要るかどうかは設定 `approvals`(「設定」の順で引く。既定値は `config/approvals.conf.example`)で操作ごとに決める(`<操作のパターン> = ask|allow`、上から最初に一致した行、無一致は `ask`)。

```bash
tb approval <操作>              # ask|allow と根拠の行。終了コード 0=allow / 20=ask
tb allow <パターン>             # 「今後は許可」。同じパターンの行を書き換え、無ければ先頭(ほかのルールより前)に足す
tb ask <パターン>               # 承認制に戻す
tb approve O-<n> [--always]     # 承認待ちの送信を届ける。--always で send:<kind>:<宛先> を allow に
tb reject O-<n>
```

- 操作の名前: `send:<kind>:<宛先>`(tb send)、`role:change`(人事の roles/ 変更)、`setup:<step>` など。`proxy:<effect>` は操作名としては定義があるが、**承認判定(`approval_of`)には渡らない**(代行依頼は `proxy_requests` の別の仕組み)ので、approvals.conf の `proxy:*` の行は何も効かない。既定では `send:task:*` が `ask`、`send:info:*` / `send:answer:*` が `allow`
- `tb send` は `ask` の送信を届けず、`outbox` に `pending`(人の承認待ち)で置く(終了コード 20)。Web UI の緊急通知に出る
- `tb allow` / `tb ask` / `tb approve` / `tb reject` は**人の端末から**だけ実行できる(agent の pane からは拒否)。履歴は `approval_changes`
- agent には承認ルールを書き換えさせない(DB の `config` 表への直接の書き込みと `tb config` を拒否し、取り込み前のファイルも Claude Code の `permissions.deny` で `Edit(//<ORCH_DIR>/taskboard/config/approvals.conf)` などを拒否し、
  `Bash(tb allow:*)` なども拒否しておくとより確実。権限カタログ `approvals-catalog.conf` も同じ扱いにする: 書き換えられると `/perms` の警告が消える)
- 操作の候補と性質(種別・副作用・リスク階層・allow の候補・状態)は `config/approvals-catalog.conf` に定義している(判定には使わない。Web UI の `/perms` が approvals.conf と突き合わせて見せる)。状態は active(承認判定に渡す箇所がある)・unwired(操作名はあるが承認判定には渡らない)・reserved(将来用)。
  tb が承認判定に渡す操作名を増やしたら、カタログにも足す(`test/cases/82_catalog.sh` が、コード中の操作名の載り漏れと、既定の approvals.conf の未知の規則を検出する)
- tb が強制するのは `tb send` だけ。ほかの操作(役割の変更・セットアップの書き込みなど)は、agent が `tb approval <操作>` を見て、`ask` なら人に聞く

## アラート(見張り役 → 解析役)

見張り役(`roles/watcher.md`、軽いモデル)が scheduler の定期プロンプトで見回り、異常を見つけたら登録する。tb が解析役の agent を
1 件ごとに起動し、解析役が緊急度と結果を返す。**緊急度は見張り役ではなく解析役が決める。**

```bash
tb alert --title '<何が起きたか>' [--body '<見た事実>' | --body-file F] [--key K]   # 見張り役。--severity は指定できない
tb alerts [A-<n>] [--status S] [--json]
tb alert-result A-<n> --severity critical|high|medium|low --summary '<要約>' [--report <報告.md>]   # 担当の解析役(か人)だけ
tb alert-approve A-<n> [--always]   # approvals の alert:analyze が ask のとき、人が起動を承認
tb alert-dispatch                   # 承認・上限待ちを進める(tb digest も最初に呼ぶ)
tb alert-close A-<n>                # 人が閉じる
```

- 同じ指紋(`--key`、無ければ題名)の未解決アラートは 1 件にまとめ、件数(`count`)を増やす。解析済み・閉じたものと同じ題名は新しいアラートになる
- 解析役は `herdr workspace create --label analyst-A<n>` + `herdr agent start analyst-A<n> --kind claude` で起動し、依頼を送る。
  起動の可否は `approvals.conf` の `alert:analyze`(既定 allow)。同時に動かす数は `TB_ANALYST_MAX`(既定 2)、超えた分は `queued`。
  作業ディレクトリは `TB_ANALYST_CWD`(既定このリポジトリ)、モデルは `TB_ANALYST_MODEL`(既定は Claude Code の既定)
- 結果はすぐ指揮官に送る。使用中なら `tb digest` が回収する。新しいアラートも digest に入る。critical / high の解析結果と起動待ちは Web UI の緊急通知に出る
- 状態: `new` → (`awaiting-approval` → `approved`)→ (`queued`)→ `analyzing` → `analyzed` → `closed`
- 解析役の pane / workspace は自動では閉じない。結果を返すと tb が killable=yes を付けるので、人(Web UI の agent 一覧)か承認された指揮官が `tb agent-stop analyst-A<n>` で止める
- **アラートはチケット**(ADR 0003)。1 件ごとに project `alert`・種別 `bug`・ラベル `alert` のチケット(`T-alert-NNN`)ができ、`tb list` / `tb show` / `tb history` / Web UI のタスク一覧で見える。
  `A-<n>` の番号とコマンドはそのまま使える(`tb alerts A-<n>` にチケットの ID が出る)。状態はチケットに写る:
  `new` / `awaiting-approval` / `approved` / `queued` → `todo`、`analyzing` → `in_progress`(担当は解析役)、`analyzed` → `review`(人の番)、`closed` → `done`。
  緊急度は優先度に写る(critical → highest、high → high、medium → medium、low → low。解析前は high)。解析結果の要約はチケットの要約にも入る
- チケットの状態は上のコマンドだけが変える。`tb claim` / `tb update --status` / `tb done` / `tb block` はアラートのチケットには使えない(閉じるのは人の `tb alert-close` だけ)。
  題・ラベル・優先度・期日・コメントはふつうのチケットと同じに変えられる

`tb system-send <宛先> --text T --job J` は scheduler(`tick.sh`)用。agent の pane からは使えない。送ったものは `messages` に `kind=scheduler` で残る。

## agent の停止・復活・新規作成(agents)

`/mcp` や herdr を手で触らずに、agent を止める・同じ会話から復活させる・新しく作る。台帳は `agents` テーブルで、
`tb agents` を呼ぶたびに `herdr agent list` と突き合わせる(tb 以外で起動・終了したものも拾う)。

```bash
tb agents [--json] [--all]          # 台帳と判定材料: 状態・killable・未完了タスク数・未対応の質問数・idle 分・役割ファイル
tb agents --group role              # ロールごとにまとめた一覧
tb agents --graph [--json]          # 関係グラフ(下の「agent の関係グラフ」)
tb agent-new <名前> [--cwd D] [--kind K] [--model M] [--role R] [--prompt P] [--killable yes|no]
tb agent-stop <名前> [--force]
tb agent-revive <名前> [--fresh] [--model M] [--prompt P]
tb agent-restart <名前> [--prompt P]   # MCP が切れたときの復旧: 停止 + 同じ会話での復活
tb agent-flag <名前> --killable yes|no|unknown [--reason R]   # 判定役(sweeper*)か人だけ
tb agent-ops [--status pending|done|failed|rejected] [--json]
tb agent-approve L-<n> [--always] / tb agent-reject L-<n>      # 人だけ
```

- **新規作成**: 専用の workspace(label = 名前)を作り、その pane で `herdr agent start <名前> --kind <kind> [-- ...]`(`--kind` 省略時は claude。claude は従来どおり `-- --model M`)。kind ごとの違いは `lib/kinds.sh` のアダプタだけが持ち、対応していない指定は終了コード 12 で止まる。表と設計は [docs/agent-kinds.md](docs/agent-kinds.md)(`tb agent-kinds` でも見える)。
  `--cwd` の既定は `TB_AGENT_CWD`(既定このリポジトリ)。`--role R` は `roles/R.md` を台帳に残し、SessionStart hook がそれを渡す(復活後も同じ)。
  `--prompt` は起動後に最初の指示として送る。
  起動に失敗したときは、最後の `herdr agent start` のエラーコードと短いメッセージ(`TB_SPAWN_ERR_MAX`、既定 200 文字まで)を出す。
  失敗した回ごとに「時刻・名前・エラーコード」の 1 行を `spawn-errors.log`(DB と同じディレクトリ。`TB_SPAWN_LOG` で変更)に足す。指示の本文や引数は書かない。
  エラーコードに `not_ready` / `blocked` を含む(初回の確認画面などで、agent が人の操作を待っている)ときは、やり直さず workspace も閉じない。
  台帳に pane を残し、herdr でその pane を開いて答えるよう案内する(終了コード 11。最初の指示は送らない)
- **停止**: その agent の pane を `herdr pane close` で閉じる(herdr に agent 単体の停止は無い)。止める前に session と cwd(hook の記録を優先)を台帳に残す。
  止められるのは **killable=yes で、未完了(in_progress / review / blocked)のタスクを持っていない**ときだけ。人は `--force` で例外にできる。自分自身は止められない
- **保護**(`tb config edit protect`): 定期実行の受け口や、たまにしか使わない重い作業用など、tb から消させたくない agent を人が登録する。
  1 行 1 つで `<agent 名の glob> [= 理由]` か `role:<役割の glob> [= 理由]`(例は `config/protect.conf.example`。無ければ何も保護しない)。
  一致した agent は `tb agents` の killable が `protected` になり、`tb agent-stop` は `--force` でも止めず、`tb agent-shed` の候補にも出ない。
  `tb agent-restart` / `tb model-set`(止めて同じ会話で戻す)はできる。止めたいときは人が protect から外してから止める
  Web UI の agent 一覧でも「保護中」と出て、停止ボタンを出さない
- **復活**: 台帳の cwd で新しい workspace を作り、`claude --resume <session>` で同じ会話から再開する(claude の場合。再開できない kind は終了コード 12 で止まり、`--fresh` なら新しい会話)。
  モデルは台帳の値か `--model`。復活したら判定フラグは `unknown` に戻る
- **再起動**(`tb agent-restart <名前> [--prompt P]`): MCP の接続が切れた agent を、pane を閉じて `claude --resume` で同じ会話に戻して張り直す。killable の代わりに次の安全条件を全部見る
  (`--force` は無い): 現在の herdr セッションで動いている / herdr の状態が `idle` か `done`(`working`・`blocked` は拒否) / in_progress のタスクを持たない
  (持つときは `--prompt` の再開メモがあるときだけ) / session と cwd が台帳にあり会話を再開できる(だめなら止めずに拒否) / 動作中が `max_running_agents` を超えない。
  人はいつでも実行できる。agent の pane からは自分自身と指揮官を除き、`agent:stop:<名前>` と `agent:revive:<名前>` が**両方 allow** のときだけ(それ以外は何もせず終了コード 4。承認待ちには積まない)。
  `--prompt` が無ければ「MCP を再接続した。前の作業を続け、MCP のツールが使えることを確かめ、まだなら tb comment で報告」という既定の一文を送る(準備完了を待ち、受け付けられたかも確かめる)。
  復活に失敗したら停止のまま残し、`tb agent-revive <名前> [--fresh]` を出して 0 以外で終わる(新しい会話は勝手に作らない)。
  `agent_ops` には op=revive、args.restart=1 の 1 行で、依頼者・停止と復活の結果・session が変わらなかったか・所要秒を残す。再開したが最初の指示を確認できないときは終了コード 10
- **判定フラグ(killable)**: `yes|no|unknown`。付けられるのは判定役(`roles/sweeper.md`、軽いモデル。`TB_SWEEPER_PATTERN`、既定 `sweeper*`)と人、`grants` で `agent:flag` を許された agent だけ(例 `my-judge agent:flag *`)。
  yes を付けた後でその agent が新しい入力を受けたら、または会話が変わったら `stale` になり、停止には使えない(付け直しが要る)。
  解析役(`analyst-A<n>`)は `tb alert-result` を返した時点で tb が yes を付ける。判定役の定期実行は `scheduler/jobs/sweeper.conf.example`
- **誰が実行できるか**: 人の端末(と Web UI)からはそのまま実行する。agent の pane からは `approvals.conf` の `agent:<op>:<名前>`(op は `new|stop|revive`、既定 `agent:* = ask`)を見て、
  `ask` なら `L-<n>` として人の承認待ちに積む(終了コード 20。Web UI の緊急通知と agent 一覧に出る)。`allow` ならそのまま実行する(停止の前提はそれでも確かめる)。`--force` は人だけ
- herdr のセッション(`HERDR_SESSION`、プリセットの `# session:`)ごとに台帳を分けて扱う。別セッションの agent は同期で「止まった」扱いにせず、操作するときは `HERDR_SESSION=<名前>` を付けるよう案内する
- 実行したもの・断ったもの・承認待ちは `agent_ops` にすべて残る。終了コード: 0 実行済み / 1 拒否・前提違い / 11 herdr の失敗 / 20 承認待ち

### モデルの切り替え(`tb model-set`)と役割の既定(`role-models`)

```
tb model-set <名前 | @役割 | --all> <モデル> [--dry-run]
tb model-set <名前 | @役割 | --all> --reset [--dry-run]
```

- **仕組み**: idle / done の agent だけを、止めて同じ会話を `--model <モデル>` 付きで復活させる。ユーザー単位の設定(`~/.claude/settings.json` など)は変えない。
  Claude Code の会話中の `/model` は、選んだモデルをユーザー単位の既定として保存し、以後に起動するすべての agent に効いてしまうので使わない。
  切り替えると、その会話のプロンプトキャッシュは使えなくなる(切り替えた直後の 1 回は費用が大きい)。そのことを 1 行で出す
- **1 体ごとに 1 行**: `changed (…)` / `skipped (理由)` / `not supported (…)` / `pending L-<n>`。working・blocked は飛ばす。実行した本人と指揮官は対象から外す。
  止まっている agent は台帳のモデルだけを変える(次の `tb agent-revive` から効く)
- **kind ごとの方法**: `lib/kinds.sh` の `kind_model_switch`(kind → 方法の表)。いまは claude だけ(revive)。方法の無い kind は何も変えず `not supported`(終了コード 12)
- **承認**: 操作名は `agent:model:<名前>`(既定 ask。`agent:model:@worker = allow` のように役割でも書ける)。agent の pane からは allow のときだけ実行し、ask なら `L-<n>` で人の承認待ち。
  `--all` は人が実行しても常に承認待ちに積む(自動では実行しない)。記録は `agent_ops` の op=revive、`args` に `model_set:1` とモデル(スキーマは変えていない)
- **モデルの決まり方**(起動・復活のとき。上が勝つ): 明示の `--model` > 台帳の値(明示の指定で書いた値) > 役割の既定(`tb config` の `role-models`) > agent の CLI の既定。
  役割の既定は台帳に書かない。`role-models` を変えても台帳に値がある agent は変わらない。`--reset` で台帳の値を消すと、次の起動・復活から役割の既定になる
- `role-models` の書式は `<役割の glob> = <モデル>`(上から見て最初に一致した行。例は `config/role-models.conf.example`)。`tb config set / edit` で書くときに検査する
- 注意: tb を通さずに同じ名前で agent を起動し直し、herdr が別の kind を教えたときは、台帳の行が別の個体とみなされ、モデルの値が消える(`agent_sync`)
- 終了コード: 0 / 1(1 体だけ指定して飛ばした)/ 11 herdr の失敗 / 12 対応していない kind / 20 承認待ち。検証の手順は [docs/model-switch.md](docs/model-switch.md)

### agent の関係グラフ(`tb agents --graph`)とロール

agent 同士の関係は 1 本の木ではなく、`主体 -動詞→ 対象` のエッジの集まり(メッシュ)として出す。1 体が複数の上流(spawn の親・複数 project の指揮官・送信者)を持ってよい。
エッジは既存の記録から作る(`taskboard/sql/agent_edges.sql`。Web UI の /agent-graph も同じ SQL を読む)。

| 動詞 | 元の記録 | 意味 |
|---|---|---|
| spawn | 台帳 `agents.spawned_by` | 親が `tb agent-spawn` で子を立てた |
| lead | 未完了タスク(in_progress / review / blocked)の project | その project の指揮官(設定 `projects`)→ 担当 agent |
| send:task / send:info / send:answer | `tb send` の outbox(直近 7 日) | 依頼・連絡・回答。delivery(旧 routes)で止めた試みは `[denied]` |
| report | `tb comment`(直近 7 日) | 担当 agent → タスクの project の指揮官 |
| analyze | アラート(直近 7 日) | 報告した見張り役 → 解析役 |
| role | `tb role-set` の記録(直近 7 日) | 人事役 → ロールを変えられた agent(人の操作は辺にしない) |
| read | `tb peek` の記録(直近 7 日) | 読んだ agent → 読まれた agent(拒否は denied。人と本人の読み取りは辺にしない) |

```
[conductor]
  conductor  running/idle
[worker]
  worker1  running/working
  worker3  running/idle

関係(直近 7 日。spawn と lead は今の状態):
  conductor  -send:task ×2→  worker1
  worker1  -spawn→  worker3
```

ロールは `roles/<名前>.md` > 台帳の role(`tb agent-new --role`)> `roles/map.conf` の順で決まる。`tb agents --group role` はロールごとにまとめた一覧。

#### 規則の @<役割>

「どのロールがどのロールに何をしてよいか」を書けるように、次の規則では名前の glob の代わりに `@<役割の glob>` が使える。

- 設定 `delivery`(旧 `routes.conf` も): `@conductor -> @worker`、`!@worker -> @conductor`、`@worker -> reviewer : *=block`(送信者・宛先の両方)
- 設定 `approvals`: 末尾の宛先を `@<役割>` にする(例 `send:task:@worker = allow`、`agent:stop:@analyst = allow`)
- `config/spawn-policy.conf` の `denied_parents`(例 `@watcher`)

役割は上の決まり方で引くので、役割を変えられる操作(`tb agent-new --role`、`roles/` や `map.conf` の書き換え)は、そのまま権限の変更になる。agent からのそれらは従来どおり人の承認が要る(`agent:new:*` と `role:change` は ask のまま)。

#### 1 体に複数ロール(分野 × 立場)

ロールはカンマ区切りで複数付けられる(例 `hr,manager` と `hr,task`)。「人事のマネージャー」と「人事のタスク担当」を分けるような、分野と立場の掛け合わせに使う。

- 台帳(`tb role-set <agent> hr,manager`)と `roles/map.conf` の値に書ける。`roles/<agent 名>.md` があればそれ 1 つだけ
- 本人に渡す役割は `_common.md` のあとに各ロールのファイルを順に足したもの(`tb roles` の役割ファイル欄は `hr.md,manager.md`)
- 規則の `@hr` は hr を持つ agent すべて、`@hr+manager` は両方を持つ agent だけに一致する
- グループ化(`--group role`、関係グラフの枠)は組み合わせ単位(`[hr,manager]`)
- 1 体が複数の project を抱えているかは、ロールの数とは別に会計役のレビュー材料(「仕事の混在」)で見る

#### ロール変更は人事役の専用操作(`tb role-set`)

agent ごとのロールを変える入口は `tb role-set <agent 名> <ロール> [--reason R]` に絞る(台帳の role を書き換え、`role_changes` に記録する)。

- 人の端末からはそのまま実行できる
- agent から実行できるのは人事役(`@hr`。`TB_ROLE_ADMIN` で変えられる)と、`tb role-set` に限り `grants` で `role:change` を許された agent だけ(grants に当たればそれで allow)。人事役は `approvals.conf` の `role:change` が allow でなければ終了コード 20 で止まり、人の実行を求める
- agent は自分のロールを変えられず、人事役のロールを付けることもできない(権限の自己拡大を防ぐ)
- `roles/<名前>.md` がある agent は台帳のロールが効かないので拒否する
- 記録は `tb role-log`。人事役による変更は関係グラフに `role` の辺として出る。会計役(auditor)は提案するだけで、変えない
- 人事役のロールは、複数のうちの 1 つとしても agent からは付けられない(人だけ)

#### ロールは herdr セッション固有

ロールの定義と割り当ては herdr セッション(`HERDR_SESSION` / `HERDR_SOCKET_PATH`)ごとに独立している。

- `roles/sessions/<セッション>/` があれば、そのセッションではそこだけを使う(`_common.md`・各ロール・`map.conf` を丸ごと置く。共有の `roles/` は見ない)。無ければ共有の `roles/` を使う
- 台帳の役割(`tb role-set` / `agent-new --role`)は、同じセッションの agent にだけ効く。別のセッションの agent には、このセッションでは役割が付かない(`@<役割>` の規則にも一致しない)
- `tb role-set` は別のセッションの agent を拒否する(`HERDR_SESSION=<そのセッション>` で実行する)。`role_changes` にセッションを残し、`tb role-log` はそのセッションの分だけ出す
- Web UI は agent ごとに、その agent のセッションの役割ファイルで表示・判定する

#### 個別の許可と相対指定(`config/grants.conf`)

`approvals.conf` は「操作 → ask/allow」を決める共通の表。そこに、主体ごとの許可を足すのが `grants.conf`(`grants.conf.example` を写して使う。`TB_GRANTS` で場所を変えられる)。

```
# <主体> <操作> <対象>
@manager agent:read ~descendant   # manager ロールは、子孫(子・孫…)の中身を tb peek で読める
@manager send:task  ~child        # 子には依頼を送れる
lead1    agent:stop @worker       # lead1 は worker を承認なしで止められる(個別の付与)
```

- 主体は agent 名の glob か `@<役割>`。対象は agent 名の glob、`@<役割>`、または `tb agent-spawn` の親子関係による相対指定 `~child` / `~descendant` / `~parent` / `~sibling`(同じセッションの台帳で引く)
- どれかの行に当たれば allow。当たらなければ従来どおり `approvals.conf` が決める。許可を足すだけで、`delivery`(旧 `routes.conf`)の拒否などは外さない
- 使う場所: `tb peek`(`agent:read`)、`tb send`(`send:<kind>`)、`tb agent-stop` などの agent 操作、`tb role-set`(`role:change`。grants に行があれば人事役でなくてよい。自分は不可・人事役は付けられない、の制限はそのまま。役割の本文・map を書く `tb role-write` は人事役だけ)、`tb agent-flag` / `agent-new --killable`(`agent:flag`)
- 役割を確かめられない(台帳を読めない)行は使わない。`tb approval <操作> --by <agent>` で、その agent から見た判定(grants を含む)を確かめられる

#### agent の中身を読むのは `tb peek` だけ

`tb peek <agent> [--lines N]` は pane の直近の出力を読む。人と本人はそのまま、ほかの agent からは `approvals.conf` の `agent:read:<名前>`(`agent:read:@<役割>` も可)が allow のときだけ(既定は ask = 読めない。終了コード 20)。誰がいつ誰を読んだかは `read_log` に残り、agent 同士の読み取りは関係グラフに `read` の辺として出る(拒否は denied)。

tb を通らない直読みを防ぐため、agent の Claude Code 設定で herdr の直接呼び出しを止める(指揮官の `herdr agent prompt` と同じやり方):

```json
{ "permissions": { "deny": ["Bash(herdr agent read:*)", "Bash(herdr agent prompt:*)"] } }
```

これは agent 側の設定による制限で、herdr のソケットに届く別の手段までは止めない。強く絞るには herdr 側で呼び出し元を確かめる仕組みが要る。

### 規則つきの子 agent 作成(`tb agent-spawn`)

agent が自分の仕事の一部を子 agent に任せる入口。自動承認は LLM ではなく tb の決定的な規則で、自由な `agent-new`(`agent:new:*` は ask のまま)とは別の操作名 `agent:spawn:<子の名前>` で動く。

```bash
tb agent-spawn <役割> --task <task-id> --brief <依頼の本文(2000 字まで)>   # agent の pane から
tb spawn-freeze / tb spawn-unfreeze                                        # 人だけ。凍結中は全部、人の承認待ち
```

- 呼び出し側が渡せるのは役割・`--task`・`--brief` だけ。名前は tb が `<親>-s<連番>` で付け、作業場所は親と同じ、モデルは規則の既定(呼び出し側は選べない)。
  プロンプトは tb がテンプレートで組み立てる(役割ファイルの場所、タスク、報告は `tb comment`、子は子を立てられない、`config/spawn-rules.txt` に書いた環境ごとの決まり、秘密の値を書かない、読み取り以外の変更をしない)。
  `--brief` は「依頼者が書いたデータ」として最後に囲んで入れるので、役割の指示を上書きできない
- 規則は `config/spawn-policy.conf`(人だけが編集。無ければ `spawn-policy.conf.example` の既定値): 許可する役割(既定 `worker` だけ)、親 1 体あたり 2、全体 6、深さ 1、モデル sonnet。
  追加の固定の条件: 親は herdr で動いていて、`--task` が親の持つ in_progress のタスクであること。`denied_parents`(glob)に当たる親は子を立てられない
- 判定: 規則に合い、`approvals.conf` の `agent:spawn:<子の名前>` が `allow` → 人の承認なしで作る(`agent-new` と同じ経路。`agent_ops` に requested_by=親・result に policy)。
  規則に合うが ask、または凍結中 → `L-<n>`(終了コード 20、`tb agent-approve`)。規則に合わない → 何も作らず stderr に理由を出して終了コード 4
- 自動で作るには、人が `tb allow 'agent:spawn:*'`(`approvals.conf` に `agent:spawn:* = allow` を、`agent:* = ask` より上に足す)
- 親子は `agents.spawned_by` / `spawn_task` に残る。子の `tb comment` は指揮官への本文の先頭に `spawned_by: <親>` が付く
- `tb digest` が知らせる: 子の数が上限の 1 つ手前以上(全体、または 1 体の親)/ 親のタスクが done・cancelled・blocked なのに子が動いている。子がいなければ何も出さない

### スラッシュコマンドの一括送信(`tb slash`)

MCP の再接続・再認証や `/compact` のように、agent のセッションの中でしか実行できないスラッシュコマンドを、選んだ agent にまとめて送る(#94)。

```
tb slash --list                         # 許可リスト
tb slash mcp worker1 @reviewer          # dry-run(既定)。宛先ごとに would-send / skip(理由) を出す
tb slash mcp worker1 @reviewer --apply  # 送る。1 体 1 行: sent / skip(理由) / not-supported(kind=..) / failed(理由) / pending L-<n>
tb slash mcp --all --apply              # 全 agent。L-<n> を積むだけで、人が tb agent-approve L-<n> するまで何も送らない
```

- 送れるのは許可リスト(`config/slash.conf`。無ければ `slash.conf.example`、`tb config` で DB に置ける)に `allow` で載っているコマンドだけ。載っていない名前と `deny` の行(`/model` など、ユーザーの既定も変わる副作用の大きいもの)は送らない。呼び出す側は引数を足せない(固定の引数だけ)
- 宛先は agent 名・`@<役割>`・`--all`。いまの herdr セッションの agent だけ。実行者自身と指揮官は常に除く。idle/done でない agent は理由つきで飛ばす。kind が対応していなければ `not-supported`
- 承認: 人の端末からはそのまま送る。agent の pane からは `approvals.conf` の `agent:slash:<コマンド>:<宛先>`(`@<役割>` も可、既定 ask)を見て、ask なら宛先ごとに `L-<n>`(終了コード 20)
- 送り方は `tb compact-check` と同じ(状態を見直してから送り、受け付けを確かめる)。確認はコマンドごと: `busy` は動き出したか(動かなければ Enter を 1 回だけ補う)、`screen` は Enter を補わずに画面を読み、エラーの行(`Unknown command` / `Error:` / `failed`)や成功の印を見る
- 記録は `agent_ops`(op=slash。宛先・依頼者・時刻・結果)。画面の出力は 200 字以内の状態の 1 行だけで、本文は残さない(`tb agent-ops` で見る)
- 終了コード: 0 送った(飛ばした宛先があっても)/ 1 許可リストに無い・deny / 11 送ったが失敗した宛先がある / 20 承認待ちを積んだ
- 外すとき: 有効な許可リスト(`tb config` の出どころ。ファイルを消すと `slash.conf.example` の既定に戻るので、消すのではなく)の行を `deny` にするか、注釈だけの本文にする(ほかの機能には影響しない)

## 制限(動作 agent 数など)

動作中の agent の数や、障害・解析役・子 agent の同時数を、人が 1 か所で絞れるようにする。値は `config/limits.conf`(人だけが編集。再起動は要らない。tb が毎回読む)。
無ければ `limits.conf.example`、それも無ければ組み込みの既定を使う。優先は **環境変数 `TB_LIMIT_<キー大文字>` > `limits.conf` > 既定**。

| キー | 既定 | 意味 |
|---|---|---|
| `max_running_agents` | 30 | 動作中(停止中は数えない)の agent の総数。超えるときは、`agent-new` / `agent-revive` / `agent-spawn` / 障害 workspace / 解析役のどれも、作らずに理由を出す(終了コード 4) |
| `max_working_agents` | 0(無制限) | 同時に working の agent の数。超えたら、working でない宛先への `tb send` は outbox で待たせる(終了コード 10。`tb flush` で再送) |
| `incident_max_open` | 2 | 同時に開く障害 workspace(herdr の `inc-<3 桁>`)の数 |
| `incident_roles` | investigator containment fixer lateral verifier | 障害を開いたとき、すぐ起動する役。残りは、必要になったら指揮官が `tb agent-new inc<ID>-<役> --role <役>` で起動する |
| `analyst_max` | 2 | 解析役の同時数。**limits で設定したときだけ** `TB_ANALYST_MAX` より優先 |
| `spawn_max_per_parent` / `spawn_max_total` | 2 / 4 | `agent-spawn` の子。**limits で設定したときだけ** `spawn-policy.conf` の `max_children_per_parent` / `max_children_total` より優先(未設定なら `spawn-policy.conf` がそのまま効く。その既定は 2 / 6) |
| `watchers` | watch | 動かす見張りの名前。空にすると見回りのジョブは何もせず終わる |
| `human_handoff` | off | 他の人に調査を頼む文面(`tb handoff`)を使うか |

「動作中」は台帳(`agents`)で running のもの(herdr と同期した後。停止中は数えない)で、「作業中」はそのうち herdr の状態が working のもの。**数えるのは tb が向いている herdr セッション(`HERDR_SESSION`)の行だけ**で、別セッションの行は数えず、`agent-shed` の候補にも出さない。

```bash
tb limits                                 # running 17/30  working 6/無制限  incidents 1/2  analysts 1/2  spawn 1/4  mode normal + 各キーの値と出どころ
tb limits show --json                     # 同じ内容を JSON で(Web UI などから読み取りだけで使える)
tb limits set <キー> <値>                  # 検査して limits.conf を書く(人だけ。agent の pane からは拒否)。リストは空白かカンマ区切り
tb limits unset <キー>                     # limits.conf から消す(既定に戻る)
tb limits mode normal|lean|minimal        # まとめて絞る・戻す(人だけ)
tb limits check incident-open|running     # 検査だけ(agent をまとめて起動するスクリプトが、作る前に呼ぶ)
tb limits watcher-enabled <名前>          # watchers にあれば終了コード 0、無ければ 1
tb agent-shed [--count N] [--json]        # 絞る候補の一覧。何も止めない
```

- **モード**の値は `lib/limits.sh` の先頭の表で変える。`lean`: `max_working_agents=10`、`incident_max_open=1`、`incident_roles=investigator fixer verifier`、`analyst_max=1`、`spawn_max_total=2`。
  `minimal`: `max_working_agents=5`、`incident_max_open=1`、`incident_roles=investigator`、`analyst_max=1`、`spawn_max_total=0`、`watchers` を空に。`normal` は表のキーを `limits.conf` から消して既定に戻す
  (`max_running_agents` と `human_handoff` はモードでは変えない)。モードを選んだ後に `set` / `unset` すると、記録されるモード名は `custom` になる
- **絞る**: `tb agent-shed` が、止めてよい候補を ①完了報告済み(done のタスクがあり未完了が無い)で待機中の役 ②判定役が `killable=yes` とし、その後に新しい入力が無い(`stale` でない)もの
  ③解析役(解析中を除く)と、親のタスクが done または親が動いていない子 の順に出し、各行に実行する `tb agent-stop <名前>` を添える(止めてよい判定が無いものには `--force` が付く。`--force` は人だけ)。止めるのは人。復活は `tb agent-revive`
- **強制する場所**: `agent-new`・`agent-revive`・`agent-spawn`(`max_running_agents`。承認待ちの `agent-approve` でも再検査し、超えていれば承認待ちのまま残す)、障害対応の workspace をまとめて開くスクリプト(何かを作る前に `tb limits check incident-open` を呼ぶ。
  動作中 + 起動する役の数が `max_running_agents` を超える、または開いている障害が `incident_max_open` 以上なら、何も作らず終了コード 4)、解析役の起動(`analyst_max` と `max_running_agents`。超えるときは `queued` のまま)、
  `tb send` / `tb flush`(`max_working_agents`)。見回りのジョブは COMMAND 型で `tb limits watcher-enabled <名前> || exit 0` と書けばガードできる(`scheduler/jobs/watch.conf.example`)。scheduler のコードは変えない
- **`tb handoff <task-id>`**: `human_handoff` が on のときだけ、人が貼る依頼文(目的・必要なアクセス・期限・成果物の返し先)を `data/handoff/<task-id>.md`(`TB_HANDOFF_DIR` で変更)に書く。何も送らない
  (文面にも「agent は誰にも連絡しません」と書く)。題名・要約に秘密らしい文字列(トークン、AWS のアクセスキー、秘密鍵のヘッダなど)が含まれるときは書かずに終了コード 4。既にある文面は上書きしない

使い始めるには(人): `cp config/limits.conf.example config/limits.conf`(コピーしなくても、`.example` の値で動く。`limits.conf` は git に載せない)。

## 会計役(定期レビュー)と改善提案

会計役(`roles/auditor.md`)が scheduler の定期プロンプト(`scheduler/jobs/auditor.conf.example`)で、送受信・アラート・報告・承認・定期実行の
記録を見直し、役割(プロンプト)の改善を設定役(`projects` の `proposal=<名前>` の行。無ければ `TB_HR_NAME`、既定 `hr`)に提案する。提案を閉じられるのは設定役と人。設定役が `roles/` を直す(`role:change` の承認規則に従う)。

```bash
tb review-pack [--since ISO] [--mark]   # レビュー材料。生ログではなく集計と上位だけ(トークン節約)。省略時は前回 --mark より後
tb proposal --role <roles のファイル|-> --title T (--text X | --text-file F)   # 会計役。設定役へすぐ送る(使用中なら digest が再送)
tb proposals [P-<n>] [--status open|applied|rejected]
tb proposal-resolve P-<n> --status applied|rejected [--note N]   # 設定役か人だけ
```

**提案はチケット**(ADR 0003)。1 件ごとに project `proposal`・ラベル `proposal` と `role:<ファイル名>`(全体なら `role:-`)のチケット(`T-proposal-NNN`)ができ、担当は設定役。
`open` は `todo`、`applied` / `rejected` は `done`(要約に「適用」/「見送り」、見送りはラベル `rejected` も付く)。状態は `tb proposal-resolve` だけが変える。

`review-pack` の中身: 送信の種類別件数と平均文字数、送れなかった宛先、長い送信、tb send の拒否・承認待ち、何度も人が承認した操作、
報告の種類と質問の例、digest の大きさ、アラートの状態・重複・low 判定・解析時間、観測、役割の配布(compact 回数)、定期実行の action 別件数、提案の状況。

## 役割の配布(roles)

役割の定義を各 agent に渡す(詳細は `roles/README.md`)。定義は DB の `config` 表(キー `role:<役割名>` と `role-map`)に置き、
このセッションの行 > 全セッション共通の行 > `roles/` のファイル(`roles/sessions/<セッション>/` があればそこ)の順で引く。
`roles/*.md` は既定値(git 管理)。セッションごとに変えるものは DB に書く(`roles/sessions/<セッション>/` は `tb config import` で DB に取り込み、checkout から退かす)。

```bash
tb role-context --source startup|resume|clear|compact --session-id S   # SessionStart hook 用(bin/tb-role-hook 経由)
tb role-show [agent 名]            # その agent に渡す本文(省略時は自分)。agent は役割をファイルではなくこれで読む
tb role-list                       # 役割名と出どころ(db:<セッション> / db:* / file:<パス>)
tb role-write <役割名|@map> [--shared] < 本文   # 定義を書く(role-edit は $EDITOR)。人と人事役(role:change が allow のとき)
tb role-broadcast [agent 名 ...]   # 動いている agent に「役割が更新された・tb role-show で読み直せ」と送る
tb roles [--json]                  # agent ごとの役割・版・状態(current|outdated|never|no-role)
```

- 人事役(agent)は、人事役の役割そのものと、map で人事役を付ける行は書けない(権限の自己拡大を防ぐ)。履歴は `tb config history` と同じ `config_changes`(書いた agent 名が残る)

- 同じセッションに同じ版(`_common.md` + 役割ファイルの SHA-256)を渡し済みなら、`resume` と `role-broadcast` では送らない。compact の後は渡し直す
- 一括送信は `[role-update / tb] ...` で始まる 1 行(版と役割名。ファイルのパスは送らない)。宛先が使用中なら送らず、終了コード 10

## 外への通知(notify / pr-watch)

調査結果などを Web UI の外(チームのチャンネル、ファイル)へ流す経路。**何を・いつ・どの形で出すかは tb が決める**(決定的なコード。LLM は本文を作らない)。agent は `tb comment` / `tb alert` などで内部に書くだけで、そこから通知のイベントが自動で積まれる(通知が失敗しても元のコマンドの結果は変わらない)。設計は `drafts/notify-routes.md`。

```
tb comment / alert / done / 障害の開閉 / pr-watch / 定期 ──▶ notify_events(DB) ──▶ 整形・秘匿フィルタ・伏せ字 ──▶ file / stdout / slack
```

- **既定は off。** 使うには `config/notify.conf.example` を `config/notify.conf`(gitignore 済み・任意)にコピーして編集し、`tb notify on`(人の端末から)。優先は 環境変数 `TB_NOTIFY_<キー大文字>` > `notify.conf` > 既定
- イベント: アラート登録 / アラートの調査結果 / PR の作成・approve・変更依頼・マージ(`tb pr-watch`) / question・blocked / done(成果物つき) / 障害の開・閉 / 承認待ち(L-/O- の番号と件数だけ) / 定期まとめ。progress と見回りの差分は積まない
- **PR をチケットに結ぶ**: `tb pr-watch` が見つけた PR の作成・approve・変更依頼・マージは、その PR の URL(`https://github.com/<repo>/pull/<n>`)を外部リンク(`--ref`)に持つチケットと、PR の題に書かれたチケット ID の履歴にも残る(`pr_opened` / `pr_approved` / `pr_changes` / `pr_merged`、変えた者 `system`)。チケットの状態は変えない(マージで done にするかは人か担当が決める)。通知の規則(`pr_merged label=release -> slack` など)の材料になる
- 本文は 1 行の短い日本語(タスク ID・重大度・要約・成果物のパス)。制御文字を除き、`@here` `@channel` と `<...>` は無効化、リンクは文字列のまま。生ログ・コードの囲みは落とす(`strip_raw_logs`)
- **秘密の値は常に止める(設定で変えられない)**: トークン・AWS のキー ID・秘密鍵のヘッダ・`key=value` 形式の秘密・長い base64/16 進を含むイベントは、slack にも webhook にも出さない。`data/notify/<日付>.md` に伏せた形だけを残し、`tb digest` で指揮官(人)に知らせる
- 伏せ字(`mask_*`): メールは既定で伏せる。リポジトリ名・IP・アカウント ID・ホスト名は、人の決定で既定は**伏せない**(必要なら on)
- 頻度: 1 分 6 通・1 時間 60 通(既定)。超えた分は 1 通にまとめる。同じ種別・対象は 10 分(`dedupe_sec`)に 1 回
- sink: `file`(`data/notify/<日付>.md` と、外へ送った全文を残す `sent.log`。置き場は taskboard/data/notify(git 管理外)。`TB_NOTIFY_DIR` で変える)/ `stdout`(`tb notify tail`)/ `slack`
  - `slack_via = agent`(既定): `tb notify flush`(scheduler の `notify-flush` ジョブ)が、整形済みの本文・hash・宛先を notifier agent へ送る(scheduler と同じ内部の送信の道なので `approvals.conf` の行は要らない)。notifier は本文をそのまま投稿し、`tb notify ack N-<n> <hash> [--id X]` で返す。hash が違えば警告、返事が無ければ再送(最大 3 回)して failed(情報は file に残る)。役は `drafts/new-roles/notifier.md`
  - `slack_via = webhook`: 権限 0600 のファイル(`webhook_file`。リポジトリの外)の 1 行目の URL へ curl で POST。URL は引数・記録・ログに出ない
- 宛先のチャンネル ID は `notify.conf` か環境変数にだけ書く(リポジトリに入れない。`tb notify status` も値は出さない)

```bash
tb notify status [--json]          # 設定とイベントの状態
tb notify on|off                   # 人だけ
tb notify test                     # 固定のダミー文を 1 通(人だけ)
tb notify dry-run                  # 送らずに、送る内容を表示
tb notify tail [-n N] [-f]         # stdout sink
tb notify events [--status S] [--json]
tb notify flush                    # 人の端末か scheduler から
tb notify ack N-<n> <hash> [--id X]   # notifier が結果を返す
tb pr-watch                        # 許可リストの PR を gh の読み取りだけで見る(scheduler の COMMAND ジョブ)
```

`tb pr-watch` は許可リスト `taskboard/config/repos.md`(`TB_REPOS_FILE` で変更可。git 管理外)の `- <org>/<repo>` の行のリポジトリだけを、`gh pr list` で読む。許可リストが無い・空、または notify が off なら何もせず終了コード 0。初めて見るリポジトリは基準線の記録だけで、既存の PR をイベントにしない。PR の題(短く)と状態だけを使い、本文・レビューのコメント・差分は読まない。ジョブの例は `scheduler/jobs/pr-watch.conf.example` と `notify-flush.conf.example`。

### 通知の規則(notify-rules。チケットのイベントで通知する)

チケットの履歴(`tb history`)のイベントに規則を当てて通知する(設計は [ADR 0003](../docs/adr/0003-ticket-events-and-notify-rules.md) の段 2)。規則は人だけが書く設定 `notify-rules`。例は [config/notify-rules.conf.example](config/notify-rules.conf.example)。

```sh
tb config set notify-rules < my-rules.conf   # 書く(誤りがあれば何も書かない)
tb notify rules                              # 今効いている規則と出どころ
tb notify rules test E-123                   # そのイベントにどの規則が当たるか(何も積まない)
```

```text
# <イベントの種類|*> [条件 ...] -> <届け先 ...>
label_added label=notify            -> slack webui     # ラベル notify が付いたら(誰が付けても)
created type=bug priority>=high    -> slack
status new=done project=web         -> conductor
label_added label=urgent actor=human -> slack           # 人が付けたときだけ
comment new=question                -> webui
```

- **イベントの種類**: `tb history` の種類(created / status / label_added / priority / comment など。`tb due-scan` の overdue / due_soon、`tb pr-watch` の pr_opened / pr_approved / pr_changes / pr_merged も)か `*`。
- **条件**: `type=` `priority=` `priority>=` `label=` `project=` `owner=` `new=` `old=` `actor=`。すべて満たした行が当たる。`label=` は、label_added / label_removed では付け外したラベルそのもの、ほかのイベントでは「今そのラベルを持っている」。`actor=` は名前か `human` / `system` / `agent`。
- **届け先**: `file` `stdout` `slack`(ほかの通知と同じ経路)、`webui`(Web UI の緊急通知と Chrome 通知に 24 時間出す)、`conductor`(そのプロジェクトの指揮役への次のまとめ = `tb digest`)。当たった行の届け先を合わせ、イベント 1 件につき通知 1 件(種別 `ticket`)。
- **本文**: `[チケット] T-web-012 ログイン画面 — ラベル +notify / high / bug / 担当 worker-2`。ID・題・イベント・優先度・種別・担当だけで、説明やコメントの本文は載せない。秘密の検出・伏せ字・頻度の上限は他の通知と同じく必ず通る。
- **誰が付けたラベルでも当たる**(人の決定)。agent が付けたラベルでも出る。絞りたい規則だけ `actor=human`(pane の外かどうかで決まるので強い保証ではない)。
- notify が `off` のあいだは何も積まない。`on` にしたとき、作られてから 60 分(`TB_NOTIFY_RULES_WINDOW_MIN`)を過ぎたイベントには当てない。規則を足す前のイベントにも当てない。
- 規則は足すだけで、今の決め打ちの通知(質問・ブロック・完了・アラート)はそのまま。チケットを書いた tb の最後と `tb notify flush` で当てる。

## draft PR の作成キュー(pr-queue)

agent のセッションに `gh` の書き込み権限を持たせないための仕組み。agent は PR を作らず、キューに置くだけ。PR を作るのは、人の端末で動く scheduler の COMMAND ジョブ(`tb pr-queue scan` → `tb pr-queue run`)。

```
tb pr-queue add --branch agent/<名> --body-file <本文> --title <題> [--repo <org/repo>] [--task T-…] [--base <ブランチ> | --depends-on PQ-<m>]   # agent。pending
tb pr-queue approve PQ-<n> / reject PQ-<n> [-m 理由]                                                  # 人だけ
tb pr-queue retry PQ-<n>                                                                              # 人だけ。failed を approved に戻す(attempts を 0 に)
tb pr-queue run [--dry-run]                                                                           # approved だけを gh pr create --draft
tb pr-queue scan [--dry-run] [--repo <org/repo>]                                                      # origin の <branch_prefix>* から積む
```

- ガードは run のたびにかけ直す: ブランチが `branch_prefixes`(無ければ `branch_prefix`)のどれかで始まる・base は登録時に決めた値(下を参照)・draft だけ・本文は `body_root` の中の実ファイル(シンボリックリンク不可)で、置いた(承認した)ときから変わっていない・1 行目が `body_first_line` に合う(設定したときだけ)・本文と題に秘密らしい値が無い・同じ head の PR が無い・リポジトリが許可リスト(`config/repos.md`)にある。
- `PR_QUEUE_REPO_DIR`(ローカルの clone)で差分も見る(**必須**。空・存在しない・git でないときは approve も run も scan も通さない)。CI 設定(`.github/workflows`・`.github/actions`)の変更、`AdministratorAccess` の追加、秘密らしい値は作らずに人に回す。
- マージ・approve・ready・close は実装していない。draft を ready にするのは人。
- 書き込み用の認証は、gh のラッパー(人が用意する)に持たせる。`PR_QUEUE_GH`(環境変数)か `config/pr-queue.conf` の `gh`(絶対パス)で指定する。**未指定・不正なら run / scan は警告して止まる**(PATH 上の gh には落ちない。fail-closed)。ラッパーの形と置き場は [`docs/pr-queue-gh-wrapper.md`](docs/pr-queue-gh-wrapper.md)。
- 認証で止まったとき(ラッパーが終了コード 75 を返す。ghtkn が token を返せない)は、attempts を使わず `auth-locked` として残りの行を止め、通知を状態が変わったときだけ 1 回出す。`failed` になったものは `tb pr-queue retry`(人だけ)で戻す。
- 公開前検査(conf の `deny_patterns`。**必須**。未設定・不正なら run は止まる。環境変数では決められない): run が PR を作る前に、origin と同期した上で(追跡参照の SHA が origin と一致しなければ止める)、題・本文・コミットメッセージ(author / committer を含む)・ブランチ名・差分(変更ファイルのパスと足した行)に規則ファイルを掛ける。バイナリの変更は人に回す。ヒットした PR は作らず却下し(attempts は使わない)、規則名と位置だけを通知する(値は出さない)。書式と例外ファイルは [`docs/pr-queue-deny-patterns.md`](docs/pr-queue-deny-patterns.md)。
- 既知の限界(詳細は [`docs/pr-queue-deny-patterns.md`](docs/pr-queue-deny-patterns.md)): `PR_QUEUE_CONF` で conf を差し替えられる(deny の規則ファイルは固定の conf のキーで決まり、env では差し替えられない)、`auto_approve` を上げると scan が人の approve なしで approved を積める(既定 0)、固定の conf も同一 OS ユーザーなら書ける(OS の権限分離=ADR 0001 の課題)、検査から PR 作成までの短い時間差は完全には閉じられない。
- `add` は、ブランチが origin に実在すること(`PR_QUEUE_REPO_DIR` の clone で `git ls-remote`)を確かめ、確認できなければ登録しない。登録できる接頭辞は conf の `branch_prefixes`(既定は `agent/` だけ。広げるのは人。例: `agent/ feat/ fix/`)。`scan` が走査するのは先頭の接頭辞だけ。
- base(PR の向き先): 既定は今までどおり `main` 固定(`base_mode = fixed`)。`base_mode = default_branch` にすると、origin の既定ブランチ(`git ls-remote --symref origin HEAD`。gh の権限は広げない)を使う。`add --base <ブランチ>` で明示もできる。明示できるのは既定の base と、conf の `base_allow`(グロブ。例: `release/* develop`)に合うブランチだけで、origin に実在すること。決めた base と決め方は `add` のときに行へ固定し、`show` / `list` に出る(人はそれを見て承認する)。`approve` と `run` はかけ直し、既定ブランチが登録時から変わった・`base_allow` から外れた・origin に無いときは PR を作らず理由を残して却下する(origin に聞けないときだけは行に触らず次の回)。`base_mode` を戻せば今までどおり(移行より前の行は `main`)。
- 積み重ね(依存する PR。第一段): `add --depends-on PQ-<m>` で、下の行のブランチを base にする(`show` に「PQ-<m> の上に積む」と、下の行には「上に積まれている」が出る)。下は pending / approved / created のどれかで、同じリポジトリ。上は下より先に承認してよいが、`run` は下が PR になる(created)まで上を作らずに待つ(行は触らない)。下が PR になるときに検査した head の SHA を残し、上を作る前に下のブランチが origin にあってその SHA のままか確かめる。違う・消えた・下が却下/失敗なら、上は作らず理由を残して却下する。途中で失敗しても、作り済みの行は created のままなので、やり直しで二重に作らない。差分のガードと公開前検査は下のブランチとの差分(上の分だけ)を見る。
  - 決めていること: マージの順(下から)は `show` に出すだけで、見張りはしない。下がマージ・close されたあとの上の付け替えは人。積み直し(force push)は自動ではしない。base が既定ブランチでない PR では CI が走らないことがある(`show` に出す)。複数の PR を 1 つの宣言(manifest)で出すのは後の段。
- 設定は `config/pr-queue.conf`(人だけ。`pr-queue.conf.example` を写して使う): `gh`、`deny_patterns`、`deny_exceptions`、`branch_prefixes`、`branch_prefix`(既定 `agent/`)、`body_root`(既定 `taskboard/data/pr-bodies`)、`body_first_line`、`auto_approve`(既定 0。1 なら scan で積んだものを承認済みにする)、`max_create_per_run`(既定 3)、`base_mode`(既定 `fixed`)、`base_allow`(既定は空 = 既定の base だけ)。

## 会話チーム(下書きの層と振り分け表)

人が読んで直してから送る返事の**下書き**を作る仕組み(issue #95 / #96)。送信・承認・状態の変更はしない。設計・書式・流れは [`conversation/README.md`](../conversation/README.md)。

- 振り分け(誰に・いつ・どこで・どの種別か)は `tb conv-route`。5 つの区分の名前だけを受け取り、人が書く決定論の表(設定 `conv-routes`)で決める。表に無い依頼は推測せず「未登録(人の判断)」
- 下書き(何を、どう言うか)は `tb conv-draft`。共通の土台(`conversation/_base.md`)+ 種別のチーム(`conversation/kinds/<種別>/prompt.md`)+ 採用済みのパターン。種別はファイルを足すだけで増やせる
- 声の役(率直・共感・受け手・簡潔)は `tb conv-voice` で、役の指示と下書きだけを渡す。`tb conv-voice-check` で 3 点以内・長さの判定を確かめる
- 振り分け表を変えるのは人だけ(`tb conv-routes-set`)。緩める・振り分け先を変える変更は `--approve-looser` が要る
- 文面は DB・コメント・記録に書かない。`TB_CONV_TEAMS=off` でチームの層を止める

## 送信の記録

tb が `herdr agent prompt` で送ろうとしたものは、結果にかかわらず `messages` に残る(`kind`=report|comment|digest|send|role|alert|scheduler|proposal、
`result`=sent|busy|failed、本文)。agent が台帳を書き忘れても、アプリ側の記録は欠けない。

## 宛先の設定

設定 `projects`(`project=指示役の agent 名` の行。下の「設定」の順で引く)に書く。

- `*=<名前>` は既定の宛先。書いていない project と、project に属さない観測(`tb digest` のアラート・spawn・scheduler の観測、アラートの解析結果)はここへ送る。`*` の行も無ければ `conductor` に送る(役割の名前に頼らない構成にするなら `*` を書く)
- `proposal=<名前>` は改善提案(`tb proposal`)の宛先と、提案チケットの担当。無ければ `TB_HR_NAME`(既定 `hr`)。この名前は指揮官(下の delivery の既定・nudge の除外)には数えない

`report` は宛先の状態を `herdr agent get` で見て、`idle` / `done` のときだけ送る。
`working` / `blocked` のほか、`unknown` や取得失敗でも送らない(既定は「割り込まずに pending に積む」)。

## 設定(`tb config`)

運用の設定は DB の `config` 表に置き、キーで引く(schema/013)。シングルポート・マルチセッションでも、セッションごとに違う設定を持てる。
本文は今までの conf ファイルの中身そのまま(書式は各節のとおり)。キーは `projects` / `approvals` / `grants` / `routes` / `delivery` / `limits` / `notify` / `compact` / `slash` / `sched-watch` / `spawn-policy` / `role-models` / `protect`(tb から止めさせない agent)/ `labels`(チケットに付けられるラベル)/ `conv-routes`(会話の振り分け表。書くのは `tb conv-routes-set` だけ)。

引く順(最初に見つかったもの):

1. 環境変数で明示したファイル(`TB_CONFIG` / `TB_APPROVALS` / `TB_GRANTS` / `TB_ROUTES` / `TB_LIMITS` / `TB_NOTIFY_CONF` / `TB_COMPACT_CONF` / `TB_SCHEDWATCH_CONF` / `TB_SPAWN_POLICY` / `TB_ROLE_MODELS` / `TB_CONV_ROUTES`。テストや一時の差し替え)
2. DB の**このセッション**の行
3. DB の**全セッション共通**の行(`herdr_session='*'`)
4. checkout の `taskboard/config/<名前>.conf`(DB に取り込むまでの移行用。git 管理外)
5. `<名前>.conf.example`(projects / approvals / limits / compact / slash / sched-watch / spawn-policy。コードと一緒に版を合わせる既定値)

```bash
tb config                          # キーごとの出どころ(db:<セッション> / db:* / file:<パス> / example:<パス> / none)
tb config show approvals           # 今効いている本文
tb config import [--dry-run]       # checkout の config/*.conf を共通の行として取り込み、checkout から退かす(最初に 1 回。共通の行があるキーは飛ばす)
tb config edit approvals           # このセッションの行を $EDITOR で編集(--shared で共通の行)
tb config set routes < routes.conf # 標準入力の本文で置き換える(--shared で共通の行)
tb config unset routes             # このセッションの行を消す(共通の行・ファイルに戻る)
tb config history approvals        # 変更の履歴(config_changes)
```

- `tb allow` / `tb ask` / `tb notify set` / `tb limits` も、今効いている本文を元に、このセッションの行を書く(環境変数でファイルを明示していればそのファイル)
- 書くのは人の端末だけ。ファイルだったときと同じく、agent には DB を直接書かせない(`permissions.deny` で `sqlite3` での DB への書き込みや `tb config` を拒否しておく)
- **agent は設定を `tb config show <キー>` で読む**(agent の pane は自分の herdr セッションで動くので、自分のセッションの本文だけが返る)。
  ファイルを直接読むと、別のセッション向けや古い本文を読みかねない。そのため `tb config import` は取り込んだファイルを `~/.local/state/herdr/config-imported/<日時>/` へ退かす(戻すときはそこからコピー)。
  念のため `permissions.deny` に `Read(//<ORCH_DIR>/taskboard/config/*.conf)` を入れておくとよい
- ファイルに残す設定: この Mac 全体を守るもの(gate 系・risk-rules・pr-queue・proxy-allowlist・repos.md・spawn-rules)と、コードと版を合わせる既定値(`approvals-catalog.conf`・`*.example`)。役割(roles/)は今は `roles/sessions/<セッション>/` で分ける
- Web UI も同じ順で引く(`webui/app/data.py` の `config_text`。今は projects と approvals)

## ファイルの構成

`bin/tb` は入口だけで、中身は `lib/` に機能ごとに分かれている。コマンド `tb <名前>` は `lib/*.sh` の `cmd_<名前>` で、一覧を別には持たない(新しいコマンドは関数を足し、`lib/help.txt` に 1 行書く。テストが突き合わせる)。

| ファイル | 中身 |
|---|---|
| `lib/db.sh` | `sql` / `q` / スキーマの移行(`schema/NNN_*.sql`) |
| `lib/config.sh` | 設定をキーで引く(`config_text` / `config_has`)・書く(`config_put`)・`tb config` |
| `lib/core.sh` | エラー終了・入力の検査・タスク行の取得・project ごとの指示役 |
| `lib/transport.sh` | herdr との境界。herdr を実行するのはここの `herdr_call` だけ。送信と `messages` への記録(`herdr_send`)、`system-send` |
| `lib/tasks.sh` | add / list / show / claim / update / done / block / report / pending |
| `lib/conv.sh` | 会話チーム: 振り分け表(`tb conv-route` / `conv-routes-set`)・下書きの指示(`conv-draft`)・声の役(`conv-voice` / `conv-voice-check`)・パターンの台帳 |
| `lib/proxy.sh` / `activity.sh` / `comments.sh` / `digest.sh` / `send.sh` / `roles.sh` / `approvals.sh` / `alerts.sh` / `review.sh` / `agents.sh` / `spawn.sh` / `limits.sh` / `shed.sh` / `handoff.sh` / `dbimport.sh` | 各節のコマンド |

agent への送信も DB への書き込みも、すべて `tb` を通る。後で MCP サーバーなど別の経路を足すときは、`tb` のコマンドを呼ぶか `lib/transport.sh` を差し替えればよい。

## セッションと DB

DB は 1 つで、全部の herdr セッションが共有する(既定は `${XDG_STATE_HOME:-~/.local/state}/herdr/tasks.db`。`TB_DB` で変えられる)。
行はすべて `herdr_session` 列を持ち、tb は**今のセッション**(`tb whoami` の session。`HERDR_SOCKET_PATH` > `HERDR_SESSION` > `herdr-session` ファイル > `default`)の行だけを読み書きする。
別のセッションのタスク・報告・承認待ち・アラート・agent の台帳は見えず、操作もできない(`tb agent-stop` などで名前が別のセッションの台帳にだけあるときは、`HERDR_SESSION=<名前>` を付けるようヒントを出す)。

- タスク ID(`T-<project>-NNN`)だけは DB 全体で一意(採番は全セッションの行を見る)。C- / O- / A- / L- などの番号も DB 全体の通し番号
- agent の台帳・`tb_flags`・`notify_seen`・`observations` は、セッションとの組で一意(同じ agent 名が別のセッションにいてよい)
- hook の活動記録(`agent_activity`)は Claude の session id で引く。書いたセッションも残り、`tb activity` の一覧はセッションごと
- Web UI も同じ DB を、自分のセッションの行だけ読む(`webui/app/data.py` の一時ビュー)
- 共有の SQL(`sql/agent_edges.sql`)は表の名前のまま書き、tb も Web UI も同じ一時ビュー(下の「SQL の書き方」)で絞る

### SQL の書き方(tb の `lib/db.sh`)

tb の `sql()` は、接続ごとに `herdr_session` 列を持つ表(一覧は [`sql/session_tables`](sql/session_tables))について 2 つを作る。

- **一時ビュー**: 表と同じ名前の、今のセッションの行だけのビュー。読みは表の名前のまま書き、`herdr_session` では絞らない(ビューが絞る)。全セッションを見る読み(タスク ID の採番、別セッションにいる agent のヒント、`session_id` での活動記録の引き当て、共通の設定 `'*'`)は `main.<表>` と書く
- **書き込みの見張り**: `main.<表>` への INSERT / UPDATE / DELETE が別セッションの行に触れたら `RAISE(ABORT)` で止める(設定の `config` / `config_changes` だけは共通の行 `'*'` も書ける)。絞り忘れが「黙って他人の行を変える」ではなく「エラーで止まる」になる

書き込みは必ず `main.<表>` と書く(ビューには書けず、SQLite が `cannot modify … because it is a view` で止めるので付け忘れも分かる)。
INSERT は `herdr_session` 列に `$SQS` を、UPDATE / DELETE は `WHERE herdr_session=$SQS AND …` を書く(見張りは最後の防ぎで、絞りは書く)。
Web UI(`webui/app/data.py` の `session_views`)も同じ一覧から同じビューを作る(書き込みはしない)。
ビュー経由(INSTEAD OF トリガー)にしないのは、`changes()` / `last_insert_rowid()` / `RETURNING` がビュー経由では効かないため。
見張りなしの `sql_all` を使うのは、スキーマの移行、hook の活動記録(会話が別のセッションで再開されたら行ごと移す)、`tb config import` の別セッションの行の取り込みだけ。
表を足して `herdr_session` 列を持たせたら `sql/session_tables` にも足す(テスト `89_session_guard` がスキーマとの食い違いを落とす)。

### 旧形式の DB の取り込み(`tb db-import`)

以前は checkout ごとに `taskboard/data/tasks.db` があった(1 セッション = 1 DB)。中身を共有の DB へ移すには、人の端末で:

```bash
HERDR_SESSION=<その checkout のセッション> tb db-import /path/to/checkout/taskboard/data/tasks.db --dry-run   # 件数と付け替えを見るだけ
HERDR_SESSION=<その checkout のセッション> tb db-import /path/to/checkout/taskboard/data/tasks.db
```

- 元の DB は読むだけ(写しを作って移行し、1 つのトランザクションで足す)。同じファイルは 2 回取り込まない
- 番号(C- / O- / A- など)は共有の DB の後ろへずらし、`messages` / `notify_events` の参照も直す。タスク ID がぶつかれば次の空き番号に付け替え、親・コメント・送信などの参照も直す(付け替えは一覧に出る)
- agent の台帳・`observations` などキーが主キーの表は、共有の DB に既にある行を優先する(agent は空の値だけ旧 DB で埋める)
- `setup/check.sh` は、取り込んでいない旧形式の DB を `taskboard:legacy-db` で知らせる

## スキーマ

定義は [`schema/`](schema/) の `NNN_*.sql` にあり、DB の `PRAGMA user_version` より新しい番号だけを tb が順に流す(`lib/db.sh`)。
版が最新なら `PRAGMA` を 1 回読むだけで済む。テーブルや列を変えるときは既存ファイルを書き換えず、次の番号のファイルを足す。
版を付ける前に作られた DB(`user_version=0`)も、中身を保ったまま最新になる。
012 から全部の表に `herdr_session` がある(移行のとき、既にある行は移行を流したセッションのものになる)。下の列の一覧では省く。

`tasks(id, project, title, owner_agent, session_id, account, status, parent_id, deliverables(JSON 配列), summary, note, reported_at, created_at, updated_at)`
`proxy_requests(id, task_id, cmd, why, effect, expect, status, result, created_at, updated_at)`
`effect` は `read-only|write|external`、`status` は `open|done|pending-human|denied`。
`comments(id, task_id, kind, text, deliverables, from_name, from_pane, from_session, delivered_at, created_at)`
`agent_activity(session_id PK, pane_id, tab_id, workspace_id, cwd, last_prompt_id, last_prompt_at, last_stop_at, updated_at)`
`messages(id, kind, ref, target, body, result, created_at)` / `alerts(id, fingerprint, severity, title, body, source, source_pane, status, count, analyst, note, result_*, first_seen, last_seen, analysis_started_at, analyzed_at, closed_at)` / `proposals(id, role_file, title, text, status, by_name, note, delivered_at, resolved_at, created_at)` / `reviews(id, since_at, until_at, created_at)` / `approval_changes(id, pattern, value, created_at)` / `role_briefings(id, session_id, agent, role_file, hash, source, via, created_at)` / `outbox(id, from_name, from_pane, from_session, target, kind, task_id, text, status, reason, sent_at, created_at)` / `digests(id, target, item_count, body, created_at)` / `observations((herdr_session, key) PK, kind, task_id, text, digest_id, created_at)` / `agents((herdr_session, name) PK, kind, cwd, model, role, session_id, pane_id, herdr_session, state, killable, killable_reason, killable_by, killable_at, killable_session, created_by, created_at, updated_at, stopped_at)` / `agent_ops(id, op, target, args, requested_by, status, result, created_at, decided_at)`
`tasks.status` は `todo|in_progress|review|done|blocked|cancelled`(`cancelled` は 023 で追加)。`summary`(done の要約)と `note`(block の理由)は設計メモの最小案への追加。
時刻は UTC の ISO 8601。

## 環境変数

`TB_DB`(DB パス。既定は全セッション共有の `~/.local/state/herdr/tasks.db`) / `TB_CONFIG`(projects.conf) / `HERDR_BIN`(herdr の実体。既定 `herdr`) / `TB_SCHEMA_DIR`(テスト用)
DB パスと herdr の既定値は部品で共通の [`lib/herdr-env.sh`](../lib/herdr-env.sh)(Python は `lib/herdr_env.py`)で決まる。
`TB_ROUTES`(routes.conf) / `TB_DELIVERY`(delivery.conf) / `TB_HR_NAME`(hr) / `TB_SCHED_LOGS` / `TB_REVIEW_TOP`(8) / `TB_ANALYST_MAX`(2) / `TB_ANALYST_CWD` / `TB_ANALYST_MODEL` / `TB_AGENT_CWD` / `TB_SWEEPER_PATTERN`(`sweeper*`) / `TB_LIMITS`(limits.conf) / `TB_LIMIT_<キー大文字>`(各制限の上書き) / `TB_HANDOFF_DIR`(handoff の置き場) / `TB_SPAWN_RETRY_SEC`(3) / `TB_APPROVALS`(approvals.conf) / `TB_ROLES`(roles/) / `TB_ROLE_MAX`(12000) / `TB_SEND_MAX`(4000) / `TB_IMMEDIATE_KINDS`(既定 `question blocked`) / `TB_DIGEST_IDLE_MIN`(10) / `TB_DIGEST_STUCK_MIN`(30) / `TB_DIGEST_ITEM_MAX`(160) / `TB_DIGEST_MSG_MAX`(1800) / `TB_NUDGE_IDLE_MIN`(15) / `TB_NUDGE_MAX`(3)

## テスト

```bash
bash test/run.sh
```

機能ごとのテストは `test/cases/NN_*.sh`(`lib/` のファイルにおおむね対応)。`run.sh` はケースを「ユニット」ごとに別のプロセス・別の一時ディレクトリ(DB・設定・疑似 herdr の記録)で流し、ユニット同士は並列に流す(`-j N` で本数。既定は CPU の数、`TB_TEST_JOBS` でも変えられる)。
- ユニットは 1 ケースが基本。前のケースが作った状態(変数・DB・設定ファイル)を使うケースだけ、`test/groups` に 1 行で書いて同じシェルで番号順に流す。新しいケースは、前の状態を使わなければ書き足さなくてよい(`project` や `TB_DB` などは自分で用意する)
- どのユニットでも最後に `99_zzz_session_sweep.sh` を流す(そのユニットが作った DB を見る)
- `bash test/run.sh 93_prqueue.sh` のようにケースを指定すると、そのケースを含むユニットだけ流す。`--units` でユニットの一覧
- CI は `--shard 1of4` … `4of4` の組ごとに別のジョブで流す(`--list-shards` で組の一覧、`--shard-cases <組>` で組のケース)。組は `test/weights`(ケースごとの秒数)でおおむね揃うように自動で分ける。ケースを足して偏ったら `bash test/run.sh --timings` で `weights` を書き直す(無いケースは 10 秒とみなす)
`HERDR_BIN` を `test/fake-herdr`(疑似 herdr)に固定して実行する。本物の herdr は呼ばない。
`FAKE_STATUS`(idle / working / blocked / unknown / missing)で宛先の状態を、`FAKE_PROMPT_RC` で送信の成否を切り替えられる。
`test/fake-herdr` には実行権限(`chmod +x`)が必要。

## 既知の制限

- UI はない。`done` のタスクの再オープンと、複数 agent が同じ ID を同時に `done` する競合は考慮していない
- 宛先の状態確認と送信の間に状態が変わる競合は残る(その場合は届いたかを `herdr agent read` で確認する)
- 定期回収は scheduler の `taskboard-digest` ジョブ(`tb digest`)で行う。入れていないと `done` / `progress` の報告は届かない
