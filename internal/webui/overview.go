package webui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	appticket "github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
)

// overview.go は概要(/。views.page_overview)。読み取りのみ。

func (s *Server) pageOverview(q *rq, c pageCtx) (string, string) {
	v := q.data()
	agents, err := getAgents(q.name)
	h := v.needsHuman()
	counts := map[string]int{}
	for _, a := range agents {
		st := "unknown"
		if truthy(a["agent_status"]) {
			st = pyStr(a["agent_status"])
		}
		counts[st]++
	}
	type stallAg struct {
		a   map[string]any
		act row
	}
	var stall []stallAg
	for _, a := range agents {
		if act := v.activityFor(a); stalled(a, act) {
			stall = append(stall, stallAg{a, act})
		}
	}
	var bl []string
	for _, a := range h.blocked {
		bl = append(bl, alink(a, ""))
	}
	body := errHTML(err) + "<h2>承認待ち</h2>" + table([]string{"種別", "件数", ""}, [][]string{
		{"報告 (question/blocked)", strconv.Itoa(len(h.comments)), `<a href="/inbox">見る</a>`},
		{"代行依頼 (pending-human)", strconv.Itoa(len(h.proxies)), `<a href="/inbox">見る</a>`},
		{"blocked の agent", strconv.Itoa(len(h.blocked)), strings.Join(bl, ", ")},
		{"承認待ち (Q-&lt;n&gt;。送信・agent の操作・コマンド)", strconv.Itoa(len(v.pendingRequests(notifyMax))), `<a href="/inbox#approvals">見る</a>`}})
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ps []string
	for _, k := range keys {
		ps = append(ps, stateHTML(k)+": "+strconv.Itoa(counts[k]))
	}
	body += "<h2>agent の状態</h2>" + orStr(strings.Join(ps, " / "), "<p class=note>(なし)</p>")
	var rows [][]string
	for _, x := range stall {
		rows = append(rows, []string{alink(x.a, ""), ev(x.act["last_prompt_at"])})
	}
	body += fmt.Sprintf("<h2>滞留 (working のまま %d 分超)</h2>", stallMin) + table([]string{"agent", "最終入力"}, rows)
	rows = nil
	for _, t := range appticket.RecentDone(v.qChecked, 5) {
		rows = append(rows, []string{tlink(t["id"]), ev(t["project"]), ev(t["title"]), ev(t["owner_agent"]), ev(t["updated_at"])})
	}
	body += "<h2>直近の完了</h2>" + table([]string{"タスク", "PJ", "題", "担当", "更新"}, rows)
	return "概要", body
}
