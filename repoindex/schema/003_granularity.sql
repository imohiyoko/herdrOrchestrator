-- 要約の細かさ(#107)。dir = ディレクトリだけ(既定。大きなリポジトリと最初の段階)/ file = ディレクトリと各ファイル
ALTER TABLE repos ADD COLUMN granularity TEXT NOT NULL DEFAULT 'dir' CHECK (granularity IN ('dir','file'));
