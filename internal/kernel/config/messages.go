package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// MsgVars は config.sh の MSG_VARS: agent に返す文面の識別子と、使える {名前}。
var MsgVars = map[string][]string{
	"send.denied.default":        {"sender"},
	"send.blocked.notice":        {"id", "target", "kind", "rule"},
	"nudge.text":                 {"task", "title", "minutes", "status", "active"},
	"nudge.start":                {"task", "title", "minutes", "status", "agent", "session"},
	"nudge.multi":                {"task", "others", "active", "todo"},
	"nudge.pool":                 {"task", "title", "agent", "session", "todo"},
	"alert.analyze":              {"id"},
	"spawn.denied":               {"why"},
	"role.change.needs-approval": {"rule", "command"},
	"identity.no-pane":           nil,
	"identity.herdr-failed":      nil,
}

// MsgCodes は識別子の一覧(名前の順。bash の msg_codes と同じ)。
func MsgCodes() string {
	var ks []string
	for k := range MsgVars {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, " ")
}

var reVar = regexp.MustCompile(`\{([A-Za-z_]+)\}`)

// ValidateMessages は config_validate_messages。
func ValidateMessages(body string) string {
	var bad strings.Builder
	for i, line := range Lines(body) {
		n := i + 1
		line = Trim(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, text, ok := strings.Cut(line, "=")
		if !ok {
			fmt.Fprintf(&bad, "%d 行目: <識別子> = <文面> の形ではありません\n", n)
			continue
		}
		code, text = Trim(code), Trim(text)
		vars, known := MsgVars[code]
		if !known {
			fmt.Fprintf(&bad, "%d 行目: 知らない識別子です: %s(使えるのは: %s)\n", n, code, MsgCodes())
			continue
		}
		if text == "" {
			fmt.Fprintf(&bad, "%d 行目: 文面が空です: %s\n", n, code)
		}
		for {
			m := reVar.FindStringSubmatch(text)
			if m == nil {
				break
			}
			v := m[1]
			if !slices.Contains(vars, v) {
				use := strings.Join(vars, " ")
				if use == "" {
					use = "なし"
				}
				fmt.Fprintf(&bad, "%d 行目: %s では {%s} は使えません(使えるのは: %s)\n", n, code, v, use)
			}
			text = strings.ReplaceAll(text, "{"+v+"}", "")
		}
	}
	return bad.String()
}
