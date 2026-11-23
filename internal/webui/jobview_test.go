package webui

import (
	"testing"
)

// mask・shell の値の読み取り・tb のサブコマンドの推定を、期待値(testdata/jobview.json。Python 版 jobview.py と同じだったもの)と比べる。
// shell の値の終わりの位置は文字数で持つ。
func TestJobviewGolden(t *testing.T) {
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
	got := map[string]any{}
	for _, x := range texts {
		m, n := mask(x)
		v, inexact, end := shValue(x, 0)
		if end > len(x) {
			end = len(x)
		}
		got["mask "+x] = []any{m, n}
		got["value "+x] = []any{v, inexact, len([]rune(x[:end]))}
	}
	for _, x := range cmds {
		var labels []string
		for _, g := range commandSummary(x) {
			labels = append(labels, g[0])
		}
		got["cmd "+x] = labels
	}
	golden(t, "jobview", got)
}
