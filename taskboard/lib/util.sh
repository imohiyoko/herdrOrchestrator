# shellcheck shell=bash
# 土台(ADR 0004): 機能を知らない小さな道具。bin/tb から source される。単体では実行しない。
# どの機能からも呼んでよい。ここから機能のファイルの関数を呼ばない。

# 前後の空白を除く
trim() { local s=$1; s=${s#"${s%%[![:space:]]*}"}; s=${s%"${s##*[![:space:]]}"}; printf '%s' "$s"; }

# 1 行化 + ラベル偽装の無効化 + 先頭 N 字
oneline() {  # $1=本文 $2=最大字数
  printf '%s' "$1" | tr '\r\n\t' '   ' | tr -d '\000-\010\013\014\016-\037' | tr -s ' ' \
    | jq -Rrs --argjson n "$2" 'gsub("\\[\\s*from\\s*[:：]";"(from:";"i") | gsub("^\\s+|\\s+$";"") | .[0:$n]'
}

# 標準入力の SHA-256(16 進 64 文字)
sha256() { if command -v sha256sum >/dev/null; then sha256sum | cut -c1-64; else shasum -a 256 | cut -c1-64; fi; }
