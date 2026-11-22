-- 024: チケットの状態を設定(config の statuses。taskboard/lib/core.sh)で足したり外したりできるように、status の値の CHECK を外す。
-- 値の一覧と分類(todo / active / review / blocked / done / cancelled)は tb と Web UI が設定から読み、書く前に確かめる。
-- DB には「空でなく 30 文字まで」だけを残す。既定値 'todo' と列・索引は 023 と同じ。作り直しの手順も 023 と同じ。
CREATE TABLE tasks_new (
  id           TEXT PRIMARY KEY,
  project      TEXT NOT NULL,
  title        TEXT NOT NULL,
  owner_agent  TEXT,
  session_id   TEXT,
  account      TEXT,
  status       TEXT NOT NULL DEFAULT 'todo'
               CHECK (length(status) BETWEEN 1 AND 30),   -- 値の一覧は設定 statuses(tb が書く前に確かめる)
  parent_id    TEXT,
  deliverables TEXT NOT NULL DEFAULT '[]',   -- JSON 配列
  summary      TEXT,                          -- 閉じた(done / cancelled の分類)時の要約
  note         TEXT,                          -- block 理由など
  reported_at  TEXT,                          -- report 成功時のみ埋まる
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  herdr_session TEXT NOT NULL DEFAULT '',
  type         TEXT NOT NULL DEFAULT 'task' CHECK (type IN ('epic','story','task','bug','subtask')),
  priority     TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('highest','high','medium','low','lowest')),
  labels       TEXT NOT NULL DEFAULT '[]',
  description  TEXT,
  refs         TEXT NOT NULL DEFAULT '[]',
  due          TEXT CHECK (due IS NULL OR (due GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(due) IS due)),
  sprint_id    INTEGER,
  points       INTEGER CHECK (points IS NULL OR points >= 0)
);
INSERT INTO tasks_new (id, project, title, owner_agent, session_id, account, status, parent_id, deliverables, summary, note, reported_at,
                       created_at, updated_at, herdr_session, type, priority, labels, description, refs, due, sprint_id, points)
  SELECT id, project, title, owner_agent, session_id, account, status, parent_id, deliverables, summary, note, reported_at,
         created_at, updated_at, herdr_session, type, priority, labels, description, refs, due, sprint_id, points
  FROM tasks ORDER BY rowid;
DROP TABLE tasks;
PRAGMA legacy_alter_table=ON;
ALTER TABLE tasks_new RENAME TO tasks;
PRAGMA legacy_alter_table=OFF;
CREATE INDEX tasks_project_status ON tasks(project, status);
CREATE INDEX tasks_session ON tasks(herdr_session, project, status);
CREATE INDEX tasks_due ON tasks(herdr_session, due) WHERE due IS NOT NULL;
CREATE INDEX tasks_sprint ON tasks(herdr_session, sprint_id);
