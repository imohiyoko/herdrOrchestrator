package config

// 画面向けの読み取り(Web UI)。接続は呼び手の rowfmt.Q。

import (
	"database/sql"

	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
)

// RoleRows は役割の定義の行(role:<名前> と role-map。列は s(セッション)・key・body)。セッション sess と共通('*')の行。
func RoleRows(q rowfmt.Q, sess string) []rowfmt.Row {
	r, _ := q("SELECT herdr_session AS s, key, body FROM main.config "+
		"WHERE (key LIKE 'role:%' OR key='role-map') AND herdr_session IN (?, '*')", sess)
	return r
}

// JobNames は DB にある job の名前の行(列は name)。セッション sess と共通の行。
func JobNames(q rowfmt.Q, sess string) []rowfmt.Row {
	r, _ := q("SELECT DISTINCT substr(key, 11) AS name FROM main.config WHERE key LIKE 'sched-job:%' AND herdr_session IN (?, '*')", sess)
	return r
}

// Body は設定 key の本文の行(列は body。このセッションの行が共通の行より先。1 行まで)。
func Body(q rowfmt.Q, key, sess string) []rowfmt.Row {
	r, _ := q("SELECT body FROM main.config WHERE key = ? AND herdr_session IN (?, '*') ORDER BY herdr_session = '*' LIMIT 1", key, sess)
	return r
}

// Sessions は DB に行のある herdr セッションの名前(agents・tasks・config。表ごとに max 件まで)を out に足す。
// 表・列が無い(古い DB)ものは飛ばす。
func Sessions(db *sql.DB, max int, out map[string]bool) {
	for _, q := range []string{"SELECT DISTINCT herdr_session FROM main.agents", "SELECT DISTINCT herdr_session FROM main.tasks",
		"SELECT DISTINCT herdr_session FROM main.config"} {
		rows, err := db.Query(q)
		if err != nil {
			continue
		}
		for n := 0; n < max && rows.Next(); n++ {
			var s sql.NullString
			if rows.Scan(&s) == nil && s.Valid {
				out[s.String] = true
			}
		}
		rows.Close()
	}
}

// BodyFrom は設定 key の本文と、どのセッションの行か(列は s・body。このセッションの行が共通の行より先。1 行まで)。
// max が 0 より大きければ本文は先頭 max 文字まで。
func BodyFrom(q rowfmt.Q, key, sess string, max int) []rowfmt.Row {
	if max > 0 {
		r, _ := q("SELECT herdr_session AS s, substr(body, 1, ?) AS body FROM main.config WHERE key=? AND herdr_session IN (?, '*') "+
			"ORDER BY herdr_session='*' LIMIT 1", max, key, sess)
		return r
	}
	r, _ := q("SELECT herdr_session AS s, body FROM main.config WHERE key = ? AND herdr_session IN (?, '*') ORDER BY herdr_session = '*' LIMIT 1", key, sess)
	return r
}
