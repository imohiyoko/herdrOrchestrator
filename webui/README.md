# webui — herdr 進捗の Web UI

agent・タスク・報告・定期実行・送信履歴・権限を、ブラウザで見るための小さなサーバー。
Python 3 の標準ライブラリだけで動く(依存なし)。Mermaid の図を描くための `mermaid.min.js` だけは `webui/static/` に同梱している(CDN は使わない)。

- サーバー側で HTML を組み立てる複数ページ構成(SPA ではない)。自動更新は `<meta http-equiv="refresh">`(8 秒)。
- 全ページに共通ナビと、承認待ち件数のバッジ(報告の question/blocked + `pending-human` の代行依頼 + blocked の agent)。
- まとめ送信(10 分おき)より急ぐものは「緊急通知」(`/notifications`)に集め、ナビにも件数バッジを出す。
  Web UI のタブが開いていれば、新しく出たものを Chrome のデスクトップ通知でも出す(下の「緊急通知」)。
- **変更できるのは定期実行のスケジュール**(`/jobs/<job>`: 間隔・時間帯・曜日・一時停止)と、**question / blocked の報告を「対応済みにする」**(`/inbox` とタスク画面。書き込みは `tb resolve` が行い、tasks.db には直接書かない)、
  **agent の停止・復活・新規作成**(`/agents` と `/agent-new`。書き込みと herdr の操作は `tb agent-stop` / `agent-revive` / `agent-new` / `agent-approve` / `agent-reject` が行う。下の「agent の操作」)**だけ**。
  それ以外は読み取り専用で、POST/PUT/DELETE/PATCH は 405。入力欄のある画面は自動更新しない。
  agent の承認ダイアログに答えるボタンはない(承認は人が手元の端末で行う)。

## 起動

```bash
python3 webui/server.py
```

起動時に標準出力へポートと、ログイン(または初回設定)の URL を出す。URL に機密は含まれない。

```
herdr-webui: listening on http://127.0.0.1:8800/
herdr-webui: port=8800
herdr-webui: 初回設定(ユーザー名とパスワードを作る) http://127.0.0.1:8800/setup
```

## ログイン(ID/パスワード)

認証は ID/パスワードのログイン方式。従来の `?token=` と `run/token` は**廃止**した(コードから参照しない)。
残っている `run/token` は不要なので削除してよい。

- **初回設定**: `run/auth.json` が無いときだけ `/setup` が開く。ユーザー名とパスワード(確認入力あり、**12 文字以上**)を
  画面で作る。設定後は自動でログインされ、`/setup` は 404 で閉じる。最初にアクセスした人が設定するので、
  起動したらすぐ自分で設定すること(待ち受けは 127.0.0.1 のみ)。
- **ログイン**: `/login`。未ログインで他の URL を開くと `/login?next=<元のパス>` に 302 する。
  `next` は同一オリジンのパスだけ有効で、外部 URL は無視して `/` に戻す。
- **有効期間**: 12 時間(`WEBUI_SESSION_TTL` 秒で変更可)。サーバーを再起動してもセッションは続く。
- **ログアウト**: 各ページ右上のボタン(`POST /logout`)。サーバー側のセッションも破棄される。
- **総当たり対策**: 失敗が 5 回続くと 5 分ロック(全体でカウント。`WEBUI_LOCK_MAX` / `WEBUI_LOCK_SEC`)。ロック中は正しい
  パスワードでも通らない(429)。カウントはメモリ上なので、サーバーの再起動でリセットされる。
  失敗時は「ユーザー名違い」か「パスワード違い」かを区別して表示せず、ログにも残さない。

### パスワードを忘れたとき(再設定)

ブラウザだけで再設定できる画面は**作らない**(画面に届く人がパスワードを奪えてしまうため)。
シェルを操作できる本人だけが「許可ファイル」を作り、それが 10 分間だけ再設定画面を開く。

1. ログイン画面の「パスワードを忘れた」(`/reset`)を開く。許可がなければ、実行するコマンドが表示される。
2. 手元のシェルで実行する(パスは画面に表示される絶対パス):

   ```bash
   touch webui/run/allow-reset
   ```

3. `/reset` を再読み込みし、新しいパスワード(確認入力あり、12 文字以上)を設定する。

許可は、`run/allow-reset` が**通常ファイル**で、**自分のユーザーの所有**で、**作成(更新)から 10 分以内**のときだけ有効。
シンボリックリンク・期限切れ・未来の時刻のものは無効で、画面に理由が出る。存在確認は毎回行う(キャッシュしない)。
期限が切れたら `touch` し直す。

再設定に成功すると、`run/allow-reset` を削除し、`run/auth.json` のハッシュを更新(0600 のまま)、`run/sessions.json` の
全セッションを無効化(全員が再ログイン)、ロックアウトのカウントもリセットする。ユーザー名は変わらない。
`/reset` の POST にも、ログインと同じ CSRF 検査(Origin/Referer と隠しトークン)と失敗時の遅延(`WEBUI_FAIL_DELAY`、既定 1 秒)を適用する。
成功・拒否(期限切れ・不正・未許可)はログに `reset: ok` / `reset: denied (...)` として残る(パスワードは出さない)。

**最終手段**: ユーザー名も含めて最初からやり直すなら、`run/auth.json` と `run/sessions.json` を削除して `/setup` から作り直す。

```bash
rm webui/run/auth.json webui/run/sessions.json
```

### 保存されるもの(`run/`、すべて 0600 で作成)

- `auth.json`: ユーザー名と、scrypt(salt 16 バイト、パラメータも記録)によるパスワードのハッシュ。平文はどこにも残さない。
- `sessions.json`: セッション値の SHA-256 と有効期限だけ。Cookie の値そのものは保存しない。
- Cookie は `HttpOnly; SameSite=Strict; Path=/`。

POST を受け付けるのは `/login`・`/setup`・`/logout`・`/reset`・`/jobs/<job>`・`/resolve`・`/agent-op`・`/agent-new` だけ(`/jobs/<job>`・`/resolve`・`/agent-op`・`/agent-new` はログイン中のセッションに結びついたトークンが必要)。これらは `Origin`/`Referer` が自分のオリジン
(`127.0.0.1:<port>`)であることと、フォームの隠しトークン(署名付き・30 分で失効)を確認する。それ以外の
POST/PUT/DELETE/PATCH は 405。

### ポート

- 既定は 8800 から +1 で実際に bind を試して空きを探す(上限 8819)。全部埋まっていればエラー終了。
- `TB_UI_PORT=8810` のように固定指定した場合は探索しない(使用中ならエラー終了)。
- 開いたポートは `run/port` に書く(終了時に消す)。

## ページ

| URL | 内容 |
| --- | --- |
| `/` | 概要(承認待ち、agent の状態、滞留、直近の完了) |
| `/notifications` | 緊急通知の一覧と、Chrome 通知を有効にするボタン |
| `/api/notifications` | 緊急通知の JSON(`/notify.js` が 15 秒ごとに取得。未ログインは 401) |
| `/agents` | agent 一覧(状態・workspace・いま何を・最終入力/停止・警告・止めてよいか・停止ボタン)、承認待ちの agent 操作、停止中の agent(復活ボタン)、直近の操作 |
| `/agent-new` | 新しい agent を作るフォーム(名前・作業ディレクトリ・モデル・役割・止めてよいか・最初の指示) |
| `/agents/<名前>` | 状態、担当タスク、直近の出力(`herdr agent read`)、最終入力/停止、`session_id`、手元で開く `herdr agent attach`(`claude --resume` は会話が枝分かれするので出さない) |
| `/tasks` | タスク一覧(読み取りのみ)。状態の件数サマリ(todo / in_progress / review / done / blocked)、`?project=` `?owner=`(担当 agent) `?status=` `?unreported=1`(未報告だけ) `?stale=1`(in_progress のまま 24 時間超更新が無い「停滞」だけ。しきい値は `app/config.py` の `TASK_STALE_HOURS`)で絞り込み、`?sort=`(`project` / `oldest` / `created` / `id`)で並び替え。既定の並びは優先順(未対応の報告があるもの → blocked → in_progress → review → todo → done)で、最大 300 件。超えたときは「N 件のうち 300 件だけ表示」と明示する。親子(parent_id)、session、報告の種別ごとの件数(人が未対応の question / blocked を強調)、成果物の数、停滞の目印を出す。題は先頭 80 文字だけ。期限・優先度は tb に無いので出さない |
| `/tasks/<id>` | タスクの詳細: 親へのリンク・子タスクの一覧・session・成果物のパス・報告(先頭 1000 文字、人が対応済みにしたか)・代行依頼。報告の「対応済みにする」(`/resolve`)は既存の操作で、このページで増えた書き込みは無い |
| `/inbox` | 要対応(question・blocked・pending-human・blocked の agent)を上に、直近の報告を下に |
| `/docs` | agent が成果物として報告したドキュメント(ローカルのファイル)の一覧。新しい順、`?q=` で絞り込み |
| `/docs/view?ref=<参照>` | そのドキュメントを読む。Markdown は整形して表示(Mermaid は図に。「元のテキストを表示」で切り替え)、フォルダなら中のファイル一覧 |
| `/docs/view?path=<絶対パス>` | パスを指定して読む(agent の作業ディレクトリ配下だけ。`/docs` の入力欄から) |
| `/jobs` | 各 job の型・宛先(agent 名 / コマンド)・実際に効く設定(間隔・時間帯・曜日・有効/一時停止)、最終記録と最後の実行 |
| `/jobs/<job>` | 「ジョブが行うこと」(型・宛先 agent と今の状態・送られる本文 / 実行されるコマンドの全文・次回の予定・直近の結果・副作用の注記・承認の扱い。読み取りのみ)と、スケジュールの変更フォーム(自動更新しない)。保存先は `scheduler/settings/<job>.settings`、次の分から反映。「conf の値に戻す」で settings を消す |
| `/ledger` | 送信履歴(tb の `messages` 表)。種別 × 結果の件数サマリと直近 100 件。failed は既定で畳む。`?kind=` `?result=`(sent / busy / failed / all) `?target=` `?from=` `?to=`(日付 `YYYY-MM-DD`、UTC)で絞り込み。send の `O-n` は `/outbox` にリンクし、本文は 1 行目だけ |
| `/perms` | 権限(`taskboard/config/approvals.conf` の読み取り)。上から順に順位・パターン・ask/allow・最後の変更(行末の注記と `approval_changes`)・その行で決まる操作の例・警告(`* = ask`、広い allow、上の行に隠れる行(ワイルドカードを含まない行)、「この操作はどの行で決まるか」の検索(`?op=`、tb の `approval_of` と同じ規則をここで再現。tb は呼ばない)、操作ごとの現在の権限(権限カタログ `taskboard/config/approvals-catalog.conf` × approvals.conf: 現在の値・決まる行・規則の有無(明示 / 既定に落ちる)・リスク階層・副作用・allow 候補・状態、`?kind=` `?state=` `?fallback=1` `?allowed=1` で絞り込み、high / human-only の操作が allow なら警告、reserved(将来用)と unwired(承認判定に渡らない。`proxy:*`)は薄く表示し、unwired の操作に当たる規則には「何も効かない」と警告、カタログに無い規則は「未知の規則」の欄)、変更履歴、`tb allow` / `tb ask` のコマンド行の組み立て表示(`?pat=&val=`。表示だけで実行しない)。変更の操作・POST は無い |

名前の無い agent は `pane_id`(例: `w2:p3`)が名前の代わりになる。未知の URL は 404。

## データ源(すべて読み取りのみ)

- `herdr agent list` / `herdr agent read`(結果は 3 秒キャッシュ)
- `taskboard/data/tasks.db`(`mode=ro` で開く。無い・壊れていても落ちず、空の表になる)
- 成果物として登録されたローカルのファイル(`/docs`。読むだけ)
- `scheduler/logs/*.log`、`scheduler/jobs/*.conf`(`scheduler/bin/show.sh` 経由)、`taskboard/config/approvals.conf`(`/perms`)
- 書き込むのは `scheduler/settings/<job>.settings` だけ。入力は数値(間隔は分か秒)・`HH:MM` か `HH:MM:SS`・決まった曜日だけを受け付け、scheduler が読めない内容なら元に戻す

「滞留」は、`working` のまま最終入力(`agent_activity.last_prompt_at`)から 30 分を超えて停止がない agent。

## ドキュメント(agent のまとめを読む)

agent が「ここにまとめました」と報告したファイルを、VS Code などを開かずにブラウザで読むための画面。

- 出すもの: 報告の成果物(`tb comment --deliverables`)、タスクの成果物(`tasks.deliverables`)、アラートの解析レポート(`tb alert-result --report`)。
  タスク画面の成果物もここへのリンクになる。URL の成果物はそのまま外部リンク。
- 一覧の項目は参照(`C-<報告id>/<n>`・`task:<タスクid>/<n>`・`A-<アラートid>`)で指す。
- **パスを指定して開く**(`/docs` の入力欄、`/docs/view?path=`): 絶対パス(`~` 可)だけ。開けるのは、agent の作業ディレクトリ
  (`agent_activity.cwd`)と `WEBUI_DOC_ROOTS`(`:` 区切り)の配下だけ。ホーム・`/`・`/tmp` などそのものは広すぎるので範囲にしない。
  シンボリックリンクは実体で判定する。開ける範囲は `/docs` に表示する。
- 相対パスは、報告した agent の作業ディレクトリ(`agent_activity.cwd`)を基準にする。分からなければ「場所不明」。
- フォルダが成果物なら中のファイルも読める。`..`・絶対パス・外を指すリンクでフォルダの外へは出られない(隠しファイルは一覧に出さない)。
- 鍵・`.env`・`.ssh` などの認証情報らしいもの、Web UI 自身の `run/` は、登録されていても表示しない。バイナリは表示しない。1 MB を超える分は省略。
- アラートのレポートは、ファイルが消えていても DB に保存された本文(先頭 50 KB)を出す。
- Markdown は依存なしの最小限の整形(見出し・箇条書き・表・コード・引用・リンク)。本文は先にエスケープし、生の HTML は文字として出す。リンクにするのは http(s) だけ。
- **Mermaid**: ```` ```mermaid ```` のブロックと `.mmd` / `.mermaid` ファイルは、同梱の `/static/mermaid.min.js` がブラウザで図にする
  (`securityLevel: "strict"`。図があるページだけ読み込む)。JS が動かなければ元のコードのまま読める。
- 読んでいる途中で再読み込みしないよう、この 2 画面は自動更新しない。

## 緊急通知

10 分おきのまとめ送信を待たずに知らせるもの。

- 未完了タスクの `question` / `blocked` の報告
- `pending-human` の代行依頼
- `blocked` の agent(承認待ち)
- 滞留している agent
- 最後の実行(`ran`/`failed`/`sent`)が `failed` の定期実行(次に成功すると消える)

Chrome 通知の使い方: `/notifications` を開いて「Chrome 通知を有効にする」を押し、許可する。
以後、Web UI のどれかのタブが開いている間、`/notify.js` が 15 秒ごとに `/api/notifications` を見て、
前回なかった項目だけを通知する(既読の管理はブラウザの localStorage。通知をクリックすると該当ページへ)。
タブを閉じると通知は止まる(Service Worker・Push は使わない)。最初に開いたときに既にあるものは通知せず、一覧とバッジで見る。
`127.0.0.1` はブラウザ上で安全なコンテキスト扱いなので、HTTPS なしで通知を使える。

## 承認待ちの対応(手元の herdr で行う)

blocked の agent は `/inbox` からリンクで `/agents/<名前>#approval` に飛べる。そこに次を出す。

- **承認待ちの内容**: `herdr agent read` の直近の画面(承認ダイアログ部分)を、ANSI を除いて HTML エスケープしただけで表示する。要約・加工はしない。
- **手元で開く**: `herdr agent attach <名前>`(名前が無い agent は `pane_id`)をコピー用の入力欄に出す。`readonly` の入力欄を選択してコピーする。
- **場所**: workspace ラベル / tab / pane_id。

**方針: ブラウザからは承認の入力をしない。** 承認ボタンも `send-keys` も作らない。内容を見て、手元の端末で `herdr agent attach` して、人が操作する。

ttyd(ブラウザ上の端末)は採用しない。署名が無効で、会社の EDR に検知された前例があるため。

## 安全性

- `127.0.0.1` のみで待ち受ける。Host ヘッダが自分以外なら 403(DNS リバインディング対策)。
- agent の出力・タスク本文・ログなど外部由来の文字列は、ANSI 等の制御文字を除いたうえで HTML エスケープして出す。
  CSP は `default-src 'none'` を基本に、`script-src 'self'` と `connect-src 'self'` だけを許す。JS は同一オリジンの
  `/notify.js`(通知のポーリングだけ。ログイン必須)と、Mermaid の図があるドキュメントだけで読む `/static/mermaid.min.js`・`/docs.js`
  (どれもログイン必須)で、インラインスクリプトは使えない。通知の文面は
  `textContent` と Notification の本文(プレーンテキスト)にしか入れない。
- ログ(launchd の out/err を含む)には、メソッド・パス・状態コードだけを出す。クエリ、Cookie、フォーム本文、
  パスワード、セッション値、例外のメッセージは出さない。ログイン成否も理由を区別せず `login: ok/failed/locked` のみ。

## 見える内容に機密が含まれうる

agent の端末出力、タスクの報告、送信履歴(送ったプロンプト)には、社内の情報・認証情報の断片・個人名などが含まれうる。
`127.0.0.1` 限定でも、**ログイン情報(ユーザー名・パスワード)を他人に共有しない**(チャット、スクリーンショット、PR にも貼らない)。
画面共有の前に、表示中のページを確認すること。

## 常駐(launchd)

```bash
bash webui/bin/gen-plist.sh      # webui/generated/local.herdr-webui.plist を生成して lint(登録はしない)
```

ラベルは `local.herdr-webui`、`KeepAlive` あり、ログは `webui/logs/`。**登録は人が行う**:

複数の herdr セッションを別の checkout(`git worktree` など)で動かすときは、`TB_INSTANCE=<名前>` を付けて生成すると、
ラベルと plist のファイル名が `local.herdr-<名前>-webui` になり、checkout 同士で衝突しない(未設定なら従来のまま)。
名前は英数字で始まり、英数字と `.` `_` `-` だけ。`HERDR_SESSION` と `TB_UI_PORT` を付けて生成すると、plist の環境変数にも渡す。

```bash
cp webui/generated/local.herdr-webui.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.herdr-webui.plist
# 解除: launchctl bootout gui/$(id -u)/local.herdr-webui && rm ~/Library/LaunchAgents/local.herdr-webui.plist
```

## 設定(環境変数)

| 変数 | 既定 | 意味 |
| --- | --- | --- |
| `TB_INSTANCE` | (なし) | セッション(インスタンス)名。設定すると Cookie 名が `herdr_webui_session_<名>`、run ファイルが `run/instance-<名>/`、全画面のタイトルとヘッダに名前が出る(`default` 以外は色の帯)。未設定は従来どおり |
| `TB_UI_PORT` | (なし) | 固定ポート。指定すると探索せず、使用中なら別ポートへ移らずエラーで終了する |
| `HERDR_WEBUI_RANGE` | `8800-8819` | 探索範囲(`下限-上限`)。テスト用 |
| `WEBUI_SESSION_TTL` / `WEBUI_LOCK_MAX` / `WEBUI_LOCK_SEC` | `43200` / `5` / `300` | ログインの有効秒数 / ロックまでの失敗回数 / ロック秒数 |
| `WEBUI_FAIL_DELAY` | `1` | ログイン・再設定の失敗時の遅延(秒) |
| `WEBUI_DOC_ROOTS` | (なし) | パス指定で開いてよいディレクトリを足す(`:` 区切り)。agent の作業ディレクトリは自動で入る |
| `HERDR_BIN` | `herdr` | herdr の実行ファイル(テストで疑似に差し替え) |
| `WEBUI_TB_DB` | `TB_DB`、無ければ `taskboard/data/tasks.db` | tb と同じ DB(既定は `lib/herdr_env.py`) |
| `WEBUI_SCHED_LOGS` / `WEBUI_SCHED_JOBS` / `WEBUI_SCHED_SETTINGS` / `WEBUI_RUN_DIR` | 兄弟ディレクトリ / `webui/run` | データ源・settings・`run/` の場所 |
| (固定) | `taskboard/config/approvals-catalog.conf` | 権限カタログ。env では差し替えられない |
| `WEBUI_APPROVALS` | `TB_APPROVALS`、無ければ `taskboard/config/approvals.conf` | `/perms` が読む approvals.conf(tb と同じ解決順。launchd 運用では tb が読むのと同じ approvals.conf を指すこと) |

## コードの構成

`server.py` は入口だけで、中身は `app/` にある。依存は上から下への一方向(逆向きの import はしない)。

| ファイル | 中身 |
|---|---|
| `app/config.py` | 定数と、環境変数で上書きできるパス(DB と herdr の既定は `lib/herdr_env.py`) |
| `app/sanitize.py` | 外部由来の文字列の無害化(`e()` を通してから HTML に出す) |
| `app/herdr.py` | herdr CLI の呼び出し(読むだけ)と短いキャッシュ |
| `app/data.py` | tasks.db とログの読み取り(読み取り専用の接続)、緊急通知の集計 |
| `app/docs.py` | 成果物(ドキュメント)の参照の解決と、安全な読み取り |
| `app/markdown.py` | 最小限の Markdown → HTML(エスケープしてから整形) |
| `app/actions.py` | 画面からの変更。tasks.db には書かず `tb` を呼ぶ(今は `tb resolve` だけ) |
| `app/jobs.py` | scheduler のジョブ定義と、Web UI から書く上書き設定 |
| `app/jobview.py` | `/jobs/<job>` の読み取り表示(conf を実行せず PROMPT / COMMAND をそのまま読む、本文のマスク、次回の予定、宛先の状態) |
| `app/views.py` | HTML の組み立てと各ページ |
| `app/handler.py` | HTTP ハンドラ・ログイン・起動 |
| `auth.py` | パスワードとセッション |

## テスト

```bash
bash webui/tests/run.sh
```

herdr は `tests/fake/herdr`(疑似)、DB・ログは一時ディレクトリ。本物のポートは 30000〜60000 の空き範囲から
最大 3 つだけ使い、終了時に必ず片付ける。確認する境界: ポート衝突で +1、全埋まりでエラー、`TB_UI_PORT` 固定は探索しない、
未ログインは /login へ、初回設定は 1 回だけ、パスワード 12 文字未満の拒否、ハッシュに平文を含まない、0600、ロックアウト、
パスワード再設定(許可なし・期限切れ・シンボリックリンクの拒否、成功後の許可削除とセッション失効、時刻の注入)、期限切れ・再起動後・ログアウトのセッション、`next` の外部 URL 無視、Origin 不一致・CSRF トークン無しの POST の拒否、他の POST 等は 405、不正な Host 403、XSS・ANSI のエスケープ、DB 不在・破損・herdr 失敗でも落ちない、未知の URL は 404。

## まだできていないこと

ブラウザからの承認操作、端末リンク(ttyd は採用しない)、`pane_agent_status_changed` の購読(いまは数秒おきの取得)は第 2 段以降。

## agent の操作(停止・復活・新規作成)

`/agents` から、`/mcp` や herdr を手で触らずに agent を止める・復活させる・作る。Web UI からの操作は人の操作として扱い
(`HERDR_PANE_ID` を外して `tb` を呼ぶ)、実体は `tb agent-*` が行う(詳細は `taskboard/README.md` の「agent の停止・復活・新規作成」)。

- **停止**: 判定役(`roles/sweeper.md`)か人が「止めてよい」印を付け、未完了タスクが無い agent には「停止」ボタン。
  それ以外は「強制停止」で、確認のチェックを入れないと送れない。止めても台帳に会話(session)と cwd が残る
- **復活**: 「停止中の agent」から「同じ会話で復活」(`claude --resume <session>`)か「新しい会話で復活」
- **新規作成**: `/agent-new`。専用の workspace で claude を起動する。役割は `roles/` から選べ、起動時に SessionStart hook が渡す
- **承認待ちの操作**: agent(指揮官など)が `tb agent-*` を使い、`approvals.conf` の `agent:<op>:<名前>` が `ask` だったものは
  「承認待ちの操作」に出る(緊急通知にも出る)。「承認して実行」か「却下」
- 結果は画面上部(実行した / 承認待ちに積んだ / 前提を満たさない / 失敗)と、「直近の操作」(`agent_ops`)に出る

## 複数セッションの並行運用

herdr のセッションごとに、別ポート・別プロセスで webui を動かす(同一ポートでの切り替えはしない)。

- セッションごとに `TB_INSTANCE=<名>` と `TB_UI_PORT=<ポート>` を決めて起動する(`bin/gen-plist.sh` は両方を plist に渡す)。
- Cookie は host 単位で、ポートでは分かれない。そのため Cookie 名にインスタンス名を入れ、別セッションのログインと干渉しないようにしている。
- `TB_UI_PORT` が使用中ならエラーで終了する(別ポートへ黙って移ると、別セッションの画面と取り違えるため)。未指定なら従来どおり `HERDR_WEBUI_RANGE` で探索し、選んだポートとインスタンス名を起動ログ(`instance=<名> port=<n>`)と run ファイル(`port`・`instance`)に残す。
- run ファイル(ログイン情報・セッション・port)は `run/instance-<名>/`、launchd のログは `logs/launchd-webui-<名>.{out,err}`。未設定なら従来の `run/`・`logs/launchd-webui.*`。
- bind は `127.0.0.1` のまま(変更なし)。

### 既に `TB_INSTANCE` を付けて動かしている場合の移行

以前は `TB_INSTANCE` はラベルにだけ効き、ログイン情報は `run/auth.json` にあった。更新後は `run/instance-<名>/` が置き場になる。

- **既定では引き継がない**。更新後の最初の起動は `/setup`(初回設定)になる。引き継ぎを既定にすると、これから作る別インスタンス(研修用など)も default の認証で入れてしまい、別の認証情報を設定できないため。
- 既存の運用を続けたい人は、`WEBUI_LEGACY_AUTH=1` を付けて起動する(`bin/gen-plist.sh` に付けて生成すれば plist にも入る)。新しい場所に `auth.json` が無いときだけ、旧い `run/auth.json` を**読み取りで**使う(消さない・書き換えない)。**1 回で済むのではなく、外すまで起動のたびに使われる**。起動ログに「旧い … を読み取りで使います」と出る。
- 引き継ぐのは、通常ファイル(リンクでない)・自分の所有・group/other に書き込み権限が無いときだけ。満たさなければ引き継がず、起動ログで知らせて `/setup` になる。
- 新しい場所に `auth.json` があれば、常にそちらが優先(インスタンスごとのパスワード)。`WEBUI_RUN_DIR` を明示している場合は引き継がない。
- セッション(`sessions.json`)と Cookie 名は新しくなるので、ログインし直しになる。**外す手順**: `run/auth.json` を `run/instance-<名>/auth.json` にコピー(権限 0600)してから、`WEBUI_LEGACY_AUTH` を外して再起動する。新しい場所が常に優先されるので、コピー後は引き継ぎは働かない。

### 固定ポート(`TB_UI_PORT`)の使用中の判定

- `127.0.0.1` で待ち受け中なら、bind の失敗でエラーになる。macOS は `0.0.0.0` など全インターフェースで待ち受け中のポートにも bind が通ってしまうため、固定ポートのときは bind の前に接続を試し、何かが待ち受けていればエラーにする。
- 範囲は 1〜65535。範囲外・数字でない指定はエラー終了する(範囲探索には落ちない)。

### 将来(`tb use` / 台帳)との関係

Cookie 名は現在 `herdr_webui_session_<TB_INSTANCE>`。台帳側の設計に合わせて名前の付け方を変えるときは、ログインし直しが 1 回で済むよう、同時に切り替える。

### `TB_INSTANCE` の大文字小文字

大文字小文字を区別しないボリューム(macOS の既定など)で run を共有しないよう、webui は名前を**小文字に正規化**する(`Alpha` と `alpha` は同じインスタンス: Cookie 名・run・表示とも小文字)。launchd のラベルと plist のファイル名は、`TB_INSTANCE` の書いたまま(`bin/gen-plist.sh`)。

## 定期実行ジョブの表示(`/jobs/<job>`)の読み取り範囲

- 引用符なしの値は行末まで(` #` の前まで)を記述として出す。`$`・`` ` ``・`;`・`(`・引用符の連結など、bash が展開・解釈する記述を含む値は「シェルで展開・解釈される記述です(表示は不完全)」と明示し、`PROMPT_FILE` はファイルを確定できないので中身を読まない。conf が 64 KiB を超えるときも注意を出す。
- `scheduler/jobs/<job>.conf` は**実行しない**(bash として source しない)。`PROMPT` / `PROMPT_FILE` / `COMMAND` / `SKIP_IF` / `COMMAND_TIMEOUT` の記述を、`$変数` なども展開せずそのまま表示する。
- `PROMPT_FILE` の中身は、絶対パスで、`scheduler/`(と jobs の置き場)の配下の通常ファイルで、認証情報の置き場(`/docs` と同じ判定)でないときだけ表示する。相対パス・範囲外は、パスだけ表示して中身は読まない。
- 本文・コマンドのトークンらしい文字列(`ghp_…`・`xox…-`・`AKIA…`・`Bearer …`・`token=…` など)と秘密鍵のブロックは `[マスク]` に置き換える。完全ではない(機密を含みうる画面)。
- 見るのはこの webui のプロセスの `scheduler/jobs`・`scheduler/logs`(別 checkout・別インスタンスのジョブは見えない)。
- scheduler からの送信(`tb system-send`)は `approvals.conf` を見ず、人の承認待ちにならない(宛先が idle / done のときだけ自動で送る)。画面にはその旨と、参考として通常の `tb send` の判定(`send:task:<宛先>`)を出す。
