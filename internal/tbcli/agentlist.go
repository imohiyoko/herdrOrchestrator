package tbcli

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
)

// 台帳と判定材料の一覧(taskboard/lib/agents.sh の cmd_agents)。読むのは agent.ledger / agent.graph。

func jvStr(v any) string {
	if v == nil {
		return ""
	}
	return jv.Scalar(v)
}

// orDash は jq の (.x // "-")(null・false・空でない値はそのまま)。
func orDash(v any) string {
	if v == nil || v == false {
		return "-"
	}
	return jv.Scalar(v)
}

// nilIfEmpty は空文字を null にする(--graph --json の agent_status・role)。
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const liveErrList = "tb: herdr agent list に失敗しました(台帳の内容だけを出します)"

func cmdAgents(args []string, w, stderr io.Writer) error {
	js, all, graph, group := false, false, false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			js = true
		case "--all":
			all = true
		case "--graph":
			graph = true
		case "--group":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			if args[i+1] != "role" {
				return usage("agents: --group は role だけ")
			}
			group = "role"
			i++
		default:
			return usage("agents: 不明な引数 %s", args[i])
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if graph {
		var g agent.GraphResult
		if err := callAppW(db, app.Human(), io.Discard, "agent.graph", agent.GraphReq{All: all}, &g); err != nil {
			return err
		}
		if g.LiveErr {
			fmt.Fprintln(stderr, liveErrList)
		}
		return printGraph(w, g, js)
	}
	var res agent.LedgerResult
	if err := callAppW(db, app.Human(), io.Discard, "agent.ledger", agent.LedgerReq{All: all}, &res); err != nil {
		return err
	}
	if res.LiveErr {
		fmt.Fprintln(stderr, liveErrList)
	}
	if js {
		fmt.Fprint(w, res.Rows)
		return nil
	}
	vs, err := jv.DecodeAll([]byte(res.Rows))
	if err != nil {
		return err
	}
	rows := make([]jv.Obj, 0, len(vs))
	for _, v := range vs {
		if o, ok := v.(jv.Obj); ok {
			rows = append(rows, o)
		}
	}
	line := func(r jv.Obj, withFile bool) string {
		f := []string{jvStr(r.Get("name")), jvStr(r.Get("state")), orDash(r.Get("agent_status")), jvStr(r.Get("killable")),
			"tasks:" + jvStr(r.Get("open_tasks")), "q:" + jvStr(r.Get("open_questions")), "idle:" + orDash(r.Get("idle_min")) + "m"}
		if withFile {
			f = append(f, orDash(r.Get("role_file")))
		}
		reason := r.Get("killable_reason")
		if reason == false {
			reason = nil
		}
		return strings.Join(append(f, jvStr(reason)), "\t")
	}
	if group == "role" {
		sort.SliceStable(rows, func(i, j int) bool { return orDash(rows[i].Get("role_name")) < orDash(rows[j].Get("role_name")) })
		for i, r := range rows {
			if i == 0 || orDash(rows[i-1].Get("role_name")) != orDash(r.Get("role_name")) {
				fmt.Fprintf(w, "[%s]\n", orDash(r.Get("role_name")))
			}
			fmt.Fprintln(w, "  "+line(r, false))
		}
		return nil
	}
	for _, r := range rows {
		fmt.Fprintln(w, line(r, true))
	}
	return nil
}

func printGraph(w io.Writer, g agent.GraphResult, js bool) error {
	nodes, edges := g.Nodes, g.Edges
	if js {
		ns := make([]any, len(nodes))
		for i, n := range nodes {
			ns[i] = jv.Obj{{K: "name", V: n.Name}, {K: "state", V: n.State}, {K: "agent_status", V: nilIfEmpty(n.Status)}, {K: "role", V: nilIfEmpty(n.Role)}}
		}
		es := make([]any, len(edges))
		for i, e := range edges {
			es[i] = jv.Obj{{K: "verb", V: e.Verb}, {K: "from", V: e.From}, {K: "to", V: e.To}, {K: "status", V: e.Status}, {K: "n", V: e.N}, {K: "last_at", V: e.LastAt}}
		}
		fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "nodes", V: ns}, {K: "edges", V: es}}))
		return nil
	}
	role := func(n agent.GraphNode) string { return orDash(nilIfEmpty(n.Role)) }
	sort.SliceStable(nodes, func(i, j int) bool { return role(nodes[i]) < role(nodes[j]) })
	for i, n := range nodes {
		if i == 0 || role(nodes[i-1]) != role(n) {
			fmt.Fprintf(w, "[%s]\n", role(n))
		}
		fmt.Fprintf(w, "  %s  %s/%s\n", n.Name, n.State, orDash(nilIfEmpty(n.Status)))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "関係(直近 7 日。spawn と lead は今の状態):")
	if len(edges) == 0 {
		fmt.Fprintln(w, "  (なし)")
		return nil
	}
	sort.SliceStable(edges, func(i, j int) bool {
		return slices.Compare([]string{edges[i].From, edges[i].To, edges[i].Verb}, []string{edges[j].From, edges[j].To, edges[j].Verb}) < 0
	})
	for _, e := range edges {
		times := ""
		if e.N > 1 {
			times = " ×" + jv.NumStr(e.N)
		}
		den := ""
		if e.Status == "denied" {
			den = "  [denied]"
		}
		fmt.Fprintf(w, "  %s  -%s%s→  %s%s\n", e.From, e.Verb, times, e.To, den)
	}
	return nil
}
