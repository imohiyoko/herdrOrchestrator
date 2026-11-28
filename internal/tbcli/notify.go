package tbcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appnotify "github.com/imohiyoko/herdrorchestrator/internal/app/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	reEventRef = regexp.MustCompile(`^E-([0-9]{1,18})$`)
	reHash12   = regexp.MustCompile(`^[0-9a-f]{12}$`)
	reMsgID    = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,80}$`)
	reLimit    = regexp.MustCompile(`^[0-9]{1,4}$`)
	reStatusW  = regexp.MustCompile(`^[a-z-]+$`)
)

type notifyCmd struct {
	db     *store.DB
	st     tickets.StatusSet
	q      *notify.Q
	w      io.Writer
	stderr io.Writer
}

func cmdNotify(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	q := notify.New(db.DB, db.Session)
	q.ActiveSQL = st.SQLList("active", "review")
	n := &notifyCmd{db, st, q, w, stderr}
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "status":
		return n.status(args)
	case "on", "off":
		if len(args) != 0 {
			return usage("notify %s(引数なし)", sub)
		}
		if err := requireHuman("tb notify " + sub); err != nil {
			return err
		}
		var r appnotify.SwitchResult
		if err := callAppWarn(db, "tb notify "+sub, stderr, "notify."+sub, appnotify.SwitchReq{}, &r); err != nil {
			return err
		}
		if r.Pending != nil {
			return pendingExit(w, r.Pending)
		}
		fmt.Fprintf(w, "notify: enabled = %s\n", sub)
		if sub == "on" {
			fmt.Fprintln(w, "(外へ出る前に tb notify status と tb notify test で確かめてください)")
		}
		return nil
	case "test":
		if len(args) != 0 {
			return usage("notify test(引数なし)")
		}
		if err := requireHuman("tb notify test"); err != nil {
			return err
		}
		var r appnotify.TestResult
		if err := callAppWarn(db, "tb notify test", stderr, "notify.test", appnotify.SwitchReq{}, &r); err != nil {
			return err
		}
		if r.Pending != nil {
			return pendingExit(w, r.Pending)
		}
		fmt.Fprintf(w, "%s %s %s\n", r.ID, r.Status, r.Text)
		return nil
	case "dry-run":
		return n.dryRun(args)
	case "tail":
		return n.tail(args)
	case "ack":
		return n.ack(args)
	case "flush":
		if len(args) != 0 {
			return usage("notify flush(引数なし)")
		}
		if err := requireHuman("tb notify flush"); err != nil {
			return err
		}
		var r appnotify.FlushResult // TB_TODAY が日付でなければ期限のイベントは見ない(今と同じ)
		if err := callAppWarn(db, "tb notify flush", stderr, "notify.flush", appnotify.FlushReq{Today: os.Getenv("TB_TODAY")}, &r); err != nil {
			return err
		}
		if r.Pending != nil {
			return pendingExit(w, r.Pending)
		}
		if r.Off {
			fmt.Fprintln(w, "notify: off(何もしません)")
			return nil
		}
		fmt.Fprintln(w, "notify: flushed")
		return nil
	case "events":
		return n.events(args)
	case "hash":
		if len(args) != 1 {
			return usage("notify hash <本文>")
		}
		fmt.Fprintln(w, notify.Hash(args[0]))
		return nil
	case "event":
		return n.event(args)
	case "rules":
		return n.rules(args)
	}
	return usage("notify: 不明なサブコマンド %s(status|on|off|test|dry-run|tail|ack|flush|events|rules)", sub)
}

// readText は通知の読むだけの操作を呼んで、Text をそのまま出す(人として呼ぶ。ticket の readText と同じ)。
func (n *notifyCmd) readText(op string, in any) error {
	var r appnotify.TextResult
	if err := callAppW(n.db, app.Human(), io.Discard, op, in, &r); err != nil {
		return err
	}
	fmt.Fprint(n.w, r.Text)
	return nil
}

func (n *notifyCmd) status(args []string) error {
	return n.readText("notify.status", appnotify.StatusReq{JSON: len(args) > 0 && args[0] == "--json"})
}

func (n *notifyCmd) dryRun(args []string) error {
	if len(args) != 0 {
		return usage("notify dry-run(引数なし)")
	}
	return n.readText("notify.dryrun", appnotify.DryRunReq{})
}

func (n *notifyCmd) tail(args []string) error {
	lines, follow := "20", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n":
			if i+1 >= len(args) {
				return usage("-n に値が必要です")
			}
			if !reNum.MatchString(args[i+1]) {
				return usage("tail: -n は整数")
			}
			lines = args[i+1]
			i++
		case "-f", "--follow":
			follow = true
		default:
			return usage("notify tail [-n N] [-f]")
		}
	}
	d := notify.Dir()
	if os.MkdirAll(d, 0o755) == nil {
		_ = os.Chmod(d, 0o700)
	}
	f := filepath.Join(d, "stdout.log")
	if fh, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
		fh.Close()
		_ = os.Chtimes(f, time.Now(), time.Now())
	}
	a := []string{"-n", lines}
	if follow {
		a = append(a, "-f")
	}
	cmd := exec.Command("tail", append(a, f)...)
	cmd.Stdout, cmd.Stderr = n.w, n.stderr
	if follow {
		if bw, ok := n.w.(interface{ Flush() error }); ok {
			_ = bw.Flush()
		}
		cmd.Stdout = os.Stdout
	}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &exit{ee.ExitCode(), ""}
		}
		return err
	}
	return nil
}

func (n *notifyCmd) events(args []string) error {
	js, st, lim := false, "", "50"
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--json":
			js = true
		case "--status", "--limit":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			i++
			if a == "--status" {
				st = args[i]
			} else {
				if !reLimit.MatchString(args[i]) {
					return usage("events: --limit は整数")
				}
				lim = args[i]
			}
		default:
			return usage("notify events [--status S] [--limit N] [--json]")
		}
	}
	if st != "" && !reStatusW.MatchString(st) { // --limit 0 で op を呼ばないときも確かめる
		return usage("events: --status が不正")
	}
	l, _ := strconv.Atoi(lim)
	if l == 0 { // --limit 0 は前と同じく 0 件(op の 0 は既定の 50)
		return nil
	}
	return n.readText("notify.events", appnotify.EventsReq{Status: st, Limit: l, JSON: js})
}

func (n *notifyCmd) ack(args []string) error {
	if len(args) < 2 {
		return usage("notify ack N-<n> <hash> [--id <投稿の ID>]")
	}
	idS := strings.TrimPrefix(args[0], "N-")
	if !reNum.MatchString(idS) {
		return usage("イベント ID は N-<数字>: %s", args[0])
	}
	hash := args[1]
	f, err := flags("notify ack", args[2:], []string{"--id"}, nil)
	if err != nil {
		return err
	}
	mid := f["--id"]
	if !reHash12.MatchString(hash) {
		return usage("ack: hash は 12 桁の 16 進(tb notify hash で出す)")
	}
	if mid != "" && !reMsgID.MatchString(mid) {
		return usage("ack: --id は英数字と . _ : / - の 80 字まで")
	}
	var r appnotify.AckResult
	if err := reporterCall(n.db, "tb notify ack", n.stderr, "notify.ack", appnotify.AckReq{ID: args[0], Hash: hash, MsgID: mid}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(n.w, r.Pending)
	}
	if r.Status == "mismatch" {
		fmt.Fprintf(n.stderr, "%s mismatch\n", r.ID)
		return &exit{exitErr, ""}
	}
	fmt.Fprintf(n.w, "%s %s\n", r.ID, r.Status)
	return nil
}

func (n *notifyCmd) event(args []string) error {
	if len(args) < 2 {
		return usage("notify event incident_opened|incident_closed <ID> [--title T] [--severity S]")
	}
	typ, ref := args[0], args[1]
	if typ != "incident_opened" && typ != "incident_closed" {
		return usage("notify event: 種別は incident_opened|incident_closed")
	}
	f, err := flags("notify event", args[2:], []string{"--title", "--severity"}, nil)
	if err != nil {
		return err
	}
	if err := requireHuman("tb notify event"); err != nil {
		return err
	}
	var r appnotify.EventResult
	if err := callAppWarn(n.db, "tb notify event", n.stderr, "notify.event", appnotify.EventReq{Type: typ, Ref: ref, Severity: f["--severity"], Title: f["--title"]}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(n.w, r.Pending)
	}
	fmt.Fprintf(n.w, "notify: %s %s\n", typ, ref)
	return nil
}

func (n *notifyCmd) rules(args []string) error {
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	w := n.w
	switch sub {
	case "show":
		if len(args) != 0 {
			return usage("notify rules [show]")
		}
		body, src, _ := config.Lookup(n.db.DB, n.db.Session, "notify-rules")
		fmt.Fprintf(w, "# notify-rules(出どころ: %s)。書くのは人だけ: tb config set notify-rules < 規則のファイル\n", src)
		rules, bad := notify.ParseRules(body)
		if bad != "" {
			var b strings.Builder
			for _, l := range strings.Split(bad, "\n") { // sed 's/^/#   /' <<<"$NRULE_ERR"(最後の空行にも付く)
				b.WriteString("#   " + l + "\n")
			}
			fmt.Fprintf(w, "# 誤りのある行(飛ばしています):\n%s\n", strings.TrimRight(b.String(), "\n"))
		}
		if len(rules) == 0 {
			fmt.Fprintln(w, "(規則なし。今の決め打ちの通知だけが動きます)")
			return nil
		}
		for _, r := range rules {
			conds := ""
			if len(r.Conds) > 0 {
				conds = strings.Join(r.Conds, " ") + " "
			}
			fmt.Fprintf(w, "%d 行目\t%s %s-> %s\n", r.Line, r.Kind, conds, strings.Join(r.Sinks, " "))
		}
		return nil
	case "test":
		var m []string
		if len(args) == 1 {
			m = reEventRef.FindStringSubmatch(args[0])
		}
		if m == nil {
			return usage("notify rules test E-<n>")
		}
		return n.readText("notify.rules.test", appnotify.RulesTestReq{ID: args[0]})
	case "run":
		if len(args) != 0 {
			return usage("notify rules run")
		}
		_ = n.q.RunRules()
		fmt.Fprintf(w, "notify rules: 読み位置 E-%s\n", n.q.SeenGet("rules:last"))
		return nil
	}
	return usage("notify rules [show | test E-<n> | run]")
}
