// herdrctl は、bash と Python の部品を順に移していく先の 1 つのバイナリ(ADR 0002)。
// 今はまだ version と help だけ。コマンドは移し終えたものから足す。
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/imohiyoko/herdrorchestrator/internal/version"
)

const usage = `usage: herdrctl <command>

commands:
  version   版・コミット・ビルド日時・OS/アーキテクチャを出す
  help      この一覧を出す
`

// 終了コードは tb に合わせる: 0 成功 / 1 エラー / 2 使い方の誤り
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.String())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "herdrctl: 不明なコマンド: %s\n%s", args[0], usage)
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
