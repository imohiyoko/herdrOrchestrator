-- 安全の検査で落ちた要約の記録(#108)。本文は持たない。理由の符号と回数だけ
CREATE TABLE rejections (
  id         INTEGER PRIMARY KEY,
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  path       TEXT NOT NULL,
  symbol     TEXT NOT NULL DEFAULT '',
  reasons    TEXT NOT NULL,              -- 理由の符号をカンマ区切り(例: aws_access_key,email)
  attempts   INTEGER NOT NULL DEFAULT 1, -- 作り直しを含めた試行の回数
  final      INTEGER NOT NULL DEFAULT 0, -- 1 = 上限に達して、もう作り直さない
  commit_id  TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX rejections_repo ON rejections(repo_id, created_at);
