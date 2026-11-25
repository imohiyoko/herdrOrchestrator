package webui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// approvals.go は Q チケット(requests の Q-<n>)の一覧(/approvals)。読み取りのみ。
// allow / ask / deny のどれも 1 行の記録なので、誰が依頼し(requester)、何が・誰が決めたか(tier・rule・decided_by)をここで追う。
// 表示は 2 つ: 一覧(新しい順)とボード(状態ごとの列。スプリントのボードと同じ見た目)。
// 承認・却下のボタンはない(承認は人が手元で tb approval approve Q-<n>)。

const approvalsMax = 200 // 1 画面に出す最大件数

// approvalStatuses はボードの列の順(決まっていない → 決まった → 実行の結果 → 止めた・記録だけ)。
// 送信・agent の操作は元の行(outbox・agent_ops)の状態を出すので、ここに無い値は後ろに足す。
var approvalStatuses = []string{"pending", "approved", "running", "done", "failed", "rejected", "denied", "allowed"}

var approvalTiers = []string{"allow", "ask", "deny"}

var (
	reQID = regexp.MustCompile(`^Q-?([0-9]{1,12})$`)
)

// approvalStatusSQL は Q-<n> の今の状態(tb approval history と同じ: 送信・agent の操作は元の行の状態)。
const approvalStatusSQL = `CASE r.source
  WHEN 'outbox' THEN COALESCE((SELECT o.status FROM outbox o WHERE o.id=r.source_id), r.status)
  WHEN 'agent_ops' THEN COALESCE((SELECT a.status FROM agent_ops a WHERE a.id=r.source_id), r.status)
  ELSE r.status END`

type approvalFilter struct {
	view, tier, status, requester, by, id string
}

// where は絞り込みの条件。tier が空なら allow を除く(allow は tb を叩くたびに残るので既定では畳む)。
func (f approvalFilter) where(withStatus bool) (string, []any) {
	var w []string
	var a []any
	switch f.tier {
	case "":
		w = append(w, "r.tier<>'allow'")
	case "all":
	default:
		w = append(w, "r.tier=?")
		a = append(a, f.tier)
	}
	if f.requester != "" {
		w = append(w, "r.requester=?")
		a = append(a, f.requester)
	}
	if f.by != "" {
		w = append(w, "COALESCE(r.decided_by,'')=?")
		a = append(a, f.by)
	}
	if f.id != "" {
		w = append(w, "r.id=?")
		a = append(a, f.id)
	}
	if withStatus && f.status != "" {
		w = append(w, "("+approvalStatusSQL+")=?")
		a = append(a, f.status)
	}
	if len(w) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(w, " AND "), a
}

func (s *Server) pageApprovals(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true // 絞り込みの入力欄があるので自動更新しない
	v := q.data()
	f := approvalFilter{c.one("view"), c.one("tier"), c.one("status"), c.one("requester"), c.one("by"), c.one("id")}
	var errs []string
	if f.view != "board" {
		f.view = ""
	}
	if f.tier != "" && f.tier != "all" && !contains(approvalTiers, f.tier) {
		errs = append(errs, "判定は allow / ask / deny / all のどれかです")
		f.tier = ""
	}
	if f.status != "" && !reName.MatchString(f.status) {
		errs = append(errs, "状態は英数字と . _ - だけです")
		f.status = ""
	}
	if f.requester != "" && !reName.MatchString(f.requester) {
		errs = append(errs, "依頼者は英数字と . _ - だけです")
		f.requester = ""
	}
	if f.by != "" && !reName.MatchString(f.by) {
		errs = append(errs, "決めた人は英数字と . _ - だけです")
		f.by = ""
	}
	if f.id != "" {
		if m := reQID.FindStringSubmatch(f.id); m != nil {
			f.id = m[1]
		} else {
			errs = append(errs, "番号は Q-<番号> です")
			f.id = ""
		}
	}
	link := func(mod func(d *approvalFilter)) string {
		d := f
		mod(&d)
		var kv [][2]string
		for _, p := range [][2]string{{"view", d.view}, {"tier", d.tier}, {"status", d.status}, {"requester", d.requester}, {"by", d.by}} {
			if p[1] != "" {
				kv = append(kv, p)
			}
		}
		if d.id != "" {
			kv = append(kv, [2]string{"id", "Q-" + d.id})
		}
		if len(kv) == 0 {
			return "/approvals"
		}
		return "/approvals?" + pyURLEncode(kv)
	}
	body := ""
	if len(errs) > 0 {
		body += errHTML(strings.Join(errs, "。"))
	}
	if v.conn() == nil {
		body += `<p class=err>タスク DB がありません(または開けません)</p>`
	}

	// 判定ごとの件数(判定以外の絞り込みを反映)と、状態ごとの件数(状態以外の絞り込みを反映)
	tf := f
	tf.tier = "all"
	tw, ta := tf.where(true)
	tcount := map[string]int64{}
	var tall int64
	for _, r := range v.q("SELECT r.tier, COUNT(*) AS n FROM requests r"+tw+" GROUP BY r.tier", ta...) {
		tcount[pyStr(r["tier"])] = intOf(r["n"])
		tall += intOf(r["n"])
	}
	var tabs strings.Builder
	for _, o := range [][2]string{{"", "allow 以外"}, {"all", "すべて"}, {"allow", "allow"}, {"ask", "ask"}, {"deny", "deny"}} {
		o := o
		n := tall - tcount["allow"]
		switch o[0] {
		case "all":
			n = tall
		case "allow", "ask", "deny":
			n = tcount[o[0]]
		}
		tabs.WriteString(`<a class="pill` + sel(f.tier == o[0], " on") + `" href="` + E(link(func(d *approvalFilter) { d.tier = o[0] })) + `">` +
			E(o[1]) + ` ` + strconv.FormatInt(n, 10) + `</a>`)
	}
	sw, sa := f.where(false)
	scount := map[string]int64{}
	present := []string{}
	for _, r := range v.q("SELECT "+approvalStatusSQL+" AS st, COUNT(*) AS n FROM requests r"+sw+" GROUP BY st", sa...) {
		k := pyStr(r["st"])
		scount[k] = intOf(r["n"])
		present = append(present, k)
	}
	cols := append([]string(nil), approvalStatuses...)
	for _, k := range present {
		if !contains(cols, k) {
			cols = append(cols, k)
		}
	}
	var stabs strings.Builder
	stabs.WriteString(`<a class="pill` + sel(f.status == "", " on") + `" href="` + E(link(func(d *approvalFilter) { d.status = "" })) + `">(すべての状態)</a>`)
	for _, k := range cols {
		if scount[k] == 0 && k != f.status {
			continue
		}
		k := k
		stabs.WriteString(`<a class="pill` + sel(f.status == k, " on") + `" href="` + E(link(func(d *approvalFilter) { d.status = k })) + `">` +
			E(k) + ` ` + strconv.FormatInt(scount[k], 10) + `</a>`)
	}
	var vtabs strings.Builder
	for _, o := range [][2]string{{"", "一覧"}, {"board", "ボード"}} {
		o := o
		vtabs.WriteString(`<a class="pill` + sel(f.view == o[0], " on") + `" href="` + E(link(func(d *approvalFilter) { d.view = o[0] })) + `">` + o[1] + `</a>`)
	}
	body += `<div class=tabs>` + vtabs.String() + `<span class=sep></span>` + tabs.String() + `</div>` +
		`<div class=tabs>` + stabs.String() + `</div>`

	hidden := func(name, val string) string {
		if val == "" {
			return ""
		}
		return `<input type=hidden name=` + name + ` value="` + E(val) + `">`
	}
	qid := ""
	if f.id != "" {
		qid = "Q-" + f.id
	}
	body += `<form method=get action="/approvals"><p>` + hidden("view", f.view) + hidden("tier", f.tier) + hidden("status", f.status) +
		`<label>番号 <input type=text name=id value="` + E(qid) + `" size=8 placeholder="Q-12"></label> ` +
		`<label>依頼者 <input type=text name=requester value="` + E(f.requester) + `" size=14></label> ` +
		`<label>決めた人 <input type=text name=by value="` + E(f.by) + `" size=14 placeholder="human / policy / agent"></label> ` +
		`<button type=submit>絞り込む</button> <a href="/approvals">解除</a></p></form>`

	w, a := f.where(true)
	rows := v.q("SELECT r.id, r.source, r.source_id, r.tier, "+approvalStatusSQL+" AS status, r.requester, r.op, r.target, r.priority, "+
		"COALESCE(r.decided_by,'') AS decided_by, COALESCE(r.rule,'') AS rule, r.created_at, COALESCE(r.decided_at,'') AS decided_at, "+
		"r.detail, COALESCE(r.result,'') AS result FROM requests r"+w+" ORDER BY r.id DESC LIMIT ?", append(a, approvalsMax)...)
	qlink := func(r row) string {
		id := pyStr(r["id"])
		return `<a href="` + E(link(func(d *approvalFilter) { *d = approvalFilter{view: f.view, tier: "all", id: id} })) + `">Q-` + E(id) + `</a>`
	}
	who := func(name string) string {
		if name == "" {
			return "-"
		}
		return `<a href="` + E(link(func(d *approvalFilter) { d.by, d.id = name, "" })) + `">` + E(name) + `</a>`
	}
	req := func(name string) string {
		return `<a href="` + E(link(func(d *approvalFilter) { d.requester, d.id = name, "" })) + `">` + E(name) + `</a>`
	}
	stHTML := func(st string) string {
		switch st {
		case "pending":
			return `<span class=warn>` + E(st) + `</span>`
		case "failed", "rejected", "denied":
			return `<span class=st-blocked>` + E(st) + `</span>`
		case "done", "allowed":
			return `<span class=st-done>` + E(st) + `</span>`
		}
		return E(st)
	}
	more := func(r row) string {
		var parts []string
		if d := pyStr(r["detail"]); d != "" {
			parts = append(parts, `<pre>`+E(short(d, 2000))+`</pre>`)
		}
		if rl := pyStr(r["rule"]); rl != "" {
			parts = append(parts, `<span class=note>当たった policy: `+E(rl)+`</span>`)
		}
		if res := pyStr(r["result"]); res != "" {
			parts = append(parts, `<span class=note>結果:</span><pre>`+E(short(res, 2000))+`</pre>`)
		}
		if len(parts) == 0 {
			return "-"
		}
		return `<details><summary>中身</summary>` + strings.Join(parts, "") + `</details>`
	}
	if f.view == "board" {
		var heads, cells []string
		for _, col := range cols {
			if f.status != "" && col != f.status {
				continue
			}
			var cs []string
			for _, r := range rows {
				if pyStr(r["status"]) == col {
					cs = append(cs, `<div class=card>`+qlink(r)+` <span class=note>`+E(pyStr(r["tier"])+" / "+priorityLabel(r["priority"]))+`</span><br>`+
						E(short(pyStr(r["op"])+" "+pyStr(r["target"]), 60))+`<br><span class=note>`+E(pyStr(r["requester"]))+` → `+
						E(orStr(pyStr(r["decided_by"]), "-"))+`</span></div>`)
				}
			}
			if len(cs) == 0 && scount[col] == 0 {
				continue
			}
			heads = append(heads, E(col)+" ("+strconv.Itoa(len(cs))+")")
			cells = append(cells, orStr(strings.Join(cs, ""), "-"))
		}
		if len(heads) == 0 {
			body += table(nil, nil)
		} else {
			body += table(heads, [][]string{cells})
		}
	} else {
		var out [][]string
		for _, r := range rows {
			out = append(out, []string{qlink(r), E(pyStr(r["tier"])), stHTML(pyStr(r["status"])), req(pyStr(r["requester"])),
				E(short(pyStr(r["op"])+" "+pyStr(r["target"]), 80)), who(pyStr(r["decided_by"])),
				E(pyStr(r["created_at"])), E(orStr(pyStr(r["decided_at"]), "-")), more(r)})
		}
		body += `<div class=tscroll>` + table([]string{"Q", "判定", "状態", "依頼者", "何を", "決めた人", "依頼(UTC)", "決定(UTC)", ""}, out) + `</div>`
	}
	body += fmt.Sprintf("<p class=note>tb が判定したもの(allow / ask / deny)はどれも Q-&lt;n&gt; として残ります。新しい順に最大 %d 件。"+
		"allow は policy が許可した記録(決めた人は policy)で、既定では畳んでいます。"+
		"送信・agent の操作は元の依頼の状態を出します。承認・却下は手元で <code>tb approval approve Q-&lt;n&gt;</code> / "+
		"<code>tb approval reject Q-&lt;n&gt;</code>(手元では <code>tb approval history</code> でも見られます)。この画面からは操作できません。</p>", approvalsMax)
	return "承認(Q チケット)", body
}
