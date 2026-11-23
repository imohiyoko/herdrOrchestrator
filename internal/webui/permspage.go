package webui

import (
	"fmt"
	"strconv"
	"strings"
)

// permspage.go は権限(/perms。設定 policy の読み取り。views.page_perms と _catalog_section)。変更操作は置かない。

func sel(on bool, s string) string {
	if on {
		return s
	}
	return ""
}

func (s *Server) catalogSection(c pageCtx, p *perms, err string, catalog []catEntry, cerr string, cbad int) string {
	if cerr != "" {
		return "<h2>操作ごとの現在の権限</h2>" + errHTML(cerr)
	}
	if err != "" {
		return "<h2>操作ごとの現在の権限</h2>" + errHTML("policy を全部は読めていないので、現在の値は出しません")
	}
	fKind, fState := c.one("kind"), c.one("state")
	fFb, fAl := c.one("fallback") == "1", c.one("allowed") == "1"
	if !contains(catKinds, fKind) {
		fKind = ""
	}
	if !contains(catStates, fState) {
		fState = ""
	}
	var rows []catRow
	for _, r := range p.catalogRows(catalog) {
		e := r.entry
		if fKind != "" && e.kind != fKind || fState != "" && e.state != fState {
			continue
		}
		if fFb && r.explicit || fAl && r.value != "allow" {
			continue
		}
		rows = append(rows, r)
	}
	opts := func(xs []string, cur string) string {
		o := `<option value="">(すべて)</option>`
		for _, k := range xs {
			o += `<option value="` + k + `"` + sel(k == cur, " selected") + `>` + k + `</option>`
		}
		return o
	}
	form := `<form method=get action="/perms"><p>` +
		`<label>種別 <select name=kind>` + opts(catKinds, fKind) + `</select></label> <label>状態 <select name=state>` + opts(catStates, fState) + `</select></label> ` +
		`<label><input type=checkbox name=fallback value=1` + sel(fFb, " checked") + `> 既定に落ちているものだけ</label> ` +
		`<label><input type=checkbox name=allowed value=1` + sel(fAl, " checked") + `> allow になっているものだけ</label> ` +
		`<button type=submit>絞り込む</button> <a href="/perms">解除</a></p></form>`
	var out [][]string
	for _, r := range rows {
		c := r.entry
		dim := c.state == "reserved" || c.state == "unwired"
		cell := func(x string) string {
			if dim {
				return `<span class=note>` + x + `</span>`
			}
			return x
		}
		var val, via, how string
		if r.value == "unknown" {
			val = `<b class=warn>判定不能</b>`
			via = strconv.Itoa(r.rule.no) + ` 行目(<code>[</code> を含む)`
			how = "-"
		} else {
			val = E(r.value)
			if r.value == "allow" {
				val = `<b class=warn>allow</b>`
			}
			if r.rule == nil {
				via, how = `なし(既定の表 perms/ops.tbl の `+E(r.value)+`)`, "既定に落ちる"
			} else {
				via, how = strconv.Itoa(r.rule.no)+` 行目 <code>`+E(r.rule.pattern)+` = `+E(r.rule.effect)+`</code>`, "明示"
			}
		}
		var warns []string
		if c.state == "unwired" {
			warns = append(warns, "承認判定に渡らない操作です。policy の行は効きません(代行依頼は別の仕組み)")
		}
		if r.risky {
			warns = append(warns, `リスク `+E(c.risk)+` / allow 候補 `+E(c.allowOK)+` の操作が allow になっています`)
		}
		if r.riskyExc {
			warns = append(warns, "allow にしない操作に、宛先を絞った allow の例外があります")
		}
		var ex []string
		for i, x := range r.exceptions {
			if i == 4 {
				break
			}
			ex = append(ex, `<code>`+E(x.pattern)+` = `+E(x.effect)+`</code>`)
		}
		exc := strings.Join(ex, " ")
		risk := E(c.risk)
		if c.risk == "high" || c.risk == "human-only" {
			risk = `<b class=warn>` + risk + `</b>`
		}
		last := exc
		if exc != "" && len(warns) > 0 {
			last += "<br>"
		}
		for _, w := range warns {
			last += `<span class=warn>` + w + `</span>`
		}
		out = append(out, []string{cell(`<code>` + E(c.pattern) + `</code>`), cell(E(c.kind)), cell(E(c.desc)), val, cell(how), cell(via),
			cell(risk), cell(E(c.side)), cell(E(c.allowOK)), cell(E(c.state)), last})
	}
	note := fmt.Sprintf("<p class=note>権限カタログ(<code>taskboard/config/approvals-catalog.conf</code>)の %d 件のうち %d 件。", len(catalog), len(rows)) +
		"現在の値は、パターンの <code>*</code> を例の名前に置いて、policy の主体 <code>*</code> の行を当てた結果です" +
		"(当たる行のいちばん厳しい効果。無ければ既定の表。主体や宛先を絞った行は「例外」の欄に出ます)。reserved(将来用で未実装)と unwired(操作名はあるが承認判定には渡らない)は薄く表示。" +
		"カタログは操作の説明で、判定には使いません。"
	if cbad != 0 {
		note += fmt.Sprintf(" 形式が不正でスキップした行が %d 件あります。", cbad)
	}
	note += "</p>"
	return "<h2>操作ごとの現在の権限(カタログ × policy)</h2>" + form +
		table([]string{"操作", "種別", "説明", "現在の値", "規則の有無", "決まる行", "リスク", "副作用", "allow 候補", "状態", "例外・警告"}, out) + note
}

func (v *sv) approvalHistory(limit int) []row {
	return v.q("SELECT id, pattern, value, created_at FROM approval_changes ORDER BY id DESC LIMIT ?", limit)
}

func (s *Server) pagePerms(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true
	v := q.data()
	rules, err := v.loadRules()
	p := &perms{v: v, rules: rules, ops: loadOps()}
	op := c.one("op")
	body := doneNotice(c) + editErr(c)
	if err != "" {
		body += errHTML(err)
	}
	// 「この操作はどの行で決まるか」
	form := `<form method=get action="/perms"><p><label>操作名 ` +
		`<input type=text name=op value="` + E(op) + `" size=36 placeholder="send:task:worker1"></label> ` +
		`<button type=submit>どの行で決まるか調べる</button></p></form>`
	res := ""
	var hit *rule
	if op != "" {
		switch {
		case !reAction.MatchString(op):
			res = errHTML("操作名に使えない文字があります(英数字と . _ : * ? - だけ、128 文字まで)")
		case err != "":
			res = errHTML("policy を全部は読めていないので、判定しません")
		default:
			var val string
			val, hit = p.decide(op)
			switch {
			case val == "unknown":
				res = `<p><code>` + E(op) + `</code> → <b class=warn>判定不能</b>(` + strconv.Itoa(hit.no) + ` 行目 <code>` + E(hit.pattern) + `</code> に <code>[</code> があり、` +
					`bash と照合規則が違いうるため、この画面では判定しません。手元で <code>tb approval check</code> を使ってください)</p>`
			case hit != nil:
				res = `<p><code>` + E(op) + `</code> → <b class="` + sel(val == "allow", "warn") + `">` + E(val) + `</b>` +
					`(<b>` + strconv.Itoa(hit.no) + ` 行目</b>: <code>` + E(hit.pattern) + ` = ` + E(hit.effect) + `</code> で決まる)</p>`
			default:
				res = `<p><code>` + E(op) + `</code> → <b>` + E(val) + `</b>(当たる行が無いので既定の表 perms/ops.tbl)</p>`
			}
		}
	}
	body += "<h2>この操作はどの行で決まるか</h2>" + form + res +
		`<p class=note>tb の <code>tb approval check &lt;操作&gt;</code>(<code>--by</code> なし)と同じ規則(主体 <code>*</code> の行のうち、当たる行のいちばん厳しい効果。` +
		`無ければ既定の表)をここで再現した結果です。関係(<code>~child</code> など)の行は確かめず、締める向きにだけ数えます。` +
		`実際に tb を呼んだり、何かを書き換えたりはしません。リスク評価(<code>tb approval check &lt;操作&gt; --to 宛先 --text 本文</code>)は` +
		`本文が要るので、ここでは出しません。</p>`
	// 権限カタログ × policy(操作ごとの現在の値)
	catalog, cerr, cbad := loadCatalog()
	body += s.catalogSection(c, p, err, catalog, cerr, cbad)
	// 一覧
	lastChg := map[string]row{}
	for _, h := range v.approvalHistory(200) {
		k := policyRow(pyStr(h["value"]), pyStr(h["pattern"]))
		k = k[:strings.LastIndex(k, " = ")] // tb approval allow|ask が書いた行
		if _, ok := lastChg[k]; !ok {
			lastChg[k] = h
		}
	}
	var rows [][]string
	exm := p.examples()
	for _, r := range rules {
		var notes []string
		if w := permWarning(r); w != "" {
			notes = append(notes, `<span class=warn>`+E(w)+`</span>`)
		}
		if len(catalog) > 0 && err == "" && ruleInert(r, catalog) {
			notes = append(notes, `<span class=warn>この規則は何も効きません(当たる操作はカタログで「承認判定に渡らない」とされています)</span>`)
		}
		if sh := p.stricterOver(r); sh != nil {
			notes = append(notes, `<span class=warn>もっと厳しい `+strconv.Itoa(sh.no)+` 行目(<code>`+E(sh.pattern)+` = `+E(sh.effect)+`</code>)が重なるため、`+
				`そこではこの行は効きません(厳しい方が勝つ)</span>`)
		}
		if r.subject == "human" || strings.HasPrefix(r.subject, "system:") {
			notes = append(notes, `<span class=warn>この主体の行はまだ効きません(人は常に通る。system は ADR 0007 の段 1 から)</span>`)
		}
		chg := E(r.note)
		if h, ok := lastChg[r.raw]; ok {
			if chg != "" {
				chg += " / "
			}
			chg += `履歴: ` + ev(h["created_at"]) + ` に ` + ev(h["value"])
		}
		if chg == "" {
			chg = `<span class=note>(注記なし)</span>`
		}
		val := E(r.effect)
		if r.effect == "allow" {
			val = `<b class=warn>` + val + `</b>`
		}
		mark := sel(hit == r, ` style="background:#fff8c5"`)
		var ex []string
		for i, x := range exm[r] {
			if i == 8 {
				break
			}
			ex = append(ex, `<code>`+E(x)+`</code>`)
		}
		exs := strings.Join(ex, " ")
		if exs == "" {
			exs = `<span class=note>(例なし)</span>`
		}
		rows = append(rows, []string{strconv.Itoa(r.no), `<code` + mark + `>` + E(r.pattern) + `</code>`, val, chg, exs, strings.Join(notes, "<br>")})
	}
	body += "<h2>ルール一覧(当たる行のいちばん厳しい効果が勝つ。順番は関係ない)</h2>" +
		table([]string{"番号", "主体 操作 対象", "効果", "最後の変更", "この行で決まる操作の例", "警告"}, rows)
	body += `<p class=note>元ファイル: <code>` + E(v.confPathLabel()) + `</code>。「最後の変更」は行末の注記と、tb が残した変更履歴(approval_changes)です。` +
		`当たる行が無ければ既定の表(taskboard/perms/ops.tbl)。ワイルドカードは <code>*</code> と <code>?</code>。</p>`
	if len(catalog) > 0 && err == "" {
		unk := unknownRules(rules, catalog)
		body += "<h2>未知の規則(カタログに無い)</h2>"
		if len(unk) > 0 {
			var ur [][]string
			for _, r := range unk {
				ur = append(ur, []string{strconv.Itoa(r.no), `<code>` + E(r.pattern) + `</code>`, E(r.effect)})
			}
			body += table([]string{"番号", "主体 操作 対象", "効果"}, ur)
		} else {
			body += "<p class=note>(なし)カタログに無い規則は見つかりませんでした。</p>"
		}
		body += "<p class=note>policy にあるが、権限カタログのどの操作とも重ならない規則です(コマンドの権限 cmd: の行は除く)。" +
			"操作名の書き間違いか、カタログへの追記漏れの可能性があります。</p>"
	}
	// 変更履歴
	var hr [][]string
	for _, h := range v.approvalHistory(50) {
		hr = append(hr, []string{ev(h["created_at"]), `<code>` + ev(h["pattern"]) + `</code>`, ev(h["value"])})
	}
	body += "<h2>変更履歴(直近 50 件)</h2>" + table([]string{"時刻(UTC)", "パターン", "値"}, hr)
	body += `<p class=note>tb approval allow / ask が残した履歴です。誰が変えたか・変更前の値は記録されません(tb にその欄がありません)。</p>`
	// コマンドの組み立て
	pat, val := c.one("pat"), c.one("val")
	cmdHTML := ""
	if pat != "" || val != "" {
		cmd := tbCommand(val, pat)
		if cmd == "" {
			cmdHTML = errHTML("パターンは英数字と . _ : * ? - だけ(128 文字まで)、値は allow か ask です")
		} else {
			var warns []string
			row := policyRow(val, pat)
			op_, rest, _ := strings.Cut(row[2:], " ")
			tgt, _, _ := strings.Cut(rest, " = ")
			eff := "ask"
			if val == "allow" {
				eff = "allow"
			}
			probe := &rule{subject: "*", op: op_, target: tgt, effect: eff}
			if w := permWarning(probe); val == "allow" && pat == "*" {
				warns = append(warns, "全体を allow にします(どの agent の、どの操作も承認なし)")
			} else if w != "" {
				warns = append(warns, E(w))
			}
			warns = append(warns, `設定 policy に <code>`+E(row)+`</code> の行を書きます(主体・操作・対象が同じ行があれば、その行を書き換えます)`)
			if val == "allow" {
				if sh := p.stricterOver(probe); sh != nil {
					warns = append(warns, `もっと厳しい `+strconv.Itoa(sh.no)+` 行目(<code>`+E(sh.pattern)+` = `+E(sh.effect)+`</code>)が重なるため、そこでは allow になりません`)
				}
			}
			cmdHTML = `<p>人が<b>自分の端末で</b>実行するコマンド行です(この画面では実行しません):</p><pre>` + E(cmd) + `</pre>`
			for _, x := range warns {
				cmdHTML += `<p class=warn>` + x + `</p>`
			}
		}
	}
	patForm := `<form method=get action="/perms"><p>` +
		`<label>パターン <input type=text name=pat value="` + E(pat) + `" size=30 placeholder="send:task:worker1"></label> ` +
		`<label>値 <select name=val><option value=allow` + sel(val == "allow", " selected") + `>allow(承認なしで進めてよい)</option>` +
		`<option value=ask` + sel(val == "ask", " selected") + `>ask(承認が要る)</option></select></label> ` +
		`<button type=submit>コマンド行を組み立てる</button></p></form>`
	body += "<h2>変更したいとき</h2>" + patForm + cmdHTML +
		"<p class=note>上のコマンド行は表示だけです(人が端末で <code>tb approval allow</code> / <code>tb approval ask</code> を実行します)。" +
		"policy の行をまとめて書き換えるなら、下の「policy を編集する」か、手元の <code>tb config edit policy</code>。" +
		"広い allow(例: <code>agent:*</code>)は、agent が自分の承認権限を広げる経路になりえます。</p>"
	return "権限(policy)", body + v.policyForm(c)
}
