package webui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mask・shell の値の読み取り・tb のサブコマンドの推定が、Python 版(webui/app/jobview.py)と同じ結果を返すか。
func TestJobviewMatchesPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 が無い")
	}
	webui, _ := filepath.Abs("../../webui")
	texts := []string{
		"", "plain", "ghp_abcdefghijklmnopqrstuvwxyz", "xghp_abcdefghijklmnopqrstuvwxyz", "あghp_abcdefghijklmnopqrstuvwxyz",
		"ghp_abcdefghijklmnopqrstuvwxyzあ", "ghp_abcdefghijklmnopqrstuvwxyz-", "ghp_short", "github_pat_ABCDEFGHIJ_abcdefghij_xyz",
		"xoxb-1234567890-abc", "xoxb-123456789_", "xoxb-1234567890-", "AKIAABCDEFGHIJKLMNOP", "AKIAABCDEFGHIJKLMNOPQ", "AKIAABCDEFGHIJKLMNO",
		"sk-abcdefghijklmnopqrstuvwxyz", "sk-abcdefghijklmnopqrst-", "sk-abcdefghijklmnopqrstuvwx_y--",
		"Authorization: Bearer abcdefghijklmnop", "xBearer abcdefghijklmnop", "あBearer abcdefghijklmnop", "BASIC　abcdefghijklmnop",
		"basic abc", "token=abcdef", "TOKEN : 'abcdefgh'", "api-key=\"secretvalue\"", "password=abc", "passwd:abcdefghi x",
		"api_key　=　abcdefgh", "secret=abc\"def", "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY----- tail",
		"-----BEGIN PRIVATE KEY-----\nopen ended", "a ghp_abcdefghijklmnopqrstuvwxyz b sk-abcdefghijklmnopqrstuvwxyz token=zzzzzzzz",
		"echo 'x'#v2", "'x' # c", "\"a\\\"b\" # c", "\"$HOME\"", "\"a\"b", "abc def # c", "a\\\nb\nc", "'open", "\"open\\",
		"x\tdef#y", "a　#c", "  ", "$(date) x", "~/x", "{a,b}",
	}
	var cmds = []string{"tb notify flush", "tb  message   flush", "tb flush", "xtb digest", "あtb digest", "tb digestx", "tb pr-watch",
		"tb pr  watch", "tb pr-queue scan && tb pr queue run", "tb review-pack", "tb system-send x", "tb　pending", "tb compact-check;tb pending",
		"tb notify flushあ", "(tb digest)"}
	in, _ := json.Marshal(map[string]any{"texts": texts, "cmds": cmds})
	script := `
import json, sys
sys.path.insert(0, sys.argv[1])
from app import jobview
d = json.load(sys.stdin)
out = {"mask": [list(jobview.mask(x)) for x in d["texts"]],
       "value": [list(jobview._value(x, 0)) for x in d["texts"]],
       "cmd": [jobview.command_summary(x) for x in d["cmds"]]}
print(json.dumps(out))
`
	c := exec.Command(py, "-c", script, webui)
	c.Stdin = strings.NewReader(string(in))
	c.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8") // Windows でも標準入出力を UTF-8 に
	raw, err := c.Output()
	if err != nil {
		t.Fatalf("python: %v", err)
	}
	var want struct {
		Mask  [][]any      `json:"mask"`
		Value [][]any      `json:"value"`
		Cmd   [][][]string `json:"cmd"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for i, x := range texts {
		m, n := mask(x)
		if m != want.Mask[i][0] || float64(n) != want.Mask[i][1] {
			t.Errorf("mask(%q) = %q, %d; Python %q, %v", x, m, n, want.Mask[i][0], want.Mask[i][1])
		}
		v, inexact, end := shValue(x, 0)
		// Python の位置は文字数、Go はバイト数なので文字数にそろえる
		if end > len(x) {
			end = len(x)
		}
		if v != want.Value[i][0] || inexact != want.Value[i][1] || float64(len([]rune(x[:end]))) != want.Value[i][2] {
			t.Errorf("shValue(%q) = %q, %v, %d; Python %v", x, v, inexact, end, want.Value[i])
		}
	}
	for i, x := range cmds {
		got := commandSummary(x)
		var labels, wl []string
		for _, g := range got {
			labels = append(labels, g[0])
		}
		for _, w := range want.Cmd[i] {
			wl = append(wl, w[0])
		}
		if strings.Join(labels, ",") != strings.Join(wl, ",") {
			t.Errorf("commandSummary(%q) = %v; Python %v", x, labels, wl)
		}
	}
}
