# shellcheck shell=bash
# 認可(ADR 0006 / 0007): 表 taskboard/perms/commands.tbl が全コマンドを覆っていること、入口と require_human が表の既定と設定 policy の行で決まること。
# project は pm だけを使う。DB と policy は $TMP/perms.* を使う
_saved_env=$(declare -p TB_DB TB_POLICY TB_POLICY_A HERDR_PANE_ID FAKE_AGENTS 2>/dev/null)
PM_ROOT=$(cd "$HERE/.." && pwd)
PM_TBL=$PM_ROOT/perms/commands.tbl

# ---------- 表が全コマンドを覆う(コマンドや require_human を足したら、表に行を足さないと落ちる)----------
pm_rows() { sed 's/#.*//' "$PM_TBL" | awk 'NF == 5'; }
pm_cmds=$( { grep -hoE '^cmd_[a-z][a-z0-9-]*\(\)' "$PM_ROOT"/lib/*.sh | sed 's/^cmd_//; s/()$//'; printf '%s\n' help whoami touch role-context; } | sort -u)
pm_words=$(pm_rows | awk '{split($1, a, "."); print a[1]}' | sort -u)
eq "perms: every tb command has a row in commands.tbl" "$(comm -23 <(printf '%s\n' "$pm_cmds") <(printf '%s\n' "$pm_words"))" ""
eq "perms: every command in commands.tbl exists" "$(comm -13 <(printf '%s\n' "$pm_cmds") <(printf '%s\n' "$pm_words"))" ""
eq "perms: every command has a one-word row (the fallback for other words)" \
  "$(comm -23 <(printf '%s\n' "$pm_cmds") <(pm_rows | awk '$1 !~ /\./ {print $1}' | sort -u))" ""
eq "perms: every non-comment line has exactly 5 columns" "$(sed 's/#.*//' "$PM_TBL" | awk 'NF != 0 && NF != 5')" ""
eq "perms: rows are well-formed (cmd:<service>:<verb>, known tiers, default >= floor, entry|inner|none)" \
  "$(pm_rows | awk 'function r(t){return t=="allow"?0:t=="ask-any"?1:t=="ask-human"?2:t=="deny"?3:-1}
    $2 !~ /^cmd:[a-z]+:[a-z][a-z-]*$/ || r($3) < 0 || r($4) < 0 || r($3) < r($4) || $5 !~ /^(entry|inner|none)$/ {print}')" ""
eq "perms: a path appears once" "$(pm_rows | awk '{print $1}' | sort | uniq -d)" ""
eq "perms: the same operation has the same default and floor on every row" \
  "$(pm_rows | awk '{k=$2; v=$3" "$4; if (k in seen && seen[k] != v) print k; seen[k]=v}')" ""
eq "perms: service names come from the naming skill" \
  "$(pm_rows | awk '{split($2, a, ":"); print a[2]}' | sort -u | grep -vxE 'ticket|team|notify|message|digest|agent|alert|review|proposal|pr|gate|approval|recipe|conv|reminder|job|proxy|activity|handoff|config|policy')" ""

# require_human の文面("tb <語...>")は、表の inner の行に当たる。$変数 は * とみなす(例: tb config $sub → config.*)
pm_labels=$(grep -rhoE 'require_human "tb [^"]+"' "$PM_ROOT"/lib/*.sh | sed -E 's/^require_human "tb //; s/"$//' | sort -u)
pm_inner=$(pm_rows | awk '$5 == "inner" {print $1}' | sort -u)
pm_bad=""; pm_used=""
while IFS= read -r l; do
  [[ -n $l ]] || continue
  pat=${l// /.}; pat=$(sed -E 's/\$\{?[A-Za-z_][A-Za-z0-9_]*\}?/*/g' <<<"$pat")
  hit=0
  while IFS= read -r p; do
    # shellcheck disable=SC2053  # glob として比べる
    [[ $p == $pat ]] && { hit=1; pm_used+="$p"$'\n'; }
  done <<<"$pm_inner"
  ((hit)) || pm_bad+="tb $l"$'\n'
done <<<"$pm_labels"
eq "perms: every require_human label has an inner row" "$pm_bad" ""
# 使われない inner の行を残さない。同じ操作名の別名の行(例: pr.queue.approve と pr-queue.approve)は、どれかが当たればよい
pm_used_ops=$(printf '%s' "$pm_used" | sort -u | while IFS= read -r p; do pm_rows | awk -v p="$p" '$1 == p {print $2}'; done | sort -u)
eq "perms: every inner operation is reached by a require_human label (bash)" \
  "$(pm_rows | awk '$5 == "inner" {print $2}' | sort -u | comm -23 - <(printf '%s\n' "$pm_used_ops"))" ""
eq "perms: the bash table is the one herdrctl embeds (no copy)" "$(ls "$PM_ROOT/perms")" "$(printf 'commands.tbl\nembed.go\nops.tbl')"

# ---------- 振る舞い(設定 policy。ADR 0007)----------
export TB_DB=$TMP/perms.db TB_POLICY=$TMP/perms.policy
export FAKE_AGENTS='[{"pane_id":"wW:p1","name":"pw1","agent_session":{"value":"s1"}},{"pane_id":"wW:p2","name":"pw2","agent_session":{"value":"s2"}}]'
: >"$TB_POLICY"
unset HERDR_PANE_ID
t1=$($TB add -p pm -t "権限 1"); t2=$($TB add -p pm -t "権限 2"); t3=$($TB add -p pm -t "権限 3")
export HERDR_PANE_ID=wW:p1
eq "perms: agent runs an allow command (default)" "$(rc $TB cancel "$t1")" "0"
eq "perms: default allow does not ask herdr who is calling (no extra herdr call)" "$(: >"$FAKE_LOG"; $TB list -p pm >/dev/null; grep -c '^list' "$FAKE_LOG" || true)" "0"

printf 'pw1 cmd:ticket:cancel * = ask-human\n' >"$TB_POLICY"
eq "perms: tightened to ask-human for pw1 → agent stops with 20" "$(rc $TB cancel "$t2")" "20"
eq "perms: the old name and the service name share one operation" "$(rc $TB ticket cancel "$t2")" "20"
eq "perms: the message names the operation and the row" \
  "$($TB cancel "$t2" 2>&1 >/dev/null | grep -c 'cmd:ticket:cancel = ask-human。policy: pw1 cmd:ticket:cancel \* = ask-human')" "1"
eq "perms: nothing was done" "$($TB show "$t2" | jq -r .status)" "todo"
eq "perms: a row for pw1 does not touch pw2" "$(HERDR_PANE_ID=wW:p2 rc $TB cancel "$t3")" "0"
eq "perms: a human terminal is not stopped" "$(env -u HERDR_PANE_ID bash -c "$TB cancel $t2 >/dev/null 2>&1; echo \$?")" "0"

printf '* cmd:ticket:* * = deny\n' >"$TB_POLICY"
eq "perms: a glob row applies (deny → 1)" "$(rc $TB show "$t1")" "1"
eq "perms: a pane with no agent is a human (rows do not apply)" "$(HERDR_PANE_ID=wW:p9 rc $TB show "$t1")" "0"
eq "perms: the gate also covers commands moved to herdrctl (tb list)" "$($TB list -p pm 2>&1 >/dev/null)" \
  "tb: tb list は実行できません(cmd:ticket:list = deny。policy: * cmd:ticket:* * = deny)(agent 'pw1')"
printf 'pw* cmd:ticket:show * = allow\npw1 cmd:ticket:show * = ask-any\n' >"$TB_POLICY"
eq "perms: the strictest matching row wins, whatever the order (→ 20)" "$(rc $TB show "$t1")" "20"
printf 'pw1 cmd:ticket:show * = ask-any\npw* cmd:ticket:show * = allow\n' >"$TB_POLICY"
eq "perms: the strictest matching row wins in the other order too" "$(rc $TB show "$t1")" "20"
printf 'pw1 cmd:ticket:show * = nonsense\npw1 cmd:ticket:show = deny\n' >"$TB_POLICY"
eq "perms: rows with an unknown effect or without 3 columns are ignored (default allow)" "$(rc $TB show "$t1")" "0"
printf 'human cmd:ticket:show * = deny\n' >"$TB_POLICY"
eq "perms: rows for human do not apply (agent default allow)" "$(rc $TB show "$t1")" "0"
eq "perms: rows for human do not stop a human" "$(env -u HERDR_PANE_ID bash -c "$TB show $t1 >/dev/null 2>&1; echo \$?")" "0"
eq "perms: config set refuses a human row" "$(printf 'human cmd:ticket:show * = deny\n' | env -u HERDR_PANE_ID $TB config set policy 2>&1 >/dev/null | grep -c '主体 human の行は効きません')" "1"
eq "perms: config set refuses an unknown effect" "$(printf 'pw1 cmd:ticket:show * = maybe\n' | env -u HERDR_PANE_ID $TB config set policy 2>&1 >/dev/null | grep -c '効果は allow / ask-any / ask-human / deny')" "1"
if [[ $(id -u) != 0 ]]; then
  printf 'pw1 cmd:ticket:show * = allow\n' >"$TB_POLICY"; chmod 000 "$TB_POLICY"
  eq "perms: a policy file that cannot be read → deny (not the default)" "$(rc $TB show "$t1")" "1"
  chmod 644 "$TB_POLICY"
  # 置き場のディレクトリの中を見られない(ファイルがあるか確かめられない)のも、設定が無いのとは分ける
  mkdir -p "$TMP/perms-noexec"; _pm_pol=$TB_POLICY; export TB_POLICY=$TMP/perms-noexec/policy; chmod 000 "$TMP/perms-noexec"
  eq "perms: a policy dir that cannot be searched → deny (not the default)" "$(rc $TB show "$t1")" "1"
  chmod 755 "$TMP/perms-noexec"
  # さらに上のディレクトリの中を見られないときも同じ
  mkdir -p "$TMP/perms-noexec/a/b"; export TB_POLICY=$TMP/perms-noexec/a/b/policy; chmod 000 "$TMP/perms-noexec/a"
  eq "perms: an ancestor dir that cannot be searched → deny" "$(rc $TB show "$t1")" "1"
  chmod 755 "$TMP/perms-noexec/a"
  export TB_POLICY=$TMP/perms-noexec/none/policy
  eq "perms: a missing policy file (dirs searchable) is no config → default allow" "$(rc $TB show "$t1")" "0"
  export TB_POLICY=$_pm_pol
fi

# 下限は今はすべて allow(表の下限の列で守りを足せる。下限で丸める処理は internal/kernel/perms のテストで確かめる)
eq "perms: every floor in the table is allow (the human tunes tiers by config)" "$(pm_rows | awk '$4 != "allow"')" ""
# require_human の中の確かめも表と設定で決まる。deny の既定は設定で緩められる
printf '@nobody-role cmd:ticket:resolve * = allow\npw1 cmd:ticket:resolve * = ask-human\n' >"$TB_POLICY"
eq "perms: a deny default loosened to ask-human → 20 instead of 1" "$(rc $TB resolve C-1)" "20"
eq "perms: reopen is a separate operation (still deny → 1)" "$(rc $TB reopen C-1)" "1"
printf 'pw1 cmd:ticket:resolve * = allow\n' >"$TB_POLICY"
eq "perms: a deny default loosened to allow → the command itself runs (no gate message)" \
  "$($TB resolve C-1 2>&1 | grep -cE '人が自分の端末で|承認が要ります|実行できません' || true)" "0"
: >"$TB_POLICY"
eq "perms: default require_human is unchanged (1 and the old message)" "$($TB resolve C-1 2>&1 >/dev/null)" \
  "tb: tb resolve は人が自分の端末で実行します(agent 'pw1' の pane からは実行できません)"
eq "perms: --dry-run paths that skip require_human still run (nudge --dry-run)" "$(rc $TB nudge --dry-run)" "0"
eq "perms: herdr failure while checking → 1" "$(FAKE_LIST_FAIL=1 rc $TB resolve C-1)" "1"
printf 'pw1 cmd:ticket:show * = allow\n' >"$TB_POLICY"
eq "perms: herdr failure while matching a row → 1" "$(FAKE_LIST_FAIL=1 rc $TB show "$t1")" "1"
eq "perms: approvals cmd: rows no longer apply (moved to policy)" "$(: >"$TB_POLICY"; printf 'cmd:ticket:show = only-human\n' >"$TMP/perms.approvals"; TB_POLICY_A=$TMP/perms.approvals rc $TB show "$t1")" "0"

# DB の設定を読めない(問い合わせが失敗する)ときは、設定が無いのとは分けて deny(fail-closed)。表が無い・行が無いのは既定のまま
unset TB_POLICY HERDR_PANE_ID; export TB_DB=$TMP/perms-dberr.db
t4=$($TB add -p pm -t "権限 4")
export HERDR_PANE_ID=wW:p1
eq "perms: no policy row in the DB → default allow" "$(rc $TB list -p pm)" "0"
# 読むと実行時に失敗する config(整数のあふれ)に差し替える
sqlite3 "$TB_DB" "ALTER TABLE config RENAME TO config_real; CREATE VIEW config AS SELECT * FROM config_real WHERE abs(-9223372036854775808) IS NOT NULL;"
eq "perms: a failed DB read of policy → deny (not the default)" "$(rc $TB cancel "$t4")" "1"
eq "perms: a failed DB read also stops commands moved to herdrctl (tb list)" "$(rc $TB list -p pm)" "1"
eq "perms: nothing was done" "$(env -u HERDR_PANE_ID $TB show "$t4" | jq -r .status)" "todo"
# config が参照する別の表が無いのも失敗(config そのものが無いときだけ「設定が無い」)
sqlite3 "$TB_DB" "DROP VIEW config; CREATE TABLE config_gone (key TEXT, herdr_session TEXT, body TEXT); CREATE VIEW config AS SELECT * FROM config_gone; DROP TABLE config_gone;"
eq "perms: a config view over a missing table → deny" "$(rc $TB cancel "$t4")" "1"
sqlite3 "$TB_DB" "DROP VIEW config; CREATE TABLE \"config-gone\" (key TEXT, herdr_session TEXT, body TEXT); CREATE VIEW config AS SELECT * FROM \"config-gone\"; DROP TABLE \"config-gone\";"
eq "perms: a config view over a missing table named config-… → deny" "$(rc $TB cancel "$t4")" "1"
sqlite3 "$TB_DB" "DROP VIEW config; CREATE TABLE \"config gone\" (key TEXT, herdr_session TEXT, body TEXT); CREATE VIEW config AS SELECT * FROM \"config gone\"; DROP TABLE \"config gone\";"
eq "perms: a config view over a missing table named \"config gone\" → deny" "$(rc $TB cancel "$t4")" "1"
export TB_DB=$TMP/perms.db TB_POLICY=$TMP/perms.policy

# 古い herdrctl(権限段階の門が無い)には渡さない
mkdir -p "$TMP/perms-oldhc"; printf '#!/bin/sh\n[ "$2" = --commands ] && echo list && exit 0\nexit 2\n' >"$TMP/perms-oldhc/herdrctl"; chmod +x "$TMP/perms-oldhc/herdrctl"
eq "perms: TB_IMPL=go refuses an herdrctl without the perms feature" "$(TB_IMPL=go TB_HERDRCTL=$TMP/perms-oldhc/herdrctl $TB list -p pm 2>&1 >/dev/null | grep -c '認可と呼び出し元(ADR 0007)を確かめられない')" "1"
# TB_IMPL が未設定なら: 使える herdrctl があれば Go、無い・古いなら何も言わずに bash
mkdir -p "$TMP/perms-newhc"; printf '#!/bin/sh\ncase "$2" in --features) echo "perms policy policy-ops auth";; --commands) echo list;; list) echo from-go;; esac\nexit 0\n' >"$TMP/perms-newhc/herdrctl"; chmod +x "$TMP/perms-newhc/herdrctl"
eq "impl: unset TB_IMPL with a usable herdrctl -> Go" "$(TB_IMPL= TB_HERDRCTL=$TMP/perms-newhc/herdrctl $TB list -p pm 2>/dev/null)" "from-go"
eq "impl: unset TB_IMPL with an old herdrctl -> bash, no error" "$(TB_IMPL= TB_HERDRCTL=$TMP/perms-oldhc/herdrctl $TB list -p pm 2>&1 >/dev/null | wc -l | tr -d ' '; TB_IMPL= TB_HERDRCTL=$TMP/perms-oldhc/herdrctl rc $TB list -p pm)" "$(printf '0\n0')"
eq "impl: unset TB_IMPL without herdrctl -> bash, no error" "$(TB_IMPL= TB_HERDRCTL=$TMP/no-such-herdrctl rc $TB list -p pm)" "0"
eq "impl: TB_IMPL=bash ignores a usable herdrctl" "$(TB_IMPL=bash TB_HERDRCTL=$TMP/perms-newhc/herdrctl $TB list -p pm 2>/dev/null | grep -c from-go)" "0"

unset TB_DB TB_POLICY TB_POLICY_A HERDR_PANE_ID FAKE_AGENTS
eval "$_saved_env"
