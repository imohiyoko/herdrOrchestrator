# tb が「どの herdr セッションを指すか」を決める設計案

- 対象タスク: T-herdrorch-007(設計のみ。実装・台帳の作成・セッション操作は含まない)
- **方針(人の決定。2 回目の改訂)**:
  1. herdr のセッションごとに **DB・台帳・checkout を分ける**。1 つの DB を複数セッションで共有しない。同じ `tb` の実体である必要はない。
  2. 問題は、tb に**セッションを指定する構造が無い**こと。`tb --session staging list` を、どの cwd・どのシェルからでも、staging セッションの tb・DB・herdr に向けて実行できるようにする。
  3. staging は「1 つの OS の上で herdr セッションが複数立ち上がる場合」の模擬と動作確認の場所。
- **後の変更(Web UI)**: Web UI はブラウザから開くポートを入口(`webui/gateway.py`、既定 8800)の 1 つにした。各セッションの Web UI は空きポートで待ち受け、`/s/<セッション名>/` に並ぶ。ログインは全セッション共通。この文書の「webui ポート」「`TB_UI_PORT`」は当時の設計(`webui/README.md` が今の決まり)。
- 前提: 複数セッションの紐づけ設計(案 A+)= 1 セッション = 1 checkout = 1 DB = 1 launchd 名前空間 = 1 webui。
  この文書の「**インスタンス**」は、その 1 組を指し、**herdr セッションと 1 対 1** である(インスタンス名 = セッション名。default は default)。
- 根拠: コード読み取りと、`ls -l` / `grep` / `herdr` の読み取り専用の確認(「確認済み」)。実機で動かしていない点は「推測」と書く。

## 1. 結論(先に)

- 今の tb は「どのセッションか」を**持っていない**。PATH 上の `tb` が symlink で指す checkout の DB を開くだけで、それが事実上の「現在のインスタンス」になっている(**cwd は見ない**。2.1 章。確認済み)。
  別セッション用の checkout を足しても、人のシェルの素の `tb` は、これまでと同じ checkout の DB に書く。**取り違えの根はここ**。
- 推奨は次の 5 つ。
  1. **台帳のキーは herdr セッション名**(人だけが書く 1 ファイル)。1 行 = 1 セッション = 1 checkout = 1 DB = 1 webui ポート。launchd ラベルはセッション名から導く。
  2. **`tb --session <名> <コマンド>` が主たる指定**。台帳の checkout の `tb` に振り分け(`exec`)、そのプロセス内の herdr 呼び出しは**すべてそのセッション**に向く。振り分け先で DB・approvals・roles・ラベル・ポートがそのセッションのものになる(4 章)。
  3. **未指定時**: agent のペインは、ペインの herdr セッション(herdr の環境から決まる)に固定され、切り替えられない。人のシェルは、`--session` > `TB_SESSION` > `HERDR_SESSION` > 現在の選択(`tb use`)の順。台帳に複数あって未選択なら、書き込み系は止まり(fail-closed)、読み取りは対象を表示して実行する(5 章)。
  4. **全コマンドが stderr に今のセッションを出す**。台帳が無い環境は今と同じ動作(出力の追加もしない)。
  5. **別セッション宛ての操作**は、既定では人の `tb --session <宛先> …` だけ。承認は宛先のセッションの設定で判定する(5.1 章)。
- launchd が 1 つの名前空間に 2 checkout の job を混在させている実機の状態は、セッション = launchd 名前空間の 1 対 1 化で解消する。**検出**と**移行手順**は 9.2 章。
- 実装は 7 段階(10 章)。まず `tb whoami`(台帳なし・挙動変更なし)から。
- **webui はセッションごとに別ポート・別プロセス**(人の決定。13 章)。
- **分離の単位**(物理 / 論理 / 1 つの DB)の比較と推奨は 14 章。
- 人が決める点は 11 章(P1〜P24)。

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

- 普段の起動に使う `herdr-up.sh` が checkout と別の場所のコピーだと、上の `TB_INSTANCE` の行が入っていない。
  その場合 staging のペインでも `TB_INSTANCE` は空で、PATH の `tb` は**開発 checkout の DB を開く**(推測: ペインの PATH が人のシェルと同じ前提)。
- hook(`tb-hook`)の DB は「呼ばれたスクリプトがある checkout」で決まる。herdr の設定は全セッション共有なので、どのセッションの hook も同じ checkout を向きうる。

### 2.1 実機の配置(確認済み。読み取りのみで調べた。公開版では一般化し、件数・パス・名前は書かない)

cwd や checkout に関係なく、**PATH 上の `tb`(symlink)が指す checkout の DB** が使われる。
別の checkout のディレクトリに cd して `tb list` を打っても、PATH の `tb` の checkout のタスク一覧が出た(再現済み)。`tb` は cwd を見ない。

運用では checkout が複数ある(呼び名は A〜D。役割は一般化した):

| 呼び名 | 実体 | DB | launchd |
|---|---|---|---|
| A(普段の checkout) | git clone。PATH の `tb` の symlink 先 | 自分の DB(普段の運用) | 一部の job |
| B(派生の checkout) | 別の git clone。A より先行した派生 | **自分の別の DB** | **残りの job** |
| C(旧配置のディレクトリ) | 旧版の `tb` を持つ。`taskboard/data` が A の `data` への symlink | **A と同じ DB**(別名の入口) | なし |
| D(検証用 clone) | 別の git clone。A と同じコミット | 未作成(初回の `tb` 実行時に作られる) | なし |

- フルパスで `tb list` を呼ぶと、各 checkout は自分の DB を見る(C だけは symlink 経由で A と同じ)。`tb` 自体は実行せず、DB の存在と `tb` の既定値の読み取りで確認した。
- DB の共有と分離: A と C は共有(realpath が同じ)/ A と B は別 / A と D は別。`tb sessions --check` の DB の重複は、**パスの文字列でなく realpath で比べる**。
- **launchd が 1 つの名前空間に複数 checkout を混ぜている**(確認済み): ラベルは全部従来の `local.herdr-…`(`TB_INSTANCE` 無し)で、plist の指す checkout が job ごとに違う(A と B)。plist に `TB_DB` は無く、各 job は**自分の checkout の DB** を開く。同じ規則の下に、2 つの DB を触る job が同居している。`install.sh` は「後勝ち」なので、最後にどの checkout で install したかで決まった結果、と読める(推測。履歴は未確認)。

### 2.2 設計書の前提との照合(前の版の前提。今回の方針で 9.2 章に移した点がある)

| 前提 | 判定 | 直し方 |
|---|---|---|
| 「PATH 上の `tb` が指す checkout の DB が事実上のインスタンス」 | **人のシェルの `tb` については合っている**(cwd を見ない、まで確認) | 1 章の文に「cwd は関係ない」を足した |
| 「1 インスタンス = 1 checkout = 1 DB = 1 launchd 名前空間」(案 A+ の前提) | **実機は満たしていない**。launchd ラベルの名前空間が 1 つで、そこに 2 checkout の job が混在する。また 2 つのパスが同じ DB を指す(symlink) | 台帳の DB 検査を realpath にする。`tb sessions --check` に「同じラベル規則の下で plist の向き先が複数 checkout」を足す。**案 A+ のラベル分離(`TB_INSTANCE`)を先に実機へ適用しないと、台帳の「launchd ラベル」の列は意味を持たない** |
| 「`TB_INSTANCE` を `herdr-up.sh` が設定する」 | 実機の launchd には効いていない(plist は従来のラベル) | 台帳からの導出は、plist の再生成後にだけ成り立つ(9.2 章) |
| 台帳の「checkout」の列は 1 つの DB を持つ | 旧配置(C) は DB を共有する別名なので、台帳には**登録しない**(別名として `tb sessions --check` が検出して警告) | 3 章に 1 行足す |

**P1 の推奨は変わらない**(案 (a): `tb` 本体の冒頭で、解決した checkout の `tb` に振り分ける)。理由は、cwd でも PATH でも選べない実機の挙動と、「フルパスで呼ぶ以外に別 DB を見る手段が無い」ことが、案 (a) の動機そのものだから。ただし次を足す。

- 振り分け先の `tb` が**旧版**(旧配置(C) のような別スクリプト)だと、`TB_FORWARDED` などの合図を解釈しない。台帳に登録できるのは、新しい `tb`(`lib/herdr-env.sh` を持つ checkout)だけにし、`tb sessions --check` で確認する。
- 当面は、**派生の checkout B を台帳の別セッションとして登録**し、`tb --session <名前> list` でその DB を見られるようにするのが、最初の実用的な利益になる。

## 3. 台帳

### セッション名(= インスタンス名)

- **キーは herdr のセッション名**。herdr のセッション名以外の識別子は無い(確認済み: `herdr session list --json` の項目は `name` / `default` / `running` / `session_dir` / `socket_path` だけ。socket のパスは名前から決まる)。
- 規則は launchd ラベルと同じ(英数字で始まり、英数字と `. _ -` の 64 字以内。`herdr_label_prefix` の検査と同じ)。`default` はセッション `default`。
- `TB_INSTANCE`(launchd ラベルの接頭辞用の既存 env)は、**セッション名から導く**値に降格する(9 章)。人が手で設定する入力ではなくなる。

### 台帳(`sessions.conf`)

- 置き場所: `${XDG_CONFIG_HOME:-$HOME/.config}/herdr-tb/sessions.conf`。**人だけが書く**。tb は読むだけで、書くコマンドを作らない。
- 1 行 1 セッション、`|` 区切り(presets と同じ流儀)。`#` と空行は無視。

```
# session | checkout(絶対パス) | webui ポート | DB(空なら checkout の既定)
default  | /path/to/orchestrator            | 8800 |
staging  | /path/to/orchestrator-staging    | 8801 |
training | /path/to/orchestrator-training   | 8802 | /path/to/other/tasks.db
```

- **セッション・checkout・DB の実体・ポートは、それぞれ重複不可**(読み込み時に検査し、違反は台帳全体を無効にして止まる)。DB と checkout の重複は realpath で比べる(同じ実体を指す symlink の別名は、別セッションとして登録できない。2.1 章)。
- 台帳に書かず**導く**もの(二重管理を避ける): launchd ラベル(`herdr_label_prefix` と同じ規則。`default` は従来の `local.herdr-` のまま。`default` では `TB_INSTANCE` を**渡さない**)、approvals / roles / routes / projects の場所(checkout 内)。
- 台帳の checkout は、**新しい tb(`lib/herdr-env.sh` を持つ checkout)**だけ。旧版(別スクリプト)は振り分けの合図を解釈できないので、`tb sessions --check` が弾く。
- 台帳ファイルが他のユーザーの所有、または group / world 書き込み可なら、無視して止まる(最低限の衛生。6 章の限界は残る)。
- 台帳が**無い**とき: 「台帳なし」として今と完全に同じに動く。出力も増やさない(互換)。
- presets の `# session:` と台帳の組が食い違う場合は、`tb sessions --check` が警告する(直すのは人)。

## 4. コマンドとセッション単位の routing

| コマンド | 内容 | 誰が |
|---|---|---|
| `tb --session <名> <コマンド>` | **その 1 回だけ**、台帳のセッション `<名>` の checkout の `tb` に振り分けて実行する。`tb` の他の引数より前に書く | 人。agent は**自分のペインのセッションと同じ名前のときだけ**(5 章) |
| `TB_SESSION=<名> tb …` | 同じ意味を env で(シェルや 1 回限りの指定) | 人(agent のペインでは無視して警告) |
| `tb whoami` | 今のセッションと、その決まり方(`flag` / `env` / `use` / `pane` / 未選択 / 台帳なし)、checkout、DB、ポート、ラベルの接頭辞、herdr の socket を出す。`--short` は名前だけ | 誰でも |
| `tb sessions` | 台帳の一覧。現在の選択に印を付ける。`--check` は台帳と実際のずれを探す(9.2 章の launchd の混在、checkout が無い・旧版、presets の不一致、DB の実体の重複、herdr に無いセッション名など) | 誰でも(読み取り) |
| `tb use <名>` | 現在の選択を保存する。台帳に無い名前は拒否 | **人だけ**(6 章) |
| `tb use --unset` | 選択を消す | 人だけ |
| `tb use --shell <名>` | 保存せず `export TB_SESSION=<名>` を出す(`eval "$(tb use --shell staging)"`)。そのシェルだけの切り替え | 人だけ |

- 「そのコマンドでだけ」は `--session`(1 回)と `TB_SESSION=… tb …`(env で 1 回)で満たす。`tb use` は kubectl の `use-context` に相当するが、シェルごとの切り替えは `--shell` / env で別に取れる(P3)。
- `tb use` / `tb sessions` / `tb whoami` は **DB を開かない**(`init_db` の前に処理する)。壊れた DB のセッションでも切り替えと確認ができるようにする。
- **`--instance`**: セッションと 1 対 1 になったので、別名を持つと「2 つの名前が食い違う」状態が生まれる。**廃止を推奨**(まだ実装も公開もされていない。P11)。
- env の整理: `TB_SESSION`(**tb の選択**。人のシェル用)/ `HERDR_SESSION`(**herdr の環境**。agent のペインでは herdr が設定する。tb は振り分け先の子プロセスにだけ渡す)/ `TB_INSTANCE`(**launchd ラベル用の導出値**。`default` では未設定)。`TB_DB` などの個別の上書きは 5 章。

### routing の仕組み(P1)

PATH の `tb` は 1 本の checkout の実体なので、**別セッションの checkout の `tb` に渡す**仕組みが要る。

- **案 (a) `tb` 本体の冒頭で振り分ける(推奨)**。`taskboard/bin/tb` の `source` より前で、解決したセッションの checkout が自分と違えば、次の環境を整えて相手の `taskboard/bin/tb` を `exec` する。
  - `HERDR_SOCKET_PATH` は **unset**、`HERDR_SESSION=<セッション>` だけを渡す(`herdr_session_name` は socket を優先するので、別セッションの socket が残ると `HERDR_SESSION` が無視される。`herdr-up.sh` が両方を unset してから渡すのと同じ流儀。socket のパスを tb が組み立てない)。`default` は両方を unset する(herdr の既定が default のため)。
  - `TB_INSTANCE=<セッション>`(`default` では **unset**。`TB_INSTANCE=default` を渡すとラベルが `local.herdr-default-` になり、従来の `local.herdr-` と変わるため)。
  - `TB_DB`(台帳の DB 列があるときだけ。無ければ unset して相手の checkout の既定に任せる)、`TB_UI_PORT`(台帳のポート。webui を扱う操作のみ)。
  - 継承された `TB_APPROVALS` / `TB_ROUTES` / `TB_CONFIG` / `TB_ROLES` / `TB_ALLOWLIST` は **unset**(別セッションの設定を引き継がない。テストの上書きは `TB_FORWARDED` が付かない経路で使う)。
  - 無限ループ防止に `TB_FORWARDED=1`(と振り分け元の名前)を付ける。付いていれば振り分けない。
  - 振り分けを持つのは PATH の `tb` を含む checkout だけでよい。相手の `tb` は台帳に載る新しい版だけ(3 章)。
- 案 (b) 別名の薄いラッパー(`tb` を人用の shim に置き換える)。tb 本体は触らないが、shim の更新と配置が増え、agent が PATH の別 `tb` を打つ経路との整合を取る別の仕組みが要る。
- 案 (c) 振り分けなし。env を整えるだけ。checkout のコード(approvals などの設定)は PATH の checkout のまま混ざるので、取り違えの防止にならない。

### herdr 側への指定(P14)

- **herdr の `agent` / `workspace` / `pane` の各サブコマンドは `--session` を持たない**(確認済み: `herdr agent list --help` に options が無い)。セッションを選ぶ手段は、**環境(`HERDR_SESSION` / `HERDR_SOCKET_PATH`)**。トップレベルの `herdr --session <名> [options]` は「セッションを使う・作る」ためのもので、`herdr --session <名> agent list` が通るかは**未確認**(12 章)。
- 推奨: `herdr_call` を、**解決済みのセッションを環境で明示する**形にする(`env -u HERDR_SOCKET_PATH HERDR_SESSION=<セッション> herdr …`。`default` は両方を unset)。`herdr_call` は tb の herdr 呼び出しの唯一の入口(`taskboard/lib/transport.sh:7`)なので、1 か所で足りる。**ただし明示するのは、tb が別のセッションを選んだ場合(台帳があり、`--session` などで振り分けたとき)だけ**。台帳なし・振り分けなしでは `HERDR_SOCKET_PATH` を**そのまま継承**する(`herdr_session_name` は標準でない配置の socket のパスから名前を導けず、socket を捨てると herdr の向き先が変わるため)。シェルに残った別セッションの socket による事故は、台帳があるときの `tb sessions --check` と banner の `socket=` 表示で**気づく**形にとどめる。**フラグ形が使えるとわかっても、環境形を主にする**(`herdr-up.sh` が使っている実績のある形。差し替える利益が薄い)。
- この明示は、振り分け済みの子プロセスだけでなく、振り分けが起きない経路(PATH の `tb` がそのセッションの checkout のとき)でも、**シェルに残った別セッションの `HERDR_SOCKET_PATH`** で意図しないセッションに向く事故を防ぐ。

### 振り分け先で確認する観点(テストの項目)

`tb --session <名> whoami` と、擬似 herdr(`HERDR_BIN`)を使った 2 つの fixture checkout で、次を**振り分けの前後で比べる**。

| 観点 | 期待 |
|---|---|
| DB | 台帳の DB(無ければ振り分け先 checkout の既定)。元の checkout の DB を開かない |
| approvals / routes / roles / projects / allowlist | 振り分け先の checkout のファイル。継承した `TB_*` の上書きは落ちる |
| herdr の宛先 | 擬似 herdr が受け取った環境で、`HERDR_SOCKET_PATH` が空・`HERDR_SESSION` が指定のセッション |
| launchd ラベル | `default` は `local.herdr-`、他は `local.herdr-<名>-`(`install` / `gen-plist` を dry-run で) |
| webui ポート | 台帳のポート(`TB_UI_PORT`) |
| 無限ループ | `TB_FORWARDED` 付きは振り分けない。台帳に無い名前は止まる |
| 互換 | 台帳なしでは振り分けも出力の追加もしない |

## 5. 解決の優先順位

### 人のシェル(`HERDR_PANE_ID` が無い)

1. `--session <名>`
2. env `TB_SESSION`
3. env `HERDR_SESSION` / `HERDR_SOCKET_PATH`(**台帳があるときだけ**効く。`herdr_session_name` の結果。既存の案内「`HERDR_SESSION=<名前> で指定`」の運用を壊さないため。`TB_SESSION` と食い違えば止まる)
4. 現在の選択(`current` ファイル)
5. 未選択

- 1〜4 で決まった名前が台帳に無ければ、**止まる**(暗黙のフォールバックはしない)。
- env の `TB_INSTANCE` が、決まったセッションから導く値(`default` は未設定)と違えば、止まる(黙って片方を選ばない)。
- 明示した `TB_DB` が、決まったセッションの DB と違えば、止まる(両方指定したのにずれている状態は、取り違えの典型なので)。
- **`TB_DB` が設定されていて、セッションが選ばれていない**とき(P10): tb は `TB_DB` が継承されたものか明示されたものかを区別できないので、区別しない。
  - `TB_DB` が台帳のいずれかのセッションの DB と**一致する** → そのセッションが選ばれたものとして扱う(未選択ではない)。
  - 一致する DB が無い(**台帳外の DB**) → fail-closed の対象外とし、今と同じに動く。出力には「DB 明示(台帳外)」と出す。
  - この例外は、テスト・scheduler・webui が `TB_DB` で動くための互換。`TB_DB` を付けるだけで fail-closed を抜けられるが、これは 6 章の限界(同じ OS ユーザーが env で回避できる柵)と同じ性質で、新しい穴ではない。
- **黙って無視しない**(P24): `tb use <名>` は保存した後、環境の `TB_SESSION` / `HERDR_SESSION` がそれより優先して効いているなら **stderr で警告**する(「現在の選択は X だが、環境 HERDR_SESSION=Y が優先されている」)。`tb whoami` と banner は決まり方を `(flag)` / `(env)` / `(use)` で出す。`--session` と env が食い違うときは flag が勝つが、**差があることを 1 行出す**。
- 停止メッセージには逃げ道を出す(例: 古い `TB_INSTANCE` が残っているなら「`unset TB_INSTANCE`」)。
- 5(未選択)の扱い:

| 台帳の状態 | 読み取り系 | 書き込み系・送信系 |
|---|---|---|
| 台帳なし | 今と同じ | 今と同じ |
| セッション 1 つ | そのセッション(未選択でも唯一を選ぶ。**決まった名前が台帳に無い、またはペインのセッションと違うときは対象外で、上の停止**) | 同左 |
| **2 つ以上で未選択** | **実行する。対象は PATH の `tb` の checkout(今と同じ場所)とし、対象を出す**(「未選択。N 個ある。`tb use` か `--session` で選ぶ」) | **止まる(fail-closed)**(ただし `TB_DB` が設定されているときは上の `TB_DB` の規則) |

- 読み取り / 書き込みの分類は、**コード側に読み取りコマンドの一覧**(許可リスト)を持つ。一覧に無いコマンドは書き込み扱い(新しいコマンドを足して分類を忘れても安全側に倒れる)。一覧に入れる候補: `list` / `show` / `comments` / `digest` / `agents` / `approval`(参照)/ `limits` / `roles` / `alerts` / `proposals` / `pending` / `outbox`(一覧)/ `whoami` / `sessions` など。`touch` と hook 系(`role-context`)は常に 0 で返す既存の決まりを守るので、この判定の外。
- 止まるときの終了コードは専用の値にする(既存の 1 / 2 / 5 / 10 / 11 と衝突しないもの。実装時に決める)。メッセージに、選べるセッションの一覧と `tb use` / `--session` の書き方を出す。

### agent のペイン(`HERDR_PANE_ID` がある)

- agent は台帳の選択(`current`)も `TB_SESSION` も**使えない**。ペインが属する herdr セッション(`herdr_session_name` = `HERDR_SOCKET_PATH` > `HERDR_SESSION`)で**固定**され、台帳のセッション名として引く。
- 台帳に該当が無い → 書き込みは止まる。読み取りは対象を出して実行する。
- `--session <名>` は、**ペインのセッションと同じ名前のときだけ**通す(振り分けが要るときの指定方法として)。違う名前は拒否(終了コード 5)。別セッション宛ては 5.1 章。
- `TB_SESSION` / `TB_INSTANCE` が環境にあり、ペインのセッションと**食い違う**なら止まる。一致していれば、そのまま。
- 同じ checkout を向いていない場合(PATH の `tb` がペインのセッションの checkout でない場合)は、人のシェルと同じ仕組み(P1)でペインのセッションの `tb` に渡す。

### 5.1 別セッション宛ての操作(P12・P13)

例: 人が default のシェルから、staging セッションの agent に `send` する(agent の conductor からは、この形は成立しない。下記)。

- 現状は、`send` / `report` の宛先は**名前だけ**で、herdr が見るセッションは実行プロセスの環境で決まる。宛先が別セッションにいれば `agent_not_found` で失敗し、**同名の agent が実行セッションにいると、意図しない相手に届く**(案 A+ の調査で確認済み)。
- 方針:
  - 別セッション宛ては、**`tb --session <宛先> send …` と明示したときだけ**。宛先の名前だけで暗黙に別セッションへ届けない(`agent_not_found` のまま)。
  - **送信者の特定は、実行する側のセッションの `agent list` とペインの ID**で行う(`HERDR_PANE_ID` と `pane_owner_name`)。振り分けると `agent list` の見るセッションが宛先側に変わるので、別セッションのペインの ID は**見つからず、送信者を特定できない**(`comments.sh` の「報告者を特定できないため保存しません」と同じ失敗に倒れる)。したがって agent からの別セッション宛ては、そのままでは**成立しない**。
  - 成立させるには、送信者を「振り分け元のセッションとペインの名前」として宛先に渡す仕組みが要る(宛先側が元のセッションの `agent list` を引く、または振り分け元が署名する)。これは agent の自己申告に頼れず、実装が重い。**初期は作らず、人だけが別セッションに送れる**とする(推奨)。
- 承認(approvals)の判定: **宛先のセッションの `approvals.conf` / `routes.conf`**(推奨)。振り分けが宛先 checkout の `tb` を `exec` するので、設定は自然に宛先のものになる。送信元のセッションの設定を持ち込むと、2 つのセッションの設定を突き合わせる仕組みが要り、研修用など設定を**分けたい**セッションの隔離が崩れる。
- 注意: 宛先のセッションの `routes.conf` は、送信者の役割を**宛先の `roles/`** で判定する。人の送信(送信者が人)は、宛先の設定で人として扱われるか(ルールが人を許すか)を実装時に確認する。

### 5.2 解決の入口のまとめ(どの経路で決まるか)

| 経路 | セッションの決まり方 |
|---|---|
| 人のシェル | `--session` > `TB_SESSION` > `HERDR_SESSION`(台帳があるときだけ)> `use` > 未選択。flag は env より常に強く、**env 同士が食い違うときだけ停止**(P24) |
| agent のペイン | ペインのセッション(固定) |
| hook(`tb-hook` / `tb-role-hook`) | ペインのセッション。決まらなければ 9 章 |
| scheduler(launchd) | plist の `HERDR_SESSION`(9.1 章) |
| webui(launchd) | plist の `HERDR_SESSION`(9.1 章) |

## 6. agent は切り替えられない(`tb use` は人専用)

- `tb use`(`--shell` を含む)・`tb use --unset`・他セッションへの `--session` は、`HERDR_PANE_ID` があれば **拒否する**(終了コード 5。`gate ingest-review` が同じ判定で拒否している実装に倣う)。
- **既知の限界**:
  - `HERDR_PANE_ID` は環境変数なので、同じ OS ユーザーで動く agent は `env -u HERDR_PANE_ID tb use …` で外せる。**これは防御ではなく、うっかりを防ぐ柵**。
  - 親プロセスをたどる・tty を見る判定を足しても、同じユーザーの agent には回避できる(別シェルを起動する等)。足すかどうかは費用対効果で P4。
  - 人が herdr のペインの中のシェルで `tb` を打つと、`HERDR_PANE_ID` があるので agent と同じ扱いになり、`tb use` が拒否される(切り替えは `--session` か、ペインの外のシェルで)。
  - **台帳(`sessions.conf`)も同じユーザーが書ける**。台帳の checkout 列は振り分けで `exec` する `tb` のパスなので、書き換えられると別のコードを人の権限で実行させうる(`current` より影響が大きい)。防御ではなく気づく側で対処する: 振り分け時の banner に checkout の**実パス全体**を出す(短縮しない)、`tb sessions --check` が checkout の remote を確認する。
  - `TB_FORWARDED` が人のシェルに残ると振り分けが無効になる。`tb whoami` に `forwarded=1` を出して気づけるようにする。
  - `tb use --shell` が出す `export TB_SESSION=<名>` は `eval` されるので、**台帳に載り名前の規則に合う名前だけ**を出す(実装の条件)。
  - 選択を保存するファイル(7 章)も同じユーザーが書ける。つまり、**agent が `current` を書き換えて、人の次の `tb`(未選択のはずが選択済みになる)の対象を変える**ことは防げない。
- 効く対策は「防ぐ」より「**気づく**」:
  1. 8 章の出力(全コマンドが対象を出す)。人が見れば、書き換えられていても対象が出る。
  2. `tb whoami` に選択の**保存時刻と保存したときの tty / ペインの有無**を出す(`use` が記録する)。
  3. `use` の監査ログ(追記専用の `use.log`。時刻・名前・ペインの有無)。
  4. シェルごとの選択(`--shell` / env)を優先して使う。agent は人のシェルの環境変数を書き換えられない。
- 補助(多層防御): agent の実行設定(Claude Code の権限設定の deny など)で、`tb use` の呼び出しと `~/.config/herdr-tb/` への書き込みを拒否する。シェル経由で回避できるので**柵の一枚**として扱い、主たる防御とは書かない。

## 7. 選択の保存先

- `${XDG_CONFIG_HOME:-$HOME/.config}/herdr-tb/current`(1 行: 名前。2 行目以降に保存時刻など)。ディレクトリ 0700、ファイル 0600。ユーザーごと。
- 台帳と同じディレクトリにする(`sessions.conf` が置かれる所で人が見つけやすい)。状態ファイルを設定ディレクトリに置くのが気になる場合は `$XDG_STATE_HOME` 側も取れる(P5。推奨は台帳と同じ場所)。
- checkout の中には置かない(checkout は複数あり、`git` で追跡される事故の元になる)。
- 書き込みは一時ファイル + `mv`(途中の状態を読まれない)。
- **限界**(6 章と同じ): 同じ OS ユーザーの agent は読み書きできる。権限で守れない。読み取りで「他のセッションの存在が見える」程度は、同じユーザーの前提では防げないので、台帳に秘密を書かない。
- 研修用など**別の OS ユーザー・別のホームを使う運用**なら、台帳も選択も別になり、完全に分かれる(これが最も強い分離。必要なら案 A+ の外で検討。P9)。

## 8. 取り違えの防止

### 全コマンドの出力に今のセッションを出す

- stderr に 1 行: `[tb: session=staging | checkout=…/orchestrator-staging | db=…/tasks.db]`(checkout と DB は末尾 2 要素に短縮。決まり方も付ける: `(use)` / `(env)` / `(flag)` / `(pane)`。別セッションへ振り分けたときは `(flag) → staging` のように振り分け先も)。
- **stdout は変えない**(スクリプトや agent が出力を解析しているため)。
- エラーメッセージ(`die` / `usage`)の先頭にもセッション名を付ける。
- 台帳なしのときは**出さない**(互換)。台帳があって 1 つしかないときも、既定では出さない(出力を増やす価値が薄い。`TB_BANNER=always` で出せる)。
- 色: 端末のとき、`default` 以外は色を付ける(台帳に `protected` のような列を足さず、名前で決める。P6)。

### 破壊的・送信系は対象を表示してから実行

- 対象: `send` / `report` / `approve` / `reject` / `flush` / `allow` / `ask` / `agent-*`(spawn・stop・shed・restart など)/ `gate` / `pr-queue` / `proposal-resolve` など。
- 実行の直前に「**対象: staging(選択による)。実行します**」を出す。`--yes` なしの確認プロンプトは、スクリプトと agent を止めるので**付けない**(P7 の推奨)。
- 人のシェルで、選択が `current` ファイル(暗黙)から来ていて、かつ台帳が複数あるときだけ、書き込み系に限って **1 行を強調**して出す(明示した `--session` / env のときは出さない)。

### シェルのプロンプト表示案(zsh)

`tb` を毎回起動すると遅いので、ファイルと環境変数を**直接**読む。zsh の `$(<file)` は子プロセスを作らない。

```zsh
# ~/.zshrc 用の例
_tb_inst() {
  local f=${XDG_CONFIG_HOME:-$HOME/.config}/herdr-tb/current
  local n=${TB_SESSION:-}
  [[ -z $n && -r $f ]] && n=$(<$f) && n=${n%%$'\n'*}
  [[ -n $n ]] && print -n "[tb:$n] "
}
setopt prompt_subst
RPROMPT='$(_tb_inst)'"${RPROMPT:-}"
```

- 表示は**選択の写し**であって、決まり方の正本ではない(agent のペインでは ペインから決まる。人のシェルでのみ意味がある)。
- 未選択のときは何も出さないので、「未選択で複数ある」状態は prompt では分からない。気になるなら `[tb:?]` を出す変種を S7 の仕上げに載せる(P6 と同じ検討)。

## 9. 既存との関係(互換)

| 既存 | 扱い |
|---|---|
| `TB_INSTANCE` | **セッション名から導く値**に降格(`default` は未設定)。人が env に入れて指定する入力ではなくなる。台帳があって、導く値と食い違えば止まる(5 章)。台帳が無いときは今と同じ(ラベルの接頭辞に使うだけ) |
| `HSESS` / `herdr_session_name` | そのまま。ペインのセッションを引く入力にする。台帳のキーは同じ名前を指す |
| launchd ラベル | 台帳に持たず、セッション名から導く規則を維持(`default` は従来のラベルのまま。`herdr-up.sh` と同じ)。`tb sessions --check` が衝突と混在を検査する(9.2 章) |
| webui ポート | 台帳の列は **`TB_UI_PORT` の既定値として、`webui` を起動する側に渡す**(webui の探索コードは変えない)。`tb use` は webui や scheduler に**影響しない**(それらは launchd の環境で固定) |
| `TB_DB` | 明示があれば優先。台帳のセッションと食い違えば止まる。未選択のときは、台帳の DB と一致すればそのセッション、台帳外なら「DB 明示(台帳外)」の例外(5 章) |
| `herdr-up.sh` / presets | 変更しない。`# session:` と台帳が一致するかを `tb sessions --check` で確かめる。ずれた場合に直すのは人 |
| hook(`tb-hook` など) | 常に 0 で返す既存の決まりを守る。セッションはペインから決める。決まらないとき: **台帳が無い運用は現状どおり**(checkout の DB)。**台帳があって決まらない(該当が無い・複数)ときは DB を更新せず 0 で返し**、`TOUCH_LOG` に `reason=instance-unresolved` を残す(別セッションの DB を誤って更新しないため) |
| **台帳が無い運用** | 今と完全に同じ。振り分けも出力の追加もしない(受け入れ条件。テストで固定する) |

### 9.1 herdr の呼び出し箇所と、セッションを通す必要がある所(P14)

確認済み(`grep`)。「tb の herdr_call」は `taskboard/lib/transport.sh:7` の 1 か所に集約されている。

| 箇所 | 呼び方 | 今のセッションの決まり方 | 変更 |
|---|---|---|---|
| `transport.sh` の `herdr_call` | `"$HERDR_BIN" "$@"` | 実行プロセスの環境 | **ここ 1 か所で、解決済みセッションを環境で明示**(4 章 P14) |
| 同 `herdr_send`(`agent get` / `agent prompt`) | `herdr_call` 経由 | 同上 | 上で足りる。エラー文の `HERDR_SESSION` の案内を `--session` に直す |
| 同 spawn / close(`workspace create` / `agent start` / `workspace close` / `pane close`) | `herdr_call` 経由 | 同上 | 上で足りる |
| `agents.sh` の `agent_sync` / `HSESS` | `herdr_call agent list` + `herdr_session_name` | 環境(socket 優先) | 上で足りる。`HSESS` は source 時に 1 度だけ決まる。振り分けで新しいプロセスを起こす場合は一致する。**同一プロセスの中で環境を差し替えない設計にする**(差し替えると `HSESS` とずれる)。`send.sh` にも別の `HSESS` の計算がある |
| `roles.sh`(`pane_owner_name` ほか)・`comments.sh`・`digest.sh`・`send.sh`・`limits.sh`・`approvals.sh`・`gate.sh`・`compact.sh` | 全部 `herdr_call` 経由(`agent list` / `workspace list`) | 同上 | 上で足りる |
| `send.sh` の `OUTBOX_SESSION` | `HERDR_SESSION` 未指定時の既定の宛先セッション | 設定ファイル | 台帳があれば、台帳のセッションを使う(別の入口を作らない)。台帳なしは今のまま |
| `scheduler/bin/tick.sh`(`agent list`)と `system-send` | `"$HERDR" …` を直接実行。`system-send` は tb を呼ぶ | launchd の環境(`HERDR_SESSION` 無し = default 固定)。**plist に `HERDR_SESSION` を渡す口は案 A+ で追加済み**(`herdr_plist_value_ok`)。使うかは設定次第 | **`tick.sh` の直接呼び出しは `herdr_call` と同じ環境の明示に揃える**。plist は `gen-plist` が台帳のセッションを `HERDR_SESSION` に入れる(default は入れない) |
| `webui/app/herdr.py`(`subprocess.run([HERDR_BIN(), …])`)と `actions.py`(tb を `bash` で起動) | herdr を直接実行。tb は env を引き継ぐ | launchd の環境(default 固定) | 同じ環境の明示を足す(Python 側の `herdr_env.py` に、`herdr_session_name` と同じ規則を持たせる。`tests/test_env.sh` が突き合わせる既存の仕組みを使う) |
| `tb-hook` / `tb-role-hook` | pane・tab・workspace の ID と、`agent list` で名前を引く | herdr が hook に渡す環境 | ペインのセッションで決まる(9 章の表)。`tb-hook` は ID を渡すだけで herdr を呼ばない |
| `herdr-up.sh` | `herdr --session <名>` を明示(`:20`)。起動時に両 env を整理(`:90-92`) | プリセットの `# session:` | 変更しない |
| `setup/check.sh` | ラベルの接頭辞を `TB_INSTANCE` から導く | env | セッション名から導くように揃える(S4) |

- つまり、**tb 本体は 1 か所の変更で全部のセッションを通せる**。残るのは、`tb` を経由せず herdr を直接呼ぶ **scheduler の `tick.sh` と webui の 2 系統**(と、それらの plist の環境)。

### 9.2 launchd の 1 名前空間に 2 checkout が混在している状態の解消

実機の状態(2.1 章): ラベルは全部 `local.herdr-…`(`TB_INSTANCE` 無し)で、plist の向き先の checkout が job ごとに違う。大半の scheduler job は 2 つ目の checkout、webui と残りの job は普段の checkout。plist に `TB_DB` も `HERDR_SESSION` も無い(**推測**: そのため、2 つ目の checkout の job が herdr を呼ぶ場合は default セッションを見る。その checkout の `tick.sh` の版を未確認)。

セッション = インスタンス = launchd 名前空間の 1 対 1 化で、次のように解消する。

- **混在の定義**: 同じラベルの接頭辞(= 同じセッション)の plist の向き先が、台帳のそのセッションの checkout と違う。または、1 つの接頭辞の下に複数の checkout の plist がある。
- **検出**(`tb sessions --check`、読み取りのみ。`launchctl` は呼ばず plist を読む):
  1. `~/Library/LaunchAgents/local.herdr-*.plist` を列挙し、`ProgramArguments` / `WorkingDirectory` のパスから checkout を決める。
  2. ラベルの接頭辞(`local.herdr-<名>-` か、無印 = default)から期待するセッションを決め、台帳の checkout と一致するかを見る。
  3. 不一致を「ラベルのセッション」「plist の checkout」「期待する checkout」の表で出す。plist の環境に `HERDR_SESSION` / `TB_DB` が無いものも指摘する。
  4. `TB_DB` / `HERDR_SESSION` が plist にあるのに、台帳のセッションと食い違うものも指摘する。
- **移行手順**(人が行う。tb は実行しない。この文書も実行していない):
  1. **棚卸し**: 全 plist について、ラベル・向き先 checkout・job 名を表にする(`tb sessions --check` の出力)。どの job をどのセッションに属させるかを人が決める(**P13 とは別の判断。job ごとに「普段のセッションにも要るか」を決める**)。
  2. **台帳を先に作る**: 全セッションと checkout を登録して `--check` が通る状態にする(plist は触らない)。
  3. **default は何も変えない**: 普段の checkout の job は、従来のラベルのまま(互換優先。案 A+ の P3 の推奨)。
  4. **他セッションの checkout ごとに**、その checkout で `TB_INSTANCE=<セッション>`(と `HERDR_SESSION`)を付けて `gen-plist` で plist を作り直し(新しいラベル `local.herdr-<名>-sched-<job>`)、**新旧を並べて内容を比べる**。
  5. **旧ラベルを止めてから新ラベルを入れる**: 旧ラベル(その checkout を指していたもの)を `bootout` し、plist を退避(削除でなく別ディレクトリへ)。その後、新ラベルを `install`。**順序を逆にすると同じ job が二重に動く**。job ごとに 1 つずつ行う。
  6. **後勝ちの巻き戻し**: 旧い `install` の「後勝ち」で、普段の checkout の job が 2 つ目の checkout で上書きされた可能性がある。各旧ラベルの向き先を見て、普段のセッションに属すべき job は**普段の checkout から再 install** する。
  7. **確認**: `tb sessions --check` が混在 0。`launchctl list` のラベルが期待どおり。各 job の最初の実行で、そのセッションの DB と herdr を見ていること(ログのセッション名・DB パス)。
  8. **戻し方**: 退避した旧 plist を戻して `bootstrap` する。DB は触らないので、データの戻しは不要。
- **ずれたまま残さないための予防**: `install.sh` が、同じ接頭辞の既存の plist の向き先が自分の checkout と違えば、**警告して止まる**(上書きしない)。今の「後勝ち」をやめる(S4)。
- staging は、この移行の**予行**に使う: 1 つの OS 上に 2 セッション(default と staging)を立て、staging の checkout で `gen-plist` / `install` を dry-run し、ラベルが衝突しないこと、`--check` が混在を検出し、直すと消えることを先に確かめる(普段のセッションの plist に触らずに)。

## 10. 段階案

実装は「テスト先行」。順序の依存: **S4 は S3 の後(または同時)、S5 は S4 の後**(止まったときの逃げ道が `--session`)。S5 の前に、全 agent のセッションが台帳に載っていることを `tb sessions --check` で確かめる(載っていないと書き込みが止まる)。**テストは台帳の場所を差し替えられる前提**: `XDG_CONFIG_HOME` を一時領域に向け、台帳なしを既定とし、台帳ありの試験は専用のケースだけが台帳を置く。実台帳のある開発機でも、実台帳の有無の両方で既存のテストが同じ結果になることを S2 の受け入れ条件にする。staging(1 つの OS 上の複数セッションの模擬)が、各段階の動作確認の場所になる。

1. **S1: `tb whoami`**(台帳なし。**実装済み**: `taskboard/lib/whoami.sh`。台帳・`--session`・`forwarded` の表示は後の段)。checkout・DB・herdr セッション(`herdr_session_name`)・`TB_INSTANCE`・ラベルの接頭辞・決まり方を出す。DB を開かない。何も変えない。
2. **S2: 台帳と `tb sessions`**(読み取りのみ)。書式の検査、`--check`(**9.2 章の launchd 混在の検出を含む**。plist を読むだけ)。
3. **S3: `herdr_call` のセッション明示**(S4 と同時か、S4 の前)。`herdr_call` 1 か所で、tb が別のセッションを選んだとき(台帳あり・振り分けあり)だけ、解決済みセッションを環境で明示する。**台帳なし・振り分けなしは `HERDR_SOCKET_PATH` を継承したまま、挙動を変えない**(標準でない socket 配置でも同じ)。擬似 herdr のテストで、標準・非標準の両方の配置を固定する。
4. **S4: `--session` と振り分け**(P1 の案 (a))。`TB_SESSION`、ペインの固定、別セッションの `--session` の拒否(agent)、`TB_FORWARDED`、4 章の観点のテスト。**scheduler の `tick.sh` と webui に同じ環境の明示**を入れ、`gen-plist` が `HERDR_SESSION` を入れ、`install.sh` が向き先の食い違いで止まるようにする。
5. **S5: `tb use` と fail-closed**。`current` の保存、`use.log`、ペインでの拒否、書き込み系の fail-closed(複数・未選択)、読み取り許可リスト、専用の終了コード。
6. **S6: 別セッション宛ての送信**(P12 で許す場合のみ)。人の `tb --session <宛先> send` を確かめるテストから。agent からの送信者の引き継ぎは別の設計(5.1 章)。
7. **S7: 仕上げと移行**。案 2(1 checkout・セッション別ディレクトリ)に進む場合の S8 は 14.7 章。webui の台帳への寄せ方は 13.6 章(W1〜W4。S2 以降と並行できる)。破壊的操作の対象表示、`TB_BANNER`、色、launchd の移行(9.2 章。人の手順)。

S1 だけで「今どこを指しているか」が分かるので、S2 以降を急がなくても事故の確認はできる。S3 は、台帳なしでも効く(シェルに残った別セッションの socket による取り違えを防ぐ)ので、**S2 と並行してよい**。

## 11. 人が決める点

- **P1. セッション単位の routing の仕組み**: (a) `tb` 本体の冒頭で台帳の checkout の `tb` に振り分ける(推奨)/ (b) 人用の別名ラッパー / (c) 振り分けなし(env を整えるだけ。取り違えを防げない)。
- **P2. 台帳の置き場**: `~/.config/herdr-tb/sessions.conf`(推奨。ユーザーごと)/ リポジトリ内(公開物に載る危険があるので非推奨)。
- **P3. 選択の持ち方**: `current` ファイル(全シェル共通)+ `--shell` / env でシェル別の切り替え(推奨。両方用意し、人が使い分ける。迷うなら `--shell` を勧める)/ ファイルだけ / env だけ。
- **P4. ペイン判定を強めるか**: `HERDR_PANE_ID` だけ(推奨。柵と割り切る)/ 親プロセス・tty の判定も足す(回避できるので効果が薄い)。
- **P5. 状態ファイルの場所**: 台帳と同じ `~/.config/herdr-tb/`(推奨)/ `$XDG_STATE_HOME`。
- **P6. 色と prompt**: `default` 以外を色で強調(推奨)/ 台帳に `protected` 列を足して選べるようにする / 色なし。
- **P7. 破壊的操作の確認**: 対象を表示するだけで止めない(推奨)/ `--yes` を要求(スクリプトと agent が止まる)。
- **P8. 台帳が 1 つだけのときの出力**: 出さない(推奨)/ 常に出す。
- **P9. 研修用の分離の強さ**: 同じ OS ユーザーの別 checkout(この設計の範囲。選択と台帳は共有で、6〜7 章の限界を受け入れる)/ 別 OS ユーザー・別ホーム(台帳も選択も別。最も強い。運用の手間が増える)。
- **P10. `TB_DB` が設定済みで未選択のとき**: 台帳の DB と一致すればそのセッション、台帳外なら「DB 明示(台帳外)」の例外として今と同じに動かす(推奨。継承と明示を区別できず、テスト・scheduler が `TB_DB` で動くため)/ `TB_DB` も fail-closed の対象にする(テストと scheduler に別の口が要る)/ 台帳外の例外をなくし台帳に登録を必須にする。
- **P11. `--instance` の扱い**: 廃止し `--session` だけ(推奨。1 対 1 なので 2 つの名前は食い違いの元。まだ実装も公開もされていない)/ `--session` の別名として残す(食い違いは同じ値だけ許す)。`TB_INSTANCE` は launchd ラベル用の導出値として残す。
- **P12. 別セッション宛ての操作**: (a) 人の `tb --session <宛先> …` だけ許す。agent は拒否(推奨。送信者の引き継ぎが未設計で、別セッションの agent は元のセッションのペインを `agent list` で引けない)/ (b) agent も `--session` 明示で許す(送信者の引き継ぎの設計が先に要る)/ (c) 許さない(同名の宛先の取り違えは防げるが、人が別セッションを操作する手段も無くなる)。
- **P13. 別セッション宛ての承認の判定**: 宛先のセッションの `approvals.conf` / `routes.conf`(推奨。振り分けが宛先の `tb` を実行するので自然。設定を分けたいセッションの隔離が保てる)/ 送信元の設定 / 両方で許可されたときだけ(2 つの設定を突き合わせる仕組みが要る。要検討)。
- **P14. herdr へのセッション指定の形**: 環境で明示する(`HERDR_SOCKET_PATH` を unset して `HERDR_SESSION`。`default` は両方 unset)。`herdr_call` 1 か所 + scheduler の `tick.sh` + webui の 2 系統(推奨。実績がある形)/ `herdr --session <名> …` のフラグ形(サブコマンドで通るか未確認。確認できたら再検討)。
- **P15. webui のポートの決め方**: 台帳の webui ポート列で固定し、使用中なら別ポートへ逃げずエラー(推奨)/ 台帳のポートを起点に +1 探索。範囲の自動探索は台帳に無いセッションの既定だけ(13 章)。
- **P16. 取り違え防止の画面表示**: タイトルとヘッダにセッション名を常時表示し、default 以外は色の帯を付ける(推奨)/ 名前だけ / 表示なし。
- **P17. 目次ページ**: 置かない(推奨。目次自体が全セッションの一覧を持つ別の入口になる。`tb sessions` で足りる)/ 台帳を読むだけの静的な 1 ページを置く(認証は各セッション側)。
- **P18. 台帳導入前の webui の扱い**: 別の実装担当の `TB_INSTANCE` 方式をそのまま使い、台帳導入後に移行(推奨)/ 台帳を先に作ってから webui を実装。
- **P19. 既存の webui の Cookie 名**: セッション名入りの名前に変える。default だけは従来の `herdr_webui_session` のまま(推奨。互換)/ default も新しい名前にする(既存のログインが 1 回切れる)。
- **P20. 分離の単位**(14 章): 混成 = 既定は案 2(1 checkout・セッション別ディレクトリ)、コードの版を分けたいセッションだけ台帳の checkout 列で別 clone(推奨)/ 案 1 のみ(今のまま)/ 案 2 のみ。案 3 は却下。
- **P21. 設定の継承**: セッション別に**完全に独立**(フォールバックなし。新しいセッションは人が雛形から作る)(推奨)/ 既定のディレクトリへフォールバック(研修用など隔離したいセッションが普段の設定を読んでしまうので非推奨)。
- **P22. DB の中のセッション名の照合**: 入れる。DB の `meta` に記録し、開くときに指定と照合して不一致は fail-closed(推奨。取り違えと、DB ファイルのコピー・移動の事故を拾う)/ 入れない。
- **P23. スキーマの移行と版**: 全 DB の一括移行を**人が実行する明示コマンド**にし、通常の起動は「自分の DB だけ遅延移行」(推奨)/ 常に一括 / 遅延だけ。**DB の版がコードより新しいときは書き込み系を止める**(今は止めない。14.4 章)。
- **P24. 解決の順の矛盾の扱い**: 優先順位の表を 1 つ(5.2 章)に統一し、効かなかった指定は**警告を出す**(推奨。`tb use` が環境に負けるのを黙らせない)/ 黙って優先順に従う(取り違え防止の目的に反する)/ 食い違いはすべて停止(既存の `HERDR_SESSION` 運用が止まる)。

## 12. 未確認

- agent のペインの PATH が、人のシェルと同じか(同じ前提で 2 章を書いた)。
- `HERDR_PANE_ID` が agent の起動するサブシェル・子プロセスに必ず引き継がれるか(引き継がれなければ、ペイン判定が外れる)。
- herdr の hook が、セッションごとに別の checkout の `tb-hook` を呼べるか(設定は全セッション共有)。
- **`herdr --session <名> agent list` のようなフラグ形が通るか**(今回、セッションへの影響を避けて試していない。通れば P14 の別案になる)。`HERDR_SESSION=default` を明示した場合の herdr の挙動(default は両方 unset にする前提で、試していない)。
- 台帳に書く webui ポートと、`HERDR_WEBUI_RANGE` の探索が競合した場合の挙動。
- 既存の `TB_INSTANCE` を人がシェルに恒久的に入れている運用があるか(あれば、導く値と食い違って止まる)。
- `HERDR_SOCKET_PATH` が人のシェルに入る場面(herdr がペインに入れる / 人が設定する / 親から継承される)。入る場合、振り分けでの unset と 5 章の `HERDR_SESSION` / socket の扱いが効く前提になる。
- 2 つ目の checkout の `tick.sh` の版と、その job が今どのセッションの herdr を呼んでいるか(9.2 章の推測)。
- 宛先のセッションの `routes.conf` が、人の送信者を許すか(5.1 章)。
- herdr のセッションが停止中(`running: false`)のとき、`tb --session <名>` の読み取り系が何を返すか(herdr の呼び出しは失敗し、DB だけで動く読み取りは動く、と想定)。
- webui のログイン・Cookie 名の変更が、ブラウザの既存のログインに与える影響(default を従来の名前のままにする P19 の根拠は推測)。
- 台帳のポートが、別の常駐ソフト(webui 以外)に使われていた場合の起動失敗の見え方(launchd のログに出るだけで、人が気づきにくい。`tb sessions` の状態列で見えるようにする案は 13 章)。
- 案 2 を採った場合の `webui/run`(認証・Cookie の状態)、`scheduler/logs`・`jobs`・`settings`、`roles/` を、セッション別ディレクトリにどこまで移せるか(コードの参照箇所の洗い出しは未実施)。
- 古い版の `tb` が、新しい版のスキーマの DB を開いたときの実際の挙動(コードを読んだ限り止めない。実機未検証)。

## 13. webui: セッションごとに別ポート・別プロセス

人の決定: webui は **セッションごとに別ポート・別プロセス**(案 1)。次の 2 案は採らない。

- 同一ポートで ID/パスワードを切り替えてセッションを選ぶ形: 並行作業のたびに切り替える手間が要る。
- 1 つのプロセスが全セッションの DB を読む形: 研修用などの分離が**プロセスの段で崩れる**(1 つのプロセスが全 DB を開ける)。

### 13.1 現状(確認済み。コード読み取り)

- bind は `webui/app/handler.py` の `bind_server`。`TB_UI_PORT` が**数字で設定されていれば探索せず**、使用中なら終了する。無ければ `HERDR_WEBUI_RANGE`(既定 8800-8819)を先頭から探し、全部使用中なら終了する。**固定の挙動は既に目的に合う**。
- ホストは `webui/app/config.py` の定数(ループバック)。変更しない(4 章の方針どおり)。
- Cookie 名は定数 `herdr_webui_session`(`config.py`)。認証の状態(`auth.json` / `sessions.json`)は `RUN_DIR`(既定は checkout の `webui/run`)にあるので、**checkout ごとに別**。
- `gen-plist.sh` は `HERDR_SESSION` と `TB_UI_PORT` が設定されていれば plist の環境に渡す。`TB_INSTANCE` が設定されていればラベルは `local.herdr-<instance>-webui`。
- 別の実装担当が 先行ブランチで、既存の `TB_INSTANCE` を使う形で先行している。確認時点では、その worktree に未コミットの変更もコミットも無かった(**コードの中身は見ていない**)。

### 13.2 決まり(1 つのセッション名から全部決まる)

| 項目 | 決め方 | `default` | 他のセッション(例 staging) |
|---|---|---|---|
| ポート | 台帳の webui ポート列(固定)。使用中ならエラーで終了 | 台帳の値(従来の運用は 8800 から探索) | 台帳の値 |
| launchd ラベル | `local.herdr-<セッション名>-webui` | `local.herdr-webui`(従来のまま) | `local.herdr-staging-webui` |
| DB | 台帳の DB 列、無ければそのセッションの checkout の既定 | default の checkout | staging の checkout |
| Cookie 名 | `herdr_webui_<セッション名>` | `herdr_webui_session`(従来のまま。P19) | `herdr_webui_staging` |
| ログイン情報(`auth.json`) | そのセッションの checkout の `webui/run`(**セッションごとに別のユーザー名・パスワード**) | default の checkout | staging の checkout |
| herdr のセッション | plist の `HERDR_SESSION` | 渡さない | `staging` |
| 画面の表示 | セッション名(13.4 章) | 色なし | 色の帯 |
| bind | **ループバックのまま**(変えない) | 同左 | 同左 |

- 台帳からの導出なので、**`tb sessions` と `gen-plist` が同じ表を見る**(二重管理しない)。

### 13.3 ポート(P15)

- 台帳にあるセッション: **固定**。`TB_UI_PORT` を台帳の値にして渡し、**使用中なら別ポートへ逃げず、エラーで終了**(launchd のログに残る)。人が知らないうちに別のポートで立ち上がり、ブックマークや認証が食い違う事故を避けるため。
- 台帳にないセッション(台帳なしの運用を含む): 今と同じ範囲探索(8800-8819)。互換のため。
- 台帳の検査で、ポートの重複と、範囲探索の範囲に入る値を避けられるかを見る(台帳のポートが 8800-8819 のときは、台帳なしのセッションの探索とぶつかりうる。`tb sessions --check` が警告)。
- 起動の可否は `tb sessions` の状態列で見える(13.5 章)。

### 13.4 Cookie と取り違え防止の表示

- **Cookie は host 単位で、ポートでは分離されない**(ブラウザの仕様)。同じホストの別ポートの webui が同じ名前の Cookie を上書きし合い、ログインが互いに外れる。そのため、**Cookie 名にセッション名を入れる**(13.2 章の表)。`SameSite=Strict` と `HttpOnly` は変えない。
- 画面の表示(P16): 全ページの**タイトル**と**ヘッダ**にセッション名を出す。`default` 以外は、ヘッダの**色の帯**を付ける(色はセッション名から決まる固定の割り当てか、台帳に色の列を足さず名前の hash で決める)。ログイン画面と `/setup` にも出す(別セッションのパスワードを誤って入れる事故を防ぐ)。
- Origin / Referer の検査(`_origins`)は、自分のポートだけを許す現状のままでよい(別ポートは別オリジン)。

### 13.5 一覧コマンドと目次ページ(P17)

- `tb sessions` に、列を足す: セッション名・**webui の URL**(`http://<ループバック>:<ポート>/`)・**状態**(`webui: 稼働 / 停止 / ポート使用中(別プロセス)`)・herdr の状態(`running` / `stopped`)・checkout。
  - 状態の確認は、ポートへの接続を試す(読み取りのみ。認証のページには入らない)。launchd の状態は `launchctl` を呼ばず、plist の有無と接続の可否で示す。
- **目次ページは置かない(推奨)**。目次は全セッションの名前と URL を 1 か所に出す**別の入口**で、これ自体が別プロセスになるか、どれかのセッションの webui が他のセッションの存在を知ることになり、「プロセスの段で分離する」という決定の趣旨に反する。人が一覧を見る手段は `tb sessions`(台帳を読むだけ)で足りる。置くなら、台帳を読んで**リンクを出すだけの静的ページ**(各セッションの認証は各セッション側)にとどめる。

### 13.6 移行の段階(P18)

別の実装担当の先行ブランチは、**既存の `TB_INSTANCE`**(ラベルの接頭辞)を使う形で先行している。台帳の導入前に作った分を、台帳に寄せる。

1. **W1(先行分。台帳なし)**: `TB_INSTANCE` で webui のラベル・`TB_UI_PORT`・`HERDR_SESSION` を決める。Cookie 名と画面表示も `TB_INSTANCE` から導く(`default` = 未設定は従来のまま)。**台帳なしの互換を守る**(未設定なら今と同じ)。
2. **W2(台帳の読み取り = 設計の S2)**: `tb sessions` が台帳の webui ポート列を表示し、W1 の実際の設定(plist の `TB_UI_PORT` / ラベル)と食い違えば `--check` が警告する。**webui のコードはまだ変えない**。
3. **W3(台帳のポートへ寄せる)**: `gen-plist` が、`TB_UI_PORT` を台帳のポート列から入れる(env の明示があればそちらを優先し、台帳と食い違えば止まる)。webui のコードは変えない(既存の `TB_UI_PORT` を使うだけ)。
4. **W4(任意)**: Cookie 名・表示の導出元を `TB_INSTANCE` から「`HERDR_SESSION` + 台帳」に寄せる。W1 と同じ結果になることを、テストで固定する。

- 1 〜 3 のどの時点でも、**台帳なしの運用は変わらない**。
- 別の実装担当の分が `TB_INSTANCE` を**そのまま使う**ので、台帳導入後も `TB_INSTANCE` は「セッション名から導く値」として残る(3 章)。**矛盾しない**。
- 移行後の確認: `tb sessions --check` が、台帳のポートと plist の `TB_UI_PORT`、ラベルと plist の向き先(9.2 章)を一度に検査する。

## 14. 分離の単位: 物理(clone)/ 論理(1 checkout)/ 1 つの DB

人からの提案: 「DB だけアプリの内部で論理分離する形(1 checkout、セッション名から DB を決める)の方が綺麗かもしれない」。A(セッション = instance の 1 対 1)の実現方法として、3 案を比べる。

### 14.1 3 案

- **案 1(物理分離)**: セッションごとに clone(または worktree)。今の 派生の checkout B と staging 用 clone。
- **案 2(論理分離)**: **1 checkout** で、DB を `taskboard/data/<セッション>/tasks.db` のようにセッション名から決める。approvals・roles・routes などの設定もセッション別のディレクトリ。launchd ラベルと webui ポートもセッション名から決める。`tb --session X` は **env と DB パスの切り替えだけ**で済み、別の `tb` への振り分け(`exec`)が要らない。
- **案 3(1 つの DB に session 列)**: 全テーブルにセッション列を足し、1 つの DB に混ぜる。

### 14.2 比較表

| 観点 | 案 1 物理 | 案 2 論理 | 案 3 session 列 |
|---|---|---|---|
| コードの版を分けられるか | **できる**(clone ごとに別ブランチ・別コミット) | **できない**(全セッションが同じコード。上げれば全部上がる) | できない |
| 変更の大きさ | 小(振り分け = 4 章。DB の場所は今のまま) | 中(DB・設定・roles・ログ・`webui/run` の参照先をセッション別にする) | **大**(全テーブルのスキーマ・全クエリに session 条件) |
| 設定(approvals など)の独立 | 自然に独立(checkout 内) | ディレクトリで独立(実装が要る) | 設定は別に分ける必要があり、DB だけ共有しても意味が薄い |
| DB の隔離 | ファイルが別・checkout も別 | ファイルが別(同じ checkout の中) | **混ざる**。条件の書き漏れ 1 つで別セッションの行が見える・書き換わる |
| 同じ OS ユーザーでの隔離 | 別ディレクトリの別ファイル。agent が開けば読める(限界は 14.6) | 同じ。さらに同じ checkout のコードを共有 | 最悪(1 ファイル) |
| 研修用のコード固定 | **できる**(clone を固定) | できない(全体と同じコード) | できない |
| マージ前のブランチの検証(staging の本来の目的) | **できる** | **できない**(検証中のコードが、同じ checkout の他のセッションを動かす) | できない |
| 運用の手間 | clone ごとに pull(worktree なら軽い) | 1 か所の更新で済む | 1 か所 |
| 移行 | 既存の実機はそのまま | 既存 DB をセッション別の場所へ移す手順が要る(14.5 章) | 全 DB の統合と列の埋め込み(戻せない) |
| 実機の現状との差 | **現状そのもの** | 大きい | 最大 |

### 14.3 案 3 を却下する理由

- スキーマの変更が最大(全テーブルに列、主キーに `(session, …)`、全クエリに条件)。agents の主キーが name のみ(案 A+ の調査)で、すでに共有で壊れる箇所が確認されている。
- **条件の書き漏れが、別セッションのデータの閲覧・更新になる**。研修用セッションに普段のタスクが見える事故が、**コードの 1 行の漏れ**で起きる。論理分離を DB のファイルで行う案 2 は、漏れても**開いているファイルが別**なので事故にならない。
- `approvals.conf` などの設定は DB の外にあるので、列だけでは分離にならない。結局、設定の分離は別に要る(案 2 の作業を含む)。
- 戻せない移行(統合した DB を後で分けるのが難しい)。
- 以上より、**却下を推奨**(P20)。

### 14.4 案 2 の評価観点

- **マイグレーション**:
  - 今の仕組みは DB ごとの `PRAGMA user_version`(`taskboard/lib/db.sh` の `init_db`)で、**開いたときに遅延で最新に上げる**。案 2 ではセッションごとの DB が各自の `user_version` を持つので、**セッションごとの `schema_version` は既にある**(新しい列は不要)。
  - ただし**遅延**なので、コードを更新した後、**ある DB は最初に開かれるまで古い版のまま**。tb を使わないセッションの DB は古いままになり、scheduler・webui がその DB を開くときに初めて上がる(上げる側が合わないと失敗する)。
  - **全 DB の一括移行**: 人が実行する明示コマンド(例: `tb sessions --migrate`。読み取りの `--check` とは別)にする(P23)。実行前に各 DB を `sqlite3 .backup` で退避し、全 DB を順に上げ、どれかが失敗したら**そこで止めて報告**(成功した DB はそのまま。版の混在を表で出す)。通常の起動は、自分の DB だけ遅延で上げる(今のまま)。
  - **コードが古く、DB が新しい場合**: 確認済み(コード読み取り)として、`init_db` は `user_version` が最新以上なら**何もせず通す**(止めない)。別 clone の古い版の `tb` が、新しい版の DB を開くと、**知らない列・表があるまま書き込む**。案 2 では全セッションが同じコードなので起きにくいが、**混成**(別 clone のセッションがある)では起きうる。→ **DB の `user_version` がコードの最新より新しければ、書き込み系を止める**(P23。読み取りは通す)。
- **設定の継承(P21)**:
  - 設定のディレクトリは、`taskboard/data/<セッション>/config/`(DB と同じ場所)のように**セッション別**にする。approvals / routes / limits / notify / repos / gate など(今は `taskboard/config/` にあり、未追跡のものが多い)。
  - **フォールバックなしで完全に独立**を推奨。理由: 研修用の 派生(B)は「普段の設定が見えない」ことが要件(案 A+ の 5 章)。フォールバックがあると、セッション別の設定が**無い項目は普段の値**になり、設定の置き忘れが隔離の穴になる(fail-open)。
  - 新しいセッションは、人が雛形(`*.example`・追跡されている `approvals.conf` の既定)から作る。雛形からの作成を助ける**読み取りの確認**(`tb sessions --check` が、必須の設定の欠けを列挙する)は足してよい。コピーそのものは人。
  - `roles/` は追跡されたファイル(コードと同じ版)。**既定は checkout の `roles/` を全セッションが使う**。セッション別に変えたいときは、セッション別ディレクトリに**置き換え**(マージしない)で持つ。
  - 同様に、`scheduler/jobs`・`settings`・`logs`、`webui/run`(認証・Cookie)も**セッション別**にする(13 章の「ログイン情報はセッションごとに別」と整合)。
- **DB の中のセッション名(安全弁。P22)**:
  - DB に `meta`(key / value)の表を足し(新しい移行ファイル 1 つ)、`session` を記録する。**初回に作られるとき**に、そのときの指定(解決したセッション名)を入れる。
  - 開くたびに、指定したセッション名と `meta.session` を照合し、**不一致なら fail-closed**(書き込み・読み取りとも止める。メッセージに両方の名前を出す)。DB ファイルを別のディレクトリへコピーした・パスを取り違えた・`TB_DB` で別セッションの DB を指した、を拾う。
  - **台帳外の DB**(`TB_DB` 明示。P10)は `meta` が無い・名前が無いので、照合しない(今と同じ。出力に「DB 明示(台帳外)」)。
  - 既存の DB(`meta` が無い)は、**初回の起動で、解決したセッション名を記録して通す**(ただし 1 度だけ。人が `--migrate` で明示的に記録するのが安全。P23)。
  - 限界: `meta` は DB の中の文字列なので、同じ OS ユーザーが書き換えられる(14.6 章)。**事故の防止であり、攻撃の防御ではない**。

### 14.5 既存の実機の移行

実機の現状(2.1 章): 開発 checkout の DB が default、派生の checkout B は別 DB で稼働、staging 用 clone は DB 未作成、旧配置のディレクトリ(C)は開発 checkout の DB への symlink。

- **推奨の混成では、既存の DB を動かさない**(移動が要らない)。
  - **default**: 開発 checkout の `taskboard/data/tasks.db` のまま。案 2 の「セッション別ディレクトリ」は、**default だけ従来の場所を使う**(互換。P20 の混成の既定)。他のセッションは `taskboard/data/<名>/tasks.db`。
  - **派生(B)**: 台帳の checkout 列で、**今の 派生の checkout B を指す**(コードが独立した派生で、上流より先行しているため、案 1 のまま)。DB は動かさない。
  - **staging**: staging 用 clone(コードの検証用なので別 clone)。DB は未作成なので、そのまま。
  - 追加するのは、各 DB の `meta`(初回起動で記録、または `--migrate` で明示)と、台帳だけ。
- **DB の移動が要る場合**(派生の B を案 2 に畳む、など。**推奨ではない**。派生コードを失うため):
  1. その DB を使う job と agent を止める(scheduler の job を `bootout`、セッションを `stop`)。DB への書き込みが無いことを確かめる。
  2. `sqlite3 <旧 DB> ".backup <新 DB>"` で新しい場所へ(`cp` ではなく `.backup`。WAL の有無で壊れない。WAL かは未確認)。
  3. 新しい DB の `meta` にセッション名を記録し、`PRAGMA integrity_check` と、タスク・agent の件数を旧と比べる。
  4. 台帳を更新して、新しい場所を指す。**旧 DB は消さず、読み取り専用にして残す**(戻し用)。
  5. job とセッションを再開する。
  - 戻し方: 台帳を旧に戻し、旧 DB の読み取り専用を外す。**移動後に新しい DB に入ったデータは旧に戻らない**ので、戻す判断は早く(再開前の確認の間だけ可能)。
  - 旧配置のディレクトリ(C)の symlink(`taskboard/data` → 開発 checkout)は、**別名のまま台帳に登録しない**(3 章)。
- 全セッションを案 2 の 1 checkout に統合する場合のみ、DB 移動と設定の分割が大きな作業になる。**推奨しない**(14.2 章の「コードの版を分けられない」が、staging の目的とぶつかる)。

### 14.6 同じ OS ユーザーでの隔離の限界

- SQLite の DB はファイルで、認証が無い。同じ OS ユーザーの agent は、**パスを知っていれば別セッションの DB を開ける**(読み書きとも)。`meta` の照合・台帳・`--session` の制限は、どれも**同じユーザーが回避できる柵**(6 章と同じ性質)。
- 案 1 でも案 2 でも、**ファイルの権限(0600 など)では守れない**(同じユーザーのため)。案 2 の隔離は、「普通の操作で取り違えない」「設定の置き忘れが穴にならない」ことであって、**悪意のある同ユーザーの agent への防御ではない**。
- 強い隔離(研修用など)が要るなら、**別の OS ユーザー・別のホーム**(P9)。この場合、台帳・選択・DB・設定が OS の権限で分かれる。論理分離(案 2)はその代わりにならない。
- 案 2 の弱点: 1 つの checkout に全セッションの DB・設定が**並ぶ**ので、1 つの `ls` で全セッションが見える。案 1 は、別のディレクトリを開かない限り見えない(ただし同じユーザーなら `ls ~/developer` で見える)。**差は小さい**。

### 14.7 推奨と conductor の混成案への評価

- **混成を推奨する**(P20)。評価:
  - **既定を案 2、コードの版を分けたいセッションだけ別 clone**という分け方は、14.2 章の表で「コードの版を分けられるか」が決め手になる観点と合う。マージ前のブランチの検証(staging の本来の目的)と研修用のコード固定は、案 2 では**できない**ので、別 clone の逃げ道は必須。
  - 台帳の checkout 列は、**空なら今の checkout(PATH の `tb` が属する checkout)**とする(3 章の台帳の「checkout は必須」を、空を許す形に変える)。空のセッションは**振り分けない**(env と DB パスの切り替えだけ)。列があり、**今の checkout と違う**ときだけ、4 章の `exec` による振り分けが働く。**両方の経路(切り替えだけ・振り分け)が要る**ので、実装は案 1 だけ・案 2 だけより少し大きい。
  - 同じ台帳の中で、**セッションごとに「同じ checkout・別ディレクトリ」と「別 clone」が混ざる**ので、`tb sessions --check` は、(a) 同じ checkout を指す複数のセッションは DB のパスが別であること、(b) 別 clone のセッションは、その clone の `tb` が新しい版であること(3 章)を検査する。
  - 注意: 混成では、**同じ checkout を共有するセッションは、コードの版が同じ**(上げれば全部上がる)。デフォルトの案 2 のセッション同士が 1 つのコードに揃うことは、**利点と制約が表裏**(PATH の `tb` の checkout を更新したときの影響が、台帳の checkout 列が空の全セッションに及ぶ)。更新の前に `tb sessions --migrate`(一括)と `--check` を人が行う手順を、移行の手順に入れる。
- **段階案への影響**: S1〜S3(whoami・台帳・`herdr_call`)は変わらない。S4 の振り分けは「checkout が違うときだけ」に条件が付く。**案 2 の分(DB・設定・roles・ログ・`webui/run` のセッション別化)は、S4 とは独立の大きな別段階(S8 とする)**で、既存の default の場所を変えない互換を受け入れ条件にする。先に S1〜S5 を進めても、案 1(今の clone 運用)のまま台帳と `--session` が使える。
