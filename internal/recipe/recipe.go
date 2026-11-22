package recipe

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// レシピ(ADR 0005 の段 2。recipes.sh の recipe_parse)。部品の並びに名前を付けて DB に登録する。

const (
	MaxSteps = 16
	MaxArgs  = 8
	MaxBytes = 8000
)

var (
	// NameRE は RECIPE_NAME_RE。
	NameRE    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	reArgDecl = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
)

// Recipe は検査を通った本文(RCP_*)。
type Recipe struct {
	Desc, Decl, Risk string
	ArgN             []string          // 宣言の順。省略できるものは名前の後ろに ?
	ArgT             map[string]string // 名前 → 型
	Steps            []string          // step の行の残り(部品と引数)
}

// isSpace は [[:space:]](C ロケール)。
func isSpace(b byte) bool { return b == ' ' || (b >= '\t' && b <= '\r') }

// fields は read -ra(IFS の既定: 空白・タブ・改行)。
func fields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Parse は recipe_parse: 通れば Recipe、通らなければ理由(1 行 1 件)。
func Parse(body string) (Recipe, string) {
	r := Recipe{ArgT: map[string]string{}}
	if utf8.RuneCountInString(body) > MaxBytes {
		return r, fmt.Sprintf("本文が長すぎます(%d 字まで)", MaxBytes)
	}
	var err strings.Builder
	addErr := func(n int, f string, a ...any) { fmt.Fprintf(&err, "%d 行目: "+f+"\n", append([]any{n}, a...)...) }
	var parts []string
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		n := i + 1
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = line[:j]
		}
		line = trim(line)
		if line == "" {
			continue
		}
		kw, rest := line, ""
		for k := 0; k < len(line); k++ {
			if isSpace(line[k]) {
				kw, rest = line[:k], trim(line[k+1:])
				break
			}
		}
		switch kw {
		case "desc":
			if r.Desc != "" {
				addErr(n, "desc が 2 回あります")
				continue
			}
			if rest == "" || charLen(rest) > 200 {
				addErr(n, "desc は 1〜200 字")
				continue
			}
			r.Desc = rest
		case "risk":
			if r.Decl != "" {
				addErr(n, "risk が 2 回あります")
				continue
			}
			if rest == "" || !strings.Contains(levels, " "+rest+" ") {
				addErr(n, "risk は %s のどれか: %s", strings.Join(RiskLevels, " "), rest)
				continue
			}
			r.Decl = rest
		case "arg":
			toks := fields(rest)
			if len(toks) != 2 {
				addErr(n, "arg <名前>[?] <型>")
				continue
			}
			nm, t, opt := toks[0], toks[1], ""
			if strings.HasSuffix(nm, "?") {
				opt, nm = "?", nm[:len(nm)-1]
			}
			if !reArgDecl.MatchString(nm) || nm == "requester" || nm == "target" {
				addErr(n, "引数の名前は英小文字で始まる 16 字まで(requester / target は予約): %s", nm)
				continue
			}
			if _, dup := r.ArgT[nm]; dup {
				addErr(n, "引数 %s が 2 回あります", nm)
				continue
			}
			if !TypeOK(t) {
				addErr(n, "引数 %s の型が不正です: %s", nm, t)
				continue
			}
			if t == "agent" {
				addErr(n, "agent 型の引数は宣言できません(対象は --target で渡し、step では $target と書きます): %s", nm)
				continue
			}
			r.ArgT[nm] = t
			r.ArgN = append(r.ArgN, nm+opt)
		case "step":
			toks := fields(rest)
			if len(toks) < 1 {
				addErr(n, "step <部品> [<名前>=<値> ...]")
				continue
			}
			p, ok := Lookup(toks[0])
			if !ok {
				addErr(n, "部品の一覧にありません: %s(tb parts)", toks[0])
				continue
			}
			good := true
			seen := map[string]bool{}
			specs := fields(p.Args)
			for _, kv := range toks[1:] {
				k, v, isKV := strings.Cut(kv, "=")
				if !isKV {
					addErr(n, "引数は <名前>=<値>: %s", kv)
					good = false
					continue
				}
				if seen[k] {
					addErr(n, "引数 %s が 2 回あります", k)
					good = false
					continue
				}
				seen[k] = true
				pt, popt := "", false
				for _, spec := range specs {
					sn, st, _ := strings.Cut(spec, ":")
					if sn == k {
						pt = st
					}
					if sn == k+"?" {
						pt, popt = st, true
					}
				}
				if pt == "" {
					can := p.Args
					if can == "" {
						can = "なし"
					}
					addErr(n, "部品 %s に引数 %s はありません(使えるのは: %s)", p.Name, k, can)
					good = false
					continue
				}
				if pt == "agent" && v != "$requester" && v != "$target" {
					addErr(n, "%s の %s(agent 型)に書けるのは $requester か $target だけです: %s", p.Name, k, v)
					good = false
					continue
				}
				if strings.HasPrefix(v, "$") {
					ref := v[1:]
					switch ref {
					case "requester", "target":
						if pt != "agent" {
							addErr(n, "%s は agent 型の引数にだけ使えます(%s の %s は %s)", v, p.Name, k, pt)
							good = false
						}
					default:
						t := r.ArgT[ref]
						switch {
						case t == "":
							addErr(n, "%s は宣言されていません(arg の行は step より前に書きます)", v)
							good = false
						case typeHead(t) != typeHead(pt):
							addErr(n, "%s の型 %s は %s の %s の型 %s と合いません", v, t, p.Name, k, pt)
							good = false
						case !popt && has(r.ArgN, ref+"?"):
							addErr(n, "省略できる引数 %s を、省略できない %s の %s に渡せません", v, p.Name, k)
							good = false
						}
					}
				} else {
					if strings.Contains(v, "$") {
						addErr(n, "値の中に $ は書けません($<名前> は 1 語だけ): %s", v)
						good = false
						continue
					}
					if why := ValueOK(pt, v); why != "" {
						addErr(n, "%s の引数 %s: %s", p.Name, k, why)
						good = false
					}
				}
			}
			if good {
				joined := " " + strings.Join(toks[1:], " ") + " "
				for _, spec := range specs {
					sn, _, _ := strings.Cut(spec, ":")
					if strings.HasSuffix(sn, "?") {
						continue
					}
					if !strings.Contains(joined, " "+sn+"=") {
						addErr(n, "部品 %s の引数 %s がありません", p.Name, sn)
						good = false
					}
				}
			}
			if !good {
				continue
			}
			r.Steps = append(r.Steps, rest)
			parts = append(parts, p.Name)
		default:
			addErr(n, "書けるのは desc / risk / arg / step だけです: %s", kw)
		}
	}
	if len(r.Steps) == 0 && len(parts) == 0 && err.Len() == 0 {
		err.WriteString("step が 1 つもありません\n")
	}
	if len(r.Steps) > MaxSteps {
		fmt.Fprintf(&err, "step は %d 個まで\n", MaxSteps)
	}
	if len(r.ArgN) > MaxArgs {
		fmt.Fprintf(&err, "arg は %d 個まで\n", MaxArgs)
	}
	if err.Len() > 0 {
		return r, strings.TrimSuffix(err.String(), "\n")
	}
	r.Risk = RiskMax(parts)
	if r.Decl != "" && RiskRank(r.Decl) > RiskRank(r.Risk) {
		r.Risk = r.Decl
	}
	return r, ""
}

// typeHead は ${t%%:*}。
func typeHead(t string) string { h, _, _ := strings.Cut(t, ":"); return h }

// StepPart は step の行の部品名(${s%%[[:space:]]*})。
func StepPart(s string) string {
	for k := 0; k < len(s); k++ {
		if isSpace(s[k]) {
			return s[:k]
		}
	}
	return s
}
