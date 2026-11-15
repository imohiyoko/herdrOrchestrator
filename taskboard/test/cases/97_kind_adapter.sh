# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- agent の kind アダプタ(lib/kinds.sh): claude は従来どおり、codex / gemini は対応していない項目をはっきり止める
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
export TB_DB=$TMP/kinds.db TB_APPROVALS=$TMP/appr-kinds.conf TB_SPAWN_RETRY_SEC=0 TB_AGENT_CWD=$TMP TB_LIMITS=$TMP/kd-lim.conf
printf '* = allow\n' >"$TB_APPROVALS"
unset FAKE_DYNAMIC FAKE_START_RC FAKE_CLOSE_RC FAKE_STATUS
kdsql() { sqlite3 "$TB_DB" "$@"; }
kd_ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"idle","agent_session":{"value":"%s"},"cwd":"%s"%s}' "$2" "$1" "$3" "$TMP" "${4:-}"; }
KD_C=$(kd_ag conductor wC:p1 sc)
export FAKE_AGENTS="[$KD_C]"
: >"$FAKE_LOG"
$TB agents >/dev/null

# ---- 一覧(アダプタの切り替えが見える)
eq "kinds: list shows every kind with its items" "$($TB agent-kinds | cut -f1 | tr '\n' ' ')" "claude codex gemini "
eq "kinds: claude supports everything" "$($TB agent-kinds | grep '^claude' | cut -f2-6 | tr '\t' ' ')" "launch=yes model=yes resume=yes live-model=yes session=herdr"
eq "kinds: codex / gemini only launch" "$($TB agent-kinds | grep -v '^claude' | cut -f2-5 | tr '\t' ' ' | tr '\n' '|')" "launch=yes model=no resume=no live-model=no|launch=yes model=no resume=no live-model=no|"

# ---- claude: 従来どおり(--kind なし = claude。--kind claude も同じ)
$TB agent-new kc1 --cwd "$TMP" --model haiku >/dev/null
$TB agent-new kc2 --cwd "$TMP" --kind claude --model haiku >/dev/null
eq "claude: default kind starts exactly like before" "$(grep -c '^start kc1 --kind claude --pane wA:p1 -- --model haiku$' "$FAKE_LOG")" "1"
eq "claude: explicit --kind claude is the same command" "$(grep -c '^start kc2 --kind claude --pane wA:p1 -- --model haiku$' "$FAKE_LOG")" "1"
eq "claude: ledger kind is claude" "$(kdsql "SELECT kind FROM agents WHERE name IN ('kc1','kc2') ORDER BY name;" | tr '\n' ' ')" "claude claude "
eq "claude: model rule unchanged (bad chars → 2)" "$(rc $TB agent-new kc3 --model 'x;y')" "2"

# ---- kind 未指定の古い台帳(kind 列は既定 claude): 復活は --resume と --model を引き継ぐ
HS=$(kdsql "SELECT herdr_session FROM agents WHERE name='kc1';")
kdsql "INSERT INTO agents (name, herdr_session, cwd, model, session_id, state, killable, created_by, created_at, updated_at)
  VALUES ('legacy1', '$HS', '$TMP', 'opus', 'ls1', 'stopped', 'unknown', 'human', 't', 't');"
: >"$FAKE_LOG"
out=$($TB agent-revive legacy1)
eq "back-compat: row without kind resumes as claude, inherits the model" "$(cut -d' ' -f1-4 <<<"$out") $(grep -c '^start legacy1 --kind claude --pane wA:p1 -- --resume ls1 --model opus$' "$FAKE_LOG")" "legacy1 revived (resume ls1, 1"

# ---- codex: 起動だけ。--model / resume は黙って claude 前提で動かさず、終了コード 12 と項目名で止める
: >"$FAKE_LOG"
out=$($TB agent-new kx1 --cwd "$TMP" --kind codex 2>&1; echo "rc=$?")
eq "codex: agent-new without model starts with --kind codex and no claude flags" "$(tail -1 <<<"$out") $(grep -c '^start kx1 --kind codex --pane wA:p1$' "$FAKE_LOG") $(kdsql "SELECT kind FROM agents WHERE name='kx1';")" "rc=0 1 codex"
: >"$FAKE_LOG"
out=$($TB agent-new kx2 --cwd "$TMP" --kind codex --model gpt-x 2>&1; echo "rc=$?")
eq "codex: --model → rc 12 naming kind and item" "$(grep -c 'kind=codex.*item=model' <<<"$out") $(tail -1 <<<"$out")" "1 rc=12"
eq "codex: refused before anything starts or is registered" "$(grep -c '^start\|^workspace-create' "$FAKE_LOG") $(kdsql "SELECT COUNT(*) FROM agents WHERE name='kx2';")" "0 0"
eq "gemini: --model → rc 12" "$(rc $TB agent-new kx3 --cwd "$TMP" --kind gemini --model m)" "12"
eq "gemini: starts without model" "$($TB agent-new kx4 --cwd "$TMP" --kind gemini | cut -d' ' -f1-2) $(grep -c '^start kx4 --kind gemini --pane wA:p1$' "$FAKE_LOG")" "kx4 started 1"
eq "kind: unknown name → usage error 2" "$(rc $TB agent-new kx5 --kind nosuch)" "2"

# ---- codex の復活: 会話の再開(resume)と --model は 12。--fresh なら新しい会話として起動できる
kdsql "UPDATE agents SET state='stopped', pane_id=NULL, session_id='cs1', cwd='$TMP' WHERE name='kx1';"
: >"$FAKE_LOG"
out=$($TB agent-revive kx1 2>&1; echo "rc=$?")
eq "codex revive: resume unsupported → rc 12 naming the item" "$(grep -c 'kind=codex.*item=resume' <<<"$out") $(tail -1 <<<"$out") $(grep -c '^start' "$FAKE_LOG")" "1 rc=12 0"
eq "codex revive: ledger stays stopped" "$(kdsql "SELECT state FROM agents WHERE name='kx1';")" "stopped"
eq "codex revive --model → rc 12 (item=model)" "$($TB agent-revive kx1 --fresh --model m 2>&1 | grep -c 'item=model') $(rc $TB agent-revive kx1 --fresh --model m)" "1 12"
out=$($TB agent-revive kx1 --fresh 2>&1; echo "rc=$?")
eq "codex revive --fresh: new conversation, no --resume / --model" "$(tail -1 <<<"$out") $(grep -c '^start kx1 --kind codex --pane wA:p1$' "$FAKE_LOG")" "rc=0 1"

# ---- agent_sync: herdr が教える kind(.agent)を台帳に載せる。教えなければ claude
export FAKE_AGENTS="[$KD_C,$(kd_ag viaherdr wV:p1 sv ',"agent":"codex"'),$(kd_ag plain wP:p1 sp)]"
$TB agents >/dev/null
eq "sync: kind comes from herdr, default claude" "$(kdsql "SELECT name||'='||kind FROM agents WHERE name IN ('viaherdr','plain') ORDER BY name;" | tr '\n' ' ')" "plain=claude viaherdr=codex "

# ---- 同名の別個体(停止済み claude の行 + 外部で起動された codex)は kind を更新する。同一個体・未知の値は保持
kdsql "INSERT INTO agents (name, kind, herdr_session, cwd, session_id, state, killable, created_by, created_at, updated_at)
  VALUES ('swap1', 'claude', '$HS', '$TMP', 'old', 'stopped', 'unknown', 'human', 't', 't');"
export FAKE_AGENTS="[$KD_C,$(kd_ag swap1 wS:p1 newcodex ',"agent":"codex"')]"
$TB agents >/dev/null
eq "sync: same name, different kind from herdr → kind updated" "$(kdsql "SELECT kind FROM agents WHERE name='swap1';")" "codex"
: >"$FAKE_LOG"
eq "sync→restart: swapped codex refused with 12 before pane close" "$(rc $TB agent-restart swap1) $(grep -c '^pane-close' "$FAKE_LOG")" "12 0"
export FAKE_AGENTS="[$KD_C,$(kd_ag swap1 wS:p1 newcodex ',"agent":"codex"')]"; $TB agents >/dev/null
eq "sync: same individual keeps kind" "$(kdsql "SELECT kind FROM agents WHERE name='swap1';")" "codex"
export FAKE_AGENTS="[$KD_C,$(kd_ag swap1 wS:p1 newcodex ',"agent":"foo"'),$(kd_ag newfoo wF:p1 sf ',"agent":"foo"')]"; $TB agents >/dev/null
eq "sync: unknown herdr kind keeps existing row, new row gets claude" "$(kdsql "SELECT name||'='||kind FROM agents WHERE name IN ('swap1','newfoo') ORDER BY name;" | tr '\n' ' ')" "newfoo=claude swap1=codex "

# ---- agent-restart: 再開できない kind は、pane を閉じる前に 12 で止める(閉じてから失敗すると会話を失う)
export FAKE_AGENTS="[$KD_C,$(kd_ag viaherdr wV:p1 sv ',"agent":"codex"')]"
: >"$FAKE_LOG"; rm -f "$FAKE_LOG.closed"
eq "codex restart: refused with 12, pane is NOT closed" "$(rc $TB agent-restart viaherdr) $(grep -c '^pane-close' "$FAKE_LOG")" "12 0"

# ---- compact-check: relies on the claude transcript, so other kinds are skipped with a stated reason
mkdir -p "$TMP/kd-claude"
printf 'enabled = 1\nthreshold_tokens = 1\nmin_interval_sec = 0\n' >"$TMP/kd-compact.conf"
eq "compact-check: non-claude kind is skipped with a reason" "$(TB_COMPACT_CONF=$TMP/kd-compact.conf TB_CLAUDE_PROJECTS=$TMP/kd-claude $TB compact-check 2>&1 | grep -c '^viaherdr - skip(kind=codex')" "1"

unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
