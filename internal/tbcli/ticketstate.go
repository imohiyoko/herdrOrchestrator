package tbcli

// チケットの状態を動かすコマンド(taskboard/lib/tasks.sh の claim / done / block / cancel)。

import (
	"errors"
	"fmt"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// specialTicketGuard は special_ticket_guard: アラート・提案のチケットの状態は、それぞれのコマンドだけが変える。
func specialTicketGuard(db tickets.RowQuerier, id, op string) error {
	return ticketErr(tickets.SpecialGuard(db, id, op))
}

// taskStatus は require_task のあとの .status。
func taskStatus(db *store.DB, id string) (string, error) {
	st, err := tickets.StatusOf(db.DB, id)
	return st, ticketErr(err)
}

// ticketErr は tickets の断り(Refused)を tb の die にする。
func ticketErr(err error) error {
	var r *tickets.Refused
	if errors.As(err, &r) {
		return die("%s", r.Msg)
	}
	return err
}

// taskDoneHook は notify_hook notify_hook_task_done: 完了の通知を積む(失敗しても止めない)。
func taskDoneHook(db *store.DB, id string) { notify.TaskDone(db.DB, db.Session, id) }

// cmdClaim は tb claim <id> --owner <name> [--session <sid>] [--account <acct>]。
func cmdClaim(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("claim <id> --owner <name> [--session <sid>] [--account <acct>]")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	var owner, session, account string
	for i := 1; i < len(args); i++ {
		var p *string
		switch args[i] {
		case "--owner":
			p = &owner
		case "--session":
			p = &session
		case "--account":
			p = &account
		default:
			return usage("claim: 不明な引数 %s", args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		*p = v
		i++
	}
	if blank(owner) {
		return usage("claim: --owner が必要です")
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.StateResult
	if err := callApp(db, "tb claim", "ticket.claim", ticket.ClaimReq{ID: id, Owner: owner, Session: session, Account: account}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintf(w, "%s claimed by %s\n", id, owner)
	return nil
}

// cmdDone は tb done <id> -d <成果物> [-d ...] -m <要約>。
func cmdDone(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("done <id> -d <成果物パス> [-d ...] -m <要約>")
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	var summary string
	var dels []string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-d", "--deliverable":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			if !blank(v) {
				dels = append(dels, v)
			}
		case "-m", "--summary":
			v, err := argv(args, i)
			if err != nil {
				return err
			}
			summary = v
		default:
			return usage("done: 不明な引数 %s", args[i])
		}
		i++
	}
	if len(dels) == 0 {
		return usage("done: 成果物(-d)が必須です")
	}
	if blank(summary) {
		return usage("done: 要約(-m)が必須です")
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.StateResult
	if err := callApp(db, "tb done", "ticket.done", ticket.DoneReq{ID: id, Deliverables: dels, Summary: summary}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintf(w, "%s done (報告は tb digest でまとめて届く。すぐ届けるなら tb report %s)\n", id, id)
	return nil
}

// reasonArgs は block / cancel の [-m <理由>]。
func reasonArgs(name string, args []string) (string, error) {
	reason := ""
	for i := 1; i < len(args); i++ {
		if args[i] != "-m" && args[i] != "--message" {
			return "", usage("%s: 不明な引数 %s", name, args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return "", err
		}
		reason = v
		i++
	}
	return reason, nil
}

// cmdBlock は tb block <id> [-m <理由>]。
func cmdBlock(args []string, w, stderr io.Writer) error {
	return reasonCmd("block", args, w, stderr)
}

// cmdCancel は tb cancel <id> [-m <理由>]: やらないと決めたタスクを閉じる(完了報告には出さない)。
func cmdCancel(args []string, w, stderr io.Writer) error {
	return reasonCmd("cancel", args, w, stderr)
}

// reasonCmd は block / cancel。中身はアプリケーション API の ticket.block / ticket.cancel。
func reasonCmd(name string, args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("%s <id> [-m <理由>]", name)
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	reason, err := reasonArgs(name, args)
	if err != nil {
		return err
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.StateResult
	if err := callApp(db, "tb "+name, "ticket."+name, ticket.ReasonReq{ID: id, Reason: reason}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintf(w, "%s %s\n", id, res.Status)
	return nil
}
