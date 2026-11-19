// Package schema は tasks.db のスキーマ(NNN_*.sql)をバイナリに埋め込む。
// 正はこのディレクトリの .sql で、bash の tb(taskboard/lib/db.sh)と Go の herdrctl(internal/store)が同じファイルを流す。
package schema

import "embed"

//go:embed [0-9][0-9][0-9]_*.sql
var FS embed.FS
