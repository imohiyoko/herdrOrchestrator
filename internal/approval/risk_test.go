package approval

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// リスク評価を bash の risk.sh と突き合わせる(規則ファイルの読み方・誤りの文面・寄与の行)。
func TestRiskMatchesBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash の risk.sh と比べる")
	}
	if out, err := exec.Command("bash", "-c", `echo "${BASH_VERSINFO[0]}"`).Output(); err != nil || len(out) == 0 || out[0] < '4' || out[0] > '9' {
		t.Skipf("bash 4 以上が無い(%q。tb の bash 版は 4 以上が前提)", out)
	}
	lib, _ := filepath.Abs("../../taskboard/lib/risk.sh")
	if _, err := os.Stat(lib); err != nil {
		t.Skip("risk.sh が無い")
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
		for _, in := range inputs {
			ri := RiskInput{Text: set(in[4]), Kind: in[5]}
			for i, p := range []*Opt{&ri.SenderRole, &ri.TargetRole, &ri.TargetState, &ri.SenderModel} {
				if in[i] != "-" {
					*p = set(in[i])
				}
			}
			r := Eval(ri)
			got := r.Format() + r.Oneline()
			var sh strings.Builder
			sh.WriteString("source " + shq(lib) + "\nsource " + shq(filepath.Join(filepath.Dir(lib), "notify.sh")) + " 2>/dev/null || true\n")
			sh.WriteString("RISK_RULES=" + shq(f) + "\n")
			sh.WriteString("unset RISK_IN_SENDER_ROLE RISK_IN_TARGET_ROLE RISK_IN_TARGET_STATE RISK_IN_SENDER_MODEL\n") // 親の環境から入らないように
			for i, v := range []string{"RISK_IN_SENDER_ROLE", "RISK_IN_TARGET_ROLE", "RISK_IN_TARGET_STATE", "RISK_IN_SENDER_MODEL"} {
				if in[i] != "-" {
					sh.WriteString(v + "=" + shq(in[i]) + "\n")
				}
			}
			sh.WriteString("RISK_IN_TEXT=" + shq(in[4]) + "; RISK_IN_KIND=" + shq(in[5]) + "\nrisk_eval; risk_format; risk_oneline\n")
			out, err := exec.Command("bash", "-c", sh.String()).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: bash: %v\n%s", name, err, out)
			}
			if got != string(out) {
				t.Errorf("%s %q:\n go:   %q\n bash: %q", name, in, got, out)
			}
		}
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
