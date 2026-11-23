// Package message は agent への連絡(taskboard/lib/send.sh ほか)。
package message

import (
	"fmt"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ParseNudge は nudge_parse: ignore の glob と wake の名前。不正な行があれば bad に(行ごとに改行つき)。
func ParseNudge(st tickets.StatusSet, body string) (ignores, wake []string, bad string) {
	var b strings.Builder
	for i, line := range config.Lines(body) {
		n := i + 1
		line = config.Trim(config.StripComment(line))
		if line == "" {
			continue
		}
		k, v := config.FirstWord(line)
		switch {
		case k == "ignore" && v != "" && !strings.ContainsFunc(v, config.IsSpace) && v != "@":
			ignores = append(ignores, v)
		case k == "wake" && v != "":
			for _, w := range strings.FieldsFunc(v, config.IsSpace) {
				switch {
				case config.CharLen(w) > 30 || strings.ContainsAny(w, "],\"'`\\*?["):
					fmt.Fprintf(&b, "%d 行目: wake の状態の名前に使えない文字があります: %s\n", n, w)
				case w == "done" || w == "cancelled" || st.Cat(w) == "done" || st.Cat(w) == "cancelled":
					fmt.Fprintf(&b, "%d 行目: 閉じた状態(done / cancelled の分類)は催促できません: %s\n", n, w)
				default:
					wake = append(wake, w)
				}
			}
		default:
			fmt.Fprintf(&b, "%d 行目: ignore <@役割の glob | agent 名の glob> か wake <分類 | 状態> ... の形ではありません: %s\n", n, line)
		}
	}
	return ignores, wake, b.String()
}

// ValidateNudge は config_validate_nudge(nudge_parse と、wake の名前が今の分類か状態にあること)。
func ValidateNudge(st tickets.StatusSet) config.Validator {
	return func(body string) string {
		_, wake, bad := ParseNudge(st, body)
		if bad != "" {
			return bad
		}
		var b strings.Builder
		for _, w := range wake {
			if !slices.Contains(tickets.Categories, w) && st.Cat(w) == "" {
				fmt.Fprintf(&b, "wake: 分類にも状態(設定 statuses)にもない名前です: %s\n", w)
			}
		}
		return b.String()
	}
}

// ParseWakeArg は tb reminder nudge --wake の値(分類か状態の名前。カンマか空白で区切る。複数回は足し合わせる)。
// 設定 nudge の wake と同じ名前の決まりに加えて、今の分類にも状態にも無い名前も断る(打ち間違いで誰も催促しないのを防ぐ)。
// 設定の本文としては読まない(# を注釈として落とさず、名前ごとに検査する)。
func ParseWakeArg(st tickets.StatusSet, vals []string) (wake []string, bad string) {
	var b strings.Builder
	for _, v := range vals {
		names := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || config.IsSpace(r) })
		if len(names) == 0 {
			b.WriteString("--wake に分類か状態の名前が必要です\n")
			continue
		}
		for _, w := range names {
			switch {
			case config.CharLen(w) > 30 || strings.ContainsAny(w, "],\"'`\\*?[#"):
				fmt.Fprintf(&b, "--wake の状態の名前に使えない文字があります: %s\n", w)
			case w == "done" || w == "cancelled" || st.Cat(w) == "done" || st.Cat(w) == "cancelled":
				fmt.Fprintf(&b, "--wake: 閉じた状態(done / cancelled の分類)は催促できません: %s\n", w)
			case !slices.Contains(tickets.Categories, w) && st.Cat(w) == "":
				fmt.Fprintf(&b, "--wake: 分類にも状態(設定 statuses)にもない名前です: %s\n", w)
			default:
				wake = append(wake, w)
			}
		}
	}
	return wake, b.String()
}
