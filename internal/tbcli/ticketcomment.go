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

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	commentKinds = []string{"done", "progress", "question", "blocked"}
	reDigits     = regexp.MustCompile(`^[0-9]+$`)
	reCRef       = regexp.MustCompile(`^C-[0-9]+$`)
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

// commentJSON は tb comments --json の 1 行(deliverables を JSON に、id を C-<n> に)。
func commentRows(db *store.DB, w io.Writer, where string, args ...any) error {
	rows, err := sqlObjs(db.DB, []string{"deliverables"}, "SELECT * FROM comments WHERE ("+where+") ORDER BY id", args...)
	if err != nil {
		return err
	}
	for _, r := range rows {
		r = r.Set("id", "C-"+jv.Scalar(r.Get("id")))
		fmt.Fprintln(w, jv.Compact(r))
	}
	return nil
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
		if err := commentRows(db, w, "id=?", res.ID); err != nil {
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
	where, qa := "1=1", []any{}
	if len(args) > 0 && strings.HasPrefix(args[0], "T-") {
		if err := needID(args[0]); err != nil {
			return err
		}
		where += " AND task_id=?"
		qa = append(qa, args[0])
		args = args[1:]
	}
	js, kind := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			i++
			kind = v
		case "--undelivered":
			where += " AND delivered_at IS NULL"
		case "--open":
			where += " AND kind IN ('question','blocked') AND resolved_at IS NULL" // 人の対応待ち
		case "--json":
			js = true
		default:
			return usage("comments: 不明な引数 %s", args[i])
		}
	}
	if kind != "" { // bash と同じく最後に指定した値を見る。空なら絞らない
		if !contains(commentKinds, kind) {
			return usage("comments: --kind は %s", strings.Join(commentKinds, " "))
		}
		where += " AND kind=?"
		qa = append(qa, kind)
	}
	if js {
		return commentRows(db, w, where, qa...)
	}
	// needs-human: 未対応の question/blocked(人の判断・承認待ち)。resolved: 人が対応済みにしたもの
	return sqlTSV(db.DB, w, `SELECT 'C-'||id, kind, CASE WHEN resolved_at IS NOT NULL THEN 'resolved'
		WHEN kind IN ('question','blocked') THEN 'needs-human' ELSE '-' END,
		CASE WHEN delivered_at IS NULL THEN 'undelivered' ELSE 'delivered' END, task_id, from_name,
		replace(replace(text, char(10), ' '), char(9), ' ') FROM comments WHERE (`+where+`) ORDER BY id`, qa...)
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
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	mode := "tsv"
	for _, a := range args {
		switch a {
		case "--json":
			mode = "json"
		case "--ids":
			mode = "ids"
		default:
			return usage("pending: 不明な引数 %s", a)
		}
	}
	// 未配送の done コメントがあるタスクは C-<n> の行で出す(同じ報告を 2 回送らない)
	where := "status IN (" + st.SQLList("done") + ") AND (reported_at IS NULL OR reported_at='') AND NOT EXISTS (SELECT 1 FROM comments c WHERE c.task_id=tasks.id AND c.kind='done' AND c.delivered_at IS NULL)"
	switch mode {
	case "ids":
		if err := sqlTSV(db.DB, w, "SELECT id FROM tasks WHERE ("+where+") ORDER BY updated_at, id"); err != nil {
			return err
		}
		return sqlTSV(db.DB, w, "SELECT 'C-'||id FROM comments WHERE delivered_at IS NULL ORDER BY id")
	case "json":
		rows, err := sqlObjs(db.DB, []string{"deliverables"}, "SELECT * FROM tasks WHERE ("+where+") ORDER BY updated_at, id")
		if err != nil {
			return err
		}
		compactRows(w, rows)
		return commentRows(db, w, "delivered_at IS NULL")
	}
	// 宛先は行を読み終えてから引く(接続は 1 本なので、行を読んでいる間に別の問い合わせをすると止まる)
	var out [][3]string
	line := func(_ []string, vals []any) error {
		out = append(out, [3]string{tickets.Text(vals[0]), tickets.Text(vals[1]), tickets.Text(vals[2])})
		return nil
	}
	if err := qrows(db.DB, "SELECT id, project, replace(replace(COALESCE(summary,''), char(10), ' '), char(9), ' ') FROM tasks WHERE ("+where+") ORDER BY updated_at, id", nil, line); err != nil {
		return err
	}
	// 未配送のコメント: ID は C-<n>(tb report C-<n> で再送)
	if err := qrows(db.DB, `SELECT 'C-'||c.id, COALESCE(t.project,''), '['||c.kind||'] '||substr(replace(replace(c.text, char(10), ' '), char(9), ' '),1,80)
		FROM comments c LEFT JOIN tasks t ON t.id=c.task_id WHERE c.delivered_at IS NULL ORDER BY c.id`, nil, line); err != nil {
		return err
	}
	for _, r := range out {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r[0], r[1], message.TargetFor(db.DB, db.Session, r[1]), r[2])
	}
	return nil
}
