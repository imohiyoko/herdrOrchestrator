package webui

import (
	"encoding/json"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// renderMD が markdown.render(Python)と同じ HTML を返すか。手で書いた例と、記法の部品を混ぜた例で比べる。
func TestRenderMDMatchesPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 が無い")
	}
	webui, _ := filepath.Abs("../../webui")
	cases := []string{
		"", "plain", "# h1", "###### h6", "####### h7", "#nospace", "  ## h2 ##  ", "# ", "#\tx #",
		"**b** *i* ~~s~~ `c` __u__", "***x***", "**a**b**", "* not list*", "a*b*c", "*a*b", "_*x*_", "**\nx**",
		"** x**", "**x **", "*x *", "* x*", "~~ a~~", "~~a ~~", "__a__b", "x__a__", "**`c`**", "*`c`*",
		"[t](https://e.com) [t2](ftp://x) ![img](https://e.com/a.png) ![](x)", "[](https://e.com)", `[t](https://e.com "title")`,
		"[a](https://e.com?x=1&y=2)", "see https://a.b/c?d=1 and x=https://no and 'https://no' ahttps://no", "<https://x.y>",
		"`https://in.code`", "a & b < c > d \" e ' f", "<script>alert(1)</script>", "line1\nline2\n\npara2",
		"---", "***", "___", " - - -", "    ---", "--", "-*-", "*\t*\t*", "_ _ _ _",
		"- a\n- b\n  - c\n    - d\n- [ ] t\n- [x] done\n- [X] D", "1. a\n2. b\n10) c", "- a\ncont\n  cont2", "+ p\n* q",
		"\t- tab\n\t\t- tab2", "-no", "1.no", "1234567890. big", "١. arabic",
		"> q\n> > nested\n>no space", "  > indented", "> # h\n> - l",
		"| a | b |\n|---|:-:|\n| 1 | 2 |\n| 3 |", "a|b\n-|-\nc|d\n\ne", "|x|\n|-|", "a | b\n| :--- |", "a|b\n|  |",
		"```\ncode <b>\n```", "```mermaid\ngraph TD; A-->B\n```", "~~~py\nx\n~~~", "```MERMAID\nx\n```", "```\nunclosed",
		"```` \nx\n````", "```py x\ny", "  ```\nx\n  ```", "```\nx\n``", "~~~\n```\n~~~",
		"a\r\nb\rc", "\x1b[31mred\x1b[0m", "x\x00y", "全角　スペース", "**太字**と*斜体*", "*日本*語", "語*日本*",
		"Σ **x**", "- a\n\n- b", "# h\npara\n- l\n> q\n---\n|a|b|\n|-|-|",
	}
	parts := []string{"*", "**", "_", "__", "~~", "`", "[", "]", "(", ")", "!", "https://e.x/", "a", "b", " ", "\n", "\t", "#", "-",
		"1.", ">", "|", "&", "<", "\"", "'", "=", "/", "x", "日", "  ", "```", "---", "[ ]", "[x]", ":"}
	rnd := rand.New(rand.NewSource(1))
	for range 1500 {
		var b strings.Builder
		for range 1 + rnd.Intn(14) {
			b.WriteString(parts[rnd.Intn(len(parts))])
		}
		cases = append(cases, b.String())
	}
	in, _ := json.Marshal(cases)
	c := exec.Command(py, "-c", "import json,sys\nsys.path.insert(0, sys.argv[1])\nfrom app.markdown import render\n"+
		"print(json.dumps([render(x) for x in json.load(sys.stdin)]))", webui)
	c.Stdin = strings.NewReader(string(in))
	c.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for i, x := range cases {
		if got := renderMD(x); got != want[i] {
			if bad++; bad <= 20 {
				t.Errorf("renderMD(%q)\n go: %q\n py: %q", x, got, want[i])
			}
		}
	}
}
