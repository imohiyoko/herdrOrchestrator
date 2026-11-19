# shellcheck shell=bash
# 019: 既存のアラート・提案の行をチケットへ移す(ADR 0003 の段 4)。018 までの DB に行を入れてから最新へ移行する
export TB_DB=$TMP/alert-migrate.db
_old=$TMP/schema-018; mkdir -p "$_old"
for f in "$HERE/../schema"/[0-9][0-9][0-9]_*.sql; do n=$((10#$(basename "$f" | cut -c1-3))); ((n <= 18)) && cp "$f" "$_old/"; done
TB_SCHEMA_DIR=$_old $TB list >/dev/null
sqlite3 "$TB_DB" "INSERT INTO tasks (id, project, title, created_at, updated_at, herdr_session) VALUES ('T-alert-001', 'alert', '手で作った同名 PJ のタスク', 't', 't', 'default');
  INSERT INTO alerts (herdr_session, fingerprint, severity, title, body, source, status, count, analyst, result_summary, first_seen, last_seen, analyzed_at, closed_at) VALUES
    ('default', 'f1', 'medium', '古いアラート', '本文1', 'watch1', 'closed', 3, 'analyst-A1', '誤報', 't1', 't2', 't3', 't4'),
    ('default', 'f2', NULL, '解析中', NULL, 'watch1', 'analyzing', 1, 'analyst-A2', NULL, 't5', 't5', NULL, NULL),
    ('other', 'f3', NULL, '別セッション', NULL, 'cli', 'awaiting-approval', 1, NULL, NULL, 't6', 't6', NULL, NULL);
  INSERT INTO proposals (herdr_session, role_file, title, text, status, by_name, note, created_at, resolved_at) VALUES
    ('default', 'watcher.md', '提案1', '本文', 'rejected', 'auditor', '不要', 't1', 't2');"
$TB list >/dev/null
eq "migrate 019: alerts and proposals are views now" "$(sqlite3 "$TB_DB" "SELECT group_concat(name||':'||type, ' ') FROM (SELECT name, type FROM sqlite_master WHERE name IN ('alerts','proposals') ORDER BY name);")" "alerts:view proposals:view"
eq "migrate 019: numbers kept, tickets after the existing ones" "$(sqlite3 "$TB_DB" "SELECT group_concat(id||'='||task_id, ' ') FROM (SELECT id, task_id FROM alerts ORDER BY id);")" "1=T-alert-002 2=T-alert-003 3=T-alert-004"
eq "migrate 019: stage → status, severity → priority, rows keep their session" \
  "$(sqlite3 "$TB_DB" "SELECT group_concat(t.status||'/'||t.priority||'/'||COALESCE(t.owner_agent,'-')||'/'||t.herdr_session, ' ') FROM (SELECT * FROM alert_details ORDER BY alert_no) d JOIN tasks t ON t.id=d.task_id;")" \
  "done/medium/-/default in_progress/high/analyst-A2/default todo/high/-/other"
eq "migrate 019: details kept (count, result, body)" "$(sqlite3 "$TB_DB" "SELECT count||' '||result_summary||' '||body FROM alerts WHERE id=1;")" "3 誤報 本文1"
eq "migrate 019: tb reads only this session's alerts" "$($TB alerts | cut -f1 | tr '\n' ' ')" "A-2 A-1 "
eq "migrate 019: proposal ticket" "$($TB show T-proposal-001 | jq -r '[.status, .summary, (.labels|join(","))] | join(" ")')" "done 見送り: 不要 proposal,rejected,role:watcher.md"
eq "migrate 019: the next alert continues the numbers" "$(HERDR_PANE_ID= $TB alert --title '新しい' | cut -d' ' -f1-2) $(sqlite3 "$TB_DB" "SELECT task_id FROM alerts WHERE id=4;")" "A-4 new T-alert-005"
