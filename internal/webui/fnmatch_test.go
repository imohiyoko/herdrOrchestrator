package webui

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fnmatchcase が Python の fnmatch.fnmatchcase と同じ結果を返すか。
func TestFnmatchMatchesPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 が無い")
	}
	pats := []string{"*", "send:*", "send:task", "?", "a?c", "[abc]", "[!abc]", "[!]", "[!]]", "[]]", "[]a]", "[a-c]x", "[!a-c]",
		"[-a]", "[a-]", "[a\\]", "[^a]", "[z-a]", "[", "a[", "[a", "a[b", "**", "*/*", "*.md", "@lead", "a.b", "a+b", "(x)", "[.]",
		"[&~|]", "[!-]", "x*y?z", "日本*", "[日本]", "[[]", "[a-]]", "\\*"}
	names := []string{"", "a", "b", "c", "d", "x", "ax", "bx", "dx", "abc", "aXc", "send:task", "send:task:w1", "]", "!", "-", "^",
		"\\", "[", "a[", "a[b", "[a", "a/b", "x.md", "x/y.md", "@lead", "a.b", "aXb", "a+b", "aab", "(x)", ".", "&", "~", "|",
		"xAyBz", "x/y/z", "日本語", "日", "本", "*", "a]"}
	var cases [][2]string
	for _, p := range pats {
		for _, n := range names {
			cases = append(cases, [2]string{n, p})
		}
	}
	in, _ := json.Marshal(cases)
	c := exec.Command(py, "-c", "import json,sys\nfrom fnmatch import fnmatchcase\nprint(json.dumps([fnmatchcase(n,p) for n,p in json.load(sys.stdin)]))")
	c.Stdin = strings.NewReader(string(in))
	c.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8") // Windows でも標準入出力を UTF-8 に
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []bool
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	for i, cs := range cases {
		if got := fnmatchcase(cs[0], cs[1]); got != want[i] {
			t.Errorf("fnmatchcase(%q, %q) = %v, Python は %v", cs[0], cs[1], got, want[i])
		}
	}
}
