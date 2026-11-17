-- 011: ロールは herdr セッション固有。どのセッションでの変更かを履歴に残す
ALTER TABLE role_changes ADD COLUMN herdr_session TEXT;
