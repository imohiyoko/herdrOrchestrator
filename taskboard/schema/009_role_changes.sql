-- 009: ロール変更の記録(tb role-set)。誰がいつ誰のロールを何から何に変えたか。関係グラフの「role」の辺にも使う
CREATE TABLE IF NOT EXISTS role_changes (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  agent       TEXT NOT NULL,
  old_role    TEXT,                 -- 変更前に効いていた役割(roles/<名前>.md / 台帳 / map.conf のどれか由来)
  new_role    TEXT NOT NULL,
  changed_by  TEXT NOT NULL,        -- human か、herdr から引いた agent 名(人事役)
  reason      TEXT,
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS role_changes_agent ON role_changes(agent);
