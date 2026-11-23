package webui

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// fnmatchcase は Python の fnmatch.fnmatchcase(* は / も含めて何にでも当たる。[...] と [!...] も)。

var (
	reNever = regexp.MustCompile(`[^\x00-\x{10FFFF}]`)
	fnMu    sync.Mutex
	fnCache = map[string]*regexp.Regexp{}
)

func fnmatchcase(name, pat string) bool {
	fnMu.Lock()
	re, ok := fnCache[pat]
	fnMu.Unlock()
	if !ok {
		var err error
		if re, err = regexp.Compile(fnTranslate(pat)); err != nil {
			re = reNever // 逆向きの範囲([z-a])など。どれにも当てない
		}
		fnMu.Lock()
		if len(fnCache) > 1000 {
			fnCache = map[string]*regexp.Regexp{}
		}
		fnCache[pat] = re
		fnMu.Unlock()
	}
	return re.MatchString(name)
}

// fnTranslate は fnmatch.translate と同じ考えで、パターンを正規表現にする。
func fnTranslate(pat string) string {
	p := []rune(pat)
	n := len(p)
	var b strings.Builder
	b.WriteString(`(?s)^(?:`)
	for i := 0; i < n; {
		c := p[i]
		i++
		switch c {
		case '*':
			for i < n && p[i] == '*' {
				i++
			}
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		case '[':
			j := i
			if j < n && p[j] == '!' {
				j++
			}
			if j < n && p[j] == ']' {
				j++
			}
			for j < n && p[j] != ']' {
				j++
			}
			if j >= n {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(fnClass(p, i, j))
			i = j + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`)$`)
	return b.String()
}

// fnClass は fnmatch.translate(Python 3.13)の [...]。pat[i:j] が括弧の中身。
// Python と同じく、逆向きの範囲([z-a])はその範囲だけを除き、範囲でない - と \ を逃がした中身を作ってから、Go の文字クラスに組み直す。
func fnClass(pat []rune, i, j int) string {
	esc := func(c []rune) string {
		return strings.NewReplacer(`\`, `\\`, `-`, `\-`).Replace(string(c))
	}
	var stuff string
	if !containsRune(pat[i:j], '-') {
		stuff = strings.ReplaceAll(string(pat[i:j]), `\`, `\\`)
	} else {
		var chunks [][]rune
		k := i + 1
		if pat[i] == '!' {
			k = i + 2
		}
		for {
			f := -1
			for x := k; x < j; x++ {
				if pat[x] == '-' {
					f = x
					break
				}
			}
			if f < 0 {
				break
			}
			chunks = append(chunks, pat[i:f])
			i = f + 1
			k = f + 3
		}
		if i < j {
			chunks = append(chunks, pat[i:j])
		} else {
			chunks[len(chunks)-1] = append(append([]rune(nil), chunks[len(chunks)-1]...), '-')
		}
		for k := len(chunks) - 1; k > 0; k-- { // 空の範囲(逆向き)を除く
			a, b := chunks[k-1], chunks[k]
			if a[len(a)-1] > b[0] {
				chunks[k-1] = append(append([]rune(nil), a[:len(a)-1]...), b[1:]...)
				chunks = append(chunks[:k], chunks[k+1:]...)
			}
		}
		parts := make([]string, len(chunks))
		for x, c := range chunks {
			parts[x] = esc(c)
		}
		stuff = strings.Join(parts, "-")
	}
	stuff = regexp.MustCompile(`([&~|])`).ReplaceAllString(stuff, `\$1`)
	switch {
	case stuff == "":
		return `[^\x00-\x{10FFFF}]` // 空: どれにも当たらない
	case stuff == "!":
		return `.` // 中身が空の否定: 何にでも当たる
	}
	rs := []rune(stuff)
	neg := false
	if rs[0] == '!' {
		neg, rs = true, rs[1:]
	}
	// Python の正規表現の文字クラスとして読む(\x は文字 x、a-b は範囲、端の - は文字)
	read := func(p int) (rune, int) {
		if rs[p] == '\\' && p+1 < len(rs) {
			return rs[p+1], p + 2
		}
		return rs[p], p + 1
	}
	var b strings.Builder
	b.WriteByte('[')
	if neg {
		b.WriteByte('^')
	}
	for p := 0; p < len(rs); {
		lo, np := read(p)
		if np+1 < len(rs) && rs[np] == '-' {
			hi, np2 := read(np + 1)
			if lo > hi {
				return `[^\x00-\x{10FFFF}]` // Python では re.error になる並び(ここには来ない)
			}
			fmt.Fprintf(&b, `\x{%x}-\x{%x}`, lo, hi)
			p = np2
			continue
		}
		fmt.Fprintf(&b, `\x{%x}`, lo)
		p = np
	}
	b.WriteByte(']')
	return b.String()
}

func containsRune(rs []rune, r rune) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}
