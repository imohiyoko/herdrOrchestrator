package tbcli

// チケットの報告コメントと完了報告(taskboard/lib/comments.sh の comment / comments / resolve / reopen と、
// tasks.sh の report / pending)。

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	commentKinds = []string{"done", "progress", "question", "blocked"}
	reDigits     = regexp.MustCompile(`^[0-9]+$`)
	reSpawnedBy  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reCRef       = regexp.MustCompile(`^C-[0-9]+$`)
)

// joinDeliverables は jq の '.deliverables | fromjson | join(", ")'(読めなければ ok=false)。
func joinDeliverables(s string) (string, bool) {
	vs, err := jv.DecodeAll([]byte(s))
	if err != nil || len(vs) != 1 {
		return "", false
	}
	arr, ok := vs[0].([]any)
	if !ok {
		return "", false
	}
	parts := make([]string, len(arr))
	for i, v := range arr {
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		parts[i] = s
	}
	return strings.Join(parts, ", "), true
}

// deliverComment は deliver_comment: 報告コメントを指揮役へ届ける。戻り値は 0 / 10 / 11(transport.Send と同じ)。
func deliverComment(db *store.DB, w, stderr io.Writer, ref string) error {
	if !reDigits.MatchString(ref) {
		return usage("コメント ID は C-<数字>")
	}
	var tid, kind, text, dels, name, pane string
	var delivered sql.NullString
	err := db.QueryRow(`SELECT task_id, kind, text, deliverables, from_name, from_pane, delivered_at FROM comments WHERE id=`+ref).
		Scan(&tid, &kind, &text, &dels, &name, &pane, &delivered)
	if err == sql.ErrNoRows {
		return die("コメントが見つかりません: C-%s", ref)
	}
	if err != nil {
		return err
	}
	if delivered.String != "" {
		fmt.Fprintf(w, "C-%s は配送済みです(%s)\n", ref, delivered.String)
		return nil
	}
	var project string
	_ = db.QueryRow(`SELECT project FROM tasks WHERE id=?`, tid).Scan(&project)
	target := message.TargetFor(db.DB, db.Session, project)
	// tb agent-spawn で立てた子なら、指揮官が親にも伝えられるよう親の名前を付ける
	var sby string
	_ = db.QueryRow(`SELECT COALESCE(spawned_by,'') FROM agents WHERE name=?`, name).Scan(&sby)
	if reSpawnedBy.MatchString(sby) {
		sby = "spawned_by: " + sby + " "
	} else {
		sby = ""
	}
	d := ""
	if kind == "done" {
		d, _ = joinDeliverables(dels)
	}
	msg := "[from: " + name + " / " + pane + " / " + tid + " / " + kind + "] " + sby + util.Oneline(text, 200)
	if d != "" {
		msg += " / 成果物: " + util.Oneline(d, 300)
	}
	msg += " (詳細: tb comments " + tid + ")"
	if rc := transport.Send(db.DB, db.Session, target, msg, "C-"+ref, "comment", stderr); rc != transport.Sent {
		return &exit{rc, ""}
	}
	ts := now()
	if _, err := db.Exec(`UPDATE main.comments SET delivered_at=? WHERE herdr_session=? AND id=`+ref, ts, db.Session); err != nil {
		return err
	}
	commentMarksReported(db, ref, ts)
	fmt.Fprintf(w, "C-%s delivered to %s\n", ref, target)
	return nil
}

// commentMarksReported は comment_marks_reported: done の報告コメントが届いたら、そのタスク(done のもの)を報告済みにする。
func commentMarksReported(db *store.DB, ref, ts string) {
	st := tickets.LoadStatuses(db.DB, db.Session)
	_, _ = db.Exec(`UPDATE main.tasks SET reported_at=?1, updated_at=?1
		WHERE herdr_session=?2 AND status IN (`+st.SQLList("done")+`) AND reported_at IS NULL
		AND id=(SELECT task_id FROM comments WHERE id=`+ref+` AND kind='done')`, ts, db.Session)
}

// commentNotify は notify_hook_comment: 外への通知を積む(失敗しても止めない)。
func commentNotify(db *store.DB, cid int64) {
	var tid, kind, text, name, dels string
	if db.QueryRow(`SELECT task_id, kind, text, from_name, deliverables FROM comments WHERE id=?`, cid).Scan(&tid, &kind, &text, &name, &dels) != nil {
		return
	}
	q := notify.New(db.DB, db.Session)
	switch kind {
	case "question", "blocked":
		q.Emit(notify.Event{Type: kind, Ref: "C-" + strconv.FormatInt(cid, 10), A: tid, B: name, C: text})
	case "done":
		d, ok := joinDeliverables(dels)
		if !ok {
			return
		}
		q.Emit(notify.Event{Type: "done", Ref: tid, A: tid, B: text, C: d})
	}
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
	db, st, err := openWrite(stderr)
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
	if !contains(commentKinds, kind) {
		return usage("comment: --kind は %s のどれか", strings.Join(commentKinds, " "))
	}
	if blank(text) {
		return usage("comment: --text が必要です")
	}
	darr := []string{}
	if dels != "" {
		for _, d := range strings.Split(dels, ",") {
			if d = config.Trim(d); d != "" {
				darr = append(darr, d)
			}
		}
	}
	if kind == "done" && len(darr) == 0 {
		return usage("comment: done は --deliverables(成果物)が必須です")
	}
	if err := requireTask(db, tid); err != nil {
		return err
	}
	l, err := label(db, stderr)
	if err != nil {
		return err
	}
	r, err := db.Exec(`INSERT INTO main.comments (herdr_session, task_id, kind, text, deliverables, from_name, from_pane, from_session, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, db.Session, tid, kind, text, strArray(darr), l.Name, l.Pane, l.Session, now())
	if err != nil {
		return err
	}
	cid, _ := r.LastInsertId()
	commentNotify(db, cid)
	rulesHook(db, st)
	state, rc := "queued", 0
	imm := "question blocked"
	if v, ok := os.LookupEnv("TB_IMMEDIATE_KINDS"); ok && v != "" {
		imm = v
	}
	if contains(strings.Fields(imm), kind) { // 人の判断待ちはすぐ送る。それ以外は tb digest でまとめて送る
		if err := deliverComment(db, io.Discard, stderr, strconv.FormatInt(cid, 10)); err != nil {
			e, ok := err.(*exit)
			if !ok {
				return err
			}
			rc = e.code
			if e.msg != "" {
				return err
			}
		}
		state = "delivered"
		if rc != 0 {
			state = "saved-undelivered"
		}
	}
	if js {
		if err := commentRows(db, w, "id=?", cid); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(w, "C-%d %s\n", cid, state)
	}
	if rc != 0 {
		return &exit{rc, ""}
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
	js := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			i++
			if !contains(commentKinds, v) {
				return usage("comments: --kind は %s", strings.Join(commentKinds, " "))
			}
			where += " AND kind=?"
			qa = append(qa, v)
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
	if err := requireHuman("tb resolve"); err != nil {
		return err
	}
	var kind string
	var at sql.NullString
	err = db.QueryRow(`SELECT kind, resolved_at FROM comments WHERE id=`+ref).Scan(&kind, &at)
	if err == sql.ErrNoRows {
		return die("報告が見つかりません: C-%s", ref)
	}
	if err != nil {
		return err
	}
	if kind != "question" && kind != "blocked" {
		return die("対応済みにできるのは question / blocked だけです(C-%s は %s)", ref, kind)
	}
	if at.String != "" {
		fmt.Fprintf(w, "C-%s はすでに対応済みです(%s)\n", ref, at.String)
		return nil
	}
	if _, err := db.Exec(`UPDATE main.comments SET resolved_at=?, resolved_note=NULLIF(?,'') WHERE herdr_session=? AND id=`+ref+` AND resolved_at IS NULL`, now(), note, db.Session); err != nil {
		return err
	}
	fmt.Fprintf(w, "C-%s resolved\n", ref)
	return nil
}

// cmdReopen は tb reopen C-<n> [-m 理由]: tb resolve の逆。戻したことは task_events の comment_reopened に残す。
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
	if err := requireHuman("tb reopen"); err != nil {
		return err
	}
	var kind, tid string
	var at sql.NullString
	err = db.QueryRow(`SELECT kind, resolved_at, task_id FROM comments WHERE id=`+ref).Scan(&kind, &at, &tid)
	if err == sql.ErrNoRows {
		return die("報告が見つかりません: C-%s", ref)
	}
	if err != nil {
		return err
	}
	if kind != "question" && kind != "blocked" {
		return die("未対応に戻せるのは question / blocked だけです(C-%s は %s)", ref, kind)
	}
	if at.String == "" {
		fmt.Fprintf(w, "C-%s は対応済みではありません(未対応のまま)\n", ref)
		return nil
	}
	var hasEv int
	_ = db.QueryRow(`SELECT COUNT(*) FROM main.sqlite_master WHERE type='table' AND name='task_events'`).Scan(&hasEv)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if _, err := conn.ExecContext(ctx, `UPDATE main.comments SET resolved_at=NULL, resolved_note=NULL WHERE herdr_session=? AND id=`+ref+` AND resolved_at IS NOT NULL`, db.Session); err != nil {
		return err
	}
	if hasEv > 0 {
		if _, err := conn.ExecContext(ctx, `INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at)
			SELECT ?, ?, 'comment_reopened', ?, NULLIF(?,''), ?, ?, ? WHERE changes() > 0`, db.Session, tid, at.String, note, eventActorDefault(), "C-"+ref, now()); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	commit = true
	fmt.Fprintf(w, "C-%s reopened\n", ref)
	return nil
}

// cmdReport は tb report <id> | report C-<n>: 完了の報告を指揮役へ届ける(C-<n> は未配送コメントの再送)。
func cmdReport(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) != 1 {
		return usage("report <id>")
	}
	if reCRef.MatchString(args[0]) {
		return deliverComment(db, w, stderr, strings.TrimPrefix(args[0], "C-"))
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	var status, project string
	var reported sql.NullString
	err = db.QueryRow(`SELECT status, project, reported_at FROM tasks WHERE id=?`, id).Scan(&status, &project, &reported)
	if err == sql.ErrNoRows {
		return die("タスクが見つかりません: %s", id)
	}
	if err != nil {
		return err
	}
	if !st.Is(status, "done") {
		return die("done ではないタスクは report できません(%s): %s", status, id)
	}
	if reported.String != "" {
		fmt.Fprintf(w, "%s は報告済みです(%s)\n", id, reported.String)
		return nil
	}
	target := message.TargetFor(db.DB, db.Session, project)
	// 完了の報告も報告コメント(kind done の C-<n>)として届ける。未配送の done コメントがあればそれを送り直す
	var cid int64
	err = db.QueryRow(`SELECT id FROM comments WHERE task_id=? AND kind='done' AND delivered_at IS NULL ORDER BY id LIMIT 1`, id).Scan(&cid)
	if err == sql.ErrNoRows {
		r, err := db.Exec(`INSERT INTO main.comments (herdr_session, task_id, kind, text, deliverables, from_name, from_pane, from_session, created_at)
			SELECT ?, id, 'done', COALESCE(summary,''), deliverables, COALESCE(owner_agent,'-'), '-', COALESCE(session_id,'-'), ? FROM tasks WHERE id=?`, db.Session, now(), id)
		if err != nil {
			return err
		}
		cid, _ = r.LastInsertId()
	} else if err != nil {
		return err
	}
	if err := deliverComment(db, io.Discard, stderr, strconv.FormatInt(cid, 10)); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s reported to %s (C-%d)\n", id, target, cid)
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
