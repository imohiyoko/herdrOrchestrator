// Package message は agent への連絡(taskboard/lib/send.sh ほか)。今は設定 nudge の検査だけ。
package message

import (
	"fmt"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ValidateNudge は config_validate_nudge(nudge_parse と、wake の名前が今の分類か状態にあること)。
func ValidateNudge(st tickets.StatusSet) config.Validator {
	return func(body string) string {
		var bad strings.Builder
		var wake []string
		for i, line := range config.Lines(body) {
			n := i + 1
			line = config.Trim(config.StripComment(line))
			if line == "" {
				continue
			}
			k, v := config.FirstWord(line)
			switch {
			case k == "ignore" && v != "" && !strings.ContainsFunc(v, config.IsSpace) && v != "@":
			case k == "wake" && v != "":
				for _, w := range strings.FieldsFunc(v, config.IsSpace) {
					switch {
					case config.CharLen(w) > 30 || strings.ContainsAny(w, "],\"'`\\*?["):
						fmt.Fprintf(&bad, "%d 行目: wake の状態の名前に使えない文字があります: %s\n", n, w)
					case w == "done" || w == "cancelled" || st.Cat(w) == "done" || st.Cat(w) == "cancelled":
						fmt.Fprintf(&bad, "%d 行目: 閉じた状態(done / cancelled の分類)は催促できません: %s\n", n, w)
					default:
						wake = append(wake, w)
					}
				}
			default:
				fmt.Fprintf(&bad, "%d 行目: ignore <@役割の glob | agent 名の glob> か wake <分類 | 状態> ... の形ではありません: %s\n", n, line)
			}
		}
		if bad.Len() > 0 {
			return bad.String()
		}
		for _, w := range wake {
			if !slices.Contains(tickets.Categories, w) && st.Cat(w) == "" {
				fmt.Fprintf(&bad, "wake: 分類にも状態(設定 statuses)にもない名前です: %s\n", w)
			}
		}
		return bad.String()
	}
}
