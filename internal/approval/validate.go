// Package approval は承認・人だけの判定(taskboard/lib/approvals.sh ほか)。今は設定 org の検査だけ。
package approval

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

var (
	reSub  = regexp.MustCompile(`^@?[A-Za-z0-9*?._+-]{1,64}$`)
	reBoss = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// ValidateOrg は config_validate_org。
func ValidateOrg(body string) string {
	var bad strings.Builder
	for i, line := range config.Lines(body) {
		n := i + 1
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		l, r, ok := strings.Cut(line, "->")
		if !ok {
			fmt.Fprintf(&bad, "%d 行目: <部下のパターン> -> <上司> の形ではありません: %s\n", n, line)
			continue
		}
		l, r = config.Trim(l), config.Trim(r)
		if !reSub.MatchString(l) {
			fmt.Fprintf(&bad, "%d 行目: 部下は agent 名の glob か @<役割>: %s\n", n, l)
		}
		if r != "~parent" && !reBoss.MatchString(r) {
			fmt.Fprintf(&bad, "%d 行目: 上司は agent 名か ~parent(glob・@役割は書けない): %s\n", n, r)
		}
	}
	return bad.String()
}
