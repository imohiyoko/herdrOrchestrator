package tbcli

import (
	"fmt"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

// 絞る候補の一覧(taskboard/lib/shed.sh の cmd_agent-shed)。読み取り専用: 何も止めない。選ぶのは agent.shed。

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
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var res agent.ShedResult
	if err := callAppW(db, app.Human(), io.Discard, "agent.shed", agent.ShedReq{Count: count}, &res); err != nil {
		return err
	}
	if res.LiveErr {
		fmt.Fprintln(stderr, "tb: herdr agent list に失敗しました(台帳の内容だけで選びます)")
	}
	for _, r := range res.Rows {
		if js {
			fmt.Fprintln(w, jsonC("group", r.Group, "name", r.Name, "killable", r.Killable, "command", r.Command))
		} else {
			fmt.Fprintf(w, "%-10s %-24s %-14s %s\n", r.Group, r.Name, "killable="+r.Killable, r.Command)
		}
	}
	return nil
}
