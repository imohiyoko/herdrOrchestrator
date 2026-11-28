package webui

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appapproval "github.com/imohiyoko/herdrorchestrator/internal/app/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/app/server"
)

// approvals.go は Q チケット(requests の Q-<n>)の一覧(/approvals)と、その承認・却下(POST /approval-op)。
// allow / ask / deny のどれも 1 行の記録なので、誰が依頼し(requester)、何が・誰が決めたか(tier・rule・decided_by)をここで追う。
// 表示は 2 つ: 一覧(新しい順)とボード(状態ごとの列。スプリントのボードと同じ見た目)。
// 承認待ち(pending)には承認・却下、policy が拒否したもの(denied)には「例外として 1 回許可」のボタンを出す。
// 書き込みは API の approval.approve / reject(tb approval approve / reject Q-<n> と同じ操作。Web UI からは人の操作。deny の例外は API の側でも人だけ)。

const approvalsMax = 200 // 1 画面に出す最大件数

// approvalStatuses はボードの列の順(決まっていない → 決まった → 実行の結果 → 止めた・記録だけ)。
// ここに無い値(古い行など)は後ろに足す。
var approvalStatuses = []string{"pending", "approved", "running", "done", "failed", "rejected", "denied", "allowed"}

var approvalTiers = []string{"allow", "ask", "deny"}

var (
	reQID = regexp.MustCompile(`^Q-?([0-9]{1,12})$`)
)

type approvalFilter struct {
	view, tier, status, requester, by, id string
}

func (f approvalFilter) app() appapproval.Filter {
	return appapproval.Filter{Tier: f.tier, Status: f.status, Requester: f.requester, By: f.by, ID: f.id}
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
	body := approvalDone(c) + editErr(c)
	if len(errs) > 0 {
		body += errHTML(strings.Join(errs, "。"))
	}
	if v.conn() == nil {
		body += `<p class=err>タスク DB がありません(または開けません)</p>`
	}

	// 判定ごとの件数(判定以外の絞り込みを反映)と、状態ごとの件数(状態以外の絞り込みを反映)
	tcount := map[string]int64{}
	var tall int64
	for _, r := range appapproval.TierCounts(v.qChecked, f.app()) {
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
	scount := map[string]int64{}
	present := []string{}
	for _, r := range appapproval.StatusCounts(v.qChecked, f.app()) {
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

	rows := appapproval.Rows(v.qChecked, f.app(), approvalsMax)
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
	// outcome は承認のあとどうなったか(送信: sent など)。無ければ ""
	outcome := func(r row) string {
		o := pyStr(r["outcome"])
		if o == "" {
			return ""
		}
		what := "実行"
		if pyStr(r["source"]) == "outbox" {
			what = "送信"
		}
		if o == "denied" {
			o += "(届け方の規則で止まった)"
		}
		return `<span class=note>` + what + `: ` + E(o) + `</span>`
	}
	stCell := func(r row) string {
		return stHTML(pyStr(r["status"])) + ifs(outcome(r) != "", `<br>`+outcome(r))
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
	here := link(func(d *approvalFilter) {}) // 決めたあとに戻る先(いま見ている絞り込みのまま)
	decide := func(r row) string {
		if c.csrf == "" {
			return ""
		}
		id := strconv.FormatInt(intOf(r["id"]), 10)
		hid := `<input type=hidden name=csrf value="` + E(c.csrf) + `"><input type=hidden name=id value="` + id + `">` +
			`<input type=hidden name=back value="` + E(here) + `">`
		form := func(op, label, extra string) string {
			return `<form method=post action="/approval-op" style="display:inline">` + hid + `<input type=hidden name=op value=` + op + `>` +
				extra + `<button type=submit>` + label + `</button></form> `
		}
		switch pyStr(r["status"]) {
		case "pending":
			out := form("approve", "承認", "") + form("reject", "却下", "")
			if src := pyStr(r["source"]); src == "outbox" || src == "agent_ops" { // 送信と agent の操作だけ(tb approval approve --always と同じ)
				out += form("always", "承認して今後は承認なし", `<label class=note><input type=checkbox name=confirm value=1 required>policy に allow を書く</label> `)
			}
			return out
		case "denied":
			if pyStr(r["tier"]) == "deny" {
				return form("exception", "例外として 1 回許可", `<label class=note><input type=checkbox name=confirm value=1 required>policy の拒否を越えて許可する</label> `)
			}
		}
		return ""
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
					d := decide(r)
					cs = append(cs, `<div class=card>`+qlink(r)+` <span class=note>`+E(pyStr(r["tier"])+" / "+priorityLabel(r["priority"]))+`</span><br>`+
						E(short(pyStr(r["op"])+" "+pyStr(r["target"]), 60))+`<br><span class=note>`+E(pyStr(r["requester"]))+` → `+
						E(orStr(pyStr(r["decided_by"]), "-"))+`</span>`+ifs(outcome(r) != "", `<br>`+outcome(r))+ifs(d != "", `<br>`+d)+`</div>`)
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
			out = append(out, []string{qlink(r), E(pyStr(r["tier"])), stCell(r), req(pyStr(r["requester"])),
				E(short(pyStr(r["op"])+" "+pyStr(r["target"]), 80)), who(pyStr(r["decided_by"])),
				E(pyStr(r["created_at"])), E(orStr(pyStr(r["decided_at"]), "-")), more(r), decide(r)})
		}
		body += `<div class=tscroll>` + table([]string{"Q", "判定", "状態", "依頼者", "何を", "決めた人", "依頼(UTC)", "決定(UTC)", "", "操作"}, out) + `</div>`
	}
	body += fmt.Sprintf("<p class=note>tb が判定したもの(allow / ask / deny)はどれも Q-&lt;n&gt; として残ります。新しい順に最大 %d 件。"+
		"allow は policy が許可した記録(決めた人は policy)で、既定では畳んでいます。"+
		"送信・agent の操作は元の依頼の状態を出します。承認・却下はこの画面のボタンか、手元で <code>tb approval approve Q-&lt;n&gt;</code> / "+
		"<code>tb approval reject Q-&lt;n&gt;</code>(手元では <code>tb approval history</code> でも見られます)。"+
		"ask で止まったコマンドは、承認すると依頼した agent に知らせ、その agent が 1 回だけ実行します(依頼した agent がいなければ承認できません)。"+
		"deny は人だけが例外として 1 回だけ許可できます。今後も許可するときは policy を変えてください(<a href=\"/perms\">権限</a>)。</p>", approvalsMax)
	return "承認(Q チケット)", body
}

// postApprovalOp は POST /approval-op(Q-<n> の承認・却下・承認して今後は承認なし(--always)と、deny の例外の許可)。書き込みは API の approval.approve / reject。
func (s *Server) postApprovalOp(q *rq, f form) (string, string, string, func(pageCtx) (string, string, string)) {
	id := f["id"]
	// 戻り先はボタンを押した画面(絞り込みのまま)。/approvals の中だけ受ける
	back, qs := "/approvals", url.Values{}
	if b := f["back"]; strings.HasPrefix(b, "/approvals?") && !strings.ContainsAny(b, "\r\n#") {
		if v, err := url.ParseQuery(strings.TrimPrefix(b, "/approvals?")); err == nil {
			back, qs = b, v
		}
	}
	render := func(c pageCtx) (string, string, string) {
		c.qs = map[string][]string{}
		for k, v := range qs {
			c.qs[k] = v
		}
		t, b := s.pageApprovals(q, c)
		return t, b, "/approvals"
	}
	if !reOpID.MatchString(id) {
		return back, "", "番号が不正です", render
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	decided := back + sep + pyURLEncode([][2]string{{"decided", "Q-" + id}})
	if op := f["op"]; op != "approve" && op != "reject" && op != "exception" && op != "always" {
		return "", "", "", nil
	}
	cur := appapproval.StatusOf(q.data().qChecked, id)
	switch {
	case cur == "":
		return back, "", "Q-" + id + " が見つかりません", render
	case f["op"] == "exception" && cur != "denied":
		return back, "", "Q-" + id + " は policy が拒否したもの(denied)ではありません(" + cur + ")", render
	case (f["op"] == "approve" || f["op"] == "reject" || f["op"] == "always") && cur != "pending":
		return back, "", "Q-" + id + " は承認待ちではありません(" + cur + ")", render
	}
	if f["op"] == "exception" && f["confirm"] != "1" {
		return back, "", "例外として許可するには、確認のチェックを入れてください", render
	}
	if f["op"] == "always" && f["confirm"] != "1" {
		return back, "", "今後は承認なしにするには、確認のチェックを入れてください", render
	}
	q.v.close() // tb が書き込むので、読んだ接続は閉じて描き直しで開き直す
	q.v = nil
	// deny の例外も approve(API の側で人だけに限る)。always は承認して、今後は承認なし(policy に * <操作> <対象> = allow。送信と agent の操作だけ)
	op := "approval.approve"
	if f["op"] == "reject" {
		op = "approval.reject"
	}
	// 依頼者が知らせる相手でないとき(scheduler など)は承認した側(ここ)で実行するので、長めに待つ
	if msg := approvalCall(q.name, op, appapproval.DecideReq{ID: "Q-" + id, Always: f["op"] == "always"}); msg != "" {
		return back, "", msg, render
	}
	return decided, f["op"], "", render
}

// approvalCall は承認の操作を同じプロセスで人として呼ぶ(ADR 0008。tb approval approve / reject と同じ操作)。
// 承認したコマンドを依頼者として動かす子は、前と同じくこの checkout の tb(HERDR_SESSION と TB_DB はこの画面のもの)。
// 断られたか、実行したものが 0 以外で終わったら画面に出す理由(実行できたら空)。
func approvalCall(sess, op string, in appapproval.DecideReq) string {
	if sess != SessionName() {
		// 送信の配送・agent の操作は herdr を呼ぶ。herdr の呼び出し先はプロセスの環境で決まるので、ほかのセッションは
		// 前と同じく、HERDR_SESSION を渡した tb の子プロセスで行う(agentDecide と同じ)
		args := []string{"approval", strings.TrimPrefix(op, "approval."), in.ID}
		if in.Always {
			args = append(args, "--always")
		}
		if rc, out := runTB(sess, 60*time.Second, args...); rc != 0 {
			return tbFail(rc, out)
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := appEnv(sess)
	env.TbCmd = []string{"bash", tbPath()}
	for _, kv := range childEnv(sess) {
		if k, _, _ := strings.Cut(kv, "="); k != "HERDR_PANE_ID" && k != "TB_DB" {
			env.ChildEnv = append(env.ChildEnv, kv)
		}
	}
	env.ChildEnv = append(env.ChildEnv, "TB_DB="+TBDB())
	c, err := server.New(env)
	if err != nil {
		return "実行できませんでした(" + Clean(err.Error()) + ")"
	}
	var r appapproval.Result
	if err := c.Call(ctx, app.Human(), op, in, &r); err != nil {
		return refusedMsg(err)
	}
	if r.Exit != 0 {
		out := Clean(strings.TrimSpace(r.Out + r.Err))
		if utf8.RuneCountInString(out) > editOutMax {
			out = string([]rune(out)[:editOutMax]) + "…"
		}
		return "承認しましたが、終わりの状態が 0 ではありません(終了コード " + strconv.Itoa(r.Exit) + "): " + out
	}
	return ""
}

// approvalDone は決めたあとの知らせ(?done=approve|reject|exception&decided=Q-<n>)。
func approvalDone(c pageCtx) string {
	m, ok := doneMsg[c.one("done")]
	if !ok {
		return ""
	}
	if d := c.one("decided"); reQID.MatchString(d) {
		m = d + ": " + m
	}
	return "<p class=note>" + E(m) + "</p>"
}
