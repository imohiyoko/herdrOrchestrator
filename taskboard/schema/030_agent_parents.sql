-- 030: agent の親を複数持てるようにする(子 1 体に親が何体でも)。spawned_by は「立てた親」として残し(上限・深さ・記録)、
-- 今の親はこの表で持つ。親を足す・外すのは tb agent parent add / rm。子が orphan(tb agent-shed)になるのは、親が 1 体以上いて全員が動いていないときだけ。
CREATE TABLE IF NOT EXISTS agent_parents (
  herdr_session TEXT NOT NULL,
  child         TEXT NOT NULL,
  parent        TEXT NOT NULL,
  added_by      TEXT,
  added_at      TEXT NOT NULL,
  PRIMARY KEY (herdr_session, child, parent)
);
CREATE INDEX IF NOT EXISTS agent_parents_parent ON agent_parents(herdr_session, parent);
INSERT OR IGNORE INTO agent_parents (herdr_session, child, parent, added_by, added_at)
  SELECT herdr_session, name, spawned_by, created_by, COALESCE(created_at, '') FROM agents WHERE spawned_by IS NOT NULL AND spawned_by != '';
