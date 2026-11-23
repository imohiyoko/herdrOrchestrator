#!/bin/bash
# 使い方: bin/serve.sh   -> Web UI を動かす(中身は Go の herdrctl webui serve。ここは場所を渡して置き換わるだけ)
# 1 プロセスで全 herdr セッションの画面を返す(/s/<セッション名>/。ポートは既定 8800、使用中なら +1 ずつずらす)。
# herdrctl: WEBUI_HERDRCTL > TB_HERDRCTL > PATH。launchd から動くときは PATH が決まった値なので、~/.local/bin と ~/go/bin も最後に探す。
# 設定は環境変数(README 参照)。
set -eu
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HC=${WEBUI_HERDRCTL:-${TB_HERDRCTL:-}}
if [ -z "$HC" ]; then
  HC=$(PATH="$PATH${HOME:+:$HOME/.local/bin:$HOME/go/bin}" command -v herdrctl 2>/dev/null) || HC=""
fi
if [ -z "$HC" ] || ! command -v "$HC" >/dev/null 2>&1; then
  echo "herdr-webui: herdrctl が見つかりません(go install ./cmd/herdrctl で入れるか、WEBUI_HERDRCTL で場所を指定)" >&2
  exit 2
fi
case $("$HC" help 2>&1 </dev/null || true) in
  *"webui serve"*) ;;
  *) echo "herdr-webui: herdrctl が古く、Web UI(webui serve)がありません(go install ./cmd/herdrctl で入れ直す): $HC" >&2; exit 2;;
esac
export WEBUI_ROOT="$ROOT"   # static/ はこの checkout のものを読む
exec "$HC" webui serve
