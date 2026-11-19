// Package sql は tb と herdrctl が共有する SQL まわりの定義をバイナリに埋め込む。
package sql

import _ "embed"

// SessionTables は herdr_session 列を持つ表の一覧(session_tables。1 行 1 つ、# はコメント)。
//
//go:embed session_tables
var SessionTables string
