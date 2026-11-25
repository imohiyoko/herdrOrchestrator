package webui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// docspage.go はドキュメント(/docs、/docs/view)とタスクの詳細(/tasks/<id>)。読み取りのみ(views.page_docs / page_doc / page_task)。

func docHref(ref, sub string) string {
	kv := [][2]string{{"ref", ref}}
	if sub != "" {
		kv = append(kv, [2]string{"sub", sub})
	}
	return "/docs/view?" + pyURLEncode(kv)
}

// docLink は成果物の文字列 → Web UI で開くリンク(URL ならそのまま外部リンク)。
func docLink(ref, raw string) string {
	if isDocURL(raw) {
		return `<a href="` + E(pyStrip(raw)) + `" rel="noopener noreferrer" target=_blank>` + E(raw) + `</a>`
	}
	return `<a href="` + E(docHref(ref, "")) + `">` + E(raw) + `</a>`
}

func docLinks(refs []docRef) string {
	ls := make([]string, len(refs))
	for i, r := range refs {
		ls[i] = docLink(r.ref, r.raw)
	}
	return strings.Join(ls, "<br>")
}

// docName は views._doc_name。
func docName(raw string) string {
	s := strings.TrimRight(raw, "/")
	if b := pyBasename(s); b != "" {
		return b
	}
	return s
}

// pyLower は str.lower(İ と語末のΣも Python と同じにする)。
func pyLower(s string) string {
	if !strings.ContainsAny(s, "İΣ") {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case 'İ':
			b.WriteString("i̇")
		case 'Σ':
			if finalSigma(rs, i) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isCased(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }

func isCaseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) || strings.ContainsRune("'.:·’‘­", r)
}

func finalSigma(rs []rune, i int) bool {
	j := i - 1
	for j >= 0 && isCaseIgnorable(rs[j]) {
		j--
	}
	if j < 0 || !isCased(rs[j]) {
		return false
	}
	j = i + 1
	for j < len(rs) && isCaseIgnorable(rs[j]) {
		j++
	}
	return j == len(rs) || !isCased(rs[j])
}

// pySplitExt は os.path.splitext(path)[1](先頭の . だけの名前には拡張子なし)。
func pySplitExt(p string) string {
	sep := strings.LastIndex(p, "/")
	dot := strings.LastIndex(p, ".")
	if dot > sep {
		for k := sep + 1; k < dot; k++ {
			if p[k] != '.' {
				return p[dot:]
			}
		}
	}
	return ""
}

func (s *Server) pageDocs(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true // 絞り込みの入力欄があるので自動更新しない
	v := q.data()
	items := v.recentDocs(200)
	kw := ""
	if x := c.qs["q"]; len(x) > 0 {
		kw = runesN(pyStrip(Clean(x[0])), kwMax)
	}
	if kw != "" {
		low := pyLower(kw)
		var keep []docEntry
		for _, it := range items {
			title := ""
			if it.hasTitle {
				title = orEmpty(it.title)
			}
			if strings.Contains(pyLower(strings.Join([]string{it.raw, orEmpty(it.task), orEmpty(it.from), title}, " ")), low) {
				keep = append(keep, it)
			}
		}
		items = keep
	}
	var roots []string
	for _, r := range v.allowedRoots() {
		roots = append(roots, "<code>"+E(r)+"</code>")
	}
	rootsS := strings.Join(roots, ", ")
	if rootsS == "" {
		rootsS = "(なし)"
	}
	body := "<p>agent が報告(<code>tb comment --deliverables</code>・タスクの成果物・アラートの解析レポート)で" +
		"「ここにまとめました」と示したファイルを、新しい順に並べています。名前を押すとこの画面で読めます" +
		"(Markdown は整形して表示)。</p>" +
		`<form method=get action="/docs"><input type=search name=q value="` + E(kw) + `" placeholder="パス・タスク・agent で絞り込み" size=36> ` +
		"<button type=submit>絞り込み</button></form>" +
		"<h2>パスを指定して開く</h2>" + openForm("") +
		"<p class=note>開ける範囲: " + rootsS +
		"(agent の作業ディレクトリと <code>WEBUI_DOC_ROOTS</code>。ホームや / そのものは対象外)</p><h2>報告されたドキュメント</h2>"
	var rows [][]string
	for _, it := range items {
		name := docName(it.raw)
		if name == "" {
			name = "-"
			if it.kind == "alert" {
				name = "A-" + it.ref[2:] + " の解析レポート"
			}
		}
		link := `<a href="` + E(docHref(it.ref, "")) + `">` + E(name) + `</a>`
		if isDocURL(it.raw) {
			link = docLink(it.ref, it.raw)
		}
		ctxCell := ""
		if truthy(it.task) {
			ctxCell = tlink(it.task)
		} else if it.hasTitle {
			ctxCell = E(orEmpty(it.title))
		}
		rows = append(rows, []string{ev(it.ts), link, `<span class=note>` + E(it.raw) + `</span>`, ctxCell, ev(it.from),
			E(it.kind), E(existsLabel(it))})
	}
	body += table([]string{"時刻", "ドキュメント", "パス", "タスク / アラート", "from", "種別", "状態"}, rows)
	body += "<p class=note>一覧に出るのは tasks.db に成果物として登録されたものです。相対パスは報告した agent の作業ディレクトリが基準です。" +
		"鍵・.env などの認証情報らしいものは表示しません。</p>"
	return "ドキュメント", body
}

func openForm(value string) string {
	return `<form method=get action="/docs/view"><input type=text name=path value="` + E(value) + `" ` +
		`placeholder="/絶対パス/まとめ.md(agent の作業ディレクトリ配下)" size=60> <button type=submit>開く</button></form>`
}

func first(qs map[string][]string, k string) string {
	if x := qs[k]; len(x) > 0 {
		return x[0]
	}
	return ""
}

type kvs = [][2]string

func withKV(base kvs, k, val string) kvs {
	out := append(kvs{}, base...)
	for i, x := range out {
		if x[0] == k {
			out[i][1] = val
			return out
		}
	}
	return append(out, [2]string{k, val})
}

// pageDoc は views.page_doc。見つからないときは ok=false(404)。
func (s *Server) pageDoc(q *rq, c pageCtx) (string, string, bool) {
	c.opts.norefresh = true // 読んでいる途中で再読み込みしない
	v := q.data()
	ref, given, sub := first(c.qs, "ref"), first(c.qs, "path"), first(c.qs, "sub")
	rawMode := first(c.qs, "raw") == "1"
	hd := `<p><a href="/docs">← ドキュメント一覧</a></p>`
	var ent docEntry
	var baseQ kvs
	var title string
	var meta [][2]string
	if given != "" && ref == "" { // パスを直接指定(agent の作業ディレクトリ配下だけ)
		p, why := v.openDocPath(given)
		if p == "" {
			return "ドキュメント", hd + errHTML(why) + openForm(given), true
		}
		baseQ = kvs{{"path", given}}
		ent = docEntry{raw: given, path: p}
		title = docName(given)
		meta = [][2]string{{"指定したパス", E(given)}, {"場所", E(p)}}
	} else {
		var ok bool
		if ent, ok = v.lookupDoc(ref); !ok {
			return "", "", false
		}
		baseQ = kvs{{"ref", ref}}
		title = docName(ent.raw)
		if title == "" {
			title = ref + " の解析レポート"
		}
		place := "-"
		if ent.path != "" {
			place = E(ent.path)
		}
		taskCell := ""
		if truthy(ent.task) {
			taskCell = tlink(ent.task)
		} else if ent.hasTitle && truthy(ent.title) {
			taskCell = ev(ent.title)
		} else {
			taskCell = "-"
		}
		meta = [][2]string{{"登録された値", orStr(E(ent.raw), "-")}, {"場所", place}, {"タスク", taskCell},
			{"from", ev(ent.from)}, {"時刻", ev(ent.ts)}, {"参照", E(ref)}}
		if isDocURL(ent.raw) {
			return title, hd + `<p>URL です: ` + docLink(ref, ent.raw) + `</p>`, true
		}
	}
	path := ent.path
	if path != "" && sub != "" {
		p2 := docSubPath(path, sub)
		if p2 == "" || !isDirPath(path) {
			return "", "", false
		}
		path, title = p2, sub
		meta = append(meta[:2], append([][2]string{{"フォルダ内", E(sub)}}, meta[2:]...)...)
		baseQ = withKV(baseQ, "sub", sub)
	}
	var info strings.Builder
	info.WriteString("<table>")
	for _, m := range meta {
		info.WriteString("<tr><th>" + m[0] + "</th><td>" + m[1] + "</td></tr>")
	}
	info.WriteString("</table>")
	if path == "" {
		if ent.hasReport() {
			return title, hd + info.String() + "<h2>解析レポート(DB に保存された本文)</h2>" + docBody(pyStr(ent.report), ".md", rawMode, baseQ, c), true
		}
		return title, hd + info.String() + errHTML("ファイルの場所を決められません(相対パスで、報告した agent の作業ディレクトリが分からない)"), true
	}
	kind, text, trunc, size := readDocText(path)
	switch kind {
	case "dir":
		files, more := listDocDir(path)
		var top kvs
		for _, x := range baseQ {
			if x[0] != "sub" {
				top = append(top, x)
			}
		}
		var rows [][]string
		for _, f := range files {
			rel, _ := filepath.Rel(ent.path, filepath.Join(path, f))
			rows = append(rows, []string{`<a href="/docs/view?` + E(pyURLEncode(withKV(top, "sub", rel))) + `">` + E(f) + `</a>`})
		}
		return title, hd + info.String() + "<h2>フォルダの中身</h2>" + table([]string{"ファイル"}, rows) +
			ifs(more, fmt.Sprintf("<p class=note>先頭 %d 件だけ表示しています</p>", docMaxList)), true
	case "missing":
		if ent.hasReport() {
			return title, hd + info.String() + "<p class=note>ファイルは見つかりませんでした。DB に保存された本文を表示します。</p>" +
				docBody(pyStr(ent.report), ".md", rawMode, baseQ, c), true
		}
		return title, hd + info.String() + errHTML("ファイルが見つかりません(移動・削除された可能性があります)"), true
	case "binary":
		return title, hd + info.String() + errHTML(fmt.Sprintf("テキストではないので表示しません(%d バイト)", size)), true
	case "denied", "error":
		return title, hd + info.String() + errHTML(text), true
	}
	note := ifs(trunc, fmt.Sprintf("<p class=note>大きいので先頭 %d KB だけ表示しています(全体 %d バイト)</p>", docMaxBytes/1024, size))
	return title, hd + info.String() + note + docBody(text, pyLower(pySplitExt(path)), rawMode, baseQ, c), true
}

func docBody(text, ext string, rawMode bool, baseQ kvs, c pageCtx) string {
	if contains(docMDExt, ext) || ext == ".mmd" || ext == ".mermaid" {
		if ext == ".mmd" || ext == ".mermaid" {
			text = "```mermaid\n" + text + "\n```"
		}
		if rawMode {
			return `<p class=note><a href="/docs/view?` + E(pyURLEncode(baseQ)) + `">整形して表示</a></p>` + "<pre>" + E(text) + "</pre>"
		}
		h := renderMD(text)
		if strings.Contains(h, "<pre class=mermaid>") { // 図は同梱の mermaid.js でブラウザが描く(JS が無ければコードのまま)
			c.opts.scripts = []string{"/static/mermaid.min.js", "/docs.js"}
		}
		return `<p class=note><a href="/docs/view?` + E(pyURLEncode(withKV(baseQ, "raw", "1"))) + `">元のテキストを表示</a></p>` +
			`<div class=doc>` + h + `</div>`
	}
	return "<pre>" + E(text) + "</pre>"
}

// refsHTML は外部リンク(Slack・PR など)。http(s) の URL だけをリンクにし、それ以外は文字として出す。
func refsHTML(raw any) string {
	var out []string
	for _, r := range safeJSONList(raw) {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		kind, u := "link", ""
		if truthy(m["kind"]) {
			kind = pyJSONValStr(m["kind"])
		}
		if truthy(m["url"]) {
			u = pyJSONValStr(m["url"])
		}
		if reRefURL.MatchString(u) {
			out = append(out, `<code>`+E(kind)+`</code> <a href="`+E(u)+`" rel="noopener noreferrer" target="_blank">`+E(head(u, titleMax))+`</a>`)
		} else {
			out = append(out, `<code>`+E(kind)+`</code> `+E(head(u, titleMax)))
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, "<br>")
}

const historyMax = 100

// pageTask は views.page_task。無いタスクは ok=false(404)。
func (s *Server) pageTask(q *rq, tid string, c pageCtx) (string, string, bool) {
	v := q.data()
	rows := v.q("SELECT * FROM tasks WHERE id=?", tid)
	if len(rows) == 0 {
		return "", "", false
	}
	t := rows[0]
	type kv struct{ k, v string }
	var info []kv
	for _, k := range []string{"id", "project", "title", "status", "owner_agent", "session_id", "account", "parent_id", "reported_at", "created_at", "updated_at"} {
		info = append(info, kv{k, ev(t[k])})
	}
	if truthy(t["parent_id"]) {
		info[7] = kv{"parent_id", tlink(t["parent_id"])}
	}
	if _, ok := t["labels"]; ok {
		ins := []kv{{"種別", ev(t["type"])}, {"優先度", priorityHTML(t["priority"])},
			{"ラベル", labelsHTML(t["labels"], func(l string) string { return "/tasks?" + pyURLEncode(kvs{{"label", l}}) })}}
		info = append(info[:3], append(ins, info[3:]...)...)
		desc := "-"
		if truthy(t["description"]) {
			desc = `<pre>` + E(head(t["description"], reportMax)) + `</pre>`
		}
		info = append(info, kv{"説明", desc})
	}
	if _, ok := t["refs"]; ok {
		info = append(info, kv{"外部リンク", refsHTML(t["refs"])})
	}
	if _, ok := t["due"]; ok {
		info = append(info, kv{"期日", v.dueHTML(t, todayStr())})
	}
	if _, ok := t["sprint_id"]; ok {
		if t["sprint_id"] != nil {
			n := strconv.FormatInt(intOf(t["sprint_id"]), 10)
			info = append(info, kv{"スプリント", `<a href="/board?sprint=S-` + n + `">S-` + n + `</a>`})
		} else {
			info = append(info, kv{"スプリント", "バックログ"})
		}
		pts := "-"
		if t["points"] != nil {
			pts = ev(t["points"])
		}
		info = append(info, kv{"ポイント", pts})
	}
	info = append(info, kv{"summary", E(head(t["summary"], reportMax))}, kv{"note", E(head(t["note"], reportMax))})
	if sh, ok := v.taskStale(t, nowFn().UTC()); ok {
		info = append(info, kv{"目印", fmt.Sprintf(`<span class=warn>停滞: %s のまま %d 時間更新がありません</span>`, ev(t["status"]), sh)})
	}
	info = append(info, kv{"成果物", orStr(docLinks(taskRefs(t["id"], t["deliverables"])), "-")})
	var b strings.Builder
	b.WriteString(doneNotice(c) + editErr(c) + resolveNotice(c) + "<table>")
	for _, x := range info {
		b.WriteString("<tr><th>" + x.k + "</th><td>" + x.v + "</td></tr>")
	}
	b.WriteString("</table>")
	if f := v.taskStatusForm(pyStr(t["id"]), pyStr(t["status"]), c); f != "" {
		if c.opts != nil {
			c.opts.norefresh = true // 入力欄があるので自動更新しない
		}
		b.WriteString(f)
	}
	if kids := v.q("SELECT id, title, status, owner_agent, updated_at FROM tasks WHERE parent_id=? ORDER BY id", tid); len(kids) > 0 {
		var rs [][]string
		for _, k := range kids {
			rs = append(rs, []string{tlink(k["id"]), E(head(k["title"], titleMax)), ev(k["status"]), ev(k["owner_agent"]), ev(k["updated_at"])})
		}
		b.WriteString("<h2>子タスク</h2>" + table([]string{"タスク", "題", "状態", "担当", "更新"}, rs))
	}
	if _, ok := t["labels"]; ok {
		lk := v.q("SELECT CASE WHEN l.from_id=? THEN l.link_type WHEN l.link_type='blocks' THEN 'blocked by'"+
			" WHEN l.link_type='duplicates' THEN 'duplicated by' ELSE 'relates' END AS rel,"+
			" x.id, x.title, x.status FROM task_links l JOIN tasks x ON x.id = CASE WHEN l.from_id=? THEN l.to_id ELSE l.from_id END"+
			" WHERE l.from_id=? OR l.to_id=? ORDER BY l.id", tid, tid, tid, tid)
		if len(lk) > 0 {
			var rs [][]string
			for _, x := range lk {
				rs = append(rs, []string{ev(x["rel"]), tlink(x["id"]), E(head(x["title"], titleMax)), ev(x["status"])})
			}
			b.WriteString("<h2>リンク</h2>" + table([]string{"関係", "タスク", "題", "状態"}, rs))
		}
	}
	// 履歴(018 の task_events。ADR 0003)。表の無い DB では出さない。新しい順に最大 historyMax 件
	if evs := v.q("SELECT id, kind, old, new, actor, ref, created_at FROM task_events WHERE task_id=? ORDER BY id DESC LIMIT ?", tid, historyMax); len(evs) > 0 {
		var rs [][]string
		for _, x := range evs {
			rs = append(rs, []string{"E-" + pyStr(x["id"]), ev(x["created_at"]), ev(x["kind"]), E(head(x["old"], titleMax)), E(head(x["new"], titleMax)),
				ev(x["actor"]), ev(x["ref"])})
		}
		b.WriteString("<h2>履歴</h2>" + table([]string{"id", "時刻", "種類", "前", "後", "変えた者", "元"}, rs))
		if len(evs) >= historyMax {
			b.WriteString(fmt.Sprintf("<p class=note>新しい %d 件だけです。全部は手元で <code>tb history %s</code>。</p>", historyMax, E(Clean(tid))))
		}
	}
	back := "/tasks/" + pyQuote(tid, "")
	var rs [][]string
	for _, cm := range v.q("SELECT * FROM comments WHERE task_id=? ORDER BY id DESC", tid) {
		rs = append(rs, []string{"C-" + pyStr(cm["id"]), ev(cm["kind"]), `<pre>` + E(head(cm["text"], reportMax)) + `</pre>`,
			docLinks(commentRefs(cm["id"], cm["deliverables"])), ev(cm["from_name"]), ifElse(truthy(cm["delivered_at"]), "済", "未"),
			ev(cm["created_at"]), resolveCell(cm, back, c)})
	}
	b.WriteString("<h2>報告</h2>" + countsLabel(v.reportCounts(tid)[tid]) + table([]string{"id", "種別", "本文(先頭)", "成果物", "from", "配送", "時刻", "対応"}, rs))
	b.WriteString(fmt.Sprintf("<p class=note>本文は先頭 %d 文字だけです(…は続きがあるしるし)。全文は手元で <code>tb comments %s</code> か、", reportMax, E(Clean(tid))) +
		"成果物のパスで読んでください。</p>")
	rs = nil
	for _, p := range v.q("SELECT * FROM proxy_requests WHERE task_id=? ORDER BY id DESC", tid) {
		rs = append(rs, []string{ev(p["id"]), ev(p["status"]), ev(p["effect"]), `<pre>` + ev(p["cmd"]) + `</pre>`, ev(p["why"]), ev(p["result"])})
	}
	b.WriteString("<h2>代行依頼</h2>" + table([]string{"id", "状態", "effect", "コマンド", "理由", "結果"}, rs))
	b.WriteString(taskRmForm(pyStr(t["id"]), pyStr(t["created_at"]), c))
	return "タスク " + Clean(pyStr(t["id"])), b.String(), true
}

func ifElse(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}
