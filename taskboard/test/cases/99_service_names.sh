# shellcheck shell=bash
# サービスの名前(tb agent <動詞> / tb recipe <動詞>)が古い名前と同じに動くこと、同じ操作名で認可されること。
# TB_IMPL=go では、Go に移し終えた動詞だけが herdrctl に渡り、まだ bash の動詞は bash に残ること
_saved_env=$(declare -p TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS 2>/dev/null)
export TB_DB=$TMP/svcnames.db TB_POLICY=$TMP/svcnames.policy
: >"$TB_POLICY"
export FAKE_AGENTS='[{"pane_id":"wS:p1","name":"sn1","agent_status":"idle","agent_session":{"value":"s1"},"cwd":"'"$TMP"'"},{"pane_id":"wS:p2","name":"sn2","agent_status":"idle","agent_session":{"value":"s2"},"cwd":"'"$TMP"'"}]'
unset HERDR_PANE_ID
$TB agents >/dev/null

# ---- 新旧の名前で同じ出力
eq "svc: agent kinds = agent-kinds" "$($TB agent kinds)" "$($TB agent-kinds)"
eq "svc: agent ops = agent-ops" "$($TB agent ops --json)" "$($TB agent-ops --json)"
eq "svc: agent role list = role-list" "$($TB agent role list)" "$($TB role-list)"
eq "svc: agent role status = roles" "$($TB agent role status --json)" "$($TB roles --json)"
eq "svc: agent org = org" "$($TB agent org)" "$($TB org)"
eq "svc: agent can = can" "$($TB agent can sn1 agent:stop sn2; echo "rc=$?")" "$($TB can sn1 agent:stop sn2; echo "rc=$?")"
eq "svc: agent slash --list = slash --list" "$($TB agent slash --list)" "$($TB slash --list)"
eq "svc: agent compact check = compact-check" "$($TB agent compact check)" "$($TB compact-check)"
eq "svc: agent list = agents" "$($TB agent list --json)" "$($TB agents --json)"
eq "svc: agent freeze / unfreeze" "$($TB agent freeze >/dev/null; sqlite3 "$TB_DB" "SELECT value FROM tb_flags WHERE key='spawn_frozen'"; $TB agent unfreeze >/dev/null; sqlite3 "$TB_DB" "SELECT value FROM tb_flags WHERE key='spawn_frozen'")" "1
0"
eq "svc: agent flag" "$($TB agent flag sn1 --killable unknown)" "sn1 killable=unknown"
eq "svc: recipe part list = parts" "$($TB recipe part list --json)" "$($TB parts --json)"
eq "svc: recipe part check = part-check" "$($TB recipe part check agent-restart target=sn1)" "$($TB part-check agent-restart target=sn1)"
eq "svc: recipe part risk = part-risk" "$($TB recipe part risk agent-status agent-stop)" "mid"
eq "svc: recipe set / list / show / history / rm" \
   "$(printf 'desc d\nstep agent-status target=$target\n' | $TB recipe set sv1; $TB recipe list | cut -f1,2; $TB recipe show sv1 | sed -n 1p; $TB recipe history sv1 | cut -f2,3; $TB recipe rm sv1; $TB recipe-list | wc -l | tr -d ' ')" \
   "sv1 added (risk low, steps 1, op recipe:sv1:<対象>)
sv1	low
name: sv1
sv1	add
sv1 removed
0"
eq "svc: recipe queue (still bash) = recipe-queue" "$($TB recipe queue --all --json)" "$($TB recipe-queue --all --json)"

# ---- 使い方の誤り(2)と一覧
eq "svc: tb agent alone lists subcommands (2)" "$($TB agent 2>/dev/null | grep -c '^tb agent '; rc $TB agent)" "13
2"
eq "svc: tb recipe alone lists subcommands (2)" "$($TB recipe 2>/dev/null | grep -c '^tb recipe '; rc $TB recipe)" "5
2"
eq "svc: unknown verbs are usage errors" "$(rc $TB agent bogus) $(rc $TB agent role bogus) $(rc $TB agent model x) $(rc $TB agent compact x) $(rc $TB recipe bogus) $(rc $TB recipe part bogus)" "2 2 2 2 2 2"

# ---- 認可: 新しい名前も古い名前と同じ操作名(別名で逃れられない)
export HERDR_PANE_ID=wS:p1
printf 'sn1 cmd:agent:ops * = deny\nsn1 cmd:recipe:parts * = deny\n' >"$TB_POLICY"
eq "svc: policy on cmd:agent:ops stops both names" "$(rc $TB agent-ops) $(rc $TB agent ops) $($TB agent ops 2>&1 | grep -c cmd:agent:ops)" "1 1 1"
eq "svc: policy on cmd:recipe:parts stops both names" "$(rc $TB parts) $(rc $TB recipe part list) $($TB recipe part list 2>&1 | grep -c cmd:recipe:parts)" "1 1 1"
eq "svc: other verbs still pass" "$(rc $TB agent kinds)" "0"
eq "svc: human-only verbs stay human-only under the new name" "$(rc $TB agent freeze) $(rc $TB recipe set sv2 <<<'step agent-status target=$target')" "1 1"
: >"$TB_POLICY"
unset HERDR_PANE_ID

unset TB_DB TB_POLICY HERDR_PANE_ID FAKE_AGENTS
eval "$_saved_env"
