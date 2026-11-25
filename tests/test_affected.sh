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
eq "なし: docs だけ" "$(q '[.full, .suites, .go, .snapshot]' docs/adr/0001.md README.md)" '[false,[],[],false]'
eq "なし: 空" "$(q '[.suites, .go]')" '[[],[]]'

# --- 部品ごと
eq "webui の画面だけ → webui の組と internal/webui" "$(q '[.suites, .go, .snapshot]' webui/static/app.js)" '[["webui"],["./internal/webui"],false]'
eq "Go のテストだけ → そのパッケージのテストだけ(組・配布物は流さない)" "$(q '[.suites, .go, .snapshot]' internal/webui/testdata/pages.json)" '[[],["./internal/webui"],false]'
eq "internal/webui のコード → webui と、Go を文字で読む tb のケースだけ(tb の組全部は流さない)" \
  "$(q '[.suites, (.go | index("./internal/webui") != null), (.go | index("./internal/tbcli")), .snapshot]' internal/webui/server.go)" \
  '[["taskboard-only:83_parts.sh,99_layout.sh","webui"],true,null,true]'
eq "tb のコード → tb を呼ぶ組すべて" "$(q .suites internal/tbcli/run.go)" "[\"misc\",$TB4,\"webui\",\"scheduler\"]"
eq "tb のコード → 依存する側の Go のテスト" "$(q '.go | (index("./internal/tbcli") != null and index("./cmd/herdrctl") != null)' internal/tbcli/run.go)" "true"
eq "依存の根(store) → 使う側の Go のテストへ広がる" "$(q '.go | (index("./internal/webui") != null and index("./internal/tbcli") != null)' internal/kernel/store/store.go)" "true"
eq "tb のケースだけ → そのケースだけ" "$(q .suites taskboard/test/cases/14_cancel.sh taskboard/test/cases/12_conv.sh)" '["taskboard-only:12_conv.sh,14_cancel.sh"]'
eq "tb のケースと tb のコード → tb の組全部(ケースだけの組は出さない)" "$(q .suites taskboard/test/cases/14_cancel.sh taskboard/bin/tb | jq -c 'map(select(startswith("taskboard")))')" "[$TB4]"
eq "消した tb のケース → 流せないので出さない" "$(q .suites taskboard/test/cases/00_gone.sh)" '[]'
eq "どのユニットでも流れるケース → tb の組全部" "$(q .suites taskboard/test/cases/99_zzz_session_sweep.sh)" "[$TB4]"
eq "scheduler の bash" "$(q .suites scheduler/bin/tick.sh)" '["webui","scheduler"]'
eq "setup" "$(q '[.suites, .go]' setup/check.sh presets/example.conf)" '[["setup"],[]]'
eq "repoindex" "$(q '[.suites, .go]' repoindex/ri/cli.py)" '[["repoindex"],[]]'
eq "配布の設定 → 配布物のビルドだけ" "$(q '[.suites, .go, .snapshot]' .goreleaser.yaml)" '[[],[],true]'
eq "改行 CRLF のファイル名一覧も読む" "$(printf 'webui/static/app.js\r\n' | bash "$HERE/affected.sh" | jq -c .suites)" '["webui"]'

# --- run-all.sh が taskboard-only の組を受ける
eq "run-all.sh: 無いケースの taskboard-only は使い方の誤り" "$(bash "$HERE/run-all.sh" taskboard-only:00_gone.sh >/dev/null 2>&1; echo $?)" "2"
eq "run-all.sh: ケースの名前に余計なものがあれば使い方の誤り" "$(bash "$HERE/run-all.sh" 'taskboard-only:14_cancel.sh;x' >/dev/null 2>&1; echo $?)" "2"
echo "pass=$pass fail=$fail"; [[ $fail -eq 0 ]]
