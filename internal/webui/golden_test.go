package webui

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// 期待値のファイル(testdata/*.json)。Python 版を消す前に、Python 版と同じ出力であることを確かめたうえで書き出したもの。
// 表示を意図して変えたときは go test ./internal/webui -run <テスト名> -update で書き直し、差分を見て確かめる。
var update = flag.Bool("update", false, "testdata の期待値を書き直す")

// golden は got を JSON にして testdata/<name>.json と比べる(-update なら書き直す)。
func golden(t *testing.T, name string, got any) {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(got); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join("testdata", name+".json")
	if *update {
		if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v(-update で作る)", err)
	}
	if bytes.Equal(b.Bytes(), want) {
		return
	}
	// 表(キーごと)なら違うキーだけを出す
	var gm, wm map[string]any
	if json.Unmarshal(b.Bytes(), &gm) == nil && json.Unmarshal(want, &wm) == nil {
		for k, g := range gm {
			gs, _ := json.Marshal(g)
			ws, _ := json.Marshal(wm[k])
			if string(gs) != string(ws) {
				t.Errorf("%s の %s が違う:\n%s", p, k, firstDiff(string(gs), string(ws)))
			}
		}
		for k := range wm {
			if _, ok := gm[k]; !ok {
				t.Errorf("%s の %s が出ない", p, k)
			}
		}
		if !t.Failed() {
			t.Errorf("%s と違う(並びか書式)", p)
		}
	} else {
		t.Errorf("%s と違う:\n%s", p, firstDiff(b.String(), string(want)))
	}
	t.Log("意図した変更なら -update で書き直す")
}
