# tb が「どのインスタンスを指すか」を決める設計案

- 対象タスク: T-herdrorch-007(設計のみ。実装・台帳の作成・セッション操作は含まない)
- 前提: 複数セッションの紐づけ設計(案 A+) = 1 セッション = 1 checkout = 1 DB = 1 launchd 名前空間 = 1 webui。
  この文書の「インスタンス」は、その 1 組を指す。
- 根拠: コード読み取りと、`ls -l` / `grep` による確認(「確認済み」)。実機で動かしていない点は「推測」と書く。

## 1. 結論(先に)

- 今の tb は「どのインスタンスか」を**持っていない**。PATH 上の `tb` が symlink で指す checkout の DB を開くだけで、
  それが事実上の「現在のインスタンス」になっている(**cwd は見ない**。2.1 章)(確認済み: 普段使う `tb` は開発 checkout の `taskboard/bin/tb` への symlink 1 本。`taskboard/bin/tb:17` の `DB=${TB_DB:-$HERDR_DEFAULT_TB_DB}` は、その checkout を基準に決まる)。
  staging 用 checkout や研修用 checkout を足しても、人のシェルで素の `tb` を打てば、これまでと同じ checkout の DB に書く。**取り違えの根はここ**。
- 推奨は次の 4 つ。
  1. **台帳**(人だけが書く 1 ファイル)にインスタンスを名前で登録し、名前 → checkout・DB・herdr セッション・webui ポートを引く。
  2. **解決の順**は `--instance` > 環境変数 `TB_INSTANCE` > 現在の選択(`tb use`)> 未選択。agent のペインでは、これらを見ず **herdr のセッションから決める**。
  3. **複数インスタンスがあるのに未選択**なら、書き込み系は止まる(fail-closed)。読み取りは動くが、対象を必ず出す。
  4. **全コマンドが stderr に今のインスタンスを出す**。台帳が無い環境は、今と同じ動作(出力の追加もしない)。
- 実装は 5 段階(10 章)。まず `tb whoami`(台帳なし・挙動変更なし)から。
- 人が決める点は 11 章(P1〜P10)。

## 2. 現状

| 項目 | 今の決まり方(根拠) |
|---|---|
| 対象の checkout | 実行された `tb` の実体の位置(`taskboard/bin/tb:11-14` で symlink をたどる)。選ぶ操作は無い |
| DB | `TB_DB` > checkout の `taskboard/data/tasks.db`(`lib/herdr-env.sh`) |
| herdr セッション名 | `HERDR_SOCKET_PATH` > `HERDR_SESSION` > `default`(`herdr_session_name`。`agents.sh` の `HSESS`) |
| launchd ラベル | `TB_INSTANCE` 未設定なら `local.herdr-…`、設定すれば `local.herdr-<instance>-…`(`herdr_label_prefix`。名前は英数字と `. _ -` の 64 字以内) |
| `TB_INSTANCE` を誰が設定するか | checkout 内の `herdr-up.sh` が、default 以外のセッションで `${TB_INSTANCE:-<セッション名>}` を export する |
| webui ポート | `TB_UI_PORT` か、範囲(既定 8800-8819)の先頭から探索 |
| 役割ごとの設定 | `TB_APPROVALS` / `TB_ROUTES` / `TB_ROLES` などは checkout 内が既定 |

既に分かっている穴(別の調査で確認済み):

- 普段使う `herdr-up.sh`(`hd` から呼ばれる側)が checkout と別の場所のコピーだと、上の `TB_INSTANCE` の行が入っていない。
  その場合 staging のペインでも `TB_INSTANCE` は空で、PATH の `tb` は**開発 checkout の DB を開く**(推測: ペインの PATH が人のシェルと同じ前提)。
- hook(`tb-hook`)の DB は「呼ばれたスクリプトがある checkout」で決まる。herdr の設定は全セッション共有なので、どのセッションの hook も同じ checkout を向きうる。

### 2.1 実機の配置(確認済み。読み取りのみで調べた。公開版なので件数は伏せる)

cwd や checkout に関係なく、**PATH 上の `tb`(symlink)が指す checkout の DB** が使われる。
人のシェルで、別 checkout(研修 / hardening 用)の `scheduler` ディレクトリに cd して `tb list` を打っても、普段の開発 checkout のタスク一覧が出た(再現済み)。`tb` は cwd を見ない。

| 場所(呼び名) | 実体 | origin / 関係 | DB | launchd |
|---|---|---|---|---|
| 開発 checkout | git clone。ブランチ `main`、origin は公開リポジトリ | PATH の `tb` の symlink 先(`which -a tb` は 1 本だけ) | 自分の `taskboard/data/tasks.db`(**普段の運用の DB**) | webui と scheduler の一部 |
| hardening 用 checkout | 別の git clone。remote は開発 checkout のローカルパス(`upstream`)で、上流より先行し、worktree 多数・変更あり | 開発 checkout の派生 | **自分の DB(別。開発 checkout の DB とはタスク ID の体系も違う)** | **scheduler の job の大半**(plist が自分の `tick.sh` を指す) |
| 旧 `herdr` ディレクトリ | git リポジトリ。remote なし。`taskboard/bin/tb` は旧版(1 本の大きなスクリプト) | `hd` が呼ぶ `herdr-up.sh` の置き場。`lib/` は無い | **`taskboard/data` が開発 checkout の `taskboard/data` への symlink = 開発 checkout と同じ DB**(別名の入口) | なし |
| staging clone | 別の git clone(origin は公開リポジトリ)。開発 checkout と同じコミット | 検証用 | **未作成**(初回の `tb` 実行時に作られる。確認時点では無い) | なし(install 系は実行させない運用) |

- **フルパスで `tb list` を呼んだときの DB**(DB ファイルの存在と、各 `tb` の DB 既定値の読み取りで確認。`tb` 自体は実行していない。`init_db` が書く可能性があるため):
  開発 checkout → 自分の DB / hardening 用 → 自分の別 DB / 旧 `herdr` → symlink 経由で**開発 checkout と同じ DB** / staging → 初回に自分の新しい DB。
- **DB の共有と分離**:

| 組 | DB | 備考 |
|---|---|---|
| 開発 checkout と旧 `herdr` | **共有**(symlink。パスは別、実体は 1 つ) | `tb instances --check` の「DB の重複」は**パスの文字列でなく realpath で比べる**こと |
| 開発 checkout と hardening 用 | **別** | PATH の `tb` では hardening 用の DB は見えない。見るにはフルパスの `tb` か `TB_DB` |
| 開発 checkout と staging | **別**(staging の DB はこれから) | |

- **launchd が 1 つの名前空間に複数 checkout を混ぜている**(確認済み): ラベルは全部 `local.herdr-…`(`TB_INSTANCE` 無し)で、plist の指す checkout が job ごとに違う。大半の scheduler job は hardening 用 checkout、webui と残りの job は開発 checkout を指す。plist に `TB_DB` は無く、各 job は**自分の checkout の DB** を開く。つまり、同じラベルの規則の下に、2 つの DB を触る job が同居している。`install.sh` は「後勝ち」なので、どの checkout で最後に install したかで job ごとに行き先が決まった結果、と読める(推測。履歴は未確認)。

### 2.2 設計書の前提との照合

| 前提 | 判定 | 直し方 |
|---|---|---|
| 「PATH 上の `tb` が指す checkout の DB が事実上のインスタンス」 | **人のシェルの `tb` については合っている**(cwd を見ない、まで確認) | 1 章の文に「cwd は関係ない」を足した |
| 「1 インスタンス = 1 checkout = 1 DB = 1 launchd 名前空間」(案 A+ の前提) | **実機は満たしていない**。launchd ラベルの名前空間が 1 つで、そこに 2 checkout の job が混在する。また 2 つのパスが同じ DB を指す(symlink) | 台帳の DB 検査を realpath にする。`tb instances --check` に「同じラベル規則の下で plist の向き先が複数 checkout」を足す。**案 A+ のラベル分離(`TB_INSTANCE`)を先に実機へ適用しないと、台帳の「launchd ラベル」の列は意味を持たない** |
| 「`TB_INSTANCE` を `herdr-up.sh` が設定する」 | 実機の launchd には効いていない(plist は従来のラベル) | 台帳の導出は、plist の再生成後にだけ成り立つと書く |
| 台帳の「checkout」の列は 1 つの DB を持つ | 旧 `herdr` は DB を共有する別名なので、台帳には**登録しない**(別名として `tb instances --check` が検出して警告) | 3 章に 1 行足す |

**P1 の推奨は変わらない**(案 (a): `tb` 本体の冒頭で、解決した checkout の `tb` に振り分ける)。理由は、cwd でも PATH でも選べない実機の挙動と、「フルパスで呼ぶ以外に別 DB を見る手段が無い」ことが、案 (a) の動機そのものだから。ただし次を足す。

- 振り分け先の `tb` が**旧版**(旧 `herdr` のような別スクリプト)だと、`TB_FORWARDED` などの合図を解釈しない。台帳に登録できるのは、新しい `tb`(`lib/herdr-env.sh` を持つ checkout)だけにし、`tb instances --check` で確認する。
- 当面は、**hardening 用 checkout を台帳の別インスタンスとして登録**し、`tb --instance <名前> list` でその DB を見られるようにするのが、最初の実用的な利益になる。

## 3. 用語と台帳

### インスタンス名

- `TB_INSTANCE` と**同じ名前空間**にする(新しい名前を増やさない)。規則は既存と同じ: `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`。
- 既定の運用では、**インスタンス名 = herdr セッション名**にする(案 A+ の P2 の推奨と同じ)。`default` セッションのインスタンスは `default`。

### 台帳(`instances.conf`)

- 置き場所: `${XDG_CONFIG_HOME:-$HOME/.config}/herdr-tb/instances.conf`。**人だけが書く**。tb は読むだけで、書くコマンドを作らない。
- 1 行 1 インスタンス、`|` 区切り(presets と同じ流儀)。`#` と空行は無視。

```
# name | checkout(絶対パス) | herdr セッション | webui ポート | DB(空なら checkout の既定)
default | /path/to/orchestrator            | default  | 8800 |
staging | /path/to/orchestrator-staging    | staging  | 8801 |
training| /path/to/orchestrator-training   | training | 8802 | /path/to/other/tasks.db
```

- DB が**同じ実体を指す別名**(symlink)は、別インスタンスとして登録しない。重複の検査は realpath で比べる(2.1 章)。
- 名前・セッション・ポートはそれぞれ**重複不可**(読み込み時に検査し、違反は台帳全体を無効にして止まる)。
- 台帳に書かず**導く**もの(二重管理を避ける): launchd ラベル(`herdr_label_prefix` と同じ規則。`default` は従来のラベルのまま。そのため `default` のインスタンスでは `TB_INSTANCE` を未設定にして渡す。`tb whoami` の「ラベルの接頭辞」も、その実際の値(`local.herdr-` か `local.herdr-<名前>-`)をそのまま出す)、approvals / roles / routes の場所(checkout 内)。
- 台帳ファイルが他のユーザーの所有、または group / world 書き込み可なら、無視して止まる(最低限の衛生。6 章の限界は残る)。
- 台帳が**無い**とき: 「台帳なし」として今と完全に同じに動く。出力も増やさない(互換)。

## 4. コマンド

| コマンド | 内容 | 誰が |
|---|---|---|
| `tb whoami` | 今のインスタンスと、その決まり方(`--instance` / env / 選択 / ペイン / 未選択 / 台帳なし)、checkout、DB、セッション、ポート、ラベルの接頭辞を出す。`--short` は名前だけ(プロンプト用) | 誰でも |
| `tb instances` | 台帳の一覧。現在の選択に印を付ける。`--check` は台帳と実際のずれを探す(checkout が無い、presets の `# session:` と不一致、DB の場所が重複、ラベルの衝突など) | 誰でも(読み取り) |
| `tb use <名前>` | 現在の選択を保存する。台帳に無い名前は拒否 | **人だけ**(6 章) |
| `tb use --unset` | 選択を消す | 人だけ |
| `tb use --shell <名前>` | 保存せず `export TB_INSTANCE=<名前>` を出す(`eval "$(tb use --shell staging)"`)。そのシェルだけの切り替え | 人だけ |
| `tb --instance <名前> <コマンド>` | その 1 回だけ対象を指定する。`tb` の他の引数より前に書く | 人 |

- 「そのコマンドでだけ」は、`--instance`(1 回だけ)と `TB_INSTANCE=staging tb …`(env で 1 回だけ)で満たす。
- `use` は kubectl の `use-context` に相当するが、保存は**ユーザーごとの 1 ファイル**で、シェルごとの切り替えは `--shell` / env で別に取れる(P3)。
- `tb use` / `tb instances` / `tb whoami` は **DB を開かない**(`init_db` の前に処理する)。DB を壊しているインスタンスでも切り替えられるようにするため。

### 実際に別の checkout を動かす仕組み(P1)

選択しても、PATH の `tb` は 1 本の checkout の実体のままなので、**別の checkout の `tb` に渡す**仕組みが要る。

- **案 (a) tb 本体の冒頭で振り分ける(推奨)**。`taskboard/bin/tb` の `source` より前で、解決したインスタンスの checkout が自分と違えば、`TB_INSTANCE` / `HERDR_SESSION` / `TB_DB` / `TB_UI_PORT` を整えて(**`default` のインスタンスでは `TB_INSTANCE` を渡さず unset する**。`TB_INSTANCE=default` を渡すとラベルが `local.herdr-default-` になり、従来の `local.herdr-` と変わる。`herdr-up.sh` の default 分岐と同じ)、相手の `taskboard/bin/tb` を `exec` する。**このとき `HERDR_SOCKET_PATH` は unset し、`HERDR_SESSION=<台帳のセッション>` だけを渡す**(`herdr_session_name` は socket を優先するので、default の socket が残ると `HERDR_SESSION` が無視される。`herdr-up.sh` が両方を unset してから `HERDR_SESSION` を入れるのと同じ流儀。socket のパスを tb が組み立てない)。5 章の agent のペインでの逆引きは、振り分けの前に済ませる。無限ループ防止に `TB_FORWARDED=1` を付け、付いていれば振り分けない。振り分けを持つのは PATH の `tb` を含む checkout だけでよく、相手が古くても動く。
- 案 (b) 別名の薄いラッパー(`tb` を人用の shim に置き換える)。tb 本体は触らないが、shim の更新と配置が増え、agent が PATH の別 `tb` を打つ経路との整合を取る別の仕組みが要る。
- 案 (c) 振り分けなし。選択は DB 等の環境変数を整えるだけ。checkout のコード(approvals などの設定)は PATH の checkout のまま混ざるので、取り違えの防止にならない。

## 5. 解決の優先順位

### 人のシェル(`HERDR_PANE_ID` が無い)

1. `--instance <名前>`
2. 環境変数 `TB_INSTANCE`
3. 現在の選択(`current` ファイル)
4. 未選択

- 1〜3 で決まった名前が台帳に無ければ、**止まる**(暗黙のフォールバックはしない)。
- 明示した `TB_DB` が、決まったインスタンスの DB と違えば、止まる(両方指定したのにずれている状態は、取り違えの典型なので)。
- **`TB_DB` が設定されていて、インスタンスが選ばれていない**とき(P10): tb は `TB_DB` が継承されたものか明示されたものかを区別できないので、区別しない。
  - `TB_DB` が台帳のいずれかのインスタンスの DB と**一致する** → そのインスタンスが選ばれたものとして扱う(未選択ではない)。
  - 一致する DB が無い(**台帳外の DB**) → fail-closed の対象外とし、今と同じに動く。出力には「DB 明示(台帳外)」と出す。
  - この例外は、テスト・scheduler・webui が `TB_DB` で動くための互換。`TB_DB` を付けるだけで fail-closed を抜けられるが、これは 6 章の限界(同じ OS ユーザーが env で回避できる柵)と同じ性質で、新しい穴ではない。
- 4 の扱い:

| 台帳の状態 | 読み取り系 | 書き込み系・送信系 |
|---|---|---|
| 台帳なし | 今と同じ | 今と同じ |
| インスタンス 1 つ | そのインスタンス(選択済みと同じ扱い) | 同左 |
| **2 つ以上で未選択** | **実行する。対象は PATH の `tb` の checkout(今と同じ場所)とし、対象を出す**(「未選択。N 個ある。`tb use` で選ぶ」) | **止まる(fail-closed)**(ただし `TB_DB` が設定されているときは上の `TB_DB` の規則) |

- 読み取り / 書き込みの分類は、**コード側に読み取りコマンドの一覧**(許可リスト)を持つ。一覧に無いコマンドは書き込み扱い(新しいコマンドを足して分類を忘れても安全側に倒れる)。一覧に入れる候補: `list` / `show` / `comments` / `digest` / `agents` / `approval`(参照)/ `limits` / `roles` / `alerts` / `proposals` / `pending` / `outbox`(一覧)/ `whoami` / `instances` など。`touch` と hook 系(`role-context`)は常に 0 で返す既存の決まりを守るので、この判定の外。
- 止まるときの終了コードは専用の値にする(既存の 1 / 2 / 5 / 10 / 11 と衝突しないもの。実装時に決める)。メッセージに、選べるインスタンスの一覧と `tb use` / `--instance` の書き方を出す。

### agent のペイン(`HERDR_PANE_ID` がある)

- agent は台帳の選択(`current`)も `--instance` / `TB_INSTANCE` による切り替えも**使えない**。ペインが属する herdr セッション(`herdr_session_name` = `HERDR_SOCKET_PATH` > `HERDR_SESSION`)から、**台帳の「herdr セッション」の列を逆引き**して決める。
- 台帳に該当が無い・複数ある → 書き込みは止まる。読み取りは対象を出して実行する。
- `TB_INSTANCE` が環境にあり、逆引きの結果と**食い違う**なら止まる(黙って片方を選ばない)。一致していれば、そのまま。
- 同じ checkout を向いていない場合(振り分けが要る場合)は、人のシェルと同じ仕組み(P1)で相手の `tb` に渡す。

## 6. agent は切り替えられない(`tb use` は人専用)

- `tb use`(`--shell` を含む)と `tb use --unset` は、`HERDR_PANE_ID` があれば **拒否する**(終了コード 5。`gate ingest-review` が同じ判定で拒否している実装に倣う)。
- **既知の限界**:
  - `HERDR_PANE_ID` は環境変数なので、同じ OS ユーザーで動く agent は `env -u HERDR_PANE_ID tb use …` で外せる。**これは防御ではなく、うっかりを防ぐ柵**。
  - 親プロセスをたどる・tty を見る判定を足しても、同じユーザーの agent には回避できる(別シェルを起動する等)。足すかどうかは費用対効果で P4。
  - 選択を保存するファイル(7 章)も同じユーザーが書ける。つまり、**agent が `current` を書き換えて、人の次の `tb`(未選択のはずが選択済みになる)の対象を変える**ことは防げない。
- 効く対策は「防ぐ」より「**気づく**」:
  1. 8 章の出力(全コマンドが対象を出す)。人が見れば、書き換えられていても対象が出る。
  2. `tb whoami` に選択の**保存時刻と保存したときの tty / ペインの有無**を出す(`use` が記録する)。
  3. `use` の監査ログ(追記専用の `use.log`。時刻・名前・ペインの有無)。
  4. シェルごとの選択(`--shell` / env)を優先して使う。agent は人のシェルの環境変数を書き換えられない。
- 補助(多層防御): agent の実行設定(Claude Code の権限設定の deny など)で、`tb use` の呼び出しと `~/.config/herdr-tb/` への書き込みを拒否する。シェル経由で回避できるので**柵の一枚**として扱い、主たる防御とは書かない。

## 7. 選択の保存先

- `${XDG_CONFIG_HOME:-$HOME/.config}/herdr-tb/current`(1 行: 名前。2 行目以降に保存時刻など)。ディレクトリ 0700、ファイル 0600。ユーザーごと。
- 台帳と同じディレクトリにする(`instances.conf` が置かれる所で人が見つけやすい)。状態ファイルを設定ディレクトリに置くのが気になる場合は `$XDG_STATE_HOME` 側も取れる(P5。推奨は台帳と同じ場所)。
- checkout の中には置かない(checkout は複数あり、`git` で追跡される事故の元になる)。
- 書き込みは一時ファイル + `mv`(途中の状態を読まれない)。
- **限界**(6 章と同じ): 同じ OS ユーザーの agent は読み書きできる。権限で守れない。読み取りで「他のインスタンスの存在が見える」程度は、同じユーザーの前提では防げないので、台帳に秘密を書かない。
- 研修用など**別の OS ユーザー・別のホームを使う運用**なら、台帳も選択も別になり、完全に分かれる(これが最も強い分離。必要なら案 A+ の外で検討)。

## 8. 取り違えの防止

### 全コマンドの出力に今のインスタンスを出す

- stderr に 1 行: `[tb: staging | checkout=…/orchestrator-staging | db=…/tasks.db | session=staging]`(checkout と DB は末尾 2 要素に短縮。決まり方も付ける: `(use)` / `(env)` / `(flag)` / `(pane)`)。
- **stdout は変えない**(スクリプトや agent が出力を解析しているため)。
- エラーメッセージ(`die` / `usage`)の先頭にもインスタンス名を付ける。
- 台帳なしのときは**出さない**(互換)。台帳があって 1 つしかないときも、既定では出さない(出力を増やす価値が薄い。`TB_BANNER=always` で出せる)。
- 色: 端末のとき、`default` 以外は色を付ける(台帳に `protected` のような列を足さず、名前で決める。P6)。

### 破壊的・送信系は対象を表示してから実行

- 対象: `send` / `report` / `approve` / `reject` / `flush` / `allow` / `ask` / `agent-*`(spawn・stop・shed・restart など)/ `gate` / `pr-queue` / `proposal-resolve` など。
- 実行の直前に「**対象: staging(選択による)。実行します**」を出す。`--yes` なしの確認プロンプトは、スクリプトと agent を止めるので**付けない**(P7 の推奨)。
- 人のシェルで、選択が `current` ファイル(暗黙)から来ていて、かつ台帳が複数あるときだけ、書き込み系に限って **1 行を強調**して出す(明示した `--instance` / env のときは出さない)。

### シェルのプロンプト表示案(zsh)

`tb` を毎回起動すると遅いので、ファイルと環境変数を**直接**読む。zsh の `$(<file)` は子プロセスを作らない。

```zsh
# ~/.zshrc 用の例
_tb_inst() {
  local f=${XDG_CONFIG_HOME:-$HOME/.config}/herdr-tb/current
  local n=${TB_INSTANCE:-}
  [[ -z $n && -r $f ]] && n=$(<$f) && n=${n%%$'\n'*}
  [[ -n $n ]] && print -n "[tb:$n] "
}
setopt prompt_subst
RPROMPT='$(_tb_inst)'"${RPROMPT:-}"
```

- 表示は**選択の写し**であって、決まり方の正本ではない(agent のペインでは ペインから決まる。人のシェルでのみ意味がある)。
- 未選択のときは何も出さないので、「未選択で複数ある」状態は prompt では分からない。気になるなら `[tb:?]` を出す変種を 4 段目に載せる(P6 と同じ検討)。

## 9. 既存との関係(互換)

| 既存 | 扱い |
|---|---|
| `TB_INSTANCE` | そのまま**インスタンス名**として使う(launchd ラベルの接頭辞と同じ規則)。台帳に無い名前を人が env に入れたら止まる(台帳がある場合のみ) |
| `HSESS` / `herdr_session_name` | そのまま。ペインのインスタンス逆引きの入力にする。台帳の「herdr セッション」列は同じ名前を指す |
| launchd ラベル | 台帳に持たず、`TB_INSTANCE` から導く規則を維持(`default` は従来のラベルのまま。`default` のインスタンスでは `TB_INSTANCE` を unset して渡す。`herdr-up.sh` と同じ)。`tb instances --check` が衝突を検査する |
| webui ポート | 台帳の列は **`TB_UI_PORT` の既定値として、`webui` を起動する側に渡す**(webui の探索コードは変えない)。`tb use` は webui や scheduler に**影響しない**(それらは launchd の環境で固定) |
| `TB_DB` | 明示があれば優先。台帳のインスタンスと食い違えば止まる。未選択のときは、台帳の DB と一致すればそのインスタンス、台帳外なら「DB 明示(台帳外)」の例外(5 章) |
| `herdr-up.sh` / presets | 変更しない。`# session:` と台帳のセッションが一致するかを `tb instances --check` で確かめる。ずれた場合に直すのは人 |
| hook(`tb-hook` など) | 常に 0 で返す既存の決まりを守る。インスタンスはペインのセッションから決める。決まらないとき: **台帳が無い運用は現状どおり**(checkout の DB)。**台帳があって決まらない(逆引きに該当が無い・複数)ときは DB を更新せず 0 で返し**、`TOUCH_LOG` に `reason=instance-unresolved` を残す(別インスタンスの DB を誤って更新しないため) |
| **台帳が無い運用** | 今と完全に同じ。振り分けも出力の追加もしない(受け入れ条件。テストで固定する) |

## 10. 段階案

各段階は、前の段階が無くても壊れない。実装は「テスト先行」。

1. **S1: `tb whoami`**(台帳なし)。checkout・DB・セッション・`TB_INSTANCE`・ラベルの接頭辞・決まり方を出す。DB を開かない。何も変えない。
2. **S2: 台帳と `tb instances`**(読み取りのみ)。書式の検査、`--check`、プロンプトの例を docs に載せる。
3. **S3: `tb use` / `--instance` / 振り分け**(P1 の案 (a))。ペインの逆引き、`use` のペイン拒否、`current` の保存、`use.log`。出力に対象の 1 行(複数のときだけ)。
4. **S4: 書き込み系の fail-closed**(複数・未選択)。読み取り許可リスト、専用の終了コード。
5. **S5: 仕上げ**。破壊的操作の対象表示、`TB_BANNER`、色、`--check` の拡張。

S1 だけで「今どこを指しているか」が分かるので、S2 以降を急がなくても事故の確認はできる。

## 11. 人が決める点

- **P1. 別 checkout への渡し方**: (a) `tb` 本体の冒頭で振り分ける(推奨)/ (b) 人用の別名ラッパー / (c) 振り分けなし(環境変数だけ。取り違えを防げない)。
- **P2. 台帳の置き場**: `~/.config/herdr-tb/instances.conf`(推奨。ユーザーごと)/ リポジトリ内(公開物に載る危険があるので非推奨)。
- **P3. 選択の持ち方**: `current` ファイル(全シェル共通)+ `--shell` / env でシェル別の切り替え(推奨。両方用意し、人が使い分ける。迷うなら `--shell` を勧める)/ ファイルだけ / env だけ。
- **P4. ペイン判定を強めるか**: `HERDR_PANE_ID` だけ(推奨。柵と割り切る)/ 親プロセス・tty の判定も足す(回避できるので効果が薄い)。
- **P5. 状態ファイルの場所**: 台帳と同じ `~/.config/herdr-tb/`(推奨)/ `$XDG_STATE_HOME`。
- **P6. 色と prompt**: `default` 以外を色で強調(推奨)/ 台帳に `protected` 列を足して選べるようにする / 色なし。
- **P7. 破壊的操作の確認**: 対象を表示するだけで止めない(推奨)/ `--yes` を要求(スクリプトと agent が止まる)。
- **P8. 台帳が 1 つだけのときの出力**: 出さない(推奨)/ 常に出す。
- **P9. 研修用の分離の強さ**: 同じ OS ユーザーの別 checkout(この設計の範囲。選択と台帳は共有で、6〜7 章の限界を受け入れる)/ 別 OS ユーザー・別ホーム(台帳も選択も別。最も強い。運用の手間が増える)。
- **P10. `TB_DB` が設定済みで未選択のとき**: 台帳の DB と一致すればそのインスタンス、台帳外なら「DB 明示(台帳外)」の例外として今と同じに動かす(推奨。継承と明示を区別できず、テスト・scheduler が `TB_DB` で動くため)/ `TB_DB` も fail-closed の対象にする(テストと scheduler に別の口が要る)/ 台帳外の例外をなくし台帳に登録を必須にする。

## 12. 未確認

- agent のペインの PATH が、人のシェルと同じか(同じ前提で 2 章を書いた)。
- `HERDR_PANE_ID` が agent の起動するサブシェル・子プロセスに必ず引き継がれるか(引き継がれなければ、ペイン判定が外れる)。
- herdr の hook が、セッションごとに別の checkout の `tb-hook` を呼べるか(設定は全セッション共有)。
- 台帳に書く webui ポートと、`HERDR_WEBUI_RANGE` の探索が競合した場合の挙動。
- 既存の `TB_INSTANCE` を人がシェルに恒久的に入れている運用があるか(あれば、3 の選択より優先されるため気づきにくい)。
- `HERDR_SOCKET_PATH` が人のシェルに入る場面(herdr がペインに入れる / 人が設定する / 親から継承される)。入る場合、振り分けでの unset(5 章・4 章の P1)が効く前提になる。
