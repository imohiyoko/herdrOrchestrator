package tbcli

import (
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
)

// agent の小さなコマンド(taskboard/lib/agents.sh の cmd_agent-ops / cmd_agent-flag、kinds.sh の cmd_agent-kinds、
// spawn.sh の cmd_spawn-freeze / cmd_spawn-unfreeze)。

func cmdAgentKinds(args []string, w, stderr io.Writer) error {
	for _, k := range kindAll {
		row := k
		for _, i := range []string{"launch", "model", "resume", "live-model"} {
			row += "\t" + i + "=" + kindYN(kindSupports(k, i))
		}
		row += "\tsession=herdr\tcompact=" + kindYN(kindSupports(k, "compact"))
		fmt.Fprintln(w, row)
	}
	return nil
}

func cmdAgentOps(args []string, w, stderr io.Writer) error {
	json, st := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			json = true
		case "--status":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			st = args[i+1]
			i++
		default:
			return usage("agent-ops: 不明な引数 %s", args[i])
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var res agent.OpsResult
	if err := callAppW(db, app.Human(), io.Discard, "agent.ops", agent.OpsReq{Status: st, JSON: json}, &res); err != nil {
		return err
	}
	fmt.Fprint(w, res.Text)
	return nil
}

func cmdAgentFlag(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-flag <名前> --killable yes|no|unknown [--reason R]")
	}
	name, kill, why := args[0], "", ""
	if err := validAgentName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--killable", "--reason":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			if args[i] == "--killable" {
				kill = args[i+1]
			} else {
				why = args[i+1]
			}
			i++
		default:
			return usage("agent-flag: 不明な引数 %s", args[i])
		}
	}
	if err := agent.ValidFlag(kill, why); err != nil {
		return appErr(err)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	var r agent.FlagResult
	if err := callAppEnv(appEnv(db, stderr), by, "agent.flag", agent.FlagReq{Name: name, Killable: kill, Reason: why}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	fmt.Fprintf(w, "%s killable=%s\n", name, r.Killable)
	return nil
}

func setSpawnFrozen(args []string, w, stderr io.Writer, v, cmd, msg string) error {
	if len(args) != 0 {
		return usage("%s", cmd)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireHuman("tb " + cmd); err != nil {
		return err
	}
	op := "agent.freeze"
	if v == "0" {
		op = "agent.unfreeze"
	}
	var r agent.FreezeResult
	if err := callAppWarn(db, "tb "+cmd, stderr, op, agent.FreezeReq{}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	fmt.Fprintln(w, msg)
	return nil
}

func cmdSpawnFreeze(args []string, w, stderr io.Writer) error {
	return setSpawnFrozen(args, w, stderr, "1", "spawn-freeze", "spawn frozen(以後の agent-spawn はすべて人の承認待ち。解除: tb spawn-unfreeze)")
}

func cmdSpawnUnfreeze(args []string, w, stderr io.Writer) error {
	return setSpawnFrozen(args, w, stderr, "0", "spawn-unfreeze", "spawn unfrozen")
}

func kindYN(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
