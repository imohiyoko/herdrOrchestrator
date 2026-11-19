# scheduler

定期実行を agent の cron ではなく macOS の launchd で担保する。agent は呼ばれた回に実行するだけ。
launchd が `bin/tick.sh <job>` を毎分起動し、時間帯・曜日・間隔・agent の状態を**秒単位で**判定して、
`taskboard/bin/tb system-send` で本文を送る(送ったものは taskboard の `messages` に `kind=scheduler` で残る。`TB_BIN` で tb の場所を変えられる)。

## ジョブの足し方

`jobs/<name>.conf` を作る(bash の変数代入)。例は `jobs/watch.conf.example`(`.example` を外して使う)。

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
| `WATCHER` | 任意。`1` で見張り系のジョブ(tb digest が ran / sent の途絶を見る)、`0` で対象外。未設定なら、TARGET が `tb limits` の watchers に入っているか、`COMMAND=` の行がその名前を含むかで推定する。PROMPT のパスやコメントの語は見ない |

### コマンド実行型ジョブ

`TARGET` / `PROMPT` の代わりに `COMMAND='...'`(シェル 1 行)を書くと、agent を介さずそのコマンドを実行する
(例: `jobs/taskboard-digest.conf.example` — worker の報告を 10 分おきにまとめて指揮官へ送る)。`WINDOW` / `DAYS` / `INTERVAL_MIN` の判定は同じ。終日は `WINDOW=00:00-23:59`。

- `TARGET` / `PROMPT` / `PROMPT_FILE` との同時指定はエラー。agent の状態は見ない。
- `COMMAND_TIMEOUT`(秒、既定 60)を超えると子プロセスごと kill し、`failed`(exit 124)。
- 同じジョブの多重実行はロック(`logs/<job>.lock.d`)で防ぎ、実行中なら `skipped_locked`。
- ログには `action`(`ran` | `failed` | `skipped_*`)、`exit`、`stdout`、`stderr`(各先頭 500 字)を記録。
- **`COMMAND` は `bash -c` でそのまま実行される。`jobs/*.conf` は人が管理するもので、外部入力(Slack・ファイル内容・引数など)を埋め込まないこと。**

## ジョブ定義を DB に置く(`tb sched-job`)

ジョブ定義(conf の本文)と上書き設定(settings の本文)は、taskboard の DB の `config` 表にも置ける。
キーは `sched-job:<job>` と `sched-settings:<job>`、本文の書式はファイルと同じ。変更の履歴は `config_changes` に残る(`tb config` と同じ仕組み)。

- 読む順: DB の**このセッション**の行 > DB の共通の行(`*`)> ファイル(`jobs/<job>.conf`・`settings/<job>.settings`)。
  DB に行が無いジョブは、今までどおりファイルを読む。sqlite3・DB・表が無い環境でもファイルを読む。DB は読み取り専用で開く
- セッションごとに別の定義を持てる(`HERDR_SESSION`、checkout の `herdr-session` ファイル。tb と同じ決まり方)
- 書くのは人だけ: `tb sched-job set <job> < 本文` / `edit` / `unset`(`--settings` で上書き設定、`--shared` で共通の行)。
  書く前に、scheduler 自身(`bin/show.sh`)がその本文を読めるかを一時のディレクトリで確かめる(読めなければ書かない)
- 取り込み: `tb sched-job import --dry-run` で確かめてから `tb sched-job import [<job> ...]`。`jobs/*.conf` と `settings/*.settings` を
  このセッションの行(`--shared` なら共通の行)に取り込み、取り込んだファイルは `$HERDR_STATE_DIR/sched-imported/<日時>/` へ退かす。
  既に行があるジョブは飛ばす。取り込みの前後で、判定の結果(`tick.sh` の dry-run)と実際に効く設定(`show.sh`)は変わらない(`tests/run.sh` が確かめる)
- 一覧: `bin/jobs.sh`(DB とファイルの和)、`tb sched-job list`(出どころつき)。`bin/show.sh` は `source` / `settings_source`(`db` / `file` / `none`)も出す
- Web UI: DB に定義があるジョブの上書き設定は、`tb sched-job set <job> --settings` を通して DB に書く(ファイルは書かない)。
  「conf の値に戻す」はこのセッションの行を消す(共通の行は人が `tb sched-job unset <job> --settings --shared` で消す)
- plist(`gen-plist.sh`)はジョブ名だけを持つので、取り込んだあとも作り直さなくてよい
- DB の本文も `COMMAND` と同じく bash として評価される。`tb sched-job` は人の端末からだけ書ける(agent の pane からは断る)

## Web UI からの変更(settings)

間隔・時間帯・曜日・一時停止は、Web UI の `/jobs/<job>` から変えられる。保存先は `settings/<job>.settings`(git 管理外、0600)。

- launchd は**毎分** `tick.sh` を起動し、実行するかどうか(曜日・時間帯・`INTERVAL_MIN` の刻み・一時停止)は `tick.sh` が実行時に判定する。
  なので設定を変えても plist の作り直し・再登録は要らない(次の分から反映)。対象外の回はログを書かない
- 刻みは窓の始端から数える(`START=09:00` `INTERVAL_MIN=15` なら 09:00, 09:15, ...。`START=09:00:10` `INTERVAL_SEC=20` なら 09:00:10, 09:00:30, 09:00:50, ...)
- 時刻は秒単位で判定する。秒を省いた時刻は、開始は `:00`、**終了はその分の `:59` まで**を含む(`WINDOW=09:00-21:00` は 21:00:59 まで)。
  分単位だった頃と同じ意味になるので、既存の conf / settings はそのまま使える
- 1 分の中に刻みの回が複数ある(`INTERVAL_SEC` が 60 未満など)ときは、毎分起動された `tick.sh` がその時刻まで待って順に実行する。
  launchd の起動が遅れて過ぎてしまった回は、最後の 1 回にまとめて実行する(まとめて何度も送らない)。
  前の回が長引いて次の分にかかったら、残りの回は飛ばす。また launchd は実行中のジョブを重ねて起動しないので、
  分の最後の回(例: `:55`)の実行が次の分の頭にかかると、次の 1 分はまるごと飛ぶことがある
- 優先順: `jobs/<job>.conf` < `settings/<job>.settings` < 環境変数 `SCHED_*`
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
```

- `NOW` は `YYYY-MM-DD HH:MM[:SS]`(ジョブの TZ の現地時刻)。待たずに判定する。秒まで書くとその 1 秒だけ、
  分までならその分(00〜59 秒)に入る回をすべて判定する(回ごとに 1 行)。
- 実行結果は `logs/<job>.log` に JSONL 1 行(`ts, job, action, reason, at, target, pane, state`)。`at` はその回の時刻(`YYYY-MM-DD HH:MM:SS`)。
  `action` は `sent | ran | skipped_state | skipped_disabled | skipped_locked | failed`。窓の外・刻みの外(`skipped_window` / `skipped_interval`)は
  DRY_RUN のときだけ出す。DRY_RUN はログを書かず stdout に出す。
- 複数の herdr セッションを別の checkout で動かすときは、`gen-plist.sh` / `install.sh` に `TB_INSTANCE=<名前>` を付ける。
  ラベルが `local.herdr-<名前>-sched-<job>` になり、checkout 同士で衝突しない(未設定なら従来の `local.herdr-sched-<job>`)。
  `HERDR_SESSION` を付けて生成すると plist の環境変数に渡り、tick.sh がそのセッションの agent を見る。
- セッション名は checkout 直下の `herdr-session` ファイル(1 行目に名前。git 管理外)でも固定できる。
  優先順は `HERDR_SOCKET_PATH` > `HERDR_SESSION` > `herdr-session` > `default`。ファイルで決めておけば、
  jobs の COMMAND に `HERDR_SESSION=…` を書かなくてよい(tick.sh も tb も Web UI もファイルを読む)。今どれで決まったかは `tb whoami`。
- launchd 自体の出力は `logs/launchd-<job>.out / .err`。
