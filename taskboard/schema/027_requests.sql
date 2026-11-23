-- 027: 承認待ちを 1 か所に(ADR 0007 の段 4)。ask になったものは、送信・agent の操作・コマンドのどれも、この表の 1 行(Q-<n>)になる。
-- 送信(outbox)と agent の操作(agent_ops)は、元の表に pending の行ができたときにトリガーがここへ足す(状態の正は元の行)。
-- コマンド(ask で止まった tb のコマンド)は、ここが正(依頼の中身 payload と状態を持つ)。
-- 1 行が持つラベル: 依頼者(requester)・要る権限(op と target)・優先度(priority。2 high / 1 normal / 0 low)。
CREATE TABLE IF NOT EXISTS requests (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,   -- Q-<n>
  herdr_session TEXT NOT NULL,
  source        TEXT NOT NULL CHECK (source IN ('outbox','agent_ops','cmd')),
  source_id     INTEGER,                             -- outbox / agent_ops の id(cmd は NULL)
  requester     TEXT NOT NULL,                       -- 依頼した agent 名(human / system:<名前> もありうる)
  op            TEXT NOT NULL,                       -- 要る権限の操作名(send:task、agent:stop、cmd:ticket:cancel、policy:agent:stop など)
  target        TEXT NOT NULL DEFAULT '*',           -- その操作の対象
  priority      INTEGER NOT NULL DEFAULT 1 CHECK (priority IN (0,1,2)),
  detail        TEXT NOT NULL DEFAULT '',            -- 一覧に出す中身(送る本文・操作の引数・コマンド)
  payload       TEXT,                                -- cmd: JSON {"args":[...],"stdin":"..."}
  status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','running','done','failed','rejected')),  -- cmd のときだけ使う
  decided_by    TEXT,
  decided_at    TEXT,
  result        TEXT,
  created_at    TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS requests_source ON requests(source, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS requests_session ON requests(herdr_session, status);

CREATE TRIGGER IF NOT EXISTS requests_outbox_ins AFTER INSERT ON outbox WHEN NEW.status = 'pending' BEGIN
  INSERT OR IGNORE INTO requests (herdr_session, source, source_id, requester, op, target, detail, created_at)
  VALUES (NEW.herdr_session, 'outbox', NEW.id, NEW.from_name, 'send:' || NEW.kind, NEW.target, NEW.text, NEW.created_at);
END;
CREATE TRIGGER IF NOT EXISTS requests_outbox_upd AFTER UPDATE OF status ON outbox WHEN NEW.status = 'pending' AND OLD.status IS NOT 'pending' BEGIN
  INSERT OR IGNORE INTO requests (herdr_session, source, source_id, requester, op, target, detail, created_at)
  VALUES (NEW.herdr_session, 'outbox', NEW.id, NEW.from_name, 'send:' || NEW.kind, NEW.target, NEW.text, NEW.created_at);
END;
CREATE TRIGGER IF NOT EXISTS requests_agent_ops_ins AFTER INSERT ON agent_ops WHEN NEW.status = 'pending' BEGIN
  INSERT OR IGNORE INTO requests (herdr_session, source, source_id, requester, op, target, detail, created_at)
  VALUES (NEW.herdr_session, 'agent_ops', NEW.id, NEW.requested_by, 'agent:' || NEW.op, NEW.target, NEW.args, NEW.created_at);
END;

-- いまの承認待ちにも番号を振る
INSERT OR IGNORE INTO requests (herdr_session, source, source_id, requester, op, target, detail, created_at)
  SELECT herdr_session, 'outbox', id, from_name, 'send:' || kind, target, text, created_at FROM outbox WHERE status = 'pending' ORDER BY id;
INSERT OR IGNORE INTO requests (herdr_session, source, source_id, requester, op, target, detail, created_at)
  SELECT herdr_session, 'agent_ops', id, requested_by, 'agent:' || op, target, args, created_at FROM agent_ops WHERE status = 'pending' ORDER BY id;
