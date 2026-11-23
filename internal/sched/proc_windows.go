//go:build windows

package sched

import (
	"os"
	"os/exec"
)

// scheduler は macOS(launchd)で動く。Windows ではビルドが通るだけの置き換え。

func setGroup(*exec.Cmd) {}

func killGroup(c *exec.Cmd) { _ = c.Process.Kill() }

func signaled(*os.ProcessState) (int, bool) { return 0, false }

func pidAlive(n int) bool {
	p, err := os.FindProcess(n)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

func readable(f string) bool {
	fh, err := os.Open(f)
	if err != nil {
		return false
	}
	fh.Close()
	return true
}
