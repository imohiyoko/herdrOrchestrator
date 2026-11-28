package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	appagent "github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	appticket "github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
)

// agents.go は agent の画面のための読み取り: graph.py の関係グラフと役割の一覧、data.py の台帳(止めてよいか・保護)と担当タスク、
// herdr.py の agent read。

var (
	verbLabel = map[string]string{"spawn": "spawn", "lead": "指揮", "report": "報告", "analyze": "解析",
		"role": "ロール変更", "read": "中身を読む", "send:task": "依頼", "send:info": "連絡", "send:answer": "回答"}
	reMermaidUnsafe = regexp.MustCompile(`[^A-Za-z0-9._:@,-]`)
)

// roleNames は役割名の一覧(_ で始まるもの・README を除く)。DB の行とファイル。
func (v *sv) roleNames() []string {
	set := map[string]bool{}
	for k := range v.roleDB() {
		if n, ok := strings.CutPrefix(k, "role:"); ok {
			set[n] = true
		}
	}
	if ents, err := os.ReadDir(v.rolesDir()); err == nil {
		for _, e := range ents {
			if n, ok := strings.CutSuffix(e.Name(), ".md"); ok {
				set[n] = true
			}
		}
	}
	var out []string
	for n := range set {
		if reRole.MatchString(n) && !strings.HasPrefix(n, "_") && n != "README" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// roleOf は役割名をカンマ区切りで(無ければ "")。
func (v *sv) roleOf(name, regRole string) string { return strings.Join(v.rolesOf(name, regRole), ",") }

func removePySpace(s string) string {
	return strings.Map(func(r rune) rune {
		if pyIsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// targetFor は project → 指揮官(core.sh の target_for。後に書いた行が勝つ。* の行は既定の宛先で、project の行が優先)。
func (v *sv) targetFor(project string) string {
	text, _, _ := v.configText("projects")
	star, exact := "", ""
	for _, line := range pySplitlines(text) {
		line = strings.TrimRight(line, "\r")
		k, val, sep := strings.Cut(line, "=")
		k, val = removePySpace(k), removePySpace(val)
		if sep && k != "" && !strings.HasPrefix(k, "#") && val != "" {
			if k == "*" {
				star = val
			}
			if k == project {
				exact = val
			}
		}
	}
	if exact != "" {
		return exact
	}
	return star // どちらも無ければ空(既定の宛先はコードで決めない。tb の message.TargetFor と同じ)
}

// edgeSQL は taskboard/sql/agent_edges.sql(-- で始まる行を除く)。
func edgeSQL() string {
	b, err := os.ReadFile(filepath.Join(HerdrRoot(), "taskboard", "sql", "agent_edges.sql"))
	if err != nil {
		return ""
	}
	var keep []string
	for _, ln := range pySplitlines(pyDecodeUTF8(b)) {
		if !strings.HasPrefix(ln, "--") {
			keep = append(keep, ln)
		}
	}
	return strings.Join(keep, "\n")
}

type edge struct {
	verb, from, to, status string
	n                      int64
	lastAt                 string
}

// edges は agent_edges.sql の行をまとめる(tb の agent_edges と同じ)。
func (v *sv) edges(rows []row) []*edge {
	acc := map[[4]string]*edge{}
	var order []*edge
	end := func(col string, r row) string {
		if truthy(r[col]) {
			return pyStr(r[col])
		}
		if truthy(r["project"]) {
			return v.targetFor(pyStr(r["project"]))
		}
		return ""
	}
	for _, r := range rows {
		src, dst := end("src", r), end("dst", r)
		if src == "" || dst == "" || src == dst {
			continue
		}
		st := "ok"
		if truthy(r["status"]) {
			st = pyStr(r["status"])
		}
		k := [4]string{pyStr(r["verb"]), src, dst, st}
		e, ok := acc[k]
		if !ok {
			e = &edge{verb: k[0], from: src, to: dst, status: st}
			acc[k] = e
			order = append(order, e)
		}
		e.n += intOf(r["n"])
		if la := orEmpty(r["last_at"]); la > e.lastAt {
			e.lastAt = la
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.from != b.from {
			return a.from < b.from
		}
		if a.to != b.to {
			return a.to < b.to
		}
		return a.verb < b.verb
	})
	return order
}

type gnode struct {
	name    string
	role    string // "" は役割なし
	agent   map[string]any
	hasRole bool
}

type roleGroup struct {
	role  string
	nodes []gnode
}

// byRole は役割ごとのまとまり(役割名の順、役割なしは最後)。
func byRole(nodes []gnode) []roleGroup {
	idx := map[string]int{}
	var gs []roleGroup
	for _, n := range nodes {
		i, ok := idx[n.role]
		if !ok {
			i = len(gs)
			idx[n.role] = i
			gs = append(gs, roleGroup{role: n.role})
		}
		gs[i].nodes = append(gs[i].nodes, n)
	}
	sort.SliceStable(gs, func(i, j int) bool {
		a, b := gs[i].role == "", gs[j].role == ""
		if a != b {
			return !a
		}
		return gs[i].role < gs[j].role
	})
	return gs
}

func mermaidSafe(s string) string {
	r := []rune(reMermaidUnsafe.ReplaceAllString(s, "_"))
	if len(r) > 64 {
		r = r[:64]
	}
	if len(r) == 0 {
		return "_"
	}
	return string(r)
}

// mermaidGraph は flowchart の元(graph.mermaid)。
func mermaidGraph(nodes []gnode, es []*edge) string {
	ids := map[string]string{}
	for i, n := range nodes {
		ids[n.name] = "n" + strconv.Itoa(i)
	}
	out := []string{"flowchart LR"}
	for gi, g := range byRole(nodes) {
		label := "役割なし"
		if g.role != "" {
			label = mermaidSafe(g.role)
		}
		out = append(out, "  subgraph g"+strconv.Itoa(gi)+`["`+label+`"]`)
		for _, n := range g.nodes {
			out = append(out, "    "+ids[n.name]+`["`+mermaidSafe(n.name)+`"]`)
		}
		out = append(out, "  end")
	}
	for _, e := range es {
		f, ok1 := ids[e.from]
		t, ok2 := ids[e.to]
		if !ok1 || !ok2 {
			continue
		}
		label, ok := verbLabel[e.verb]
		if !ok {
			label = mermaidSafe(e.verb)
		}
		if e.n > 1 {
			label += " x" + strconv.FormatInt(e.n, 10)
		}
		arrow := "-- " + label + " -->"
		if e.status == "denied" {
			arrow = "-. " + label + " 拒否 .->"
		}
		out = append(out, "  "+f+" "+arrow+" "+t)
	}
	return strings.Join(out, "\n")
}

// ---- 台帳(止めてよいか・保護)

type protectRule struct{ pat, why string }

// protectRules は config の protect([(パターン, 理由)])。
func (v *sv) protectRules() []protectRule {
	text, _, _ := v.configText("protect")
	var out []protectRule
	for _, line := range pySplitlines(text) {
		line, _, _ = strings.Cut(line, "#")
		line = pyStrip(line)
		if line == "" {
			continue
		}
		k, val, _ := strings.Cut(line, "=")
		k, val = pyStrip(k), pyStrip(val)
		if val == "" {
			val = "protect: " + k
		}
		out = append(out, protectRule{k, val})
	}
	return out
}

// protectReason は保護されていれば理由(上から見て最初に一致した行)。
func (v *sv) protectReason(name, regRole string, rules []protectRule) string {
	for _, r := range rules {
		hit := false
		if g, ok := strings.CutPrefix(r.pat, "role:"); ok {
			for _, x := range v.rolesOf(name, regRole) {
				if fnmatchcase(x, g) {
					hit = true
					break
				}
			}
		} else {
			hit = fnmatchcase(name, r.pat)
		}
		if hit {
			return r.why
		}
	}
	return ""
}

func (v *sv) withProtect(rows []row) []row {
	rules := v.protectRules()
	if len(rules) > 0 {
		for _, r := range rows {
			if why := v.protectReason(pyStr(r["name"]), orEmpty(r["role"]), rules); why != "" {
				r["killable_now"], r["protect_reason"] = "protected", why
			}
		}
	}
	return rows
}

// agentRegistry は tb の台帳(agents)。{名前: 行}。
func (v *sv) agentRegistry() map[string]row {
	out := map[string]row{}
	for _, r := range v.withProtect(appagent.LedgerAll(v.qChecked)) {
		out[pyStr(r["name"])] = r
	}
	return out
}

// registryRow は台帳の行。台帳に行が無くても、protect に一致すれば保護中の行を返す。
func (v *sv) registryRow(reg map[string]row, name string) row {
	r, ok := reg[name]
	if !ok && name != "" {
		if why := v.protectReason(name, "", v.protectRules()); why != "" {
			r = row{"name": name, "killable_now": "protected", "protect_reason": why}
		}
	}
	return r
}

func (v *sv) stoppedAgents() []row {
	return v.withProtect(appagent.LedgerStopped(v.qChecked))
}

// tasksForAgent は担当(名前か session)の閉じていないタスク。
func (v *sv) tasksForAgent(a map[string]any) []row {
	var rows []row
	if truthy(a["name"]) {
		rows = append(rows, appticket.OpenByOwner(v.qChecked, a["name"])...)
	}
	if sid := agentSession(a); truthy(sid) {
		have := map[string]bool{}
		for _, r := range rows {
			have[pyKey(r["id"])] = true
		}
		for _, r := range appticket.OpenBySession(v.qChecked, sid) {
			if !have[pyKey(r["id"])] {
				rows = append(rows, r)
			}
		}
	}
	return rows
}

type herdrOut struct {
	out, err string
}

// readAgent は agent read の出力(無害化前)。err が空でなければ読めなかった。
func readAgent(sess string, a map[string]any, lines int, source string) (string, string) {
	pane, ok := a["pane_id"]
	if !truthy(pane) || !ok {
		return "", "pane_id がありません"
	}
	p := pyStr(pane)
	r := cached(sess, "read\x00"+p+"\x00"+strconv.Itoa(lines)+"\x00"+source, func() herdrOut {
		out, err := runHerdr(sess, "agent", "read", p, "--source", source, "--lines", strconv.Itoa(lines), "--format", "text")
		return herdrOut{out, err}
	})
	return r.out, r.err
}
