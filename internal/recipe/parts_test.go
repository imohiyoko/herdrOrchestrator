package recipe

import (
	"testing"
)

func TestTableParses(t *testing.T) {
	if _, err := ParseTable(Table); err != nil {
		t.Fatal(err)
	}
}

func TestValueOK(t *testing.T) {
	cases := []struct {
		t, v string
		ok   bool
	}{
		{"int:1-600", "1", true}, {"int:1-600", "601", false}, {"int:1-600", "-1", false},
		{"enum:a/b", "a", true}, {"enum:a/b", "a/b", false}, {"enum:a/b", "", false},
		{"text:3", "あいう", true}, {"text:3", "あいうx", false}, {"text:9", "a\tb\nc", true}, {"text:9", "a\rb", false},
		{"agent", "human", false}, {"agent", "w1", true}, {"config-key", "slash", true}, {"config-key", "nope", false},
	}
	for _, c := range cases {
		if got := ValueOK(c.t, c.v) == ""; got != c.ok {
			t.Errorf("ValueOK(%q, %q) = %v", c.t, c.v, got)
		}
	}
}
