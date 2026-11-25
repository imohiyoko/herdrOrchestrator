package webui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// notify.go は data.py の緊急通知の集計(urgent_items)と、承認待ちの数(badge_count)。

const (
	notifyMax      = 100
	stallMin       = 30 // working のまま最終入力から何分超で滞留警告にするか
	webuiRuleHours = 24
)

// item は通知 1 件(id は同じ事象なら同じ値)。値はすべて clean 済みのプレーンテキスト。href はこのサーバー内の絶対パス。
type item struct {
	ID, Kind, Label, Title, Body, How, Href, TS string
}

func (v *sv) hasResolution() bool {
	for _, r := range v.q("PRAGMA table_info(comments)") {
		if r["name"] == "resolved_at" {
			return true
		}
	}
	return false
}

type human struct {
	comments, proxies []row
	blocked           []map[string]any
}

// needsHuman は承認待ち: 未対応の question/blocked の報告(未完了タスク)、pending-human の代行依頼、blocked の agent。
func (v *sv) needsHuman() human {
	res := ""
	if v.hasResolution() {
		res = " AND c.resolved_at IS NULL"
	}
	comments := v.q("SELECT c.*, t.status AS task_status, t.title AS task_title FROM comments c " +
		"LEFT JOIN tasks t ON t.id=c.task_id WHERE c.kind IN ('question','blocked') " +
		"AND COALESCE(t.status,'') NOT IN (__CLOSED_STATUSES__)" + res + " ORDER BY c.id DESC LIMIT 100")
	proxies := v.q("SELECT * FROM proxy_requests WHERE status='pending-human' ORDER BY id DESC LIMIT 100")
	agents, _ := getAgents(v.sess)
	var blocked []map[string]any
	for _, a := range agents {
		if a["agent_status"] == "blocked" {
			blocked = append(blocked, a)
		}
	}
	return human{comments, proxies, blocked}
}

// badgeCount は「報告・承認待ち」の件数: 報告の question/blocked、代行依頼、blocked の agent、承認待ち(Q-<n>)。
func (v *sv) badgeCount() int {
	h := v.needsHuman()
	return len(h.comments) + len(h.proxies) + len(h.blocked) + len(v.pendingRequests(notifyMax))
}

// reqPendingSQL は承認待ち(Q-<n>)のうち、まだ決まっていないもの(tb approval list と同じ。internal/tbcli の reqPending)。
// 送信と agent の操作は元の行(outbox・agent_ops)の状態が正、コマンドは requests の状態が正。
const reqPendingSQL = `((r.source='cmd' AND r.status='pending')
  OR (r.source='outbox' AND EXISTS (SELECT 1 FROM outbox o WHERE o.id=r.source_id AND o.status='pending'))
  OR (r.source='agent_ops' AND EXISTS (SELECT 1 FROM agent_ops a WHERE a.id=r.source_id AND a.status='pending')))`

// pendingRequests は承認待ち(Q-<n>)。優先度の高い順、同じなら古い順(tb approval list と同じ)。
func (v *sv) pendingRequests(limit int) []row {
	return v.q("SELECT r.id, r.source, r.source_id, r.requester, r.op, r.target, r.priority, r.detail, r.created_at FROM requests r WHERE "+
		reqPendingSQL+" ORDER BY r.priority DESC, r.id LIMIT ?", limit)
}

// requestOf は元の行(outbox・agent_ops の id)の承認待ちの番号(Q-<n>)。無ければ ""。
func (v *sv) requestOf(source string, id any) string {
	if r := v.q("SELECT id FROM requests WHERE source=? AND source_id=?", source, id); len(r) > 0 {
		return "Q-" + pyStr(r[0]["id"])
	}
	return ""
}

func priorityLabel(p any) string {
	switch intOf(p) {
	case 2:
		return "high"
	case 0:
		return "low"
	}
	return "normal"
}

func (v *sv) agentOps(status string, limit int) []row {
	if status != "" {
		return v.q("SELECT * FROM agent_ops WHERE status=? ORDER BY id DESC LIMIT ?", status, limit)
	}
	return v.q("SELECT * FROM agent_ops ORDER BY id DESC LIMIT ?", limit)
}

func (v *sv) activityFor(a map[string]any) row {
	var rows []row
	if sid := agentSession(a); truthy(sid) {
		rows = v.q("SELECT * FROM agent_activity WHERE session_id=?", sid)
	}
	if len(rows) == 0 && truthy(a["pane_id"]) {
		rows = v.q("SELECT * FROM agent_activity WHERE pane_id=? ORDER BY updated_at DESC LIMIT 1", a["pane_id"])
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func stalled(a map[string]any, act row) bool {
	if a["agent_status"] != "working" || act == nil {
		return false
	}
	p, ok := parseTS(act["last_prompt_at"])
	if !ok {
		return false
	}
	if s, ok := parseTS(act["last_stop_at"]); ok && !s.Before(p) {
		return false
	}
	return nowFn().Sub(p) > stallMin*time.Minute
}

// ---- tb reminder publish の結果(notify_seen の reminders:last)

var remPrio = map[int]string{1: "急ぎ", 2: "人の番", 3: "停滞", 4: "待ち・予定"}

type reminder struct {
	p                             int
	label, status                 string
	day                           int
	key, what, who, due, ref, age string
}

type reminders struct {
	at      string
	full    bool
	removed int
	items   []reminder
}

// pyInt は JSON の値が Python で int として扱える(bool も含む)とき、その値。
func pyInt(x any) (int, bool) {
	switch t := x.(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// remP は Python の `p in {1: …}`(1.0 や true も 1 と同じ)。
func remP(x any) (int, bool) {
	if i, ok := pyInt(x); ok {
		_, in := remPrio[i]
		return i, in
	}
	if n, ok := x.(json.Number); ok {
		if f, err := n.Float64(); err == nil && f == float64(int(f)) {
			_, in := remPrio[int(f)]
			return int(f), in
		}
	}
	return 0, false
}

func (v *sv) remindersLast() *reminders {
	rows := v.q("SELECT value, updated_at FROM notify_seen WHERE key='reminders:last'")
	if len(rows) == 0 {
		return nil
	}
	s, ok := rows[0]["value"].(string)
	if !ok {
		return nil
	}
	d, ok := decodeObject(s)
	if !ok {
		return nil
	}
	list, ok := d["items"].([]any)
	if !ok {
		return nil
	}
	out := &reminders{at: short(d["at"], 40), full: d["full"] == true}
	if r, ok := d["removed"].([]any); ok {
		out.removed = len(r)
	}
	for _, x := range list {
		it, ok := x.(map[string]any)
		if !ok {
			continue
		}
		p, ok := remP(it["p"])
		st, _ := it["status"].(string)
		if !ok || (st != "new" && st != "changed" && st != "continuing") {
			continue
		}
		day := 1
		if dv, ok := pyInt(it["day"]); ok && dv > 0 {
			day = dv
		}
		out.items = append(out.items, reminder{p: p, label: remPrio[p], status: st, day: day,
			key: short(it["key"], 120), what: short(it["what"], 120), who: short(it["who"], 120),
			due: short(it["due"], 120), ref: short(it["ref"], 120), age: short(it["age"], 120)})
	}
	return out
}

var reTaskRef = regexp.MustCompile(`^T-[A-Za-z0-9_-]{1,80}$`)

// reminderHref は項目の行き先。無ければ ""。
func reminderHref(it reminder) string {
	k, ref := it.key, it.ref
	hasAny := func(ps ...string) bool {
		for _, p := range ps {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}
	switch {
	case hasAny("q:", "o:", "p:", "a:"):
		return "/inbox"
	case hasAny("l:"):
		return "/agents#ops"
	case hasAny("queue:"):
		return "/outbox"
	case hasAny("inbox:") && strings.HasPrefix(ref, "I-") && isDecimal(ref[2:]):
		return "/inbox?held=" + ref[2:]
	case hasAny("stall:", "due:T-") && reTaskRef.MatchString(ref):
		return "/tasks/" + pyQuote(ref, "")
	}
	return ""
}

// isDecimal は str.isdigit()(空は偽)。
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// ticketRef は re.search(r"\bT-[A-Za-z0-9_]+-[0-9]+\b", text)(\b は Python と同じく Unicode の単語の境目)。
func ticketRef(s string) string {
	isW := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }
	for i := 0; i+1 < len(s); i++ {
		if s[i] != 'T' || s[i+1] != '-' {
			continue
		}
		if i > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:i]); isW(r) {
				continue
			}
		}
		j := i + 2
		k := j
		for k < len(s) && (s[k] == '_' || s[k] >= 'A' && s[k] <= 'Z' || s[k] >= 'a' && s[k] <= 'z' || s[k] >= '0' && s[k] <= '9') {
			k++
		}
		if k == j || k >= len(s) || s[k] != '-' {
			continue
		}
		d := k + 1
		e := d
		for e < len(s) && s[e] >= '0' && s[e] <= '9' {
			e++
		}
		if e == d {
			continue
		}
		if e < len(s) {
			if r, _ := utf8.DecodeRuneInString(s[e:]); isW(r) {
				continue
			}
		}
		return s[i:e]
	}
	return ""
}

// urgentItems は 10 分のまとめ送信を待たずに知らせるもの(data.py の urgent_items)。
func (v *sv) urgentItems() []item {
	return cached(v.sess, "urgent", func() []item {
		h := v.needsHuman()
		var items []item
		add := func(it item) { items = append(items, it) }
		for _, c := range h.comments {
			from, tid := pyStr(c["from_name"]), pyStr(c["task_id"])
			label, how := "ブロック報告", fmt.Sprintf("止まっている原因を取り除くか、%s に指示を送る(手元で tb message send %s --task %s --text '指示')", from, from, tid)
			if c["kind"] == "question" {
				label, how = "質問", fmt.Sprintf("%s に答える: 手元で tb message send %s --kind answer --task %s --text '答え'", from, from, tid)
			}
			how += fmt.Sprintf("。済んだら「報告・承認待ち」の「対応済みにする」(手元なら tb resolve C-%s)", pyStr(c["id"]))
			add(item{"c:" + pyStr(c["id"]), "comment_" + pyStr(c["kind"]), label,
				short(tid+" "+or(c["task_title"], ""), 80), short(from+": "+pyStr(c["text"]), 200), short(how, 300),
				"/tasks/" + pyQuote(tid, ""), Clean(or(c["created_at"], ""))})
		}
		for _, r := range h.proxies {
			id := pyStr(r["id"])
			add(item{"p:" + id, "proxy_pending", "代行依頼(人の判断待ち)",
				short(pyStr(r["task_id"])+" "+or(r["effect"], ""), 80), short(pyStr(r["cmd"])+" / 理由: "+pyStr(r["why"]), 200),
				short("agent の代わりに人が実行するか決める。実行したら手元で tb proxy resolve "+id+" --status done --result '結果'、断るなら --status denied", 300),
				"/tasks/" + pyQuote(pyStr(r["task_id"]), ""), Clean(or(r["updated_at"], ""))})
		}
		for _, al := range v.q("SELECT * FROM alerts WHERE status='analyzed' AND severity IN ('critical','high') ORDER BY id DESC LIMIT 50") {
			id := pyStr(al["id"])
			add(item{"r:" + id + ":" + Clean(pyStr(al["analyzed_at"])), "alert_result", "アラート解析結果(" + Clean(pyStr(al["severity"])) + ")",
				short("A-"+id+" "+pyStr(al["title"]), 80), short(or(al["result_summary"], ""), 200),
				"全文は手元で tb alerts A-" + id + "。対応したら tb alert-close A-" + id, "/notifications", Clean(or(al["analyzed_at"], ""))})
		}
		for _, al := range v.q("SELECT * FROM alerts WHERE status='awaiting-approval' ORDER BY id DESC LIMIT 50") {
			id := pyStr(al["id"])
			add(item{"w:" + id, "alert_approval", "アラート(解析役の起動待ち)",
				short("A-"+id+" "+pyStr(al["title"]), 80), short(or(al["body"], ""), 200),
				"解析役を起動してよければ手元で tb alert-approve A-" + id + "(今後も自動なら --always)", "/notifications", Clean(or(al["last_seen"], ""))})
		}
		// 承認待ちは 1 種類(Q-<n>。送信・agent の操作・ask で止まったコマンド)。tb approval list と同じ順
		for _, r := range v.pendingRequests(notifyMax) {
			id := pyStr(r["id"])
			label, how, href := "承認待ち", "承認するなら手元で tb approval approve Q-"+id, "/approvals?tier=all&id=Q-"+id
			if p := priorityLabel(r["priority"]); p != "normal" {
				label += "(優先度 " + p + ")"
			}
			switch r["source"] {
			case "outbox", "agent_ops":
				how += "(今後も許可なら --always)"
			}
			how += "、却下は tb approval reject Q-" + id + "。Web UI の承認(Q)の画面からも決められる"
			if r["source"] == "agent_ops" {
				how += "(agent の操作は agent 一覧の「承認待ちの操作」からも)"
				href = "/agents#ops"
			}
			add(item{"q:" + id, "approval_pending", label,
				short(fmt.Sprintf("Q-%s %s → %s %s", id, pyStr(r["requester"]), pyStr(r["op"]), pyStr(r["target"])), 80), short(or(r["detail"], ""), 200),
				how, href, Clean(or(r["created_at"], ""))})
		}
		agents, _ := getAgents(v.sess)
		for _, a := range h.blocked {
			k := agentKey(a)
			body := a["terminal_title_stripped"]
			if !truthy(body) {
				body = a["terminal_title"]
			}
			add(item{"a:" + Clean(k), "agent_blocked", "承認待ちの agent", short(k, 80), short(or(body, "人の対応待ち"), 200),
				"agent の画面の「承認待ちの内容」で何を求めているか確かめ、その agent の herdr の pane で許可か拒否を選ぶ",
				"/agents/" + pyQuote(k, "") + "#approval", ""})
		}
		for _, a := range agents {
			act := v.activityFor(a)
			if stalled(a, act) {
				k := agentKey(a)
				add(item{"s:" + Clean(k) + ":" + Clean(pyStr(act["last_prompt_at"])), "agent_stalled", fmt.Sprintf("滞留(%d 分超)", stallMin), short(k, 80),
					short("working のまま。最終入力 "+pyStr(act["last_prompt_at"]), 200),
					"agent の画面で直近の出力を見て、止まっていれば pane で続きを指示する(要らなければ停止)",
					"/agents/" + pyQuote(k, ""), Clean(or(act["last_prompt_at"], ""))})
			}
		}
		// 通知の規則で届け先に webui を書いたチケットの通知。直近 webuiRuleHours 時間
		for _, ev := range v.q("SELECT id, ref, text, created_at FROM notify_events WHERE type='ticket' AND (' ' || sinks || ' ') LIKE '% webui %'"+
			" AND julianday(created_at) >= julianday('now') - ? / 24.0 ORDER BY id DESC LIMIT 50", webuiRuleHours) {
			text := Clean(or(ev["text"], ""))
			href := "/tasks"
			if m := ticketRef(text); m != "" {
				href = "/tasks/" + pyQuote(m, "")
			}
			add(item{"n:" + pyStr(ev["id"]), "ticket_rule", "チケット(通知の規則)",
				short(strings.Replace(text, "[チケット] ", "", 1), 80), short(text, 200), "規則は手元で tb notify rules。チケットを開いて確かめる",
				href, Clean(or(ev["created_at"], ""))})
		}
		if rem := v.remindersLast(); rem != nil {
			for _, it := range rem.items {
				if it.p != 1 {
					continue
				}
				href := reminderHref(it)
				if href == "" {
					href = "/inbox#reminders"
				}
				add(item{"rm:" + it.key + ":" + it.due, "reminder_due", "期限(忘れているかも)", short(it.what, 80),
					short(fmt.Sprintf("期限 %s / 誰の番: %s / %s", orStr(it.due, "-"), orStr(it.who, "-"), it.ref), 200),
					"期限を過ぎた・今日か明日が期限。片付けるか、期限を見直す(一覧は「報告・承認待ち」の「忘れているかも」)", href, it.due})
			}
		}
		for _, j := range failedJobs() {
			add(item{"j:" + j.name + ":" + Clean(pyStr(j.rec["ts"])), "job_failed", "定期実行の失敗", j.name, short(or(j.rec["reason"], "failed"), 200),
				"定期実行の画面で理由と設定を確かめる(記録は scheduler/logs/" + j.name + ".log)", "/jobs/" + pyQuote(j.name, "/"), Clean(or(j.rec["ts"], ""))})
		}
		if len(items) > notifyMax {
			items = items[:notifyMax]
		}
		return items
	})
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// pyJSONStr は json.dumps(s, ensure_ascii=False) と同じ文字列。
func pyJSONStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// notificationsJSON は /api/notifications の本文(handler.py と同じ json.dumps の形)。
func notificationsJSON(items []item) string {
	var b strings.Builder
	b.WriteString(`{"items": [`)
	for i, it := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		fields := [][2]string{{"id", it.ID}, {"kind", it.Kind}, {"label", it.Label}, {"title", it.Title}, {"body", it.Body},
			{"how", it.How}, {"href", it.Href}, {"ts", it.TS}}
		b.WriteByte('{')
		for j, f := range fields {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pyJSONStr(f[0]) + ": " + pyJSONStr(f[1]))
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(&b, `], "poll_sec": %d}`, NotifyPollSec)
	return b.String()
}
