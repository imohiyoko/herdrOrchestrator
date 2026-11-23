package webui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// inbox.go は報告・承認待ち(views.page_inbox)と、報告を対応済みにする操作(POST /resolve。書き込みは tb resolve)。

const noteMax = 200

// ev は views.e: None は空、それ以外は str() してエスケープ。
func ev(x any) string {
	if x == nil {
		return ""
	}
	return E(pyStr(x))
}

func tlink(tid any) string {
	return `<a href="/tasks/` + pyQuote(pyStr(tid), "") + `">` + ev(tid) + `</a>`
}

func alink(a map[string]any, frag string) string {
	k := agentKey(a)
	return `<a href="/agents/` + pyQuote(k, "") + frag + `">` + E(k) + `</a>`
}

var resolveMsg = map[string]string{"done": "対応済みにしました。", "already": "すでに対応済みでした。",
	"error": "対応済みにできませんでした(tb の実行に失敗)。"}

func resolvable(c row) bool {
	_, has := c["resolved_at"]
	return (c["kind"] == "question" || c["kind"] == "blocked") && has && !truthy(c["resolved_at"])
}

func resolveForm(c row, back string, pc pageCtx) string {
	pc.opts.norefresh = true // 入力欄があるので自動更新しない
	id, _ := c["id"].(int64)
	return `<form method=post action="/resolve"><input type=hidden name=csrf value="` + E(pc.csrf) + `">` +
		`<input type=hidden name=id value="` + strconv.FormatInt(id, 10) + `"><input type=hidden name=back value="` + E(back) + `">` +
		`<input type=text name=note maxlength=` + strconv.Itoa(noteMax) + ` placeholder="メモ(任意)" size=18> ` +
		`<button type=submit>対応済みにする</button></form>`
}

func resolvedLabel(c row) string {
	if !truthy(c["resolved_at"]) {
		return ""
	}
	s := "対応済み " + ev(c["resolved_at"])
	if truthy(c["resolved_note"]) {
		s += "<br>" + ev(c["resolved_note"])
	}
	return s
}

func resolveCell(c row, back string, pc pageCtx) string {
	if resolvable(c) {
		return resolveForm(c, back, pc)
	}
	return resolvedLabel(c)
}

// resolveNotice は ?resolved=<結果> の知らせ(views.resolve_notice。値は前後の空白を落とさない)。
func resolveNotice(pc pageCtx) string {
	if v := pc.qs["resolved"]; len(v) > 0 {
		if m, ok := resolveMsg[v[0]]; ok {
			return "<p class=note>" + E(m) + "</p>"
		}
	}
	return ""
}

// ---- 忘れているかも(tb reminder publish の最新のまとめ)

var remStatusLabel = map[string]string{"new": "新規", "changed": "変化", "continuing": "継続"}

func (v *sv) remindersHTML() string {
	rem := v.remindersLast()
	if rem == nil || len(rem.items) == 0 {
		return ""
	}
	var rows [][]string
	for _, it := range rem.items {
		ref := E(it.ref)
		if href := reminderHref(it); href != "" {
			ref = `<a href="` + E(href) + `">` + E(it.ref) + `</a>`
		}
		label := E(it.label)
		if it.p == 1 {
			label = `<span class=warn>` + label + `</span>`
		}
		st := remStatusLabel[it.status]
		if it.status == "continuing" {
			st += fmt.Sprintf("(%d 日目)", it.day)
		}
		rows = append(rows, []string{label, E(it.what), E(orStr(it.who, "-")), E(orStr(it.due, "-")), ref, E(orStr(it.age, "-")), E(st)})
	}
	s := `<h2 id="reminders">忘れているかも</h2>` + table([]string{"優先", "何が", "誰の番", "期限", "参照", "経過", "前回から"}, rows) +
		`<p class=note>` + E(rem.at) + ` の tb reminder publish の結果(定期実行が更新する)。`
	if rem.removed != 0 {
		s += fmt.Sprintf("前回から解消 %d 件。", rem.removed)
	}
	return s + "この欄は読むだけで、agent には何も送りません。承認や質問への対応は、参照のリンク先の画面で。</p>"
}

// ---- 保留した送信(inbox 表。tb message send の delivery=hold)

const (
	heldMax     = 100
	heldTextMax = 20000
	heldCols    = "SELECT id, outbox_id, from_name, recipient, kind, task_id, created_at, read_at"
)

func (v *sv) hasInbox() bool {
	for _, r := range v.q("PRAGMA table_info(inbox)") {
		if r["name"] == "recipient" {
			return true
		}
	}
	return false
}

// cleanRow は id・outbox_id 以外を clean した文字列にする(None は空)。
func cleanRow(r row) row {
	out := row{}
	for k, x := range r {
		if k == "id" || k == "outbox_id" {
			out[k] = x
			continue
		}
		if x == nil {
			out[k] = ""
		} else {
			out[k] = Clean(pyStr(x))
		}
	}
	return out
}

func (v *sv) heldHTML() string {
	if !v.hasInbox() {
		return ""
	}
	rows := v.q(heldCols+" FROM main.inbox WHERE herdr_session=? ORDER BY id DESC LIMIT ?", v.sess, heldMax)
	if len(rows) == 0 {
		return ""
	}
	var out [][]string
	for _, r0 := range rows {
		r := cleanRow(r0)
		task := "-"
		if truthy(r["task_id"]) {
			task = tlink(r["task_id"])
		}
		read := "未読"
		if truthy(r["read_at"]) {
			read = "既読 " + ev(r["read_at"])
		}
		out = append(out, []string{"I-" + pyStr(r["id"]), ev(r["from_name"]), ev(r["recipient"]), ev(r["kind"]), task, ev(r["created_at"]), read,
			`<a href="/inbox?held=` + pyStr(r["id"]) + `#held-body">本文を見る</a>`})
	}
	return `<h3 id="held">保留した送信 (inbox)</h3>` + table([]string{"id", "from", "宛先", "種別", "タスク", "時刻", "状態", ""}, out) +
		"<p class=note>宛先の入力に打ち込まず保留した送信です。受け手は人に頼まれたときに tb message inbox で読みます。" +
		"この画面は読むだけで、既読にも送信にもしません。</p>"
}

var reHeldID = regexp.MustCompile(`^[0-9]{1,12}$`)

func (v *sv) heldDetailHTML(pc pageCtx) string {
	hid := pc.one("held")
	if hid == "" {
		return ""
	}
	var m row
	if reHeldID.MatchString(hid) && v.hasInbox() {
		n, _ := strconv.ParseInt(hid, 10, 64)
		if rows := v.q(heldCols+", text FROM main.inbox WHERE herdr_session=? AND id=?", v.sess, n); len(rows) > 0 {
			m = cleanRow(rows[0])
		}
	}
	if m == nil {
		return `<p class=note id="held-body">保留した送信が見つかりません。</p>`
	}
	text := pyStr(m["text"])
	if utf8.RuneCountInString(text) > heldTextMax {
		text = string([]rune(text)[:heldTextMax])
	}
	return `<h2 id="held-body">保留した送信 I-` + pyStr(m["id"]) + ` の本文</h2>` +
		`<p>` + ev(m["from_name"]) + ` → ` + ev(m["recipient"]) + ` (` + ev(m["kind"]) + `) ` + ev(m["created_at"]) + ` / O-` + pyStr(m["outbox_id"]) + `</p>` +
		`<pre>` + E(text) + `</pre><p><a href="/inbox#held">閉じる</a></p>`
}

var approvalKinds = map[string]bool{"approval_pending": true, "alert_approval": true, "proxy_pending": true, "agent_blocked": true}

func (s *Server) pageInbox(q *rq, pc pageCtx) (string, string) {
	v := q.data()
	h := v.needsHuman()
	ids := map[string]bool{}
	for _, c := range h.comments {
		ids[pyStr(c["id"])] = true
	}
	body := resolveNotice(pc) + v.heldDetailHTML(pc) + v.remindersHTML() + `<h2 id="approvals">承認待ち(どこで・どう承認するか)</h2>`
	var appr [][]string
	how := map[string]string{}
	for _, it := range v.urgentItems() {
		if approvalKinds[it.Kind] {
			appr = append(appr, []string{`<span class=warn>` + E(it.Label) + `</span>`, summary3(it), E(it.TS)})
		}
		if strings.HasPrefix(it.ID, "c:") {
			how[it.ID] = it.How
		}
	}
	body += table([]string{"種別", "何が・中身・どう対応するか", "時刻"}, appr)
	body += "<p class=note>「手元で tb …」は、人の端末(agent の pane ではない)で実行するコマンドです。" +
		"承認待ち(Q-&lt;n&gt;。送信・agent の操作・ask で止まったコマンド)の一覧は、手元の <code>tb approval list</code> でも見られます。" +
		"agent の操作の承認だけは Web UI の agent 一覧からもできます。</p>"
	body += "<h2>要対応の報告 (needs-human)</h2>"
	var rows [][]string
	for _, c := range h.comments {
		rows = append(rows, []string{"C-" + pyStr(c["id"]), `<span class=warn>` + ev(c["kind"]) + `</span>`, tlink(c["task_id"]),
			`<pre>` + ev(c["text"]) + `</pre>`, ev(c["from_name"]), ev(c["created_at"]),
			`<span class=note>` + E(how["c:"+pyStr(c["id"])]) + `</span>`, resolveCell(c, "/inbox", pc)})
	}
	body += table([]string{"id", "種別", "タスク", "本文", "from", "時刻", "どう対応するか", "操作"}, rows)
	rows = nil
	for _, p := range h.proxies {
		rows = append(rows, []string{ev(p["id"]), tlink(p["task_id"]), ev(p["effect"]), `<pre>` + ev(p["cmd"]) + `</pre>`, ev(p["why"])})
	}
	body += "<h3>代行依頼 (pending-human)</h3>" + table([]string{"id", "タスク", "effect", "コマンド", "理由"}, rows)
	rows = nil
	for _, a := range h.blocked {
		rows = append(rows, []string{alink(a, "#approval"), ev(a["workspace_id"]),
			`<a href="/agents/` + pyQuote(agentKey(a), "") + `#approval">承認待ちの内容を見る</a>`})
	}
	body += "<h3>blocked の agent</h3>" + table([]string{"agent", "workspace", ""}, rows)
	body += v.heldHTML()
	rows = nil
	for _, c := range v.q("SELECT * FROM comments ORDER BY id DESC LIMIT 60") {
		if ids[pyStr(c["id"])] {
			continue
		}
		if len(rows) == 50 {
			break
		}
		rows = append(rows, []string{"C-" + pyStr(c["id"]), ev(c["kind"]), tlink(c["task_id"]), `<pre>` + ev(c["text"]) + `</pre>`, ev(c["from_name"]),
			ev(c["created_at"]), resolvedLabel(c)})
	}
	body += "<h2>直近の報告</h2>" + table([]string{"id", "種別", "タスク", "本文", "from", "時刻", "状態"}, rows)
	return "報告・承認待ち", body
}

// ---- 書き込み(必ず tb を通す。actions.py)

// tbPath は taskboard/bin/tb。
func tbPath() string { return filepath.Join(HerdrRoot(), "taskboard", "bin", "tb") }

// runTB は actions._run_tb: (終了コード, 出力)。tb が無い・失敗・時間切れでは -1。
// HERDR_PANE_ID は外す(Web UI の操作は人の操作として扱う。ログイン済みのブラウザからだけ届く)。
func runTB(sess string, timeout time.Duration, args ...string) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "bash", append([]string{tbPath()}, args...)...)
	var env []string
	for _, kv := range childEnv(sess) {
		if k, _, _ := strings.Cut(kv, "="); k == "HERDR_PANE_ID" || k == "TB_DB" {
			continue
		}
		env = append(env, kv)
	}
	c.Env = append(env, "TB_DB="+TBDB())
	c.Stdin = nil
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	text := strings.TrimFunc(strings.ToValidUTF8(out.String(), "�"), pyIsSpace)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ctx.Err() == nil {
			return ee.ExitCode(), text
		}
		return -1, ""
	}
	return 0, text
}

// resolveComment は question / blocked の報告を対応済みにする。返り値: done | already | error。
func resolveComment(sess string, cid int, note string) string {
	note = strings.TrimFunc(note, pyIsSpace)
	if utf8.RuneCountInString(note) > noteMax {
		note = string([]rune(note)[:noteMax])
	}
	args := []string{"resolve", "C-" + strconv.Itoa(cid)}
	if note != "" {
		args = append(args, "-m", note)
	}
	rc, out := runTB(sess, 15*time.Second, args...)
	if rc != 0 {
		return "error"
	}
	if strings.Contains(out, "すでに") {
		return "already"
	}
	return "done"
}

var (
	reCommentID = regexp.MustCompile(`^[0-9]{1,9}$`)
	reBack      = regexp.MustCompile(`^(?:/inbox|/tasks/[A-Za-z0-9_%-]{1,200})$`)
)

// postResolve は POST /resolve(handler.py と同じ)。
func (s *Server) postResolve(q *rq, form map[string]string) {
	cid, back := form["id"], form["back"]
	if _, ok := form["back"]; !ok {
		back = "/inbox"
	}
	if !reCommentID.MatchString(cid) {
		s.text(q, 400, "Bad Request")
		return
	}
	if !reBack.MatchString(back) {
		back = "/inbox"
	}
	n, _ := strconv.Atoi(cid)
	st := resolveComment(q.name, n, form["note"])
	s.logf("resolve: C-%s %s\n", cid, st)
	s.redirect(q, back+"?"+pyURLEncode([][2]string{{"resolved", st}}))
}
