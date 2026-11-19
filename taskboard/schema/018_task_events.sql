-- 018: チケットの履歴(task_events)。チケットを変える書き込みと同じトランザクションで 1 件ずつ残す(ADR 0003 の段 1)。
-- 書くのは tb の接続の一時トリガー(lib/db.sh の event_setup)。tasks / comments / task_links への書き込みから作る。
-- 追記だけ(消さない・書き換えない)。既存のタスクの過去の履歴は無い(この移行より後の変化だけが残る)。
CREATE TABLE IF NOT EXISTS task_events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,   -- E-<n>
  herdr_session TEXT NOT NULL DEFAULT '',
  task_id       TEXT NOT NULL,
  kind          TEXT NOT NULL,     -- created / status / title / type / priority / owner / due / parent / description
                                   -- label_added / label_removed / ref_added / ref_removed / link / unlink / comment
  old           TEXT,              -- 変わる前(無ければ NULL)
  new           TEXT,              -- 変わった後(無ければ NULL)。ラベル・外部リンクはその 1 つ、リンクは "<種類> <相手>"、コメントは種類
  actor         TEXT NOT NULL,     -- agent の pane からなら herdr から引いた agent 名(引けなければ pane:<id>)、人の端末なら human
  ref           TEXT,              -- 元になった行(C-<n> など)。無ければ NULL
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS task_events_task ON task_events(herdr_session, task_id, id);
CREATE INDEX IF NOT EXISTS task_events_kind ON task_events(herdr_session, kind, id);
