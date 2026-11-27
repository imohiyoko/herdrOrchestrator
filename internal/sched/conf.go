// Package sched は scheduler の中身(scheduler/bin/*.sh はここに置き換わるだけ)。ジョブ定義(conf)の読み込みと検証は、元の bash 版(lib.sh の load_job)と同じ。
// ただし conf は bash として実行しない: 決まった書き方(KEY=値。値は '…' / "…" / 引用なし)だけを読み、展開($ や `)は拒む。
package sched

import (
	"fmt"
	"regexp"
	"strings"
)

// ConfKeys は conf に書けるキー(元の bash 版の load_job が読み込みの前に unset していた変数と同じ)。
var ConfKeys = map[string]bool{
	"TARGET": true, "PROMPT": true, "PROMPT_FILE": true, "PROMPT_RULES": true, "PROMPT_CMD": true, "PROMPT_CMD_TIMEOUT": true,
	"PROMPT_VARS": true, "PROMPT_VAR_TIMEOUT": true, "COMMAND": true, "COMMAND_TIMEOUT": true, "GATE": true, "GATE_TIMEOUT": true,
	"WINDOW": true, "START": true, "END": true, "INTERVAL_MIN": true, "INTERVAL_SEC": true, "DAYS": true, "SKIP_IF": true,
	"TZ": true, "ENABLED": true, "WATCHER": true, "OWNER": true, "AT": true,
}

var reConfName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ConfError は conf の書き方の誤り(何行目か)。
type ConfError struct {
	Line int
	Msg  string
}

func (e *ConfError) Error() string { return fmt.Sprintf("%d 行目: %s", e.Line, e.Msg) }

// ParseConf は conf の本文を読み、キーと値を返す。書けるのは bash の代入と同じ見た目の KEY=値 だけ:
//   - 引用なしの値: 空白・改行・# の前まで。\ は次の 1 文字をそのまま(\ と改行は行の続き)
//   - '…': 中身をそのまま(改行も含む)
//   - "…": \$ \` \" \\ と \ と改行だけを解く。ほかの \ はそのまま
//   - 1 行に KEY=値 をいくつか並べてよい。# から行末は注釈
//
// 展開($VAR・$(…)・`…`・~)と、コマンド・; | & ( ) < > は拒む(bash で実行しないので、書いても意味が変わるため)。
func ParseConf(body string) (map[string]string, error) {
	out := map[string]string{}
	rs := []rune(body)
	line := 1
	i := 0
	fail := func(msg string) error { return &ConfError{line, msg} }
	for i < len(rs) {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '\n':
			line++
			i++
			continue
		case c == '#':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			continue
		}
		// KEY=
		start := i
		for i < len(rs) && rs[i] != '=' && !isSep(rs[i]) {
			i++
		}
		name := string(rs[start:i])
		if i >= len(rs) || rs[i] != '=' || !reConfName.MatchString(name) {
			return nil, fail("KEY=値 の形ではない(コマンドは書けない): " + firstLine(string(rs[start:])))
		}
		if !ConfKeys[name] {
			return nil, fail("使えないキー: " + name)
		}
		i++ // '='
		var v strings.Builder
		startLine := line
		for i < len(rs) && !isSep(rs[i]) {
			c := rs[i]
			switch c {
			case '\'':
				j := i + 1
				for j < len(rs) && rs[j] != '\'' {
					if rs[j] == '\n' {
						line++
					}
					j++
				}
				if j >= len(rs) {
					line = startLine
					return nil, fail(name + ": ' が閉じていない")
				}
				v.WriteString(string(rs[i+1 : j]))
				i = j + 1
			case '"':
				j := i + 1
				for ; j < len(rs) && rs[j] != '"'; j++ {
					switch rs[j] {
					case '\\':
						if j+1 < len(rs) {
							switch rs[j+1] {
							case '$', '`', '"', '\\':
								v.WriteRune(rs[j+1])
								j++
								continue
							case '\n':
								line++
								j++
								continue
							}
						}
						v.WriteRune('\\')
					case '$', '`':
						return nil, fail(name + ": 展開($ や `)は使えない(文字として書くなら '…' で囲む)")
					case '\n':
						line++
						v.WriteRune('\n')
					default:
						v.WriteRune(rs[j])
					}
				}
				if j >= len(rs) {
					line = startLine
					return nil, fail(name + `: " が閉じていない`)
				}
				i = j + 1
			case '\\':
				if i+1 < len(rs) {
					if rs[i+1] == '\n' {
						line++
					} else {
						v.WriteRune(rs[i+1])
					}
					i += 2
				} else {
					i++
				}
			case '$', '`', '~':
				return nil, fail(name + ": 展開($ や ` や ~)は使えない(文字として書くなら '…' で囲む)")
			case ';', '|', '&', '(', ')', '<', '>':
				return nil, fail(name + ": " + string(c) + " は使えない(値に含めるなら '…' で囲む)")
			default:
				v.WriteRune(c)
				i++
			}
		}
		out[name] = v.String()
	}
	return out, nil
}

func isSep(c rune) bool { return c == ' ' || c == '\t' || c == '\n' }

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60]) + "…"
	}
	return s
}
