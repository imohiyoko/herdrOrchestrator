package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 層の検査(ADR 0008 の決定 4)。今ある違反は testdata/layers-allow.txt に載せ、減る方向にだけ動かす。
// 違反を減らしたら go test ./cmd/herdrctl -run TestLayers -update で書き直し、差分が減っているだけか確かめる。
var updateLayers = flag.Bool("update", false, "testdata/layers-allow.txt を今の違反で書き直す")

const mod = "github.com/imohiyoko/herdrorchestrator/"

// 入口のパッケージ。import してよい内部パッケージは internal/app/... と下の entryOK だけ。
var entries = []string{"internal/tbcli", "internal/webui"}

var entryOK = []string{"internal/kernel/env", "internal/kernel/identity"}

// herdrctl に入れない依存(imohiyoko/API の gRPC・YAML・契約の生成。ADR 0008 の 2a)。
var forbiddenDeps = []string{
	"google.golang.org/grpc", "google.golang.org/protobuf", "gopkg.in/yaml", "go.yaml.in/yaml", "sigs.k8s.io/yaml",
	"github.com/imohiyoko/API/cli", "github.com/imohiyoko/API/runtime/grpcx", "github.com/imohiyoko/API/manifest/yamlx",
}

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go が無い")
	}
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

func TestHerdrctlDeps(t *testing.T) {
	for _, p := range goList(t, "-deps", "./cmd/herdrctl") {
		for _, f := range forbiddenDeps {
			if p == f || strings.HasPrefix(p, f+"/") {
				t.Errorf("herdrctl が %s に依存しています(ADR 0008: grpc・protobuf・YAML・cli を入れない)", p)
			}
		}
	}
}

// layerViolations は今の違反(1 行 1 つ。"import <パッケージ> <import 先>" と "sql <ファイル> <数>")。
func layerViolations(t *testing.T) []string {
	var out []string
	lines := goList(t, "-f", "{{.ImportPath}}:{{join .Imports \",\"}}", "./internal/...")
	for _, l := range lines {
		pkg, imps, _ := strings.Cut(l, ":")
		pkg = strings.TrimPrefix(pkg, mod)
		for _, imp := range strings.Split(imps, ",") {
			if !strings.HasPrefix(imp, mod+"internal/") {
				continue
			}
			imp = strings.TrimPrefix(imp, mod)
			switch {
			case slices.Contains(entries, pkg):
				if imp == "internal/app" || strings.HasPrefix(imp, "internal/app/") || slices.Contains(entryOK, imp) {
					continue
				}
				out = append(out, "import "+pkg+" "+imp)
			case pkg == "internal/app" || strings.HasPrefix(pkg, "internal/app/"):
				if slices.Contains(entries, imp) {
					t.Errorf("%s が入口 %s を import しています(ADR 0008: API は入口を import しない)", pkg, imp)
				}
			}
		}
	}
	out = append(out, sqlCounts(t)...)
	sort.Strings(out)
	return out
}

var reSQL = regexp.MustCompile(`\b(SELECT|INSERT|UPDATE|DELETE)\b`)

// sqlCounts は入口のファイル(_test.go を除く)ごとの SQL の数("sql <ファイル> <数>")。
func sqlCounts(t *testing.T) []string {
	var out []string
	for _, e := range entries {
		files, err := filepath.Glob(filepath.Join("..", "..", filepath.FromSlash(e), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(reSQL.FindAll(b, -1)); n > 0 {
				out = append(out, fmt.Sprintf("sql %s/%s %d", e, filepath.Base(f), n))
			}
		}
	}
	return out
}

func TestLayers(t *testing.T) {
	path := filepath.Join("testdata", "layers-allow.txt")
	got := layerViolations(t)
	if *updateLayers {
		body := "# ADR 0008 の層の検査の許容リスト(cmd/herdrctl/layers_test.go)。減る方向にだけ動かす。\n" +
			"# 書き直し: go test ./cmd/herdrctl -run TestLayers -update\n" + strings.Join(got, "\n") + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allowed := map[string]int{} // import の行は 1、sql の行は数
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, n := sqlKey(l)
		allowed[k] = n
	}
	seen := map[string]bool{}
	for _, l := range got {
		k, n := sqlKey(l)
		seen[k] = true
		lim, ok := allowed[k]
		switch {
		case !ok:
			t.Errorf("新しい違反: %s(ADR 0008 の決定 4。入口は internal/app を通す)", l)
		case n > lim:
			t.Errorf("違反が増えました: %s(許容 %d)", l, lim)
		case n < lim:
			t.Errorf("違反が減りました: %s(許容 %d)。go test ./cmd/herdrctl -run TestLayers -update で許容リストを減らす", l, lim)
		}
	}
	for k := range allowed {
		if !seen[k] {
			t.Errorf("もう無い違反が許容リストに残っています: %s(-update で消す)", k)
		}
	}
}

// sqlKey は許容リストの行を、比べる鍵と数にする(import の行は数 1)。
func sqlKey(l string) (string, int) {
	if rest, ok := strings.CutPrefix(l, "sql "); ok {
		if i := strings.LastIndexByte(rest, ' '); i > 0 {
			if n, err := strconv.Atoi(rest[i+1:]); err == nil {
				return "sql " + rest[:i], n
			}
		}
	}
	return l, 1
}
