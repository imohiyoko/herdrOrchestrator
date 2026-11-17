-- 012: 1 つの DB を複数の herdr セッションで共有する(セッションは herdr_session 列で分ける)。
-- これまでは 1 セッション = 1 DB だったので、既にある行は「この移行を流したセッション」のものとして埋める(@SESSION@ は tb が差し込む)。
-- 新しい行は tb が必ず herdr_session を書く(既定値の '' は「書き忘れ」で、どのセッションからも見えない。テストで検出する)。
-- 名前やキーが一意だった表(agents / observations / tb_flags / notify_seen)は、セッションとの組で一意にする(作り直して行を移す)。

ALTER TABLE tasks ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE tasks SET herdr_session=@SESSION@;
ALTER TABLE proxy_requests ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE proxy_requests SET herdr_session=@SESSION@;
ALTER TABLE comments ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE comments SET herdr_session=@SESSION@;
ALTER TABLE agent_activity ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE agent_activity SET herdr_session=@SESSION@;
ALTER TABLE messages ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE messages SET herdr_session=@SESSION@;
ALTER TABLE outbox ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE outbox SET herdr_session=@SESSION@;
ALTER TABLE approval_changes ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE approval_changes SET herdr_session=@SESSION@;
ALTER TABLE role_briefings ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE role_briefings SET herdr_session=@SESSION@;
ALTER TABLE alerts ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE alerts SET herdr_session=@SESSION@;
ALTER TABLE proposals ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE proposals SET herdr_session=@SESSION@;
ALTER TABLE reviews ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE reviews SET herdr_session=@SESSION@;
ALTER TABLE digests ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE digests SET herdr_session=@SESSION@;
ALTER TABLE agent_ops ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE agent_ops SET herdr_session=@SESSION@;
ALTER TABLE notify_events ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE notify_events SET herdr_session=@SESSION@;
ALTER TABLE notify_sends ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE notify_sends SET herdr_session=@SESSION@;
ALTER TABLE pr_queue ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE pr_queue SET herdr_session=@SESSION@;
ALTER TABLE read_log ADD COLUMN herdr_session TEXT NOT NULL DEFAULT '';
UPDATE read_log SET herdr_session=@SESSION@;
UPDATE role_changes SET herdr_session=@SESSION@ WHERE herdr_session IS NULL OR herdr_session='';

CREATE TABLE agents_new (
  herdr_session    TEXT NOT NULL,
  name             TEXT NOT NULL,
  kind             TEXT NOT NULL DEFAULT 'claude',
  cwd              TEXT,
  model            TEXT,
  role             TEXT,
  session_id       TEXT,
  pane_id          TEXT,
  state            TEXT NOT NULL CHECK (state IN ('running','stopped')),
  killable         TEXT NOT NULL DEFAULT 'unknown' CHECK (killable IN ('yes','no','unknown')),
  killable_reason  TEXT,
  killable_by      TEXT,
  killable_at      TEXT,
  killable_session TEXT,
  created_by       TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  stopped_at       TEXT,
  spawned_by       TEXT,
  spawn_task       TEXT,
  PRIMARY KEY (herdr_session, name)
);
INSERT INTO agents_new (herdr_session, name, kind, cwd, model, role, session_id, pane_id, state, killable, killable_reason, killable_by,
                        killable_at, killable_session, created_by, created_at, updated_at, stopped_at, spawned_by, spawn_task)
  SELECT COALESCE(NULLIF(herdr_session,''), 'default'), name, kind, cwd, model, role, session_id, pane_id, state, killable, killable_reason, killable_by,
         killable_at, killable_session, created_by, created_at, updated_at, stopped_at, spawned_by, spawn_task FROM agents;
DROP TABLE agents;
ALTER TABLE agents_new RENAME TO agents;
CREATE INDEX IF NOT EXISTS agents_spawned_by ON agents(herdr_session, spawned_by);

CREATE TABLE observations_new (
  herdr_session TEXT NOT NULL,
  key        TEXT NOT NULL,
  kind       TEXT NOT NULL,
  task_id    TEXT,
  text       TEXT NOT NULL,
  digest_id  INTEGER,
  created_at TEXT NOT NULL,
  PRIMARY KEY (herdr_session, key)
);
INSERT INTO observations_new (herdr_session, key, kind, task_id, text, digest_id, created_at)
  SELECT @SESSION@, key, kind, task_id, text, digest_id, created_at FROM observations;
DROP TABLE observations;
ALTER TABLE observations_new RENAME TO observations;

CREATE TABLE tb_flags_new (
  herdr_session TEXT NOT NULL,
  key    TEXT NOT NULL,
  value  TEXT NOT NULL,
  set_by TEXT,
  set_at TEXT NOT NULL,
  PRIMARY KEY (herdr_session, key)
);
INSERT INTO tb_flags_new (herdr_session, key, value, set_by, set_at) SELECT @SESSION@, key, value, set_by, set_at FROM tb_flags;
DROP TABLE tb_flags;
ALTER TABLE tb_flags_new RENAME TO tb_flags;

CREATE TABLE notify_seen_new (
  herdr_session TEXT NOT NULL,
  key        TEXT NOT NULL,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (herdr_session, key)
);
INSERT INTO notify_seen_new (herdr_session, key, value, updated_at) SELECT @SESSION@, key, value, updated_at FROM notify_seen;
DROP TABLE notify_seen;
ALTER TABLE notify_seen_new RENAME TO notify_seen;

CREATE INDEX IF NOT EXISTS tasks_session ON tasks(herdr_session, project, status);
CREATE INDEX IF NOT EXISTS comments_session ON comments(herdr_session, delivered_at);
CREATE INDEX IF NOT EXISTS outbox_session ON outbox(herdr_session, status);
CREATE INDEX IF NOT EXISTS messages_session ON messages(herdr_session, created_at);
CREATE INDEX IF NOT EXISTS alerts_session ON alerts(herdr_session, status);
CREATE INDEX IF NOT EXISTS notify_events_session ON notify_events(herdr_session, slack_status);
CREATE INDEX IF NOT EXISTS agent_ops_session ON agent_ops(herdr_session, status);
