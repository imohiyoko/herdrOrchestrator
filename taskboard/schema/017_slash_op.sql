-- 017: agent_ops に op=slash(tb slash が送った、または承認待ちのスラッシュコマンド。#94)を足す。CHECK 制約は作り直しでしか変えられない
-- 列・索引は 012(herdr_session)までの形をそのまま保つ
CREATE TABLE agent_ops_new (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  op            TEXT NOT NULL CHECK (op IN ('new','stop','revive','compact','slash')),
  target        TEXT NOT NULL,
  args          TEXT NOT NULL DEFAULT '{}',
  requested_by  TEXT NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('pending','done','failed','rejected')),
  result        TEXT,
  created_at    TEXT NOT NULL,
  decided_at    TEXT,
  herdr_session TEXT NOT NULL DEFAULT ''
);
INSERT INTO agent_ops_new (id, op, target, args, requested_by, status, result, created_at, decided_at, herdr_session)
  SELECT id, op, target, args, requested_by, status, result, created_at, decided_at, herdr_session FROM agent_ops;
DROP TABLE agent_ops;
ALTER TABLE agent_ops_new RENAME TO agent_ops;
CREATE INDEX IF NOT EXISTS agent_ops_status ON agent_ops(status);
CREATE INDEX IF NOT EXISTS agent_ops_session ON agent_ops(herdr_session, status);
