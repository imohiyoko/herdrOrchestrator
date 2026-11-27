package webui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/sched"
)

// jobview.go は定期実行ジョブの「何が・誰に・いつ・承認は」を読み取りで見せる。
// conf は実行しない(PROMPT などの記述を、展開せずにそのまま読む)。本文は表示前にトークンらしい文字列をマスクする。

const (
	jobConfMax  = 64 * 1024
	jobFileMax  = 64 * 1024
	pySpaceCls  = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	pySpace     = `[` + pySpaceCls + `]`
	pyNonSpaceQ = `[^` + pySpaceCls + `"']`
)

var jobKeys = []string{"PROMPT", "PROMPT_FILE", "PROMPT_RULES", "PROMPT_CMD", "PROMPT_VARS", "COMMAND", "SKIP_IF", "COMMAND_TIMEOUT"}

// isPyWord は Python の \w(str のパターン)。
func isPyWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// pyBoundary は s の byte 位置 i が Python の \b か。
func pyBoundary(s string, i int) bool {
	before, after := false, false
	if i > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:i])
		before = isPyWord(r)
	}
	if i < len(s) {
		r, _ := utf8.DecodeRuneInString(s[i:])
		after = isPyWord(r)
	}
	return before != after
}

// pyFindB は re に当たる最初の位置。left / right なら、その端が Python の \b であるものだけ(ほかは次の位置から探し直す)。
func pyFindB(re *regexp.Regexp, s string, from int, left, right bool) []int {
	for pos := from; pos <= len(s); {
		loc := re.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			return nil
		}
		for k := range loc {
			if loc[k] >= 0 {
				loc[k] += pos
			}
		}
		if (!left || pyBoundary(s, loc[0])) && (!right || pyBoundary(s, loc[1])) {
			return loc
		}
		_, w := utf8.DecodeRuneInString(s[loc[0]:])
		if w == 0 {
			w = 1
		}
		pos = loc[0] + w
	}
	return nil
}

// pySubB は re.subn(左端の \b つき)。repl は当たった部分の submatch の位置を受け取る。
func pySubB(re *regexp.Regexp, s string, left bool, repl func(loc []int) string) (string, int) {
	var b strings.Builder
	n, pos := 0, 0
	for pos <= len(s) {
		loc := pyFindB(re, s, pos, left, false)
		if loc == nil {
			break
		}
		b.WriteString(s[pos:loc[0]])
		b.WriteString(repl(loc))
		n++
		if loc[1] == loc[0] { // 空の一致(ここでは起きないが、念のため 1 文字進める)
			if loc[1] < len(s) {
				_, w := utf8.DecodeRuneInString(s[loc[1]:])
				b.WriteString(s[loc[1] : loc[1]+w])
				pos = loc[1] + w
				continue
			}
			pos = len(s) + 1
			break
		}
		pos = loc[1]
	}
	if pos <= len(s) {
		b.WriteString(s[pos:])
	}
	return b.String(), n
}

var (
	maskKey    = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|\z)`)
	maskAuth   = regexp.MustCompile(`(?i)(Bearer|Basic)` + pySpace + `+[A-Za-z0-9._~+/=-]{12,}`)
	maskAssign = regexp.MustCompile(`(?i)((?:token|secret|passw(?:or)?d|api[_-]?key|authorization)["']?` + pySpace + `*[=:]` + pySpace + `*)["']?` + pyNonSpaceQ + `{6,}`)
)

// tokenAlt は \b(?:gh[pousr]_[A-Za-z0-9]{20,}|...)\b の 1 つの選択肢(接頭辞 + 1 種類の文字の繰り返し)。
type tokenAlt struct {
	prefix   func(s string) int // 接頭辞に当たれば長さ、当たらなければ -1
	cls      func(b byte) bool
	min, max int // max < 0 は上限なし
}

func lit(p string) func(string) int {
	return func(s string) int {
		if strings.HasPrefix(s, p) {
			return len(p)
		}
		return -1
	}
}

func litSet(a string, set string, c string) func(string) int {
	return func(s string) int {
		if len(s) >= len(a)+1+len(c) && strings.HasPrefix(s, a) && strings.IndexByte(set, s[len(a)]) >= 0 && strings.HasPrefix(s[len(a)+1:], c) {
			return len(a) + 1 + len(c)
		}
		return -1
	}
}

func alnum(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }

var tokenAlts = []tokenAlt{
	{litSet("gh", "pousr", "_"), alnum, 20, -1},
	{lit("github_pat_"), func(b byte) bool { return alnum(b) || b == '_' }, 20, -1},
	{litSet("xox", "abprs", "-"), func(b byte) bool { return alnum(b) || b == '-' }, 10, -1},
	{lit("AKIA"), func(b byte) bool { return b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' }, 16, 16},
	{lit("sk-"), func(b byte) bool { return alnum(b) || b == '_' || b == '-' }, 20, -1},
}

// matchToken は s[i:] が \b(?:...)\b に当たれば、その終わり(Python の後戻りと同じく、長い方から \b を探す)。
func matchToken(s string, i int) int {
	if !pyBoundary(s, i) {
		return -1
	}
	for _, a := range tokenAlts {
		p := a.prefix(s[i:])
		if p < 0 {
			continue
		}
		st := i + p
		L := 0
		for st+L < len(s) && a.cls(s[st+L]) && (a.max < 0 || L < a.max) {
			L++
		}
		for k := L; k >= a.min; k-- {
			if pyBoundary(s, st+k) {
				return st + k
			}
		}
	}
	return -1
}

// mask は (マスク後の文字列, マスクした数)。
func mask(text string) (string, int) {
	n := 0
	out, k := pySubB(maskKey, text, false, func([]int) string { return "[マスク]" })
	n += k
	var b strings.Builder
	for i := 0; i < len(out); {
		if e := matchToken(out, i); e >= 0 {
			b.WriteString("[マスク]")
			n++
			i = e
			continue
		}
		_, w := utf8.DecodeRuneInString(out[i:])
		b.WriteString(out[i : i+w])
		i += w
	}
	out = b.String()
	out, k = pySubB(maskAuth, out, true, func(loc []int) string { return out[loc[2]:loc[3]] + " [マスク]" })
	n += k
	cur := out
	out, k = pySubB(maskAssign, cur, false, func(loc []int) string { return cur[loc[2]:loc[3]] + "[マスク]" })
	n += k
	return out, n
}

func pySlice(s string, a, b int) string {
	if a < 0 {
		a = 0
	}
	if b > len(s) {
		b = len(s)
	}
	if a >= b {
		return ""
	}
	return s[a:b]
}

func isComment(rest string) bool {
	return (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")) && strings.HasPrefix(strings.TrimLeftFunc(rest, pyIsSpace), "#")
}

var (
	reDQSpecial = regexp.MustCompile("[$`\\\\]")
	reUnquoted  = regexp.MustCompile("[$`;&|()<>\\\\ \t'\"*?~{]")
	reSpaceHash = regexp.MustCompile(pySpace + `#`)
)

// shValue は text[i:] から shell の代入の右辺を、展開せずにそのまま取り出す。(値, 解釈が不完全か, 値の終わりの位置)。
func shValue(text string, i int) (string, bool, int) {
	n := len(text)
	eol := -1
	if i <= n {
		if k := strings.IndexByte(text[i:], '\n'); k >= 0 {
			eol = i + k
		}
	}
	lineEnd := n
	if eol >= 0 {
		lineEnd = eol
	}
	line := pySlice(text, i, lineEnd)
	if i >= n || pyStrip(line) == "" {
		return "", false, lineEnd
	}
	restEnd := func(j int) int {
		switch {
		case eol >= 0 && eol > j:
			return eol
		case eol < 0:
			return n
		}
		return j + 1
	}
	switch text[i] {
	case '\'':
		k := strings.IndexByte(text[i+1:], '\'')
		if k < 0 {
			return text[i+1:], true, n
		}
		j := i + 1 + k
		rest := pySlice(text, j+1, restEnd(j))
		return text[i+1 : j], rest != "" && !isComment(rest), j + 1
	case '"':
		j := i + 1
		for j < n {
			if text[j] == '\\' {
				j += 2
				continue
			}
			if text[j] == '"' {
				break
			}
			j++
		}
		val := pySlice(text, i+1, j)
		rest := pySlice(text, j+1, restEnd(j))
		return val, reDQSpecial.MatchString(val) || (rest != "" && !isComment(rest)), min(j+1, n)
	}
	raw := line
	if loc := reSpaceHash.FindStringIndex(line); loc != nil {
		raw = line[:loc[0]]
	}
	raw = pyStrip(raw)
	end := lineEnd
	for strings.HasSuffix(pySlice(text, i, end), "\\") && end < n {
		if k := strings.IndexByte(text[end+1:], '\n'); k >= 0 {
			end = end + 1 + k
		} else {
			end = n
		}
	}
	return raw, reUnquoted.MatchString(raw), end
}

// jobText はジョブ定義の本文(DB の sched-job:<job>。SCHED_FILE_FALLBACK=1 なら、無いときに jobs/<名>.conf)。limit 文字まで。
func (v *sv) jobText(name string, limit int) (string, bool) {
	if b, ok := v.dbBody("sched-job:" + name); ok {
		return runesUpTo(b, limit), true
	}
	if !filesOK() {
		return "", false
	}
	p := filepath.Join(schedJobs(), name+".conf")
	if st, err := os.Lstat(p); err != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return "", false
	}
	return readUpTo(p, limit)
}

var reJobAssign = regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+)?(` + strings.Join(jobKeys, "|") + `)=`)

type jobDefsT struct {
	vals      map[string]string
	complex   map[string]bool
	truncated bool
}

func (d *jobDefsT) get(k string) string { return d.vals[k] }

// jobDefs は PROMPT 等の記述(展開しない)。読めなければ空の定義。
func (v *sv) jobDefs(name string) (*jobDefsT, bool) {
	d := &jobDefsT{vals: map[string]string{}, complex: map[string]bool{}}
	text, ok := v.jobText(name, jobConfMax+1)
	if !ok {
		return d, false
	}
	all := reJobAssign.FindAllStringSubmatchIndex(text, -1)
	pos := 0
	for {
		var m []int
		for _, x := range all {
			if x[0] >= pos {
				m = x
				break
			}
		}
		if m == nil {
			break
		}
		key := text[m[2]:m[3]]
		val, inexact, p := shValue(text, m[1])
		d.vals[key] = val // 後ろの代入が勝つ(source と同じ)
		pos = max(p, m[1])
		d.complex[key] = inexact
	}
	d.truncated = utf8.RuneCountInString(text) > jobConfMax
	return d, true
}

var reBoiler = regexp.MustCompile(`^(注意: COMMAND は bash -c|HERDR_SESSION は|先頭を / にしないこと|xargs は空入力では)`)

// jobAbout は conf の先頭のコメント(最初の代入より前の # 行)を説明として返す。
func (v *sv) jobAbout(name string) []string {
	text, ok := v.jobText(name, jobConfMax)
	if !ok {
		return nil
	}
	var block []string
	for _, line := range pySplitlines(text) {
		t := pyStrip(line)
		if t == "" {
			if len(block) > 0 {
				break
			}
			continue
		}
		if !strings.HasPrefix(t, "#") {
			break
		}
		block = append(block, pyStrip(strings.TrimLeft(t, "#")))
	}
	masked, _ := mask(Clean(strings.Join(block, "\n")))
	var out []string
	for _, t := range pySplitlines(masked) {
		if t = pyStrip(t); t != "" && !reBoiler.MatchString(t) {
			out = append(out, t)
		}
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// tbCmds は COMMAND 型でよく使う tb のサブコマンドの説明(両端は Python の \b で確かめる)。
var tbCmds = []struct {
	re          *regexp.Regexp
	label, desc string
}{
	{pyRe(`tb\s+notify\s+flush`), "tb notify flush", "通知のキューを進める(承認待ち・定期まとめを Slack などへ送る。返事の無いものは再送)"},
	{pyRe(`tb\s+(?:message\s+)?flush`), "tb message flush", "届かなかった送信(宛先が作業中など)を再送する"},
	{pyRe(`tb\s+digest`), "tb digest", "worker から指揮官への報告をまとめて送る"},
	{pyRe(`tb\s+pr(?:-|\s+)watch`), "tb pr watch", "許可したリポジトリの PR の動き(新規・approve・変更依頼・マージ)を通知に積む"},
	{pyRe(`tb\s+compact-check`), "tb compact-check", "文脈が大きくなった待機中の agent に /compact を送る"},
	{pyRe(`tb\s+pending`), "tb pending", "未報告のタスクと未配送の報告(C-<n>)を一覧する(再送は続く tb report が行う)"},
	{pyRe(`tb\s+system-send`), "tb system-send", "agent に本文を送る(scheduler からの送信)"},
	{pyRe(`tb\s+review(?:-|\s+)pack`), "tb review pack", "レビュー用の集計を作る"},
	{pyRe(`tb\s+pr(?:-|\s+)queue\s+scan`), "tb pr queue scan", "origin のブランチを走査し、本文があるものを PR 作成キューに積む"},
	{pyRe(`tb\s+pr(?:-|\s+)queue\s+run`), "tb pr queue run", "承認済みの PR 作成キューを、ガードを通して draft PR にする"},
}

// pyRe は Python の \s(Unicode の空白)を Go の正規表現に置き換える。
func pyRe(p string) *regexp.Regexp { return regexp.MustCompile(strings.ReplaceAll(p, `\s`, pySpace)) }

// commandSummary は COMMAND から tb のサブコマンドを見つけて [(コマンド, 説明)]。
func commandSummary(cmd string) [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	for _, c := range tbCmds {
		if !seen[c.label] && pyFindB(c.re, cmd, 0, true, true) != nil {
			seen[c.label] = true
			out = append(out, [2]string{c.label, c.desc})
		}
	}
	return out
}

// jobSummary は一覧用の 1 行: conf の先頭コメントの 1 行目。無ければ COMMAND から推した説明。
func (v *sv) jobSummary(name string, info *jobInfo) string {
	if about := v.jobAbout(name); len(about) > 0 {
		return about[0]
	}
	if info != nil && info.kind != "prompt" {
		d, _ := v.jobDefs(name)
		if cs := commandSummary(d.get("COMMAND")); len(cs) > 0 {
			var ds []string
			for _, c := range cs {
				ds = append(ds, c[1])
			}
			return strings.Join(ds, " / ")
		}
	}
	return ""
}

// pyRealpath は os.path.realpath(strict でない)。posix では posixpath と同じく 1 つずつ symlink をたどり、
// 存在しない部分はそのまま、.. はそこまでたどった実体から 1 つ上がる。
func pyRealpath(p string) string {
	if !filepathIsPosix {
		return winRealpath(p)
	}
	rest := strings.Split(p, "/")
	for l, r := 0, len(rest)-1; l < r; l, r = l+1, r-1 {
		rest[l], rest[r] = rest[r], rest[l]
	}
	path := "/"
	if !strings.HasPrefix(p, "/") {
		path, _ = os.Getwd()
	}
	const mark = "\x00" // 解決済みのしるし(次に積んだものが symlink のパス)
	seen := map[string]*string{}
	for len(rest) > 0 {
		name := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		if name == mark {
			sl := rest[len(rest)-1]
			rest = rest[:len(rest)-1]
			r := path
			seen[sl] = &r
			continue
		}
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			if i := strings.LastIndex(path, "/"); i > 0 {
				path = path[:i]
			} else {
				path = "/"
			}
			continue
		}
		newpath := path + "/" + name
		if path == "/" {
			newpath = "/" + name
		}
		st, err := os.Lstat(newpath)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			path = newpath
			continue
		}
		if r, ok := seen[newpath]; ok {
			if r != nil {
				path = *r
			} else {
				path = newpath // ループ
			}
			continue
		}
		target, err := os.Readlink(newpath)
		if err != nil {
			path = newpath
			continue
		}
		if strings.HasPrefix(target, "/") {
			path = "/"
		}
		seen[newpath] = nil
		rest = append(rest, newpath, mark)
		tp := strings.Split(target, "/")
		for k := len(tp) - 1; k >= 0; k-- {
			rest = append(rest, tp[k])
		}
	}
	return path
}

// winRealpath は Windows 向けの近似(存在する祖先まで実体にし、残りはそのまま)。
func winRealpath(p string) string {
	p, _ = filepath.Abs(p)
	rest := ""
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

var (
	denyDirs   = []string{".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".password-store", ".config/gcloud", "Keychains"}
	reDenyName = regexp.MustCompile(`(?i)(^\.env(\..*)?$|^id_[a-z0-9]+(\.pub)?$|\.(pem|key|p12|pfx|keychain|keychain-db)$|^\.netrc$|^\.pgpass$|^credentials(\.json)?$)`)
)

func runBase() string { return cfg("WEBUI_RUN_BASE", filepath.Join(Root(), "run")) }

// under は path が root(の実体)と同じか、その配下か(存在する祖先で比べる)。
func under(path, root string) bool {
	rs, err := os.Stat(root)
	if err != nil || !rs.IsDir() {
		return false
	}
	cur, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	for {
		if st, err := os.Stat(cur); err == nil && os.SameFile(st, rs) {
			return true
		}
		nxt := filepath.Dir(cur)
		if nxt == cur {
			return false
		}
		cur = nxt
	}
}

// denied は認証情報らしい場所・Web UI 自身のログイン情報なら理由(docs.denied)。
func denied(path string) string {
	joined := strings.ToLower(path) + "/"
	for _, d := range denyDirs {
		if strings.Contains(joined, "/"+strings.ToLower(d)+"/") {
			return "認証情報が置かれる場所なので開きません"
		}
	}
	if reDenyName.MatchString(pyBasename(path)) {
		return "認証情報らしいファイル名なので開きません"
	}
	for _, r := range []string{RunDir(), runBase(), AuthDir(), StateDir()} {
		if under(path, r) {
			return "Web UI のログイン情報なので開きません"
		}
	}
	return ""
}

// promptFile は PROMPT_FILE の中身。(本文, 読めたか, 注記)。
func promptFile(path string) (string, bool, string) {
	if path == "" {
		return "", false, ""
	}
	if !filepath.IsAbs(path) {
		return "", false, "相対パスなので、どこから読まれるか webui には分からず、中身は表示しません"
	}
	real := pyRealpath(path)
	inRoot := false
	for _, r := range []string{pyRealpath(schedRoot()), pyRealpath(schedJobs())} {
		if real == r || strings.HasPrefix(real, r+string(os.PathSeparator)) {
			inRoot = true
		}
	}
	if !inRoot {
		return "", false, "scheduler/(と jobs の置き場)の外なので、中身は表示しません"
	}
	if denied(real) != "" {
		return "", false, "認証情報などの置き場なので、中身は表示しません"
	}
	if !isFile(real) {
		return "", false, "ファイルがありません(tick は failed になります)"
	}
	body, ok := readUpTo(real, jobFileMax+1)
	if !ok {
		return "", false, "読めません(tick は failed になります)"
	}
	if utf8.RuneCountInString(body) > jobFileMax {
		return runesUpTo(body, jobFileMax), true, fmt.Sprintf("先頭 %d KiB だけ表示しています", jobFileMax/1024)
	}
	return body, true, ""
}

func dayNums(days string) map[int]bool {
	out := map[int]bool{}
	for _, t := range pyFields(strings.ReplaceAll(strings.ToLower(days), ",", " ")) {
		switch t {
		case "daily", "everyday":
			for i := 1; i <= 7; i++ {
				out[i] = true
			}
		case "weekdays":
			for i := 1; i <= 5; i++ {
				out[i] = true
			}
		case "weekends":
			out[6], out[7] = true, true
		default:
			for i, p := range dayTokens {
				if strings.HasPrefix(t, p) {
					out[i+1] = true
				}
			}
		}
	}
	return out
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func hmsOf(s string, end bool) (int, bool) {
	p := strings.Split(s, ":")
	if len(p) < 2 {
		return 0, false
	}
	var n [3]int
	for i := 0; i < len(p) && i < 3; i++ {
		if _, err := fmt.Sscanf(p[i], "%d", &n[i]); err != nil {
			return 0, false
		}
	}
	sec := n[2]
	if len(p) <= 2 && end {
		sec = 59
	}
	return n[0]*3600 + n[1]*60 + sec, true
}

var nowFn = time.Now

// nextRuns は次回の実行予定(ジョブの TZ の時刻文字列)。(リスト, 注記)。
func nextRuns(info *jobInfo, n int) ([]string, string) {
	if !info.enabled {
		return nil, "一時停止中のため、次回の予定はありません"
	}
	unknown := "次回の予定を計算できません(不明)"
	if info.tz == "" || info.tz == "Local" {
		return nil, unknown
	}
	loc, err := time.LoadLocation(info.tz)
	if err != nil {
		return nil, unknown
	}
	now := nowFn().In(loc)
	if info.at != "" {
		at, err := sched.ParseAt(info.at, loc)
		if err != nil {
			return nil, unknown
		}
		if at.Before(now.Truncate(time.Minute)) {
			return nil, "1 回だけのジョブで、その日時(" + info.at + ")は過ぎました"
		}
		return []string{at.Format("2006-01-02 15:04:05")}, ""
	}
	iv := info.intervalSec
	if iv == 0 {
		iv = max(info.intervalMin, 0) * 60
	}
	start, ok1 := hmsOf(info.start, false)
	end, ok2 := hmsOf(info.end, true)
	nums := dayNums(info.days)
	if !ok1 || !ok2 || iv <= 0 || len(nums) == 0 {
		return nil, unknown
	}
	var out []string
	for off := 0; off < 9; off++ {
		d := now.AddDate(0, 0, off)
		if !nums[(int(d.Weekday())+6)%7+1] {
			continue
		}
		k := 0
		if off == 0 {
			sod := now.Hour()*3600 + now.Minute()*60 + now.Second()
			k = max(0, floorDiv(sod-start, iv)+1)
		}
		for t := start + k*iv; t <= end && len(out) < n; t += iv {
			out = append(out, fmt.Sprintf("%s %02d:%02d:%02d", d.Format("2006-01-02"), t/3600, t%3600/60, t%60))
		}
		if len(out) >= n {
			break
		}
	}
	if len(out) == 0 {
		return nil, "1 週間以内に実行予定がありません"
	}
	return out, ""
}

// sendTaskDecision は参考: 通常の tb send(send:task:<宛先>)の判定。読めなければ ""。
func (v *sv) sendTaskDecision(target string) string {
	rules, err := v.loadRules()
	if err != "" || target == "" {
		return ""
	}
	p := &perms{v: v, rules: rules, ops: loadOps()}
	d, _ := p.decide("send:task:" + Clean(target))
	return d
}

type agentSt struct {
	err               string
	count             int
	status, workspace string
	registry          row
}

// workspaceLabel は workspace_id → 表示ラベル(herdr.workspace_label。取れなければ nil)。
func workspaceLabel(sess string, id any) any {
	m := cached(sess, "workspaces", func() map[string]any {
		out, err := runHerdr(sess, "workspace", "list")
		res := map[string]any{}
		if err != "" {
			return res
		}
		d, ok := decodeObject(out)
		r, ok2 := d["result"].(map[string]any)
		ws, ok3 := r["workspaces"].([]any)
		if !ok || !ok2 || !ok3 {
			return res
		}
		for _, x := range ws {
			w, ok := x.(map[string]any)
			if !ok {
				return map[string]any{}
			}
			k, ok := w["workspace_id"]
			if !ok {
				return map[string]any{}
			}
			if ks, ok := k.(string); ok {
				res[ks] = w["label"]
			}
		}
		return res
	})
	if k, ok := id.(string); ok {
		return m[k]
	}
	return nil
}

// agentState は宛先 agent の今の状態。
func (v *sv) agentState(target string) agentSt {
	agents, err := getAgents(v.sess)
	if err != "" {
		return agentSt{err: Clean(err)}
	}
	var hit []map[string]any
	for _, a := range agents {
		if a["name"] == target {
			hit = append(hit, a)
		}
	}
	out := agentSt{count: len(hit), registry: row{}}
	if rows, ok := v.qChecked("SELECT * FROM agents WHERE name=?", target); ok && len(rows) > 0 {
		out.registry = rows[0]
	}
	if len(hit) == 1 {
		a := hit[0]
		out.status = orStr(truthyStr(a["agent_status"]), "")
		out.workspace = truthyStr(workspaceLabel(v.sess, a["workspace_id"]))
		if out.workspace == "" {
			out.workspace = truthyStr(a["workspace_id"])
		}
	}
	return out
}

// truthyStr は (x or "") を文字列にしたもの。
func truthyStr(x any) string {
	if !truthy(x) {
		return ""
	}
	return pyStr(x)
}
