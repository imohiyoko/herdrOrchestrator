# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- モデル: tb model-set(切り替え)、approvals の agent:model:<名前>、config の role-models(役割の既定)と決まり方の順
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
export TB_DB=$TMP/models.db TB_APPROVALS=$TMP/appr-models.conf TB_SPAWN_RETRY_SEC=0 TB_AGENT_CWD=$TMP TB_LIMITS=$TMP/ms-lim.conf
export TB_ROLE_MODELS=$TMP/role-models.conf
export FAKE_DYNAMIC=1
mssql() { sqlite3 "$TB_DB" "$@"; }
ms_ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"%s","agent_session":{"value":"%s"},"cwd":"%s"}' "$2" "$1" "${3:-idle}" "${4:-s-$1}" "$TMP"; }
ms_reset() { : >"$FAKE_LOG"; rm -f "$FAKE_LOG.closed" "$FAKE_LOG.started" "$FAKE_LOG.ready" "$FAKE_LOG.working"; unset FAKE_START_RC FAKE_START_SESSION FAKE_CLOSE_RC FAKE_STATUS; }
ms_model() { mssql "SELECT COALESCE(model,'-') FROM agents WHERE name='$1';"; }
# ユーザー単位の設定ファイル(切り替えで変わらないことをハッシュで確かめる)
export MS_HOME=$TMP/ms-home; mkdir -p "$MS_HOME/.claude"; printf '{"model":"opus"}\n' >"$MS_HOME/.claude/settings.json"
ms_hash() { cksum <"$MS_HOME/.claude/settings.json"; }
H0=$(ms_hash)
MS_C=$(ms_ag conductor wC:p1); MS_W1=$(ms_ag worker1 wW:p1 idle s1); MS_W2=$(ms_ag worker2 wX:p1 working s2); MS_H=$(ms_ag hr1 wH:p1 idle sh)
export FAKE_AGENTS="[$MS_C,$MS_W1,$MS_W2,$MS_H]"
printf 'agent:model:worker1 = allow\n* = ask\n' >"$TB_APPROVALS"
: >"$TB_ROLE_MODELS"
ms_reset
$TB agents >/dev/null
mssql "UPDATE agents SET cwd='$TMP';"

# ---- 1 体を切り替える: 止めて、同じ session を --model 付きで復活。台帳のモデルが変わる。ユーザー単位の設定は変わらない
out=$(HOME=$MS_HOME $TB model-set worker1 sonnet 2>&1; echo "rc=$?")
eq "model-set: single agent → rc 0, one line per agent, cache notice" "$(tail -1 <<<"$out") $(grep -c '^worker1 changed (既定 -> sonnet' <<<"$out") $(grep -c 'プロンプトキャッシュ' <<<"$out")" "rc=0 1 1"
eq "model-set: pane closed, revived with the same session and --model" "$(grep -c '^pane-close wW:p1' "$FAKE_LOG") $(grep -c '^start worker1 --kind claude --pane wA:p1 -- --resume s1 --model sonnet$' "$FAKE_LOG")" "1 1"
eq "model-set: ledger model updated" "$(ms_model worker1)" "sonnet"
eq "model-set: user-level settings file unchanged (hash)" "$(ms_hash)" "$H0"
eq "model-set: recorded as op=revive with the model in args" "$(mssql "SELECT op||' '||status||' '||json_extract(args,'\$.model')||' '||json_extract(args,'\$.model_set') FROM agent_ops ORDER BY id DESC LIMIT 1;")" "revive done sonnet 1"
eq "model-set: no prompt sent to the agent" "$(grep -c '^prompt worker1' "$FAKE_LOG")" "0"
ms_reset; eq "model-set: same model again → skipped, rc 1, nothing closed" "$($TB model-set worker1 sonnet 2>&1 | grep -c 'skipped (すでに sonnet)') $(rc $TB model-set worker1 sonnet) $(grep -c '^pane-close' "$FAKE_LOG")" "1 1 0"

# ---- --dry-run は計画だけ
ms_reset; out=$($TB model-set worker1 opus --dry-run)
eq "model-set --dry-run: plan only" "$(grep -c '^worker1 would change (sonnet -> opus' <<<"$out") $(grep -c 'dry-run: 何も変えていません' <<<"$out") $(grep -c '^pane-close\|^start' "$FAKE_LOG") $(ms_model worker1)" "1 1 0 sonnet"

# ---- 作業中は飛ばす。指揮官と自分自身は除く
ms_reset; out=$($TB model-set worker2 opus 2>&1; echo "rc=$?")
eq "model-set: working agent skipped with the reason" "$(grep -c '^worker2 skipped (working' <<<"$out") $(tail -1 <<<"$out") $(grep -c '^pane-close' "$FAKE_LOG")" "1 rc=1 0"
eq "model-set: conductor excluded" "$($TB model-set conductor opus 2>&1 | grep -c '^conductor skipped (指揮官は')" "1"
eq "model-set: caller excluded" "$(HERDR_PANE_ID=wW:p1 $TB model-set worker1 opus 2>&1 | grep -c '^worker1 skipped (自分自身は')" "1"
eq "model-set: unknown agent → 1" "$(rc $TB model-set nobody opus)" "1"
eq "model-set: bad model name → 2" "$(rc $TB model-set worker1 'x;y')" "2"
eq "model-set: --reset with a model → 2" "$(rc $TB model-set worker1 opus --reset)" "2"

# ---- 対応していない kind: 何も変えず、専用の終了コード 12
mssql "INSERT INTO agents (name, kind, herdr_session, cwd, session_id, state, killable, created_by, created_at, updated_at) VALUES ('codex1','codex',(SELECT herdr_session FROM agents WHERE name='worker1'),'$TMP','sc','stopped','unknown','human','x','x');"
ms_reset; out=$($TB model-set codex1 gpt-5 2>&1; echo "rc=$?")
eq "model-set: unsupported kind → not supported, rc 12, nothing changed" "$(grep -c '^codex1 not supported (kind=codex' <<<"$out") $(tail -1 <<<"$out") $(ms_model codex1) $(grep -c '^pane-close\|^start' "$FAKE_LOG")" "1 rc=12 - 0"

# ---- 止まっている agent: 台帳だけを変える(次の revive から効く)
mssql "INSERT INTO agents (name, herdr_session, cwd, session_id, state, killable, created_by, created_at, updated_at) VALUES ('idle9',(SELECT herdr_session FROM agents WHERE name='worker1'),'$TMP','s9','stopped','unknown','human','x','x');"
ms_reset; eq "model-set: stopped agent → ledger only" "$($TB model-set idle9 haiku | grep -c '^idle9 changed (既定 -> haiku。停止中') $(ms_model idle9) $(grep -c '^start' "$FAKE_LOG")" "1 haiku 0"
ms_reset; $TB agent-revive idle9 >/dev/null 2>&1
eq "model-set: the ledger model is used on the next revive" "$(grep -c '^start idle9 .* --model haiku$' "$FAKE_LOG")" "1"

# ---- agent の pane から: agent:model:<名前> の承認。allow ならそのまま、ask なら承認待ち(L-<n>)
ms_reset; export FAKE_AGENTS="[$MS_C,$(ms_ag worker1 wW:p1 idle s1),$MS_W2,$MS_H]"
eq "approval: agent:model:<name> defaults to ask" "$($TB approval agent:model:hr1 | head -1)" "ask"
eq "approval: matching allow rule" "$($TB approval agent:model:worker1 | head -1)" "allow"
out=$(HERDR_PANE_ID=wH:p1 $TB model-set worker1 opus 2>&1; echo "rc=$?")
eq "model-set from an agent pane, allowed → runs" "$(tail -1 <<<"$out") $(ms_model worker1)" "rc=0 opus"
mssql "UPDATE agents SET cwd='$TMP';"
ms_reset; export FAKE_AGENTS="[$MS_C,$(ms_ag worker1 wW:p1 idle s1),$(ms_ag worker2 wX:p1 idle s2),$MS_H]"
out=$(HERDR_PANE_ID=wW:p1 $TB model-set worker2 haiku 2>&1; echo "rc=$?")
lid=$(grep -o 'L-[0-9]*' <<<"$out" | head -1)
eq "model-set from an agent pane, ask → pending L-<n>, rc 20, nothing changed" "$(tail -1 <<<"$out") ${lid:+lid} $(ms_model worker2) $(grep -c '^pane-close' "$FAKE_LOG")" "rc=20 lid - 0"
eq "model-set pending: recorded as op=revive with args.model_set" "$(mssql "SELECT op||' '||status||' '||json_extract(args,'\$.model') FROM agent_ops WHERE id=${lid#L-};")" "revive pending haiku"
out=$($TB agent-approve "$lid" 2>&1; echo "rc=$?")
eq "agent-approve runs the model switch" "$(tail -1 <<<"$out") $(ms_model worker2) $(grep -c '^start worker2 .* --resume s2 --model haiku$' "$FAKE_LOG")" "rc=0 haiku 1"
# 役割の glob でも同じ経路(@worker = allow)
printf 'agent:model:@worker = allow\n* = ask\n' >"$TB_APPROVALS"
eq "approval: @role rule takes effect" "$($TB approval agent:model:worker2 | head -1) $($TB approval agent:model:hr1 | head -1)" "allow ask"
printf 'agent:model:worker1 = allow\n* = ask\n' >"$TB_APPROVALS"

# ---- @役割 と --all
mssql "UPDATE agents SET cwd='$TMP';"
ms_reset; export FAKE_AGENTS="[$MS_C,$(ms_ag worker1 wW:p1 idle s1),$(ms_ag worker2 wX:p1 working s2),$MS_H]"
out=$($TB model-set @worker sonnet 2>&1; echo "rc=$?")
eq "model-set @role: matching agents only, one line each" "$(grep -c '^worker1 changed' <<<"$out") $(grep -c '^worker2 skipped (working' <<<"$out") $(grep -c '^hr1 ' <<<"$out") $(tail -1 <<<"$out")" "1 1 0 rc=0"
ms_reset; export FAKE_AGENTS="[$MS_C,$(ms_ag worker1 wW:p1 idle s1),$(ms_ag worker2 wX:p1 idle s2),$MS_H]"
n0=$(mssql "SELECT COUNT(*) FROM agent_ops WHERE status='pending';")
out=$($TB model-set --all opus 2>&1; echo "rc=$?")
eq "model-set --all: held for approval even for a human, nothing changed" "$(tail -1 <<<"$out") $(grep -c 'pending L-' <<<"$out") $(grep -c '^pane-close' "$FAKE_LOG") $(ms_model worker1)" "rc=20 4 0 sonnet"
eq "model-set --all: conductor excluded" "$(grep -c '^conductor skipped' <<<"$out") $(( $(mssql "SELECT COUNT(*) FROM agent_ops WHERE status='pending';") - n0 ))" "1 4"
eq "model-set --all --dry-run: plan only" "$($TB model-set --all opus --dry-run | grep -c 'would hold for approval') $(( $(mssql "SELECT COUNT(*) FROM agent_ops WHERE status='pending';") - n0 ))" "4 4"
mssql "UPDATE agent_ops SET status='rejected' WHERE status='pending';"

# ---- role-models(役割の既定)と決まり方の順: 明示 > 台帳 > 役割の既定 > CLI の既定
printf '# 例\nhr = haiku\nwork* = sonnet\n' >"$TB_ROLE_MODELS"
eq "config show role-models" "$($TB config show role-models 2>/dev/null | grep -c '^hr = haiku$')" "1"
unset TB_ROLE_MODELS
eq "config show role-models: example by default" "$($TB config show role-models 2>&1 >/dev/null | grep -c 'role-models.conf.example')" "1"
eq "config set role-models: bad model refused, nothing written" "$(printf 'hr = x;y\n' | rc $TB config set role-models) $(src_of=$($TB config list | grep '^role-models'); grep -c example <<<"$src_of")" "1 1"
eq "config set role-models: bad line refused" "$(printf 'hr haiku\n' | rc $TB config set role-models)" "1"
printf 'hr = haiku\n' | $TB config set role-models >/dev/null
eq "config set role-models: valid body stored in the DB" "$($TB config list | grep '^role-models' | grep -c 'db:')" "1"
$TB config unset role-models >/dev/null
export TB_ROLE_MODELS=$TMP/role-models.conf
# 役割の既定: 台帳に値が無い agent の新規作成・復活で使う。台帳には書かない
ms_reset; export FAKE_AGENTS="[$MS_C]"
$TB agent-new hr2 --role hr --cwd "$TMP" >/dev/null 2>&1
eq "role default: agent-new without --model uses the role default" "$(grep -c '^start hr2 --kind claude --pane wA:p1 -- --model haiku$' "$FAKE_LOG") $(ms_model hr2)" "1 -"
ms_reset; $TB agent-new hr3 --role hr --model opus --cwd "$TMP" >/dev/null 2>&1
eq "precedence: explicit --model beats the role default" "$(grep -c '^start hr3 .* --model opus$' "$FAKE_LOG") $(ms_model hr3)" "1 opus"
mssql "UPDATE agents SET state='stopped', pane_id=NULL, session_id='sh3' WHERE name IN ('hr2','hr3');"
ms_reset; export FAKE_AGENTS="[$MS_C]"
$TB agent-revive hr3 >/dev/null 2>&1; $TB agent-revive hr2 >/dev/null 2>&1
eq "precedence: ledger value beats the role default on revive" "$(grep -c '^start hr3 .* --model opus$' "$FAKE_LOG")" "1"
eq "precedence: role default on revive when the ledger is empty" "$(grep -c '^start hr2 .* --model haiku$' "$FAKE_LOG") $(ms_model hr2)" "1 -"
# 表を変えても、台帳に値がある agent は変わらない。--reset で台帳を消すと表が効く
printf 'hr = sonnet\n' >"$TB_ROLE_MODELS"
mssql "UPDATE agents SET state='stopped', pane_id=NULL WHERE name IN ('hr2','hr3');"
ms_reset; $TB agent-revive hr3 >/dev/null 2>&1
eq "role default change does not touch agents with a ledger value" "$(grep -c '^start hr3 .* --model opus$' "$FAKE_LOG")" "1"
mssql "UPDATE agents SET state='stopped', pane_id=NULL WHERE name='hr3';"
out=$($TB model-set hr3 --reset 2>&1)
eq "model-set --reset: clears the ledger value and names the role default" "$(grep -c '^hr3 reset (台帳のモデルを消しました。次の起動・復活から 役割の既定 sonnet' <<<"$out") $(ms_model hr3)" "1 -"
ms_reset; $TB agent-revive hr3 >/dev/null 2>&1
eq "after --reset the role default applies" "$(grep -c '^start hr3 .* --model sonnet$' "$FAKE_LOG")" "1"
# 役割の既定は、モデル指定に対応していない kind には渡さない
printf 'hr = sonnet\n* = haiku\n' >"$TB_ROLE_MODELS"
ms_reset; $TB agent-new cx1 --kind codex --cwd "$TMP" >/dev/null 2>&1
eq "role default is not passed to kinds without model support" "$(grep -c '^start cx1 --kind codex --pane wA:p1$' "$FAKE_LOG")" "1"
# name:<agent 名の glob> の行: 役割の無い agent にも効く。上から最初に当たった行
printf 'name:solo* = haiku\nhr = sonnet\n' >"$TB_ROLE_MODELS"
ms_reset; $TB agent-new solo1 --cwd "$TMP" >/dev/null 2>&1
eq "role-models name: line applies to an agent without a role" "$(grep -c '^start solo1 --kind claude --pane wA:p1 -- --model haiku$' "$FAKE_LOG")" "1"
ms_reset; $TB agent-new hr9 --role hr --cwd "$TMP" >/dev/null 2>&1
eq "role-models: role lines still apply below a name: line" "$(grep -c '^start hr9 .* --model sonnet$' "$FAKE_LOG")" "1"
_saved_rm=$TB_ROLE_MODELS; unset TB_ROLE_MODELS
eq "config set role-models: name: lines are accepted, a bad glob is refused" "$(printf 'name:watch* = haiku\n' | rc $TB config set role-models) $(printf 'name:a/b = haiku\n' | rc $TB config set role-models)" "0 1"
$TB config unset role-models >/dev/null; export TB_ROLE_MODELS=$_saved_rm
: >"$TB_ROLE_MODELS"

unset FAKE_AGENTS FAKE_DYNAMIC TB_ROLE_MODELS TB_LIMITS MS_HOME; export TB_APPROVALS=$TMP/appr.conf; export HERDR_PANE_ID=$_saved_pane
