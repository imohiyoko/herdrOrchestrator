# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- tb の sql(): このセッションの一時ビューと、別セッションの行への書き込みの見張り(internal/kernel/store)
_saved_hs=${HERDR_SESSION-}; unset HERDR_SESSION
export TB_DB=$TMP/guard.db TB_CONFIG=$TMP/guard.conf
printf 'gd=conductor\n' >"$TB_CONFIG"
ga=$(HERDR_SESSION=ga "$TB" add -p gd -t 'task of ga'); gb=$(HERDR_SESSION=gb "$TB" add -p gd -t 'task of gb')
# 別セッションの行への書き込みの見張り・ビューそのものは internal/kernel/store の TestSessionViewsAndGuards

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
