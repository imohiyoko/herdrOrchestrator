-- 016: スプリント(期間で区切ったタスクの束)と見積りポイント。ボード(状態ごとの列)はスプリント単位で見る
CREATE TABLE IF NOT EXISTS sprints (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,   -- S-<id> として出す(DB 全体で一意)
  project       TEXT NOT NULL,
  name          TEXT NOT NULL,
  goal          TEXT,
  start_date    TEXT,                                -- YYYY-MM-DD(任意)
  end_date      TEXT,
  state         TEXT NOT NULL DEFAULT 'planned' CHECK (state IN ('planned','active','closed')),
  created_at    TEXT NOT NULL,
  started_at    TEXT,
  closed_at     TEXT,
  herdr_session TEXT NOT NULL DEFAULT ''
);
-- 1 つの project で active なスプリントは 1 つだけ
CREATE UNIQUE INDEX IF NOT EXISTS sprints_one_active ON sprints(herdr_session, project) WHERE state='active';
ALTER TABLE tasks ADD COLUMN sprint_id INTEGER;                                         -- NULL = バックログ
ALTER TABLE tasks ADD COLUMN points INTEGER CHECK (points IS NULL OR points >= 0);      -- 見積り(任意)
CREATE INDEX IF NOT EXISTS tasks_sprint ON tasks(herdr_session, sprint_id);
