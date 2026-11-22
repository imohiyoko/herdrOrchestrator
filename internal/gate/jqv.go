package gate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// gh の応答を jq と同じ意味で読むための小さな道具。値は json.Decoder(UseNumber)で読んだ any。
// jq なら式がエラーになる形(文字列でない本文など)は errBad を返し、呼ぶ側は「解析に失敗」として何も記録しない。

var errBad = errors.New("解析に失敗")

func decodeAll(b []byte) ([]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out []any
	for {
		var v any
		if err := d.Decode(&v); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

// Fetch は _gate_ingest_fetch: gh api --paginate <path> のページ(配列)をつないで 1 つの配列にする。
func Fetch(gh, path string) ([]any, error) {
	out, err := exec.Command(gh, "api", "--paginate", path).Output()
	if err != nil {
		return nil, err
	}
	vs, err := decodeAll(out)
	if err != nil || len(vs) == 0 {
		return nil, errBad
	}
	var all []any
	for _, v := range vs {
		a, ok := v.([]any)
		if !ok {
			return nil, errBad
		}
		all = append(all, a...)
	}
	if all == nil {
		all = []any{}
	}
	return all, nil
}

// FetchObj は _gate_ingest_fetch_obj: gh api <path> が object を返すこと。
func FetchObj(gh, path string) (map[string]any, error) {
	out, err := exec.Command(gh, "api", path).Output()
	if err != nil {
		return nil, err
	}
	vs, err := decodeAll(out)
	if err != nil || len(vs) == 0 {
		return nil, errBad
	}
	for _, v := range vs {
		if _, ok := v.(map[string]any); !ok {
			return nil, errBad
		}
	}
	return vs[0].(map[string]any), nil
}

// get は .k(null の .k は null、object でなければエラー)。
func get(v any, ks ...string) (any, error) {
	for _, k := range ks {
		switch x := v.(type) {
		case nil:
			return nil, nil
		case map[string]any:
			v = x[k]
		default:
			return nil, errBad
		}
	}
	return v, nil
}

func mustGet(v any, ks ...string) any {
	x, err := get(v, ks...)
	if err != nil {
		panic(errBad)
	}
	return x
}

func truthy(v any) bool { return v != nil && v != false }

// alt は (v // d)。
func alt(v, d any) any {
	if truthy(v) {
		return v
	}
	return d
}

// str は文字列であること(jq の文字列の関数に渡せること)。
func str(v any) string {
	s, ok := v.(string)
	if !ok {
		panic(errBad)
	}
	return s
}

// jqNum は jq の数の出し方(整数ならそのまま、それ以外は 17 桁)。
func jqNum(n json.Number) string {
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return strconv.FormatInt(i, 10)
	}
	f, err := n.Float64()
	if err != nil {
		return string(n)
	}
	if f == float64(int64(f)) && f < 1e17 && f > -1e17 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 17, 64)
}

// tostring は jq の tostring。
func tostring(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return jqNum(x)
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func num(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// equal は jq の ==(数は値で比べる)。
func equal(a, b any) bool {
	if x, ok := num(a); ok {
		y, ok2 := num(b)
		return ok2 && x == y
	}
	switch x := a.(type) {
	case nil, bool, string:
		return a == b
	default:
		ja, _ := json.Marshal(x)
		jb, _ := json.Marshal(b)
		return bytes.Equal(ja, jb)
	}
}

func rank(v any) int {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 2
		}
		return 1
	case json.Number:
		return 3
	case string:
		return 4
	case []any:
		return 5
	default:
		return 6
	}
}

// less は jq の並びでの a < b(配列・object は JSON の文字列で比べる。目安)。
func less(a, b any) bool {
	ra, rb := rank(a), rank(b)
	if ra != rb {
		return ra < rb
	}
	switch x := a.(type) {
	case json.Number:
		fa, _ := num(x)
		fb, _ := num(b)
		return fa < fb
	case string:
		return x < b.(string)
	case bool, nil:
		return false
	default:
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		return string(ja) < string(jb)
	}
}

func sortBy(xs []any, key func(any) any) {
	sort.SliceStable(xs, func(i, j int) bool { return less(key(xs[i]), key(xs[j])) })
}

var (
	reHTMLComment = regexp.MustCompile(`<!--.*?-->`) // jq の "s" は ^ $ の扱いだけで、. は改行に当たらない
	reHTMLTag     = regexp.MustCompile(`<[^>]*>`)
	wsClass       = `[\t\n\v\f\r \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`
	reAddressed   = regexp.MustCompile(`(?:^|\n)` + wsClass + `*✅ Addressed in commit`)
	reSkipMark    = regexp.MustCompile(`(?i)rate limit|review skipped|reviews paused|usage limit|quota`)
	re40          = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func asciiDown(s string) string { return lowerASCII(s) }

// firstLine は GATE_JQ_DEFS の firstline: HTML コメントとタグを除き、空行を飛ばした最初の行(小文字)。
func firstLine(v any) string {
	s := str(alt(v, ""))
	s = reHTMLComment.ReplaceAllString(s, "")
	s = reHTMLTag.ReplaceAllString(s, "")
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimFunc(l, unicode.IsSpace); l != "" {
			return asciiDown(l)
		}
	}
	return ""
}

// sev は表の語が最初の行にあれば blocker / should、無ければ unknown。
func (c IngestConf) sev(v any) string {
	l := firstLine(v)
	hit := ""
	for i, k := range c.SevKeys {
		if strings.Contains(l, k) {
			if c.SevVals[i] == "blocker" {
				return "blocker"
			}
			hit = "should"
		}
	}
	if hit == "" {
		return "unknown"
	}
	return hit
}

func addressed(v any) bool { return reAddressed.MatchString(str(alt(v, ""))) }

func skipmark(v any) bool {
	ls := strings.Split(str(alt(v, "")), "\n")
	if len(ls) > 12 {
		ls = ls[:12]
	}
	return reSkipMark.MatchString(strings.Join(ls, "\n"))
}
