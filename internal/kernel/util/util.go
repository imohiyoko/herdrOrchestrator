// Package util は機能を知らない小さな道具(ADR 0004 の土台。taskboard/lib/util.sh)。
package util

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ws は jq(Oniguruma)の \s と同じ(Unicode の White_Space)
const ws = `[\t\n\v\f\r\x{85}\p{Z}]`

var (
	reFrom   = regexp.MustCompile(`(?i)\[` + ws + `*from` + ws + `*[:：]`)
	reSpaces = regexp.MustCompile(` +`)
)

// Oneline は util.sh の oneline: 1 行にし、制御文字を落とし、ラベルの偽装([from: ...)を崩し、前後の空白を除いて先頭 n 字にする。
func Oneline(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			return ' '
		case r < 0x20:
			return -1
		}
		return r
	}, s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = reFrom.ReplaceAllString(s, "(from:")
	s = strings.TrimFunc(s, unicode.IsSpace)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// JQString は jq -c と同じ書き方の JSON 文字列(< > & や非 ASCII はそのまま、制御文字と DEL だけ \u で書く)。
func JQString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case utf8.RuneError:
			b.WriteString("\uFFFD")
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
