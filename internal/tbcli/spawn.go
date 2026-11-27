package tbcli

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// tb agent spawn: 規則つきの子 agent 作成。判定と作成は API(agent.spawn。internal/app/agent/spawn.go)。
// ここは引数の読み取りと表示だけ。

// itemText は item_text: 1 行にして [ ] | を ( ) / に。
func itemText(s string, n int) string {
	return strings.NewReplacer("[", "(", "]", ")", "|", "/").Replace(util.Oneline(s, n))
}

// spawnRecheck は spawn_recheck: 承認時の再検査。規則に合わなくなっていたら理由を stderr に出して false。
func spawnRecheck(db *store.DB, st tickets.StatusSet, id int64, parent, role, task string, stderr io.Writer) bool {
	if why := agent.SpawnRecheck(db, st, id, parent, role, task); why != "" {
		fmt.Fprintf(stderr, "tb: L-%d は承認できません(規則に合わなくなりました): %s。L-%d は承認待ちのままです(tb agent-reject L-%d で却下)\n", id, why, id, id)
		return false
	}
	return true
}

func cmdAgentSpawn(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || (args[0] != "-" && strings.HasPrefix(args[0], "-")) {
		return usage("agent-spawn <役割|-> --task <task-id> --brief <依頼の本文>")
	}
	role, task, brief, hasBrief := args[0], "", "", false
	for i := 1; i < len(args); i++ {
		switch a := args[i]; a {
		case "--task", "--brief":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			if a == "--task" {
				task = args[i+1]
			} else {
				brief, hasBrief = args[i+1], true
			}
			i++
		case "--name", "--cwd", "--model", "--prompt", "--role", "--killable":
			return usage("agent-spawn: %s は指定できません(名前・作業場所・モデル・プロンプトは tb が決めます)", a)
		default:
			return usage("agent-spawn: 不明な引数 %s", a)
		}
	}
	if role != "-" && !reRoleName.MatchString(role) {
		return usage("agent-spawn: 役割は roles/<名前>.md の名前(例: worker)か、役割なしの -: %s", role)
	}
	if task == "" {
		return usage("agent-spawn: --task が必要です")
	}
	if err := needID(task); err != nil {
		return err
	}
	if !hasBrief || blank(brief) {
		return usage("agent-spawn: --brief が必要です")
	}
	if n := utf8.RuneCountInString(brief); n > agent.BriefMax {
		return usage("agent-spawn: --brief は %d 字まで(%d 字)", agent.BriefMax, n)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	parent, err := requester()
	if err != nil {
		return err
	}
	var res agent.SpawnResult
	if err := callAppW(db, app.PrincipalOf(parent), stderr, "agent.spawn", agent.SpawnReq{Role: role, Task: task, Brief: brief}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.Out)
	return nil
}
