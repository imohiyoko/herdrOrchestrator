// Package conv は会話チームのサービス(taskboard/lib/conv.sh: 振り分け表・下書きの指示・声の役・パターンの台帳)。
// 文面(依頼・下書き・最終文)は DB にも記録にも書かない。ファイルと標準入力を読んで標準出力に出すだけ。
package conv

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 振り分け表(設定 conv-routes)。bash では awk 1 か所(_conv_routes_awk)にあった判定をそのまま移したもの。

type rule struct {
	f                               [5]string
	kind, channel, timing, severity string
	line                            int
}

// Table は読み込んだ振り分け表。
type Table struct {
	Ver   string // awk の v+0 を出した形
	rules []rule
	cp    map[string]bool
	cpl   []string
}

func atrim(s string) string { return strings.Trim(s, " \t\r") }

var reName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func okname(s string) bool { return reName.MatchString(s) && len(s) <= 40 }

func okpat(s string) bool {
	if s == "*" {
		return true
	}
	for _, a := range strings.Split(s, ",") {
		if !okname(a) {
			return false
		}
	}
	return true
}

// awkSplit は split(s, a, /[ \t]+/)(先頭・末尾の区切りは空の要素を作る。ただし s が空なら 0 個)。
var reWS = regexp.MustCompile(`[ \t]+`)

func awkSplit(s string) []string {
	if s == "" {
		return nil
	}
	return reWS.Split(s, -1)
}

// awkNum は v+0 を print したもの(整数ならそのまま)。
func awkNum(v string) string {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n < 1<<53 {
		return strconv.FormatInt(n, 10)
	}
	f, _ := strconv.ParseFloat(v, 64)
	if f == float64(int64(f)) && f < 1e16 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 6, 64)
}

// Load は load(): 表を読む。誤りは理由を返す。
func Load(body string) (*Table, string) {
	t := &Table{cp: map[string]bool{}}
	ls := strings.Split(body, "\n")
	if len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	for i, line := range ls {
		ln := i + 1
		line = atrim(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m, _ := regexp.MatchString(`^version[ \t]*=`, line); m {
			v := atrim(line[strings.Index(line, "=")+1:])
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(v) || strings.Trim(v, "0") == "" {
				return nil, fmt.Sprintf("L%d: version は 1 以上の整数です", ln)
			}
			if t.Ver != "" {
				return nil, fmt.Sprintf("L%d: version が 2 回あります", ln)
			}
			t.Ver = awkNum(v)
			continue
		}
		if m, _ := regexp.MatchString(`^counterparts[ \t]*=`, line); m {
			for _, o := range awkSplit(atrim(line[strings.Index(line, "=")+1:])) {
				if o == "" {
					continue
				}
				if !okname(o) {
					return nil, fmt.Sprintf("L%d: 相手の区分の名前が不正です: %s", ln, o)
				}
				t.cp[o] = true
				t.cpl = append(t.cpl, o)
			}
			continue
		}
		ix := strings.Index(line, "->")
		if ix < 0 {
			return nil, fmt.Sprintf("L%d: 'version = N' / 'counterparts = ...' / '<5 欄> -> kind=... channel=... timing=...' のどれでもありません", ln)
		}
		lhs, rhs := atrim(line[:ix]), atrim(line[ix+2:])
		f := awkSplit(lhs)
		if len(f) != 5 {
			return nil, fmt.Sprintf("L%d: 左辺は 5 欄(event counterpart depth severity deadline)です", ln)
		}
		for i := 0; i < 5; i++ {
			if !okpat(f[i]) {
				return nil, fmt.Sprintf("L%d: 欄 %d は * か名前(カンマ区切り可)です: %s", ln, i+1, f[i])
			}
		}
		seen := map[string]string{}
		for _, o := range awkSplit(rhs) {
			eq := strings.Index(o, "=")
			if eq < 0 {
				return nil, fmt.Sprintf("L%d: 右辺は キー=値 です: %s", ln, o)
			}
			k, v := o[:eq], o[eq+1:]
			if k != "kind" && k != "channel" && k != "timing" && k != "severity" {
				return nil, fmt.Sprintf("L%d: 右辺のキーは kind / channel / timing / severity です: %s", ln, k)
			}
			if !okname(v) {
				return nil, fmt.Sprintf("L%d: %s の値が不正です: %s", ln, k, v)
			}
			if _, dup := seen[k]; dup {
				return nil, fmt.Sprintf("L%d: %s が 2 回あります", ln, k)
			}
			seen[k] = v
		}
		_, hk := seen["kind"]
		_, hc := seen["channel"]
		_, ht := seen["timing"]
		if !hk || !hc || !ht {
			return nil, fmt.Sprintf("L%d: 右辺には kind / channel / timing が要ります", ln)
		}
		r := rule{kind: seen["kind"], channel: seen["channel"], timing: seen["timing"], severity: "keep", line: ln}
		if s, ok := seen["severity"]; ok {
			r.severity = s
		}
		copy(r.f[:], f)
		t.rules = append(t.rules, r)
	}
	if t.Ver == "" {
		return nil, "version の行がありません"
	}
	return t, ""
}

// Rules は表の行数。
func (t *Table) Rules() int { return len(t.rules) }

func fmatch(p, v string) bool {
	if p == "*" {
		return true
	}
	for _, a := range strings.Split(p, ",") {
		if a == v {
			return true
		}
	}
	return false
}

// Result は振り分けの結果。
type Result struct {
	Routed                                 bool
	Reason                                 string // 未登録の理由(counterpart / no-route / no-table)
	Line                                   int
	Kind, Counterpart, Channel, Timing, Sv string
}

// Route は route(): 依頼(5 つの区分の名前)を振り分ける。上から見て最初に一致した行で決まる。
func (t *Table) Route(q [5]string) Result {
	if !t.cp[q[1]] {
		return Result{Reason: "counterpart"}
	}
	for _, r := range t.rules {
		ok := true
		for i := 0; i < 5; i++ {
			if !fmatch(r.f[i], q[i]) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		sv := r.severity
		if sv == "keep" {
			sv = q[3]
		}
		return Result{Routed: true, Line: r.line, Kind: r.kind, Counterpart: q[1], Channel: r.channel, Timing: r.timing, Sv: sv}
	}
	return Result{Reason: "no-route"}
}

// strip は strip_line: 行番号を除いた比べる形。
func (r Result) strip() string {
	if !r.Routed {
		return "unregistered\t" + r.Reason
	}
	return strings.Join([]string{"routed", r.Kind, r.Counterpart, r.Channel, r.Timing, r.Sv}, "\t")
}

// Compare は mode=compare: 今の表(nil なら空の表)と新しい表を、出てくる名前の全組み合わせで比べる。
// big なら組み合わせが多すぎて比べていない。
func Compare(old, nw *Table) (looser, changed, stricter int, big bool) {
	if old == nil {
		old = &Table{Ver: "0", cp: map[string]bool{}}
	}
	var dom [5][]string
	total := 1
	for i := 0; i < 5; i++ {
		seen := map[string]bool{}
		for _, t := range []*Table{old, nw} {
			for _, r := range t.rules {
				if r.f[i] == "*" {
					continue
				}
				for _, a := range strings.Split(r.f[i], ",") {
					if !seen[a] {
						seen[a] = true
						dom[i] = append(dom[i], a)
					}
				}
			}
			if i == 1 {
				for _, c := range t.cpl {
					if !seen[c] {
						seen[c] = true
						dom[i] = append(dom[i], c)
					}
				}
			}
		}
		dom[i] = append(dom[i], "~other")
		total *= len(dom[i])
		if total > 500000 {
			return 0, 0, 0, true
		}
	}
	var q [5]string
	for _, a1 := range dom[0] {
		for _, a2 := range dom[1] {
			for _, a3 := range dom[2] {
				for _, a4 := range dom[3] {
					for _, a5 := range dom[4] {
						q = [5]string{a1, a2, a3, a4, a5}
						o, w := old.Route(q), nw.Route(q)
						if o.strip() == w.strip() || (!o.Routed && !w.Routed) {
							continue
						}
						switch {
						case !o.Routed:
							looser++
						case !w.Routed:
							stricter++
						default:
							changed++
						}
					}
				}
			}
		}
	}
	return
}
