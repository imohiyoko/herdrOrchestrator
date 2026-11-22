package tickets

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

func builtinCat(k string) string {
	for _, b := range builtin {
		if b[0] == k {
			return b[1]
		}
	}
	return ""
}

// ValidateStatuses は statuses_parse の検査(tb config set / edit statuses)。
func ValidateStatuses(body string) string {
	var bad strings.Builder
	seen := map[string]bool{}
	cats := map[string]bool{}
	parsed := 0
	for i, line := range config.Lines(body) {
		n := i + 1
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		if k, _, ok := strings.Cut(line, "="); ok {
			line = config.Trim(k)
		}
		k, c := config.FirstWord(line)
		if c == "" || strings.ContainsFunc(c, config.IsSpace) {
			fmt.Fprintf(&bad, "%d 行目: <状態> <分類> [= 説明] の形ではありません: %s\n", n, line)
			continue
		}
		if config.CharLen(k) > 30 || strings.ContainsAny(k, "],\"'`\\*?[") {
			fmt.Fprintf(&bad, "%d 行目: 状態は 1〜30 文字で、カンマ・引用符・* ? [ ] を含まない: %s\n", n, k)
			continue
		}
		if !slices.Contains(Categories, c) {
			fmt.Fprintf(&bad, "%d 行目: 分類は %s のどれか: %s\n", n, strings.Join(Categories, " "), c)
			continue
		}
		if b := builtinCat(k); b != "" && b != c {
			fmt.Fprintf(&bad, "%d 行目: 組み込みの状態 %s の分類は %s だけです(別の分類にするなら別の名前にする): %s\n", n, k, b, c)
			continue
		}
		if seen[k] {
			fmt.Fprintf(&bad, "%d 行目: 状態が重複しています: %s\n", n, k)
			continue
		}
		seen[k], cats[c] = true, true
		parsed++
	}
	if bad.Len() == 0 {
		if parsed == 0 {
			bad.WriteString("状態が 1 つもありません(設定を消すなら tb config unset statuses)\n")
		}
		for _, need := range []string{"todo", "active", "done"} {
			if parsed > 0 && !cats[need] {
				fmt.Fprintf(&bad, "分類 %s の状態が 1 つもありません\n", need)
			}
		}
	}
	return bad.String()
}

func badLabel(k string) bool {
	return k == "" || config.CharLen(k) > 40 || strings.ContainsFunc(k, func(r rune) bool { return config.IsSpace(r) || r == ',' || r == '"' || r == '\'' })
}

// ValidateLabels は config_validate_labels(tb config set / edit labels)。
func ValidateLabels(body string) string {
	var bad strings.Builder
	seen := map[string]bool{}
	for i, line := range config.Lines(body) {
		n := i + 1
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		k, _, _ := strings.Cut(line, "=")
		k = config.Trim(k)
		if badLabel(k) {
			fmt.Fprintf(&bad, "%d 行目: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: %s\n", n, k)
		} else if seen[k] {
			fmt.Fprintf(&bad, "%d 行目: ラベルが重複しています: %s\n", n, k)
		}
		seen[k] = true
	}
	return bad.String()
}

// SystemLabelBuiltin は core.sh の SYSLABEL_BUILTIN。
var SystemLabelBuiltin = map[string]string{"deadline": "deadline", "team": "team:{team}"}

// ValidateSystemLabels は system_labels_parse の検査。
func ValidateSystemLabels(body string) string {
	var bad strings.Builder
	var uses []string
	for k := range SystemLabelBuiltin {
		uses = append(uses, k)
	}
	sort.Strings(uses)
	seen := map[string]bool{}
	for i, line := range config.Lines(body) {
		n := i + 1
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			fmt.Fprintf(&bad, "%d 行目: <用途> = <ラベル> の形ではありません: %s\n", n, line)
			continue
		}
		k, v = config.Trim(k), config.Trim(v)
		if _, known := SystemLabelBuiltin[k]; !known {
			fmt.Fprintf(&bad, "%d 行目: 知らない用途です: %s(使えるのは: %s)\n", n, k, strings.Join(uses, " "))
			continue
		}
		if seen[k] {
			fmt.Fprintf(&bad, "%d 行目: 用途が重複しています: %s\n", n, k)
			continue
		}
		chk := v
		if k == "team" {
			if !strings.Contains(v, "{team}") {
				fmt.Fprintf(&bad, "%d 行目: team のラベルには {team} を含める: %s\n", n, v)
				continue
			}
			chk = strings.ReplaceAll(v, "{team}", "x")
		}
		if badLabel(chk) {
			fmt.Fprintf(&bad, "%d 行目: ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: %s\n", n, v)
			continue
		}
		seen[k] = true
	}
	return bad.String()
}
