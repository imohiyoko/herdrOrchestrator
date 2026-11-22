package handoff

import (
	"slices"
	"strings"
	"testing"
)

func TestSecretScan(t *testing.T) {
	if hits := SecretScan(Text("T-a-001", "plain title", "")); len(hits) != 0 {
		t.Errorf("plain = %q", hits)
	}
	hits := SecretScan("x AKIA" + strings.Repeat("A", 16) + "\nBearer " + strings.Repeat("b", 16))
	if !slices.Contains(hits, "AWS アクセスキー") || !slices.Contains(hits, "Bearer トークン") {
		t.Errorf("hits = %q", hits)
	}
}
