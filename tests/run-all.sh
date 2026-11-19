#!/usr/bin/env bash
# すべての部品のテストを流す。どれかが失敗したら終了コード 1
#   tests/run-all.sh                 すべてを順に流す
#   tests/run-all.sh -j 4            すべてを 4 本ずつ並列に流す
#   tests/run-all.sh <組> ...        指定した組だけ流す(CI は組ごとに別のジョブで流す)
#   tests/run-all.sh --list          組の名前を JSON の配列で出す(CI の matrix 用)
# 組: misc(tests/ の小さなもの)/ taskboard-<名前>(taskboard/test/shards の組)/ webui / scheduler / setup / repoindex
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)

suites() {
  echo misc
  bash "$ROOT/taskboard/test/run.sh" --list-shards | sed 's/^/taskboard-/'
  printf '%s\n' webui scheduler setup repoindex
}
# 組 1 つ分のコマンド(1 行 1 コマンド)
suite_cmds() {
  case $1 in
    misc)        printf '%s\n' "bash tests/test_env.sh" "bash tests/test_instance.sh" "python3 tests/test_webui_queries.py";;
    taskboard-*) echo "bash taskboard/test/run.sh --shard ${1#taskboard-}";;
    webui|scheduler|setup|repoindex) echo "bash $1/tests/run.sh";;
    *) return 1;;
  esac
}
# 組を流す。出力は組ごとのファイルにためて、終わったら失敗の行と最後の行だけ出す
run_suite() {  # $1=組 $2=出力先 $3=要約の出力先(コマンドごとに最後の 1 行)
  local cmd rc=0 one; one=$2.one
  while IFS= read -r cmd; do
    if (cd "$ROOT" && $cmd) >"$one" 2>&1; then :; else rc=1; fi
    { echo "--- $cmd"; cat "$one"; } >>"$2"
    echo "  $cmd: $(tail -n 1 "$one")" >>"$3"
  done < <(suite_cmds "$1")
  rm -f "$one"
  return $rc
}

jobs=1 sel=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --list) suites | jq -Rnc '[inputs]'; exit 0;;
    -j) [[ ${2-} =~ ^[1-9][0-9]*$ ]] || { echo "run-all.sh: -j は 1 以上の整数" >&2; exit 2; }; jobs=$2; shift 2;;
    -*) echo "run-all.sh: 不明な引数 $1" >&2; exit 2;;
    *) [[ $'\n'$(suites)$'\n' == *$'\n'"$1"$'\n'* ]] || { echo "run-all.sh: 組がありません: $1(--list で一覧)" >&2; exit 2; }; sel+=("$1"); shift;;
  esac
done
((${#sel[@]})) || mapfile -t sel < <(suites)

out=$(mktemp -d); trap 'rm -rf "$out"' EXIT
running=0
for s in "${sel[@]}"; do
  ( run_suite "$s" "$out/$s.log" "$out/$s.sum"; echo $? >"$out/$s.rc" ) &
  running=$((running + 1))
  if ((running >= jobs)); then wait -n; running=$((running - 1)); fi
done
wait

failed=()
for s in "${sel[@]}"; do
  echo "=== $s"
  if [[ $(cat "$out/$s.rc") != 0 ]]; then
    failed+=("$s"); grep -E 'FAIL|Error|Traceback' "$out/$s.log" | head -40
  fi
  cat "$out/$s.sum"
done
if ((${#failed[@]})); then echo; echo "失敗: ${failed[*]}"; exit 1; fi
echo; echo "all passed"
