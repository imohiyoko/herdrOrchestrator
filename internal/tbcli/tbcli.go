// Package tbcli は tb のコマンドの Go 版(ADR 0002 の手順 3)。bin/tb が TB_IMPL=go のとき、移し終えたコマンドをここへ渡す。
// 出力・終了コード・エラーの文面は bash の tb と同じにする(taskboard/test の契約テストを両方で流す)。
package tbcli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// Commands は Go に移し終えたコマンド。bin/tb はこの一覧(herdrctl tb --commands)を見て渡すかを決める。
var Commands = []string{"activity", "allow", "approval", "approve", "ask", "config", "flush", "handoff", "inbox", "inbox-read", "job", "list", "message", "notify", "outbox", "proposal", "proposal-resolve", "proposals", "proxy", "proxy-check", "proxy-list", "proxy-request", "proxy-resolve", "reject", "sched-job", "send", "touch"}

// 終了コード(tb と同じ)
const (
	exitErr   = 1
	exitUsage = 2
)

type exit struct {
	code int
	msg  string
}

func (e *exit) Error() string { return e.msg }

func usage(format string, a ...any) error {
	return &exit{exitUsage, fmt.Sprintf(format, a...)}
}
func die(format string, a ...any) error { return &exit{exitErr, fmt.Sprintf(format, a...)} }

// Run は tb <cmd> <args...> を実行し、終了コードを返す。
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "tb: コマンドがありません")
		return exitUsage
	}
	if args[0] == "--commands" {
		fmt.Fprintln(stdout, strings.Join(Commands, " "))
		return 0
	}
	if args[0] != "touch" {
		// 権限段階の入口(ADR 0006。bash の main → perm_gate と同じ)
		if err := permErr(perms.Gate(args, permDB)); err != nil {
			return finish(err, stderr)
		}
	}
	w := bufio.NewWriter(stdout)
	var err error
	switch args[0] {
	case "touch":
		// hook 用。常に 0 で、標準出力・標準エラーには何も出さない(失敗は touch-errors.log に理由コードだけ)
		cmdTouch(args[1:])
		return 0
	case "activity":
		err = cmdActivity(args[1:], w, stderr)
	case "config":
		err = cmdConfig(args[1:], w, stderr)
	case "handoff":
		err = cmdHandoff(args[1:], w, stderr)
	case "job":
		err = cmdJob(args[1:], w, stderr, false)
	case "sched-job":
		err = cmdJob(args[1:], w, stderr, true)
	case "proxy":
		err = cmdProxy(args[1:], w, stderr)
	case "proxy-request":
		err = cmdProxyRequest(args[1:], w, stderr)
	case "proxy-list":
		err = cmdProxyList(args[1:], w, stderr)
	case "proxy-resolve":
		err = cmdProxyResolve(args[1:], w, stderr)
	case "proxy-check":
		err = cmdProxyCheck(args[1:], w, stderr)
	case "notify":
		err = cmdNotify(args[1:], w, stderr)
	case "proposal":
		err = cmdProposal(args[1:], w, stderr, "")
	case "proposals", "proposal-resolve":
		err = cmdProposal(args[1:], w, stderr, args[0])
	case "approval":
		err = cmdApproval(args[1:], w, stderr, "")
	case "allow", "ask", "approve", "reject":
		err = cmdApproval(args[1:], w, stderr, args[0])
	case "message":
		err = cmdMessage(args[1:], w, stderr, "")
	case "send", "flush", "outbox", "inbox", "inbox-read":
		err = cmdMessage(args[1:], w, stderr, args[0])
	case "list":
		err = cmdList(args[1:], w, stderr)
	default:
		err = usage("Go 版には無いコマンド: %s", args[0])
	}
	w.Flush()
	return finish(err, stderr)
}

// finish は err を tb の終了コードと標準エラーの文面にする。
func finish(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var e *exit
	if !errors.As(err, &e) {
		e = &exit{exitErr, err.Error()}
	}
	if e.msg == "" { // 使い方を標準出力に出して 2 で終わるとき(tb <サービス> だけ)
		return e.code
	}
	fmt.Fprintf(stderr, "tb: %s\n", e.msg)
	if e.code == exitUsage {
		fmt.Fprintln(stderr, "(tb help で使い方)")
	}
	return e.code
}

func openDB() (*store.DB, error) {
	var opt *store.Options
	if d := os.Getenv("TB_SCHEMA_DIR"); d != "" {
		opt = &store.Options{Schema: os.DirFS(d)}
	}
	path := env.TBDB()
	db, err := store.Open(path, env.SessionName(), opt)
	if err != nil {
		return nil, die("DB を開けないか、スキーマの移行に失敗しました: %s", path)
	}
	return db, nil
}

var (
	reID     = regexp.MustCompile(`^T-[A-Za-z0-9_]+-[0-9]{3,}$`)
	reSprint = regexp.MustCompile(`^S-[0-9]+$`)
	reDue    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	reDays   = regexp.MustCompile(`^[0-9]{1,4}$`)
)

func oneOf(list []string, v, what string) error {
	if slices.Contains(list, v) {
		return nil
	}
	return usage("%s は %s のどれか: %s", what, strings.Join(list, " "), v)
}

func validDue(s string) bool {
	if !reDue.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// today は tb の today(TB_TODAY で固定できる)。
func today() (string, error) {
	if t := os.Getenv("TB_TODAY"); t != "" {
		if !validDue(t) {
			return "", usage("期日は YYYY-MM-DD の日付: %s", t)
		}
		return t, nil
	}
	return time.Now().Format("2006-01-02"), nil
}

// labels は tasks.sh の labels_json と同じ: カンマで分け、前後の空白を落とし、検査する。
func labels(arg string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(arg, ",") {
		p = strings.TrimFunc(p, unicode.IsSpace)
		if p == "" {
			continue
		}
		if n := utf8.RuneCountInString(p); n < 1 || n > 40 || strings.ContainsFunc(p, func(r rune) bool {
			return unicode.IsSpace(r) || r == ',' || r == '"' || r == '\''
		}) {
			return nil, usage("ラベルは 1〜40 文字で、空白・カンマ・引用符を含まない: %s", p)
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// openCmd は bin/tb の main がコマンドの前にすること(init_db と statuses_load)をする。
// 設定 statuses が壊れていれば、どのコマンドでも bash と同じ警告を標準エラーに出す。
func openCmd(stderr io.Writer) (*store.DB, tickets.StatusSet, error) {
	db, err := openDB()
	if err != nil {
		return nil, tickets.StatusSet{}, err
	}
	st := tickets.LoadStatuses(db.DB, db.Session)
	if st.Broken {
		fmt.Fprintln(stderr, "tb: 設定 statuses に不正な行があるので組み込みの状態を使います(tb config edit statuses で直す)")
	}
	return db, st, nil
}

func cmdList(args []string, w, stderr io.Writer) error {
	// tb と同じく、引数を読む前に DB を開く(版上げも先に済む)
	db, st, err := openCmd(stderr) // 設定 statuses(閉じた状態と、--status に使える状態)
	if err != nil {
		return err
	}
	defer db.Close()
	closed := st.SQLList("done", "cancelled")
	var f tickets.Filter
	project, status, sort := "", "", ""
	long, js := false, false
	need := func(i int) (string, error) {
		if i+1 >= len(args) {
			return "", usage("%s に値が必要です", args[i])
		}
		return args[i+1], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-p", "--project", "-s", "--status", "--type", "--priority", "-l", "--label", "--owner", "--parent", "--sprint", "--due-within", "--sort":
			v, err := need(i)
			if err != nil {
				return err
			}
			i++
			switch a {
			case "-p", "--project":
				project = v
			case "-s", "--status":
				status = v
			case "--type":
				if err := oneOf(tickets.Types, v, "種別"); err != nil {
					return err
				}
				f.Add("type=?", v)
			case "--priority":
				if err := oneOf(tickets.Priorities, v, "優先度"); err != nil {
					return err
				}
				f.Add("priority=?", v)
			case "-l", "--label":
				ls, err := labels(v)
				if err != nil {
					return err
				}
				for _, l := range ls {
					f.Add("EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)", l)
				}
			case "--owner":
				f.Add("owner_agent=?", v)
			case "--parent":
				if !reID.MatchString(v) {
					return usage("タスク ID は T-<project>-NNN 形式: %s", v)
				}
				f.Add("parent_id=?", v)
			case "--sprint":
				if v == "backlog" {
					f.Add("sprint_id IS NULL")
				} else {
					if !reSprint.MatchString(v) {
						return usage("スプリント ID は S-<番号> 形式: %s", v)
					}
					f.Add("sprint_id=?", strings.TrimPrefix(v, "S-"))
				}
			case "--due-within":
				if !reDays.MatchString(v) {
					return usage("list: --due-within は 0〜9999 の日数: %s", v)
				}
				td, err := today()
				if err != nil {
					return err
				}
				n := strings.TrimLeft(v, "0")
				if n == "" {
					n = "0"
				}
				f.Add("due IS NOT NULL AND due >= ? AND due <= date(?, '+"+n+" days') AND status NOT IN ("+closed+")", td, td)
			case "--sort":
				sort = v
			}
		case "--overdue":
			td, err := today()
			if err != nil {
				return err
			}
			f.Add("due IS NOT NULL AND due < ? AND status NOT IN ("+closed+")", td)
		case "--has-due":
			f.Add("due IS NOT NULL")
		case "--long":
			long = true
		case "--json":
			js = true
		default:
			return usage("list: 不明な引数 %s", a)
		}
	}
	if project != "" {
		f.Add("project=?", project)
	}
	if status != "" {
		if err := oneOf(st.Names, status, "status"); err != nil {
			return err
		}
		f.Add("status=?", status)
	}
	order, ok := tickets.Order(sort)
	if !ok {
		return usage("list: --sort は priority / updated / created / id / due のどれか: %s", sort)
	}
	// --json と --long を両方付けたら、tb と同じく --json が勝つ
	format := tickets.Short
	if js {
		format = tickets.JSON
	} else if long {
		format = tickets.Long
	}
	return tickets.List(db.DB, &f, order, format, func(cols []string, vals []any) error {
		if format == tickets.JSON {
			s, err := rowJSON(cols, vals)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(w, s)
			return err
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = tickets.Text(v)
		}
		_, err := fmt.Fprintln(w, strings.Join(parts, "\t"))
		return err
	})
}
