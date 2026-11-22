package proxy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDetectMeta(t *testing.T) {
	cases := map[string][]string{
		"git log -5":                  nil,
		"git log | head":              {"パイプ/||"},
		"gh pr view 1 -XDELETE":       {"書き込み系の HTTP メソッド"},
		"gh pr view 1 --method=patch": {"書き込み系の HTTP メソッド"},
		"gh pr view 1 --add-label x":  {"書き込み系のフラグ(--approve/--merge/--body/--add)"},
		"git diff --output=f":         {"ファイル出力(--output)"},
		"a\n-X POST":                  {"改行", "書き込み系の HTTP メソッド"},
		"a -X\nPOST":                  {"改行"}, // grep と同じく行ごとに見る
	}
	for in, want := range cases {
		if got := DetectMeta(in); !slices.Equal(got, want) {
			t.Errorf("DetectMeta(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	al := filepath.Join(t.TempDir(), "allow.txt")
	os.WriteFile(al, []byte("# c\n\n  git log  \r\ngh pr view\n"), 0o600)
	join := func(r Row) string { return strings.Join(Check(r, al), "\n") }
	if got := join(Row{"git log -5", "read-only", "open"}); got != "allow\nmatch: git log" {
		t.Errorf("allow = %q", got)
	}
	if got := join(Row{"git logx", "read-only", "open"}); got != "human\nreason: 許可リストにない" {
		t.Errorf("word boundary = %q", got)
	}
	if got := join(Row{"git log", "read-only", "denied"}); got != "human\nreason: すでに denied" {
		t.Errorf("denied = %q", got)
	}
	if got := Check(Row{"git log", "read-only", "open"}, al+".none"); got[0] != "human" {
		t.Errorf("missing allowlist = %q", got)
	}
}

func TestTrim(t *testing.T) {
	if got := Trim(" \t x y \r\n"); got != "x y" {
		t.Errorf("Trim = %q", got)
	}
	if !Blank(" \t\n") || Blank(" x ") {
		t.Error("Blank")
	}
}
