package webui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pyRealpath・pyDecodeUTF8・pyLower・pySplitExt・pyExpandUser が Python と同じ結果を返すか。
func TestDocsHelpersMatchPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 が無い")
	}
	if runtime.GOOS == "windows" {
		t.Skip("posix のパスだけ")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", dir)
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "f"), nil, 0o644)
	os.Symlink(filepath.Join(dir, "a", "b"), filepath.Join(dir, "abs"))
	os.Symlink("a/b", filepath.Join(dir, "rel"))
	os.Symlink("..", filepath.Join(dir, "a", "up"))
	os.Symlink("loop2", filepath.Join(dir, "loop1"))
	os.Symlink("loop1", filepath.Join(dir, "loop2"))
	os.Symlink("missing/x", filepath.Join(dir, "dangling"))
	os.Symlink("/etc", filepath.Join(dir, "a", "etc"))
	var paths []string
	for _, p := range []string{"", "/", "a", "a/", "a//b/.", "abs", "abs/..", "abs/../f", "rel/../..", "a/up/a/up/rel", "loop1", "loop1/x",
		"dangling", "dangling/../y", "nope/../a", "nope/x/../../abs/..", "a/etc/..", "a/f/..", "./a/./b", "a/../../.."} {
		paths = append(paths, filepath.Join(dir, p), dir+"/"+p)
	}
	exts := []string{"a.md", ".bashrc", "..md", "x/.y.MD", "a.b/c", "a.", "/x/y.tar.gz", "...", "x.Σ", "dir.d/"}
	lows := []string{"ABC", "İstanbul", "ΣΑΣ", "ΑΣ.", "Σ", "aΣ'b", "x ΣΣ y", "ÀÉ", "K"}
	users := []string{"~", "~/x", "~root/y", "~nosuchuser_zz/x", "x~", "~/"}
	bs := []string{"\xe3\x81", "\xff\xfe", "a\xe3\x81\x82b", "\xf0\x9f\x98", "\xed\xa0\x80", "\xc0\xaf", "\xf4\x90\x80\x80", "\xe0\x80\xaf", "ok", "\xc3"}
	in, _ := json.Marshal(map[string]any{"paths": paths, "exts": exts, "lows": lows, "users": users, "bytes": func() []string {
		var h []string
		for _, b := range bs {
			h = append(h, strings.ToUpper(hexOf(b)))
		}
		return h
	}()})
	c := exec.Command(py, "-c", `import json,sys,os
d=json.load(sys.stdin)
print(json.dumps({"paths":[os.path.realpath(p) for p in d["paths"]],"exts":[os.path.splitext(p)[1] for p in d["exts"]],
"lows":[s.lower() for s in d["lows"]],"users":[os.path.expanduser(s) for s in d["users"]],
"bytes":[bytes.fromhex(h).decode("utf-8","replace") for h in d["bytes"]]}))`)
	c.Stdin = strings.NewReader(string(in))
	c.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want map[string][]string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		if got := pyRealpath(p); got != want["paths"][i] {
			t.Errorf("realpath(%q) = %q, Python は %q", p, got, want["paths"][i])
		}
	}
	for i, p := range exts {
		if got := pySplitExt(p); got != want["exts"][i] {
			t.Errorf("splitext(%q) = %q, Python は %q", p, got, want["exts"][i])
		}
	}
	for i, s := range lows {
		if got := pyLower(s); got != want["lows"][i] {
			t.Errorf("lower(%q) = %q, Python は %q", s, got, want["lows"][i])
		}
	}
	for i, s := range users {
		if got := pyExpandUser(s); got != want["users"][i] {
			t.Errorf("expanduser(%q) = %q, Python は %q", s, got, want["users"][i])
		}
	}
	for i, b := range bs {
		if got := pyDecodeUTF8([]byte(b)); got != want["bytes"][i] {
			t.Errorf("decode(%q) = %q, Python は %q", b, got, want["bytes"][i])
		}
	}
}

func hexOf(s string) string {
	const h = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteByte(h[s[i]>>4])
		b.WriteByte(h[s[i]&15])
	}
	return b.String()
}
