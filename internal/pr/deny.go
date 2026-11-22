package pr

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 公開前検査(deny-patterns)。run が PR を作る前に、題・本文・コミットメッセージ・ブランチ名・差分(パスと足した行)に掛ける。
// 規則ファイル: 1 行 = "<名前> <i|s> <パターン(ERE)>"。例外ファイル: 1 行 = "<規則名> <ERE>"。設定は固定の config/pr-queue.conf だけ。

type denyRule struct{ name, flag, pat string }
type denyEx struct{ name, pat string }

// Deny は読み込んだ規則と例外。
type Deny struct {
	rules []denyRule
	exs   []denyEx
}

func absFile(stderr io.Writer, label, p string) bool {
	st, err := os.Stat(p)
	ok := strings.HasPrefix(p, "/") && err == nil && st.Mode().IsRegular()
	if ok {
		f, err := os.Open(p)
		ok = err == nil
		if ok {
			f.Close()
		}
	}
	if !ok {
		fmt.Fprintf(stderr, "pr-queue: %s が絶対パスの読めるファイルではないため止めます(fail-closed): %s\n", label, p)
	}
	return ok
}

var reRuleName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// fileLines は while IFS= read -r line || [[ -n $line ]] の行(末尾の \r は呼ぶ側で落とす)。
func fileLines(b []byte) []string {
	s := string(b)
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func blankOrComment(l string) bool {
	t := strings.TrimLeft(l, " \t\n\v\f\r")
	return t == "" || strings.HasPrefix(t, "#")
}

// readFields は read -r a b c <<<"$line"(空白・タブで分け、最後に残りを前後の空白を落として入れる)。
func readFields(l string, n int) []string {
	out := make([]string, n)
	s := strings.Trim(l, " \t\n")
	for i := 0; i < n; i++ {
		if i == n-1 {
			out[i] = strings.TrimRight(s, " \t\n")
			break
		}
		j := strings.IndexAny(s, " \t\n")
		if j < 0 {
			out[i], s = s, ""
			continue
		}
		out[i] = s[:j]
		s = strings.TrimLeft(s[j:], " \t\n")
	}
	return out
}

// LoadDeny は pq_deny_load。読めない・不正・規則 0 件なら理由を stderr に出して false(fail-closed)。
func LoadDeny(stderr io.Writer) (*Deny, bool) {
	if os.Getenv("PR_QUEUE_DENY_PATTERNS")+os.Getenv("PR_QUEUE_DENY_EXCEPTIONS") != "" {
		fmt.Fprintln(stderr, "pr-queue: 警告: PR_QUEUE_DENY_PATTERNS / PR_QUEUE_DENY_EXCEPTIONS は無視します(環境変数では決めません)。config/pr-queue.conf の deny_patterns / deny_exceptions を使います")
	}
	f := denyConf("deny_patterns")
	if f == "" {
		fmt.Fprintln(stderr, "pr-queue: 公開前検査の規則ファイルが指定されていないため止めます(fail-closed)。config/pr-queue.conf の deny_patterns に絶対パスを指定してください")
		return nil, false
	}
	if !absFile(stderr, "公開前検査の規則ファイル(deny_patterns)", f) {
		return nil, false
	}
	d := &Deny{}
	b, _ := os.ReadFile(f)
	n, bad := 0, false
	for _, l := range fileLines(b) {
		l = strings.TrimSuffix(l, "\r")
		if blankOrComment(l) {
			continue
		}
		x := readFields(l, 3)
		n++
		name, flag, pat := x[0], x[1], x[2]
		if !reRuleName.MatchString(name) || (flag != "i" && flag != "s") || pat == "" {
			fmt.Fprintf(stderr, "pr-queue: 規則ファイルの %d 番目の規則の書式が不正です(<名前> <i|s> <パターン>)\n", n)
			bad = true
			continue
		}
		if !ereValid(pat) {
			fmt.Fprintf(stderr, "pr-queue: 規則ファイルの規則 %s の正規表現が不正です\n", name)
			bad = true
			continue
		}
		d.rules = append(d.rules, denyRule{name, flag, pat})
	}
	if bad || len(d.rules) == 0 {
		fmt.Fprintln(stderr, "pr-queue: 公開前検査の規則ファイルが不正か、規則が 0 件のため止めます(fail-closed)")
		return nil, false
	}
	if x := denyConf("deny_exceptions"); x != "" {
		if !absFile(stderr, "公開前検査の例外ファイル(deny_exceptions)", x) {
			return nil, false
		}
		b, _ := os.ReadFile(x)
		n := 0
		for _, l := range fileLines(b) {
			l = strings.TrimSuffix(l, "\r")
			if blankOrComment(l) {
				continue
			}
			f := readFields(l, 2)
			n++
			name, pat := f[0], f[1]
			if !reRuleName.MatchString(name) || pat == "" || !ereValid(pat) {
				fmt.Fprintf(stderr, "pr-queue: 例外ファイルの %d 番目の書式が不正です(<規則名> <ERE>)\n", n)
				return nil, false
			}
			if ereMatch(pat, "\n", false) {
				fmt.Fprintf(stderr, "pr-queue: 例外ファイルの %d 番目が空文字列に合います(何にでも合う例外は許しません)\n", n)
				return nil, false
			}
			d.exs = append(d.exs, denyEx{name, pat})
		}
	}
	return d, true
}

// units は検査する単位: pos(位置)の行と txt(内容)の行を同じ行番号で並べる(bash と同じく、txt は書いたバイトをそのまま足す)。
type units struct {
	pos []string
	txt bytes.Buffer
}

func (u *units) add(pos, line string) {
	u.pos = append(u.pos, pos)
	u.txt.WriteString(line + "\n")
}

// addAwk は pq_deny_unit: awk で 1 行ずつ(最後の改行が無い行も 1 行)。
func (u *units) addAwk(prefix string, s string) {
	if s == "" {
		return
	}
	for i, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		u.add(prefix+":"+strconv.Itoa(i+1), l)
	}
}

func rawOut(bin string, args ...string) (string, error) {
	b, err := exec.Command(bin, args...).Output()
	return string(b), err
}

var reHunk = regexp.MustCompile(`\+[0-9]+`)

// check は pq_deny_check: ヒットなし 0 / ヒットあり 1(規則名@位置だけ)/ 検査できない 2。
func (d *Deny) check(r *Row, base string) (string, int) {
	dir := repoDir()
	ref := "refs/remotes/origin/" + r.Branch
	if !hasCommit(ref) {
		return "公開前検査ができない(ブランチの追跡参照が clone に無い)", 2
	}
	td, err := os.MkdirTemp("", "pq-deny")
	if err != nil {
		return "公開前検査ができない(作業領域)", 2
	}
	defer os.RemoveAll(td)
	u := &units{}
	u.add("title", r.Title)
	u.add("branch", r.Branch)
	body, _ := os.ReadFile(r.BodyFile)
	u.addAwk("body", string(body))
	revs, err := out(gitBin(), "-C", dir, "rev-list", "origin/"+base+".."+ref)
	if err != nil {
		return "公開前検査ができない(コミットを取得できない)", 2
	}
	for _, c := range lines(revs) {
		if c == "" {
			continue
		}
		short := c
		if len(short) > 7 {
			short = short[:7]
		}
		msg, _ := rawOut("git", "-C", dir, "log", "-1", "--format=%B", c)
		u.addAwk("commit "+short, msg)
		a, _ := rawOut("git", "-C", dir, "log", "-1", "--format=%an <%ae>", c)
		u.pos = append(u.pos, "author "+short)
		u.txt.WriteString(a)
		cm, _ := rawOut("git", "-C", dir, "log", "-1", "--format=%cn <%ce>", c)
		u.pos = append(u.pos, "committer "+short)
		u.txt.WriteString(cm)
	}
	names, err := rawOut("git", "-C", dir, "-c", "core.quotePath=false", "diff", "-z", "--name-only", "origin/"+base+"..."+ref)
	if err != nil {
		return "公開前検査ができない(差分を取得できない)", 2
	}
	parts := strings.Split(names, "\x00")
	for i, f := range parts[:len(parts)-1] { // read -d ” は NUL で終わる分だけ
		u.add("path#"+strconv.Itoa(i+1), f)
	}
	diff, _ := rawOut("git", "-C", dir, "-c", "core.quotePath=false", "diff", "-U0", "--no-color", "origin/"+base+"..."+ref)
	hdr, fn, n := false, 0, 0
	if diff != "" {
		for _, l := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
			switch {
			case strings.HasPrefix(l, "diff --git "):
				hdr = true
				fn++
			case hdr && (strings.HasPrefix(l, "+++ ") || strings.HasPrefix(l, "--- ")):
			case strings.HasPrefix(l, "@@"):
				hdr = false
				n = 0
				if m := reHunk.FindString(l); m != "" {
					n, _ = strconv.Atoi(m[1:])
				}
			case !hdr && strings.HasPrefix(l, "+"):
				u.add("diff#"+strconv.Itoa(fn)+":"+strconv.Itoa(n), l[1:])
				n++
			}
		}
	}
	txt := filepath.Join(td, "txt")
	if err := os.WriteFile(txt, u.txt.Bytes(), 0o600); err != nil {
		return "公開前検査ができない(作業領域)", 2
	}
	hits, nh := "", 0
	for _, rule := range d.rules {
		gf := "-noE"
		if rule.flag == "i" {
			gf = "-noEi"
		}
		o, _ := out("grep", gf, "--", rule.pat, txt)
		for _, l := range lines(o) {
			if l == "" {
				continue
			}
			ln, frag, _ := strings.Cut(l, ":")
			ex := false
			for _, e := range d.exs {
				if e.name == rule.name && ereMatch(e.pat, frag, rule.flag == "i") {
					ex = true
					break
				}
			}
			if ex {
				continue
			}
			pos := ""
			if k, err := strconv.Atoi(ln); err == nil && k >= 1 && k <= len(u.pos) {
				pos = u.pos[k-1]
			}
			key := rule.name + "@" + pos
			if strings.Contains(hits, key) {
				continue
			}
			nh++
			if nh <= 5 {
				if hits != "" {
					hits += ", "
				}
				hits += key
			}
		}
	}
	if nh == 0 {
		return "", 0
	}
	more := ""
	if nh > 5 {
		more = " ほか"
	}
	return fmt.Sprintf("公開前検査にかかった(%d 件): %s%s", nh, hits, more), 1
}
