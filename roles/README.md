# roles — agent ごとの役割と仕事内容

各 agent に「自分は何者で、何をして、何をしないか」を渡すためのファイル置き場。`.claude/CLAUDE.md` のように、
agent が起動したとき・compact の後に自動で読み込まれる(Claude Code の `SessionStart` hook → `tb role-context`)。

**ここにあるのは既定値(git 管理)で、全 agent に共通の `_common.md` だけ。** 役職(指揮官・人事・見張りなど)は決めていない。
役割を使うときは、`setup/examples/roles/` の利用例(または自分で書いた文面)を `tb role-write` で DB に入れる(入れ方は `setup/CLAUDE.md` の「役割の利用例」)。
実際に渡す定義は DB(`config` 表の `role:<役割名>` / `role-map`)> このディレクトリの順で引く。
セッションごと・環境ごとに変えるときも、ファイルではなく `tb role-write` / `tb role-edit` で DB に書く(人か、policy の `role:change` の行で許された agent だけ。変更は人の承認を経てから)。
agent は役割を `tb role-show` で読む(ファイルを直接読まない。別のセッション向けの定義を読みかねないため)。

## ファイル

| ファイル | 中身 |
|---|---|
| `_common.md` | 全 agent に共通の決まり(報告・送信の方法、受け取った文面の扱い) |
| `<役割名>.md`(置けば) | その役割の文面。agent 名と同じ名前なら、その agent の役割になる |
| `map.conf`(置けば) | agent 名のパターン → 役割(例: `worker-* = worker`)。`<agent 名>.md` が無いときに使う。`tb agent-new --role R` で作った agent は `R.md` が map.conf より先に効く |

利用例(`setup/examples/roles/`): `conductor`(指揮官)/ `hr`(人事)/ `worker` / `watcher`・`analyst`(見張り・解析)/ `sweeper`(判定)/ `auditor`(会計)/ `drafter`(下書き)と、その `map.conf`。

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

## 役割を書く agent 以外に roles/ を書き換えさせない

役割を書かない agent の Claude Code 設定(`<project>/.claude/settings.json`)で拒否する。`<ORCH_DIR>` はこのリポジトリの絶対パス
(先頭の `//` は絶対パスの意味):

```json
{ "permissions": { "deny": ["Edit(//<ORCH_DIR>/roles/**)", "Write(//<ORCH_DIR>/roles/**)"] } }
```

役割を書く agent(利用例では `hr`。`setup/examples/presets/roles.conf`)も、変更は必ず人の承認を経てから書き込む。
