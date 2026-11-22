package perms_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tbcli"
)

// herdrctl に移したコマンドは、どれも表に 1 語の行がある(コマンドを足したら表に行を足さないと落ちる)。
func TestEveryGoCommandHasARow(t *testing.T) {
	tb := perms.Std()
	for _, c := range tbcli.Commands {
		if _, ok := tb.PathOf([]string{c}); !ok {
			t.Errorf("tb %s: taskboard/perms/commands.tbl に行がありません", c)
		}
	}
}

// Go の requireHuman の文面は、表の inner の行に当たる(文字列の連結は * とみなす)。
func TestEveryGoRequireHumanLabelHasAnInnerRow(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "tbcli", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("tbcli のソースが見つかりません: %v", err)
	}
	re := regexp.MustCompile(`requireHuman\("tb ([^"]*)"( \+)?`)
	var inner []string
	for _, r := range perms.Std().Rows {
		if r.Where == "inner" {
			inner = append(inner, r.Path)
		}
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			n++
			pat := strings.ReplaceAll(strings.TrimSpace(m[1]), " ", ".")
			if m[2] != "" {
				pat += "*"
			}
			hit := false
			for _, p := range inner {
				if util.Glob(pat, p) {
					hit = true
				}
			}
			if !hit {
				t.Errorf("%s: requireHuman(\"tb %s\") に当たる inner の行がありません", filepath.Base(f), m[1])
			}
		}
	}
	if n == 0 {
		t.Fatal("requireHuman の呼び出しを 1 つも見つけられません(正規表現が古い)")
	}
}

func TestRowsAreConsistent(t *testing.T) {
	seen := map[string]perms.Row{}
	for _, r := range perms.Std().Rows {
		if perms.Rank(r.Default) < perms.Rank(r.Floor) {
			t.Errorf("%s: 既定 %s が下限 %s より緩い", r.Path, r.Default, r.Floor)
		}
		if s, ok := seen[r.Op]; ok && (s.Default != r.Default || s.Floor != r.Floor) {
			t.Errorf("%s: %s と既定・下限が違う", r.Op, s.Path)
		}
		seen[r.Op] = r
	}
}

func TestPathOfAndTier(t *testing.T) {
	tb := perms.Parse("a cmd:x:a allow allow entry\na.b cmd:x:ab only-human ask-human inner\na.b.c cmd:x:abc allow allow entry\n")
	for _, c := range []struct {
		in   []string
		path string
	}{{[]string{"a"}, "a"}, {[]string{"a", "b"}, "a.b"}, {[]string{"a", "b", "c"}, "a.b.c"}, {[]string{"a", "z", "c"}, "a"}, {[]string{"a", "b", "z"}, "a.b"}} {
		r, ok := tb.PathOf(c.in)
		if !ok || r.Path != c.path {
			t.Errorf("PathOf(%v) = %q, want %q", c.in, r.Path, c.path)
		}
	}
	if _, ok := tb.PathOf([]string{"zz"}); ok {
		t.Error("表に無い語が当たった")
	}
	if tier, _ := tb.Tier(nil, "", "cmd:x:ab"); tier != "only-human" {
		t.Errorf("既定 = %s", tier)
	}
	if tier, _ := tb.Tier(nil, "", "cmd:nope:x"); tier != "only-human" {
		t.Errorf("表に無い操作 = %s", tier)
	}
}

func TestEnforceHumanPassesWithoutHerdr(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	if err := perms.Enforce("cmd:x:y", "only-human", "", "tb x"); err != nil {
		t.Fatal(err)
	}
}
