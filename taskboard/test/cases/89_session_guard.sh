# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb の sql(): このセッションの一時ビューと、別セッションの行への書き込みの見張り(lib/db.sh の session_setup)
_saved_hs=${HERDR_SESSION-}; unset HERDR_SESSION
export TB_DB=$TMP/guard.db TB_CONFIG=$TMP/guard.conf
printf 'gd=conductor\n' >"$TB_CONFIG"
ga=$(HERDR_SESSION=ga "$TB" add -p gd -t 'task of ga'); gb=$(HERDR_SESSION=gb "$TB" add -p gd -t 'task of gb')
# tb の DB 層だけを読み込んで、セッション ga として SQL を流す(絞り忘れのある SQL を試すため)
gsql() { HERDR_SESSION=ga TB_ROOT="$HERE/.." DB=$TB_DB bash -c 'set -euo pipefail; source "$TB_ROOT/../lib/herdr-env.sh"; source "$TB_ROOT/lib/db.sh"; init_db; sql "$1"' _ "$1" 2>&1; }

eq "guard: 表の名前のまま読むと、このセッションの行だけ" "$(gsql "SELECT id FROM tasks ORDER BY id;" | tr '\n' ' ')" "$ga "
eq "guard: main.<表> と書けば全セッションの行" "$(gsql "SELECT id FROM main.tasks ORDER BY id;" | tr '\n' ' ')" "$ga $gb "
eq "guard: 絞り忘れの UPDATE は別セッションの行に触れた時点で止まり、何も変えない" \
  "$(gsql "UPDATE main.tasks SET title='x';" | grep -c '別の herdr セッションの行は書き換えられません(tasks)') $(sqlite3 "$TB_DB" "SELECT title FROM tasks ORDER BY id;" | tr '\n' '|')" \
  "1 task of ga|task of gb|"
eq "guard: 絞り忘れの DELETE も止まる" "$(gsql "DELETE FROM main.tasks;" | grep -c '書き換えられません') $(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM tasks;")" "1 2"
eq "guard: 別セッションの行は足せない" "$(gsql "INSERT INTO main.tasks (id, project, title, status, created_at, updated_at, herdr_session) VALUES ('T-gd-009', 'gd', 'x', 'todo', 'x', 'x', 'gb');" | grep -c '書き換えられません(tasks)') $(sqlite3 "$TB_DB" "SELECT COUNT(*) FROM tasks;")" "1 2"
eq "guard: 行を別セッションへ移す UPDATE も止まる" "$(gsql "UPDATE main.tasks SET herdr_session='gb' WHERE id='$ga';" | grep -c '書き換えられません') $(sqlite3 "$TB_DB" "SELECT herdr_session FROM tasks WHERE id='$ga';")" "1 ga"
eq "guard: このセッションの行は書ける" "$(gsql "UPDATE main.tasks SET title='ok' WHERE herdr_session='ga'; SELECT changes();")" "1"
eq "guard: ビュー(表の名前のまま)には書けない(main. の付け忘れが分かる)" "$(gsql "UPDATE tasks SET title='y';" | grep -c 'cannot modify tasks because it is a view')" "1"
eq "guard: 設定は共通の行('*')も書ける" "$(gsql "INSERT INTO main.config (herdr_session, key, body, updated_by, updated_at) VALUES ('*', 'limits', 'x', 'h', 'x'); SELECT COUNT(*) FROM main.config WHERE herdr_session='*';")" "1"
eq "guard: 設定でも別セッションの行は書けない" "$(gsql "INSERT INTO main.config (herdr_session, key, body, updated_by, updated_at) VALUES ('gb', 'limits', 'x', 'h', 'x');" | grep -c '書き換えられません(config)')" "1"

# 一覧(taskboard/sql/session_tables)は、スキーマで herdr_session 列を持つ表とちょうど同じ
want=$(sqlite3 "$TB_DB" "SELECT m.name FROM sqlite_master m WHERE m.type='table' AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session') ORDER BY 1;" | tr '\n' ' ')
eq "guard: session_tables はスキーマの herdr_session 列を持つ表と一致" "$(grep -v '^#' "$HERE/../sql/session_tables" | sort | tr '\n' ' ')" "$want"

# 活動記録は session_id で DB 全体で一意: 別の herdr セッションで同じ会話が再開されたら、行ごとそのセッションへ移る(従来どおり)
HERDR_SESSION=ga "$TB" touch --event UserPromptSubmit --session-id g-act --cwd /x
HERDR_SESSION=gb "$TB" touch --event Stop --session-id g-act
eq "guard: 活動記録は再開したセッションへ移る" "$(sqlite3 "$TB_DB" "SELECT herdr_session||':'||cwd FROM agent_activity WHERE session_id='g-act';")" "gb:/x"
# 壊れた・足りない表があっても、ある表だけで動く(ビューと見張りは今ある表にだけ作る)
sqlite3 "$TB_DB" "ALTER TABLE alerts RENAME TO alerts_x;"
eq "guard: 表が欠けていても、ほかの表は読める" "$(HERDR_SESSION=ga "$TB" list | cut -f1)" "$ga"
sqlite3 "$TB_DB" "ALTER TABLE alerts_x RENAME TO alerts;"

if [[ -n $_saved_hs ]]; then export HERDR_SESSION=$_saved_hs; fi
