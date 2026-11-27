#!/usr/bin/env bash
# 変えたファイルから、流すテストを決める(CI の plan ジョブと、手元の run-all.sh --affected が使う)
#   git diff --name-only --no-renames A B | tests/affected.sh    変えたファイル(1 行 1 つ)を標準入力で渡す
#   tests/affected.sh --base origin/main                         REF...HEAD と、まだコミットしていない変更から
#   tests/affected.sh --all                                      すべて(main への push・手で流すとき)
#   tests/affected.sh --ci <イベント>                             CI の plan ジョブ用。pull_request ならマージ後のコミット(HEAD)と
#                                                                 1 つ目の親(main)の差、ほかのイベントは全部。$GITHUB_OUTPUT に
#                                                                 suites(JSON)・go(空白区切り)・snapshot(true/false)を書き、$GITHUB_STEP_SUMMARY に要約
#   TB_COVMAP=<ファイル>   tb のユニットごとに通った関数の記録(tests/covmap.sh が作る)。あれば、Go の関数の中身だけを変えたとき、
#                          tb の組を全部ではなく、変えた関数を通るユニットだけ流す。記録のコミットが、比べる元(--ci は HEAD^1、
#                          --base は REF と HEAD の merge-base)と違えば使わない。標準入力で渡すときは、記録のコミットから今の作業ツリーへの変更とみなす
# 出力は JSON 1 行:
#   {"full":真偽,"reasons":[全部流す理由],"suites":[run-all.sh の組],"go":[go vet / go test に渡すパッケージ],"snapshot":真偽,"notes":[決め方の補足]}
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
#   - tb の組を Go のコードの変更だけで流すとき、TB_COVMAP があれば、tests/funcdiff で変わった関数を出し、記録でそれを通るユニットだけ流す。
#     関数で言い切れない変更(init・型引数・埋め込むファイルなど。tests/funcdiff の頭)や、記録が使えないときは tb の組を全部
# 組やテストが新しくファイルを読むようになったら、ここの規則に足す(足し忘れると PR では流れず、main で初めて落ちる)
set -uo pipefail
set -f   # 規則のパスを引用符なしで渡すので、glob を展開しない
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 1

# ---------- 規則
# パスは前方一致。/ で終わるものはディレクトリ、* を含むものは glob
# 変わったら全部流すもの
GLOBAL=(.github/workflows/test.yml .github/workflows/go.yml .github/actions/ tests/run-all.sh tests/affected.sh tests/funcdiff/ tests/covmap.sh
  go.mod go.sum .gitattributes)
# どのテストも読まないもの
IGNORE=(docs/ .claude/ herdr-up.sh conductor/ taskboard/docs/ taskboard/README.md webui/README.md setup/README.md
  .github/workflows/release.yml)
# 配布物(.goreleaser.yaml の files)に入るもの: 変わったら配布物のビルドを流す
SNAPSHOT_FILES=(.goreleaser.yaml LICENSE README.md)
# 組ごとの、読むファイル(パスの前方一致)。任意の部品(core でない組)はここに書かない(core から名前を出さない約束)。
# 任意の部品は、その部品のディレクトリと、名前を含むファイル(core から名前を出さないことをその部品のテストが確かめる)で流す
suite_paths() {
  case $1 in
    misc)      echo tests/test_env.sh tests/test_instance.sh tests/test_affected.sh lib/ taskboard/bin/ taskboard/schema/ \
                    webui/bin/gen-plist.sh webui/bin/gen-gateway-plist.sh scheduler/bin/gen-plist.sh scheduler/bin/lib.sh \
                    taskboard/test/run.sh taskboard/test/groups taskboard/test/weights taskboard/test/cases/;;   # test_affected が tb の組・ケースを見る
    taskboard) echo taskboard/bin/ taskboard/lib/convtext.py taskboard/schema/ taskboard/sql/ taskboard/perms/ taskboard/config/ taskboard/.gitignore \
                    taskboard/test/run.sh taskboard/test/fake-herdr taskboard/test/groups taskboard/test/weights \
                    taskboard/test/cases/99_zzz_session_sweep.sh .gitignore roles/ setup/examples/ conversation/ lib/ \
                    scheduler/bin/show.sh scheduler/bin/lib.sh scheduler/jobs/;;
    webui)     echo webui/ roles/ conversation/ setup/examples/ scheduler/ lib/ \
                    taskboard/bin/ taskboard/lib/convtext.py taskboard/schema/ taskboard/sql/ taskboard/perms/ taskboard/config/;;
    scheduler) echo scheduler/ lib/ taskboard/bin/ taskboard/schema/ taskboard/test/fake-herdr;;
    setup)     echo setup/ presets/ lib/ taskboard/schema/;;
    *)         echo "$1/";;   # 任意の部品
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
# tb のケースのうち読むものを、関数の記録(TB_COVMAP の files)で選ぶファイル。記録が無ければ tb の組全部(tb help で読む。Web UI などは読まない)
TB_FILES=(taskboard/lib/help.txt)
# Go のコードを文字として読む tb のケース(internal/ の .go が変わったら、*_test.go でも、tb の組を流さないときもこれだけ流す)
TB_SOURCE_CASES=(03_perms.sh 69_pr_contract.sh 83_parts.sh 93_prqueue.sh 99_layout.sh)
CORE_SUITES=(misc taskboard webui scheduler setup)
# core のうち、任意の部品の名前を出してよいファイル(部品の一覧と、全体の README)
NAME_OK=(tests/run-all.sh README.md)

# ---------- 引数
mode=stdin base= event=
while [[ $# -gt 0 ]]; do
  case $1 in
    --all) mode=all; shift;;
    --ci) [[ -n ${2-} ]] || { echo "affected.sh: --ci にはイベント名が要る" >&2; exit 2; }; mode=ci event=$2; shift 2;;
    --base) [[ -n ${2-} ]] || { echo "affected.sh: --base には REF が要る" >&2; exit 2; }; mode=base base=$2; shift 2;;
    -h|--help) sed -n '2,27p' "$0"; exit 0;;
    *) echo "affected.sh: 不明な引数 $1" >&2; exit 2;;
  esac
done

# CI: 決めた結果を GitHub Actions の出力に書く(自分を呼び直して、結果の JSON を受ける)
# 記録(TB_COVMAP)のコミット。比べる元と違えば使わない(covmap_note に理由)
covmap_note=
covmap_check() {  # $1=比べる元のコミット
  [[ -n ${TB_COVMAP-} ]] || { covmap_note="関数の記録なし"; return 1; }
  local c; c=$(jq -er 'select(.version == 1) | .commit' "$TB_COVMAP" 2>/dev/null) || { covmap_note="関数の記録が読めない"; unset TB_COVMAP; return 1; }
  [[ $c == "$1" ]] || { covmap_note="関数の記録が別のコミットのもの(${c:0:7}。比べる元は ${1:0:7})"; unset TB_COVMAP; return 1; }
}
if [[ $mode == ci ]]; then
  if [[ $event == pull_request ]]; then
    covmap_check "$(git rev-parse HEAD^1 2>/dev/null)" || export TB_COVMAP_NOTE=$covmap_note
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
    + "\n- 配布物のビルド: " + (if .snapshot then "する" else "しない" end)
    + (if (.notes | length) > 0 then "\n- 補足: " + (.notes | join(", ")) else "" end)' <<<"$plan" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"
  exit 0
fi

reasons=()
files=()
case $mode in
  all) reasons+=("すべて流す指定");;
  base)
    if mb=$(git merge-base "$base" HEAD 2>/dev/null); then covmap_check "$mb" || export TB_COVMAP_NOTE=$covmap_note; else unset TB_COVMAP; fi
    if ! out=$( { git diff --name-only --no-renames "$base...HEAD" && git diff --name-only --no-renames HEAD && git ls-files --others --exclude-standard; } 2>/dev/null); then
      reasons+=("git diff が取れない: $base")
    else mapfile -t files < <(printf '%s\n' "$out" | sed '/^$/d' | sort -u); fi;;
  stdin) mapfile -t files < <(sed 's/\r$//; /^$/d' | sort -u);;
esac

all_suites() { bash "$ROOT/tests/run-all.sh" --list | jq -c .; }
emit_full() {
  jq -nc --argjson s "$(all_suites)" '{full:true, reasons:$ARGS.positional, suites:$s, go:["./..."], snapshot:true, notes:[]}' --args "$@"
  exit 0
}
((${#reasons[@]})) && emit_full "${reasons[@]}"
# 組の並び(run-all.sh --list の順。taskboard-<組> は taskboard 1 つにまとめる)。core でないものは任意の部品
mapfile -t SUITE_ORDER < <(bash "$ROOT/tests/run-all.sh" --list | jq -r '.[]' | sed 's/^taskboard-.*/taskboard/' | awk '!seen[$0]++')
((${#SUITE_ORDER[@]})) || emit_full "組の一覧が取れない"
OPTIONAL=()
for s in "${SUITE_ORDER[@]}"; do [[ " ${CORE_SUITES[*]} " == *" $s "* ]] || OPTIONAL+=("$s"); done

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

# 記録(TB_COVMAP)が使えるか。使えなければ covmap_why に理由
covmap_ok=
covmap_usable() {
  if [[ -z $covmap_ok ]]; then
    covmap_ok=yes
    if [[ -z ${TB_COVMAP-} ]]; then covmap_ok=no covmap_why="${TB_COVMAP_NOTE:-関数の記録なし}なので tb の組は全部"
    elif ! jq -e '.version == 1 and (.funcs | type) == "object" and (.files | type) == "object"' "$TB_COVMAP" >/dev/null 2>&1; then
      covmap_ok=no covmap_why="関数の記録が読めないので tb の組は全部"
    elif [[ $(jq -c .units "$TB_COVMAP" 2>/dev/null) != "$(bash taskboard/test/run.sh --units | awk '{print $1}' | jq -Rnc '[inputs]')" ]]; then
      covmap_ok=no covmap_why="関数の記録と tb のユニットの分け方が合わないので tb の組は全部"
    fi
  fi
  [[ $covmap_ok == yes ]]
}
# tb の組を Go のコードの変更だけで流すとき: 記録(TB_COVMAP)があれば、変わった関数を通るユニットだけ。言い切れなければ組全部
tb_by_func() {
  local f out c line u
  tb_all() { SUITE[taskboard]=1; notes+=("$1"); }
  covmap_usable || { tb_all "$covmap_why"; return; }
  for f in "${SRC_FILES[@]}"; do [[ $f == *.go ]] || { tb_all "Go のパッケージの .go でないファイル($f)があるので tb の組は全部"; return; }; done
  c=$(jq -r .commit "$TB_COVMAP")
  # shellcheck disable=SC2086
  out=$(${TB_FUNCDIFF:-go run ./tests/funcdiff} "$c" "${SRC_FILES[@]}") || { tb_all "tests/funcdiff が失敗したので tb の組は全部"; return; }
  line=$(grep -m1 $'\t\*$' <<<"$out") && { tb_all "関数で言い切れない変更(${line%$'\t'*})があるので tb の組は全部"; return; }
  while IFS= read -r u; do [[ -n $u ]] && TB_CASES[$u]=1; done < <(
    sed -n 's/\t/:/p' <<<"$out" | jq -Rr --slurpfile m "$TB_COVMAP" '$m[0].funcs[.] // [] | .[]' | sort -u)
  notes+=("tb は変えた関数を通るユニットだけ(記録 ${c:0:7})")
}

# ---------- ファイルを分ける
declare -A SUITE=() CHG_SRC=() CHG_TEST=() GO_SEL=() TB_CASES=()
SRC_FILES=()   # Go のパッケージの、テストでないファイル
TB_FILE_CHG=() # TB_FILES のうち変わったもの
snapshot=false
notes=()
for f in ${files[@]+"${files[@]}"}; do
  matched=
  has_prefix "$f" "${GLOBAL[@]}" && emit_full "全体にかかるファイル: $f"
  if pkg=$(pkg_of "$f"); then
    matched=1
    if [[ $f == *_test.go || $f == */testdata/* ]]; then CHG_TEST[$pkg]=1; else CHG_SRC[$pkg]=1; SRC_FILES+=("$f"); fi
  fi
  for s in "${SUITE_ORDER[@]}"; do
    # shellcheck disable=SC2046
    has_prefix "$f" $(suite_paths "$s") && { SUITE[$s]=1; matched=1; }
  done
  if [[ $f =~ ^taskboard/test/cases/([A-Za-z0-9_.-]+\.sh)$ ]]; then
    TB_CASES[${BASH_REMATCH[1]}]=1; matched=1
  fi
  [[ $f == internal/*.go ]] && for c in "${TB_SOURCE_CASES[@]}"; do TB_CASES[$c]=1; done
  has_prefix "$f" "${TB_FILES[@]}" && { TB_FILE_CHG+=("$f"); matched=1; }
  for rule in "${GO_EXTRA[@]}"; do
    # shellcheck disable=SC2086
    has_prefix "$f" ${rule#*:} && { GO_SEL[${rule%%:*}]=1; matched=1; }
  done
  has_prefix "$f" "${SNAPSHOT_FILES[@]}" && { snapshot=true; matched=1; }
  for s in ${OPTIONAL[@]+"${OPTIONAL[@]}"}; do
    [[ $f != "$s/"* && -f $f ]] && ! has_prefix "$f" "${NAME_OK[@]}" && grep -qiF -- "$s" "$f" && SUITE[$s]=1
  done
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
      for p in "${cl[@]}"; do [[ -n ${CHG_SRC[$p]-} ]] && { if [[ $s == taskboard ]]; then TB_BY_GO=1; else SUITE[$s]=1; fi; break 2; }; done
    done
  done
  [[ -n ${TB_BY_GO-} ]] && tb_by_func
  # テストの依存に変わったパッケージを含むパッケージ(P.test の Deps)
  tlist=$(go list -test -f '{{.ImportPath}}{{"\t"}}{{join .Deps " "}}' ./... 2>/dev/null) || emit_full "go list -test が使えない"
  while IFS=$'\t' read -r ip deps; do
    [[ $ip == *.test ]] || continue
    t=${ip%.test}; t=${t#"$MOD"}; t=${t#/}
    while IFS= read -r d; do [[ -n ${CHG_SRC[$d]-} ]] && { GO_SEL[$t]=1; break; }; done < <(strip_mod <<<"$deps")
  done <<<"$tlist"
  for p in "${!CHG_SRC[@]}"; do GO_SEL[$p]=1; done
fi
if ((${#TB_FILE_CHG[@]})) && [[ -z ${SUITE[taskboard]-} ]]; then
  if covmap_usable; then
    for f in "${TB_FILE_CHG[@]}"; do
      while IFS= read -r u; do [[ -n $u ]] && TB_CASES[$u]=1; done < <(jq -r --arg f "$f" '.files[$f] // error("no") | .[]' "$TB_COVMAP" 2>/dev/null || echo "!")
    done
    [[ -n ${TB_CASES['!']-} ]] && { unset 'TB_CASES[!]'; SUITE[taskboard]=1; notes+=("関数の記録に ${TB_FILE_CHG[*]} が無いので tb の組は全部"); }
    [[ -z ${SUITE[taskboard]-} ]] && notes+=("tb は ${TB_FILE_CHG[*]} を読むユニットだけ")
  else SUITE[taskboard]=1; notes+=("$covmap_why"); fi
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
  --argjson s "$(printf '%s\n' ${suites[@]+"${suites[@]}"} | jq -Rnc '[inputs | select(. != "")]')" \
  --argjson g "$(printf '%s\n' ${gopkgs[@]+"${gopkgs[@]}"} | sort -u | jq -Rnc '[inputs | select(. != "")]')" \
  --argjson n "$(printf '%s\n' ${notes[@]+"${notes[@]}"} | jq -Rnc '[inputs | select(. != "")]')" \
  '{full:false, reasons:[], suites:$s, go:$g, snapshot:$snap, notes:$n}'
