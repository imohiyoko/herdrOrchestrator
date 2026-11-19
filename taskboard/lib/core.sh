# shellcheck shell=bash
# 共通の小道具: エラー終了、入力の検査、タスク行の取得、project ごとの指示役
# bin/tb から source される。単体では実行しない。

DEFAULT_TARGET=conductor
STATUSES="todo in_progress review done blocked"
TYPES="epic story task bug subtask"
PRIORITIES="highest high medium low lowest"   # 上から順に高い
LINK_TYPES="blocks relates duplicates"

die()   { echo "tb: $*" >&2; exit 1; }
usage() { echo "tb: $*" >&2; echo "(tb help で使い方)" >&2; exit 2; }

need_tools() {
  command -v sqlite3 >/dev/null || die "sqlite3 が必要です"
  command -v jq >/dev/null || die "jq が必要です"
}

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

valid_id()      { [[ $1 =~ ^T-[A-Za-z0-9_]+-[0-9]{3,}$ ]] || usage "タスク ID は T-<project>-NNN 形式: $1"; }
valid_project() { [[ $1 =~ ^[A-Za-z0-9_]+$ ]] || usage "project は英数字と _ のみ: $1"; }
valid_status()  { [[ " $STATUSES " == *" $1 "* ]] || usage "status は $STATUSES のどれか: $1"; }
valid_type()     { [[ " $TYPES " == *" $1 "* ]] || usage "種別は $TYPES のどれか: $1"; }
valid_priority() { [[ " $PRIORITIES " == *" $1 "* ]] || usage "優先度は $PRIORITIES のどれか: $1"; }
# ラベルは 1〜40 文字。空白・カンマ・引用符は使えない(日本語は可)
valid_label()    { [[ ${#1} -ge 1 && ${#1} -le 40 && ! $1 =~ [[:space:],\"\'] ]] || usage "ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: $1"; }
# 外部リンク: <種類>=<URL> か URL だけ(種類は slack / github / link を URL から決める)。種類は英小文字・数字・_ - で 20 文字まで
valid_ref_kind() { [[ $1 =~ ^[a-z][a-z0-9_-]{0,19}$ ]] || usage "外部リンクの種類は英小文字で始まる 20 文字までの英数字・_ -: $1"; }
valid_ref_url()  { [[ $1 =~ ^https?://[^[:space:]\"\'\<\>]+$ && ${#1} -le 500 ]] || usage "外部リンクは http(s):// で始まる 500 文字までの URL(空白・引用符・<> なし): $1"; }
valid_sprint()  { [[ $1 =~ ^S-[0-9]+$ ]] || usage "スプリント ID は S-<番号> 形式: $1"; }
valid_points()  { [[ $1 =~ ^[0-9]{1,4}$ ]] || usage "ポイントは 0〜9999 の整数: $1"; }
valid_date()    { [[ $1 =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ && $(sqlite3 :memory: "SELECT date('$1')='$1';") == 1 ]] || usage "日付は YYYY-MM-DD(実在する日): $1"; }
need_arg()      { [[ $# -ge 2 ]] || usage "$1 に値が必要です"; }
blank()         { [[ -z "${1//[[:space:]]/}" ]]; }

task_row() {  # $1=id → JSON 1 行(なければ空)
  session_row tasks id "$1"
}
require_task() { local r; r=$(task_row "$1"); [[ -n $r ]] || die "タスクが見つかりません: $1"; printf '%s' "$r"; }

target_for() {  # $1=project → 指示役の名前
  local t=$DEFAULT_TARGET k v
  if config_has projects; then
    while IFS='=' read -r k v || [[ -n $k ]]; do
      k=${k//[[:space:]]/}; v=${v//[[:space:]]/}
      [[ -z $k || $k == \#* ]] && continue
      [[ $k == "$1" && -n $v ]] && t=$v
    done < <(config_text projects)
  fi
  printf '%s' "$t"
}
