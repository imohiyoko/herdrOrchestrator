package tbcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/proxy"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var reNum = regexp.MustCompile(`^[0-9]+$`)

// now は core.sh の now(UTC、秒まで)。
func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func validPID(s string) error {
	if !reNum.MatchString(s) {
		return usage("代行依頼の ID は数字: %s", s)
	}
	return nil
}

// allowlist は proxy.sh の ALLOWLIST: TB_ALLOWLIST > 設定の置き場(bin/tb が渡す TB_CONFIG_DIR)の proxy-allowlist.txt。
func allowlist() string {
	if p := os.Getenv("TB_ALLOWLIST"); p != "" {
		return p
	}
	if d := os.Getenv("TB_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "proxy-allowlist.txt")
	}
	return "proxy-allowlist.txt"
}

// flags は「--名前 値」だけの引数を読む(bash の need_arg と同じ文面)。bools は値を取らない名前。
func flags(cmd string, args []string, names, bools []string) (map[string]string, error) {
	m := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case slices.Contains(bools, a):
			m[a] = "1"
		case slices.Contains(names, a):
			if i+1 >= len(args) {
				return nil, usage("%s に値が必要です", a)
			}
			i++
			m[a] = args[i]
		default:
			return nil, usage("%s: 不明な引数 %s", cmd, a)
		}
	}
	return m, nil
}

func cmdProxyRequest(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("proxy-request <task-id> --cmd C --why W --effect read-only|write|external [--expect E]")
	}
	tid := args[0]
	if !reID.MatchString(tid) {
		return usage("タスク ID は T-<project>-NNN 形式: %s", tid)
	}
	f, err := flags("proxy-request", args[1:], []string{"--cmd", "--why", "--effect", "--expect"}, nil)
	if err != nil {
		return err
	}
	c, why, effect := proxy.Trim(f["--cmd"]), f["--why"], f["--effect"]
	switch {
	case proxy.Blank(c):
		return usage("proxy-request: --cmd が必要です")
	case proxy.Blank(why):
		return usage("proxy-request: --why が必要です")
	case effect == "":
		return usage("proxy-request: --effect が必須です(read-only|write|external。迷ったら write)")
	case !slices.Contains(proxy.Effects, effect):
		return usage("proxy-request: --effect は %s のどれか: %s", strings.Join(proxy.Effects, " "), effect)
	}
	if ok, err := tickets.Exists(db.DB, tid); err != nil {
		return err
	} else if !ok {
		return die("タスクが見つかりません: %s", tid)
	}
	id, status, result, err := proxy.Request(db.DB, db.Session, tid, c, why, effect, f["--expect"], now())
	if err != nil {
		return err
	}
	if status == "denied" {
		fmt.Fprintf(stderr, "tb: %s\n", result)
	}
	fmt.Fprintf(w, "%d %s\n", id, status)
	return nil
}

func cmdProxyList(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	f, err := flags("proxy-list", args, []string{"--status"}, []string{"--json"})
	if err != nil {
		return err
	}
	status := f["--status"]
	if status != "" && !slices.Contains(proxy.Statuses, status) {
		return usage("proxy-list: status は open|done|pending-human|denied: %s", status)
	}
	js := f["--json"] != ""
	return proxy.List(db.DB, status, js, func(cols []string, vals []any) error {
		return emitRow(w, js, cols, vals, "")
	})
}

func cmdProxyResolve(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("proxy-resolve <id> --status done|pending-human|denied [--result <本文かファイルパス>]")
	}
	id := args[0]
	if err := validPID(id); err != nil {
		return err
	}
	f, err := flags("proxy-resolve", args[1:], []string{"--status", "--result"}, nil)
	if err != nil {
		return err
	}
	status := f["--status"]
	if status != "done" && status != "pending-human" && status != "denied" {
		return usage("proxy-resolve: --status は done|pending-human|denied のどれか")
	}
	cur, err := proxy.Resolve(db.DB, db.Session, id, status, f["--result"], now())
	switch {
	case errors.Is(err, proxy.ErrNotFound):
		return die("代行依頼が見つかりません: %s", id)
	case errors.Is(err, proxy.ErrFinal):
		return die("解決済み(%s)の依頼は変更できません: %s", cur, id)
	case err != nil:
		return err
	}
	fmt.Fprintf(w, "%s %s\n", id, status)
	return nil
}

func cmdProxyCheck(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) != 1 {
		return usage("proxy-check <id>")
	}
	if err := validPID(args[0]); err != nil {
		return err
	}
	r, err := proxy.Get(db.DB, args[0])
	if errors.Is(err, proxy.ErrNotFound) {
		return die("代行依頼が見つかりません: %s", args[0])
	} else if err != nil {
		return err
	}
	for _, l := range proxy.Check(r, allowlist()) {
		fmt.Fprintln(w, l)
	}
	return nil
}

// emitRow は rowfmt.EmitRow(--json なら jq -c の形、でなければ sqlite3 の -separator \t の形。NULL は null の文字)。
func emitRow(w io.Writer, js bool, cols []string, vals []any, null string) error {
	return rowfmt.EmitRow(w, js, cols, vals, null)
}

const proxyUsage = `tb proxy request <task-id> --cmd C --why W --effect read-only|write|external [--expect E]   → "<id> <status>"
tb proxy list [--status open|done|pending-human|denied] [--json]
tb proxy resolve <id> --status done|pending-human|denied [--result <本文かファイルパス>]
tb proxy check <id>   1 行目が allow(自動代行可)か human(人に回す)。mismatch: があれば申告の食い違い
(古い名前 tb proxy-request / proxy-list / proxy-resolve / proxy-check も同じに使える)
`

// cmdProxy は tb proxy <サブコマンド>(proxy.sh の cmd_proxy)。
func cmdProxy(args []string, w, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "request":
		return cmdProxyRequest(args, w, stderr)
	case "list":
		return cmdProxyList(args, w, stderr)
	case "resolve":
		return cmdProxyResolve(args, w, stderr)
	case "check":
		return cmdProxyCheck(args, w, stderr)
	case "", "-h", "--help", "help":
		db, _, err := openCmd(stderr) // bash と同じく、使い方を出すときも先に DB を開く
		if err != nil {
			return err
		}
		db.Close()
		fmt.Fprint(w, proxyUsage)
		if sub == "" {
			return &exit{exitUsage, ""}
		}
		return nil
	}
	return usage("proxy: 不明なサブコマンド %s(request / list / resolve / check)", sub)
}
