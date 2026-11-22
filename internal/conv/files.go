package conv

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

const (
	VoiceMax = 3
	NoteMax  = 300
)

var Evidence = []string{"human-edit", "human-written", "other-source", "agent-generated"}

// Dir は CONV_DIR(TB_CONV_DIR。既定は <tb>/../conversation)。
func Dir() string {
	if d := os.Getenv("TB_CONV_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("TB_ROOT"), "..", "conversation")
}

var reConvName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// ValidName は conv_valid_name。
func ValidName(s string) bool { return reConvName.MatchString(s) }

// TeamsOn は conv_teams_on(TB_CONV_TEAMS=off でチームの層を止める)。
func TeamsOn() bool { return os.Getenv("TB_CONV_TEAMS") != "off" }

// regular はシンボリックリンクでない普通のファイル([[ -f $f && ! -L $f ]])。
func regular(p string) bool {
	l, err := os.Lstat(p)
	return err == nil && l.Mode().IsRegular()
}

// HasTeam は conv_has_team。
func HasTeam(kind string) bool {
	return ValidName(kind) && regular(filepath.Join(Dir(), "kinds", kind, "prompt.md"))
}

// isUnicodeSpace は jq(Oniguruma)の \s。
func isUnicodeSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', 0x85:
		return true
	}
	return unicode.Is(unicode.Zs, r) || r == 0x2028 || r == 0x2029
}

// Chars は conv_chars: 文字数(コードポイント。末尾の空白・改行は数えない)。
func Chars(b []byte) int {
	s := strings.ToValidUTF8(string(b), "�")
	return utf8.RuneCountInString(strings.TrimRightFunc(s, isUnicodeSpace))
}

// FileChars はファイルの Chars。
func FileChars(p string) int {
	b, _ := os.ReadFile(p)
	return Chars(b)
}

// Strlen は conv_strlen。
func Strlen(s string) int { return utf8.RuneCountInString(strings.ToValidUTF8(s, "�")) }

// Readable は [[ -f $f && -r $f ]]。
func Readable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Generalized は conv_generalized: 一般化した説明か(数字・URL・メール・@名前・#番号を含まない)。だめなら理由。
func Generalized(what, s string) string {
	switch {
	case strings.ContainsAny(s, "0123456789０１２３４５６７８９"):
		return what + " に数字があります(件数・番号・日付は書かず、一般化した言い方にする)"
	case strings.Contains(s, "://") || strings.Contains(s, "www."):
		return what + " に URL があります(参照は --ref で渡す)"
	case strings.ContainsAny(s, "@＠"):
		return what + " に @ があります(名前・メールは書かない)"
	case strings.ContainsAny(s, "#＃"):
		return what + " に # があります(チケット・レビューの番号は書かない)"
	case strings.ContainsAny(s, "|\n\t"):
		return what + " に | ・改行・タブは使えません"
	}
	return ""
}

// Pattern はパターンの台帳の 1 行(| 区切りの 8 列)。
type Pattern [8]string

func (p Pattern) Adopted() bool { return p[1] == "adopted" }

var rePatID = regexp.MustCompile(`^P-[A-Za-z0-9-]{1,20}$`)

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ReadPatterns は conv_patterns_read: 正しい行と誤りの一覧。
func ReadPatterns(f string) (pats []Pattern, errs []string) {
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, nil
	}
	ls := strings.Split(string(b), "\n")
	if len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	var ids []string
	for i, line := range ls {
		n := i + 1
		line = config.Trim(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c := strings.Split(line, "|")
		if len(c) != 8 {
			errs = append(errs, fmt.Sprintf("L%d: 列は 8 つ(id | state | evidence | situation | intent | ending | conclusion | avoid)です", n))
			continue
		}
		for i := range c {
			c[i] = config.Trim(c[i])
		}
		id, state, ev, concl := c[0], c[1], c[2], c[6]
		if !rePatID.MatchString(id) {
			errs = append(errs, fmt.Sprintf("L%d: id は P-<英数字>: %s", n, id))
			continue
		}
		if state != "candidate" && state != "adopted" {
			errs = append(errs, fmt.Sprintf("L%d %s: state は candidate か adopted: %s", n, id, state))
			continue
		}
		if !has(Evidence, ev) {
			errs = append(errs, fmt.Sprintf("L%d %s: evidence は %s のどれか: %s", n, id, strings.Join(Evidence, " "), ev))
			continue
		}
		if concl != "first" && concl != "last" {
			errs = append(errs, fmt.Sprintf("L%d %s: conclusion は first か last: %s", n, id, concl))
			continue
		}
		if state == "adopted" && ev == "agent-generated" {
			errs = append(errs, fmt.Sprintf("L%d %s: agent-generated を根拠に adopted にはできません", n, id))
			continue
		}
		ok := true
		for _, i := range []int{3, 4, 5, 7} {
			if c[i] == "" {
				errs = append(errs, fmt.Sprintf("L%d %s: 空の列があります(無いときは -)", n, id))
				ok = false
				break
			}
			if Strlen(c[i]) > 120 {
				errs = append(errs, fmt.Sprintf("L%d %s: 列が長すぎます(120 文字まで)", n, id))
				ok = false
				break
			}
			if e := Generalized(id, c[i]); e != "" {
				errs = append(errs, fmt.Sprintf("L%d %s", n, e))
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if has(ids, id) {
			errs = append(errs, fmt.Sprintf("L%d: id が重なっています: %s", n, id))
			continue
		}
		ids = append(ids, id)
		var p Pattern
		copy(p[:], c)
		pats = append(pats, p)
	}
	return pats, errs
}

// PatternsFile は種別のパターンの台帳のパス。
func PatternsFile(kind string) string { return filepath.Join(Dir(), "kinds", kind, "patterns.conf") }

// VoiceFile は conv_voice_file: 声の役の指示のファイル(無ければ "")。
func VoiceFile(role string) string {
	p := filepath.Join(Dir(), "voices", role+".md")
	if ValidName(role) && regular(p) {
		return p
	}
	return ""
}

// Voices は conv_voices: voices/*.md の名前(並びはグロブの順)。
func Voices() []string {
	fs, _ := filepath.Glob(filepath.Join(Dir(), "voices", "*.md"))
	sort.Strings(fs)
	var out []string
	for _, f := range fs {
		if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
			out = append(out, strings.TrimSuffix(filepath.Base(f), ".md"))
		}
	}
	return out
}

// Kinds は conv_kinds: kinds/<種別>/ のうち、シンボリックリンクでないディレクトリで名前が正しいもの。
func Kinds() []string {
	es, err := os.ReadDir(filepath.Join(Dir(), "kinds"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range es {
		p := filepath.Join(Dir(), "kinds", e.Name())
		l, err := os.Lstat(p)
		if err != nil || l.Mode()&os.ModeSymlink != 0 || !l.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if ValidName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
