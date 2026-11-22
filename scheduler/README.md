# scheduler

定期実行を agent の cron ではなく macOS の launchd で担保する。agent は呼ばれた回に実行するだけ。
launchd が `bin/tick.sh <job>` を毎分起動し、時間帯・曜日・間隔・agent の状態を**秒単位で**判定して、
`taskboard/bin/tb system-send` で本文を送る(送ったものは taskboard の `messages` に `kind=scheduler` で残る。`TB_BIN` で tb の場所を変えられる)。

## ジョブの足し方

ジョブの定義(bash の変数代入)を DB に置く: `taskboard/bin/tb sched-job set <name> < 本文のファイル`(下の「ジョブ定義は DB に置く」)。
本文の例は `jobs/watch.conf.example`(パスを直してから `set` に渡す)。**定期実行は DB の定義だけで動く。`jobs/<name>.conf` を置いただけでは動かない**(`tb sched-job import` で取り込む)。

| 変数 | 意味 |
|---|---|
| `TARGET` | agent 名(`herdr agent list` の `name`)。実行のたびに pane ID へ解決する |
| `PROMPT` / `PROMPT_FILE` | 送る本文 / 本文を入れたファイル。**先頭を `/` や `-` にしない**(拒否される) |
| `WINDOW` | `09:00-21:00`。両端を含む。日またぎは不可。秒まで書ける(`09:00:30-21:00:00`) |
| `START` / `END` | `WINDOW` の別表記(`START=10:00` `END=12:00`)。`WINDOW` と同時には使わない |
| `INTERVAL_MIN` | 起動間隔(分)。窓の始端から刻む |
| `INTERVAL_SEC` | 起動間隔(秒)。`INTERVAL_MIN` の代わりに書く(同じファイルに両方は書けない)。例: `INTERVAL_SEC=30` |
| `DAYS` | 既定 `daily`。`weekdays` や `"Mon Wed Fri"` |
| `SKIP_IF` | 既定 `"working blocked"`。該当する状態ならスキップ。idle/done 以外も送らない |
| `TZ` | 既定 `Asia/Tokyo`。判定は tick.sh がこの TZ で行う |
| `ENABLED` | 既定 `1`。`0` で一時停止 |
| `GATE` | 任意。発火条件のコマンド(シェル 1 行)。終了コード 0 のときだけ発火する。0 以外・時間切れ・起動失敗のときは発火せず、`skipped_gate` と理由をログに残す。下の「発火条件(GATE)」 |
| `GATE_TIMEOUT` | 既定 `10`(秒)。`GATE` がこれを超えたら子プロセスごと止め、発火しない |
| `PROMPT_RULES` | 任意。回の時刻で本文を切り替える(例: 00 分は A、30 分は B)。下の「回ごとに本文を変える」 |
| `PROMPT_CMD` / `PROMPT_CMD_TIMEOUT` | 任意。本文を出すコマンド(既定 10 秒)。出力が空でなければそれを送る |
| `PROMPT_VARS` / `PROMPT_VAR_TIMEOUT` | 任意。本文の `{{名前}}` に、送る直前に実行したコマンドの出力(検索結果など)を差し込む(既定 30 秒) |
| `OWNER` | 任意。コマンド型のジョブの持ち主(agent 名)。`COMMAND` に `SCHED_OWNER` で渡す。`tb recipe-run` は定期ジョブ(`SCHED_JOB` がある)から呼ばれると人として扱わず、この agent の権限で判定する。**持ち主の無いジョブからは `tb recipe-run` は実行しない**(ADR 0005 の守り 8)。conf にだけ書ける(settings・Web UI からは変えられない)。プロンプト型には書けない |
| `WATCHER` | 任意。`1` で見張り系のジョブ(tb digest が ran / sent の途絶を見る)、`0` で対象外。未設定なら、TARGET が `tb limits` の watchers に入っているか、`COMMAND=` の行がその名前を含むかで推定する。PROMPT のパスやコメントの語は見ない |

### コマンド実行型ジョブ

`TARGET` / `PROMPT` の代わりに `COMMAND='...'`(シェル 1 行)を書くと、agent を介さずそのコマンドを実行する
(例: `jobs/taskboard-digest.conf.example` — worker の報告を 10 分おきにまとめて指揮官へ送る)。`WINDOW` / `DAYS` / `INTERVAL_MIN` の判定は同じ。終日は `WINDOW=00:00-23:59`。

- `TARGET` / `PROMPT` / `PROMPT_FILE` との同時指定はエラー。agent の状態は見ない。
- `COMMAND_TIMEOUT`(秒、既定 60)を超えると子プロセスごと kill し、`failed`(exit 124)。
- 同じジョブの多重実行はロック(`logs/<job>.lock.d`)で防ぎ、実行中なら `skipped_locked`。
- ログには `action`(`ran` | `failed` | `skipped_*`)、`exit`、`stdout`、`stderr`(各先頭 500 字)を記録。
- **`COMMAND` は `bash -c` でそのまま実行される。ジョブの定義は人が管理するもので、外部入力(Slack・ファイル内容・引数など)を埋め込まないこと。**

### 発火条件(GATE)

データの有無などで発火を止めたいときは、`ENABLED` を手で書き換える代わりに `GATE='...'` を書く(例: `GATE='test -s /path/to/queue'`)。

- 判定の順序: 窓・間隔・曜日 → `ENABLED` → `GATE` → 本文の選択(`PROMPT_CMD` / `PROMPT_RULES`)→ `SKIP_IF` と状態(プロンプト型)→ 差し込み(`PROMPT_VARS`)→ 多重実行のロック(コマンド型)→ 発火。`GATE` の後の判定は、`GATE` が 0 のときだけ行う。
- 0 以外は `skipped_gate`(理由 `gate exit <n>`)、`GATE_TIMEOUT` 超えは `gate timeout after <n>s`、起動できない(終了コード 126 / 127)は `gate could not start (exit <n>)`。
- `GATE` の stdout / stderr は捨てる。ログにも送る本文にも入れない。
- `DRY_RUN=1` でも `GATE` は実行する(判定結果を正しく出すため)。副作用のないコマンドにする。
- `GATE` が無いジョブの動きは変わらない。`GATE` は conf にだけ書ける(settings・Web UI からは変えられない)。`COMMAND` と同じく `bash -c` で実行するので、外部入力を埋め込まないこと。
- `bin/show.sh` の出力の `gate` は、`GATE` があるかどうか(true / false)だけを出す。

### 回ごとに本文を変える(PROMPT_RULES / PROMPT_CMD / PROMPT_VARS)

プロンプト型のジョブは、回ごとに本文を変えられる。例は `jobs/prompt-rules.conf.example`。

**本文の選び方**(上から順に、最初に決まったものを送る)

1. `PROMPT_CMD='...'`(シェル 1 行): 終了コード 0 で出力が空でなければ、その出力が本文。0 で空なら次へ。0 以外・時間切れ(`PROMPT_CMD_TIMEOUT`、既定 10 秒)・起動失敗は `failed`
2. `PROMPT_RULES`: 1 行 1 ルールの `<条件> | <本文>`。上から見て、**この回の時刻**に最初に合った行の本文
3. `PROMPT_FILE` / `PROMPT`(既定の本文)
4. どれも無ければ、その回は送らない(`skipped_prompt`)。「00 分と 30 分だけ送る」は既定を書かなければよい

```bash
PROMPT_RULES='
# 00 分は全体のレビュー、30 分は差分だけ。平日 9-12 時の 15 分おきは別のファイルの本文
min=00 | 役割(tb role-show)の手順で全体をレビューしてください。
min=30 | 前回からの差分だけ見てください。
days=weekdays hour=9-12 min=*/15 | file:/path/to/morning.md
'
PROMPT='定期の見回りをお願いします。'   # どの行にも合わない回(無ければ送らない)
```

- 条件は空白区切りの `key=値` で、すべて満たすと合う。key は `min` / `hour` / `sec` / `days`。`*` だけの条件は常に合う
- 値は 数(`00`)/ 範囲(`9-12`)/ 刻み(`*/15`)/ それらのカンマ区切り(`00,30`)。`days` は `DAYS` と同じ語をカンマで(`days=mon,wed` / `days=weekdays`)
- 本文は 1 行。複数行の本文は `file:<パス>` で書く(そのファイルの中身を送る。`PROMPT_FILE` と同じく絶対パスで)。差し込んだ値は複数行でもよい
- 時刻は「この回の予定時刻」(ジョブの `TZ`)。launchd の起動が数秒遅れても判定は変わらない
- 定義の誤り(範囲外の数・知らない key・`|` が無い行など)は読み込みでエラー(`show.sh` / `tb sched-job set` で弾かれる)

**差し込み(`{{名前}}`)**: 選んだ本文の中の `{{名前}}` を、送る直前に置き換える。検索結果や外部のデータを本文に入れるのに使う。

```bash
PROMPT_VARS='
issues | gh issue list --repo owner/repo --label bug --limit 10
news   | curl -fsS https://example.com/api/latest | jq -r ".items[].title"
'
PROMPT='新しい bug の issue です。優先度をつけてください:
{{issues}}
({{date}} {{time}} 時点)'
```

- 1 行 1 つ `<名前> | <コマンド(シェル 1 行)>`。名前は英小文字で始まる英小文字・数字・`_`
- コマンドの終了コード: `0` → stdout を差し込む(空も可。16 KiB まで)/ `1` → **その回は送らない**(`skipped_prompt`。`grep` の「該当なし」と同じ意味)/
  それ以外・時間切れ(`PROMPT_VAR_TIMEOUT`、既定 30 秒)・起動失敗 → `failed`。stderr は捨てる
- コマンドは本文に出てくる名前のものだけ、送ると決まってから(窓・間隔・`GATE`・`SKIP_IF`・状態の判定のあと)実行する。同じ名前は 1 回だけ
- 組み込みの名前: `{{job}}` `{{target}}` `{{at}}`(`YYYY-MM-DD HH:MM:SS`)`{{date}}` `{{time}}` `{{hour}}` `{{min}}` `{{sec}}` `{{dow}}`(1=Mon..7=Sun)
- 知らない名前の `{{...}}` はそのまま残す。差し込んだ値の中の `{{...}}` は展開しない(1 回だけ)
- 差し込んだ後の本文も、先頭が `/` か `-` なら `failed`

**この回の時刻を渡す環境変数**: `GATE` / `COMMAND` / `PROMPT_CMD` / `PROMPT_VARS` のコマンドには、
`SCHED_JOB` `SCHED_AT` `SCHED_HOUR` `SCHED_MIN` `SCHED_SEC` `SCHED_DOW` が渡る。`COMMAND` 型のジョブもこれで分岐できる。

**注意**

- `PROMPT_CMD` / `PROMPT_VARS` は `COMMAND` と同じく `bash -c` で実行する。conf(または `tb sched-job`)にだけ書け、settings・Web UI からは変えられない。外部入力をコマンドに埋め込まないこと
- 差し込んだ出力(検索結果など)はそのまま agent に送られ、taskboard の `messages` にも残る。秘密を出すコマンドを使わないこと。外部の文章は agent への指示として読まれうる
- `DRY_RUN=1` でも `PROMPT_CMD` / `PROMPT_VARS` は実行する(本文を確かめるため)。副作用のないコマンドにする。
  `DRY_RUN=1` の出力には、送るはずの本文(`text`)と出どころ(`prompt`: `cmd` / `rule:<i>` / `default`)が出る。通常の実行のログには `prompt` だけを残し、本文は残さない
- `COMMAND` 型のジョブには書けない(エラー)。`bin/show.sh` は `prompt_rules`(ルールの数)/ `prompt_cmd`(有無)/ `prompt_vars`(数)を出す

## ジョブ定義は DB に置く(`tb sched-job`)

ジョブ定義(conf の本文)と上書き設定(settings の本文)は、taskboard の DB の `config` 表に置く。
キーは `sched-job:<job>` と `sched-settings:<job>`、本文の書式はファイルと同じ。変更の履歴は `config_changes` に残る(`tb config` と同じ仕組み)。

- 読む順: DB の**このセッション**の行 > DB の共通の行(`*`)。DB は読み取り専用で開く。
  **ファイル(`jobs/<job>.conf`・`settings/<job>.settings`)は読まない**。DB に定義が無いジョブは動かさず、`tick.sh` が `failed`
  (理由: DB に定義が無い。conf があれば `tb sched-job import <job>` で取り込む)を記録する(同じ理由が続く間は 1 行だけ)。
  Web UI の一覧と `setup/check.sh` も、取り込んでいない conf を知らせる
- `SCHED_FILE_FALLBACK=1` のときだけ、DB に無いジョブ・設定をファイルから読む(`tb sched-job` が書く前の検査とテスト用。普段は付けない)
- セッションごとに別の定義を持てる(`HERDR_SESSION`、checkout の `herdr-session` ファイル。tb と同じ決まり方)
- 書くのは人だけ: `tb sched-job set <job> < 本文` / `edit` / `unset`(`--settings` で上書き設定、`--shared` で共通の行)。
  書く前に、scheduler 自身(`bin/show.sh`)がその本文を読めるかを一時のディレクトリで確かめる(読めなければ書かない)
- 取り込み: `tb sched-job import --dry-run` で確かめてから `tb sched-job import [<job> ...]`。`jobs/*.conf` と `settings/*.settings` を
  このセッションの行(`--shared` なら共通の行)に取り込み、取り込んだファイルは `$HERDR_STATE_DIR/sched-imported/<日時>/` へ退かす。
  既に行があるジョブは飛ばす。取り込みの前後で、判定の結果(`tick.sh` の dry-run)と実際に効く設定(`show.sh`)は変わらない(`tests/run.sh` が確かめる)
- 一覧: `bin/jobs.sh`(DB とファイルの和)、`tb sched-job list`(出どころつき)。`bin/show.sh` は `source` / `settings_source`(`db` / `file` / `none`)も出す
- Web UI: 上書き設定は定義の置き場所(DB / ファイル)によらず、`tb sched-job set <job> --settings` を通して DB のこのセッションの行に書く(ファイルは書かない)。
  残っている `settings/<job>.settings` は保存・戻すときに消す(置き場所を DB の 1 つにする)。
  「conf の値に戻す」はこのセッションの行を消す。共通の行は人が `tb sched-job unset <job> --settings --shared` で消す。
  保存・戻したあと、`show.sh` の値が書いた値になったかを確かめ、違えばエラーにする
- plist(`gen-plist.sh`)はジョブ名だけを持つので、取り込んだあとも作り直さなくてよい
- DB の本文も `COMMAND` と同じく bash として評価される。`tb sched-job` は人の端末からだけ書ける(agent の pane からは断る)

## Web UI からの変更(settings)

間隔・時間帯・曜日・一時停止は、Web UI の `/jobs/<job>` から変えられる。保存先は DB の `sched-settings:<job>`(上の「ジョブ定義は DB に置く」)。
`settings/<job>.settings` は DB に移す前の置き場所で、scheduler は読まない(残っていれば `tb sched-job import` で定義と一緒に取り込む)。

- launchd は**毎分** `tick.sh` を起動し、実行するかどうか(曜日・時間帯・`INTERVAL_MIN` の刻み・一時停止)は `tick.sh` が実行時に判定する。
  なので設定を変えても plist の作り直し・再登録は要らない(次の分から反映)。対象外の回はログを書かない
- 刻みは窓の始端から数える(`START=09:00` `INTERVAL_MIN=15` なら 09:00, 09:15, ...。`START=09:00:10` `INTERVAL_SEC=20` なら 09:00:10, 09:00:30, 09:00:50, ...)
- 時刻は秒単位で判定する。秒を省いた時刻は、開始は `:00`、**終了はその分の `:59` まで**を含む(`WINDOW=09:00-21:00` は 21:00:59 まで)。
  分単位だった頃と同じ意味になるので、既存の conf / settings はそのまま使える
- 1 分の中に刻みの回が複数ある(`INTERVAL_SEC` が 60 未満など)ときは、毎分起動された `tick.sh` がその時刻まで待って順に実行する。
  launchd の起動が遅れて過ぎてしまった回は、最後の 1 回にまとめて実行する(まとめて何度も送らない)。
  前の回が長引いて次の分にかかったら、残りの回は飛ばす。また launchd は実行中のジョブを重ねて起動しないので、
  分の最後の回(例: `:55`)の実行が次の分の頭にかかると、次の 1 分はまるごと飛ぶことがある
- 優先順: 定義(`sched-job:<job>`)< 上書き設定(`sched-settings:<job>`)< 環境変数 `SCHED_*`
- settings は bash として読まない。`INTERVAL_MIN` か `INTERVAL_SEC` / `START` / `END`(`HH:MM` か `HH:MM:SS`)/ `DAYS` / `ENABLED` のキーだけを、決まった形で受け付ける
  (それ以外の行やシンボリックリンクはエラー)。宛先・本文・コマンドは Web UI から変えられない
- `ENABLED=0` は一時停止(`skipped_disabled` を記録)
- 実際に効く設定は `bin/show.sh <job>` で JSON 1 行に出る

この版より前に登録した plist は時刻を焼き込んでいるので、一度 `gen-plist.sh` → `install.sh install` をやり直す。

## 登録・確認・解除

```bash
bin/gen-plist.sh <job>          # generated/ に plist を生成(登録はしない)
bin/install.sh install <job>    # ~/Library/LaunchAgents/ にコピーして bootstrap
bin/install.sh status <job>     # launchctl print
bin/install.sh uninstall <job>  # bootout して plist を削除
```

### まとめて 1 本で登録する(推奨)

ジョブごとに plist を登録すると、ジョブの数だけ毎分 bash が起動し、そのたびに Mac をスリープから起こす。
`--dispatch` を使うと 1 本の plist(`local.herdr-sched-dispatch`)が毎分 1 回だけ `bin/dispatch.sh` を起動し、
その中で各ジョブの `tick.sh` をサブシェルで並べて動かす(判定・ログ・ロックはジョブごとに今までと同じ)。

```bash
bin/gen-plist.sh --dispatch taskboard-digest taskboard-flush notify-flush   # 動かすジョブを並べる
bin/install.sh install --dispatch
bin/install.sh status --dispatch
bin/install.sh uninstall --dispatch
```

- ジョブを足す・外すときは `gen-plist.sh --dispatch …` → `install.sh install --dispatch` をやり直す(間隔や時間帯の変更では不要)。
- 個別の plist から移るときは、dispatch に入れたジョブの個別の plist を `install.sh uninstall <job>` で外す。
  両方に登録すると同じジョブが二重に動く(`setup/check.sh` が warn を出す)。
- ジョブ名 `dispatch` は予約済み。

conf を書き換えずに一時的に変えたいときは(Web UI の settings より優先)、環境変数 `SCHED_WINDOW` / `SCHED_START` / `SCHED_END` /
`SCHED_INTERVAL_MIN` / `SCHED_INTERVAL_SEC` で上書きできる。

```bash
SCHED_WINDOW=10:00-18:00 SCHED_INTERVAL_MIN=10 bash bin/tick.sh <job>
```

plist は毎分起動するだけなので、conf や settings を変えても再登録は要らない。

## 動作確認

```bash
bash tests/run.sh                                            # 判定の決定論的テスト
DRY_RUN=1 NOW="2026-11-05 21:00" bash bin/tick.sh <job>      # herdr を呼ばず判定だけ
DRY_RUN=1 FAKE_STATE=working NOW="2026-11-05 12:00" bash bin/tick.sh <job>
DRY_RUN=1 NOW="2026-11-05 12:00:30" bash bin/tick.sh <job>   # 秒まで指定
SCHED_FILE_FALLBACK=1 DRY_RUN=1 bash bin/tick.sh <job>       # DB に取り込む前の jobs/<job>.conf を試す
```

- `NOW` は `YYYY-MM-DD HH:MM[:SS]`(ジョブの TZ の現地時刻)。待たずに判定する。秒まで書くとその 1 秒だけ、
  分までならその分(00〜59 秒)に入る回をすべて判定する(回ごとに 1 行)。
- 実行結果は `logs/<job>.log` に JSONL 1 行(`ts, job, action, reason, at, target, pane, state`)。`at` はその回の時刻(`YYYY-MM-DD HH:MM:SS`)。
  `action` は `sent | ran | skipped_state | skipped_disabled | skipped_locked | skipped_gate | skipped_prompt | failed`(プロンプト型は本文の出どころ `prompt` も)。窓の外・刻みの外(`skipped_window` / `skipped_interval`)は
  DRY_RUN のときだけ出す。DRY_RUN はログを書かず stdout に出す。
- 複数の herdr セッションを別の checkout で動かすときは、`gen-plist.sh` / `install.sh` に `TB_INSTANCE=<名前>` を付ける。
  ラベルが `local.herdr-<名前>-sched-<job>` になり、checkout 同士で衝突しない(未設定なら従来の `local.herdr-sched-<job>`)。
  `HERDR_SESSION` を付けて生成すると plist の環境変数に渡り、tick.sh がそのセッションの agent を見る。
- セッション名は checkout 直下の `herdr-session` ファイル(1 行目に名前。git 管理外)でも固定できる。
  優先順は `HERDR_SOCKET_PATH` > `HERDR_SESSION` > `herdr-session` > `default`。ファイルで決めておけば、
  jobs の COMMAND に `HERDR_SESSION=…` を書かなくてよい(tick.sh も tb も Web UI もファイルを読む)。今どれで決まったかは `tb whoami`。
- launchd 自体の出力は `logs/launchd-<job>.out / .err`。
