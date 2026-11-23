# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb role-* / roles の使い方・文面・終了コード(bash と Go で同じ。期待値は bash の出力から作った)。DB は自分のものを使う
_rc_saved=$(declare -p HERDR_PANE_ID HERDR_SESSION TB_DB TB_ROLES TB_POLICY_A TB_POLICY_G TB_ROLE_ADMIN TB_ROLE_MAX FAKE_AGENTS FAKE_STATUS FAKE_PROMPT_RC VISUAL EDITOR 2>/dev/null || true)
unset HERDR_PANE_ID HERDR_SESSION TB_ROLE_ADMIN TB_ROLE_MAX FAKE_STATUS FAKE_PROMPT_RC VISUAL
export TB_DB=$TMP/rc.db TB_ROLES=$TMP/rc-roles TB_POLICY_A=$TMP/rc-appr.conf TB_POLICY_G=$TMP/rc-grants.conf
rm -rf "$TB_ROLES" "$TB_POLICY_G"; mkdir -p "$TB_ROLES"
printf '共通の心得\n' >"$TB_ROLES/_common.md"; printf 'worker の役割\n\n' >"$TB_ROLES/worker.md"; printf 'hr の役割\n' >"$TB_ROLES/hr.md"
printf 'lead の役割\n' >"$TB_ROLES/lead.md"; printf 'x\n' >"$TB_ROLES/README.md"; printf 'x\n' >"$TB_ROLES/_private.md"; printf '   \n' >"$TB_ROLES/empty.md"
printf 'w* = worker\nhr* = hr\nlead1 = lead, worker # 2 つ\nboss = nothing\n' >"$TB_ROLES/map.conf"
printf '* role:change * = allow\n* send:info * = ask-any\n* send:answer * = ask-any\n' >"$TB_POLICY_A"
rcag() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"idle","agent_session":{"value":"s-%s"}}' "$1" "$1" "$1"; }
export FAKE_AGENTS="[$(rcag w1),$(rcag w2),$(rcag hr1),$(rcag lead1),$(rcag boss),{\"pane_id\":\"wX:p1\",\"agent_session\":{\"value\":\"s-x\"}}]"
$TB agents >/dev/null 2>&1
rcerr() { "$@" 2>&1 | sed -e "s|$TMP|<TMP>|g" -e 's/版 [0-9a-f]\{12\}/版 <H>/g' -e 's/([0-9a-f]\{12\})/(<H>)/g' -e 's/渡し済み [0-9a-f]\{12\}/渡し済み <H>/g'; echo "rc=${PIPESTATUS[0]}"; }
rcctx() { "$@" | jq -r '.hookSpecificOutput | .hookEventName, .additionalContext' | sed 's/版 [0-9a-f]\{12\}/版 <H>/'; echo "rc=${PIPESTATUS[0]}"; }

# role-list / role-show
eq "role-list: bad arg" "$(rcerr $TB role-list x)" $'tb: role-list\n(tb help で使い方)\nrc=2'
eq "role-list" "$(rcerr $TB role-list)" $'_common\tfile:<TMP>/rc-roles/_common.md\n@map\tfile:<TMP>/rc-roles/map.conf\nempty\tfile:<TMP>/rc-roles/empty.md\nhr\tfile:<TMP>/rc-roles/hr.md\nlead\tfile:<TMP>/rc-roles/lead.md\nworker\tfile:<TMP>/rc-roles/worker.md\nrc=0'
eq "role-show: too many" "$(rcerr $TB role-show a b)" $'tb: role-show [<agent 名>]\n(tb help で使い方)\nrc=2'
eq "role-show: no name, no pane" "$(rcerr $TB role-show)" $'tb: role-show <agent 名>(agent の pane では省略できる)\n(tb help で使い方)\nrc=2'
eq "role-show: no name, unknown pane" "$(HERDR_PANE_ID=wnone:p1 rcerr $TB role-show)" $'tb: この pane の agent が分かりません\nrc=1'
eq "role-show: no name, list fails" "$(FAKE_LIST_FAIL=1 HERDR_PANE_ID=ww1:p1 rcerr $TB role-show)" $'rc=1'
eq "role-show: pane" "$(HERDR_PANE_ID=ww1:p1 rcerr $TB role-show)" $'# あなたの役割(agent 名: w1。tb が自動で渡しています。版 <H>)\n\n共通の心得\n\nworker の役割\n\n\nrc=0'
eq "role-show: two roles from map" "$(rcerr $TB role-show lead1)" $'# あなたの役割(agent 名: lead1。tb が自動で渡しています。版 <H>)\n\n共通の心得\n\nlead の役割\n\nworker の役割\n\n\nrc=0'
eq "role-show: only common" "$(rcerr $TB role-show boss)" $'# あなたの役割(agent 名: boss。tb が自動で渡しています。版 <H>)\n\n共通の心得\n\n\nrc=0'
eq "role-show: name is a role" "$(rcerr $TB role-show hr)" $'# あなたの役割(agent 名: hr。tb が自動で渡しています。版 <H>)\n\n共通の心得\n\nhr の役割\n\n\nrc=0'
eq "role-show: none" "$(mv "$TB_ROLES/_common.md" "$TMP/rc-common"; rcerr $TB role-show boss; rcerr $TB role-show empty; mv "$TMP/rc-common" "$TB_ROLES/_common.md")" $'tb: boss に渡す役割がありません\nrc=1\ntb: empty に渡す役割がありません\nrc=1'
eq "role-show: truncated" "$(printf 'abcdefghijklmnopqrstuvwxyz\n' >"$TB_ROLES/ascii.md"; mv "$TB_ROLES/_common.md" "$TMP/rc-common"; TB_ROLE_MAX=10 rcerr $TB role-show ascii; TB_ROLE_MAX=28 rcerr $TB role-show ascii; mv "$TMP/rc-common" "$TB_ROLES/_common.md"; rm -f "$TB_ROLES/ascii.md")" $'# あなたの役割(agent 名: ascii。tb が自動で渡しています。版 <H>)\n\nabcdefghij\n\n(長すぎるため途中で切りました。全文は tb role-show で読んでください)\nrc=0\n# あなたの役割(agent 名: ascii。tb が自動で渡しています。版 <H>)\n\nabcdefghijklmnopqrstuvwxyz\n\n\nrc=0'

# role-context
eq "role-context: no pane" "$(rcerr $TB role-context --source startup --session-id s1)" $'rc=0'
eq "role-context: bad source" "$(HERDR_PANE_ID=ww1:p1 rcerr $TB role-context --source nope --session-id s1)" $'rc=0'
eq "role-context: bad session id" "$(HERDR_PANE_ID=ww1:p1 rcerr $TB role-context --source startup --session-id 'a b')" $'rc=0'
eq "role-context: missing value" "$(HERDR_PANE_ID=ww1:p1 rcerr $TB role-context --source)" $'rc=0'
eq "role-context: unknown arg" "$(HERDR_PANE_ID=ww1:p1 rcerr $TB role-context --bogus x --source startup --session-id s1)" $'rc=0'
eq "role-context: unknown pane" "$(HERDR_PANE_ID=wnone:p1 rcerr $TB role-context --source startup --session-id s1)" $'rc=0'
eq "role-context: startup" "$(HERDR_PANE_ID=ww1:p1 rcctx $TB role-context --source startup --session-id s-w1)" $'SessionStart\n# あなたの役割(agent 名: w1。tb が自動で渡しています。版 <H>)\n\n共通の心得\n\nworker の役割\n\n\nrc=0'
eq "role-context: resume same version is skipped" "$(HERDR_PANE_ID=ww1:p1 rcerr $TB role-context --source resume --session-id s-w1)" $'rc=0'
eq "role-context: compact is passed again" "$(HERDR_PANE_ID=ww1:p1 rcctx $TB role-context --source compact --session-id s-w1 | tail -1)" $'rc=0'
eq "role-context: no role" "$(mv "$TB_ROLES/_common.md" "$TMP/rc-common"; HERDR_PANE_ID=wboss:p1 rcerr $TB role-context --source startup --session-id s-boss; mv "$TMP/rc-common" "$TB_ROLES/_common.md")" $'rc=0'
eq "role-context: briefings" "$(sqlite3 "$TB_DB" "SELECT session_id, agent, COALESCE(role_file,'<null>'), length(hash), source, via FROM role_briefings ORDER BY id;")" $'s-w1|w1|worker.md|64|startup|hook\ns-w1|w1|worker.md|64|compact|hook'

# roles / role-broadcast
eq "roles" "$(rcerr $TB roles | sed 's/\t[0-9a-f]\{12\}\t/\t<H>\t/; s/\t20[0-9T:-]*Z$/\t<T>/')" $'w1\tworker.md\t<H>\tcurrent\t<T>\nw2\tworker.md\t<H>\tnever\t-\nhr1\thr.md\t<H>\tnever\t-\nlead1\tlead.md,worker.md\t<H>\tnever\t-\nboss\t\t<H>\tnever\t-\nrc=0'
eq "roles --json" "$(rcerr $TB roles --json | sed -E 's/"version":"[0-9a-f]{12}"/"version":"<H>"/; s/"briefed_at":"[^"]*"/"briefed_at":"<T>"/')" $'{"name":"w1","pane":"ww1:p1","role_file":"worker.md","version":"<H>","state":"current","briefed_at":"<T>"}\n{"name":"w2","pane":"ww2:p1","role_file":"worker.md","version":"<H>","state":"never","briefed_at":null}\n{"name":"hr1","pane":"whr1:p1","role_file":"hr.md","version":"<H>","state":"never","briefed_at":null}\n{"name":"lead1","pane":"wlead1:p1","role_file":"lead.md,worker.md","version":"<H>","state":"never","briefed_at":null}\n{"name":"boss","pane":"wboss:p1","role_file":"","version":"<H>","state":"never","briefed_at":null}\nrc=0'
eq "roles: list fails" "$(FAKE_LIST_FAIL=1 rcerr $TB roles)" $'tb: herdr agent list に失敗しました\nrc=1'
eq "role-broadcast: list fails" "$(FAKE_LIST_FAIL=1 rcerr $TB role-broadcast)" $'tb: herdr agent list に失敗しました\nrc=1'
: >"$FAKE_LOG"
eq "role-broadcast: only some" "$(rcerr $TB role-broadcast w2 boss nobody)" $'sent w2 (<H>)\nsent boss (<H>)\nrc=0'
eq "role-broadcast: message" "$(grep '^prompt ' "$FAKE_LOG" | sed 's/版 [0-9a-f]\{12\}/版 <H>/')" $'prompt w2\t[role-update / tb] あなた(w2)の役割が更新されました(版 <H>、worker.md)。tb role-show で読み直し、以後はこれに従ってください\nprompt boss\t[role-update / tb] あなた(boss)の役割が更新されました(版 <H>、)。tb role-show で読み直し、以後はこれに従ってください'
eq "role-broadcast: all" "$(rcerr $TB role-broadcast)" $'skip w1 (渡し済み <H>)\nskip w2 (渡し済み <H>)\nsent hr1 (<H>)\nsent lead1 (<H>)\nskip boss (渡し済み <H>)\nrc=0'
eq "role-broadcast: busy" "$(printf 'w の新版\n' >"$TB_ROLES/worker.md"; FAKE_STATUS=working rcerr $TB role-broadcast w1 w2)" $'pending w1 (宛先が使用中か送信失敗。あとで再実行)\npending w2 (宛先が使用中か送信失敗。あとで再実行)\nrc=10'
eq "role-broadcast: failed" "$(FAKE_PROMPT_RC=3 rcerr $TB role-broadcast w1)" $'pending w1 (宛先が使用中か送信失敗。あとで再実行)\nrc=11'
eq "role-broadcast: missing" "$(FAKE_STATUS=missing rcerr $TB role-broadcast w1)" $'pending w1 (宛先が使用中か送信失敗。あとで再実行)\nrc=11'
eq "role-broadcast: messages log" "$(sqlite3 "$TB_DB" "SELECT kind, ref, target, result FROM messages WHERE kind='role' ORDER BY id;")" $'role|role:w2|w2|sent\nrole|role:boss|boss|sent\nrole|role:hr1|hr1|sent\nrole|role:lead1|lead1|sent\nrole|role:w1|w1|busy\nrole|role:w2|w2|busy\nrole|role:w1|w1|failed\nrole|role:w1|w1|failed'

# role-write / role-edit / role-unset
eq "role-write: no name" "$(printf 'x' | rcerr $TB role-write)" $'tb: role-write|role-edit <役割名|@map> [--shared]\n(tb help で使い方)\nrc=2'
eq "role-write: two names" "$(printf 'x' | rcerr $TB role-write a b)" $'tb: 役割名は 1 つ\n(tb help で使い方)\nrc=2'
eq "role-write: bad flag" "$(printf 'x' | rcerr $TB role-write a --nope)" $'tb: 不明な引数: --nope\n(tb help で使い方)\nrc=2'
eq "role-write: bad name" "$(printf 'x' | rcerr $TB role-write README; printf 'x' | rcerr $TB role-write '-x'; printf 'x' | rcerr $TB role-write 'a b')" $'tb: 役割名は英数字・_ で始まり、英数字と . _ - の 64 字以内(または @map): README\n(tb help で使い方)\nrc=2\ntb: 不明な引数: -x\n(tb help で使い方)\nrc=2\ntb: 役割名は英数字・_ で始まり、英数字と . _ - の 64 字以内(または @map): a b\n(tb help で使い方)\nrc=2'
eq "role-write: empty" "$(printf '  \n' | rcerr $TB role-write worker)" $'tb: 本文が空です(消すときは人が tb role-unset worker で行を消す)\nrc=1'
eq "role-write: ok" "$(printf 'worker(DB)\n\n\n' | rcerr $TB role-write worker)" $'worker: db:default。動いている agent に配るには: tb role-broadcast\nrc=0'
eq "role-write: shared" "$(printf 'analyst(共通)\n' | rcerr $TB role-write analyst --shared)" $'analyst: db:*。動いている agent に配るには: tb role-broadcast\nrc=0'
eq "role-write: map" "$(printf 'w* = worker\nhr* = hr\nlead1 = lead, worker\na* = analyst\n' | rcerr $TB role-write @map)" $'@map: db:default。動いている agent に配るには: tb role-broadcast\nrc=0'
eq "role-write: stored" "$(sqlite3 "$TB_DB" "SELECT herdr_session, key, body, updated_by FROM config WHERE key LIKE 'role%' ORDER BY key;"; sqlite3 "$TB_DB" "SELECT key, changed_by, how FROM config_changes WHERE key LIKE 'role%' ORDER BY id;")" $'default|role-map|w* = worker\nhr* = hr\nlead1 = lead, worker\na* = analyst|human\n*|role:analyst|analyst(共通)|human\ndefault|role:worker|worker(DB)|human\nrole:worker|human|role-write\nrole:analyst|human|role-write\nrole-map|human|role-write'
eq "role-list: after writes" "$(rcerr $TB role-list)" $'_common\tfile:<TMP>/rc-roles/_common.md\n@map\tdb:default\nanalyst\tdb:*\nempty\tfile:<TMP>/rc-roles/empty.md\nhr\tfile:<TMP>/rc-roles/hr.md\nlead\tfile:<TMP>/rc-roles/lead.md\nworker\tdb:default\nrc=0'
eq "role-write: non-admin agent" "$(printf 'x\n' | HERDR_PANE_ID=ww1:p1 rcerr $TB role-write worker)" $'tb: ロールを変えられるのは人と人事役(@hr)だけです(w1)\nrc=1'
eq "role-write: admin writes" "$(printf 'worker(hr)\n' | HERDR_PANE_ID=whr1:p1 rcerr $TB role-write worker --shared)" $'worker: db:default。動いている agent に配るには: tb role-broadcast\nrc=0'
eq "role-write: admin role" "$(printf 'x\n' | HERDR_PANE_ID=whr1:p1 rcerr $TB role-write hr)" $'tb: 人事役の役割(hr)を書き換えられるのは人だけです\nrc=1'
eq "role-write: admin map" "$(printf 'z* = worker, hr # c\n' | HERDR_PANE_ID=whr1:p1 rcerr $TB role-write @map; printf 'z* = hr=x\n' | HERDR_PANE_ID=whr1:p1 rcerr $TB role-write @map --shared)" $'tb: 人事役(hr)を付ける対応を書けるのは人だけです\nrc=1\n@map: db:default。動いている agent に配るには: tb role-broadcast\nrc=0'
eq "role-write: custom admin" "$(printf 'x\n' | TB_ROLE_ADMIN=lead HERDR_PANE_ID=whr1:p1 rcerr $TB role-write worker)" $'tb: ロールを変えられるのは人と人事役(@lead)だけです(hr1)\nrc=1'
eq "role-write: ask" "$(printf '* = ask\n' >"$TMP/rc-ask.conf"; printf 'x\n' | TB_POLICY_A=$TMP/rc-ask.conf HERDR_PANE_ID=whr1:p1 rcerr $TB role-write worker --shared)" $'tb: [role.change.needs-approval] role:change は人の承認が要ります(* = ask)。人が自分の端末で実行してください: tb role-write worker --shared < 本文\nrc=20'
eq "role-write: grant" "$(printf 'hr1 role:change worker = allow\n' >"$TB_POLICY_G"; printf 'worker(grant)\n' | TB_POLICY_A=$TMP/rc-ask.conf HERDR_PANE_ID=whr1:p1 rcerr $TB role-write worker; rm -f "$TB_POLICY_G")" $'worker: db:default。動いている agent に配るには: tb role-broadcast\nrc=0'
eq "role-write: admin empty" "$(printf '\n' | HERDR_PANE_ID=whr1:p1 rcerr $TB role-write worker)" $'tb: 本文が空です(消すときは人が tb role-unset worker で行を消す)\nrc=1'
eq "role-write: list fails" "$(printf 'x\n' | FAKE_LIST_FAIL=1 HERDR_PANE_ID=whr1:p1 rcerr $TB role-write worker)" $'tb: herdr agent list に失敗したため、実行者を特定できません\nrc=1'
printf '#!/bin/sh\nprintf "edited\\n" >>"$1"\n' >"$TMP/rc-ed.sh"; chmod +x "$TMP/rc-ed.sh"
printf '#!/bin/sh\nexit 1\n' >"$TMP/rc-ed-fail.sh"; chmod +x "$TMP/rc-ed-fail.sh"
eq "role-edit: existing" "$(EDITOR=$TMP/rc-ed.sh rcerr $TB role-edit lead; $TB role-show lead1 | grep -c edited)" $'lead: db:default。動いている agent に配るには: tb role-broadcast\nrc=0\n1'
eq "role-edit: new" "$(EDITOR=$TMP/rc-ed.sh rcerr $TB role-edit newrole --shared; sqlite3 "$TB_DB" "SELECT body FROM config WHERE key='role:newrole';")" $'newrole: db:*。動いている agent に配るには: tb role-broadcast\nrc=0\nedited'
eq "role-edit: editor fails" "$(EDITOR=$TMP/rc-ed-fail.sh rcerr $TB role-edit lead)" $'tb: 編集を中止しました\nrc=1'
eq "role-edit: usage" "$(rcerr $TB role-edit)" $'tb: role-write|role-edit <役割名|@map> [--shared]\n(tb help で使い方)\nrc=2'
eq "role-unset: usage" "$(rcerr $TB role-unset)" $'tb: role-write|role-edit <役割名|@map> [--shared]\n(tb help で使い方)\nrc=2'
eq "role-unset: agent" "$(HERDR_PANE_ID=whr1:p1 rcerr $TB role-unset worker)" $'tb: tb role-unset は人が自分の端末で実行します(agent \'hr1\' の pane からは実行できません)\nrc=1'
eq "role-unset: back to the shared row" "$(rcerr $TB role-unset worker)" $'worker: db:*\nrc=0'
eq "role-unset: back to the file" "$(rcerr $TB role-unset worker --shared)" $'worker: file:<TMP>/rc-roles/worker.md\nrc=0'
eq "role-unset: none" "$(rcerr $TB role-unset newrole --shared; rcerr $TB role-unset newrole)" $'newrole: なし\nrc=0\nnewrole: なし\nrc=0'
eq "role-unset: history" "$(sqlite3 "$TB_DB" "SELECT key, COALESCE(body,'<null>'), changed_by, how FROM config_changes WHERE how='unset' ORDER BY id;")" $'role:worker|<null>|human|unset\nrole:worker|<null>|human|unset\nrole:newrole|<null>|human|unset'

# role-set
eq "role-set: usage" "$(rcerr $TB role-set w1)" $'tb: role-set <agent 名> <ロール[,ロール...]> [--reason R]\n(tb help で使い方)\nrc=2'
eq "role-set: bad arg" "$(rcerr $TB role-set w1 worker --nope)" $'tb: role-set: 不明な引数 --nope\n(tb help で使い方)\nrc=2'
eq "role-set: reason needs value" "$(rcerr $TB role-set w1 worker --reason)" $'tb: --reason に値が必要です\n(tb help で使い方)\nrc=2'
eq "role-set: bad name" "$(rcerr $TB role-set W1 worker)" $'tb: agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内: W1\n(tb help で使い方)\nrc=2'
eq "role-set: empty role" "$(rcerr $TB role-set w1 '')" $'tb: ロールはカンマ区切りで 1〜4 個: \n(tb help で使い方)\nrc=2'
eq "role-set: five roles" "$(rcerr $TB role-set w1 a,b,c,d,e)" $'tb: ロールはカンマ区切りで 1〜4 個: a,b,c,d,e\n(tb help で使い方)\nrc=2'
eq "role-set: unknown role" "$(rcerr $TB role-set w1 worker,ghost)" $'tb: ロールは定義のある役割名(tb role-list。カンマ区切りで複数可): ghost\n(tb help で使い方)\nrc=2'
eq "role-set: empty item" "$(rcerr $TB role-set w1 worker,,lead)" $'tb: ロールは定義のある役割名(tb role-list。カンマ区切りで複数可): \n(tb help で使い方)\nrc=2'
eq "role-set: trailing comma" "$(rcerr $TB role-set w2 lead, --reason $'まとめて\n変える')" $'w2 role worker -> lead (by human)。本人に配るには: tb role-broadcast w2\nrc=0'
eq "role-set: spaced item" "$(rcerr $TB role-set w1 'worker, lead')" $'tb: ロールは定義のある役割名(tb role-list。カンマ区切りで複数可):  lead\n(tb help で使い方)\nrc=2'
eq "role-set: README" "$(rcerr $TB role-set w1 README)" $'tb: ロールは定義のある役割名(tb role-list。カンマ区切りで複数可): README\n(tb help で使い方)\nrc=2'
eq "role-set: not in ledger" "$(rcerr $TB role-set ghost worker)" $'tb: 台帳に無い agent です: ghost(tb agents で確かめる)\nrc=1'
eq "role-set: name has a role" "$(printf 'w9 の役割\n' | $TB role-write w9 >/dev/null; sqlite3 "$TB_DB" "INSERT INTO agents (name, herdr_session, state, created_by, created_at, updated_at) VALUES ('w9', (SELECT herdr_session FROM agents LIMIT 1), 'stopped', 'human', 'x', 'x');"; rcerr $TB role-set w9 worker)" $'tb: 役割 w9(db:default)があるため、台帳のロールは効きません(その役割を人事役の手順で直す)\nrc=1'
eq "role-set: other session" "$(sqlite3 "$TB_DB" "INSERT INTO agents (name, herdr_session, state, created_by, created_at, updated_at) VALUES ('far', 'other-sess', 'running', 'human', 'x', 'x');"; rcerr $TB role-set far worker)" $'tb: far は herdr セッション \'other-sess\' の agent です。HERDR_SESSION=other-sess を付けて実行してください(ロールはセッション固有)\nrc=1'
eq "role-set: human" "$(rcerr $TB role-set w1 lead,worker --reason 'リード兼務')" $'w1 role worker -> lead,worker (by human)。本人に配るには: tb role-broadcast w1\nrc=0'
eq "role-set: non-admin agent" "$(HERDR_PANE_ID=ww2:p1 rcerr $TB role-set w1 worker)" $'tb: ロールを変えられるのは人、人事役(@hr)、grants で role:change を許された agent だけです(w2)\nrc=1'
eq "role-set: self" "$(HERDR_PANE_ID=whr1:p1 rcerr $TB role-set hr1 worker)" $'tb: 自分のロールは変えられません\nrc=1'
eq "role-set: give admin" "$(HERDR_PANE_ID=whr1:p1 rcerr $TB role-set w1 worker,hr)" $'tb: 人事役のロール(hr)を付けられるのは人だけです\nrc=1'
eq "role-set: admin ok" "$(HERDR_PANE_ID=whr1:p1 rcerr $TB role-set w1 worker)" $'w1 role lead,worker -> worker (by hr1)。本人に配るには: tb role-broadcast w1\nrc=0'
eq "role-set: admin ask" "$(TB_POLICY_A=$TMP/rc-ask.conf HERDR_PANE_ID=whr1:p1 rcerr $TB role-set w1 lead)" $'tb: [role.change.needs-approval] role:change は人の承認が要ります(* = ask)。人が自分の端末で実行してください: tb role-set w1 lead\nrc=20'
eq "role-set: grant only" "$(printf 'w2 role:change w1 = allow\n' >"$TB_POLICY_G"; TB_POLICY_A=$TMP/rc-ask.conf HERDR_PANE_ID=ww2:p1 rcerr $TB role-set w1 lead; rm -f "$TB_POLICY_G")" $'w1 role worker -> lead (by w2)。本人に配るには: tb role-broadcast w1\nrc=0'
eq "role-set: list fails" "$(FAKE_LIST_FAIL=1 HERDR_PANE_ID=whr1:p1 rcerr $TB role-set w1 worker)" $'tb: herdr agent list に失敗したため、実行者を特定できません\nrc=1'
eq "role-set: herdr down still sets" "$(FAKE_LIST_FAIL=1 rcerr $TB role-set boss worker)" $'boss role - -> worker (by human)。本人に配るには: tb role-broadcast boss\nrc=0'

# role-log
eq "role-log" "$(rcerr $TB role-log | sed 's/^20[0-9T:-]*Z\t/<T>\t/')" $'<T>\tboss\t- -> worker\thuman\t\n<T>\tw1\tworker -> lead\tw2\t\n<T>\tw1\tlead,worker -> worker\thr1\t\n<T>\tw1\tworker -> lead,worker\thuman\tリード兼務\n<T>\tw2\tworker -> lead,\thuman\tまとめて 変える\nrc=0'
eq "role-log --json" "$(rcerr $TB role-log --json | sed -E 's/"created_at":"[^"]*"/"created_at":"<T>"/; s/"herdr_session":"[^"]*"/"herdr_session":"<S>"/')" $'{"id":5,"agent":"boss","old_role":null,"new_role":"worker","changed_by":"human","reason":null,"created_at":"<T>","herdr_session":"<S>"}\n{"id":4,"agent":"w1","old_role":"worker","new_role":"lead","changed_by":"w2","reason":null,"created_at":"<T>","herdr_session":"<S>"}\n{"id":3,"agent":"w1","old_role":"lead,worker","new_role":"worker","changed_by":"hr1","reason":null,"created_at":"<T>","herdr_session":"<S>"}\n{"id":2,"agent":"w1","old_role":"worker","new_role":"lead,worker","changed_by":"human","reason":"リード兼務","created_at":"<T>","herdr_session":"<S>"}\n{"id":1,"agent":"w2","old_role":"worker","new_role":"lead,","changed_by":"human","reason":"まとめて 変える","created_at":"<T>","herdr_session":"<S>"}\nrc=0'

unset HERDR_PANE_ID HERDR_SESSION TB_DB TB_ROLES TB_POLICY_A TB_POLICY_G TB_ROLE_ADMIN TB_ROLE_MAX FAKE_AGENTS FAKE_STATUS FAKE_PROMPT_RC VISUAL EDITOR
eval "$_rc_saved"
