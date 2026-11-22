package util

import (
	"regexp"
	"strings"
	"sync"
)

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// Glob は bash の [[ s == pattern ]](引用しない右辺)と同じ照合: * ? [...]([!...] [^...] も)、\ は次の 1 字をそのまま。
// / は特別扱いしない。閉じていない [ は文字の [ として扱う。
func Glob(pattern, s string) bool {
	globMu.Lock()
	re, ok := globCache[pattern]
	if !ok {
		re = regexp.MustCompile("^(?s:" + globRE(pattern) + ")$")
		globCache[pattern] = re
	}
	globMu.Unlock()
	return re.MatchString(s)
}

func globRE(p string) string {
	var b strings.Builder
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '\\':
			if i+1 < len(rs) {
				i++
				b.WriteString(regexp.QuoteMeta(string(rs[i])))
			} else {
				b.WriteString(`\\`)
			}
		case '[':
			if cls, n, ok := globClass(rs[i:]); ok {
				b.WriteString(cls)
				i += n - 1
			} else {
				b.WriteString(`\[`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// globClass は [ から始まる括弧式を正規表現の文字クラスにする。n は読んだ字数。閉じていなければ ok=false。
func globClass(rs []rune) (string, int, bool) {
	i := 1
	neg := false
	if i < len(rs) && (rs[i] == '!' || rs[i] == '^') {
		neg = true
		i++
	}
	start := i
	var b strings.Builder
	for ; i < len(rs); i++ {
		c := rs[i]
		if c == ']' && i > start {
			cls := "["
			if neg {
				cls += "^"
			}
			return cls + b.String() + "]", i + 1, true
		}
		if c == '[' && i+1 < len(rs) && rs[i+1] == ':' { // [:alpha:] など
			if j := strings.Index(string(rs[i:]), ":]"); j > 0 {
				name := string(rs[i : i+j+2])
				b.WriteString(name)
				i += len([]rune(name)) - 1
				continue
			}
		}
		switch c {
		case '\\', ']', '[', '^':
			b.WriteString(`\` + string(c))
		case '-':
			if i == start || i+1 < len(rs) && rs[i+1] == ']' {
				b.WriteString(`\-`)
			} else {
				b.WriteRune('-')
			}
		default:
			b.WriteRune(c)
		}
	}
	return "", 0, false
}
