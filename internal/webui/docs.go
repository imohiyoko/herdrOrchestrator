package webui

import (
	"encoding/json"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// docs.go は docs.py: agent が報告した成果物(ローカルのファイル)を見つけて、安全に読む。
// 見せてよいのは tasks.db に登録された成果物だけ(パスを URL で受け取らない)。
//   - C-<報告id>/<n>: comments.deliverables の n 番目
//   - task:<タスクid>/<n>: tasks.deliverables の n 番目
//   - A-<アラートid>: alerts.result_path(解析レポート。ファイルが無くても DB に保存された本文を出す)
//
// パスを直接指定して開くこともできる(openPath)。その場合は agent の作業ディレクトリと WEBUI_DOC_ROOTS の配下だけ。

const (
	docMaxBytes = 1024 * 1024 // これより大きいファイルは先頭だけ出す
	docMaxList  = 500         // ディレクトリの一覧に出す件数
)

var (
	docMDExt = []string{".md", ".markdown", ".mdown", ".mkd"}
	reDocURL = regexp.MustCompile(`(?i)^https?://[^` + pySpaceCls + `<>"']+$`)
	reDocRef = regexp.MustCompile(`^(?:(C)-([0-9]{1,9})/([0-9]{1,4})|(task):([^/]{1,200})/([0-9]{1,4})|(A)-([0-9]{1,9}))$`)
)

// docEntry は docs.lookup / recent_docs の 1 件。path が "" は None(場所を決められない)。
type docEntry struct {
	ref, raw, path string
	report         any // alerts.result_report(None か本文)
	task, from, ts any
	kind           string
	title          any
	hasTitle       bool
}

func (d docEntry) hasReport() bool { return truthy(d.report) }

// jsonStrList は docs._json_list: JSON の配列なら各要素を str() したもの。
func jsonStrList(x any) []string {
	var out []string
	for _, v := range safeJSONList(x) {
		out = append(out, pyJSONValStr(v))
	}
	return out
}

// pyJSONValStr は json.loads した値の str()。
func pyJSONValStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		if i, err := strconv.ParseInt(t.String(), 10, 64); err == nil && !strings.ContainsAny(t.String(), ".eE") {
			return strconv.FormatInt(i, 10)
		}
		if strings.ContainsAny(t.String(), ".eE") {
			f, _ := strconv.ParseFloat(t.String(), 64)
			return pyFloatRepr(f)
		}
		return t.String()
	}
	return pyJSONRepr(v)
}

// pyJSONRepr は json.loads した値の repr()。
func pyJSONRepr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		return pyStr(t)
	case string:
		return pyStrRepr(t)
	case json.Number:
		return pyJSONValStr(t)
	case []any:
		ps := make([]string, len(t))
		for i, x := range t {
			ps[i] = pyJSONRepr(x)
		}
		return "[" + strings.Join(ps, ", ") + "]"
	case map[string]any:
		// json.loads の dict は出てきた順。Go の map は順を持たないので、ここだけは並びが違いうる(登録時に文字列に限っている)
		ks := sortedKeysAny(t)
		ps := make([]string, len(ks))
		for i, k := range ks {
			ps[i] = pyStrRepr(k) + ": " + pyJSONRepr(t[k])
		}
		return "{" + strings.Join(ps, ", ") + "}"
	}
	return pyStr(v)
}

func sortedKeysAny(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// pyFloatRepr は float の repr(-4 <= 指数 < 16 は小数で、ほかは指数表記)。
func pyFloatRepr(f float64) string {
	e := strconv.FormatFloat(f, 'e', -1, 64)
	_, ex, _ := strings.Cut(e, "e")
	if n, err := strconv.Atoi(ex); err == nil && n >= -4 && n < 16 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	return e
}

// pyStrRepr は str の repr(引用符の選び方と、印字できない文字の逃がし方)。
func pyStrRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	var b strings.Builder
	b.WriteString(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == q:
			b.WriteString(`\` + q)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\x` + hex2(int(r)))
		case !unicode.IsPrint(r) && r != ' ':
			switch {
			case r <= 0xff:
				b.WriteString(`\x` + hex2(int(r)))
			case r <= 0xffff:
				b.WriteString(`\u` + leftPad(strconv.FormatInt(int64(r), 16), 4))
			default:
				b.WriteString(`\U` + leftPad(strconv.FormatInt(int64(r), 16), 8))
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(q)
	return b.String()
}

func hex2(i int) string { return leftPad(strconv.FormatInt(int64(i), 16), 2) }

func leftPad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

func (v *sv) cwdFor(session any) string {
	if !truthy(session) {
		return ""
	}
	r := v.q("SELECT cwd FROM agent_activity WHERE session_id=?", session)
	if len(r) == 0 || !truthy(r[0]["cwd"]) {
		return ""
	}
	return pyStr(r[0]["cwd"])
}

// isDocURL は docs.is_url。
func isDocURL(raw string) bool { return reDocURL.MatchString(pyStrip(raw)) }

// pyExpandUser は os.path.expanduser(posix)。
func pyExpandUser(s string) string {
	if !strings.HasPrefix(s, "~") {
		return s
	}
	i := strings.Index(s, "/")
	if i < 0 {
		i = len(s)
	}
	var home string
	if i == 1 {
		h, ok := os.LookupEnv("HOME")
		if !ok {
			u, err := user.Current()
			if err != nil {
				return s
			}
			h = u.HomeDir
		}
		home = h
	} else {
		u, err := user.Lookup(s[1:i])
		if err != nil {
			return s
		}
		home = u.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if home == "" {
		home = "/"
		if i < len(s) {
			home = ""
		}
	}
	return home + s[i:]
}

var filepathIsPosix = os.PathSeparator == '/'

func pyIsAbs(p string) bool {
	if filepathIsPosix {
		return strings.HasPrefix(p, "/")
	}
	return filepath.IsAbs(p)
}

// pyJoin は os.path.join(posix、2 つ)。
func pyJoin(a, b string) string {
	if strings.HasPrefix(b, "/") {
		return b
	}
	if a == "" || strings.HasSuffix(a, "/") {
		return a + b
	}
	return a + "/" + b
}

// resolveDocPath は docs.resolve_path: 登録された文字列 → 絶対パス(実体)。決められないときは ""。
func resolveDocPath(raw any, base string) string {
	s := ""
	if truthy(raw) {
		s = pyStrip(pyStr(raw))
	}
	if s == "" || isDocURL(s) || strings.Contains(s, "\x00") {
		return ""
	}
	s = strings.TrimPrefix(s, "file://")
	s = pyExpandUser(s)
	if !pyIsAbs(s) {
		if base == "" || !pyIsAbs(base) {
			return ""
		}
		s = pyJoin(base, s)
	}
	return pyRealpath(s)
}

// lookupDoc は docs.lookup。
func (v *sv) lookupDoc(ref string) (docEntry, bool) {
	m := reDocRef.FindStringSubmatch(ref)
	if m == nil {
		return docEntry{}, false
	}
	switch {
	case m[1] != "":
		id, _ := strconv.Atoi(m[2])
		rows := v.q("SELECT id,task_id,kind,deliverables,from_name,from_session,created_at FROM comments WHERE id=?", id)
		if len(rows) == 0 {
			return docEntry{}, false
		}
		c := rows[0]
		i, _ := strconv.Atoi(m[3])
		dl := jsonStrList(c["deliverables"])
		if i >= len(dl) {
			return docEntry{}, false
		}
		base := v.cwdFor(c["from_session"])
		return docEntry{ref: ref, raw: dl[i], path: resolveDocPath(dl[i], base), task: c["task_id"], from: c["from_name"],
			ts: c["created_at"], kind: pyStr(c["kind"])}, true
	case m[4] != "":
		rows := v.q("SELECT id,owner_agent,session_id,deliverables,updated_at FROM tasks WHERE id=?", m[5])
		if len(rows) == 0 {
			return docEntry{}, false
		}
		t := rows[0]
		i, _ := strconv.Atoi(m[6])
		dl := jsonStrList(t["deliverables"])
		if i >= len(dl) {
			return docEntry{}, false
		}
		base := v.cwdFor(t["session_id"])
		return docEntry{ref: ref, raw: dl[i], path: resolveDocPath(dl[i], base), task: t["id"], from: t["owner_agent"],
			ts: t["updated_at"], kind: "task"}, true
	}
	id, _ := strconv.Atoi(m[8])
	rows := v.q("SELECT id,title,result_path,result_report,result_by,analyzed_at FROM alerts WHERE id=?", id)
	if len(rows) == 0 || !(truthy(rows[0]["result_path"]) || truthy(rows[0]["result_report"])) {
		return docEntry{}, false
	}
	a := rows[0]
	raw := ""
	if truthy(a["result_path"]) {
		raw = pyStr(a["result_path"])
	}
	return docEntry{ref: ref, raw: raw, path: resolveDocPath(a["result_path"], ""), report: a["result_report"], from: a["result_by"],
		ts: a["analyzed_at"], kind: "alert", title: a["title"], hasTitle: true}, true
}

// docSubPath は docs.sub_path: ディレクトリの成果物の中のファイル。外へ出る指定(.. やリンク)は ""。
func docSubPath(root, sub string) string {
	if sub == "" {
		return root
	}
	if strings.HasPrefix(sub, "/") || strings.Contains(sub, "\x00") {
		return ""
	}
	p := pyRealpath(pyJoin(root, sub))
	if strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return p
	}
	return ""
}

// readDocText は docs.read_text: (kind, text, truncated, size)。kind: file / dir / missing / binary / denied / error。
func readDocText(path string) (string, string, bool, int64) {
	if why := denied(path); why != "" {
		return "denied", why, false, 0
	}
	st, err := os.Stat(path)
	if err != nil {
		return "missing", "", false, 0
	}
	if st.IsDir() {
		return "dir", "", false, 0
	}
	if !st.Mode().IsRegular() {
		return "error", "通常のファイルではありません", false, 0
	}
	f, err := os.Open(path)
	if err != nil {
		return "error", "読めませんでした(権限など)", false, st.Size()
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, docMaxBytes+1))
	if err != nil {
		return "error", "読めませんでした(権限など)", false, st.Size()
	}
	if strings.Contains(string(data[:min(len(data), 8192)]), "\x00") {
		return "binary", "", false, st.Size()
	}
	trunc := len(data) > docMaxBytes
	return "file", pyDecodeUTF8(data[:min(len(data), docMaxBytes)]), trunc, st.Size()
}

// pyDecodeUTF8 は bytes.decode("utf-8", "replace")(壊れた並びは、最長の正しい前置きごとに U+FFFD 1 つ)。
func pyDecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		need, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c == 0xE0:
			need, lo = 2, 0xA0
		case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
			need = 2
		case c == 0xED:
			need, hi = 2, 0x9F
		case c == 0xF0:
			need, lo = 3, 0x90
		case c >= 0xF1 && c <= 0xF3:
			need = 3
		case c == 0xF4:
			need, hi = 3, 0x8F
		default:
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		k := 1
		for ; k <= need; k++ {
			if i+k >= len(b) {
				break
			}
			x := b[i+k]
			l, h := byte(0x80), byte(0xBF)
			if k == 1 {
				l, h = lo, hi
			}
			if x < l || x > h {
				break
			}
		}
		if k > need {
			out.Write(b[i : i+need+1])
		} else {
			out.WriteRune(utf8.RuneError)
		}
		i += min(k, need+1)
	}
	return out.String()
}

// listDocDir は docs.list_dir: [相対名]。隠しファイルと、外を指すリンクは出さない。
func listDocDir(path string) ([]string, bool) {
	var out []string
	var walk func(dir string) bool
	walk = func(dir string) bool {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		var dirs, files []string
		for _, e := range ents {
			full := filepath.Join(dir, e.Name())
			isDir := e.IsDir()
			if e.Type()&os.ModeSymlink != 0 {
				st, err := os.Stat(full)
				isDir = err == nil && st.IsDir()
			}
			if isDir {
				if !strings.HasPrefix(e.Name(), ".") && e.Name() != "node_modules" && e.Name() != "__pycache__" &&
					e.Type()&os.ModeSymlink == 0 {
					dirs = append(dirs, e.Name())
				}
				continue
			}
			files = append(files, e.Name())
		}
		sort.Strings(dirs)
		sort.Strings(files)
		for _, fn := range files {
			if strings.HasPrefix(fn, ".") {
				continue
			}
			rel, _ := filepath.Rel(path, filepath.Join(dir, fn))
			if docSubPath(path, rel) == "" {
				continue
			}
			out = append(out, rel)
			if len(out) >= docMaxList {
				return true
			}
		}
		for _, d := range dirs {
			if walk(filepath.Join(dir, d)) {
				return true
			}
		}
		return false
	}
	more := walk(path)
	return out, more
}

func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFilePath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// existsLabel は docs.exists_label。
func existsLabel(d docEntry) string {
	if isDocURL(d.raw) {
		return "URL"
	}
	if d.path == "" {
		if d.hasReport() {
			return "DB に本文"
		}
		return "場所不明"
	}
	if denied(d.path) != "" {
		return "非表示"
	}
	if isDirPath(d.path) {
		return "フォルダ"
	}
	if isFilePath(d.path) {
		return "あり"
	}
	if d.hasReport() {
		return "DB に本文"
	}
	return "なし"
}

// pyKey は Python の値を dict のキーとして比べるための文字列(型ごとに分ける)。
func pyKey(x any) string {
	switch t := x.(type) {
	case nil:
		return "n:"
	case string:
		return "s:" + t
	case []byte:
		return "b:" + string(t)
	}
	return "v:" + pyStr(x)
}

func orEmpty(x any) string {
	if truthy(x) {
		return pyStr(x)
	}
	return ""
}

// recentDocs は docs.recent_docs: 一覧用。新しい順。同じ (タスク, 文字列) は最新の 1 件にまとめる。
func (v *sv) recentDocs(limit int) []docEntry {
	var items []docEntry
	seen := map[string]bool{}
	add := func(rows []row, taskCol, prefix, sessCol, fromCol, tsCol, kindCol, kind string) {
		for _, c := range rows {
			base, got := "", false
			for i, raw := range jsonStrList(c["deliverables"]) {
				key := pyKey(c[taskCol]) + "\x00" + pyStrip(raw)
				if seen[key] {
					continue
				}
				seen[key] = true
				if !got {
					base, got = v.cwdFor(c[sessCol]), true
				}
				k := kind
				if kindCol != "" {
					k = pyStr(c[kindCol])
				}
				items = append(items, docEntry{ref: prefix + pyStr(c["id"]) + "/" + strconv.Itoa(i), raw: raw, path: resolveDocPath(raw, base),
					task: c[taskCol], from: c[fromCol], ts: orEmpty(c[tsCol]), kind: k})
			}
		}
	}
	add(v.q("SELECT id,task_id,kind,deliverables,from_name,from_session,created_at FROM comments "+
		"WHERE deliverables IS NOT NULL AND deliverables NOT IN ('','[]') ORDER BY id DESC LIMIT 300"),
		"task_id", "C-", "from_session", "from_name", "created_at", "kind", "")
	add(v.q("SELECT id,owner_agent,session_id,deliverables,updated_at FROM tasks "+
		"WHERE deliverables IS NOT NULL AND deliverables NOT IN ('','[]') ORDER BY updated_at DESC LIMIT 300"),
		"id", "task:", "session_id", "owner_agent", "updated_at", "", "task")
	for _, a := range v.q("SELECT id,title,result_path,result_report,result_by,analyzed_at FROM alerts " +
		"WHERE result_path IS NOT NULL OR result_report IS NOT NULL ORDER BY id DESC LIMIT 100") {
		items = append(items, docEntry{ref: "A-" + pyStr(a["id"]), raw: orEmpty(a["result_path"]), path: resolveDocPath(a["result_path"], ""),
			report: a["result_report"], from: a["result_by"], ts: orEmpty(a["analyzed_at"]), kind: "alert", title: a["title"], hasTitle: true})
	}
	sort.SliceStable(items, func(i, j int) bool { return pyStr(items[i].ts) > pyStr(items[j].ts) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

type docRef struct{ ref, raw string }

func commentRefs(cid any, deliverables any) []docRef {
	var out []docRef
	for i, raw := range jsonStrList(deliverables) {
		out = append(out, docRef{"C-" + pyStr(cid) + "/" + strconv.Itoa(i), raw})
	}
	return out
}

func taskRefs(tid any, deliverables any) []docRef {
	var out []docRef
	for i, raw := range jsonStrList(deliverables) {
		out = append(out, docRef{"task:" + pyStr(tid) + "/" + strconv.Itoa(i), raw})
	}
	return out
}

// allowedRoots は docs.allowed_roots: パス指定で開いてよい範囲(実体の絶対パス)。agent の作業ディレクトリと WEBUI_DOC_ROOTS(: 区切り)。
func (v *sv) allowedRoots() []string {
	var cands []string
	for _, r := range v.q("SELECT DISTINCT cwd FROM agent_activity WHERE cwd IS NOT NULL AND cwd != ''") {
		cands = append(cands, pyStr(r["cwd"]))
	}
	for _, x := range strings.Split(cfg("WEBUI_DOC_ROOTS", ""), string(os.PathListSeparator)) {
		if x != "" {
			cands = append(cands, x)
		}
	}
	broad := map[string]bool{pyRealpath(pyExpandUser("~")): true, "/": true}
	for _, b := range []string{"/Users", "/home", "/tmp", "/private/tmp"} {
		broad[pyRealpath(b)] = true
	}
	var out []string
	for _, c := range cands {
		c = pyExpandUser(c)
		if !pyIsAbs(c) {
			continue
		}
		r := pyRealpath(c)
		if broad[r] || contains(out, r) || !isDirPath(r) {
			continue
		}
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// openDocPath は docs.open_path: パス指定 → (実体の絶対パス, "") か ("", 理由)。
func (v *sv) openDocPath(raw string) (string, string) {
	s := strings.TrimPrefix(pyStrip(raw), "file://")
	s = pyExpandUser(s)
	if s == "" || strings.Contains(s, "\x00") || !pyIsAbs(s) {
		return "", "絶対パス(/ か ~ で始まる)を指定してください"
	}
	p := pyRealpath(s)
	for _, r := range v.allowedRoots() {
		if p == r || strings.HasPrefix(p, r+string(os.PathSeparator)) {
			return p, ""
		}
	}
	return "", "agent の作業ディレクトリの外なので開けません"
}
