package tbcli

// tb limits(taskboard/lib/limits.sh の cmd_limits): 動作 agent 数などの制限を見る・変える。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
)

var reWatcher = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// limitsCounts は limits_counts: herdr と台帳を同期して、動作中(台帳で running)と作業中(herdr で working の名前つき agent)を数える。

// openIncidents は limits_open_incidents: 今ある障害 workspace(ラベル inc-<3 桁>)の数。取れなければ ok=false。
func openIncidents() (int, bool) {
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

var reIncident = regexp.MustCompile(`^inc-[0-9]{3}$`)

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func limitsUsageErr() error { return usage("limits show|set|unset|mode|get|check|watcher-enabled") }

func cmdLimits(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return limitsUsageErr()
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	sub, args := args[0], args[1:]
	set := limits.Load(db.DB, db.Session)
	keyOK := func(k string) error {
		if !slices.Contains(limits.Keys, k) {
			return usage("不明なキー: %s(使えるキー: %s)", k, strings.Join(limits.Keys, " "))
		}
		return nil
	}
	switch sub {
	case "show":
		return limitsShow(db, set, args, w, stderr)
	case "get":
		if len(args) != 1 {
			return usage("limits get <キー>")
		}
		if err := keyOK(args[0]); err != nil {
			return err
		}
		fmt.Fprintln(w, set.Get(args[0]))
		return nil
	case "set":
		if len(args) != 2 {
			return usage("limits set <キー> <値>(キー: %s。空にする値は '')", strings.Join(limits.Keys, " "))
		}
		v, err := limits.Norm(args[0], args[1])
		if err != nil {
			return usage("limits set: %s", err.Error())
		}
		if err := requireHuman("tb limits set"); err != nil {
			return err
		}
		if err := limitsEdit(db, []limits.KV{{K: args[0], V: v}, {K: "mode", V: "custom"}}); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s = %s\n", args[0], v)
		return nil
	case "unset":
		if len(args) != 1 {
			return usage("limits unset <キー>")
		}
		if err := keyOK(args[0]); err != nil {
			return err
		}
		if err := requireHuman("tb limits unset"); err != nil {
			return err
		}
		if err := limitsEdit(db, []limits.KV{{K: args[0], V: limits.Unset}, {K: "mode", V: "custom"}}); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s unset(既定に戻る)\n", args[0])
		return nil
	case "mode":
		if len(args) != 1 {
			return usage("limits mode normal|lean|minimal")
		}
		t, ok := limits.ModeTable(args[0])
		if !ok {
			return usage("limits mode: normal|lean|minimal のどれか: %s", args[0])
		}
		if err := requireHuman("tb limits mode"); err != nil {
			return err
		}
		var kvs []limits.KV
		for _, k := range limits.ModeKeys {
			v := t[k]
			if v != limits.Unset {
				n, err := limits.Norm(k, v)
				if err != nil {
					return die("モード表の値が不正です(%s=%s): %s", k, v, err.Error())
				}
				v = n
			}
			kvs = append(kvs, limits.KV{K: k, V: v})
		}
		if err := limitsEdit(db, append(kvs, limits.KV{K: "mode", V: args[0]})); err != nil {
			return err
		}
		fmt.Fprintf(w, "mode %s\n", args[0])
		for _, kv := range kvs {
			v := kv.V
			if v == limits.Unset {
				v = "(既定)"
			}
			fmt.Fprintf(w, "  %-18s = %s\n", kv.K, v)
		}
		return nil
	case "watcher-enabled":
		if len(args) != 1 || !reWatcher.MatchString(args[0]) {
			return usage("limits watcher-enabled <見張りの名前>")
		}
		if !strings.Contains(" "+set.Get("watchers")+" ", " "+args[0]+" ") {
			return &exit{exitErr, ""}
		}
		return nil
	case "check":
		if len(args) != 1 {
			return usage("limits check incident-open|running")
		}
		switch args[0] {
		case "running":
			if running, _, ok := limitsCounts(db); ok { // 同期に失敗したら通す(起動側が失敗を報告する)
				if max, lim := set.Max("max_running_agents"); limits.Exceeds(running+1, max, lim) {
					fmt.Fprintf(stderr, "tb: 動作中の agent が上限です(動作中 %d、上限 max_running_agents=%d)。何も作りません。 止める候補は tb agent-shed、上限を変えるのは人(tb limits set max_running_agents <数>)\n", running, max)
					return &exit{4, ""}
				}
			}
			fmt.Fprintln(w, "ok")
			return nil
		case "incident-open":
			return limitsCheckIncidentOpen(db, set, w, stderr)
		}
		return usage("limits check: incident-open|running のどれか: %s", args[0])
	}
	return usage("limits: 不明なサブコマンド %s(show|set|unset|mode|get|check|watcher-enabled)", sub)
}

// limitsEdit は limits_edit: 設定 limits を書き換える(TB_LIMITS でファイルを明示していればそのファイル、無ければこのセッションの行)。
func limitsEdit(db *store.DB, kvs []limits.KV) error {
	cur, src, has, readErr := config.LookupStrict(db.DB, db.Session, "limits")
	if readErr != "" { // 読めなかった本文を空とみなして書くと、今の設定が消える
		return die("設定 limits を読めないため書き換えません(%s)", readErr)
	}
	if strings.HasPrefix(src, "example:") { // 例のファイル(既定値)は書き換えの元にしない
		cur, has = "", false
	}
	body := limits.EditBody(cur, has, kvs)
	info, _ := config.Info("limits")
	if f := os.Getenv(info.Env); f != "" {
		return writeConfigFile(f, body)
	}
	return config.DBPut(db.DB, db.Session, "limits", body, "limits", false, "")
}

func limitsCheckIncidentOpen(db *store.DB, set limits.Set, w, stderr io.Writer) error {
	n := len(strings.Fields(set.Get("incident_roles")))
	max, mlim := set.Max("max_running_agents")
	imax, ilim := set.Max("incident_max_open")
	running, _, ok := limitsCounts(db)
	if !ok {
		fmt.Fprintln(stderr, "tb: herdr agent list に失敗したため、動作中の agent の数を確かめられません")
		return &exit{exitErr, ""}
	}
	if limits.Exceeds(running+n, max, mlim) {
		fmt.Fprintf(stderr, "tb: 障害 workspace を開けません(動作中 %d + 起動する役 %d > 上限 max_running_agents=%d)。何も作りません。 止める候補は tb agent-shed、起動する役を減らす・上限を変えるのは人(tb limits set incident_roles / max_running_agents)\n", running, n, max)
		return &exit{4, ""}
	}
	open, ok := openIncidents()
	if !ok {
		fmt.Fprintln(stderr, "tb: herdr workspace list に失敗したため、開いている障害の数を確かめられません")
		return &exit{exitErr, ""}
	}
	if ilim && open >= imax {
		fmt.Fprintf(stderr, "tb: 障害 workspace を開けません(開いている障害 %d、上限 incident_max_open=%d)。何も作りません。上限を変えるのは人(tb limits set incident_max_open)\n", open, imax)
		return &exit{4, ""}
	}
	fmt.Fprintf(w, "ok(動作中 %d + %d <= %s、障害 %d < %s)\n", running, n, limits.Show(max, mlim), open, limits.Show(imax, ilim))
	return nil
}

func limitsShow(db *store.DB, set limits.Set, args []string, w, stderr io.Writer) error {
	js, line := false, false
	for _, a := range args {
		switch a {
		case "--json":
			js = true
		case "--line":
			line = true
		default:
			return usage("limits show: 不明な引数 %s", a)
		}
	}
	running, working, synced := limitsCounts(db)
	rmax, rlim := set.Max("max_running_agents")
	wmax, wlim := set.Max("max_working_agents")
	imax, ilim := set.Max("incident_max_open")
	amax, alim := set.Max("analyst_max")
	smax, slim := set.Max("spawn_max_total")
	inc, incOK := openIncidents()
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
		fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "running", V: pair(num(running), rmax, rlim)}, {K: "working", V: wpair(num(working))},
			{K: "incidents", V: pair(incN, imax, ilim)}, {K: "analysts", V: pair(num(an), amax, alim)}, {K: "spawn", V: pair(num(sp), smax, slim)},
			{K: "mode", V: set.Mode}, {K: "over", V: ov}, {K: "limits", V: keys}}))
		return nil
	}
	is := "?"
	if incOK {
		is = strconv.Itoa(inc)
	}
	fmt.Fprintf(w, "running %d/%s  working %d/%s  incidents %s/%s  analysts %d/%s  spawn %d/%s  mode %s\n", running, limits.Show(rmax, rlim), working, limits.Show(wmax, wlim),
		is, limits.Show(imax, ilim), an, limits.Show(amax, alim), sp, limits.Show(smax, slim), set.Mode)
	if line {
		return nil
	}
	if len(over) > 0 {
		fmt.Fprintf(w, "超過: %s(新しく作る経路は拒否されます。止める候補: tb agent-shed)\n", strings.Join(over, " "))
	}
	if !synced {
		flush(w) // 標準出力の行との順を bash と同じにする
		fmt.Fprintln(stderr, "注意: herdr agent list に失敗したため、台帳の値で数えました")
	}
	for _, k := range limits.Keys {
		_, src, _ := set.Raw(k)
		fmt.Fprintf(w, "  %-22s = %s   (%s)\n", k, set.Get(k), src)
	}
	return nil
}
