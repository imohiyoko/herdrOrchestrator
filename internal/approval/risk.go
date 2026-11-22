package approval

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
)

// リスク評価(taskboard/lib/risk.sh。第 1 段: 評価を出すだけで、allow / ask の判定には使わない)。設計: taskboard/docs/risk-based-approvals.md

const (
	riskDefaultAskAt = 2
	riskDefaultWords = "push|merge|--force|--approve|reset --hard|rm -rf"
)

// RiskRules は規則ファイルの場所(固定。環境変数では差し替えない)。
func RiskRules() string {
	root := os.Getenv("TB_ROOT")
	if root == "" {
		root = "."
	}
	return filepath.Join(root, "config", "risk-rules.conf")
}

// Opt は取れたかどうかの付いた値(Set=false は取れなかった = 評価不能)。
type Opt struct {
	V   string
	Set bool
}

func set(v string) Opt { return Opt{v, true} }

// RiskInput は risk_collect が集める入力。
type RiskInput struct {
	SenderRole, TargetRole, TargetState, SenderModel, Text Opt
	Kind                                                   string
}

// Risk は risk_eval の結果。
type Risk struct {
	Value     string // allow|ask
	Score     int
	Threshold int
	Lines     []string
	Warns     []string
}

type riskRule struct{ id, cond, res, why string }

type riskConf struct {
	askAt    int
	unknown  map[string]string // sender.role など → ask|allow|risk:N
	patNames []string
	pats     map[string]string
	rules    []riskRule
	warns    []string
}

var (
	reSection = regexp.MustCompile(`^\[([a-z]+)\]$`)
	rePatName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	reRiskN   = regexp.MustCompile(`^risk:[0-9]+$`)
	reRuleID  = regexp.MustCompile(`^R-[0-9]+$`)
	reInt     = regexp.MustCompile(`^[0-9]+$`)
	reMatches = regexp.MustCompile(`^body\.matches\(([a-z][a-z0-9-]*)\)$`)
	reIn      = regexp.MustCompile(`^(sender\.role|target\.role|target\.state|kind) +in +\(([A-Za-z0-9._, -]+)\)$`)
)

var unknownAttrs = []string{"sender.role", "target.role", "target.state", "sender.model"}

func riskTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' })
}

func level(v string) int {
	switch {
	case v == "ask":
		return 999
	case v == "allow":
		return 0
	case strings.HasPrefix(v, "risk:"):
		n, _ := strconv.Atoi(v[5:])
		return n
	}
	return 0
}

func validValue(v string) bool { return v == "ask" || v == "allow" || reRiskN.MatchString(v) }

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// splitIFS は IFS=<1 字> read の欄の分け方(末尾の区切り 1 つは欄を作らない)。
func splitIFS(s, sep string) []string {
	f := strings.Split(s, sep)
	if len(f) > 1 && f[len(f)-1] == "" {
		f = f[:len(f)-1]
	}
	return f
}

// ereOK は _risk_ere_ok: 正規表現として正しく、空文字列に一致しない。
func ereOK(re string) bool {
	r, err := regexp.Compile(re)
	if err != nil {
		return false
	}
	return !r.MatchString("")
}

func loadRisk() (*riskConf, string) {
	c := &riskConf{
		askAt:    riskDefaultAskAt,
		unknown:  map[string]string{"sender.role": "ask", "target.role": "ask", "target.state": "ask", "sender.model": "risk:1"},
		patNames: []string{"destructive"},
		pats:     map[string]string{"destructive": riskDefaultWords},
		rules: []riskRule{
			{"R-02", "body.matches(destructive)", "ask", "本文に破壊的・公開系の操作語"},
			{"R-06", "target.state in (stopped)", "risk:2", "宛先が停止中"},
		},
	}
	f := RiskRules()
	st, err := os.Stat(f)
	if err != nil {
		if _, lerr := os.Lstat(f); lerr != nil {
			return c, "" // ファイルなし: 既定だけで動く
		}
	}
	data, rerr := os.ReadFile(f)
	if err != nil || !st.Mode().IsRegular() || rerr != nil {
		return nil, "規則ファイルを読めません"
	}
	body := string(data)
	lines := strings.Split(body, "\n")
	if strings.HasSuffix(body, "\n") {
		lines = lines[:len(lines)-1]
	}
	sec, fileAskAt := "", ""
	var seen []string
	var uAttrs, uVals []string
	for i, line := range lines {
		n := i + 1
		line = riskTrim(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := reSection.FindStringSubmatch(line); m != nil {
			sec = m[1]
			if sec != "threshold" && sec != "unknown" && sec != "patterns" && sec != "rules" {
				return nil, fmt.Sprintf("L%d: 未知の節 [%s]", n, sec)
			}
			continue
		}
		kv := func(what string) (string, string, string) {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return "", "", fmt.Sprintf("L%d: '%s' の形ではありません", n, what)
			}
			return riskTrim(k), riskTrim(v), ""
		}
		switch sec {
		case "threshold":
			k, v, e := kv("キー = 値")
			if e != "" {
				return nil, e
			}
			if k != "ask-at" {
				return nil, fmt.Sprintf("L%d: [threshold] で使えるのは ask-at だけです", n)
			}
			if !reInt.MatchString(v) || atoi(v) < 1 || atoi(v) > 99 {
				return nil, fmt.Sprintf("L%d: ask-at は 1 以上の整数です", n)
			}
			fileAskAt = v
		case "unknown":
			k, v, e := kv("キー = 値")
			if e != "" {
				return nil, e
			}
			switch k {
			case "sender.role", "target.role", "target.state", "sender.model":
			default:
				return nil, fmt.Sprintf("L%d: [unknown] に未知の属性 %s", n, k)
			}
			if !validValue(v) {
				return nil, fmt.Sprintf("L%d: 値は ask | allow | risk:<整数> です: %s", n, v)
			}
			uAttrs, uVals = append(uAttrs, k), append(uVals, v)
		case "patterns":
			k, v, e := kv("名前 = 正規表現")
			if e != "" {
				return nil, e
			}
			if !rePatName.MatchString(k) {
				return nil, fmt.Sprintf("L%d: パターン名は小文字英数字と - です: %s", n, k)
			}
			if v == "" {
				return nil, fmt.Sprintf("L%d: 空のパターンです", n)
			}
			if !ereOK(v) {
				return nil, fmt.Sprintf("L%d: 正規表現として不正、または空文字列に一致します(%s)", n, k)
			}
			if _, ok := c.pats[k]; ok { // 既存の名前には足すだけ(語を減らす = 緩めることはできない)
				c.pats[k] += "|" + v
			} else {
				c.patNames = append(c.patNames, k)
				c.pats[k] = v
			}
		case "rules":
			f := strings.SplitN(line, "|", 4)
			for len(f) < 4 {
				f = append(f, "")
			}
			f[3] = util.ReadRest(f[3], "|")
			id, cond, res, why := riskTrim(f[0]), riskTrim(f[1]), riskTrim(f[2]), riskTrim(f[3])
			if !reRuleID.MatchString(id) {
				return nil, fmt.Sprintf("L%d: 規則 ID は R-<数字> です: %s", n, id)
			}
			if id == "R-02" || id == "R-06" || id == "R-07" {
				return nil, fmt.Sprintf("L%d: %s は組み込みの規則で、ファイルでは定義できません", n, id)
			}
			for _, s := range seen {
				if s == id {
					return nil, fmt.Sprintf("L%d: 規則 ID が重複しています: %s", n, id)
				}
			}
			seen = append(seen, id)
			if cond == "" || why == "" {
				return nil, fmt.Sprintf("L%d: '<ID> | <条件> | <ask|risk:N> | <理由>' の 4 欄が要ります", n)
			}
			if res != "ask" && !reRiskN.MatchString(res) {
				return nil, fmt.Sprintf("L%d: 結果は ask か risk:<整数> です: %s", n, res)
			}
			c.rules = append(c.rules, riskRule{id, cond, res, why})
		default:
			return nil, fmt.Sprintf("L%d: 節([threshold] など)の前に行があります", n)
		}
	}
	if fileAskAt != "" {
		if a := atoi(fileAskAt); a < c.askAt {
			c.askAt = a
		} else if a > c.askAt {
			c.warns = append(c.warns, fmt.Sprintf("緩め設定は第 3 段まで無効: [threshold] ask-at = %s(既定 %d のまま)", fileAskAt, c.askAt))
		}
	}
	for i, a := range uAttrs {
		v := uVals[i]
		if level(v) < level(c.unknown[a]) {
			c.warns = append(c.warns, fmt.Sprintf("緩め設定は第 3 段まで無効: [unknown] %s = %s(既定 %s のまま)", a, v, c.unknown[a]))
		} else {
			c.unknown[a] = v
		}
	}
	for _, r := range c.rules[2:] {
		if strings.HasPrefix(r.res, "risk:") && atoi(r.res[5:]) > c.askAt {
			return nil, fmt.Sprintf("%s: 点がしきい値(%d)を超えています", r.id, c.askAt)
		}
		if !c.condOK(r.cond) {
			return nil, fmt.Sprintf("%s: 条件を解釈できません: %s", r.id, r.cond)
		}
	}
	for _, a := range []string{"sender_role", "target_role", "target_state", "sender_model"} {
		v := c.unknown[strings.Replace(a, "_", ".", 1)]
		if strings.HasPrefix(v, "risk:") && atoi(v[5:]) > c.askAt {
			return nil, fmt.Sprintf("[unknown] %s の点がしきい値(%d)を超えています", a, c.askAt)
		}
	}
	return c, ""
}

func terms(cond string) []string { return splitIFS(strings.ReplaceAll(cond, " and ", "\x01"), "\x01") }

func (c *riskConf) condOK(cond string) bool {
	ts := terms(cond)
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		t = riskTrim(t)
		if m := reMatches.FindStringSubmatch(t); m != nil {
			if _, ok := c.pats[m[1]]; !ok {
				return false
			}
		} else if t != "body.secret" && !reIn.MatchString(t) {
			return false
		}
	}
	return true
}

const wordChars = "_0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// match は _risk_cond_match: 一致すれば補足(語・種類名)と true。
func (c *riskConf) match(cond string, in RiskInput) (string, bool) {
	var hit []string
	for _, t := range terms(cond) {
		t = riskTrim(t)
		if m := reMatches.FindStringSubmatch(t); m != nil {
			re, err := regexp.Compile("(?i)(^|[^" + wordChars + "])(" + c.pats[m[1]] + ")([^" + wordChars + "]|$)")
			if err != nil {
				return "", false
			}
			re.Longest()
			sm := re.FindStringSubmatch(in.Text.V)
			if sm == nil || sm[2] == "" {
				return "", false
			}
			hit = append(hit, "語: "+sm[2])
		} else if t == "body.secret" {
			kinds, _ := notify.Scan(in.Text.V)
			if kinds == "" {
				return "", false
			}
			hit = append(hit, "種類: "+kinds)
		} else if m := reIn.FindStringSubmatch(t); m != nil {
			list := strings.ReplaceAll(m[2], " ", "")
			v := ""
			switch m[1] {
			case "sender.role":
				v = in.SenderRole.V
			case "target.role":
				v = in.TargetRole.V
			case "target.state":
				v = in.TargetState.V
			case "kind":
				v = in.Kind
			}
			if v == "" || !strings.Contains(","+list+",", ","+v+",") {
				return "", false
			}
		} else {
			return "", false
		}
	}
	return strings.Join(hit, ", "), true
}

// Eval は risk_eval(入力が欠けている・設定が壊れているときは評価不能 R-07 として ask)。
func Eval(in RiskInput) Risk {
	r := Risk{Value: "ask", Threshold: riskDefaultAskAt}
	var missing string
	for _, x := range []struct {
		name string
		o    Opt
	}{{"sender.role", in.SenderRole}, {"target.role", in.TargetRole}, {"target.state", in.TargetState}, {"sender.model", in.SenderModel}, {"body", in.Text}} {
		if !x.o.Set {
			missing += " " + x.name
		}
	}
	if missing != "" {
		r.Lines = append(r.Lines, "R-07 +ask 評価不能(fail-closed): 入力が取れません:"+missing)
		return r
	}
	c, err := loadRisk()
	if err != "" {
		r.Lines = append(r.Lines, "R-07 +ask 評価不能(fail-closed): "+err)
		return r
	}
	r.Warns = c.warns
	r.Threshold = c.askAt
	ask := false
	vals := map[string]string{"sender.role": in.SenderRole.V, "target.role": in.TargetRole.V, "target.state": in.TargetState.V, "sender.model": in.SenderModel.V}
	for _, a := range unknownAttrs {
		if vals[a] != "" {
			continue
		}
		switch v := c.unknown[a]; {
		case v == "ask":
			ask = true
			r.Lines = append(r.Lines, "unknown:"+a+" +ask "+a+" が不明(設定 ask)")
		case v == "allow":
			r.Lines = append(r.Lines, "unknown:"+a+" +0 "+a+" が不明(設定 allow)")
		default:
			p := v[5:]
			r.Score += atoi(p)
			r.Lines = append(r.Lines, "unknown:"+a+" +"+p+" "+a+" が不明(設定 "+v+")")
		}
	}
	for _, rl := range c.rules {
		h, ok := c.match(rl.cond, in)
		if !ok {
			continue
		}
		w := ""
		if h != "" {
			w = "(" + h + ")"
		}
		if rl.res == "ask" {
			ask = true
			r.Lines = append(r.Lines, rl.id+" +ask "+rl.why+w)
		} else {
			p := rl.res[5:]
			r.Score += atoi(p)
			r.Lines = append(r.Lines, rl.id+" +"+p+" "+rl.why+w)
		}
	}
	if !ask && r.Score < r.Threshold {
		r.Value = "allow"
	}
	return r
}

// Format は risk_format(tb approval の複数行)。
func (r Risk) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "risk: %s   # 第 1 段の参考値。最終判定には使われない\n", r.Value)
	fmt.Fprintf(&b, "risk-score: %d / threshold %d\n", r.Score, r.Threshold)
	for _, l := range r.Lines {
		fmt.Fprintf(&b, "risk-reason: %s\n", l)
	}
	for _, w := range r.Warns {
		if w != "" {
			fmt.Fprintf(&b, "risk-warn: %s\n", w)
		}
	}
	return b.String()
}

// Oneline は risk_oneline(outbox.reason に足す 1 行)。
func (r Risk) Oneline() string {
	out := fmt.Sprintf("risk: %s(参考。score %d/%d)", r.Value, r.Score, r.Threshold)
	sep := " ["
	for _, l := range r.Lines {
		out += sep + l
		sep = "; "
	}
	if sep == "; " {
		out += "]"
	}
	for _, w := range r.Warns {
		if w != "" {
			out += " {警告: " + w + "}"
		}
	}
	return strings.ReplaceAll(out, "\n", " ")
}

// Collect は risk_collect: sender は送信者名(空 = 人、? = 特定できない)。
func (j *Judge) Collect(sender, target, kind, text string) RiskInput {
	in := RiskInput{Text: set(text), Kind: kind}
	model := func(name string, col string) Opt {
		var v string
		err := j.DB.QueryRow(`SELECT COALESCE(`+col+`,'') FROM agents WHERE name=?`, name).Scan(&v)
		if err != nil && err != sql.ErrNoRows {
			return Opt{}
		}
		return set(v)
	}
	switch sender {
	case "":
		in.SenderRole, in.SenderModel = set("human"), set("human")
	case "?":
	default:
		in.SenderRole = set(j.Roles.First(sender))
		in.SenderModel = model(sender, "model")
	}
	in.TargetRole = set(j.Roles.First(target))
	in.TargetState = model(target, "state")
	return in
}
