package tbcli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJQString(t *testing.T) {
	cases := map[string]string{
		"a":            `"a"`,
		"<&>":          `"<&>"`,
		"日本 ":          "\"日本 \"",
		"q\"\\":        `"q\"\\"`,
		"\n\t\x01\x7f": `"\n\t\u0001\u007f"`,
	}
	for in, want := range cases {
		if got := jqString(in); got != want {
			t.Errorf("jqString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestFromJSONKeepsOrderAndCompacts(t *testing.T) {
	got, err := fromJSON(`[ {"url": "https://x", "kind": "link"}, 1.0, 2, "あ" ]`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"url":"https://x","kind":"link"},1,2,"あ"]`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
	if _, err := fromJSON(`[1] x`); err == nil {
		t.Error("trailing garbage was accepted")
	}
}

func TestLabels(t *testing.T) {
	got, err := labels(" b, a ,,b")
	if err != nil || strings.Join(got, "|") != "a|b" {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := labels("a b"); err == nil {
		t.Error("label with space was accepted")
	}
	if _, err := labels(strings.Repeat("あ", 41)); err == nil {
		t.Error("41-char label was accepted")
	}
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	c := Run(args, &out, &errb)
	return c, out.String(), errb.String()
}

func TestListUsageAndEmpty(t *testing.T) {
	t.Setenv("TB_DB", filepath.Join(t.TempDir(), "tasks.db"))
	t.Setenv("HERDR_SESSION", "s1")
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("TB_SCHEMA_DIR", "")
	if c, out, _ := run(t, "list"); c != 0 || out != "" {
		t.Errorf("empty list = %d %q", c, out)
	}
	if c, _, e := run(t, "list", "--bogus"); c != 2 || !strings.Contains(e, "tb: list: 不明な引数 --bogus\n(tb help で使い方)") {
		t.Errorf("bogus = %d %q", c, e)
	}
	if c, _, e := run(t, "list", "-s"); c != 2 || !strings.Contains(e, "-s に値が必要です") {
		t.Errorf("missing value = %d %q", c, e)
	}
	if c, _, e := run(t, "list", "--sort", "x"); c != 2 || !strings.Contains(e, "--sort は") {
		t.Errorf("bad sort = %d %q", c, e)
	}
	if c, out, _ := run(t, "--commands"); c != 0 || !strings.Contains(out, "list") {
		t.Errorf("--commands = %d %q", c, out)
	}
}

func TestTouchAndActivity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TB_DB", filepath.Join(dir, "tasks.db"))
	t.Setenv("TB_TOUCH_LOG", filepath.Join(dir, "touch.log"))
	t.Setenv("HERDR_SESSION", "s1")
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("TB_SCHEMA_DIR", "")
	if c, out, e := run(t, "touch", "--event", "UserPromptSubmit", "--session-id", "a1", "--pane-id", "w:p1"); c != 0 || out != "" || e != "" {
		t.Fatalf("touch = %d %q %q", c, out, e)
	}
	if c, out, _ := run(t, "activity"); c != 0 || !strings.HasPrefix(out, "a1\tw:p1\t1\t0\t") || !strings.HasSuffix(out, "\t-\t-\n") {
		t.Errorf("activity = %d %q", c, out)
	}
	if c, out, _ := run(t, "activity", "--json"); c != 0 || !strings.Contains(out, `"session_id":"a1"`) || !strings.Contains(out, `"working":1,"stuck":0,"since_prompt_min":0}`) {
		t.Errorf("activity --json = %d %q", c, out)
	}
	if c, _, e := run(t, "activity", "--stuck-min", "x"); c != 2 || !strings.Contains(e, "tb: 分は数字: x") {
		t.Errorf("bad --stuck-min = %d %q", c, e)
	}
	// 不正な入力: 何も出さず 0、記録には理由コードだけ
	if c, out, e := run(t, "touch", "--event", "Stop", "--prompt", "SECRET"); c != 0 || out != "" || e != "" {
		t.Errorf("bad touch = %d %q %q", c, out, e)
	}
	b, err := os.ReadFile(filepath.Join(dir, "touch.log"))
	if err != nil || !strings.Contains(string(b), " event=Stop reason=invalid-args\n") || strings.Contains(string(b), "SECRET") {
		t.Errorf("log = %q %v", b, err)
	}
}
