# モデルの切り替えの検証

`tb model-set`(モデルの切り替え)と `role-models`(役割の既定)を、どう確かめるかの手順。
ユーザー単位の既定を変えないこと、本番の agent を使わないことを前提にする。

## 自動テスト(疑似 herdr)

`taskboard/test/cases/98_model_set.sh`(`bash taskboard/test/run.sh --shard agents`)。本物の herdr・claude には触れない。

| 確かめること | テスト |
|---|---|
| 復活の引数(`--resume <session> --model <モデル>`)と、pane を閉じてからの復活 | `model-set: pane closed, revived …` |
| 台帳のモデルの更新 | `model-set: ledger model updated` |
| ユーザー単位の設定ファイルが変わらない(ハッシュ) | `model-set: user-level settings file unchanged (hash)` |
| `--dry-run` は計画だけ | `model-set --dry-run: plan only` / `--all --dry-run` |
| `--all` は人が実行しても承認待ち | `model-set --all: held for approval …` |
| 実行した本人と指揮官を外す | `caller excluded` / `conductor excluded` |
| working の agent は飛ばす(理由つき) | `working agent skipped with the reason` |
| 対応していない kind は `not supported`(終了コード 12)で何も変えない | `unsupported kind → …` |
| `agent:model:<名前>` の既定 ask、allow の規則、`@役割` の規則、承認後の実行 | `approval: …` / `agent-approve runs the model switch` |
| 決まり方の順(明示 > 台帳 > 役割の既定 > CLI の既定)と `--reset` | `precedence: …` / `model-set --reset: …` |

## 本物での確認(claude の kind、使い捨ての agent)

前提: `tb agent-new` が安定して動くこと(起動に失敗したときの理由は `spawn-errors.log` と出力に出る。issue #83)。
本番の agent ではなく、使い捨ての agent で行う。

```bash
# 1. ユーザー単位の設定のハッシュを控える
H0=$(shasum ~/.claude/settings.json)

# 2. モデルを明示して、使い捨ての agent を作る
tb agent-new mscheck --model haiku --prompt 'tb whoami を実行して終わってください'

# 3. 切り替える(idle / done になってから)
tb model-set mscheck sonnet --dry-run
tb model-set mscheck sonnet

# 4. 新しいモデルを、agent 自身の表示で確かめる
#    pane の見出し行(起動直後の画面)にモデル名が出る。agent に /status を送って確かめてもよい
tb peek mscheck            # 直近の画面
# 5. 台帳のモデルの列
tb agents --json | jq -r 'select(.name=="mscheck") | .model'   # sonnet

# 6. ユーザー単位の設定が変わっていない
[ "$H0" = "$(shasum ~/.claude/settings.json)" ] && echo unchanged

# 7. 片付け
tb agent-stop mscheck --force
```

注意: 切り替えた直後の 1 回は、プロンプトキャッシュが使えないため会話の全体を読み直す。その回の費用(トークン数)を控えておく。

## ほかの kind

codex / gemini は、いまは `not supported`(`lib/kinds.sh` の `kind_model_switch`)。
次のことは確かめるまで分からない、として扱う:

- 起動の引数でモデルを渡せるか、会話の中のコマンドで変えるか
- 変えたモデルが、その会話・そのユーザーの既定として残るか
- 同じ会話を再開できるか(`resume`)

確かめたら、`kind_model_switch` に方法を足し、このファイルと `docs/agent-kinds.md` の表を更新する。
