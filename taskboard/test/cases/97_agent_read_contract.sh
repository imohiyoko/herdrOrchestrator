# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb can / org / peek の使い方・文面・終了コード(期待値は消した bash 版の出力から作った。今はこれが約束)。DB は自分のものを使う
_ar_saved=$(declare -p HERDR_PANE_ID TB_DB TB_POLICY_A TB_ROLES TB_POLICY_G TB_ORG TB_DELIVERY TB_PROTECT TB_ROUTES TB_ROLE_ADMIN TB_SWEEPER_PATTERN FAKE_AGENTS FAKE_READ_RC FAKE_READ_TEXT 2>/dev/null || true)
unset HERDR_PANE_ID TB_ROUTES TB_ROLE_ADMIN TB_SWEEPER_PATTERN FAKE_READ_RC FAKE_READ_TEXT
export TB_DB=$TMP/ar.db TB_POLICY_A=$TMP/ar-appr.conf TB_ROLES=$TMP/ar-roles TB_POLICY_G=$TMP/ar-grants.conf
export TB_ORG=$TMP/ar-org.conf TB_DELIVERY=$TMP/ar-delivery.conf TB_PROTECT=$TMP/ar-protect.conf
rm -rf "$TB_ROLES" "$TB_POLICY_G" "$TB_ORG" "$TB_DELIVERY" "$TB_PROTECT"
mkdir -p "$TB_ROLES"; for r in lead worker hr; do echo "ROLE-$r" >"$TB_ROLES/$r.md"; done
printf 'boss = lead\nlead* = lead\nw* = worker\nhr1 = hr\n' >"$TB_ROLES/map.conf"
printf '* agent:read @worker = allow\n* agent:stop w2 = allow\n* send:info * = allow\n* send:answer * = ask-any\n' >"$TB_POLICY_A"
arag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(arag boss),$(arag lead1),$(arag w1),$(arag w2),$(arag hr1),$(arag sweeper1)]"
$TB agents >/dev/null 2>&1
arerr() { "$@" 2>&1 | sed "s|$TMP|<TMP>|g"; echo "rc=${PIPESTATUS[0]}"; }

# can: 使い方
eq "can: no args" "$(arerr $TB can)" $'tb: can [<主体>] <操作> <対象>(例: tb can w1 send:task lead1、主体を省くと自分: tb can agent:stop w1)\n(tb help で使い方)\nrc=2'
eq "can: 4 args" "$(arerr $TB can a b c d)" $'tb: can [<主体>] <操作> <対象>(例: tb can w1 send:task lead1、主体を省くと自分: tb can agent:stop w1)\n(tb help で使い方)\nrc=2'
eq "can: bad subject" "$(arerr $TB can Bad send:task w1)" $'tb: agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): Bad\n(tb help で使い方)\nrc=2'
eq "can: human target" "$(arerr $TB can w1 send:task human)" $'tb: agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): human\n(tb help で使い方)\nrc=2'
eq "can: bad op" "$(arerr $TB can w1 'send task' w2)" $'tb: 操作名に使えない文字があります: send task\n(tb help で使い方)\nrc=2'
eq "can: 2 args from the human terminal" "$(arerr $TB can agent:stop w1)" $'allow\nrule: 人の端末からはそのまま実行できる\nrc=0'
eq "can: 2 args from an agent pane" "$(HERDR_PANE_ID=ww1:p1 arerr $TB can agent:read w2)" $'allow\nrule: policy: * agent:read @worker = allow\nrc=0'
eq "can: 2 args, agent list fails" "$(FAKE_LIST_FAIL=1 HERDR_PANE_ID=ww1:p1 arerr $TB can agent:read w2)" $'tb: herdr agent list に失敗したため、実行者を特定できません\nrc=1'
# can: 人
eq "can: human send" "$(arerr $TB can human send:task w1)" $'deny\nrule: tb send は agent の pane から使う(人の端末には送信者がいない。人は herdr で直接話す)\nrc=4'
eq "can: human stop" "$(arerr $TB can human agent:stop w1)" $'allow\nrule: 人の端末からはそのまま実行できる\nrc=0'
printf 'boss = 最上位\nrole:hr\n' >"$TB_PROTECT"
eq "can: human stop protected" "$(arerr $TB can human agent:stop boss)" $'deny\nrule: protect: 最上位(人が設定 protect から外すまで止められない)\nrc=4'
eq "can: human stop protected by role" "$(arerr $TB can human agent:stop hr1)" $'deny\nrule: protect: protect: role:hr(人が設定 protect から外すまで止められない)\nrc=4'
eq "can: human role:change with role file" "$(arerr $TB can human role:change lead)" $'deny\nrule: 役割 lead の定義があるため、台帳のロールは効かない(tb role-set は止まる)\nrc=4'
eq "can: human other" "$(arerr $TB can human agent:read w1)" $'allow\nrule: 人の端末からはそのまま実行できる\nrc=0'
# can: 自分
eq "can: self send" "$(arerr $TB can w1 send:info w1)" $'deny\nrule: 自分自身には使えない\nrc=4'
eq "can: self role:change" "$(arerr $TB can w1 role:change w1)" $'deny\nrule: 自分自身には使えない\nrc=4'
eq "can: self read" "$(arerr $TB can w1 agent:read w1)" $'allow\nrule: 本人はそのまま読める(tb peek)\nrc=0'
# can: send
eq "can: send default block" "$(arerr $TB can w1 send:task w2)" $'deny\nrule: default: w1 は指揮官ではない(worker からの報告は tb comment、依頼はチケットを使う)\nrc=4'
printf 'lead1 -> @worker : task=now info=now\nw1 -> lead1 : info=hold answer=now task=block\n' >"$TB_DELIVERY"
eq "can: send delivery now, approvals ask" "$(arerr $TB can lead1 send:task w1)" $'ask\nrule: delivery: lead1 -> @worker : task=now\nrule: 認可: 既定 ask-any\nrc=20'
eq "can: send delivery now, approvals allow" "$(arerr $TB can lead1 send:info w1)" $'allow\nrule: delivery: lead1 -> @worker : info=now\nrule: 認可: policy: * send:info * = allow\nrc=0'
eq "can: send hold" "$(arerr $TB can w1 send:info lead1)" $'hold\nrule: delivery: w1 -> lead1 : info=hold(届けず宛先の inbox に置く)\nrule: 認可: policy: * send:info * = allow\nrc=21'
eq "can: send hold but ask" "$(printf '* send:info w2 = allow\n* send:info * = ask-any\n* send:answer * = ask-any\n' >"$TMP/ar-appr2.conf"; TB_POLICY_A=$TMP/ar-appr2.conf arerr $TB can w1 send:info lead1)" $'ask\nrule: delivery: w1 -> lead1 : info=hold(届けず宛先の inbox に置く)\nrule: 認可: policy: * send:info * = ask-any\nrc=20'
eq "can: send answer" "$(arerr $TB can w1 send:answer lead1)" $'ask\nrule: delivery: w1 -> lead1 : answer=now\nrule: 認可: policy: * send:answer * = ask-any\nrc=20'
eq "can: send block line" "$(arerr $TB can w1 send:task lead1)" $'deny\nrule: delivery: w1 -> lead1 : task=block\nrc=4'
# can: stop
eq "can: stop protected" "$(arerr $TB can lead1 agent:stop boss)" $'deny\nrule: protect: 最上位\nrc=4'
eq "can: stop allow" "$(arerr $TB can lead1 agent:stop w2)" $'allow\nrule: policy: * agent:stop w2 = allow\nrc=0'
eq "can: stop ask" "$(arerr $TB can lead1 agent:stop w1)" $'ask\nrule: 既定 ask-any\nrc=20'
# can: flag
eq "can: flag sweeper" "$(arerr $TB can sweeper1 agent:flag w1)" $'allow\nrule: 判定役(sweeper*)\nrc=0'
eq "can: flag custom sweeper" "$(TB_SWEEPER_PATTERN='lead*' arerr $TB can lead1 agent:flag w1)" $'allow\nrule: 判定役(lead*)\nrc=0'
eq "can: flag deny" "$(arerr $TB can w1 agent:flag w2)" $'deny\nrule: 判定役でなく、policy に agent:flag を許す行もない\nrc=4'
printf 'lead1 agent:flag @worker = allow\n@lead role:change ~report = allow\nlead1 approve:agent:* @worker = allow\n' >"$TB_POLICY_G"
eq "can: flag grant" "$(arerr $TB can lead1 agent:flag w1)" $'allow\nrule: policy: lead1 agent:flag @worker = allow\nrc=0'
# can: role:change
eq "can: role:change grant" "$(printf 'w1 -> lead1\n' >"$TB_ORG"; arerr $TB can lead1 role:change w1)" $'allow\nrule: policy: @lead role:change ~report = allow\nrc=0'
eq "can: role:change hr ask" "$(arerr $TB can hr1 role:change w2)" $'ask\nrule: 人事役(@hr)/ 認可: 既定 ask-any\nrc=20'
eq "can: role:change hr allow" "$(printf '@hr role:change * = allow\n' >"$TMP/ar-appr3.conf"; TB_POLICY_A=$TMP/ar-appr3.conf arerr $TB can hr1 role:change w2)" $'allow\nrule: policy: @hr role:change * = allow\nrc=0'
eq "can: role:change custom admin" "$(TB_ROLE_ADMIN=lead arerr $TB can boss role:change w2)" $'ask\nrule: 人事役(@lead)/ 認可: 既定 ask-any\nrc=20'
eq "can: role:change deny" "$(arerr $TB can w1 role:change w2)" $'deny\nrule: 人事役(@hr)でなく、policy に role:change を許す行もない\nrc=4'
eq "can: role:change role file" "$(arerr $TB can hr1 role:change worker)" $'deny\nrule: 役割 worker の定義があるため、台帳のロールは効かない(tb role-set は止まる)\nrc=4'
# can: approve
eq "can: approve self" "$(arerr $TB can w1 approve:agent:stop:w2 w1)" $'deny\nrule: 自分の依頼は承認できない\nrc=4'
eq "can: approve grant" "$(arerr $TB can lead1 approve:agent:stop:w2 w1)" $'allow\nrule: policy: lead1 approve:agent:* @worker = allow(--always・--all 系・spawn-freeze 中・依頼が ask-human のものは人だけ)\nrc=0'
eq "can: approve deny" "$(arerr $TB can w2 approve:agent:stop:w2 w1)" $'deny\nrule: policy に approve:agent:stop:w2 w1 を許す approve の行がない(承認は人だけ)\nrc=4'
# can: そのほか
eq "can: read allow" "$(arerr $TB can lead1 agent:read w1)" $'allow\nrule: policy: * agent:read @worker = allow\nrc=0'
eq "can: read ask" "$(arerr $TB can w1 agent:read lead1)" $'ask\nrule: 既定 ask-any\nrc=20'
eq "can: other op" "$(arerr $TB can w1 agent:new w2)" $'ask\nrule: 既定 ask-any\nrc=20'
rm -f "$TB_POLICY_G" "$TB_ORG" "$TB_DELIVERY"

# org
eq "org: bad arg" "$(arerr $TB org --bogus)" $'tb: org: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "org: two names" "$(arerr $TB org w1 w2)" $'tb: org: agent 名は 1 つだけ\n(tb help で使い方)\nrc=2'
eq "org: name and --me" "$(arerr $TB org w1 --me)" $'tb: org: agent 名か --me のどちらか 1 つ\n(tb help で使い方)\nrc=2'
eq "org: --me from human" "$(arerr $TB org --me)" $'tb: org: --me は agent の pane から使う(人の端末には上司がいない。tb org <agent 名> で見る)\n(tb help で使い方)\nrc=2'
eq "org: bad name" "$(arerr $TB org Bad)" $'tb: agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): Bad\n(tb help で使い方)\nrc=2'
eq "org: no setting" "$(arerr $TB org)" $'組織図は未設定です(設定 org。人が tb config set org で書く。例: setup/examples/org.conf)。誰にも上司はいません\nrc=0'
eq "org: no setting json" "$(arerr $TB org --json)" $'{"source":"none","agents":[{"name":"boss","state":"running","manager":null},{"name":"hr1","state":"running","manager":null},{"name":"lead1","state":"running","manager":null},{"name":"sweeper1","state":"running","manager":null},{"name":"w1","state":"running","manager":null},{"name":"w2","state":"running","manager":null}]}\nrc=0'
eq "org: no setting one" "$(arerr $TB org w1)" $'agent: w1\n上司: -\n上の全員: -\n直属の部下: -\n同じ上司: -\nrc=0'
eq "org: no setting one json" "$(arerr $TB org w1 --json)" $'{"name":"w1","state":"running","manager":null,"superiors":[],"reports":[],"peers":[]}\nrc=0'
cat >"$TB_ORG" <<'ORG'
w1 -> lead1
w2 -> lead1
@lead -> boss
boss -> ceo
ceo -> chair
chair -> ceo
sweeper1 -> ghost
ORG
sqlite3 "$TB_DB" "UPDATE agents SET state='stopped' WHERE name='w2';"
eq "org: tree" "$(arerr $TB org)" $'組織図(設定 org: file:<TMP>/ar-org.conf)\nghost  (台帳にいない)\n  sweeper1\nhr1\n循環していて木にできない: boss ceo chair lead1 w1 w2\nrc=0'
eq "org: json" "$(arerr $TB org --json)" $'{"source":"file:<TMP>/ar-org.conf","agents":[{"name":"boss","state":"running","manager":"ceo"},{"name":"hr1","state":"running","manager":null},{"name":"lead1","state":"running","manager":"boss"},{"name":"sweeper1","state":"running","manager":"ghost"},{"name":"w1","state":"running","manager":"lead1"},{"name":"w2","state":"stopped","manager":"lead1"},{"name":"ceo","state":"-","manager":"chair"},{"name":"ghost","state":"-","manager":null},{"name":"chair","state":"-","manager":"ceo"}]}\nrc=0'
eq "org: one" "$(arerr $TB org w1)" $'agent: w1\n上司: lead1\n上の全員: lead1 boss ceo chair\n直属の部下: -\n同じ上司: w2\nrc=0'
eq "org: one json" "$(arerr $TB org w1 --json)" $'{"name":"w1","state":"running","manager":"lead1","superiors":["lead1","boss","ceo","chair"],"reports":[],"peers":["w2"]}\nrc=0'
eq "org: manager" "$(arerr $TB org lead1)" $'agent: lead1\n上司: boss\n上の全員: boss ceo chair\n直属の部下: w1 w2\n同じ上司: -\nrc=0'
eq "org: not in ledger" "$(arerr $TB org nobody)" $'agent: nobody\n上司: -\n上の全員: -\n直属の部下: -\n同じ上司: -\nrc=0'
eq "org: not in ledger json" "$(arerr $TB org nobody --json)" $'{"name":"nobody","state":"-","manager":null,"superiors":[],"reports":[],"peers":[]}\nrc=0'
eq "org: loop" "$(arerr $TB org ceo)" $'agent: ceo\n上司: chair\n上の全員: chair\n直属の部下: boss chair\n同じ上司: -\nrc=0'
eq "org: --me" "$(HERDR_PANE_ID=ww2:p1 arerr $TB org --me --json)" $'{"name":"w2","state":"stopped","manager":"lead1","superiors":["lead1","boss","ceo","chair"],"reports":[],"peers":["w1"]}\nrc=0'
eq "org: --me agent list fails" "$(FAKE_LIST_FAIL=1 HERDR_PANE_ID=ww2:p1 arerr $TB org --me)" $'tb: herdr agent list に失敗したため、実行者を特定できません\nrc=1'
printf '@lead -> boss\n' >"$TB_ORG"
sqlite3 "$TB_DB" "UPDATE agents SET state='running' WHERE name='w2';"
eq "org: roles unknown" "$(sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role TO role_x;"; arerr $TB org; sqlite3 "$TB_DB" "ALTER TABLE agents RENAME COLUMN role_x TO role;")" $'組織図(設定 org: file:<TMP>/ar-org.conf)\nboss\nhr1\nlead1\nsweeper1\nw1\nw2\n役割を確かめられず上司が決まらない: boss hr1 lead1 sweeper1 w1 w2\nrc=0'
printf 'a -> b\n' >"$TB_ORG"
sqlite3 "$TB_DB" "DELETE FROM agents;"
eq "org: empty ledger" "$(arerr $TB org)" $'組織図(設定 org: file:<TMP>/ar-org.conf)\n(台帳に agent がいません)\nrc=0'
eq "org: empty ledger json" "$(arerr $TB org --json)" $'{"source":"file:<TMP>/ar-org.conf","agents":[]}\nrc=0'
$TB agents >/dev/null 2>&1
rm -f "$TB_ORG"

# peek
: >"$FAKE_LOG"
eq "peek: no args" "$(arerr $TB peek)" $'tb: peek <agent 名> [--lines N]\n(tb help で使い方)\nrc=2'
eq "peek: bad name" "$(arerr $TB peek Bad)" $'tb: agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): Bad\n(tb help で使い方)\nrc=2'
eq "peek: bad arg" "$(arerr $TB peek w1 --bogus)" $'tb: peek: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "peek: lines needs value" "$(arerr $TB peek w1 --lines)" $'tb: --lines に値が必要です\n(tb help で使い方)\nrc=2'
eq "peek: lines 0" "$(arerr $TB peek w1 --lines 0)" $'tb: peek: --lines は 1〜200\n(tb help で使い方)\nrc=2'
eq "peek: lines 201" "$(arerr $TB peek w1 --lines 201)" $'tb: peek: --lines は 1〜200\n(tb help で使い方)\nrc=2'
eq "peek: lines x" "$(arerr $TB peek w1 --lines 1x)" $'tb: peek: --lines は 1〜200\n(tb help で使い方)\nrc=2'
eq "peek: human" "$(arerr $TB peek w1 --lines 200)" $'screen of ww1:p1\nrc=0'
eq "peek: text trailing newlines" "$(FAKE_READ_TEXT=$'a\n\nb\n\n' arerr $TB peek w1)" $'a\n\nb\nrc=0'
eq "peek: self" "$(HERDR_PANE_ID=wlead1:p1 arerr $TB peek lead1)" $'screen of wlead1:p1\nrc=0'
eq "peek: allowed" "$(HERDR_PANE_ID=wlead1:p1 arerr $TB peek w2 --lines 3)" $'screen of ww2:p1\nrc=0'
eq "peek: denied" "$(HERDR_PANE_ID=ww1:p1 arerr $TB peek lead1)" $'tb: w1 は lead1 の中身を読めません(既定 ask-any)。必要なら人が policy に \'<主体> agent:read lead1 = allow\'(対象は @<役割> や ~child なども書ける)を足す\nrc=20'
eq "peek: agent list fails" "$(FAKE_LIST_FAIL=1 arerr $TB peek w1)" $'tb: herdr agent list に失敗しました\nrc=1'
eq "peek: agent list fails from agent" "$(FAKE_LIST_FAIL=1 HERDR_PANE_ID=ww1:p1 arerr $TB peek w2)" $'tb: herdr agent list に失敗したため、実行者を特定できません\nrc=1'
eq "peek: not running" "$(arerr $TB peek nobody)" $'tb: nobody は herdr で動いていません\nrc=1'
eq "peek: read fails" "$(FAKE_READ_RC=3 HERDR_PANE_ID=wlead1:p1 arerr $TB peek w1)" $'tb: herdr agent read に失敗しました(w1)\nrc=1'
eq "peek: herdr calls" "$(grep -c '^read ' "$FAKE_LOG")" $'5'
eq "peek: read_log" "$(sqlite3 "$TB_DB" "SELECT reader, target, lines, status, COALESCE(rule,'<null>') FROM read_log ORDER BY id;")" $'human|w1|200|ok|<null>\nhuman|w1|40|ok|<null>\nlead1|lead1|40|ok|<null>\nlead1|w2|3|ok|policy: * agent:read @worker = allow\nw1|lead1|40|denied|既定 ask-any\nhuman|w1|40|failed|<null>\nhuman|nobody|40|failed|<null>\nlead1|w1|40|failed|policy: * agent:read @worker = allow'

unset HERDR_PANE_ID TB_DB TB_POLICY_A TB_ROLES TB_POLICY_G TB_ORG TB_DELIVERY TB_PROTECT TB_ROUTES TB_ROLE_ADMIN TB_SWEEPER_PATTERN FAKE_AGENTS FAKE_READ_RC FAKE_READ_TEXT
eval "$_ar_saved"
