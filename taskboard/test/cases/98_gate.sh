# shellcheck shell=bash
# run.sh から番号順に source される。merge / approve の判定の口(lib/gate.sh。段 0: 判定は読み取りだけ、実行は常に無効)
# lib/gate.sh を別のシェルで source して直接評価する。設定の差し替えはテストの中(GATE_CONF / GATE_PATHS の代入)だけ。tb 本体は env で差し替えられない
GT=$TMP/gate; mkdir -p "$GT"
cat >"$GT/gate.conf" <<'C'
[mode]
state = disabled
[repos]
allow = example-owner/example-repo, other-owner/other-repo
[pr-kinds]
approve = docs-only
merge = docs-only, tests-only
[threshold]
approve = 2
merge = 1
[limits]
merge-per-day = 3
merge-min-interval-min = 30
approve-per-day = 5
C
cat >"$GT/paths.conf" <<'C'
# example
roles/**
**/CLAUDE.md
scheduler/**
C
: >"$GT/empty-paths.conf"
# 基準の事実(docs のみ・小さい・2 系統・全部そろう)。行を足す・差し替えるテストは gtf で作る
BASE=(
  'HEAD 3f9c2ab'
  'PR example-owner/example-repo#123'
  'REVIEW self 3f9c2ab blocker=0 should=0 verdict=pass'
  'REVIEW external 3f9c2ab blocker=0 should=0 verdict=pass'
  'TEST exit=0 sha=3f9c2ab'
  'PUBLIC-CHECK clean'
  'FILE M docs/guide.md'
  'SIZE 5 100'
  'AUTHOR author-a'
  'EXECUTOR runner-b'
  'AUTHOR-REJECT-RATE low'
)
# gtf <除く行の接頭辞|-> [足す行...] → 基準から接頭辞の行を除いて、足した行を加えた事実
gtf() { local drop=$1; shift; local l; for l in "${BASE[@]}"; do [[ $drop != - && $l == "$drop"* ]] || printf '%s\n' "$l"; done; printf '%s\n' "$@"; }
# gt <action> <gate.conf> <paths.conf> [pr] < 事実 → 評価の全出力。終了コードは最後の行 "rc=<n>"
gt() {
  local act=$1 conf=$2 paths=$3 pr=${4:-} facts=()
  while IFS= read -r l || [[ -n $l ]]; do facts+=("$l"); done
  ( TB_ROOT=$HERE/..; source "$HERE/../lib/gate.sh"; usage() { echo "usage: $*"; exit 2; }
    GATE_CONF=$conf; GATE_PATHS=$paths
    gate_eval "$act" T-1 "$pr" "${facts[@]}"; gate_format; echo "rc=$GATE_EXIT" )
}
G1() { gt "$1" "$GT/gate.conf" "$GT/paths.conf"; }
hardl() { grep '^hard:' | sed 's/^hard: //'; }
rsn() { grep '^reason:' | sed 's/^reason: //'; }
rcof() { grep '^rc=' | sed 's/^rc=//'; }

# --- 基準: 全部 0 点 → allow(approve も merge も)
for a in approve merge; do
  eq "gate $a: clean docs-only PR → allow, exit 0" "$(gtf - | G1 $a | sed -n '1p;$p' | tr '\n' '|')" "gate: allow|rc=0|"
done
eq "gate: output has score/hard lines" "$(gtf - | G1 merge | sed -n '5,7p' | cut -d' ' -f1-3 | tr '\n' '|')" "score: 0 /|hard: none|reason: S-01 +0|"
eq "gate: reasons list every contribution (one line each)" "$(gtf - | G1 merge | rsn | cut -d' ' -f1-2 | tr '\n' ',')" "S-01 +0,P-01 +0,K-01 +0,T-01 +0,R-01 +0,A-01 +0,"
# --- 決定論: 同じ入力は同じ出力
eq "gate: deterministic (two runs identical)" "$(gtf - | G1 merge | cksum)" "$(gtf - | G1 merge | cksum)"
eq "gate: mode disabled does not stop the evaluation" "$(gtf - | G1 merge | grep -c '^mode: disabled')" "1"

# --- 点数の規則(1 件ずつ)。しきい値: approve 2 / merge 1
# S-01 変更の大きさ(変更行数 ÷ 全行数)
for t in '9 100|+0 変更の大きさ: 小' '10 100|+1 変更の大きさ: 中' '29 100|+1 変更の大きさ: 中' '30 100|+2 変更の大きさ: 大' '59 100|+2 変更の大きさ: 大' '60 100|+3 変更の大きさ: 特大' '500 100|+3 変更の大きさ: 特大'; do
  eq "gate S-01: SIZE ${t%%|*} → ${t#*|}" "$(gtf 'SIZE' "SIZE ${t%%|*}" | G1 approve | rsn | grep '^S-01' | cut -d' ' -f2-)" "${t#*|}"
done
# P-01 / K-01
pk() { gtf 'FILE' "$@" | G1 approve | rsn | grep -E '^(P-01|K-01)' | sed 's/ 触るパス//;s/ PR の種類//' | tr '\n' '|'; }
eq "gate P-01/K-01: docs only" "$(pk 'FILE M docs/a.md' 'FILE A README.md')" "P-01 +0: docs・tests のみ|K-01 +0: docs-only|"
eq "gate P-01/K-01: tests only" "$(pk 'FILE M test/a.sh' 'FILE A tests/b.sh')" "P-01 +0: docs・tests のみ|K-01 +0: tests-only|"
eq "gate P-01/K-01: general code" "$(pk 'FILE M src/app.sh')" "P-01 +1: 一般のコード|K-01 +1: code|"
eq "gate P-01/K-01: lib/config (important) → +2" "$(pk 'FILE M tool/lib/x.sh')" "P-01 +2: lib・config・scheduler 配下(重要度 高)|K-01 +1: code|"
eq "gate P-01/K-01: docs + code → mixed +2" "$(pk 'FILE M docs/a.md' 'FILE M src/app.sh')" "P-01 +1: 一般のコード|K-01 +2: mixed|"
eq "gate P-01/K-01: docs + tests → mixed" "$(pk 'FILE M docs/a.md' 'FILE M test/a.sh')" "P-01 +0: docs・tests のみ|K-01 +2: mixed|"
# T-01
eq "gate T-01: not run → +2" "$(gtf 'TEST' 'TEST none' | G1 approve | rsn | grep '^T-01')" "T-01 +2 テスト: 未実行"
eq "gate T-01: not run → score 2 = threshold(approve) → allow" "$(gtf 'TEST' 'TEST none' | G1 approve | sed -n 1p)" "gate: allow"
eq "gate T-01: not run → exceeds threshold(merge 1) → ask" "$(gtf 'TEST' 'TEST none' | G1 merge | sed -n '1p;$p' | tr '\n' '|')" "gate: ask|rc=20|"
# R-01
rr() { gtf 'REVIEW' "$@" | G1 approve | rsn | grep '^R-01'; }
eq "gate R-01: should remains → +1" "$(rr 'REVIEW a 3f9c2ab blocker=0 should=2 verdict=pass' 'REVIEW b 3f9c2ab blocker=0 should=0 verdict=pass')" "R-01 +1 レビュー: should が残っている"
eq "gate R-01: one lineage only → +2" "$(rr 'REVIEW a 3f9c2ab blocker=0 should=0 verdict=pass')" "R-01 +2 レビュー: 1 系統のみ"
eq "gate R-01: same lineage twice counts once" "$(rr 'REVIEW a 3f9c2ab blocker=0 should=0 verdict=pass' 'REVIEW a 3f9c2ab blocker=0 should=0 verdict=pass')" "R-01 +2 レビュー: 1 系統のみ"
# A-01
for t in low:0 mid:1 high:2 unknown:1; do
  eq "gate A-01: ${t%%:*} → +${t#*:}" "$(gtf 'AUTHOR-REJECT-RATE' "AUTHOR-REJECT-RATE ${t%%:*}" | G1 approve | rsn | grep '^A-01' | cut -d' ' -f1-2)" "A-01 +${t#*:}"
done
# しきい値の境界: approve 2 以下 allow / 3 で ask(mid + 大きさ中 + 一般 code = 3 だが code も加わる。ここは A=high(+2) + S 中(+1))
eq "gate threshold: score 2 → allow (approve)" "$(gtf 'AUTHOR-REJECT-RATE' 'AUTHOR-REJECT-RATE high' | G1 approve | sed -n '1p;$p' | tr '\n' '|')" "gate: allow|rc=0|"
eq "gate threshold: score 3 → ask (approve), reason is score only (hard: none)" "$(gtf 'AUTHOR-REJECT-RATE' 'AUTHOR-REJECT-RATE high' | sed 's/^SIZE 5 100/SIZE 10 100/' | G1 approve | grep -E '^(gate|hard|score|rc)' | tr '\n' '|')" "gate: ask|score: 3 / threshold(approve) 2|hard: none|rc=20|"
eq "gate threshold: score 1 → allow (merge)" "$(gtf 'AUTHOR-REJECT-RATE' 'AUTHOR-REJECT-RATE unknown' | G1 merge | sed -n 1p)" "gate: allow"

# --- 常に人に回す規則(hard)。点数が 0 でも ask
hh() { gtf "$1" "${@:2}" | G1 merge | grep -E '^(gate|score|rc)' | tr '\n' '|'; }
eq "gate H-01: governance path (roles/) → ask even with score 0" "$(hh FILE 'FILE M roles/worker.md')" "gate: ask|score: 0 / threshold(merge) 1|rc=20|"
eq "gate H-01: names the path and rule" "$(gtf FILE 'FILE M roles/worker.md' | G1 merge | hardl)" "H-01 ガバナンスのパス規則に当たる変更: roles/worker.md(規則 roles/**)"
eq "gate H-01: rename hits on the OLD path" "$(gtf FILE 'FILE R roles/old.md docs/new.md' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate H-01: rename hits on the NEW path" "$(gtf FILE 'FILE R docs/old.md scheduler/new.conf' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate H-01: delete hits" "$(gtf FILE 'FILE D scheduler/job.conf' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate H-01: **/ prefix also matches the root file" "$(gtf FILE 'FILE M CLAUDE.md' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate H-01: case-insensitive" "$(gtf FILE 'FILE M Roles/x.md' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate H-01: nested path matches" "$(gtf FILE 'FILE M sub/dir/CLAUDE.md' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate H-01: unrelated docs path does not hit" "$(gtf FILE 'FILE M docs/roles-overview.md' | G1 merge | hardl)" "none"
# 組み込みのパス規則は、パス規則ファイルが空でも効く(外せない)
for p in '.github/workflows/ci.yml' 'taskboard/config/gate.conf' 'taskboard/config/gate-paths.conf' 'taskboard/lib/gate.sh' 'taskboard/config/approvals.conf' 'x/config/risk-rules.conf' 'gate.conf'; do
  eq "gate H-01 builtin: $p hits with an empty paths file" "$(gtf FILE "FILE M $p" | gt merge "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)" "H-01 "
done
for p in 'taskboard/bin/tb' 'bin/tb' 'tests/run-all.sh' 'taskboard/test/run.sh' 'webui/tests/run.sh' 'taskboard/test/cases/98_gate.sh' 'tests/test_gate.sh' '.github'; do
  eq "gate H-01 builtin: $p hits (merge and approve)" "$(gtf FILE "FILE M $p" | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)|$(gtf FILE "FILE M $p" | gt merge "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)" "H-01 |H-01 "
done
eq "gate H-01: an ordinary test file does not hit" "$(gtf FILE 'FILE M taskboard/test/cases/10_tasks.sh' | G1 merge | hardl)" "none"
eq "gate P-01: executable under docs/ is code, not docs (+1)" "$(pk 'FILE M docs/run.sh' | cut -d'|' -f1)" "P-01 +1: 一般のコード"
eq "gate P-01: bin/ is important (+2)" "$(pk 'FILE M tool/bin/x' | cut -d'|' -f1)" "P-01 +2: lib・config・scheduler 配下(重要度 高)"
eq "gate N-2: control characters in an unknown line are not echoed" "$(gtf - $'\033[31mBAD\001x' | G1 merge | grep -c $'[\001\033]')" "0"
eq "gate threshold cap: approve = 9 is warned and capped at 2" "$(sed 's/^approve = 2/approve = 9/' "$GT/gate.conf" >"$GT/loose2.conf"; gtf - | gt approve "$GT/loose2.conf" "$GT/paths.conf" | grep -E '^(score|warn)' | tr '\n' '|')" "score: 0 / threshold(approve) 2|warn: 緩め設定は無効: [threshold] approve = 9(上限 2 のまま)|"
eq "gate H-06: type/mode change → ask" "$(gtf FILE 'FILE T docs/a.md' | G1 merge | hardl | cut -c1-5)" "H-06 "
eq "gate H-02: public check dirty → ask" "$(gtf PUBLIC-CHECK 'PUBLIC-CHECK dirty' | G1 merge | hardl | cut -c1-5)" "H-02 "
eq "gate H-02: public check unavailable → ask" "$(gtf PUBLIC-CHECK 'PUBLIC-CHECK unavailable' | G1 merge | hardl | cut -c1-5)" "H-02 "
eq "gate H-03: review blocker → ask" "$(gtf REVIEW 'REVIEW a 3f9c2ab blocker=1 should=0 verdict=pass' 'REVIEW b 3f9c2ab blocker=0 should=0 verdict=pass' | G1 merge | hardl | cut -c1-5)" "H-03 "
eq "gate H-03: review verdict=fail → ask" "$(gtf REVIEW 'REVIEW a 3f9c2ab blocker=0 should=0 verdict=fail' 'REVIEW b 3f9c2ab blocker=0 should=0 verdict=pass' | G1 merge | hardl | cut -c1-5)" "H-03 "
eq "gate H-03: review sha mismatch → ask" "$(gtf REVIEW 'REVIEW a 1111111 blocker=0 should=0 verdict=pass' 'REVIEW b 3f9c2ab blocker=0 should=0 verdict=pass' | G1 merge | hardl | cut -c1-5)" "H-03 "
eq "gate H-03: zero lineages → ask" "$(gtf REVIEW 'REVIEW none' | G1 merge | hardl | cut -c1-5)" "H-03 "
eq "gate H-03: test failed → ask" "$(gtf TEST 'TEST exit=1 sha=3f9c2ab' | G1 merge | hardl | cut -c1-5)" "H-03 "
eq "gate H-03: test sha mismatch → ask" "$(gtf TEST 'TEST exit=0 sha=1111111' | G1 merge | hardl | cut -c1-5)" "H-03 "
eq "gate H-04: author == executor (case-insensitive) → ask" "$(gtf EXECUTOR 'EXECUTOR Author-A' | G1 merge | hardl | cut -c1-5)" "H-04 "
eq "gate H-05: repo not in allow list → ask" "$(gtf PR 'PR example-owner/unlisted#1' | G1 merge | hardl | cut -c1-5)" "H-05 "
eq "gate H-05: empty allow list → ask" "$(sed 's/^allow = .*/allow =/' "$GT/gate.conf" >"$GT/noallow.conf"; gtf - | gt merge "$GT/noallow.conf" "$GT/paths.conf" | hardl | cut -c1-5)" "H-05 "
eq "gate hard: every hard hit is listed (2 hits → 2 lines)" "$(gtf PUBLIC-CHECK 'PUBLIC-CHECK dirty' | sed 's/^EXECUTOR .*/EXECUTOR author-a/' | G1 merge | hardl | wc -l | tr -d ' ')" "2"
# hard と点数は AND: 点数が 0 でも、hard があれば allow にならない(全 hard で確かめた)
for t in 'FILE|FILE M roles/a.md' 'PUBLIC-CHECK|PUBLIC-CHECK dirty' 'TEST|TEST exit=2 sha=3f9c2ab' 'EXECUTOR|EXECUTOR author-a' 'PR|PR example-owner/zzz#1'; do
  eq "gate hard-and-score: '${t#*|}' never allow (approve)" "$(gtf "${t%%|*}" "${t#*|}" | G1 approve | rcof)" "20"
done

# --- fail-closed: 設定なし・構文エラー・入力欠損 → 21(ask と同じ扱い)。allow にならない
eq "gate fail-closed: no gate.conf → 21" "$(gtf - | gt merge "$GT/none.conf" "$GT/paths.conf" | sed -n '1p;$p' | tr '\n' '|')" "gate: ask|rc=21|"
eq "gate fail-closed: no gate-paths.conf → 21" "$(gtf - | gt merge "$GT/gate.conf" "$GT/none.conf" | rcof)" "21"
eq "gate fail-closed: reason says why (no config)" "$(gtf - | gt merge "$GT/none.conf" "$GT/paths.conf" | rsn | grep -c '^E-01 評価不能(fail-closed): 設定ファイルがありません')" "1"
bad() { printf '%s\n' "$2" >"$GT/bad.conf"; eq "gate fail-closed: bad conf ($1) → 21" "$(gtf - | gt merge "$GT/bad.conf" "$GT/paths.conf" | rcof)" "21"; }
GOODC=$(cat "$GT/gate.conf")
bad 'garbage line' "$GOODC"$'\nnot a key value'
bad 'unknown section' "$GOODC"$'\n[extra]\nx = 1'
bad 'unknown key' "$GOODC"$'\n[limits]\nbogus = 1'
bad 'bad state' "${GOODC/state = disabled/state = on}"
bad 'bad threshold' "${GOODC/merge = 1/merge = abc}"
bad 'threshold out of range' "${GOODC/merge = 1/merge = 100}"
bad 'bad repo' "${GOODC/other-owner\/other-repo/not a repo}"
bad 'bad pr-kind' "${GOODC/docs-only, tests-only/everything}"
bad 'no threshold.merge' "$(grep -v '^merge = 1' "$GT/gate.conf")"
bad 'no state' "$(grep -v '^state' "$GT/gate.conf")"
bad 'line before section' $'state = disabled\n'"$GOODC"
printf 'roles/**\nbad pattern with space\n' >"$GT/badpaths.conf"
eq "gate fail-closed: bad paths file → 21" "$(gtf - | gt merge "$GT/gate.conf" "$GT/badpaths.conf" | rcof)" "21"
printf '/etc/**\n' >"$GT/badpaths.conf"
eq "gate fail-closed: absolute path pattern → 21" "$(gtf - | gt merge "$GT/gate.conf" "$GT/badpaths.conf" | rcof)" "21"
# 入力欠損
for k in 'HEAD ' 'PR ' REVIEW TEST PUBLIC-CHECK FILE SIZE 'AUTHOR ' EXECUTOR AUTHOR-REJECT-RATE; do
  eq "gate fail-closed: missing ${k% } → 21, reason names it" "$(gtf "$k" | G1 merge | sed -n '1p;$p' | tr '\n' '|')|$(gtf "$k" | G1 merge | rsn | grep -c "入力: ${k% } がありません")" "gate: ask|rc=21||1"
done
eq "gate fail-closed: empty stdin → 21" "$(printf '' | G1 merge | rcof)" "21"
eq "gate fail-closed: all missing inputs are listed (not only the first)" "$(printf 'HEAD 3f9c2ab\n' | G1 merge | rsn | wc -l | tr -d ' ')" "9"
eq "gate fail-closed: 21 is never allow" "$(printf 'HEAD 3f9c2ab\n' | G1 merge | sed -n 1p)" "gate: ask"
badin() { eq "gate fail-closed: bad input ($1) → 21" "$(gtf "$2" "$3" | G1 merge | rcof)" "21"; }
badin 'HEAD not hex' HEAD 'HEAD zzzzzzz'
badin 'HEAD too short' HEAD 'HEAD abc'
badin 'PR form' PR 'PR example-owner/example-repo'
badin 'REVIEW extra field' REVIEW 'REVIEW a 3f9c2ab blocker=0 should=0 verdict=pass junk'
badin 'REVIEW verdict' REVIEW 'REVIEW a 3f9c2ab blocker=0 should=0 verdict=maybe'
badin 'TEST form' TEST 'TEST ok'
badin 'PUBLIC-CHECK value' PUBLIC-CHECK 'PUBLIC-CHECK fine'
badin 'FILE path traversal' FILE 'FILE M ../x'
badin 'FILE absolute path' FILE 'FILE M /etc/passwd'
badin 'FILE bad kind' FILE 'FILE X a.md'
badin 'FILE rename with one path' FILE 'FILE R a.md'
badin 'SIZE total 0' SIZE 'SIZE 1 0'
badin 'SIZE not numeric' SIZE 'SIZE a b'
badin 'REJECT-RATE value' AUTHOR-REJECT-RATE 'AUTHOR-REJECT-RATE ?'
badin 'unknown line' - 'WHATEVER x'
eq "gate fail-closed: duplicate HEAD → 21" "$(gtf - 'HEAD 3f9c2ab' | G1 merge | rcof)" "21"
eq "gate fail-closed: REVIEW none together with REVIEW lines → 21" "$(gtf - 'REVIEW none' | G1 merge | rcof)" "21"
eq "gate fail-closed: --pr differing from the recorded PR → 21" "$(gtf - | gt merge "$GT/gate.conf" "$GT/paths.conf" 'example-owner/example-repo#999' | rcof)" "21"
eq "gate: --pr equal to the recorded PR → evaluates normally" "$(gtf - | gt merge "$GT/gate.conf" "$GT/paths.conf" 'example-owner/example-repo#123' | rcof)" "0"

# --- 緩め設定は無効(しきい値の上限)
eq "gate threshold cap: merge = 5 is warned and capped at 1" "$(sed 's/^merge = 1/merge = 5/' "$GT/gate.conf" >"$GT/loose.conf"; gtf 'AUTHOR-REJECT-RATE' 'AUTHOR-REJECT-RATE high' | gt merge "$GT/loose.conf" "$GT/paths.conf" | grep -E '^(gate|score|warn)' | tr '\n' '|')" "gate: ask|score: 2 / threshold(merge) 1|warn: 緩め設定は無効: [threshold] merge = 5(上限 1 のまま)|"
eq "gate threshold: stricter setting is honored (approve = 0)" "$(sed 's/^approve = 2/approve = 0/' "$GT/gate.conf" >"$GT/strict.conf"; gtf 'AUTHOR-REJECT-RATE' 'AUTHOR-REJECT-RATE mid' | gt approve "$GT/strict.conf" "$GT/paths.conf" | sed -n 1p)" "gate: ask"
# 理由は locale に依存しない(語境界などを使わず、LC_ALL を変えても同じ)
eq "gate: same output under LC_ALL=C and ja_JP.UTF-8" "$(gtf - | LC_ALL=C G1 merge | cksum)" "$(gtf - | LC_ALL=ja_JP.UTF-8 G1 merge | cksum)"

# --- tb 経由(コマンド登録・引数・実行の口)
tbg() { "$TB" gate "$@"; }
eq "gate cmd: evaluate without --action → 2" "$(rc "$TB" gate evaluate T-1)" "2"
eq "gate cmd: evaluate bad action → 2" "$(rc "$TB" gate evaluate T-1 --action delete)" "2"
eq "gate cmd: evaluate bad chain-id → 2" "$(rc "$TB" gate evaluate 'a b' --action merge)" "2"
eq "gate cmd: evaluate without chain-id → 2" "$(rc "$TB" gate evaluate --action merge)" "2"
eq "gate cmd: no subcommand → 2" "$(rc "$TB" gate)" "2"
eq "gate cmd: unknown subcommand → 2" "$(rc "$TB" gate run T-1 --action merge)" "2"
eq "gate cmd: unknown flag → 2" "$(rc "$TB" gate evaluate T-1 --action merge --json)" "2"
# 設定の無いツリーと、実設定のあるツリーを $TMP に作って tb を呼ぶ(実チェックアウトの config の有無に左右されない)
mkiso() { mkdir -p "$1/taskboard" "$1/lib"; cp -R "$HERE/../bin" "$HERE/../lib" "$HERE/../schema" "$1/taskboard/"; cp "$HERE/../../lib/"*.sh "$1/lib/"; mkdir -p "$1/taskboard/config"; }
ISO0=$GT/iso-noconf; mkiso "$ISO0"
ISO1=$GT/iso-realconf; mkiso "$ISO1"
# 実設定: 許可リストに例のリポジトリが入っていない(= 基準の事実は H-05 で ask になる)
sed 's#^allow = .*#allow = someone-else/other-repo#' "$GT/gate.conf" >"$ISO1/taskboard/config/gate.conf"; cp "$GT/paths.conf" "$ISO1/taskboard/config/gate-paths.conf"
tbi() { local iso=$1; shift; "$iso/taskboard/bin/tb" gate "$@"; }
eq "gate cmd: no config in the tree → 21 (fail-closed)" "$(tbi "$ISO0" evaluate T-1 --action merge --facts-stdin </dev/null >/dev/null 2>&1; echo $?)" "21"
eq "gate cmd: --fact lines are accepted (still 21 without config)" "$(tbi "$ISO0" evaluate T-1 --action merge --fact 'HEAD 3f9c2ab' >/dev/null 2>&1; echo $?)" "21"
# env で有効な設定を渡しても読まない: 設定の無いツリーは 21 のまま
eq "gate cmd: env cannot supply a config (tree without config stays 21)" "$(GATE_CONF=$GT/gate.conf GATE_PATHS=$GT/paths.conf TB_GATE_CONF=$GT/gate.conf TB_GATE_PATHS=$GT/paths.conf TB_CONFIG=$GT/gate.conf; gtf - | GATE_CONF=$GT/gate.conf GATE_PATHS=$GT/paths.conf TB_GATE_CONF=$GT/gate.conf TB_GATE_PATHS=$GT/paths.conf tbi "$ISO0" evaluate T-1 --action merge --facts-stdin >/dev/null 2>&1; echo $?)" "21"
# 実設定のあるツリー: 実設定を読む(許可リスト外 → hard で 20)。env で緩い設定を渡しても結果は変わらない
eq "gate cmd: real config in the tree is read (repo not allowed → 20)" "$(gtf - | tbi "$ISO1" evaluate T-1 --action merge --facts-stdin >/dev/null 2>&1; echo $?)" "20"
eq "gate cmd: env cannot override a real config (same 20, same output)" "$(gtf - | GATE_CONF=$GT/gate.conf GATE_PATHS=$GT/paths.conf TB_GATE_CONF=$GT/gate.conf TB_GATE_PATHS=$GT/paths.conf tbi "$ISO1" evaluate T-1 --action merge --facts-stdin 2>&1 | cksum)" "$(gtf - | tbi "$ISO1" evaluate T-1 --action merge --facts-stdin 2>&1 | cksum)"
eq "gate cmd: the permissive config would have allowed (so the 20 above is the real config)" "$(gtf - | gt merge "$GT/gate.conf" "$GT/paths.conf" | rcof)" "0"
eq "gate cmd: execute stays 30 in a tree with a real config" "$(tbi "$ISO1" execute T-1 --action merge >/dev/null 2>&1; echo $?)" "30"
eq "gate cmd: execute stays 30 in a tree without config" "$(tbi "$ISO0" execute T-1 --action approve >/dev/null 2>&1; echo $?)" "30"
# execute: 常に 30(無効)。何も記録せず、何も実行しない
for a in merge approve; do
  eq "gate execute $a: always 30 (disabled)" "$(rc "$TB" gate execute T-1 --action $a)" "30"
done
eq "gate execute: 30 even when env tries to supply a config (no env door)" "$(GATE_CONF=$GT/gate.conf GATE_MODE=enabled TB_GATE_CONF=$GT/gate.conf rc "$TB" gate execute T-1 --action merge)" "30"
eq "gate execute: 30 with facts that would be allow" "$(gtf - | "$TB" gate execute T-1 --action merge >/dev/null 2>&1; echo $?)" "30"
eq "gate execute: bad args → 2 (usage), not 30" "$(rc "$TB" gate execute T-1)|$(rc "$TB" gate execute --action merge)|$(rc "$TB" gate execute T-1 --action delete)" "2|2|2"
eq "gate execute: evaluate-only flags are rejected → 2" "$(rc "$TB" gate execute T-1 --action merge --fact 'HEAD 3f9c2ab')" "2"
eq "gate execute: records nothing (db tables unchanged, no gate table)" "$(sqlite3 "$TB_DB" "SELECT count(*) FROM sqlite_master WHERE name LIKE '%gate%';")" "0"
eq "gate execute: message says disabled, on stderr" "$("$TB" gate execute T-1 --action merge 2>&1 >/dev/null | grep -c '^gate: execute は無効です')" "1"
# env で設定の場所を差し替えられない: 実ファイルが無いチェックアウトでは、env を足しても 21 のまま
eq "gate cmd: help lists gate" "$("$TB" help | grep -c 'tb gate evaluate')" "1"
eq "gate: config examples are in the repo and gitignored when real" "$(for f in gate.conf gate-paths.conf; do [[ -f $HERE/../config/$f.example ]] && git -C "$HERE" check-ignore -q "../config/$f" && echo ok; done | tr '\n' ',')" "ok,ok,"
# 例の設定は、そのままコピーして評価できる(構文が正しい)
eq "gate: the example files parse (evaluation reaches a real verdict)" "$(gtf - | gt merge "$HERE/../config/gate.conf.example" "$HERE/../config/gate-paths.conf.example" | rcof)" "0"
