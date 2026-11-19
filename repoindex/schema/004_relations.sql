-- ファイルどうしの関係(#109)。索引が持っている分だけ(呼び出し元の多くは、引くときに普通のコード検索で探す)
CREATE TABLE relations (
  id        INTEGER PRIMARY KEY,
  repo_id   INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  src       TEXT NOT NULL,                -- 呼ぶ側(リポジトリ内の相対パス)
  dst       TEXT NOT NULL,                -- 呼ばれる側
  kind      TEXT NOT NULL DEFAULT 'calls' CHECK (kind IN ('calls','imports')),
  commit_id TEXT NOT NULL,                -- この関係を見たコミット
  UNIQUE (repo_id, src, dst, kind)
);
CREATE INDEX relations_dst ON relations(repo_id, dst);
