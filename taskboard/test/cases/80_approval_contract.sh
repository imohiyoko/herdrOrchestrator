# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb approval の出力・文面・終了コードの細かいところ(期待値は消した bash 版の出力から作った。今はこれが約束)
_ac_vars="HERDR_PANE_ID TB_DB TB_POLICY_A TB_ROLES TB_POLICY_G TB_POLICY TB_DELIVERY TB_ROUTES TB_ORG TB_CONFIG TB_LIMITS FAKE_AGENTS FAKE_STATUS"
declare -A _ac_saved=()
for _v in $_ac_vars; do if [[ -v $_v ]]; then _ac_saved[$_v]=${!_v}; fi; done
unset HERDR_PANE_ID FAKE_STATUS
export TB_DB=$TMP/acontract.db TB_POLICY_A=$TMP/ac-appr.conf TB_ROLES=$TMP/ac-roles TB_POLICY_G=$TMP/ac-grants.conf TB_DELIVERY=$TMP/ac-delivery.conf
export TB_ROUTES=$TMP/ac-routes.conf TB_ORG=$TMP/ac-org.conf TB_CONFIG=$TMP/ac-projects.conf
rm -f "$TB_POLICY_A" "$TB_POLICY_G" "$TB_DELIVERY" "$TB_ROUTES" "$TB_ORG"
printf '*=conductor\n' >"$TB_CONFIG"
mkdir -p "$TB_ROLES"; for r in lead worker reviewer; do echo "ROLE-$r" >"$TB_ROLES/$r.md"; done
printf 'lead* = lead\nw* = worker, reviewer\nrev* = reviewer # 対応\n' >"$TB_ROLES/map.conf"
ac_() { printf '{"pane_id":"w%s:p1","name":"%s","agent_status":"%s","agent_session":{"value":"s-%s"},"cwd":"%s"}' "$1" "$1" "${2:-idle}" "$1" "$TMP"; }
export FAKE_AGENTS="[$(ac_ conductor),$(ac_ lead1),$(ac_ w1),$(ac_ w2),$(ac_ rev1),$(ac_ busy1 working)]"
acsql() { sqlite3 "$TB_DB" "$@"; }
acerr() { "$@" 2>&1; echo "rc=$?"; }
acts() { sed -E 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z/TS/g'; }
as() { HERDR_PANE_ID=w$1:p1 "${@:2}"; }
$TB agents >/dev/null 2>&1
acsql "UPDATE agents SET spawned_by='lead1' WHERE name IN ('w1','w2'); UPDATE agents SET spawned_by='w1' WHERE name='rev1'; UPDATE agents SET model='opus' WHERE name='w1'; UPDATE agents SET state='stopped' WHERE name='w2';"

# ---- 使い方と引数の誤り
eq "approval: no subcommand → usage on stdout, rc 2" "$(acerr $TB approval)" $'tb approval check <操作> [--by A] [--to 宛先 --text 本文 [--kind K]]   1 行目 allow|ask|deny、2 行目 rule:(終了コード 0=allow 20=ask 4=deny)\ntb approval allow <操作のパターン>    今後は許可する(人だけ。設定 policy に * <操作> <対象> = allow の行を書く)\ntb approval ask <操作のパターン>      承認制に戻す(人だけ。設定 policy に * <操作> <対象> = ask の行を書く)\ntb approval list [--json]             承認待ちの一覧(Q-<n>。送信・agent の操作・ask で止まったコマンド。優先度の高い順、同じなら古い順)\ntb approval history [--mine|--by A] [--limit N] [--json]   allow / ask / deny の記録(新しい順。判定・状態・依頼者・決めた人・当たった行)\ntb approval approve Q-<n> [--always]  承認する(送信は届け、agent の操作は実行する。コマンドは依頼した agent に知らせ、依頼者が run で実行する。\n                                      deny の記録 Q-<n> は人だけが例外として承認できる。前の O-<n> / L-<n> も受ける)\ntb approval run Q-<n>                 承認された自分の依頼を 1 回だけ実行する(依頼した agent だけ。2 回目は実行しない)\ntb approval reject Q-<n>              却下する(依頼した agent に知らせる)\ntb approval priority Q-<n> high|normal|low   優先度を変える(依頼した agent と人。積むときは TB_PRIORITY で付けられる)\n(古い名前 tb approval <操作> / tb allow / tb ask / tb approve / tb reject も同じに使える)\nrc=2'
eq "approval help" "$(acerr $TB approval help)" $'tb approval check <操作> [--by A] [--to 宛先 --text 本文 [--kind K]]   1 行目 allow|ask|deny、2 行目 rule:(終了コード 0=allow 20=ask 4=deny)\ntb approval allow <操作のパターン>    今後は許可する(人だけ。設定 policy に * <操作> <対象> = allow の行を書く)\ntb approval ask <操作のパターン>      承認制に戻す(人だけ。設定 policy に * <操作> <対象> = ask の行を書く)\ntb approval list [--json]             承認待ちの一覧(Q-<n>。送信・agent の操作・ask で止まったコマンド。優先度の高い順、同じなら古い順)\ntb approval history [--mine|--by A] [--limit N] [--json]   allow / ask / deny の記録(新しい順。判定・状態・依頼者・決めた人・当たった行)\ntb approval approve Q-<n> [--always]  承認する(送信は届け、agent の操作は実行する。コマンドは依頼した agent に知らせ、依頼者が run で実行する。\n                                      deny の記録 Q-<n> は人だけが例外として承認できる。前の O-<n> / L-<n> も受ける)\ntb approval run Q-<n>                 承認された自分の依頼を 1 回だけ実行する(依頼した agent だけ。2 回目は実行しない)\ntb approval reject Q-<n>              却下する(依頼した agent に知らせる)\ntb approval priority Q-<n> high|normal|low   優先度を変える(依頼した agent と人。積むときは TB_PRIORITY で付けられる)\n(古い名前 tb approval <操作> / tb allow / tb ask / tb approve / tb reject も同じに使える)\nrc=0'
eq "approval: no subcommand → usage only on stdout, nothing on stderr" "$($TB approval 2>/dev/null | wc -l | tr -d ' ') $($TB approval 2>&1 >/dev/null | wc -c | tr -d ' ')" $'11 0'
eq "error: tb approval '' (empty action is not 'no subcommand')" "$(acerr $TB approval '')" $'tb: 操作名に使えない文字があります: \n(tb help で使い方)\nrc=2'
eq "approval: --kind with two words passes like bash" "$(acerr $TB approval send:task:w1 --to w1 --text hi --kind 'task info')" $'ask\nrule: 既定 ask\nrisk: allow   # 第 1 段の参考値。最終判定には使われない\nrisk-score: 0 / threshold 2\nrc=20'
eq "error: tb approval check" "$(acerr $TB approval check)" $'tb: approval <操作>(例: send:task:worker1, role:change)[--by agent][--to 宛先 --text 本文 [--kind K] [--task T-..]]\n(tb help で使い方)\nrc=2'
eq "error: tb approval 'a b'" "$(acerr $TB approval 'a b')" $'tb: 操作名に使えない文字があります: a b\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --bogus" "$(acerr $TB approval x --bogus)" $'tb: approval: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --by" "$(acerr $TB approval x --by)" $'tb: --by に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --by Bad" "$(acerr $TB approval x --by Bad)" $'tb: approval: --by は agent 名: Bad\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --task" "$(acerr $TB approval x --task)" $'tb: --task に値が必要です\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --to w1" "$(acerr $TB approval x --to w1)" $'tb: approval: リスク評価には --to と --text の両方が要ります\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --text t" "$(acerr $TB approval x --text t)" $'tb: approval: リスク評価には --to と --text の両方が要ります\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --to 'a b' --text t" "$(acerr $TB approval x --to 'a b' --text t)" $'tb: approval: --to は agent 名(英数字と . _ -)\n(tb help で使い方)\nrc=2'
eq "error: tb approval x --to w1 --text t --kind bogus" "$(acerr $TB approval x --to w1 --text t --kind bogus)" $'tb: approval: --kind は task info answer のどれか\n(tb help で使い方)\nrc=2'
eq "error: tb approval check x --to w1" "$(acerr $TB approval check x --to w1)" $'tb: approval: リスク評価には --to と --text の両方が要ります\n(tb help で使い方)\nrc=2'
eq "error: tb allow" "$(acerr $TB allow)" $'tb: allow <操作のパターン>\n(tb help で使い方)\nrc=2'
eq "error: tb allow a b" "$(acerr $TB allow a b)" $'tb: allow <操作のパターン>\n(tb help で使い方)\nrc=2'
eq "error: tb allow 'a b'" "$(acerr $TB allow 'a b')" $'tb: 操作名に使えない文字があります: a b\n(tb help で使い方)\nrc=2'
eq "error: tb ask" "$(acerr $TB ask)" $'tb: ask <操作のパターン>\n(tb help で使い方)\nrc=2'
eq "error: tb approval allow" "$(acerr $TB approval allow)" $'tb: allow <操作のパターン>\n(tb help で使い方)\nrc=2'
eq "error: tb approval ask x y" "$(acerr $TB approval ask x y)" $'tb: ask <操作のパターン>\n(tb help で使い方)\nrc=2'
eq "error: tb approve" "$(acerr $TB approve)" $'tb: approve O-<n> [--always]\n(tb help で使い方)\nrc=2'
eq "error: tb approve x" "$(acerr $TB approve x)" $'tb: approve O-<n> [--always]\n(tb help で使い方)\nrc=2'
eq "error: tb approve O-x" "$(acerr $TB approve O-x)" $'tb: approve O-<n> [--always]\n(tb help で使い方)\nrc=2'
eq "error: tb approval approve" "$(acerr $TB approval approve)" $'tb: approve O-<n> [--always]\n(tb help で使い方)\nrc=2'
eq "error: tb reject" "$(acerr $TB reject)" $'tb: reject O-<n>\n(tb help で使い方)\nrc=2'
eq "error: tb reject O-1 x" "$(acerr $TB reject O-1 x)" $'tb: reject O-<n>\n(tb help で使い方)\nrc=2'
eq "error: tb approval reject O-" "$(acerr $TB approval reject O-)" $'tb: reject O-<n>\n(tb help で使い方)\nrc=2'
eq "error: tb approve O-999" "$(acerr $TB approve O-999)" $'tb: outbox が見つかりません: O-999\nrc=1'
eq "error: tb reject O-999" "$(acerr $TB reject O-999)" $'tb: O-999 は承認待ちではありません\nrc=1'
eq "error: tb approval approve O-999 --always" "$(acerr $TB approval approve O-999 --always)" $'tb: outbox が見つかりません: O-999\nrc=1'
eq "error: approve O-999 from an agent (says nothing about the row)" "$(as w1 acerr $TB approve O-999)" $'tb: tb approve: この承認待ちは人が自分の端末で実行します(agent \'w1\' の pane からは承認できません)\nrc=1'
eq "error: allow from an agent" "$(as w1 acerr $TB approval allow x:y)" $'tb: tb allow は人が自分の端末で実行します(agent \'w1\' の pane からは実行できません)。記録 Q-2(例外は人が tb approval approve Q-2 で許可できます)\nrc=1'
eq "error: approve from an unknown pane list" "$(FAKE_LIST_FAIL=1 HERDR_PANE_ID=wX:p1 acerr $TB approve O-1)" $'tb: herdr agent list に失敗したため、実行者を特定できません\nrc=1'

# ---- 判定(approvals / grants / org)
eq "check: no approvals conf → default ask" "$(acerr $TB approval check send:task:w1)" $'ask\nrule: 既定 ask\nrc=20'
cat >"$TB_POLICY_A" <<'A'
* send:info * = allow
* send:task @lead = allow
* send:answer @worker+reviewer = allow
* send:answer @worker+lead = ask
* agent:stop w? = allow
* role:[ab]* * = allow
A
eq "check: send:info:w1" "$(acerr $TB approval send:info:w1)" $'allow\nrule: policy: * send:info * = allow\nrc=0'
eq "check: send:task:lead1" "$(acerr $TB approval send:task:lead1)" $'allow\nrule: policy: * send:task @lead = allow\nrc=0'
eq "check: send:task:w1" "$(acerr $TB approval send:task:w1)" $'ask\nrule: 既定 ask\nrc=20'
eq "check: send:answer:w1" "$(acerr $TB approval send:answer:w1)" $'allow\nrule: policy: * send:answer @worker+reviewer = allow\nrc=0'
eq "check: send:answer:rev1" "$(acerr $TB approval send:answer:rev1)" $'allow\nrule: 既定 allow\nrc=0'
eq "check: agent:stop:w1" "$(acerr $TB approval agent:stop:w1)" $'allow\nrule: policy: * agent:stop w? = allow\nrc=0'
eq "check: agent:stop:w10" "$(acerr $TB approval agent:stop:w10)" $'ask\nrule: 既定 ask\nrc=20'
eq "check: role:apply" "$(acerr $TB approval role:apply)" $'allow\nrule: policy: * role:[ab]* * = allow\nrc=0'
eq "check: role:change" "$(acerr $TB approval role:change)" $'ask\nrule: 既定 ask\nrc=20'
eq "check: setup:hooks" "$(acerr $TB approval setup:hooks)" $'ask\nrule: 既定 ask\nrc=20'
eq "check: @x" "$(acerr $TB approval @x)" $'ask\nrule: 既定 ask\nrc=20'
cat >"$TB_POLICY_G" <<'G'
@lead agent:read ~child = allow
w1 agent:read ~sibling = allow
rev1 agent:read ~parent = allow
lead1 agent:peek ~descendant = allow
w* send:task @reviewer = allow
bad line here x
lead1 role:change w1 extra
G
eq "check: grants agent:read:w1 --by lead1" "$(eval "acerr $TB approval check agent:read:w1 --by lead1")" $'allow\nrule: policy: @lead agent:read ~child = allow\nrc=0'
eq "check: grants agent:read:w2 --by w1" "$(eval "acerr $TB approval check agent:read:w2 --by w1")" $'allow\nrule: policy: w1 agent:read ~sibling = allow\nrc=0'
eq "check: grants agent:read:w1 --by rev1" "$(eval "acerr $TB approval check agent:read:w1 --by rev1")" $'allow\nrule: policy: rev1 agent:read ~parent = allow\nrc=0'
eq "check: grants agent:peek:rev1 --by lead1" "$(eval "acerr $TB approval check agent:peek:rev1 --by lead1")" $'allow\nrule: policy: lead1 agent:peek ~descendant = allow\nrc=0'
eq "check: grants agent:peek:w1 --by w1" "$(eval "acerr $TB approval check agent:peek:w1 --by w1")" $'ask\nrule: 既定 ask\nrc=20'
eq "check: grants send:task:rev1 --by w2" "$(eval "acerr $TB approval check send:task:rev1 --by w2")" $'allow\nrule: policy: w* send:task @reviewer = allow\nrc=0'
eq "check: grants send:task:lead1 --by w2" "$(eval "acerr $TB approval check send:task:lead1 --by w2")" $'allow\nrule: policy: * send:task @lead = allow\nrc=0'
eq "check: grants role:change:w1 --by lead1" "$(eval "acerr $TB approval check role:change:w1 --by lead1")" $'ask\nrule: 既定 ask\nrc=20'
eq "check: grants agent:read:w1 --by human" "$(eval "acerr $TB approval check agent:read:w1 --by human")" $'allow\nrule: (人は常に通る)\nrc=0'
cat >"$TB_ORG" <<'O'
w* -> lead1
rev1 -> ~parent
lead1 -> lead1
O
printf '@worker approve:send:* ~report = allow\nw1 agent:read ~manager = allow\nrev1 agent:read ~superior = allow\nw2 agent:read ~peer = allow\nlead1 agent:read ~subordinate = allow\n' >>"$TB_POLICY_G"
eq "check: org agent:read:lead1 --by w1" "$(eval "acerr $TB approval check agent:read:lead1 --by w1")" $'allow\nrule: policy: w1 agent:read ~manager = allow\nrc=0'
eq "check: org agent:read:lead1 --by rev1" "$(eval "acerr $TB approval check agent:read:lead1 --by rev1")" $'allow\nrule: policy: rev1 agent:read ~superior = allow\nrc=0'
eq "check: org agent:read:w1 --by w2" "$(eval "acerr $TB approval check agent:read:w1 --by w2")" $'allow\nrule: policy: w2 agent:read ~peer = allow\nrc=0'
eq "check: org agent:read:rev1 --by lead1" "$(eval "acerr $TB approval check agent:read:rev1 --by lead1")" $'allow\nrule: policy: lead1 agent:read ~subordinate = allow\nrc=0'
eq "check: org agent:read:w2 --by w2" "$(eval "acerr $TB approval check agent:read:w2 --by w2")" $'ask\nrule: 既定 ask\nrc=20'

# ---- リスク評価(--to と --text)
eq "risk: from a human, plain text" "$(acerr $TB approval check send:task:w1 --to w1 --text 'please review')" $'ask\nrule: 既定 ask\nrisk: allow   # 第 1 段の参考値。最終判定には使われない\nrisk-score: 0 / threshold 2\nrc=20'
eq "risk: from an agent, destructive word, stopped target" "$(as w1 acerr $TB approval send:task:w2 --to w2 --text 'then git PUSH --force it' --kind info)" $'ask\nrule: 既定 ask\nrisk: ask   # 第 1 段の参考値。最終判定には使われない\nrisk-score: 2 / threshold 2\nrisk-reason: R-02 +ask 本文に破壊的・公開系の操作語(語: PUSH)\nrisk-reason: R-06 +2 宛先が停止中\nrc=20'
eq "risk: unknown sender pane, unknown target" "$(HERDR_PANE_ID=wZ:p9 acerr $TB approval send:task:nobody --to nobody --text 'rm -rf /' --task T-x-001)" $'ask\nrule: 既定 ask\nrisk: ask   # 第 1 段の参考値。最終判定には使われない\nrisk-score: 0 / threshold 2\nrisk-reason: R-07 +ask 評価不能(fail-closed): 入力が取れません: sender.role sender.model\nrc=20'
eq "risk: word inside another word is not counted" "$(acerr $TB approval send:task:w1 --to w1 --text 'unmerged pushy')" $'ask\nrule: 既定 ask\nrisk: allow   # 第 1 段の参考値。最終判定には使われない\nrisk-score: 0 / threshold 2\nrc=20'
eq "risk: Japanese right next to the word" "$(acerr $TB approval send:task:rev1 --to rev1 --text 'あとでmergeして')" $'ask\nrule: 既定 ask\nrisk: ask   # 第 1 段の参考値。最終判定には使われない\nrisk-score: 0 / threshold 2\nrisk-reason: R-02 +ask 本文に破壊的・公開系の操作語(語: merge)\nrc=20'

# ---- allow / ask の書き方(設定 policy に「* <操作> <対象> = allow|ask」の行を書く)
_ac_pa=$TB_POLICY_A _ac_pg=$TB_POLICY_G; unset TB_POLICY_A TB_POLICY_G
export TB_POLICY=$TMP/ac-policy.conf
rm -f "$TB_POLICY"
eq "allow: no conf yet → one line" "$($TB approval allow send:task:w1; acts <"$TB_POLICY")" $'* send:task w1 = allow\n* send:task w1 = allow   # TS に人が追加(tb approval allow)'
: >"$TB_POLICY"
eq "ask: empty conf → one line" "$($TB ask 'agent:stop:*'; acts <"$TB_POLICY")" $'* agent:stop * = ask\n* agent:stop * = ask   # TS に人が追加(tb approval ask)'
printf '# コメントだけ\n\n# もう 1 つ\n\n\n' >"$TB_POLICY"
eq "allow: role:change is refused (who gets it is written in policy by a human)" "$($TB allow role:change 2>&1; echo "rc=$?"; acts <"$TB_POLICY")" $'tb: tb allow: role:change は誰に許すかを決めて policy に直接書く(例: @<役割> role:change * = allow。tb config edit policy)\nrc=1\n# コメントだけ\n\n# もう 1 つ'
eq "allow: comments only → appended" "$($TB allow 'agent:stop:*' >/dev/null; acts <"$TB_POLICY")" $'# コメントだけ\n\n# もう 1 つ\n* agent:stop * = allow   # TS に人が追加(tb approval allow)'
printf '# 説明\n\n  # 字下げのコメント\n* send:task * = ask\n#* send:info * = allow\n* send:info * = ask  # 後で\n* send:info * = ask\n*  send:info  * =ask\n\n' >"$TB_POLICY"
eq "allow: appended after the rows" "$($TB allow 'agent:stop:*' >/dev/null; acts <"$TB_POLICY")" $'# 説明\n\n  # 字下げのコメント\n* send:task * = ask\n#* send:info * = allow\n* send:info * = ask  # 後で\n* send:info * = ask\n*  send:info  * =ask\n* agent:stop * = allow   # TS に人が追加(tb approval allow)'
eq "allow: rewrites every same line once, keeps blank lines" "$($TB approval allow 'send:info:*' >/dev/null; acts <"$TB_POLICY")" $'# 説明\n\n  # 字下げのコメント\n* send:task * = ask\n#* send:info * = allow\n* send:info * = allow   # TS に人が変更(tb approval allow)\n* agent:stop * = allow   # TS に人が追加(tb approval allow)'
eq "allow: a stricter row still applies → warned" "$(printf '* send:* w1 = deny\n' >"$TB_POLICY"; $TB allow send:task:w1 2>&1 >/dev/null)" "tb: 注意: もっと厳しい行が当たるため、send:task:w1 はまだ deny です(policy: * send:* w1 = deny)"
eq "approval_changes" "$(acsql "SELECT pattern||'='||value FROM approval_changes ORDER BY id;")" $'send:task:w1=allow\nagent:stop:*=ask\nagent:stop:*=allow\nagent:stop:*=allow\nsend:info:*=allow\nsend:task:w1=allow'
_ac_pol=$TB_POLICY; unset TB_POLICY
eq "allow: no file named → DB row" "$($TB allow setup:hooks; acsql "SELECT how||'|'||changed_by FROM config_changes WHERE key='policy';"; acsql "SELECT body FROM config WHERE key='policy';" | acts)" $'* setup:hooks * = allow\nallow|human\n* setup:hooks * = allow   # TS に人が追加(tb approval allow)'
export TB_POLICY=$_ac_pol
printf '* send:info * = allow\n* send:answer * = ask\n' >"$TB_POLICY"
unset _ac_pol

# ---- 承認したあとの届け方(delivery / routes / limits)
ac_send() { as "$1" $TB send "$2" --text "$3" "${@:4}" >/dev/null 2>&1; acsql "SELECT MAX(id) FROM outbox;"; }
printf 'conductor -> *\n' >"$TB_DELIVERY"
o=$(ac_send conductor w1 'one')
eq "approve: sent" "$(acerr $TB approval approve "O-$o")" $'O-1 approved and sent to w1\nrc=0'
printf 'conductor -> w1 : task=hold\nconductor -> * : *=now\n' >"$TB_DELIVERY"
o=$(ac_send conductor w1 'two')
eq "approve: held" "$(acerr $TB approve "O-$o"; acsql "SELECT status||' '||reason FROM outbox WHERE id=$o;" | acts; acsql "SELECT recipient||' '||kind||' '||rule FROM inbox WHERE outbox_id=$o;")" $'O-2 approved and held for w1 (I-1)\nrc=0\nsent delivery: conductor -> w1 : task=hold / 認可: 既定 ask / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)] / 人が承認 TS / delivery: conductor -> w1 : task=hold → inbox I-1\nw1 task delivery: conductor -> w1 : task=hold'
printf 'conductor -> * : *=now\n' >"$TB_DELIVERY"
o=$(ac_send conductor rev1 'three')
printf 'conductor -> rev1 : task=block\nconductor -> * : *=now\n' >"$TB_DELIVERY"
: >"$FAKE_LOG"
eq "approve: blocked, the sender is told" "$(acerr $TB approve "O-$o"; acsql "SELECT status||' '||reason FROM outbox WHERE id=$o;" | acts; grep '^prompt' "$FAKE_LOG")" $'O-3 approved but blocked by rev1\nrc=0\ndenied delivery: conductor -> * : task=now / 認可: 既定 ask / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)] / 人が承認 TS / delivery: conductor -> rev1 : task=block → blocked\nprompt conductor\t[tb: blocked / O-3] rev1 への送信 O-3(task)は rev1 の受信設定で止められました(delivery: conductor -> rev1 : task=block)。rev1 には届けていません。依頼や相談はチケットにしてください(tb add -p <project> -t <題> --desc <内容>。進み具合は tb comment で返ってきます)'
printf 'conductor -> * : *=now\n' >"$TB_DELIVERY"
o=$(ac_send conductor busy1 'four')
eq "approve: target working → queued" "$(FAKE_STATUS=working acerr $TB approve "O-$o"; acsql "SELECT status FROM outbox WHERE id=$o;")" $'tb: 宛先 \'busy1\' が working のため送りません。未送信(pending に残す): O-4\nO-4 approved (宛先使用中のため queued。tb flush で届く)\nrc=0\nqueued'
o=$(ac_send conductor w1 'five')
eq "approve: target missing → 11" "$(FAKE_STATUS=missing acerr $TB approve "O-$o"; acsql "SELECT status FROM outbox WHERE id=$o;")" $'tb: 宛先 \'w1\' が herdr セッション \'default\' に見つかりません(セッション違いの可能性。HERDR_SESSION=<名前> で指定)。未送信: O-5\ntb: O-5 は承認済みですが宛先 \'w1\' に届けられません(上の理由を見てください。queued のまま。直したら tb flush)\nO-5 approved but delivery failed (queued)\nrc=11\nqueued'
o=$(ac_send conductor w1 'six')
export TB_LIMITS=$TMP/ac-limits.conf; printf 'max_working_agents = 1\n' >"$TB_LIMITS"
eq "approve: max_working_agents → queued" "$(acerr $TB approve "O-$o"; acsql "SELECT status FROM outbox WHERE id=$o;")" $'tb: 作業中の agent が上限です(作業中 1、max_working_agents=1)。\'w1\' への送信は outbox で待たせます(tb flush で再送)\nO-6 approved (宛先使用中のため queued。tb flush で届く)\nrc=0\nqueued'
rm -f "$TB_LIMITS"
o=$(ac_send conductor w1 'seven')
printf '! conductor -> w1\nconductor -> *\n' >"$TB_ROUTES"
eq "approve: legacy routes deny" "$(acerr $TB approve "O-$o"; acsql "SELECT status||' '||reason FROM outbox WHERE id=$o;" | acts)" $'O-7 approved but blocked by w1\nrc=0\ndenied delivery: conductor -> * : task=now / 認可: 既定 ask / risk: ask(参考。score 1/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1)] / 人が承認 TS / routes.conf: !conductor -> w1 → blocked'
rm -f "$TB_ROUTES"
printf 'conductor -> w1 : *=now\n' >"$TB_DELIVERY"
o=$(ac_send conductor w1 'eight')
eq "approve --always: rule then sent" "$(acerr $TB approve "O-$o" --always; grep -c '^\* send:task w1 = allow' "$TB_POLICY")" $'send:task:w1 = allow(今後は承認なし)\nO-8 approved and sent to w1\nrc=0\n1'
printf '* -> *\n' >"$TB_DELIVERY"
o=$(ac_send w1 lead1 'from a worker')
rm -f "$TB_DELIVERY"
eq "approve: default deny for a non-conductor" "$(acerr $TB approve "O-$o"; acsql "SELECT status||' '||reason FROM outbox WHERE id=$o;" | acts)" $'O-9 approved but blocked by lead1\nrc=0\ndenied delivery: * -> * : task=now / 認可: 既定 ask / risk: allow(参考。score 0/2) / 人が承認 TS / default: w1 は指揮官ではない(worker からの報告は tb comment、依頼はチケットを使う) → blocked'
o=$(ac_send conductor w2 'nine')
eq "reject" "$(acerr $TB approval reject "O-$o"; acsql "SELECT status||' '||reason FROM outbox WHERE id=$o;" | acts)" $'O-10 rejected\nrc=0\ndenied default: conductor conductor / 認可: 既定 ask / risk: ask(参考。score 3/2) [unknown:sender.role +ask sender.role が不明(設定 ask); unknown:sender.model +1 sender.model が不明(設定 risk:1); R-06 +2 宛先が停止中] / 人が却下 TS'
eq "reject: twice" "$(acerr $TB reject "O-$o")" $'tb: O-10 は承認待ちではありません\nrc=1'
eq "approve: not pending" "$(acerr $TB approve "O-$o")" $'tb: O-10 は承認待ちではありません(denied)\nrc=1'

for _v in $_ac_vars; do if [[ -v _ac_saved[$_v] ]]; then export "$_v=${_ac_saved[$_v]}"; else unset "$_v"; fi; done
unset _ac_saved _ac_pa _ac_pg TB_POLICY
