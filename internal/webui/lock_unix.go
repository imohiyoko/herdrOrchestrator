//go:build !windows

package webui

import (
	"os"
	"syscall"
)

// flockFile は auth.lock を flock で排他する(以前の Python 版の fcntl.flock と同じ)。
func flockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func ownedByMe(st os.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(s.Uid) == os.Getuid()
}

// pidAlive は kill(pid, 0) が通るか(別ユーザー・無いプロセスは false)。
func pidAlive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }
