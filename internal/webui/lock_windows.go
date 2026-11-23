//go:build windows

package webui

import "os"

// Windows ではファイルロックと所有者の確認を省く(webui は macOS / Linux で動かす。ビルドが通るためだけ)。
func flockFile(path string) (func(), error) { return func() {}, nil }

func ownedByMe(st os.FileInfo) bool { return true }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
