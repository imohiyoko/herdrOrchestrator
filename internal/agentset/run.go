package agentset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

// Caller はアプリケーション API の呼び出し(internal/app/server の Client)。
type Caller interface {
	Call(ctx context.Context, by port.Principal, op string, in, out any) error
}

// Line は 1 体ぶんの結果。
type Line struct {
	Name string
	Out  string
	Err  error
}

// Dir は定義の置き場所: TB_AGENTSETS か、DB と同じ所の agentsets/。
func Dir(dbPath string) string {
	if d := os.Getenv("TB_AGENTSETS"); d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(dbPath), "agentsets")
}

// Load は名前の定義を読む。
func Load(dir, name string) (Def, error) {
	if !ReName.MatchString(name) {
		return Def{}, fmt.Errorf("セットの名前は英小文字で始まり、英小文字・数字・_ の 16 字以内: %s", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".conf"))
	if errors.Is(err, os.ErrNotExist) {
		return Def{}, fmt.Errorf("セット %s の定義がありません(tb agentset add %s < 定義のファイル)", name, name)
	}
	if err != nil {
		return Def{}, err
	}
	return Parse(name, string(b))
}

// Names は定義のあるセットの名前(名前の順)。
func Names(dir string) []string {
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		if n, ok := strings.CutSuffix(e.Name(), ".conf"); ok && ReName.MatchString(n) && !e.IsDir() {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Save は定義を確かめてから書く(読めない定義は置かない)。
func Save(dir, name, body string) error {
	if _, err := Parse(name, body); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".conf.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name+".conf"))
}

// Remove は定義を消す(動いているメンバーは止めない)。
func Remove(dir, name string) error {
	if !ReName.MatchString(name) {
		return fmt.Errorf("セットの名前は英小文字で始まり、英小文字・数字・_ の 16 字以内: %s", name)
	}
	err := os.Remove(filepath.Join(dir, name+".conf"))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("セット %s の定義がありません", name)
	}
	return err
}

func (d Def) list(ctx context.Context, c Caller, by port.Principal, extra []string) (map[string]agent.AgentInfo, agent.ListResult, error) {
	var names []string
	for _, in := range d.Plan() {
		names = append(names, in.Name)
	}
	names = append(names, extra...)
	var res agent.ListResult
	if err := c.Call(ctx, by, "agent.list", agent.ListReq{Names: strings.Join(names, ",")}, &res); err != nil {
		return nil, res, err
	}
	m := map[string]agent.AgentInfo{}
	for _, a := range res.Agents {
		m[a.Name] = a
	}
	return m, res, nil
}

// all は台帳の全員(scale で足したメンバーを見つけるため)。
func all(ctx context.Context, c Caller, by port.Principal) (map[string]agent.AgentInfo, agent.ListResult, error) {
	var res agent.ListResult
	if err := c.Call(ctx, by, "agent.list", agent.ListReq{}, &res); err != nil {
		return nil, res, err
	}
	m := map[string]agent.AgentInfo{}
	for _, a := range res.Agents {
		m[a.Name] = a
	}
	return m, res, nil
}

// numOf は数を変えられるメンバーの agent の番号(<セット>-<メンバー>-<N> でなければ 0)。
func (d Def) numOf(m Member, name string) int {
	rest, ok := strings.CutPrefix(name, d.Name+"-"+m.Name+"-")
	if !ok || rest == "" || strings.TrimLeft(rest, "0123456789") != "" || rest[0] == '0' {
		return 0
	}
	n, _ := strconv.Atoi(rest)
	return n
}

// Known は起動の順の 1 体ずつに、台帳にいる scale で足した agent(番号の順)をメンバーごとに足したもの。
func (d Def) Known(have map[string]agent.AgentInfo) []Instance {
	plan := d.Plan()
	seen := map[string]bool{}
	for _, in := range plan {
		seen[in.Name] = true
	}
	var out []Instance
	for i, in := range plan {
		out = append(out, in)
		if !in.Member.Scalable() || (i+1 < len(plan) && plan[i+1].Member.Name == in.Member.Name) {
			continue
		}
		var extra []int
		for n := range have {
			if k := d.numOf(in.Member, n); k > 0 && !seen[n] {
				extra = append(extra, k)
			}
		}
		sort.Ints(extra)
		for _, k := range extra {
			out = append(out, Instance{Name: in.Member.instName(d.Name, k), Member: in.Member, Parent: in.Parent})
		}
	}
	return out
}

func (m Member) instName(set string, n int) string { return set + "-" + m.Name + "-" + strconv.Itoa(n) }

// split は herdr-up.sh と同じ並べ方: 2 体目は 1 体目の右、3 体目は 1 体目の下、4 体目からは 2 体目(右の列)を下へ。
func split(panes []string) *agent.PanePlacement {
	switch len(panes) {
	case 1:
		return &agent.PanePlacement{Split: panes[0], Direction: "right"}
	case 2:
		return &agent.PanePlacement{Split: panes[0], Direction: "down"}
	}
	return &agent.PanePlacement{Split: panes[1], Direction: "down"}
}

// Up はセットを立ち上げる: 親から順に、1 体目は workspace を作り、残りはその pane を分けて置く。
// 動いているメンバーはそのまま、止まっているメンバーは戻す(revive)。何度打っても同じ状態になる。
// 外の親が台帳にいない・上限を超える、なら 1 体も立てずに断る。途中で失敗したら、そこで止めて結果を返す。
func Up(ctx context.Context, c Caller, by port.Principal, d Def) ([]Line, error) {
	ext := d.External()
	have, lr, err := d.list(ctx, c, by, ext)
	if err != nil {
		return nil, err
	}
	for _, p := range ext {
		if have[p].State == "" {
			return nil, &define.Error{Code: define.CodeFailedPrecondition, Message: "親 " + p + " が台帳にいません(先に立てるか、定義の parent を直す)"}
		}
	}
	plan := d.Plan()
	var panes []string
	need := 0
	for _, in := range plan {
		if a := have[in.Name]; a.Live {
			if a.Pane != "" {
				panes = append(panes, a.Pane)
			}
		} else {
			need++
		}
	}
	if need == 0 {
		return []Line{{Name: d.Name, Out: "すべて動いています(何もしません)"}}, nil
	}
	if lr.Max >= 0 && lr.Running+need > lr.Max {
		return nil, &define.Error{Code: define.CodeFailedPrecondition, Reasons: []string{agent.ReasonLimitExceeded},
			Message: fmt.Sprintf("セット %s を立てると動作中の agent が上限を超えます(動作中 %d + 立てる %d、上限 max_running_agents=%d)。1 体も立てません", d.Name, lr.Running, need, lr.Max)}
	}
	var out []Line
	for _, in := range plan {
		a := have[in.Name]
		if a.Live {
			out = append(out, Line{Name: in.Name, Out: "動いています(そのまま)"})
			continue
		}
		place := &agent.PanePlacement{Workspace: d.WorkspaceLabel()}
		if len(panes) > 0 {
			place = split(panes)
		}
		var pane, msg string
		if a.State == "stopped" {
			var r agent.LifeResult
			err = c.Call(ctx, by, "agent.revive", agent.ReviveReq{Name: in.Name, Pane: place}, &r)
			pane, msg = r.Pane, r.Out
		} else {
			cwd := in.Member.Cwd
			if cwd == "" {
				cwd = d.Cwd
			}
			var r agent.NewResult
			err = c.Call(ctx, by, "agent.new", agent.NewReq{Name: in.Name, Cwd: agent.NormalizeCwd(cwd), Kind: in.Member.Kind, Model: in.Member.Model,
				Role: in.Member.Role, Prompt: in.Member.Prompt, Parent: in.Parent, Pane: place}, &r)
			pane, msg = r.Pane, r.Out
		}
		if err != nil {
			out = append(out, Line{Name: in.Name, Err: err})
			return out, err
		}
		out = append(out, Line{Name: in.Name, Out: msg})
		if pane != "" {
			panes = append(panes, pane)
		}
	}
	return out, nil
}

// Down はセットのメンバーを子から順に止める(定義の外の agent は止めない)。失敗しても残りは止める。
func Down(ctx context.Context, c Caller, by port.Principal, d Def, force bool) ([]Line, error) {
	have, _, err := all(ctx, c, by)
	if err != nil {
		return nil, err
	}
	plan := d.Known(have)
	var out []Line
	var first error
	for i := len(plan) - 1; i >= 0; i-- {
		in := plan[i]
		if a := have[in.Name]; !a.Live && a.State != "running" {
			continue
		}
		var r agent.StopResult
		if err := c.Call(ctx, by, "agent.stop", agent.StopReq{Name: in.Name, Force: force}, &r); err != nil {
			out = append(out, Line{Name: in.Name, Err: err})
			if first == nil {
				first = err
			}
			continue
		}
		if r.Pending != nil {
			out = append(out, Line{Name: in.Name, Out: r.Pending.Message})
			continue
		}
		out = append(out, Line{Name: in.Name, Out: r.Out})
	}
	if len(out) == 0 {
		out = append(out, Line{Name: d.Name, Out: "動いているメンバーはいません"})
	}
	return out, first
}

// Row は status の 1 行。
type Row struct {
	Name   string `json:"name"`
	Member string `json:"member"`
	State  string `json:"state"` // running / stopped / absent(まだ立てていない)/ lost(台帳では動作中だが herdr にいない)
	Pane   string `json:"pane"`
	Parent string `json:"parent"`
	Want   string `json:"want_parent"` // 定義の親(Parent と違えば定義との差)
}

// Status はメンバーごとの状態と、定義の親との差。
func Status(ctx context.Context, c Caller, by port.Principal, d Def) ([]Row, error) {
	have, _, err := all(ctx, c, by)
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, in := range d.Known(have) {
		a := have[in.Name]
		st := a.State
		switch {
		case a.Live:
			st = "running"
		case st == "":
			st = "absent"
		case st == "running":
			st = "lost"
		}
		out = append(out, Row{Name: in.Name, Member: in.Member.Name, State: st, Pane: a.Pane, Parent: a.Parent, Want: in.Parent})
	}
	return out, nil
}

// panesOf はセットの動いている agent の pane(起動の順。新しい agent を置くときに分ける元)。
func panesOf(known []Instance, have map[string]agent.AgentInfo) []string {
	var out []string
	for _, in := range known {
		if a := have[in.Name]; a.Live && a.Pane != "" {
			out = append(out, a.Pane)
		}
	}
	return out
}

// Scale は数を変えられるメンバーの動いている数を n にする(spec は N / +N / -N)。
// 増やすときは止まっている agent を番号の小さい順に戻し、足りなければ新しい番号で立てる(同じ workspace に pane を足す。
// 止めてよい = killable=yes で立てる)。減らすときは番号の大きい順に、止めてよい・未完了のタスクが無いものだけを止める。
func Scale(ctx context.Context, c Caller, by port.Principal, d Def, member, spec string) ([]Line, error) {
	m, ok := d.member(member)
	if !ok {
		return nil, &define.Error{Code: define.CodeInvalidArgument, Message: "セット " + d.Name + " にメンバー " + member + " はいません"}
	}
	if !m.Scalable() {
		return nil, &define.Error{Code: define.CodeInvalidArgument, Message: "メンバー " + member + " は数を変えられません(定義に max を書く)"}
	}
	have, lr, err := all(ctx, c, by)
	if err != nil {
		return nil, err
	}
	known := d.Known(have)
	var live, stopped []Instance
	used := map[int]bool{}
	parent := ""
	for _, in := range known {
		if in.Member.Name != m.Name {
			continue
		}
		parent = in.Parent
		a := have[in.Name]
		if a.State != "" || a.Live {
			used[d.numOf(m, in.Name)] = true
		}
		switch {
		case a.Live:
			live = append(live, in)
		case a.State == "stopped":
			stopped = append(stopped, in)
		}
	}
	if len(known) > 0 && parent == "" {
		for _, in := range d.Plan() {
			if in.Member.Name == m.Name {
				parent = in.Parent
				break
			}
		}
	}
	cur, target := len(live), 0
	if n, err := strconv.Atoi(strings.TrimLeft(spec, "+")); err == nil && spec != "" {
		switch spec[0] {
		case '+':
			target = cur + n
		case '-':
			target = cur + n // n は負
		default:
			target = n
		}
	} else {
		return nil, &define.Error{Code: define.CodeInvalidArgument, Message: "数は N / +N / -N: " + spec}
	}
	if target < 0 {
		return nil, &define.Error{Code: define.CodeInvalidArgument, Message: fmt.Sprintf("動いている %s は %d 体です(%s で 0 を下回る)", member, cur, spec)}
	}
	if l := m.Limit(); l >= 0 && target > l {
		return nil, &define.Error{Code: define.CodeFailedPrecondition, Reasons: []string{agent.ReasonLimitExceeded},
			Message: fmt.Sprintf("メンバー %s の max(%d)を超えます(%d 体にしようとしました)", member, l, target)}
	}
	if target == cur {
		return []Line{{Name: d.Name + "-" + member, Out: fmt.Sprintf("%d 体のまま(何もしません)", cur)}}, nil
	}
	var out []Line
	if target < cur {
		need := cur - target
		for i := len(live) - 1; i >= 0 && need > 0; i-- {
			var r agent.StopResult
			if err := c.Call(ctx, by, "agent.stop", agent.StopReq{Name: live[i].Name}, &r); err != nil {
				out = append(out, Line{Name: live[i].Name, Out: "そのまま(" + errText(err) + ")"})
				continue
			}
			out = append(out, Line{Name: live[i].Name, Out: r.Out})
			need--
		}
		if need > 0 {
			err := &define.Error{Code: define.CodeFailedPrecondition, Message: fmt.Sprintf("%s を %d 体まで減らせませんでした(手の空いた agent が足りません。止めるなら tb agent stop --force)", member, target)}
			return append(out, Line{Name: d.Name + "-" + member, Err: err}), err
		}
		return out, nil
	}
	need := target - cur
	if lr.Max >= 0 && lr.Running+need > lr.Max {
		return nil, &define.Error{Code: define.CodeFailedPrecondition, Reasons: []string{agent.ReasonLimitExceeded},
			Message: fmt.Sprintf("動作中の agent が上限を超えます(動作中 %d + 足す %d、上限 max_running_agents=%d)。1 体も足しません", lr.Running, need, lr.Max)}
	}
	if parent != "" {
		if p, ok := have[parent]; !ok || p.State == "" {
			return nil, &define.Error{Code: define.CodeFailedPrecondition, Message: "親 " + parent + " が台帳にいません(先に tb agentset up " + d.Name + ")"}
		}
	}
	panes := panesOf(known, have)
	next := 1
	for i := 0; i < need; i++ {
		place := &agent.PanePlacement{Workspace: d.WorkspaceLabel()}
		if len(panes) > 0 {
			place = split(panes)
		}
		var pane, msg, name string
		if len(stopped) > 0 {
			name, stopped = stopped[0].Name, stopped[1:]
			var r agent.LifeResult
			err = c.Call(ctx, by, "agent.revive", agent.ReviveReq{Name: name, Pane: place, Killable: "yes"}, &r)
			pane, msg = r.Pane, r.Out
		} else {
			for used[next] {
				next++
			}
			used[next] = true
			name = m.instName(d.Name, next)
			cwd := m.Cwd
			if cwd == "" {
				cwd = d.Cwd
			}
			var r agent.NewResult
			err = c.Call(ctx, by, "agent.new", agent.NewReq{Name: name, Cwd: agent.NormalizeCwd(cwd), Kind: m.Kind, Model: m.Model,
				Role: m.Role, Prompt: m.Prompt, Killable: "yes", Parent: parent, Pane: place}, &r)
			pane, msg = r.Pane, r.Out
		}
		if err != nil {
			return append(out, Line{Name: name, Err: err}), err
		}
		out = append(out, Line{Name: name, Out: msg})
		if pane != "" {
			panes = append(panes, pane)
		}
	}
	return out, nil
}

// errText は API のエラーの文言。
func errText(err error) string {
	var e *define.Error
	if errors.As(err, &e) && e.Message != "" {
		return e.Message
	}
	return err.Error()
}
