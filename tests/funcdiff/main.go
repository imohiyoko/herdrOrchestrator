// funcdiff は、Go のファイルの前と今を比べて、中身の変わった関数の名前を出す(tests/affected.sh が、tb のケースを関数で選ぶのに使う)。
//
//	go run ./tests/funcdiff <REV> <ファイル> ...    リポジトリの根で動かす
//
// 前は git show <REV>:<ファイル>、今は作業ツリーのファイル。1 行 1 つ「ファイル<TAB>関数」を出す。
// 関数の名前は go tool covdata func と同じ形(f、T.m、*T.m)。
//
// 関数の外(const・var・type)が変わったら、変わった名前(足した・消したものも)を使う関数を、モジュールの中(*_test.go を除く)から
// すべて探して出す。名前は字面で突き合わせるので、同じ名前の別のものを使う関数も出る(多めに出るだけで、漏れない側)。
// 変わった const・var・type の名前を使う const・var・type も、変わったものとして広げる。
//
// 関数の名前では言い切れない変更は「ファイル<TAB>*」を出す(呼ぶ側は、すべて変わったとみなす):
// 読めない、init が変わった、標準ライブラリのインターフェースのメソッドの名前(String・Error・MarshalJSON など。下の implicit)の
// メソッドを足した・消した(呼ぶ側の字面に名前が出ない)、_ や . の import・build タグ・//go: の指示が変わった、型引数のある関数・メソッド。
// 関数を足した・消したことは出さない(呼ぶ側の関数が変わるので、そちらで分かる)。ほかの名前のメソッドを足した・消したときは、
// その名前を変わった名前に入れる(名前を使う関数と、その名前を持つインターフェースを使う関数が出る)。
// 足したファイル・消したファイルは、空のファイルと比べる。コメント・空白だけの変更は変更とみなさない。
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: funcdiff <REV> <ファイル> ...")
		os.Exit(2)
	}
	rev := os.Args[1]
	out := map[string]bool{}
	pkgNames, methods := map[string]bool{}, map[string]bool{}
	for _, f := range os.Args[2:] {
		d := fileDiff(rev, f)
		if d.all {
			out[f+"\t*"] = true
			continue
		}
		for _, n := range d.funcs {
			out[f+"\t"+n] = true
		}
		for _, n := range d.idents {
			pkgNames[pathDir(filepath.ToSlash(filepath.Clean(f)))+"\t"+n] = true
		}
		for _, n := range d.methods {
			methods[n] = true
		}
	}
	if len(pkgNames)+len(methods) > 0 {
		if os.Getenv("FUNCDIFF_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "funcdiff: 変わった名前: %q メソッド: %q\n", sortedKeys(pkgNames), sortedKeys(methods))
		}
		users, err := usersOf(".", pkgNames, methods)
		if err != nil {
			fmt.Fprintln(os.Stderr, "funcdiff:", err)
			os.Exit(1)
		}
		for _, u := range users {
			out[u] = true
		}
	}
	lines := make([]string, 0, len(out))
	for l := range out {
		lines = append(lines, l)
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
}

func fileDiff(rev, path string) result {
	var old []byte
	if exec.Command("git", "cat-file", "-e", rev+":"+path).Run() == nil {
		var err error
		if old, err = exec.Command("git", "show", rev+":"+path).Output(); err != nil {
			return result{all: true}
		}
	} else if exec.Command("git", "rev-parse", "--verify", "--quiet", rev+"^{commit}").Run() != nil {
		return result{all: true} // REV が無い
	} // ほかは足したファイル(old = nil)
	cur, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return result{all: true}
		}
		cur = nil // 消したファイル
	}
	return diff(old, cur)
}

type result struct {
	funcs   []string // 中身の変わった関数
	idents  []string // 変わった(足した・消した)const・var・type の名前
	methods []string // 足した・消したメソッドの名前
	all     bool     // 関数の名前では言い切れない
}

// diff は 1 つのファイルの前と今を比べる。nil は無いファイル
func diff(old, cur []byte) result {
	if old == nil && cur == nil {
		return result{all: true}
	}
	a, ok := summarize(old)
	if !ok {
		return result{all: true}
	}
	b, ok := summarize(cur)
	if !ok {
		return result{all: true}
	}
	switch { // 無いファイルは、相手と同じ package の空のファイルとみなす
	case old == nil:
		a.head = b.pkg
	case cur == nil:
		b.head = a.pkg
	}
	if a.head != b.head {
		return result{all: true}
	}
	var r result
	addedOrRemoved := func(n string) bool { // メソッドなら、名前を変わった名前に入れる。標準のインターフェースの名前なら false
		i := strings.LastIndex(n, ".")
		if i < 0 {
			return true
		}
		if implicit[n[i+1:]] {
			return false
		}
		r.methods = append(r.methods, n[i+1:])
		return true
	}
	for n, body := range b.funcs {
		ob, found := a.funcs[n]
		switch {
		case !found && !addedOrRemoved(n):
			return result{all: true}
		case found && ob != body:
			r.funcs = append(r.funcs, n)
		}
	}
	for n := range a.funcs {
		if _, found := b.funcs[n]; !found && !addedOrRemoved(n) {
			return result{all: true}
		}
	}
	for n, d := range b.decls {
		if a.decls[n] != d {
			r.idents = append(r.idents, n)
		}
	}
	for n := range a.decls {
		if _, found := b.decls[n]; !found {
			r.idents = append(r.idents, n)
		}
	}
	sort.Strings(r.funcs)
	sort.Strings(r.idents)
	return r
}

// implicit は、標準ライブラリ(と依存)が、呼ぶ側の字面に名前を出さずに呼ぶメソッドの名前(足すと、型を渡しただけの所の動きが変わる)
var implicit = map[string]bool{
	"String": true, "GoString": true, "Format": true, "Error": true, "Unwrap": true, "Is": true, "As": true, "Timeout": true, "Temporary": true,
	"MarshalJSON": true, "UnmarshalJSON": true, "MarshalText": true, "UnmarshalText": true, "MarshalBinary": true, "UnmarshalBinary": true,
	"MarshalYAML": true, "UnmarshalYAML": true, "MarshalXML": true, "UnmarshalXML": true, "AppendText": true, "AppendBinary": true,
	"Read": true, "Write": true, "Close": true, "Seek": true, "ReadAt": true, "WriteAt": true, "ReadFrom": true, "WriteTo": true,
	"ReadByte": true, "ReadRune": true, "WriteString": true, "WriteByte": true, "WriteRune": true, "UnreadByte": true, "UnreadRune": true,
	"Len": true, "Less": true, "Swap": true, "Push": true, "Pop": true, "Set": true, "Get": true, "IsBoolFlag": true,
	"ServeHTTP": true, "RoundTrip": true, "Scan": true, "Value": true, "LogValue": true, "Handle": true, "Enabled": true,
	"Deadline": true, "Done": true, "Err": true, "Lock": true, "Unlock": true, "Sum": true, "Reset": true, "Size": true, "BlockSize": true,
	"Name": true, "Mode": true, "ModTime": true, "IsDir": true, "Sys": true, "Stat": true, "Open": true, "ReadDir": true, "Info": true, "Type": true,
}

type summary struct {
	pkg   string            // package の行
	head  string            // package・build タグ・//go: の指示・_ と . の import
	decls map[string]string // const・var・type の名前 → その宣言
	funcs map[string]string // 関数の名前 → 中身(シグネチャを含む)
}

func summarize(src []byte) (summary, bool) {
	s := summary{decls: map[string]string{}, funcs: map[string]string{}}
	if src == nil {
		return s, true
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		return summary{}, false
	}
	var head strings.Builder
	s.pkg = fmt.Sprintf("package %s\n", f.Name.Name)
	head.WriteString(s.pkg)
	for _, cg := range f.Comments { // build タグと //go: の指示(go:embed・go:linkname など)
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:") || strings.HasPrefix(c.Text, "// +build") {
				fmt.Fprintf(&head, "%s\n", c.Text)
			}
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				for _, sp := range d.Specs { // 名前を付けた import の違いは、使う関数の中身の違いに出る
					is := sp.(*ast.ImportSpec)
					if is.Name != nil && (is.Name.Name == "_" || is.Name.Name == ".") {
						fmt.Fprintf(&head, "import %s %s\n", is.Name.Name, is.Path.Value)
					}
				}
				continue
			}
			for i, sp := range d.Specs {
				// const の並びは iota と、型・値の省略で前の行に左右されるので、並び全体を各名前の宣言とみなす
				text := render(fset, sp)
				if d.Tok == token.CONST {
					text = fmt.Sprintf("%d:%s", i, render(fset, d))
				}
				for _, n := range specNames(sp) {
					if n == "_" {
						return summary{}, false // var _ = ... は名前で追えない
					}
					if _, dup := s.decls[n]; dup {
						return summary{}, false
					}
					s.decls[n] = d.Tok.String() + " " + text
				}
			}
		case *ast.FuncDecl:
			name, ok := funcName(d)
			if !ok {
				return summary{}, false
			}
			if _, dup := s.funcs[name]; dup {
				return summary{}, false
			}
			fd := *d
			fd.Doc = nil
			s.funcs[name] = render(fset, &fd)
		default:
			return summary{}, false
		}
	}
	s.head = head.String()
	return s, true
}

func specNames(sp ast.Spec) []string {
	switch sp := sp.(type) {
	case *ast.ValueSpec:
		var ns []string
		for _, n := range sp.Names {
			ns = append(ns, n.Name)
		}
		return ns
	case *ast.TypeSpec:
		return []string{sp.Name.Name}
	}
	return nil
}

// funcName は go tool covdata func と同じ形の名前。init・型引数のあるものは ok = false
func funcName(d *ast.FuncDecl) (string, bool) {
	if d.Name.Name == "init" || d.Name.Name == "_" || d.Type.TypeParams != nil {
		return "", false
	}
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name, true
	}
	t := d.Recv.List[0].Type
	ptr := ""
	if st, ok := t.(*ast.StarExpr); ok {
		ptr, t = "*", st.X
	}
	id, ok := t.(*ast.Ident)
	if !ok { // T[P] など
		return "", false
	}
	return ptr + id.Name + "." + d.Name.Name, true
}

// render は字句の並びにする(空白・改行・コメントの違いを消す)。) と } の前の ; は省けるので落とす
func render(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := (&printer.Config{Mode: printer.RawFormat}).Fprint(&buf, fset, n); err != nil {
		return fmt.Sprintf("!%v", err)
	}
	src := buf.Bytes()
	var sc scanner.Scanner
	sc.Init(token.NewFileSet().AddFile("", -1, len(src)), src, nil, 0)
	var toks []string
	for {
		_, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if (tok == token.RPAREN || tok == token.RBRACE) && len(toks) > 0 && toks[len(toks)-1] == ";" {
			toks = toks[:len(toks)-1]
		}
		if tok == token.SEMICOLON {
			lit = ";"
		} else if lit == "" {
			lit = tok.String()
		}
		toks = append(toks, lit)
	}
	return strings.Join(toks, " ")
}

// usersOf は、変わった名前を使う関数を root の下の Go のファイル(*_test.go・testdata・vendor・. や _ で始まるディレクトリを除く)から探す。
// pkgNames は「パッケージ(モジュールからのディレクトリ)<TAB>名前」(パッケージの const・var・type・関数)、methods はメソッドの名前。
// パッケージの名前は、同じパッケージの中では字面の名前、ほかのパッケージからは <import の名前>.<名前> で突き合わせる。
// メソッドの名前は、どこでも字面で突き合わせる(値の型は字面に出ないので)。
// 名前を使う const・var・type も変わったものとして、増えなくなるまで広げる。「ファイル<TAB>関数」を返す(ファイルは root からの相対)
func usersOf(root string, pkgNames, methods map[string]bool) ([]string, error) {
	type node struct {
		key   string   // 関数なら「ファイル<TAB>関数」、const・var・type なら空
		pkg   string   // パッケージ(モジュールからのディレクトリ)
		names []string // const・var・type の名前
		refs  map[string]bool
		ids   map[string]bool
	}
	var nodes []node
	mod, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			b := e.Name()
			if p != root && (b == "testdata" || b == "vendor" || strings.HasPrefix(b, ".") || strings.HasPrefix(b, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		pkg := pathDir(rel)
		imports := map[string]string{} // import の名前 → パッケージ(モジュールの中だけ)
		for _, is := range f.Imports {
			ip, _ := strconv.Unquote(is.Path.Value)
			if ip != mod && !strings.HasPrefix(ip, mod+"/") {
				continue
			}
			dir := strings.TrimPrefix(strings.TrimPrefix(ip, mod), "/")
			if dir == "" {
				dir = "."
			}
			name := ip[strings.LastIndex(ip, "/")+1:]
			if is.Name != nil {
				name = is.Name.Name
			} else if n, err := packageName(filepath.Join(root, dir)); err == nil {
				name = n
			}
			imports[name] = dir
		}
		refsIn := func(n ast.Node) (map[string]bool, map[string]bool) {
			refs, ids := map[string]bool{}, map[string]bool{}
			ast.Inspect(n, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := n.X.(*ast.Ident); ok {
						if dir, ok := imports[x.Name]; ok {
							refs[dir+"\t"+n.Sel.Name] = true
						}
					}
				case *ast.Ident:
					refs[pkg+"\t"+n.Name] = true
					ids[n.Name] = true
				}
				return true
			})
			return refs, ids
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				key := rel + "\t*"
				if name, ok := funcName(d); ok {
					key = rel + "\t" + name
				}
				refs, ids := refsIn(d)
				nodes = append(nodes, node{key: key, pkg: pkg, refs: refs, ids: ids})
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				for _, sp := range d.Specs {
					refs, ids := refsIn(sp)
					nodes = append(nodes, node{pkg: pkg, names: specNames(sp), refs: refs, ids: ids})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	uses := func(n node) bool { return anyIn(n.refs, pkgNames) || anyIn(n.ids, methods) }
	for grew := true; grew; {
		grew = false
		for _, n := range nodes {
			if n.key != "" || !uses(n) {
				continue
			}
			for _, name := range n.names {
				if k := n.pkg + "\t" + name; !pkgNames[k] {
					pkgNames[k], grew = true, true
				}
			}
		}
	}
	var out []string
	for _, n := range nodes {
		if n.key != "" && uses(n) {
			out = append(out, n.key)
		}
	}
	return out, nil
}

func pathDir(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return "."
}

func modulePath(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`), nil
		}
	}
	return "", fmt.Errorf("go.mod に module がない")
}

var pkgNameCache = map[string]string{}

// packageName はディレクトリの Go のパッケージの名前(*_test.go でないファイルの package)
func packageName(dir string) (string, error) {
	if n, ok := pkgNameCache[dir]; ok {
		return n, nil
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range es {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, n), nil, parser.PackageClauseOnly)
			if err == nil {
				pkgNameCache[dir] = f.Name.Name
				return f.Name.Name, nil
			}
		}
	}
	return "", fmt.Errorf("%s に Go のファイルがない", dir)
}

func anyIn(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
