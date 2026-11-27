package agentset

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
)

// 自動スケール: メンバーに scale_out / scale_in の条件を書くと、tb agentset autoscale <セット> が 1 回評価して
// 当たった向きに step 体ずつ増やす・減らす(増やすのは max まで、減らすのは count まで)。
//
//	[member worker]
//	count = 1                      # 減らすときの下限
//	max = 5                        # 増やすときの上限
//	queue = project:web label:bug  # queue に数えるチケット(既定: 担当の無い未着手のチケットすべて)
//	scale_out = queue > 3          # 未着手が 3 件を超えたら足す
//	scale_in = queue == 0 and idle >= 1
//
// 値: queue(queue に当たるチケットの数)/ live(動いているメンバーの数)/ idle・busy・blocked(herdr の状態ごとの数)。
// 比べ方: > >= < <= == !=。and でつなぐと全部が当たったとき。同じキーを何行か書くと、どれかが当たったとき。

// Term は条件の 1 項(値 比べ方 数)。
type Term struct {
	Metric string
	Op     string
	N      int
}

// Cond は and でつないだ項。
type Cond struct {
	Terms []Term
	Text  string // 書かれたとおり(出力に添える)
}

// Queue は queue に数えるチケットの絞り込み。
type Queue struct {
	Project  string
	Label    string
	Category string // 既定 todo
	Owned    bool   // 担当のあるものも数える(既定は担当なしだけ)
}

var metrics = map[string]bool{"queue": true, "live": true, "idle": true, "busy": true, "blocked": true}

var condOps = []string{">=", "<=", "==", "!=", ">", "<"} // 長いほうから

func parseCond(v string) (Cond, error) {
	c := Cond{Text: v}
	for _, part := range strings.Split(v, " and ") {
		f := strings.Fields(part)
		if len(f) != 3 {
			return c, fmt.Errorf("条件は「<値> <比べ方> <数>」を and でつなぐ(例: queue > 3 and idle == 0): %s", v)
		}
		t := Term{Metric: f[0], Op: f[1]}
		if !metrics[t.Metric] {
			return c, fmt.Errorf("条件の値は queue / live / idle / busy / blocked: %s", f[0])
		}
		ok := false
		for _, o := range condOps {
			ok = ok || o == t.Op
		}
		if !ok {
			return c, fmt.Errorf("条件の比べ方は > >= < <= == !=: %s", f[1])
		}
		n, err := strconv.Atoi(f[2])
		if err != nil || n < 0 || n > 100000 {
			return c, fmt.Errorf("条件の数は 0 以上の整数: %s", f[2])
		}
		t.N = n
		c.Terms = append(c.Terms, t)
	}
	return c, nil
}

func parseQueue(v string) (Queue, error) {
	var q Queue
	for _, f := range strings.Fields(v) {
		k, val, _ := strings.Cut(f, ":")
		switch {
		case f == "owned":
			q.Owned = true
		case k == "project" && val != "":
			q.Project = val
		case k == "label" && val != "":
			q.Label = val
		case k == "status" && val != "":
			q.Category = val
		default:
			return q, fmt.Errorf("queue は project:<名前> label:<ラベル> status:<分類> owned を空白で並べる: %s", f)
		}
	}
	return q, nil
}

func (t Term) holds(v int) bool {
	switch t.Op {
	case ">":
		return v > t.N
	case ">=":
		return v >= t.N
	case "<":
		return v < t.N
	case "<=":
		return v <= t.N
	case "==":
		return v == t.N
	default:
		return v != t.N
	}
}

func (c Cond) holds(m map[string]int) bool {
	for _, t := range c.Terms {
		if !t.holds(m[t.Metric]) {
			return false
		}
	}
	return true
}

func anyHolds(cs []Cond, m map[string]int) (Cond, bool) {
	for _, c := range cs {
		if c.holds(m) {
			return c, true
		}
	}
	return Cond{}, false
}

func (m Member) usesQueue() bool {
	for _, c := range append(append([]Cond{}, m.Out...), m.In...) {
		for _, t := range c.Terms {
			if t.Metric == "queue" {
				return true
			}
		}
	}
	return false
}

// Autoscaled は条件を書いたメンバー。
func (d Def) Autoscaled() []Member {
	var out []Member
	for _, m := range d.Members {
		if len(m.Out)+len(m.In) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// Metrics はメンバーの今の値(queue は条件に使うときだけ数える)。
func (d Def) Metrics(ctx context.Context, c Caller, by port.Principal, m Member, have map[string]agent.AgentInfo) (map[string]int, error) {
	v := map[string]int{"live": 0, "idle": 0, "busy": 0, "blocked": 0}
	for _, in := range d.Known(have) {
		a := have[in.Name]
		if in.Member.Name != m.Name || !a.Live {
			continue
		}
		v["live"]++
		switch a.Status {
		case "idle", "done":
			v["idle"]++
		case "working":
			v["busy"]++
		case "blocked":
			v["blocked"]++
		}
	}
	if m.usesQueue() {
		var r ticket.CountResult
		req := ticket.CountReq{Project: m.Queue.Project, Label: m.Queue.Label, Category: m.Queue.Category, Unowned: !m.Queue.Owned}
		if err := c.Call(ctx, by, "ticket.count", req, &r); err != nil {
			return v, err
		}
		v["queue"] = r.Count
	}
	return v, nil
}

func metricText(m Member, v map[string]int) string {
	s := fmt.Sprintf("live=%d idle=%d busy=%d blocked=%d", v["live"], v["idle"], v["busy"], v["blocked"])
	if m.usesQueue() {
		s = fmt.Sprintf("queue=%d ", v["queue"]) + s
	}
	return s
}

// Decide は 1 メンバーの判断: 増やす・減らす数(正なら足す、負なら減らす、0 ならそのまま)と理由。
// scale_out と scale_in が両方当たったら何もしない。増やすのは max まで、減らすのは count まで。
func Decide(m Member, v map[string]int) (int, string) {
	out, okOut := anyHolds(m.Out, v)
	in, okIn := anyHolds(m.In, v)
	live, step := v["live"], max(m.Step, 1)
	switch {
	case okOut && okIn:
		return 0, "scale_out と scale_in が両方当たるのでそのまま(" + out.Text + " / " + in.Text + ")"
	case okOut:
		n := step
		if l := m.Limit(); l >= 0 {
			n = min(n, l-live)
		}
		if n <= 0 {
			return 0, "scale_out(" + out.Text + ")が当たるが max(" + strconv.Itoa(m.Limit()) + ")なのでそのまま"
		}
		return n, "scale_out(" + out.Text + ")"
	case okIn:
		n := min(step, live-m.Count)
		if n <= 0 {
			return 0, "scale_in(" + in.Text + ")が当たるが count(" + strconv.Itoa(m.Count) + ")なのでそのまま"
		}
		return -n, "scale_in(" + in.Text + ")"
	}
	return 0, "どの条件も当たらない"
}

// Autoscale は条件を書いたメンバーごとに 1 回評価し、当たった向きに Scale する(dry なら判断だけ出す)。
// メンバーの失敗はその行に出し、ほかのメンバーは続ける(最初のエラーを返す)。
func Autoscale(ctx context.Context, c Caller, by port.Principal, d Def, dry bool) ([]Line, error) {
	ms := d.Autoscaled()
	if len(ms) == 0 {
		return nil, &define.Error{Code: define.CodeInvalidArgument, Message: "セット " + d.Name + " には scale_out / scale_in を書いたメンバーがいません"}
	}
	have, _, err := all(ctx, c, by)
	if err != nil {
		return nil, err
	}
	var out []Line
	var first error
	for _, m := range ms {
		name := d.Name + "-" + m.Name
		v, err := d.Metrics(ctx, c, by, m, have)
		if err != nil {
			out = append(out, Line{Name: name, Err: err})
			first = cmp.Or(first, err)
			continue
		}
		n, why := Decide(m, v)
		head := metricText(m, v) + ": " + why
		if n == 0 {
			out = append(out, Line{Name: name, Out: head})
			continue
		}
		spec := fmt.Sprintf("%+d", n)
		if dry {
			out = append(out, Line{Name: name, Out: head + " → " + spec + "(--dry-run なので変えません)"})
			continue
		}
		out = append(out, Line{Name: name, Out: head + " → " + spec})
		lines, err := Scale(ctx, c, by, d, m.Name, spec)
		out = append(out, lines...)
		if err != nil {
			if len(lines) == 0 || lines[len(lines)-1].Err == nil {
				out = append(out, Line{Name: name, Err: err})
			}
			first = cmp.Or(first, err)
		}
	}
	return out, first
}
