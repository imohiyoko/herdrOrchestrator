package gh

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// tb git pull|fetch|push|clone|ls-remote: GitHub のリモートへの git の操作(認証が要るものだけ)。ローカルの操作は git を直接使う。
// 操作名は git:<サブコマンド>(force・削除を含む push は git:push-force)、対象はリモートの owner/repo。
// 対象は、設定したコマンド自身に聞いた URL(insteadOf / pushInsteadOf を展開したもの)から決める。PATH 上の git には聞かない。

// gitRead は読みの操作(github.conf の git_read を引く)。
var gitRead = map[string]bool{"fetch": true, "pull": true, "ls-remote": true, "clone": true}

var gitSubs = map[string]bool{"fetch": true, "pull": true, "push": true, "ls-remote": true, "clone": true}

const GitUsage = `tb git fetch|pull|ls-remote <リモート名|URL> [引数...]   GitHub のリモートから取る(git:fetch / git:pull / git:ls-remote。既定 allow)
tb git clone <URL> [ディレクトリ] [引数...]   clone する(git:clone。既定 allow)
tb git push <リモート名> [引数...]   push する(git:push。既定 ask。--force・削除・+refspec・--mirror は git:push-force で既定 deny)
対象はリモートの owner/repo(github.com だけ)。リモート名か URL は 1 つ目に必ず書く(既定のリモートには落とさない)。
GitHub へ出るコマンドは config/github.conf の git_<サブコマンド> → git_read / git_write → git(絶対パス)。taskboard/docs/gh.md
`

// gitDenied は受けない引数(リモート側・手元で任意のコマンドを動かせるもの、設定・テンプレートを差し込めるもの)。
var gitDenied = []string{"--upload-pack", "--receive-pack", "--exec", "--template", "--config", "--separate-git-dir", "--server-option"}

var (
	reGHURL = []*regexp.Regexp{
		regexp.MustCompile(`^https://(?:[^@/]+@)?github\.com/([^/]+/[^/]+?)(?:\.git)?/?$`),
		regexp.MustCompile(`^ssh://(?:[^@/]+@)?github\.com(?::22)?/([^/]+/[^/]+?)(?:\.git)?/?$`),
		regexp.MustCompile(`^(?:[^@/:]+@)?github\.com:([^/]+/[^/]+?)(?:\.git)?/?$`),
	}
	reRemoteName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
)

// RepoOfURL は GitHub の URL から owner/repo(github.com 以外・形が違えば "")。
func RepoOfURL(u string) string {
	for _, re := range reGHURL {
		if m := re.FindStringSubmatch(u); m != nil && ValidRepo(m[1]) {
			return m[1]
		}
	}
	return ""
}

// isForcePush は force・削除を含む push か(-f / --force* / --delete / -d / --mirror / --prune / +refspec / :ref。短い形の組み合わせも)。
func isForcePush(args []string) bool {
	for _, a := range args[1:] {
		switch {
		case a == "--":
			continue
		case strings.HasPrefix(a, "--force"), a == "--delete", a == "--mirror", a == "--prune":
			return true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-"):
			if strings.ContainsAny(a[1:], "fd") {
				return true
			}
		case strings.HasPrefix(a, "+"), strings.HasPrefix(a, ":"), strings.Contains(a, ":+"):
			return true
		}
	}
	return false
}

// Git は tb git <サブコマンド> ...。
func (s *Svc) Git(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(s.W, GitUsage)
		return &Err{2, ""}
	}
	sub := args[0]
	switch {
	case sub == "help" || sub == "-h" || sub == "--help":
		fmt.Fprint(s.W, GitUsage)
		return nil
	case !gitSubs[sub]:
		return usage("git: %s は扱いません(fetch / pull / push / clone / ls-remote。ローカルの操作は git を直接使う)", sub)
	}
	rest := args[1:]
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		return usage("git %s: 1 つ目にリモート名か URL を書いてください(例: tb git %s origin ...)", sub, sub)
	}
	for _, a := range rest {
		for _, d := range gitDenied {
			if a == d || strings.HasPrefix(a, d+"=") {
				return usage("git %s: %s は受けません", sub, d)
			}
		}
		if sub == "clone" && (a == "-c" || a == "-u" || strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--")) {
			return usage("git clone: -c / -u は受けません")
		}
	}
	remote := rest[0]
	op := "git:" + sub
	if sub == "push" {
		if !reRemoteName.MatchString(remote) {
			return usage("git push: 1 つ目はリモート名(origin など。URL は受けない)")
		}
		if isForcePush(args) {
			op = "git:push-force"
		}
	} else if !reRemoteName.MatchString(remote) && RepoOfURL(remote) == "" {
		return usage("git %s: リモート名か github.com の URL を書いてください: %s", sub, remote)
	}
	repo, err := s.gitTarget(op, sub, remote)
	if err != nil {
		return err
	}
	what := "tb git " + sub
	if err := s.Authorize(op, repo, what); err != nil {
		return err
	}
	return s.gitRun(op, repo, args)
}

// gitArgs は git に付ける引数(フックと fsmonitor を切る。agent が書いたフックに token を見せない)。
func gitArgs(args []string) []string {
	return append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)
}

// gitTarget はリモートの owner/repo を、設定したコマンドに聞いて決める(push は push の URL をすべて。違う repo が混ざれば止める)。
func (s *Svc) gitTarget(op, sub, remote string) (string, error) {
	var q []string
	switch sub {
	case "push":
		q = []string{"remote", "get-url", "--push", "--all", remote}
	default:
		q = []string{"ls-remote", "--get-url", remote}
	}
	argv, _, problem := Command(op, "", s.Actor)
	if problem != "" {
		return "", die("git: %s", problem)
	}
	c := exec.Command(argv[0], append(append([]string{}, argv[1:]...), gitArgs(q)...)...)
	c.Env = childEnv(op, "", s.Actor, true)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, s.Err
	if err := c.Run(); err != nil {
		return "", die("git %s: リモート %s の URL を確かめられません(%v)", sub, remote, err)
	}
	repo := ""
	for _, u := range strings.Fields(out.String()) {
		r := RepoOfURL(u)
		if r == "" {
			return "", die("git %s: github.com のリモートだけを扱います: %s", sub, u)
		}
		if repo != "" && !strings.EqualFold(repo, r) {
			return "", die("git %s: リモート %s の push 先に別のリポジトリが混ざっています(%s / %s)", sub, remote, repo, r)
		}
		repo = r
	}
	if repo == "" {
		return "", die("git %s: リモート %s の URL がありません", sub, remote)
	}
	return repo, nil
}

func (s *Svc) gitRun(op, repo string, args []string) error {
	argv, _, problem := Command(op, repo, s.Actor)
	if problem != "" {
		return die("git: %s", problem)
	}
	c := exec.Command(argv[0], append(append([]string{}, argv[1:]...), gitArgs(args)...)...)
	c.Env = childEnv(op, repo, s.Actor, true)
	c.Stdout, c.Stderr = s.W, s.Err
	return runErr(c.Run(), "git "+args[0], op)
}
