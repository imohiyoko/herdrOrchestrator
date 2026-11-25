#!/usr/bin/env bash
# すべての部品のテストを流す。どれかが失敗したら終了コード 1
#   tests/run-all.sh                 すべてを順に流す
#   tests/run-all.sh -j 4            すべてを 4 本ずつ並列に流す
#   tests/run-all.sh <組> ...        指定した組だけ流す(CI は組ごとに別のジョブで流す)
#   tests/run-all.sh --list          組の名前を JSON の配列で出す
#   tests/run-all.sh --affected [REF]  REF(既定 origin/main)から変えたファイルに関わる組だけ流す(決め方は tests/affected.sh。Go のテストは含まない)
# 組: misc(tests/ の小さなもの)/ taskboard-<組>(taskboard/test/run.sh --list-shards の組。組の中もケースを並列に流す。herdrctl が要る: TB_HERDRCTL)/ webui / scheduler / setup / repoindex
#   taskboard-only:<ケース>,...  tb のケースをそれだけ流す(tests/affected.sh が、ケースだけを変えたときに出す。--list には出ない)
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
    misc)        printf '%s\n' "bash tests/test_env.sh" "bash tests/test_instance.sh" "bash tests/test_affected.sh";;
    taskboard-only:*) echo "bash taskboard/test/run.sh ${1#taskboard-only:}" | tr , ' ';;
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

# taskboard-only:<ケース>,... の形か(ケースは taskboard/test/cases にあるファイル名だけ)
valid_only() {
  local c; [[ $1 == taskboard-only:?* ]] || return 1
  IFS=, read -ra cs <<<"${1#taskboard-only:}"
  for c in "${cs[@]}"; do [[ $c =~ ^[A-Za-z0-9_.-]+\.sh$ && -f $ROOT/taskboard/test/cases/$c ]] || return 1; done
}
jobs=1 sel=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --list) suites | jq -Rnc '[inputs]'; exit 0;;
    --affected)
      ref=origin/main; [[ -n ${2-} && $2 != -* ]] && { ref=$2; shift; }; shift
      plan=$(bash "$ROOT/tests/affected.sh" --base "$ref") || exit 2
      echo "変えたファイルから決めた組: $(jq -c .suites <<<"$plan")$(jq -r 'if .full then "(全部: " + (.reasons | join(", ")) + ")" else "" end' <<<"$plan")"
      jq -r 'if (.go | length) > 0 then "Go のテスト(別に流す): go test " + (.go | join(" ")) else empty end' <<<"$plan"
      mapfile -t aff < <(jq -r '.suites[]' <<<"$plan")
      ((${#aff[@]})) || { echo "流す組がありません"; exit 0; }
      sel+=("${aff[@]}");;
    -j) [[ ${2-} =~ ^[1-9][0-9]*$ ]] || { echo "run-all.sh: -j は 1 以上の整数" >&2; exit 2; }; jobs=$2; shift 2;;
    -*) echo "run-all.sh: 不明な引数 $1" >&2; exit 2;;
    *) [[ $'\n'$(suites)$'\n' == *$'\n'"$1"$'\n'* ]] || valid_only "$1" || { echo "run-all.sh: 組がありません: $1(--list で一覧)" >&2; exit 2; }; sel+=("$1"); shift;;
  esac
done
((${#sel[@]})) || mapfile -t sel < <(suites)
# taskboard の組は中でも CPU の数だけ並列に流すので、組を並列に流すときは 1 組あたりの本数を割る
if ((jobs > 1)) && [[ -z ${TB_TEST_JOBS-} ]]; then
  ncpu=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2); export TB_TEST_JOBS=$(( ncpu / jobs > 0 ? ncpu / jobs : 1 ))
fi

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
