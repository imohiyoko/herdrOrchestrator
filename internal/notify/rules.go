// Package notify は通知(taskboard/lib/notify.sh・notifyrules.sh)。今は規則の検査だけ。
package notify

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	Sinks    = []string{"file", "stdout", "slack", "webui", "conductor"}
	RuleKeys = []string{"type", "priority", "label", "project", "owner", "new", "old", "actor"}
	Kinds    = []string{"created", "status", "title", "type", "priority", "owner", "due", "parent", "description", "label_added", "label_removed", "ref_added", "ref_removed", "link", "unlink", "comment", "overdue", "due_soon", "pr_opened", "pr_approved", "pr_changes", "pr_merged"}

	reCond    = regexp.MustCompile(`^([a-z]+)(=|>=)(.*)$`)
	reVal     = regexp.MustCompile("^[^[:space:],\"'<>;|&$`\\\\]{1,64}$")
	reProject = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	// 行頭か空白の後の # からがコメント(値の中の # は残す)
	reComment = regexp.MustCompile(`[ \t\n\v\f\r]#`)
)

// ValidateRules は nrule_parse の検査(tb config set notify-rules)。誤りの本文は「notify-rules: 」で始まる。
func ValidateRules(body string) string {
	var bad strings.Builder
	for i, line := range config.Lines(body) {
		n := i + 1
		if loc := reComment.FindStringIndex(line); loc != nil {
			line = line[:loc[0]]
		}
		line = config.Trim(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lhs, rhs, ok := strings.Cut(line, "->")
		if !ok {
			fmt.Fprintf(&bad, "%d 行目: -> がありません\n", n)
			continue
		}
		lhs, rhs = config.Trim(lhs), config.Trim(rhs)
		if strings.Contains(rhs, "->") {
			fmt.Fprintf(&bad, "%d 行目: -> が 2 つあります\n", n)
			continue
		}
		f := strings.Fields(lhs)
		kind := ""
		if len(f) > 0 {
			kind = f[0]
		}
		if kind != "*" && !slices.Contains(Kinds, kind) {
			fmt.Fprintf(&bad, "%d 行目: 不明なイベントの種類 %s(* か %s)\n", n, kind, strings.Join(Kinds, " "))
			continue
		}
		rowBad := false
		for _, tok := range f[1:] {
			m := reCond.FindStringSubmatch(tok)
			if m == nil {
				fmt.Fprintf(&bad, "%d 行目: 条件は <キー>=<値>: %s\n", n, tok)
				rowBad = true
				continue
			}
			k, op, v := m[1], m[2], m[3]
			switch {
			case !slices.Contains(RuleKeys, k):
				fmt.Fprintf(&bad, "%d 行目: 不明な条件 %s(%s)\n", n, k, strings.Join(RuleKeys, " "))
			case op != "=" && k != "priority":
				fmt.Fprintf(&bad, "%d 行目: >= は priority だけ: %s\n", n, tok)
			case !reVal.MatchString(v):
				fmt.Fprintf(&bad, "%d 行目: 値が不正(空白・引用符・記号は使えない): %s\n", n, tok)
			case k == "type" && !slices.Contains(tickets.Types, v):
				fmt.Fprintf(&bad, "%d 行目: type は %s のどれか: %s\n", n, strings.Join(tickets.Types, " "), v)
			case k == "priority" && !slices.Contains(tickets.Priorities, v):
				fmt.Fprintf(&bad, "%d 行目: priority は %s のどれか: %s\n", n, strings.Join(tickets.Priorities, " "), v)
			case k == "project" && !reProject.MatchString(v):
				fmt.Fprintf(&bad, "%d 行目: project は英数字と _: %s\n", n, v)
			default:
				continue
			}
			rowBad = true
		}
		if rowBad {
			continue
		}
		var sinks []string
		for _, s := range strings.Fields(rhs) {
			if s == "lead" {
				s = "conductor"
			}
			if !slices.Contains(Sinks, s) {
				fmt.Fprintf(&bad, "%d 行目: 使えない届け先 %s(%s)\n", n, s, strings.Join(Sinks, " "))
				rowBad = true
				continue
			}
			if !slices.Contains(sinks, s) {
				sinks = append(sinks, s)
			}
		}
		if rowBad {
			continue
		}
		if len(sinks) == 0 {
			fmt.Fprintf(&bad, "%d 行目: 届け先がありません\n", n)
		}
	}
	if bad.Len() == 0 {
		return ""
	}
	return "notify-rules: " + bad.String()
}
