package tbcli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/reminder"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

const reminderUsage = `tb reminder list [--json]          人が忘れているかもしれないものを優先度順に出す(何も書かない)
tb reminder check                  出すものがあれば 0、無ければ 3(何も出さず、何も書かない)
tb reminder publish [--json]       まとめを出し、前回の記録を更新する(同じ項目は「継続(N 日目)」の 1 行。1 日 1 回は全件)
tb reminder scan [-p project] [--within N]   期限切れ・期限間近のタスクに履歴のイベントを 1 日 1 回ずつ残す
tb reminder nudge [<宛先> ...] [--pool] [--dry-run] [--idle-min N] [--max N] [--json]   チケットを握ったまま休んでいる agent に催促する
(古い名前 tb reminders [--check | --publish] / tb due-scan / tb nudge も同じに使える。期限ファイルの取り込みは tb deadlines-import のまま)
`

// cmdReminder は tb reminder <list|check|publish|scan|nudge> と、古い名前 reminders / due-scan / nudge。
func cmdReminder(args []string, w, stderr io.Writer, legacy string) error {
	sub := legacy
	if sub == "" {
		if len(args) == 0 {
			fmt.Fprint(w, reminderUsage)
			return &exit{exitUsage, ""}
		}
		sub, args = args[0], args[1:]
		switch sub {
		case "help", "-h", "--help":
			fmt.Fprint(w, reminderUsage)
			return nil
		case "list", "publish":
			for _, a := range args {
				if a != "--json" {
					return usage("reminder %s: 不明な引数 %s(--json だけ)", sub, a)
				}
			}
			if sub == "publish" {
				args = append([]string{"--publish"}, args...)
			}
			sub = "reminders"
		case "check":
			if len(args) != 0 {
				return usage("reminder check(引数なし)")
			}
			sub, args = "reminders", []string{"--check"}
		case "scan":
			sub = "due-scan"
		case "nudge":
		default:
			return usage("reminder: 不明なサブコマンド %s(list / check / publish / scan / nudge)", sub)
		}
	}
	switch sub {
	case "reminders":
		return reminders(args, w, stderr)
	case "due-scan":
		return dueScan(args, w, stderr)
	default:
		return nudge(args, w, stderr)
	}
}

// reminders は cmd_reminders。
func reminders(args []string, w, stderr io.Writer) error {
	mode, js := "show", false
	for _, a := range args {
		switch a {
		case "--check":
			mode = "check"
		case "--publish":
			mode = "publish"
		case "--json":
			js = true
		default:
			return usage("reminders: 不明な引数 %s(--check | --publish [--json] | --json)", a)
		}
	}
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	env := reminder.LoadEnv()
	if !env.Valid() {
		return usage("TB_REMINDERS_STALE_HOURS / TB_REMINDERS_DUE_DAYS / TB_DIGEST_QUEUED_SEC は数字")
	}
	td, err := today() // TB_TODAY が日付でなければ、ここで使い方の誤り
	if err != nil {
		return err
	}
	r := &reminder.R{DB: db.DB, Session: db.Session, St: st, Env: env, Today: td}
	items, err := r.Collect(reminder.Now())
	if err != nil {
		return die("reminders: 材料を読めませんでした")
	}
	q := notify.New(db.DB, db.Session)
	prev := reminder.ParseState(q.SeenGet("reminders:state"))
	fullDay := q.SeenGet("reminders:full-day")
	full := mode == "publish" && fullDay != td && len(items) > 0
	d := reminder.MakeDiff(items, prev, td, fullDay, full)
	if mode == "check" { // 何も書かない・何も出さない
		if d.Changed {
			return nil
		}
		return &exit{reminder.NoChange, ""}
	}
	text := d.Text(reminder.Now())
	// 秘密らしい値: 題に紛れ込んだものを伏せる(継続の項目は本文に出ないが、--json と Web UI の行には出るので全項目の題も調べる)
	text = d.Redact(text)
	if mode == "publish" {
		ts := reminder.Now()
		last := `{"at":` + util.JQString(ts) + `,"full":` + strconv.FormatBool(d.Full) + `,"removed":` + d.RemovedJSON() +
			`,"text":` + util.JQString(text) + `,"items":` + d.ItemsJSON() + "}"
		if err := seenTx(db, ts, [][2]string{{"reminders:state", d.StateJSON()}, {"reminders:last", last}}, full, td); err != nil {
			return die("reminders: 記録を書けませんでした")
		}
	}
	if js {
		fmt.Fprintln(w, `{"full":`+strconv.FormatBool(d.Full)+`,"removed":`+d.RemovedJSON()+`,"items":`+d.ItemsJSON()+"}")
	} else {
		fmt.Fprintln(w, text)
	}
	return nil
}

// seenTx は notify_seen の行を 1 つのトランザクションで書く(full なら reminders:full-day も)。
func seenTx(db *store.DB, ts string, kv [][2]string, full bool, td string) error {
	if full {
		kv = append(kv, [2]string{"reminders:full-day", td})
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range kv {
		if _, err := tx.Exec(`INSERT INTO main.notify_seen (herdr_session, key, value, updated_at) VALUES (?, ?, ?, ?)
    ON CONFLICT(herdr_session, key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, db.Session, p[0], p[1], ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var reProject = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

var reWithin = regexp.MustCompile(`^[0-9]{1,3}$`)

// dueScan は cmd_due-scan。
func dueScan(args []string, w, stderr io.Writer) error {
	within, project := 1, ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "-p", "--project":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			if !reProject.MatchString(args[i+1]) {
				return usage("project は英数字と _ のみ: %s", args[i+1])
			}
			project = args[i+1]
			i++
		case "--within":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			if !reWithin.MatchString(args[i+1]) {
				return usage("due-scan: --within は 0〜999 の日数: %s", args[i+1])
			}
			within, _ = strconv.Atoi(args[i+1])
			i++
		default:
			return usage("due-scan: 不明な引数 %s", a)
		}
	}
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	td, err := today()
	if err != nil {
		return err
	}
	over, soon, err := tickets.DueScan(db.DB, db.Session, st.SQLList("done", "cancelled"), td, within, project)
	if err != nil {
		return die("due-scan: 履歴に書けませんでした")
	}
	q := notify.New(db.DB, db.Session)
	q.ActiveSQL = st.SQLList("active", "review")
	_ = q.RunRules()
	fmt.Fprintf(w, "overdue %d due_soon %d\n", over, soon)
	return nil
}

var reDigitsOnly = regexp.MustCompile(`^[0-9]+$`)

// nudge は cmd_nudge。
func nudge(args []string, w, stderr io.Writer) error {
	n := &reminder.Nudge{Stderr: stderr}
	idle, mx := reminder.NudgeIdleMin(), reminder.NudgeMax()
	js := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--dry-run":
			n.Dry = true
		case a == "--json":
			js = true
		case a == "--pool":
			n.Pool = true
		case a == "--idle-min" || a == "--max":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			if a == "--max" {
				mx = args[i+1]
			} else {
				idle = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "-"):
			return usage("nudge [<宛先> ...] [--pool] [--dry-run] [--idle-min N] [--max N] [--json]")
		default: // 宛先(agent 名の glob か @役割の glob)。明示したら ignore は適用しない
			n.Targets = append(n.Targets, a)
		}
	}
	if !reDigitsOnly.MatchString(idle) || !reDigitsOnly.MatchString(mx) {
		return usage("nudge: --idle-min / --max(TB_NUDGE_IDLE_MIN / TB_NUDGE_MAX)は数字")
	}
	n.SetLimits(idle, mx)
	if !n.Dry {
		if err := requireHuman("tb nudge"); err != nil {
			return err
		}
	}
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	n.DB, n.Session, n.St = db.DB, db.Session, st
	rows, err := n.Run()
	if err != nil {
		var le reminder.ErrLive
		var ce reminder.ErrConfig
		switch {
		case errors.As(err, &le):
			return &exit{11, err.Error()}
		case errors.As(err, &ce):
			return die("%s", err.Error())
		}
		return err
	}
	for _, r := range rows {
		if js {
			fmt.Fprintf(w, `{"agent":%s,"task":%s,"result":%s,"reason":%s}`+"\n", util.JQString(r.Agent), util.JQString(r.Task), util.JQString(r.Result), util.JQString(r.Reason))
		} else {
			fmt.Fprintln(w, strings.Join([]string{r.Agent, r.Task, r.Result, r.Reason}, "\t"))
		}
	}
	return nil
}
