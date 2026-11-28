package tbcli

// チケットの報告コメントと完了報告(taskboard/lib/comments.sh の comment / comments / resolve / reopen と、
// tasks.sh の report / pending)。

import (
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"

	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	reDigits = regexp.MustCompile(`^[0-9]+$`)
	reCRef   = regexp.MustCompile(`^C-[0-9]+$`)
)

func joinDeliverables(s string) (string, bool) { return tickets.JoinDeliverables(s) }

// deliverComment は deliver_comment: 報告コメントを指揮役へ届ける。戻り値は 0 / 10 / 11(transport.Send と同じ)。
func deliverComment(db *store.DB, w, stderr io.Writer, ref string) error {
	if !reDigits.MatchString(ref) {
		return usage("コメント ID は C-<数字>")
	}
	d, err := message.DeliverComment(db, ref, now(), stderr)
	var nf *message.ErrCommentNotFound
	if errors.As(err, &nf) {
		return die("%s", nf.Error())
	}
	if err != nil {
		return err
	}
	if d.Already != "" {
		fmt.Fprintf(w, "C-%s は配送済みです(%s)\n", ref, d.Already)
		return nil
	}
	if d.RC != 0 {
		return &exit{d.RC, ""}
	}
	fmt.Fprintf(w, "C-%s delivered to %s\n", ref, d.Target)
	return nil
}

// commentRows は tb comments --json の行(ticket.comments を呼ぶ)。
func commentRows(db *store.DB, w io.Writer, in ticket.CommentsReq) error {
	in.JSON = true
	return readText(db, "tb comments", "ticket.comments", in, w)
}

// cmdComment は tb comment <task-id> --kind K --text T [--deliverables a,b] [--json]。
// 終了コード: 0 配送済み / 10 宛先使用中で未配送 / 11 送信失敗 / 3 報告者を特定できず未保存 / 2 使い方 / 1 その他。
func cmdComment(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("comment <task-id> --kind done|progress|question|blocked --text T [--deliverables a,b] [--json]")
	}
	tid := args[0]
	if err := needID(tid); err != nil {
		return err
	}
	var kind, text, dels string
	js := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		var p *string
		switch {
		case a == "--kind":
			p = &kind
		case a == "--text":
			p = &text
		case a == "--deliverables":
			p = &dels
		case a == "--json":
			js = true
			continue
		case a == "--from" || strings.HasPrefix(a, "--from-") || a == "--name" || a == "--pane" || a == "--session":
			return usage("comment: 報告者は herdr から自動で決まります。%s は指定できません", a)
		default:
			return usage("comment: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		*p = v
		i++
	}
	sp := tickets.CommentSpec{TaskID: tid, Kind: kind, Text: text, Deliverables: dels}
	if _, err := tickets.CheckComment(db, sp); err != nil {
		return ticketErr(err)
	}
	l, err := label(db, stderr)
	if err != nil {
		return err
	}
	env := appEnv(db, stderr)
	env.Reporter = func(string) (string, string, string) { return l.Name, l.Pane, l.Session }
	if v, ok := os.LookupEnv("TB_IMMEDIATE_KINDS"); ok {
		env.ImmediateKinds = v
	}
	by, err := callerPrincipal("tb comment")
	if err != nil {
		return err
	}
	var res ticket.CommentResult
	if err := callAppEnv(env, by, "ticket.comment", ticket.CommentReq{TaskID: tid, Kind: kind, Text: text, Deliverables: dels}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	if js {
		if err := commentRows(db, w, ticket.CommentsReq{ID: res.ID}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(w, "C-%d %s\n", res.ID, res.State)
	}
	if res.RC != 0 {
		return &exit{res.RC, ""}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// cmdComments は tb comments [<task-id>] [--kind K] [--undelivered] [--open] [--json]。
func cmdComments(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.CommentsReq
	if len(args) > 0 && strings.HasPrefix(args[0], "T-") {
		if err := needID(args[0]); err != nil {
			return err
		}
		in.Task = args[0]
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			i++
			in.Kind = v // bash と同じく最後に指定した値を見る。空なら絞らない。値は API が確かめる
		case "--undelivered":
			in.Undelivered = true
		case "--open":
			in.Open = true
		case "--json":
			in.JSON = true
		default:
			return usage("comments: 不明な引数 %s", args[i])
		}
	}
	return readText(db, "tb comments", "ticket.comments", in, w)
}

// commentRefArgs は resolve / reopen の <C-n> [-m <メモ>]。
func commentRefArgs(name, what string, args []string) (ref, note string, err error) {
	if len(args) < 1 {
		return "", "", usage("%s <C-n> [-m <%s>]", name, what)
	}
	ref = strings.TrimPrefix(args[0], "C-")
	if !reDigits.MatchString(ref) {
		return "", "", usage("%s: ID は C-<数字>: %s", name, ref)
	}
	for i := 1; i < len(args); i++ {
		if args[i] != "-m" && args[i] != "--message" {
			return "", "", usage("%s: 不明な引数 %s", name, args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return "", "", err
		}
		note = v
		i++
	}
	if utf8.RuneCountInString(note) > 500 {
		return "", "", usage("%s: %sは 500 字まで", name, what)
	}
	return ref, note, nil
}

// cmdResolve は tb resolve C-<n> [-m メモ]: 人が question / blocked の報告を対応済みにする。
// 中身はアプリケーション API の ticket.resolve(ADR 0008)。ここは引数を読み、呼び出し元を主体にして呼び、結果を今の文言にする。
func cmdResolve(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	ref, note, err := commentRefArgs("resolve", "メモ", args)
	if err != nil {
		return err
	}
	var res ticket.ResolveResult
	if err := callApp(db, "tb resolve", "ticket.resolve", ticket.ResolveReq{ID: ref, Note: note}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	if res.Already {
		fmt.Fprintf(w, "C-%s はすでに対応済みです(%s)\n", ref, res.ResolvedAt)
		return nil
	}
	fmt.Fprintf(w, "C-%s resolved\n", ref)
	return nil
}

// cmdReopen は tb reopen C-<n> [-m 理由]: tb resolve の逆。中身は ticket.reopen。戻したことは task_events の comment_reopened に残す。
func cmdReopen(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	ref, note, err := commentRefArgs("reopen", "理由", args)
	if err != nil {
		return err
	}
	var res ticket.ReopenResult
	if err := callApp(db, "tb reopen", "ticket.reopen", ticket.ReopenReq{ID: ref, Note: note}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	if res.NotResolved {
		fmt.Fprintf(w, "C-%s は対応済みではありません(未対応のまま)\n", ref)
		return nil
	}
	fmt.Fprintf(w, "C-%s reopened\n", ref)
	return nil
}

// cmdReport は tb report <id> | report C-<n>: 完了の報告を指揮役へ届ける(C-<n> は未配送コメントの再送)。
func cmdReport(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) != 1 {
		return usage("report <id>")
	}
	var r ticket.ReportResult
	if err := callAppNoHerdr(db, "tb report", stderr, "ticket.report", ticket.ReportReq{Ref: args[0]}, &r); err != nil {
		return err
	}
	switch {
	case r.Reported != "":
		fmt.Fprintf(w, "%s は報告済みです(%s)\n", r.ID, r.Reported)
	case r.Code != 0:
		return &exit{r.Code, ""}
	case r.ID == "" && r.Already != "": // C-<n> の再送
		fmt.Fprintf(w, "%s は配送済みです(%s)\n", r.Comment, r.Already)
	case r.ID == "":
		fmt.Fprintf(w, "%s delivered to %s\n", r.Comment, r.Target)
	default:
		fmt.Fprintf(w, "%s reported to %s (%s)\n", r.ID, r.Target, r.Comment)
	}
	return nil
}

// cmdPending は tb pending [--json | --ids]: 未報告の完了と未配送のコメント。
func cmdPending(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.PendingReq
	for _, a := range args {
		switch a {
		case "--json":
			in.Mode = "json"
		case "--ids":
			in.Mode = "ids"
		default:
			return usage("pending: 不明な引数 %s", a)
		}
	}
	return readText(db, "tb pending", "ticket.pending", in, w)
}
