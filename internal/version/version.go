// Package version は、ビルド時に埋め込む版の情報を持つ。
// リリースでは GoReleaser が -ldflags -X で上書きする(.goreleaser.yaml)。
package version

import (
	"fmt"
	"runtime"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String は `herdrctl version` が出す 1 行を返す。
func String() string {
	return fmt.Sprintf("herdrctl %s (commit %s, built %s, %s/%s)", Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}
