package tbcli

// tb deadlines-import(taskboard/lib/reminders.sh の cmd_deadlines-import)。

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appreminder "github.com/imohiyoko/herdrorchestrator/internal/app/reminder"
)

// cmdDeadlinesImport は tb deadlines-import [-p project] [--dry-run]: 期限ファイルの行を、担当なしで期日つきのチケットにする
// (ADR 0003 の段 5。人だけ)。同じ題・同じ期日のチケットが既にあれば作らない(何度流してもよい)。ファイルは書き換えない。
func cmdDeadlinesImport(args []string, w, stderr io.Writer) error {
	project, dry := "deadline", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--project":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			if err := needProject(v); err != nil {
				return err
			}
			project = v
			i++
		case "--dry-run":
			dry = true
		default:
			return usage("deadlines-import [-p project(既定 deadline)] [--dry-run]")
		}
	}
	if err := requireHuman("tb deadlines-import"); err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var plan appreminder.DeadlinesResult
	if err := callAppW(db, app.Human(), io.Discard, "reminder.deadlines", appreminder.DeadlinesReq{}, &plan); err != nil {
		return err
	}
	if plan.NoDue {
		return die("deadlines-import: この DB には期日の列がありません(スキーマが古い)")
	}
	if plan.Missing {
		fmt.Fprintf(w, "期限ファイルがありません: %s\n", plan.File)
		return nil
	}
	label := systemLabelOf(db, stderr, "deadline")
	n, m := 0, 0
	added := map[[2]string]string{} // このファイルの前の行で作ったもの(同じ行が 2 度あれば 2 度目は取り込み済み)
	for _, l := range plan.Lines {
		if id, ok := added[[2]string{l.Date, l.Label}]; ok && l.State == "new" {
			l.State, l.Ticket = "have", id
		}
		switch l.State {
		case "invalid":
			fmt.Fprintf(w, "skip(日付が正しくない): %s | %s\n", l.Date, l.Label)
			continue
		case "have":
			fmt.Fprintf(w, "skip(取り込み済み %s): %s | %s\n", l.Ticket, l.Date, l.Label)
			m++
			continue
		}
		if dry {
			fmt.Fprintf(w, "add(dry-run): %s | %s\n", l.Date, l.Label)
			n++
			continue
		}
		var out bytes.Buffer
		if err := cmdAdd([]string{"-p", project, "-t", l.Label, "--due", l.Date, "-l", label}, &out, stderr); err != nil {
			finish(err, stderr) // bash は cmd_add の die の文を出してから、ここで止まる
			return die("deadlines-import: チケットを作れませんでした: %s", l.Label)
		}
		id := strings.TrimRight(out.String(), "\n")
		added[[2]string{l.Date, l.Label}] = id
		fmt.Fprintf(w, "add %s: %s | %s\n", id, l.Date, l.Label)
		n++
	}
	suffix := ""
	if dry {
		suffix = "(dry-run: 何も書いていません)"
	}
	fmt.Fprintf(w, "作成 %d 件 / 取り込み済み %d 件%s\n", n, m, suffix)
	return nil
}
