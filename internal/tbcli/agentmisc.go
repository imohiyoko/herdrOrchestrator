package tbcli

import (
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// agent の小さなコマンド(taskboard/lib/agents.sh の cmd_agent-ops / cmd_agent-flag、kinds.sh の cmd_agent-kinds、
// spawn.sh の cmd_spawn-freeze / cmd_spawn-unfreeze)。

// kindAll は KIND_ALL。
var kindAll = []string{"claude", "codex", "gemini"}

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
	where, qa := "1=1", []any{}
	if st != "" {
		where, qa = "status=?", []any{st}
	}
	if json {
		rows, err := sqlObjs(db.DB, nil, `SELECT * FROM agent_ops WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa...)
		if err != nil {
			return err
		}
		for i, o := range rows {
			rows[i] = o.Set("id", "L-"+tickets.Text(o.Get("id")))
		}
		compactRows(w, rows)
		return nil
	}
	return qrows(db.DB, `SELECT 'L-'||id, op, target, requested_by, status, created_at, COALESCE(result,'') FROM agent_ops WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa,
		func(_ []string, vals []any) error {
			s := make([]string, len(vals))
			for i, v := range vals {
				if v == nil {
					s[i] = "-"
				} else {
					s[i] = tickets.Text(v)
				}
			}
			fmt.Fprintln(w, strings.Join(s, "\t"))
			return nil
		})
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
	if kill != "yes" && kill != "no" && kill != "unknown" {
		return usage("agent-flag: --killable は yes|no|unknown")
	}
	if kill != "unknown" && strings.Trim(why, " \t\n\v\f\r") == "" {
		return usage("agent-flag: yes / no には --reason が必要です")
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	if by != "human" {
		pat := envOr("TB_SWEEPER_PATTERN", "sweeper*")
		if !util.Glob(pat, by) {
			if _, ok := approval.NewJudge(db.DB, db.Session).GrantOf(by, "agent:flag", name); !ok {
				return die("判定フラグを付けられるのは人、判定役(%s)、policy で agent:flag を許された agent だけです(%s)", pat, by)
			}
		}
		if by == name {
			return die("自分自身の判定は付けられません")
		}
	}
	if err := agents.Sync(db.DB, db.Session); err != nil {
		return die("herdr agent list に失敗しました")
	}
	var state string
	_ = db.QueryRow(`SELECT state FROM agents WHERE name=?`, name).Scan(&state)
	if state != "running" {
		return die("%s は動いていません", name)
	}
	ts := nowTS()
	if _, err := db.Exec(`UPDATE main.agents SET killable=?, killable_reason=NULLIF(?,''), killable_by=?, killable_at=?, killable_session=session_id, updated_at=? WHERE herdr_session=? AND name=?`,
		kill, util.Oneline(why, 300), by, ts, ts, db.Session, name); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s killable=%s\n", name, kill)
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
	if _, err := db.Exec(`INSERT INTO main.tb_flags (herdr_session, key, value, set_by, set_at) VALUES (?, 'spawn_frozen', ?, 'human', ?)
    ON CONFLICT(herdr_session, key) DO UPDATE SET value=excluded.value, set_by=excluded.set_by, set_at=excluded.set_at`, db.Session, v, nowTS()); err != nil {
		return err
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
