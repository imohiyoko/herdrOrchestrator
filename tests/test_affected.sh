#!/usr/bin/env bash
# tests/affected.sh: 変えたファイルから流す組と Go のパッケージを決める。迷うものは全部に倒れること
# 本物のリポジトリの Go のパッケージを使う(go list)。git の履歴には触らない(標準入力でファイルを渡す)
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
pass=0; fail=0
eq() { if [[ "$2" == "$3" ]]; then pass=$((pass+1)); echo "ok   $1"; else fail=$((fail+1)); echo "FAIL $1"; echo "   expected: $3"; echo "   actual:   $2"; fi; }
aff() { printf '%s\n' "$@" | bash "$HERE/affected.sh"; }
q() { local f=$1; shift; aff "$@" | jq -c "$f"; }   # q <jq の式> <ファイル> ...
TB4='"taskboard-1of4","taskboard-2of4","taskboard-3of4","taskboard-4of4"'

# --- 全部に倒れるもの
eq "全部: --all" "$(bash "$HERE/affected.sh" --all | jq -c '[.full, .go, .snapshot]')" '[true,["./..."],true]'
eq "全部: --all の組は run-all.sh --list と同じ" "$(bash "$HERE/affected.sh" --all | jq -c .suites)" "$(bash "$HERE/run-all.sh" --list)"
eq "全部: go.mod" "$(q '[.full, .reasons]' go.mod)" '[true,["全体にかかるファイル: go.mod"]]'
eq "全部: CI の定義" "$(q .full .github/workflows/test.yml)" "true"
eq "全部: この仕組み自身" "$(q .full tests/affected.sh)" "true"
eq "全部: 規則に無いファイル" "$(q '[.full, .reasons]' foo/new-component.sh)" '[true,["規則に無いファイル: foo/new-component.sh"]]'
eq "全部: 規則に無いファイルが 1 つでも混じる" "$(q .full docs/a.md foo/x)" "true"
eq "全部: 消したパッケージの下(どのパッケージにも当たらない)" "$(q .full cmd/gone/main.go)" "true"
eq "全部: --base に無い REF" "$(bash "$HERE/affected.sh" --base no-such-ref-xyz 2>/dev/null | jq -c .full)" "true"
eq "使い方の誤りは 2" "$(bash "$HERE/affected.sh" --nope >/dev/null 2>&1; echo $?)" "2"

# --- 何も流さないもの
eq "なし: docs だけ" "$(q '[.full, .suites, .go, .snapshot]' docs/adr/0001.md taskboard/docs/x.md)" '[false,[],[],false]'
eq "なし: 空" "$(q '[.suites, .go]')" '[[],[]]'

# --- 部品ごと
eq "webui の画面だけ → webui の組と internal/webui" "$(q '[.suites, .go, .snapshot]' webui/static/app.js)" '[["webui"],["./internal/webui"],false]'
eq "Go のテストだけ → そのパッケージのテストだけ(組・配布物は流さない)" "$(q '[.suites, .go, .snapshot]' internal/webui/testdata/pages.json)" '[[],["./internal/webui"],false]'
eq "internal/webui のコード → webui と、Go を文字で読む tb のケースだけ(tb の組全部は流さない)" \
  "$(q '[.suites, (.go | index("./internal/webui") != null), (.go | index("./internal/tbcli")), .snapshot]' internal/webui/server.go)" \
  '[["taskboard-only:69_pr_contract.sh,83_parts.sh,93_prqueue.sh,99_layout.sh","webui"],true,null,true]'
eq "tb のコード → tb を呼ぶ組すべて" "$(q .suites internal/tbcli/run.go)" "[\"misc\",$TB4,\"webui\",\"scheduler\"]"
eq "tb のコード → 依存する側の Go のテスト" "$(q '.go | (index("./internal/tbcli") != null and index("./cmd/herdrctl") != null)' internal/tbcli/run.go)" "true"
eq "依存の根(store) → 使う側の Go のテストへ広がる" "$(q '.go | (index("./internal/webui") != null and index("./internal/tbcli") != null)' internal/kernel/store/store.go)" "true"
eq "tb のケースだけ → そのケースだけ" "$(q '.suites | map(select(startswith("taskboard")))' taskboard/test/cases/14_cancel.sh taskboard/test/cases/12_conv.sh)" '["taskboard-only:12_conv.sh,14_cancel.sh"]'
eq "tb のケースと tb のコード → tb の組全部(ケースだけの組は出さない)" "$(q .suites taskboard/test/cases/14_cancel.sh taskboard/bin/tb | jq -c 'map(select(startswith("taskboard")))')" "[$TB4]"
eq "消した tb のケース → 流せないので出さない" "$(q .suites taskboard/test/cases/00_gone.sh)" '["misc"]'
eq "どのユニットでも流れるケース → tb の組全部" "$(q .suites taskboard/test/cases/99_zzz_session_sweep.sh)" "[\"misc\",$TB4]"
eq "scheduler の bash" "$(q .suites scheduler/bin/tick.sh)" '["webui","scheduler"]'
eq "setup" "$(q '[.suites, .go]' setup/check.sh presets/example.conf)" '[["setup"],[]]'
# 任意の部品(core でない組)。名前は core に書かない約束なので、run-all.sh --list の最後の組を使う
OPT=$(bash "$HERE/run-all.sh" --list | jq -r '.[-1]')
case $OPT in misc|taskboard-*|webui|scheduler|setup) OPT=;; esac
if [[ -n $OPT ]]; then
  eq "任意の部品: そのディレクトリ" "$(q '[.suites, .go]' "$OPT/x.py")" "[[\"$OPT\"],[]]"
  eq "任意の部品: 名前を出してよい README では流さない" "$(q .suites README.md)" "[]"
fi
eq "Go のテストだけでも、Go を文字で読む tb のケースは流す" "$(q .suites internal/pr/conf_test.go)" '["taskboard-only:69_pr_contract.sh,83_parts.sh,93_prqueue.sh,99_layout.sh"]'
eq "配布物に入るファイル → 配布物のビルド" "$(q '[.suites, .snapshot]' LICENSE README.md)" '[[],true]'
eq "tb のケース → misc も(test_affected がケースを見る)" "$(q .suites taskboard/test/cases/14_cancel.sh | jq -c 'index("misc") != null')" "true"
eq "scheduler のジョブの例 → tb の組も(ケースが読む)" "$(q .suites scheduler/jobs/watch.conf.example | jq -c 'map(select(startswith("taskboard"))) | length')" "4"
eq "配布の設定 → 配布物のビルドだけ" "$(q '[.suites, .go, .snapshot]' .goreleaser.yaml)" '[[],[],true]'
eq "改行 CRLF のファイル名一覧も読む" "$(printf 'webui/static/app.js\r\n' | bash "$HERE/affected.sh" | jq -c .suites)" '["webui"]'

# --- tb のケースを関数の記録(TB_COVMAP)で選ぶ。tests/funcdiff は差し替える(出す行を FD_OUT で渡す)
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$FD_OUT"\n' >"$TMP/fd.sh"
UNITS=$(bash "$HERE/../taskboard/test/run.sh" --units | awk '{print $1}' | jq -Rnc '[inputs]')
jq -n --argjson u "$UNITS" '{version: 1, commit: "c0ffee", units: $u,
  funcs: {"internal/reminder/nudge.go:*Nudge.Run": ["65_nudge.sh", "03_perms.sh"], "internal/tbcli/tbcli.go:Run": $u},
  files: {"taskboard/lib/help.txt": ["99_layout.sh"]}}' >"$TMP/map.json"
jq '.units |= .[1:]' "$TMP/map.json" >"$TMP/map-old.json"
cm() { local o=$1 m=$2 f=$3; shift 3; printf '%s\n' "$@" | FD_OUT=$o TB_FUNCDIFF="bash $TMP/fd.sh" TB_COVMAP=$m bash "$HERE/affected.sh" | jq -c "$f"; }
TBS='map(select(startswith("taskboard")))'
eq "記録: 変えた関数を通るユニットだけ(Go を文字で読むケースも)" "$(cm $'internal/reminder/nudge.go\t*Nudge.Run' "$TMP/map.json" "[(.suites | $TBS), (.notes | length)]" internal/reminder/nudge.go)" \
  '[["taskboard-only:03_perms.sh,65_nudge.sh,69_pr_contract.sh,83_parts.sh,93_prqueue.sh,99_layout.sh"],1]'
eq "記録: 記録に無い関数(どのユニットも通らない)→ tb は Go を文字で読むケースだけ" "$(cm $'internal/reminder/nudge.go\tnope' "$TMP/map.json" ".suites | $TBS" internal/reminder/nudge.go)" \
  '["taskboard-only:69_pr_contract.sh,83_parts.sh,93_prqueue.sh,99_layout.sh"]'
eq "記録: 関数を変えていない(tests/funcdiff が何も出さない)" "$(cm '' "$TMP/map.json" ".suites | $TBS" internal/reminder/nudge.go)" \
  '["taskboard-only:69_pr_contract.sh,83_parts.sh,93_prqueue.sh,99_layout.sh"]'
eq "記録: どのユニットも通る関数 → tb の組全部と同じユニット" "$(cm $'internal/tbcli/tbcli.go\tRun' "$TMP/map.json" ".suites | $TBS | .[0] | split(\",\") | length" internal/tbcli/tbcli.go)" "$(jq '.units | length' "$TMP/map.json")"
eq "記録: 関数で言い切れない変更 → tb の組全部" "$(cm $'internal/reminder/nudge.go\t*' "$TMP/map.json" "[(.suites | $TBS), (.notes[0] | test(\"言い切れない\"))]" internal/reminder/nudge.go)" "[[$TB4],true]"
eq "記録: Go のパッケージの .go でないファイル(埋め込み)→ tb の組全部" "$(cm '' "$TMP/map.json" ".suites | $TBS" internal/reminder/nudge.go taskboard/schema/999_x.sql)" "[$TB4]"
eq "記録: ユニットの分け方が違う記録は使わない" "$(cm $'internal/reminder/nudge.go\t*Nudge.Run' "$TMP/map-old.json" "[(.suites | $TBS), (.notes[0] | test(\"分け方\"))]" internal/reminder/nudge.go)" "[[$TB4],true]"
eq "記録: 読めない記録は使わない" "$(echo '{}' >"$TMP/bad.json"; cm '' "$TMP/bad.json" ".suites | $TBS" internal/reminder/nudge.go)" "[$TB4]"
eq "記録: tests/funcdiff が失敗 → tb の組全部" "$(printf 'internal/reminder/nudge.go\n' | TB_FUNCDIFF=false TB_COVMAP=$TMP/map.json bash "$HERE/affected.sh" | jq -c ".suites | $TBS")" "[$TB4]"
eq "記録: tb の組をファイルの規則でも流すとき(bin/tb など)は、記録で絞らない" "$(cm $'internal/reminder/nudge.go\t*Nudge.Run' "$TMP/map.json" ".suites | $TBS" internal/reminder/nudge.go taskboard/bin/tb)" "[$TB4]"
eq "記録: help.txt → 読むユニットだけ(Web UI は流さない)" "$(cm '' "$TMP/map.json" '[.suites, .go]' taskboard/lib/help.txt)" '[["taskboard-only:99_layout.sh"],[]]'
eq "記録なし: help.txt → tb の組全部" "$(q '.suites' taskboard/lib/help.txt)" "[$TB4]"
eq "記録に help.txt が無い → tb の組全部" "$(jq '.files = {}' "$TMP/map.json" >"$TMP/nofiles.json"; cm '' "$TMP/nofiles.json" ".suites | $TBS" taskboard/lib/help.txt)" "[$TB4]"
eq "記録に funcs が無い → 使わない" "$(jq 'del(.funcs)' "$TMP/map.json" >"$TMP/nofuncs.json"; cm $'internal/reminder/nudge.go\t*Nudge.Run' "$TMP/nofuncs.json" ".suites | $TBS" internal/reminder/nudge.go)" "[$TB4]"
eq "記録なし → tb の組全部と、その理由" "$(q '.notes' internal/reminder/nudge.go)" '["関数の記録なしなので tb の組は全部"]'
eq "全部: tests/funcdiff・tests/covmap.sh" "$(q '[.full]' tests/funcdiff/main.go tests/covmap.sh)" '[true]'

# --- run-all.sh が taskboard-only の組を受ける
eq "run-all.sh: 無いケースの taskboard-only は使い方の誤り" "$(bash "$HERE/run-all.sh" taskboard-only:00_gone.sh >/dev/null 2>&1; echo $?)" "2"
eq "run-all.sh: ケースの名前に余計なものがあれば使い方の誤り" "$(bash "$HERE/run-all.sh" 'taskboard-only:14_cancel.sh;x' >/dev/null 2>&1; echo $?)" "2"
echo "pass=$pass fail=$fail"; [[ $fail -eq 0 ]]
