package util

import (
	"sync"
	"unicode"
)

// Glob は bash の [[ s == pattern ]](引用しない右辺)と同じ照合。bash の [[ ]] は extglob を有効にしたものとして照合するので、
// * ? [...]([!...] [^...] [:alpha:] も)に加えて ?(a|b) *(..) +(..) @(..) !(..) も読む。\ は次の 1 字をそのまま(括弧式の中でも)。
// / は特別扱いしない。閉じていない [ と (は文字として扱う。逆順の範囲([z-a])と知らないクラス名は何にも当たらない。
func Glob(pattern, s string) bool {
	globMu.Lock()
	p, ok := globCache[pattern]
	if !ok {
		p = parseGlob([]rune(pattern))
		globCache[pattern] = p
	}
	globMu.Unlock()
	return globMatch(p, []rune(s))
}

var (
	globMu    sync.Mutex
	globCache = map[string][]gnode{}
)

type gkind int

const (
	gLit   gkind = iota // 1 字
	gAny                // ?
	gStar               // *
	gClass              // [...]
	gExt                // ?( *( +( @( !(
)

type grange struct{ lo, hi rune }

type gnode struct {
	kind   gkind
	r      rune
	neg    bool
	ranges []grange
	names  []string // [:alpha:] など
	op     rune     // gExt の ? * + @ !
	alts   [][]gnode
}

func parseGlob(rs []rune) []gnode {
	var out []gnode
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if (c == '?' || c == '*' || c == '+' || c == '@' || c == '!') && i+1 < len(rs) && rs[i+1] == '(' {
			if alts, n, ok := parseExt(rs[i+2:]); ok {
				out = append(out, gnode{kind: gExt, op: c, alts: alts})
				i += 1 + n
				continue
			}
		}
		switch c {
		case '*':
			if len(out) == 0 || out[len(out)-1].kind != gStar {
				out = append(out, gnode{kind: gStar})
			}
		case '?':
			out = append(out, gnode{kind: gAny})
		case '\\':
			if i+1 < len(rs) {
				i++
				out = append(out, gnode{kind: gLit, r: rs[i]})
			} else {
				out = append(out, gnode{kind: gLit, r: '\\'})
			}
		case '[':
			if n, cls, ok := parseClass(rs[i:]); ok {
				out = append(out, cls)
				i += n - 1
			} else {
				out = append(out, gnode{kind: gLit, r: '['})
			}
		default:
			out = append(out, gnode{kind: gLit, r: c})
		}
	}
	return out
}

// parseExt は ( の次からの a|b) を読む。n は ) までの字数(( の次から数えて ) を含む)。閉じていなければ ok=false。
func parseExt(rs []rune) ([][]gnode, int, bool) {
	depth, start := 0, 0
	var alts [][]gnode
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			i++
		case '[':
			if n, _, ok := parseClass(rs[i:]); ok {
				i += n - 1
			}
		case '(':
			depth++
		case ')':
			if depth == 0 {
				alts = append(alts, parseGlob(rs[start:i]))
				return alts, i + 1, true
			}
			depth--
		case '|':
			if depth == 0 {
				alts = append(alts, parseGlob(rs[start:i]))
				start = i + 1
			}
		}
	}
	return nil, 0, false
}

// parseClass は [ から始まる括弧式を読む。n は読んだ字数。閉じていなければ ok=false。
func parseClass(rs []rune) (int, gnode, bool) {
	g := gnode{kind: gClass}
	i := 1
	if i < len(rs) && (rs[i] == '!' || rs[i] == '^') {
		g.neg = true
		i++
	}
	start := i
	for i < len(rs) {
		c := rs[i]
		if c == ']' && i > start {
			return i + 1, g, true
		}
		if c == '[' && i+1 < len(rs) && rs[i+1] == ':' { // [:alpha:] など
			if j := indexRunes(rs, i+2, ':', ']'); j >= 0 {
				g.names = append(g.names, string(rs[i+2:j]))
				i = j + 2
				continue
			}
		}
		if c == '\\' && i+1 < len(rs) {
			i++
			c = rs[i]
		}
		lo := c
		i++
		if i+1 < len(rs) && rs[i] == '-' && rs[i+1] != ']' {
			hi := rs[i+1]
			i += 2
			if hi == '\\' && i < len(rs) {
				hi = rs[i]
				i++
			}
			g.ranges = append(g.ranges, grange{lo, hi})
			continue
		}
		g.ranges = append(g.ranges, grange{lo, lo})
	}
	return 0, g, false
}

// indexRunes は rs[from:] で a b と続く最初の位置(rune の添字)。無ければ -1。
func indexRunes(rs []rune, from int, a, b rune) int {
	for k := from; k+1 < len(rs); k++ {
		if rs[k] == a && rs[k+1] == b {
			return k
		}
	}
	return -1
}

func classHas(g gnode, c rune) bool {
	hit := false
	for _, r := range g.ranges {
		if r.lo <= c && c <= r.hi {
			hit = true
			break
		}
	}
	for _, n := range g.names {
		if hit {
			break
		}
		hit = namedClass(n, c)
	}
	return hit != g.neg
}

func namedClass(name string, c rune) bool {
	switch name {
	case "alpha":
		return unicode.IsLetter(c)
	case "digit":
		return '0' <= c && c <= '9'
	case "alnum":
		return unicode.IsLetter(c) || unicode.IsDigit(c)
	case "upper":
		return unicode.IsUpper(c)
	case "lower":
		return unicode.IsLower(c)
	case "space":
		return unicode.IsSpace(c)
	case "blank":
		return c == ' ' || c == '\t'
	case "punct":
		return c < 0x80 && unicode.IsPunct(c) || c < 0x80 && unicode.IsSymbol(c)
	case "xdigit":
		return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
	case "cntrl":
		return unicode.IsControl(c)
	case "print":
		return unicode.IsPrint(c)
	case "graph":
		return unicode.IsGraphic(c) && !unicode.IsSpace(c)
	case "word":
		return c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c)
	}
	return false
}

// globMatch は p が s の全体に当たるか。
func globMatch(p []gnode, s []rune) bool {
	for len(p) > 0 {
		n := p[0]
		switch n.kind {
		case gStar:
			if len(p) == 1 {
				return true
			}
			for k := 0; k <= len(s); k++ {
				if globMatch(p[1:], s[k:]) {
					return true
				}
			}
			return false
		case gExt:
			return extMatch(n, p[1:], s)
		}
		if len(s) == 0 {
			return false
		}
		switch n.kind {
		case gLit:
			if s[0] != n.r {
				return false
			}
		case gClass:
			if !classHas(n, s[0]) {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return len(s) == 0
}

func anyAlt(alts [][]gnode, s []rune) bool {
	for _, a := range alts {
		if globMatch(a, s) {
			return true
		}
	}
	return false
}

func extMatch(n gnode, rest []gnode, s []rune) bool {
	switch n.op {
	case '@':
		for k := 0; k <= len(s); k++ {
			if anyAlt(n.alts, s[:k]) && globMatch(rest, s[k:]) {
				return true
			}
		}
	case '?':
		if globMatch(rest, s) {
			return true
		}
		for k := 0; k <= len(s); k++ {
			if anyAlt(n.alts, s[:k]) && globMatch(rest, s[k:]) {
				return true
			}
		}
	case '!':
		for k := 0; k <= len(s); k++ {
			if !anyAlt(n.alts, s[:k]) && globMatch(rest, s[k:]) {
				return true
			}
		}
	case '*', '+':
		if n.op == '*' && globMatch(rest, s) {
			return true
		}
		for k := 1; k <= len(s); k++ { // 1 回以上(空でない区切りで進む)
			if anyAlt(n.alts, s[:k]) && extMatch(gnode{kind: gExt, op: '*', alts: n.alts}, rest, s[k:]) {
				return true
			}
		}
		if n.op == '+' && anyAlt(n.alts, nil) && globMatch(rest, s) { // +(x) の x が空に当たるとき
			return true
		}
	}
	return false
}
