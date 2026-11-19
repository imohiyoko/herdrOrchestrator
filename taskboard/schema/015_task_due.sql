-- 015: タスクの期日(due)。日付だけ(YYYY-MM-DD)・既定なし。既存の行は NULL のまま(期日なし = 期限切れにも間近にもならない)。
-- 期限切れ / 期限間近は tb list --overdue / --due-within N と Web UI のタスク一覧で見る。「今日」は tb を動かす端末の地方時の日付。
ALTER TABLE tasks ADD COLUMN due TEXT
  CHECK (due IS NULL OR (due GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(due) IS due));
CREATE INDEX IF NOT EXISTS tasks_due ON tasks(herdr_session, due) WHERE due IS NOT NULL;
