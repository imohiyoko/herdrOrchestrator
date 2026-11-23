//go:build !windows

package sched

import (
	"os"
	"os/exec"
	"syscall"
)

// setGroup は子をプロセスグループの頭にする(時間切れで孫までまとめて止めるため)。
func setGroup(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killGroup はプロセスグループごと止める。
func killGroup(c *exec.Cmd) { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }

// signaled はシグナルで止まったならその番号。
func signaled(ps *os.ProcessState) (int, bool) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return int(ws.Signal()), true
	}
	return 0, false
}

// pidAlive は kill -0 <pid>。
func pidAlive(n int) bool { return syscall.Kill(n, 0) == nil }

// readable は [ -r f ]。
func readable(f string) bool { return syscall.Access(f, 4) == nil }
