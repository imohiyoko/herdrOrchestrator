package tbcli

import (
	"io"
	"regexp"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
)

// tb system-send <宛先> --text T --job J(taskboard/lib/transport.sh の cmd_system-send)。
// scheduler(tick.sh)からのプロンプト送信。agent の pane からは使えない。記録は messages(kind=scheduler)。

var reSchedJob = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func cmdSystemSend(args []string, w, stderr io.Writer) error {
	if len(args) < 3 || args[1] != "--text" {
		return usage("system-send <宛先> --text T --job J")
	}
	target, text, job := args[0], args[2], ""
	if len(args) > 3 && args[3] == "--job" && len(args) > 4 {
		job = args[4]
	}
	if !reSchedJob.MatchString(job) {
		return usage("system-send: --job が必要です")
	}
	if err := requireHuman("tb system-send"); err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if rc := transport.Send(db.DB, db.Session, target, text, "job:"+job, "scheduler", stderr); rc != 0 {
		return &exit{rc, ""}
	}
	return nil
}
