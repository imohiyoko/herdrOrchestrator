package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DB を開く入口は store(Open / OpenRO)だけにする。ほかのパッケージが sql.Open を直接呼ぶと、
// DB の種類や開き方を変えるときに直す場所が散らばる。テストのファイルは対象外。
func TestOnlyStoreOpensDB(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	self, _ := filepath.Abs(".")
	var bad []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || filepath.Dir(p) == self {
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(b), "sql.Open(") {
			rel, _ := filepath.Rel(root, p)
			bad = append(bad, rel)
		}
		return nil
	})
	if len(bad) > 0 {
		t.Errorf("sql.Open は store の外で呼ばない(store.Open / store.OpenRO を使う): %v", bad)
	}
}
