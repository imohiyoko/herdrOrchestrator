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

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/capv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
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
		if err := n.writeConf("enabled", sub); err != nil {
			return err
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
		id, _ := q.Enqueue(notify.Event{Type: "test", Ref: fmt.Sprintf("test-%d-%d", time.Now().Unix(), os.Getpid()), Force: true})
		if id == 0 {
			return die("test: イベントを作れませんでした")
		}
		if n.field(id, "slack_status") == "pending" {
			q.SlackSend(id)
		}
		fmt.Fprintf(w, "N-%d %s %s\n", id, n.field(id, "slack_status"), n.field(id, "text"))
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
		if !q.C.On() {
			fmt.Fprintln(w, "notify: off(何もしません)")
			return nil
		}
		_ = q.Periodic()
		if td, err := today(); err == nil { // 期限のイベント(tb due-scan。1 日 1 回ずつ)
			_, _, _ = tickets.DueScan(db.DB, db.Session, st.SQLList("done", "cancelled"), td, 1, "")
		}
		_ = q.RunRules() // 通知の規則の取りこぼし
		q.FlushSlack()
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

// field はこのセッションのイベントの 1 列(無ければ空)。
func (n *notifyCmd) field(id int64, col string) string {
	var v any
	_ = n.db.QueryRow("SELECT "+col+" FROM notify_events WHERE id=?", id).Scan(&v)
	return strings.TrimRight(tickets.Text(v), "\n")
}

// writeConf は notify_write_conf: 設定 notify のそのキーの行を置き換える(無ければ足す)。人だけが呼ぶ。
func (n *notifyCmd) writeConf(k, v string) error {
	var out []string
	found := false
	if body, _, ok := config.Lookup(n.db.DB, n.db.Session, "notify"); ok && body != "" {
		for _, line := range config.Lines(body) {
			if !strings.HasPrefix(line, "#") && strings.Contains(line, "=") && config.Trim(strings.SplitN(line, "=", 2)[0]) == k {
				if !found {
					out = append(out, k+" = "+v)
					found = true
				}
			} else {
				out = append(out, line)
			}
		}
	}
	if !found {
		out = append(out, k+" = "+v)
	}
	body := strings.TrimRight(strings.Join(out, "\n"), "\n")
	info, _ := config.Info("notify")
	if f := os.Getenv(info.Env); f != "" {
		return writeConfigFile(f, body)
	}
	return config.DBPut(n.db.DB, n.db.Session, "notify", body, "notify", false, "")
}

func (n *notifyCmd) webhookState() string {
	f := n.q.C.Get("webhook_file")
	if f == "" {
		return "未設定"
	}
	if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
		return "ファイルが無い"
	}
	if st, err := os.Lstat(f); err == nil && st.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0o600 && st.Mode().IsRegular() {
		return "ok(0600)"
	}
	return "権限が 0600 ではない"
}

func setOrUnset(v, set, unset string) string {
	if v != "" {
		return set
	}
	return unset
}

func (n *notifyCmd) status(args []string) error {
	c, w := n.q.C, n.w
	var counts []string
	_ = store.Each(n.db.DB, "SELECT slack_status, COUNT(*) FROM notify_events GROUP BY slack_status ORDER BY 1", nil, func(_ []string, vals []any) error {
		counts = append(counts, tickets.Text(vals[0])+" "+tickets.Text(vals[1]))
		return nil
	})
	cs := strings.Join(counts, ",")
	if len(args) > 0 && args[0] == "--json" {
		fmt.Fprintf(w, `{"enabled":%s,"slack_via":%s,"slack_target":%s,"notifier_agent":%s,"webhook_file":%s,"default_sinks":%s,"rate_per_min":%s,"rate_per_hour":%s,"max_len":%s,"slack_status_counts":%s}`+"\n",
			util.JQString(c.Get("enabled")), util.JQString(c.Get("slack_via")), util.JQString(setOrUnset(c.Get("slack_target"), "set", "unset")),
			util.JQString(c.Get("notifier_agent")), util.JQString(n.webhookState()), util.JQString(c.Get("sinks_default")),
			notifyCapJSON(c, "rate_per_min"), notifyCapJSON(c, "rate_per_hour"), notifyCapJSON(c, "max_len"), util.JQString(cs))
		return nil
	}
	fmt.Fprintf(w, "enabled: %s (%s)\n", c.Get("enabled"), c.Source("enabled"))
	fmt.Fprintf(w, "default_sinks = %s\n", c.Get("sinks_default"))
	for _, t := range notify.Types {
		if s := c.SinksOf(t); s != c.Get("sinks_default") && t != "digest" {
			fmt.Fprintf(w, "  sinks[%s]: %s\n", t, s)
		}
	}
	fmt.Fprintf(w, "slack_via: %s\n", c.Get("slack_via"))
	fmt.Fprintf(w, "slack_target: %s(値は表示しません)\n", setOrUnset(c.Get("slack_target"), "設定あり", "未設定"))
	fmt.Fprintf(w, "notifier_agent: %s\n", c.Get("notifier_agent"))
	fmt.Fprintf(w, "webhook_file: %s\n", n.webhookState())
	fmt.Fprintf(w, "rate: %s/min, %s/hour(超えた分は 1 通にまとめる)  max_len: %s  dedupe_sec: %s\n", c.Get("rate_per_min"), c.Get("rate_per_hour"), c.Get("max_len"), c.Get("dedupe_sec"))
	fmt.Fprintf(w, "hash_check: %s  ack_timeout_min: %s  ack_retries: %s\n", c.Get("hash_check"), c.Get("ack_timeout_min"), c.Get("ack_retries"))
	fmt.Fprintf(w, "mask: email=%s ip=%s repo=%s account=%s host=%s  strip_raw_logs: %s\n", c.Get("mask_email"), c.Get("mask_ip"), c.Get("mask_repo"), c.Get("mask_account"), c.Get("mask_host"), c.Get("strip_raw_logs"))
	fmt.Fprintln(w, "secret filter: 常に有効(秘密らしい値を含むイベントは外へ出さず、file にだけ伏せて残す)")
	fmt.Fprintf(w, "events: %s\n", or(cs, "なし"))
	return nil
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (n *notifyCmd) dryRun(args []string) error {
	if len(args) != 0 {
		return usage("notify dry-run(引数なし)")
	}
	c, w := n.q.C, n.w
	if !c.On() {
		fmt.Fprintln(w, "(enabled=off: 今は何も積まれず、何も送られません。下は、積まれているものを送るとしたときの内容)")
	}
	budget := n.q.Budget()
	fmt.Fprintf(w, "# dry-run: 何も送りません。経路 slack_via=%s / 宛先 %s / 今回の枠 %d 通\n", c.Get("slack_via"), setOrUnset(c.Get("slack_target"), "設定あり", "未設定"), budget)
	cnt := 0
	err := store.Each(n.db.DB, "SELECT id, hash, text FROM notify_events WHERE slack_status='pending' AND blocked=0 ORDER BY id", nil, func(_ []string, v []any) error {
		cnt++
		fmt.Fprintf(w, "would send N-%s [%s] %s\n", tickets.Text(v[0]), tickets.Text(v[1]), tabRest(tickets.Text(v[2])))
		return nil
	})
	if err != nil {
		return err
	}
	if cnt > budget {
		fmt.Fprintf(w, "# 枠を超える分は 1 通にまとめます(個別に送るのは最大 %d 件)\n", max(budget-1, 0))
	}
	err = store.Each(n.db.DB, "SELECT id, text FROM notify_events WHERE blocked=1 ORDER BY id DESC LIMIT 20", nil, func(_ []string, v []any) error {
		fmt.Fprintf(w, "blocked N-%s(外へは送りません) %s\n", tickets.Text(v[0]), tabRest(tickets.Text(v[1])))
		return nil
	})
	if err != nil {
		return err
	}
	if cnt == 0 {
		fmt.Fprintln(w, "(送るものはありません)")
	}
	return nil
}

// tabRest は IFS=$'\t' read の最後の変数と同じく、前後のタブを落とす(本文はタブを含まないので念のため)。
func tabRest(s string) string { return strings.Trim(s, "\t") }

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
	where, qa := "1=1", []any{}
	if st != "" {
		if !reStatusW.MatchString(st) {
			return usage("events: --status が不正")
		}
		where, qa = "slack_status=?", []any{st}
	}
	if js {
		return store.Each(n.db.DB, "SELECT * FROM notify_events WHERE ("+where+") ORDER BY id DESC LIMIT "+lim, qa, func(cols []string, vals []any) error {
			for i, c := range cols {
				if c == "id" {
					vals[i] = "N-" + tickets.Text(vals[i])
				}
			}
			s, err := rowJSON(cols, vals)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(n.w, s)
			return err
		})
	}
	return store.Each(n.db.DB, "SELECT 'N-'||id, type, slack_status, CASE WHEN blocked=1 THEN 'blocked' ELSE '-' END, created_at, text FROM notify_events WHERE ("+where+") ORDER BY id DESC LIMIT "+lim, qa, func(cols []string, vals []any) error {
		return emitRow(n.w, false, cols, vals, "-")
	})
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
	c := n.q.C
	if os.Getenv("HERDR_PANE_ID") != "" {
		l, err := label(n.db, n.stderr)
		if err != nil {
			return err
		}
		if l.Name != c.Get("notifier_agent") {
			return die("ack は notifier agent(%s)か人だけが使えます(%s)", c.Get("notifier_agent"), l.Name)
		}
	}
	id, perr := strconv.ParseInt(idS, 10, 64)
	var stt, exp string
	if perr != nil || n.db.QueryRow("SELECT slack_status, hash FROM notify_events WHERE id=?", id).Scan(&stt, &exp) != nil {
		return die("イベントが見つかりません: N-%s", idS)
	}
	switch stt {
	case "awaiting-ack", "failed", "acked", "mismatch":
	default:
		return die("N-%s は ack を待っていません(%s)", idS, stt)
	}
	if c.Flag("hash_check") && hash != exp {
		_, _ = n.db.Exec("UPDATE main.notify_events SET slack_status='mismatch', note='投稿された本文の hash が一致しない' WHERE herdr_session=? AND id=?", n.db.Session, id)
		n.q.Emit(notify.Event{Type: "warning", Ref: "N-" + idS + "-mismatch", A: "N-" + idS + " の投稿内容が送った本文と一致しません。Slack 側を確かめてください"})
		fmt.Fprintf(n.stderr, "N-%s mismatch\n", idS)
		return &exit{exitErr, ""}
	}
	if _, err := n.db.Exec("UPDATE main.notify_events SET slack_status='acked', slack_msg_id=NULLIF(?,''), note=NULL WHERE herdr_session=? AND id=?", mid, n.db.Session, id); err != nil {
		return err
	}
	fmt.Fprintf(n.w, "N-%s acked\n", idS)
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
	n.q.Emit(notify.Event{Type: typ, Ref: ref, Sev: f["--severity"], A: f["--title"]})
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
		id, _ := strconv.ParseInt(m[1], 10, 64)
		var one int
		if n.db.QueryRow("SELECT 1 FROM main.task_events WHERE herdr_session=? AND id=?", n.db.Session, id).Scan(&one) != nil {
			return die("イベントが見つかりません: E-%s", m[1])
		}
		hits, err := n.q.Apply(n.q.LoadRules(), id-1, id, true)
		if err != nil {
			return err
		}
		if len(hits) == 0 {
			fmt.Fprintf(w, "E-%s: どの規則にも当たりません(送りません)\n", m[1])
		} else {
			fmt.Fprintf(w, "E-%s: 届け先 %s(当たった行: %s)。試すだけで、何も積みません\n", m[1], hits[0].Sinks, hits[0].Lines)
		}
		return nil
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

// notifyCapJSON は上限の設定を JSON の値にする(上限なしは null)。
func notifyCapJSON(c *notify.Cfg, k string) string {
	if capv.IsUnlimited(c.Get(k)) {
		return "null"
	}
	return strconv.Itoa(c.Int(k))
}
