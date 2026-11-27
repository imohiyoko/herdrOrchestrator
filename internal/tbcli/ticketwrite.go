package tbcli

// チケットの書き込み(taskboard/lib/tasks.sh の link / unlink / sprint-add / sprint-start / sprint-close)。
// 履歴(task_events)は bash と同じく接続ごとの一時トリガーで残す(tickets.SetupEvents)。

import (
	"database/sql"
	"fmt"
	"io"
	"os"

	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)


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

func linkArgs(name string, a []string) error {
	if len(a) != 3 {
		return usage("%s <from-id> blocks|relates|duplicates <to-id>", name)
	}
	return nil
}

// cmdLink は tb link <from> blocks|relates|duplicates <to>。中身はアプリケーション API の ticket.link。
func cmdLink(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := linkArgs("link", args); err != nil {
		return err
	}
	from, typ, to := args[0], args[1], args[2]
	if err := ticketErr(tickets.LinkCheck(db, "link", from, typ, to)); err != nil {
		return err
	}
	by, err := actorName(db, stderr)
	if err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.LinkResult
	if err := callAppActor(db, "tb link", by, "ticket.link", ticket.LinkReq{From: from, Type: typ, To: to}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	if res.Added {
		fmt.Fprintf(w, "%s %s %s\n", from, typ, to)
	} else {
		fmt.Fprintf(w, "%s %s %s(既にあります)\n", from, typ, to)
	}
	return nil
}

// cmdUnlink は tb unlink <from> <種類> <to>。中身は ticket.unlink。
func cmdUnlink(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := linkArgs("unlink", args); err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.LinkResult
	if err := callApp(db, "tb unlink", "ticket.unlink", ticket.LinkReq{From: args[0], Type: args[1], To: args[2]}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintf(w, "unlinked %s %s %s\n", args[0], args[1], args[2])
	return nil
}

// cmdSprintAdd は tb sprint-add -p <project> -n <名前> [--start D] [--end D] [--goal G] → S-<n>。中身は ticket.sprint.add。
func cmdSprintAdd(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.SprintAddReq
	for i := 0; i < len(args); i++ {
		a := args[i]
		var p *string
		switch a {
		case "-p", "--project":
			p = &in.Project
		case "-n", "--name":
			p = &in.Name
		case "--start":
			p = &in.Start
		case "--end":
			p = &in.End
		case "--goal":
			p = &in.Goal
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
	var res ticket.SprintResult
	if err := callApp(db, "tb sprint-add", "ticket.sprint.add", in, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.ID)
	return nil
}

// sprintForTask は sprint_for_task: タスクを入れられるスプリントの番号。
func sprintForTask(db *sql.DB, id, project string) (string, error) {
	n, err := tickets.SprintForTask(db, id, project)
	return n, ticketErr(err)
}

// cmdSprintStart は tb sprint-start S-<n>: planned → active(同じ project の active は 1 つだけ)。中身は ticket.sprint.start。
func cmdSprintStart(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) != 1 {
		return usage("sprint-start S-<n>")
	}
	var res ticket.SprintResult
	if err := callApp(db, "tb sprint-start", "ticket.sprint.start", ticket.SprintReq{ID: args[0]}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintf(w, "%s active\n", args[0])
	return nil
}

// cmdSprintClose は tb sprint-close S-<n> [--carry S-<m>]: 閉じる。終わっていないタスクは S-<m>(無ければバックログ)へ。中身は ticket.sprint.close。
func cmdSprintClose(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
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
	var res ticket.SprintResult
	if err := callApp(db, "tb sprint-close", "ticket.sprint.close", ticket.SprintReq{ID: id, Carry: carry}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	dest := carry
	if dest == "" {
		dest = "バックログ"
	}
	fmt.Fprintf(w, "%s closed(未完了 %d 件を %s へ)\n", id, res.Moved, dest)
	return nil
}
