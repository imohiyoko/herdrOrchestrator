# shellcheck shell=bash
# レシピの外の実行役(ADR 0005 の段 3): 待ち行列(R-<n>)、tb recipe-runner(実行の時点で依頼者の権限で判定し直す)、
# 副作用のある部品の実行、定期ジョブの持ち主(SCHED_JOB / SCHED_OWNER)。DB は $TMP/rrun.db、project は rrn だけ。環境変数は最後に戻す
_saved_pane=${HERDR_PANE_ID-}; _saved_db=$TB_DB; _saved_appr=${TB_APPROVALS-}; _saved_agents=${FAKE_AGENTS-}; _saved_lim=${TB_LIMITS-}
_saved_env=$(declare -p TB_SPAWN_RETRY_SEC TB_AGENT_CWD TB_RECIPE_POLL_SEC FAKE_DYNAMIC FAKE_STATUS SCHED_JOB SCHED_OWNER 2>/dev/null)
unset HERDR_PANE_ID SCHED_JOB SCHED_OWNER
export TB_DB=$TMP/rrun.db TB_APPROVALS=$TMP/rr-approvals.conf TB_SPAWN_RETRY_SEC=0 TB_AGENT_CWD=$TMP TB_LIMITS=$TMP/rr-lim.conf TB_RECIPE_POLL_SEC=0.05
export FAKE_DYNAMIC=1
rrsql() { sqlite3 "$TB_DB" "$@"; }
rr_ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"idle","agent_session":{"value":"%s"},"cwd":"%s"}' "$2" "$1" "${3:-s-$1}" "$TMP"; }
rr_reset() { : >"$FAKE_LOG"; rm -f "$FAKE_LOG.closed" "$FAKE_LOG.started" "$FAKE_LOG.ready" "$FAKE_LOG.working"; unset FAKE_STATUS; }
rr_closes() { grep -c '^pane-close' "$FAKE_LOG"; }
asw() { HERDR_PANE_ID=wW:p1 "$@"; }   # worker1 の pane から
ash() { HERDR_PANE_ID=wH:p1 "$@"; }   # helper1 の pane から
export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"
printf 'recipe:*:* = allow\nagent:stop:worker1 = allow\nagent:revive:worker1 = allow\nagent:stop:conductor = allow\nagent:revive:conductor = allow\n* = ask\n' >"$TB_APPROVALS"
rr_reset
$TB agents >/dev/null
$TB recipe-set restart-self >/dev/null <<'EOF'
desc 自分を安全に再起動する
arg note? text:500
step agent-status  target=$requester
step wait-idle     target=$requester timeout=5
step agent-restart target=$requester prompt=$note
EOF
$TB recipe-set restart-other >/dev/null <<'EOF'
step agent-restart target=$target
EOF
$TB recipe-set force >/dev/null <<'EOF'
step agent-force-stop target=$target
EOF

# --- 依頼者の pane では判定して待ち行列に置くだけ(自分は止めない)
out=$(asw "$TB" recipe-run restart-self 'note=続きは T-1 から' 2>&1); r=$?
eq "runner: agent の依頼は判定して R-<n> で待ち行列に置く(0)" "$r|$(tail -1 <<<"$out" | cut -d' ' -f1-2)" "0|R-1 queued"
eq "runner: 置いただけでは何も止めていない" "$(rr_closes) $(grep -c '^get ' "$FAKE_LOG")" "0 0"
eq "runner: 待ち行列の行(依頼者・対象・状態)" "$(rrsql "SELECT requester||' '||target||' '||status FROM recipe_runs WHERE id=1;")" "worker1 worker1 queued"
eq "runner: 並びは検査済みの値で、空白を含む値もそのまま" "$(rrsql "SELECT json_extract(plan,'\$[2].args.prompt')||'|'||json_extract(plan,'\$[0].args.target')||'|'||json_array_length(plan) FROM recipe_runs WHERE id=1;")" "続きは T-1 から|worker1|3"
eq "recipe-queue: queued が出る" "$($TB recipe-queue | cut -f1-5)" "$(printf 'R-1\tqueued\trestart-self\tworker1\tworker1')"
eq "recipe-queue --json: plan は JSON" "$($TB recipe-queue --json | jq -r '.plan[2].part')" "agent-restart"

# --- 実行役は人の端末からだけ
eq "runner: agent の pane からは動かない" "$(asw rc "$TB" recipe-runner) $(rrsql "SELECT status FROM recipe_runs WHERE id=1;")" "1 queued"

# --- 実行役が並びどおりに実行する(依頼者の権限で判定し直してから)
out=$($TB recipe-runner 2>&1); r=$?
eq "runner: 実行して done" "$r|$out" "0|R-1 done"
eq "runner: worker1 を止めて同じ会話で戻した" "$(grep -c '^pane-close wW:p1' "$FAKE_LOG") $(grep -c '^start worker1 .*--resume s1$' "$FAKE_LOG")" "1 1"
eq "runner: 引数の prompt を最初の指示にした" "$(grep -c '^prompt worker1.*続きは T-1 から' "$FAKE_LOG")" "1"
eq "runner: agent_ops は依頼者の名前と R-<n> で残る" "$(rrsql "SELECT op||' '||status||' '||requested_by||' '||json_extract(args,'\$.recipe') FROM agent_ops ORDER BY id DESC LIMIT 1;")" "revive done worker1 recipe:R-1"
eid=$(rrsql "SELECT event_id FROM recipe_runs WHERE id=1;")
eq "runner: 記録は recipe_run の行にまとまる(置いた・step × 3・done)" \
   "$(rrsql "SELECT kind FROM task_events WHERE ref='E-$eid' ORDER BY id;" | paste -sd' ') $(rrsql "SELECT new FROM task_events WHERE ref='E-$eid' AND kind='recipe_end';")" \
   "recipe_step recipe_step recipe_step recipe_step recipe_end done"
eq "runner: step の記録は依頼者が actor" "$(rrsql "SELECT DISTINCT actor FROM task_events WHERE ref='E-$eid';")" "worker1"
eq "runner: 待ち行列が空なら何もしない" "$($TB recipe-runner; echo "rc=$?")" "rc=0"
eq "recipe-queue: 済んだものは既定では出ない(--all で出る)" "$($TB recipe-queue | wc -l | tr -d ' ') $($TB recipe-queue --all | cut -f2 | paste -sd,)" "0 done"

# --- 実行の時点で判定し直す: 置いた後に許可が外れたら実行しない
rr_reset; export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"
asw "$TB" recipe-run restart-self >/dev/null 2>&1
printf 'recipe:*:* = allow\nagent:revive:worker1 = allow\n* = ask\n' >"$TB_APPROVALS"
eq "runner: 置いた後に agent:stop の許可が外れたら stopped で、何も止めない" "$($TB recipe-runner) $(rr_closes)" "R-2 stopped 0"
eq "runner: 理由を残す" "$(rrsql "SELECT result FROM recipe_runs WHERE id=2;" | grep -c 'agent:stop:worker1')" "1"
printf 'agent:stop:worker1 = allow\nagent:revive:worker1 = allow\n* = ask\n' >"$TB_APPROVALS"
rrsql "INSERT INTO recipe_runs (herdr_session, name, body_sha, target, requester, task_id, plan, event_id, status, created_at) SELECT herdr_session, name, body_sha, target, requester, task_id, plan, event_id, 'queued', created_at FROM recipe_runs WHERE id=1;"
eq "runner: レシピの操作名の許可が外れても実行しない" "$($TB recipe-runner) $(rr_closes)" "R-3 stopped 0"
printf 'recipe:*:* = allow\nagent:stop:worker1 = allow\nagent:revive:worker1 = allow\nagent:stop:conductor = allow\nagent:revive:conductor = allow\n* = ask\n' >"$TB_APPROVALS"

# --- レシピが変わった・消えたら実行しない
asw "$TB" recipe-run restart-self >/dev/null 2>&1
$TB recipe-set restart-self >/dev/null <<'EOF'
desc 自分を安全に再起動する(待ちを長く)
arg note? text:500
step agent-status  target=$requester
step wait-idle     target=$requester timeout=30
step agent-restart target=$requester prompt=$note
EOF
eq "runner: 置いた後にレシピの本文が変わったら実行しない" "$($TB recipe-runner) $(rr_closes)" "R-4 stopped 0"
asw "$TB" recipe-run restart-self >/dev/null 2>&1
$TB recipe-set gone-soon >/dev/null <<<'step agent-restart target=$requester'
asw "$TB" recipe-run gone-soon >/dev/null 2>&1
$TB recipe-rm gone-soon >/dev/null
eq "runner: 消えたレシピは実行しない(古い順に 1 件ずつ)" "$($TB recipe-runner --max 5 | paste -sd,) $(rr_closes)" "R-5 done,R-6 stopped 1"

# --- 古すぎるものは実行しない
rr_reset; export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"
asw "$TB" recipe-run restart-self >/dev/null 2>&1
rrsql "UPDATE recipe_runs SET created_at='2026-01-01T00:00:00Z' WHERE id=7;"
eq "runner: 置いてから既定の 900 秒を過ぎたら expired" "$($TB recipe-runner; rrsql "SELECT status FROM recipe_runs WHERE id=7;") $(rr_closes)" "expired 0"

rrsql "INSERT INTO recipe_runs (herdr_session, name, body_sha, target, requester, task_id, plan, event_id, status, created_at, started_at) SELECT herdr_session, name, body_sha, target, requester, task_id, plan, event_id, 'running', created_at, '2026-01-01T00:00:00Z' FROM recipe_runs WHERE id=1;"
stuck=$(rrsql "SELECT max(id) FROM recipe_runs;")
eq "runner: running のまま既定の 3600 秒を過ぎたものは failed(実行役が途中で落ちた)" "$($TB recipe-runner; rrsql "SELECT status FROM recipe_runs WHERE id=$stuck;") $(rr_closes)" "failed 0"

# --- 取り消し
asw "$TB" recipe-run restart-self >/dev/null 2>&1
eq "recipe-cancel: 頼んだ agent 以外は取り消せない" "$(ash rc "$TB" recipe-cancel R-9)" "1"
eq "recipe-cancel: 頼んだ agent は取り消せる" "$(asw "$TB" recipe-cancel R-9)" "R-9 cancelled"
eq "recipe-cancel: 取り消したものは実行しない" "$($TB recipe-runner; rrsql "SELECT status FROM recipe_runs WHERE id=9;") $(rr_closes)" "cancelled 0"
eq "recipe-cancel: 済んだものは取り消せない" "$(rc "$TB" recipe-cancel R-1)" "1"
eq "recipe-cancel: 形の誤りは 2" "$(rc "$TB" recipe-cancel 8)" "2"

# --- 指揮官は agent から止めない・戻さない(tb agent-restart と同じ)
eq "runner: agent から指揮官を対象にした再起動は 4(許可があっても)" "$(asw rc "$TB" recipe-run restart-other --target conductor) $(rrsql "SELECT count(*) FROM recipe_runs WHERE target='conductor';")" "4 0"
eq "runner: 人の端末からなら指揮官も判定を通る(--dry-run)" "$(rc "$TB" recipe-run restart-other --target conductor --dry-run)" "0"

# --- 人だけの部品(強制停止): agent からは置けない。人が頼めば実行役が実行する
eq "runner: 人だけの部品を含むレシピは agent から 4" "$(ash rc "$TB" recipe-run force --target helper1)" "4"
rr_reset; export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"
n0=$(rrsql "SELECT count(*) FROM recipe_runs;")
eq "runner: 人の依頼は待ち行列に置かず、その場で実行する(強制停止)" "$(rc "$TB" recipe-run force --target helper1) $(grep -c '^pane-close wH:p1' "$FAKE_LOG") $(($(rrsql "SELECT count(*) FROM recipe_runs;") - n0))" "0 1 0"
eq "runner: 強制停止の記録は人の依頼で force=1" "$(rrsql "SELECT op||' '||requested_by||' '||json_extract(args,'\$.force') FROM agent_ops ORDER BY id DESC LIMIT 1;")" "stop human 1"
$TB agents >/dev/null
rrsql "INSERT INTO recipe_runs (herdr_session, name, body_sha, target, requester, task_id, plan, event_id, status, created_at) SELECT herdr_session, name, body_sha, 'helper1', 'human', task_id, '[{\"part\":\"agent-force-stop\",\"args\":{\"target\":\"helper1\"}}]', event_id, 'queued', strftime('%Y-%m-%dT%H:%M:%SZ','now') FROM recipe_runs WHERE id=1;"
rr_reset; export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"
eq "runner: 依頼者が human の行(直接書かれたもの)は実行しない" "$($TB recipe-runner | cut -d' ' -f2) $(rr_closes)" "stopped 0"

# --- 途中で失敗したら残りは実行しない
rr_reset; export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"
$TB recipe-set restart-self >/dev/null <<'EOF'
step wait-idle     target=$requester timeout=1
step agent-restart target=$requester
EOF
asw "$TB" recipe-run restart-self >/dev/null 2>&1
eq "runner: wait-idle が時間切れなら failed で、再起動しない" "$(FAKE_STATUS=working "$TB" recipe-runner) $(rr_closes)" "R-11 failed 0"
eq "runner: 失敗の理由と、それ以降を実行していないことを残す" "$(rrsql "SELECT result FROM recipe_runs WHERE id=11;" | grep -c 'step 1 (wait-idle) が失敗しました.*step 2 以降は実行していません')" "1"

# --- 期限は取り出すたびに見る(前の依頼の実行に時間がかかっても、期限を過ぎた依頼は実行しない)
asw "$TB" recipe-run restart-self >/dev/null 2>&1; asw "$TB" recipe-run restart-self >/dev/null 2>&1
rrsql "UPDATE recipe_runs SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-99.5 seconds') WHERE id=13;"
eq "runner: 1 件目(wait-idle で 1 秒)のあいだに期限を過ぎた 2 件目は expired" \
   "$(TB_RECIPE_QUEUE_TTL_SEC=100 FAKE_STATUS=working "$TB" recipe-runner --max 2 | paste -sd,) $(rrsql "SELECT status FROM recipe_runs WHERE id=13;")" "R-12 failed expired"

# --- 改行を含む値も分けずに渡す(text 型は改行を受ける)
rr_reset; export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"
$TB recipe-set restart-memo >/dev/null <<'EOF'
arg note? text:500
step agent-restart target=$requester prompt=$note
EOF
out=$(asw "$TB" recipe-run restart-memo "note=$(printf '引継ぎ\n次は T-2')" 2>&1); r=$?
eq "runner: 改行を含む再開メモでも置ける" "$r|$(tail -1 <<<"$out" | cut -d' ' -f2)" "0|queued"
eq "runner: 改行を含む再開メモがそのまま実行される" "$($TB recipe-runner | cut -d' ' -f2) $(rrsql "SELECT json_extract(plan,'\$[0].args.prompt') FROM recipe_runs WHERE id=14;" | wc -l | tr -d ' ')" "done 2"

rr_reset; export FAKE_AGENTS="[$(rr_ag conductor wC:p1),$(rr_ag worker1 wW:p1 s1),$(rr_ag helper1 wH:p1 sh)]"   # 再起動で pane が変わったので戻す

# --- 定期ジョブ(scheduler が SCHED_JOB を渡す)からは、持ち主(OWNER → SCHED_OWNER)が無ければ実行しない
n0=$(rrsql "SELECT count(*) FROM recipe_runs;")
eq "sched: 持ち主の無いジョブからは 4(人の権限を借りない)" "$(SCHED_JOB=nightly rc "$TB" recipe-run restart-self --target worker1) $(($(rrsql "SELECT count(*) FROM recipe_runs;") - n0))" "4 0"
eq "sched: 理由を出す" "$(SCHED_JOB=nightly "$TB" recipe-run restart-other --target worker1 2>&1 | grep -c '持ち主(OWNER=<agent 名>)があるときだけ')" "1"
eq "sched: OWNER が agent 名でなければ 4" "$(SCHED_JOB=nightly SCHED_OWNER=Bad rc "$TB" recipe-run restart-self)" "4"
out=$(SCHED_JOB=nightly SCHED_OWNER=worker1 "$TB" recipe-run restart-self 2>&1); r=$?
eq "sched: 持ち主があれば、その agent の権限で判定して置く" "$r|$(tail -1 <<<"$out" | cut -d' ' -f2)|$(rrsql "SELECT requester||' '||target||' '||via FROM recipe_runs ORDER BY id DESC LIMIT 1;")" "0|queued|worker1 worker1 sched:nightly"
printf 'recipe:*:* = allow\n* = ask\n' >"$TB_APPROVALS"
eq "sched: 持ち主の権限が足りなければ人の端末でも 20(人として扱わない)" "$(SCHED_JOB=nightly SCHED_OWNER=helper1 rc "$TB" recipe-run restart-other --target helper1)" "20"
eq "sched: 持ち主の権限で、人だけの部品は 4" "$(SCHED_JOB=nightly SCHED_OWNER=helper1 rc "$TB" recipe-run force --target helper1)" "4"
eq "sched: agent の pane では SCHED_* を見ない(pane の agent が依頼者)" "$(SCHED_JOB=nightly SCHED_OWNER=conductor asw "$TB" recipe-run restart-self 2>/dev/null | head -1)" "recipe restart-self target=worker1 risk=mid: allow(recipe:*:* = allow)"
$TB recipe-cancel "$(rrsql "SELECT 'R-'||id FROM recipe_runs WHERE status='queued' ORDER BY id LIMIT 1;")" >/dev/null 2>&1 || true

# --- その場で実行するもの(外の実行役の部品が無い並び)は、副作用があっても依頼者の pane の中で実行する
tid=$($TB add -p rrn -t 'レシピの記録')
cid=$(HERDR_PANE_ID=wW:p1 FAKE_STATUS=working "$TB" comment "$tid" --kind question --text 'どちらにしますか' --json 2>/dev/null | jq -r '.id // empty')
[[ -n $cid ]] || cid=C-$(rrsql "SELECT max(id) FROM comments;")
$TB recipe-set resolve-it >/dev/null <<<$'arg c comment\nstep comment-resolve comment=$c'
eq "inside: 人の端末から、報告を対応済みにする(待ち行列に置かない)" "$($TB recipe-run resolve-it --target worker1 "c=$cid" | tail -1) $(rrsql "SELECT resolved_at IS NOT NULL FROM comments WHERE id=${cid#C-};")" "$cid resolved 1"
eq "inside: 人だけの部品なので agent からは 4" "$(asw rc "$TB" recipe-run resolve-it "c=$cid")" "4"
eq "recipe: recipe_runs にもセッションが入る" "$(rrsql "SELECT count(*) FROM recipe_runs WHERE herdr_session='';")" "0"

export TB_DB=$_saved_db
if [[ -n $_saved_appr ]]; then export TB_APPROVALS=$_saved_appr; else unset TB_APPROVALS; fi
if [[ -n $_saved_agents ]]; then export FAKE_AGENTS=$_saved_agents; else unset FAKE_AGENTS; fi
if [[ -n $_saved_lim ]]; then export TB_LIMITS=$_saved_lim; else unset TB_LIMITS; fi
[[ -z $_saved_pane ]] || export HERDR_PANE_ID=$_saved_pane
unset FAKE_DYNAMIC TB_RECIPE_POLL_SEC TB_SPAWN_RETRY_SEC TB_AGENT_CWD FAKE_STATUS SCHED_JOB SCHED_OWNER
[[ -z $_saved_env ]] || eval "${_saved_env//declare -/declare -g}"   # 元の値(と設定の有無)に戻す
