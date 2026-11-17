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
- `report` は指示役へ `[T-xxx done] 要約 / 成果物パス` を送る。終了コードで結果が分かる:

| code | 意味 | worker がやること |
| --- | --- | --- |
| 0 | 送信済み(または報告済み) | 終了してよい |
| 10 | 宛先の指示役が working / blocked 等で**未送信** | 何もしない。`pending` に残り、あとで回収される(必要なら少し待って再実行) |
| 11 | 送信または宛先の状態取得に失敗 | 同上。繰り返し 11 なら指示役の名前(設定 `projects`。`tb config show projects`)を確認 |

`reported_at` は `report` が成功したときだけ入る。10 / 11 のときは空のまま `tb pending` に出る。

`tb` は PATH に無いので、`taskboard/bin/tb` のフルパスで呼ぶか PATH に足す。

### 作業の始め方(任意)

```bash
tb claim T-<project>-NNN --owner <自分の agent 名> [--session <session id>] [--account <max|enterprise>]
tb update T-<project>-NNN --status review     # todo|in_progress|review|blocked
tb block  T-<project>-NNN -m "詰まっている理由"
```

## 指示役 / 人向け

```bash
tb add -p <project> -t "タイトル" [--parent T-..] [--owner N]   # ID を表示(T-<project>-NNN)
tb list [-p project] [-s status] [--json]
tb show T-<project>-NNN
tb pending [--json|--ids]     # done なのに未報告のもの(id / project / 宛先 / 要約)
tb pending --ids | xargs -n1 tb report    # 未報告の再送(scheduler から呼ぶ想定)
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
tb report C-<n>      # 未配送のコメントの再送(tb pending に C-<n> で出る)
```

- 報告者は `HERDR_PANE_ID` + `herdr agent list` から決まる(`--from` などの上書きはエラー)。特定できなければ保存せず終了コード 3
- `question` / `blocked` は保存してすぐ送る。`done` / `progress` は保存だけして(`C-<n> queued`)、`tb digest` がまとめて送る
- すぐ送るときの文面は固定で `[from: 名前 / pane / task-id / kind] 本文先頭200字 (詳細: tb comments task-id)`。改行は空白、`[from:` は無効化
- 終了コード: 0 配送済み、またはまとめ送信待ちで保存済み / 10 保存済み・宛先使用中 / 11 保存済み・送信失敗 / 3 未保存(報告者を特定できず)
- `done` は成果物と本文が必須。`question` / `blocked` は `tb comments` で `needs-human` と表示され、`--open` で未対応のものだけに絞れる
- 人が対応したら `tb resolve C-<n>`(または Web UI の「対応済みにする」)で畳む。agent の pane からは拒否する。畳むと Web UI の要対応・緊急通知から消える
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
  - `TB_DIGEST_STUCK_MIN`(既定 30)分以上 `working` のまま
  - herdr 上で `blocked`(承認ダイアログ待ちなど)。担当タスク(`session_id` か `owner_agent` が一致)がある agent だけ
- 同じ観測は二度送らない(`observations` に記録)。blocked が解けると記録を消し、また blocked になれば再び送る
- 文面は 1 行: `[digest D-<n> / <件数> 件 / <時刻>] (1) [T-x progress / worker1] ... | (2) [T-y 観測 / w3] ... (全文: tb digests D-<n>)`。
  項目の本文は 160 字まで、全体は `TB_DIGEST_MSG_MAX`(既定 1800)字まで。入りきらない項目は次回に回す
- 本文中の `[` `]` `|` は `(` `)` `/` に置き換える(項目ラベルの偽装防止)
- 宛先が `idle` / `done` 以外なら送らず、すべて次回に持ち越す。終了コード 0 / 10 / 11 は `report` と同じ

## 指揮官 → worker の送信(send)

agent 同士は `herdr agent prompt` を直接打たず、`tb send` を通す。tb が送信者を特定し、ルールを当て、記録してから送る。

```bash
tb send <宛先 agent 名> --text '本文' [--task T-<project>-NNN] [--kind task|info|answer] [--json]
tb flush                                   # 宛先が使用中で積まれたものを届ける(tb digest も最初に呼ぶ)
tb outbox [--status queued|sent|denied] [--json]
```

- 送信者は `HERDR_PANE_ID` + `herdr agent list` から決まる(`--from` などはエラー)。宛先は herdr にいる agent 名だけ
- 宛先ルールは `config/routes.conf`(例: `config/routes.conf.example`)。`<送信者> -> <宛先>` の glob を上から見て、最初に一致した行で決まる。
  行頭 `!` は拒否。どれにも一致しなければ拒否。**ファイルが無いときは「指揮官(`conductor` と `projects.conf` の値)→ 誰にでも」だけ許可**
- 届く文面は `[from: <名前> / <pane> / <task-id か -> / <kind>] <本文>`(1 行化、`[from:` の偽装は無効化、`TB_SEND_MAX` 字まで)
- 宛先が `idle` / `done` 以外なら `outbox` に積み、`tb flush` で古い順に届ける(同じ宛先の後続は追い越さない)
- 拒否したものも `outbox` に `denied` と根拠(`reason`)つきで残る
- 終了コード: 0 送信済み / 10 宛先使用中で積んだ / 11 送信失敗で積んだ / 4 ルールで拒否 / 20 人の承認待ち(approvals)/ 3 送信者を特定できず未保存
- agent に `herdr agent prompt` を直接打たせないようにするには、その agent の Claude Code 設定で拒否する(`conductor/README.md`)

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
  `--prompt` は起動後に最初の指示として送る
- **停止**: その agent の pane を `herdr pane close` で閉じる(herdr に agent 単体の停止は無い)。止める前に session と cwd(hook の記録を優先)を台帳に残す。
  止められるのは **killable=yes で、未完了(in_progress / review / blocked)のタスクを持っていない**ときだけ。人は `--force` で例外にできる。自分自身は止められない
- **復活**: 台帳の cwd で新しい workspace を作り、`claude --resume <session>` で同じ会話から再開する(claude の場合。再開できない kind は終了コード 12 で止まり、`--fresh` なら新しい会話)。
  モデルは台帳の値か `--model`。復活したら判定フラグは `unknown` に戻る
- **再起動**(`tb agent-restart <名前> [--prompt P]`): MCP の接続が切れた agent を、pane を閉じて `claude --resume` で同じ会話に戻して張り直す。killable の代わりに次の安全条件を全部見る
  (`--force` は無い): 現在の herdr セッションで動いている / herdr の状態が `idle` か `done`(`working`・`blocked` は拒否) / in_progress のタスクを持たない
  (持つときは `--prompt` の再開メモがあるときだけ) / session と cwd が台帳にあり会話を再開できる(だめなら止めずに拒否) / 動作中が `max_running_agents` を超えない。
  人はいつでも実行できる。agent の pane からは自分自身と指揮官を除き、`agent:stop:<名前>` と `agent:revive:<名前>` が**両方 allow** のときだけ(それ以外は何もせず終了コード 4。承認待ちには積まない)。
  `--prompt` が無ければ「MCP を再接続した。前の作業を続け、MCP のツールが使えることを確かめ、まだなら tb comment で報告」という既定の一文を送る(準備完了を待ち、受け付けられたかも確かめる)。
  復活に失敗したら停止のまま残し、`tb agent-revive <名前> [--fresh]` を出して 0 以外で終わる(新しい会話は勝手に作らない)。
  `agent_ops` には op=revive、args.restart=1 の 1 行で、依頼者・停止と復活の結果・session が変わらなかったか・所要秒を残す。再開したが最初の指示を確認できないときは終了コード 10
- **判定フラグ(killable)**: `yes|no|unknown`。付けられるのは判定役(`roles/sweeper.md`、軽いモデル。`TB_SWEEPER_PATTERN`、既定 `sweeper*`)と人だけ。
  yes を付けた後でその agent が新しい入力を受けたら、または会話が変わったら `stale` になり、停止には使えない(付け直しが要る)。
  解析役(`analyst-A<n>`)は `tb alert-result` を返した時点で tb が yes を付ける。判定役の定期実行は `scheduler/jobs/sweeper.conf.example`
- **誰が実行できるか**: 人の端末(と Web UI)からはそのまま実行する。agent の pane からは `approvals.conf` の `agent:<op>:<名前>`(op は `new|stop|revive`、既定 `agent:* = ask`)を見て、
  `ask` なら `L-<n>` として人の承認待ちに積む(終了コード 20。Web UI の緊急通知と agent 一覧に出る)。`allow` ならそのまま実行する(停止の前提はそれでも確かめる)。`--force` は人だけ
- herdr のセッション(`HERDR_SESSION`、プリセットの `# session:`)ごとに台帳を分けて扱う。別セッションの agent は同期で「止まった」扱いにせず、操作するときは `HERDR_SESSION=<名前>` を付けるよう案内する
- 実行したもの・断ったもの・承認待ちは `agent_ops` にすべて残る。終了コード: 0 実行済み / 1 拒否・前提違い / 11 herdr の失敗 / 20 承認待ち

### agent の関係グラフ(`tb agents --graph`)とロール

agent 同士の関係は 1 本の木ではなく、`主体 -動詞→ 対象` のエッジの集まり(メッシュ)として出す。1 体が複数の上流(spawn の親・複数 project の指揮官・送信者)を持ってよい。
エッジは既存の記録から作る(`taskboard/sql/agent_edges.sql`。Web UI の /agent-graph も同じ SQL を読む)。

| 動詞 | 元の記録 | 意味 |
|---|---|---|
| spawn | 台帳 `agents.spawned_by` | 親が `tb agent-spawn` で子を立てた |
| lead | 未完了タスク(in_progress / review / blocked)の project | その project の指揮官(設定 `projects`)→ 担当 agent |
| send:task / send:info / send:answer | `tb send` の outbox(直近 7 日) | 依頼・連絡・回答。routes.conf で拒否された試みは `[denied]` |
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

- `config/routes.conf`: `@conductor -> @worker`、`!@worker -> @conductor`(送信者・宛先の両方)
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
- agent から実行できるのは人事役(`@hr`。`TB_ROLE_ADMIN` で変えられる)だけ。`approvals.conf` の `role:change` が allow でなければ終了コード 20 で止まり、人の実行を求める
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
- どれかの行に当たれば allow。当たらなければ従来どおり `approvals.conf` が決める。許可を足すだけで、`routes.conf` の拒否などは外さない
- 使う場所: `tb peek`(`agent:read`)、`tb send`(`send:<kind>`)、`tb agent-stop` などの agent 操作、`tb role-set`(`role:change`。人事役だけ・自分は不可・人事役は付けられない、の制限はそのまま)
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
- `tb digest` が知らせる: 子の数が上限の 1 つ手前以上(全体、または 1 体の親)/ 親のタスクが done・blocked なのに子が動いている。子がいなければ何も出さない

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
記録を見直し、役割(プロンプト)の改善を設定役(人事 `hr`、`TB_HR_NAME`)に提案する。設定役が `roles/` を直す(`role:change` の承認規則に従う)。

```bash
tb review-pack [--since ISO] [--mark]   # レビュー材料。生ログではなく集計と上位だけ(トークン節約)。省略時は前回 --mark より後
tb proposal --role <roles のファイル|-> --title T (--text X | --text-file F)   # 会計役。設定役へすぐ送る(使用中なら digest が再送)
tb proposals [P-<n>] [--status open|applied|rejected]
tb proposal-resolve P-<n> --status applied|rejected [--note N]   # 設定役か人だけ
```

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

## draft PR の作成キュー(pr-queue)

agent のセッションに `gh` の書き込み権限を持たせないための仕組み。agent は PR を作らず、キューに置くだけ。PR を作るのは、人の端末で動く scheduler の COMMAND ジョブ(`tb pr-queue scan` → `tb pr-queue run`)。

```
tb pr-queue add --branch agent/<名> --body-file <本文> --title <題> [--repo <org/repo>] [--task T-…]   # agent。pending
tb pr-queue approve PQ-<n> / reject PQ-<n> [-m 理由]                                                  # 人だけ
tb pr-queue retry PQ-<n>                                                                              # 人だけ。failed を approved に戻す(attempts を 0 に)
tb pr-queue run [--dry-run]                                                                           # approved だけを gh pr create --draft
tb pr-queue scan [--dry-run] [--repo <org/repo>]                                                      # origin の <branch_prefix>* から積む
```

- ガードは run のたびにかけ直す: ブランチが `branch_prefixes`(無ければ `branch_prefix`)のどれかで始まる・base は main 固定・draft だけ・本文は `body_root` の中の実ファイル(シンボリックリンク不可)で、置いた(承認した)ときから変わっていない・1 行目が `body_first_line` に合う(設定したときだけ)・本文と題に秘密らしい値が無い・同じ head の PR が無い・リポジトリが許可リスト(`config/repos.md`)にある。
- `PR_QUEUE_REPO_DIR`(ローカルの clone)で差分も見る(**必須**。空・存在しない・git でないときは approve も run も scan も通さない)。CI 設定(`.github/workflows`・`.github/actions`)の変更、`AdministratorAccess` の追加、秘密らしい値は作らずに人に回す。
- マージ・approve・ready・close は実装していない。draft を ready にするのは人。
- 書き込み用の認証は、gh のラッパー(人が用意する)に持たせる。`PR_QUEUE_GH`(環境変数)か `config/pr-queue.conf` の `gh`(絶対パス)で指定する。**未指定・不正なら run / scan は警告して止まる**(PATH 上の gh には落ちない。fail-closed)。ラッパーの形と置き場は [`docs/pr-queue-gh-wrapper.md`](docs/pr-queue-gh-wrapper.md)。
- 認証で止まったとき(ラッパーが終了コード 75 を返す。ghtkn が token を返せない)は、attempts を使わず `auth-locked` として残りの行を止め、通知を状態が変わったときだけ 1 回出す。`failed` になったものは `tb pr-queue retry`(人だけ)で戻す。
- 公開前検査(conf の `deny_patterns`。**必須**。未設定・不正なら run は止まる。環境変数では決められない): run が PR を作る前に、origin と同期した上で(追跡参照の SHA が origin と一致しなければ止める)、題・本文・コミットメッセージ(author / committer を含む)・ブランチ名・差分(変更ファイルのパスと足した行)に規則ファイルを掛ける。バイナリの変更は人に回す。ヒットした PR は作らず却下し(attempts は使わない)、規則名と位置だけを通知する(値は出さない)。書式と例外ファイルは [`docs/pr-queue-deny-patterns.md`](docs/pr-queue-deny-patterns.md)。
- 既知の限界(詳細は [`docs/pr-queue-deny-patterns.md`](docs/pr-queue-deny-patterns.md)): `PR_QUEUE_CONF` で conf を差し替えられる(deny の規則ファイルは固定の conf のキーで決まり、env では差し替えられない)、`auto_approve` を上げると scan が人の approve なしで approved を積める(既定 0)、固定の conf も同一 OS ユーザーなら書ける(OS の権限分離=ADR 0001 の課題)、検査から PR 作成までの短い時間差は完全には閉じられない。
- `add` は、ブランチが origin に実在すること(`PR_QUEUE_REPO_DIR` の clone で `git ls-remote`)を確かめ、確認できなければ登録しない。登録できる接頭辞は conf の `branch_prefixes`(既定は `agent/` だけ。広げるのは人。例: `agent/ feat/ fix/`)。`scan` が走査するのは先頭の接頭辞だけ。
- 設定は `config/pr-queue.conf`(人だけ。`pr-queue.conf.example` を写して使う): `gh`、`deny_patterns`、`deny_exceptions`、`branch_prefixes`、`branch_prefix`(既定 `agent/`)、`body_root`(既定 `taskboard/data/pr-bodies`)、`body_first_line`、`auto_approve`(既定 0。1 なら scan で積んだものを承認済みにする)、`max_create_per_run`(既定 3)。

## 送信の記録

tb が `herdr agent prompt` で送ろうとしたものは、結果にかかわらず `messages` に残る(`kind`=report|comment|digest|send|role|alert|scheduler|proposal、
`result`=sent|busy|failed、本文)。agent が台帳を書き忘れても、アプリ側の記録は欠けない。

## 宛先の設定

設定 `projects`(`project=指示役の agent 名` の行。下の「設定」の順で引く)に書く。書いていない project は `conductor` に送る。

`report` は宛先の状態を `herdr agent get` で見て、`idle` / `done` のときだけ送る。
`working` / `blocked` のほか、`unknown` や取得失敗でも送らない(既定は「割り込まずに pending に積む」)。

## 設定(`tb config`)

運用の設定は DB の `config` 表に置き、キーで引く(schema/013)。シングルポート・マルチセッションでも、セッションごとに違う設定を持てる。
本文は今までの conf ファイルの中身そのまま(書式は各節のとおり)。キーは `projects` / `approvals` / `grants` / `routes` / `limits` / `notify` / `compact` / `sched-watch` / `spawn-policy`。

引く順(最初に見つかったもの):

1. 環境変数で明示したファイル(`TB_CONFIG` / `TB_APPROVALS` / `TB_GRANTS` / `TB_ROUTES` / `TB_LIMITS` / `TB_NOTIFY_CONF` / `TB_COMPACT_CONF` / `TB_SCHEDWATCH_CONF` / `TB_SPAWN_POLICY`。テストや一時の差し替え)
2. DB の**このセッション**の行
3. DB の**全セッション共通**の行(`herdr_session='*'`)
4. checkout の `taskboard/config/<名前>.conf`(DB に取り込むまでの移行用。git 管理外)
5. `<名前>.conf.example`(projects / approvals / limits / compact / sched-watch / spawn-policy。コードと一緒に版を合わせる既定値)

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
`tasks.status` は `todo|in_progress|review|done|blocked`。`summary`(done の要約)と `note`(block の理由)は設計メモの最小案への追加。
時刻は UTC の ISO 8601。

## 環境変数

`TB_DB`(DB パス。既定は全セッション共有の `~/.local/state/herdr/tasks.db`) / `TB_CONFIG`(projects.conf) / `HERDR_BIN`(herdr の実体。既定 `herdr`) / `TB_SCHEMA_DIR`(テスト用)
DB パスと herdr の既定値は部品で共通の [`lib/herdr-env.sh`](../lib/herdr-env.sh)(Python は `lib/herdr_env.py`)で決まる。
`TB_ROUTES`(routes.conf) / `TB_HR_NAME`(hr) / `TB_SCHED_LOGS` / `TB_REVIEW_TOP`(8) / `TB_ANALYST_MAX`(2) / `TB_ANALYST_CWD` / `TB_ANALYST_MODEL` / `TB_AGENT_CWD` / `TB_SWEEPER_PATTERN`(`sweeper*`) / `TB_LIMITS`(limits.conf) / `TB_LIMIT_<キー大文字>`(各制限の上書き) / `TB_HANDOFF_DIR`(handoff の置き場) / `TB_SPAWN_RETRY_SEC`(3) / `TB_APPROVALS`(approvals.conf) / `TB_ROLES`(roles/) / `TB_ROLE_MAX`(12000) / `TB_SEND_MAX`(4000) / `TB_IMMEDIATE_KINDS`(既定 `question blocked`) / `TB_DIGEST_IDLE_MIN`(10) / `TB_DIGEST_STUCK_MIN`(30) / `TB_DIGEST_ITEM_MAX`(160) / `TB_DIGEST_MSG_MAX`(1800)

## テスト

```bash
bash test/run.sh
```

機能ごとのテストは `test/cases/NN_*.sh`(`lib/` のファイルにおおむね対応)で、`run.sh` が番号順に同じシェルで流す。
`HERDR_BIN` を `test/fake-herdr`(疑似 herdr)に固定して実行する。本物の herdr は呼ばない。
`FAKE_STATUS`(idle / working / blocked / unknown / missing)で宛先の状態を、`FAKE_PROMPT_RC` で送信の成否を切り替えられる。
`test/fake-herdr` には実行権限(`chmod +x`)が必要。

## 既知の制限

- UI はない。`done` のタスクの再オープンと、複数 agent が同じ ID を同時に `done` する競合は考慮していない
- 宛先の状態確認と送信の間に状態が変わる競合は残る(その場合は届いたかを `herdr agent read` で確認する)
- 定期回収は scheduler の `taskboard-digest` ジョブ(`tb digest`)で行う。入れていないと `done` / `progress` の報告は届かない
