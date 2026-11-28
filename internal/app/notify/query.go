package notify

// 画面向けの読み取り(Web UI の通知)。接続は呼び手の rowfmt.Q。

import "github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"

// RemindersLast は最後に出したリマインダーの行(列は value・updated_at。notify_seen の reminders:last)。
func RemindersLast(q rowfmt.Q) []rowfmt.Row {
	r, _ := q("SELECT value, updated_at FROM notify_seen WHERE key='reminders:last'")
	return r
}

// WebuiEvents は届け先に webui を書いたチケットの通知のうち、直近 hours 時間のものを新しい順に 50 件まで。
func WebuiEvents(q rowfmt.Q, hours int) []rowfmt.Row {
	r, _ := q("SELECT id, ref, text, created_at FROM notify_events WHERE type='ticket' AND (' ' || sinks || ' ') LIKE '% webui %'"+
		" AND julianday(created_at) >= julianday('now') - ? / 24.0 ORDER BY id DESC LIMIT 50", hours)
	return r
}
