package webui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// jobspage.go は定期実行の画面(views.page_jobs / _job_overview / page_job)。変更できるのはスケジュールだけ。

func nonEmpty(m map[string]any) bool { return len(m) > 0 }

func lastText(last map[string]any) string {
	return ev(last["ts"]) + " " + ev(last["action"]) + " " + ev(last["reason"])
}

func ranText(lr map[string]any) string {
	s := ev(lr["ts"]) + " / " + ev(lr["action"])
	if x, ok := lr["exit"]; ok {
		s += " (exit " + ev(x) + ")"
	}
	return s
}

func (s *Server) pageJobs(q *rq, c pageCtx) (string, string) {
	v := q.data()
	names := v.jobNames()
	inNames := map[string]bool{}
	all := map[string]bool{}
	for _, n := range names {
		inNames[n], all[n] = true, true
	}
	if ents, err := os.ReadDir(SchedLogs()); err == nil {
		for _, e := range ents {
			if n, ok := strings.CutSuffix(e.Name(), ".log"); ok && JobRE.MatchString(n) {
				all[n] = true
			}
		}
	}
	var rows [][]string
	for _, n := range sortedKeys(all) {
		var info *jobInfo
		err := "DB に定義がない"
		if inNames[n] {
			info, err = v.jobInfo(n)
		}
		last, lr := jobLast(n)
		var sched, st, what string
		if info != nil {
			sched = E(intervalText(info)) + "おき / " + E(info.start) + "-" + E(info.end) + " / " + E(info.days)
			if info.at != "" {
				sched = "1 回だけ " + E(info.at)
			}
			if info.overridden {
				sched += " <span class=note>(Web UI で変更済み)</span>"
			}
			st = `<span class=warn>一時停止</span>`
			if info.enabled {
				st = "有効"
			}
			if info.kind == "prompt" {
				what = `<b>agent</b> <code>` + E(info.target) + `</code> に送る`
			} else {
				d, _ := v.jobDefs(n)
				what = "<b>コマンド</b> を実行"
				if cs := commandSummary(d.get("COMMAND")); len(cs) > 0 {
					var cc []string
					for _, c := range cs {
						cc = append(cc, `<code>`+E(c[0])+`</code>`)
					}
					what = "<b>コマンド</b> " + strings.Join(cc, " ")
				}
			}
			if summ := v.jobSummary(n, info); summ != "" {
				what += `<br><span class=note>` + E(summ) + `</span>`
			}
		} else {
			sched, st, what = `<span class=warn>`+E(err)+`</span>`, "-", "-"
		}
		link := E(n)
		if inNames[n] {
			link = `<a href="/jobs/` + pyQuote(n, "/") + `">` + E(n) + `</a>`
		}
		lt, rt := "-", "-"
		if nonEmpty(last) {
			lt = lastText(last)
		}
		if nonEmpty(lr) {
			rt = ranText(lr)
		}
		rows = append(rows, []string{link, what, sched, st, lt, rt})
	}
	note := ""
	var left []string
	for _, n := range v.unimportedJobs() {
		if !inNames[n] {
			left = append(left, `<code>`+E(n)+`</code>`)
		}
	}
	if len(left) > 0 { // DB に取り込んでいない conf のジョブは scheduler が動かさない
		note = `<p class=warn>DB に取り込んでいない <code>scheduler/jobs/*.conf</code> があります。job は DB の定義だけで動くので、これらは動きません: ` +
			strings.Join(left, ", ") +
			`。取り込む: <code>taskboard/bin/tb job import --dry-run</code> で確かめてから <code>taskboard/bin/tb job import</code></p>`
	}
	note += "<p class=note>「何をする」の 2 行目は conf の先頭のコメント(無ければコマンドから推した説明)です。job 名を開くと、型(agent に送る / コマンドを実行)・宛先と今の状態・送られる本文やコマンド・次回の予定・承認の扱いを読めます。" +
		"スケジュール(間隔・時間帯・曜日・一時停止)だけ変更できます(次の分から反映)。1 回だけのジョブ(AT)の日時は定義で変えます。</p>"
	return "job", doneNotice(c) + note + table([]string{"job", "何をする(宛先)", "いつ(間隔 / 時間帯 / 曜日、または 1 回だけの日時)", "状態", "最終記録", "最後の実行"}, rows)
}

// fold は長い本文を折りたたむ(全文は details の中)。text は無害化・マスク済み。
func fold(text, label string) string {
	body := "<pre>" + E(text) + "</pre>"
	if n := utf8.RuneCountInString(text); n > 600 || strings.Count(text, "\n") >= 12 {
		return "<details><summary>" + E(label) + "を開く(" + strconv.Itoa(n) + " 文字)</summary>" + body + "</details>"
	}
	return body
}

func maskNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(`<p class=note>トークンらしい文字列 %d 件をマスクしました</p>`, n)
}

const inexactHTML = `<p class=warn>シェルで展開・解釈される記述です(表示は不完全。実際に実行・送信される内容は、展開後の結果になります)。conf を直接確認してください。</p>`

func ifs(c bool, s string) string {
	if c {
		return s
	}
	return ""
}

// jobOverview は型・宛先・本文・いつ・直近の結果・副作用・承認の扱い(読み取りのみ。conf は実行しない)。
func (v *sv) jobOverview(name string, info *jobInfo) string {
	defs, _ := v.jobDefs(name)
	cx := defs.complex
	type kv struct{ k, v string }
	var rows []kv
	isPrompt := info.kind == "prompt"
	if defs.truncated {
		rows = append(rows, kv{"注意", `<span class=warn>conf が 64 KiB を超えているため、後半は読んでいません(表示は不完全)</span>`})
	}
	if about := v.jobAbout(name); len(about) > 0 {
		var es []string
		for _, x := range about {
			es = append(es, E(x))
		}
		rows = append(rows, kv{"説明", strings.Join(es, "<br>") + `<br><span class=note>(conf の先頭のコメント)</span>`})
	}
	if isPrompt {
		rows = append(rows, kv{"型", "<b>PROMPT 型</b>: agent にプロンプトを送る"})
	} else {
		rows = append(rows, kv{"型", "<b>COMMAND 型</b>: コマンドを実行する(agent は介さない)"})
	}
	if isPrompt {
		tgt := info.target
		st := v.agentState(tgt)
		var a string
		switch {
		case st.err != "":
			a = `<code>` + E(tgt) + `</code> <span class=warn>状態を取得できません: ` + E(st.err) + `</span>`
		case st.count == 0:
			a = `<code>` + E(tgt) + `</code> <span class=warn>herdr に見つかりません(送れず failed になります)</span>`
		case st.count > 1:
			a = `<code>` + E(tgt) + `</code> <span class=warn>同じ名前が ` + strconv.Itoa(st.count) + ` 体あり、一意に決まりません(failed になります)</span>`
		default:
			s := st.status
			cls := map[string]string{"working": "st-working", "blocked": "st-blocked", "idle": "st-idle", "done": "st-done"}[s]
			a = `<code>` + E(tgt) + `</code> 今: <span class="` + cls + `">` + E(orStr(s, "不明")) + `</span>`
			if st.workspace != "" {
				a += " / workspace " + E(st.workspace)
			}
			if truthy(st.registry["herdr_session"]) {
				a += " / herdr セッション " + ev(st.registry["herdr_session"])
			}
			if truthy(st.registry["role"]) {
				a += " / 役割 " + ev(st.registry["role"])
			}
			if s != "idle" && s != "done" {
				a += ` <span class=note>(idle / done 以外のときは送らず skipped になります)</span>`
			}
		}
		if st.registry["state"] == "stopped" {
			a += ` <span class=warn>tb の台帳では停止中です</span>`
		}
		rows = append(rows, kv{"誰に送られるか", a})
		cond := "宛先が idle / done のときだけ送ります。"
		if skip := defs.get("SKIP_IF"); skip != "" {
			cond += "さらに状態が <code>" + E(skip) + "</code> のときは送りません(SKIP_IF)。"
		}
		rows = append(rows, kv{"送る条件", cond})
		for _, x := range [][3]string{
			{"PROMPT_CMD", "本文を出すコマンド", "PROMPT_CMD: 出力が空でなければそれを送ります(下の切り替え・既定より先)"},
			{"PROMPT_RULES", "時刻での切り替え", "PROMPT_RULES: 回の時刻に最初に合った行の本文を送ります。どれにも合わなければ下の既定の本文"},
			{"PROMPT_VARS", "差し込み", "PROMPT_VARS: 本文の {{名前}} に、送る直前に実行したコマンドの出力を入れます(終了コード 1 ならその回は送りません)"},
		} {
			if d := defs.get(x[0]); d != "" {
				masked, n := mask(Clean(d))
				rows = append(rows, kv{x[1], E(x[2]) + ifs(cx[x[0]], inexactHTML) + fold(masked, x[0]) + maskNote(n)})
			}
		}
		var text, note, src string
		hasText := false
		if pf := defs.get("PROMPT_FILE"); pf != "" {
			if cx["PROMPT_FILE"] {
				note = "パスにシェルで展開される記述($ や引用符など)が含まれるため、どのファイルか確定できず、中身は表示しません"
			} else {
				text, hasText, note = promptFile(pf)
			}
			src = `ファイル <code>` + E(pf) + `</code>(展開前の記述そのまま)`
		} else if p, ok := defs.vals["PROMPT"]; ok {
			text, hasText, src = p, true, "conf の PROMPT(展開前の記述そのまま)"+ifs(cx["PROMPT"], inexactHTML)
		}
		switch {
		case !hasText && src == "" && (defs.get("PROMPT_RULES") != "" || defs.get("PROMPT_CMD") != ""):
			rows = append(rows, kv{"何が送られるか", "既定の本文はありません(上の切り替えで本文が決まらない回は送りません)"})
		case !hasText:
			rows = append(rows, kv{"何が送られるか", orStr(src, "本文の定義が見つかりません") + ` <span class=note>` + E(note) + `</span>` + ifs(cx["PROMPT_FILE"], inexactHTML)})
		default:
			masked, n := mask(Clean(text))
			rows = append(rows, kv{"何が送られるか", src + ifs(note != "", ` <span class=note>`+E(note)+`</span>`) + fold(masked, "本文") + maskNote(n)})
		}
	} else {
		cmd := defs.get("COMMAND")
		if cs := commandSummary(cmd); len(cs) > 0 {
			var ls []string
			for _, c := range cs {
				ls = append(ls, `<code>`+E(c[0])+`</code>: `+E(c[1]))
			}
			rows = append(rows, kv{"中身(推定)", strings.Join(ls, "<br>") + `<br><span class=note>(コマンドの中の tb のサブコマンドから推した説明。実際の動きは下の本文)</span>`})
		}
		masked, n := mask(Clean(cmd))
		rows = append(rows, kv{"何が実行されるか", "<code>bash -c</code> でそのまま実行(展開前の記述そのまま。タイムアウト " +
			E(orStr(defs.get("COMMAND_TIMEOUT"), "既定")) + " 秒)" + ifs(cx["COMMAND"], inexactHTML) + fold(masked, "コマンド") + maskNote(n)})
	}
	nxt, nnote := nextRuns(info, 3)
	rows = append(rows, kv{"いつ", E(intervalText(info)) + "おき / " + E(info.start) + "-" + E(info.end) + " / " + E(info.days) + " / TZ " + E(info.tz) +
		ifs(!info.enabled, ` <span class=warn>一時停止中</span>`)})
	if len(nxt) > 0 {
		var es []string
		for _, x := range nxt {
			es = append(es, E(x))
		}
		rows = append(rows, kv{"次回の予定(目安)", strings.Join(es, " → ") + ` <span class=note>(ジョブの TZ の時刻。間隔・時間帯・曜日からの推定で、実際の起動と数秒ずれることがあります)</span>`})
	} else {
		rows = append(rows, kv{"次回の予定(目安)", `<span class=note>` + E(nnote) + `</span>`})
	}
	last, lr := jobLast(name)
	recs := parseJSONL(tailLines(filepath.Join(SchedLogs(), name+".log"), 200))
	res := "記録なし"
	if nonEmpty(lr) {
		res = ev(lr["ts"]) + " " + ev(lr["action"])
		if x, ok := lr["exit"]; ok {
			res += " (exit " + ev(x) + ")"
		}
	}
	if nonEmpty(last) {
		res += " / 最後の記録: " + lastText(last)
	}
	if len(recs) > 0 {
		counts := map[string]int{}
		disp := map[string]any{}
		var keys []string
		for _, r := range recs {
			k := pyStr(r["action"])
			if _, ok := counts[k]; !ok {
				keys = append(keys, k)
				disp[k] = r["action"]
			}
			counts[k]++
		}
		sort.SliceStable(keys, func(i, j int) bool { return keys[i] < keys[j] })
		var cs []string
		for _, k := range keys {
			cs = append(cs, ev(disp[k])+" "+strconv.Itoa(counts[k]))
		}
		res += `<br><span class=note>直近 ` + strconv.Itoa(len(recs)) + ` 件: ` + strings.Join(cs, ", ") + "</span>"
	}
	rows = append(rows, kv{"直近の結果", res})
	if isPrompt {
		rows = append(rows, kv{"送信の記録", `<a href="/ledger?kind=scheduler&amp;target=` + pyQuote(info.target, "/") + `">送信履歴(scheduler → ` + E(info.target) + `)</a>`})
	}
	if isPrompt {
		rows = append(rows, kv{"副作用", "宛先 agent が受け取って動きます。その agent が Slack・Jira など外部へ書けるかは、webui からは分かりません(不明)。"})
	} else {
		rows = append(rows, kv{"副作用", "コマンドが何を書くかは、本文から推測しません(不明)。上のコマンド本文を確認してください。"})
	}
	appr := "COMMAND 型の実行は設定 policy の対象外です(承認待ちにならず、時刻になれば実行されます)。"
	if isPrompt {
		appr = "scheduler からの送信(<code>tb system-send</code>)は設定 policy を見ず、承認待ちにはなりません。上の条件を満たせば自動で送られます。"
		if d := v.sendTaskDecision(info.target); d != "" {
			appr += ` <span class=note>参考: 通常の <code>tb message send</code> の <code>send:task:` + E(info.target) + `</code> は現在 ` + E(d) + `(この job には適用されません)。</span>`
		}
	}
	rows = append(rows, kv{"承認の扱い", appr})
	var h strings.Builder
	for _, r := range rows {
		h.WriteString("<tr><th style='text-align:left;vertical-align:top;white-space:nowrap'>" + E(r.k) + "</th><td>" + r.v + "</td></tr>")
	}
	return `<h2>このジョブが行うこと</h2><table>` + h.String() + `</table>` +
		`<p class=note>conf の記述を実行せずに読んでいます(<code>$変数</code> などは展開せず、そのまま表示)。` +
		`トークンらしい文字列はマスクしますが完全ではありません。機密を含みうる画面です。</p>`
}

// pageJob は 1 ジョブの設定フォーム(自動更新しない)。
func (s *Server) pageJob(q *rq, name, csrf, errMsg string, saved bool) (string, string) {
	return s.pageJobDraft(q, name, csrf, errMsg, saved, "", nil)
}

// pageJobDraft は pageJob に、保存の知らせ(done)と、断られたときのフォーム(draft)を足したもの。
func (s *Server) pageJobDraft(q *rq, name, csrf, errMsg string, saved bool, done string, draft form) (string, string) {
	v := q.data()
	info, err := v.jobInfo(name)
	if info == nil { // scheduler が読めない定義でも、消すことはできる(tb job rm は本文を読まない)
		hid := `<input type=hidden name=csrf value="` + E(csrf) + `">`
		return "job: " + name, errHTML(orStr(err, errMsg)) + jobRmForm(pyQuote(name, "/"), hid) + `<p><a href="/jobs">一覧へ</a></p>`
	}
	days := info.days
	preset := ""
	if isPreset(days) {
		preset = days
	}
	var opts strings.Builder
	for _, p := range dayPresets {
		opts.WriteString(`<option value=` + p[0] + sel(preset == p[0], " selected") + `>` + E(p[1]) + `</option>`)
	}
	opts.WriteString(`<option value=custom` + sel(preset == "", " selected") + `>曜日を指定</option>`)
	toks := map[string]bool{}
	if preset == "" {
		for _, t := range pyFields(strings.ReplaceAll(strings.ToLower(days), ",", " ")) {
			toks[t] = true
		}
	}
	var checks strings.Builder
	for _, t := range dayTokens {
		checks.WriteString(`<label><input type=checkbox name=day_` + t + ` value=1` + sel(toks[t], " checked") + `> ` + strings.ToUpper(t[:1]) + t[1:] + `</label> `)
	}
	hid := `<input type=hidden name=csrf value="` + E(csrf) + `">`
	sec := info.intervalSec
	unit, iv := "sec", sec
	if sec%60 == 0 {
		unit, iv = "min", sec/60
	}
	var units strings.Builder
	for _, u := range intervalUnit {
		units.WriteString(`<option value=` + u.u + sel(u.u == unit, " selected") + `>` + E(u.label) + `</option>`)
	}
	qn := pyQuote(name, "/")
	form := `<form method=post action="/jobs/` + qn + `">` + hid + `<input type=hidden name=action value=save>` +
		`<p><label>間隔<br><input type=number name=interval min=1 max=86400 required value="` + strconv.Itoa(iv) + `"></label> ` +
		`<select name=interval_unit>` + units.String() + `</select> <span class=note>分は 1440 まで、秒は 86400 まで</span></p>` +
		`<p><label>開始 <input type=time name=start step=1 required value="` + E(info.start) + `"></label> ` +
		`<label>終了 <input type=time name=end step=1 required value="` + E(info.end) + `"></label>` +
		` <span class=note>両端を含む(秒を省いた終了はその分の 59 秒まで)。日またぎは不可。TZ は ` + E(info.tz) + `</span></p>` +
		`<p><label>曜日 <select name=days>` + opts.String() + `</select></label> ` + checks.String() + `</p>` +
		`<p><label><input type=checkbox name=enabled value=1` + sel(info.enabled, " checked") + `> 有効(外すと一時停止)</label></p>` +
		`<button type=submit>保存</button></form>` +
		`<form method=post action="/jobs/` + qn + `" style="margin-top:12px">` + hid + `<input type=hidden name=action value=reset>` +
		`<button type=submit>conf の値に戻す</button></form>`
	if info.at != "" { // 1 回だけのジョブ: 間隔・時間帯・曜日のフォームは出さない(日時は定義の AT で変える)
		form = `<p>1 回だけ <b>` + E(info.at) + `</b>(TZ ` + E(info.tz) + `)に動くジョブです。日時を変えるには、下の定義の <code>AT=</code> を直します。` +
			`一時停止は定義に <code>ENABLED=0</code> を書きます。</p>`
	}
	what := "コマンドを実行する"
	if info.kind == "prompt" {
		what = `agent <code>` + E(info.target) + `</code> にプロンプトを送る`
	}
	return "job: " + name, errHTML(errMsg) + ifs(saved && done == "", `<p class=note>保存しました。次の分から反映されます。</p>`) +
		ifs(done == "conf", `<p class=note>`+E(doneMsg["conf"])+`</p>`) +
		"<p>" + what + "ジョブ。" + ifs(info.overridden, "Web UI で変更済み。") + "</p>" + v.jobOverview(name, info) + "<h2>スケジュールの変更</h2>" + form +
		"<p class=note>スケジュールの保存先: DB の <code>sched-settings:" + E(name) + `</code>(セッション <code>` + E(v.sess) + `</code>)</p>` +
		v.jobConfForm(name, csrf, draft) + jobRmForm(qn, hid) + `<p><a href="/jobs">一覧へ</a></p>`
}

// jobRmForm はジョブ画面の「ジョブを消す」(元に戻せないので、印を付けないと送れない)。
func jobRmForm(qn, hid string) string {
	return `<h2>ジョブを消す</h2><form method=post action="/jobs/` + qn + `">` + hid + `<input type=hidden name=action value=rm>` +
		`<p><label><input type=checkbox name=confirm value=1 required> 元に戻せないことを確かめた</label> <button type=submit>消す</button></p></form>` +
		`<p class=note><code>tb job rm</code> と同じ扱いです(このセッションの定義と上書き設定の DB の行を消す。` +
		`どのセッションにも定義が残らなければ実行のログも消す。変更の履歴は残る)。共通の行(*)は手元で <code>tb job rm &lt;job&gt; --shared</code>。</p>`
}

// postJob は POST /jobs/<job>: スケジュールの上書き設定を保存する・conf の値に戻す・ジョブを消す。
func (s *Server) postJob(q *rq, path, name string, f map[string]string, csrf string) {
	v := q.data()
	if !JobRE.MatchString(name) || !contains(v.jobNames(), name) {
		s.text(q, 404, "Not Found")
		return
	}
	if f["action"] == "rm" { // ジョブを消す(tb job rm)
		msg := ""
		if f["confirm"] != "1" {
			msg = "消すときは「元に戻せないことを確かめた」に印を付けてください"
		} else if rc, out := runTBInput(v.sess, []string{"job", "rm", name}, ""); rc != 0 {
			msg = tbFail(rc, out)
		}
		if msg != "" {
			q.v.close()
			q.v = nil
			title, body := s.pageJob(q, name, csrf, msg, false)
			s.html(q, 400, s.page(q, title, body, path, "", pageOpts{norefresh: true}))
			return
		}
		s.logf("jobs: removed %s\n", name)
		s.redirect(q, "/jobs?done=job-rm")
		return
	}
	if f["action"] == "conf" { // 定義(本文)の保存
		if err := v.saveJobConf(name, f); err != "" {
			q.v.close()
			q.v = nil
			title, body := s.pageJobDraft(q, name, csrf, err, false, "", f)
			s.html(q, 400, s.page(q, title, body, path, "", pageOpts{norefresh: true}))
			return
		}
		s.logf("jobs: conf saved %s\n", name)
		s.redirect(q, "/jobs/"+name+"?saved=1&done=conf")
		return
	}
	if err := v.saveJobSettings(name, f); err != "" {
		title, body := s.pageJob(q, name, csrf, err, false)
		s.html(q, 400, s.page(q, title, body, path, "", pageOpts{norefresh: true}))
		return
	}
	st := "saved"
	if f["action"] == "reset" {
		st = "reset"
	}
	s.logf("jobs: settings %s %s\n", st, name)
	s.redirect(q, "/jobs/"+name+"?saved=1")
}
