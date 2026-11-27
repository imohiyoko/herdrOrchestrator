package agentset

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// セットが scheduler に置くジョブ(up が job.set で書き、down が job.rm で消す):
//   - agentset-<セット>-<メンバー>: メンバーの schedule(every <N>m: <本文>)
//   - agentset-<セット>-autoscale: 条件(scale_out / scale_in)を書いたメンバーがいれば、autoscale_every ごとに tb agentset autoscale <セット>
//   - agentset-<セット>-<メンバー>-at<N>: scale_at の N 行目(その時刻に tb agentset scale <セット> <メンバー> <数>)

// When は時刻の指定: 1 回だけ(at YYYY-MM-DD HH:MM)か、曜日と時刻(daily 09:00 / weekdays 18:30 / mon,wed 08:00)。
type When struct {
	At   string // YYYY-MM-DD HH:MM(1 回だけ)
	Days string // daily / weekdays / weekends / mon,wed …(毎回)
	HM   string // HH:MM(Days のとき)
	Text string
}

// ScaleAt は scale_at の 1 行(When に N 体にする)。
type ScaleAt struct {
	When When
	N    int
}

// scaleAtMax はメンバーごとの scale_at の行の上限(down は 1 から上限までのジョブを消す)。
const scaleAtMax = 8

var (
	reAtWhen   = regexp.MustCompile(`^at ([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2})$`)
	reDayWhen  = regexp.MustCompile(`^([a-z,]+) ([0-9]{2}:[0-9]{2})$`)
	reSafePath = regexp.MustCompile(`^/[A-Za-z0-9_./ -]+$`)
	dayWords   = map[string]bool{"daily": true, "everyday": true, "weekdays": true, "weekends": true,
		"mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true, "sun": true}
)

func parseWhen(v string) (When, error) {
	w := When{Text: v}
	if g := reAtWhen.FindStringSubmatch(v); g != nil {
		if _, err := time.Parse("2006-01-02 15:04", g[1]); err != nil {
			return w, fmt.Errorf("日時が不正: %s", g[1])
		}
		w.At = g[1]
		return w, nil
	}
	g := reDayWhen.FindStringSubmatch(v)
	if g == nil {
		return w, fmt.Errorf("時刻は「at YYYY-MM-DD HH:MM」か「<曜日> HH:MM」(曜日は daily / weekdays / weekends / mon,wed …): %s", v)
	}
	for _, d := range strings.Split(g[1], ",") {
		if !dayWords[d] {
			return w, fmt.Errorf("曜日は daily / weekdays / weekends / mon tue wed thu fri sat sun をカンマで: %s", g[1])
		}
	}
	if _, err := time.Parse("15:04", g[2]); err != nil {
		return w, fmt.Errorf("時刻が不正: %s", g[2])
	}
	w.Days, w.HM = g[1], g[2]
	return w, nil
}

// lines は scheduler のジョブ定義の時刻の行(1 回だけなら AT、毎回ならその 1 分の窓に 1 日 1 回)。
func (w When) lines() string {
	if w.At != "" {
		return "AT=\"" + w.At + "\"\n"
	}
	return "WINDOW=" + w.HM + "-" + w.HM + "\nINTERVAL_MIN=1440\nDAYS=\"" + strings.ReplaceAll(w.Days, ",", " ") + "\"\n"
}

func parseScaleAt(v string) (ScaleAt, error) {
	i := strings.LastIndex(v, ":") // 時刻の HH:MM にも : があるので、最後の : で分ける
	if i < 0 {
		return ScaleAt{}, fmt.Errorf("scale_at は「<時刻>: <数>」(例: weekdays 09:00: 3 / at 2026-12-01 18:00: 0): %s", v)
	}
	w, err := parseWhen(strings.TrimSpace(v[:i]))
	if err != nil {
		return ScaleAt{}, fmt.Errorf("scale_at: %s", err)
	}
	k, err := strconv.Atoi(strings.TrimSpace(v[i+1:]))
	if err != nil || k < 0 || k > 1000 {
		return ScaleAt{}, fmt.Errorf("scale_at の数は 0 以上の整数: %s", v)
	}
	return ScaleAt{When: w, N: k}, nil
}

// JobSpec は up が書くジョブ 1 つ。
type JobSpec struct {
	Name, Body, Desc string
}

func (d Def) autoscaleJob() string { return "agentset-" + d.Name + "-autoscale" }

func (d Def) scaleAtJob(m Member, i int) string {
	return "agentset-" + d.Name + "-" + m.Name + "-at" + strconv.Itoa(i)
}

// tbCommand は COMMAND に書く tb の呼び出し(tb の場所が分からなければ PATH の tb)。
func (d Def) tbCommand(args ...string) string {
	tb := "tb"
	if reSafePath.MatchString(d.TB) {
		tb = `"` + d.TB + `"`
	}
	return "COMMAND='" + tb + " " + strings.Join(args, " ") + "'\n"
}

// Jobs は up が書くジョブ(schedule・autoscale・scale_at)。
func (d Def) Jobs() []JobSpec {
	var out []JobSpec
	for _, m := range d.scheduled() {
		out = append(out, JobSpec{Name: d.JobName(m), Body: d.JobBody(m), Desc: "定期の指示(" + strconv.Itoa(m.Every) + " 分おき)"})
	}
	if len(d.Autoscaled()) > 0 && d.AutoscaleEvery > 0 {
		out = append(out, JobSpec{Name: d.autoscaleJob(), Desc: fmt.Sprintf("自動スケールの評価(%d 分おき)", d.AutoscaleEvery),
			Body: "# agent セット " + d.Name + " の自動スケール(tb agentset up が書き、down で消す。直すなら定義を直して up)\n" +
				d.tbCommand("agentset", "autoscale", d.Name) + "COMMAND_TIMEOUT=600\nWINDOW=00:00-23:59\nINTERVAL_MIN=" + strconv.Itoa(d.AutoscaleEvery) + "\n"})
	}
	for _, m := range d.Members {
		for i, s := range m.ScaleAt {
			out = append(out, JobSpec{Name: d.scaleAtJob(m, i+1), Desc: fmt.Sprintf("%s に %d 体", s.When.Text, s.N),
				Body: "# agent セット " + d.Name + " のメンバー " + m.Name + " の scale_at(" + s.When.Text + " に " + strconv.Itoa(s.N) + " 体。tb agentset up が書き、down で消す)\n" +
					d.tbCommand("agentset", "scale", d.Name, m.Name, strconv.Itoa(s.N)) + "COMMAND_TIMEOUT=600\n" + s.When.lines()})
		}
	}
	return out
}

// allJobNames は、セットが書きうるジョブの名前すべて(定義から外した古いジョブも消すため)。
func (d Def) allJobNames() []string {
	out := []string{d.autoscaleJob()}
	for _, m := range d.Members {
		out = append(out, d.JobName(m))
		for i := 1; i <= scaleAtMax; i++ {
			out = append(out, d.scaleAtJob(m, i))
		}
	}
	return out
}
