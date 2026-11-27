package agentset

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
)

const review = `# agentset: review
workspace = rv
cwd = /tmp
parent = conductor

[member lead]
role = reviewer-lead
prompt = 未処理の PR を拾う   # 行末のコメント
parent = ~set

[member rev]
role = reviewer
count = 3
parent = lead
prompt = issue #12 を見る
`

func TestParse(t *testing.T) {
	d, err := Parse("review", review)
	if err != nil {
		t.Fatal(err)
	}
	if d.Workspace != "rv" || d.Parent != "conductor" || len(d.Members) != 2 || d.Members[0].Prompt != "未処理の PR を拾う" || d.Members[1].Prompt != "issue #12 を見る" || d.Members[1].Count != 3 {
		t.Errorf("%+v", d)
	}
	var names, parents []string
	for _, in := range d.Plan() {
		names = append(names, in.Name)
		parents = append(parents, in.Parent)
	}
	if want := []string{"review-lead", "review-rev-1", "review-rev-2", "review-rev-3"}; !slices.Equal(names, want) {
		t.Errorf("順: %q", names)
	}
	if want := []string{"conductor", "review-lead", "review-lead", "review-lead"}; !slices.Equal(parents, want) {
		t.Errorf("親: %q", parents)
	}
	if ext := d.External(); !slices.Equal(ext, []string{"conductor"}) {
		t.Errorf("外の親: %q", ext)
	}
	// 子が先に書かれていても、親が先に立つ
	d, err = Parse("s", "[member b]\nparent = a\n[member a]\n")
	if err != nil || d.Plan()[0].Name != "s-a" || d.Plan()[1].Parent != "s-a" {
		t.Errorf("親が先: %+v %v", d.Plan(), err)
	}
	for body, want := range map[string]string{
		"": "メンバーがいません",
		"[member a]\nparent = b\n[member b]\nparent = a\n": "輪になります",
		"[member a]\nparent = ~set\n":                      "セットの parent が要ります",
		"[member a]\ncount = 2\n[member b]\nparent = a\n":  "count = 1 で max の無いメンバー",
		"[member a]\nmax = 3\n[member b]\nparent = a\n":    "count = 1 で max の無いメンバー",
		"[member a]\ncount = 3\nmax = 2\n":                 "max(2)を超えて",
		"[member a]\nmax = 0\n":                            "max は 1 以上の数か unlimited",
		"[member a]\ncount = 0\n":                          "count は 1 以上",
		"[member a]\nschedule = every 30m\n":               "schedule はまだ使えません",
		"[adopt]\nw -> a\n":                                "[adopt]",
		"[member a]\nfoo = 1\n":                            "メンバーのキーは",
		"foo = 1\n[member a]\n":                            "セットのキーは",
		"[member a]\n[member a]\n":                         "2 回あります",
		"[member Bad]\n":                                   "メンバーの名前は",
		"[member a]\nparent = Bad\n":                       "parent はメンバー名",
		"[member abcdefghijkl]\ncount = 100\n":             "長すぎます",
	} {
		name := "s"
		if want == "長すぎます" {
			name = "abcdefghijklmnop"
		}
		if _, err := Parse(name, body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v(want %q)", body, err, want)
		}
	}
	if _, err := Parse("Bad", "[member a]\n"); err == nil {
		t.Error("セットの名前")
	}
}

// fakeAPI は agent.list / new / revive / stop の呼び出しを記録し、台帳の代わりに状態を持つ。
type fakeAPI struct {
	agents map[string]agent.AgentInfo
	calls  []string
	max    int
	fail   string // この名前の new を失敗させる
	n      int
	tasks  map[string]ticket.GetResult // ticket.get の答え(無ければ not_found)
}

func (f *fakeAPI) Call(_ context.Context, by port.Principal, op string, in, out any) error {
	b, _ := json.Marshal(in)
	f.calls = append(f.calls, op+" "+string(b))
	set := func(v any) { x, _ := json.Marshal(v); _ = json.Unmarshal(x, out) }
	pane := func(p *agent.PanePlacement) string {
		f.n++
		return "wA:p" + string(rune('0'+f.n))
	}
	switch op {
	case "agent.list":
		r := agent.ListResult{Max: f.max}
		names := strings.Split(in.(agent.ListReq).Names, ",")
		if in.(agent.ListReq).Names == "" {
			names = nil
			for n := range f.agents {
				names = append(names, n)
			}
			slices.Sort(names)
		}
		for _, n := range names {
			a, ok := f.agents[n]
			if !ok {
				a = agent.AgentInfo{Name: n}
			}
			if a.State == "running" {
				r.Running++
			}
			r.Agents = append(r.Agents, a)
		}
		set(r)
	case "agent.new":
		q := in.(agent.NewReq)
		if q.Name == f.fail {
			return &define.Error{Code: define.CodeUnavailable, Message: q.Name + " を起動できません"}
		}
		p := pane(q.Pane)
		f.agents[q.Name] = agent.AgentInfo{Name: q.Name, State: "running", Live: true, Pane: p, Parent: q.Parent, Task: q.Task}
		set(agent.NewResult{Name: q.Name, Out: q.Name + " started", Pane: p})
	case "agent.revive":
		q := in.(agent.ReviveReq)
		p := pane(q.Pane)
		a := f.agents[q.Name]
		a.State, a.Live, a.Pane = "running", true, p
		f.agents[q.Name] = a
		set(agent.LifeResult{Name: q.Name, Out: q.Name + " revived", Pane: p})
	case "agent.stop":
		q := in.(agent.StopReq)
		a := f.agents[q.Name]
		a.State, a.Live = "stopped", false
		f.agents[q.Name] = a
		set(agent.StopResult{Name: q.Name, Out: q.Name + " stopped"})
	case "ticket.get":
		t, ok := f.tasks[in.(ticket.GetReq).ID]
		if !ok {
			return &define.Error{Code: define.CodeNotFound, Message: "チケットが見つかりません"}
		}
		set(t)
	default:
		return errors.New("unknown op " + op)
	}
	return nil
}

func ops(f *fakeAPI) []string {
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "agent.list") {
			out = append(out, c)
		}
	}
	return out
}

func TestUpDownStatus(t *testing.T) {
	d, err := Parse("review", review)
	if err != nil {
		t.Fatal(err)
	}
	ctx, by := context.Background(), port.Principal{}
	f := &fakeAPI{agents: map[string]agent.AgentInfo{}, max: -1}
	// 外の親が台帳にいなければ 1 体も立てない
	if _, err := Up(ctx, f, by, d); err == nil || !strings.Contains(err.Error(), "conductor") || len(ops(f)) != 0 {
		t.Fatalf("外の親: %v %q", err, ops(f))
	}
	f.agents["conductor"] = agent.AgentInfo{Name: "conductor", State: "running", Live: true, Pane: "w0:p1"}
	// 上限を超えるなら 1 体も立てない
	f.max = 4
	if _, err := Up(ctx, f, by, d); err == nil || !strings.Contains(err.Error(), "上限") || len(ops(f)) != 0 {
		t.Fatalf("上限: %v %q", err, ops(f))
	}
	f.max = -1
	lines, err := Up(ctx, f, by, d)
	if err != nil || len(lines) != 4 {
		t.Fatalf("up: %v %+v", err, lines)
	}
	got := ops(f)
	for i, want := range []string{
		`"Name":"review-lead","Cwd":"/tmp","Kind":"","Model":"","Role":"reviewer-lead","Prompt":"未処理の PR を拾う","Killable":"","Parent":"conductor","Pane":{"Workspace":"rv","Split":""`,
		`"Name":"review-rev-1","Cwd":"/tmp","Kind":"","Model":"","Role":"reviewer","Prompt":"issue #12 を見る","Killable":"","Parent":"review-lead","Pane":{"Workspace":"","Split":"wA:p1","Direction":"right"}`,
		`"Split":"wA:p1","Direction":"down"`,
		`"Split":"wA:p2","Direction":"down"`,
	} {
		if !strings.Contains(got[i], want) {
			t.Errorf("up %d: %s\nwant %s", i, got[i], want)
		}
	}
	// もう一度 up しても何もしない
	f.calls = nil
	if lines, err := Up(ctx, f, by, d); err != nil || len(ops(f)) != 0 || !strings.Contains(lines[0].Out, "すべて動いています") {
		t.Errorf("2 回目の up: %v %q %+v", err, ops(f), lines)
	}
	// 1 体が herdr から消えていたら、その 1 体だけ戻す(ほかの pane を分けて置く)
	a := f.agents["review-rev-2"]
	a.Live, a.State = false, "stopped"
	f.agents["review-rev-2"] = a
	f.calls = nil
	if _, err := Up(ctx, f, by, d); err != nil || len(ops(f)) != 1 || !strings.HasPrefix(ops(f)[0], `agent.revive {"Name":"review-rev-2"`) {
		t.Errorf("戻す: %v %q", err, ops(f))
	}
	rows, err := Status(ctx, f, by, d)
	if err != nil || len(rows) != 4 || rows[0].State != "running" || rows[0].Parent != "conductor" || rows[1].Want != "review-lead" {
		t.Errorf("status: %v %+v", err, rows)
	}
	// down は子から順。外の親(conductor)は止めない
	f.calls = nil
	if _, err := Down(ctx, f, by, d, false); err != nil {
		t.Fatal(err)
	}
	var stopped []string
	for _, c := range ops(f) {
		var q agent.StopReq
		_ = json.Unmarshal([]byte(strings.TrimPrefix(c, "agent.stop ")), &q)
		stopped = append(stopped, q.Name)
	}
	if want := []string{"review-rev-3", "review-rev-2", "review-rev-1", "review-lead"}; !slices.Equal(stopped, want) {
		t.Errorf("down: %q", stopped)
	}
	if !f.agents["conductor"].Live {
		t.Error("外の親を止めた")
	}
	// 止めたあとの up は、全員を戻す(1 体目は workspace を作り直す)
	f.calls = nil
	if _, err := Up(ctx, f, by, d); err != nil || len(ops(f)) != 4 || !strings.Contains(ops(f)[0], `"Pane":{"Workspace":"rv"`) {
		t.Errorf("down のあとの up: %v %q", err, ops(f))
	}
	// 途中で失敗したらそこで止める
	g := &fakeAPI{agents: map[string]agent.AgentInfo{"conductor": {Name: "conductor", State: "running", Live: true}}, max: -1, fail: "review-rev-1"}
	lines, err = Up(ctx, g, by, d)
	if err == nil || len(lines) != 2 || lines[1].Err == nil || len(ops(g)) != 2 {
		t.Errorf("途中の失敗: %v %+v %q", err, lines, ops(g))
	}
}

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sets")
	if err := Save(dir, "s", "[member a]\nparent = a\n"); err == nil {
		t.Error("読めない定義を書いた")
	}
	if _, err := os.Stat(dir); err == nil {
		if n := Names(dir); len(n) != 0 {
			t.Errorf("読めない定義が残った: %q", n)
		}
	}
	if err := Save(dir, "s", "[member a]\n"); err != nil {
		t.Fatal(err)
	}
	if n := Names(dir); !slices.Equal(n, []string{"s"}) {
		t.Errorf("names: %q", n)
	}
	if d, err := Load(dir, "s"); err != nil || d.Members[0].Name != "a" {
		t.Errorf("load: %+v %v", d, err)
	}
	if _, err := Load(dir, "t"); err == nil || !strings.Contains(err.Error(), "定義がありません") {
		t.Errorf("無い定義: %v", err)
	}
	if err := Remove(dir, "s"); err != nil || len(Names(dir)) != 0 {
		t.Errorf("rm: %v", err)
	}
	if err := Remove(dir, "s"); err == nil {
		t.Error("2 回目の rm")
	}
}

func TestScale(t *testing.T) {
	d, err := Parse("s", "[member lead]\n[member w]\nmax = 4\nparent = lead\nprompt = 働く\n")
	if err != nil {
		t.Fatal(err)
	}
	ctx, by := context.Background(), port.Principal{}
	f := &fakeAPI{agents: map[string]agent.AgentInfo{}, max: -1}
	if _, err := Up(ctx, f, by, d); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.agents["s-w-1"]; !ok || f.agents["s-lead"].Pane == "" {
		t.Fatalf("up: %+v", f.agents)
	}
	for spec, want := range map[string]string{"5": "max(4)を超えます", "-2": "0 を下回る", "x": "N / +N / -N"} {
		if _, err := Scale(ctx, f, by, d, "w", spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", spec, err)
		}
	}
	if _, err := Scale(ctx, f, by, d, "lead", "+1"); err == nil || !strings.Contains(err.Error(), "数を変えられません") {
		t.Errorf("lead: %v", err)
	}
	if _, err := Scale(ctx, f, by, d, "nobody", "+1"); err == nil {
		t.Error("無いメンバー")
	}
	f.calls = nil
	lines, err := Scale(ctx, f, by, d, "w", "+2")
	if err != nil || len(lines) != 2 {
		t.Fatalf("+2: %v %+v", err, lines)
	}
	got := ops(f)
	if !strings.Contains(got[0], `"Name":"s-w-2"`) || !strings.Contains(got[0], `"Killable":"yes","Parent":"s-lead"`) || !strings.Contains(got[1], `"Name":"s-w-3"`) {
		t.Errorf("+2: %q", got)
	}
	// 減らすのは番号の大きい順。止められない(手が空いていない)ものは飛ばす
	f.calls = nil
	if _, err := Scale(ctx, f, by, d, "w", "1"); err != nil {
		t.Fatal(err)
	}
	var stopped []string
	for _, c := range ops(f) {
		var q agent.StopReq
		_ = json.Unmarshal([]byte(strings.TrimPrefix(c, "agent.stop ")), &q)
		stopped = append(stopped, q.Name)
	}
	if !slices.Equal(stopped, []string{"s-w-3", "s-w-2"}) {
		t.Errorf("down: %q", stopped)
	}
	// 止まっている番号から戻す(killable=yes)
	f.calls = nil
	if _, err := Scale(ctx, f, by, d, "w", "+1"); err != nil || len(ops(f)) != 1 || !strings.Contains(ops(f)[0], `agent.revive {"Name":"s-w-2"`) || !strings.Contains(ops(f)[0], `"Killable":"yes"`) {
		t.Errorf("戻す: %v %q", err, ops(f))
	}
	// status と down は scale で足した agent も含む
	rows, _ := Status(ctx, f, by, d)
	if len(rows) != 4 || rows[3].Name != "s-w-3" || rows[3].State != "stopped" {
		t.Errorf("status: %+v", rows)
	}
	// 上限
	f.max = f.running()
	if _, err := Scale(ctx, f, by, d, "w", "+1"); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Errorf("上限: %v", err)
	}
}

func (f *fakeAPI) running() int {
	n := 0
	for _, a := range f.agents {
		if a.State == "running" {
			n++
		}
	}
	return n
}

func TestJob(t *testing.T) {
	d, err := Parse("j", "[member lead]\n[member w]\nmode = job\nmax = 2\nparent = lead\nprompt = 直す\n")
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := d.member("w"); w.Count != 0 || !w.Scalable() || len(d.Plan()) != 1 {
		t.Fatalf("job の既定: %+v %+v", w, d.Plan())
	}
	for body, want := range map[string]string{
		"[member a]\ncount = 0\n":   "count",
		"[member a]\nmode = pool\n": "mode",
	} {
		if _, err := Parse("x", body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", body, err)
		}
	}
	ctx, by := context.Background(), port.Principal{}
	f := &fakeAPI{agents: map[string]agent.AgentInfo{}, max: -1, tasks: map[string]ticket.GetResult{
		"T-p-001": {ID: "T-p-001", Status: "todo"}, "T-p-002": {ID: "T-p-002", Status: "todo"},
		"T-p-003": {ID: "T-p-003", Status: "todo"}, "T-p-009": {ID: "T-p-009", Status: "done", Closed: true},
	}}
	// 親がまだいない
	if _, err := Run(ctx, f, by, d, "w", "T-p-001", ""); err == nil || !strings.Contains(err.Error(), "親") {
		t.Errorf("親なし: %v", err)
	}
	if _, err := Up(ctx, f, by, d); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.agents["j-w-1"]; ok {
		t.Fatal("up は job を立てない")
	}
	f.calls = nil
	lines, err := Run(ctx, f, by, d, "w", "T-p-001", "ログを見る")
	if err != nil || len(lines) != 1 || lines[0].Name != "j-w-1" {
		t.Fatalf("run: %v %+v", err, lines)
	}
	var q agent.NewReq
	_ = json.Unmarshal([]byte(strings.TrimPrefix(ops(f)[1], "agent.new ")), &q)
	if q.Task != "T-p-001" || q.Killable != "yes" || q.Parent != "j-lead" || q.Pane == nil || q.Pane.Split == "" ||
		!strings.Contains(q.Prompt, "直す") || !strings.Contains(q.Prompt, "tb claim T-p-001") || !strings.Contains(q.Prompt, "ログを見る") {
		t.Errorf("new: %+v", q)
	}
	for _, c := range []struct{ member, task, want string }{
		{"lead", "T-p-002", "job 型"},
		{"w", "T-p-001", "もう担当"},
		{"w", "T-p-009", "終わって"},
		{"w", "T-p-404", "見つかりません"},
		{"x", "T-p-002", "いません"},
	} {
		if _, err := Run(ctx, f, by, d, c.member, c.task, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if _, err := Run(ctx, f, by, d, "w", "T-p-002", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, f, by, d, "w", "T-p-003", ""); err == nil || !strings.Contains(err.Error(), "max(2)") {
		t.Errorf("max: %v", err)
	}
	// status は job の agent も出す
	if rows, _ := Status(ctx, f, by, d); len(rows) != 3 {
		t.Errorf("status: %+v", rows)
	}
	// reap: 終わったタスクの agent だけを止める
	f.tasks["T-p-001"] = ticket.GetResult{ID: "T-p-001", Status: "done", Closed: true}
	f.calls = nil
	lines, err = Reap(ctx, f, by, d)
	if err != nil || len(lines) != 1 || lines[0].Name != "j-w-1" || f.agents["j-w-1"].Live || !f.agents["j-w-2"].Live {
		t.Fatalf("reap: %v %+v", err, lines)
	}
	// 消えたタスクも止める。止めた番号は次の run で使わず、新しい番号にする
	delete(f.tasks, "T-p-002")
	if lines, err := Reap(ctx, f, by, d); err != nil || len(lines) != 1 || !strings.Contains(lines[0].Out, "ありません") {
		t.Errorf("reap 消えた: %v %+v", err, lines)
	}
	if lines, err := Run(ctx, f, by, d, "w", "T-p-003", ""); err != nil || lines[0].Name != "j-w-3" {
		t.Errorf("次の番号: %v %+v", err, lines)
	}
	if lines, err := Reap(ctx, f, by, d); err != nil || len(lines) != 0 {
		t.Errorf("何も止めない: %v %+v", err, lines)
	}
}
