# shellcheck shell=bash
# レシピ(ADR 0005 の段 2): 登録・一覧・履歴(人だけ)と、汎用の入口 tb recipe-run(判定と記録。実行は読み取りの部品だけ)
# DB は $TMP/recipes.db、設定は $TMP/rc-*.conf だけを使う。project は rcp だけ。環境変数は最後に戻す
_saved_db=$TB_DB; _saved_appr=${TB_POLICY_A-}; _saved_grants=${TB_POLICY_G-}; _saved_agents=${FAKE_AGENTS-}; _saved_pane=${HERDR_PANE_ID-}
export TB_DB=$TMP/recipes.db TB_POLICY_A=$TMP/rc-approvals.conf TB_POLICY_G=$TMP/rc-grants.conf TB_RECIPE_POLL_SEC=0.05
export FAKE_AGENTS='[{"pane_id":"wL:p1","name":"lead","agent_session":{"value":"sl"}},{"pane_id":"wW:p1","name":"w1","agent_session":{"value":"s1"}}]'
unset HERDR_PANE_ID
printf '* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"; : >"$TB_POLICY_G"
rsql() { sqlite3 "$TB_DB" "$@"; }
asw1() { HERDR_PANE_ID=wW:p1 "$@"; }       # w1 の pane から
aslead() { HERDR_PANE_ID=wL:p1 "$@"; }     # lead の pane から
gets() { grep -c '^get ' "$FAKE_LOG"; }

LOOK=$'desc 状態を見て idle を待つ\nrisk low\nstep agent-status target=$target\nstep wait-idle target=$target timeout=5'
RS=$'desc 自分を安全に再起動する\nrisk low\narg note? text:500\nstep agent-status target=$requester\nstep agent-restart target=$requester prompt=$note'
CFG=$'step config-set key=approvals'

# --- 登録は人だけ
eq "recipe-set: agent の pane からは断る" "$(asw1 rc "$TB" recipe-set look <<<"$LOOK")" "1"
eq "recipe-set: 断ったら何も残らない" "$(rsql "SELECT count(*) FROM recipes; SELECT count(*) FROM recipe_changes;" 2>/dev/null | paste -sd' ')" "0 0"
eq "recipe-set: 人は登録できる" "$($TB recipe-set look <<<"$LOOK")" "look added (risk low, steps 2, op recipe:look:<対象>)"
eq "recipe-set: --file でも登録できる" "$(printf '%s\n' "$RS" >"$TMP/rc-rs.txt"; $TB recipe-set rs --file "$TMP/rc-rs.txt" 2>/dev/null)" "rs added (risk mid, steps 2, op recipe:rs:<対象>)"
eq "recipe-set: 宣言 risk low は部品の最大 mid に上がる(下げられない)" "$(rsql "SELECT risk FROM recipes WHERE name='rs';")" "mid"
eq "recipe-set: 下げられなかったことを伝える" "$($TB recipe-set rs <<<"$RS" 2>&1 >/dev/null | grep -c '宣言 risk low は、中の部品の最大 mid より低い')" "1"
eq "recipe-set: 宣言が部品より高ければ宣言が効く" "$($TB recipe-set hi <<<$'risk high\nstep agent-status target=$target'; rsql "SELECT risk FROM recipes WHERE name='hi';")" "hi added (risk high, steps 1, op recipe:hi:<対象>)
high"
eq "recipe-set: 人だけの部品を含むと human-only" "$($TB recipe-set cfg <<<"$CFG" >/dev/null; rsql "SELECT risk FROM recipes WHERE name='cfg';")" "human-only"
eq "recipe-set: 置き換えると update(前の本文も残す)" \
   "$($TB recipe-set rs <<<"$RS" 2>/dev/null; rsql "SELECT how, old_body IS NOT NULL, new_body IS NOT NULL, changed_by FROM recipe_changes WHERE name='rs' ORDER BY id DESC LIMIT 1;")" \
   "rs updated (risk mid, steps 2, op recipe:rs:<対象>)
update|1|1|human"

# --- 登録の検査(1 つでも通らなければ登録しない)
_bad() { local n=$1; shift; printf '%s' "$*" | "$TB" recipe-set "$n" >/dev/null 2>&1; echo "$?:$(rsql "SELECT count(*) FROM recipes WHERE name='$n';")"; }
eq "recipe-set: 名前に : は使えない" "$(rc "$TB" recipe-set 'a:b' <<<"$LOOK")" "2"
eq "recipe-set: 名前は英小文字で始まる" "$(rc "$TB" recipe-set 'Ab' <<<"$LOOK") $(rc "$TB" recipe-set '1a' <<<"$LOOK") $(rc "$TB" recipe-set 'a_b' <<<"$LOOK")" "2 2 2"
eq "recipe-set: 一覧にない部品" "$(_bad b1 'step no-such-part')" "2:0"
eq "recipe-set: 書けない行(シェルなど)" "$(_bad b2 $'step agent-status target=$target\nrun rm -rf /')" "2:0"
eq "recipe-set: 宣言していない \$引数" "$(_bad b3 'step wait-idle target=$target timeout=$secs')" "2:0"
eq "recipe-set: arg は step より前に書く" "$(_bad b4 $'step wait-idle target=$target timeout=$secs\narg secs int:1-60')" "2:0"
eq "recipe-set: 名前の無い \$ は宣言されていない引数" "$(_bad b4z 'step wait-idle target=$target timeout=$') $(printf 'step wait-idle target=$target timeout=$' | "$TB" recipe-set b4z 2>&1 | grep -c '\$ は宣言されていません')" "2:0 1"
eq "recipe-set: リテラルが部品の型に合わない" "$(_bad b5 'step wait-idle target=$target timeout=9999')" "2:0"
eq "recipe-set: 値の中の \$ は書けない" "$(_bad b6 'step send-info to=$target text=a$x')" "2:0"
eq "recipe-set: \$requester は agent 型の引数にだけ" "$(_bad b7 'step send-info to=$target text=$requester')" "2:0"
eq "recipe-set: 宣言の型が部品の型と合わない" "$(_bad b8 $'arg t text:10\nstep wait-idle target=$target timeout=$t')" "2:0"
eq "recipe-set: 部品の必須の引数が無い" "$(_bad b9 'step wait-idle target=$target')" "2:0"
eq "recipe-set: 部品に無い引数" "$(_bad b10 'step agent-status target=$target extra=1')" "2:0"
eq "recipe-set: step が無い" "$(_bad b11 'desc 何もしない')" "2:0"
eq "recipe-set: step は 16 個まで" "$(_bad b12 "$(for i in $(seq 17); do echo 'step agent-status target=$target'; done)")" "2:0"
eq "recipe-set: 予約の引数名(requester / target)は宣言できない" "$(_bad b13 $'arg target agent\nstep agent-status target=$target')" "2:0"
eq "recipe-set: 不正な宣言 risk" "$(_bad b14 $'risk none\nstep agent-status target=$target')" "2:0"
eq "recipe-set: 引数の名前が 2 回" "$(_bad b15 $'arg a int:1-9\narg a int:1-9\nstep wait-idle target=$target timeout=$a')" "2:0"
eq "recipe-set: step の中で同じ引数が 2 回" "$(_bad b17 'step agent-status target=$target target=$requester')" "2:0"
eq "recipe-set: 省略できる引数を、省略できない部品の引数に渡せない" "$(_bad b18 $'arg secs? int:1-60\nstep wait-idle target=$target timeout=$secs')" "2:0"
eq "recipe-set: 省略できる引数を、省略できる部品の引数には渡せる" "$($TB recipe-set b19 <<<$'arg note? text:10\nstep agent-revive target=$target prompt=$note' >/dev/null; rsql "SELECT count(*) FROM recipes WHERE name='b19';"; $TB recipe-rm b19 >/dev/null)" "1"
eq "recipe-set: agent 型の引数にリテラルの agent 名は書けない" "$(_bad b20 'step agent-status target=lead')" "2:0"
eq "recipe-set: agent 型の引数は宣言できない(対象は --target)" "$(_bad b21 $'arg who agent\nstep agent-status target=$target')" "2:0"
eq "recipe-set: 理由を出す" "$(printf 'step agent-status target=lead\n' | $TB recipe-set b22 2>&1 | grep -c '\$requester か \$target だけ')" "1"
eq "recipe-set: 検査を通らないと履歴も残らない" "$(rsql "SELECT count(*) FROM recipe_changes WHERE name LIKE 'b%' AND name != 'b19';")" "0"
eq "recipe-set: 理由を行ごとに出す" "$(printf 'step no-such-part\nfoo\n' | $TB recipe-set b16 2>&1 | grep -c '行目')" "2"

# --- 一覧・中身・履歴(だれでも読める)
eq "recipe-list: 名前の順に、リスク階層・step 数・説明" "$(asw1 "$TB" recipe-list | cut -f1-3,6)" "$(printf 'cfg\thuman-only\t1\t-\nhi\thigh\t1\t-\nlook\tlow\t2\t状態を見て idle を待つ\nrs\tmid\t2\t自分を安全に再起動する')"
eq "recipe-list --json" "$($TB recipe-list --json | jq -r 'select(.name=="rs") | "\(.risk) \(.risk_at_set) \(.steps)"')" "mid mid 2"
eq "recipe-show: 操作名・リスク階層・部品の性質" "$($TB recipe-show rs | sed -n '2p;4,6p')" "op: recipe:rs:<対象>(既定の対象は依頼者自身)
risk: mid
step 1: agent-status target=\$requester(none / low / inside / 操作名 -)
step 2: agent-restart target=\$requester prompt=\$note(local-write / mid / outside / 操作名 agent:stop:\$target agent:revive:\$target)"
eq "recipe-show: 無いレシピは 1" "$(rc "$TB" recipe-show nope)" "1"
eq "recipe-history: 名前で絞る" "$($TB recipe-history rs | cut -f2,3,5 | paste -sd' ')" "rs	add	human rs	update	human rs	update	human"
eq "recipe-history --json: 本文の前後" "$($TB recipe-history look --json | jq -r '"\(.how) \(.old_body == null) \(.new_body | contains("wait-idle"))"')" "add true true"

# --- 実行: 登録されていないレシピ名は ask ではなく実行しない
: >"$FAKE_LOG"
eq "recipe-run: 未登録は 4(人の端末でも)" "$(rc "$TB" recipe-run nope --target w1)" "4"
eq "recipe-run: 未登録は 4(agent)" "$(asw1 rc "$TB" recipe-run nope)" "4"
eq "recipe-run: 未登録でも記録は残す" "$(rsql "SELECT count(*) FROM task_events WHERE kind='recipe_end' AND new LIKE '登録されていないレシピ%';")" "2"
eq "recipe-run: 未登録では herdr に何も問い合わせない(get なし)" "$(gets)" "0"
eq "recipe-run: 名前に : は使えない" "$(rc "$TB" recipe-run 'a:b' --target w1)" "2"

# --- レシピの操作名の判定(approvals の既定 ask)
eq "recipe-run: 判定が ask なら 20 で、部品は動かない" "$(asw1 rc "$TB" recipe-run look; gets)" "20
0"
printf '* recipe:look * = allow\n* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
out=$(asw1 "$TB" recipe-run look 2>&1); r=$?
eq "recipe-run: allow なら読み取りの部品を順に実行する" "$r|$(tail -2 <<<"$out")" "0|w1 idle
w1 idle"
eq "recipe-run: 既定の対象は依頼者自身" "$(grep '^get ' "$FAKE_LOG" | sort -u)" "get w1"
eq "recipe-run: 判定を 1 行ずつ出す" "$(grep -c '^step [12] ' <<<"$out")" "2"
eid=$(rsql "SELECT max(id) FROM task_events WHERE kind='recipe_run';")
eq "recipe-run: 記録は recipe_run / recipe_step × 2 / recipe_end(done)、依頼者が actor" \
   "$(rsql "SELECT kind || ':' || actor FROM task_events WHERE id=$eid OR ref='E-$eid' ORDER BY id;" | paste -sd' ') $(rsql "SELECT new FROM task_events WHERE ref='E-$eid' AND kind='recipe_end';")" \
   "recipe_run:w1 recipe_step:w1 recipe_step:w1 recipe_end:w1 done"
eq "recipe-run: recipe_run の ref は判定の操作名" "$(rsql "SELECT ref FROM task_events WHERE id=$eid;")" "recipe:look:w1"
eq "recipe-run: 記録は tb events に出る" "$($TB events --kind recipe_end | tail -1 | cut -f4,6,7)" "recipe_end	done	w1"
eq "recipe-run: 他者を対象にすると操作名の対象が変わる" "$(asw1 "$TB" recipe-run look --target lead | head -1)" "recipe look target=lead risk=low: allow(policy: * recipe:look * = allow)"
printf '* recipe:look w1 = allow\n* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
eq "recipe-run: 対象が許可の範囲の外なら ask(20)" "$(asw1 rc "$TB" recipe-run look --target lead)" "20"
# grants: 操作は recipe:<名前>(対象を除く)。approvals が ask でも、grants の行で allow
: >"$TB_POLICY_A"; printf 'lead recipe:look w1 = allow\n' >"$TB_POLICY_G"
eq "recipe-run: grants の 'lead recipe:look w1' で lead は w1 を対象にできる" "$(aslead "$TB" recipe-run look --target w1 | head -1)" "recipe look target=w1 risk=low: allow(policy: lead recipe:look w1 = allow)"
eq "recipe-run: grants の行は対象を絞る(lead → lead は ask)" "$(aslead rc "$TB" recipe-run look)" "20"
eq "recipe-run: tb can も同じ判定" "$($TB can lead recipe:look w1 | head -1) $($TB can w1 recipe:look lead | head -1)" "allow ask"
: >"$TB_POLICY_G"

# --- 部品ごとの判定: レシピの許可は部品の許可の代わりにならない
printf '* recipe:* * = allow\n* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
: >"$FAKE_LOG"
out=$(asw1 "$TB" recipe-run rs note=memo 2>&1); r=$?
eq "recipe-run: レシピが allow でも、部品の agent:stop / revive が ask なら 20" "$r" "20"
eq "recipe-run: 止めた step と理由を出す" "$(grep -c 'step 2 (agent-restart) で止めました: ask' <<<"$out")" "1"
eq "recipe-run: 止めたら、前の読み取りの部品も実行しない(全部を判定してから実行)" "$(gets)" "0"
eid=$(rsql "SELECT max(id) FROM task_events WHERE kind='recipe_run';")
eq "recipe-run: 止めたときも step ごとの判定を記録する" "$(rsql "SELECT old FROM task_events WHERE ref='E-$eid' AND kind='recipe_step' ORDER BY id;" | paste -sd,)" "1 agent-status,2 agent-restart"
printf '* recipe:* * = allow\n* agent:stop w1 = allow\n* agent:revive w1 = allow\n* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"
out=$(asw1 "$TB" recipe-run rs 2>&1); r=$?
eq "recipe-run: 判定が全部 allow で外の実行役の部品があれば、その場では実行せず待ち行列に置く(0)" "$r|$(tail -1 <<<"$out" | grep -c '^R-[0-9]* queued')" "0|1"
eq "recipe-run: 何も止めていない・起動していない" "$(grep -cE '^(pane-close|start) ' "$FAKE_LOG")" "0"
eq "recipe-run: --dry-run は判定だけ(記録しない)" "$(n0=$(rsql "SELECT count(*) FROM task_events;"); asw1 "$TB" recipe-run look --dry-run >/dev/null; echo $(($(rsql "SELECT count(*) FROM task_events;") - n0)) $(gets))" "0 0"

# --- 人だけの部品は、許可しても agent からは実行できない
printf '* * * = allow\n' >"$TB_POLICY_A"
printf '* recipe:* * = allow\n* approve:* * = allow\n' >"$TB_POLICY_G"
eq "recipe-run: 人だけの部品は agent から 4(approvals / grants を全部許可しても)" "$(asw1 "$TB" recipe-run cfg 2>&1 | grep -c '人だけの部品(コードで固定)'; asw1 rc "$TB" recipe-run cfg)" "2
4"
eq "recipe-run: 人の端末なら人だけの判定は通る(config-set の実行はまだ無いので 4)" "$($TB recipe-run cfg --target w1 2>&1 | grep -c '人だけ')$(rc "$TB" recipe-run cfg --target w1)" "04"
: >"$TB_POLICY_G"; printf '* recipe:* * = allow\n* send:info * = ask\n* send:answer * = ask\n' >"$TB_POLICY_A"

# --- 引数
eq "recipe-run: 宣言していない引数は 2" "$(asw1 rc "$TB" recipe-run rs bogus=1)" "2"
eq "recipe-run: 宣言の型に合わない値は 2" "$(asw1 rc "$TB" recipe-run rs "note=$(printf 'x%.0s' $(seq 501))")" "2"
eq "recipe-run: 必須の引数が無いと 2" "$($TB recipe-set need <<<$'arg secs int:1-60\nstep wait-idle target=$target timeout=$secs' >/dev/null; asw1 rc "$TB" recipe-run need)" "2"
eq "recipe-run: 引数を渡せば部品に入る" "$(: >"$FAKE_LOG"; asw1 "$TB" recipe-run need secs=3 | tail -1; grep -c '^get w1' "$FAKE_LOG")" "w1 idle
1"
eq "recipe-run: 人の端末は --target が要る" "$(rc "$TB" recipe-run look)" "2"
eq "recipe-run: 人の端末から \$requester を使うレシピは 4" "$(rc "$TB" recipe-run rs --target w1)" "4"
eq "recipe-run: --target は agent 名だけ(シェルに渡さない)" "$(rc "$TB" recipe-run look --target "w1\$(touch $TMP/rc-pwn)") $(rc "$TB" recipe-run look --target human) $([[ -e $TMP/rc-pwn ]] && echo pwned || echo safe)" "2 2 safe"
eq "recipe-run: 引数の値もシェルに渡さない" "$(asw1 rc "$TB" recipe-run need "secs=\$(touch $TMP/rc-pwn2)"; [[ -e $TMP/rc-pwn2 ]] && echo pwned || echo safe)" "2
safe"

# --- 途中で失敗したら残りは続行しない
: >"$FAKE_LOG"
eq "recipe-run: 部品が失敗したら 1" "$(FAKE_STATUS=missing asw1 rc "$TB" recipe-run look)" "1"
eq "recipe-run: 失敗した後の step は実行しない" "$(gets)" "1"
eid=$(rsql "SELECT max(id) FROM task_events WHERE kind='recipe_run';")
eq "recipe-run: どこで止まったかを記録する" "$(rsql "SELECT new FROM task_events WHERE ref='E-$eid' AND kind='recipe_end';" | grep -c '^step 1 (agent-status) が失敗しました.*step 2 以降は実行していません')" "1"
eq "recipe-run: wait-idle は上限の秒で失敗する" "$($TB recipe-set slow <<<$'step wait-idle target=$target timeout=1' >/dev/null; FAKE_STATUS=working asw1 rc "$TB" recipe-run slow)" "1"

# --- チケットに結びつける
tid=$($TB add -p rcp -t 'レシピの記録')
eq "recipe-run: --task の履歴に残る" "$(asw1 "$TB" recipe-run look --task "$tid" >/dev/null; $TB history "$tid" | cut -f4 | grep -c '^recipe_')" "4"
eq "recipe-run: 無いチケットは 1" "$(asw1 rc "$TB" recipe-run look --task T-rcp-999)" "1"

# --- 登録の後でコードの部品が変わった(検査を通らなくなった)レシピは実行しない
rsql "INSERT INTO recipes (herdr_session, name, body, risk, steps, updated_by, updated_at) VALUES ('default','gone','step removed-part target=w1','low',1,'human','2026-01-01T00:00:00Z');"
eq "recipe-run: 今のコードで検査を通らないレシピは 4" "$(asw1 rc "$TB" recipe-run gone)" "4"
eq "recipe-list: 検査を通らないレシピは invalid" "$($TB recipe-list | awk -F'\t' '$1 == "gone" {print $2}')" "invalid!"

# --- 登録の検査をすり抜けた行(直接書かれた DB など)でも、依頼者・対象以外の agent には部品を向けない
rsql "INSERT INTO recipes (herdr_session, name, body, risk, steps, updated_by, updated_at) VALUES ('default','sneak','step agent-status target=lead','low',1,'human','2026-01-01T00:00:00Z');"
: >"$FAKE_LOG"
eq "recipe-run: 登録の検査を通らない行(リテラルの agent 名)は 4 で、何も読まない" "$(asw1 rc "$TB" recipe-run sneak) $(gets)" "4 0"

# --- 削除
eq "recipe-rm: agent の pane からは断る" "$(asw1 rc "$TB" recipe-rm look)" "1"
eq "recipe-rm: 人は消せる" "$($TB recipe-rm look)" "look removed"
eq "recipe-rm: 履歴に rm が残る" "$(rsql "SELECT how || ':' || (new_body IS NULL) FROM recipe_changes WHERE name='look' ORDER BY id DESC LIMIT 1;")" "rm:1"
eq "recipe-rm: 消したレシピは実行しない(4)" "$(asw1 rc "$TB" recipe-run look)" "4"
eq "recipe-rm: 無いレシピは 1" "$(rc "$TB" recipe-rm look)" "1"
eq "recipe: 表はこのセッションの行だけ(herdr_session が入る)" "$(rsql "SELECT count(*) FROM recipes WHERE herdr_session=''; SELECT count(*) FROM recipe_changes WHERE herdr_session='';" | paste -sd' ')" "0 0"

export TB_DB=$_saved_db
if [[ -n $_saved_appr ]]; then export TB_POLICY_A=$_saved_appr; else unset TB_POLICY_A; fi
if [[ -n $_saved_grants ]]; then export TB_POLICY_G=$_saved_grants; else unset TB_POLICY_G; fi
if [[ -n $_saved_agents ]]; then export FAKE_AGENTS=$_saved_agents; else unset FAKE_AGENTS; fi
[[ -z $_saved_pane ]] || export HERDR_PANE_ID=$_saved_pane
unset TB_RECIPE_POLL_SEC
