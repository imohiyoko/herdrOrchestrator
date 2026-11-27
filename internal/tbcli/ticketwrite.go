package tbcli

// チケットの書き込み(taskboard/lib/tasks.sh の link / unlink / sprint-add / sprint-start / sprint-close)。
// 履歴(task_events)は bash と同じく接続ごとの一時トリガーで残す(tickets.SetupEvents)。

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var linkTypes = []string{"blocks", "relates", "duplicates"}

// eventActorDefault は event_actor_default: pane からなら pane:<id>、人の端末なら human。
func eventActorDefault() string {
	if p := os.Getenv("HERDR_PANE_ID"); p != "" {
		return "pane:" + p
	}
	return "human"
}

// eventsOpen は event_setup_load: 既定の変えた者で履歴のトリガーを作る(DB を開いたときの bash と同じ)。
func eventsOpen(db *store.DB) error { return tickets.SetupEvents(db.DB, eventActorDefault()) }

// eventActorResolve は event_actor_resolve: pane からなら herdr から agent 名を引き直す(引けなければ pane:<id> のまま)。
func eventActorResolve(db *store.DB) error {
	if os.Getenv("HERDR_PANE_ID") == "" {
		return nil
	}
	l, _, ok := identity.ResolveLabel(func(code, def string) string { return config.Msg(db.DB, db.Session, code, def) })
	if !ok || l.Name == "" {
		return nil
	}
	return tickets.SetupEvents(db.DB, l.Name)
}

// actorName は actor_name: pane からなら herdr から引いた agent 名(引けなければ 3)、人の端末なら human。
func actorName(db *store.DB, stderr io.Writer) (string, error) {
	if os.Getenv("HERDR_PANE_ID") == "" {
		return "human", nil
	}
	l, err := label(db, stderr)
	return l.Name, err
}

// rulesHook は notify_rules_hook: 通知の規則を流す(失敗しても止めない)。
func rulesHook(db *store.DB, st tickets.StatusSet) { notify.TicketRules(db.DB, db.Session, st) }

func openWrite(stderr io.Writer) (*store.DB, tickets.StatusSet, error) {
	db, st, err := openCmd(stderr)
	if err != nil {
		return nil, st, err
	}
	if err := eventsOpen(db); err != nil {
		db.Close()
		return nil, st, err
	}
	return db, st, nil
}

func linkArgs(db *store.DB, name string, a []string) error {
	if len(a) != 3 {
		return usage("%s <from-id> blocks|relates|duplicates <to-id>", name)
	}
	if err := needID(a[0]); err != nil {
		return err
	}
	if err := needID(a[2]); err != nil {
		return err
	}
	ok := false
	for _, t := range linkTypes {
		ok = ok || t == a[1]
	}
	if !ok {
		return usage("リンクの種類は %s のどれか: %s", strings.Join(linkTypes, " "), a[1])
	}
	if a[0] == a[2] {
		return usage("自分自身とはリンクできません: %s", a[0])
	}
	if err := requireTask(db, a[0]); err != nil {
		return err
	}
	return requireTask(db, a[2])
}

// cmdLink は tb link <from> blocks|relates|duplicates <to>。
func cmdLink(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := linkArgs(db, "link", args); err != nil {
		return err
	}
	from, typ, to := args[0], args[1], args[2]
	by, err := actorName(db, stderr)
	if err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	// blocks は、to から blocks をたどって from に着くなら循環。検査と挿入は 1 つの書き込みトランザクションで行う
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if typ == "blocks" {
		var cyc int
		err := conn.QueryRowContext(ctx, `SELECT EXISTS (WITH RECURSIVE r(id) AS (SELECT ?1 UNION SELECT l.to_id FROM task_links l JOIN r ON l.from_id=r.id WHERE l.link_type='blocks')
			SELECT 1 FROM r WHERE id=?2)`, to, from).Scan(&cyc)
		if err != nil {
			return err
		}
		if cyc != 0 {
			return usage("blocks が循環します: %s → %s", from, to)
		}
	}
	r, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO main.task_links (from_id, link_type, to_id, created_by, created_at, herdr_session)
		VALUES (?, ?, ?, ?, ?, ?)`, from, typ, to, by, now(), db.Session)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	commit = true
	conn.Close()
	rulesHook(db, st)
	if n == 1 {
		fmt.Fprintf(w, "%s %s %s\n", from, typ, to)
	} else {
		fmt.Fprintf(w, "%s %s %s(既にあります)\n", from, typ, to)
	}
	return nil
}

// cmdUnlink は tb unlink <from> <種類> <to>。
func cmdUnlink(args []string, w, stderr io.Writer) error {
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := linkArgs(db, "unlink", args); err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	r, err := db.Exec(`DELETE FROM main.task_links WHERE herdr_session=? AND from_id=? AND link_type=? AND to_id=?`, db.Session, args[0], args[1], args[2])
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return die("そのリンクはありません: %s %s %s", args[0], args[1], args[2])
	}
	rulesHook(db, st)
	fmt.Fprintf(w, "unlinked %s %s %s\n", args[0], args[1], args[2])
	return nil
}

// cmdSprintAdd は tb sprint-add -p <project> -n <名前> [--start D] [--end D] [--goal G] → S-<n>。
func cmdSprintAdd(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var project, name, start, end, goal string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var p *string
		switch a {
		case "-p", "--project":
			p = &project
		case "-n", "--name":
			p = &name
		case "--start":
			p = &start
		case "--end":
			p = &end
		case "--goal":
			p = &goal
		default:
			return usage("sprint-add: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		if (a == "--start" || a == "--end") && !validDue(v) {
			return usage("期日は YYYY-MM-DD の日付: %s", v)
		}
		*p = v
		i++
	}
	if project == "" {
		return usage("sprint-add: -p <project> が必要です")
	}
	if err := needProject(project); err != nil {
		return err
	}
	if blank(name) {
		return usage("sprint-add: -n <名前> が必要です")
	}
	if start != "" && end != "" && end < start {
		return usage("sprint-add: --end は --start より後にする")
	}
	r, err := db.Exec(`INSERT INTO main.sprints (project, name, goal, start_date, end_date, created_at, herdr_session)
		VALUES (?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, ?)`, project, name, goal, start, end, now(), db.Session)
	if err != nil {
		return err
	}
	id, _ := r.LastInsertId()
	fmt.Fprintf(w, "S-%d\n", id)
	return nil
}

// sprintRow は sprint_row: このセッションのスプリントの state と project。
func sprintRow(db *sql.DB, id string) (state, project string, err error) {
	if err := needSprint(id); err != nil {
		return "", "", err
	}
	err = db.QueryRow(`SELECT state, project FROM sprints WHERE id=`+strings.TrimPrefix(id, "S-")).Scan(&state, &project)
	if err == sql.ErrNoRows {
		return "", "", die("スプリントが見つかりません: %s", id)
	}
	return state, project, err
}

// cmdSprintStart は tb sprint-start S-<n>: planned → active(同じ project の active は 1 つだけ)。
func cmdSprintStart(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) != 1 {
		return usage("sprint-start S-<n>")
	}
	id := args[0]
	st, project, err := sprintRow(db.DB, id)
	if err != nil {
		return err
	}
	if st != "planned" {
		return die("planned のスプリントだけ始められます(%s): %s", st, id)
	}
	r, err := db.Exec(`UPDATE main.sprints SET state='active', started_at=?
		WHERE herdr_session=? AND id=`+strings.TrimPrefix(id, "S-")+` AND state='planned'
		AND NOT EXISTS (SELECT 1 FROM sprints WHERE project=? AND state='active')`, now(), db.Session, project)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return die("project %s には active なスプリントがあります(先に tb sprint-close)", project)
	}
	fmt.Fprintf(w, "%s active\n", id)
	return nil
}

// sprintForTask は sprint_for_task: タスクを入れられるスプリントの番号。
func sprintForTask(db *sql.DB, id, project string) (string, error) {
	if err := needSprint(id); err != nil {
		return "", err
	}
	n := strings.TrimPrefix(id, "S-")
	var st, p string
	err := db.QueryRow(`SELECT state, project FROM sprints WHERE id=`+n).Scan(&st, &p)
	if err == sql.ErrNoRows {
		return "", die("スプリントが見つかりません: %s", id)
	}
	if err != nil {
		return "", err
	}
	if p != project {
		return "", usage("スプリント %s は project %s のものです(タスクは %s)", id, p, project)
	}
	if st == "closed" {
		return "", usage("閉じたスプリントには入れられません: %s", id)
	}
	return n, nil
}

// cmdSprintClose は tb sprint-close S-<n> [--carry S-<m>]: 閉じる。終わっていないタスクは S-<m>(無ければバックログ)へ。
func cmdSprintClose(args []string, w, stderr io.Writer) error {
	db, sts, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("sprint-close S-<n> [--carry S-<m>]")
	}
	id, carry := args[0], ""
	for i := 1; i < len(args); i++ {
		if args[i] != "--carry" {
			return usage("sprint-close: 不明な引数 %s", args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		carry = v
		i++
	}
	st, project, err := sprintRow(db.DB, id)
	if err != nil {
		return err
	}
	if st == "closed" {
		return die("すでに閉じています: %s", id)
	}
	to := "NULL"
	if carry != "" {
		if carry == id {
			return usage("sprint-close: 同じスプリントには移せません")
		}
		if to, err = sprintForTask(db.DB, carry, project); err != nil {
			return err
		}
	}
	n := strings.TrimPrefix(id, "S-")
	// 閉じる側・移す先が閉じていないことを書く時点で確かめる(並行した close と競っても二重に動かさない)
	ok := "EXISTS (SELECT 1 FROM main.sprints WHERE herdr_session=?1 AND id=" + n + " AND state!='closed')"
	if to != "NULL" {
		ok += " AND EXISTS (SELECT 1 FROM main.sprints WHERE herdr_session=?1 AND id=" + to + " AND state!='closed')"
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	db.RaceHook()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	ts := now()
	r, err := conn.ExecContext(ctx, "UPDATE main.tasks SET sprint_id="+to+", updated_at=?2 WHERE herdr_session=?1 AND sprint_id="+n+
		" AND status NOT IN ("+sts.SQLList("done", "cancelled")+") AND "+ok, db.Session, ts)
	if err != nil {
		return err
	}
	moved, _ := r.RowsAffected()
	r, err = conn.ExecContext(ctx, "UPDATE main.sprints SET state='closed', closed_at=?2 WHERE herdr_session=?1 AND id="+n+" AND "+ok, db.Session, ts)
	if err != nil {
		return err
	}
	closed, _ := r.RowsAffected()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	commit = true
	if closed != 1 {
		dest := carry
		if dest == "" {
			dest = "移す先"
		}
		return die("sprint-close: %s か %s が並行して閉じられました", id, dest)
	}
	dest := carry
	if dest == "" {
		dest = "バックログ"
	}
	fmt.Fprintf(w, "%s closed(未完了 %d 件を %s へ)\n", id, moved, dest)
	return nil
}
