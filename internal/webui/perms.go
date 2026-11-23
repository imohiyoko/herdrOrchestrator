package webui

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// perms.go は webui/app/perms.py: 設定 policy の読み取りと照合(読み取り専用。tb の perm_authorize と同じ考え。ADR 0007)。

var (
	reAction  = regexp.MustCompile(`^[A-Za-z0-9._:*?@][A-Za-z0-9._:*?@-]{0,127}$`)
	effects   = []string{"allow", "ask", "deny"} // 緩い順
	legacyAsk = []string{"ask-any", "ask-human"} // 前の効果。読むときは ask と同じ
	// 一致の例に使う、tb が実際に出す操作名(宛先は仮の名前)
	sampleOps = []string{"send:task:worker1", "send:info:worker1", "send:answer:worker1", "role:change", "proxy:read-only",
		"proxy:write", "proxy:external", "alert:analyze", "setup:hooks", "setup:scheduler", "setup:webui",
		"setup:launchctl", "agent:new:worker1", "agent:stop:worker1", "agent:revive:worker1"}
	catKinds   = []string{"send", "agent", "alert", "proxy", "role", "gate", "setup", "recipe"}
	catSide    = []string{"none", "local-write", "external-send", "external-write"}
	catRisk    = []string{"low", "mid", "high", "human-only"}
	catAllowOK = []string{"yes", "no", "conditional"}
	catStates  = []string{"active", "unwired", "reserved"}
)

const permsMaxBytes = 262144

func rank(eff string) int {
	if contains(legacyAsk, eff) {
		eff = "ask"
	}
	for i, x := range effects {
		if x == eff {
			return i
		}
	}
	return len(effects) - 1
}

// pyFields は str.split()(空白の並びで区切る)。
func pyFields(s string) []string { return strings.FieldsFunc(s, pyIsSpace) }

// readUpTo は open(..., errors="replace").read(n)(n 文字まで)。1 文字は 4 バイトまでなので、それより先は読まない。
func readUpTo(path string, n int) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(n)*4+4))
	if err != nil {
		return "", false
	}
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s, true
}

type opDef struct{ pat, def string }

// loadOps は既定の表 perms/ops.tbl。読めなければ空(どの操作も deny)。
func loadOps() []opDef {
	text, ok := readUpTo(filepath.Join(HerdrRoot(), "taskboard", "perms", "ops.tbl"), permsMaxBytes)
	if !ok {
		return nil
	}
	var out []opDef
	for _, ln := range pySplitlines(text) {
		body, _, _ := strings.Cut(ln, "#")
		if f := pyFields(body); len(f) == 2 {
			out = append(out, opDef{f[0], f[1]})
		}
	}
	return out
}

// splitAct は tb の perm_split_act: 古い形の操作名(send:task:w1)を (操作名, 対象) に。
func splitAct(a string) (string, string) {
	for _, p := range []string{"role:", "alert:", "proxy:", "setup:"} {
		if strings.HasPrefix(a, p) {
			return a, ""
		}
	}
	if rest, ok := strings.CutPrefix(a, "agent:slash:"); ok {
		if c, t, ok := strings.Cut(rest, ":"); ok {
			return "agent:slash:" + c, t
		}
	}
	parts := strings.SplitN(a, ":", 3)
	if len(parts) < 3 {
		return a, ""
	}
	return parts[0] + ":" + parts[1], parts[2]
}

type rule struct {
	no                                              int
	subject, op, target, effect, note, raw, pattern string
}

// perms は 1 リクエストの間の policy と既定の表。
type perms struct {
	v     *sv
	rules []*rule
	ops   []opDef
}

// loadRules は設定 policy の行。読めなければ (nil, エラー文)。
func (v *sv) loadRules() ([]*rule, string) {
	text, _, ok := v.configText("policy")
	if !ok {
		return nil, "" // 設定が無ければ、すべて既定の表で決まる
	}
	if len(text) > permsMaxBytes {
		return nil, "policy が大きすぎて(" + strconv.Itoa(permsMaxBytes/1024) + " KiB 超)全部は読めません。一覧と判定は出しません(tb は全体を読むので、手元で tb approval check を使ってください)"
	}
	var rules []*rule
	for _, ln := range pySplitlines(text) {
		body, note, _ := strings.Cut(ln, "#")
		body = pyStrip(body)
		lhs, val, ok := strings.Cut(body, "=")
		if !ok {
			continue
		}
		f, val := pyFields(lhs), pyStrip(val)
		if len(f) != 3 || (!contains(effects, val) && !contains(legacyAsk, val)) {
			continue
		}
		raw := strings.Join(f, " ")
		rules = append(rules, &rule{no: len(rules) + 1, subject: f[0], op: f[1], target: f[2], effect: val,
			note: Clean(pyStrip(note)), raw: raw, pattern: Clean(raw)})
	}
	return rules, ""
}

// hasBracket は bash の glob と fnmatch で結果が変わりうる行([...] と、拡張 glob の !(...) など)。
func hasBracket(r *rule) bool {
	return strings.ContainsAny(r.op+r.target+r.subject, "[(")
}

// tri は True / False / None(確かめられない)。
type tri int

const (
	triNo tri = iota
	triYes
	triUnknown
)

func triOf(b bool) tri {
	if b {
		return triYes
	}
	return triNo
}

// roleMatch は @<役割> の照合。台帳を読めず役割を確かめられないときは triUnknown。
func (p *perms) roleMatch(name, pat string) tri {
	reg, ok := p.v.qChecked("SELECT role FROM agents WHERE name=?", name)
	if !ok {
		if p.v.roleExists(name, "") {
			return triOf(p.v.nameMatches(name, pat, ""))
		}
		return triUnknown
	}
	regRole := ""
	if len(reg) > 0 && reg[0]["role"] != nil {
		regRole = pyStr(reg[0]["role"])
	}
	return triOf(p.v.nameMatches(name, pat, regRole))
}

func (p *perms) targetMatches(target, t, who string, hasWho bool) tri {
	switch {
	case t == "*":
		return triYes
	case target == "":
		return triNo // 対象の無い操作に当たるのは * の行だけ
	case t == "self":
		return triOf(hasWho && who != "" && who == target)
	case strings.HasPrefix(t, "~"):
		if hasWho && who != "" {
			return triUnknown // 関係(親子・組織図)はこの画面では確かめない
		}
		return triNo
	case strings.HasPrefix(t, "@"):
		return p.roleMatch(target, t)
	}
	return triOf(fnmatchcase(target, t))
}

func (p *perms) opDefault(op string) string {
	for _, d := range p.ops {
		if fnmatchcase(op, d.pat) {
			return d.def
		}
	}
	return "deny"
}

// decide は (効果, 決めた行 or nil)。主体 * の行だけを見る(tb approval check の --by なしと同じ)。
// [ を含む行があれば ("unknown", その行)。
func (p *perms) decide(act string) (string, *rule) {
	op, target := splitAct(act)
	var best, unk *rule
	for _, r := range p.rules {
		if r.subject == "human" || strings.HasPrefix(r.subject, "system:") {
			continue
		}
		if hasBracket(r) {
			return "unknown", r
		}
		if !fnmatchcase(op, r.op) || r.subject != "*" {
			continue
		}
		tm := p.targetMatches(target, r.target, "", false)
		if tm == triNo {
			continue
		}
		if tm == triYes {
			if best == nil || rank(r.effect) > rank(best.effect) {
				best = r
			}
		} else if unk == nil || rank(r.effect) > rank(unk.effect) {
			unk = r
		}
	}
	eff, hit := "", best
	if best != nil {
		eff = best.effect
	} else {
		eff = p.opDefault(op)
	}
	if unk != nil && rank(unk.effect) > rank(eff) {
		eff, hit = unk.effect, unk
	}
	return eff, hit
}

func wildcard(pat string) bool { return strings.ContainsAny(pat, "*?[") }

// permWarning は行ごとの警告文(無ければ空)。
func permWarning(r *rule) string {
	if hasBracket(r) {
		return "[ を含むパターン: bash と照合規則が違いうるので、この画面ではこの行に当たりうる判定をしません(手元で tb approval check を使ってください)"
	}
	if r.effect != "allow" {
		return ""
	}
	if r.op == "*" || strings.HasSuffix(r.op, ":*") && strings.Count(r.op, ":") < 2 {
		return "広い allow: 操作の種類全体を承認なしにします(agent が自分の権限を広げうる)"
	}
	if r.subject == "*" && wildcard(r.target) {
		return "ワイルドカードの allow: どの agent からも、該当する対象すべてが承認なしになります"
	}
	return ""
}

// stricterOver は r に重なる、もっと厳しい行(厳しい方が勝つので、重なる相手では r は効かない)。
func (p *perms) stricterOver(r *rule) *rule {
	for _, o := range p.rules {
		if o == r || rank(o.effect) <= rank(r.effect) || hasBracket(o) {
			continue
		}
		if fnmatchcase(r.op, o.op) && (o.subject == "*" || o.subject == r.subject) && (o.target == "*" || o.target == r.target) {
			return o
		}
	}
	return nil
}

// examples は行ごとの「この行で決まる操作の例」(例の操作を 1 回ずつ判定して、決めた行ごとにまとめる)。
func (p *perms) examples() map[*rule][]string {
	out := map[*rule][]string{}
	for _, op := range sampleOps {
		if _, hit := p.decide(op); hit != nil && !hasBracket(hit) {
			out[hit] = append(out[hit], op)
		}
	}
	return out
}

// tbCommand は表示するだけのコマンド行。検査に通らなければ ""。
func tbCommand(val, pat string) string {
	if (val != "allow" && val != "ask") || !reAction.MatchString(pat) {
		return ""
	}
	q := pat
	if strings.ContainsAny(pat, "*?") { // shlex.quote(使える文字のうち、そのままでは危ないのは * と ? だけ)
		q = "'" + pat + "'"
	}
	return "tb approval " + val + " " + q
}

// policyRow は tb approval allow|ask <パターン> が書く policy の行。
func policyRow(val, pat string) string {
	op, target := splitAct(pat)
	if target == "" {
		target = "*"
	}
	eff := "ask"
	if val == "allow" {
		eff = "allow"
	}
	return "* " + op + " " + target + " = " + eff
}

func pyBasename(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func pyDirname(p string) string {
	head := p[:strings.LastIndex(p, "/")+1]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}

// confPathLabel は policy の出どころ(画面の表示用)。
func (v *sv) confPathLabel() string {
	_, src, _ := v.configText("policy")
	if strings.HasPrefix(src, "file:") || strings.HasPrefix(src, "example:") {
		_, p, _ := strings.Cut(src, ":")
		return pyBasename(pyDirname(p)) + "/" + pyBasename(p)
	}
	if src == "db:*" {
		return "DB の設定 policy(全セッション共通)"
	}
	if s, ok := strings.CutPrefix(src, "db:"); ok {
		return "DB の設定 policy(セッション " + s + ")"
	}
	return "policy(未設定。すべて既定の表 taskboard/perms/ops.tbl で決まる)"
}

// ---- 権限カタログ(taskboard/config/approvals-catalog.conf。操作の定義。判定には使わない)

type catEntry struct{ pattern, kind, side, risk, allowOK, state, desc string }

// loadCatalog は (項目, エラー文, 不正な行の数)。
func loadCatalog() ([]catEntry, string, int) {
	text, ok := readUpTo(filepath.Join(HerdrRoot(), "taskboard", "config", "approvals-catalog.conf"), permsMaxBytes+1)
	if !ok {
		return nil, "権限カタログ(approvals-catalog.conf)を読めません", 0
	}
	if len(text) > permsMaxBytes {
		return nil, "権限カタログが大きすぎて読めません", 0
	}
	var out []catEntry
	bad := 0
	seen := map[string]bool{}
	for _, ln := range pySplitlines(text) {
		t := pyStrip(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		p := strings.SplitN(ln, "|", 7)
		for i := range p {
			p[i] = pyStrip(p[i])
		}
		if len(p) != 7 || !reAction.MatchString(p[0]) || !contains(catKinds, p[1]) || !contains(catSide, p[2]) || !contains(catRisk, p[3]) ||
			!contains(catAllowOK, p[4]) || !contains(catStates, p[5]) || p[6] == "" ||
			strings.SplitN(p[0], ":", 2)[0] != p[1] || // 種別はパターンの先頭と同じ
			(p[3] == "human-only" && p[4] != "no") || // human-only は allow 候補にしない
			(p[4] == "conditional" && strings.Count(p[0], ":") < 2) || // 宛先・対象の無い操作名は絞れない
			seen[p[0]] { // 重複
			bad++
			continue
		}
		seen[p[0]] = true
		out = append(out, catEntry{p[0], p[1], p[2], p[3], p[4], p[5], Clean(p[6])})
	}
	return out, "", bad
}

// representative はパターンの * と ? を具体的な名前に置いた 1 例(判定に使う)。
func representative(pattern string) string {
	return strings.ReplaceAll(strings.ReplaceAll(pattern, "*", "example"), "?", "x")
}

// covers は規則がカタログの項目に当たるか(操作名と対象の glob を双方向に当てる)。
func covers(catPattern string, r *rule) bool {
	cop, ct := splitAct(catPattern)
	if !fnmatchcase(cop, r.op) && !fnmatchcase(r.op, cop) {
		return false
	}
	t := r.target
	return t == "*" || ct == "" || ct == "*" || fnmatchcase(ct, t) || fnmatchcase(t, ct) ||
		strings.HasPrefix(t, "@") || strings.HasPrefix(t, "~") || t == "self"
}

// ruleInert は規則が当たるカタログの項目がすべて unwired なら true(その行は何も効かない)。
func ruleInert(r *rule, catalog []catEntry) bool {
	hit := false
	for _, c := range catalog {
		if covers(c.pattern, r) {
			if c.state != "unwired" {
				return false
			}
			hit = true
		}
	}
	return hit
}

// unknownRules はカタログのどれにも当たらない規則(操作 * と cmd: は除く)。
func unknownRules(rules []*rule, catalog []catEntry) []*rule {
	var out []*rule
outer:
	for _, r := range rules {
		if r.op == "*" || strings.HasPrefix(r.op, "cmd:") {
			continue
		}
		for _, c := range catalog {
			if covers(c.pattern, r) {
				continue outer
			}
		}
		out = append(out, r)
	}
	return out
}

type catRow struct {
	entry                     catEntry
	value                     string
	rule                      *rule
	explicit, risky, riskyExc bool
	exceptions                []*rule
}

// catalogRows はカタログ × policy。
func (p *perms) catalogRows(catalog []catEntry) []catRow {
	var out []catRow
	for _, c := range catalog {
		val, hit := p.decide(representative(c.pattern))
		var exc []*rule
		excAllow := false
		for _, r := range p.rules {
			if r != hit && covers(c.pattern, r) && !hasBracket(r) && (r.subject != "*" || r.target != "*") {
				exc = append(exc, r)
				excAllow = excAllow || r.effect == "allow"
			}
		}
		out = append(out, catRow{entry: c, value: val, rule: hit, explicit: hit != nil, exceptions: exc,
			risky:    val == "allow" && (c.risk == "high" || c.risk == "human-only" || c.allowOK == "no"),
			riskyExc: excAllow && (c.risk == "human-only" || c.allowOK == "no")})
	}
	return out
}
