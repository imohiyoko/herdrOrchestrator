package notify

import (
	"strings"
	"testing"
)

// 細かい振る舞いは taskboard/test/cases/94_notify_contract.sh(bash と同じ出力)で確かめる。ここは部品の要所だけ。

func TestScanRedact(t *testing.T) {
	tok := "ghp_" + "abcdefghij0123456789"
	kinds, hits := Scan("x " + tok + " y " + tok + "zz")
	if kinds != "トークン(API キーなどの形式)" || len(hits) != 2 {
		t.Fatalf("scan: %q %q", kinds, hits)
	}
	// 長いものから伏せる(短い方が先だと長い方の残りが出てしまう)
	if got := Redact("x "+tok+" y "+tok+"zz", hits); got != "x [伏せた秘密] y [伏せた秘密]" {
		t.Errorf("redact: %q", got)
	}
	if k, h := Scan("abcdefghijklmnopqrstuvwxyzabcdefgh と 0123456789abcdef0123456789abcde"); k != "" || h != nil {
		t.Errorf("not secrets: %q %q", k, h)
	}
}

func TestLines(t *testing.T) {
	in := "\n  1 行目 \n```\ncode\n```\n2024-01-02 10:00 log\n\t2 行目\n3 行目"
	if got := Lines(in, true); got != "1 行目 / (コードは省略)" {
		t.Errorf("strip: %q", got)
	}
	if got := Lines(in, false); got != "1 行目 / ```" {
		t.Errorf("no strip: %q", got)
	}
}

func TestClipHash(t *testing.T) {
	if got := Clip(strings.Repeat("あ", 5)+"\n", 3); got != "あああ" {
		t.Errorf("clip: %q", got)
	}
	if got := Clip("ab\n", 10); got != "ab" {
		t.Errorf("clip newline: %q", got)
	}
	if got := Hash("[障害] x 開始: あ"); got != "0323f4faf815" {
		t.Errorf("hash: %q", got)
	}
}

func TestNorm(t *testing.T) {
	for _, c := range []struct{ k, v, want string }{
		{"enabled", "yes", "on"}, {"rate_per_min", "007", "7"}, {"sinks_default", "slack, file,slack", "slack file"}, {"slack_target", "", ""},
	} {
		if got, msg, ok := Norm(c.k, c.v); !ok || got != c.want {
			t.Errorf("%s=%s: %q %q %v", c.k, c.v, got, msg, ok)
		}
	}
	for _, c := range [][2]string{{"enabled", "maybe"}, {"max_len", "39"}, {"sinks_question", "mail"}, {"slack_via", "x"}, {"bogus", "1"}} {
		if _, msg, ok := Norm(c[0], c[1]); ok || msg == "" {
			t.Errorf("%s=%s should fail", c[0], c[1])
		}
	}
	if !KeyOK("sinks_alert_raised") || KeyOK("sinks_bogus") {
		t.Error("KeyOK")
	}
}
