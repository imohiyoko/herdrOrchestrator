package webui

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
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
			stuff := p[i:j]
			i = j + 1
			switch {
			case len(stuff) == 0:
				b.WriteString(`[^\x00-\x{10FFFF}]`) // 空の [] はどれにも当たらない
			case len(stuff) == 1 && stuff[0] == '!':
				b.WriteString(`.`)
			default:
				b.WriteByte('[')
				k := 0
				if stuff[0] == '!' {
					b.WriteByte('^')
					k = 1
				}
				for first := k; k < len(stuff); k++ {
					r := stuff[k]
					switch {
					case r == '-' && k > first && k < len(stuff)-1:
						b.WriteByte('-') // 範囲
					case r < 0x80 && !unicode.IsLetter(r) && !unicode.IsDigit(r):
						b.WriteString(`\` + string(r)) // 記号はすべて逃がす(RE2 は文字クラスの中の \<記号> を文字として読む)
					default:
						b.WriteRune(r)
					}
				}
				b.WriteByte(']')
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`)$`)
	return b.String()
}
