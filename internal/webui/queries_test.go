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
// このパッケージと app/<サービス> の query*.go の Go の文字列(+ でつないだものはつないで)のうち SELECT で始まるものを集め、スキーマを流した空の DB で EXPLAIN する。
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
	// Web UI の画面向けの読み取りは app/<サービス> の query*.go に置く(ADR 0008)。このパッケージの残りと合わせて確かめる
	files, _ := filepath.Glob("*.go")
	more, _ := filepath.Glob(filepath.Join("..", "app", "*", "query*.go"))
	files = append(files, more...)
	found := 0
	fs := token.NewFileSet()
	parsed := map[string][]*ast.File{} // パッケージ(ディレクトリ)ごと。定数の名前はパッケージの中だけで引く
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed[filepath.Dir(f)] = append(parsed[filepath.Dir(f)], file)
	}
	// 定数は同じパッケージの全部のファイルから集める(query.go が同じパッケージの別ファイルの定数を使うことがある)
	for dir := range parsed {
		if dir == "." {
			continue
		}
		all, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range all {
			if strings.HasSuffix(f, "_test.go") || strings.Contains(filepath.Base(f), "query") {
				continue
			}
			file, err := parser.ParseFile(fs, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			parsed[dir+"#consts"] = append(parsed[dir+"#consts"], file)
		}
	}
	for dir, pfiles := range parsed {
		if strings.HasSuffix(dir, "#consts") {
			continue
		}
		// パッケージの文字列の定数(SQL の部品を定数にして + でつなぐものがある)
		consts := map[string]string{}
		for range 3 { // 定数が別の定数を使うときのために数回
			for _, file := range append(append([]*ast.File(nil), pfiles...), parsed[dir+"#consts"]...) {
				for _, d := range file.Decls {
					g, ok := d.(*ast.GenDecl)
					if !ok || g.Tok != token.CONST {
						continue
					}
					for _, sp := range g.Specs {
						vs := sp.(*ast.ValueSpec)
						for i, n := range vs.Names {
							if i < len(vs.Values) {
								if v, ok := foldString(vs.Values[i], consts); ok {
									consts[n.Name] = v
								}
							}
						}
					}
				}
			}
		}
		for _, file := range pfiles {
			ast.Inspect(file, func(n ast.Node) bool {
				e, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				if _, isConst := e.(*ast.Ident); isConst {
					return true // 定数そのものは、使われる所(つないだ文)で確かめる
				}
				s, ok := foldString(e, consts)
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

// foldString は文字列の定数(+ でつないだもの・パッケージの定数の名前も)をつないだ値。
func foldString(e ast.Expr, consts map[string]string) (string, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		v, ok := consts[x.Name]
		return v, ok
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return foldString(x.X, consts)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := foldString(x.X, consts)
		b, ok2 := foldString(x.Y, consts)
		return a + b, ok1 && ok2
	}
	return "", false
}
