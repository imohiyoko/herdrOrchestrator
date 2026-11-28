package agent

// 制限の今の値と数(tb limits show)。

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
)

// LimitsShowReq は agent.limits.show の入力。Line なら 1 行目だけ。
type LimitsShowReq struct {
	JSON bool `api:"json,optional"`
	Line bool `api:"line,optional"`
}

// LimitsShowResult は tb limits show の出力。Unsynced は herdr の一覧を読めず台帳の値で数えた
// (tb は Text のあとに注意を標準エラーへ出し、それから Tail を出す)。
type LimitsShowResult struct {
	Text     string `api:"text" json:"text"`
	Tail     string `api:"tail,optional" json:"tail,omitempty"`
	Unsynced bool   `api:"unsynced,optional" json:"unsynced,omitempty"`
}

var reIncident = regexp.MustCompile(`^inc-[0-9]{3}$`)

// OpenIncidents は今ある障害 workspace(ラベル inc-<3 桁>)の数。取れなければ ok=false。
func OpenIncidents() (int, bool) {
	out, err := herdr.Call("workspace", "list")
	if err != nil {
		return 0, false
	}
	var r struct {
		Result struct {
			Workspaces []struct {
				Label any `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil || r.Result.Workspaces == nil {
		return 0, false
	}
	n := 0
	for _, w := range r.Result.Workspaces {
		if l, _ := w.Label.(string); reIncident.MatchString(l) {
			n++
		}
	}
	return n, true
}

func limitsShowOp(env *app.Env) app.Op {
	return app.Query(env, "agent.limits.show", func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} },
		func(_ context.Context, in LimitsShowReq) (LimitsShowResult, error) {
			return withDB(env, func(db *store.DB) (LimitsShowResult, error) {
				return limitsShow(db, limits.Load(db.DB, db.Session), in.JSON, in.Line), nil
			})
		},
		define.Doc("制限の今の値と数(tb limits show。数える前に herdr の一覧で台帳を同期する。動作中・作業中・障害・解析役・spawn の数と上限、超過、各キーの値と出どころ)"),
		define.Authz("cmd:agent:limits-show"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/limits"),
	)
}

func limitsShow(db *store.DB, set limits.Set, js, line bool) LimitsShowResult {
	var head, tail strings.Builder
	running, working, synced := Counts(db)
	rmax, rlim := set.Max("max_running_agents")
	wmax, wlim := set.Max("max_working_agents")
	imax, ilim := set.Max("incident_max_open")
	amax, alim := set.Max("analyst_max")
	smax, slim := set.Max("spawn_max_total")
	inc, incOK := OpenIncidents()
	var an, sp int
	_ = db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status='analyzing'`).Scan(&an)
	_ = db.QueryRow(`SELECT COUNT(*) FROM agents WHERE state='running' AND spawned_by IS NOT NULL`).Scan(&sp)
	var over []string
	if limits.Exceeds(running, rmax, rlim) {
		over = append(over, "running")
	}
	if limits.Exceeds(working, wmax, wlim) {
		over = append(over, "working")
	}
	if incOK && limits.Exceeds(inc, imax, ilim) {
		over = append(over, "incidents")
	}
	if limits.Exceeds(an, amax, alim) {
		over = append(over, "analysts")
	}
	if limits.Exceeds(sp, smax, slim) {
		over = append(over, "spawn")
	}
	var res LimitsShowResult
	if js {
		num := func(n int) any { return jv.Number(strconv.Itoa(n)) }
		// 上限なしの max は null(max_working_agents の 0 は前からの約束どおり 0)
		pair := func(n any, max int, lim bool) jv.Obj {
			var m any
			if lim {
				m = num(max)
			}
			return jv.Obj{{K: "n", V: n}, {K: "max", V: m}}
		}
		wpair := func(n any) jv.Obj {
			if set.Get("max_working_agents") == "0" {
				return jv.Obj{{K: "n", V: n}, {K: "max", V: num(0)}}
			}
			return pair(n, wmax, wlim)
		}
		var incN any
		if incOK {
			incN = num(inc)
		}
		keys := jv.Obj{}
		for _, k := range limits.Keys {
			_, src, _ := set.Raw(k)
			keys = append(keys, jv.KV{K: k, V: jv.Obj{{K: "value", V: set.Get(k)}, {K: "source", V: src}}})
		}
		ov := []any{}
		for _, o := range over {
			ov = append(ov, o)
		}
		res.Text = jv.Compact(jv.Obj{{K: "running", V: pair(num(running), rmax, rlim)}, {K: "working", V: wpair(num(working))},
			{K: "incidents", V: pair(incN, imax, ilim)}, {K: "analysts", V: pair(num(an), amax, alim)}, {K: "spawn", V: pair(num(sp), smax, slim)},
			{K: "mode", V: set.Mode}, {K: "over", V: ov}, {K: "limits", V: keys}}) + "\n"
		return res
	}
	is := "?"
	if incOK {
		is = strconv.Itoa(inc)
	}
	fmt.Fprintf(&head, "running %d/%s  working %d/%s  incidents %s/%s  analysts %d/%s  spawn %d/%s  mode %s\n", running, limits.Show(rmax, rlim), working, limits.Show(wmax, wlim),
		is, limits.Show(imax, ilim), an, limits.Show(amax, alim), sp, limits.Show(smax, slim), set.Mode)
	if line {
		res.Text = head.String()
		return res
	}
	if len(over) > 0 {
		fmt.Fprintf(&head, "超過: %s(新しく作る経路は拒否されます。止める候補: tb agent-shed)\n", strings.Join(over, " "))
	}
	res.Text, res.Unsynced = head.String(), !synced
	for _, k := range limits.Keys {
		_, src, _ := set.Raw(k)
		fmt.Fprintf(&tail, "  %-22s = %s   (%s)\n", k, set.Get(k), src)
	}
	res.Tail = tail.String()
	return res
}
