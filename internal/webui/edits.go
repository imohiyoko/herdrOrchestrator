package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// edits.go は Web UI からの編集: チケットの状態(/task-status)、チケットの削除(/task-rm)、状態の定義(/statuses)、スプリント(/sprint-op)、
// 設定 policy(/policy)。ジョブの定義(/jobs/<job> の action=conf)は jobspage.go。
// 書き込みはどれも tb が行い(tasks.db にも config にも直接書かない)、検査と認可も tb のものを通る。
// Web UI からの tb は人の操作として動く(HERDR_PANE_ID を外す。runTB)。policy はさらにログインのパスワードを求める。

var editPaths = map[string]bool{"/task-status": true, "/statuses": true, "/sprint-op": true, "/policy": true, "/approval-op": true, "/task-rm": true}

var (
	reTaskID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	reStatusName = regexp.MustCompile(`^[^\s,"'` + "`" + `\\*?\[\]#=-][^\s,"'` + "`" + `\\*?\[\]#=]{0,29}$`)
	reProjName   = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
)

const (
	editMemoMax = 2000 // メモ・説明・目標の文字数
	editOutMax  = 2000 // 画面に出す tb の出力
)

// doneMsg は書き込みのあとの ?done=<キー> の知らせ(値はこの表のものだけ出す)。
var doneMsg = map[string]string{
	"status":       "状態を変えました。",
	"task-rm":      "チケットを消しました(コメント・履歴・リンクも)。",
	"job-rm":       "ジョブを消しました(定義と上書き設定)。",
	"job-run":      "ジョブを動かし始めました。結果は「最近の実行」に出ます(少し待って再読み込み)。",
	"status-add":   "状態を足しました。",
	"status-rm":    "状態を外しました。",
	"sprint-add":   "スプリントを作りました(planned)。",
	"sprint-start": "スプリントを始めました(active)。",
	"sprint-close": "スプリントを閉じました。",
	"policy":       "policy を保存しました。次の判定から効きます。",
	"conf":         "ジョブの定義を保存しました。次の分から反映されます。",
	"approve":      "承認しました。結果は一覧の状態で確かめてください(ask で止まったコマンドは、依頼した agent が 1 回だけ実行します)。",
	"reject":       "却下しました。",
	"exception":    "deny を例外として 1 回だけ許可しました。依頼した agent が 1 回だけ実行できます。",
}

func doneNotice(c pageCtx) string {
	if m, ok := doneMsg[c.one("done")]; ok {
		return "<p class=note>" + E(m) + "</p>"
	}
	return ""
}

// editErr は書き込みに失敗したときの知らせ(tb の出力を添える)。
func editErr(c pageCtx) string {
	if c.flash == "" {
		return ""
	}
	return errHTML(c.flash)
}

// tbFail は tb が断ったときの文。出力は長ければ切る。
func tbFail(rc int, out string) string {
	if rc < 0 {
		return "tb を実行できませんでした(見つからないか、時間切れ)"
	}
	out = Clean(out)
	if utf8.RuneCountInString(out) > editOutMax {
		out = string([]rune(out)[:editOutMax]) + "…"
	}
	if out == "" {
		out = "(出力なし)"
	}
	return "tb が断りました(終了コード " + strconv.Itoa(rc) + "): " + out
}

// textHash は本文の版(編集を始めたときの本文と、保存するときの本文が同じかを確かめる)。
func textHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:16]) }

// normText はフォームの本文(改行を LF に揃え、末尾に改行を 1 つ)。
func normText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

// textarea は本文の入力欄。先頭の改行は HTML が 1 つ落とすので足しておく。
func textarea(name, text string, rows int) string {
	return `<textarea name=` + name + ` rows=` + strconv.Itoa(rows) + ` style="width:100%;font-family:monospace" spellcheck=false>` + "\n" +
		htmlEsc.Replace(text) + `</textarea>`
}

// editable は本文を Web UI でそのまま編集してよいか(ターミナル制御文字を含むと、表示と保存で中身が変わるため断る)。
func editable(text string) bool { return Clean(text) == text && utf8.ValidString(text) }

// postEdit は editPaths の POST。成功なら画面へ戻し(?done=)、失敗ならその画面を理由つきで 400 で返す。
func (s *Server) postEdit(q *rq, path string, f form, csrf string) {
	var (
		back, done, msg string
		render          func(c pageCtx) (string, string, string)
	)
	switch path {
	case "/task-status":
		back, done, msg, render = s.postTaskStatus(q, f)
	case "/task-rm":
		back, done, msg, render = s.postTaskRm(q, f)
	case "/statuses":
		back, done, msg, render = s.postStatuses(q, f)
	case "/sprint-op":
		back, done, msg, render = s.postSprint(q, f)
	case "/policy":
		back, done, msg, render = s.postPolicy(q, f)
	case "/approval-op":
		back, done, msg, render = s.postApprovalOp(q, f)
	}
	if render == nil {
		s.text(q, 404, "Not Found")
		return
	}
	if msg == "" {
		s.logf("edit: %s %s ok\n", path, done)
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		s.redirect(q, back+sep+pyURLEncode([][2]string{{"done", done}}))
		return
	}
	s.logf("edit: %s refused\n", path)
	q.v.close() // 書き込みのあとの状態で描き直す
	q.v = nil
	opts := &pageOpts{norefresh: true}
	c := pageCtx{csrf: csrf, opts: opts, qs: map[string][]string{}, flash: msg, draft: f}
	title, body, cur := render(c)
	opts.norefresh = true
	s.html(q, 400, s.page(q, title, body, cur, "", *opts))
}

// ---- チケットの状態(tb ticket move)

func (v *sv) taskExists(id string) bool {
	return reTaskID.MatchString(id) && len(v.q("SELECT id FROM tasks WHERE id=?", id)) > 0
}

func (s *Server) postTaskStatus(q *rq, f form) (string, string, string, func(pageCtx) (string, string, string)) {
	v := q.data()
	id := f["id"]
	if !v.taskExists(id) {
		return "", "", "", nil
	}
	back := "/tasks/" + pyQuote(id, "")
	render := func(c pageCtx) (string, string, string) {
		t, b, _ := s.pageTask(q, id, c)
		return t, b, back
	}
	st := f["status"]
	if _, ok := v.statuses().cat[st]; !ok || !isDefinedStatus(v, st) {
		return back, "", "状態は設定 statuses にあるものから選んでください", render
	}
	memo := strings.TrimFunc(f["memo"], pyIsSpace)
	if utf8.RuneCountInString(memo) > editMemoMax {
		return back, "", "メモは " + strconv.Itoa(editMemoMax) + " 文字までです", render
	}
	args := []string{"ticket", "move", id, st}
	if memo != "" {
		args = append(args, "-m", memo)
	}
	if rc, out := runTB(q.name, 15*time.Second, args...); rc != 0 {
		return back, "", tbFail(rc, out), render
	}
	return back, "status", "", render
}

func isDefinedStatus(v *sv, st string) bool {
	for _, r := range v.statuses().rows {
		if r.name == st {
			return true
		}
	}
	return false
}

// taskStatusForm はタスク画面の「状態を変える」。
func (v *sv) taskStatusForm(id, cur string, c pageCtx) string {
	if c.csrf == "" {
		return ""
	}
	pick := cur
	if c.draft != nil && c.draft["status"] != "" {
		pick = c.draft["status"]
	}
	var opts strings.Builder
	for _, r := range v.statuses().rows {
		label := r.name + "(" + r.cat + ")"
		opts.WriteString(`<option value="` + E(r.name) + `"` + sel(r.name == pick, " selected") + `>` + E(label) + `</option>`)
	}
	memo := ""
	if c.draft != nil {
		memo = c.draft["memo"]
	}
	return `<h2>状態を変える</h2><form method=post action="/task-status"><input type=hidden name=csrf value="` + E(c.csrf) + `">` +
		`<input type=hidden name=id value="` + E(id) + `"><p><label>状態 <select name=status>` + opts.String() + `</select></label> ` +
		`<label>メモ <input type=text name=memo size=40 maxlength=` + strconv.Itoa(editMemoMax) + ` value="` + E(memo) + `"></label> ` +
		`<button type=submit>変える</button></p></form>` +
		`<p class=note><code>tb ticket move</code> と同じ扱いです(どの状態へも動かせる。メモは閉じた分類なら要約になる)。` +
		`状態の定義は <a href="/statuses">状態の定義</a> で変えます。</p>`
}

// ---- チケットの削除(tb ticket rm)

func (s *Server) postTaskRm(q *rq, f form) (string, string, string, func(pageCtx) (string, string, string)) {
	v := q.data()
	id := f["id"]
	if !v.taskExists(id) {
		return "", "", "", nil
	}
	render := func(c pageCtx) (string, string, string) {
		t, b, _ := s.pageTask(q, id, c)
		return t, b, "/tasks/" + pyQuote(id, "")
	}
	if f["confirm"] != "1" {
		return "", "", "消すときは「元に戻せないことを確かめた」に印を付けてください", render
	}
	// 画面を開いたときのチケットか(消した ID がまた使われていれば、tb が断る)
	if rc, out := runTB(q.name, 15*time.Second, "ticket", "rm", id, "--if-created", f["created"]); rc != 0 {
		return "", "", tbFail(rc, out), render
	}
	return "/tasks", "task-rm", "", render
}

// taskRmForm はタスク画面の「チケットを消す」(元に戻せないので、印を付けないと送れない)。
func taskRmForm(id, created string, c pageCtx) string {
	if c.csrf == "" {
		return ""
	}
	return `<h2>チケットを消す</h2><form method=post action="/task-rm"><input type=hidden name=csrf value="` + E(c.csrf) + `">` +
		`<input type=hidden name=id value="` + E(id) + `"><input type=hidden name=created value="` + E(created) + `"><p><label><input type=checkbox name=confirm value=1 required> 元に戻せないことを確かめた</label> ` +
		`<button type=submit>消す</button></p></form>` +
		`<p class=note><code>tb ticket rm</code> と同じ扱いです(コメント・履歴・リンクも消え、相手のチケットの履歴には unlink が残る)。` +
		`子チケットがあると消せません。アラート・提案のチケットは消せません。</p>`
}

// ---- 状態の定義(tb ticket status add / rm)

func (s *Server) pageStatuses(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true
	v := q.data()
	st := v.statuses()
	_, src, _ := v.configText("statuses")
	counts := map[string]int64{}
	for _, r := range v.q("SELECT status, COUNT(*) AS n FROM tasks GROUP BY status") {
		counts[pyStr(r["status"])] = intOf(r["n"])
	}
	var b strings.Builder
	b.WriteString(doneNotice(c) + editErr(c))
	var names []string
	for _, r := range st.rows {
		names = append(names, r.name)
	}
	hid := `<input type=hidden name=csrf value="` + E(c.csrf) + `">`
	var rs [][]string
	for _, r := range st.rows {
		n := counts[r.name]
		var to strings.Builder // チケットが残っていれば、移し先を選ばないと tb が断る
		for _, x := range names {
			if x != r.name {
				to.WriteString(`<option value="` + E(x) + `">` + E(x) + `</option>`)
			}
		}
		rm := `<form method=post action="/statuses" style="margin:0">` + hid + `<input type=hidden name=action value=rm>` +
			`<input type=hidden name=name value="` + E(r.name) + `">` +
			ifs(n > 0, `<label>残りを <select name=to>`+to.String()+`</select> へ移して</label> `) + `<button type=submit>外す</button></form>`
		rs = append(rs, []string{`<a href="/tasks?` + pyURLEncode(kvs{{"status", r.name}}) + `">` + E(r.name) + `</a>`, E(r.cat),
			E(orStr(r.desc, "-")), strconv.FormatInt(n, 10), rm})
	}
	b.WriteString("<p>チケットの状態と、その分類(todo / active / review / blocked / done / cancelled)。設定 <code>statuses</code> の出どころ: <code>" +
		E(src) + "</code></p>")
	b.WriteString(table([]string{"状態", "分類", "説明", "チケット", "外す"}, rs))
	d := c.draft
	if d == nil || d["action"] != "add" {
		d = form{}
	}
	var cats, after strings.Builder
	for _, x := range statusCategories {
		cats.WriteString(`<option value=` + x + sel(d["cat"] == x, " selected") + `>` + x + `</option>`)
	}
	after.WriteString(`<option value="">(その分類の最後)</option>`)
	for _, x := range names {
		after.WriteString(`<option value="` + E(x) + `"` + sel(d["after"] == x, " selected") + `>` + E(x) + `</option>`)
	}
	b.WriteString(`<h2>状態を足す</h2><form method=post action="/statuses">` + hid + `<input type=hidden name=action value=add><p>` +
		`<label>名前 <input type=text name=name required maxlength=30 size=16 value="` + E(d["name"]) + `"></label> ` +
		`<label>分類 <select name=cat>` + cats.String() + `</select></label> ` +
		`<label>説明 <input type=text name=desc size=30 maxlength=200 value="` + E(d["desc"]) + `"></label> ` +
		`<label>位置 <select name=after>` + after.String() + `</select></label> ` +
		`<button type=submit>足す</button></p></form>`)
	b.WriteString(`<p class=note>書き込みは <code>tb ticket status add</code> / <code>rm</code> が行います(検査も tb と同じ)。` +
		`チケットが残っている状態は、移し先を選ばないと外せません。名前を変えるには、新しい状態を足してから古い状態を外して移します。</p>`)
	return "状態の定義", b.String()
}

func (s *Server) postStatuses(q *rq, f form) (string, string, string, func(pageCtx) (string, string, string)) {
	render := func(c pageCtx) (string, string, string) {
		t, b := s.pageStatuses(q, c)
		return t, b, "/statuses"
	}
	name := f["name"]
	if !reStatusName.MatchString(name) {
		return "/statuses", "", "状態の名前は 30 文字までで、空白・カンマ・引用符・# = * ? [ ] を含まず、- で始まらないものにしてください", render
	}
	switch f["action"] {
	case "add":
		cat, desc, after := f["cat"], strings.TrimFunc(f["desc"], pyIsSpace), f["after"]
		if !isCategory(cat) {
			return "/statuses", "", "分類が不正です", render
		}
		if strings.ContainsAny(desc, "\n#") || utf8.RuneCountInString(desc) > 200 {
			return "/statuses", "", "説明は 200 文字までで、改行と # は使えません", render
		}
		args := []string{"ticket", "status", "add", name, cat}
		if desc != "" {
			args = append(args, "-m", desc)
		}
		if after != "" {
			if !isDefinedStatus(q.data(), after) {
				return "/statuses", "", "位置の状態がありません", render
			}
			args = append(args, "--after", after)
		}
		if rc, out := runTB(q.name, 15*time.Second, args...); rc != 0 {
			return "/statuses", "", tbFail(rc, out), render
		}
		return "/statuses", "status-add", "", render
	case "rm":
		args := []string{"ticket", "status", "rm", name}
		if to := f["to"]; to != "" {
			if !isDefinedStatus(q.data(), to) || to == name {
				return "/statuses", "", "移し先の状態が不正です", render
			}
			args = append(args, "--to", to)
		}
		if rc, out := runTB(q.name, 15*time.Second, args...); rc != 0 {
			return "/statuses", "", tbFail(rc, out), render
		}
		return "/statuses", "status-rm", "", render
	}
	return "", "", "", nil
}

// ---- スプリント(tb sprint-add / sprint-start / sprint-close)

// sprintForm はボードの「スプリントを作る」。
func (v *sv) sprintForm(c pageCtx) string {
	if c.csrf == "" {
		return ""
	}
	d := c.draft
	if d == nil || d["op"] != "add" {
		d = form{}
	}
	seen := map[string]bool{}
	var dl strings.Builder
	for _, sq := range []string{"SELECT DISTINCT project FROM tasks ORDER BY project", "SELECT DISTINCT project FROM sprints ORDER BY project"} {
		for _, r := range v.q(sq) {
			p := pyStr(r["project"])
			if !seen[p] && reProjName.MatchString(p) {
				seen[p] = true
				dl.WriteString(`<option value="` + E(p) + `">`)
			}
		}
	}
	return `<h2>スプリントを作る</h2><form method=post action="/sprint-op"><input type=hidden name=csrf value="` + E(c.csrf) + `">` +
		`<input type=hidden name=op value=add><p>` +
		`<label>PJ <input type=text name=project required maxlength=64 size=12 list=sprint-projects value="` + E(d["project"]) + `"></label>` +
		`<datalist id=sprint-projects>` + dl.String() + `</datalist> ` +
		`<label>名前 <input type=text name=name required maxlength=100 size=16 value="` + E(d["name"]) + `"></label> ` +
		`<label>開始 <input type=date name=start value="` + E(d["start"]) + `"></label> ` +
		`<label>終了 <input type=date name=end value="` + E(d["end"]) + `"></label></p><p>` +
		`<label>目標 <input type=text name=goal size=50 maxlength=` + strconv.Itoa(editMemoMax) + ` value="` + E(d["goal"]) + `"></label> ` +
		`<button type=submit>作る</button></p></form>` +
		`<p class=note>書き込みは <code>tb sprint-add</code>(planned で作る)。始める・閉じるは下の一覧のボタン(<code>tb sprint-start</code> / <code>tb sprint-close</code>)。</p>`
}

// sprintOps はスプリント一覧の行の操作(planned なら始める、active なら閉じる)。
func sprintOps(x row, sprints []row, csrf string) string {
	if csrf == "" {
		return "-"
	}
	id := "S-" + strconv.FormatInt(intOf(x["id"]), 10)
	hid := `<input type=hidden name=csrf value="` + E(csrf) + `"><input type=hidden name=sprint value="` + id + `">`
	switch x["state"] {
	case "planned":
		return `<form method=post action="/sprint-op" style="margin:0">` + hid + `<input type=hidden name=op value=start><button type=submit>始める</button></form>`
	case "active":
		var carry strings.Builder
		carry.WriteString(`<option value="">バックログへ</option>`)
		for _, y := range sprints {
			if y["state"] == "planned" && pyStr(y["project"]) == pyStr(x["project"]) {
				cid := "S-" + strconv.FormatInt(intOf(y["id"]), 10)
				carry.WriteString(`<option value="` + cid + `">` + cid + ` へ</option>`)
			}
		}
		return `<form method=post action="/sprint-op" style="margin:0">` + hid + `<input type=hidden name=op value=close>` +
			`<label>残りは <select name=carry>` + carry.String() + `</select></label> <button type=submit>閉じる</button></form>`
	}
	return "-"
}

func (s *Server) postSprint(q *rq, f form) (string, string, string, func(pageCtx) (string, string, string)) {
	render := func(c pageCtx) (string, string, string) {
		t, b := s.pageBoard(q, c)
		return t, b, "/board"
	}
	switch f["op"] {
	case "add":
		p, name, start, end := f["project"], strings.TrimFunc(f["name"], pyIsSpace), f["start"], f["end"]
		goal := strings.TrimFunc(f["goal"], pyIsSpace)
		switch {
		case !reProjName.MatchString(p):
			return "/board", "", "PJ は英数字と _ だけです", render
		case name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\n"):
			return "/board", "", "名前は 1〜100 文字(改行なし)で入力してください", render
		case (start != "" && !reDate.MatchString(start)) || (end != "" && !reDate.MatchString(end)):
			return "/board", "", "開始・終了は YYYY-MM-DD です", render
		case utf8.RuneCountInString(goal) > editMemoMax:
			return "/board", "", "目標は " + strconv.Itoa(editMemoMax) + " 文字までです", render
		}
		args := []string{"sprint-add", "-p", p, "-n", name}
		if start != "" {
			args = append(args, "--start", start)
		}
		if end != "" {
			args = append(args, "--end", end)
		}
		if goal != "" {
			args = append(args, "--goal", goal)
		}
		rc, out := runTB(q.name, 15*time.Second, args...)
		if rc != 0 {
			return "/board", "", tbFail(rc, out), render
		}
		back := "/board"
		if id := strings.TrimFunc(out, pyIsSpace); reSprint.MatchString(id) {
			back += "?" + pyURLEncode([][2]string{{"sprint", id}})
		}
		return back, "sprint-add", "", render
	case "start", "close":
		id := f["sprint"]
		if !reSprint.MatchString(id) {
			return "/board", "", "スプリントは S-<番号> です", render
		}
		args := []string{"sprint-" + f["op"], id}
		if c := f["carry"]; f["op"] == "close" && c != "" {
			if !reSprint.MatchString(c) || c == id {
				return "/board", "", "持ち越し先のスプリントが不正です", render
			}
			args = append(args, "--carry", c)
		}
		if rc, out := runTB(q.name, 15*time.Second, args...); rc != 0 {
			return "/board", "", tbFail(rc, out), render
		}
		return "/board?" + pyURLEncode([][2]string{{"sprint", id}}), "sprint-" + f["op"], "", render
	}
	return "", "", "", nil
}

// ---- 設定 policy(tb config set policy。人だけ: ログインのパスワードを求める)

// policyEditSource は Web UI から書ける policy の出どころと、書くときの tb の引数。書けなければ理由。
func (v *sv) policyEdit() (text, src string, args []string, why string) {
	text, src, ok := v.configText("policy")
	if !ok {
		text = ""
	}
	switch {
	case strings.HasPrefix(src, "file:") && cfg("WEBUI_POLICY", "") != "":
		return text, src, nil, "WEBUI_POLICY で表示用のファイルを指定しているので、Web UI からは書きません(tb が読むものと違うため)"
	case utf8.RuneCountInString(text) > configMax:
		return text, src, nil, "policy が大きすぎて全部は読めないので、Web UI からは書きません(手元で tb config edit policy)"
	case !editable(text):
		return text, src, nil, "policy に制御文字が含まれるので、Web UI からは書きません(手元で tb config edit policy)"
	}
	args = []string{"config", "set", "policy"}
	if src == "db:*" {
		args = append(args, "--shared") // 共通の行から読んでいるなら、その行を書き換える
	}
	return text, src, args, ""
}

func (v *sv) policyForm(c pageCtx) string {
	text, src, args, why := v.policyEdit()
	dest := "DB のこのセッションの行(<code>" + E(v.sess) + "</code>)"
	switch {
	case strings.HasPrefix(src, "file:") && cfg("TB_POLICY", "") != "":
		dest = "TB_POLICY のファイル <code>" + E(strings.TrimPrefix(src, "file:")) + "</code>"
	case len(args) > 3:
		dest = "DB の全セッション共通の行(<code>*</code>)"
	}
	h := "<h2>policy を編集する</h2>"
	if why != "" {
		return h + "<p class=note>" + E(why) + "</p>"
	}
	if c.csrf == "" {
		return ""
	}
	base := textHash(text)
	if c.draft != nil && c.draft["body"] != "" && c.draft["base"] != "" {
		text, base = normText(c.draft["body"]), c.draft["base"]
	}
	n := strings.Count(text, "\n") + 3
	return h + `<p>1 行「<code>&lt;主体&gt; &lt;操作&gt; &lt;対象&gt; = allow|ask|deny</code>」。今の出どころ: <code>` + E(src) + `</code>。保存先: ` + dest + `</p>` +
		`<form method=post action="/policy"><input type=hidden name=csrf value="` + E(c.csrf) + `">` +
		`<input type=hidden name=base value="` + E(base) + `">` + textarea("body", text, min(max(n, 8), 40)) +
		`<p><label>ログインのパスワード(policy を変えるのは人だけなので確かめます) <input type=password name=password required autocomplete=current-password></label> ` +
		`<button type=submit>保存</button></p></form>` +
		`<p class=note>書き込みは <code>tb config set policy</code> が行い、tb と同じ検査を通します(読めない行があれば書きません)。` +
		`パスワードの失敗はログインの失敗と同じく数え、続けばロックします。変更の履歴は <code>tb config history policy</code>。</p>`
}

func (s *Server) postPolicy(q *rq, f form) (string, string, string, func(pageCtx) (string, string, string)) {
	render := func(c pageCtx) (string, string, string) {
		t, b := s.pagePerms(q, c)
		return t, b, "/perms"
	}
	v := q.data()
	text, _, args, why := v.policyEdit()
	if why != "" {
		return "/perms", "", why, render
	}
	a := s.Auth
	if wait := a.LockedFor(); wait > 0 {
		return "/perms", "", "パスワードの失敗が続いたため一時的にロックしています。約 " + strconv.Itoa(wait) + " 秒後にやり直してください", render
	}
	if !a.Attempt(a.Username(), runeCut(f["password"], MaxField)) {
		time.Sleep(FailDelay())
		return "/perms", "", "パスワードが違います", render
	}
	if f["base"] != textHash(text) {
		return "/perms", "", "編集を始めたあとで policy が変わりました。画面を開き直して、今の内容から編集してください", render
	}
	body := normText(f["body"])
	if utf8.RuneCountInString(body) > configMax || !editable(body) {
		return "/perms", "", "本文が大きすぎるか、使えない文字(制御文字)を含みます", render
	}
	if body == text {
		return "/perms", "policy", "", render
	}
	if rc, out := runTBInput(q.name, args, body); rc != 0 {
		return "/perms", "", tbFail(rc, out), render
	}
	return "/perms", "policy", "", render
}

// ---- ジョブの定義(tb job set <job>。/jobs/<job> の action=conf)

// jobConfEdit は Web UI から書けるジョブの定義(conf の本文)と、書くときの tb の引数。書けなければ理由。
func (v *sv) jobConfEdit(name string) (text, src string, args []string, why string) {
	rows := v.q("SELECT herdr_session AS s, body FROM main.config WHERE key = ? AND herdr_session IN (?, '*') ORDER BY herdr_session = '*' LIMIT 1",
		"sched-job:"+name, v.sess)
	if len(rows) == 0 || rows[0]["body"] == nil {
		return "", "", nil, "定義が DB にありません(jobs/" + name + ".conf のままです)。先に手元で tb job import " + name + " で取り込みます"
	}
	text, src = pyStr(rows[0]["body"]), "db:"+pyStr(rows[0]["s"])
	switch {
	case utf8.RuneCountInString(text) > confMaxDB:
		return text, src, nil, "定義が大きすぎるので、Web UI からは書きません(手元で tb job edit " + name + ")"
	case !editable(text):
		return text, src, nil, "定義に制御文字が含まれるので、Web UI からは書きません(手元で tb job edit " + name + ")"
	}
	if _, n := mask(text); n > 0 {
		return text, src, nil, "定義にトークンらしい文字列があるので、Web UI では本文を出さず編集もしません(手元で tb job edit " + name + ")"
	}
	args = []string{"job", "set", name}
	if src == "db:*" {
		args = append(args, "--shared") // 共通の行から読んでいるなら、その行を書き換える
	}
	return text, src, args, ""
}

// jobConfForm はジョブ画面の「定義(本文)を編集する」。
func (v *sv) jobConfForm(name, csrf string, draft form) string {
	h := "<h2>定義(本文)を編集する</h2>"
	text, src, args, why := v.jobConfEdit(name)
	if why != "" {
		return h + "<p class=note>" + E(why) + "</p>"
	}
	dest := "DB のこのセッションの行(<code>sched-job:" + E(name) + "</code>、セッション <code>" + E(v.sess) + "</code>)"
	if len(args) > 3 {
		dest = "DB の全セッション共通の行(<code>sched-job:" + E(name) + "</code>、<code>*</code>)"
	}
	base := textHash(text)
	if draft != nil && draft["action"] == "conf" && draft["base"] != "" {
		text, base = normText(draft["conf"]), draft["base"]
	}
	n := strings.Count(text, "\n") + 3
	return h + `<p>宛先(<code>TARGET</code>)・送る本文(<code>PROMPT</code>)・実行するコマンド(<code>COMMAND</code>)などを含む conf の全体です。今の出どころ: <code>` +
		E(src) + `</code>。保存先: ` + dest + `</p>` +
		`<form method=post action="/jobs/` + pyQuote(name, "/") + `"><input type=hidden name=csrf value="` + E(csrf) + `">` +
		`<input type=hidden name=action value=conf><input type=hidden name=base value="` + E(base) + `">` +
		textarea("conf", text, min(max(n, 8), 40)) + `<p><button type=submit>定義を保存</button></p></form>` +
		`<p class=note>書き込みは <code>tb job set</code> が行い、scheduler が読めることを確かめてから書きます(読めなければ書きません)。` +
		`書けるのは <code>KEY=値</code> の行だけで、<code>$</code> などの展開は使えません(文字として書くなら <code>'…'</code> で囲む)。` +
		`上のスケジュールの変更(上書き設定)は定義より優先します。変更の履歴は <code>tb job history ` + E(name) + `</code>。</p>`
}

// saveJobConf はジョブの定義を保存する。エラー文を返す(成功なら "")。
func (v *sv) saveJobConf(name string, f form) string {
	text, _, args, why := v.jobConfEdit(name)
	if why != "" {
		return why
	}
	if f["base"] != textHash(text) {
		return "編集を始めたあとで定義が変わりました。画面を開き直して、今の内容から編集してください"
	}
	body := normText(f["conf"])
	if body == "" {
		return "本文が空です"
	}
	if utf8.RuneCountInString(body) > confMaxDB || !editable(body) {
		return "本文が大きすぎるか、使えない文字(制御文字)を含みます"
	}
	if body == text {
		return ""
	}
	if rc, out := runTBInput(v.sess, args, body); rc != 0 {
		return tbFail(rc, out)
	}
	return ""
}
