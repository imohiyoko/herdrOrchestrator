package util

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// Glob を bash の [[ s == pattern ]] と突き合わせる。tb の実装は Go だけになったが、設定(policy など)の glob の書き方は
// bash の [[ ]](extglob を含む)と同じという約束なので、bash そのものを正にして比べる(bash 4 以上がある所だけ)。
func TestGlobMatchesBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash と比べる")
	}
	modernBash(t)
	pats := []string{"*", "w?", "w*", "send:task:*", "role:[ab]*", "[!a]*", "[^a]x", "a[", "a]", "[]a]", "[a-c]1", "[-a]", "x\\*", "*.md", "@lead", "a\\", "[[:digit:]]*", "send:*:w1", "?*?", "",
		"[z-a]", "[a\\-z]", "[[:éé:]]", "[[:éé:]a]", "[[:digit:]]x", "!(prod)", "deploy:!(prod)", "@(a|b)", "+(a)", "*(a)b", "?(a)b", "a@(b|c)*", "!(a|b)", "@(a", "x+(", "[!z-a]", "@([)|]a)", "*(ab|a)", "+(a|aa)b", "@()", "!()", "[a-]", "[\\]]"}
	strs := []string{"", "a", "w1", "w10", "send:task:w1", "role:apply", "role:change", "bx", "ax", "a[", "a]", "]", "b1", "-", "x*", "xy", "r.md", "@lead", "a\\", "9z", "send:info:w1", "ab", "b", "m", "z", "é", "prod", "staging", "deploy:prod", "deploy:staging", "aa", "aab", "abab", "bb", "5x", "@(a", "x+(", ")a", "|a", "abd", "aaab", "x"}
	for _, p := range pats {
		for _, s := range strs {
			c := exec.Command("bash", "-c", `[[ $1 == $2 ]]`, "_", s, p)
			c.Env = append(os.Environ(), "LC_ALL=C.UTF-8") // 1 字 = 1 文字(UTF-8)で比べる
			err := c.Run()
			want := err == nil
			if got := Glob(p, s); got != want {
				t.Errorf("Glob(%q, %q) = %v, bash %v", p, s, got, want)
			}
		}
	}
}

// modernBash は bash 4 以上があるか(macOS の /bin/bash 3.2 は extglob などの振る舞いが違うので比べない)。
func modernBash(t *testing.T) {
	t.Helper()
	out, err := exec.Command("bash", "-c", `echo "${BASH_VERSINFO[0]}"`).Output()
	if err != nil || len(out) == 0 || out[0] < '4' || out[0] > '9' {
		t.Skipf("bash 4 以上が無い(%q)", out)
	}
}
