package webui

import (
	"regexp"
	"strings"
)

// sanitize.py と同じ: 外部由来の文字列は E を通して HTML に出す。

var (
	reEsc = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]" + // CSI
		"|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?" + // OSC
		"|\x1b[P^_X][^\x1b]*(?:\x1b\\\\)?" + // DCS/PM/APC/SOS
		"|\x1b[ -/]*[0-~]") // その他の ESC 列
	reCtrl = regexp.MustCompile("[\x00-\x08\x0b-\x1f\x7f-\u009f‪-‮⁦-⁩]")
)

// Clean はターミナル制御文字(ANSI など)を除く。改行とタブは残す。
func Clean(s string) string {
	s = reEsc.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return reCtrl.ReplaceAllString(s, "")
}

var htmlEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")

// E は html.escape(clean(s), quote=True)。
func E(s string) string { return htmlEsc.Replace(Clean(s)) }
