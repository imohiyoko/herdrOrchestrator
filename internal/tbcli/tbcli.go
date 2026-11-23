// Package tbcli は tb のコマンドの Go 版(ADR 0002 の手順 3)。bin/tb が TB_IMPL=go のとき、移し終えたコマンドをここへ渡す。
// 出力・終了コード・エラーの文面は bash の tb と同じにする(taskboard/test の契約テストを両方で流す)。
package tbcli

import (
	"database/sql"
	"sort"

	"bufio"
	"errors"
	"fmt"
	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// Commands は Go に移し終えたコマンド。bin/tb はこの一覧(herdrctl tb --commands)を見て渡すかを決める。
var Commands = []string{"activity", "add", "agent-flag", "agent-kinds", "agent-ops", "agent-stop", "allow", "approval", "approve", "ask", "block", "board", "can", "cancel", "claim", "comment", "comments", "compact-check", "config", "conv", "conv-candidate-add", "conv-candidate-adopt", "conv-candidate-reject", "conv-draft", "conv-gate", "conv-incident", "conv-kinds", "conv-learn-purge", "conv-ledger", "conv-length", "conv-lint", "conv-pair-add", "conv-pair-show", "conv-pairs", "conv-patterns", "conv-route", "conv-routes-set", "conv-scan", "conv-stage", "conv-stage-log", "conv-stage-reset", "conv-stage-set", "conv-voice", "conv-voice-check", "deadlines-import", "done", "due-scan", "events", "flush", "gate", "handoff", "history", "inbox", "inbox-read", "job", "labels", "limits", "link", "list", "message", "notify", "nudge", "org", "outbox", "part-check", "part-risk", "parts", "peek", "pending", "pr", "pr-queue", "pr-watch", "proposal", "proposal-resolve", "proposals", "proxy", "proxy-check", "proxy-list", "proxy-request", "proxy-resolve", "recipe-history", "recipe-list", "recipe-rm", "recipe-set", "recipe-show", "reject", "reminder", "reminders", "reopen", "report", "resolve", "review", "review-pack", "review-submit", "role-broadcast", "role-context", "role-edit", "role-list", "role-log", "role-set", "role-show", "role-unset", "role-write", "roles", "sched-job", "send", "show", "slash", "spawn-freeze", "spawn-unfreeze", "sprint-add", "sprint-close", "sprint-start", "sprints", "ticket", "touch", "tree", "unlink", "update"}

// Features は bin/tb が渡す前に確かめる機能(perms = 権限段階の門。ADR 0006)。
var Features = []string{"perms", "policy"}

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
	if args[0] == "--features" {
		// bin/tb が渡す前に確かめる(古い herdrctl に渡すと権限段階の門を通らない。ADR 0006)
		fmt.Fprintln(stdout, strings.Join(Features, " "))
		return 0
	}
	if args[0] == "--commands" {
		svc := serviceCommands()
		sort.Strings(svc)
		fmt.Fprintln(stdout, strings.Join(append(append([]string{}, Commands...), svc...), " "))
		return 0
	}
	perms.OpenDB = permDB
	perms.NameMatch = func(db *sql.DB, session, name, pat string) int { return agents.NewRoles(db, session).Match(name, pat) }
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
	case "show":
		err = cmdShow(args[1:], w, stderr)
	case "tree":
		err = cmdTree(args[1:], w, stderr)
	case "labels":
		err = cmdLabels(args[1:], w, stderr)
	case "sprints":
		err = cmdSprints(args[1:], w, stderr)
	case "board":
		err = cmdBoard(args[1:], w, stderr)
	case "history":
		err = cmdHistory(args[1:], w, stderr)
	case "events":
		err = cmdEvents(args[1:], w, stderr)
	case "add":
		err = cmdAdd(args[1:], w, stderr)
	case "update":
		err = cmdUpdate(args[1:], w, stderr)
	case "comment":
		err = cmdComment(args[1:], w, stderr)
	case "comments":
		err = cmdComments(args[1:], w, stderr)
	case "resolve":
		err = cmdResolve(args[1:], w, stderr)
	case "reopen":
		err = cmdReopen(args[1:], w, stderr)
	case "report":
		err = cmdReport(args[1:], w, stderr)
	case "pending":
		err = cmdPending(args[1:], w, stderr)
	case "ticket":
		err = cmdTicket(args[1:], w, stderr)
	case "limits":
		err = cmdLimits(args[1:], w, stderr)
	case "can":
		err = cmdCan(args[1:], w, stderr)
	case "org":
		err = cmdOrg(args[1:], w, stderr)
	case "agent-kinds":
		err = cmdAgentKinds(args[1:], w, stderr)
	case "agent-ops":
		err = cmdAgentOps(args[1:], w, stderr)
	case "agent-stop":
		err = cmdAgentStop(args[1:], w, stderr)
	case "agent-flag":
		err = cmdAgentFlag(args[1:], w, stderr)
	case "spawn-freeze":
		err = cmdSpawnFreeze(args[1:], w, stderr)
	case "spawn-unfreeze":
		err = cmdSpawnUnfreeze(args[1:], w, stderr)
	case "recipe-set":
		err = cmdRecipeSet(args[1:], w, stderr)
	case "recipe-rm":
		err = cmdRecipeRm(args[1:], w, stderr)
	case "recipe-list":
		err = cmdRecipeList(args[1:], w, stderr)
	case "recipe-show":
		err = cmdRecipeShow(args[1:], w, stderr)
	case "recipe-history":
		err = cmdRecipeHistory(args[1:], w, stderr)
	case "parts":
		err = cmdParts(args[1:], w, stderr)
	case "part-check":
		err = cmdPartCheck(args[1:], w, stderr)
	case "part-risk":
		err = cmdPartRisk(args[1:], w, stderr)
	case "agent":
		err = cmdAgent(args[1:], w, stderr)
	case "recipe":
		err = cmdRecipe(args[1:], w, stderr)
	case "slash":
		err = cmdSlash(args[1:], w, stderr)
	case "compact-check":
		err = cmdCompactCheck(args[1:], w, stderr)
	case "peek":
		err = cmdPeek(args[1:], w, stderr)
	case "role-context":
		err = cmdRoleContext(args[1:], w, stderr)
	case "role-broadcast":
		err = cmdRoleBroadcast(args[1:], w, stderr)
	case "role-show":
		err = cmdRoleShow(args[1:], w, stderr)
	case "role-list":
		err = cmdRoleList(args[1:], w, stderr)
	case "role-write":
		err = cmdRoleWrite(args[1:], w, stderr)
	case "role-edit":
		err = cmdRoleEdit(args[1:], w, stderr)
	case "role-unset":
		err = cmdRoleUnset(args[1:], w, stderr)
	case "role-set":
		err = cmdRoleSet(args[1:], w, stderr)
	case "role-log":
		err = cmdRoleLog(args[1:], w, stderr)
	case "roles":
		err = cmdRoles(args[1:], w, stderr)
	case "deadlines-import":
		err = cmdDeadlinesImport(args[1:], w, stderr)
	case "claim":
		err = cmdClaim(args[1:], w, stderr)
	case "done":
		err = cmdDone(args[1:], w, stderr)
	case "block":
		err = cmdBlock(args[1:], w, stderr)
	case "cancel":
		err = cmdCancel(args[1:], w, stderr)
	case "link":
		err = cmdLink(args[1:], w, stderr)
	case "unlink":
		err = cmdUnlink(args[1:], w, stderr)
	case "sprint-add":
		err = cmdSprintAdd(args[1:], w, stderr)
	case "sprint-start":
		err = cmdSprintStart(args[1:], w, stderr)
	case "sprint-close":
		err = cmdSprintClose(args[1:], w, stderr)
	case "gate":
		err = cmdGate(args[1:], w, stderr, os.Stdin)
	case "review":
		err = cmdReview(args[1:], w, stderr, "")
	case "conv":
		err = cmdConv(args[1:], w, stderr, "")
	case "pr":
		err = cmdPr(args[1:], w, stderr, "")
	case "pr-queue", "pr-watch":
		err = cmdPr(args[1:], w, stderr, args[0])
	case "review-pack", "review-submit":
		err = cmdReview(args[1:], w, stderr, args[0])
	case "reminder":
		err = cmdReminder(args[1:], w, stderr, "")
	case "reminders", "due-scan", "nudge":
		err = cmdReminder(args[1:], w, stderr, args[0])
	default:
		if sub, ok := strings.CutPrefix(args[0], "conv-"); ok && convSubs[sub] != nil {
			err = cmdConv(args[1:], w, stderr, args[0])
			break
		}
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

// labels は tasks.sh の labels_json と同じ(list -l も add / update と同じ検査。labelsJSON)。
func labels(arg string) ([]string, error) { return labelsJSON([]string{arg}) }

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
