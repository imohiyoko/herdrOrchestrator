package util

import (
	"os/exec"
	"runtime"
	"testing"
)

// Glob を bash の [[ s == pattern ]] と突き合わせる。
func TestGlobMatchesBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash と比べる")
	}
	modernBash(t)
	pats := []string{"*", "w?", "w*", "send:task:*", "role:[ab]*", "[!a]*", "[^a]x", "a[", "a]", "[]a]", "[a-c]1", "[-a]", "x\\*", "*.md", "@lead", "a\\", "[[:digit:]]*", "send:*:w1", "?*?", ""}
	strs := []string{"", "a", "w1", "w10", "send:task:w1", "role:apply", "role:change", "bx", "ax", "a[", "a]", "]", "b1", "-", "x*", "xy", "r.md", "@lead", "a\\", "9z", "send:info:w1", "ab"}
	for _, p := range pats {
		for _, s := range strs {
			err := exec.Command("bash", "-c", `[[ $1 == $2 ]]`, "_", s, p).Run()
			want := err == nil
			if got := Glob(p, s); got != want {
				t.Errorf("Glob(%q, %q) = %v, bash %v", p, s, got, want)
			}
		}
	}
}

// modernBash は bash 4 以上があるか(tb の bash 版は mapfile・declare -A を使うので 4 以上が前提。macOS の /bin/bash 3.2 とは比べない)。
func modernBash(t *testing.T) {
	t.Helper()
	out, err := exec.Command("bash", "-c", `echo "${BASH_VERSINFO[0]}"`).Output()
	if err != nil || len(out) == 0 || out[0] < '4' || out[0] > '9' {
		t.Skipf("bash 4 以上が無い(%q)", out)
	}
}
