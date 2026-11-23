package webui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// Web UI が tasks.db に投げる SQL が、本物のスキーマでそのまま通るか。
// このパッケージの Go の文字列(+ でつないだものはつないで)のうち SELECT で始まるものを集め、スキーマを流した空の DB で EXPLAIN する。
// 列名やテーブル名が tb 側と食い違えば、ここで落ちる(画面は DB のエラーを空の一覧として隠すため)。
func TestQueriesExplain(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "tasks.db"), "default", &store.Options{NoSession: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stToken := regexp.MustCompile(`__[A-Z]+_STATUSES__`)
	explain := func(where, sql string) {
		sql = stToken.ReplaceAllString(sql, "'x'")
		args := make([]any, strings.Count(sql, "?"))
		if _, err := db.Exec("EXPLAIN "+sql, args...); err != nil {
			t.Errorf("%s: %v\n  %.160s", where, err, sql)
		}
	}
	files, _ := filepath.Glob("*.go")
	found := 0
	fs := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			s, ok := foldString(e)
			if !ok {
				return true
			}
			// 後ろに WHERE などを足して組み立てる文もあるので、文として閉じていなくても確かめる
			s = strings.TrimSpace(s)
			if u := strings.ToUpper(s); strings.HasPrefix(u, "SELECT ") && strings.Contains(u, " FROM ") {
				found++
				explain(fs.Position(e.Pos()).String(), s)
			}
			return false // つないだ文字列の一部を別に数えない
		})
	}
	// taskboard/sql/*.sql(tb と Web UI が同じものを読む)もそのまま通るか
	sqls, _ := filepath.Glob("../../taskboard/sql/*.sql")
	for _, p := range sqls {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var keep []string
		for _, ln := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(ln, "--") {
				keep = append(keep, ln)
			}
		}
		found++
		explain(filepath.Base(p), strings.Join(keep, "\n"))
	}
	if found < 30 {
		t.Fatalf("SQL が %d 件しか見つからない(抽出が壊れている)", found)
	}
	t.Logf("%d 件", found)
}

// foldString は文字列の定数(+ でつないだものも)をつないだ値。
func foldString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return foldString(x.X)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := foldString(x.X)
		b, ok2 := foldString(x.Y)
		return a + b, ok1 && ok2
	}
	return "", false
}
