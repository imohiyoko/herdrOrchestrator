// Package gate は merge / approve の判定の口(taskboard/lib/gate.sh。段 0: 判定は読み取りだけ。実行の口は常に無効)。
// 設計と入力の形: docs/gate.md。決定論(時刻・乱数・LLM・ネットワークを使わない)。理由は寄与を 1 件ずつ列挙する。
// 設定・記録の置き場所は TB_ROOT の下に固定(env の個別の変数では差し替えられない)。テストは Files を直接作って差し替える。
package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// Files は gate が読む・書くファイルの置き場所。
type Files struct {
	Conf, Paths, Reviews, IngestConf, Events, RolesDir string
}

// Fixed は tb の固定の置き場所(TB_ROOT が空なら .)。
func Fixed() Files {
	root := os.Getenv("TB_ROOT")
	if root == "" {
		root = "."
	}
	return Files{
		Conf:       root + "/config/gate.conf",
		Paths:      root + "/config/gate-paths.conf",
		Reviews:    root + "/data/gate-reviews.log",
		IngestConf: root + "/config/gate-ingest.conf",
		Events:     root + "/data/gate-review-events.log",
		RolesDir:   filepath.Join(root, "..", "roles"),
	}
}

const (
	// IngestSender は取り込み口の送信者名(固定)。
	IngestSender = "ingest"
	capApprove   = 2
	capMerge     = 1
)

// ReviewRoles は agent の pane から review-submit できる役割。ReservedLineages は agent が名乗れない外部系統。
var (
	ReviewRoles      = []string{"reviewer"}
	ReservedLineages = []string{"coderabbit"}
)

func reserved(lin string) bool {
	for _, l := range ReservedLineages {
		if l == lin {
			return true
		}
	}
	return false
}

// BuiltinPaths は設定の有無にかかわらず効くパス規則(gate.sh の GATE_BUILTIN_PATHS)。
var BuiltinPaths = []string{".github", ".github/**", "**/.github", "**/.github/**", "**/gate*.conf", "**/gate-paths.conf", "**/gate*.sh", "**/approvals.conf", "**/policy.conf", "**/perms/*.tbl", "**/risk-rules.conf",
	"**/bin/tb", "**/run-all.sh", "**/tests/affected.sh", "**/test/run.sh", "**/tests/run.sh", "**/test/cases/*gate*", "**/tests/*gate*",
	"**/.claude", "**/.claude/**", "**/CLAUDE.md", "**/CLAUDE.local.md", "**/AGENTS.md", "**/GEMINI.md", "**/copilot-instructions.md",
	"**/.cursor", "**/.cursor/**", "**/.cursorrules", "**/.windsurf", "**/.windsurf/**", "**/.windsurfrules", "**/.clinerules*", "**/.roo", "**/.roo/**",
	"**/.claude.json", "**/.agents", "**/.agents/**", "**/AGENT.md", "**/.aider.conf.*",
	"**/.continue", "**/.continue/**", "**/.codex", "**/.codex/**", "**/.gemini", "**/.gemini/**", "**/mcp.json", "**/.vscode/tasks.json", "**/.vscode/settings.json",
	"**/CODEOWNERS", "**/.coderabbit.yaml",
	".githooks", "**/.githooks/**", ".husky", "**/.husky/**", "**/.gitattributes", "**/.gitmodules", "**/.pre-commit-config.yaml",
	"**/.envrc", "**/.mcp.json", "**/.devcontainer/**", "**/.circleci/**", "**/Jenkinsfile",
	"**/Makefile", "**/GNUmakefile", "**/Dockerfile*", "**/*.dockerfile", "**/docker-compose*.yml", "**/docker-compose*.yaml",
	"**/package.json", "**/package-lock.json", "**/yarn.lock", "**/pnpm-lock.yaml", "**/bun.lockb", "**/.npmrc", "**/.yarnrc*",
	"**/requirements*.txt", "**/Pipfile", "**/Pipfile.lock", "**/poetry.lock", "**/uv.lock", "**/pyproject.toml", "**/setup.py", "**/setup.cfg",
	"**/pip.conf", "**/pip.ini", "**/.pip/**", "**/.pypirc",
	"**/go.mod", "**/go.sum", "**/go.work", "**/go.work.sum", "**/Cargo.toml", "**/Cargo.lock",
	"**/Gemfile", "**/Gemfile.lock", "**/composer.json", "**/composer.lock", "**/pom.xml", "**/build.gradle*",
	"**/Podfile", "**/Podfile.lock", "**/Package.swift", "**/Package.resolved", "**/mix.exs", "**/mix.lock", "**/pubspec.yaml", "**/pubspec.lock",
	"**/build.sbt", "**/deno.json", "**/deno.jsonc", "**/deno.lock", "**/environment.yml", "**/flake.nix", "**/flake.lock", "**/Brewfile*", "**/.terraform.lock.hcl",
	"**/.cargo/config", "**/.cargo/config.*", "**/.bundle/config", "**/.gemrc", "**/bunfig.toml", "**/.yarn/**", "**/pnpm-workspace.yaml",
	"**/gradlew", "**/gradlew.bat", "**/mvnw", "**/mvnw.cmd", "**/.mvn/**", "**/gradle.properties", "**/settings.gradle*",
	"**/conftest.py", "**/tox.ini", "**/noxfile.py", "**/justfile", "**/.justfile", "**/Taskfile.yml", "**/Taskfile.yaml", "**/Procfile",
	"**/lefthook.yml", "**/lefthook.yaml", "**/.lefthook.yml", "**/.lefthook.yaml", "**/.pre-commit-hooks.yaml",
	"**/.tool-versions", "**/mise.toml", "**/.mise.toml", "**/mise.*.toml", "**/.mise.*.toml", "**/.mise", "**/.mise/**", "**/mise/config.toml", "**/.config/mise/**",
	"**/.bazelrc", "**/.bazelrc.*", "**/MODULE.bazel", "**/WORKSPACE", "**/WORKSPACE.bazel",
	"**/.gitlab-ci.yml", "**/.gitlab/**", "**/.travis.yml", "**/azure-pipelines*.yml", "**/azure-pipelines*.yaml", "**/.azure-pipelines/**", "**/bitbucket-pipelines.yml",
	"**/cloudbuild.yaml", "**/cloudbuild.yml", "**/appveyor.yml", "**/.appveyor.yml", "**/.drone.yml", "**/buildspec*.yml", "**/buildspec*.yaml",
	"**/renovate.json", "**/renovate.json5", "**/.renovaterc", "**/.renovaterc.*", "**/.dependabot/**"}

// trim は _gate_trim(C ロケールの [:space:])。
func trim(s string) string { return strings.Trim(s, " \t\n\v\f\r") }

// cutHash は ${line%%#*}。
func cutHash(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// lines は while IFS= read -r line || [[ -n $line ]] の行(最後の改行の後の空は数えない)。
func lines(s string) []string {
	if s == "" {
		return nil
	}
	ls := strings.Split(s, "\n")
	if ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

// splitComma は IFS=',' read -r -a(最後の 1 つの空の欄は数えない)。
func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	xs := strings.Split(s, ",")
	if xs[len(xs)-1] == "" {
		xs = xs[:len(xs)-1]
	}
	return xs
}

func isIFS(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

// readFields は read -r v1 ... vn <<<"$s"(既定の IFS)。最後の欄は残り全部(前後の空白を落とす)。
func readFields(s string, n int) []string {
	out := make([]string, n)
	i := 0
	for k := 0; k < n; k++ {
		for i < len(s) && isIFS(s[i]) {
			i++
		}
		if k == n-1 {
			j := len(s)
			for j > i && isIFS(s[j-1]) {
				j--
			}
			out[k] = s[i:j]
			break
		}
		j := i
		for j < len(s) && !isIFS(s[j]) {
			j++
		}
		out[k] = s[i:j]
		i = j
	}
	return out
}

// lowerASCII は tr A-Z a-z。
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// globNoCase は shopt -s nocasematch での [[ $p == $g ]](ASCII の大文字小文字を区別しない)。
func globNoCase(g, p string) bool { return util.Glob(lowerASCII(g), lowerASCII(p)) }

// Conf は gate.conf を読んだ結果。
type Conf struct {
	Mode               string
	Repos              []string
	ThApprove, ThMerge int
	hasApprove, hasMrg bool
	Warns              []string
}

var (
	reSection = regexp.MustCompile(`^\[([a-z-]+)\]$`)
	reRepo    = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	reDigits  = regexp.MustCompile(`^[0-9]+$`)
	rePathPat = regexp.MustCompile(`^[A-Za-z0-9._*?/@-]+$`)
	reHead    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	rePR      = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+$`)
	reExit    = regexp.MustCompile(`^exit=[0-9]{1,3}$`)
	reShaArg  = regexp.MustCompile(`^sha=[0-9a-f]{7,40}$`)
	reFileOK  = regexp.MustCompile(`^[A-Za-z0-9._@/+-]+$`)
	reSize    = regexp.MustCompile(`^[0-9]{1,9}$`)
	reActor   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reAMDT    = regexp.MustCompile(`^[AMDT]$`)
	reUnknown = regexp.MustCompile(`[^A-Za-z0-9._#/=-]`)
	recRE     = regexp.MustCompile(`^ts=([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z) task=([A-Za-z0-9._-]{1,64}) by=([A-Za-z0-9._-]{1,64}) pr=(-|[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+) REVIEW ([a-z][a-z0-9-]{0,31}) ([0-9a-f]{7,40}) blocker=([0-9]{1,4}) should=([0-9]{1,4}) verdict=(pass|fail) evidence=(-|[A-Za-z0-9._/@+~-]{1,200})$`)
)

func readable(f string) (string, bool) {
	st, err := os.Stat(f)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// existsFollow は [[ -e $f ]](リンクの先をたどる)。
func existsFollow(f string) bool { _, err := os.Stat(f); return err == nil }

// LoadConf は _gate_load_conf。失敗は理由(読めたところまでの値は c に残る)。
func LoadConf(f string) (c Conf, err string) {
	if !existsFollow(f) {
		return c, "設定ファイルがありません(config/gate.conf)"
	}
	body, ok := readable(f)
	if !ok {
		return c, "設定ファイルを読めません"
	}
	sec := ""
	for i, line := range lines(body) {
		n := i + 1
		line = trim(cutHash(line))
		if line == "" {
			continue
		}
		if m := reSection.FindStringSubmatch(line); m != nil {
			sec = m[1]
			switch sec {
			case "mode", "repos", "pr-kinds", "threshold", "limits":
			default:
				return c, fmt.Sprintf("L%d: 未知の節 [%s]", n, sec)
			}
			continue
		}
		if sec == "" {
			return c, fmt.Sprintf("L%d: 節([mode] など)の前に行があります", n)
		}
		k, v, has := strings.Cut(line, "=")
		if !has {
			return c, fmt.Sprintf("L%d: 'キー = 値' の形ではありません", n)
		}
		k, v = trim(k), trim(v)
		switch sec + "." + k {
		case "mode.state":
			if v != "disabled" && v != "shadow" && v != "enabled" {
				return c, fmt.Sprintf("L%d: state は disabled | shadow | enabled です: %s", n, v)
			}
			c.Mode = v
		case "repos.allow":
			c.Repos = nil
			for _, it := range splitComma(v) {
				it = trim(it)
				if !reRepo.MatchString(it) {
					return c, fmt.Sprintf("L%d: allow は <owner>/<repo> のコンマ区切りです: %s", n, it)
				}
				c.Repos = append(c.Repos, it)
			}
		case "pr-kinds.approve", "pr-kinds.merge":
			items := splitComma(v)
			if len(items) == 0 {
				items = []string{""}
			}
			for _, it := range items {
				it = trim(it)
				if it != "docs-only" && it != "tests-only" && it != "code" && it != "mixed" {
					return c, fmt.Sprintf("L%d: PR の種類は docs-only | tests-only | code | mixed です: %s", n, it)
				}
			}
		case "threshold.approve", "threshold.merge":
			if !reDigits.MatchString(v) || len(v) > 2 {
				return c, fmt.Sprintf("L%d: しきい値は 0 以上 99 以下の整数です: %s", n, v)
			}
			x, _ := strconv.Atoi(v)
			if k == "approve" {
				c.ThApprove, c.hasApprove = x, true
			} else {
				c.ThMerge, c.hasMrg = x, true
			}
		case "limits.merge-per-day", "limits.merge-min-interval-min", "limits.approve-per-day":
			if !reDigits.MatchString(v) || len(v) > 4 {
				return c, fmt.Sprintf("L%d: 上限は整数です: %s = %s", n, k, v)
			}
		default:
			return c, fmt.Sprintf("L%d: 未知のキー [%s] %s", n, sec, k)
		}
	}
	if c.Mode == "" {
		return c, "[mode] state がありません"
	}
	if !c.hasApprove {
		return c, "[threshold] approve がありません"
	}
	if !c.hasMrg {
		return c, "[threshold] merge がありません"
	}
	if c.ThApprove > capApprove {
		c.Warns = append(c.Warns, fmt.Sprintf("緩め設定は無効: [threshold] approve = %d(上限 %d のまま)", c.ThApprove, capApprove))
		c.ThApprove = capApprove
	}
	if c.ThMerge > capMerge {
		c.Warns = append(c.Warns, fmt.Sprintf("緩め設定は無効: [threshold] merge = %d(上限 %d のまま)", c.ThMerge, capMerge))
		c.ThMerge = capMerge
	}
	return c, ""
}

// LoadPaths は _gate_load_paths(組み込みの規則は常に加わる)。
func LoadPaths(f string) ([]string, string) {
	pats := append([]string{}, BuiltinPaths...)
	if !existsFollow(f) {
		return pats, "パス規則ファイルがありません(config/gate-paths.conf)"
	}
	body, ok := readable(f)
	if !ok {
		return pats, "パス規則ファイルを読めません"
	}
	for i, line := range lines(body) {
		line = trim(cutHash(line))
		if line == "" {
			continue
		}
		if !rePathPat.MatchString(line) || strings.HasPrefix(line, "/") {
			return pats, fmt.Sprintf("gate-paths L%d: パターンに使えない文字、または絶対パスです: %s", i+1, line)
		}
		pats = append(pats, line)
	}
	return pats, ""
}

// PathHit は _gate_path_hit: 当たったパターン(無ければ空)。* は / も越え、**/ で始まるパターンは先頭の階層なしにも当たる。
func PathHit(pats []string, p string) string {
	for _, pat := range pats {
		if globNoCase(strings.ReplaceAll(pat, "**", "*"), p) {
			return pat
		}
		if strings.HasPrefix(pat, "**/") && globNoCase(strings.ReplaceAll(pat[3:], "**", "*"), p) {
			return pat
		}
	}
	return ""
}

func pathOK(p string) bool {
	if !reFileOK.MatchString(p) || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") { // local IFS=/; for seg in $1(連続した / は空の階層)
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Facts は入力の事実(_gate_parse の G_*)。
type Facts struct {
	Head, PR, Test, TestSha, Public, Author, Executor, Reject string
	TestExit, SizeCh, SizeTot                                 int
	hasSize                                                   bool
	Files                                                     [][]string // {種別, 旧パス[, 新パス]}
}

// Parse は _gate_parse: 事実と、入力の不足・不正の一覧。
func Parse(in []string) (f Facts, errs []string) {
	seen := map[string]bool{}
	for _, line := range in {
		line = trim(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kr := readFields(line, 2)
		key, rest := kr[0], kr[1]
		switch key {
		case "HEAD", "PR", "TEST", "PUBLIC-CHECK", "SIZE", "AUTHOR", "EXECUTOR", "AUTHOR-REJECT-RATE":
			if seen[key] {
				errs = append(errs, key+" が重複しています")
				continue
			}
			seen[key] = true
		}
		switch key {
		case "HEAD":
			if reHead.MatchString(rest) {
				f.Head = rest
			} else {
				errs = append(errs, "HEAD の形が不正です")
			}
		case "PR":
			if rePR.MatchString(rest) {
				f.PR = rest
			} else {
				errs = append(errs, "PR の形が不正です(<owner>/<repo>#<n>)")
			}
		case "REVIEW":
			errs = append(errs, "REVIEW は入力では受け付けません(記録 tb review-submit から読みます)")
		case "TEST":
			if rest == "none" {
				f.Test = "none"
				break
			}
			x := readFields(rest, 3)
			if x[2] != "" || !reExit.MatchString(x[0]) || !reShaArg.MatchString(x[1]) {
				errs = append(errs, "TEST の形が不正です")
			} else {
				f.Test = "run"
				f.TestExit, _ = strconv.Atoi(strings.TrimPrefix(x[0], "exit="))
				f.TestSha = strings.TrimPrefix(x[1], "sha=")
			}
		case "PUBLIC-CHECK":
			if rest == "clean" || rest == "dirty" || rest == "unavailable" {
				f.Public = rest
			} else {
				errs = append(errs, "PUBLIC-CHECK は clean | dirty | unavailable です")
			}
		case "FILE":
			x := readFields(rest, 4)
			a, b, c := x[0], x[1], x[2]
			switch {
			case x[3] != "":
				errs = append(errs, "FILE の欄が多すぎます")
			case a == "R":
				if b != "" && c != "" && pathOK(b) && pathOK(c) {
					f.Files = append(f.Files, []string{"R", b, c})
				} else {
					errs = append(errs, "FILE R は旧パスと新パスの両方が要り、パスは相対で、空・.・.. の階層を含みません")
				}
			case reAMDT.MatchString(a):
				if b != "" && c == "" && pathOK(b) {
					f.Files = append(f.Files, []string{a, b})
				} else {
					errs = append(errs, "FILE "+a+" のパスが不正です(相対パスで、空・.・.. の階層を含まない 1 つ)")
				}
			default:
				errs = append(errs, "FILE の種別は A | M | D | R | T です")
			}
		case "SIZE":
			x := readFields(rest, 2)
			if reSize.MatchString(x[0]) && reSize.MatchString(x[1]) {
				ch, _ := strconv.Atoi(x[0])
				tot, _ := strconv.Atoi(x[1])
				if tot >= 1 {
					f.SizeCh, f.SizeTot, f.hasSize = ch, tot, true
					break
				}
			}
			errs = append(errs, "SIZE は <変更行数> <全行数(1 以上)> です")
		case "AUTHOR":
			if reActor.MatchString(rest) {
				f.Author = rest
			} else {
				errs = append(errs, "AUTHOR の形が不正です")
			}
		case "EXECUTOR":
			if reActor.MatchString(rest) {
				f.Executor = rest
			} else {
				errs = append(errs, "EXECUTOR の形が不正です")
			}
		case "AUTHOR-REJECT-RATE":
			if rest == "low" || rest == "mid" || rest == "high" || rest == "unknown" {
				f.Reject = rest
			} else {
				errs = append(errs, "AUTHOR-REJECT-RATE は low | mid | high | unknown です")
			}
		default:
			k := key
			if len(k) > 32 {
				k = k[:32]
			}
			errs = append(errs, "未知の行: "+reUnknown.ReplaceAllString(k, ""))
		}
	}
	if f.Head == "" {
		errs = append(errs, "HEAD がありません")
	}
	if f.PR == "" {
		errs = append(errs, "PR がありません")
	}
	if f.Test == "" {
		errs = append(errs, "TEST がありません(未実行は 'TEST none')")
	}
	if f.Public == "" {
		errs = append(errs, "PUBLIC-CHECK がありません")
	}
	if !f.hasSize {
		errs = append(errs, "SIZE がありません")
	}
	if f.Author == "" {
		errs = append(errs, "AUTHOR がありません")
	}
	if f.Executor == "" {
		errs = append(errs, "EXECUTOR がありません")
	}
	if f.Reject == "" {
		errs = append(errs, "AUTHOR-REJECT-RATE がありません")
	}
	if len(f.Files) == 0 {
		errs = append(errs, "FILE がありません")
	}
	return f, errs
}

// Record はレビューの記録の 1 行(この chain のもの)。
type Record struct {
	TS, By, Lin, Sha, Ver, Note string
	Blk, Shd                    int
	OK, Count                   bool
}

// LoadReviews は _gate_load_reviews: 全行を厳密に読み、chain の記録を返す。形式違反が 1 行でもあれば失敗。
func LoadReviews(f, chain, head, pr string) ([]Record, string) {
	if !existsFollow(f) {
		return nil, "レビューの記録がありません(data/gate-reviews.log)"
	}
	lst, lerr := os.Lstat(f)
	body, ok := readable(f)
	if lerr != nil || lst.Mode()&os.ModeSymlink != 0 || !ok {
		return nil, "レビューの記録を読めません(通常のファイルではない)"
	}
	var out []Record
	for i, line := range lines(body) {
		if line == "" {
			continue
		}
		m := recRE.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Sprintf("レビューの記録の形式違反(L%d)", i+1)
		}
		if m[2] != chain {
			continue
		}
		r := Record{TS: m[1], By: m[3], Lin: m[5], Sha: m[6], Ver: m[9], Count: true}
		r.Blk, _ = strconv.Atoi(m[7])
		r.Shd, _ = strconv.Atoi(m[8])
		cnote := ""
		switch {
		case m[3] == "human":
			r.Count, cnote = false, "独立系統には数えない: 送信者 human"
		case reserved(m[5]) && m[3] != IngestSender:
			r.Count, cnote = false, "独立系統には数えない: 外部系統は送信者 "+IngestSender+" の記録だけが数えられる(自称)"
		case !reserved(m[5]) && m[3] == IngestSender:
			r.Count, cnote = false, "独立系統には数えない: 送信者 "+IngestSender+" は外部系統の記録だけに使われる"
		}
		switch {
		case m[6] != head:
			r.Note = "無効: sha が head と一致しない"
		case m[4] != "-" && pr != "" && m[4] != pr:
			r.Note = "無効: 記録の PR(" + m[4] + ")が対象と違う"
		default:
			r.OK, r.Note = true, "有効"
			if cnote != "" {
				r.Note += "(" + cnote + ")"
			}
		}
		out = append(out, r)
	}
	return out, ""
}

// Result は gate_eval の結果。
type Result struct {
	Value, Action, Chain, PR, Head, Mode string
	Exit, Score                          int
	Threshold                            string
	Hard, Lines, Warns                   []string
}

// Eval は gate_eval <merge|approve> <chain> <pr 確認用|空> <行>...。
func Eval(fs Files, action, chain, prcheck string, in []string) Result {
	r := Result{Value: "ask", Exit: 21, Action: action, Chain: chain}
	var errs []string
	c, cerr := LoadConf(fs.Conf)
	r.Mode = c.Mode
	if cerr != "" {
		errs = append(errs, cerr)
	} else {
		r.Warns = c.Warns
	}
	pats, perr := LoadPaths(fs.Paths)
	if perr != "" {
		errs = append(errs, perr)
	}
	f, inerrs := Parse(in)
	r.PR, r.Head = f.PR, f.Head
	for _, e := range inerrs {
		errs = append(errs, "入力: "+e)
	}
	var recs []Record
	if f.Head != "" {
		var rerr string
		recs, rerr = LoadReviews(fs.Reviews, chain, f.Head, f.PR)
		if rerr != "" {
			errs = append(errs, rerr)
		}
	}
	if prcheck != "" && f.PR != "" && prcheck != f.PR {
		errs = append(errs, "入力: --pr("+prcheck+")が記録の PR("+f.PR+")と食い違います")
	}
	if len(errs) > 0 {
		for _, e := range errs {
			r.Lines = append(r.Lines, "E-01 評価不能(fail-closed): "+e)
		}
		return r
	}
	th := c.ThApprove
	if action == "merge" {
		th = c.ThMerge
	}
	r.Threshold = strconv.Itoa(th)
	hard := func(s string) { r.Hard = append(r.Hard, s) }
	line := func(s string) { r.Lines = append(r.Lines, s) }

	// --- hard(点数に関係なく ask)
	repo, _, _ := strings.Cut(f.PR, "#")
	allowed := false
	for _, x := range c.Repos {
		if x != "" && x == repo {
			allowed = true
		}
	}
	if !allowed {
		hard("H-05 許可リスト外のリポジトリ(またはリストが空)")
	}
	for _, fl := range f.Files {
		if fl[0] == "T" {
			hard("H-06 ファイルの種別・モードの変更: " + fl[1])
		}
		for _, p := range fl[1:] {
			if pat := PathHit(pats, p); pat != "" {
				hard("H-01 ガバナンスのパス規則に当たる変更: " + p + "(規則 " + pat + ")")
			}
		}
	}
	if f.Public != "clean" {
		hard("H-02 公開前検査が clean でない(" + f.Public + ")")
	}
	failed := map[string]bool{}
	bad, nvalid := false, 0
	for _, rc := range recs {
		line(fmt.Sprintf("REC %s %s blocker=%d should=%d verdict=%s by=%s at=%s → %s", rc.Lin, firstN(rc.Sha, 7), rc.Blk, rc.Shd, rc.Ver, rc.By, rc.TS, rc.Note))
		if !rc.OK {
			continue
		}
		nvalid++
		if rc.Blk > 0 || rc.Ver != "pass" {
			bad = true
			failed[rc.Lin] = true
			hard("H-03 レビュー(" + rc.Lin + ", " + rc.By + ")に blocker または fail がある")
		}
	}
	lins, senders := map[string]bool{}, map[string]bool{}
	should, nlin := false, 0
	for _, rc := range recs {
		if !rc.OK || failed[rc.Lin] || !rc.Count {
			continue
		}
		if rc.Shd > 0 {
			should = true
		}
		if lins[rc.Lin] || senders[rc.By] {
			continue
		}
		lins[rc.Lin], senders[rc.By] = true, true
		nlin++
	}
	if nvalid == 0 {
		hard("H-03 head に対する有効なレビューの記録が無い")
	} else if nlin < 2 {
		hard(fmt.Sprintf("H-03 独立した 2 系統の pass がそろっていない(数えられた系統: %d。送信者 human と自称の外部系統は数えない)", nlin))
	}
	if f.Test == "run" {
		if f.TestExit != 0 {
			hard(fmt.Sprintf("H-03 テストが失敗(exit=%d)", f.TestExit))
		} else if f.TestSha != f.Head {
			hard("H-03 テストの sha が head と一致しない")
		}
	}
	if lowerASCII(f.Author) == lowerASCII(f.Executor) {
		hard("H-04 作者と実行の主体が同一(" + f.Author + ")")
	}

	// --- 点数
	add := func(pts int, s string) { r.Score += pts; line(fmt.Sprintf(s, pts)) }
	pct := f.SizeCh * 100 / f.SizeTot
	switch {
	case pct < 10:
		add(0, "S-01 +%d 変更の大きさ: 小")
	case pct < 30:
		add(1, "S-01 +%d 変更の大きさ: 中")
	case pct < 60:
		add(2, "S-01 +%d 変更の大きさ: 大")
	default:
		add(3, "S-01 +%d 変更の大きさ: 特大")
	}
	hasDocs, hasTests, hasCode, hasImp := false, false, false, false
	anyGlob := func(p string, pats ...string) bool {
		for _, g := range pats {
			if globNoCase(g, p) {
				return true
			}
		}
		return false
	}
	for _, fl := range f.Files {
		for _, p := range fl[1:] {
			switch {
			case anyGlob(p, "lib/*", "*/lib/*", "config/*", "*/config/*", "scheduler/*", "*/scheduler/*", ".github/*", "bin/*", "*/bin/*"):
				hasImp, hasCode = true, true
			case anyGlob(p, "test/*", "tests/*", "*/test/*", "*/tests/*"):
				hasTests = true
			case anyGlob(p, "docs/*", "*/docs/*", "*.md"):
				base := p[strings.LastIndexByte(p, '/')+1:]
				ext := ""
				if strings.Contains(strings.TrimPrefix(base, "."), ".") {
					ext = lowerASCII(base[strings.LastIndexByte(base, '.')+1:])
				}
				switch ext {
				case "md", "txt", "png", "svg", "jpg", "jpeg", "gif", "rst":
					hasDocs = true
				default:
					hasCode = true
				}
			default:
				hasCode = true
			}
		}
	}
	switch {
	case hasImp:
		add(2, "P-01 +%d 触るパス: lib・config・scheduler 配下(重要度 高)")
	case hasCode:
		add(1, "P-01 +%d 触るパス: 一般のコード")
	default:
		add(0, "P-01 +%d 触るパス: docs・tests のみ")
	}
	n := 0
	for _, b := range []bool{hasDocs, hasTests, hasCode} {
		if b {
			n++
		}
	}
	switch {
	case n > 1:
		add(2, "K-01 +%d PR の種類: mixed")
	case hasCode:
		add(1, "K-01 +%d PR の種類: code")
	case hasTests:
		add(0, "K-01 +%d PR の種類: tests-only")
	default:
		add(0, "K-01 +%d PR の種類: docs-only")
	}
	if f.Test == "none" {
		add(2, "T-01 +%d テスト: 未実行")
	} else {
		line(fmt.Sprintf("T-01 +0 テスト: exit=%d sha 一致", f.TestExit))
	}
	switch {
	case bad || nlin < 2:
		line("R-01 +0 レビュー: hard で扱う(H-03)")
	case should:
		add(1, "R-01 +%d レビュー: should が残っている")
	default:
		line(fmt.Sprintf("R-01 +0 レビュー: %d 系統とも blocker=0 should=0 sha 一致", nlin))
	}
	pts := map[string]int{"low": 0, "mid": 1, "high": 2, "unknown": 1}[f.Reject]
	add(pts, "A-01 +%d 作者の差し戻し率: "+f.Reject)

	switch {
	case len(r.Hard) > 0:
		r.Value, r.Exit = "ask", 20
	case r.Score <= th:
		r.Value, r.Exit = "allow", 0
	default:
		r.Value, r.Exit = "ask", 20
	}
	return r
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Format は gate_format。
func (r Result) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "gate: %s\naction: %s\n", r.Value, r.Action)
	fmt.Fprintf(&b, "chain: %s   pr: %s   head: %s\n", r.Chain, dash(r.PR), dash(r.Head))
	fmt.Fprintf(&b, "mode: %s   # 段 0 は参考値だけ。実行は常に無効\n", dash(r.Mode))
	if r.Exit == 21 {
		fmt.Fprintf(&b, "score: - / threshold(%s) -\nhard: -\n", r.Action)
	} else {
		fmt.Fprintf(&b, "score: %d / threshold(%s) %s\n", r.Score, r.Action, r.Threshold)
		if len(r.Hard) == 0 {
			b.WriteString("hard: none\n")
		}
		for _, h := range r.Hard {
			fmt.Fprintf(&b, "hard: %s\n", h)
		}
	}
	for _, l := range r.Lines {
		fmt.Fprintf(&b, "reason: %s\n", l)
	}
	for _, w := range r.Warns {
		if w != "" {
			fmt.Fprintf(&b, "warn: %s\n", w)
		}
	}
	return b.String()
}
