package agentset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	have, _, err := d.list(ctx, c, by, nil)
	if err != nil {
		return nil, err
	}
	plan := d.Plan()
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
	have, _, err := d.list(ctx, c, by, nil)
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, in := range d.Plan() {
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
