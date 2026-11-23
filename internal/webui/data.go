package webui

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// data.go は webui/app/data.py の Go 版(tasks.db とログの読み取り。読み取り専用)。

// row は 1 行(列名 → 値。値は int64 / float64 / string / []byte / nil)。
type row map[string]any

// sv はリクエストの中の 1 つの herdr セッションの読み取り(data.py の db_conn と request_db)。
// 接続は 1 本だけにして(一時ビューは接続ごと)、リクエストの間は使い回す。
type sv struct {
	sess  string
	db    *sql.DB
	tried bool
	st    *statusSet
}

func newSV(sess string) *sv { return &sv{sess: sess} }

func (v *sv) close() {
	if v != nil && v.db != nil {
		v.db.Close()
		v.db = nil
	}
}

var reTableName = regexp.MustCompile(`^[a-z_]{1,64}$`)

func nameSet(names []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		if reTableName.MatchString(n) {
			m[n] = true
		}
	}
	return m
}

// conn は DB を読み取り専用で開き、このセッションの行だけの一時ビューを作る(data.py の session_views)。無い・開けなければ nil。
func (v *sv) conn() *sql.DB {
	if v.tried {
		return v.db
	}
	v.tried = true
	p := TBDB()
	if !isFile(p) {
		return nil
	}
	db, err := sql.Open("sqlite", p+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	lit := store.Quote(v.sess)
	for _, kind := range []struct {
		typ  string
		want map[string]bool
	}{{"table", nameSet(store.SessionTables())}, {"view", nameSet(store.SessionViews())}} {
		var names []string
		rows, err := db.Query("SELECT m.name FROM main.sqlite_master m WHERE m.type=? AND EXISTS "+
			"(SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session')", kind.typ)
		if err != nil {
			db.Close()
			return nil
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil && kind.want[n] {
				names = append(names, n)
			}
		}
		rows.Close()
		for _, t := range names {
			if _, err := db.Exec("CREATE TEMP VIEW " + t + " AS SELECT * FROM main." + t + " WHERE herdr_session=" + lit); err != nil {
				db.Close()
				return nil
			}
		}
	}
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		db.Close()
		return nil
	}
	v.db = db
	return db
}

// qChecked は data.py の q_checked: (行, 照会できたか)。
func (v *sv) qChecked(q string, args ...any) ([]row, bool) {
	db := v.conn()
	if db == nil {
		return nil, false
	}
	if strings.Contains(q, "_STATUSES__") {
		var ok bool
		if q, ok = v.stq(q); !ok {
			return nil, false
		}
	}
	var out []row
	err := store.Each(db, q, args, func(cols []string, vals []any) error {
		r := row{}
		for i, c := range cols {
			r[c] = vals[i]
		}
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, false
	}
	return out, true
}

// q は読み取り専用で実行。DB 不在・表の欠落・破損でも空。
func (v *sv) q(q string, args ...any) []row { r, _ := v.qChecked(q, args...); return r }

// ---- Python の値の扱い(f 文字列・str()・or の真偽)

// pyStr は Python の str(): None は "None"。
func pyStr(x any) string {
	switch t := x.(type) {
	case nil:
		return "None"
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case float64:
		return pyFloat(t)
	case bool:
		if t {
			return "True"
		}
		return "False"
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func pyFloat(f float64) string {
	if f == float64(int64(f)) && f < 1e16 && f > -1e16 {
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// truthy は Python の真偽(None・空文字・0・空の並びは偽)。
func truthy(x any) bool {
	switch t := x.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case []byte:
		return len(t) > 0
	case int64:
		return t != 0
	case int:
		return t != 0
	case float64:
		return t != 0
	case bool:
		return t
	case json.Number:
		f, err := t.Float64()
		return err != nil || f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// or は Python の `x or y`(文字にして返す)。
func or(x any, def string) string {
	if truthy(x) {
		return pyStr(x)
	}
	return def
}

// short は data.py の _short: clean して前後の空白を落とし、n 文字を超えたら切って … を付ける。
func short(x any, n int) string {
	s := ""
	if x != nil {
		s = pyStr(x)
	}
	s = strings.TrimFunc(Clean(s), pyIsSpace)
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

// pyIsSpace は str.strip() が落とす文字。
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyQuote は urllib.parse.quote(s, safe=safe)。
func pyQuote(s, safe string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~", c) >= 0 || strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pySplitlines は str.splitlines()。
func pySplitlines(s string) []string {
	var out []string
	rs := []rune(s)
	cur := []rune{}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(cur))
			cur = cur[:0]
		case '\r':
			out = append(out, string(cur))
			cur = cur[:0]
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// ---- 時刻

var tsLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999-0700", "2006-01-02 15:04:05.999999999-0700",
	"2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04Z07:00", "2006-01-02 15:04Z07:00", "2006-01-02T15:04", "2006-01-02 15:04",
	"2006-01-02",
}

// parseTS は data.py の parse_ts(datetime.fromisoformat。時差が無ければ UTC)。読めなければ ok=false。
func parseTS(x any) (time.Time, bool) {
	if !truthy(x) {
		return time.Time{}, false
	}
	s := pyStr(x)
	if strings.HasSuffix(s, "Z") {
		s = strings.TrimSuffix(s, "Z") + "+00:00"
	}
	for _, l := range tsLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ---- ログ(1 行 1 JSON)

// tailLines は data.py の tail_lines: 末尾 maxbytes だけを読み、最後の n 行。
func tailLines(path string, n int) []string {
	const maxbytes = 262144
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	size := st.Size()
	off := size - maxbytes
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	lines := pySplitlines(strings.ToValidUTF8(string(data), "�"))
	if size > maxbytes && len(lines) > 0 {
		lines = lines[1:] // 途中から始まる行は捨てる
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// parseJSONL は 1 行ずつ JSON の object だけを読む。
func parseJSONL(lines []string) []map[string]any {
	var out []map[string]any
	for _, ln := range lines {
		if m, ok := decodeObject(ln); ok {
			out = append(out, m)
		}
	}
	return out
}

// decodeObject は json.loads(s) が dict になるときの値(数は json.Number のまま)。
func decodeObject(s string) (map[string]any, bool) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var x any
	if d.Decode(&x) != nil {
		return nil, false
	}
	if _, err := d.Token(); err != io.EOF { // 後ろに余分なものがある
		return nil, false
	}
	m, ok := x.(map[string]any)
	return m, ok
}

// ---- 設定(config 表)。data.py の config_text と同じ順: 環境変数のファイル > DB のこのセッション > DB の共通(*) > checkout の config > .example

const configMax = 262144

var configKeys = map[string]struct {
	envs []string
	name string
}{
	"projects": {[]string{"TB_CONFIG"}, "projects.conf"},
	"policy":   {[]string{"WEBUI_POLICY", "TB_POLICY"}, "policy.conf"},
	"protect":  {[]string{"TB_PROTECT"}, "protect.conf"},
	"labels":   {[]string{"TB_LABELS"}, "labels.conf"},
	"statuses": {[]string{"TB_STATUSES"}, "statuses.conf"},
}

func readText(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4*(configMax+1)))
	if err != nil {
		return "", false
	}
	s := strings.ToValidUTF8(string(b), "�")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	if utf8.RuneCountInString(s) > configMax+1 {
		s = string([]rune(s)[:configMax+1])
	}
	return s, true
}

// configText は (本文, 出どころ, あるか)。
func (v *sv) configText(key string) (string, string, bool) {
	k := configKeys[key]
	for _, ev := range k.envs {
		if p := os.Getenv(ev); p != "" {
			s, ok := readText(p)
			return s, "file:" + p, ok
		}
	}
	rows, _ := v.qChecked("SELECT herdr_session AS s, substr(body, 1, ?) AS body FROM main.config WHERE key=? AND herdr_session IN (?, '*') "+
		"ORDER BY herdr_session='*' LIMIT 1", configMax+1, key, v.sess)
	if len(rows) > 0 {
		b := rows[0]["body"]
		return pyStr(b), "db:" + pyStr(rows[0]["s"]), b != nil
	}
	cdir := os.Getenv("TB_CONFIG_DIR")
	if cdir == "" {
		cdir = filepath.Join(HerdrRoot(), "taskboard", "config")
	}
	cands := [][2]string{{filepath.Join(cdir, k.name), "file:"}}
	if key != "policy" { // .example を既定値にしないキー(tb の CFG_EXAMPLE=0 と同じ)
		cands = append(cands, [2]string{filepath.Join(cdir, k.name+".example"), "example:"})
	}
	for _, c := range cands {
		if s, ok := readText(c[0]); ok {
			return s, c[1] + c[0], true
		}
	}
	return "", "none", false
}

// ---- 状態(設定 statuses)。data.py の statuses / stq

var statusCategories = []string{"todo", "active", "review", "blocked", "done", "cancelled"}

var statusBuiltin = [][2]string{{"todo", "todo"}, {"in_progress", "active"}, {"review", "review"},
	{"done", "done"}, {"blocked", "blocked"}, {"cancelled", "cancelled"}}

var reStatusBad = regexp.MustCompile("[,\"'`\\\\*?\\[\\]]")

type statusRow struct{ name, cat, desc string }

type statusSet struct {
	rows []statusRow
	cat  map[string]string
}

func builtinCat(k string) (string, bool) {
	for _, b := range statusBuiltin {
		if b[0] == k {
			return b[1], true
		}
	}
	return "", false
}

func isCategory(c string) bool {
	for _, x := range statusCategories {
		if x == c {
			return true
		}
	}
	return false
}

// parseStatuses は本文 → 状態(書いた順)。不正なら nil。
func parseStatuses(text string) []statusRow {
	var out []statusRow
	seen := map[string]bool{}
	for _, line := range pySplitlines(text) {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimFunc(line, pyIsSpace)
		if line == "" {
			continue
		}
		line, desc, _ := strings.Cut(line, "=")
		parts := strings.FieldsFunc(line, pyIsSpace)
		if len(parts) != 2 {
			return nil
		}
		k, c := parts[0], parts[1]
		if utf8.RuneCountInString(k) > 30 || reStatusBad.MatchString(k) || !isCategory(c) || seen[k] {
			return nil
		}
		if bc, ok := builtinCat(k); ok && bc != c {
			return nil
		}
		seen[k] = true
		out = append(out, statusRow{k, c, strings.TrimFunc(desc, pyIsSpace)})
	}
	cats := map[string]bool{}
	for _, r := range out {
		cats[r.cat] = true
	}
	if len(out) == 0 || !cats["todo"] || !cats["active"] || !cats["done"] {
		return nil
	}
	return out
}

func (v *sv) statuses() *statusSet {
	if v.st != nil {
		return v.st
	}
	var rows []statusRow
	if text, _, ok := v.configText("statuses"); ok {
		rows = parseStatuses(text)
	}
	if rows == nil {
		for _, b := range statusBuiltin {
			rows = append(rows, statusRow{b[0], b[1], ""})
		}
	}
	cat := map[string]string{}
	for _, r := range rows {
		cat[r.name] = r.cat
	}
	for _, b := range statusBuiltin {
		if _, ok := cat[b[0]]; !ok {
			cat[b[0]] = b[1]
		}
	}
	v.st = &statusSet{rows, cat}
	return v.st
}

// stNames はその分類の状態(設定した順の後に組み込みの名前)。
func (v *sv) stNames(cats ...string) []string {
	st := v.statuses()
	want := map[string]bool{}
	for _, c := range cats {
		want[c] = true
	}
	var out []string
	seen := map[string]bool{}
	names := []string{}
	for _, r := range st.rows {
		names = append(names, r.name)
	}
	for _, b := range statusBuiltin {
		names = append(names, b[0])
	}
	for _, k := range names {
		if !seen[k] && want[st.cat[k]] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func (v *sv) stSQL(cats ...string) string {
	var q []string
	for _, k := range v.stNames(cats...) {
		q = append(q, store.Quote(k))
	}
	if len(q) == 0 {
		return "SELECT NULL WHERE 0"
	}
	return strings.Join(q, ",")
}

var (
	statusGroups = map[string][]string{"CLOSED": {"done", "cancelled"}, "HELD": {"active", "review", "blocked"}}
	reStToken    = regexp.MustCompile(`__([A-Z]+)_STATUSES__`)
)

// stq は SQL の中の __CLOSED_STATUSES__ などを埋める。知らない置き場があれば ok=false。
func (v *sv) stq(q string) (string, bool) {
	ok := true
	out := reStToken.ReplaceAllStringFunc(q, func(m string) string {
		g := reStToken.FindStringSubmatch(m)[1]
		cats, found := statusGroups[g]
		if !found {
			if isCategory(strings.ToLower(g)) {
				cats = []string{strings.ToLower(g)}
			} else {
				ok = false
				return m
			}
		}
		return v.stSQL(cats...)
	})
	return out, ok
}

// ---- 定期実行のログ

var reJob = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// SchedLogs は WEBUI_SCHED_LOGS(既定 scheduler/logs)。
func SchedLogs() string {
	return cfg("WEBUI_SCHED_LOGS", filepath.Join(HerdrRoot(), "scheduler", "logs"))
}

type failedJob struct {
	name string
	rec  map[string]any
}

// failedJobs は最後の実行(ran/failed/sent)が failed のジョブ。
func failedJobs() []failedJob {
	ents, err := os.ReadDir(SchedLogs())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, ".log") && reJob.MatchString(n[:len(n)-4]) {
			names = append(names, n[:len(n)-4])
		}
	}
	sort.Strings(names)
	var out []failedJob
	for _, n := range names {
		var last map[string]any
		for _, r := range parseJSONL(tailLines(filepath.Join(SchedLogs(), n+".log"), 200)) {
			switch r["action"] {
			case "ran", "failed", "sent":
				last = r
			}
		}
		if last != nil && last["action"] == "failed" {
			out = append(out, failedJob{n, last})
		}
	}
	return out
}

// pyParseQS は urllib.parse.parse_qs(空の値は落とす。+ は空白)。
func pyParseQS(query string) map[string][]string {
	out := map[string][]string{}
	for _, kv := range strings.Split(query, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		k, v = pyUnquotePlus(k), pyUnquotePlus(v)
		if v == "" {
			continue
		}
		out[k] = append(out[k], v)
	}
	return out
}

// pyUnquote は urllib.parse.unquote(+ はそのまま)。
func pyUnquote(s string) string {
	return pyUnquotePlus(strings.ReplaceAll(s, "+", "%2B"))
}

// pyUnquotePlus は urllib.parse.unquote_plus(壊れた %xx はそのまま。UTF-8 でないバイトは U+FFFD)。
func pyUnquotePlus(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	if !strings.Contains(s, "%") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b = append(b, byte(v))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	return pyDecodeUTF8(b)
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// pyURLEncode は urllib.parse.urlencode(並びは渡した順。quote_plus で、空白は +)。
func pyURLEncode(kv [][2]string) string {
	var parts []string
	for _, p := range kv {
		parts = append(parts, strings.ReplaceAll(pyQuote(p[0], " "), " ", "+")+"="+strings.ReplaceAll(pyQuote(p[1], " "), " ", "+"))
	}
	return strings.Join(parts, "&")
}
