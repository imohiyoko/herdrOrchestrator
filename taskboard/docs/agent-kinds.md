# agent の kind アダプタ

台帳 `agents` の `kind` 列(既定 `claude`)ごとの違いを、`taskboard/lib/kinds.sh` の 1 か所に閉じ込める。
kind が違っても `tb agent-new` / `tb agent-revive --model` のコマンドの形は変えない。

## 1. 洗い出し: Claude 固有の前提(変更前)

| 場所 | 前提 | 扱い |
| --- | --- | --- |
| `lib/transport.sh` `herdr_spawn` | `herdr agent start --kind claude` を決め打ち | kind を引数にし、アダプタに聞く |
| `lib/agents.sh` `do_agent_new` | モデル指定は `--model M`(`MODEL_RE` で検査) | `kind_launch_args` / `kind_model_valid` |
| `lib/agents.sh` `do_agent_revive` | 再開は `--resume <session>`。`--model` を省くと台帳の値を引き継ぐ | `kind_launch_args`。引き継いだ値も kind の検査を通す |
| `lib/agents.sh` `do_agent_restart` | 停止してから `--resume` で復活(閉じた後で失敗すると会話を失う) | pane を閉じる**前**に `resume` を確かめる |
| `lib/agents.sh` `agent_register` / `agent_sync` | kind を渡していない(列の既定値 `claude` 任せ) | 登録時に kind を書く。sync は herdr の `agent` 欄を使う(無ければ claude) |
| `lib/alerts.sh` | 解析役は claude で起動 | 既定 kind を明示して渡す(動作は同じ) |
| `lib/compact.sh` | `/compact` を送り、`~/.claude/projects/*/<session>.jsonl` で文脈の大きさを測る | claude 以外は理由を出してスキップ |
| `lib/spawn.sh` | 子の `model` を `MODEL_RE` で検査 | 変更なし(子は常に既定 kind = claude) |
| `schema/004_agents.sql` | `kind TEXT NOT NULL DEFAULT 'claude'` | **変更なし**(列は既にある) |
| `bin/tb-hook` / `tb touch` | Claude Code の hook(`session_id` など)を読む | 変更なし。hook を持たない kind は活動記録が付かない(下記) |
| `herdr-up.sh` | `herdr agent start --kind claude`、`-- --model` | 変更なし(台帳を通らない起動。範囲外) |
| テスト | `97_agents.sh` / `98_agent_*.sh` が `start ... --kind claude ... -- --resume s1 --model sonnet` を固定で確認 | そのまま通す(claude は 1 文字も変えない) |
| README / help / webui の文言 | 「claude --resume で再開」 | README と help を kind 対応の書き方に。webui の文言は claude の説明として正しいので触らない |

## 2. アダプタが答える項目

`lib/kinds.sh` の関数が答える。呼び出し側は kind の名前で分岐しない。

| 項目 | 関数 | 内容 |
| --- | --- | --- |
| 起動コマンドの組み立て | `kind_herdr_kind` / `kind_launch_args` | `herdr agent start <名前> --kind <herdr の kind> --pane P [-- 引数...]` の `--kind` と `--` 以降 |
| モデル指定の渡し方 | `kind_launch_args` | claude は `--model M`。対応しない kind は引数を作らない |
| モデル名の検証 | `kind_model_valid` | claude は `^[A-Za-z0-9._:-]{1,64}$`(従来の `MODEL_RE`)。体系が違う kind は個別に |
| 再開の可否と方法 | `kind_supports K resume` / `kind_launch_args` | claude は `--resume <session>`。対応しない kind は再開しない |
| 起動中のモデル変更の可否 | `kind_supports K live-model` | 表示のみ。tb は起動中のモデル変更を行わない(変更は復活時の `--model` だけ) |
| session 識別の取り方 | `kind_session_source` | herdr `agent list` の `agent_session.value`(全 kind 共通) |
| compact の可否 | `kind_supports K compact` | `/compact` と claude の transcript に依存する |

対応しない項目の要求は `kind_require K 項目` が止める。

## 3. kind ごとの対応表(`tb agent-kinds` でも見える)

| kind | launch | model | resume | live-model | session | compact |
| --- | --- | --- | --- | --- | --- | --- |
| claude | yes(`--kind claude`) | yes(`--model M`) | yes(`--resume SID`) | yes(`/model`。tb は使わない) | herdr | yes |
| codex | yes(`--kind codex`) | **no** | **no** | no | herdr | no |
| gemini | yes(`--kind gemini`) | **no** | **no** | no | herdr | no |

codex / gemini の「no」は「その CLI に無い」ではなく「herdr 経由の渡し方を tb がまだ確かめていない」。確認済みのことと、未確認のこと:

- codex(手元の `codex --help` / `codex resume --help` で確認済み: モデルは `-m, --model <MODEL>`。再開はフラグではなくサブコマンド `codex resume [SESSION_ID]`。
  つまり claude のように「起動引数に `--resume SID` を足す」形ではない。herdr の `agent start --kind codex -- resume SID` が通るかは未確認。
- gemini: 手元に CLI が無く、ヘルプを確認できていない。推測で対応にしない。

確認できたら `kind_supports` と `kind_launch_args` の 2 か所だけを直し、表とテストを更新する(呼び出し側は変えない)。

## 4. 対応しない項目を求められたときの動作

黙って claude の引数を渡さず、止める。

- 終了コード **12**(「kind が対応していない項目がある」。既存の 0 / 1 / 2 / 4 / 10 / 11 / 20 とは別)
- 標準エラー: `tb: kind=<kind> は <項目> に対応していません item=<項目> ...`(項目は `model` / `resume` / `kind` / `launch`)
- 何も起動せず、台帳にも行を作らない。`agent-restart` は pane を閉じる前に止まる
- `agent-new` / `agent-revive` は承認待ち(`L-<n>`)に積む**前**に止める。承認後の実行(`run_op`)でも同じ検査を通る
- 未知の kind 名(`--kind nosuch`)は引数の誤りとして終了コード 2
- 後方互換: `--kind` を付けない `agent-new` と、台帳の `kind` が `claude` の行は、従来とまったく同じ引数で起動する

`agent-revive --fresh`(新しい会話)は `resume` を使わないので、codex / gemini でも起動できる(`--model` を渡さない限り)。

## 5. 台帳の DB スキーマ

変更しない(migration なし)。`agents.kind` は既にある。

- `agent-new --kind K`: 登録時に `kind` を書く(claude 以外のときだけ `agent_ops.args` に `kind` が入る)
- `agent_sync`: herdr が教える `agent` 欄が既知の kind なら、新規行にも既存行にも反映する(同名で別の kind が外部から起動されたときに、古い kind のまま resume 検査を通さないため)。空・未知の値のときは、既存の行は保持し、新規の行は `claude`
- 古い台帳(`kind` が既定の `claude`)は何も変えずに動く

## 6. 既知の制約(今回の変更の範囲外)

- `tb-hook` / `tb touch` は Claude Code の hook 前提。hook を持たない kind は `agent_activity` が付かず、`killable` の stale 判定や `agent-restart` の活動確認が効かない。
- `tb agents` の表示に kind 列は足していない(出力を変えないため)。`tb agent-kinds` と台帳の `kind` 列で確認する。
- `herdr-up.sh` の起動(台帳を通らない)は claude 固定のまま。
