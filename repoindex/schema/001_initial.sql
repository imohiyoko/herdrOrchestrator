-- repoindex 専用の DB(タスク DB とは別ファイル)。版は PRAGMA user_version で、このディレクトリの NNN_*.sql を順に流す。
-- 番号はこのモジュールの中だけの通し番号(taskboard/schema とは別の系列)。

-- 索引に入れるリポジトリ。一覧は人が選ぶ(repoindex repo-add)
CREATE TABLE repos (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  path        TEXT NOT NULL,              -- ローカルの clone の絶対パス
  head_commit TEXT,                       -- 最後に索引を進めたコミット(未作成なら NULL)
  added_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- 要約。ディレクトリ・選んだファイル・選んだシンボル(公開 API)だけ。全関数ではない
CREATE TABLE summaries (
  id          INTEGER PRIMARY KEY,
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('dir','file','symbol')),
  path        TEXT NOT NULL,              -- リポジトリ内の相対パス(ディレクトリは末尾の / なし、ルートは '.')
  symbol      TEXT NOT NULL DEFAULT '',   -- kind=symbol のときだけ
  body        TEXT NOT NULL,
  commit_id   TEXT NOT NULL,              -- 要約を作ったときのコミット
  file_hash   TEXT,                       -- 要約を作ったときのファイルの git blob hash(dir は NULL)
  fmt_version INTEGER NOT NULL DEFAULT 1, -- 要約の書式の版
  model       TEXT NOT NULL DEFAULT '',   -- 要約を作ったモデル
  state       TEXT NOT NULL DEFAULT 'fresh' CHECK (state IN ('fresh','stale','missing')),
  boundaries  TEXT NOT NULL DEFAULT '',   -- 関わる境界(auth,authz,external,data をカンマ区切り)
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (repo_id, kind, path, symbol)
);
CREATE INDEX summaries_state ON summaries(repo_id, state);

-- 全文検索(本文は summaries に 1 回だけ持つ)
CREATE VIRTUAL TABLE summaries_fts USING fts5(body, path, symbol, content='summaries', content_rowid='id');
CREATE TRIGGER summaries_ai AFTER INSERT ON summaries BEGIN
  INSERT INTO summaries_fts(rowid, body, path, symbol) VALUES (new.id, new.body, new.path, new.symbol);
END;
CREATE TRIGGER summaries_ad AFTER DELETE ON summaries BEGIN
  INSERT INTO summaries_fts(summaries_fts, rowid, body, path, symbol) VALUES ('delete', old.id, old.body, old.path, old.symbol);
END;
CREATE TRIGGER summaries_au AFTER UPDATE OF body, path, symbol ON summaries BEGIN
  INSERT INTO summaries_fts(summaries_fts, rowid, body, path, symbol) VALUES ('delete', old.id, old.body, old.path, old.symbol);
  INSERT INTO summaries_fts(rowid, body, path, symbol) VALUES (new.id, new.body, new.path, new.symbol);
END;
