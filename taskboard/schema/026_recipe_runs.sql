-- 026: レシピの実行の待ち行列(ADR 0005 の段 3)。pane の外で行う部品(実行役 outside)を含むレシピは、依頼者の pane の中で判定してから、
-- 検査済みの部品の並び(plan)だけをここに置く。外の実行役(tb recipe-runner。人が登録した定期ジョブが動かす)が取り出し、
-- 実行の時点で依頼者の権限で部品ごとに判定し直してから、並びどおりに実行する。並びの外のことはしない。
-- 記録は task_events(recipe_run / recipe_step / recipe_end)。ここは状態だけを持つ。
CREATE TABLE IF NOT EXISTS recipe_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,   -- R-<n>
  herdr_session TEXT NOT NULL,
  name          TEXT NOT NULL,
  body_sha      TEXT NOT NULL,        -- 判定したときのレシピの本文(SHA-256)。実行のときに変わっていたら実行しない
  target        TEXT NOT NULL,
  requester     TEXT NOT NULL,        -- 依頼者(agent 名か human)。実行役はこの権限で判定し直す
  via           TEXT,                 -- 定期ジョブから頼んだときは sched:<ジョブ>
  task_id       TEXT NOT NULL DEFAULT '',
  plan          TEXT NOT NULL,        -- JSON: [{"part":"...","args":{"名前":"値"}}, ...](値は型の検査を通ったもの)
  event_id      INTEGER NOT NULL,     -- task_events の recipe_run の行(E-<n>)
  status        TEXT NOT NULL CHECK (status IN ('queued','running','done','failed','stopped','expired','cancelled')),
  result        TEXT,
  created_at    TEXT NOT NULL,
  started_at    TEXT,
  finished_at   TEXT
);
CREATE INDEX IF NOT EXISTS recipe_runs_status ON recipe_runs(herdr_session, status, id);
