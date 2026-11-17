# shellcheck shell=bash
# run.sh から番号順に source される(前のファイルが作った状態は使わない)
# --- DB は WAL(lib/db.sh の init_db)。全セッションが同じ DB を使うので、読みと書きが互いを待たないようにする
_saved_db=$TB_DB
export TB_DB=$TMP/wal.db
HERDR_SESSION=wa "$TB" list >/dev/null
eq "wal: 新しい DB は WAL で作られる" "$(sqlite3 "$TB_DB" "PRAGMA journal_mode;")" "wal"
# 既存の DB(delete モード)も、次の tb で WAL に切り替わる
sqlite3 "$TB_DB" "PRAGMA journal_mode=DELETE;" >/dev/null
HERDR_SESSION=wa "$TB" list >/dev/null
eq "wal: 既存の DB も次の tb で WAL に切り替わる" "$(sqlite3 "$TB_DB" "PRAGMA journal_mode;")" "wal"
# 読み取り専用で開く側(Web UI・tb config の読み)も WAL の DB を読める
eq "wal: 読み取り専用でも読める" "$(sqlite3 -readonly "$TB_DB" "SELECT COUNT(*) FROM tasks;")" "0"
export TB_DB=$_saved_db
