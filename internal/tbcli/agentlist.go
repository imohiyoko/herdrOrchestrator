package tbcli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// 台帳と判定材料の一覧(taskboard/lib/agents.sh の cmd_agents / agent_edges)。

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

type agentEdge struct {
	verb, from, to, status string
	n                      float64
	last                   string
}

// agentEdges は agent_edges: taskboard/sql/agent_edges.sql の行を 主体 -動詞→ 対象 のエッジにする。
func agentEdges(db *store.DB, st tickets.StatusSet) ([]agentEdge, error) {
	b, err := os.ReadFile(filepath.Join(os.Getenv("TB_ROOT"), "sql", "agent_edges.sql"))
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "--") {
			lines = append(lines, l)
		}
	}
	q := strings.ReplaceAll(strings.TrimRight(strings.Join(lines, "\n"), "\n"), "__HELD_STATUSES__", st.SQLList("active", "review", "blocked"))
	rows, err := sqlObjs(db.DB, nil, q)
	if err != nil {
		return nil, err
	}
	cond := map[string]string{}
	var es []agentEdge
	for _, r := range rows {
		src, dst, proj := r.Get("src"), r.Get("dst"), r.Get("project")
		fill := func(v any) string {
			if v != nil {
				return jvStr(v)
			}
			if proj == nil || proj == false {
				return ""
			}
			p := jvStr(proj)
			if _, ok := cond[p]; !ok {
				cond[p] = message.TargetFor(db.DB, db.Session, p)
			}
			return cond[p]
		}
		s, d := fill(src), fill(dst)
		if s == "" || d == "" || s == d {
			continue
		}
		n, _ := jv.Num(r.Get("n"))
		es = append(es, agentEdge{jvStr(r.Get("verb")), s, d, jvStr(r.Get("status")), n, jvStr(r.Get("last_at"))})
	}
	key := func(e agentEdge) []string { return []string{e.verb, e.from, e.to, e.status} }
	sort.SliceStable(es, func(i, j int) bool { return slices.Compare(key(es[i]), key(es[j])) < 0 })
	var out []agentEdge
	for _, e := range es {
		if k := len(out) - 1; k >= 0 && slices.Equal(key(out[k]), key(e)) {
			out[k].n += e.n
			if e.last > out[k].last {
				out[k].last = e.last
			}
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

type graphNode struct {
	name, state  string
	status, role any
}

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
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		fmt.Fprintln(stderr, "tb: herdr agent list に失敗しました(台帳の内容だけを出します)")
	}
	where := "1=1"
	if !all {
		where = "(a.state='running' OR a.stopped_at > strftime('%Y-%m-%dT%H:%M:%SZ','now','-7 days'))"
	}
	rows, err := sqlObjs(db.DB, nil, `SELECT a.name, a.state, a.pane_id, a.session_id, a.cwd, a.model, a.role, a.killable_reason, a.killable_by, a.killable_at,
      a.created_by, a.created_at, a.stopped_at, a.spawned_by, a.spawn_task,
      (SELECT COUNT(*) FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (`+st.SQLList("active", "review", "blocked")+`)) AS open_tasks,
      (SELECT COUNT(*) FROM comments c LEFT JOIN tasks t ON t.id=c.task_id WHERE c.from_name=a.name AND c.kind IN ('question','blocked')
         AND c.resolved_at IS NULL AND COALESCE(t.status,'') NOT IN (`+st.SQLList("done", "cancelled")+`)) AS open_questions,
      aa.last_prompt_at, aa.last_stop_at,
      CASE WHEN aa.last_stop_at IS NOT NULL AND (aa.last_prompt_at IS NULL OR aa.last_stop_at >= aa.last_prompt_at)
           THEN CAST((julianday('now') - julianday(aa.last_stop_at)) * 1440 AS INTEGER) END AS idle_min
    FROM agents a LEFT JOIN main.agent_activity aa ON aa.session_id=a.session_id
    WHERE `+where+` ORDER BY a.state, a.name`)
	if err != nil {
		return err
	}
	// 実効の判定フラグと herdr の状態を足す
	roles := agents.NewRoles(db.DB, db.Session)
	for i, r := range rows {
		name := jvStr(r.Get("name"))
		a, _ := liveOf(live, name)
		var status, rfile, rname any
		if s := liveStatus(a); s != "" {
			status = s
		}
		if fs := roles.FilesForLoose(name); len(fs) > 0 {
			var lab []string
			for _, f := range fs {
				lab = append(lab, f+".md")
			}
			rfile, rname = strings.Join(lab, ","), strings.Join(fs, ",")
		}
		rows[i] = r.Set("killable", killableOf(db, name)).Set("agent_status", status).Set("role_file", rfile).Set("role_name", rname)
	}
	if graph {
		edges, err := agentEdges(db, st)
		if err != nil {
			return die("関係を読めません(DB)")
		}
		var nodes []graphNode
		have := map[string]bool{}
		for _, r := range rows {
			n := jvStr(r.Get("name"))
			have[n] = true
			nodes = append(nodes, graphNode{n, jvStr(r.Get("state")), r.Get("agent_status"), r.Get("role_name")})
		}
		// 関係の端に居るが一覧に無い名前(止まって 7 日を過ぎた agent・cli など)も点として足す
		var extra []string
		for _, e := range edges {
			for _, n := range []string{e.from, e.to} {
				if !have[n] && !slices.Contains(extra, n) {
					extra = append(extra, n)
				}
			}
		}
		sort.Strings(extra)
		for _, n := range extra {
			var role any
			if s, _ := roles.NameOf(n); s != "" {
				role = s
			}
			nodes = append(nodes, graphNode{n, "-", nil, role})
		}
		if js {
			ns := make([]any, len(nodes))
			for i, n := range nodes {
				ns[i] = jv.Obj{{K: "name", V: n.name}, {K: "state", V: n.state}, {K: "agent_status", V: n.status}, {K: "role", V: n.role}}
			}
			es := make([]any, len(edges))
			for i, e := range edges {
				es[i] = jv.Obj{{K: "verb", V: e.verb}, {K: "from", V: e.from}, {K: "to", V: e.to}, {K: "status", V: e.status}, {K: "n", V: e.n}, {K: "last_at", V: e.last}}
			}
			fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "nodes", V: ns}, {K: "edges", V: es}}))
			return nil
		}
		sort.SliceStable(nodes, func(i, j int) bool { return orDash(nodes[i].role) < orDash(nodes[j].role) })
		for i, n := range nodes {
			if i == 0 || orDash(nodes[i-1].role) != orDash(n.role) {
				fmt.Fprintf(w, "[%s]\n", orDash(n.role))
			}
			fmt.Fprintf(w, "  %s  %s/%s\n", n.name, n.state, orDash(n.status))
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "関係(直近 7 日。spawn と lead は今の状態):")
		if len(edges) == 0 {
			fmt.Fprintln(w, "  (なし)")
			return nil
		}
		sort.SliceStable(edges, func(i, j int) bool {
			return slices.Compare([]string{edges[i].from, edges[i].to, edges[i].verb}, []string{edges[j].from, edges[j].to, edges[j].verb}) < 0
		})
		for _, e := range edges {
			times := ""
			if e.n > 1 {
				times = " ×" + jv.NumStr(e.n)
			}
			den := ""
			if e.status == "denied" {
				den = "  [denied]"
			}
			fmt.Fprintf(w, "  %s  -%s%s→  %s%s\n", e.from, e.verb, times, e.to, den)
		}
		return nil
	}
	if js {
		compactRows(w, rows)
		return nil
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
