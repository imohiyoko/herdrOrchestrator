package webui

import (
	"context"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"

	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// agentspage.go は agent の画面(/agents、/agents/<key>、/agent-graph、/agent-new)と、
// その操作(POST /agent-op、/agent-new。書き込みは tb が行い、Web UI からは人の操作として扱う)。

var (
	killLabel = map[string]string{"yes": "止めてよい", "no": "止めない", "unknown": "未判定", "stale": "判定が古い", "protected": "保護中"}
	killCls   = map[string]string{"yes": "st-done", "no": "st-idle", "stale": "warn", "protected": "st-idle"}
	opMsg     = map[string]string{"done": "実行しました。", "pending": "人の承認待ちに積みました。",
		"refused": "前提を満たさないため実行しませんでした(下の「直近の操作」に理由)。",
		"error":   "実行に失敗しました(下の「直近の操作」か、手元の tb agent-ops を確認してください)。"}
	reAgentName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	reModel     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	reRoleName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	reOpID      = regexp.MustCompile(`^[0-9]{1,9}$`)
)

const (
	promptMax    = 4000
	cwdMax       = 1024
	spawnTimeout = 90 * time.Second // agent の起動は herdr の待ちがあるので長めに待つ
)

func stateHTML(s any) string {
	st := "unknown"
	if truthy(s) {
		st = pyStr(s)
	}
	return `<span class="st-` + E(st) + `">` + E(st) + `</span>`
}

func curTaskTitle(a map[string]any, tasks []row) string {
	if len(tasks) > 0 {
		return ev(tasks[0]["title"])
	}
	if truthy(a["terminal_title_stripped"]) {
		return ev(a["terminal_title_stripped"])
	}
	return E(orEmpty(a["terminal_title"]))
}

// opForm は agent 一覧の操作ボタン(POST /agent-op)。confirm なら確認のチェックが要る。
func opForm(c pageCtx, op, name, label string, confirm bool) string {
	chk := ""
	if confirm {
		chk = `<label class=note><input type=checkbox name=confirm value=1 required>本当に` + E(label) + `</label> `
	}
	return `<form method=post action="/agent-op" style="display:inline"><input type=hidden name=csrf value="` + E(c.csrf) + `">` +
		`<input type=hidden name=op value="` + E(op) + `"><input type=hidden name=name value="` + E(name) + `">` + chk +
		`<button type=submit>` + E(label) + `</button></form>`
}

func decideForm(c pageCtx, lid any) string {
	var b strings.Builder
	for _, x := range [][2]string{{"approve", "承認して実行"}, {"reject", "却下"}} {
		b.WriteString(`<form method=post action="/agent-op" style="display:inline"><input type=hidden name=csrf value="` + E(c.csrf) + `">` +
			`<input type=hidden name=op value="` + x[0] + `"><input type=hidden name=id value="` + strconv.FormatInt(intOf(lid), 10) +
			`"><button type=submit>` + x[1] + `</button></form> `)
	}
	return b.String()
}

func killableHTML(r row) string {
	if len(r) == 0 {
		return `<span class=note>未判定</span>`
	}
	k := "unknown"
	if truthy(r["killable_now"]) {
		k = pyStr(r["killable_now"])
	}
	cls, ok := killCls[k]
	if !ok {
		cls = "note"
	}
	if k == "protected" {
		return `<span class=` + cls + `>` + E(killLabel[k]) + `</span><br><span class=note>` + ev(r["protect_reason"]) + `(config の protect)</span>`
	}
	why := ""
	if truthy(r["killable_reason"]) {
		why = `<br><span class=note>` + ev(r["killable_reason"]) + `(` + ev(r["killable_by"]) + `)</span>`
	}
	lab, ok := killLabel[k]
	if !ok {
		lab = k
	}
	return `<span class=` + cls + `>` + E(lab) + `</span>` + why
}

func stopCell(c pageCtx, name string, r row) string {
	if c.csrf == "" || name == "" {
		return ""
	}
	if r != nil && r["killable_now"] == "protected" { // tb が --force でも止めない。外すのは人が tb config edit protect
		return `<span class=note>保護中(停止は protect から外してから)</span>`
	}
	if r != nil && r["killable_now"] == "yes" && !truthy(r["open_tasks"]) {
		return opForm(c, "stop", name, "停止", false)
	}
	return opForm(c, "force-stop", name, "強制停止", true)
}

// regRole は台帳の行の role から役割(台帳も役割ファイルも、このセッションのもの)。
func (v *sv) regRole(reg map[string]row, name string) string {
	return v.roleOf(name, orEmpty(reg[name]["role"]))
}

func (v *sv) roleGroupsHTML(agents []map[string]any, reg map[string]row) string {
	var nodes []gnode
	for _, a := range agents {
		nodes = append(nodes, gnode{agent: a, role: v.regRole(reg, orEmpty(a["name"]))})
	}
	var rows [][]string
	for _, g := range byRole(nodes) {
		var ps []string
		for _, n := range g.nodes {
			ps = append(ps, alink(n.agent, "")+" "+stateHTML(n.agent["agent_status"]))
		}
		rows = append(rows, []string{ifElse(g.role != "", E(g.role), "(役割なし)"), strings.Join(ps, ", ")})
	}
	return table([]string{"ロール", "agent"}, rows)
}

func (s *Server) pageAgentGraph(q *rq, c pageCtx) (string, string) {
	v := q.data()
	agents, err := getAgents(q.name)
	reg := v.agentRegistry()
	es := v.edges(v.q(edgeSQL()))
	var nodes []gnode
	seen := map[string]bool{}
	for _, a := range agents {
		n := agentKey(a) // 名前が無ければ pane_id(一覧・リンクと同じ)
		if n != "" && !seen[n] {
			seen[n] = true
			nodes = append(nodes, gnode{name: n, role: v.regRole(reg, orEmpty(a["name"])), agent: a})
		}
	}
	for _, e := range es {
		for _, x := range []string{e.from, e.to} {
			if !seen[x] { // 関係の端に居るが今は動いていない名前(止まった agent・cli など)
				seen[x] = true
				nodes = append(nodes, gnode{name: x, role: v.regRole(reg, x)})
			}
		}
	}
	c.opts.scripts = []string{"/static/mermaid.min.js", "/docs.js"}
	body := errHTML(err) + `<p class=note>直近 7 日の「誰が誰に何をしたか」(spawn と指揮は今の状態)。1 体が複数の上流を持つこともあります。` +
		`点線は routes.conf で拒否された送信の試みです。手元では tb agents --graph で同じものを出せます。 <a href="/agents">agent 一覧へ</a></p>`
	body += "<pre class=mermaid>" + E(mermaidGraph(nodes, es)) + "</pre>"
	var rows [][]string
	for _, e := range es {
		lab, ok := verbLabel[e.verb]
		if !ok {
			lab = e.verb
		}
		rows = append(rows, []string{E(e.from), E(lab), E(e.to), strconv.FormatInt(e.n, 10), E(e.lastAt),
			ifElse(e.status == "denied", `<span class=warn>拒否</span>`, "ok")})
	}
	body += "<h2>関係</h2>" + table([]string{"主体", "動詞", "対象", "件数", "最後", "判定"}, rows)
	rows = nil
	for _, g := range byRole(nodes) {
		var ps []string
		for _, n := range g.nodes {
			if n.agent != nil {
				ps = append(ps, alink(n.agent, ""))
			} else {
				ps = append(ps, E(n.name))
			}
		}
		rows = append(rows, []string{ifElse(g.role != "", E(g.role), "(役割なし)"), strings.Join(ps, ", ")})
	}
	body += "<h2>ロール</h2>" + table([]string{"ロール", "agent"}, rows)
	return "agent の関係", body
}

func actCell(act row, k string) string {
	if act == nil {
		return "-"
	}
	return ev(act[k])
}

func (s *Server) pageAgents(q *rq, c pageCtx) (string, string) {
	v := q.data()
	agents, err := getAgents(q.name)
	reg := v.agentRegistry()
	var rows [][]string
	for _, a := range agents {
		act := v.activityFor(a)
		tasks := v.tasksForAgent(a)
		warn := ""
		if a["agent_status"] == "blocked" {
			warn = "blocked(人の対応待ち)"
		} else if stalled(a, act) {
			warn = fmt.Sprintf("滞留 (%d 分超)", stallMin)
		}
		name := orEmpty(a["name"])
		r := v.registryRow(reg, name)
		rows = append(rows, []string{alink(a, ""), stateHTML(a["agent_status"]), ev(a["workspace_id"]), curTaskTitle(a, tasks),
			`入力 ` + actCell(act, "last_prompt_at") + `<br>停止 ` + actCell(act, "last_stop_at"),
			ifs(warn != "", `<span class=warn>`+E(warn)+`</span>`), killableHTML(r), stopCell(c, name, r)})
	}
	body := ""
	if st := c.qs["op"]; len(st) > 0 {
		if m, ok := opMsg[st[0]]; ok {
			body = "<p class=note>" + E(m) + "</p>"
		}
	}
	body += errHTML(err) + table([]string{"名前", "状態", "workspace", "いま何を", "最終入力 / 停止", "警告", "止めてよいか", "操作"}, rows)
	body += `<h2 id="roles">ロール別</h2>` + v.roleGroupsHTML(agents, reg) +
		`<p class=note>誰が誰に何をしたか(spawn・指揮・依頼・報告・解析)は <a href="/agent-graph">関係グラフ</a> で見られます。</p>`
	body += `<p class=note>「止めてよいか」は判定役(grants の agent:flag)か人が付けます。止めてよい印があり、未完了タスクが無いものは「停止」、` +
		`それ以外は確認つきの「強制停止」。止めても台帳に会話が残り、下から同じ会話で復活できます。 ` +
		`<a href="/agent-new">新しい agent を作る</a></p>`
	rows = nil
	for _, o := range v.agentOps("pending", 50) {
		id := "L-" + pyStr(o["id"])
		if qid := v.requestOf("agent_ops", o["id"]); qid != "" {
			id = qid + `<br><span class=note>` + id + `</span>`
		}
		rows = append(rows, []string{id, ev(o["requested_by"]), ev(o["op"]), ev(o["target"]), `<code>` + ev(o["args"]) + `</code>`,
			ev(o["created_at"]), ifs(c.csrf != "", decideForm(c, o["id"]))})
	}
	body += `<h2 id="ops">承認待ちの操作(agent からの依頼)</h2>` + table([]string{"id", "依頼者", "操作", "対象", "内容", "時刻", ""}, rows)
	rows = nil
	for _, r := range v.stoppedAgents() {
		sess := "-"
		if truthy(r["session_id"]) {
			sess = `<code>` + ev(r["session_id"]) + `</code>`
		}
		ops := ""
		if c.csrf != "" {
			name := pyStr(r["name"])
			ops = ifs(truthy(r["session_id"]), opForm(c, "revive", name, "同じ会話で復活", false)) + " " +
				opForm(c, "revive-fresh", name, "新しい会話で復活", false)
		}
		rows = append(rows, []string{ev(r["name"]), ev(r["stopped_at"]), sess, ev(r["cwd"]),
			E(orStr(orEmpty(r["model"]), "-")) + ` / ` + E(orStr(orEmpty(r["role"]), "-")), ops})
	}
	body += "<h2>停止中の agent</h2>" + table([]string{"名前", "止めた時刻", "会話 (session)", "cwd", "モデル / 役割", "操作"}, rows)
	rows = nil
	for _, o := range v.agentOps("", 15) {
		at := o["created_at"]
		if truthy(o["decided_at"]) {
			at = o["decided_at"]
		}
		rows = append(rows, []string{"L-" + pyStr(o["id"]), ev(o["op"]), ev(o["target"]), ev(o["requested_by"]), ev(o["status"]), ev(o["result"]), ev(at)})
	}
	body += "<h2>直近の操作</h2>" + table([]string{"id", "操作", "対象", "依頼者", "結果", "内容", "時刻"}, rows)
	return "agent 一覧", body
}

// pageAgentNew は新しい agent を作るフォーム(入力欄があるので自動更新しない)。
func (s *Server) pageAgentNew(q *rq, csrf, errMsg string, f map[string]string) (string, string) {
	if f == nil {
		f = map[string]string{}
	}
	opts := `<option value="">(名前と役割の対応(map)で決める)</option>`
	for _, r := range q.data().roleNames() {
		if role, ok := f["role"]; ok && role == r {
			opts += `<option value="` + E(r) + `" selected>` + E(r) + `</option>`
		} else {
			opts += `<option value="` + E(r) + `">` + E(r) + `</option>`
		}
	}
	kill, ok := f["killable"]
	if !ok {
		kill = "unknown"
	}
	kopts := ""
	for _, x := range [][2]string{{"unknown", "判定役に任せる"}, {"yes", "止めてよい(使い捨て)"}, {"no", "止めない(常駐)"}} {
		kopts += `<option value="` + x[0] + `"` + ifs(kill == x[0], " selected") + `>` + x[1] + `</option>`
	}
	body := errHTML(errMsg) +
		`<form method=post action="/agent-new"><input type=hidden name=csrf value="` + E(csrf) + `">` +
		`<p><label>名前(英小文字で始まる英小文字・数字・_・-、32 字以内)<br><input name=name required maxlength=32 pattern="[a-z][a-z0-9_-]{0,31}" value="` + E(f["name"]) + `"></label></p>` +
		`<p><label>作業ディレクトリ(空ならこのリポジトリ)<br><input name=cwd size=60 maxlength=1024 value="` + E(f["cwd"]) + `"></label></p>` +
		`<p><label>モデル(空なら Claude Code の既定。例: haiku / sonnet)<br><input name=model maxlength=64 value="` + E(f["model"]) + `"></label></p>` +
		`<p><label>役割<br><select name=role>` + opts + `</select></label></p>` +
		`<p><label>止めてよいか<br><select name=killable>` + kopts + `</select></label></p>` +
		`<p><label>最初の指示(任意)<br><textarea name=prompt rows=4 cols=60 maxlength=4000>` + E(f["prompt"]) + `</textarea></label></p>` +
		`<button type=submit>作成する</button></form>` +
		`<p class=note>専用の workspace を作り、そこで claude を起動します(tb agent-new)。役割は起動時に SessionStart hook が渡します。</p>`
	return "新しい agent", body
}

func (s *Server) pageAgentNewRoute(q *rq, c pageCtx) (string, string) {
	c.opts.norefresh = true
	return s.pageAgentNew(q, c.csrf, "", nil)
}

// findAgent は名前(か pane_id)で agent を探す。
func findAgent(sess, key string) (map[string]any, string) {
	agents, err := getAgents(sess)
	for _, a := range agents {
		if agentKey(a) == key {
			return a, err
		}
		if p, ok := a["pane_id"].(string); ok && p == key {
			return a, err
		}
	}
	return nil, err
}

// pageAgent は views.page_agent。見つからなければ ok=false(404)。
func (s *Server) pageAgent(q *rq, key string) (string, string, bool) {
	v := q.data()
	a, err := findAgent(q.name, key)
	if a == nil {
		return "", "", false
	}
	act := v.activityFor(a)
	tasks := v.tasksForAgent(a)
	sid := agentSession(a)
	if !truthy(sid) {
		sid = nil
		if len(tasks) > 0 && truthy(tasks[0]["session_id"]) {
			sid = tasks[0]["session_id"]
		}
	}
	ws := workspaceLabel(q.name, a["workspace_id"])
	wsl := "-"
	if truthy(ws) {
		wsl = pyStr(ws)
	}
	cwd := a["foreground_cwd"]
	if !truthy(cwd) {
		cwd = a["cwd"]
	}
	title := a["terminal_title_stripped"]
	if !truthy(title) {
		title = a["terminal_title"]
	}
	info := [][2]string{
		{"状態", stateHTML(a["agent_status"]) + ifs(stalled(a, act), ` <span class=warn>滞留</span>`)},
		{"場所", `workspace ` + E(wsl) + ` (` + ev(a["workspace_id"]) + `) / tab ` + ev(a["tab_id"]) + ` / pane ` + ev(a["pane_id"])},
		{"cwd", ev(cwd)},
		{"端末タイトル", ev(title)},
		{"最終入力", actCell(act, "last_prompt_at")},
		{"最終停止", actCell(act, "last_stop_at")},
		{"session_id", ifElse(truthy(sid), `<code>`+ev(sid)+`</code>`, "-")},
		{"止めてよいか", killableHTML(v.registryRow(v.agentRegistry(), orEmpty(a["name"]))) + ` <a href="/agents">停止・復活は agent 一覧から</a>`},
	}
	var b strings.Builder
	b.WriteString(errHTML(err) + "<table>")
	for _, x := range info {
		b.WriteString("<tr><th>" + x[0] + "</th><td>" + x[1] + "</td></tr>")
	}
	b.WriteString("</table>")
	attach := "herdr agent attach " + shQuote(agentKey(a))
	if a["agent_status"] == "blocked" {
		b.WriteString(`<h2 id="approval">承認待ちの内容</h2>`)
		dlg, derr := readAgent(q.name, a, 40, "visible")
		b.WriteString(errHTML(derr))
		if derr == "" {
			b.WriteString("<pre>" + E(dlg) + "</pre>")
		}
		b.WriteString(`<p class=note>要約・加工なしの端末表示です。承認の入力はブラウザ上では行えません。` +
			`下のコマンドで手元の herdr から開いて、人が操作してください。</p>`)
	}
	b.WriteString(`<h2>手元で開く</h2><p><input type=text readonly size=60 value="` + E(attach) +
		`"> <span class=note>欄をクリックして全選択(Cmd+A)→ コピーして手元の端末で実行</span></p>` +
		`<p class=warn>別のターミナルで claude --resume を実行しないでください。動いている agent と会話が枝分かれします。</p>`)
	var rows [][]string
	for _, t := range tasks {
		rows = append(rows, []string{tlink(t["id"]), ev(t["project"]), ev(t["title"]), ev(t["status"]), ev(t["updated_at"])})
	}
	b.WriteString("<h2>担当タスク</h2>" + table([]string{"タスク", "PJ", "題", "状態", "更新"}, rows))
	out, rerr := readAgent(q.name, a, 60, "recent")
	b.WriteString("<h2>直近の出力 (agent read)</h2>" + errHTML(rerr))
	if rerr == "" {
		b.WriteString("<pre>" + E(out) + "</pre>")
	}
	return "agent: " + Clean(agentKey(a)), b.String(), true
}

// ---- 操作(actions.py の agent_op / agent_decide / agent_new)

// rcState は tb の終了コード → 画面に出す結果。0 実行済み / 20 人の承認待ち / 1,2 前提違いで断った / それ以外は失敗。
func rcState(rc int) string {
	switch rc {
	case 0:
		return "done"
	case 20:
		return "pending"
	case 1, 2:
		return "refused"
	}
	return "error"
}

func agentOp(sess, op, name string) string {
	if !reAgentName.MatchString(name) {
		return "refused"
	}
	var args []string
	switch op {
	case "stop", "force-stop":
		if sess == SessionName() {
			return agentStop(sess, name, op == "force-stop")
		}
		// herdr の呼び出し先はプロセスの環境で決まる(herdr アダプタはまだセッションを受け取らない)。ほかのセッションは
		// 前と同じく、HERDR_SESSION を渡した tb の子プロセスで行う
		args = []string{"agent-stop", name}
		if op == "force-stop" {
			args = append(args, "--force")
		}
	case "revive":
		args = []string{"agent-revive", name}
	case "revive-fresh":
		args = []string{"agent-revive", name, "--fresh"}
	default:
		return "refused"
	}
	rc, _ := runTB(sess, spawnTimeout, args...)
	return rcState(rc)
}

// agentStop はアプリケーション API の agent.stop を同じプロセスで人として呼ぶ(ADR 0008。tb agent-stop と同じ操作)。
func agentStop(sess, name string, force bool) string {
	ctx, cancel := context.WithTimeout(context.Background(), spawnTimeout)
	defer cancel()
	c, err := appClient(sess)
	if err != nil {
		return "error"
	}
	var res agent.StopResult
	return appState(c.Call(ctx, app.Human(), "agent.stop", agent.StopReq{Name: name, Force: force}, &res), res.Pending)
}

func agentDecide(sess string, lid int, approve bool) string {
	cmd := "agent-reject"
	if approve {
		cmd = "agent-approve"
	}
	rc, _ := runTB(sess, spawnTimeout, cmd, "L-"+strconv.Itoa(lid))
	return rcState(rc)
}

// agentNew は新しい agent を作る。入力の検査はここと tb の両方で行う。(結果, エラー文)。
func agentNew(sess string, form map[string]string) (string, string) {
	get := func(k string) string { return pyStrip(form[k]) }
	name, cwd, model, role, prompt := get("name"), get("cwd"), get("model"), get("role"), get("prompt")
	killable := "unknown"
	if x, ok := form["killable"]; ok && x != "" {
		killable = pyStrip(x)
	}
	switch {
	case !reAgentName.MatchString(name):
		return "refused", "名前は英小文字で始まり、英小文字・数字・_・- の 32 字以内にしてください"
	case cwd != "" && (utf8.RuneCountInString(cwd) > cwdMax || strings.Contains(cwd, "\n") || !(strings.HasPrefix(cwd, "/") || strings.HasPrefix(cwd, "~"))):
		return "refused", "作業ディレクトリは絶対パス(/ か ~ で始まる)にしてください"
	case model != "" && !reModel.MatchString(model):
		return "refused", "モデル名に使えない文字があります"
	case role != "" && !reRoleName.MatchString(role):
		return "refused", "役割名に使えない文字があります"
	case utf8.RuneCountInString(prompt) > promptMax:
		return "refused", fmt.Sprintf("最初の指示は %d 字までです", promptMax)
	case killable != "unknown" && killable != "yes" && killable != "no":
		return "refused", "止めてよいかの指定が不正です"
	}
	args := []string{"agent-new", name}
	for _, x := range [][2]string{{"--cwd", cwd}, {"--model", model}, {"--role", role}, {"--prompt", prompt}} {
		if x[1] != "" {
			args = append(args, x[0], x[1])
		}
	}
	if killable != "unknown" {
		args = append(args, "--killable", killable)
	}
	rc, out := runTB(sess, spawnTimeout, args...)
	st := rcState(rc)
	if st == "done" {
		return st, ""
	}
	// tb のエラー文(「tb: ...」の 1 行目)をそのまま見せる。値は表示時にエスケープする
	msg := "作成できませんでした"
	for _, ln := range pySplitlines(out) {
		if rest, ok := strings.CutPrefix(ln, "tb: "); ok {
			msg = rest
			break
		}
	}
	return st, runesN(msg, 300)
}

// postAgentOp は POST /agent-op(agent の停止・復活と、承認待ちの操作の承認・却下)。
func (s *Server) postAgentOp(q *rq, form map[string]string) {
	op := form["op"]
	var st string
	switch op {
	case "approve", "reject":
		lid := form["id"]
		if !reOpID.MatchString(lid) {
			s.text(q, 400, "Bad Request")
			return
		}
		n, _ := strconv.Atoi(lid)
		st = agentDecide(q.name, n, op == "approve")
		s.logf("agent-op: %s L-%s %s\n", op, lid, st)
	case "stop", "force-stop", "revive", "revive-fresh":
		name := form["name"]
		if !reAgentName.MatchString(name) {
			s.text(q, 400, "Bad Request")
			return
		}
		if op == "force-stop" && form["confirm"] != "1" {
			s.redirect(q, "/agents?"+pyURLEncode(kvs{{"op", "refused"}}))
			return
		}
		st = agentOp(q.name, op, name)
		s.logf("agent-op: %s %s %s\n", op, name, st)
	default:
		s.text(q, 400, "Bad Request")
		return
	}
	s.redirect(q, "/agents?"+pyURLEncode(kvs{{"op", st}}))
}

// postAgentNew は POST /agent-new。断られたらフォームを出し直す(400)。
func (s *Server) postAgentNew(q *rq, form map[string]string, csrf string) {
	st, errMsg := agentNew(q.name, form)
	s.logf("agent-new: %s\n", st)
	if st == "done" || st == "pending" {
		s.redirect(q, "/agents?"+pyURLEncode(kvs{{"op", st}}))
		return
	}
	title, body := s.pageAgentNew(q, csrf, errMsg, form)
	s.html(q, 400, s.page(q, title, body, "/agents", "", pageOpts{norefresh: true}))
}
