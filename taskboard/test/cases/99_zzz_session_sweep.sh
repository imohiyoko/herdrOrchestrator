# shellcheck shell=bash
# run.sh から番号順に source される(最後に流す)
# --- tb が herdr_session を書き忘れた行がないか: テストが作った全部の DB を見る(既定値の '' はどのセッションからも見えない)
sweep=""
while IFS= read -r -d '' f; do
  [[ $(sqlite3 "$f" "PRAGMA user_version;" 2>/dev/null) -ge 12 ]] 2>/dev/null || continue
  while IFS= read -r t; do
    n=$(sqlite3 "$f" "SELECT COUNT(*) FROM \"$t\" WHERE herdr_session IS NULL OR herdr_session='';" 2>/dev/null) || continue
    [[ $n == 0 ]] || sweep+="$(basename "$f"):$t=$n "
  done < <(sqlite3 "$f" "SELECT m.name FROM sqlite_master m WHERE m.type='table' AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session');" 2>/dev/null)
done < <(find "$TMP" -name '*.db' -type f -print0)
eq "sweep: every row written during the tests has its herdr_session" "${sweep:-none}" "none"
