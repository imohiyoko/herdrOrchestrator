#!/usr/bin/env bash
# tb のテスト。本物の herdr は一切呼ばない(HERDR_BIN を疑似 herdr に固定)。
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
export HERDR_SESSION_FILE=$TMP/no-herdr-session   # 手元の checkout の herdr-session に左右されない
export TB_DB=$TMP/tasks.db TB_CONFIG=$TMP/projects.conf TB_LIMITS=$TMP/limits-default.conf
unset HERDR_SOCKET_PATH   # 実行する pane の socket に左右されない(HSESS は HERDR_SOCKET_PATH を HERDR_SESSION より優先する)
export HERDR_BIN=$HERE/fake-herdr FAKE_LOG=$TMP/herdr.log
TB=$HERE/../bin/tb
# 起動直後の準備待ちは既定では短く、送信確認は切る(98_ready で個別に有効化)
export TB_PROMPT_VERIFY_SEC=0 TB_SPAWN_POLL_SEC=0.05 TB_SPAWN_READY_SEC=0.2 TB_VERIFY_POLL_SEC=0.05
: >"$FAKE_LOG"
pass=0; fail=0
ok()   { pass=$((pass+1)); echo "ok   $1"; }
ng()   { fail=$((fail+1)); echo "FAIL $1"; }
eq()   { [[ "$2" == "$3" ]] && ok "$1" || { ng "$1"; echo "   expected: $3"; echo "   actual:   $2"; }; }
rc()   { "$@" >/dev/null 2>&1; echo $?; }
prompts() { grep -c '^prompt' "$FAKE_LOG"; }

# 各機能のテストは cases/NN_*.sh(taskboard/lib/ のファイルにおおむね対応)。番号順に同じシェルで流す
# --shard <名前>: shards に書いた組だけ流す(CI の並列ジョブ用)。--list-shards: 組の名前を 1 行ずつ出す。--shard-cases <名前>: 組のケースの一覧
SWEEP=99_zzz_session_sweep.sh
shard_names() { sed -n 's/^\([a-z][a-z0-9_-]*\):.*/\1/p' "$HERE/shards"; echo rest; }
shard_cases() {  # $1=組の名前。その組のケースを番号順に(最後に SWEEP)
  local all listed c
  all=$(cd "$HERE/cases" && ls [0-9][0-9]_*.sh | grep -vx "$SWEEP")
  if [[ $1 == rest ]]; then
    listed=$(sed -n 's/^[a-z][a-z0-9_-]*:\(.*\)/\1/p' "$HERE/shards" | tr -s ' \t' '\n\n' | grep .)
    grep -vxF -f <(printf '%s\n' "$listed") <<<"$all" || true
  else
    for c in $(sed -n "s/^$1:\\(.*\\)/\\1/p" "$HERE/shards"); do
      [[ -f $HERE/cases/$c ]] || { echo "shards: ケースがありません: $c" >&2; return 1; }
      echo "$c"
    done | sort
  fi
  echo "$SWEEP"
}
cases=()
case ${1-} in
  --list-shards) shard_names; exit 0;;
  --shard-cases) shard_cases "${2-}"; exit $?;;   # 組に入るケースの一覧(確かめ用)
  --shard) [[ -n ${2-} && " $(shard_names | tr '\n' ' ') " == *" $2 "* ]] || { echo "run.sh: --shard は次のどれか: $(shard_names | tr '\n' ' ')" >&2; exit 2; }
           mapfile -t cases < <(shard_cases "$2") || exit 2
           [[ ${#cases[@]} -gt 1 ]] || { echo "run.sh: 組 $2 にケースがありません" >&2; exit 2; };;
  "") mapfile -t cases < <(cd "$HERE/cases" && ls [0-9][0-9]_*.sh);;
  *) echo "usage: run.sh [--shard <名前> | --list-shards | --shard-cases <名前>]" >&2; exit 2;;
esac
for c in "${cases[@]}"; do
  # shellcheck source=/dev/null
  source "$HERE/cases/$c"
done

echo; echo "pass=$pass fail=$fail"; [[ $fail -eq 0 ]]
