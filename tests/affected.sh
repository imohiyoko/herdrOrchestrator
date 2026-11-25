#!/usr/bin/env bash
# 変えたファイルから、流すテストを決める(CI の plan ジョブと、手元の run-all.sh --affected が使う)
#   git diff --name-only --no-renames A B | tests/affected.sh    変えたファイル(1 行 1 つ)を標準入力で渡す
#   tests/affected.sh --base origin/main                         REF...HEAD と、まだコミットしていない変更から
#   tests/affected.sh --all                                      すべて(main への push・手で流すとき)
#   tests/affected.sh --ci <イベント>                             CI の plan ジョブ用。pull_request ならマージ後のコミット(HEAD)と
#                                                                 1 つ目の親(main)の差、ほかのイベントは全部。$GITHUB_OUTPUT に
#                                                                 suites(JSON)・go(空白区切り)・snapshot(true/false)を書き、$GITHUB_STEP_SUMMARY に要約
# 出力は JSON 1 行:
#   {"full":真偽,"reasons":[全部流す理由],"suites":[run-all.sh の組],"go":[go vet / go test に渡すパッケージ],"snapshot":真偽}
#   suites の taskboard-only:<ケース>,... は tb のケースをそれだけ流す組(run-all.sh が受ける)
#   go は ./internal/x の形。すべてなら ["./..."]。snapshot は配布物のビルド(go.yml の snapshot)を流すか
#
# 決め方(迷ったら全部流す側に倒す):
#   - GLOBAL のファイル(CI・この仕組み・go.mod など)が変わった、どの規則にも当たらないファイルがある、
#     Go のパッケージの一覧が取れない → 全部
#   - 組は、下の suite_paths のパスに当たるか、suite_go の Go パッケージ(とその依存)のコードが変わったら流す
#   - Go のテストは、テストの依存(go list -test -deps)に変わったパッケージを含むものだけ流す。
#     *_test.go・testdata/ だけの変更は、そのパッケージのテストだけ(依存する側には広げない)
#   - tb のケース(taskboard/test/cases/NN_*.sh)だけの変更は、そのケースだけ流す
# 組やテストが新しくファイルを読むようになったら、ここの規則に足す(足し忘れると PR では流れず、main で初めて落ちる)
set -uo pipefail
set -f   # 規則のパスを引用符なしで渡すので、glob を展開しない
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 1

# ---------- 規則
# パスは前方一致。/ で終わるものはディレクトリ、* を含むものは glob
# 変わったら全部流すもの
GLOBAL=(.github/workflows/test.yml .github/workflows/go.yml .github/actions/ tests/run-all.sh tests/affected.sh go.mod go.sum .gitattributes)
# どのテストも読まないもの
IGNORE=(docs/ .claude/ LICENSE README.md herdr-up.sh conductor/ taskboard/docs/ taskboard/README.md webui/README.md setup/README.md
  .github/workflows/release.yml)
# 組ごとの、読むファイル(パスの前方一致)
suite_paths() {
  case $1 in
    misc)      echo tests/test_env.sh tests/test_instance.sh tests/test_affected.sh lib/ taskboard/bin/ taskboard/schema/ \
                    webui/bin/gen-plist.sh webui/bin/gen-gateway-plist.sh scheduler/bin/gen-plist.sh;;
    taskboard) echo taskboard/bin/ taskboard/lib/ taskboard/schema/ taskboard/sql/ taskboard/perms/ taskboard/config/ taskboard/.gitignore \
                    taskboard/test/run.sh taskboard/test/fake-herdr taskboard/test/groups taskboard/test/weights \
                    taskboard/test/cases/99_zzz_session_sweep.sh .gitignore roles/ setup/examples/ conversation/ lib/ \
                    scheduler/bin/show.sh scheduler/bin/lib.sh;;
    webui)     echo webui/ roles/ conversation/ setup/examples/ scheduler/ lib/ \
                    taskboard/bin/ taskboard/lib/ taskboard/schema/ taskboard/sql/ taskboard/perms/ taskboard/config/;;
    scheduler) echo scheduler/ lib/ taskboard/bin/ taskboard/schema/ taskboard/test/fake-herdr;;
    setup)     echo setup/ presets/ lib/ taskboard/schema/;;
    repoindex) echo repoindex/;;
  esac
}
# 組が動かす herdrctl の Go パッケージ。go:<pkg> はそのパッケージと依存、pkg:<pkg> はそのパッケージだけ
# (cmd/herdrctl は全部を import するが、組が呼ぶのは tb・sched・webui の口だけ)
suite_go() {
  case $1 in
    misc|taskboard) echo go:internal/tbcli pkg:cmd/herdrctl;;
    webui)          echo go:internal/webui go:internal/tbcli pkg:cmd/herdrctl;;   # webui は tb も呼ぶ
    scheduler)      echo go:internal/sched go:internal/tbcli pkg:cmd/herdrctl;;   # scheduler も tb を呼ぶ
  esac
}
# Go のテストが、自分のディレクトリの外から読むファイル(パッケージ: パスの前方一致 ...)
GO_EXTRA=(
  "internal/webui: webui/ taskboard/sql/ scheduler/ internal/tbcli/request.go"
  "internal/kernel/perms: internal/*.go"   # internal/*/*.go を読んで確かめる
)
# Go のコードを文字として読む tb のケース(internal/ の .go が変わったら、tb の組を流さないときもこれだけ流す)
TB_SOURCE_CASES=(83_parts.sh 99_layout.sh)
SUITE_ORDER=(misc taskboard webui scheduler setup repoindex)

# ---------- 引数
mode=stdin base= event=
while [[ $# -gt 0 ]]; do
  case $1 in
    --all) mode=all; shift;;
    --ci) [[ -n ${2-} ]] || { echo "affected.sh: --ci にはイベント名が要る" >&2; exit 2; }; mode=ci event=$2; shift 2;;
    --base) [[ -n ${2-} ]] || { echo "affected.sh: --base には REF が要る" >&2; exit 2; }; mode=base base=$2; shift 2;;
    -h|--help) sed -n '2,20p' "$0"; exit 0;;
    *) echo "affected.sh: 不明な引数 $1" >&2; exit 2;;
  esac
done

# CI: 決めた結果を GitHub Actions の出力に書く(自分を呼び直して、結果の JSON を受ける)
if [[ $mode == ci ]]; then
  if [[ $event == pull_request ]]; then
    if d=$(git diff --name-only --no-renames HEAD^1 HEAD 2>/dev/null); then plan=$(bash "$0" <<<"$d")
    else plan=$(bash "$0" --all | jq -c '.reasons = ["PR の変更が取れない(git diff HEAD^1 HEAD)"]'); fi
  else plan=$(bash "$0" --all | jq -c --arg e "$event" '.reasons = ["\($e) では全部流す"]'); fi
  [[ -n $plan ]] || exit 1
  echo "$plan"
  {
    echo "suites=$(jq -c .suites <<<"$plan")"
    echo "go=$(jq -r '.go | join(" ")' <<<"$plan")"
    echo "snapshot=$(jq -r .snapshot <<<"$plan")"
  } >>"${GITHUB_OUTPUT:-/dev/null}"
  jq -r '"### 流すテスト\n\n" + (if .full then "全部(" + (.reasons | join(", ")) + ")" else "変えたファイルに関わるものだけ" end)
    + "\n\n- 組: " + (if (.suites | length) > 0 then (.suites | map("`" + . + "`") | join(" ")) else "なし" end)
    + "\n- Go: " + (if (.go | length) > 0 then (.go | map("`" + . + "`") | join(" ")) else "なし" end)
    + "\n- 配布物のビルド: " + (if .snapshot then "する" else "しない" end)' <<<"$plan" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"
  exit 0
fi

reasons=()
files=()
case $mode in
  all) reasons+=("すべて流す指定");;
  base)
    if ! out=$( { git diff --name-only --no-renames "$base...HEAD" && git diff --name-only --no-renames HEAD && git ls-files --others --exclude-standard; } 2>&1); then
      reasons+=("git diff が取れない: $base")
    else mapfile -t files < <(printf '%s\n' "$out" | sed '/^$/d' | sort -u); fi;;
  stdin) mapfile -t files < <(sed 's/\r$//; /^$/d' | sort -u);;
esac

all_suites() { bash "$ROOT/tests/run-all.sh" --list | jq -c .; }
emit_full() {
  jq -nc --argjson s "$(all_suites)" '{full:true, reasons:$ARGS.positional, suites:$s, go:["./..."], snapshot:true}' --args "$@"
  exit 0
}
((${#reasons[@]})) && emit_full "${reasons[@]}"

has_prefix() {  # $1=ファイル 残り=前方一致のパス
  local f=$1 p; shift
  for p in "$@"; do
    if [[ $p == *'*'* ]]; then [[ $f == $p ]] && return 0   # * を含むものは glob(* は / もまたぐ)
    else [[ $f == "$p" || ( $p == */ && $f == "$p"* ) ]] && return 0; fi
  done
  return 1
}

# ---------- Go のパッケージの一覧と依存
MOD=$(go list -m 2>/dev/null) || emit_full "go list が使えない"
list=$(go list -f '{{.ImportPath}}{{"\t"}}{{.Dir}}' ./... 2>/dev/null) || emit_full "go list が使えない"
declare -A PKG_DIR=()      # ディレクトリ(リポジトリからの相対) → パッケージ(モジュールからの相対)
while IFS=$'\t' read -r ip dir; do
  rel=${ip#"$MOD"}; rel=${rel#/}; PKG_DIR[${dir#"$ROOT"/}]=$rel
done <<<"$list"
((${#PKG_DIR[@]})) || emit_full "Go のパッケージが見つからない"
pkg_of() {  # ファイルを含むいちばん近いパッケージ
  local d=$1
  while [[ $d == */* ]]; do d=${d%/*}; [[ -n ${PKG_DIR[$d]-} ]] && { echo "${PKG_DIR[$d]}"; return 0; }; done
  return 1
}
strip_mod() { sed -E "s# \[[^]]*\]##g" | tr ' ' '\n' | sed -n "s#^$MOD/##p" | sort -u; }
deps_of() { local o; o=$(go list -deps "./$1" 2>/dev/null) || { echo "$1"; return 1; }; sed -n "s#^$MOD/##p" <<<"$o"; }

# ---------- ファイルを分ける
declare -A SUITE=() CHG_SRC=() CHG_TEST=() GO_SEL=() TB_CASES=()
snapshot=false
for f in "${files[@]}"; do
  matched=
  has_prefix "$f" "${GLOBAL[@]}" && emit_full "全体にかかるファイル: $f"
  if pkg=$(pkg_of "$f"); then
    matched=1
    if [[ $f == *_test.go || $f == */testdata/* ]]; then CHG_TEST[$pkg]=1; else CHG_SRC[$pkg]=1; fi
  fi
  for s in "${SUITE_ORDER[@]}"; do
    # shellcheck disable=SC2046
    has_prefix "$f" $(suite_paths "$s") && { SUITE[$s]=1; matched=1; }
  done
  if [[ $f =~ ^taskboard/test/cases/([A-Za-z0-9_.-]+\.sh)$ ]]; then
    TB_CASES[${BASH_REMATCH[1]}]=1; matched=1
  fi
  [[ $f == internal/*.go && $f != *_test.go ]] && for c in "${TB_SOURCE_CASES[@]}"; do TB_CASES[$c]=1; done
  for rule in "${GO_EXTRA[@]}"; do
    # shellcheck disable=SC2086
    has_prefix "$f" ${rule#*:} && { GO_SEL[${rule%%:*}]=1; matched=1; }
  done
  [[ $f == .goreleaser.yaml ]] && { snapshot=true; matched=1; }
  has_prefix "$f" "${IGNORE[@]}" && matched=1
  [[ -n $matched ]] || emit_full "規則に無いファイル: $f"
done

# ---------- Go のコードの変更を、組と Go のテストに広げる
if ((${#CHG_SRC[@]})); then
  snapshot=true
  for s in "${SUITE_ORDER[@]}"; do
    [[ -n ${SUITE[$s]-} ]] && continue
    for r in $(suite_go "$s"); do
      case $r in
        go:*) o=$(deps_of "${r#go:}") || emit_full "go list -deps が使えない: ${r#go:}"; mapfile -t cl <<<"$o";;
        pkg:*) cl=("${r#pkg:}");;
      esac
      for p in "${cl[@]}"; do [[ -n ${CHG_SRC[$p]-} ]] && { SUITE[$s]=1; break 2; }; done
    done
  done
  # テストの依存に変わったパッケージを含むパッケージ(P.test の Deps)
  tlist=$(go list -test -f '{{.ImportPath}}{{"\t"}}{{join .Deps " "}}' ./... 2>/dev/null) || emit_full "go list -test が使えない"
  while IFS=$'\t' read -r ip deps; do
    [[ $ip == *.test ]] || continue
    t=${ip%.test}; t=${t#"$MOD"}; t=${t#/}
    while IFS= read -r d; do [[ -n ${CHG_SRC[$d]-} ]] && { GO_SEL[$t]=1; break; }; done < <(strip_mod <<<"$deps")
  done <<<"$tlist"
  for p in "${!CHG_SRC[@]}"; do GO_SEL[$p]=1; done
fi
for p in "${!CHG_TEST[@]}"; do GO_SEL[$p]=1; done

# ---------- 出す
suites=()
for s in "${SUITE_ORDER[@]}"; do
  if [[ $s == taskboard ]]; then
    if [[ -n ${SUITE[taskboard]-} ]]; then
      mapfile -t -O "${#suites[@]}" suites < <(bash taskboard/test/run.sh --list-shards | sed 's/^/taskboard-/')
    else
      cases=()
      for c in "${!TB_CASES[@]}"; do [[ -f taskboard/test/cases/$c ]] && cases+=("$c"); done   # 消したケースは流せない
      ((${#cases[@]})) && suites+=("taskboard-only:$(printf '%s\n' "${cases[@]}" | sort | paste -sd, -)")
    fi
  elif [[ -n ${SUITE[$s]-} ]]; then suites+=("$s"); fi
done
gopkgs=()
for p in "${!GO_SEL[@]}"; do gopkgs+=("./$p"); done
jq -nc --argjson snap "$snapshot" \
  --argjson s "$(printf '%s\n' "${suites[@]}" | jq -Rnc '[inputs | select(. != "")]')" \
  --argjson g "$(printf '%s\n' "${gopkgs[@]}" | sort -u | jq -Rnc '[inputs | select(. != "")]')" \
  '{full:false, reasons:[], suites:$s, go:$g, snapshot:$snap}'
