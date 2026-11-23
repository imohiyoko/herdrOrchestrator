# webui — herdr 進捗の Web UI

agent・タスク・報告・定期実行・送信履歴・権限を、ブラウザで見るための小さなサーバー。
Python 3 の標準ライブラリだけで動く(依存なし)。Mermaid の図を描くための `mermaid.min.js` だけは `webui/static/` に同梱している(CDN は使わない)。

- サーバー側で HTML を組み立てる複数ページ構成(SPA ではない)。自動更新は `/refresh.js`(30 秒。見ていないタブでは止め、見えたときに期限を過ぎていればすぐ読み直す。スクリプトが動かないときだけ `<noscript>` の meta refresh)。DB の接続はリクエストごとに 1 本を使い回す。
- 全ページに共通ナビと、承認待ち件数のバッジ(報告の question/blocked + `pending-human` の代行依頼 + blocked の agent)。
- まとめ送信(10 分おき)より急ぐものは「緊急通知」(`/notifications`)に集め、ナビにも件数バッジを出す。
  Web UI のタブが開いていれば、新しく出たものを Chrome のデスクトップ通知でも出す(下の「緊急通知」)。
- **変更できるのは定期実行のスケジュール**(`/jobs/<job>`: 間隔・時間帯・曜日・一時停止)と、**question / blocked の報告を「対応済みにする」**(`/inbox` とタスク画面。書き込みは `tb resolve` が行い、tasks.db には直接書かない)、
  **agent の停止・復活・新規作成**(`/agents` と `/agent-new`。書き込みと herdr の操作は `tb agent-stop` / `agent-revive` / `agent-new` / `agent-approve` / `agent-reject` が行う。下の「agent の操作」)**だけ**。
  それ以外は読み取り専用で、POST/PUT/DELETE/PATCH は 405。入力欄のある画面は自動更新しない。
  agent の承認ダイアログに答えるボタンはない(承認は人が手元の端末で行う)。

## 起動

Web UI は**1 つのプロセスで全 herdr セッションの画面を返す**。ブラウザで開くのはその 1 つのポートだけ(既定 `http://127.0.0.1:8800/`)で、
各セッションの画面は `/s/<セッション名>/` に並ぶ。セッションはリクエストごとに URL から決まる(`app/reqctx.py`。DB の行の絞り込み・herdr と tb の呼び出しがそのセッションになる)。

```bash
python3 webui/server.py      # 1 つだけ(checkout・セッションをまたいで 1 つ)
# → http://127.0.0.1:8800/ はこのプロセスの herdr セッション(HERDR_SESSION > checkout の herdr-session > default)の画面へ移る
```

```
herdr-webui: listening on http://127.0.0.1:8800/(全 herdr セッション。/s/<セッション名>/)
herdr-webui: 開く http://127.0.0.1:8800/s/default/(ログインは全セッション共通)
```

URL に書けるセッション名は、DB に行のあるセッション(agents / tasks / config)、herdr のセッションのディレクトリ
(`~/.config/herdr/sessions/<名前>/`。`XDG_CONFIG_HOME` で変わる)、`default`、このプロセスのセッションだけ(`app/sessions.py`)。それ以外は 404。

### Go 版(移行中)

`WEBUI_IMPL=go python3 webui/server.py` で、Go 版(`herdrctl webui serve`。場所は `WEBUI_HERDRCTL` > `TB_HERDRCTL` > PATH)に替わる。
今の Go 版にあるのは、ログインまわり(初回設定・ログイン・再設定・ログアウト)と同梱のスクリプト、画面の枠(ナビとバッジ)、緊急通知(`/notifications` と `/api/notifications`)・送信(`/outbox`)・セッション一覧(`/sessions`)・送信履歴(`/ledger`)・報告・承認待ち(`/inbox` と、報告を対応済みにする `POST /resolve`)。ほかの画面は 501 の案内を返す(知らないパスは 404)。
画面の CSS とスクリプト(`static/`)と、ログイン情報のファイル(`auth.json` など)は Python 版と同じものを使うので、入れ替えてもログインは続く。
既定は Python 版のまま。画面を移し終えてから既定を Go にする。

## ログイン(ID/パスワード)

認証は ID/パスワードのログイン方式。従来の `?token=` と `run/token` は**廃止**した(コードから参照しない)。
残っている `run/token` は不要なので削除してよい。

ログインは**全セッションで共通**(1 回ログインすれば、どのセッションの画面にも入れる)。ログイン情報はホームの 1 か所
(`~/.local/state/herdr-webui/`。`XDG_STATE_HOME` / `WEBUI_STATE_DIR` / `WEBUI_AUTH_DIR` で変更可)に置く(旧版の複数プロセスとも共有できる形のまま)。

- **初回設定**: `auth.json` が無いときだけ `/setup` が開く(どのセッションの `/s/<名>/setup` からでも同じ)。ユーザー名とパスワード(確認入力あり、**12 文字以上**)を
  画面で作る。設定後は自動でログインされ、`/setup` は 404 で閉じる。最初にアクセスした人が設定するので、
  起動したらすぐ自分で設定すること(待ち受けは 127.0.0.1 のみ)。
- **ログイン**: `/login`。未ログインで他の URL を開くと `/login?next=<元のパス>` に 302 する。
  `next` は同一オリジンのパスだけ有効で、外部 URL は無視して `/` に戻す。
- **有効期間**: 12 時間(`WEBUI_SESSION_TTL` 秒で変更可)。サーバーを再起動してもセッションは続く。
- **ログアウト**: 各ページ右上のボタン(`POST /logout`)。サーバー側のセッションも破棄される。
- **総当たり対策**: 失敗が 5 回続くと 5 分ロック(全体でカウント。`WEBUI_LOCK_MAX` / `WEBUI_LOCK_SEC`)。ロック中は正しい
  パスワードでも通らない(429)。カウントは `lockout.json` に置く(全セッションで 1 つ。再起動でも消えない)。
  失敗時は「ユーザー名違い」か「パスワード違い」かを区別して表示せず、ログにも残さない。

### パスワードを忘れたとき(再設定)

ブラウザだけで再設定できる画面は**作らない**(画面に届く人がパスワードを奪えてしまうため)。
シェルを操作できる本人だけが「許可ファイル」を作り、それが 10 分間だけ再設定画面を開く。

1. ログイン画面の「パスワードを忘れた」(`/reset`)を開く。許可がなければ、実行するコマンドが表示される。
2. 手元のシェルで実行する(パスは画面に表示される絶対パス):

   ```bash
   touch ~/.local/state/herdr-webui/allow-reset
   ```

3. `/reset` を再読み込みし、新しいパスワード(確認入力あり、12 文字以上)を設定する。

許可は、`allow-reset` が**通常ファイル**で、**自分のユーザーの所有**で、**作成(更新)から 10 分以内**のときだけ有効。
シンボリックリンク・期限切れ・未来の時刻のものは無効で、画面に理由が出る。存在確認は毎回行う(キャッシュしない)。
期限が切れたら `touch` し直す。

再設定に成功すると、`allow-reset` を削除し、`auth.json` のハッシュを更新(0600 のまま)、`sessions.json` の
全セッションを無効化(全員が再ログイン)、ロックアウトのカウントもリセットする。ユーザー名は変わらない。
`/reset` の POST にも、ログインと同じ CSRF 検査(Origin/Referer と隠しトークン)と失敗時の遅延(`WEBUI_FAIL_DELAY`、既定 1 秒)を適用する。
成功・拒否(期限切れ・不正・未許可)はログに `reset: ok` / `reset: denied (...)` として残る(パスワードは出さない)。

**最終手段**: ユーザー名も含めて最初からやり直すなら、`auth.json` と `sessions.json` を削除して `/setup` から作り直す。

```bash
rm ~/.local/state/herdr-webui/auth.json ~/.local/state/herdr-webui/sessions.json
```

### 保存されるもの(`~/.local/state/herdr-webui/`、ディレクトリ 0700・ファイル 0600)

- `auth.json`: ユーザー名と、scrypt(salt 16 バイト、パラメータも記録)によるパスワードのハッシュ。平文はどこにも残さない。
- `sessions.json`: セッション値の SHA-256 と有効期限だけ。Cookie の値そのものは保存しない。
- `lockout.json`: ログイン失敗の回数とロックの期限。
- 複数のプロセスが同じファイルを書くので、書き換えは `auth.lock` の排他ロック(flock)の中で行う。
- `gateway.json`: 実際に待ち受けたポートと pid(二重起動の検出と setup の確認が読む。終了時に消す)。
- Cookie は 1 つ(`herdr_webui_session`)で `HttpOnly; SameSite=Strict; Path=/`。
- checkout の `webui/run/` には、待ち受けたポートの `port` だけを置く。旧い `webui/run/auth.json` はもう読まない(setup の確認で warn)。

POST を受け付けるのは `/login`・`/setup`・`/logout`・`/reset`・`/jobs/<job>`・`/resolve`・`/agent-op`・`/agent-new` だけ(`/jobs/<job>`・`/resolve`・`/agent-op`・`/agent-new` はログイン中のセッションに結びついたトークンが必要)。これらは `Origin`/`Referer` が自分のオリジン
(`127.0.0.1:<ポート>` か `localhost:<ポート>`)であることと、フォームの隠しトークン(署名付き・30 分で失効)を確認する。それ以外の
POST/PUT/DELETE/PATCH は 405。

### ポート

- 既定 8800。ほかのアプリが使っていれば +1 ずつずらす(最大 8819。`TB_UI_GATEWAY_PORT` で起点を変えられる)。実際のポートは起動ログ・`run/port`・`gateway.json` に出る。
  既に動いていれば、2 つ目は起動しない(ずれたポートで重ねて動かない)。

## ページ

| URL | 内容 |
| --- | --- |
| `/` | 概要(承認待ち、agent の状態、滞留、直近の完了) |
| `/notifications` | 緊急通知の一覧と、Chrome 通知を有効にするボタン |
| `/api/notifications` | 緊急通知の JSON(`/notify.js` が 15 秒ごとに取得。未ログインは 401) |
| `/agents` | agent 一覧(状態・workspace・いま何を・最終入力/停止・警告・止めてよいか・停止ボタン)、承認待ちの agent 操作、停止中の agent(復活ボタン)、直近の操作、ロール別の一覧 |
| `/agent-graph` | agent の関係グラフ(誰が誰に何をしたか。ロールごとの枠にまとめた mermaid の図と表。`tb agents --graph` と同じ SQL) |
| `/agent-new` | 新しい agent を作るフォーム(名前・作業ディレクトリ・モデル・役割・止めてよいか・最初の指示) |
| `/agents/<名前>` | 状態、担当タスク、直近の出力(`herdr agent read`)、最終入力/停止、`session_id`、手元で開く `herdr agent attach`(`claude --resume` は会話が枝分かれするので出さない) |
| `/tasks` | タスク一覧(読み取りのみ)。**既定は未完了だけ**(分類 done・cancelled を除く)。上の切り替えで「未完了 / 閉じたもの / すべて」(`?view=open\|closed\|all`)、状態ごとの件数を押すとその状態だけ。絞り込み: `?q=`(ID・題の部分一致) `?project=` `?owner=`(担当 agent。`~` は未割り当て) `?status=`(複数可。選ぶと表示の切り替えより優先) `?type=` `?priority=` `?label=` `?due=overdue\|soon&days=N` `?unreported=1`(未報告だけ) `?stale=1`(作業中のまま 24 時間超更新が無い「停滞」だけ。しきい値は `app/config.py` の `TASK_STALE_HOURS`)。今の条件はチップで並び、× で 1 つずつ外せる。選択欄は変えたらすぐ反映する(`/filter.js`。キーワードと日数は Enter か「絞り込む」)。`?sort=`(`project` / `oldest` / `created` / `id` / `due` / `priority`)で並び替え。既定の並びは優先順(未対応の報告があるもの → blocked → in_progress → review → todo → done・cancelled)で、最大 300 件。超えたときは「N 件のうち 300 件だけ表示」と明示する。親子(parent_id)、報告の種別ごとの件数(人が未対応の question / blocked を強調)、成果物の数、停滞の目印を出す。題は先頭 80 文字だけ。session_id は詳細画面に出す。表は狭い画面では横にスクロールする |
| `/tasks/<id>` | タスクの詳細: 親へのリンク・子タスクの一覧・session・成果物のパス・報告(先頭 1000 文字、人が対応済みにしたか)・代行依頼。報告の「対応済みにする」(`/resolve`)は既存の操作で、このページで増えた書き込みは無い |
| `/inbox` | 要対応(question・blocked・pending-human・blocked の agent)を上に、直近の報告を下に。`tb reminder publish` の結果があれば一番上に「忘れているかも」(優先度順。項目は既存の画面へリンク)、保留した送信(`inbox` 表)があれば「保留した送信」(本文は出さず、「本文を見る」で 1 件ずつ `/inbox?held=<n>`)。どちらも読むだけ |
| `/docs` | agent が成果物として報告したドキュメント(ローカルのファイル)の一覧。新しい順、`?q=` で絞り込み |
| `/docs/view?ref=<参照>` | そのドキュメントを読む。Markdown は整形して表示(Mermaid は図に。「元のテキストを表示」で切り替え)、フォルダなら中のファイル一覧 |
| `/docs/view?path=<絶対パス>` | パスを指定して読む(agent の作業ディレクトリ配下だけ。`/docs` の入力欄から) |
| `/jobs` | 各 job の型・宛先(agent 名 / コマンド)・実際に効く設定(間隔・時間帯・曜日・有効/一時停止)、最終記録と最後の実行 |
| `/jobs/<job>` | 「ジョブが行うこと」(型・宛先 agent と今の状態・送られる本文 / 実行されるコマンドの全文・次回の予定・直近の結果・副作用の注記・承認の扱い。読み取りのみ)と、スケジュールの変更フォーム(自動更新しない)。保存先は DB の `sched-settings:<job>`(このセッションの行。`tb job set --settings` を通す)、次の分から反映。残っている `scheduler/settings/<job>.settings` は消す。保存後に scheduler の読む値が変わったかを確かめる。「conf の値に戻す」で DB の行を消す |
| `/ledger` | 送信履歴(tb の `messages` 表)。種別 × 結果の件数サマリと直近 100 件。failed は既定で畳む。`?kind=` `?result=`(sent / busy / failed / all) `?target=` `?from=` `?to=`(日付 `YYYY-MM-DD`、UTC)で絞り込み。send の `O-n` は `/outbox` にリンクし、本文は 1 行目だけ |
| `/sessions` | セッション一覧(URL に書ける全セッションへのリンクと、それぞれの緊急通知・報告待ちの件数) |
| `/perms` | 権限(設定 `policy` の読み取り。tb と同じく DB のこのセッションの行 → 共通の行 → `taskboard/config/policy.conf`。`.example` は既定値にしない)。policy の行(主体・操作・対象・効果・最後の変更)、警告(広い allow、ほかの行に締められて効かない allow など)、「この操作はどう決まるか」の検索(`?op=`、tb の判定と同じく当たる行のいちばん厳しい効果、無ければ既定の表 `perms/ops.tbl`。tb は呼ばない)、操作ごとの今の権限(権限カタログ `approvals-catalog.conf` と突き合わせ)、変えるためのコマンドと policy の行の組み立て(表示だけ。実行は人が端末で) |

名前の無い agent は `pane_id`(例: `w2:p3`)が名前の代わりになる。未知の URL は 404。

## データ源(すべて読み取りのみ)

- `herdr agent list` / `herdr agent read`(結果は 3 秒キャッシュ)
- tb と同じ DB(既定 `~/.local/state/herdr/tasks.db`。`mode=ro` で開く。無い・壊れていても落ちず、空の表になる)。DB は全セッション共有で、各セッションの Web UI は自分のセッションの行だけを見る(同じ名前の一時ビューで絞る)
- 成果物として登録されたローカルのファイル(`/docs`。読むだけ)
- `scheduler/logs/*.log`、ジョブの定義(DB の `sched-job:<job>`。`scheduler/bin/show.sh` 経由。DB に取り込んでいない `scheduler/jobs/*.conf` は一覧の上で知らせる)、設定 `approvals` / `projects`(DB の `config` 表か `taskboard/config/` のファイル。`/perms` と宛先の表示)
- スケジュールの上書き設定は `tb job set <job> --settings` で DB に書く(ファイルには書かず、残っている `scheduler/settings/<job>.settings` は消す)。入力は数値(間隔は分か秒)・`HH:MM` か `HH:MM:SS`・決まった曜日だけを受け付け、scheduler が読めない内容なら tb が書かない

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
bash webui/bin/gen-plist.sh   # webui/generated/local.herdr-webui.plist(TB_INSTANCE によらず 1 つ。TB_UI_GATEWAY_PORT があれば渡す)
```

`KeepAlive` あり、ログは `webui/logs/launchd-webui.{out,err}`。**登録は人が行う**:

```bash
cp webui/generated/local.herdr-webui.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.herdr-webui.plist
# 解除: launchctl bootout gui/$(id -u)/local.herdr-webui && rm ~/Library/LaunchAgents/local.herdr-webui.plist
```

旧版の入口(`local.herdr-webui-gateway`)とセッションごとの plist(`local.herdr-<名前>-webui`)は、残すと二重起動で落ち続けるので外す
(`setup/check.sh` の `webui:old-plist` が知らせる): `launchctl bootout gui/$(id -u)/<ラベル> && rm ~/Library/LaunchAgents/<ラベル>.plist`。

## 設定(環境変数)

| 変数 | 既定 | 意味 |
| --- | --- | --- |
| `TB_UI_GATEWAY_PORT` | `8800` | 待ち受けるポートの起点(使用中なら +1 ずつ、最大 +19) |
| `WEBUI_STATE_DIR` | `$XDG_STATE_HOME/herdr-webui`(無ければ `~/.local/state/…`) | ログイン情報・待ち受けたポートの記録の置き場 |
| `WEBUI_AUTH_DIR` | `WEBUI_STATE_DIR` | ログイン情報だけ別の場所に置く(テスト用) |
| `WEBUI_SESSION_TTL` / `WEBUI_LOCK_MAX` / `WEBUI_LOCK_SEC` | `43200` / `5` / `300` | ログインの有効秒数 / ロックまでの失敗回数 / ロック秒数 |
| `WEBUI_FAIL_DELAY` | `1` | ログイン・再設定の失敗時の遅延(秒) |
| `WEBUI_DOC_ROOTS` | (なし) | パス指定で開いてよいディレクトリを足す(`:` 区切り)。agent の作業ディレクトリは自動で入る |
| `HERDR_BIN` | `herdr` | herdr の実行ファイル(テストで疑似に差し替え) |
| `WEBUI_TB_DB` | `TB_DB`、無ければ `~/.local/state/herdr/tasks.db` | tb と同じ DB(既定は `lib/herdr_env.py`) |
| `WEBUI_SCHED_LOGS` / `WEBUI_SCHED_JOBS` / `WEBUI_SCHED_SETTINGS` / `WEBUI_RUN_DIR` | 兄弟ディレクトリ / `webui/run` | データ源・settings・`run/` の場所 |
| (固定) | `taskboard/config/approvals-catalog.conf` | 権限カタログ。env では差し替えられない |
| `WEBUI_POLICY` | `TB_POLICY`、無ければ設定 `policy`(DB → ファイル) | `/perms` が読む policy のファイルを明示する(テスト用。普段は指定せず、tb と同じく DB から引く) |

## コードの構成

`server.py` は起動するだけで、中身は `app/` にある。依存は上から下への一方向(逆向きの import はしない)。

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
| `app/scripts.py` | 画面に配るスクリプト(緊急通知・mermaid・件数の取得)。CSP でインラインのスクリプトは使えないので、すべて別ファイルとして配る |
| `app/httpbase.py` | HTTP 応答の共通部分(安全のためのヘッダ、中身を残さないアクセスログ、Host の検査) |
| `app/registry.py` | 待ち受けたポートの記録(`gateway.json`。二重起動の検出) |
| `app/reqctx.py` | リクエストごとの herdr セッション(URL の `/s/<名前>/`) |
| `app/sessions.py` | URL に書けるセッションの一覧(DB の行・herdr のディレクトリ・default・プロセスのセッション) |
| `app/handler.py` | HTTP ハンドラ・ログイン・起動 |
| `auth.py` | パスワードとセッション |
| `static/page.css` ・ `static/*.js` | 画面の CSS とスクリプト(Go 版も同じファイルを読む) |
| `../internal/webui/` | Go 版(`herdrctl webui serve`。移したものから増やす) |

## テスト

```bash
bash webui/tests/run.sh
go test ./internal/webui/    # Go 版(Python 版とログイン情報のファイルを入れ替えられること、移した画面が Python 版と同じ HTML を返すことも確かめる)
```

herdr は `tests/fake/herdr`(疑似)、DB・ログ・ログイン情報の置き場は一時ディレクトリ。ポートは 30000〜60000 の空き範囲を使い、
終了時に必ず片付ける。確認する境界: ポートの使用中は +1・二重起動しない、`/s/<名>/` の付け外しとセッションの切り替え(行・herdr の呼び出し・知らない名前は 404)、ログインの共通化(Cookie 1 つ・失敗回数の共有)、
未ログインは /login へ、初回設定は 1 回だけ、パスワード 12 文字未満の拒否、ハッシュに平文を含まない、0600、ロックアウト、
パスワード再設定(許可なし・期限切れ・シンボリックリンクの拒否、成功後の許可削除とセッション失効、時刻の注入)、期限切れ・再起動後・ログアウトのセッション、`next` の外部 URL 無視、Origin 不一致・CSRF トークン無しの POST の拒否、他の POST 等は 405、不正な Host 403、XSS・ANSI のエスケープ、DB 不在・破損・herdr 失敗でも落ちない、未知の URL は 404。

## まだできていないこと

ブラウザからの承認操作、端末リンク(ttyd は採用しない)、`pane_agent_status_changed` の購読(いまは数秒おきの取得)は第 2 段以降。

## agent の操作(停止・復活・新規作成)

`/agents` から、`/mcp` や herdr を手で触らずに agent を止める・復活させる・作る。Web UI からの操作は人の操作として扱い
(`HERDR_PANE_ID` を外して `tb` を呼ぶ)、実体は `tb agent-*` が行う(詳細は `taskboard/README.md` の「agent の停止・復活・新規作成」)。

- **停止**: 判定役(`grants` の `agent:flag`。利用例は `setup/examples/roles/sweeper.md`)か人が「止めてよい」印を付け、未完了タスクが無い agent には「停止」ボタン。
  それ以外は「強制停止」で、確認のチェックを入れないと送れない。止めても台帳に会話(session)と cwd が残る
- **復活**: 「停止中の agent」から「同じ会話で復活」(`claude --resume <session>`)か「新しい会話で復活」
- **新規作成**: `/agent-new`。専用の workspace で claude を起動する。役割は `roles/` から選べ、起動時に SessionStart hook が渡す
- **承認待ちの操作**: agent(指揮官など)が `tb agent-*` を使い、policy の `agent:<op>`(対象は agent 名)が ask-any だったものは
  「承認待ちの操作」に出る(緊急通知にも出る)。「承認して実行」か「却下」
- 結果は画面上部(実行した / 承認待ちに積んだ / 前提を満たさない / 失敗)と、「直近の操作」(`agent_ops`)に出る

## 複数セッションの並行運用

1 つのプロセスが全 herdr セッションの画面を `/s/<セッション名>/` で返す(セッションごとにプロセスを立てない)。

- DB は全セッション共有で、画面はそのセッションの行だけを読む(同じ名前の一時ビュー)。herdr と tb はそのセッションとして呼ぶ(`HERDR_SESSION`)。
  設定(`policy` / `projects`)と役割も、そのセッションの DB の行 → 共通の行 → ファイルの順で引く。
- ヘッダとタイトルにセッション名が出る(`default` 以外は色の帯)。ほかのセッションへは「セッション」(`/sessions`)のリンクで移る。
- ログインは全セッションで共通(Cookie 1 つ、`Path=/`)。セッション名は herdr と同じく大文字小文字を区別する。
- 全セッションが同じオリジン(`127.0.0.1:8800`)に並ぶ。通知の保存キーとタグはセッションごとに分ける。画面に出すのは自前のスクリプトだけで、外部由来の文字列はすべてエスケープしている。
- 定期実行(`/jobs`)は checkout の `scheduler/` を見る(セッションによらず同じ)。

## 定期実行ジョブの表示(`/jobs/<job>`)の読み取り範囲

- 引用符なしの値は行末まで(` #` の前まで)を記述として出す。`$`・`` ` ``・`;`・`(`・引用符の連結など、bash が展開・解釈する記述を含む値は「シェルで展開・解釈される記述です(表示は不完全)」と明示し、`PROMPT_FILE` はファイルを確定できないので中身を読まない。conf が 64 KiB を超えるときも注意を出す。
- 読み取り処理(`app/jobview.py`)は `scheduler/jobs/<job>.conf` を**実行しない**(bash として source しない)。スケジュール等の情報を得る経路(`job_info` → `show.sh` → `herdrctl sched show`)も conf を実行せず、`KEY=値` だけを読む(展開を含む conf は読めず、詳細ページはその理由を出す)。webui から conf を書き換える機能はスケジュールの保存だけ。`PROMPT` / `PROMPT_FILE` / `COMMAND` / `SKIP_IF` / `COMMAND_TIMEOUT` の記述を、`$変数` なども展開せずそのまま表示する。
- `PROMPT_FILE` の中身は、絶対パスで、`scheduler/`(と jobs の置き場)の配下の通常ファイルで、認証情報の置き場(`/docs` と同じ判定)でないときだけ表示する。相対パス・範囲外は、パスだけ表示して中身は読まない。
- 本文・コマンドのトークンらしい文字列(`ghp_…`・`xox…-`・`AKIA…`・`Bearer …`・`token=…` など)と秘密鍵のブロックは `[マスク]` に置き換える。完全ではない(機密を含みうる画面)。
- 見るのはこの webui のプロセスの `scheduler/jobs`・`scheduler/logs`(別 checkout のジョブは見えない)。
- scheduler からの送信(`tb system-send`)は policy を見ず、人の承認待ちにならない(宛先が idle / done のときだけ自動で送る)。画面にはその旨と、参考として通常の `tb message send` の判定(`send:task:<宛先>`)を出す。
