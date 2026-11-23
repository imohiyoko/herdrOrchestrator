package tbcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/proposal"
)

const proposalUsage = `tb proposal add --role <roles のファイル|-> --title T (--text X|--text-file F)   改善提案を設定役へ送る
tb proposal list [--status open|applied|rejected]
tb proposal show P-<n>
tb proposal resolve P-<n> --status applied|rejected [--note N]   設定役か人だけ
(古い名前 tb proposal --role ... / tb proposals / tb proposal-resolve も同じに使える)
`

var (
	rePNo   = regexp.MustCompile(`^P-[0-9]+$`)
	reRole  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}(\.md|\.conf)?$|^-$`)
	exitLbl = 3 // resolve_label に失敗(報告者を特定できない)
)

// blank は core.sh の blank(ASCII の空白だけなら真)。
func blank(s string) bool { return strings.TrimFunc(s, config.IsSpace) == "" }

// label は resolve_label。失敗したら理由を stderr に出して 3。
func label(db *store.DB, stderr io.Writer) (identity.Label, error) {
	l, msg, ok := identity.ResolveLabel(func(code, def string) string { return config.Msg(db.DB, db.Session, code, def) })
	if !ok {
		fmt.Fprintln(stderr, msg)
		return l, &exit{exitLbl, ""}
	}
	return l, nil
}

// pno は P-<n> の n(大きすぎる番号は無い番号として 0)。
func pno(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "P-"), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// cmdProposal は tb proposal <サブコマンド>(古い形 tb proposal --role ... も)。legacy は古い名前(proposals / proposal-resolve)。
func cmdProposal(args []string, w, stderr io.Writer, legacy string) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	switch legacy {
	case "proposals":
		return proposalsLegacy(db, args, w)
	case "proposal-resolve":
		return proposalResolve(db, args, w, stderr)
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if strings.HasPrefix(sub, "--") && sub != "--help" { // 古い形: tb proposal --role ...
		return proposalAdd(db, args, w, stderr)
	}
	if len(args) > 0 {
		args = args[1:]
	}
	switch sub {
	case "add":
		return proposalAdd(db, args, w, stderr)
	case "list":
		if len(args) > 0 && args[0] != "--status" {
			return usage("proposal list: 不明な引数 %s", args[0])
		}
		return proposalsLegacy(db, args, w)
	case "show":
		if len(args) != 1 || !rePNo.MatchString(args[0]) {
			return usage("proposal show P-<n>")
		}
		return proposalsLegacy(db, args, w)
	case "resolve":
		return proposalResolve(db, args, w, stderr)
	case "", "-h", "--help", "help":
		fmt.Fprint(w, proposalUsage)
		if sub == "" {
			return &exit{exitUsage, ""}
		}
		return nil
	}
	return usage("proposal: 不明なサブコマンド %s(add / list / show / resolve)", sub)
}

func proposalAdd(db *store.DB, args []string, w, stderr io.Writer) error {
	var role, title, text, tfile string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--role", "--title", "--text", "--text-file":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			i++
			switch a {
			case "--role":
				role = args[i]
			case "--title":
				title = args[i]
			case "--text":
				text = args[i]
			default:
				tfile = args[i]
			}
		case "--from", "--name", "--pane":
			return usage("proposal: 提案者は herdr から自動で決まります")
		default:
			return usage("proposal: 不明な引数 %s", a)
		}
	}
	if !reRole.MatchString(role) {
		return usage("proposal: --role は roles/ のファイル名(全体に関わるなら -)")
	}
	if blank(title) {
		return usage("proposal: --title が必要です")
	}
	if tfile != "" {
		b, err := readHead(tfile, 20000)
		if err != nil {
			return die("--text-file を読めません: %s", tfile)
		}
		text = b
	}
	if blank(text) {
		return usage("proposal: --text か --text-file が必要です")
	}
	l, err := label(db, stderr)
	if err != nil {
		return err
	}
	id, err := proposal.Add(db.DB, db.Session, role, title, text, l.Name, proposal.Target(db.DB, db.Session))
	if err != nil {
		return err
	}
	r := "saved-undelivered"
	if rc, err := proposal.Deliver(db.DB, db.Session, id, io.Discard); err == nil && rc == 0 { // bash は >/dev/null 2>&1
		r = "delivered"
	}
	fmt.Fprintf(w, "P-%d %s\n", id, r)
	return nil
}

// readHead は $(head -c n f): 先頭 n バイト(NUL は落ち、末尾の改行も落とす)。通常のファイルでなければ err。
func readHead(path string, n int) (string, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("not a file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(n)))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.ReplaceAll(string(b), "\x00", ""), "\n"), nil
}

// proposalsLegacy は cmd_proposals: P-<n> なら全文、--status <s> ならその状態の一覧、ほかは全部の一覧。
func proposalsLegacy(db *store.DB, args []string, w io.Writer) error {
	a1 := ""
	if len(args) > 0 {
		a1 = args[0]
	}
	if rePNo.MatchString(a1) {
		r, err := proposal.Get(db.DB, pno(a1))
		if errors.Is(err, proposal.ErrNotFound) {
			return die("提案が見つかりません: %s", a1)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(w, proposal.Show(r))
		return nil
	}
	status := ""
	if a1 == "--status" {
		if len(args) > 1 {
			status = args[1]
		}
		switch status {
		case "open", "applied", "rejected":
		default:
			return usage("proposals: --status は open|applied|rejected")
		}
	}
	return proposal.List(db.DB, status, func(cols []string, vals []any) error {
		return emitRow(w, false, cols, vals, "-")
	})
}

func proposalResolve(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) < 1 || !rePNo.MatchString(args[0]) {
		return usage("proposal-resolve P-<n> --status applied|rejected [--note N]")
	}
	idS := strings.TrimPrefix(args[0], "P-")
	f, err := flags("proposal-resolve", args[1:], []string{"--status", "--note"}, nil)
	if err != nil {
		return err
	}
	st := f["--status"]
	if st != "applied" && st != "rejected" {
		return usage("proposal-resolve: --status は applied|rejected")
	}
	if os.Getenv("HERDR_PANE_ID") != "" {
		l, err := label(db, stderr)
		if err != nil {
			return err
		}
		if pt := proposal.Target(db.DB, db.Session); pt == "" || l.Name != pt {
			return die("提案を閉じられるのは設定役(%s。projects の proposal の行)か人だけです", pt)
		}
	}
	id := pno(args[0])
	cur, err := proposal.Status(db.DB, id)
	if err != nil {
		return err
	}
	if cur != "open" {
		return die("P-%s は open ではありません", idS)
	}
	if err := proposal.Resolve(db.DB, db.Session, id, st, f["--note"]); err != nil {
		return err
	}
	fmt.Fprintf(w, "P-%s %s\n", idS, st)
	return nil
}
