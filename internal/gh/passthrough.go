package gh

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
)

// tb gh <gh の引数...>: 専用のコマンド(comment / review / pr merge)以外の gh のコマンドを、そのまま gh に渡す。
// 操作名は gh:<コマンド>-<サブコマンド>(例: gh:issue-create、gh:run-list、gh:api)、対象は owner/repo。
// 既定は perms/ops.tbl(読み取りは allow、ほかは ask、認証・秘密・設定・api は deny)。

// repoless はリポジトリを対象にしないコマンド(対象は空。policy の対象 * の行だけが当たる)。
var repoless = map[string]bool{"search": true, "status": true, "gist": true, "org": true, "project": true, "codespace": true,
	"auth": true, "config": true, "extension": true, "alias": true, "ssh-key": true, "gpg-key": true, "completion": true,
	"copilot": true, "agent-task": true, "preview": true, "version": true}

// ghCommands は受ける gh のコマンド(1 語目)。別名(gh alias)と拡張(gh extension)は通さない
// (gh:<別名>-list のような名前で既定の allow に当たり、中身は別のコマンドやシェルを動かせるため)。
var ghCommands = map[string]bool{"agent-task": true, "alias": true, "api": true, "attestation": true, "auth": true, "browse": true,
	"cache": true, "codespace": true, "completion": true, "config": true, "copilot": true, "extension": true, "gist": true,
	"gpg-key": true, "issue": true, "label": true, "org": true, "pr": true, "preview": true, "project": true, "release": true,
	"repo": true, "ruleset": true, "run": true, "search": true, "secret": true, "ssh-key": true, "status": true,
	"variable": true, "workflow": true}

// threeLevel は 3 語目までが操作になるもの(gh repo deploy-key add など)。
var threeLevel = map[string]bool{"deploy-key": true, "autolink": true, "gitignore": true, "license": true, "ports": true}

// routed は専用のコマンドに回すもの(approve を gh:review に紛れ込ませない。本文を承認待ちと一緒に積む)。
var routed = map[string]string{
	"pr review":  "tb gh review add|approve",
	"pr comment": "tb gh comment add",
}

var reWord = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// opOf は gh の引数から操作名(gh:<語>-<語>[-<語>])と、語の数を返す。
func opOf(args []string) (op string, n int) {
	var words []string
	for _, a := range args {
		if !reWord.MatchString(a) || len(words) == 3 {
			break
		}
		words = append(words, a)
	}
	switch {
	case len(words) == 0:
		return "", 0
	case words[0] == "api": // gh api <パス>: パスは語ではない
		words = words[:1]
	case len(words) >= 3 && threeLevel[words[1]]:
		words = words[:3]
	case len(words) >= 2:
		words = words[:2]
	}
	return "gh:" + strings.Join(words, "-"), len(words)
}

// repoFlag は -R / --repo の値(無ければ "")。2 回以上あれば ok=false。
func repoFlag(args []string) (repo string, ok bool) {
	n := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		v, found := "", false
		switch {
		case a == "--":
			i = len(args)
			continue
		case a == "-R" || a == "--repo":
			if i+1 < len(args) {
				v, found = args[i+1], true
				i++
			} else {
				return "", false
			}
		case strings.HasPrefix(a, "--repo="):
			v, found = strings.TrimPrefix(a, "--repo="), true
		case strings.HasPrefix(a, "-R") && len(a) > 2:
			v, found = a[2:], true
		}
		if found {
			n++
			repo = v
		}
	}
	return repo, n <= 1
}

var reAPIRepo = regexp.MustCompile(`^/?repos/([^/?#]+/[^/?#]+)`)

// targetOf は操作の対象(owner/repo。リポジトリを対象にしないコマンドは "")。決められなければ problem。
func targetOf(args []string, n int) (repo, problem string) {
	flag, ok := repoFlag(args)
	if !ok {
		return "", "-R / --repo は 1 つだけ指定してください"
	}
	if flag != "" {
		if !ValidRepo(flag) {
			return "", "-R / --repo は <owner>/<repo>(ホスト名は付けない): " + flag
		}
		return flag, ""
	}
	cmd := args[0]
	switch {
	case cmd == "api":
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if m := reAPIRepo.FindStringSubmatch(a); m != nil && ValidRepo(m[1]) {
				return m[1], ""
			}
			return "", "" // repos/<owner>/<repo>/... 以外のパス(user など)は、対象なし
		}
		return "", ""
	case cmd == "repo":
		// gh repo view|clone|fork|edit|delete|... [<owner>/<repo>]。create / list は owner だけのこともある
		if len(args) > n && !strings.HasPrefix(args[n], "-") {
			if ValidRepo(args[n]) {
				return args[n], ""
			}
			if args[1] == "list" || args[1] == "create" {
				return "", ""
			}
			return "", "リポジトリは <owner>/<repo>(URL・ホスト名は付けない): " + args[n]
		}
		if args[1] == "list" || args[1] == "create" {
			return "", ""
		}
	case repoless[cmd]:
		return "", ""
	}
	return "", "対象のリポジトリを -R <owner>/<repo> で指定してください(tb は今いるディレクトリの clone からは決めない)"
}

// isReadOp は読みの操作か(既定の表で allow のもの。コマンドのキー gh_read を引くかの分かれ目)。
func isReadOp(op string) bool { return op == OpRead || perms.OpDefault(op) == "allow" }

// passthrough は tb gh <gh の引数...>。
func (s *Svc) passthrough(args []string) error {
	op, n := opOf(args)
	if op == "" {
		return usage("gh: コマンドが要ります(例: tb gh issue list -R <owner>/<repo>。tb gh help)")
	}
	if !ghCommands[args[0]] {
		return usage("gh: %s は扱いません(gh のコマンドだけ。別名・拡張は通さない)", args[0])
	}
	if n >= 2 {
		if to, ok := routed[args[0]+" "+args[1]]; ok {
			return usage("gh %s %s は %s を使ってください(approve と本文の扱いを分けているため)", args[0], args[1], to)
		}
	}
	repo, problem := targetOf(args, n)
	if problem != "" {
		return usage("gh %s: %s", strings.Join(args[:n], " "), problem)
	}
	what := "tb gh " + strings.Join(args[:n], " ")
	if err := s.Authorize(op, repo, what); err != nil {
		return err
	}
	return s.stream(op, repo, args)
}

// stream は操作 op のコマンドを、標準入出力をそのままつないで実行する(終了コードもそのまま返す)。
func (s *Svc) stream(op, repo string, args []string) error {
	argv, _, problem := Command(op, repo, s.Actor)
	if problem != "" {
		return die("gh: %s", problem)
	}
	c := exec.Command(argv[0], append(append([]string{}, argv[1:]...), args...)...)
	c.Env = childEnv(op, repo, s.Actor, false)
	c.Stdin, c.Stdout, c.Stderr = s.Stdin, s.W, s.Err
	return runErr(c.Run(), strings.Join(args[:min(2, len(args))], " "), op)
}

// childEnv は子の環境変数: 呼び出し元の token を外し、TB_GH_* を付ける。git なら GIT_* も外す(フックや ssh のコマンドを差し込ませない)。
func childEnv(op, repo, actor string, git bool) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if tokenEnv[k] || strings.HasPrefix(k, "TB_GH_") || git && strings.HasPrefix(k, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	if git {
		env = append(env, "GIT_TERMINAL_PROMPT=0")
	}
	return append(env, "TB_GH_OP="+op, "TB_GH_REPO="+repo, "TB_GH_ACTOR="+actor)
}

// runErr は子の終了を tb の終了にする(75 は auth-locked、ほかの失敗は子の終了コードのまま)。
func runErr(err error, what, op string) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == AuthRC {
			return &Err{AuthRC, "gh: 認証で止まりました(auth-locked。ラッパーが 75 を返した)。人が認証を戻してから、もう一度実行してください"}
		}
		return &Err{ee.ExitCode(), fmt.Sprintf("%s(%s)が失敗しました(終了コード %d)", what, op, ee.ExitCode())}
	}
	return die("コマンドを実行できません: %v", err)
}
