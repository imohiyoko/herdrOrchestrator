# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 役職の名前に頼らない設定: projects の * / proposal の行、grants の agent:flag / role:change、通知の届け先 lead
# project は rn / rnx だけを使う
_saved_pane=${HERDR_PANE_ID-}; _saved_db=$TB_DB; _saved_conf=$TB_CONFIG; _saved_roles=${TB_ROLES-}; _saved_appr=${TB_POLICY_A-}
_had_grants=${TB_POLICY_G+1}; _saved_grants=${TB_POLICY_G-}; _had_fake=${FAKE_AGENTS+1}; _saved_fake=${FAKE_AGENTS-}
unset HERDR_PANE_ID
export TB_DB=$TMP/rnames.db TB_CONFIG=$TMP/rnames-projects.conf TB_POLICY_G=$TMP/rnames-grants.conf TB_ROLES=$TMP/rnames-roles TB_POLICY_A=$TMP/rnames-appr.conf
mkdir -p "$TB_ROLES"; echo "ROLE-w" >"$TB_ROLES/worker.md"; : >"$TB_ROLES/map.conf"
printf '* send:info * = allow\n* send:answer * = ask-any\n' >"$TB_POLICY_A"
printf '*=old-lead\n*=lead1\nrnx=rnx-lead\nproposal=cfg1\n' >"$TB_CONFIG"
: >"$TB_POLICY_G"
rn_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(rn_ lead1),$(rn_ rnx-lead),$(rn_ cfg1),$(rn_ judge1),$(rn_ wk1),$(rn_ aud1),$(rn_ conductor),$(rn_ hr),$(rn_ old-lead)]"
$TB agents >/dev/null 2>&1
lastp() { grep '^prompt' "$FAKE_LOG" | tail -1 | cut -f1; }

# 既定の宛先: * の行。project の行が優先
t1=$($TB add -p rn -t "既定の宛先"); t2=$($TB add -p rnx -t "project の宛先")
: >"$FAKE_LOG"; HERDR_PANE_ID=wwk1:p1 $TB comment "$t1" --kind question --text q >/dev/null 2>&1
eq "projects *: an unlisted project goes to the * line, not conductor" "$(lastp)" "prompt lead1"
: >"$FAKE_LOG"; HERDR_PANE_ID=wwk1:p1 $TB comment "$t2" --kind question --text q >/dev/null 2>&1
eq "projects *: the project's own line wins over *" "$(lastp)" "prompt rnx-lead"

# 送信の既定の規則: 指揮官 = 既定の宛先と projects の値(proposal は除く)
eq "delivery default: the * lead sends" "$(HERDR_PANE_ID=wlead1:p1 rc $TB send wk1 --kind info --text hi)" "0"
eq "delivery default: conductor is no longer a lead once * is set" "$(HERDR_PANE_ID=wconductor:p1 rc $TB send wk1 --kind info --text hi)" "4"
eq "delivery default: an earlier * line is not a lead (the last * wins)" "$(HERDR_PANE_ID=wold-lead:p1 rc $TB send wk1 --kind info --text hi)" "4"
eq "delivery default: the proposal handler is not a lead" "$(HERDR_PANE_ID=wcfg1:p1 rc $TB send wk1 --kind info --text hi)" "4"

# 提案: proposal=<名前> が宛先・担当・閉じる者
: >"$FAKE_LOG"
out=$(HERDR_PANE_ID=waud1:p1 $TB proposal --role - --title t --text x)
eq "proposal=: delivered to the configured handler" "$out $(lastp)" "P-1 delivered prompt cfg1"
eq "proposal=: the ticket owner is the handler" "$(sqlite3 "$TB_DB" "SELECT owner_agent FROM tasks WHERE project='proposal';")" "cfg1"
eq "proposal-resolve: hr is not the handler any more" "$(HERDR_PANE_ID=whr:p1 rc $TB proposal-resolve P-1 --status applied)" "1"
eq "proposal-resolve: the proposer cannot close it" "$(HERDR_PANE_ID=waud1:p1 rc $TB proposal-resolve P-1 --status applied)" "1"
eq "proposal-resolve: the configured handler closes it" "$(HERDR_PANE_ID=wcfg1:p1 $TB proposal-resolve P-1 --status rejected)" "P-1 rejected"

# 判定フラグ: sweeper* でなくても grants の agent:flag で付けられる(対象で絞れる)
: >"$TB_POLICY_G"
eq "agent-flag: refused without a grant" "$(HERDR_PANE_ID=wjudge1:p1 rc $TB agent-flag wk1 --killable yes --reason x)" "1"
printf 'judge1 agent:flag wk* = allow\n' >"$TB_POLICY_G"
eq "agent-flag: an agent:flag grant allows it" "$(HERDR_PANE_ID=wjudge1:p1 $TB agent-flag wk1 --killable yes --reason x)" "wk1 killable=yes"
printf 'judge1 agent:flag @worker = allow\n' >"$TB_POLICY_G"
eq "agent-new --killable: an @role grant is matched against the requested --role" "$(HERDR_PANE_ID=wjudge1:p1 $TB agent-new fresh1 --role worker --killable yes 2>&1 | grep -c 'agent-new --killable を付けられるのは')" "0"
eq "agent-new --killable: without --role the @role grant does not match" "$(HERDR_PANE_ID=wjudge1:p1 rc $TB agent-new fresh2 --killable yes)" "1"
printf 'judge1 agent:flag wk* = allow\n' >"$TB_POLICY_G"
eq "agent-flag: the grant is limited to its target" "$(HERDR_PANE_ID=wjudge1:p1 rc $TB agent-flag aud1 --killable yes --reason x)" "1"

# 役割の変更: 人事役でなくても grants の role:change で(grants に当たれば approvals の ask を待たない)
eq "role-set: refused without a grant" "$(HERDR_PANE_ID=wjudge1:p1 rc $TB role-set wk1 worker)" "1"
printf 'judge1 role:change wk* = allow\n' >"$TB_POLICY_G"
eq "role-set: a role:change grant allows it" "$(HERDR_PANE_ID=wjudge1:p1 rc $TB role-set wk1 worker)" "0"
eq "role-set: the grant is limited to its target" "$(HERDR_PANE_ID=wjudge1:p1 rc $TB role-set aud1 worker)" "1"
printf 'judge1 role:change * = allow\n' >"$TB_POLICY_G"
eq "role-write: a role:change grant does not make a non-admin able to write role texts" "$(echo x | HERDR_PANE_ID=wjudge1:p1 rc $TB role-write worker)" "1"
eq "role-write: nor the map (no self-escalation through @map)" "$(echo 'judge1 = worker' | HERDR_PANE_ID=wjudge1:p1 rc $TB role-write @map)" "1"

# 通知の規則: 届け先 lead は conductor の別名
_saved_nr=${TB_NOTIFY_RULES-}; export TB_NOTIFY_RULES=$TMP/rnames-nrules.conf; rm -f "$TB_NOTIFY_RULES"
eq "notify-rules: lead is accepted as a sink" "$(printf 'status new=blocked -> webui lead\n' | rc $TB config set notify-rules)" "0"
if [[ -n $_saved_nr ]]; then export TB_NOTIFY_RULES=$_saved_nr; else unset TB_NOTIFY_RULES; fi

if [[ -n $_had_grants ]]; then export TB_POLICY_G=$_saved_grants; else unset TB_POLICY_G; fi
if [[ -n $_had_fake ]]; then export FAKE_AGENTS=$_saved_fake; else unset FAKE_AGENTS; fi
export TB_DB=$_saved_db TB_CONFIG=$_saved_conf
if [[ -n $_saved_roles ]]; then export TB_ROLES=$_saved_roles; else unset TB_ROLES; fi
if [[ -n $_saved_appr ]]; then export TB_POLICY_A=$_saved_appr; else unset TB_POLICY_A; fi
if [[ -n $_saved_pane ]]; then export HERDR_PANE_ID=$_saved_pane; else unset HERDR_PANE_ID; fi
