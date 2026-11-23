# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 組織図(設定 org): 上司の線、grants / delivery の ~report ~subordinate ~manager ~superior ~peer、tb org(全員が見る)、tb can
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
# 変える環境変数は、設定の有無ごと覚えて最後に戻す
_org_vars="TB_DB TB_POLICY_A TB_ROLES TB_POLICY_G TB_ORG TB_DELIVERY TB_PROTECT"
for _v in $_org_vars; do if [[ -n ${!_v+x} ]]; then printf -v "_org_saved_$_v" '%s' "${!_v}"; printf -v "_org_set_$_v" 1; else printf -v "_org_set_$_v" 0; fi; done
export TB_DB=$TMP/org.db TB_POLICY_A=$TMP/org-appr.conf TB_ROLES=$TMP/org-roles TB_POLICY_G=$TMP/org-grants.conf
export TB_ORG=$TMP/org.conf TB_DELIVERY=$TMP/org-delivery.conf TB_PROTECT=$TMP/org-protect.conf
mkdir -p "$TB_ROLES"; for r in lead worker hr; do echo "ROLE-$r" >"$TB_ROLES/$r.md"; done
printf 'boss = lead\nlead* = lead\nw* = worker\nhr1 = hr\n' >"$TB_ROLES/map.conf"
printf '* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
og_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(og_ boss),$(og_ lead1),$(og_ lead2),$(og_ w1),$(og_ w2),$(og_ w3),$(og_ kid),$(og_ hr1),$(og_ loner)]"
$TB agents >/dev/null 2>&1
sqlite3 "$TB_DB" "UPDATE agents SET spawned_by='w1' WHERE name='kid';"

# 設定が無いうちは中立: 誰にも上司はいない
eq "org: without the setting nobody has a manager" "$($TB org | grep -c "^組織図は未設定です")" "1"
eq "org: without the setting relations match nothing" "$(printf '@lead agent:read ~report = allow\n' >"$TB_POLICY_G"; $TB approval agent:read:w1 --by lead1 | head -1)" "ask"

# w1 w2 → lead1、w3 → lead2、lead* → boss、子は spawn の親へ、自分を上司にする行は飛ばす
cat >"$TB_ORG" <<'ORG'
# コメント
w1 -> lead1
w2 -> lead1
@worker -> lead2
kid -> ~parent
@lead -> boss
* -> boss
ORG
eq "org: tree (stopped / ledger state, ~parent, a boss is not its own manager)" "$($TB org)" "組織図(設定 org: file:$TB_ORG)
boss
  hr1
  lead1
    w1
      kid
    w2
  lead2
    w3
  loner"
eq "org: one agent's view" "$($TB org w1)" "agent: w1
上司: lead1
上の全員: lead1 boss
直属の部下: kid
同じ上司: w2"
eq "org: json" "$($TB org --json | jq -c '[.agents[] | select(.name=="w3" or .name=="boss")]')" '[{"name":"boss","state":"running","manager":null},{"name":"w3","state":"running","manager":"lead2"}]'
eq "org: an agent can read it too (everyone sees the chart)" "$(HERDR_PANE_ID=ww3:p1 HERDR_PANE_ID=ww3:p1 $TB org w3 | sed -n 2p)" "上司: lead2"

eq "org: --me is the calling agent (from its pane, not self-reported)" "$(HERDR_PANE_ID=wlead1:p1 $TB org --me | sed -n '1p;4p' | tr '\n' ' ')" "agent: lead1 直属の部下: w1 w2 "
eq "org: --me from the human terminal is a usage error" "$(rc $TB org --me)" "2"
eq "org: --me json" "$(HERDR_PANE_ID=ww2:p1 $TB org --me --json | jq -c '[.manager, .peers]')" '["lead1",["w1"]]'

# grants の関係
cat >"$TB_POLICY_G" <<'G'
@lead agent:read ~report = allow
boss agent:stop ~subordinate = allow
@worker send:task ~manager = allow
@worker agent:read ~peer = allow
w1 agent:read ~superior = allow
G
ga() { $TB approval "$1" --by "$2" | head -1; }
eq "org grants: ~report is the direct report only" "$(ga agent:read:w1 lead1) $(ga agent:read:kid lead1) $(ga agent:read:w3 lead1)" "allow ask ask"
eq "org grants: ~subordinate reaches every level below" "$(ga agent:stop:kid boss) $(ga agent:stop:w3 boss) $(ga agent:stop:boss lead1)" "allow allow ask"
eq "org grants: ~manager / ~superior / ~peer" "$(ga send:task:lead1 w1) $(ga send:task:boss w1) $(ga agent:read:boss w1) $(ga agent:read:w2 w1) $(ga agent:read:w3 w1)" "allow ask allow allow ask"
eq "org grants: the rule names the relation" "$($TB approval agent:read:w1 --by lead1 | sed -n 2p)" "rule: policy: @lead agent:read ~report = allow"

# delivery の宛先に関係: worker は自分の上司へだけ届く、ほかの指揮系統の lead へは止める
cat >"$TB_DELIVERY" <<'D'
@worker -> ~manager : *=now
@worker -> @lead    : *=block
@lead   -> ~report  : task=now info=hold
D
eq "org delivery: to one's own manager (granted) / to another lead" "$($TB can w1 send:task lead1 | head -1) $($TB can w1 send:task lead2 | head -1)" "allow deny"
eq "org delivery: hold to a report (approvals still ask), a non-report falls to the default block" "$($TB can lead1 send:info w1 | head -1) $(rc $TB can lead1 send:task w3)" "ask 4"
printf '* send:info w1 = allow\n* send:answer * = ask\n' >"$TB_POLICY_A"   # 厳しい方が勝つので、info の締める行は置かない
eq "org can: hold with policy allow is hold (exit 21)" "$($TB can lead1 send:info w1 | head -1) $(rc $TB can lead1 send:info w1)" "hold 21"
printf '* send:info w1 = ask\n* send:task * = allow\n* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
eq "org can: hold with approvals ask is ask" "$($TB can lead1 send:info w1 | head -1) $(rc $TB can lead1 send:info w1)" "ask 20"
printf '* send:task * = allow\n* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
eq "org can: delivery now + approvals allow = allow (exit 0)" "$(rc $TB can w1 send:task lead1) $($TB can w1 send:task lead1 | sed -n 2p)" "0 rule: delivery: @worker -> ~manager : task=now"
printf '* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"

# tb can: stop は protect が先、role:change / agent:flag は人事役・判定役・grants、人は端末からそのまま
printf 'w3 = 受け口\n' >"$TB_PROTECT"
eq "org can: protect wins over a grant" "$($TB can boss agent:stop w3 | tr '\n' ' ') $(rc $TB can boss agent:stop w3)" "deny rule: protect: 受け口  4"
eq "org can: a grant on stop" "$($TB can boss agent:stop w1 | head -1) $($TB can lead1 agent:stop w2 | head -1)" "allow ask"
eq "org can: the human does not use tb send / an agent reads itself" "$($TB can human send:task w1 | head -1) $($TB can w1 agent:read w1 | head -1)" "deny allow"
eq "org can: the human" "$($TB can human agent:stop w1 | head -1) $($TB can human agent:stop w3 | head -1)" "allow deny"
eq "org can: role:change without a policy row needs approval for anyone; self is refused" "$($TB can hr1 role:change w1 | head -1) $($TB can w1 role:change w2 | head -1) $($TB can hr1 role:change hr1 | head -1)" "ask ask deny"
printf 'boss role:change ~subordinate = allow\n' >>"$TB_POLICY_G"
eq "org can: role:change through an org grant" "$($TB can boss role:change w2 | head -1) $($TB can boss role:change loner | head -1)" "allow allow"
eq "org can: without a subject it is the calling agent" "$(HERDR_PANE_ID=wboss:p1 $TB can agent:stop w1 | head -1) $(HERDR_PANE_ID=wlead1:p1 $TB can agent:stop w2 | head -1) $($TB can agent:stop w1 | head -1)" "allow ask allow"
eq "org can: role:change on an agent with its own role file is refused even with a grant" "$(echo X >"$TB_ROLES/w2.md"; $TB can boss role:change w2 | head -1) $($TB can human role:change w2 | head -1)" "deny deny"
rm -f "$TB_ROLES/w2.md"
eq "org can: usage" "$(rc $TB can w1)" "2"

printf '* send:* * = allow\n' >"$TMP/org-allow.conf"
# 役割を確かめられなければ関係は当たらない(許可側に倒さない)。delivery は hold
sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role TO role_x;"
eq "org: an unreadable role grants nothing" "$($TB approval agent:read:w1 --by lead1 2>/dev/null | head -1)" "ask"
sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role_x TO role;"
eq "org delivery: an unreadable parent holds instead of skipping a block line" "$(printf '* -> ~manager : *=block\n* -> * : *=now\n' >"$TB_DELIVERY"; printf 'kid -> ~parent\n' >"$TB_ORG"; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN spawned_by TO sb_x;"; TB_POLICY_A=$TMP/org-allow.conf $TB can kid send:task w1 2>/dev/null | head -1; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN sb_x TO spawned_by;")" "hold"
eq "spawn relation: an unreadable parent holds a delivery line too" "$(printf '* -> ~parent : *=block\n* -> * : *=now\n' >"$TB_DELIVERY"; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN spawned_by TO sb_x;"; TB_POLICY_A=$TMP/org-allow.conf $TB can kid send:task w1 2>/dev/null | head -1; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN sb_x TO spawned_by;")" "hold"

# 検査(tb config set のとき)
eq "org validate: a glob or @role as the manager is refused" "$(unset TB_ORG; printf 'w1 -> lead*\n' | rc $TB config set org 2>/dev/null)" "1"
eq "org validate: a line without -> is refused" "$(unset TB_ORG; printf 'w1 lead1\n' | rc $TB config set org 2>/dev/null)" "1"
eq "org validate: a good body is stored" "$(unset TB_ORG; printf '@worker -> lead1\nkid -> ~parent\n' | rc $TB config set org >/dev/null 2>&1; unset TB_ORG; $TB org w2 | sed -n 2p)" "上司: lead1"

# 循環は木にせず名前を出す
printf 'lead1 -> w1\nw1 -> lead1\n' >"$TB_ORG"
eq "org: a cycle is reported, not looped" "$($TB org | tail -1)" "循環していて木にできない: lead1 w1"
eq "org: ~subordinate stops on a cycle" "$($TB approval agent:stop:lead1 --by boss | head -1)" "ask"

for _v in $_org_vars; do _s="_org_set_$_v"; _o="_org_saved_$_v"; if [[ ${!_s} == 1 ]]; then export "$_v=${!_o}"; else unset "$_v"; fi; done
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
