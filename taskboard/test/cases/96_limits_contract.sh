# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb limits の使い方・文面・終了コード・書く本文(期待値は消した bash 版の出力から作った。今はこれが約束)。DB は自分のものを使う
_lc_saved=$(declare -p HERDR_PANE_ID TB_DB TB_LIMITS TB_SPAWN_POLICY TB_ANALYST_MAX FAKE_AGENTS FAKE_WORKSPACES 2>/dev/null || true)
unset HERDR_PANE_ID TB_ANALYST_MAX TB_SPAWN_POLICY FAKE_WORKSPACES
export TB_DB=$TMP/lc.db TB_LIMITS=$TMP/lc-limits.conf; rm -f "$TB_LIMITS" "$TB_LIMITS.example"
lcag() { printf '{"pane_id":"%s","name":"%s","agent_status":"%s","agent_session":{"value":"s-%s"}}' "$2" "$1" "${3:-idle}" "$1"; }
export FAKE_AGENTS="[$(lcag c1 wC:p1 working),$(lcag w1 wW:p1 working),$(lcag w2 wW:p2)]"
lcerr() { "$@" 2>&1; echo "rc=$?"; }
lcpane() { HERDR_PANE_ID=wW:p1 "$@"; }

eq "limits: no args" "$(lcerr $TB limits)" $'tb: limits show|set|unset|mode|get|check|watcher-enabled\n(tb help で使い方)\nrc=2'
eq "limits: unknown sub" "$(lcerr $TB limits bogus)" $'tb: limits: 不明なサブコマンド bogus(show|set|unset|mode|get|check|watcher-enabled)\n(tb help で使い方)\nrc=2'
eq "get: no key" "$(lcerr $TB limits get)" $'tb: limits get <キー>\n(tb help で使い方)\nrc=2'
eq "get: bad key" "$(lcerr $TB limits get nope)" $'tb: 不明なキー: nope(使えるキー: max_running_agents max_working_agents incident_max_open incident_roles analyst_max spawn_max_per_parent spawn_max_total watchers human_handoff)\n(tb help で使い方)\nrc=2'
eq "get: defaults" "$(for k in max_running_agents max_working_agents incident_max_open incident_roles analyst_max spawn_max_per_parent spawn_max_total watchers human_handoff; do $TB limits get $k; done)" $'30\n0\n2\ninvestigator containment fixer lateral verifier\n2\n2\n6\nwatch\noff'
printf 'max_children_per_parent = 3\nmax_children_total = x\n' >"$TMP/lc-spawn.conf"
eq "get: spawn policy fallback" "$(TB_SPAWN_POLICY=$TMP/lc-spawn.conf $TB limits get spawn_max_per_parent; TB_SPAWN_POLICY=$TMP/lc-spawn.conf $TB limits get spawn_max_total)" $'3\n6'
eq "get: env wins" "$(TB_LIMIT_MAX_RUNNING_AGENTS=05 $TB limits get max_running_agents; TB_LIMIT_MAX_RUNNING_AGENTS=0 $TB limits get max_running_agents; TB_ANALYST_MAX=9 $TB limits get analyst_max)" $'5\n30\n9'

eq "set: wrong args" "$(lcerr $TB limits set max_running_agents)" $'tb: limits set <キー> <値>(キー: max_running_agents max_working_agents incident_max_open incident_roles analyst_max spawn_max_per_parent spawn_max_total watchers human_handoff。空にする値は \'\')\n(tb help で使い方)\nrc=2'
eq "set: bad value" "$(lcerr $TB limits set max_running_agents 0)" $'tb: limits set: max_running_agents は 1 以上の整数: 0\n(tb help で使い方)\nrc=2'
eq "set: bad role" "$(lcerr $TB limits set incident_roles boss)" $'tb: limits set: incident_roles に使えない役: boss(使えるのは investigator containment fixer lateral verifier)\n(tb help で使い方)\nrc=2'
eq "set: no roles" "$(lcerr $TB limits set incident_roles ' , ')" $'tb: limits set: incident_roles は 1 つ以上の役が要ります\n(tb help で使い方)\nrc=2'
eq "set: bad watcher" "$(lcerr $TB limits set watchers '-x')" $'tb: limits set: watchers に使えない名前: -x\n(tb help で使い方)\nrc=2'
eq "set: bad handoff" "$(lcerr $TB limits set human_handoff yes)" $'tb: limits set: human_handoff は on|off: yes\n(tb help で使い方)\nrc=2'
eq "set: bad key" "$(lcerr $TB limits set nope 1)" $'tb: limits set: 不明なキー: nope(使えるキー: max_running_agents max_working_agents incident_max_open incident_roles analyst_max spawn_max_per_parent spawn_max_total watchers human_handoff)\n(tb help で使い方)\nrc=2'
eq "set: agent pane" "$(lcpane lcerr $TB limits set max_running_agents 3)" $'tb: tb limits set は人が自分の端末で実行します(agent \'w1\' の pane からは実行できません)。記録 Q-1(例外は人が tb approval approve Q-1 で許可できます)\nrc=1'
eq "set: ok" "$(lcerr $TB limits set max_running_agents 007)" $'max_running_agents = 7\nrc=0'
eq "set: roles normalized" "$(lcerr $TB limits set incident_roles 'fixer,investigator fixer')" $'incident_roles = fixer investigator\nrc=0'
eq "set: empty watchers" "$(lcerr $TB limits set watchers '')" $'watchers = \nrc=0'
eq "set: body" "$(cat "$TB_LIMITS")" $'# tb limits が書いた制限の設定。編集するのは人だけ(項目は limits.conf.example)。環境変数 TB_LIMIT_<キー大文字> が優先。\nmax_running_agents = 7\nmode = custom\nincident_roles = fixer investigator\nwatchers = '
printf '# 手で書いた\nmax_running_agents = 9 # 前の値\nmax_running_agents = 10\nfoo = bar\n\nanalyst_max=3\n' >"$TB_LIMITS"
eq "set: rewrites the line, drops duplicates, keeps others" "$(lcerr $TB limits set max_running_agents 4; cat "$TB_LIMITS")" $'max_running_agents = 4\nrc=0\n# 手で書いた\nmax_running_agents = 4\nfoo = bar\n\nanalyst_max=3\nmode = custom'
eq "unset: no key" "$(lcerr $TB limits unset)" $'tb: limits unset <キー>\n(tb help で使い方)\nrc=2'
eq "unset: bad key" "$(lcerr $TB limits unset nope)" $'tb: 不明なキー: nope(使えるキー: max_running_agents max_working_agents incident_max_open incident_roles analyst_max spawn_max_per_parent spawn_max_total watchers human_handoff)\n(tb help で使い方)\nrc=2'
eq "unset: agent pane" "$(lcpane lcerr $TB limits unset analyst_max)" $'tb: tb limits unset は人が自分の端末で実行します(agent \'w1\' の pane からは実行できません)。記録 Q-2(例外は人が tb approval approve Q-2 で許可できます)\nrc=1'
eq "unset: ok" "$(lcerr $TB limits unset analyst_max; cat "$TB_LIMITS")" $'analyst_max unset(既定に戻る)\nrc=0\n# 手で書いた\nmax_running_agents = 4\nfoo = bar\n\nmode = custom'
eq "mode: no arg" "$(lcerr $TB limits mode)" $'tb: limits mode normal|lean|minimal\n(tb help で使い方)\nrc=2'
eq "mode: bad" "$(lcerr $TB limits mode fast)" $'tb: limits mode: normal|lean|minimal のどれか: fast\n(tb help で使い方)\nrc=2'
eq "mode: agent pane" "$(lcpane lcerr $TB limits mode lean)" $'tb: tb limits mode は人が自分の端末で実行します(agent \'w1\' の pane からは実行できません)。記録 Q-3(例外は人が tb approval approve Q-3 で許可できます)\nrc=1'
eq "mode: minimal" "$(lcerr $TB limits mode minimal; cat "$TB_LIMITS")" $'mode minimal\n  max_working_agents = 5\n  incident_max_open  = 1\n  incident_roles     = investigator\n  analyst_max        = 1\n  spawn_max_total    = 0\n  watchers           = \nrc=0\n# 手で書いた\nmax_running_agents = 4\nfoo = bar\n\nmode = minimal\nmax_working_agents = 5\nincident_max_open = 1\nincident_roles = investigator\nanalyst_max = 1\nspawn_max_total = 0\nwatchers = '
eq "mode: lean" "$(lcerr $TB limits mode lean; cat "$TB_LIMITS")" $'mode lean\n  max_working_agents = 10\n  incident_max_open  = 1\n  incident_roles     = investigator fixer verifier\n  analyst_max        = 1\n  spawn_max_total    = 2\n  watchers           = (既定)\nrc=0\n# 手で書いた\nmax_running_agents = 4\nfoo = bar\n\nmode = lean\nmax_working_agents = 10\nincident_max_open = 1\nincident_roles = investigator fixer verifier\nanalyst_max = 1\nspawn_max_total = 2'
eq "mode: normal" "$(lcerr $TB limits mode normal; cat "$TB_LIMITS")" $'mode normal\n  max_working_agents = (既定)\n  incident_max_open  = (既定)\n  incident_roles     = (既定)\n  analyst_max        = (既定)\n  spawn_max_total    = (既定)\n  watchers           = (既定)\nrc=0\n# 手で書いた\nmax_running_agents = 4\nfoo = bar\n\nmode = normal'
rm -f "$TB_LIMITS"; printf 'max_running_agents = 7\n' >"$TB_LIMITS.example"
eq "set: example is not the base" "$(lcerr $TB limits set incident_max_open 1; cat "$TB_LIMITS")" $'incident_max_open = 1\nrc=0\n# tb limits が書いた制限の設定。編集するのは人だけ(項目は limits.conf.example)。環境変数 TB_LIMIT_<キー大文字> が優先。\nincident_max_open = 1\nmode = custom'
rm -f "$TB_LIMITS.example"

eq "watcher-enabled: bad name" "$(lcerr $TB limits watcher-enabled)" $'tb: limits watcher-enabled <見張りの名前>\n(tb help で使い方)\nrc=2'
eq "watcher-enabled" "$($TB limits set watchers 'watch,ops' >/dev/null; lcerr $TB limits watcher-enabled ops; lcerr $TB limits watcher-enabled watc)" $'rc=0\nrc=1'

eq "show: bad arg" "$(lcerr $TB limits show --bogus)" $'tb: limits show: 不明な引数 --bogus\n(tb help で使い方)\nrc=2'
$TB limits set max_running_agents 3 >/dev/null; $TB limits set max_working_agents 1 >/dev/null
eq "show" "$(FAKE_WORKSPACES='[{"label":"inc-001"},{"label":"inc-01"},{"label":"x"}]' lcerr $TB limits show)" $'running 3/3  working 2/1  incidents 1/1  analysts 0/2  spawn 0/6  mode custom\n超過: working(新しく作る経路は拒否されます。止める候補: tb agent-shed)\n  max_running_agents     = 3   (conf)\n  max_working_agents     = 1   (conf)\n  incident_max_open      = 1   (conf)\n  incident_roles         = investigator containment fixer lateral verifier   (default)\n  analyst_max            = 2   (default)\n  spawn_max_per_parent   = 2   (default)\n  spawn_max_total        = 6   (default)\n  watchers               = watch ops   (conf)\n  human_handoff          = off   (default)\nrc=0'
eq "show --line" "$(lcerr $TB limits show --line)" $'running 3/3  working 2/1  incidents 0/1  analysts 0/2  spawn 0/6  mode custom\nrc=0'
eq "show: herdr fails" "$(FAKE_LIST_FAIL=1 FAKE_WS_LIST_FAIL=1 lcerr $TB limits show)" $'running 3/3  working 0/1  incidents ?/1  analysts 0/2  spawn 0/6  mode custom\n注意: herdr agent list に失敗したため、台帳の値で数えました\n  max_running_agents     = 3   (conf)\n  max_working_agents     = 1   (conf)\n  incident_max_open      = 1   (conf)\n  incident_roles         = investigator containment fixer lateral verifier   (default)\n  analyst_max            = 2   (default)\n  spawn_max_per_parent   = 2   (default)\n  spawn_max_total        = 6   (default)\n  watchers               = watch ops   (conf)\n  human_handoff          = off   (default)\nrc=0'
eq "show --json" "$(TB_LIMIT_ANALYST_MAX=4 FAKE_WORKSPACES='[{"label":"inc-002"}]' lcerr $TB limits show --json)" $'{"running":{"n":3,"max":3},"working":{"n":2,"max":1},"incidents":{"n":1,"max":1},"analysts":{"n":0,"max":4},"spawn":{"n":0,"max":6},"mode":"custom","over":["working"],"limits":{"max_running_agents":{"value":"3","source":"conf"},"max_working_agents":{"value":"1","source":"conf"},"incident_max_open":{"value":"1","source":"conf"},"incident_roles":{"value":"investigator containment fixer lateral verifier","source":"default"},"analyst_max":{"value":"4","source":"env"},"spawn_max_per_parent":{"value":"2","source":"default"},"spawn_max_total":{"value":"6","source":"default"},"watchers":{"value":"watch ops","source":"conf"},"human_handoff":{"value":"off","source":"default"}}}\nrc=0'
eq "show --json: herdr fails" "$(FAKE_WS_LIST_FAIL=1 $TB limits show --json | jq -c '.incidents')" $'{"n":null,"max":1}'

eq "check: no arg" "$(lcerr $TB limits check)" $'tb: limits check incident-open|running\n(tb help で使い方)\nrc=2'
eq "check: bad" "$(lcerr $TB limits check nope)" $'tb: limits check: incident-open|running のどれか: nope\n(tb help で使い方)\nrc=2'
eq "check running: at the limit" "$(lcerr $TB limits check running)" $'tb: 動作中の agent が上限です(動作中 3、上限 max_running_agents=3)。何も作りません。 止める候補は tb agent-shed、上限を変えるのは人(tb limits set max_running_agents <数>)\nrc=4'
eq "check running: over" "$($TB limits set max_running_agents 2 >/dev/null; lcerr $TB limits check running)" $'tb: 動作中の agent が上限です(動作中 3、上限 max_running_agents=2)。何も作りません。 止める候補は tb agent-shed、上限を変えるのは人(tb limits set max_running_agents <数>)\nrc=4'
eq "check running: herdr fails passes" "$(FAKE_LIST_FAIL=1 lcerr $TB limits check running)" $'ok\nrc=0'
eq "check incident-open: too many roles" "$(lcerr $TB limits check incident-open)" $'tb: 障害 workspace を開けません(動作中 3 + 起動する役 5 > 上限 max_running_agents=2)。何も作りません。 止める候補は tb agent-shed、起動する役を減らす・上限を変えるのは人(tb limits set incident_roles / max_running_agents)\nrc=4'
$TB limits set max_running_agents 30 >/dev/null
eq "check incident-open: ok" "$(lcerr $TB limits check incident-open)" $'ok(動作中 3 + 5 <= 30、障害 0 < 1)\nrc=0'
eq "check incident-open: open incidents" "$(FAKE_WORKSPACES='[{"label":"inc-001"},{"label":"inc-002"}]' lcerr $TB limits check incident-open)" $'tb: 障害 workspace を開けません(開いている障害 2、上限 incident_max_open=1)。何も作りません。上限を変えるのは人(tb limits set incident_max_open)\nrc=4'
eq "check incident-open: agent list fails" "$(FAKE_LIST_FAIL=1 lcerr $TB limits check incident-open)" $'tb: herdr agent list に失敗したため、動作中の agent の数を確かめられません\nrc=1'
eq "check incident-open: workspace list fails" "$(FAKE_WS_LIST_FAIL=1 lcerr $TB limits check incident-open)" $'tb: herdr workspace list に失敗したため、開いている障害の数を確かめられません\nrc=1'

unset TB_LIMITS
eq "db: set writes this session's row" "$(lcerr $TB limits set analyst_max 1; sqlite3 "$TB_DB" "SELECT herdr_session, body FROM config WHERE key='limits';"; sqlite3 "$TB_DB" "SELECT changed_by, how FROM config_changes WHERE key='limits' ORDER BY id DESC LIMIT 1;")" $'analyst_max = 1\nrc=0\ndefault|# tb limits が書いた制限の設定。編集するのは人だけ(項目は limits.conf.example)。環境変数 TB_LIMIT_<キー大文字> が優先。\nanalyst_max = 1\nmode = custom\nhuman|limits'
eq "db: show source" "$($TB limits show | grep -E 'analyst_max|mode')" $'running 3/30  working 2/無制限  incidents 0/2  analysts 0/1  spawn 0/6  mode custom\n  analyst_max            = 1   (conf)'

unset HERDR_PANE_ID TB_DB TB_LIMITS TB_SPAWN_POLICY TB_ANALYST_MAX FAKE_AGENTS FAKE_WORKSPACES
eval "$_lc_saved"
