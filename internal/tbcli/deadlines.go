package tbcli

// tb deadlines-import(taskboard/lib/reminders.sh の cmd_deadlines-import)。

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/reminder"
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
	var hasDue int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name='due'`).Scan(&hasDue)
	if hasDue == 0 {
		return die("deadlines-import: この DB には期日の列がありません(スキーマが古い)")
	}
	env := reminder.LoadEnv()
	lines, ok := env.Lines()
	if !ok {
		fmt.Fprintf(w, "期限ファイルがありません: %s\n", env.Deadlines)
		return nil
	}
	label := systemLabelOf(db, stderr, "deadline")
	n, m := 0, 0
	for _, l := range lines {
		var valid int
		_ = db.QueryRow(`SELECT date(?1) IS ?1`, l.Date).Scan(&valid)
		if valid != 1 {
			fmt.Fprintf(w, "skip(日付が正しくない): %s | %s\n", l.Date, l.Label)
			continue
		}
		if have := reminder.DeadlineTicket(db.DB, l.Date, l.Label); have != "" {
			fmt.Fprintf(w, "skip(取り込み済み %s): %s | %s\n", have, l.Date, l.Label)
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
		fmt.Fprintf(w, "add %s: %s | %s\n", strings.TrimRight(out.String(), "\n"), l.Date, l.Label)
		n++
	}
	suffix := ""
	if dry {
		suffix = "(dry-run: 何も書いていません)"
	}
	fmt.Fprintf(w, "作成 %d 件 / 取り込み済み %d 件%s\n", n, m, suffix)
	return nil
}
