package approval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// taskboard/test/cases/81_risk.sh の評価そのものの確かめ(bash 版は lib/risk.sh を直接 source していた)。
// tb への組み込み(tb approval・tb send の reason)は 81_risk.sh に残す。

// rk は規則ファイルの本文(nil = 規則ファイルなし)と入力から Format() の全出力を返す。
// in の "-" は未設定(取れなかった)。
func rk(t *testing.T, rules *string, sr, tr, ts, sm, text string, kind ...string) string {
	t.Helper()
	dir := t.TempDir()
	if rules != nil {
		if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "risk-rules.conf"), []byte(*rules), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TB_ROOT", dir)
	in := RiskInput{}
	for i, p := range []*Opt{&in.SenderRole, &in.TargetRole, &in.TargetState, &in.SenderModel, &in.Text} {
		v := []string{sr, tr, ts, sm, text}[i]
		if v != "-" {
			*p = set(v)
		}
	}
	if len(kind) > 0 {
		in.Kind = kind[0]
	}
	return Eval(in).Format()
}

func head1(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	f := strings.Fields(l)
	if len(f) < 2 {
		return l
	}
	return f[0] + " " + f[1]
}

func line(s string, n int) string {
	ls := strings.Split(s, "\n")
	if n-1 < len(ls) {
		return ls[n-1]
	}
	return ""
}

func reasons(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if r, ok := strings.CutPrefix(l, "risk-reason: "); ok {
			out = append(out, r)
		}
	}
	return out
}

func rs(s string) string { return strings.Join(reasons(s), "|") }

func eqs(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s\n got:  %q\n want: %q", name, got, want)
	}
}

func sp(s string) *string { return &s }

var ok4 = []string{"conductor", "worker", "running", "opus"}

func rkOK(t *testing.T, rules *string, text string, kind ...string) string {
	return rk(t, rules, ok4[0], ok4[1], ok4[2], ok4[3], text, kind...)
}

func TestRiskBuiltin(t *testing.T) {
	out := rkOK(t, nil, "please read the docs")
	eqs(t, "clean input → allow 0/2", head1(out)+"|"+strings.Fields(line(out, 2))[0]+" "+strings.Fields(line(out, 2))[1], "risk: allow|risk-score: 0")
	eqs(t, "no reasons", rs(rkOK(t, nil, "hello")), "")
	for _, w := range []string{"git push origin main", "merge the PR", "use --force here", "then --approve it", "git reset --hard HEAD", "rm -rf build"} {
		out := rkOK(t, nil, w)
		r := rs(out)
		if len(r) > 9 {
			r = r[:9]
		}
		eqs(t, "R-02 "+w, head1(out)+"|"+r, "risk: ask|R-02 +ask")
	}
	eqs(t, "R-02 names the word", rs(rkOK(t, nil, "please Push it")), "R-02 +ask 本文に破壊的・公開系の操作語(語: Push)")
	eqs(t, "R-02 word boundary", head1(rkOK(t, nil, "pushed emerge mergeable")), "risk: allow")
	out = rk(t, nil, "conductor", "worker", "stopped", "opus", "hi")
	eqs(t, "R-06 stopped", rs(out)+"|"+head1(out), "R-06 +2 宛先が停止中|risk: ask")
	eqs(t, "unknown sender.role", rs(rk(t, nil, "", "worker", "running", "opus", "hi")), "unknown:sender.role +ask sender.role が不明(設定 ask)")
	eqs(t, "unknown target.role", head1(rk(t, nil, "conductor", "", "running", "opus", "hi")), "risk: ask")
	eqs(t, "unknown target.state", rs(rk(t, nil, "conductor", "worker", "", "opus", "hi")), "unknown:target.state +ask target.state が不明(設定 ask)")
	out = rk(t, nil, "conductor", "worker", "running", "", "hi")
	eqs(t, "unknown model = 1 point", head1(out)+"|"+rs(out), "risk: allow|unknown:sender.model +1 sender.model が不明(設定 risk:1)")
	eqs(t, "model unknown + stopped → 3", line(rk(t, nil, "conductor", "worker", "stopped", "", "hi"), 2), "risk-score: 3 / threshold 2")
	var ids []string
	for _, r := range reasons(rk(t, nil, "conductor", "worker", "running", "", "git push")) {
		f := strings.Fields(r)
		ids = append(ids, f[0]+" "+f[1])
	}
	eqs(t, "model + body word", strings.Join(ids, "|"), "unknown:sender.model +1|R-02 +ask")
	// R-07: 入力欠損(未設定。空とは別)
	names := []string{"sender.role", "target.role", "target.state", "sender.model", "body"}
	for i, n := range names {
		in := []string{"a", "b", "running", "m", "t"}
		in[i] = "-"
		out := rk(t, sp(""), in[0], in[1], in[2], in[3], in[4])
		eqs(t, "R-07 "+n, head1(out)+"|"+rs(out), "risk: ask|R-07 +ask 評価不能(fail-closed): 入力が取れません: "+n)
	}
	// 語境界は ASCII 限定: 日本語に隣接した語も検出する
	for _, txt := range []string{"mainにpushして", "それをmergeして", "pushしてください"} {
		eqs(t, "R-02 "+txt, head1(rkOK(t, nil, txt)), "risk: ask")
	}
	eqs(t, "R-02 ASCII neighbour", head1(rkOK(t, nil, "pushedして")), "risk: allow")
	nat := `R&D costs $5 > $3 & more; see ` + "`foo`" + ` (a|b) "quoted" it's <ok> 50% 〜 $(not run)`
	out = rkOK(t, nil, nat)
	eqs(t, "natural language", head1(out)+"|"+rs(out), "risk: allow|")
}

const goodConf = `[threshold]
ask-at = 2
[patterns]
external = slack|curl|https?://
destructive = drop table
[rules]
R-04 | body.matches(external) and target.role in (worker) | risk:1 | 外部送信を含む依頼
R-05 | sender.role in (worker) and target.role in (worker) | risk:1 | 横の連絡
R-03 | body.secret | ask | 秘密らしき文字列
R-08 | kind in (info) | risk:1 | info 種別
`

func TestRiskRulesFile(t *testing.T) {
	g := sp(goodConf)
	out := rkOK(t, g, "post to Slack")
	eqs(t, "R-04", rs(out)+"|"+head1(out), "R-04 +1 外部送信を含む依頼(語: Slack)|risk: allow")
	eqs(t, "R-04 non-worker", rs(rk(t, g, "conductor", "hr", "running", "opus", "post to Slack")), "")
	out = rk(t, g, "worker", "worker", "running", "opus", "curl it")
	eqs(t, "R-04 + R-05", head1(out)+"|"+line(out, 2), "risk: ask|risk-score: 2 / threshold 2")
	eqs(t, "R-05", rs(rk(t, g, "worker", "worker", "running", "opus", "hello")), "R-05 +1 横の連絡")
	eqs(t, "destructive extended", rs(rkOK(t, g, "DROP TABLE users")), "R-02 +ask 本文に破壊的・公開系の操作語(語: DROP TABLE)")
	key := "AK" + "IA" + "ABCDEFGHIJKLMNOP" // 実行時に組み立てる偽の値
	r := rs(rkOK(t, g, "key "+key+" end"))
	if strings.Contains(r, key) || !strings.HasPrefix(r, "R-03 +") {
		t.Errorf("R-03: %q", r)
	}
	eqs(t, "kind=info", rs(rkOK(t, g, "hi", "info")), "R-08 +1 info 種別")
	eqs(t, "kind=task", rs(rkOK(t, g, "hi", "task")), "")
	eqs(t, "kind unset", rs(rkOK(t, g, "hi")), "")
	a := rk(t, g, "worker", "worker", "stopped", "", "git push; curl http://x")
	b := rk(t, g, "worker", "worker", "stopped", "", "git push; curl http://x")
	eqs(t, "deterministic", a, b)
	var ids []string
	for _, r := range reasons(a) {
		ids = append(ids, strings.Fields(r)[0])
	}
	eqs(t, "fixed order", strings.Join(ids, " "), "unknown:sender.model R-02 R-06 R-04 R-05")
}

func TestRiskBrokenFile(t *testing.T) {
	bad := func(body, want string) {
		t.Helper()
		if r := rs(rkOK(t, sp(body), "hello")); !strings.Contains(r, want) {
			t.Errorf("%q: %q に %q が無い", body, r, want)
		}
	}
	bad("[nope]\n", "R-07 +ask 評価不能(fail-closed): L1: 未知の節")
	bad("x = 1\n", "R-07 +ask 評価不能(fail-closed): L1: ")
	for _, b := range []string{"[threshold]\nask-at = 0\n", "[unknown]\nfoo = ask\n", "[unknown]\nsender.model = risk:x\n", "[patterns]\nfoo = (\n",
		"[patterns]\nfoo = a*\n", "[rules]\nR-04 | body.secret | ask\n", "[rules]\nR-04 | body.contains(x) | ask | y\n",
		"[rules]\nR-04 | body.matches(nothing) | ask | y\n", "[rules]\nR-04 | body.secret | risk:9 | y\n",
		"[rules]\nR-04 | body.secret | ask | y\nR-04 | body.secret | ask | z\n", "[rules]\nR-02 | body.secret | ask | y\n"} {
		bad(b, "R-07 +ask")
	}
	eqs(t, "broken file score line", line(rkOK(t, sp("[nope]\n"), "hi"), 2), "risk-score: 0 / threshold 2")
	// 読めない場所(ディレクトリ)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "config", "risk-rules.conf"), 0o755)
	t.Setenv("TB_ROOT", dir)
	in := RiskInput{SenderRole: set("conductor"), TargetRole: set("worker"), TargetState: set("running"), SenderModel: set("opus"), Text: set("hi")}
	eqs(t, "directory", head1(Eval(in).Format()), "risk: ask")
}

func TestRiskLoosenStrict(t *testing.T) {
	loose := sp("[threshold]\nask-at = 5\n[unknown]\ntarget.role = allow\nsender.model = allow\n")
	eqs(t, "target.role=allow ignored", rs(rk(t, loose, "conductor", "", "running", "opus", "hi")), "unknown:target.role +ask target.role が不明(設定 ask)")
	var warns []string
	for _, l := range strings.Split(rkOK(t, loose, "hi"), "\n") {
		if w, ok := strings.CutPrefix(l, "risk-warn: 緩め設定は第 3 段まで無効: "); ok {
			warns = append(warns, w)
		}
	}
	eqs(t, "warns", strings.Join(warns, "|"), "[threshold] ask-at = 5(既定 2 のまま)|[unknown] target.role = allow(既定 ask のまま)|[unknown] sender.model = allow(既定 risk:1 のまま)")
	eqs(t, "sender.model=allow ignored", rs(rk(t, loose, "conductor", "worker", "running", "", "hi")), "unknown:sender.model +1 sender.model が不明(設定 risk:1)")
	eqs(t, "threshold stays 2", line(rkOK(t, loose, "hi"), 2), "risk-score: 0 / threshold 2")
	strict := sp("[threshold]\nask-at = 1\n[unknown]\nsender.model = ask\n")
	eqs(t, "strict", rs(rk(t, strict, "conductor", "worker", "running", "", "hi"))+"|"+line(rkOK(t, strict, "hi"), 2),
		"unknown:sender.model +ask sender.model が不明(設定 ask)|risk-score: 0 / threshold 1")
}

func TestRiskRelax(t *testing.T) {
	relax := sp("[threshold]\nask-at = 5\nrelax = on\n[unknown]\ntarget.role = allow\n")
	var warns []string
	for _, l := range strings.Split(rkOK(t, relax, "hi"), "\n") {
		if w, ok := strings.CutPrefix(l, "risk-warn: 緩め設定が有効(relax = on): "); ok {
			warns = append(warns, w)
		}
	}
	eqs(t, "relax: the looser values are used and always named", strings.Join(warns, "|"), "[threshold] ask-at = 5(既定 2)|[unknown] target.role = allow(既定 ask)")
	eqs(t, "relax: threshold 5", line(rkOK(t, relax, "hi"), 2), "risk-score: 0 / threshold 5")
	eqs(t, "relax: target.role=allow is no longer ask", head1(rk(t, relax, "conductor", "", "running", "opus", "hi")), "risk: allow")
	off := sp("[threshold]\nask-at = 5\nrelax = off\n")
	eqs(t, "relax = off: same as the default", line(rkOK(t, off, "hi"), 2), "risk-score: 0 / threshold 2")
	bad := sp("[threshold]\nrelax = yes\n")
	if !strings.Contains(rkOK(t, bad, "hi"), "relax は on | off です") {
		t.Error("relax: bad value is not fail-closed")
	}
}
