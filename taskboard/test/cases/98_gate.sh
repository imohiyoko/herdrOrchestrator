# shellcheck shell=bash
# run.sh から番号順に source される。merge / approve の判定の口(tb gate。段 0: 判定は読み取りだけ、実行は常に無効)
# 設定は隔離ツリー(TB_ROOT の下の固定の置き場所)に置いて tb gate evaluate で評価する。tb 本体は env で差し替えられない
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
# レビューの記録(tb review-submit が書く定型行)。rv <task> <送信者> <系統> <sha> <blocker> <should> <verdict> → 1 行
rv() { printf 'ts=2026-11-15T00:00:00Z task=%s by=%s pr=- REVIEW %s %s blocker=%s should=%s verdict=%s evidence=-\n' "$@"; }
# mkrv <名前> <行>... → $GT/<名前>.log を作ってパスを出す
mkrv() { local n=$1; shift; printf '%s\n' "$@" >"$GT/$n.log"; printf '%s' "$GT/$n.log"; }
mkrv reviews-good "$(rv T-1 rev-a self 3f9c2ab 0 0 pass)" "$(rv T-1 ingest coderabbit 3f9c2ab 0 0 pass)" >/dev/null
# 基準の事実(docs のみ・小さい・2 系統・全部そろう)。行を足す・差し替えるテストは gtf で作る
BASE=(
  'HEAD 3f9c2ab'
  'PR example-owner/example-repo#123'
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
# 設定の無いツリーと、実設定のあるツリーを $TMP に作って tb を呼ぶ(実チェックアウトの config の有無に左右されない)
mkiso() { mkdir -p "$1/taskboard" "$1/lib" "$1/roles"; cp -R "$HERE/../bin" "$HERE/../lib" "$HERE/../schema" "$HERE/../sql" "$HERE/../perms" "$1/taskboard/"; cp "$HERE/../../lib/"*.sh "$1/lib/"; mkdir -p "$1/taskboard/config"; }
# gt は隔離ツリーに設定と記録を置いて tb gate evaluate で評価する(設定の置き場所は TB_ROOT の下に固定で、env では差し替えられない)
GTGO=$GT/iso-go; mkiso "$GTGO"; mkdir -p "$GTGO/taskboard/data"
gtcp() { rm -f "$2"; if [[ -e $1 || -L $1 ]]; then cp -P "$1" "$2"; fi; }
# gt <action> <gate.conf> <paths.conf> [pr] < 事実 → 評価の全出力。終了コードは最後の行 "rc=<n>"
gt() {
  local act=$1 conf=$2 paths=$3 pr=${4:-} facts=()
  while IFS= read -r l || [[ -n $l ]]; do facts+=("$l"); done
  gtcp "$conf" "$GTGO/taskboard/config/gate.conf"; gtcp "$paths" "$GTGO/taskboard/config/gate-paths.conf"; gtcp "${RVF:-$GT/reviews-good.log}" "$GTGO/taskboard/data/gate-reviews.log"
  printf '%s\n' "${facts[@]}" | "$GTGO/taskboard/bin/tb" gate evaluate T-1 --action "$act" ${pr:+--pr "$pr"} --facts-stdin; echo "rc=$?"
}
G1() { gt "$1" "$GT/gate.conf" "$GT/paths.conf"; }
hardl() { grep '^hard:' | sed 's/^hard: //'; }
rsn() { grep '^reason:' | sed 's/^reason: //'; }
rcof() { grep '^rc=' | sed 's/^rc=//'; }

# --- 基準: 全部 0 点 → allow(approve も merge も)
for a in approve merge; do
  eq "gate $a: clean docs-only PR → allow, exit 0" "$(gtf - | G1 $a | sed -n '1p;$p' | tr '\n' '|')" "gate: allow|rc=0|"
done
eq "gate: output has score/hard lines" "$(gtf - | G1 merge | sed -n '5,7p' | cut -d' ' -f1-3 | tr '\n' '|')" "score: 0 /|hard: none|reason: REC self|"
eq "gate: reasons list every contribution (one line each)" "$(gtf - | G1 merge | rsn | cut -d' ' -f1-2 | tr '\n' ',')" "REC self,REC coderabbit,S-01 +0,P-01 +0,K-01 +0,T-01 +0,R-01 +0,A-01 +0,"
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
# R-01(レビューは記録から読む)
rr() { gtf - | RVF=$(mkrv rr "$@") G1 approve | rsn | grep '^R-01'; }
eq "gate R-01: should remains → +1" "$(rr "$(rv T-1 rev-a self 3f9c2ab 0 2 pass)" "$(rv T-1 ingest coderabbit 3f9c2ab 0 0 pass)")" "R-01 +1 レビュー: should が残っている"
eq "gate R-01: two independent pass → +0" "$(rr "$(rv T-1 rev-a self 3f9c2ab 0 0 pass)" "$(rv T-1 ingest coderabbit 3f9c2ab 0 0 pass)")" "R-01 +0 レビュー: 2 系統とも blocker=0 should=0 sha 一致"
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
for p in '.github/workflows/ci.yml' 'taskboard/config/gate.conf' 'taskboard/config/gate-paths.conf' 'taskboard/lib/gate.sh' 'taskboard/config/approvals.conf' 'taskboard/config/policy.conf' 'taskboard/perms/ops.tbl' 'x/config/risk-rules.conf' 'gate.conf'; do
  eq "gate H-01 builtin: $p hits with an empty paths file" "$(gtf FILE "FILE M $p" | gt merge "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)" "H-01 "
done
for p in 'taskboard/bin/tb' 'bin/tb' 'tests/run-all.sh' 'tests/affected.sh' 'taskboard/test/run.sh' 'webui/tests/run.sh' 'taskboard/test/cases/98_gate.sh' 'tests/test_gate.sh' '.github'; do
  eq "gate H-01 builtin: $p hits (merge and approve)" "$(gtf FILE "FILE M $p" | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)|$(gtf FILE "FILE M $p" | gt merge "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)" "H-01 |H-01 "
done
# パスの正規形に頼らない: 不正は評価不能(21)。規則を回避できない
for p in './.github/x' '././.github/workflows/ci.yml' './bin/tb' 'a/../.github/x' '../x' '/etc/x' 'a//b' 'docs/' 'docs/./x.md' '.' '..' 'a/.'; do
  eq "gate path form: '$p' → 21 (not allow)" "$(gtf FILE "FILE M $p" | G1 approve | rcof)" "21"
done
eq "gate path form: rename with a bad new path → 21" "$(gtf FILE 'FILE R docs/a.md ././x' | G1 approve | rcof)" "21"
eq "gate path form: dot-prefixed names are fine (.github/x hits H-01, not 21)" "$(gtf FILE 'FILE M .github/x' | G1 approve | rcof)" "20"
eq "gate path form: upper-case .GITHUB hits" "$(gtf FILE 'FILE M .GITHUB/workflows/ci.yml' | G1 approve | rcof)" "20"
# docs 配下は拡張子の許可リストだけが docs。それ以外は一般のコード(+1)
for p in docs/x.js docs/x.yml docs/x.plist docs/Makefile docs/deploy docs/RUN.SH docs/run.sh docs/x.py docs/.hidden sub/docs/a.json; do
  eq "gate P-01: $p is code, not docs (+1)" "$(pk "FILE M $p" | cut -d'|' -f1)" "P-01 +1: 一般のコード"
done
for p in docs/a.md docs/a.txt docs/a.rst docs/a.PNG docs/a.svg docs/a.jpg docs/a.jpeg docs/a.gif README.md sub/docs/b.md; do
  eq "gate P-01: $p is docs (+0)" "$(pk "FILE M $p" | cut -d'|' -f1)" "P-01 +0: docs・tests のみ"
done
eq "gate P-01: docs/x.js + docs/a.md → mixed" "$(pk 'FILE M docs/a.md' 'FILE M docs/x.js' | cut -d'|' -f2)" "K-01 +2: mixed"
eq "gate H-01: an ordinary test file does not hit" "$(gtf FILE 'FILE M taskboard/test/cases/10_tasks.sh' | G1 merge | hardl)" "none"
eq "gate P-01: executable under docs/ is code, not docs (+1)" "$(pk 'FILE M docs/run.sh' | cut -d'|' -f1)" "P-01 +1: 一般のコード"
eq "gate P-01: bin/ is important (+2)" "$(pk 'FILE M tool/bin/x' | cut -d'|' -f1)" "P-01 +2: lib・config・scheduler 配下(重要度 高)"
eq "gate N-2: control characters in an unknown line are not echoed" "$(gtf - $'\033[31mBAD\001x' | G1 merge | grep -c $'[\001\033]')" "0"
eq "gate threshold cap: approve = 9 is warned and capped at 2" "$(sed 's/^approve = 2/approve = 9/' "$GT/gate.conf" >"$GT/loose2.conf"; gtf - | gt approve "$GT/loose2.conf" "$GT/paths.conf" | grep -E '^(score|warn)' | tr '\n' '|')" "score: 0 / threshold(approve) 2|warn: 緩め設定は無効: [threshold] approve = 9(上限 2 のまま)|"
eq "gate H-06: type/mode change → ask" "$(gtf FILE 'FILE T docs/a.md' | G1 merge | hardl | cut -c1-5)" "H-06 "
eq "gate H-02: public check dirty → ask" "$(gtf PUBLIC-CHECK 'PUBLIC-CHECK dirty' | G1 merge | hardl | cut -c1-5)" "H-02 "
eq "gate H-02: public check unavailable → ask" "$(gtf PUBLIC-CHECK 'PUBLIC-CHECK unavailable' | G1 merge | hardl | cut -c1-5)" "H-02 "
h3() { gtf - | RVF=$(mkrv h3 "$@") G1 merge | hardl | grep '^H-03' | cut -c1-5 | sort -u | tr '\n' ','; }
GOODR=("$(rv T-1 rev-a self 3f9c2ab 0 0 pass)" "$(rv T-1 ingest coderabbit 3f9c2ab 0 0 pass)")
eq "gate H-03: baseline reviews → no H-03" "$(h3 "${GOODR[@]}")" ""
eq "gate H-03: review blocker → ask" "$(h3 "$(rv T-1 rev-a self 3f9c2ab 1 0 fail)" "${GOODR[1]}")" "H-03 ,"
eq "gate H-03: review verdict=fail → ask" "$(h3 "$(rv T-1 rev-a self 3f9c2ab 0 0 fail)" "${GOODR[1]}")" "H-03 ,"
eq "gate H-03: a later fail on the same sha beats an earlier pass" "$(h3 "${GOODR[@]}" "$(rv T-1 rev-a self 3f9c2ab 0 0 fail)")" "H-03 ,"
eq "gate H-03: stale review (sha mismatch) is ignored → fewer than 2 lineages → ask" "$(h3 "$(rv T-1 rev-a self 1111111 0 0 pass)" "${GOODR[1]}")" "H-03 ,"
eq "gate H-03: stale review is listed as invalid in the reasons" "$(gtf - | RVF=$(mkrv h3 "$(rv T-1 rev-a self 1111111 0 0 pass)" "${GOODR[1]}") G1 merge | rsn | grep -c '^REC self 1111111 .* → 無効: sha が head と一致しない')" "1"
eq "gate H-03: only one lineage → ask" "$(h3 "${GOODR[0]}")" "H-03 ,"
eq "gate H-03: two lineages by the same sender are not independent → ask" "$(h3 "$(rv T-1 rev-a self 3f9c2ab 0 0 pass)" "$(rv T-1 rev-a coderabbit 3f9c2ab 0 0 pass)")" "H-03 ,"
eq "gate H-03: same lineage by two senders counts once → ask" "$(h3 "$(rv T-1 rev-a self 3f9c2ab 0 0 pass)" "$(rv T-1 rev-b self 3f9c2ab 0 0 pass)")" "H-03 ,"
eq "gate H-03: records of another chain are not counted → ask" "$(h3 "$(rv T-2 rev-a self 3f9c2ab 0 0 pass)" "$(rv T-2 ingest coderabbit 3f9c2ab 0 0 pass)")" "H-03 ,"
eq "gate H-03: no record for this chain (file exists) → ask, reason says so" "$(gtf - | RVF=$(mkrv h3 "$(rv T-2 rev-a self 3f9c2ab 0 0 pass)") G1 merge | hardl | grep -c '有効なレビューの記録が無い')" "1"
eq "gate H-03: a record whose PR differs from the target is invalid" "$(h3 "$(rv T-1 rev-a self 3f9c2ab 0 0 pass | sed 's| pr=- | pr=other/repo#9 |')" "${GOODR[1]}")" "H-03 ,"
eq "gate H-03: a record with the same PR is valid" "$(h3 "$(rv T-1 rev-a self 3f9c2ab 0 0 pass | sed 's| pr=- | pr=example-owner/example-repo#123 |')" "${GOODR[1]}")" ""
eq "gate R-01: which lineage recorded what is listed (REC lines)" "$(gtf - | G1 merge | rsn | grep '^REC' | cut -d' ' -f1-5 | tr '\n' '|')" "REC self 3f9c2ab blocker=0 should=0|REC coderabbit 3f9c2ab blocker=0 should=0|"
eq "gate R-01: REC line names the sender and time" "$(gtf - | G1 merge | rsn | grep '^REC self' | grep -c 'by=rev-a at=2026-11-15T00:00:00Z → 有効')" "1"
# human の記録は独立 2 系統の根拠に数えない(pane の無い実行は誰でも名乗れるため)
eq "gate H-03: a human record does not count as the second lineage → ask" "$(h3 "${GOODR[0]}" "$(rv T-1 human coderabbit 3f9c2ab 0 0 pass)")" "H-03 ,"
eq "gate H-03: two human records (even different lineages) → ask" "$(h3 "$(rv T-1 human self 3f9c2ab 0 0 pass)" "$(rv T-1 human coderabbit 3f9c2ab 0 0 pass)")" "H-03 ,"
eq "gate H-03: the reason says human is not counted, with the counted number" "$(gtf - | RVF=$(mkrv h3 "${GOODR[0]}" "$(rv T-1 human coderabbit 3f9c2ab 0 0 pass)") G1 merge | hardl | grep -c '数えられた系統: 1。送信者 human と自称の外部系統は数えない')" "1"
eq "gate H-03: a human record that FAILS still blocks (fail counts from anyone)" "$(h3 "${GOODR[@]}" "$(rv T-1 human lint 3f9c2ab 1 0 fail)")" "H-03 ,"
eq "gate H-03: two different agent senders → allowed path (no H-03)" "$(h3 "$(rv T-1 rev-a self 3f9c2ab 0 0 pass)" "$(rv T-1 rev-b lint 3f9c2ab 0 0 pass)")" ""
# failed の系統は数えない(この行を消すと、self の pass(rev-b)が数えられて 2 系統になり、理由の数が変わる)
eq "gate H-03: a failed lineage is excluded from the count even if another sender passed it" "$(gtf - | RVF=$(mkrv h3 "$(rv T-1 rev-a self 3f9c2ab 0 0 fail)" "$(rv T-1 rev-b self 3f9c2ab 0 0 pass)" "$(rv T-1 rev-c lint 3f9c2ab 0 0 pass)") G1 merge | hardl | grep -c '数えられた系統: 1。')" "1"
# 外部系統(coderabbit)は、送信者 ingest の記録だけが独立な系統に数えられる(自称は数えない)。自称の fail は、ほかの fail と同じく効く
eq "gate H-03: a self-declared coderabbit (by an agent) does not count → ask" "$(h3 "${GOODR[0]}" "$(rv T-1 rev-b coderabbit 3f9c2ab 0 0 pass)")" "H-03 ,"
eq "gate H-03: the reason for a self-declared coderabbit is shown on the REC line" "$(gtf - | RVF=$(mkrv h3 "${GOODR[0]}" "$(rv T-1 rev-b coderabbit 3f9c2ab 0 0 pass)") G1 merge | rsn | grep -c '^REC coderabbit .* → 有効(独立系統には数えない: 外部系統は送信者 ingest の記録だけが数えられる(自称)')" "1"
eq "gate H-03: a self-declared coderabbit that FAILS still blocks" "$(h3 "${GOODR[@]}" "$(rv T-1 rev-b coderabbit 3f9c2ab 1 0 fail)")" "H-03 ,"
eq "gate H-03: sender ingest on a non-external lineage does not count → ask" "$(h3 "${GOODR[0]}" "$(rv T-1 ingest lint 3f9c2ab 0 0 pass)")" "H-03 ,"
eq "gate H-03: ingest's coderabbit + one agent's self → no H-03 (the intended pair)" "$(h3 "${GOODR[@]}")" ""
eq "gate H-03: ingest's coderabbit alone is not enough → ask" "$(h3 "${GOODR[1]}")" "H-03 ,"
eq "gate H-03: ingest's coderabbit + a human self is not enough → ask" "$(h3 "$(rv T-1 human self 3f9c2ab 0 0 pass)" "${GOODR[1]}")" "H-03 ,"
eq "gate H-03: ingest's coderabbit with a stale sha is ignored → ask" "$(h3 "${GOODR[0]}" "$(rv T-1 ingest coderabbit 1111111 0 0 pass)")" "H-03 ,"
eq "gate H-03: ingest's coderabbit with blocker (fail) blocks" "$(h3 "${GOODR[0]}" "$(rv T-1 ingest coderabbit 3f9c2ab 2 0 fail)")" "H-03 ,"
# 記録が無い・読めない・形式違反 → 21(fail-closed)
eq "gate fail-closed: no review file → 21" "$(gtf - | RVF=$GT/none.log G1 merge | sed -n '1p;$p' | tr '\n' '|')" "gate: ask|rc=21|"
eq "gate fail-closed: reason names the missing review record" "$(gtf - | RVF=$GT/none.log G1 merge | rsn | grep -c 'レビューの記録がありません')" "1"
printf 'ts=bad\n' >"$GT/badrec.log"
eq "gate fail-closed: malformed review line → 21" "$(gtf - | RVF=$GT/badrec.log G1 merge | rcof)" "21"
for bad in 'REVIEW self 3f9c2ab blocker=0 should=0 verdict=pass' "$(rv T-1 a self 3f9c2ab 0 0 maybe)" "$(rv T-1 a self 3f9c2ab 0 0 pass) junk" "$(rv T-1 a SELF 3f9c2ab 0 0 pass)" "$(rv T-1 a self zzzzzzz 0 0 pass)" "# comment" "$(rv 'T 1' a self 3f9c2ab 0 0 pass)"; do
  eq "gate fail-closed: malformed review line '${bad:0:40}' (even beside good ones) → 21" "$(gtf - | RVF=$(mkrv badrec "${GOODR[@]}" "$bad") G1 merge | rcof)" "21"
done
eq "gate fail-closed: blank lines in the record are ignored" "$(gtf - | RVF=$(mkrv blank "${GOODR[0]}" "" "${GOODR[1]}") G1 merge | rcof)" "0"
ln -sf "$GT/reviews-good.log" "$GT/link.log"
eq "gate fail-closed: a symlinked record file → 21" "$(gtf - | RVF=$GT/link.log G1 merge | rcof)" "21"
eq "gate fail-closed: REVIEW lines in the input are rejected (the record is the only source)" "$(gtf - 'REVIEW self 3f9c2ab blocker=0 should=0 verdict=pass' | G1 merge | rcof)" "21"
eq "gate fail-closed: REVIEW in the input is not counted even if it would make a pass" "$(gtf - 'REVIEW a 3f9c2ab blocker=0 should=0 verdict=pass' | RVF=$(mkrv one "${GOODR[0]}") G1 merge | rcof)" "21"
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
for k in 'HEAD ' 'PR ' TEST PUBLIC-CHECK FILE SIZE 'AUTHOR ' EXECUTOR AUTHOR-REJECT-RATE; do
  eq "gate fail-closed: missing ${k% } → 21, reason names it" "$(gtf "$k" | G1 merge | sed -n '1p;$p' | tr '\n' '|')|$(gtf "$k" | G1 merge | rsn | grep -c "入力: ${k% } がありません")" "gate: ask|rc=21||1"
done
eq "gate fail-closed: empty stdin → 21" "$(printf '' | G1 merge | rcof)" "21"
eq "gate fail-closed: all missing inputs are listed (not only the first)" "$(printf 'HEAD 3f9c2ab\n' | G1 merge | rsn | wc -l | tr -d ' ')" "8"
eq "gate fail-closed: 21 is never allow" "$(printf 'HEAD 3f9c2ab\n' | G1 merge | sed -n 1p)" "gate: ask"
badin() { eq "gate fail-closed: bad input ($1) → 21" "$(gtf "$2" "$3" | G1 merge | rcof)" "21"; }
badin 'HEAD not hex' HEAD 'HEAD zzzzzzz'
badin 'HEAD too short' HEAD 'HEAD abc'
badin 'PR form' PR 'PR example-owner/example-repo'
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
ISO0=$GT/iso-noconf; mkiso "$ISO0"
ISO1=$GT/iso-realconf; mkiso "$ISO1"
# 実設定: 許可リストに例のリポジトリが入っていない(= 基準の事実は H-05 で ask になる)
sed 's#^allow = .*#allow = someone-else/other-repo#' "$GT/gate.conf" >"$ISO1/taskboard/config/gate.conf"; cp "$GT/paths.conf" "$ISO1/taskboard/config/gate-paths.conf"; mkdir -p "$ISO1/taskboard/data"; cp "$GT/reviews-good.log" "$ISO1/taskboard/data/gate-reviews.log"
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

# --- 組み込みの hard(基準: ガバナンス・権限・コードの取得や実行の入口)。規則 1 件につき、根直下・深い階層・大文字小文字を approve と merge の両方で確かめる
BUILTIN_HARD=(
  CODEOWNERS docs/CODEOWNERS .github/CODEOWNERS .coderabbit.yaml
  .github/workflows/ci.yml .github/copilot-instructions.md
  .claude/settings.json .claude/hooks/pre.sh .claude/commands/x.md .claude/agents/a.md .claude/skills/s/SKILL.md .claude/settings.local.json
  CLAUDE.md CLAUDE.local.md AGENTS.md GEMINI.md copilot-instructions.md
  .cursor/rules/a.mdc .cursorrules .windsurf/rules/a.md .windsurfrules .clinerules .clinerules.md .roo/rules/a.md .continue/config.json .codex/config.toml .gemini/settings.json
  mcp.json .vscode/mcp.json .vscode/tasks.json .vscode/settings.json
  .githooks/pre-commit .husky/pre-commit .gitattributes .gitmodules .pre-commit-config.yaml .envrc .mcp.json .devcontainer/devcontainer.json .circleci/config.yml Jenkinsfile
  Makefile docs/Makefile GNUmakefile Dockerfile Dockerfile.dev app.dockerfile docker-compose.yml docker-compose.override.yaml
  package.json package-lock.json yarn.lock pnpm-lock.yaml bun.lockb .npmrc .yarnrc .yarnrc.yml
  requirements.txt requirements-dev.txt requirements/base.txt Pipfile Pipfile.lock poetry.lock uv.lock pyproject.toml setup.py setup.cfg pip.conf pip.ini .pip/pip.conf .pypirc
  go.mod go.sum go.work go.work.sum Cargo.toml Cargo.lock Gemfile Gemfile.lock composer.json composer.lock pom.xml build.gradle build.gradle.kts
  .claude.json .agents/skills/a.md .agents/x AGENT.md .aider.conf.yml .aider.conf.yaml
  conftest.py tests/conftest.py tox.ini noxfile.py .tool-versions .mise.toml mise.toml mise.local.toml .mise.local.toml .mise/config.toml .config/mise.toml .config/mise/config.toml mise/config.toml .cargo/config .cargo/config.toml
  justfile Justfile .justfile Taskfile.yml Taskfile.yaml Procfile lefthook.yml .lefthook.yml lefthook.yaml .pre-commit-hooks.yaml
  .bazelrc .bazelrc.user MODULE.bazel WORKSPACE WORKSPACE.bazel
  .gitlab-ci.yml .gitlab/ci/a.yml .travis.yml azure-pipelines.yml azure-pipelines-ci.yml .azure-pipelines/a.yml bitbucket-pipelines.yml cloudbuild.yaml cloudbuild.yml appveyor.yml .appveyor.yml .drone.yml buildspec.yml buildspec-test.yml
  renovate.json renovate.json5 .renovaterc .renovaterc.json .dependabot/config.yml
  .bundle/config .gemrc bunfig.toml .yarn/releases/yarn.cjs .yarn/plugins/a.cjs pnpm-workspace.yaml gradlew gradlew.bat mvnw mvnw.cmd .mvn/wrapper/maven-wrapper.properties .mvn/settings.xml gradle.properties settings.gradle settings.gradle.kts .terraform.lock.hcl Brewfile Brewfile.lock.json flake.nix flake.lock environment.yml
  Podfile Podfile.lock Package.swift Package.resolved mix.exs mix.lock pubspec.yaml pubspec.lock build.sbt deno.json deno.jsonc deno.lock
)
for p in "${BUILTIN_HARD[@]}"; do
  up=$(printf %s "$p" | tr a-z A-Z)
  eq "gate builtin hard: $p → ask in approve and merge (root)" "$(for a in approve merge; do gtf FILE "FILE M $p" | gt $a "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5; done | tr '\n' '|')" "H-01 |H-01 |"
  eq "gate builtin hard: $p → ask when deep (a/b/c/d/)" "$(gtf FILE "FILE M a/b/c/d/$p" | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)" "H-01 "
  eq "gate builtin hard: $p → ask in upper case" "$(gtf FILE "FILE M $up" | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5)" "H-01 "
done
# 見送ったもの(単一の値の固定・通常のコード・説明の文書)は hard にしない。名前が近い文書も当たらない
for p in .nvmrc .python-version .ruby-version CMakeLists.txt BUILD .editorconfig .prettierrc tsconfig.json docs/tox-notes.md docs/conftest-usage.md notes/justfile-tips.md docs/cargo-config.md docs/mise-intro.md notes/renovate-guide.md notes/mvnw-notes.md docs/gradlew-usage.md notes/procfile-tips.md; do
  eq "gate builtin hard (not a hit, deferred): $p" "$(gtf FILE "FILE M $p" | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl)" "none"
done
# 実設定のある隔離ツリーでも、追加した名前の結果は同じ(組み込みは設定で外せない)
for p in .claude.json AGENT.md conftest.py .cargo/config.toml .mise.toml .gitlab-ci.yml renovate.json; do
  eq "gate builtin hard: $p same in a tree with a real config" "$(gtf FILE "FILE M $p" | tbi "$ISO1" evaluate T-1 --action merge --facts-stdin | grep -c '^hard: H-01 ')" "1"
done
# 隣の名前は hard にならない(入れすぎない)
for p in README.md docs/guide.md src/package.json.md Makefile.md CODEOWNERS.md docs/docker-notes.md notes/requirements.md; do
  eq "gate builtin hard: $p is not a hit" "$(gtf FILE "FILE M $p" | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl)" "none"
done
# 改名・削除・追加でも、旧パスと新パスの両方を見る
eq "gate builtin hard: rename onto CODEOWNERS hits" "$(gtf FILE 'FILE R docs/a.md docs/CODEOWNERS' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate builtin hard: delete of a lockfile hits" "$(gtf FILE 'FILE D web/yarn.lock' | G1 merge | hardl | cut -c1-5)" "H-01 "
eq "gate builtin hard: add of a Dockerfile hits" "$(gtf FILE 'FILE A svc/Dockerfile' | G1 merge | hardl | cut -c1-5)" "H-01 "
# 実設定の有無・中身によらず同じ結果(パス規則ファイルが空 / 例と同じ / 実設定のあるツリー)
eq "gate builtin hard: same hit with an empty paths file, the example paths file, and a real config tree" "$(
  f=$(gtf FILE 'FILE M docs/CODEOWNERS')
  printf '%s\n' "$f" | gt merge "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | grep -c '^H-01 '
  printf '%s\n' "$f" | gt merge "$GT/gate.conf" "$HERE/../config/gate-paths.conf.example" | hardl | grep -c '^H-01 '
  printf '%s\n' "$f" | tbi "$ISO1" evaluate T-1 --action merge --facts-stdin | grep -c '^hard: H-01 ' )" "$(printf '1\n1\n1')"
# 点数の分類も大文字小文字を区別しない(N-B)
for p in lib/db.sh Lib/db.sh LIB/x.sh taskboard/Lib/db.sh taskboard/CONFIG/x.conf Webui/Scheduler/x.py; do
  eq "gate P-01 case-insensitive: $p → +2" "$(pk "FILE M $p" | cut -d'|' -f1)" "P-01 +2: lib・config・scheduler 配下(重要度 高)"
done
eq "gate P-01 case-insensitive: Docs dir with .MD is docs" "$(pk 'FILE M DOCS/A.MD' | cut -d'|' -f1)" "P-01 +0: docs・tests のみ"
# docs の分類の細部(N-C): 先頭の . だけの名前や、末尾の . は拡張子なし = コード
for p in docs/.md docs/a. docs/a.md.js docs/.txt docs/dir.md/x; do
  eq "gate P-01 docs detail: $p is code (+1)" "$(pk "FILE M $p" | cut -d'|' -f1)" "P-01 +1: 一般のコード"
done

# --- tb review-submit(レビュー結果の記録の口)。隔離ツリー(自分の roles/ と data/ を持つ)で呼ぶ。pane は疑似 herdr の agent 一覧で決まる
ISO2=$GT/iso-submit; mkiso "$ISO2"
printf '# reviewer\n' >"$ISO2/roles/reviewer.md"; printf '# worker\n' >"$ISO2/roles/worker.md"
printf 'rev-* = reviewer\nworker* = worker\n' >"$ISO2/roles/map.conf"
RVLOG=$ISO2/taskboard/data/gate-reviews.log
SUBT=$($TB add -p gt -t "gate submit target")
$TB claim "$SUBT" --owner worker1 --session s-sub >/dev/null 2>&1
NOOWN=$($TB add -p gt -t "gate no owner")
AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"s1"}},{"pane_id":"wR:p1","name":"rev-a","agent_session":{"value":"s2"}},{"pane_id":"wR:p2","name":"rev-b","agent_session":{"value":"s3"}},{"pane_id":"wW:p2","agent_session":{"value":"s4"}},{"pane_id":"wX:p1","name":"worker9","agent_session":{"value":"s5"}}]'
# sub <pane|-(人)> <task> 引数... → 終了コード。pane の agent 一覧は AGENTS
sub() { local pane=$1; shift; if [[ $pane == - ]]; then ( unset HERDR_PANE_ID; FAKE_AGENTS=$AGENTS "$ISO2/taskboard/bin/tb" review-submit "$@" ) >/dev/null 2>&1; else ( HERDR_PANE_ID=$pane FAKE_AGENTS=$AGENTS "$ISO2/taskboard/bin/tb" review-submit "$@" ) >/dev/null 2>&1; fi; echo $?; }
subo() { local pane=$1; shift; ( HERDR_PANE_ID=$pane FAKE_AGENTS=$AGENTS "$ISO2/taskboard/bin/tb" review-submit "$@" ) 2>&1; }
OKARGS=(--lineage self --sha 3f9c2ab --blocker 0 --should 1 --verdict pass)
eq "review-submit: no log before the first record" "$([[ -e $RVLOG ]] && echo yes || echo no)" "no"
eq "review-submit: reviewer role (rev-a) records → 0" "$(sub wR:p1 "$SUBT" "${OKARGS[@]}")" "0"
eq "review-submit: the line has the fixed form (time, task, sender from the pane)" "$(grep -cE "^ts=[0-9T:Z-]+ task=$SUBT by=rev-a pr=- REVIEW self 3f9c2ab blocker=0 should=1 verdict=pass evidence=-$" "$RVLOG")" "1"
eq "review-submit: the log is exactly one line" "$(wc -l <"$RVLOG" | tr -d ' ')" "1"
FIRST=$(sed -n 1p "$RVLOG")
eq "review-submit: sender comes from the pane, not an argument (an argument for it is a usage error)" "$(sub wR:p1 "$SUBT" "${OKARGS[@]}" --by someone)" "2"
eq "review-submit: second record by another reviewer (rev-b) → 0, lineage other" "$(sub wR:p2 "$SUBT" --lineage lint --sha 3f9c2ab --blocker 0 --should 0 --verdict pass --pr example-owner/example-repo#123 --evidence reviews/r1.md)" "0"
eq "review-submit: append-only (first line byte-identical, 2 lines)" "$(sed -n 1p "$RVLOG")|$(wc -l <"$RVLOG" | tr -d ' ')" "$FIRST|2"
eq "review-submit: pr and evidence are recorded" "$(sed -n 2p "$RVLOG" | grep -c 'by=rev-b pr=example-owner/example-repo#123 .* evidence=reviews/r1.md$')" "1"
SIZE1=$(wc -c <"$RVLOG" | tr -d ' ')
# 職務分掌
eq "review-submit: author (task owner worker1) is refused → 4" "$(sub wW:p1 "$SUBT" "${OKARGS[@]}")" "4"
eq "review-submit: refused author wrote nothing" "$(wc -c <"$RVLOG" | tr -d ' ')" "$SIZE1"
eq "review-submit: a non-reviewer agent (worker9) is refused → 5" "$(sub wX:p1 "$SUBT" "${OKARGS[@]}")" "5"
eq "review-submit: refusal message names the role" "$(subo wX:p1 "$SUBT" "${OKARGS[@]}" | grep -c 'worker')" "1"
eq "review-submit: an agent cannot claim the external lineage → 5" "$(sub wR:p1 "$SUBT" --lineage coderabbit --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)" "5"
eq "review-submit: task without owner: agent refused (author unknown) → 4" "$(sub wR:p1 "$NOOWN" "${OKARGS[@]}")" "4"
eq "review-submit: unnamed agent pane → 3" "$(sub wW:p2 "$SUBT" "${OKARGS[@]}")" "3"
eq "review-submit: pane not in the agent list → 3" "$(sub wZ:p9 "$SUBT" "${OKARGS[@]}")" "3"
eq "review-submit: agent list fails → 3" "$(FAKE_LIST_FAIL=1 sub wR:p1 "$SUBT" "${OKARGS[@]}")" "3"
eq "review-submit: nothing written by any refusal" "$(wc -c <"$RVLOG" | tr -d ' ')" "$SIZE1"
eq "review-submit: human (no pane) may record the external lineage → 0, by=human" "$(sub - "$SUBT" --lineage coderabbit --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)|$(tail -1 "$RVLOG" | grep -c ' by=human ')" "0|1"
# 役割は env で変えられない: TB_ROLES で worker9 を reviewer にしようとしても効かない
mkdir -p "$GT/fake-roles"; printf '# reviewer\n' >"$GT/fake-roles/reviewer.md"; printf 'worker* = reviewer\n' >"$GT/fake-roles/map.conf"
eq "review-submit: TB_ROLES cannot make an agent a reviewer → 5" "$(TB_ROLES=$GT/fake-roles sub wX:p1 "$SUBT" "${OKARGS[@]}")" "5"
eq "review-submit: an env var cannot move the log (no log elsewhere)" "$(TB_GATE_REVIEWS=$GT/elsewhere.log GATE_REVIEWS=$GT/elsewhere.log sub wR:p1 "$SUBT" --lineage extra --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)|$([[ -e $GT/elsewhere.log ]] && echo moved || echo fixed)" "0|fixed"
# 使い方・形式
eq "review-submit: unknown task → 1" "$(sub - T-gt-999 "${OKARGS[@]}")" "1"
eq "review-submit: bad task id → 2" "$(sub - 'x y' "${OKARGS[@]}")" "2"
for bad in "--lineage Self" "--lineage a_b" "--sha XYZ" "--sha abc" "--blocker -1" "--blocker x" "--should 99999" "--verdict maybe" "--pr nope" "--evidence a\ b" "--evidence ../../etc/passwd;rm"; do
  args=("${OKARGS[@]}"); read -r k v <<<"$bad"; args+=("$k" "$v")
  # 後ろの指定が前を上書きする
  eq "review-submit: bad value ($bad) → 2 and nothing written" "$(sub - "$SUBT" "${args[@]}")|$(wc -c <"$RVLOG" | tr -d ' ')" "2|$(wc -c <"$RVLOG" | tr -d ' ')"
done
eq "review-submit: a missing required option → 2" "$(sub - "$SUBT" --lineage self --sha 3f9c2ab --blocker 0 --should 0)" "2"
eq "review-submit: verdict=pass with blockers is not recordable → 2" "$(sub - "$SUBT" --lineage self --sha 3f9c2ab --blocker 2 --should 0 --verdict pass)" "2"
eq "review-submit: verdict=fail with blockers → 0" "$(sub - "$SUBT" --lineage self --sha 3f9c2ab --blocker 2 --should 0 --verdict fail)" "0"
# 記録は gate evaluate が読める(往復)。記録を実ツリーに依存させない: 同じ記録ファイルを lib の評価に渡す
ROUND=$GT/roundtrip.log; cp "$RVLOG" "$ROUND"
eq "review-submit: every line written parses in the evaluator (no malformed line)" "$(gtf - | RVF=$ROUND G1 merge | rcof)" "20"
eq "review-submit: records of another chain are not shown for chain T-1" "$(gtf - | RVF=$ROUND G1 merge | rsn | grep -c '^REC ')" "0"
# 記録の置き場の symlink は書かない
ISO3=$GT/iso-link; mkiso "$ISO3"; cp -R "$ISO2/roles" "$ISO3/"; mkdir -p "$ISO3/taskboard/data"; : >"$GT/linktarget.log"; ln -s "$GT/linktarget.log" "$ISO3/taskboard/data/gate-reviews.log"
eq "review-submit: a symlinked log is refused and the target stays empty" "$( ( unset HERDR_PANE_ID; "$ISO3/taskboard/bin/tb" review-submit "$SUBT" "${OKARGS[@]}" ) >/dev/null 2>&1; echo $?)|$(wc -c <"$GT/linktarget.log" | tr -d ' ')" "1|0"
# 実設定の有無に左右されない(評価側は固定の記録を読む。review-submit は gate の設定を読まない)
eq "review-submit: does not depend on gate.conf (tree without config records fine)" "$([[ -e $ISO2/taskboard/config/gate.conf ]] && echo conf || echo noconf)" "noconf"
cp "$GT/gate.conf" "$ISO2/taskboard/config/gate.conf"; cp "$GT/paths.conf" "$ISO2/taskboard/config/gate-paths.conf"
eq "review-submit: same result with a real gate.conf in the tree" "$(sub wR:p1 "$SUBT" --lineage third --sha 3f9c2ab --blocker 0 --should 0 --verdict pass)|$(sub wX:p1 "$SUBT" "${OKARGS[@]}")|$(sub wW:p1 "$SUBT" "${OKARGS[@]}")" "0|5|4"
# tb 経由の evaluate が、記録を読む(隔離ツリー + 実設定 + 記録)
ISO4=$GT/iso-eval; mkiso "$ISO4"; cp "$GT/gate.conf" "$ISO4/taskboard/config/gate.conf"; cp "$GT/paths.conf" "$ISO4/taskboard/config/gate-paths.conf"; mkdir -p "$ISO4/taskboard/data"
eq "gate cmd: tb evaluate without a review record file → 21" "$(gtf - | "$ISO4/taskboard/bin/tb" gate evaluate T-1 --action merge --facts-stdin >/dev/null 2>&1; echo $?)" "21"
cp "$GT/reviews-good.log" "$ISO4/taskboard/data/gate-reviews.log"
eq "gate cmd: tb evaluate reads the fixed record file → allow (0)" "$(gtf - | "$ISO4/taskboard/bin/tb" gate evaluate T-1 --action merge --facts-stdin 2>&1 | sed -n 1p)" "gate: allow"
eq "gate cmd: env cannot point evaluate at another record file" "$(gtf - | GATE_REVIEWS=$GT/none.log TB_GATE_REVIEWS=$GT/none.log "$ISO4/taskboard/bin/tb" gate evaluate T-1 --action merge --facts-stdin 2>&1 | sed -n 1p)" "gate: allow"
eq "gate cmd: execute is still 30 and writes nothing to the record" "$(S=$(wc -c <"$ISO4/taskboard/data/gate-reviews.log"); "$ISO4/taskboard/bin/tb" gate execute T-1 --action merge >/dev/null 2>&1; echo "$?|$([[ $S == $(wc -c <"$ISO4/taskboard/data/gate-reviews.log") ]] && echo same)")" "30|same"
eq "gate cmd: help lists review-submit" "$("$TB" help | grep -c 'tb review-submit')" "1"
eq "gate: the log is gitignored (data/)" "$(git -C "$HERE" check-ignore -q ../data/gate-reviews.log && echo ok)" "ok"
# .github はどの階層でも hard(ルート直下だけではない)
for p in sub/.github/x a/b/.github/workflows/ci.yml sub/.GITHUB/x .Github/workflows/ci.yml; do
  eq "gate builtin hard: $p → ask in approve and merge" "$(for a in approve merge; do gtf FILE "FILE M $p" | gt $a "$GT/gate.conf" "$GT/empty-paths.conf" | hardl | cut -c1-5; done | tr '\n' '|')" "H-01 |H-01 |"
done
eq "gate builtin hard: a path that only contains 'github' is not a hit" "$(gtf FILE 'FILE M docs/github-notes.md' | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl)" "none"
eq "gate builtin hard: agent-config look-alikes are not hits" "$(gtf FILE 'FILE M docs/claude-usage.md' 'FILE M notes/agent-notes.md' | gt approve "$GT/gate.conf" "$GT/empty-paths.conf" | hardl)" "none"
