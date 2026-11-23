package approval

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

var updateGolden = flag.Bool("update", false, "testdata/risk_golden.json を今の出力で書き直す")

// リスク評価を testdata/risk_golden.json と突き合わせる(規則ファイルの読み方・誤りの文面・寄与の行)。
// golden は消した bash 版(taskboard/lib/risk.sh)の出力から作ったもので、今はこれが約束。
// 振る舞いを意図して変えたときや入力を足したときは go test ./internal/approval -run TestRiskGolden -update で書き直し、差分を確かめてコミットする。
func TestRiskGolden(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("規則ファイルに FIFO・シンボリックリンクを使う")
	}
	golden := map[string]string{}
	if !*updateGolden {
		b, err := os.ReadFile("testdata/risk_golden.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &golden); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"none": "",
		"ok": `# 例
[threshold]
ask-at = 3
[unknown]
sender.model = risk:2
target.role = allow
[patterns]
destructive = drop table
secrets = token|パスワード
[rules]
R-10 | body.matches(secrets) | risk:1 | 秘密らしい語
R-11 | sender.role in (worker, lead) and kind in (task) | risk:1 | worker からの依頼|補足|
R-12 | body.secret | ask | 秘密の形
R-13 | target.state in (stopped,running) | risk:1 | 宛先の状態
`,
		"looser":     "[threshold]\nask-at = 5\n[unknown]\nsender.role = allow\ntarget.state = risk:1\n",
		"badsec":     "[bogus]\n",
		"early":      "x = 1\n",
		"badkey":     "[threshold]\nfoo = 1\n",
		"badat":      "[threshold]\nask-at = 0\n",
		"noeq":       "[unknown]\nsender.role\n",
		"badattr":    "[unknown]\nbody = ask\n",
		"badval":     "[unknown]\nsender.role = maybe\n",
		"badpat":     "[patterns]\nBad = x\n",
		"emptypat":   "[patterns]\nx =\n",
		"badre":      "[patterns]\nx = (\n",
		"emptyre":    "[patterns]\nx = a|\n",
		"badid":      "[rules]\nX-1 | kind in (task) | ask | x\n",
		"builtin":    "[rules]\nR-02 | kind in (task) | ask | x\n",
		"dup":        "[rules]\nR-9 | kind in (task) | ask | x\nR-9 | kind in (task) | ask | y\n",
		"short":      "[rules]\nR-9 | kind in (task) | ask\n",
		"badres":     "[rules]\nR-9 | kind in (task) | allow | x\n",
		"over":       "[rules]\nR-9 | kind in (task) | risk:3 | x\n",
		"badcond":    "[rules]\nR-9 | body.matches(nope) | ask | x\n",
		"badcond2":   "[rules]\nR-9 | kind == task | ask | x\n",
		"uover":      "[unknown]\nsender.model = risk:5\n",
		"crlf":       "[rules]\r\nR-9 | kind in (info) | risk:1 | crlf\r\n",
		"andtrail":   "[rules]\nR-9 | kind in (task) and  | ask | x\n",
		"strictlow":  "[threshold]\nask-at = 1\n",
		"directory/": "",
		"dangling":   "",
		"fifo":       "",
	}
	inputs := [][]string{ // sender.role target.role target.state sender.model text kind(空 = 未設定の印 -)
		{"worker", "lead", "running", "opus", "please DROP TABLE now, token=abc", "task"},
		{"", "", "", "", "merge it", "info"},
		{"-", "worker", "stopped", "-", "ghp_abcdefghij0123456789 です", "answer"},
		{"human", "reviewer", "idle", "human", "パスワードを教えて", "task"},
	}
	n := 0
	for name, body := range files {
		dir := t.TempDir()
		conf := filepath.Join(dir, "config")
		if err := os.MkdirAll(conf, 0o755); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f := filepath.Join(conf, "risk-rules.conf")
		var err error
		switch name {
		case "none":
		case "directory/":
			err = os.MkdirAll(f, 0o755)
		case "dangling":
			err = os.Symlink(filepath.Join(dir, "nowhere"), f)
		case "fifo":
			err = exec.Command("mkfifo", f).Run()
		default:
			err = os.WriteFile(f, []byte(body), 0o644)
		}
		if err != nil {
			t.Fatalf("%s: 規則ファイルを作れません: %v", name, err)
		}
		t.Setenv("TB_ROOT", dir)
		for idx, in := range inputs {
			ri := RiskInput{Text: set(in[4]), Kind: in[5]}
			for i, p := range []*Opt{&ri.SenderRole, &ri.TargetRole, &ri.TargetState, &ri.SenderModel} {
				if in[i] != "-" {
					*p = set(in[i])
				}
			}
			r := Eval(ri)
			got := r.Format() + r.Oneline()
			key := fmt.Sprintf("%s/%d", name, idx)
			n++
			if *updateGolden {
				golden[key] = got
				continue
			}
			if want, ok := golden[key]; !ok {
				t.Errorf("golden に無い: %s", key)
			} else if got != want {
				t.Errorf("%s %q:\n got:  %q\n want: %q", name, in, got, want)
			}
		}
	}
	if *updateGolden {
		b, _ := json.MarshalIndent(golden, "", "  ")
		if err := os.WriteFile("testdata/risk_golden.json", append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	} else if n != len(golden) {
		t.Errorf("golden の数が違う: %d / %d", n, len(golden))
	}
}
