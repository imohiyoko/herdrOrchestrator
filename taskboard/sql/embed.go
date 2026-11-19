// Package sql は tb と herdrctl が共有する SQL まわりの定義をバイナリに埋め込む。
package sql

import _ "embed"

// SessionTables は herdr_session 列を持つ表の一覧(session_tables。1 行 1 つ、# はコメント)。
//
//go:embed session_tables
var SessionTables string

// SessionViews は互換ビューの一覧(session_views。019 の alerts / proposals。中身はチケットと小さな表)。
//
//go:embed session_views
var SessionViews string
