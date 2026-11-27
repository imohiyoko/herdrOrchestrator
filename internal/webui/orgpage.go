package webui

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

// orgpage.go は組織図の画面(/org)。上司は設定 org で決まる(tb org と同じ。アプリケーション API の agent.org)。

const orgTimeout = 10 * time.Second

// agentOrg は agent.org を同じプロセスで人として呼ぶ。
func agentOrg(sess string) (agent.OrgResult, error) {
	var res agent.OrgResult
	ctx, cancel := context.WithTimeout(context.Background(), orgTimeout)
	defer cancel()
	c, err := appClient(sess)
	if err != nil {
		return res, err
	}
	err = c.Call(ctx, app.Human(), "agent.org", agent.OrgReq{}, &res)
	return res, err
}

func (s *Server) pageOrg(q *rq, c pageCtx) (string, string) {
	tree, mm := orgHTML(q)
	body := `<p class=note><a href="/agents">agent 一覧へ</a></p>`
	if mm != "" {
		c.opts.scripts = []string{"/static/mermaid.min.js", "/docs.js"}
		body += "<pre class=mermaid>" + E(mm) + "</pre><h2>一覧</h2>"
	}
	return "組織図", body + tree
}

// orgHTML は組織図の木(HTML)と、図の元(mermaid。設定 org が無い・誰もいないときは空)。agent 一覧と /org で使う。
func orgHTML(q *rq) (string, string) {
	if _, err := os.Stat(TBDB()); err != nil { // 見るだけで DB を作らない(agent.org は DB を書き込みで開く)
		return `<p class=note>(タスク DB がありません)</p>`, ""
	}
	org, err := agentOrg(q.name)
	if err != nil {
		return `<p class=warn>組織図を読めませんでした: ` + E(err.Error()) + `</p>`, ""
	}
	if !org.Set {
		return `<p class=note>組織図は未設定です(設定 org)。誰にも上司はいません。` +
			`人が <code>tb config set org</code> で書きます(例: <code>setup/examples/org.conf</code>)。</p>`, ""
	}
	v := q.data()
	reg := v.agentRegistry()
	live := map[string]any{}
	if as, aerr := getAgents(q.name); aerr == "" {
		for _, a := range as {
			if n := orEmpty(a["name"]); n != "" {
				live[n] = a["agent_status"]
			}
		}
	}
	mgr, st := map[string]string{}, map[string]string{}
	var names []string
	for _, a := range org.Agents {
		names = append(names, a.Name)
		mgr[a.Name], st[a.Name] = a.Manager, a.State
	}
	sort.Strings(names)
	kids := map[string][]string{}
	for _, n := range names {
		if m := mgr[n]; m != "" && m != "?" {
			kids[m] = append(kids[m], n)
		}
	}
	label := func(n string) string {
		h := `<a href="/agents/` + pyQuote(n, "") + `">` + E(n) + `</a>`
		if r := v.regRole(reg, n); r != "" {
			h += ` <span class=note>` + E(r) + `</span>`
		}
		switch {
		case live[n] != nil:
			h += " " + stateHTML(live[n])
		case st[n] == "stopped":
			h += ` <span class=note>(停止中)</span>`
		case st[n] == "-":
			h += ` <span class=note>(台帳にいない)</span>`
		}
		return h
	}
	// 木(上司のいない人が根)。循環していて根から届かない人は下に分けて出す
	seen := map[string]bool{}
	var tree func(n string) string
	tree = func(n string) string {
		seen[n] = true
		h := "<li>" + label(n)
		var sub []string
		for _, c := range kids[n] {
			if !seen[c] {
				sub = append(sub, tree(c))
			}
		}
		if len(sub) > 0 {
			h += "<ul>" + strings.Join(sub, "") + "</ul>"
		}
		return h + "</li>"
	}
	var roots []string
	for _, n := range names {
		if m := mgr[n]; (m == "" || m == "?") && !seen[n] {
			roots = append(roots, tree(n))
		}
	}
	var unk, rest []string
	for _, n := range names {
		if mgr[n] == "?" {
			unk = append(unk, E(n))
		}
		if !seen[n] {
			rest = append(rest, E(n))
		}
	}
	if len(names) == 0 {
		return `<p class=note>(台帳に agent がいません)</p>`, ""
	}
	ids := map[string]string{}
	for i, n := range names {
		ids[n] = "n" + strconv.Itoa(i)
	}
	mm := []string{"flowchart TD"}
	for _, n := range names {
		lab := mermaidSafe(n)
		if r := v.regRole(reg, n); r != "" {
			lab += "<br>" + mermaidSafe(r)
		}
		mm = append(mm, "  "+ids[n]+`["`+lab+`"]`)
	}
	for _, n := range names {
		if m := mgr[n]; m != "" && m != "?" {
			mm = append(mm, "  "+ids[m]+" --> "+ids[n])
		}
	}
	body := "<ul class=org>" + strings.Join(roots, "") + "</ul>"
	if len(unk) > 0 {
		body += `<p class=warn>役割を確かめられず上司が決まらない: ` + strings.Join(unk, " ") + `</p>`
	}
	if len(rest) > 0 {
		body += `<p class=warn>循環していて木にできない: ` + strings.Join(rest, " ") + `</p>`
	}
	body += `<p class=note>上司は設定 org(` + E(org.Source) + `)で決まります。手元では <code>tb org</code> で同じものを出せます。</p>`
	return body, strings.Join(mm, "\n")
}
