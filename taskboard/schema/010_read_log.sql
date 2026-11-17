-- 010: agent の中身(pane の出力)を読んだ記録(tb peek)。誰がいつ誰を読んだか、許可されたか
CREATE TABLE IF NOT EXISTS read_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  reader      TEXT NOT NULL,        -- human か、herdr から引いた agent 名
  target      TEXT NOT NULL,
  lines       INTEGER NOT NULL,
  status      TEXT NOT NULL CHECK (status IN ('ok','denied','failed')),
  rule        TEXT,                 -- 決めた approvals の行
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS read_log_created ON read_log(created_at);
