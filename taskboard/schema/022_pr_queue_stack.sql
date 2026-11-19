-- 022: pr-queue で PR を積み重ねる(#134 の第一段)。上の行の base は、下の行の head(ブランチ)。
-- depends_on: 下の行の id(PQ-<n> の n)。NULL なら単独の PR(今までどおり)。このときの base_source は stack。
-- head_sha: PR を作ったときに検査した head の SHA。上の行は、下のブランチがまだこの SHA であることを確かめてから作る。
ALTER TABLE pr_queue ADD COLUMN depends_on INTEGER;
ALTER TABLE pr_queue ADD COLUMN head_sha TEXT;
