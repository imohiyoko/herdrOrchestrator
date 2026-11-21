# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- 組織図(設定 org): 上司の線、grants / delivery の ~report ~subordinate ~manager ~superior ~peer、tb org(全員が見る)、tb can
_saved_pane=${HERDR_PANE_ID-}; unset HERDR_PANE_ID
_saved_appr=${TB_APPROVALS-}; _saved_roles=${TB_ROLES-}
export TB_DB=$TMP/org.db TB_APPROVALS=$TMP/org-appr.conf TB_ROLES=$TMP/org-roles TB_GRANTS=$TMP/org-grants.conf
export TB_ORG=$TMP/org.conf TB_DELIVERY=$TMP/org-delivery.conf TB_PROTECT=$TMP/org-protect.conf
mkdir -p "$TB_ROLES"; for r in lead worker hr; do echo "ROLE-$r" >"$TB_ROLES/$r.md"; done
printf 'boss = lead\nlead* = lead\nw* = worker\nhr1 = hr\n' >"$TB_ROLES/map.conf"
printf '* = ask\n' >"$TB_APPROVALS"
og_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(og_ boss),$(og_ lead1),$(og_ lead2),$(og_ w1),$(og_ w2),$(og_ w3),$(og_ kid),$(og_ hr1),$(og_ loner)]"
$TB agents >/dev/null 2>&1
sqlite3 "$TB_DB" "UPDATE agents SET spawned_by='w1' WHERE name='kid';"

# 設定が無いうちは中立: 誰にも上司はいない
eq "org: without the setting nobody has a manager" "$($TB org | grep -c "^組織図は未設定です")" "1"
eq "org: without the setting relations match nothing" "$(printf '@lead agent:read ~report\n' >"$TB_GRANTS"; $TB approval agent:read:w1 --by lead1 | head -1)" "ask"

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
cat >"$TB_GRANTS" <<'G'
@lead agent:read ~report
boss  agent:stop ~subordinate
@worker send:info ~manager
@worker agent:read ~peer
w1    agent:read ~superior
G
ga() { $TB approval "$1" --by "$2" | head -1; }
eq "org grants: ~report is the direct report only" "$(ga agent:read:w1 lead1) $(ga agent:read:kid lead1) $(ga agent:read:w3 lead1)" "allow ask ask"
eq "org grants: ~subordinate reaches every level below" "$(ga agent:stop:kid boss) $(ga agent:stop:w3 boss) $(ga agent:stop:boss lead1)" "allow allow ask"
eq "org grants: ~manager / ~superior / ~peer" "$(ga send:info:lead1 w1) $(ga send:info:boss w1) $(ga agent:read:boss w1) $(ga agent:read:w2 w1) $(ga agent:read:w3 w1)" "allow ask allow allow ask"
eq "org grants: the rule names the relation" "$($TB approval agent:read:w1 --by lead1 | sed -n 2p)" "rule: grants.conf: @lead agent:read ~report"

# delivery の宛先に関係: worker は自分の上司へだけ届く、ほかの指揮系統の lead へは止める
cat >"$TB_DELIVERY" <<'D'
@worker -> ~manager : *=now
@worker -> @lead    : *=block
@lead   -> ~report  : task=now info=hold
D
eq "org delivery: to one's own manager / to another lead" "$($TB can w1 send:task lead1 | head -1) $($TB can w1 send:task lead2 | head -1)" "ask deny"
eq "org delivery: hold to a report, a non-report falls to the default block" "$($TB can lead1 send:info w1 | head -1) $(rc $TB can lead1 send:task w3)" "hold 4"
printf 'send:task:* = allow\n* = ask\n' >"$TB_APPROVALS"
eq "org can: delivery now + approvals allow = allow (exit 0)" "$(rc $TB can w1 send:task lead1) $($TB can w1 send:task lead1 | sed -n 2p)" "0 rule: delivery: @worker -> ~manager : task=now"
printf '* = ask\n' >"$TB_APPROVALS"

# tb can: stop は protect が先、role:change / agent:flag は人事役・判定役・grants、人は端末からそのまま
printf 'w3 = 受け口\n' >"$TB_PROTECT"
eq "org can: protect wins over a grant" "$($TB can boss agent:stop w3 | tr '\n' ' ') $(rc $TB can boss agent:stop w3)" "deny rule: protect: 受け口  4"
eq "org can: a grant on stop" "$($TB can boss agent:stop w1 | head -1) $($TB can lead1 agent:stop w2 | head -1)" "allow ask"
eq "org can: the human does not use tb send / an agent reads itself" "$($TB can human send:task w1 | head -1) $($TB can w1 agent:read w1 | head -1)" "deny allow"
eq "org can: the human" "$($TB can human agent:stop w1 | head -1) $($TB can human agent:stop w3 | head -1)" "allow deny"
eq "org can: role:change by the HR role needs approval, others are refused" "$($TB can hr1 role:change w1 | head -1) $($TB can w1 role:change w2 | head -1) $($TB can hr1 role:change hr1 | head -1)" "ask deny deny"
printf 'boss role:change ~subordinate\n' >>"$TB_GRANTS"
eq "org can: role:change through an org grant" "$($TB can boss role:change w2 | head -1) $($TB can boss role:change loner | head -1)" "allow allow"
eq "org can: without a subject it is the calling agent" "$(HERDR_PANE_ID=wboss:p1 $TB can agent:stop w1 | head -1) $(HERDR_PANE_ID=wlead1:p1 $TB can agent:stop w2 | head -1) $($TB can agent:stop w1 | head -1)" "allow ask allow"
eq "org can: usage" "$(rc $TB can w1)" "2"

# 役割を確かめられなければ関係は当たらない(許可側に倒さない)。delivery は hold
sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role TO role_x;"
eq "org: an unreadable role grants nothing" "$($TB approval agent:read:w1 --by lead1 2>/dev/null | head -1)" "ask"
sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role_x TO role;"

# 検査(tb config set のとき)
eq "org validate: a glob or @role as the manager is refused" "$(unset TB_ORG; printf 'w1 -> lead*\n' | rc $TB config set org 2>/dev/null)" "1"
eq "org validate: a line without -> is refused" "$(unset TB_ORG; printf 'w1 lead1\n' | rc $TB config set org 2>/dev/null)" "1"
eq "org validate: a good body is stored" "$(unset TB_ORG; printf '@worker -> lead1\nkid -> ~parent\n' | rc $TB config set org >/dev/null 2>&1; unset TB_ORG; $TB org w2 | sed -n 2p)" "上司: lead1"

# 循環は木にせず名前を出す
printf 'lead1 -> w1\nw1 -> lead1\n' >"$TB_ORG"
eq "org: a cycle is reported, not looped" "$($TB org | tail -1)" "循環していて木にできない: lead1 w1"
eq "org: ~subordinate stops on a cycle" "$($TB approval agent:stop:lead1 --by boss | head -1)" "ask"

unset TB_GRANTS TB_ORG TB_DELIVERY TB_PROTECT
export TB_APPROVALS=$_saved_appr
if [[ -n $_saved_roles ]]; then export TB_ROLES=$_saved_roles; else unset TB_ROLES; fi
unset FAKE_AGENTS; export HERDR_PANE_ID=$_saved_pane
