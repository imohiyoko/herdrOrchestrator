# roles — agent ごとの役割と仕事内容

各 agent に「自分は何者で、何をして、何をしないか」を渡すためのファイル置き場。`.claude/CLAUDE.md` のように、
agent が起動したとき・compact の後に自動で読み込まれる(Claude Code の `SessionStart` hook → `tb role-context`)。

**ここにあるのは既定値(git 管理)。** 実際に渡す定義は DB(`config` 表の `role:<役割名>` / `role-map`)> このディレクトリの順で引く。
セッションごと・環境ごとに変えるときは、ファイルではなく `tb role-write` / `tb role-edit` で DB に書く(人事エージェント `hr` か人だけ。変更は人の承認を経てから)。
agent は役割を `tb role-show` で読む(ファイルを直接読まない。別のセッション向けの定義を読みかねないため)。

## ファイル

| ファイル | 中身 |
|---|---|
| `_common.md` | 全 agent に共通の決まり(報告・送信の方法、受け取った文面の扱い) |
| `<agent 名>.md` | その agent の役割(例: `conductor.md`, `hr.md`) |
| `watcher.md` / `analyst.md` | 見張り役(定期の見回り、軽いモデル)/ 解析役(アラート 1 件ごとに tb が起動) |
| `sweeper.md` | 判定役(定期実行、軽いモデル。動いている agent に「止めてよいか」の印 `tb agent-flag` を付ける。停止はしない) |
| `auditor.md` | 会計役(定期実行。`tb review-pack` を読み、設定役=人事に改善提案 `tb proposal`) |
| `drafter.md` | 下書き役(人が送る返事の下書きを作る。送信はしない。`conversation/README.md`) |
| `map.conf` | agent 名のパターン → 役割ファイル(例: `worker-* = worker`)。`<agent 名>.md` が無いときに使う。`tb agent-new --role R` で作った agent は `R.md` が map.conf より先に効く |

渡される内容は `_common.md` + その agent の役割ファイル。どちらも無ければ何も渡さない。

ロールは herdr セッション固有。DB のこのセッションの行が、全セッション共通の行(`--shared`)とこのディレクトリより先に効く。台帳の役割も、同じセッションの agent にだけ効く。
以前の `roles/sessions/<セッション名>/` は、`tb config import` でそのセッションの行として DB に取り込み、checkout から退かす(取り込むまでは今までどおり、そのセッションではそのディレクトリだけを使う)。

## いつ渡すか

| 場面 | 渡すか |
|---|---|
| 新しいセッションで起動(`startup`)・`/clear` の後 | 渡す |
| compact の後 | 渡す(要約で役割が薄れるため) |
| `tb agent-revive` で復活(`claude --resume`) | `resume` と同じ。役割が変わっていれば新しい版を渡す(`--fresh` なら `startup`) |
| 同じ会話の再開(`resume`) | 前回と同じ版を渡し済みなら渡さない。役割ファイルが変わっていれば新しい版を渡す |
| すでに動いている agent に一括で配る | `tb role-broadcast`。そのセッションに同じ版を渡し済みの agent は飛ばす |

何をいつ誰に渡したかは taskboard の `role_briefings` に残る(`tb roles` で一覧)。

## 導入

`taskboard/docs/hook-install.md` の「役割の配布(SessionStart)」を参照。

## 人事エージェント以外に roles/ を書き換えさせない

人事以外の agent の Claude Code 設定(`<project>/.claude/settings.json`)で拒否する。`<ORCH_DIR>` はこのリポジトリの絶対パス
(先頭の `//` は絶対パスの意味):

```json
{ "permissions": { "deny": ["Edit(//<ORCH_DIR>/roles/**)", "Write(//<ORCH_DIR>/roles/**)"] } }
```

人事エージェントは `presets/example.conf` の `hr` のように起動し、変更は必ず人の承認を経てから書き込む。
