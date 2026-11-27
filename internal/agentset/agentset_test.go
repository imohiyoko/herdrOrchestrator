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
	"time"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/app/job"
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
		"[member a]\nparent = b\n[member b]\nparent = a\n":      "輪になります",
		"[member a]\nparent = ~set\n":                           "セットの parent が要ります",
		"[member a]\ncount = 2\n[member b]\nparent = a\n":       "count = 1 で max の無いメンバー",
		"[member a]\nmax = 3\n[member b]\nparent = a\n":         "count = 1 で max の無いメンバー",
		"[member a]\ncount = 3\nmax = 2\n":                      "max(2)を超えて",
		"[member a]\nmax = 0\n":                                 "max は 1 以上の数か unlimited",
		"[member a]\ncount = 0\n":                               "count は 1 以上",
		"[member a]\nschedule = every 30m\n":                    "schedule は「every",
		"[member a]\nschedule = every 0m: x\n":                  "1 分から 24 時間",
		"[member a]\nschedule = every 25h: x\n":                 "1 分から 24 時間",
		"[member a]\nschedule = every 307445734561825861h: x\n": "1 分から 24 時間",
		"[member a]\nschedule = every 1441m: x\n":               "1 分から 24 時間",
		"[member a]\nschedule = every 5m: /compact\n":           "/ や - で始められません",
		"[member a]\nschedule = every 5m: it's\n":               "' と",
		"[member a]\ncount = 2\nschedule = every 5m: x\n":       "count = 1 で max の無い",
		"[member a]\n[adopt]\nw -> Bad\n":                       "[adopt] の w の親",
		"[member a]\nfoo = 1\n":                                 "メンバーのキーは",
		"foo = 1\n[member a]\n":                                 "セットのキーは",
		"[member a]\n[member a]\n":                              "2 回あります",
		"[member Bad]\n":                                        "メンバーの名前は",
		"[member a]\nparent = Bad\n":                            "parent はメンバー名",
		"[member abcdefghijkl]\ncount = 100\n":                  "長すぎます",
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
	jobs   map[string]string           // job.set / job.rm
	queue  int                         // ticket.count の答え
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
		a := agent.AgentInfo{Name: q.Name, State: "running", Live: true, Pane: p, Parent: q.Parent, Task: q.Task}
		if q.Parent != "" {
			a.Parents = []string{q.Parent}
		}
		f.agents[q.Name] = a
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
	case "agent.parent.add", "agent.parent.rm":
		q := in.(agent.ParentReq)
		a, ok := f.agents[q.Name]
		if !ok {
			return &define.Error{Code: define.CodeNotFound, Message: "agent が台帳にいません: " + q.Name}
		}
		if op == "agent.parent.add" {
			if !slices.Contains(a.Parents, q.Parent) {
				a.Parents = append(a.Parents, q.Parent)
				slices.Sort(a.Parents)
			}
		} else {
			a.Parents = slices.DeleteFunc(a.Parents, func(p string) bool { return p == q.Parent })
		}
		f.agents[q.Name] = a
		set(agent.ParentResult{Name: q.Name, Parents: a.Parents, Changed: true})
	case "job.set":
		q := in.(job.SetReq)
		if f.jobs == nil {
			f.jobs = map[string]string{}
		}
		changed := f.jobs[q.Name] != q.Body
		f.jobs[q.Name] = q.Body
		set(job.Result{Name: q.Name, Changed: changed})
	case "job.rm":
		q := in.(job.RmReq)
		_, ok := f.jobs[q.Name]
		delete(f.jobs, q.Name)
		set(job.Result{Name: q.Name, Changed: ok})
	case "ticket.count":
		set(ticket.CountResult{Count: f.queue})
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
		if !strings.HasPrefix(c, "agent.list") && !strings.HasPrefix(c, "job.") {
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

func TestAdoptParentExit(t *testing.T) {
	if _, err := Parse("a", "[member l]\n[adopt]\na-x -> l\n"); err == nil || !strings.Contains(err.Error(), "メンバーの名前の形") {
		t.Errorf("メンバーの名前の adopt: %v", err)
	}
	for body, want := range map[string]string{
		"[member l]\n[adopt]\nw\n":                  "<agent> -> <親>",
		"[member l]\n[adopt]\nw -> ~set\n":          "セットの parent",
		"[member l]\n[adopt]\nw -> l\nw -> l\n":     "2 回",
		"on_parent_exit = later\n[member l]\n":      "on_parent_exit",
		"on_parent_exit = set-parent\n[member l]\n": "セットの parent",
		"[member l]\n[bogus]\n":                     "[adopt]",
	} {
		if _, err := Parse("a", body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v(want %q)", body, err, want)
		}
	}
	d, err := Parse("a", "parent = top\non_parent_exit = grandparent\n[member l]\nparent = boss\n[member w]\nparent = l\n[adopt]\nold -> l\n")
	if err != nil {
		t.Fatal(err)
	}
	if ext := d.External(); !slices.Equal(ext, []string{"boss", "old"}) {
		t.Errorf("external: %q", ext)
	}
	ctx, by := context.Background(), port.Principal{}
	f := &fakeAPI{agents: map[string]agent.AgentInfo{
		"top":  {Name: "top", State: "running", Live: true},
		"boss": {Name: "boss", State: "running", Live: true, Parents: []string{"top"}},
		"old":  {Name: "old", State: "running", Live: true},
	}, max: -1}
	lines, err := Up(ctx, f, by, d)
	if err != nil || !slices.Equal(f.agents["old"].Parents, []string{"a-l"}) {
		t.Fatalf("up adopt: %v %+v %+v", err, lines, f.agents["old"])
	}
	// もう一度 up しても親は足さない
	f.calls = nil
	if _, err := Up(ctx, f, by, d); err != nil || slices.ContainsFunc(ops(f), func(c string) bool { return strings.HasPrefix(c, "agent.parent") }) {
		t.Errorf("up again: %v %q", err, ops(f))
	}
	rows, _ := Status(ctx, f, by, d)
	if len(rows) != 3 || rows[2].Name != "old" || rows[2].Member != "~adopt" || rows[2].Parent != "a-l" || rows[2].Diff {
		t.Errorf("status: %+v", rows)
	}
	// keep 以外: 親が全員止まったら、止まった親の親(top)を足す
	boss := f.agents["boss"]
	boss.Live, boss.State = false, "stopped"
	f.agents["boss"] = boss
	lines, err = Reap(ctx, f, by, d)
	if err != nil || len(lines) != 1 || lines[0].Name != "a-l" || !slices.Equal(f.agents["a-l"].Parents, []string{"boss", "top"}) {
		t.Fatalf("grandparent: %v %+v %+v", err, lines, f.agents["a-l"])
	}
	if lines, _ := Reap(ctx, f, by, d); len(lines) != 0 {
		t.Errorf("2 回目は何もしない: %+v", lines)
	}
	// stop
	d.OnParentExit = "stop"
	l := f.agents["a-l"]
	l.Live, l.State = false, "stopped"
	f.agents["a-l"] = l
	lines, _ = Reap(ctx, f, by, d)
	if len(lines) != 2 || f.agents["a-w"].Live || f.agents["old"].Live {
		t.Errorf("stop: %+v", lines)
	}
	// down は adopt の親を外し、adopt の agent は止めない
	o := f.agents["old"]
	o.Live, o.State = true, "running"
	f.agents["old"] = o
	f.calls = nil
	if _, err := Down(ctx, f, by, d, false); err != nil || len(f.agents["old"].Parents) != 0 || !f.agents["old"].Live {
		t.Errorf("down: %v %+v %q", err, f.agents["old"], ops(f))
	}
	// keep(既定)は何もしない
	d.OnParentExit = ""
	if lines, _ := d.parentExit(ctx, f, by, f.agents); lines != nil {
		t.Errorf("keep: %+v", lines)
	}
}

func TestSchedule(t *testing.T) {
	d, err := Parse("s", "[member lead]\nschedule = every 2h: 未処理を確かめる\n[member w]\nparent = lead\n")
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := d.member("lead"); m.Every != 120 || m.Say != "未処理を確かめる" || d.JobName(m) != "agentset-s-lead" ||
		!strings.Contains(d.JobBody(m), "TARGET=s-lead\nPROMPT='未処理を確かめる'\n") || !strings.Contains(d.JobBody(m), "INTERVAL_MIN=120") {
		t.Fatalf("parse: %+v %q", m, d.JobBody(m))
	}
	ctx, by := context.Background(), port.Principal{}
	f := &fakeAPI{agents: map[string]agent.AgentInfo{}, max: -1}
	if _, err := Up(ctx, f, by, d); err != nil || len(f.jobs) != 1 || f.jobs["agentset-s-lead"] == "" {
		t.Fatalf("up: %v %+v", err, f.jobs)
	}
	lines, err := Up(ctx, f, by, d)
	if err != nil || len(lines) != 3 || !strings.Contains(lines[2].Out, "同じ") {
		t.Errorf("up again: %v %+v", err, lines)
	}
	if _, err := Down(ctx, f, by, d, true); err != nil || len(f.jobs) != 0 {
		t.Errorf("down: %v %+v", err, f.jobs)
	}
}

const scaled = `
[member w]
count = 1
max = 3
queue = project:web label:bug
scale_out = queue > 2
scale_out = blocked >= 1 and idle == 0
scale_in = queue == 0 and idle >= 1
`

func TestAutoscaleParse(t *testing.T) {
	d, err := Parse("as", scaled)
	if err != nil {
		t.Fatal(err)
	}
	m := d.Members[0]
	if len(m.Out) != 2 || len(m.In) != 1 || m.Queue != (Queue{Project: "web", Label: "bug"}) || len(m.In[0].Terms) != 2 {
		t.Fatalf("%+v", m)
	}
	for _, bad := range []string{
		"[member w]\nmax = 3\nscale_out = queue >\n",
		"[member w]\nmax = 3\nscale_out = cpu > 1\n",
		"[member w]\nmax = 3\nscale_out = queue => 1\n",
		"[member w]\nmax = 3\nscale_out = queue > -1\n",
		"[member w]\nscale_out = queue > 1\n",             // max が要る
		"[member w]\nmode = job\nscale_out = queue > 1\n", // job 型は書けない
		"[member w]\nmax = 3\nqueue = project:web\n",      // 条件なしの queue
		"[member w]\nmax = 3\nscale_out = queue > 1\nqueue = foo\n",
		"[member w]\nmax = 3\nscale_out = queue > 1\nscale_step = 0\n",
	} {
		if _, err := Parse("as", bad); err == nil {
			t.Errorf("断るはず: %q", bad)
		}
	}
}

func TestDecide(t *testing.T) {
	d, _ := Parse("as", scaled)
	m := d.Members[0]
	for _, c := range []struct {
		v    map[string]int
		want int
	}{
		{map[string]int{"queue": 3, "live": 1}, 1},
		{map[string]int{"queue": 3, "live": 3}, 0},               // max
		{map[string]int{"queue": 0, "live": 2, "idle": 1}, -1},   // scale_in
		{map[string]int{"queue": 0, "live": 1, "idle": 1}, 0},    // count が下限
		{map[string]int{"queue": 1, "live": 2, "idle": 0}, 0},    // どれも当たらない
		{map[string]int{"queue": 0, "live": 2, "blocked": 1}, 1}, // 2 行目の scale_out
	} {
		if n, why := Decide(m, c.v, nil, time.Time{}); n != c.want {
			t.Errorf("%v: %d %s (want %d)", c.v, n, why, c.want)
		}
	}
	m.Step = 5
	if n, _ := Decide(m, map[string]int{"queue": 9, "live": 1}, nil, time.Time{}); n != 2 {
		t.Errorf("step は max で切る: %d", n)
	}
	m.In = append(m.In, Cond{Text: "queue > 2", Terms: []Term{{"queue", ">", 2}}})
	if n, why := Decide(m, map[string]int{"queue": 3, "live": 1}, nil, time.Time{}); n != 0 || !strings.Contains(why, "両方") {
		t.Errorf("両方: %d %s", n, why)
	}
}

func TestAutoscale(t *testing.T) {
	d, _ := Parse("as", scaled)
	ctx, by := context.Background(), port.Principal{}
	f := &fakeAPI{agents: map[string]agent.AgentInfo{
		"as-w-1": {Name: "as-w-1", State: "running", Live: true, Pane: "w0:p1", Status: "working"},
	}, max: -1, queue: 5}
	lines, err := Autoscale(ctx, f, by, d, true, nil, time.Time{})
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0].Out, "queue=5") || !strings.Contains(lines[0].Out, "--dry-run") || len(ops(f)) != 1 {
		t.Fatalf("dry: %v %+v %q", err, lines, ops(f))
	}
	if !strings.Contains(ops(f)[0], `ticket.count {"Project":"web","Label":"bug","Category":"","Unowned":true}`) {
		t.Errorf("count: %q", ops(f))
	}
	f.calls = nil
	lines, err = Autoscale(ctx, f, by, d, false, nil, time.Time{})
	if err != nil || !f.agents["as-w-2"].Live {
		t.Fatalf("out: %v %+v", err, lines)
	}
	// 両方 idle、queue 0 → 1 体減らす(番号の大きいほう)
	for _, n := range []string{"as-w-1", "as-w-2"} {
		a := f.agents[n]
		a.Status = "idle"
		f.agents[n] = a
	}
	f.queue = 0
	if lines, err = Autoscale(ctx, f, by, d, false, nil, time.Time{}); err != nil || f.agents["as-w-2"].Live || !f.agents["as-w-1"].Live {
		t.Fatalf("in: %v %+v", err, lines)
	}
	if _, err := Autoscale(ctx, f, by, Def{Name: "x", Members: []Member{{Name: "a", Count: 1}}}, false, nil, time.Time{}); err == nil {
		t.Error("条件の無いセットは断る")
	}
}

func TestAutoscaleForCooldown(t *testing.T) {
	d, err := Parse("as", "[member w]\ncount = 1\nmax = 5\nscale_out = queue > 2 for 10m\nscale_in = queue == 0 for 1h\nscale_cooldown = 15m\n")
	if err != nil {
		t.Fatal(err)
	}
	m := d.Members[0]
	if m.Out[0].For != 10 || m.In[0].For != 60 || m.Cooldown != 15 || m.Out[0].Terms[0].N != 2 {
		t.Fatalf("%+v", m)
	}
	t0 := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	st := &MemberState{}
	hi := map[string]int{"queue": 5, "live": 1}
	if n, why := Decide(m, hi, st, t0); n != 0 || !strings.Contains(why, "あと 10 分") {
		t.Fatalf("当たり始め: %d %s", n, why)
	}
	if n, why := Decide(m, hi, st, t0.Add(9*time.Minute)); n != 0 || !strings.Contains(why, "あと 1 分") {
		t.Fatalf("9 分: %d %s", n, why)
	}
	if n, _ := Decide(m, hi, st, t0.Add(10*time.Minute)); n != 1 {
		t.Fatalf("10 分続いたら足す: %d", n)
	}
	// 一度外れたら数え直す
	Decide(m, map[string]int{"queue": 1, "live": 1}, st, t0.Add(11*time.Minute))
	if n, _ := Decide(m, hi, st, t0.Add(12*time.Minute)); n != 0 {
		t.Fatalf("外れたあとはまた 10 分: %d", n)
	}
	// クールダウン: 前に変えてから 15 分は変えない
	st = &MemberState{Last: t0.Format(time.RFC3339), Since: map[string]string{"scale_out:queue > 2 for 10m": t0.Add(-time.Hour).Format(time.RFC3339)}}
	if n, why := Decide(m, hi, st, t0.Add(5*time.Minute)); n != 0 || !strings.Contains(why, "あと 10 分") {
		t.Fatalf("cooldown: %d %s", n, why)
	}
	if n, _ := Decide(m, hi, st, t0.Add(15*time.Minute)); n != 1 {
		t.Fatalf("cooldown 後: %d", n)
	}
	// 状態が無ければ for のある条件は当たらない
	if n, _ := Decide(m, hi, nil, t0); n != 0 {
		t.Fatalf("状態なし: %d", n)
	}
	for _, bad := range []string{"scale_out = queue > 1 for 0m", "scale_out = queue > 1 for 25h", "scale_out = queue > 1 for 5s", "scale_cooldown = 2d\nscale_out = queue > 1"} {
		if _, err := Parse("as", "[member w]\nmax = 3\n"+bad+"\n"); err == nil {
			t.Errorf("断るはず: %q", bad)
		}
	}
}

func TestAutoscaleState(t *testing.T) {
	dir := t.TempDir()
	d, _ := Parse("as", "[member w]\ncount = 1\nmax = 3\nscale_out = queue > 0 for 5m\n")
	f := &fakeAPI{agents: map[string]agent.AgentInfo{"as-w-1": {Name: "as-w-1", State: "running", Live: true, Pane: "w0:p1"}}, max: -1, queue: 3}
	ctx, by := context.Background(), port.Principal{}
	t0 := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	st := LoadState(dir, "as")
	if _, err := Autoscale(ctx, f, by, d, false, st, t0); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(dir, "as", st); err != nil {
		t.Fatal(err)
	}
	st = LoadState(dir, "as")
	if lines, err := Autoscale(ctx, f, by, d, false, st, t0.Add(5*time.Minute)); err != nil || !f.agents["as-w-2"].Live {
		t.Fatalf("5 分後に足す: %v %+v", err, lines)
	}
	if st.Members["w"].Last == "" || len(st.Members["w"].Since) != 0 {
		t.Errorf("変えたら Last を書き、Since を消す: %+v", st.Members["w"])
	}
	os.WriteFile(filepath.Join(dir, "as.conf"), []byte("[member w]\nmax = 3\nscale_out = queue > 0\n"), 0o644)
	if err := Remove(dir, "as"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath(dir, "as")); !os.IsNotExist(err) {
		t.Errorf("rm で状態も消す: %v", err)
	}
}

func TestJobs(t *testing.T) {
	d, err := Parse("js", "autoscale_every = 2m\n[member lead]\nschedule = every 30m: 見る\n[member w]\ncount = 1\nmax = 4\nscale_out = queue > 2\nscale_at = weekdays 09:00: 3\nscale_at = at 2026-12-01 18:00: 0\nscale_at = mon,fri 07:30: 2\n")
	if err != nil {
		t.Fatal(err)
	}
	d.TB = "/opt/herdr/taskboard/bin/tb"
	var names []string
	bodies := map[string]string{}
	for _, j := range d.Jobs() {
		names = append(names, j.Name)
		bodies[j.Name] = j.Body
	}
	if strings.Join(names, ",") != "agentset-js-lead,agentset-js-autoscale,agentset-js-w-at1,agentset-js-w-at2,agentset-js-w-at3" {
		t.Fatalf("names: %v", names)
	}
	for n, want := range map[string]string{
		"agentset-js-autoscale": "COMMAND='\"/opt/herdr/taskboard/bin/tb\" agentset autoscale js'\nCOMMAND_TIMEOUT=600\nWINDOW=00:00-23:59\nINTERVAL_MIN=2\n",
		"agentset-js-w-at1":     "COMMAND='\"/opt/herdr/taskboard/bin/tb\" agentset scale js w 3'\nCOMMAND_TIMEOUT=600\nWINDOW=09:00-09:00\nINTERVAL_MIN=1440\nDAYS=\"weekdays\"\n",
		"agentset-js-w-at2":     "AT=\"2026-12-01 18:00\"\n",
		"agentset-js-w-at3":     "DAYS=\"mon fri\"\n",
	} {
		if !strings.Contains(bodies[n], want) {
			t.Errorf("%s:\n%s\nwant %q", n, bodies[n], want)
		}
	}
	d.TB = "/x/it's/tb"
	if b := d.Jobs()[1].Body; !strings.Contains(b, "COMMAND='tb agentset autoscale js'") {
		t.Errorf("引用できない場所は PATH の tb: %s", b)
	}
	if d2, _ := Parse("js", "autoscale_every = off\n[member w]\nmax = 2\nscale_out = queue > 1\n"); len(d2.Jobs()) != 0 {
		t.Errorf("off は書かない: %+v", d2.Jobs())
	}
	if d3, _ := Parse("js", "[member w]\nmax = 2\nscale_out = queue > 1\n"); len(d3.Jobs()) != 1 || !strings.Contains(d3.Jobs()[0].Body, "INTERVAL_MIN=5") {
		t.Errorf("既定は 5 分: %+v", d3.Jobs())
	}
	for _, bad := range []string{
		"[member w]\nmax = 3\nscale_at = 09:00: 1\n",
		"[member w]\nmax = 3\nscale_at = weekdays 25:00: 1\n",
		"[member w]\nmax = 3\nscale_at = funday 09:00: 1\n",
		"[member w]\nmax = 3\nscale_at = at 2026-02-30 09:00: 1\n",
		"[member w]\nmax = 3\nscale_at = weekdays 09:00: 4\n", // max を超える
		"[member w]\nscale_at = weekdays 09:00: 1\n",           // 数を変えられない
		"[member w]\nmax = 3\nscale_at = weekdays 09:00\n",
		"autoscale_every = 0m\n[member w]\nmax = 2\n",
		"[member autoscale]\nmax = 2\n",
	} {
		if _, err := Parse("js", bad); err == nil {
			t.Errorf("断るはず: %q", bad)
		}
	}
}

func TestUpDownJobs(t *testing.T) {
	d, _ := Parse("uj", "[member w]\ncount = 1\nmax = 3\nscale_out = queue > 2\nscale_at = daily 09:00: 2\n")
	ctx, by := context.Background(), port.Principal{}
	f := &fakeAPI{agents: map[string]agent.AgentInfo{}, max: -1, jobs: map[string]string{"agentset-uj-w-at5": "old"}}
	if _, err := Up(ctx, f, by, d); err != nil {
		t.Fatal(err)
	}
	var got []string
	for n := range f.jobs {
		got = append(got, n)
	}
	slices.Sort(got)
	if strings.Join(got, ",") != "agentset-uj-autoscale,agentset-uj-w-at1" {
		t.Fatalf("up のあとのジョブ(古い at5 は消す): %v", got)
	}
	if _, err := Down(ctx, f, by, d, true); err != nil || len(f.jobs) != 0 {
		t.Fatalf("down で全部消す: %v %v", err, f.jobs)
	}
}
