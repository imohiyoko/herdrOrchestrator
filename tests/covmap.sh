#!/usr/bin/env bash
# tb のユニットごとに、通った herdrctl の関数を記録する(tests/affected.sh が、Go の関数を変えた PR で tb のケースを選ぶのに使う)
#   tests/covmap.sh run <出力>      -cover の herdrctl を作り、tb のテストを全部流して、記録(JSON)を <出力> に書く。失敗したら書かない
#   tests/covmap.sh build <ディレクトリ> <コミット>   run.sh の TB_TEST_COVER_DIR にためたものから JSON を標準出力に出す
# JSON: {"version":1,"commit":"<作ったコミット>","units":["<ユニットの先頭のケース>",...],"funcs":{"<ファイル>:<関数>":["<ユニットの先頭のケース>",...]},
#        "files":{"<ファイル>":["<ユニットの先頭のケース>",...]}}
#   funcs: 関数の名前は go tool covdata func の形(f、T.m、*T.m)。1 文でも通った関数だけ入れる
#   files: Go でないのに、どのユニットが読むかを記録するファイル(今は taskboard/lib/help.txt だけ: tb help で読んだユニット
#          (bin/tb が印を付ける)と、ケースのファイルに名前を書いたユニット)
# CI では main への push のたびに作り、キャッシュに置く(test.yml の covmap)。PR の plan は main の記録を使う
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 1

build() {  # $1=ためたディレクトリ $2=コミット
  local dir=$1 commit=$2 mod u first n
  mod=$(go list -m) || return 1
  mapfile -t units < <(bash taskboard/test/run.sh --units | awk '{print $1}')
  ((${#units[@]})) || { echo "covmap: ユニットの一覧が取れない" >&2; return 1; }
  for u in "${units[@]}"; do   # 流れなかったユニットがあれば作らない(記録の漏れは、選ぶケースの漏れになる)
    [[ -d $dir/$u ]] || { echo "covmap: 記録が無いユニット: $u" >&2; return 1; }
  done
  local help
  help=$(bash taskboard/test/run.sh --units | while read -r -a cs; do
    if [[ -e $dir/${cs[0]}/read-help.txt ]] || (cd taskboard/test/cases && grep -qF help.txt "${cs[@]}" 99_zzz_session_sweep.sh); then echo "${cs[0]}"; fi
  done | jq -Rnc '[inputs]')
  for u in "${units[@]}"; do
    compgen -G "$dir/$u/covcounters.*" >/dev/null || continue   # herdrctl を呼ばないユニット
    go tool covdata func -i "$dir/$u" | awk -v u="$u" -v m="$mod/" '
      $1 != "total:" && $NF != "0.0%" { f = $1; sub(/:[0-9]+:$/, "", f); if (index(f, m) != 1) next; print substr(f, length(m) + 1) ":" $2 "\t" u }' ||
      { echo "covmap: go tool covdata が失敗: $u" >&2; return 1; }
  done | sort -u | jq -Rn --arg c "$commit" --argjson help "$help" --args '
    [inputs | split("\t")] as $r
    | {version: 1, commit: $c, units: $ARGS.positional,
       funcs: (reduce $r[] as $x ({}; .[$x[0]] += [$x[1]])),
       files: {"taskboard/lib/help.txt": $help}}' "${units[@]}"
}

case ${1-} in
  build) [[ $# -eq 3 ]] || { echo "usage: covmap.sh build <ディレクトリ> <コミット>" >&2; exit 2; }; build "$2" "$3";;
  run)
    [[ $# -eq 2 ]] || { echo "usage: covmap.sh run <出力>" >&2; exit 2; }
    tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
    commit=$(git rev-parse HEAD) || exit 1
    go build -cover -coverpkg="$(go list -m)/..." -o "$tmp/herdrctl" ./cmd/herdrctl || exit 1
    mkdir -p "$tmp/cov"
    TB_TEST_COVER_DIR=$tmp/cov TB_HERDRCTL=$tmp/herdrctl bash taskboard/test/run.sh | tail -n 3 || { echo "covmap: tb のテストが失敗した" >&2; exit 1; }
    build "$tmp/cov" "$commit" >"$tmp/map.json" || exit 1
    mv "$tmp/map.json" "$2";;
  *) sed -n '2,7p' "$0" >&2; exit 2;;
esac
