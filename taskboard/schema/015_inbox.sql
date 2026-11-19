-- 015: 送信の「今届ける / 保留する」(delivery 設定)と、保留した送信を受け手が読む inbox(tb inbox)。
-- 保留した送信は宛先の入力に打ち込まず、ここに置く。outbox の行は sent(届け先 = inbox)になり、reason に I-<n> が残る(outbox の制約は変えない)。
-- 読めるのは宛先の agent だけ(tb inbox / tb inbox-read が pane から決まる名前で絞る)。read_at が NULL なら未読。
CREATE TABLE IF NOT EXISTS inbox (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  herdr_session TEXT NOT NULL DEFAULT '',
  outbox_id     INTEGER NOT NULL,          -- 元の送信(O-<n>)
  recipient     TEXT NOT NULL,             -- 宛先の agent 名(= outbox.target)
  from_name     TEXT NOT NULL,
  kind          TEXT NOT NULL,             -- task / info / answer
  task_id       TEXT,
  text          TEXT NOT NULL,
  rule          TEXT NOT NULL,             -- 保留と決めた delivery の行
  created_at    TEXT NOT NULL,
  read_at       TEXT,
  UNIQUE (herdr_session, outbox_id)
);
CREATE INDEX IF NOT EXISTS inbox_recipient ON inbox(herdr_session, recipient, read_at);
-- 返信(tb send --reply-to O-<n>)。reply_to は送信者が主張した元の送信、reply_check は tb が DB で確かめた結果
-- (ok = 元の送信が「この送信の宛先 → この送信の送信者」で、まだ返信されていない。それ以外は理由の文。主張が無ければ NULL)。
-- reply_check='ok' の行だけを「返信済み」とみなす(返されていない依頼 = reply_check='ok' の行に指されていない送信)
ALTER TABLE outbox ADD COLUMN reply_to INTEGER;
ALTER TABLE outbox ADD COLUMN reply_check TEXT;
CREATE INDEX IF NOT EXISTS outbox_reply_to ON outbox(herdr_session, reply_to);
-- 1 つの送信に認められる返信は 1 つだけ(同時に 2 つ確かめても、2 つ目は ok にならない)
CREATE UNIQUE INDEX IF NOT EXISTS outbox_reply_once ON outbox(herdr_session, reply_to) WHERE reply_check='ok';
