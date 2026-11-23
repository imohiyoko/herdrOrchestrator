package webui

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// markdown.go は markdown.py(最小限の Markdown → HTML)。外部由来の本文なので、必ず先に E でエスケープしてから記法を当てる。
// Python の正規表現の先読み・後読み・後方参照は Go の regexp に無いので、その部分は手で書いた照合にしている。

var (
	reMdCode    = regexp.MustCompile("`([^`\n]+)`")
	reMdLink    = regexp.MustCompile(`!?\[([^\]\n]*)\]\(([^)` + pySpaceCls + `]+)(?:` + pySpace + `+&quot;[^\n]*?&quot;)?\)`)
	reMdAutoURL = regexp.MustCompile(`https?://[^` + pySpaceCls + `<>&"']+`)
	reMdSafeURL = regexp.MustCompile(`(?i)^https?://[^` + pySpaceCls + "<>\"'`" + `]+$`)
	reMdKeep    = regexp.MustCompile("\x00([0-9]+)\x00")

	reMdH     = regexp.MustCompile(`^ {0,3}(#{1,6})` + pySpace + `+(.*?)` + pySpace + `*#*` + pySpace + `*$`)
	reMdUL    = regexp.MustCompile(`^(` + pySpace + `*)[-*+]` + pySpace + `+(.*)$`)
	reMdOL    = regexp.MustCompile(`^(` + pySpace + `*)\p{Nd}{1,9}[.)]` + pySpace + `+(.*)$`)
	reMdTask  = regexp.MustCompile(`^\[([ xX])\]` + pySpace + `+(.*)$`)
	reMdFence = regexp.MustCompile("^ {0,3}(```+|~~~+)" + pySpace + `*([\p{L}\p{N}_+-]*)$`)
	reMdTSep  = regexp.MustCompile(`^` + pySpace + `*\|?` + pySpace + `*:?-{1,}:?` + pySpace + `*(\|` + pySpace + `*:?-{1,}:?` + pySpace + `*)*\|?` + pySpace + `*$`)
	reMdQuote = regexp.MustCompile(`^` + pySpace + `*>` + pySpace + `?`)
)

func isPySpaceR(r rune) bool { return pyIsSpace(r) }

// mdInline は markdown.inline: 1 行(または段落)分。返り値は HTML。
func mdInline(raw string) string {
	s := E(raw)
	var codes []string
	keep := func(h string) string {
		codes = append(codes, h)
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	}
	s = reMdCode.ReplaceAllStringFunc(s, func(m string) string { return keep("<code>" + m[1:len(m)-1] + "</code>") })
	s = replaceSubmatch(reMdLink, s, func(m []string) string {
		text, u := m[1], strings.ReplaceAll(m[2], "&amp;", "&")
		if strings.HasPrefix(m[0], "!") {
			if text == "" {
				text = "無題"
			}
			return keep("[画像: " + text + "]")
		}
		if reMdSafeURL.MatchString(u) {
			if text == "" {
				text = E(u)
			}
			return keep(`<a href="` + E(u) + `" rel="noopener noreferrer" target=_blank>` + text + `</a>`)
		}
		return keep(text + ` <span class=note>(` + E(u) + `)</span>`)
	})
	s = mdAutolink(s, keep)
	s = mdPaired(s, "**", "__", "strong")
	s = mdItalic(s)
	s = mdPaired(s, "~~", "", "del")
	return reMdKeep.ReplaceAllStringFunc(s, func(m string) string {
		i, err := strconv.Atoi(m[1 : len(m)-1])
		if err != nil || i >= len(codes) {
			return m
		}
		return codes[i]
	})
}

func replaceSubmatch(re *regexp.Regexp, s string, f func([]string) string) string {
	var b strings.Builder
	last := 0
	for _, ix := range re.FindAllStringSubmatchIndex(s, -1) {
		m := make([]string, len(ix)/2)
		for k := range m {
			if ix[2*k] >= 0 {
				m[k] = s[ix[2*k]:ix[2*k+1]]
			}
		}
		b.WriteString(s[last:ix[0]])
		b.WriteString(f(m))
		last = ix[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// mdAutolink は (?<!["'=/\w])(https?://[^\s<>&"']+)。
func mdAutolink(s string, keep func(string) string) string {
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		ix := reMdAutoURL.FindStringIndex(s[pos:])
		if ix == nil {
			break
		}
		st, en := pos+ix[0], pos+ix[1]
		if st > 0 {
			if p := lastRune(s[:st]); strings.ContainsRune(`"'=/`, p) || isPyWord(p) {
				pos = st + 1
				continue
			}
		}
		u := s[st:en]
		b.WriteString(s[last:st])
		b.WriteString(keep(`<a href="` + u + `" rel="noopener noreferrer" target=_blank>` + u + `</a>`))
		last, pos = en, en
	}
	b.WriteString(s[last:])
	return b.String()
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// mdPaired は \*\*(?=\S)(.+?)(?<=\S)\*\* | __(?=\S)(.+?)(?<=\S)__(取り消し線は ~~ だけ)。
func mdPaired(s, d1, d2, tag string) string {
	rs := []rune(s)
	var b strings.Builder
	i := 0
	try := func(d string) (int, int) { // (中身の終わり, 一致の終わり)。当たらなければ -1
		dr := []rune(d)
		if d == "" || !hasRunes(rs, i, dr) {
			return -1, -1
		}
		st := i + len(dr)
		if st >= len(rs) || isPySpaceR(rs[st]) {
			return -1, -1
		}
		for k := st + 1; k <= len(rs); k++ {
			if rs[k-1] == '\n' {
				return -1, -1
			}
			if !isPySpaceR(rs[k-1]) && hasRunes(rs, k, dr) {
				return k, k + len(dr)
			}
		}
		return -1, -1
	}
	for i < len(rs) {
		en, me := try(d1)
		dl := len([]rune(d1))
		if en < 0 {
			en, me = try(d2)
			dl = len([]rune(d2))
		}
		if en < 0 {
			b.WriteRune(rs[i])
			i++
			continue
		}
		b.WriteString("<" + tag + ">" + string(rs[i+dl:en]) + "</" + tag + ">")
		i = me
	}
	return b.String()
}

func hasRunes(rs []rune, i int, d []rune) bool {
	if i+len(d) > len(rs) {
		return false
	}
	for k, r := range d {
		if rs[i+k] != r {
			return false
		}
	}
	return true
}

// mdItalic は (?<![\w*])\*(?=\S)([^*\n]+?)(?<=\S)\*(?![\w*])。
func mdItalic(s string) string {
	rs := []rune(s)
	var b strings.Builder
	i := 0
	for i < len(rs) {
		if rs[i] == '*' && (i == 0 || (rs[i-1] != '*' && !isPyWord(rs[i-1]))) {
			st := i + 1
			if st < len(rs) && !isPySpaceR(rs[st]) && rs[st] != '*' {
				k := st + 1
				for k < len(rs) && rs[k] != '*' && rs[k] != '\n' {
					k++
				}
				if k < len(rs) && rs[k] == '*' && !isPySpaceR(rs[k-1]) && (k+1 >= len(rs) || (rs[k+1] != '*' && !isPyWord(rs[k+1]))) {
					b.WriteString("<em>" + string(rs[st:k]) + "</em>")
					i = k + 1
					continue
				}
			}
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

// mdHR は  {0,3}([-*_])(\s*\1){2,}\s* の fullmatch。
func mdHR(line string) bool {
	rs := []rune(line)
	i := 0
	for i < len(rs) && i < 4 && rs[i] == ' ' {
		i++
	}
	if i > 3 || i >= len(rs) || !strings.ContainsRune("-*_", rs[i]) {
		return false
	}
	c, n := rs[i], 0
	for _, r := range rs[i+1:] {
		switch {
		case r == c:
			n++
		case !isPySpaceR(r):
			return false
		}
	}
	return n >= 2
}

func mdCells(line string) []string {
	s := pyStrip(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	for i, c := range parts {
		parts[i] = pyStrip(c)
	}
	return parts
}

// expandTabs4Len は len(s.expandtabs(4))(s は空白だけ)。
func expandTabs4Len(s string) int {
	col := 0
	for _, r := range s {
		switch r {
		case '\t':
			col += 4 - col%4
		case '\n', '\r':
			col = 0
		default:
			col++
		}
	}
	return col
}

// renderMD は markdown.render。
func renderMD(text string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	var out, para []string
	n := len(lines)
	flush := func() {
		if len(para) > 0 {
			ps := make([]string, len(para))
			for k, x := range para {
				ps[k] = mdInline(x)
			}
			out = append(out, "<p>"+strings.Join(ps, "<br>")+"</p>")
			para = nil
		}
	}
	for i := 0; i < n; {
		line := lines[i]
		if m := reMdFence.FindStringSubmatch(line); m != nil {
			flush()
			fence := m[1]
			var body []string
			i++
			for i < n && !strings.HasPrefix(pyStrip(lines[i]), fence) {
				body = append(body, lines[i])
				i++
			}
			i++
			if !strings.ContainsRune(m[2], 'İ') && strings.ToLower(m[2]) == "mermaid" {
				out = append(out, "<pre class=mermaid>"+E(strings.Join(body, "\n"))+"</pre>")
			} else {
				out = append(out, "<pre><code>"+E(strings.Join(body, "\n"))+"</code></pre>")
			}
			continue
		}
		if pyStrip(line) == "" {
			flush()
			i++
			continue
		}
		if mdHR(line) {
			flush()
			out = append(out, "<hr>")
			i++
			continue
		}
		if m := reMdH.FindStringSubmatch(line); m != nil {
			flush()
			lv := min(len(m[1])+1, 6)
			out = append(out, "<h"+strconv.Itoa(lv)+" class=md>"+mdInline(m[2])+"</h"+strconv.Itoa(lv)+">")
			i++
			continue
		}
		if strings.Contains(line, "|") && i+1 < n && reMdTSep.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-") {
			flush()
			hd := mdCells(line)
			var rows [][]string
			i += 2
			for i < n && strings.Contains(lines[i], "|") && pyStrip(lines[i]) != "" {
				rows = append(rows, mdCells(lines[i]))
				i++
			}
			var b strings.Builder
			b.WriteString("<table><tr>")
			for _, c := range hd {
				b.WriteString("<th>" + mdInline(c) + "</th>")
			}
			b.WriteString("</tr>")
			for _, r := range rows {
				b.WriteString("<tr>")
				for _, c := range r {
					b.WriteString("<td>" + mdInline(c) + "</td>")
				}
				b.WriteString("</tr>")
			}
			b.WriteString("</table>")
			out = append(out, b.String())
			continue
		}
		if strings.HasPrefix(strings.TrimLeftFunc(line, pyIsSpace), ">") {
			flush()
			var quote []string
			for i < n && strings.HasPrefix(strings.TrimLeftFunc(lines[i], pyIsSpace), ">") {
				quote = append(quote, reMdQuote.ReplaceAllString(lines[i], ""))
				i++
			}
			out = append(out, "<blockquote>"+renderMD(strings.Join(quote, "\n"))+"</blockquote>")
			continue
		}
		if reMdUL.MatchString(line) || reMdOL.MatchString(line) {
			flush()
			ordered := reMdOL.MatchString(line) && !reMdUL.MatchString(line)
			type item struct {
				depth int
				body  string
			}
			var items []item
			for i < n {
				mu := reMdUL.FindStringSubmatch(lines[i])
				if mu == nil {
					mu = reMdOL.FindStringSubmatch(lines[i])
				}
				if mu != nil {
					items = append(items, item{expandTabs4Len(mu[1]) / 2, mu[2]})
				} else if pyStrip(lines[i]) != "" && len(items) > 0 && (lines[i][0] == ' ' || lines[i][0] == '\t') {
					items[len(items)-1].body += " " + pyStrip(lines[i]) // 続きの行
				} else {
					break
				}
				i++
			}
			tag := "ul"
			if ordered {
				tag = "ol"
			}
			var b strings.Builder
			b.WriteString("<" + tag + ">")
			for _, it := range items {
				content := ""
				if mt := reMdTask.FindStringSubmatch(it.body); mt != nil {
					mark := "☐ "
					if mt[1] != " " {
						mark = "☑ "
					}
					content = mark + mdInline(mt[2])
				} else {
					content = mdInline(it.body)
				}
				b.WriteString(`<li style="margin-left:` + pyFloat(float64(min(it.depth, 6))*1.5) + `em">` + content + `</li>`)
			}
			b.WriteString("</" + tag + ">")
			out = append(out, b.String())
			continue
		}
		para = append(para, pyStrip(line))
		i++
	}
	flush()
	return strings.Join(out, "")
}
