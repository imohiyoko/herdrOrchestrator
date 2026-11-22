package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

// posix は grep -E / sed -E と同じく、いちばん左で最も長い一致を取る正規表現。
func posix(expr string) *regexp.Regexp {
	re := regexp.MustCompile(expr)
	re.Longest()
	return re
}

// 秘匿フィルタ(設定では変えられない)。grep -oEi で見つかった文字列が「秘密らしい値」
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"AWS アクセスキー ID", posix(`(?i)(AKIA|ASIA)[0-9A-Z]{16}`)},
	{"秘密鍵のヘッダ", posix(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"トークン(API キーなどの形式)", posix(`(?i)gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{30,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}|hooks\.slack\.com/services/[A-Za-z0-9/]{8,}`)},
	{"Bearer トークン", posix(`(?i)bearer +[A-Za-z0-9._~+/=-]{16,}`)},
	{"キー=値の形の秘密", posix(`(?i)(token|secret|passw(or)?d|api[_-]?key|access[_-]?key|private[_-]?key|credential)[A-Za-z0-9_]*[[:space:]]*[:=][[:space:]]*[A-Za-z0-9_+/=.@:!#$%^&*-]{6,}`)},
}

var (
	reRun    = posix(`[A-Za-z0-9+=]{32,}|[A-Za-z0-9_-]{40,}`)
	reHexRun = regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)
	reDigit  = regexp.MustCompile(`[0-9]`)
	reLower  = regexp.MustCompile(`[a-z]`)
	reUpper  = regexp.MustCompile(`[A-Z]`)
)

// grepO は grep -o: 行ごとに、重ならない一致を左から。
func grepO(re *regexp.Regexp, s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		for _, m := range re.FindAllString(l, -1) {
			if m != "" {
				out = append(out, m)
			}
		}
	}
	return out
}

// longRuns は notify_long_runs: 長い base64 / 16 進の連なり(大文字・小文字・数字の混在か、32 字以上の 16 進)。
func longRuns(s string) []string {
	var out []string
	for _, c := range grepO(reRun, s) {
		if reHexRun.MatchString(c) || (reDigit.MatchString(c) && reLower.MatchString(c) && reUpper.MatchString(c)) {
			out = append(out, c)
		}
	}
	return out
}

// Scan は notify_secret_scan: 見つけた種類の名前(", " 区切り)と、見つけた文字列。
func Scan(s string) (kinds string, hits []string) {
	var ks []string
	for _, p := range secretPatterns {
		if h := grepO(p.re, s); len(h) > 0 {
			ks = append(ks, p.name)
			hits = append(hits, h...)
		}
	}
	if h := longRuns(s); len(h) > 0 {
		ks = append(ks, "長い英数字の連なり")
		hits = append(hits, h...)
	}
	return strings.Join(ks, ", "), hits
}

// PatternKinds は pq_patterns_only: 決まった形(長い連なりは見ない)のうち見つかった種類の名前(", " 区切り)。値は返さない。
func PatternKinds(s string) string {
	var ks []string
	for _, p := range secretPatterns {
		if len(grepO(p.re, s)) > 0 {
			ks = append(ks, p.name)
		}
	}
	return strings.Join(ks, ", ")
}

// Redact は notify_redact: 見つけた文字列を長いものから順に [伏せた秘密] にする(sort -rn と同じ順)。
func Redact(t string, hits []string) string {
	type h struct {
		line string
		n    int
		s    string
	}
	var hs []h
	for _, s := range hits {
		if strings.TrimFunc(s, func(r rune) bool { return r == ' ' || r == '\t' }) == "" {
			continue // awk の NF(空白だけの行は飛ばす)
		}
		hs = append(hs, h{strconv.Itoa(len(s)) + "\t" + s, len(s), s})
	}
	sort.SliceStable(hs, func(i, j int) bool {
		if hs[i].n != hs[j].n {
			return hs[i].n > hs[j].n
		}
		return hs[i].line > hs[j].line
	})
	for _, x := range hs {
		t = strings.ReplaceAll(t, x.s, "[伏せた秘密]")
	}
	return t
}

// dropCtl は tr -d '\000-\010\013\014\016-\037\177'(タブ・改行・CR は残す)。
func dropCtl(s string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

var (
	reFence   = regexp.MustCompile("^[[:space:]]*```")
	reLogDate = regexp.MustCompile(`^[[:space:]]*\[?[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9]{2}:[0-9]{2}`)
	reLogTime = regexp.MustCompile(`^[[:space:]]*\[?[0-9]{2}:[0-9]{2}:[0-9]{2}`)
)

// Lines は notify_lines: (strip なら)コードと時刻付きのログ行を落とし、空でない先頭の 2 行を " / " でつなぐ。
func Lines(t string, strip bool) string {
	ls := strings.Split(t, "\n")
	if strip {
		var out []string
		fence := false
		for _, l := range ls {
			switch {
			case reFence.MatchString(l):
				if !fence {
					out = append(out, "(コードは省略)")
				}
				fence = !fence
			case fence, reLogDate.MatchString(l), reLogTime.MatchString(l):
			default:
				out = append(out, l)
			}
		}
		ls = out
	}
	var b strings.Builder
	n := 0
	for _, l := range ls {
		l = strings.NewReplacer("\r", " ", "\t", " ").Replace(dropCtl(l))
		if strings.Trim(l, " ") == "" {
			continue
		}
		if n > 0 {
			b.WriteString(" / ")
		}
		b.WriteString(strings.Trim(l, " "))
		if n++; n >= 2 {
			break
		}
	}
	return b.String()
}

// Clip は nclip: 先頭 n 字(jq の .[0:$n]。末尾の改行は落ちる)。
func Clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return strings.TrimRight(s, "\n")
}

var (
	reEmail   = posix(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reIP      = posix(`([0-9]{1,3})\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}`)
	reAccount = posix(`(^|[^0-9])[0-9]{8}([0-9]{4})($|[^0-9])`)
	reHost    = posix(`[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.(internal|local|lan|corp|amazonaws\.com)`)
	reMention = posix(`@(here|channel|everyone)`)
	reSpaces  = regexp.MustCompile(` +`)
)

// Finish は notify_finish: 制御文字の除去 → 伏せ字 → メンションと <...> の無効化 → 長さの上限。
func (c *Cfg) Finish(t string) string {
	t = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(dropCtl(t))
	t = reSpaces.ReplaceAllString(t, " ")
	if c.Flag("mask_email") {
		t = reEmail.ReplaceAllString(t, "a***@***")
	}
	if c.Flag("mask_ip") {
		t = reIP.ReplaceAllString(t, "${1}.*.*.*")
	}
	if c.Flag("mask_account") {
		t = reAccount.ReplaceAllString(t, "${1}********${2}${3}")
	}
	if c.Flag("mask_host") {
		t = reHost.ReplaceAllString(t, "[host]")
	}
	if c.Flag("mask_repo") {
		for _, r := range Repos() {
			t = strings.ReplaceAll(t, r, "[repo]")
		}
	}
	t = reMention.ReplaceAllString(t, "＠${1}")
	t = strings.NewReplacer("<", "‹", ">", "›").Replace(t)
	return Clip(config.Trim(t), c.Int("max_len"))
}

// Hash は notify_hash: 本文の sha256 の先頭 12 字。
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func plus(s, f string) string {
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(f, "%", s)
}

// Compose は notify_compose: 種別ごとの本文。
func Compose(typ, ref, sev, a, b, c, d string) string {
	switch typ {
	case "alert_raised":
		return "[アラート] " + ref + " 検出(緊急度は調査中) / 検出元: " + or(b, "-") + " / " + a + plus(c, " — %")
	case "alert_result":
		return "[調査結果] " + ref + " " + or(sev, "?") + ": " + a + " — " + b + plus(c, " (報告: %)")
	case "pr_opened":
		return "[PR] " + ref + " を出した" + plus(b, "(%)") + ": " + a + plus(c, " / 作成: %") + plus(d, " / %")
	case "pr_approved":
		return "[PR] " + ref + " approve" + plus(c, "(レビュー: %)") + ": " + a + " / マージは人"
	case "pr_changes":
		return "[PR] " + ref + " 変更依頼" + plus(c, "(レビュー: %)") + ": " + a
	case "pr_merged":
		return "[PR] " + ref + " マージ済み" + plus(c, "(%)") + ": " + a
	case "question":
		return "[質問] " + a + plus(sev, " %") + " / " + b + ": " + c
	case "blocked":
		return "[詰まり] " + a + plus(sev, " %") + " / " + b + ": " + c
	case "done":
		return "[完了] " + a + plus(sev, " %") + ": " + b + plus(c, " / 成果物: %")
	case "incident_opened":
		return "[障害] " + ref + " 開始" + plus(sev, " %") + ": " + a
	case "incident_closed":
		return "[障害] " + ref + " 終了" + plus(sev, " %") + ": " + a
	case "approvals_pending":
		return "[承認待ち] " + a + "(承認は人の端末)"
	case "summary", "digest":
		return "[まとめ] " + a
	case "test":
		return "[テスト] tb notify test: 疎通確認の固定文です。返信や操作は要りません"
	case "warning":
		return "[通知の警告] " + a
	case "ticket":
		return "[チケット] " + a + " — " + b
	}
	return ""
}
