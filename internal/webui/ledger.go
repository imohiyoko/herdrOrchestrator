package webui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ledger.go は送信履歴(tb の messages 表。読み取り専用。views.page_ledger と data.py の ledger_*)。

const ledgerMax = 100

var (
	ledgerResults = []string{"sent", "busy", "failed"}
	reTarget      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reDate        = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

func (v *sv) ledgerKinds() []string {
	var out []string
	for _, r := range v.q("SELECT DISTINCT kind FROM messages ORDER BY kind") {
		out = append(out, pyStr(r["kind"]))
	}
	return out
}

type ledgerFilter struct{ target, from, to, kind, result string }

func ledgerWhere(f ledgerFilter, hideFailed bool) (string, []any) {
	var w []string
	var a []any
	add := func(cond, val string) {
		if val != "" {
			w = append(w, cond)
			a = append(a, val)
		}
	}
	add("target=?", f.target)
	add("substr(created_at,1,10)>=?", f.from)
	add("substr(created_at,1,10)<=?", f.to)
	add("kind=?", f.kind)
	if f.result != "" {
		add("result=?", f.result)
	} else if hideFailed {
		w = append(w, "result!='failed'")
	}
	if len(w) == 0 {
		return "", a
	}
	return " WHERE " + strings.Join(w, " AND "), a
}

type ledgerRow struct{ kind, ref, target, result, createdAt, first string }

// ledgerRows は新しい順に最大 ledgerMax 件(result が空なら failed を除く)。本文は 1 行目の先頭 200 文字だけ。
func (v *sv) ledgerRows(f ledgerFilter, includeFailed bool) []ledgerRow {
	where, a := ledgerWhere(f, f.result == "" && !includeFailed)
	var out []ledgerRow
	for _, r := range v.q("SELECT id, kind, ref, target, body, result, created_at FROM messages"+where+" ORDER BY id DESC LIMIT ?", append(a, ledgerMax)...) {
		first, _, _ := strings.Cut(Clean(or(r["body"], "")), "\n")
		first = strings.TrimFunc(first, pyIsSpace)
		if utf8.RuneCountInString(first) > 200 {
			first = string([]rune(first)[:200]) + "…"
		}
		out = append(out, ledgerRow{Clean(pyStr(r["kind"])), Clean(or(r["ref"], "")), Clean(pyStr(r["target"])), Clean(pyStr(r["result"])),
			Clean(pyStr(r["created_at"])), first})
	}
	return out
}

func isOutboxRef(s string) bool {
	if !strings.HasPrefix(s, "O-") || len(s) == 2 {
		return false
	}
	for _, r := range s[2:] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func (s *Server) pageLedger(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true // 絞り込みの入力欄があるので自動更新しない
	v := q.data()
	f := ledgerFilter{c.one("target"), c.one("from"), c.one("to"), c.one("kind"), c.one("result")}
	var errs []string
	if f.target != "" && !reTarget.MatchString(f.target) {
		errs = append(errs, "宛先は英数字と . _ - だけです")
		f.target = ""
	}
	if f.from != "" && !reDate.MatchString(f.from) {
		errs = append(errs, "開始日は YYYY-MM-DD の形です")
		f.from = ""
	}
	if f.to != "" && !reDate.MatchString(f.to) {
		errs = append(errs, "終了日は YYYY-MM-DD の形です")
		f.to = ""
	}
	kinds := v.ledgerKinds()
	if f.kind != "" && !contains(kinds, f.kind) {
		errs = append(errs, "その種別は履歴にありません")
		f.kind = ""
	}
	showAll := f.result == "all"
	if showAll {
		f.result = ""
	}
	if f.result != "" && !contains(ledgerResults, f.result) {
		errs = append(errs, "結果は sent / busy / failed / all のどれかです")
		f.result = ""
	}
	curResult := f.result
	if curResult == "" && showAll {
		curResult = "all"
	}
	link := func(kind, result *string) string {
		d := [][2]string{{"target", f.target}, {"from", f.from}, {"to", f.to}, {"kind", f.kind}, {"result", curResult}}
		if kind != nil {
			d[3][1] = *kind
		}
		if result != nil {
			d[4][1] = *result
		}
		var kv [][2]string
		for _, p := range d {
			if p[1] != "" {
				kv = append(kv, p)
			}
		}
		return "/ledger?" + E(pyURLEncode(kv))
	}
	where, a := ledgerWhere(ledgerFilter{target: f.target, from: f.from, to: f.to}, false)
	summ := v.q("SELECT kind, result, COUNT(*) AS n FROM messages"+where+" GROUP BY kind, result ORDER BY kind, result", a...)
	type kr struct{ k, r string }
	sm := map[kr]int64{}
	seen := map[string]bool{}
	var kindsSeen []string
	for _, r := range summ {
		k := pyStr(r["kind"])
		n, _ := r["n"].(int64)
		sm[kr{k, pyStr(r["result"])}] = n
		if !seen[k] {
			seen[k] = true
			kindsSeen = append(kindsSeen, k)
		}
	}
	sort.Strings(kindsSeen)
	var rows [][]string
	empty := ""
	for _, k := range kindsSeen {
		k := k
		cells := []string{`<a href="` + link(&k, &empty) + `">` + E(k) + `</a>`}
		var total int64
		for _, rs := range ledgerResults {
			rs := rs
			n := sm[kr{k, rs}]
			total += n
			txt := strconv.FormatInt(n, 10)
			if rs == "failed" && n != 0 {
				txt = `<span class=warn>` + txt + `</span>`
			}
			if n != 0 {
				cells = append(cells, `<a href="`+link(&k, &rs)+`">`+txt+`</a>`)
			} else {
				cells = append(cells, "0")
			}
		}
		rows = append(rows, append(cells, strconv.FormatInt(total, 10)))
	}
	var failedTotal int64
	for key, n := range sm {
		if key.r == "failed" && (f.kind == "" || key.k == f.kind) {
			failedTotal += n
		}
	}
	body := "<h2>種別ごとの件数</h2>" + table([]string{"種別", "sent", "busy", "failed", "計"}, rows)
	var kopts strings.Builder
	kopts.WriteString(`<option value="">(すべて)</option>`)
	for _, k := range kinds {
		sel := ""
		if k == f.kind {
			sel = " selected"
		}
		kopts.WriteString(`<option value="` + E(k) + `"` + sel + `>` + E(k) + `</option>`)
	}
	var ropts strings.Builder
	for _, o := range [][2]string{{"", "failed 以外(既定)"}, {"all", "すべて"}, {"sent", "sent"}, {"busy", "busy"}, {"failed", "failed"}} {
		sel := ""
		if curResult == o[0] {
			sel = " selected"
		}
		ropts.WriteString(`<option value="` + o[0] + `"` + sel + `>` + o[1] + `</option>`)
	}
	form := `<form method=get action="/ledger"><p>` +
		`<label>種別 <select name=kind>` + kopts.String() + `</select></label> ` +
		`<label>結果 <select name=result>` + ropts.String() + `</select></label> ` +
		`<label>宛先 <input type=text name=target value="` + E(f.target) + `" size=16></label> ` +
		`<label>開始日 <input type=date name=from value="` + E(f.from) + `"></label> ` +
		`<label>終了日 <input type=date name=to value="` + E(f.to) + `"></label> ` +
		`<button type=submit>絞り込む</button> <a href="/ledger">解除</a></p></form>`
	var out [][]string
	for _, r := range v.ledgerRows(f, showAll) {
		ref := E(r.ref)
		if r.kind == "send" && isOutboxRef(r.ref) {
			ref = `<a href="/outbox">` + E(r.ref) + `</a>`
		}
		res := E(r.result)
		if r.result == "failed" {
			res = `<span class=warn>` + res + `</span>`
		}
		out = append(out, []string{E(r.createdAt), E(r.kind), ref, E(r.target), res, E(r.first)})
	}
	note := ""
	if f.result == "" && !showAll && failedTotal != 0 {
		failed, all := "failed", "all"
		note = fmt.Sprintf(`<p class=note>failed %d 件は畳んでいます。<a href="%s">failed だけを見る</a> / <a href="%s">すべて見る</a></p>`,
			failedTotal, link(nil, &failed), link(nil, &all))
	}
	eh := ""
	if len(errs) > 0 {
		eh = errHTML(strings.Join(errs, "。"))
	}
	body += "<h2>履歴</h2>" + eh + form + note + table([]string{"時刻(UTC)", "種別", "参照", "宛先", "結果", "本文(1 行目)"}, out)
	body += fmt.Sprintf("<p class=note>tb の messages 表(送ろうとしたものすべて)を新しい順に最大 %d 件。時刻と日付は UTC。"+
		"send の O-n は送信の依頼の状態(承認待ちなど)を見る <a href=\"/outbox\">送信</a> 画面に対応します。本文は 1 行目だけ(全文は表示しません)。</p>", ledgerMax)
	return "送信履歴", body
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
