package config

import (
	"strings"
	"unicode/utf8"
)

// Trim は util.sh の trim(前後の ASCII の空白を除く)。
func Trim(s string) string { return strings.Trim(s, " \t\n\v\f\r") }

// Lines は bash の `while IFS= read -r line || [[ -n $line ]]; done <<<"$body"` と同じ行の分け方(行番号は 1 から)。
func Lines(body string) []string { return strings.Split(body, "\n") }

// StripComment は ${line%%#*}(最初の # から後ろを落とす)。
func StripComment(line string) string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		return line[:i]
	}
	return line
}

// CharLen は core.sh の char_len(UTF-8 の文字数)。
func CharLen(s string) int { return utf8.RuneCountInString(s) }

// IsSpace は [[:space:]](ASCII)。
func IsSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r'
}

// FirstWord は ${line%%[[:space:]]*} と、その後ろを trim したもの。
func FirstWord(line string) (string, string) {
	i := strings.IndexFunc(line, IsSpace)
	if i < 0 {
		return line, ""
	}
	return line[:i], Trim(line[i:])
}

// Validator は tb config set / edit の前の検査(config_validate_<キー>)。誤りは標準エラーに出す本文(行ごとに改行つき)。空なら通った。
type Validator func(body string) string
