-- 013: 設定を DB に置く(checkout のファイルではなく、セッションごとの行と全セッション共通の行)。
-- tb と Web UI はキー(projects / approvals / routes など)で本文を引く: このセッションの行 → 共通の行(herdr_session='*') → checkout のファイル。
-- 本文は conf ファイルの中身そのまま(書式は変えない)。書くのは人だけ(tb config / tb allow / tb notify set / tb limits)。
CREATE TABLE IF NOT EXISTS config (
  herdr_session TEXT NOT NULL,          -- セッション名、または '*'(全セッション共通)
  key           TEXT NOT NULL,
  body          TEXT NOT NULL,
  updated_by    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  PRIMARY KEY (herdr_session, key)
);
-- 変更の履歴(ファイルだったときの git の差分の代わり)。body は変更後の本文。消したときは NULL
CREATE TABLE IF NOT EXISTS config_changes (
  id            INTEGER PRIMARY KEY,
  herdr_session TEXT NOT NULL,
  key           TEXT NOT NULL,
  body          TEXT,
  changed_by    TEXT NOT NULL,
  how           TEXT NOT NULL,          -- set / edit / unset / import / allow / notify / limits など
  changed_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS config_changes_key ON config_changes(herdr_session, key, id);
