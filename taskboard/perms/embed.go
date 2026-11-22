// Package perms は tb の全コマンドの権限段階の表(commands.tbl。ADR 0006)をバイナリに埋め込む。bash の tb も同じファイルを読む。
package perms

import _ "embed"

// Commands は commands.tbl の本文。
//
//go:embed commands.tbl
var Commands string
