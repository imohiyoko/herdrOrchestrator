# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb slash: 許可リストのスラッシュコマンドを agent に送る(#94)。許可リスト・宛先・状態・承認・--all・結果の確認・記録
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_appr=${TB_POLICY_A-}
export TB_DB=$TMP/slash.db TB_POLICY_A=$TMP/appr-slash.conf TB_SLASH_CONF=$TMP/slash.conf TB_SLASH_SCREEN_SEC=0
unset FAKE_DYNAMIC FAKE_STATUS FAKE_READ_TEXT FAKE_PROMPT_MODE
slsql() { sqlite3 "$TB_DB" "$@"; }
sl_ag() { printf '{"pane_id":"%s","name":"%s","agent_status":"%s","agent_session":{"value":"s-%s"},"cwd":"%s"}' "$2" "$1" "${3:-idle}" "$1" "$TMP"; }
sl_sent() { grep -c "^prompt $1	$2\$" "$FAKE_LOG"; }   # $1=宛先 $2=本文
sl_all() { grep -c "^prompt " "$FAKE_LOG"; }
cat >"$TB_SLASH_CONF" <<'CONF'
# 名前 | 固定の引数 | 方針 | 確認 | 成功の印 | 説明
compact |  | allow | busy |  | 圧縮
mcp     |  | allow | screen |  | MCP の一覧
login   | --check | allow | busy | Logged in | 固定の引数と成功の印
model   |  | deny  | busy |  | 既定で拒否
bad!    |  | allow | busy |  | 名前が不正
oddargs | a;b | allow | busy | | 引数が不正
CONF
printf '* agent:slash:mcp @worker = allow\n* send:info * = ask-any\n* send:answer * = ask-any\n' >"$TB_POLICY_A"
export FAKE_AGENTS="[$(sl_ag conductor wC:p1),$(sl_ag worker1 wW1:p1),$(sl_ag worker2 wW2:p1 working),$(sl_ag worker3 wW3:p1 done),$(sl_ag sweeper wS:p1),$(sl_ag watcher wT:p1 blocked)]"
: >"$FAKE_LOG"
$TB agents >/dev/null
slsql "UPDATE agents SET kind='codex' WHERE name='worker3';"

# ---- 許可リスト
out=$($TB slash --list)
eq "slash: --list shows listed commands with policy" "$(grep -c $'^/model\t\tdeny' <<<"$out") $(grep -c $'^/login\t--check\tallow\tbusy' <<<"$out")" "1 1"
eq "slash: --list marks invalid lines" "$(grep -c $'^/oddargs\t-\tinvalid' <<<"$out")" "1"
eq "slash: unlisted command → 1, nothing sent" "$(rc $TB slash rewind worker1 --apply) $(sl_all)" "1 0"
eq "slash: deny-by-default command → 1, nothing sent" "$($TB slash model worker1 --apply 2>&1 | grep -c 'deny'; ) $(sl_all)" "1 0"
eq "slash: invalid list line → 1" "$(rc $TB slash oddargs worker1 --apply) $(sl_all)" "1 0"
eq "slash: bad usage → 2" "$(rc $TB slash mcp) $(rc $TB slash mcp worker1 --all) $(rc $TB slash 'mc p' worker1) $(rc $TB slash mcp 'Bad Name')" "2 2 2 2"

# ---- dry-run(既定)は何も送らない
out=$($TB slash mcp worker1 worker2 worker3 watcher conductor nobody 2>&1)
eq "slash: dry-run sends nothing" "$(sl_all) $(slsql "SELECT COUNT(*) FROM agent_ops WHERE op='slash';")" "0 0"
eq "slash: dry-run idle target would-send" "$(grep -c '^worker1 would-send' <<<"$out")" "1"
eq "slash: busy targets skipped with the state" "$(grep -c '^worker2 skip(working)' <<<"$out") $(grep -c '^watcher skip(blocked)' <<<"$out")" "1 1"
eq "slash: unsupported kind reported" "$(grep -c '^worker3 not-supported(kind=codex)' <<<"$out")" "1"
eq "slash: conductor excluded, unknown name reported" "$(grep -c '^conductor skip(excluded)' <<<"$out") $(grep -c '^nobody skip(not found' <<<"$out")" "1 1"

# ---- 人の端末から --apply: idle の宛先にだけ送り、記録する
: >"$FAKE_LOG"
out=$($TB slash /compact worker1 worker2 --apply 2>&1); r=$?
eq "slash: human apply sends to the idle target only" "$r $(sl_sent worker1 /compact) $(sl_all)" "0 1 1"
eq "slash: apply prints sent / skip lines" "$(grep -c '^worker1 sent' <<<"$out") $(grep -c '^worker2 skip(working)' <<<"$out")" "1 1"
eq "slash: each send recorded in agent_ops" "$(slsql "SELECT op||' '||target||' '||status||' '||requested_by||' '||json_extract(args,'$.command') FROM agent_ops WHERE op='slash';")" "slash worker1 done human compact"
eq "slash: the message history keeps the send" "$(slsql "SELECT kind||' '||target||' '||result FROM messages WHERE ref='slash:compact:worker1';")" "slash worker1 sent"

# ---- 役割の glob と固定の引数・成功の印
: >"$FAKE_LOG"
FAKE_READ_TEXT=$'> /login --check\n  Logged in as someone' $TB slash login @worker --apply >/dev/null 2>&1
eq "slash: role glob expands to idle workers, with the fixed args" "$(sl_sent worker1 '/login --check') $(sl_all)" "1 1"
eq "slash: success pattern seen → done" "$(slsql "SELECT status FROM agent_ops WHERE op='slash' AND json_extract(args,'$.command')='login';")" "done"
: >"$FAKE_LOG"
out=$(FAKE_READ_TEXT='nothing here' $TB slash login worker1 --apply 2>&1); r=$?
eq "slash: success pattern missing → failed (11)" "$r $(grep -c '^worker1 failed(成功の印' <<<"$out")" "11 1"
out=$(FAKE_READ_TEXT=$'> /mcp\nError: unknown server' $TB slash mcp worker1 --apply 2>&1); r=$?
eq "slash: error line on screen → failed, recorded short" "$r $(slsql "SELECT status FROM agent_ops WHERE op='slash' ORDER BY id DESC LIMIT 1;")" "11 failed"
eq "slash: screen check does not press Enter" "$(grep -c '^keys ' "$FAKE_LOG")" "0"
eq "slash: recorded status line is short" "$(slsql "SELECT MAX(LENGTH(result)) <= 200 FROM agent_ops WHERE op='slash';")" "1"
eq "slash: an unknown slash command shows as failed" "$(FAKE_READ_TEXT='Unknown slash command: mcp' rc $TB slash mcp worker1 --apply)" "11"
export TB_PROMPT_VERIFY_SEC=0.1
FAKE_PROMPT_MODE=swallow-always $TB slash compact worker1 --apply >/dev/null 2>&1; r=$?
eq "slash: prompt not accepted → failed" "$r $(slsql "SELECT status FROM agent_ops WHERE op='slash' ORDER BY id DESC LIMIT 1;")" "11 failed"
export TB_PROMPT_VERIFY_SEC=0
eq "slash: herdr says working right before send → skip(busy)" "$(FAKE_STATUS=working $TB slash compact worker1 --apply 2>&1)" "worker1 skip(busy)"
eq "slash: busy right before send is recorded as not sent" "$(slsql "SELECT status||' '||(result LIKE '%使用中%') FROM agent_ops WHERE op='slash' ORDER BY id DESC LIMIT 1;")" "failed 1"

# ---- agent の pane から: approvals を見る。ask なら宛先ごとに L-<n>
: >"$FAKE_LOG"
out=$(HERDR_PANE_ID=wS:p1 $TB slash mcp worker1 sweeper --apply 2>&1); r=$?
eq "slash: agent caller allowed by agent:slash:mcp:@worker, self excluded" "$r $(sl_sent worker1 /mcp) $(grep -c '^sweeper skip(excluded)' <<<"$out")" "0 1 1"
eq "slash: agent caller recorded as requester" "$(slsql "SELECT requested_by FROM agent_ops WHERE op='slash' ORDER BY id DESC LIMIT 1;")" "sweeper"
: >"$FAKE_LOG"
out=$(HERDR_PANE_ID=wS:p1 $TB slash compact worker1 --apply 2>&1); r=$?
lid=$(sed -n 's/^worker1 pending \(L-[0-9]*\).*/\1/p' <<<"$out")
eq "slash: ask → pending L-<n>, nothing sent (20)" "$r $(sl_all) ${lid:+L}" "20 0 L"
eq "slash: agent dry-run shows would-ask" "$(HERDR_PANE_ID=wS:p1 $TB slash compact worker1 2>&1 | grep -c '^worker1 would-ask')" "1"
eq "slash: approval from an agent pane is refused" "$(HERDR_PANE_ID=wS:p1 rc $TB agent-approve "$lid")" "1"
out=$($TB agent-approve "$lid" 2>&1); r=$?
eq "slash: human approval sends it" "$r $(sl_sent worker1 /compact) $(slsql "SELECT status||' '||requested_by FROM agent_ops WHERE id=${lid#L-};")" "0 1 done sweeper"
eq "slash: approved op is not approvable twice" "$(rc $TB agent-approve "$lid")" "1"
lid=$(HERDR_PANE_ID=wS:p1 $TB slash compact worker1 --apply 2>/dev/null | sed -n 's/^worker1 pending \(L-[0-9]*\).*/\1/p')
$TB agent-reject "$lid" >/dev/null
eq "slash: rejected op sends nothing" "$(slsql "SELECT status FROM agent_ops WHERE id=${lid#L-};") $(sl_all)" "rejected 1"

# ---- --all: 誰が実行しても承認待ちにする。承認後に宛先を選び直し、宛先ごとに記録する
: >"$FAKE_LOG"
out=$($TB slash mcp --all 2>&1)
eq "slash: --all dry-run sends nothing and lists targets" "$(sl_all) $(grep -c '^worker1 would-send(after approval)' <<<"$out")" "0 1"
out=$($TB slash mcp --all --apply 2>&1); r=$?
lid=$(sed -n 's/^\(L-[0-9]*\) pending.*/\1/p' <<<"$out")
eq "slash: --all --apply from a human is held for approval (20)" "$r $(sl_all) ${lid:+L}" "20 0 L"
eq "slash: --all held op targets *" "$(slsql "SELECT target||' '||status FROM agent_ops WHERE id=${lid#L-};")" "* pending"
eq "slash: --all cannot be approved with --always" "$(rc $TB agent-approve "$lid" --always) $(sl_all)" "1 0"
before=$(slsql "SELECT COUNT(*) FROM agent_ops WHERE op='slash' AND target!='*';")
out=$($TB agent-approve "$lid" 2>&1); r=$?
eq "slash: approved --all sends to idle supported non-conductor agents" "$r $(sl_sent worker1 /mcp) $(sl_sent sweeper /mcp) $(sl_all)" "0 1 1 2"
eq "slash: approved --all skips busy / unsupported / conductor" "$(grep -c '^worker2 skip(working)' <<<"$out") $(grep -c '^worker3 not-supported' <<<"$out") $(grep -c '^conductor skip(excluded)' <<<"$out")" "1 1 1"
eq "slash: approved --all recorded per target and summarized" "$(($(slsql "SELECT COUNT(*) FROM agent_ops WHERE op='slash' AND target!='*';") - before)) $(slsql "SELECT status FROM agent_ops WHERE id=${lid#L-};")" "2 done"
lid=$(HERDR_PANE_ID=wS:p1 $TB slash mcp --all --apply 2>/dev/null | sed -n 's/^\(L-[0-9]*\) pending.*/\1/p')
eq "slash: --all from an agent is held too" "${lid:+L} $(slsql "SELECT requested_by FROM agent_ops WHERE id=${lid#L-};")" "L sweeper"

# ---- 承認待ちの間に固定の引数が変わったら、今の行で送り、承認の行の args も送ったものにする
lid=$(HERDR_PANE_ID=wS:p1 $TB slash login worker1 --apply 2>/dev/null | sed -n 's/^worker1 pending \(L-[0-9]*\).*/\1/p')
sed -i.bak 's/^login   | --check |/login   | --status |/' "$TB_SLASH_CONF"
: >"$FAKE_LOG"
FAKE_READ_TEXT='Logged in' $TB agent-approve "$lid" >/dev/null 2>&1
eq "slash: approval uses the current fixed args and records them" "$(sl_sent worker1 '/login --status') $(slsql "SELECT json_extract(args,'$.args') FROM agent_ops WHERE id=${lid#L-};")" "1 --status"

# ---- 台帳に無い個体(名前が台帳の規則に合わない)は kind を確かめられないので送らない
: >"$FAKE_LOG"
lid=$($TB slash mcp --all --apply 2>/dev/null | sed -n 's/^\(L-[0-9]*\) pending.*/\1/p')
out=$(FAKE_AGENTS="[$(sl_ag worker1 wW1:p1),$(sl_ag analyst-A1 wA:p1)]" $TB agent-approve "$lid" 2>&1)
eq "slash: agent not in the ledger is not sent" "$(grep -c '^analyst-A1 not-supported(kind unknown' <<<"$out") $(sl_sent analyst-A1 /mcp) $(sl_sent worker1 /mcp)" "1 0 1"

# ---- 承認待ちの間に許可リストから外されたら送らない
lid=$(HERDR_PANE_ID=wS:p1 $TB slash compact worker1 --apply 2>/dev/null | sed -n 's/^worker1 pending \(L-[0-9]*\).*/\1/p')
sed -i.bak 's/^compact |  | allow/compact |  | deny/' "$TB_SLASH_CONF"
: >"$FAKE_LOG"
eq "slash: list changed to deny while pending → not sent" "$(rc $TB agent-approve "$lid") $(sl_all) $(slsql "SELECT status FROM agent_ops WHERE id=${lid#L-};")" "1 0 failed"

# ---- 許可リストの読み方(コメント・同名の行は最初の行・説明の # 以降)と宛先の重複・役割の不一致
printf '  # c\ncompact | | | | | note # x | extra\ncompact | y | allow\n' >"$TMP/slash2.conf"
out=$(TB_SLASH_CONF=$TMP/slash2.conf $TB slash --apply --list)
eq "slash: --list uses the first line of a name, note cut at #" "$(grep -c $'^/compact\t\tallow\tbusy\tnote$' <<<"$out")" "2"
eq "slash: role with no agent is reported" "$(TB_SLASH_CONF=$TMP/slash2.conf $TB slash compact @nobody 2>&1)" "@nobody skip(no agent with this role)"
eq "slash: a target named twice is listed once" "$(TB_SLASH_CONF=$TMP/slash2.conf $TB slash compact worker1 worker1 2>&1)" "worker1 would-send(dry-run)"

# ---- 許可リストが無ければ何も送らない(.example も無い場所)
eq "slash: no allow-list → unlisted" "$(TB_SLASH_CONF=$TMP/none.conf rc $TB slash compact worker1 --apply) $(sl_all)" "1 0"

export TB_POLICY_A=$_saved_appr
unset FAKE_AGENTS TB_SLASH_CONF TB_SLASH_SCREEN_SEC
[[ -z $_saved_pane ]] || export HERDR_PANE_ID=$_saved_pane
