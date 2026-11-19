-- 020: アラートと提案をチケットに移す(ADR 0003 の段 4)。alerts / proposals の表をやめ、チケット(tasks)と小さな表(alert_details / proposal_details)にする。
-- 題・本文・状態・優先度・担当・ラベルはチケットに置き、チケットで表せない部分だけを小さな表に残す(主キーはチケットの ID)。
-- 読み手(digest・limits・notify・review・Web UI など)と A-<n> / P-<n> の番号のために、元と同じ列の alerts / proposals をビューとして残す。
-- ビューへの INSERT / UPDATE は INSTEAD OF トリガーがチケットと小さな表へ振り分け、状態・緊急度をチケットの状態・優先度に写す。
-- ビューの id は A-<n> / P-<n> の番号(alert_no / proposal_no)。task_id 列でチケットを指す。

-- ---------- アラート ----------
CREATE TABLE alert_details (
  task_id        TEXT PRIMARY KEY,               -- チケット(project alert、種別 bug、ラベル alert)
  alert_no       INTEGER NOT NULL UNIQUE,        -- A-<n> の n。DB 全体で一意
  herdr_session  TEXT NOT NULL DEFAULT '',
  fingerprint    TEXT NOT NULL,
  severity       TEXT CHECK (severity IS NULL OR severity IN ('critical','high','medium','low')),
  source         TEXT NOT NULL,
  source_pane    TEXT,
  stage          TEXT NOT NULL CHECK (stage IN ('new','awaiting-approval','approved','queued','analyzing','analyzed','closed')),
  count          INTEGER NOT NULL DEFAULT 1,
  analyst        TEXT,
  note           TEXT,
  result_summary TEXT, result_report TEXT, result_path TEXT, result_by TEXT, result_delivered_at TEXT,
  first_seen TEXT NOT NULL, last_seen TEXT NOT NULL, analysis_started_at TEXT, analyzed_at TEXT, closed_at TEXT
);
CREATE INDEX alert_details_session ON alert_details(herdr_session, stage);
CREATE INDEX alert_details_fp ON alert_details(herdr_session, fingerprint);

-- 既存の行を移す。チケットの番号は project alert の既存の番号の後ろから、A-<n> の順に振る
CREATE TEMP TABLE _alert_map AS
  SELECT a.id AS alert_no,
         'T-alert-' || printf('%03d', (SELECT COALESCE(MAX(CAST(substr(id, 9) AS INTEGER)), 0) FROM tasks WHERE project='alert')
                                      + ROW_NUMBER() OVER (ORDER BY a.id)) AS task_id
  FROM alerts a;
INSERT INTO tasks (id, project, title, owner_agent, status, type, priority, labels, description, summary, reported_at, created_at, updated_at, herdr_session)
  SELECT m.task_id, 'alert', a.title,
         CASE WHEN a.status='analyzing' THEN a.analyst END,
         CASE a.status WHEN 'analyzing' THEN 'in_progress' WHEN 'analyzed' THEN 'review' WHEN 'closed' THEN 'done' ELSE 'todo' END,
         'bug',
         CASE a.severity WHEN 'critical' THEN 'highest' WHEN 'medium' THEN 'medium' WHEN 'low' THEN 'low' ELSE 'high' END,
         '["alert"]', a.body,
         CASE WHEN a.status='closed' THEN a.result_summary END,
         CASE WHEN a.status='closed' THEN COALESCE(a.closed_at, a.last_seen) END,
         a.first_seen, COALESCE(a.closed_at, a.analyzed_at, a.last_seen), a.herdr_session
  FROM alerts a JOIN _alert_map m ON m.alert_no = a.id;
INSERT INTO alert_details (task_id, alert_no, herdr_session, fingerprint, severity, source, source_pane, stage, count, analyst, note,
                           result_summary, result_report, result_path, result_by, result_delivered_at,
                           first_seen, last_seen, analysis_started_at, analyzed_at, closed_at)
  SELECT m.task_id, a.id, a.herdr_session, a.fingerprint, a.severity, a.source, a.source_pane, a.status, a.count, a.analyst, a.note,
         a.result_summary, a.result_report, a.result_path, a.result_by, a.result_delivered_at,
         a.first_seen, a.last_seen, a.analysis_started_at, a.analyzed_at, a.closed_at
  FROM alerts a JOIN _alert_map m ON m.alert_no = a.id;
DROP TABLE _alert_map;
DROP TABLE alerts;

CREATE VIEW alerts AS
  SELECT d.alert_no AS id, d.fingerprint, d.severity, t.title, t.description AS body, d.source, d.source_pane, d.stage AS status, d.count,
         d.analyst, d.note, d.result_summary, d.result_report, d.result_path, d.result_by, d.result_delivered_at,
         d.first_seen, d.last_seen, d.analysis_started_at, d.analyzed_at, d.closed_at, d.herdr_session, d.task_id
  FROM alert_details d JOIN tasks t ON t.id = d.task_id;

-- 登録: チケットを作り、続けて小さな表に足す。id(A-<n> の n)を省けば次の番号
CREATE TRIGGER alerts_insert INSTEAD OF INSERT ON alerts BEGIN
  INSERT INTO tasks (id, project, title, owner_agent, status, type, priority, labels, description, created_at, updated_at, herdr_session)
    SELECT 'T-alert-' || printf('%03d', COALESCE(MAX(CAST(substr(id, 9) AS INTEGER)), 0) + 1), 'alert', NEW.title,
           CASE WHEN NEW.status='analyzing' THEN NEW.analyst END,
           CASE COALESCE(NEW.status, 'new') WHEN 'analyzing' THEN 'in_progress' WHEN 'analyzed' THEN 'review' WHEN 'closed' THEN 'done' ELSE 'todo' END,
           'bug',
           CASE NEW.severity WHEN 'critical' THEN 'highest' WHEN 'medium' THEN 'medium' WHEN 'low' THEN 'low' ELSE 'high' END,
           '["alert"]', NEW.body,
           COALESCE(NEW.first_seen, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), COALESCE(NEW.last_seen, NEW.first_seen, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
           COALESCE(NEW.herdr_session, '')
    FROM tasks WHERE project='alert';
  INSERT INTO alert_details (task_id, alert_no, herdr_session, fingerprint, severity, source, source_pane, stage, count, analyst, note,
                             result_summary, result_report, result_path, result_by, result_delivered_at,
                             first_seen, last_seen, analysis_started_at, analyzed_at, closed_at)
    VALUES ((SELECT id FROM tasks WHERE rowid = last_insert_rowid()),
            COALESCE(NEW.id, (SELECT COALESCE(MAX(alert_no), 0) + 1 FROM alert_details)),
            COALESCE(NEW.herdr_session, ''), NEW.fingerprint, NEW.severity, NEW.source, NEW.source_pane, COALESCE(NEW.status, 'new'),
            COALESCE(NEW.count, 1), NEW.analyst, NEW.note,
            NEW.result_summary, NEW.result_report, NEW.result_path, NEW.result_by, NEW.result_delivered_at,
            COALESCE(NEW.first_seen, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), COALESCE(NEW.last_seen, NEW.first_seen, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
            NEW.analysis_started_at, NEW.analyzed_at, NEW.closed_at);
END;

-- 更新: 小さな表を書き、変わった項目だけをチケットに写す(チケット側で人が変えた題や優先度を、関係ない更新で戻さない)
-- 状態 → チケットの状態: new / awaiting-approval / approved / queued → todo、analyzing → in_progress(担当は解析役)、analyzed → review(人の番)、closed → done
-- 緊急度 → 優先度: critical → highest、high → high、medium → medium、low → low
CREATE TRIGGER alerts_update INSTEAD OF UPDATE ON alerts BEGIN
  UPDATE alert_details SET alert_no=NEW.id, herdr_session=NEW.herdr_session, fingerprint=NEW.fingerprint, severity=NEW.severity,
         source=NEW.source, source_pane=NEW.source_pane, stage=NEW.status, count=NEW.count, analyst=NEW.analyst, note=NEW.note,
         result_summary=NEW.result_summary, result_report=NEW.result_report, result_path=NEW.result_path, result_by=NEW.result_by,
         result_delivered_at=NEW.result_delivered_at, first_seen=NEW.first_seen, last_seen=NEW.last_seen,
         analysis_started_at=NEW.analysis_started_at, analyzed_at=NEW.analyzed_at, closed_at=NEW.closed_at
    WHERE task_id = OLD.task_id;
  UPDATE tasks SET
      title       = CASE WHEN NEW.title IS NOT OLD.title THEN NEW.title ELSE title END,
      description = CASE WHEN NEW.body IS NOT OLD.body THEN NEW.body ELSE description END,
      status      = CASE WHEN NEW.status IS NOT OLD.status THEN
                      CASE NEW.status WHEN 'analyzing' THEN 'in_progress' WHEN 'analyzed' THEN 'review' WHEN 'closed' THEN 'done' ELSE 'todo' END
                    ELSE status END,
      owner_agent = CASE WHEN NEW.status IS NOT OLD.status OR NEW.analyst IS NOT OLD.analyst THEN
                      CASE WHEN NEW.status='analyzing' THEN NEW.analyst END
                    ELSE owner_agent END,
      priority    = CASE WHEN NEW.severity IS NOT OLD.severity THEN
                      CASE NEW.severity WHEN 'critical' THEN 'highest' WHEN 'medium' THEN 'medium' WHEN 'low' THEN 'low' ELSE 'high' END
                    ELSE priority END,
      summary     = CASE WHEN NEW.result_summary IS NOT OLD.result_summary THEN NEW.result_summary ELSE summary END,
      -- 閉じたアラートは完了報告(digest の完了の知らせ)に出さない: 閉じたときに報告済みにする
      reported_at = CASE WHEN NEW.status='closed' AND OLD.status IS NOT 'closed' THEN COALESCE(reported_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')) ELSE reported_at END,
      herdr_session = NEW.herdr_session,
      updated_at  = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
    WHERE id = OLD.task_id;
END;

-- 消す: 小さな表の行とチケットを一緒に消す(tb は消さない。テストや手での片付け用)
CREATE TRIGGER alerts_delete INSTEAD OF DELETE ON alerts BEGIN
  DELETE FROM alert_details WHERE task_id = OLD.task_id;
  DELETE FROM tasks WHERE id = OLD.task_id;
END;

-- ---------- 提案 ----------
CREATE TABLE proposal_details (
  task_id       TEXT PRIMARY KEY,                -- チケット(project proposal、種別 task、ラベル proposal と role:<ファイル名>)
  proposal_no   INTEGER NOT NULL UNIQUE,         -- P-<n> の n。DB 全体で一意
  herdr_session TEXT NOT NULL DEFAULT '',
  role_file     TEXT NOT NULL,
  by_name       TEXT NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('open','applied','rejected')),
  note          TEXT,
  delivered_at TEXT, resolved_at TEXT, created_at TEXT NOT NULL
);
CREATE INDEX proposal_details_session ON proposal_details(herdr_session, state);

CREATE TEMP TABLE _proposal_map AS
  SELECT p.id AS proposal_no,
         'T-proposal-' || printf('%03d', (SELECT COALESCE(MAX(CAST(substr(id, 12) AS INTEGER)), 0) FROM tasks WHERE project='proposal')
                                         + ROW_NUMBER() OVER (ORDER BY p.id)) AS task_id
  FROM proposals p;
INSERT INTO tasks (id, project, title, status, type, priority, labels, description, summary, reported_at, created_at, updated_at, herdr_session)
  SELECT m.task_id, 'proposal', p.title,
         CASE p.status WHEN 'open' THEN 'todo' ELSE 'done' END, 'task', 'medium',
         CASE WHEN p.status='rejected' THEN json_array('proposal', 'rejected', 'role:' || p.role_file) ELSE json_array('proposal', 'role:' || p.role_file) END,
         p.text,
         CASE p.status WHEN 'applied' THEN '適用' || COALESCE(': ' || p.note, '') WHEN 'rejected' THEN '見送り' || COALESCE(': ' || p.note, '') END,
         CASE WHEN p.status != 'open' THEN COALESCE(p.resolved_at, p.created_at) END,
         p.created_at, COALESCE(p.resolved_at, p.created_at), p.herdr_session
  FROM proposals p JOIN _proposal_map m ON m.proposal_no = p.id;
INSERT INTO proposal_details (task_id, proposal_no, herdr_session, role_file, by_name, state, note, delivered_at, resolved_at, created_at)
  SELECT m.task_id, p.id, p.herdr_session, p.role_file, p.by_name, p.status, p.note, p.delivered_at, p.resolved_at, p.created_at
  FROM proposals p JOIN _proposal_map m ON m.proposal_no = p.id;
DROP TABLE _proposal_map;
DROP TABLE proposals;

CREATE VIEW proposals AS
  SELECT d.proposal_no AS id, d.role_file, t.title, t.description AS text, d.state AS status, d.by_name, d.note,
         d.delivered_at, d.resolved_at, d.created_at, d.herdr_session, d.task_id
  FROM proposal_details d JOIN tasks t ON t.id = d.task_id;

CREATE TRIGGER proposals_insert INSTEAD OF INSERT ON proposals BEGIN
  INSERT INTO tasks (id, project, title, status, type, priority, labels, description, created_at, updated_at, herdr_session)
    SELECT 'T-proposal-' || printf('%03d', COALESCE(MAX(CAST(substr(id, 12) AS INTEGER)), 0) + 1), 'proposal', NEW.title,
           CASE COALESCE(NEW.status, 'open') WHEN 'open' THEN 'todo' ELSE 'done' END, 'task', 'medium',
           CASE WHEN NEW.status='rejected' THEN json_array('proposal', 'rejected', 'role:' || NEW.role_file) ELSE json_array('proposal', 'role:' || NEW.role_file) END,
           NEW.text,
           COALESCE(NEW.created_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), COALESCE(NEW.created_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
           COALESCE(NEW.herdr_session, '')
    FROM tasks WHERE project='proposal';
  INSERT INTO proposal_details (task_id, proposal_no, herdr_session, role_file, by_name, state, note, delivered_at, resolved_at, created_at)
    VALUES ((SELECT id FROM tasks WHERE rowid = last_insert_rowid()),
            COALESCE(NEW.id, (SELECT COALESCE(MAX(proposal_no), 0) + 1 FROM proposal_details)),
            COALESCE(NEW.herdr_session, ''), NEW.role_file, NEW.by_name, COALESCE(NEW.status, 'open'), NEW.note,
            NEW.delivered_at, NEW.resolved_at, COALESCE(NEW.created_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')));
END;

-- 状態 → チケット: open → todo、applied → done(要約「適用」)、rejected → done(要約「見送り」とラベル rejected)
CREATE TRIGGER proposals_update INSTEAD OF UPDATE ON proposals BEGIN
  UPDATE proposal_details SET proposal_no=NEW.id, herdr_session=NEW.herdr_session, role_file=NEW.role_file, by_name=NEW.by_name,
         state=NEW.status, note=NEW.note, delivered_at=NEW.delivered_at, resolved_at=NEW.resolved_at, created_at=NEW.created_at
    WHERE task_id = OLD.task_id;
  UPDATE tasks SET
      title       = CASE WHEN NEW.title IS NOT OLD.title THEN NEW.title ELSE title END,
      description = CASE WHEN NEW.text IS NOT OLD.text THEN NEW.text ELSE description END,
      status      = CASE WHEN NEW.status IS NOT OLD.status THEN CASE NEW.status WHEN 'open' THEN 'todo' ELSE 'done' END ELSE status END,
      summary     = CASE WHEN NEW.status IS NOT OLD.status OR NEW.note IS NOT OLD.note THEN
                      CASE NEW.status WHEN 'applied' THEN '適用' || COALESCE(': ' || NEW.note, '') WHEN 'rejected' THEN '見送り' || COALESCE(': ' || NEW.note, '') END
                    ELSE summary END,
      -- ラベル: 見送りになったら rejected を足す(並びは辞書順。他のラベルはそのまま)
      labels      = CASE WHEN NEW.status='rejected' AND OLD.status IS NOT 'rejected'
                      THEN (SELECT json_group_array(v) FROM (SELECT value AS v FROM json_each(labels) UNION SELECT 'rejected' ORDER BY v))
                    ELSE labels END,
      reported_at = CASE WHEN NEW.status != 'open' AND OLD.status = 'open' THEN COALESCE(reported_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')) ELSE reported_at END,
      herdr_session = NEW.herdr_session,
      updated_at  = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
    WHERE id = OLD.task_id;
END;

CREATE TRIGGER proposals_delete INSTEAD OF DELETE ON proposals BEGIN
  DELETE FROM proposal_details WHERE task_id = OLD.task_id;
  DELETE FROM tasks WHERE id = OLD.task_id;
END;
