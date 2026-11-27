package tbcli

// チケットを作る・変えるコマンド(taskboard/lib/tasks.sh の add / update)。中身はアプリケーション API の ticket.add / ticket.update
// (ADR 0008)と internal/tickets(ラベル・外部リンク・親子の規則)。ここは引数を読み、結果を今の文言にする。

import (
	"fmt"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// labelsJSON は labels_json: 引数(それぞれカンマ区切り可)→ ラベルの配列(重複なし・辞書順)。
func labelsJSON(args []string) ([]string, error) {
	l, err := tickets.LabelsJSON(args)
	return l, ticketErr(err)
}

func strArray(ss []string) string { return tickets.StrArray(ss) }

// systemLabelOf は system_label_of <用途>(team の {team} は置き換えない)。
func systemLabelOf(db *store.DB, stderr io.Writer, use string) string {
	return tickets.SystemLabelOf(db, stderr, use)
}

// cmdAdd は tb add -p <project> -t <title> [...] → ID。
func cmdAdd(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var in ticket.AddReq
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-p", "--project", "-t", "--title", "--type", "--priority", "-l", "--label", "--desc", "--description",
			"--ref", "--due", "--sprint", "--points", "--parent", "--owner", "--session", "--account":
		default:
			return usage("add: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		i++
		var check error
		switch a {
		case "-p", "--project":
			in.Project = v
		case "-t", "--title":
			in.Title = v
		case "--type":
			check, in.Type = tickets.ValidType(v), v
		case "--priority":
			check, in.Priority = tickets.ValidPriority(v), v
		case "-l", "--label":
			in.Labels = append(in.Labels, v)
		case "--desc", "--description":
			in.Desc = v
		case "--ref":
			in.Refs = append(in.Refs, v)
		case "--due":
			check, in.Due = tickets.NeedDue(v), v
		case "--sprint":
			in.Sprint = v
		case "--points":
			check, in.Points = tickets.ValidPoints(v), v
		case "--parent":
			in.Parent = v
		case "--owner":
			in.Owner = v
		case "--session":
			in.Session = v
		case "--account":
			in.Account = v
		}
		if err := ticketErr(check); err != nil {
			return err
		}
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.AddResult
	if err := callAppWarn(db, "tb add", stderr, "ticket.add", in, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.ID)
	return nil
}

// statusEnterSets は status_enter_sets: 分類に入ったときだけ完了報告の列を合わせる。
func statusEnterSets(st tickets.StatusSet, to, cur string) string {
	return tickets.EnterSets(st, to, cur, now())
}

// statusEnteredHook は status_entered_hook: done の分類に入ったら done の通知。
func statusEnteredHook(db *store.DB, st tickets.StatusSet, id, to, cur string) {
	if st.Is(to, "done") && !st.Is(cur, "done") {
		taskDoneHook(db, id)
	}
}

const updateUsage = "update <id> [--status S] [--title T] [--owner N] [--session S] [--account A] [--parent ID] [--type T] [--priority P] [--desc D] [--due YYYY-MM-DD|''] [--label L ...] [--add-label L] [--remove-label L]"

// updateFlags は tb update の引数 → tickets.Change の Field。
var updateFlags = map[string]string{
	"--status": "status", "--title": "title", "--owner": "owner", "--session": "session", "--account": "account", "--parent": "parent",
	"--type": "type", "--priority": "priority", "--desc": "desc", "--description": "desc", "--due": "due", "-l": "label", "--label": "label",
	"--add-label": "add-label", "--remove-label": "remove-label", "--add-ref": "add-ref", "--sprint": "sprint", "--points": "points",
	"--remove-ref": "remove-ref",
}

// cmdUpdate は tb update <id> [...]。
func cmdUpdate(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage(updateUsage)
	}
	id := args[0]
	if err := needID(id); err != nil {
		return err
	}
	in := ticket.UpdateReq{ID: id, Changes: []tickets.Change{}}
	for i := 1; i < len(args); i++ {
		f, ok := updateFlags[args[i]]
		if !ok {
			return usage("update: 不明な引数 %s", args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		i++
		in.Changes = append(in.Changes, tickets.Change{Field: f, Value: v})
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.UpdateResult
	if err := callAppWarn(db, "tb update", stderr, "ticket.update", in, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintf(w, "%s updated\n", id)
	return nil
}
