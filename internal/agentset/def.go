// Package agentset は agent セット: 役割・最初の指示・親子をまとめた定義を、1 つの herdr workspace に
// pane を分けて 1 コマンドで立ち上げる・止める・状態を見る(tb agentset)。
//
// セットはアプリケーション API(internal/app/agent の agent.new / agent.revive / agent.stop / agent.list)を
// 呼ぶ入口の 1 つで、台帳や store を直接触らない。承認・上限・役割の検査は API に任せる(ADR 0008)。
package agentset

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

// Def はセットの定義。
type Def struct {
	Name      string
	Workspace string // workspace の label(空ならセットの名前)
	Cwd       string // メンバーの既定の作業ディレクトリ(空なら tb の既定)
	Parent    string // セットの上に付く既存の agent(メンバーの parent = ~set で使う)
	// OnParentExit は親が全員止まったメンバー・adopt の agent の扱い(tb agentset reap で行う):
	// keep(既定。そのまま。tb agent shed の orphan に出る)/ grandparent(止まった親の親を足す。いなければセットの parent)/
	// set-parent(セットの parent を足す)/ stop(止める)
	OnParentExit string
	Members      []Member
	Adopt        []Adopt
}

// Adopt は [adopt] の 1 行(すでに動いている agent をセットの子にする。up で親を足し、down で外す。止めない)。
type Adopt struct {
	Agent  string
	Parent string // メンバー名 / ~set / 既存の agent 名
}

// Member はセットのメンバー(同じ役割の agent を Count 体)。
type Member struct {
	Name   string
	Role   string
	Model  string
	Kind   string
	Cwd    string
	Prompt string
	Count  int
	Max    int    // scale の上限(0 は書いていない = Count まで、-1 は上限なし)
	Mode   string // job: 1 タスクに 1 体(tb agentset run で立て、タスクが終わったら reap で止める)
	Parent string // セットのメンバー名 / ~set / 既存の agent 名(空なら親なし)
	// Every と Say は schedule(every <N>m|<N>h: <本文>): up で scheduler のジョブ agentset-<セット>-<メンバー> を書き、down で消す
	Every int // 分(0 なら schedule なし)
	Say   string
}

// JobName は schedule を書いたメンバーの scheduler のジョブの名前。
func (d Def) JobName(m Member) string { return "agentset-" + d.Name + "-" + m.Name }

// JobBody は schedule のジョブの本文(TARGET はメンバーの agent、終日、Every 分おき)。
func (d Def) JobBody(m Member) string {
	return "# agent セット " + d.Name + " のメンバー " + m.Name + " の schedule(tb agentset up が書き、down で消す。直すなら定義を直して up)\n" +
		"TARGET=" + m.agentNames(d.Name)[0] + "\n" +
		"PROMPT='" + m.Say + "'\n" +
		"WINDOW=00:00-23:59\n" +
		"INTERVAL_MIN=" + strconv.Itoa(m.Every) + "\n"
}

// Scalable は数を変えられるメンバー(count が 2 以上か、max を書いた)。agent の名前は <セット>-<メンバー>-<N>。
func (m Member) Scalable() bool { return m.Count > 1 || m.Max != 0 || m.Mode == "job" }

// Limit は scale の上限(-1 は上限なし)。
func (m Member) Limit() int {
	if m.Max == 0 {
		return m.Count
	}
	return m.Max
}

var (
	// ReName はセットの名前(agent の名前 <セット>-<メンバー>[-<N>] の頭になる)。
	ReName   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
	reMember = regexp.MustCompile(`^[a-z][a-z0-9_]{0,11}$`)
	reTrail  = regexp.MustCompile(`\s+#\s.*$`)
)

// まだ入れていない項目(設計案の段 4 以降)。書いてあれば断る(黙って無視しない)。
var later = map[string]bool{"panes_per_workspace": true}

// Parse は定義の本文を読む。書式は「キー = 値」の行と、メンバーごとの段落 [member <名前>]。
// # で始まる行と、空白のあとの「# 」から行末はコメント。
func Parse(name, body string) (Def, error) {
	d := Def{Name: name}
	if !ReName.MatchString(name) {
		return d, fmt.Errorf("セットの名前は英小文字で始まり、英小文字・数字・_ の 16 字以内: %s", name)
	}
	var m *Member
	adopt := false
	countSet := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for ln := 1; sc.Scan(); ln++ {
		line := strings.TrimSpace(reTrail.ReplaceAllString(sc.Text(), ""))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			sec, ok := strings.CutSuffix(line[1:], "]")
			f := strings.Fields(sec)
			if !ok || len(f) == 0 {
				return d, fmt.Errorf("%d 行目: 段落の見出しは [member <名前>]: %s", ln, line)
			}
			if len(f) == 1 && f[0] == "adopt" {
				m, adopt = nil, true
				continue
			}
			if f[0] != "member" {
				return d, fmt.Errorf("%d 行目: 段落の見出しは [member <名前>] か [adopt]: %s", ln, line)
			}
			adopt = false
			if len(f) != 2 || !reMember.MatchString(f[1]) {
				return d, fmt.Errorf("%d 行目: メンバーの名前は英小文字で始まり、英小文字・数字・_ の 12 字以内: %s", ln, line)
			}
			for _, x := range d.Members {
				if x.Name == f[1] {
					return d, fmt.Errorf("%d 行目: メンバー %s が 2 回あります", ln, f[1])
				}
			}
			d.Members = append(d.Members, Member{Name: f[1], Count: 1})
			m = &d.Members[len(d.Members)-1]
			continue
		}
		if adopt {
			a, p, ok := strings.Cut(line, "->")
			a, p = strings.TrimSpace(a), strings.TrimSpace(p)
			if !ok || a == "" || p == "" {
				return d, fmt.Errorf("%d 行目: [adopt] の行は「<agent> -> <親>」: %s", ln, line)
			}
			d.Adopt = append(d.Adopt, Adopt{Agent: a, Parent: p})
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return d, fmt.Errorf("%d 行目: 「キー = 値」の形ではありません: %s", ln, line)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if later[k] {
			return d, fmt.Errorf("%d 行目: %s はまだ使えません(次の段で入れます)", ln, k)
		}
		if m == nil {
			switch k {
			case "workspace":
				d.Workspace = v
			case "cwd":
				d.Cwd = v
			case "parent":
				d.Parent = v
			case "on_parent_exit":
				switch v {
				case "keep", "grandparent", "set-parent", "stop":
				default:
					return d, fmt.Errorf("%d 行目: on_parent_exit は keep / grandparent / set-parent / stop: %s", ln, v)
				}
				d.OnParentExit = v
			default:
				return d, fmt.Errorf("%d 行目: セットのキーは workspace / cwd / parent / on_parent_exit: %s", ln, k)
			}
			continue
		}
		switch k {
		case "role":
			m.Role = v
		case "model":
			m.Model = v
		case "kind":
			m.Kind = v
		case "cwd":
			m.Cwd = v
		case "prompt":
			m.Prompt = v
		case "parent":
			m.Parent = v
		case "count":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return d, fmt.Errorf("%d 行目: count は 1 以上の数(job 型は 0 も): %s", ln, v)
			}
			m.Count = n
			countSet[m.Name] = true
		case "schedule":
			every, say, err := parseSchedule(v)
			if err != nil {
				return d, fmt.Errorf("%d 行目: %s", ln, err)
			}
			m.Every, m.Say = every, say
		case "mode":
			if v != "job" {
				return d, fmt.Errorf("%d 行目: mode は job だけ: %s", ln, v)
			}
			m.Mode = v
		case "max":
			if v == "unlimited" {
				m.Max = -1
				break
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return d, fmt.Errorf("%d 行目: max は 1 以上の数か unlimited: %s", ln, v)
			}
			m.Max = n
		default:
			return d, fmt.Errorf("%d 行目: メンバーのキーは role / model / kind / cwd / prompt / count / max / mode / parent / schedule: %s", ln, k)
		}
	}
	if err := sc.Err(); err != nil {
		return d, err
	}
	for i := range d.Members { // job 型は、書かなければ起動時 0 体・上限なし
		x := &d.Members[i]
		if x.Count == 0 && x.Mode != "job" {
			return d, fmt.Errorf("メンバー %s: count は 1 以上の数(job 型は 0 も)", x.Name)
		}
		if x.Mode == "job" {
			if !countSet[x.Name] {
				x.Count = 0
			}
			if x.Max == 0 {
				x.Max = -1
			}
		}
	}
	return d, d.check()
}

func (d Def) member(n string) (Member, bool) {
	for _, m := range d.Members {
		if m.Name == n {
			return m, true
		}
	}
	return Member{}, false
}

// check は定義全体の確かめ: メンバーがいる、親の指し先、親のメンバーは 1 体、輪が無い、agent の名前が使える。
func (d Def) check() error {
	if len(d.Members) == 0 {
		return fmt.Errorf("メンバーがいません([member <名前>] の段落が要ります)")
	}
	if d.Parent != "" && !agent.ValidName(d.Parent) {
		return fmt.Errorf("parent は agent の名前: %s", d.Parent)
	}
	for _, m := range d.Members {
		if m.Every > 0 && m.Scalable() {
			return fmt.Errorf("メンバー %s の schedule は count = 1 で max の無いメンバーだけ(送り先の agent が 1 体に決まらない)", m.Name)
		}
		if l := m.Limit(); l >= 0 && m.Count > l {
			return fmt.Errorf("メンバー %s の count(%d)が max(%d)を超えています", m.Name, m.Count, l)
		}
		probe := m
		if m.Scalable() { // 名前の長さは、ありうる番号の大きいほうで確かめる
			probe.Count = max(m.Count, m.Limit(), 99)
		}
		for _, n := range probe.agentNames(d.Name) {
			if !agent.ValidName(n) {
				return fmt.Errorf("agent の名前 %s が長すぎます(32 字まで。セットかメンバーの名前を短くする)", n)
			}
		}
		switch p := m.Parent; {
		case p == "":
		case p == "~set":
			if d.Parent == "" {
				return fmt.Errorf("メンバー %s の parent = ~set には、セットの parent が要ります", m.Name)
			}
		default:
			if pm, ok := d.member(p); ok {
				if pm.Scalable() {
					return fmt.Errorf("メンバー %s の親 %s は count = 1 で max の無いメンバーにしてください(親が複数体だとどれの子か決まらない)", m.Name, p)
				}
			} else if !agent.ValidName(p) {
				return fmt.Errorf("メンバー %s の parent はメンバー名・~set・agent の名前のどれか: %s", m.Name, p)
			}
		}
	}
	if d.OnParentExit == "set-parent" && d.Parent == "" {
		return fmt.Errorf("on_parent_exit = set-parent には、セットの parent が要ります")
	}
	own := map[string]bool{}
	for _, m := range d.Members {
		own[m.Name] = true
	}
	seenAdopt := map[string]bool{}
	for _, a := range d.Adopt {
		if !agent.ValidName(a.Agent) {
			return fmt.Errorf("[adopt] の agent の名前: %s", a.Agent)
		}
		if strings.HasPrefix(a.Agent, d.Name+"-") {
			return fmt.Errorf("[adopt] の %s はセットのメンバーの名前の形(%s-…)です。メンバーは adopt しません", a.Agent, d.Name)
		}
		if seenAdopt[a.Agent] {
			return fmt.Errorf("[adopt] に %s が 2 回あります", a.Agent)
		}
		seenAdopt[a.Agent] = true
		switch p := a.Parent; {
		case p == "~set":
			if d.Parent == "" {
				return fmt.Errorf("[adopt] の %s -> ~set には、セットの parent が要ります", a.Agent)
			}
		case own[p]:
			if pm, _ := d.member(p); pm.Scalable() {
				return fmt.Errorf("[adopt] の %s の親 %s は count = 1 で max の無いメンバーにしてください", a.Agent, p)
			}
		case !agent.ValidName(p) || p == a.Agent:
			return fmt.Errorf("[adopt] の %s の親はメンバー名・~set・agent の名前のどれか: %s", a.Agent, p)
		}
	}
	// 輪: メンバーの親をたどって、同じメンバーに戻らないこと
	for _, m := range d.Members {
		seen := map[string]bool{m.Name: true}
		for p := m.Parent; ; {
			pm, ok := d.member(p)
			if !ok {
				break
			}
			if seen[pm.Name] {
				return fmt.Errorf("メンバー %s の親をたどると輪になります", m.Name)
			}
			seen[pm.Name] = true
			p = pm.Parent
		}
	}
	return nil
}

// agentNames はメンバーの起動時の agent の名前: 数を変えないメンバーは <セット>-<メンバー>、変えられるなら <セット>-<メンバー>-<N>。
func (m Member) agentNames(set string) []string {
	if !m.Scalable() {
		return []string{set + "-" + m.Name}
	}
	out := make([]string, m.Count)
	for i := range out {
		out[i] = set + "-" + m.Name + "-" + strconv.Itoa(i+1)
	}
	return out
}

// Instance は起動する 1 体。
type Instance struct {
	Name   string // agent の名前
	Member Member
	Parent string // 親の agent の名前(空なら親なし)
}

// Order はメンバーの順: 親が先(定義の順を保ち、親のメンバーを先に出す)。
func (d Def) Order() []Member {
	var out []Member
	done := map[string]bool{}
	var visit func(m Member)
	visit = func(m Member) {
		if done[m.Name] {
			return
		}
		done[m.Name] = true
		if pm, ok := d.member(m.Parent); ok {
			visit(pm)
		}
		out = append(out, m)
	}
	for _, m := range d.Members {
		visit(m)
	}
	return out
}

// Plan は起動の順の 1 体ずつ(Order の順。止めるのは逆の順)。
func (d Def) Plan() []Instance {
	var out []Instance
	for _, m := range d.Order() {
		parent := d.ParentAgent(m)
		for _, n := range m.agentNames(d.Name) {
			out = append(out, Instance{Name: n, Member: m, Parent: parent})
		}
	}
	return out
}

// ParentAgent はメンバーの親の agent の名前(~set はセットの parent、メンバー名はその agent。空なら親なし)。
func (d Def) ParentAgent(m Member) string { return d.parentName(m.Parent) }

// parentName は定義の親の書き方(メンバー名 / ~set / agent 名)を agent の名前にする。
func (d Def) parentName(p string) string {
	switch {
	case p == "~set":
		return d.Parent
	case p == "":
		return ""
	}
	if pm, ok := d.member(p); ok {
		return pm.agentNames(d.Name)[0]
	}
	return p
}

// External はセットの外の agent(定義に書かれた親と、[adopt] の agent とその親)。起動の前に台帳にいるか確かめる。
func (d Def) External() []string {
	plan := d.Plan()
	own := map[string]bool{}
	for _, in := range plan {
		own[in.Name] = true
	}
	var out []string
	for _, in := range plan {
		if in.Parent != "" && !own[in.Parent] {
			own[in.Parent] = true
			out = append(out, in.Parent)
		}
	}
	for _, a := range d.Adopt {
		for _, n := range []string{a.Agent, d.parentName(a.Parent)} {
			if !own[n] {
				own[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// WorkspaceLabel は workspace の label。
func (d Def) WorkspaceLabel() string {
	if d.Workspace != "" {
		return d.Workspace
	}
	return d.Name
}

var reSchedule = regexp.MustCompile(`^every\s+([0-9]+)(m|h)\s*:\s*(.+)$`)

// parseSchedule は schedule の値「every <N>m|<N>h: <本文>」を分と本文にする。
func parseSchedule(v string) (int, string, error) {
	g := reSchedule.FindStringSubmatch(v)
	if g == nil {
		return 0, "", fmt.Errorf("schedule は「every <N>m: <本文>」か「every <N>h: <本文>」: %s", v)
	}
	n, err := strconv.Atoi(g[1])
	if g[2] == "h" {
		n *= 60
	}
	if err != nil || n < 1 || n > 24*60 {
		return 0, "", fmt.Errorf("schedule の間隔は 1 分から 24 時間まで: %s", v)
	}
	say := strings.TrimSpace(g[3])
	if strings.HasPrefix(say, "/") || strings.HasPrefix(say, "-") {
		return 0, "", fmt.Errorf("schedule の本文は / や - で始められません(スラッシュコマンドと解釈されるため): %s", say)
	}
	if strings.ContainsAny(say, "'\\") {
		return 0, "", fmt.Errorf("schedule の本文に ' と \\ は使えません: %s", say)
	}
	return n, say, nil
}
