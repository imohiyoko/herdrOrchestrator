// Package pr は PR のサービス(taskboard/lib/prqueue.sh の tb pr-queue と、notify.sh の tb pr-watch)。
// できないこと(実装しない): マージ・承認・ready・close・コメント。gh は create(--draft 固定)と pr list だけを呼ぶ。
// 利用者が書く正規表現(body_first_line・公開前検査の規則と例外)は、bash と同じ意味になるよう grep -E で評価する。
package pr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
)

const (
	BaseFixed   = "main"
	MaxBody     = 102400
	MaxAttempts = 3
	AuthRC      = 75 // gh ラッパーが「認証で止まった(auth-locked)」を伝える終了コード
	MaxCreate   = 3
)

// envOr は bash の ${X:-d}(空も既定)。
func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func root() string { return os.Getenv("TB_ROOT") }

// confValue は pq_conf の 1 ファイル分: "^[[:space:]]*<key>[[:space:]]*=" の最後の行の値(最初の # から後ろと前後の空白を落とす)。
func confValue(f, key string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`^[[:space:]]*` + regexp.QuoteMeta(key) + `[[:space:]]*=`)
	v, found := "", false
	for _, l := range strings.Split(string(b), "\n") {
		if re.MatchString(l) {
			v, found = l, true
		}
	}
	if !found {
		return ""
	}
	_, v, _ = strings.Cut(v, "=")
	v = strings.TrimLeft(v, " \t\n\v\f\r")
	v, _, _ = strings.Cut(v, "#")
	return strings.TrimRight(v, " \t\n\v\f\r")
}

// Conf は pq_conf: PR_QUEUE_CONF(既定は <tb>/config/pr-queue.conf)の値。空なら d。
func Conf(key, d string) string {
	f := envOr("PR_QUEUE_CONF", filepath.Join(root(), "config", "pr-queue.conf"))
	if v := confValue(f, key); v != "" {
		return v
	}
	return d
}

// denyConf は pq_deny_conf: 公開前検査の設定は固定の <tb>/config/pr-queue.conf だけから読む(環境変数・PR_QUEUE_CONF では変えない)。
func denyConf(key string) string {
	return confValue(filepath.Join(root(), "config", "pr-queue.conf"), key)
}

// physDir は (cd d && pwd -P)。
func physDir(d string) (string, bool) {
	a, err := filepath.Abs(d)
	if err != nil {
		return "", false
	}
	p, err := filepath.EvalSymlinks(a)
	if err != nil {
		return "", false
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		return "", false
	}
	return p, true
}

// BodyRoot は pq_body_root。
func BodyRoot() (string, bool) {
	return physDir(envOr("PR_QUEUE_BODY_ROOT", Conf("body_root", filepath.Join(root(), "data", "pr-bodies"))))
}

var reRepo = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Repos は pq_repos: 許可リストの "- <org>/<repo>   # 注釈" から <org>/<repo>(注釈を落とす)。
func Repos() []string {
	b, err := os.ReadFile(notify.ReposFile())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		l = config.Trim(l)
		rest, ok := strings.CutPrefix(l, "-")
		if !ok {
			continue
		}
		rest = config.Trim(rest)
		rest, _, _ = strings.Cut(rest, "#")
		rest = config.Trim(rest)
		if reRepo.MatchString(rest) {
			out = append(out, rest)
		}
	}
	return out
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// GH は pq_gh: PR_QUEUE_GH > conf の gh。絶対パスの実行ファイルだけ(PATH 上の gh には落とさない)。使えなければ警告を stderr に出して false。
func GH(stderr io.Writer) (string, bool) {
	g := envOr("PR_QUEUE_GH", Conf("gh", ""))
	if g == "" {
		fmt.Fprintln(stderr, "pr-queue: gh が指定されていないため止めます(fail-closed)。PR_QUEUE_GH か config/pr-queue.conf の gh に、専用の gh ラッパーの絶対パスを指定してください。PATH 上の gh には落としません")
		return "", false
	}
	st, err := os.Stat(g)
	if !strings.HasPrefix(g, "/") || err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
		fmt.Fprintf(stderr, "pr-queue: 指定された gh を使えないため止めます(fail-closed)。絶対パスの実行ファイルを指定してください: %s\n", g)
		return "", false
	}
	return g, true
}

var rePrefix = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}/$`)

// Prefixes は pq_prefixes: branch_prefixes(空白かカンマ区切り)> branch_prefix > agent/。不正な要素は捨て、全部不正なら agent/。
func Prefixes() []string {
	raw := Conf("branch_prefixes", "")
	if raw == "" {
		raw = Conf("branch_prefix", "agent/")
	}
	var out []string
	for _, p := range strings.Fields(strings.ReplaceAll(raw, ",", " ")) {
		if rePrefix.MatchString(p) && !has(out, p) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = []string{"agent/"}
	}
	return out
}

func prefixList() string { return strings.Join(Prefixes(), " ") }

var reBranchRest = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,120}$`)

// ValidBranch は pq_valid_branch: どれかの接頭辞で始まり、安全な文字だけ。
func ValidBranch(b string) bool {
	ok := false
	for _, p := range Prefixes() {
		if rest, found := strings.CutPrefix(b, p); found && reBranchRest.MatchString(rest) {
			ok = true
			break
		}
	}
	return ok && !strings.Contains(b, "..") && !strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".lock") && !strings.Contains(b, "//")
}

var reRef = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// validRef は pq_valid_ref: ブランチ名として安全な形(接頭辞は問わない)。
func validRef(b string) bool {
	return reRef.MatchString(b) && !strings.HasPrefix(b, "-") && !strings.HasPrefix(b, "/") && !strings.HasSuffix(b, "/") &&
		!strings.Contains(b, "..") && !strings.Contains(b, "//") && !strings.HasSuffix(b, ".lock")
}

func baseMode() string {
	if Conf("base_mode", "fixed") == "default_branch" {
		return "default_branch"
	}
	return "fixed"
}

// fileSHA は pq_sha(sha256 の 16 進)。
func fileSHA(f string) string {
	b, err := os.ReadFile(f)
	if err != nil {
		return "" // 読めない: bash はリダイレクトで止まり、何も出さない
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ereMatch は grep -qE[i] -- re(標準入力に s)。正規表現が不正なら false。
func ereMatch(re, s string, icase bool) bool {
	fl := "-qE"
	if icase {
		fl = "-qEi"
	}
	c := exec.Command("grep", fl, "--", re)
	c.Stdin = strings.NewReader(s)
	return c.Run() == nil
}

// ereValid は printf ” | grep -E -- re の終了コードが 1 以下(= 正規表現として読める)。
func ereValid(re string) bool {
	c := exec.Command("grep", "-E", "--", re)
	c.Stdin = strings.NewReader("")
	err := c.Run()
	if err == nil {
		return true
	}
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode() <= 1
	}
	return false
}
