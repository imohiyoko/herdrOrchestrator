# shellcheck shell=bash
# run.sh から番号順に source される
# --- PR 作成キュー(tb pr-queue)。本物の gh・ネットワーク・GitHub は呼ばない(疑似 gh)
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_pq_saved=$(env | grep -E '^(TB_DB|TB_REPOS_FILE|PR_QUEUE_[A-Z_]+|FAKE_GH_[A-Z_]+|FAKE_GIT_[A-Z_]+|FAKE_AGENTS)=' || true)
export TB_DB=$TMP/pq.db TB_REPOS_FILE=$TMP/pq-repos.md PR_QUEUE_BODY_ROOT=$TMP/pq-reports PR_QUEUE_GH=$TMP/pq-gh FAKE_GH_LOG=$TMP/pq-gh.log
export FAKE_AGENTS='[{"pane_id":"wW:p1","name":"worker1","agent_session":{"value":"sw"}}]'
unset FAKE_GH_LIST FAKE_GH_LIST_FAIL FAKE_GH_CREATE_FAIL FAKE_GH_RC
mkdir -p "$PR_QUEUE_BODY_ROOT"; : >"$FAKE_GH_LOG"
printf '%s\n' '# 許可リスト' '- org/repo   # 対象' >"$TB_REPOS_FILE"
cat >"$PR_QUEUE_GH" <<'G'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_GH_LOG"
case "$1 $2" in
  "pr list") [[ -z ${FAKE_GH_RC:-} ]] || exit "$FAKE_GH_RC"; [[ -z ${FAKE_GH_LIST_FAIL:-} ]] || exit 1
    if [[ -n ${FAKE_GH_DUP:-} && " $* " == *" --head $FAKE_GH_DUP "* ]]; then echo '[{"number":5}]'; else echo "${FAKE_GH_LIST:-[]}"; fi;;
  "pr create") [[ -z ${FAKE_GH_RC:-} ]] || exit "$FAKE_GH_RC"; [[ -z ${FAKE_GH_CREATE_FAIL:-} ]] || exit 1; echo "https://github.com/org/repo/pull/77";;
  *) exit 9;;
esac
G
chmod +x "$PR_QUEUE_GH"
PQ_L1='Report: https://example.com/r/123'
PQ_RULE='body_first_line = ^Report: https://example\.com/r/[0-9]+$'
export PR_QUEUE_CONF=$TMP/pq.conf; printf '%s\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
pqbody() { printf '%s\n\n## 原因\nテスト\n' "${2:-$PQ_L1}" >"$PR_QUEUE_BODY_ROOT/$1.md"; }
pqsql() { sqlite3 "$TB_DB" "$@"; }
pqgh() { grep -c "$1" "$FAKE_GH_LOG"; }
# 差分のガードは必須(PR_QUEUE_REPO_DIR が空なら run しない)。clone を先に作り、pqadd がブランチも用意する
# 公開前検査の設定は固定の conf(<tb>/config/pr-queue.conf)だけで決まり、環境変数では差し替えられない。テストでは tb をコピーした木の conf を書く(リポジトリの config は触らない)
_tb_saved=$TB; PQ_TBROOT=$TMP/pq-tbtree; mkdir -p "$PQ_TBROOT/taskboard/config"
cp -R "$HERE/../bin" "$HERE/../lib" "$HERE/../schema" "$PQ_TBROOT/taskboard/"; cp -R "$HERE/../../lib" "$PQ_TBROOT/lib"
TB=$PQ_TBROOT/taskboard/bin/tb; PQ_FIXEDCONF=$PQ_TBROOT/taskboard/config/pr-queue.conf
pqdeny() { { [[ -z ${1:-} ]] || printf 'deny_patterns = %s\n' "$1"; [[ -z ${2:-} ]] || printf 'deny_exceptions = %s\n' "$2"; } >"$PQ_FIXEDCONF"; }
# 本物の bare origin と clone。mkb は clone で枝を作って origin へ force push する(追跡参照も更新される)
PQ_O=$TMP/pq-origin.git; git init -q --bare -b main "$PQ_O"
R=$TMP/pq-clone; mkdir -p "$R"; git -C "$R" init -q -b main; git -C "$R" config user.email "t""@example.invalid"; git -C "$R" config user.name t
git -C "$R" remote add origin "$PQ_O"
echo base >"$R/a.txt"; git -C "$R" add .; git -C "$R" commit -qm base; git -C "$R" push -q origin main 2>/dev/null
mkb() { git -C "$R" checkout -q -B "$1" main; mkdir -p "$R/$(dirname "$2")"; printf '%s\n' "$3" >"$R/$2"; git -C "$R" add .; git -C "$R" commit -q -m "$4"; git -C "$R" push -q --force origin "$1" 2>/dev/null; git -C "$R" checkout -q main; }
mkbranch() { mkb "$1" "$2" "$3" c; }
export PR_QUEUE_REPO_DIR=$R
# 疑似 git(scan 用に ls-remote / fetch / log を差し替え、残りは本物の git)。FAKE_GIT_HEADS が無いときは本物の bare origin に対して動く
PQ_FAKEGIT=$TMP/pq-git; mkdir -p "$TMP/pq-titles"
cat >"$PQ_FAKEGIT" <<'G'
#!/usr/bin/env bash
args=("$@"); [[ ${args[0]} == -C ]] && args=("${args[@]:2}")
case ${args[0]} in
  ls-remote)
    [[ -z ${FAKE_GIT_LSREMOTE_FAIL:-} ]] || exit 1
    if [[ -n ${FAKE_GIT_HEADS+x} ]]; then for b in $FAKE_GIT_HEADS; do [[ refs/heads/$b == ${args[${#args[@]}-1]} ]] && printf '%s\trefs/heads/%s\n' "$(git -C "$FAKE_GIT_CLONE" rev-parse "refs/remotes/origin/$b")" "$b"; done; exit 0
    elif [[ -n ${FAKE_GIT_SHA_OVERRIDE:-} ]]; then git "$@" | sed "s/^[0-9a-f]*/$FAKE_GIT_SHA_OVERRIDE/"
    else exec git "$@"; fi;;
  fetch) [[ -z ${FAKE_GIT_FETCH_FAIL:-} ]] || exit 1; [[ -z ${FAKE_GIT_HEADS+x} ]] || exit 0; exec git "$@";;
  log) cat "$FAKE_GIT_TITLES/$(sed 's#.*origin/agent/##' <<<"${args[${#args[@]}-1]}")" 2>/dev/null;;
  *) exec git "$@";;
esac
G
chmod +x "$PQ_FAKEGIT"
export PR_QUEUE_GIT=$PQ_FAKEGIT FAKE_GIT_TITLES=$TMP/pq-titles FAKE_GIT_CLONE=$R
# 公開前検査の規則(ダミー。書式は deny-patterns.txt と同じ)。既存のテストは何にも掛からない規則で通す
PQ_DENY=$TMP/pq-deny.txt; printf '%s\n' '# ダミー' 'never-match i  ZZZ_NEVER_MATCHES_ZZZ' >"$PQ_DENY"
pqdeny "$PQ_DENY"
pqadd() { git -C "$R" rev-parse -q --verify "refs/remotes/origin/$1" >/dev/null 2>&1 || mkbranch "$1" "terraform/${1//\//-}.tf" ok; $TB pr-queue add --branch "$1" --body-file "$PR_QUEUE_BODY_ROOT/$2.md" --title "題 $2" "${@:3}" 2>&1; }

# ---- add: 置くだけ。pending。gh は呼ばれない
pqbody b1
eq "pr-queue add: pending" "$(pqadd agent/t-1 b1)" "PQ-1 pending"
eq "pr-queue list: shows it" "$($TB pr-queue list | cut -f1-3)" "$(printf 'PQ-1\tpending\tagent/t-1')"
eq "pr-queue add: invalid input refused (branch prefix, branch chars, repo not on list, outside body_root, missing file, no repo list)" \
  "$(rc $TB pr-queue add --branch feature/x --body-file "$PR_QUEUE_BODY_ROOT/b1.md" --title t) $(rc $TB pr-queue add --branch 'agent/a b' --body-file "$PR_QUEUE_BODY_ROOT/b1.md" --title t) $(rc $TB pr-queue add --branch agent/x --body-file "$PR_QUEUE_BODY_ROOT/b1.md" --title t --repo other/repo) $(rc $TB pr-queue add --branch agent/x --body-file /etc/hosts --title t) $(rc $TB pr-queue add --branch agent/x --body-file "$PR_QUEUE_BODY_ROOT/none.md" --title t)" "1 1 1 1 1"
ln -sf "$PR_QUEUE_BODY_ROOT/b1.md" "$PR_QUEUE_BODY_ROOT/link.md"
eq "pr-queue add: symlink body refused" "$(rc $TB pr-queue add --branch agent/x --body-file "$PR_QUEUE_BODY_ROOT/link.md" --title t)" "1"
eq "pr-queue add: nothing else was queued" "$(pqsql 'SELECT COUNT(*) FROM pr_queue;')" "1"

# ---- 承認なしは実行されない
$TB pr-queue run >/dev/null 2>&1
eq "pr-queue run: pending is never executed (gh not called)" "$(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(pqsql 'SELECT status FROM pr_queue WHERE id=1;')" "0 pending"

# ---- agent の pane からは approve / reject できない
export HERDR_PANE_ID=wW:p1
eq "pr-queue approve/reject: refused from an agent pane" "$(rc $TB pr-queue approve 1) $(rc $TB pr-queue reject 1) $(pqsql 'SELECT status FROM pr_queue WHERE id=1;')" "1 1 pending"
unset HERDR_PANE_ID

# ---- 承認 → run: draft PR だけを、決まった引数で作る
eq "pr-queue approve: human approves" "$($TB pr-queue approve PQ-1 | cut -d"(" -f1)" "PQ-1 approved"
out=$($TB pr-queue run 2>&1)
eq "pr-queue run: created, url recorded" "$(grep -c 'PQ-1 created: https://github.com/org/repo/pull/77' <<<"$out") $(pqsql 'SELECT status FROM pr_queue WHERE id=1;')" "1 created"
eq "pr-queue run: duplicate check first, then create with draft/base main/head/repo" \
  "$(sed -n 1p "$FAKE_GH_LOG") | $(sed -n 2p "$FAKE_GH_LOG" | sed "s#--body-file [^ ]*#--body-file F#")" \
  "pr list --repo org/repo --head agent/t-1 --state open --json number | pr create --repo org/repo --base main --head agent/t-1 --title 題 b1 --body-file F --draft"
$TB pr-queue run >/dev/null 2>&1
eq "pr-queue run: created ones are not run again" "$(pqgh 'pr create')" "1"
eq "pr-queue: no merge / review / ready / close is ever issued" "$(pqgh 'pr merge\|pr review\|pr ready\|pr close\|--approve')" "0"
eq "pr-queue: the code has no merge/approve either" "$(grep -cE 'pr (merge|review|ready|close)|--approve' "$HERE/../lib/prqueue.sh")" "0"

# ---- ガード(approve の時点)
pqbody g1 'Report: <!-- link -->'; pqbody g2 'TODO'; pqbody g3 'https://example.com/r/1'
pqadd agent/g1 g1 >/dev/null; pqadd agent/g2 g2 >/dev/null; pqadd agent/g3 g3 >/dev/null
eq "guard: body line 1 must match body_first_line (template / TODO / bare URL refused at approve)" \
  "$(rc $TB pr-queue approve 2) $(rc $TB pr-queue approve 3) $(rc $TB pr-queue approve 4) $(pqsql "SELECT group_concat(status) FROM pr_queue WHERE id IN (2,3,4);")" "1 1 1 pending,pending,pending"
SEC_AWS="AKIA""IOSFODNN7EXAMPLE"
printf '%s\n\nkey は %s です\n' "$PQ_L1" "$SEC_AWS" >"$PR_QUEUE_BODY_ROOT/s1.md"
pqadd agent/s1 s1 >/dev/null
out=$($TB pr-queue approve 5 2>&1); r=$?
eq "guard: secret-looking value in body refused; the value is not printed" "$r $(grep -c "$SEC_AWS" <<<"$out") $(grep -c 'AWS' <<<"$out")" "1 0 1"
printf '%s\n\nトークン %s\n' "$PQ_L1" "ghp_""abcdefghijklmnopqrstuvwxyz0123456789" >"$PR_QUEUE_BODY_ROOT/s2.md"
pqadd agent/s2 s2 >/dev/null
eq "guard: token-shaped value refused" "$(rc $TB pr-queue approve 6)" "1"
printf '%s\n\n何かの手順(コミット %s)\n' "$PQ_L1" "$(printf 'a%.0s' {1..8})" >"$PR_QUEUE_BODY_ROOT/ok2.md"
pqadd agent/ok2 ok2 >/dev/null
eq "guard: a body with a long doc URL on line 1 and normal text passes" "$($TB pr-queue approve 7 | cut -d"(" -f1)" "PQ-7 approved"
$TB pr-queue run >/dev/null 2>&1   # 7 を流して、以後の回数を数えやすくする

# ---- ガード(run の時点: 承認後に状態が変わった)
$TB pr-queue approve 1 >/dev/null 2>&1   # created なので失敗してよい
pqbody c1; pqadd agent/c1 c1 >/dev/null; $TB pr-queue approve 8 >/dev/null
printf 'あとから足した行\n' >>"$PR_QUEUE_BODY_ROOT/c1.md"
: >"$FAKE_GH_LOG"; out=$($TB pr-queue run 2>&1)
eq "guard at run: body changed after approval → rejected, no create" "$(grep -c '本文が、置かれたときから変わっている' <<<"$out") $(pqgh 'pr create') $(pqsql 'SELECT status FROM pr_queue WHERE id=8;')" "1 0 rejected"
pqbody c2; pqadd agent/c2 c2 >/dev/null; $TB pr-queue approve 9 >/dev/null
printf '%s\n' '# 許可リスト' '- other/repo' >"$TB_REPOS_FILE"
: >"$FAKE_GH_LOG"; out=$($TB pr-queue run 2>&1)
eq "guard at run: repo removed from the allowlist → rejected" "$(grep -c '許可リストに無い' <<<"$out") $(pqgh 'pr create') $(pqsql 'SELECT status FROM pr_queue WHERE id=9;')" "1 0 rejected"
printf '%s\n' '# 許可リスト' '- org/repo' >"$TB_REPOS_FILE"
pqbody c3; pqadd agent/c3 c3 >/dev/null; $TB pr-queue approve 10 >/dev/null
pqsql "UPDATE pr_queue SET branch='main' WHERE id=10;"
: >"$FAKE_GH_LOG"; out=$($TB pr-queue run 2>&1)
eq "guard at run: a branch outside branch_prefix (tampered row) → rejected" "$(pqgh 'pr create') $(pqsql 'SELECT status FROM pr_queue WHERE id=10;')" "0 rejected"

# ---- 重複: 同じ head の open PR が既にある
pqbody d1; pqadd agent/d1 d1 >/dev/null; $TB pr-queue approve 11 >/dev/null
: >"$FAKE_GH_LOG"; out=$(FAKE_GH_LIST='[{"number":5}]' $TB pr-queue run 2>&1)
eq "duplicate: an open PR with the same head exists → rejected, no create" "$(grep -c '既にある' <<<"$out") $(pqgh 'pr create') $(pqsql 'SELECT status FROM pr_queue WHERE id=11;')" "1 0 rejected"

# ---- gh の失敗: 重複確認が取れなければ作らない。作成の失敗は 3 回で failed
pqbody e1; pqadd agent/e1 e1 >/dev/null; $TB pr-queue approve 12 >/dev/null
: >"$FAKE_GH_LOG"
for _i in 1 2; do FAKE_GH_LIST_FAIL=1 $TB pr-queue run >/dev/null 2>&1; done
eq "gh list fails: never creates, stays approved, attempts counted" "$(pqgh 'pr create') $(pqsql 'SELECT status||attempts FROM pr_queue WHERE id=12;')" "0 approved2"
FAKE_GH_LIST_FAIL=1 $TB pr-queue run >/dev/null 2>&1
eq "gh list fails 3 times → failed" "$(pqsql 'SELECT status FROM pr_queue WHERE id=12;')" "failed"
pqbody e2; pqadd agent/e2 e2 >/dev/null; $TB pr-queue approve 13 >/dev/null
for _i in 1 2 3; do FAKE_GH_CREATE_FAIL=1 $TB pr-queue run >/dev/null 2>&1; done
eq "gh create fails 3 times → failed, error has no raw gh output" "$(pqsql 'SELECT status||attempts FROM pr_queue WHERE id=13;') $(pqsql 'SELECT last_error FROM pr_queue WHERE id=13;')" "failed3 gh pr create が失敗(exit 1)"

# ---- dry-run / reject
pqbody f1; pqadd agent/f1 f1 >/dev/null; $TB pr-queue approve 14 >/dev/null
: >"$FAKE_GH_LOG"; out=$($TB pr-queue run --dry-run 2>&1)
eq "run --dry-run: reports, creates nothing, changes nothing" "$(grep -c 'would-create' <<<"$out") $(pqgh 'pr create') $(pqsql 'SELECT status FROM pr_queue WHERE id=14;')" "1 0 approved"
eq "reject: human rejects an approved item; it is not run" "$($TB pr-queue reject 14 -m 不要) $($TB pr-queue run >/dev/null 2>&1; pqgh 'pr create')" "PQ-14 rejected 0"
eq "approve: only pending can be approved" "$(rc $TB pr-queue approve 14)" "1"

# ---- 差分のガード(必須。clone が無い・不正なら run しない。読み取りだけ)
mkbranch agent/df-ok terraform/a.tf 'resource "x" "y" {}'
for b in df-ok df-ci df-sec df-admin; do pqbody $b; pqadd agent/$b $b >/dev/null; done
for i in 15 16 17 18; do $TB pr-queue approve $i >/dev/null 2>&1; done   # きれいなうちに承認し、承認後に枝を書き換えて run 時のガードを見る
mkbranch agent/df-ci .github/workflows/ci.yml 'on: push'
mkbranch agent/df-sec terraform/b.tf "key = \"$SEC_AWS\""
mkbranch agent/df-admin terraform/c.tf 'policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"'
: >"$FAKE_GH_LOG"; out=$($TB pr-queue run 2>&1)
eq "diff guard: clean change created; CI config / secret-looking / AdministratorAccess rejected" \
  "$(pqsql "SELECT group_concat(status) FROM pr_queue WHERE id IN (15,16,17,18);") $(pqgh 'pr create') $(grep -c "$SEC_AWS" <<<"$out")" "created,rejected,rejected,rejected 1 0"
pqbody dfx; pqadd agent/df-missing dfx >/dev/null; $TB pr-queue approve 19 >/dev/null 2>&1
git -C "$R" push -q origin --delete agent/df-missing 2>/dev/null; git -C "$R" update-ref -d refs/remotes/origin/agent/df-missing
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "diff guard: branch missing in the clone → rejected (fail closed)" "$(pqgh 'pr create') $(pqsql 'SELECT status FROM pr_queue WHERE id=19;')" "0 rejected"

# ---- 差分のガードの必須化: PR_QUEUE_REPO_DIR が空・存在しない・git でない → approve も run も通さない(以前は空だと素通り)
pqbody rq1; pqadd agent/rq1 rq1 >/dev/null; rqid=$(pqsql 'SELECT MAX(id) FROM pr_queue;')
eq "diff guard required: empty PR_QUEUE_REPO_DIR → approve refused, stays pending" "$(rc env PR_QUEUE_REPO_DIR= $TB pr-queue approve $rqid) $(pqsql "SELECT status FROM pr_queue WHERE id=$rqid;")" "1 pending"
eq "diff guard required: the refusal says why" "$(PR_QUEUE_REPO_DIR= $TB pr-queue approve $rqid 2>&1 | grep -c 'PR_QUEUE_REPO_DIR')" "1"
$TB pr-queue approve $rqid >/dev/null 2>&1
for _bad in "" "$TMP/no-such-dir" "$TMP"; do
  : >"$FAKE_GH_LOG"; PR_QUEUE_REPO_DIR=$_bad $TB pr-queue run >/dev/null 2>&1
  eq "diff guard required: run with REPO_DIR='${_bad##*/}' creates nothing and leaves it approved" "$(pqgh 'pr create') $(pqsql "SELECT status FROM pr_queue WHERE id=$rqid;")" "0 approved"
done
eq "diff guard required: a relative PR_QUEUE_REPO_DIR is refused (run and scan stop, exit 1)" "$(cd "$TMP" && PR_QUEUE_REPO_DIR=pq-clone rc $TB pr-queue run) $(cd "$TMP" && PR_QUEUE_REPO_DIR=pq-clone rc $TB pr-queue scan) $(PR_QUEUE_REPO_DIR= rc $TB pr-queue scan)" "1 1 1"
unset _bad; $TB pr-queue reject $rqid -m 'REPO_DIR テスト用' >/dev/null
# ---- scan: origin の agent/* を走査してキューに積む(疑似 git は先頭で定義済み)
export PR_QUEUE_CONF=$TMP/pq.conf
unset FAKE_GH_DUP
printf '%s\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
sc_title() { printf '%s\n' "$2" >"$FAKE_GIT_TITLES/$1"; }
for n in ok1 ok2x nolink dup sec ph notitle nobody; do mkbranch agent/sc-$n terraform/$n.tf "x $n"; done
for n in ok1 ok2x nolink dup sec ph; do pqbody pr-body-agent-sc-$n; done
mv_body() { mv "$PR_QUEUE_BODY_ROOT/$1.md" "$PR_QUEUE_BODY_ROOT/$2.md"; }
for n in ok1 ok2x nolink dup sec ph notitle; do pqbody "pr-body-agent-sc-$n"; done
pqbody pr-body-agent-sc-nolink 'TODO リンクを貼る'
pqbody pr-body-agent-sc-ph "$PQ_L1"; printf '{{原因}} を書く\n' >>"$PR_QUEUE_BODY_ROOT/pr-body-agent-sc-ph.md"
printf '%s\n\nkey %s\n' "$PQ_L1" "$SEC_AWS" >"$PR_QUEUE_BODY_ROOT/pr-body-agent-sc-sec.md"
for n in ok1 ok2x nolink dup sec ph; do sc_title sc-$n "対応 $n"; done
: >"$FAKE_GIT_TITLES/sc-notitle"
export FAKE_GIT_HEADS="agent/sc-ok1 agent/sc-nolink agent/sc-dup agent/sc-sec agent/sc-ph agent/sc-notitle agent/sc-nobody"
export FAKE_GH_DUP=agent/sc-dup
: >"$FAKE_GH_LOG"; out=$($TB pr-queue scan 2>&1)
eq "scan (auto_approve unset): only the good branch is queued, as pending (never approved)" \
  "$(pqsql "SELECT group_concat(branch||':'||status) FROM pr_queue WHERE branch LIKE 'agent/sc-%';") $(pqsql "SELECT title FROM pr_queue WHERE branch='agent/sc-ok1';")" "agent/sc-ok1:pending 対応 ok1"
eq "scan: skip reasons (no body / no link / placeholder / duplicate / secret / empty title)" \
  "$(grep -c 'sc-nobody: 本文' <<<"$out") $(grep -c 'sc-nolink: .*1 行目' <<<"$out") $(grep -c 'sc-ph: .*プレースホルダ' <<<"$out") $(grep -c 'sc-dup: 同じ head の PR' <<<"$out") $(grep -c 'sc-sec: .*秘密' <<<"$out") $(grep -c 'sc-notitle: 最新コミットの題が空' <<<"$out") $(grep -c "$SEC_AWS" <<<"$out")" "1 1 1 1 1 1 0"
eq "scan: duplicate check looks at all states; no create happened" "$(grep -c -- '--state all' "$FAKE_GH_LOG") $(pqgh 'pr create')" "2 0"
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "run after scan (auto_approve=0): nothing is created for pending items" "$(pqgh 'pr create')" "0"
eq "scan: already-queued branch is not queued twice" "$($TB pr-queue scan 2>&1 | grep -c 'sc-ok1: 既にキューにある') $(pqsql "SELECT COUNT(*) FROM pr_queue WHERE branch='agent/sc-ok1';")" "1 1"
eq "scan --dry-run: queues nothing" "$(pqsql "SELECT COUNT(*) FROM pr_queue;") $(FAKE_GIT_HEADS=agent/sc-ok2x pqbody pr-body-agent-sc-ok2x; FAKE_GIT_HEADS=agent/sc-ok2x $TB pr-queue scan --dry-run | grep -c 'would-queue') $(pqsql "SELECT COUNT(*) FROM pr_queue;")" "$(pqsql "SELECT COUNT(*) FROM pr_queue;") 1 $(pqsql "SELECT COUNT(*) FROM pr_queue;")"
# auto_approve=1: approved で積む。run のガードは同じ。1 回の上限は 3
printf '%s\nauto_approve=1\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
for n in a b c d; do mkbranch agent/cap-$n terraform/cap$n.tf "x $n"; pqbody pr-body-agent-cap-$n; sc_title cap-$n "cap $n"; done
export FAKE_GIT_HEADS="agent/cap-a agent/cap-b agent/cap-c agent/cap-d"
$TB pr-queue scan >/dev/null 2>&1
eq "scan (auto_approve=1): queued as approved" "$(pqsql "SELECT group_concat(status) FROM pr_queue WHERE branch LIKE 'agent/cap-%';")" "approved,approved,approved,approved"
: >"$FAKE_GH_LOG"; out=$($TB pr-queue run 2>&1)
eq "run: at most 3 PRs per run; the rest wait for the next run" "$(pqgh 'pr create') $(pqsql "SELECT COUNT(*) FROM pr_queue WHERE branch LIKE 'agent/cap-%' AND status='approved';") $(grep -c '上限' <<<"$out")" "3 1 1"
$TB pr-queue run >/dev/null 2>&1
eq "run: next run creates the remaining one" "$(pqsql "SELECT COUNT(*) FROM pr_queue WHERE branch LIKE 'agent/cap-%' AND status='created';")" "4"
# 再試行の上限: 作成が失敗し続けたブランチは 3 回で failed、scan は積み直さない
mkbranch agent/fl terraform/fl.tf x; pqbody pr-body-agent-fl; sc_title fl "fl"
export FAKE_GIT_HEADS="agent/fl"; $TB pr-queue scan >/dev/null 2>&1
export FAKE_GH_CREATE_FAIL=1; for i in 1 2 3 4; do $TB pr-queue run >/dev/null 2>&1; done; unset FAKE_GH_CREATE_FAIL
eq "retry cap: failing branch becomes failed after 3 attempts and is not re-queued by scan" \
  "$(pqsql "SELECT status||attempts FROM pr_queue WHERE branch='agent/fl';") $($TB pr-queue scan 2>&1 | grep -c 'fl: 既にキューにある') $(pqsql "SELECT COUNT(*) FROM pr_queue WHERE branch='agent/fl';")" "failed3 1 1"
eq "scan: no merge/approve/ready in the code or the gh calls" "$(pqgh 'pr merge\|pr review\|pr ready\|pr close\|--approve')" "0"
for _v in PR_QUEUE_CONF FAKE_GIT_HEADS FAKE_GH_DUP; do unset "$_v"; done

# ---- gh の指定は必須(fail-closed): PR_QUEUE_GH も conf の gh も無ければ、PATH 上の gh に落ちず、警告して止まる
pqbody gh1; pqadd agent/gh1 gh1 >/dev/null; ghid=$(pqsql 'SELECT MAX(id) FROM pr_queue;'); $TB pr-queue approve $ghid >/dev/null
export PR_QUEUE_CONF=$TMP/pq-gh.conf; printf '%s\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
_gh_saved=$PR_QUEUE_GH; unset PR_QUEUE_GH
: >"$FAKE_GH_LOG"; out=$($TB pr-queue run 2>&1); r=$?
eq "gh unset: run warns, stops with a non-zero exit, calls no gh, row stays approved" "$r $(grep -c 'PR_QUEUE_GH' <<<"$out") $(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(pqsql "SELECT status FROM pr_queue WHERE id=$ghid;")" "1 1 0 approved"
eq "gh unset: --dry-run and scan stop too" "$(rc $TB pr-queue run --dry-run) $(rc $TB pr-queue scan)" "1 1"
export PR_QUEUE_GH=gh
eq "gh set to a bare name (resolved via PATH) is refused: must be an absolute path to an executable" "$(rc $TB pr-queue run) $(PR_QUEUE_GH=$TMP/none rc $TB pr-queue run)" "1 1"
unset PR_QUEUE_GH
printf '%s\ngh = %s   # conf\n' "$PQ_RULE" "$_gh_saved" >"$PR_QUEUE_CONF"
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "gh from config/pr-queue.conf (gh = <absolute path>) works" "$(pqgh 'pr create') $(pqsql "SELECT status FROM pr_queue WHERE id=$ghid;")" "1 created"
pqbody gh2; pqadd agent/gh2 gh2 >/dev/null; ghid=$(pqsql 'SELECT MAX(id) FROM pr_queue;'); $TB pr-queue approve $ghid >/dev/null
: >"$FAKE_GH_LOG"; PR_QUEUE_GH=$TMP/none $TB pr-queue run >/dev/null 2>&1
eq "env PR_QUEUE_GH wins over the config (and a bad one fails closed)" "$(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(pqsql "SELECT status FROM pr_queue WHERE id=$ghid;")" "0 approved"
export PR_QUEUE_GH=$_gh_saved; printf '%s\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
$TB pr-queue run >/dev/null 2>&1   # gh2 を流す

# ---- 認証で止まる(wrapper の終了コード 75 = auth-locked): attempts を使わず、残りを止め、通知は状態が変わったとき 1 回だけ
export TB_NOTIFY_DEDUPE_SEC=0 TB_NOTIFY_ENABLED=on TB_NOTIFY_DIR=$TMP/pq-nout TB_NOTIFY_CONF=$TMP/pq-notify.conf
for n in al1 al2; do pqbody $n; pqadd agent/$n $n >/dev/null; $TB pr-queue approve "$(pqsql 'SELECT MAX(id) FROM pr_queue;')" >/dev/null; done
al1=$(pqsql "SELECT id FROM pr_queue WHERE branch='agent/al1';"); al2=$(pqsql "SELECT id FROM pr_queue WHERE branch='agent/al2';")
pqwarn() { pqsql "SELECT COUNT(*) FROM notify_events WHERE type='warning' AND text LIKE '%pr-queue%';"; }
w0=$(pqwarn)
for _i in 1 2 3 4; do : >"$FAKE_GH_LOG"; FAKE_GH_RC=75 $TB pr-queue run >/dev/null 2>&1; done
eq "auth-locked (75) x4: attempts not consumed, never failed, still approved, last_error=auth-locked" \
  "$(pqsql "SELECT group_concat(status||attempts||':'||last_error) FROM pr_queue WHERE id IN ($al1,$al2);")" "approved0:auth-locked,approved0:auth-locked"
eq "auth-locked: the rest of the run is not processed (one gh call per run) and no create" "$(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(pqgh 'pr create')" "1 0"
eq "auth-locked: one notification for the whole stretch (4 runs), mentioning unlock/auth" "$(($(pqwarn)-w0)) $(pqsql "SELECT text FROM notify_events WHERE type='warning' AND text LIKE '%pr-queue%' ORDER BY id DESC LIMIT 1;" | grep -c 'ghtkn')" "1 1"
eq "auth-locked (75) in --dry-run: changes nothing and notifies nothing" "$(FAKE_GH_RC=75 $TB pr-queue run --dry-run >/dev/null 2>&1; pqsql "SELECT group_concat(attempts) FROM pr_queue WHERE id IN ($al1,$al2);") $(($(pqwarn)-w0))" "0,0 1"
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "after unlock: next run creates both, last_error cleared" "$(pqgh 'pr create') $(pqsql "SELECT group_concat(status||(last_error IS NULL)) FROM pr_queue WHERE id IN ($al1,$al2);")" "2 created1,created1"
pqbody al3; pqadd agent/al3 al3 >/dev/null; al3=$(pqsql 'SELECT MAX(id) FROM pr_queue;'); $TB pr-queue approve $al3 >/dev/null
FAKE_GH_RC=75 $TB pr-queue run >/dev/null 2>&1
eq "a new lock after recovery notifies again" "$(($(pqwarn)-w0))" "2"
# create の側でも 75 は同じ(list は通り create が 75)。他の失敗(exit 1)は従来どおり attempts を使う
cat >"$TMP/pq-gh75" <<'G'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_GH_LOG"
case "$1 $2" in "pr list") echo '[]';; "pr create") exit 75;; *) exit 9;; esac
G
chmod +x "$TMP/pq-gh75"
PR_QUEUE_GH=$TMP/pq-gh75 $TB pr-queue run >/dev/null 2>&1
eq "auth-locked on gh pr create (75): attempts not consumed" "$(pqsql "SELECT status||attempts||':'||last_error FROM pr_queue WHERE id=$al3;")" "approved0:auth-locked"
FAKE_GH_RC=1 $TB pr-queue run >/dev/null 2>&1
eq "any other failure (exit 1) still consumes attempts" "$(pqsql "SELECT status||attempts FROM pr_queue WHERE id=$al3;")" "approved1"
$TB pr-queue run >/dev/null 2>&1
# list は通り create だけ 75 を返し続ける状態: run を 4 回しても通知は 1 件。印を消すのは、run が認証で止まらず最後まで進んだときだけ
pqbody al4; pqadd agent/al4 al4 >/dev/null; al4=$(pqsql 'SELECT MAX(id) FROM pr_queue;'); $TB pr-queue approve $al4 >/dev/null
w1=$(pqwarn)
for _i in 1 2 3 4; do PR_QUEUE_GH=$TMP/pq-gh75 $TB pr-queue run >/dev/null 2>&1; done
eq "list ok + create 75 x4: one notification, mark stays, attempts untouched" "$(($(pqwarn)-w1)) $(pqsql "SELECT status||attempts||':'||last_error FROM pr_queue WHERE id=$al4;")" "1 approved0:auth-locked"
$TB pr-queue run >/dev/null 2>&1
eq "a run that goes through (no auth stop) clears the mark and creates" "$(pqsql "SELECT status||(last_error IS NULL) FROM pr_queue WHERE id=$al4;")" "created1"
unset TB_NOTIFY_DEDUPE_SEC TB_NOTIFY_ENABLED TB_NOTIFY_DIR TB_NOTIFY_CONF

# ---- failed から人が戻す: tb pr-queue retry <PQ-n>(人だけ。failed だけ。attempts を 0 に戻し approved へ)
fid=$(pqsql "SELECT id FROM pr_queue WHERE status='failed' ORDER BY id LIMIT 1;")
export HERDR_PANE_ID=wW:p1
eq "retry: refused from an agent pane" "$(rc $TB pr-queue retry $fid) $(pqsql "SELECT status FROM pr_queue WHERE id=$fid;")" "1 failed"
unset HERDR_PANE_ID
eq "retry: only failed rows (created / pending are refused)" "$(rc $TB pr-queue retry 1) $(rc $TB pr-queue retry 999) $(rc $TB pr-queue retry)" "1 1 2"
eq "retry: human puts a failed row back to approved, attempts reset" "$($TB pr-queue retry PQ-$fid | cut -d'(' -f1) $(pqsql "SELECT status||attempts FROM pr_queue WHERE id=$fid;")" "PQ-$fid approved approved0"
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "retry: the guards still run, and the next run picks it up" "$(pqgh 'pr create') $(pqsql "SELECT status FROM pr_queue WHERE id=$fid;")" "1 created"
eq "retry: no schema change (pr_queue columns unchanged)" "$(pqsql "SELECT group_concat(name) FROM pragma_table_info('pr_queue');")" "id,repo,branch,title,body_file,body_sha256,task_id,requested_by,status,last_error,attempts,pr_url,created_at,decided_at,executed_at"
unset PR_QUEUE_CONF

# ---- ブランチ登録の拡張: 接頭辞を conf で列挙(既定は agent/ のみ)。登録時に origin に実在するかを確かめる(fail-closed)
pqbr() { git -C "$R" rev-parse -q --verify "refs/remotes/origin/$1" >/dev/null 2>&1 || mkbranch "$1" "terraform/${1//\//-}.tf" ok; }
pqbody pf1
pqbr feat/pf1; pqbr fix/pf2; pqbr docs/pf3
pqraw() { $TB pr-queue add --branch "$1" --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title "題 $1" 2>&1; }
export PR_QUEUE_CONF=$TMP/pq-pfx.conf; printf '%s\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
eq "prefixes: default is agent/ only (feat/ fix/ refused)" "$(rc $TB pr-queue add --branch feat/pf1 --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t) $(rc $TB pr-queue add --branch fix/pf2 --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t)" "1 1"
printf '%s\nbranch_prefixes = agent/, feat/ fix/ bad prefix\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
eq "prefixes: branch_prefixes (comma/space separated) allows feat/ and fix/ via add; docs/ stays refused; invalid entries are dropped" \
  "$(pqraw feat/pf1 | cut -d' ' -f2) $(pqraw fix/pf2 | cut -d' ' -f2) $(rc $TB pr-queue add --branch docs/pf3 --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t) $(rc $TB pr-queue add --branch bad/x --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t)" "pending pending 1 1"
pfid=$(pqsql "SELECT id FROM pr_queue WHERE branch='feat/pf1';")
eq "prefixes: the guard (approve) accepts the extra prefix too" "$($TB pr-queue approve $pfid | cut -d'(' -f1)" "PQ-$pfid approved"
printf '%s\nbranch_prefixes = ,,  bad\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
eq "prefixes: all-invalid branch_prefixes falls back to agent/ only" "$(rc $TB pr-queue add --branch fix/pf2 --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t) $(pqadd agent/pf4 pf1 | cut -d' ' -f2)" "1 pending"
printf '%s\nbranch_prefixes = agent/ feat/\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
export FAKE_GIT_HEADS="feat/sx agent/sy"; pqbody pr-body-feat-sx; pqbody pr-body-agent-sy; mkbranch agent/sy terraform/sy.tf x; mkbranch feat/sx terraform/sx.tf x; sc_title() { printf '%s\n' "$2" >"$FAKE_GIT_TITLES/$1"; }; sc_title sy "対応 sy"
out=$($TB pr-queue scan 2>&1)
eq "prefixes: scan walks only the first prefix (agent/); feat/ branches are registered with add, not scanned" "$(grep -c 'feat/sx' <<<"$out") $(pqsql "SELECT COUNT(*) FROM pr_queue WHERE branch='feat/sx';") $(pqsql "SELECT COUNT(*) FROM pr_queue WHERE branch='agent/sy';")" "0 0 1"
unset FAKE_GIT_HEADS
printf '%s\n' "$PQ_RULE" >"$PR_QUEUE_CONF"
# origin に実在するか(確認できなければ登録しない)
eq "add: a branch that is not on origin is refused (says push first), nothing queued" \
  "$(n0=$(pqsql 'SELECT COUNT(*) FROM pr_queue;'); out=$($TB pr-queue add --branch agent/not-pushed --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t 2>&1); echo "$? $(grep -c 'origin' <<<"$out") $(($(pqsql 'SELECT COUNT(*) FROM pr_queue;')-n0))")" "1 1 0"
eq "add: ls-remote failure → refused (fail-closed)" "$(FAKE_GIT_LSREMOTE_FAIL=1 rc $TB pr-queue add --branch agent/pf1b --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t)" "1"
pqbr agent/pf5
eq "add: needs PR_QUEUE_REPO_DIR (empty → refused)" "$(PR_QUEUE_REPO_DIR= rc $TB pr-queue add --branch agent/pf5 --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t) $(rc $TB pr-queue add --branch agent/pf5 --body-file "$PR_QUEUE_BODY_ROOT/pf1.md" --title t)" "1 0"
unset PR_QUEUE_CONF

# ---- 公開前検査(deny-patterns): run が PR を作る前に、題・本文・コミットメッセージ・ブランチ名・差分に掛ける
PQ_NR="noreply""@mail-corp.invalid"; PQ_OT="someone""@mail-corp.invalid"
PQ_DENY=$TMP/pq-deny2.txt
printf '%s\n' '# 規則(ダミー。deny-patterns.txt と同じ書式)' 'r-internal i  internal-corp' 'r-case     s  SECRETWORD' 'r-mail     s  [a-z.]+@mail-corp\.invalid' 'r-jp       i  社外秘' '' >"$PQ_DENY"
PQ_EX=$TMP/pq-deny-ex.txt; printf '%s\n' '# 例外' 'r-mail ^noreply@mail-corp\.invalid$' >"$PQ_EX"
pqdeny "$PQ_DENY"
# dn <名> <題> <本文の 3 行目> <ファイル> <内容> <コミットメッセージ> [ブランチ名の接尾辞]: 登録 → 承認までする。id は DQ_ID
dn() {
  local br=agent/dn-$1${7:-}
  mkb "$br" "${4:-terraform/dn-$1.tf}" "${5:-x}" "${6:-c}"
  printf '%s\n\n%s\n' "$PQ_L1" "${3:-ふつうの本文}" >"$PR_QUEUE_BODY_ROOT/dn-$1.md"
  $TB pr-queue add --branch "$br" --body-file "$PR_QUEUE_BODY_ROOT/dn-$1.md" --title "${2:-ふつうの題}" >/dev/null 2>&1
  DQ_ID=$(pqsql "SELECT MAX(id) FROM pr_queue;"); $TB pr-queue approve "$DQ_ID" >/dev/null 2>&1
}
dst() { pqsql "SELECT status||attempts FROM pr_queue WHERE id=$1;"; }
derr() { pqsql "SELECT last_error FROM pr_queue WHERE id=$1;"; }
export TB_NOTIFY_DEDUPE_SEC=0 TB_NOTIFY_ENABLED=on TB_NOTIFY_DIR=$TMP/pq-nout2 TB_NOTIFY_CONF=$TMP/pq-notify2.conf
dwarn() { pqsql "SELECT COUNT(*) FROM notify_events WHERE type='warning' AND text LIKE '%公開前検査%';"; }
dn ok; ok_id=$DQ_ID
dn t1 '対応 internal-corp の件'; t_id=$DQ_ID
dn b1 ふつうの題 'SECRETWORD を含む行'; b_id=$DQ_ID
dn c1 ふつうの題 ふつうの本文 terraform/c1.tf x 'fix: INTERNAL-CORP 向け'; c_id=$DQ_ID
dn n1 ふつうの題 ふつうの本文 terraform/n1.tf x c -internal-corp-x; n_id=$DQ_ID
dn d1 ふつうの題 ふつうの本文 terraform/d1.tf $'a\nb SECRETWORD'; d_id=$DQ_ID
dn p1 ふつうの題 ふつうの本文 terraform/internal-corp.tf x; p_id=$DQ_ID
dn s1 ふつうの題 'secretword は小文字(s なので通る)' terraform/s1.tf 'lower secretword'; s_id=$DQ_ID
dn i1 ふつうの題 ふつうの本文 terraform/i1.tf 'INTERNAL-CORP'; i_id=$DQ_ID
: >"$FAKE_GH_LOG"; w0=$(dwarn); out=$($TB pr-queue run 2>&1)
eq "deny: a clean PR passes (created)" "$(dst $ok_id)" "created0"
eq "deny: title hit → rejected, attempts not consumed, position = title" "$(dst $t_id) $(derr $t_id | grep -c 'r-internal@title')" "rejected0 1"
eq "deny: body hit reports the line (body:3)" "$(dst $b_id) $(derr $b_id | grep -c 'r-case@body:3')" "rejected0 1"
eq "deny: commit message hit (flag i: INTERNAL-CORP matches internal-corp)" "$(dst $c_id) $(derr $c_id | grep -c 'r-internal@commit ')" "rejected0 1"
eq "deny: branch name hit" "$(dst $n_id) $(derr $n_id | grep -c 'r-internal@branch')" "rejected0 1"
eq "deny: added diff line hit reports file and line" "$(dst $d_id) $(derr $d_id | grep -c 'r-case@diff#1:2')" "rejected0 1"
eq "deny: changed file path hit" "$(dst $p_id) $(derr $p_id | grep -c 'r-internal@path#1')" "rejected0 1"
eq "deny: flag s is case-sensitive (lowercase secretword passes), flag i is not" "$(dst $s_id) $(dst $i_id)" "created0 rejected0"
eq "deny: only the clean/lowercase ones were created (gh pr create x2)" "$(pqgh 'pr create')" "2"
eq "deny: output and DB never contain the matched values (positions are numbers: path#N / diff#N:line)" "$(grep -c 'SECRETWORD\|internal-corp' <<<"$out") $(pqsql "SELECT COUNT(*) FROM pr_queue WHERE last_error LIKE '%SECRETWORD%' AND last_error NOT LIKE '%r-case%';")" "0 0"
eq "deny: one notification per hit (6 hits: t b c n d p; i also hit → 7), rule names only" "$(($(dwarn)-w0)) $(pqsql "SELECT COUNT(*) FROM notify_events WHERE text LIKE '%SECRETWORD%' AND text NOT LIKE '%r-case%';")" "7 0"
# 例外ファイル(任意): noreply の Co-Authored-By 行だけを通し、ほかのメールは止める
dn e1 ふつうの題 ふつうの本文 terraform/e1.tf x "msg"$'\n\n'"Co-Authored-By: Claude <$PQ_NR>"; e1=$DQ_ID
dn e2 ふつうの題 ふつうの本文 terraform/e2.tf x "msg"$'\n\n'"Co-Authored-By: Someone <$PQ_OT>"; e2=$DQ_ID
$TB pr-queue run >/dev/null 2>&1
eq "deny: without an exceptions file the noreply Co-Authored-By line is a hit (rule r-mail)" "$(dst $e1) $(derr $e1 | grep -c 'r-mail@commit ')" "rejected0 1"
dn e3 ふつうの題 ふつうの本文 terraform/e3.tf x "msg"$'\n\n'"Co-Authored-By: Claude <$PQ_NR>"; e3=$DQ_ID
dn e4 ふつうの題 ふつうの本文 terraform/e4.tf x "msg"$'\n\n'"Co-Authored-By: Someone <$PQ_OT>"; e4=$DQ_ID
pqdeny "$PQ_DENY" "$PQ_EX"; $TB pr-queue run >/dev/null 2>&1; pqdeny "$PQ_DENY"
eq "deny: with the exceptions file the noreply line passes; another address is still a hit" "$(dst $e3) $(dst $e4)" "created0 rejected0"
# dry-run: 報告するだけ。DB は変えない
dn y1 '対応 internal-corp'; y1=$DQ_ID
: >"$FAKE_GH_LOG"; w1=$(dwarn); out=$($TB pr-queue run --dry-run 2>&1)
eq "deny: --dry-run reports the hit, changes nothing, notifies nothing" "$(grep -c "PQ-$y1 rejected" <<<"$out") $(dst $y1) $(($(dwarn)-w1)) $(pqgh 'pr create')" "1 approved0 0 0"
$TB pr-queue reject $y1 >/dev/null
# 設定の不備は fail-closed(run は何もせず終了コード 1。承認済みの行はそのまま)
dn k1; k1=$DQ_ID
: >"$FAKE_GH_LOG"
printf '%s\n' 'bad-flag x  foo' >"$TMP/pq-deny-badflag.txt"; printf '%s\n' 'bad-re i  ([unclosed' >"$TMP/pq-deny-badre.txt"; printf '%s\n' '# 規則なし' >"$TMP/pq-deny-empty.txt"
printf '%s\n' 'r-mail ([unclosed' >"$TMP/pq-deny-badex.txt"
rm -f "$PQ_FIXEDCONF"
eq "deny config: unset → run stops (exit 1, no gh call, row stays approved)" "$(rc $TB pr-queue run) $(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(dst $k1)" "1 0 approved0"
eq "deny config: unset message names the conf key" "$($TB pr-queue run 2>&1 | grep -c 'deny_patterns')" "1"
for _f in "$TMP/none.txt" "pq-deny2.txt" "$TMP/pq-deny-badflag.txt" "$TMP/pq-deny-badre.txt" "$TMP/pq-deny-empty.txt"; do
  pqdeny "$_f"
  eq "deny config: bad rules file ($(basename "$_f")) → stops" "$(rc $TB pr-queue run) $(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(dst $k1)" "1 0 approved0"
done
printf '%s\n' 'r-mail .*' >"$TMP/pq-deny-anyex.txt"
pqdeny "$PQ_DENY" "$TMP/pq-deny-badex.txt"; _r1=$(rc $TB pr-queue run)
pqdeny "$PQ_DENY" "$TMP/none.txt"; _r2=$(rc $TB pr-queue run)
pqdeny "$PQ_DENY" "$TMP/pq-deny-anyex.txt"; _r3=$(rc $TB pr-queue run)
eq "deny config: bad / missing / match-everything exceptions file → stops" "$_r1 $_r2 $_r3 $(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(dst $k1)" "1 1 1 0 approved0"
pqdeny "$PQ_DENY"
eq "deny config: --dry-run stops too" "$(rc $TB pr-queue run --dry-run)" "0"
rm -f "$PQ_FIXEDCONF"; eq "deny config: --dry-run with no setting stops (exit 1)" "$(rc $TB pr-queue run --dry-run)" "1"
# 環境変数と PR_QUEUE_CONF では決められない(差し替えの口を作らない)
: >"$FAKE_GH_LOG"
out=$(PR_QUEUE_DENY_PATTERNS=$PQ_DENY PR_QUEUE_DENY_EXCEPTIONS=$PQ_EX $TB pr-queue run 2>&1); r=$?
eq "deny config: env values are ignored with a warning (no conf → still stops)" "$r $(grep -c '無視' <<<"$out") $(wc -l <"$FAKE_GH_LOG" | tr -d ' ') $(dst $k1)" "1 1 0 approved0"
export PR_QUEUE_CONF=$TMP/pq-deny.conf; printf '%s\n' "$PQ_RULE" "deny_patterns = $PQ_DENY" >"$PR_QUEUE_CONF"
eq "deny config: deny_patterns in a PR_QUEUE_CONF file is ignored (only the fixed config/pr-queue.conf counts)" "$(rc $TB pr-queue run) $(dst $k1)" "1 approved0"
unset PR_QUEUE_CONF
pqdeny "$PQ_DENY"
: >"$FAKE_GH_LOG"; out=$(PR_QUEUE_DENY_PATTERNS=$TMP/none.txt PR_QUEUE_DENY_EXCEPTIONS=$TMP/none.txt $TB pr-queue run 2>&1)
eq "deny config: with the conf set, a bad env value changes nothing (warned and ignored; the run goes through)" "$(grep -c '無視' <<<"$out") $(dst $k1)" "1 created0"

# ---- 検査した内容と PR になる内容を同じにする(origin と同期し、追跡参照だけを検査する)
pq2=$TMP/pq-clone2; git clone -q "$PQ_O" "$pq2" 2>/dev/null; git -C "$pq2" config user.email "t""@example.invalid"; git -C "$pq2" config user.name t2
push2() { git -C "$pq2" fetch -q origin 2>/dev/null; git -C "$pq2" checkout -q -B "$1" origin/main; mkdir -p "$pq2/$(dirname "$2")"; printf '%s\n' "$3" >"$pq2/$2"; git -C "$pq2" add .; git -C "$pq2" commit -q -m "${4:-c}"; git -C "$pq2" push -q --force origin "$1" 2>/dev/null; }
dn o1; o1=$DQ_ID
push2 agent/dn-o1 terraform/dn-o1.tf 'internal-corp を足した(承認後に別の clone から force push)'
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "sync: content force-pushed to origin after approval is what gets checked (rejected, not created); the stale local branch is not used" "$(dst $o1) $(derr $o1 | grep -c 'r-internal') $(pqgh 'pr create')" "rejected0 1 0"
dn o2; o2=$DQ_ID
push2 agent/dn-o2 terraform/dn-o2.tf 'clean change'
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "sync: a clean change pushed after approval is fetched and created" "$(dst $o2) $(pqgh 'pr create')" "created0 1"
dn o3; o3=$DQ_ID
: >"$FAKE_GH_LOG"
FAKE_GIT_FETCH_FAIL=1 $TB pr-queue run >/dev/null 2>&1
eq "sync: fetch failure → not created, row untouched (no attempts), retried next run" "$(dst $o3) $(pqgh 'pr create')" "approved0 0"
FAKE_GIT_LSREMOTE_FAIL=1 $TB pr-queue run >/dev/null 2>&1
eq "sync: ls-remote failure → not created, row untouched" "$(dst $o3) $(pqgh 'pr create')" "approved0 0"
FAKE_GIT_SHA_OVERRIDE="$(printf '1%.0s' {1..20})""$(printf '1%.0s' {1..20})" $TB pr-queue run >/dev/null 2>&1
eq "sync: tracking ref differs from origin's SHA (origin moved after the fetch) → not created, row untouched" "$(dst $o3) $(pqgh 'pr create')" "approved0 0"
$TB pr-queue run >/dev/null 2>&1
eq "sync: next normal run creates it" "$(dst $o3)" "created0"
dn o4; o4=$DQ_ID; : >"$FAKE_GH_LOG"
git -C "$R" push -q origin --delete agent/dn-o4 2>/dev/null
$TB pr-queue run >/dev/null 2>&1
eq "sync: branch deleted on origin → rejected (clone's tracking ref is not trusted)" "$(dst $o4) $(pqgh 'pr create')" "rejected0 0"
dn o5 ふつうの題 ふつうの本文 terraform/o5.tf x c; o5=$DQ_ID
git -C "$R" update-ref -d refs/remotes/origin/agent/dn-o5
git -C "$R" branch -q -f agent/dn-o5 "$(git -C "$pq2" rev-parse origin/agent/dn-o5 2>/dev/null || git -C "$R" rev-parse origin/main)" 2>/dev/null
eq "sync: approve also syncs (a human approval checks what is on origin)" "$(rc $TB pr-queue approve $o5) $(dst $o5)" "1 approved0"
$TB pr-queue run >/dev/null 2>&1; $TB pr-queue reject $o5 >/dev/null 2>&1

# ---- 非 ASCII のパス名(引用符つきで返らない)
dn j1 ふつうの題 ふつうの本文 'docs/社外秘メモ.txt' x c; j1=$DQ_ID
dn j2; j2=$DQ_ID; push2 agent/dn-j2 '.github/workflows/変.yml' 'on: push'
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "non-ASCII: a rule word only in the (non-ASCII) path name is caught, position is a number" "$(dst $j1) $(derr $j1 | grep -c 'r-jp@path#1')" "rejected0 1"
eq "non-ASCII: a non-ASCII file under .github/workflows is still seen as CI config" "$(dst $j2) $(derr $j2 | grep -c 'CI 設定')" "rejected0 1"

# ---- バイナリの変更は内容を検査できないので人に回す
mkbin() { git -C "$R" checkout -q -B "$1" main; printf 'a\0b\001\002 internal-corp' >"$R/b.bin"; git -C "$R" add .; git -C "$R" commit -q -m c; git -C "$R" push -q --force origin "$1" 2>/dev/null; git -C "$R" checkout -q main; }
pqbody bin1; mkbin agent/bin1
eq "binary: a branch with a binary change cannot be approved (needs a human)" "$(rc $TB pr-queue add --branch agent/bin1 --body-file "$PR_QUEUE_BODY_ROOT/bin1.md" --title t; $TB pr-queue approve "$(pqsql 'SELECT MAX(id) FROM pr_queue;')" 2>&1 | grep -c 'バイナリ')" "0
1"
dn bb1; bb1=$DQ_ID; mkbin agent/dn-bb1
: >"$FAKE_GH_LOG"; $TB pr-queue run >/dev/null 2>&1
eq "binary: a binary change pushed after approval is rejected at run, not created" "$(dst $bb1) $(derr $bb1 | grep -c 'バイナリ') $(pqgh 'pr create')" "rejected0 1 0"

# ---- コミットの author / committer も検査に含める
mka() { git -C "$R" checkout -q -B "$1" main; mkdir -p "$R/terraform"; printf '%s\n' "$1" >"$R/terraform/$(basename "$1").tf"; git -C "$R" add .; git -C "$R" -c user.name=a -c user.email="$2" commit -q -m c; git -C "$R" push -q --force origin "$1" 2>/dev/null; git -C "$R" checkout -q main; }
pqbody au1; pqbody au2; mka agent/au1 "someone""@mail-corp.invalid"; mka agent/au2 "noreply""@mail-corp.invalid"
for n in au1 au2; do $TB pr-queue add --branch agent/$n --body-file "$PR_QUEUE_BODY_ROOT/$n.md" --title "題 $n" >/dev/null 2>&1; done
au1=$(pqsql "SELECT id FROM pr_queue WHERE branch='agent/au1';"); au2=$(pqsql "SELECT id FROM pr_queue WHERE branch='agent/au2';")
$TB pr-queue approve $au1 >/dev/null 2>&1; $TB pr-queue approve $au2 >/dev/null 2>&1
pqdeny "$PQ_DENY" "$PQ_EX"; $TB pr-queue run >/dev/null 2>&1; pqdeny "$PQ_DENY"
eq "author/committer: an author address that matches a rule is a hit (position author <hash>); the noreply address passes via the exceptions file" "$(dst $au1) $(derr $au1 | grep -c 'r-mail@author ') $(dst $au2)" "rejected0 1 created0"

unset TB_NOTIFY_DEDUPE_SEC TB_NOTIFY_ENABLED TB_NOTIFY_DIR TB_NOTIFY_CONF

# ---- 設定: branch_prefix を変える / body_first_line が空なら 1 行目は見ない
export PR_QUEUE_CONF=$TMP/pq2.conf; printf 'branch_prefix = bot/\n' >"$PR_QUEUE_CONF"
pqbody nf1 'anything on the first line'
out=$(pqadd bot/nf1 nf1); nid=${out%% *}; nid=${nid#PQ-}
eq "config: custom branch_prefix is used, the default one is refused" \
  "$(rc $TB pr-queue add --branch agent/nf2 --body-file "$PR_QUEUE_BODY_ROOT/nf1.md" --title t) ${out#* }" "1 pending"
eq "config: without body_first_line any first line passes the guard" "$(rc $TB pr-queue approve "$nid") $(pqsql "SELECT status FROM pr_queue WHERE id=$nid;")" "0 approved"
printf 'branch_prefix = not valid\n' >"$PR_QUEUE_CONF"
eq "config: an invalid branch_prefix falls back to agent/" "$(pqadd agent/nf3 nf1 | cut -d' ' -f2)" "pending"
unset PR_QUEUE_CONF

# 後片付け: 触った変数を元に戻す
TB=$_tb_saved
for _v in TB_DB TB_REPOS_FILE PR_QUEUE_BODY_ROOT PR_QUEUE_GH FAKE_GH_LOG FAKE_AGENTS; do unset "$_v"; done
while IFS= read -r _l; do [[ -n $_l ]] && export "$_l"; done <<<"$_pq_saved"
[[ -z $_saved_pane ]] || export HERDR_PANE_ID=$_saved_pane
