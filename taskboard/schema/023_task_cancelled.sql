-- 023: チケットの状態に cancelled(取り消し)を足す。done と同じく閉じた状態で、やらないと決めたチケットに使う。
-- SQLite は CHECK を後から変えられないので、tasks を作り直して行を移す(列・既定値・索引は 022 までと同じ)。
-- alerts / proposals のビューとトリガーは名前で tasks を指すだけなので、作り直した後もそのまま使える。
-- ただし今の ALTER TABLE ... RENAME はビューも読み直すので、tasks を消した時点の「無い表を指すビュー」で止まる。
-- 改名の間だけ legacy_alter_table にして、ビューとトリガーを触らせない(終わったら戻す)。
CREATE TABLE tasks_new (
  id           TEXT PRIMARY KEY,
  project      TEXT NOT NULL,
  title        TEXT NOT NULL,
  owner_agent  TEXT,
  session_id   TEXT,
  account      TEXT,
  status       TEXT NOT NULL DEFAULT 'todo'
               CHECK (status IN ('todo','in_progress','review','done','blocked','cancelled')),
  parent_id    TEXT,
  deliverables TEXT NOT NULL DEFAULT '[]',   -- JSON 配列
  summary      TEXT,                          -- done / cancelled 時の要約
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
