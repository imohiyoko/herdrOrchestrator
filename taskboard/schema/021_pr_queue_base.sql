-- 021: pr-queue の base(PR の向き先)を行ごとに持つ(#133)。これまでは main 固定。
-- base: 使うブランチ名。NULL は従来どおり main(この移行より前に積まれた行)。
-- base_source: 決め方。fixed(base_mode = fixed のときの main)/ default(origin の既定ブランチを読んだ)/ explicit(add --base で指定)。
-- 値の検査は tb が行う(許可の形は config/pr-queue.conf の base_allow)。
ALTER TABLE pr_queue ADD COLUMN base TEXT;
ALTER TABLE pr_queue ADD COLUMN base_source TEXT;
