package recipe

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// bash の PARTS_TABLE(taskboard/lib/recipes.sh)と同じ表か。移行の間は両方に同じ表を置く。
func TestTableMatchesBash(t *testing.T) {
	b, err := os.ReadFile("../../taskboard/lib/recipes.sh")
	if err != nil {
		t.Skip("recipes.sh がありません")
	}
	m := regexp.MustCompile(`(?s)PARTS_TABLE='\n(.*?)\n'`).FindSubmatch(b)
	if m == nil {
		t.Fatal("PARTS_TABLE が見つかりません")
	}
	if strings.TrimSpace(string(m[1])) != strings.TrimSpace(Table) {
		t.Fatal("recipe.Table が recipes.sh の PARTS_TABLE と違います")
	}
	if _, err := Parse(Table); err != nil {
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
