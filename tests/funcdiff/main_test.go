package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	const base = `package p

import "fmt"

// T は型
type T struct{ A int }

const (
	X = iota
	Y
)

var V = 1

func F() { fmt.Println(1) }

func (t *T) M() int { return t.A }

func (t T) N() int { return 0 }
`
	cases := []struct {
		name    string
		old     string
		cur     string
		funcs   []string
		idents  []string
		methods []string
		all     bool
	}{
		{name: "同じ", old: base, cur: base},
		{name: "コメント・空白だけ", old: base, cur: replace(base, "func F() { fmt.Println(1) }", "// F は関数\nfunc F() {\n\t// 中\n\tfmt.Println(1)\n}")},
		{name: "関数の中身", old: base, cur: replace(base, "Println(1)", "Println(2)"), funcs: []string{"F"}},
		{name: "ポインタのメソッド", old: base, cur: replace(base, "return t.A", "return t.A + 1"), funcs: []string{"*T.M"}},
		{name: "値のメソッド", old: base, cur: replace(base, "return 0", "return 1"), funcs: []string{"T.N"}},
		{name: "シグネチャ", old: base, cur: replace(base, "func F()", "func F(int)"), funcs: []string{"F"}},
		{name: "関数を足す", old: base, cur: base + "func G() {}\n"},
		{name: "関数を消す", old: base + "func G() {}\n", cur: base},
		{name: "メソッドを足す → 名前", old: base, cur: base + "func (T) Q() {}\n", methods: []string{"Q"}},
		{name: "メソッドを消す → 名前", old: base + "func (T) Q() {}\n", cur: base, methods: []string{"Q"}},
		{name: "標準のインターフェースのメソッドを足す", old: base, cur: base + "func (T) String() string { return \"\" }\n", all: true},
		{name: "型", old: base, cur: replace(base, "A int", "A, B int"), idents: []string{"T"}},
		{name: "var", old: base, cur: replace(base, "V = 1", "V = 2"), idents: []string{"V"}},
		{name: "var を足す", old: base, cur: base + "var W = 3\n", idents: []string{"W"}},
		{name: "const の並びは全部", old: base, cur: replace(base, "\tY\n", "\tZ\n\tY\n"), idents: []string{"X", "Y", "Z"}},
		{name: "名前を付けた import だけ", old: base, cur: replace(base, `import "fmt"`, `import fm "fmt"`)},
		{name: "_ の import", old: base, cur: replace(base, `import "fmt"`, "import (\n\t\"fmt\"\n\t_ \"embed\"\n)"), all: true},
		{name: "init", old: base, cur: base + "func init() {}\n", all: true},
		{name: "型引数", old: base, cur: base + "func G[X any]() {}\n", all: true},
		{name: "//go: の指示", old: base, cur: replace(base, "var V = 1", "//go:embed x\nvar V string"), all: true},
		{name: "build タグ", old: base, cur: "//go:build linux\n\n" + base, all: true},
		{name: "var _", old: base, cur: base + "var _ = F\n", all: true},
		{name: "読めない", old: base, cur: base + "func {", all: true},
		{name: "package", old: base, cur: replace(base, "package p", "package q"), all: true},
		{name: "足したファイル", old: "", cur: "package p\n\nfunc G() {}\n"},
		{name: "足したファイルの型", old: "", cur: "package p\n\ntype U int\n", idents: []string{"U"}},
		{name: "消したファイル", old: "package p\n\nfunc G() {}\n", cur: ""},
		{name: "消したファイルのメソッド", old: "package p\n\nfunc (T) G() {}\n", cur: "", methods: []string{"G"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := diff(bytesOrNil(c.old), bytesOrNil(c.cur))
			if r.all != c.all {
				t.Fatalf("all = %v, want %v (%+v)", r.all, c.all, r)
			}
			if c.all {
				return
			}
			if !reflect.DeepEqual(r.funcs, c.funcs) || !reflect.DeepEqual(r.idents, c.idents) || !reflect.DeepEqual(r.methods, c.methods) {
				t.Fatalf("got funcs=%q idents=%q methods=%q, want %q %q %q", r.funcs, r.idents, r.methods, c.funcs, c.idents, c.methods)
			}
		})
	}
}

func TestUsersOf(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n")
	write("a/a.go", "package a\n\ntype Conf struct{ N int }\n\ntype Wrap struct{ C Conf }\n\nfunc Load() Conf { return Conf{} }\n\nfunc Other() int { return 1 }\n")
	write("b/b.go", "package b\n\nimport al \"example.com/m/a\"\n\nfunc Use() int { return al.Load().N }\n\nfunc UseWrap(w al.Wrap) {}\n\nfunc Local() { type Conf int; var _ Conf }\n")
	write("c/c.go", "package c\n\nfunc Call(x interface{ Q() }) { x.Q() }\n\nfunc Conf() {}\n")
	write("a/a_test.go", "package a\n\nfunc TestX() { _ = Conf{} }\n")
	write("a/testdata/x.go", "package x\n\nfunc Conf() {}\n")
	got, err := usersOf(root, map[string]bool{"a\tConf": true}, map[string]bool{"Q": true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/a.go\tLoad", "b/b.go\tUseWrap", "c/c.go\tCall"} // Wrap は Conf を使うので広がる。b.Local・c.Conf は別のもの
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func replace(s, old, new string) string {
	if !strings.Contains(s, old) {
		panic("not found: " + old)
	}
	return strings.Replace(s, old, new, 1)
}

func bytesOrNil(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
}
