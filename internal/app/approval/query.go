package approval

// 画面向けの読み取り(Web UI)。接続は呼び手の rowfmt.Q。

import "github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"

// Changes は承認の規則の変更の記録を新しい順に limit 件まで。
func Changes(q rowfmt.Q, limit int) []rowfmt.Row {
	r, _ := q("SELECT id, pattern, value, created_at FROM approval_changes ORDER BY id DESC LIMIT ?", limit)
	return r
}
