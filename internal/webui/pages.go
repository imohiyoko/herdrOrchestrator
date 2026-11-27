package webui

import (
	"fmt"
	"strconv"
	"strings"
)

// pages.go は画面の枠と、いくつかの画面。

const refreshSec = 30 // 自動更新の間隔(見ていないタブでは止まる。static/refresh.js)

var nav = [][2]string{{"/", "概要"}, {"/notifications", "緊急通知"}, {"/agents", "agent"}, {"/tasks", "タスク"}, {"/board", "ボード"},
	{"/inbox", "報告・承認待ち"}, {"/approvals", "承認(Q)"}, {"/outbox", "送信"}, {"/docs", "ドキュメント"}, {"/jobs", "job"}, {"/ledger", "送信履歴"}, {"/perms", "権限"},
	{"/sessions", "セッション"}}

// pageOpts は views.page の引数(ページごとの指定)。
type pageOpts struct {
	norefresh bool
	scripts   []string
}

func badgeSpan(id string, n int) string {
	cls := "badge"
	if n == 0 {
		cls += " zero"
	}
	return `<span ` + id + `class="` + cls + `">` + strconv.Itoa(n) + `</span>`
}

// page は views.page: ナビ・バッジ・自動更新つきの画面。
func (s *Server) page(q *rq, title, body, path, logout string, o pageOpts) string {
	v := q.data()
	var links strings.Builder
	for _, n := range nav {
		href, label := n[0], n[1]
		on := path == href || (href != "/" && strings.HasPrefix(path, href+"/"))
		b := ""
		switch href {
		case "/inbox":
			b = badgeSpan("", v.badgeCount())
		case "/notifications":
			b = badgeSpan("id=nbadge ", len(v.urgentItems()))
		}
		cls := ""
		if on {
			cls = " class=on"
		}
		links.WriteString(`<a href="` + href + `"` + cls + `>` + label + b + `</a>`)
	}
	var h strings.Builder
	h.WriteString("<!doctype html><html lang=ja><head><meta charset=utf-8>")
	if !o.norefresh {
		fmt.Fprintf(&h, `<meta name=herdr-refresh content="%d"><script src="/refresh.js" defer></script>`+
			`<noscript><meta http-equiv="refresh" content="%d"></noscript>`, refreshSec, refreshSec)
	}
	h.WriteString(`<meta name=viewport content="width=device-width,initial-scale=1">` +
		"<meta name=referrer content=same-origin>" +
		"<title>" + E(title) + " - herdr" + E(titleSuffix(q.name)) + "</title><style>" + s.css() + "</style>" +
		`<script src="/notify.js" defer></script>`)
	for _, src := range o.scripts {
		h.WriteString(`<script src="` + E(src) + `" defer></script>`)
	}
	h.WriteString("</head><body><nav>" + instLabel(q.name) + links.String() + logout + "</nav><main><h1>" + E(title) + "</h1>" + body)
	note := "入力欄があるので自動更新なし(再読み込みで更新)"
	if !o.norefresh {
		note = fmt.Sprintf("自動更新 %d 秒(見ていないタブでは止まる)", refreshSec)
	}
	h.WriteString("<p class=note>" + note + " / 変更できるのは job の設定と定義・報告の「対応済み」・agent の停止 / 復活 / 作成・チケットの状態と状態の定義・スプリント・policy・承認待ち(Q-n)の承認 / 却下だけ(書き込みは tb が行う。送信履歴は読み取りのみ) / 機密を含みうる画面です / Chrome 通知は「緊急通知」で設定</p></main></body></html>")
	return h.String()
}

// table は views.table。各セルは HTML 済み。
func table(headers []string, rows [][]string) string {
	if len(rows) == 0 {
		return "<p class=note>(なし)</p>"
	}
	var b strings.Builder
	b.WriteString("<table><tr>")
	for _, x := range headers {
		b.WriteString("<th>" + x + "</th>")
	}
	b.WriteString("</tr>")
	for _, r := range rows {
		b.WriteString("<tr>")
		for _, c := range r {
			b.WriteString("<td>" + c + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	return b.String()
}

// ---- 緊急通知

func summary3(it item) string {
	body, how := it.Body, it.How
	if body == "" {
		body = "-"
	}
	if how == "" {
		how = "リンク先で確かめる"
	}
	return `<div><a href="` + E(it.Href) + `">` + E(it.Title) + `</a></div>` +
		`<div>` + E(body) + `</div>` +
		`<div class=note>対応: ` + E(how) + `</div>`
}

func (s *Server) pageNotifications(q *rq, _ pageCtx) (string, string) {
	items := q.data().urgentItems()
	body := fmt.Sprintf("<p>10 分おきのまとめ送信を待たずに知らせるものです。開いている間、"+
		"%d 秒ごとに確認し、新しく出たものを Chrome のデスクトップ通知で出します。</p>"+
		"<p><span id=nstate class=note>Chrome 通知: JavaScript が無効か、読み込み中です</span> "+
		"<button type=button id=nenable hidden>Chrome 通知を有効にする</button></p>", NotifyPollSec)
	var rows [][]string
	for _, it := range items {
		rows = append(rows, []string{`<span class=warn>` + E(it.Label) + `</span>`, summary3(it), E(it.TS)})
	}
	body += table([]string{"種別", "何が・中身・どう対応するか", "時刻"}, rows)
	body += fmt.Sprintf("<p class=note>対象: 未完了タスクの question/blocked 報告、pending-human の代行依頼、blocked の agent、"+
		"working のまま %d 分超の agent、最後の実行が失敗した job、tb reminder が急ぎとしたもの(期限切れ・今日・明日が期限)。"+
		"通知は Web UI のタブが 1 つ開いている間だけ届きます(タブを閉じると止まる)。</p>", stallMin)
	return "緊急通知", body
}

// ---- 送信(outbox)

const (
	outboxListMax = 100
	queuedWarnSec = 60
)

type outboxRow struct {
	id                                           string
	fromName, target, kind, taskID, status, text string
	age                                          int
	hasAge, stuck                                bool
}

// outboxRows は outbox の一覧(新しい順に最大 100 件。text は先頭 80 文字だけ)。
func (v *sv) outboxRows() []outboxRow {
	var out []outboxRow
	for _, o := range v.q("SELECT id, from_name, target, kind, task_id, status, text, created_at FROM outbox ORDER BY id DESC LIMIT ?", outboxListMax) {
		r := outboxRow{id: pyStr(o["id"]), fromName: Clean(pyStr(o["from_name"])), target: Clean(pyStr(o["target"])), kind: Clean(pyStr(o["kind"])),
			taskID: Clean(or(o["task_id"], "")), status: Clean(pyStr(o["status"])), text: short(o["text"], 80)}
		if t, ok := parseTS(o["created_at"]); ok {
			r.hasAge = true
			r.age = int(nowFn().Sub(t).Seconds())
			if r.age < 0 {
				r.age = 0
			}
		}
		r.stuck = o["status"] == "queued" && r.hasAge && r.age > queuedWarnSec
		out = append(out, r)
	}
	return out
}

func ageLabel(sec int, ok bool) string {
	switch {
	case !ok:
		return "?"
	case sec < 60:
		return fmt.Sprintf("%d 秒", sec)
	case sec < 3600:
		return fmt.Sprintf("%d 分", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%d 時間", sec/3600)
	}
	return fmt.Sprintf("%d 日", sec/86400)
}

func (s *Server) pageOutbox(q *rq, _ pageCtx) (string, string) {
	var rows [][]string
	v := q.data()
	for _, o := range v.outboxRows() {
		st, age := E(o.status), E(ageLabel(o.age, o.hasAge))
		if o.stuck {
			st, age = `<span class=warn>`+st+`</span>`, `<span class=warn>`+age+`</span>`
		}
		id := "O-" + o.id
		if o.status == "pending" {
			if qid := v.requestOf("outbox", o.id); qid != "" {
				id += `<br><span class=note>` + qid + `</span>`
			}
		}
		rows = append(rows, []string{id, st, E(o.fromName), E(o.target), E(o.kind), E(o.taskID), age, E(o.text)})
	}
	body := table([]string{"id", "状態", "from", "宛先", "種別", "タスク", "経過", "本文(先頭 80 文字)"}, rows)
	body += fmt.Sprintf("<p class=note>新しい順に最大 100 件。queued のまま %d 秒を超えたものは赤で強調"+
		"(scheduler の flush が止まっていないか見てください)。pending(承認待ち)の送信は Q-&lt;n&gt; として「報告・承認待ち」に出ます。承認は手元で tb approval approve Q-&lt;n&gt;。この画面からは操作できません。</p>", queuedWarnSec)
	return "送信 (outbox)", body
}

// ---- セッション一覧

func (s *Server) pageSessions(q *rq, _ pageCtx) (string, string) {
	var rows strings.Builder
	for _, n := range KnownSessions() {
		cell := func() (cell string) {
			defer func() {
				if r := recover(); r != nil { // 1 セッションが読めなくても一覧は出す
					cell = `<td colspan=2>読めない(` + E(fmt.Sprintf("%T", r)) + `)</td>`
				}
			}()
			v := q.data()
			if n != q.name {
				v = newSV(n)
				defer v.close()
			}
			return fmt.Sprintf("<td>%d</td><td>%d</td>", len(v.urgentItems()), v.badgeCount())
		}()
		// ページは /s/<この画面>/sessions。隣のセッションは相対パスで(/… はこの画面の下に付け替えられるため)
		link := `<a href="` + E("../"+n+"/") + `">` + E(n) + `</a>`
		if n == q.name {
			link = "<strong>" + E(n) + "</strong>(この画面)"
		}
		rows.WriteString("<tr><td>" + link + "</td>" + cell + "</tr>")
	}
	body := "<p>この Web UI は全 herdr セッションの画面を返します(ログインは共通)。行き先を選んでください。" +
		"一覧は DB に行のあるセッションと、herdr のセッションのディレクトリ(" + E(herdrSessionsDir()) + ")から作ります。</p>" +
		"<table><tr><th>セッション</th><th>緊急通知</th><th>報告・承認待ち</th></tr>" + rows.String() + "</table>"
	return "セッション一覧", body
}

// ---- 振り分け(views.route)

// pageCtx はページに渡すもの(views.route の ctx)。ページは opts を書き換えてよい。
type pageCtx struct {
	csrf string
	opts *pageOpts
	qs   map[string][]string // クエリ(parse_qs: 空の値は落とす)
	// 書き込みが断られたときだけ: 理由(flash)と、送られたフォーム(draft。入力を残して描き直す)
	flash string
	draft form
}

// one は views._one: その名前の最初の値の前後の空白を落としたもの(無ければ "")。
func (c pageCtx) one(name string) string {
	if v := c.qs[name]; len(v) > 0 {
		return strings.TrimFunc(v[0], pyIsSpace)
	}
	return ""
}

type pageFn func(s *Server, q *rq, c pageCtx) (string, string)

// pages はパスごとの画面(/jobs/<job>・/docs/view・/tasks/<id>・/agents/<key> は server.go の route が別に扱う)。
var pages = map[string]pageFn{
	"/":              (*Server).pageOverview,
	"/notifications": (*Server).pageNotifications,
	"/outbox":        (*Server).pageOutbox,
	"/sessions":      (*Server).pageSessions,
	"/ledger":        (*Server).pageLedger,
	"/inbox":         (*Server).pageInbox,
	"/approvals":     (*Server).pageApprovals,
	"/perms":         (*Server).pagePerms,
	"/jobs":          (*Server).pageJobs,
	"/tasks":         (*Server).pageTasks,
	"/board":         (*Server).pageBoard,
	"/docs":          (*Server).pageDocs,
	"/agents":        (*Server).pageAgents,
	"/agent-graph":   (*Server).pageAgentGraph,
	"/org":           (*Server).pageOrg,
	"/agent-new":     (*Server).pageAgentNewRoute,
	"/statuses":      (*Server).pageStatuses,
}
