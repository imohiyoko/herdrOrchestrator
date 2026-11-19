-- 014: チケットを Jira 風にする(種別・優先度・ラベル・説明・外部リンク・タスク間のリンク)。
-- 状態(todo / in_progress / review / done / blocked)と遷移は変えない(役割・gate・digest がこれに頼っている)。
-- 既存の行は 種別 task・優先度 medium・ラベルなし になる。
ALTER TABLE tasks ADD COLUMN type TEXT NOT NULL DEFAULT 'task'
  CHECK (type IN ('epic','story','task','bug','subtask'));
ALTER TABLE tasks ADD COLUMN priority TEXT NOT NULL DEFAULT 'medium'
  CHECK (priority IN ('highest','high','medium','low','lowest'));
ALTER TABLE tasks ADD COLUMN labels TEXT NOT NULL DEFAULT '[]';   -- JSON 配列(文字列)。重複なし・並びは辞書順
ALTER TABLE tasks ADD COLUMN description TEXT;                     -- 本文・受け入れ条件
ALTER TABLE tasks ADD COLUMN refs TEXT NOT NULL DEFAULT '[]';     -- 外部リンク。JSON 配列 [{"kind":"slack","url":"https://..."}]。url で重複なし
-- タスク間のリンク。from が to を blocks なら、to は from が done になるまで claim できない
CREATE TABLE IF NOT EXISTS task_links (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  from_id       TEXT NOT NULL,
  link_type     TEXT NOT NULL CHECK (link_type IN ('blocks','relates','duplicates')),
  to_id         TEXT NOT NULL,
  created_by    TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  herdr_session TEXT NOT NULL DEFAULT '',
  UNIQUE (herdr_session, from_id, link_type, to_id)
);
CREATE INDEX IF NOT EXISTS task_links_to ON task_links(herdr_session, to_id);
