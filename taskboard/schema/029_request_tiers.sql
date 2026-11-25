-- 029: allow / ask / deny のどれも承認待ちの表 requests の 1 行(Q-<n>)に残す(誰が依頼し、何が・誰が決めたかを 1 か所で追えるように)。
-- tier: 判定(allow / ask / deny)。rule: 当たった policy の行(allow / deny は決めたのが policy、decided_by は 'policy')。
-- status に足す値: allowed(policy が許可。記録だけで承認は要らない)、denied(policy が拒否。人が approve すると例外として 1 回だけ実行できる)。
-- ask の承認(approved)では実行せず、依頼者に知らせ、依頼者が tb approval run Q-<n> で 1 回だけ実行する(scheduler など知らせる相手がいない依頼者は従来どおり承認した側が実行する)。
-- notified_at: 依頼者に結果を知らせた時刻。作り直しの手順は 024 と同じ。
CREATE TABLE requests_new (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,   -- Q-<n>
  herdr_session TEXT NOT NULL,
  source        TEXT NOT NULL CHECK (source IN ('outbox','agent_ops','cmd')),
  source_id     INTEGER,
  requester     TEXT NOT NULL,
  op            TEXT NOT NULL,
  target        TEXT NOT NULL DEFAULT '*',
  priority      INTEGER NOT NULL DEFAULT 1 CHECK (priority IN (0,1,2)),
  detail        TEXT NOT NULL DEFAULT '',
  payload       TEXT,
  status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','running','done','failed','rejected','allowed','denied')),
  decided_by    TEXT,
  decided_at    TEXT,
  result        TEXT,
  created_at    TEXT NOT NULL,
  run_key       TEXT,
  tier          TEXT NOT NULL DEFAULT 'ask' CHECK (tier IN ('allow','ask','deny')),
  rule          TEXT,
  notified_at   TEXT
);
INSERT INTO requests_new (id, herdr_session, source, source_id, requester, op, target, priority, detail, payload, status, decided_by, decided_at, result, created_at, run_key)
  SELECT id, herdr_session, source, source_id, requester, op, target, priority, detail, payload, status, decided_by, decided_at, result, created_at, run_key
  FROM requests ORDER BY id;
DROP TABLE requests;
PRAGMA legacy_alter_table=ON;
ALTER TABLE requests_new RENAME TO requests;
PRAGMA legacy_alter_table=OFF;
CREATE UNIQUE INDEX IF NOT EXISTS requests_source ON requests(source, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS requests_session ON requests(herdr_session, status);
CREATE INDEX IF NOT EXISTS requests_requester ON requests(herdr_session, requester, id);
