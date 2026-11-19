-- 段階的な導入(#110)。段階の変更は人の判断として、測った値と一緒に残す
CREATE TABLE stages (
  id         INTEGER PRIMARY KEY,
  stage      INTEGER NOT NULL CHECK (stage BETWEEN 0 AND 4),
  decided_by TEXT NOT NULL,              -- 判断した人
  os_user    TEXT NOT NULL DEFAULT '',   -- 実行したユーザー(記録用)
  measures   TEXT NOT NULL,              -- 測った値(JSON のオブジェクト)
  note       TEXT NOT NULL DEFAULT '',
  decided_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- 更新 1 回ごとの数(本文は持たない)。測る値の元
CREATE TABLE runs (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  from_commit  TEXT,
  to_commit    TEXT NOT NULL,
  targets      INTEGER NOT NULL DEFAULT 0,
  summarized   INTEGER NOT NULL DEFAULT 0,
  regenerated  INTEGER NOT NULL DEFAULT 0,   -- 作り直しで通ったもの
  rejected     INTEGER NOT NULL DEFAULT 0,   -- 検査で落ちた回数
  final_rejects INTEGER NOT NULL DEFAULT 0,  -- 上限に達したもの
  missing      INTEGER NOT NULL DEFAULT 0,
  seconds      REAL NOT NULL DEFAULT 0,
  finished_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- 定期更新の登録・解除の記録(誰が・いつ・どのテンプレートの hash で)
CREATE TABLE job_log (
  id         INTEGER PRIMARY KEY,
  action     TEXT NOT NULL CHECK (action IN ('register','remove','refused')),
  template   TEXT NOT NULL,
  sha256     TEXT NOT NULL DEFAULT '',
  os_user    TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '',
  at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
