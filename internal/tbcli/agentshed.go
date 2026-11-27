package tbcli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// 絞る候補の一覧(taskboard/lib/shed.sh の cmd_agent-shed)。読み取り専用: 何も止めない。

type shedRow struct{ group, name, killable, command string }

func sqlNames(db *store.DB, q string, a ...any) []string {
	var out []string
	_ = qrows(db.DB, q, a, func(_ []string, v []any) error {
		out = append(out, fmt.Sprint(v[0]))
		return nil
	})
	return out
}

func cmdAgentShed(args []string, w, stderr io.Writer) error {
	js, count := false, 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			js = true
		case "--count":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			if !reDigits.MatchString(args[i+1]) {
				return usage("agent-shed: --count は 0 以上の整数")
			}
			count = atoi(args[i+1])
			i++
		default:
			return usage("agent-shed: 不明な引数 %s", args[i])
		}
	}
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		fmt.Fprintln(stderr, "tb: herdr agent list に失敗しました(台帳の内容だけで選びます)")
	}
	held, closed := st.SQLList("active", "review", "blocked"), st.SQLList("done", "cancelled")
	conductors := message.ConductorNames(db.DB, db.Session)
	var out []shedRow
	seen := map[string]bool{}
	add := func(group, name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		k := killableOf(db, name)
		if k == "protected" {
			return
		}
		var open int
		_ = db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE owner_agent=? AND status IN (`+held+`)`, name).Scan(&open)
		cmd := "tb agent-stop " + name
		if k != "yes" || open != 0 {
			cmd += " --force"
		}
		out = append(out, shedRow{group, name, k, cmd})
	}
	// 1. 完了報告済みで待機中
	for _, name := range sqlNames(db, `SELECT a.name FROM agents a WHERE a.state='running'
      AND EXISTS (SELECT 1 FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (`+closed+`))
      AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (`+held+`)) ORDER BY a.name`) {
		if name == "" || slices.Contains(conductors, name) {
			continue
		}
		a, _ := liveOf(live, name)
		if s := liveStatus(a); s == "idle" || s == "done" {
			add("done-idle", name)
		}
	}
	// 2. 止めてよいと判定されている(stale でない)
	for _, name := range sqlNames(db, `SELECT name FROM agents WHERE state='running' AND killable='yes' ORDER BY name`) {
		if name == "" || slices.Contains(conductors, name) || killableOf(db, name) != "yes" {
			continue
		}
		var one int
		if db.QueryRow(`SELECT 1 FROM tasks WHERE owner_agent=? AND status IN (`+held+`) LIMIT 1`, name).Scan(&one) == nil {
			continue
		}
		add("killable", name)
	}
	// 3. 解析役(解析中のものは除く)と、親が終わった子
	for _, name := range sqlNames(db, `SELECT name FROM agents WHERE state='running' AND name LIKE 'analyst-%' ORDER BY name`) {
		if name == "" {
			continue
		}
		if aid := strings.TrimPrefix(name, "analyst-A"); aid != name && reDigits.MatchString(aid) {
			var s string
			_ = db.QueryRow(`SELECT status FROM alerts WHERE id=` + aid).Scan(&s)
			if s == "analyzing" {
				continue
			}
		}
		add("analyst", name)
	}
	// 親(agent_parents)が 1 体以上いて全員が動いていない子と、立てた親(spawned_by)がいて任されたタスクが終わった子
	orphans := agent.Orphans(db.DB)
	orphans = append(orphans, sqlNames(db, `SELECT a.name FROM agents a WHERE a.state='running' AND a.spawned_by IS NOT NULL
      AND EXISTS (SELECT 1 FROM tasks t WHERE t.id=a.spawn_task AND t.status IN (`+closed+`))`)...)
	slices.Sort(orphans)
	for _, name := range orphans {
		if name != "" {
			add("orphan", name)
		}
	}
	if count > 0 && len(out) > count {
		out = out[:count]
	}
	for _, r := range out {
		if js {
			fmt.Fprintln(w, jsonC("group", r.group, "name", r.name, "killable", r.killable, "command", r.command))
		} else {
			fmt.Fprintf(w, "%-10s %-24s %-14s %s\n", r.group, r.name, "killable="+r.killable, r.command)
		}
	}
	return nil
}
