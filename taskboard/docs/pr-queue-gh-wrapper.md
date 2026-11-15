# pr-queue の実行者の認証(gh ラッパー)

`tb pr-queue run` / `scan` が呼ぶ `gh` は、**人が用意した専用のラッパー**にします。agent のセッションには書き込みの認証を持たせません。

## 指定の仕方(必須)

次のどちらかに、ラッパーの**絶対パス**を書きます。優先は 1 → 2。

1. 環境変数 `PR_QUEUE_GH`(scheduler のジョブの `COMMAND` の先頭に `PR_QUEUE_GH=/path/to/wrapper` と書く、など)
2. `config/pr-queue.conf` の `gh = /path/to/wrapper`(人だけが編集。`pr-queue.conf.example` を写す)

**未指定、絶対パスでない、実行できないときは、`run` も `scan` も何もせず警告して終了コード 1 で止まります(fail-closed)。** 以前は未指定だと PATH 上の `gh` を使っていました。launchd のジョブの PATH には `~/bin` が無いので、ghtkn を通らない `gh` が黙って選ばれていたためです。承認済みの行はそのまま残り、指定すれば次の回に進みます。

## ラッパーの形(例)

許可する呼び出しは `pr create` と `pr list` だけ。所有者ごとの専用 app(draft PR の作成と重複確認に足りる最小権限。merge・approve を含めない)を `ghtkn exec` で固定します。名前は架空です。

```bash
#!/usr/bin/env bash
set -u
REAL_GH=/opt/homebrew/bin/gh                 # 絶対パス。環境変数で差し替えさせない
EMPTY_GH_CONFIG=~/.config/pr-queue-gh-empty  # 人が作る空のディレクトリ(保存済みの gh 認証へ落ちない想定。効果は未確認)
case "${1:-} ${2:-}" in
  "pr create"|"pr list") ;;
  *) echo "pr-queue-gh: 許可外の操作: ${1:-} ${2:-}" >&2; exit 64 ;;
esac
repo=""; prev=""
for a in "$@"; do [ "$prev" = --repo ] && repo=$a; prev=$a; done
case ${repo%%/*} in                          # 所有者 → 専用 app(人が管理する表)
  example-owner) app=example-owner/pr-write ;;
  *) echo "pr-queue-gh: 所有者に対応する app が無い" >&2; exit 64 ;;
esac
unset GH_TOKEN GITHUB_TOKEN
export GH_CONFIG_DIR=$EMPTY_GH_CONFIG
ghtkn exec -e "GH_TOKEN:$app" -- "$REAL_GH" "$@"
rc=$?
[ "$rc" = 111 ] && { echo "pr-queue-gh: auth-locked(ghtkn が token を返せない)" >&2; exit 75; }
exit "$rc"
```

置き場は人が管理し、agent が書けない場所にします(agent が書けると、認証の迂回に使えます)。このリポジトリはラッパー自体を含みません。

注: `GH_CONFIG_DIR` を空のディレクトリにすると保存済みの `gh auth login` の認証へ落ちない、は**未確認の推測**です。使う前に、人が実機で確かめてください。

## 認証で止まったとき(終了コード 75)

`ghtkn exec` は token を取れないと、コマンドを実行せず終了コード 111 を返します(ロック中・期限切れ)。ラッパーが 111 を **75** に読み替えると、pr-queue は次のように扱います。

- **attempts を増やさない**(認証が戻るまで `failed` にならない)。`last_error` は `auth-locked`
- その回の残りの行は処理しない(次の回に自動で再開)
- 通知(`tb notify` が on のとき)を、**状態が変わったときだけ 1 回**積む(止まっている間は run のたびに積まない。回復してから再び止まったときに、また 1 回)
- 人が対話端末で `ghtkn agent unlock --enable-refresh`(ロック中)か `ghtkn auth <app>`(期限切れ)。迂回(別の token、`GH_TOKEN` の直書き、PATH 上の `gh`)はしない

75 以外の失敗(1 など)は従来どおり attempts を使い、3 回で `failed` になります。

## failed から戻す

`tb pr-queue retry <PQ-n>`(**人だけ**。agent の pane からは拒否)。`failed` の行を `approved` に戻し、attempts を 0 にします。ガードは次の `run` でかけ直します。`failed` 以外は戻せません。

## 差分のガード(必須)

`PR_QUEUE_REPO_DIR`(ローカルの clone のパス)が空・絶対パスでない・存在しない・git のリポジトリでないときは、差分を見られないので **`approve` も `run` も `scan` も通しません**(以前は空だと差分を見ずに素通りしていました)。`run` は環境の不備で承認済みの行を却下せず、警告して終了コード 1 で止まります。
