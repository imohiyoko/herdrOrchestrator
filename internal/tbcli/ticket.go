package tbcli

// tb ticket <サブコマンド>(taskboard/lib/tasks.sh の cmd_ticket / ticket_move / ticket_status)。
// 状態そのものの中身は設定 statuses。別の置き場は作らない。

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

const ticketUsage = `tb ticket move <id> <状態> [-m メモ] [-d 成果物 ...]   どの状態からどの状態へも(閉じた状態から戻すのも)。
      -m は閉じた分類(done / cancelled)なら要約、それ以外ならメモ(note)に入る。-d は done の分類へ動かすときだけ
tb ticket status [list] [--json]                      状態・分類・説明・チケットの件数
tb ticket status add <状態> <分類> [-m 説明] [--after <状態>]   状態を足す(設定 statuses に書き足す。人だけ)
tb ticket status rm <状態> [--to <状態>]              状態を外す(その状態のチケットが残っていれば --to で移すまで外さない。人だけ)
tb ticket rm <id> [<id> ...]                         チケットを消す(コメント・履歴・リンクも。子があれば子も一緒に指定する。agent からは ask)
tb ticket add|list|show|update|claim|done|block|cancel ...   従来の tb <それ> と同じ
`

// cmdTicket は tb ticket <サブコマンド>。
func cmdTicket(args []string, w, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "move":
		return ticketMove(args, w, stderr)
	case "status", "statuses":
		return ticketStatus(args, w, stderr)
	case "add":
		return cmdAdd(args, w, stderr)
	case "list":
		return cmdList(args, w, stderr)
	case "show":
		return cmdShow(args, w, stderr)
	case "update":
		return cmdUpdate(args, w, stderr)
	case "claim":
		return cmdClaim(args, w, stderr)
	case "done":
		return cmdDone(args, w, stderr)
	case "block":
		return cmdBlock(args, w, stderr)
	case "cancel":
		return cmdCancel(args, w, stderr)
	case "report":
		return cmdReport(args, w, stderr)
	case "history":
		return cmdHistory(args, w, stderr)
	case "tree":
		return cmdTree(args, w, stderr)
	case "link":
		return cmdLink(args, w, stderr)
	case "unlink":
		return cmdUnlink(args, w, stderr)
	case "rm":
		return ticketRm(args, w, stderr)
	case "", "-h", "--help", "help":
		fmt.Fprint(w, ticketUsage)
		if sub == "" {
			return &exit{exitUsage, ""}
		}
		return nil
	}
	return usage("ticket: 不明なサブコマンド %s(move / status / rm / add / list / show / update / claim / done / block / cancel)", sub)
}

// ticketMove は ticket_move。中身はアプリケーション API の ticket.move(ADR 0008)。
// ここは引数を読み、呼び出し元を主体にして呼び、結果を今の文言にする。
func ticketMove(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 2 {
		return usage("ticket move <id> <状態> [-m メモ] [-d 成果物 ...]")
	}
	in := ticket.MoveReq{ID: args[0], Status: args[1], Deliverables: []string{}}
	if err := needID(in.ID); err != nil {
		return err
	}
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-m", "--message", "--summary", "-d", "--deliverable":
		default:
			return usage("ticket move: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		i++
		if a == "-d" || a == "--deliverable" {
			if !blank(v) {
				in.Deliverables = append(in.Deliverables, v)
			}
		} else {
			in.Message, in.SetMessage = v, true
		}
	}
	if err := eventActorResolve(db); err != nil {
		return err
	}
	var res ticket.MoveResult
	if err := callApp(db, "tb ticket move", "ticket.move", in, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	if !res.Changed {
		fmt.Fprintf(w, "%s はすでに %s です\n", res.ID, res.To)
		return nil
	}
	fmt.Fprintf(w, "%s %s → %s\n", res.ID, res.From, res.To)
	return nil
}

// ticketStatus は ticket_status。
func ticketStatus(args []string, w, stderr io.Writer) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		return ticketStatusList(args, w, stderr)
	case "add":
		return ticketStatusAdd(args, w, stderr)
	case "rm", "remove":
		return ticketStatusRm(args, w, stderr)
	}
	return usage("ticket status [list [--json] | add <状態> <分類> [-m 説明] [--after <状態>] | rm <状態> [--to <状態>]]")
}

// statusDescs は statuses_parse の説明(<状態> <分類> = 説明)。本文が不正なら空(組み込みに戻るので説明も無い)。
func statusDescs(body string) map[string]string {
	out := map[string]string{}
	if _, _, ok := tickets.ParseStatuses(body); !ok {
		return out
	}
	for _, line := range config.Lines(body) {
		line = config.Trim(config.StripComment(line))
		k, d, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name, _ := config.FirstWord(config.Trim(k))
		out[name] = config.Trim(d)
	}
	return out
}

func statusSource(db *store.DB) string { return tickets.StatusSource(db) }

func ticketStatusList(args []string, w, stderr io.Writer) error {
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	js := false
	for _, a := range args {
		if a != "--json" {
			return usage("ticket status list [--json]")
		}
		js = true
	}
	counts := map[string]int64{}
	var order []string
	if err := qrows(db.DB, "SELECT status, COUNT(*) FROM tasks GROUP BY status", nil, func(_ []string, v []any) error {
		k := tickets.Text(v[0])
		n, _ := v[1].(int64)
		counts[k] = n
		order = append(order, k)
		return nil
	}); err != nil {
		return err
	}
	body, src, _ := config.Lookup(db.DB, db.Session, "statuses")
	descs := statusDescs(body)
	all := boardCols(st)
	for _, k := range order {
		if !slices.Contains(all, k) {
			all = append(all, k)
		}
	}
	for _, k := range all {
		c, n, d := st.Cat(k), counts[k], descs[k]
		configured := slices.Contains(st.Names, k)
		if js {
			fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "status", V: k}, {K: "category", V: c}, {K: "description", V: d}, {K: "tickets", V: jv.Number(fmt.Sprint(n))}, {K: "configured", V: configured}}))
			continue
		}
		if c == "" {
			c = "-"
		}
		if d == "" && !configured {
			d = "(設定から外した状態)"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", k, c, n, d)
	}
	if !js {
		fmt.Fprintf(stderr, "# 設定 statuses: %s(状態 分類 件数 説明)\n", src)
	}
	return nil
}

func ticketStatusAdd(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 2 {
		return usage("ticket status add <状態> <分類> [-m 説明] [--after <状態>]")
	}
	name, cat := args[0], args[1]
	desc, after := "", ""
	for i := 2; i < len(args); i++ {
		a := args[i]
		var p *string
		switch a {
		case "-m", "--desc", "--description":
			p = &desc
		case "--after":
			p = &after
		default:
			return usage("ticket status add: 不明な引数 %s", a)
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		*p = v
		i++
	}
	var res ticket.StatusDefResult
	if err := callAppWarn(db, "tb ticket status add", stderr, "ticket.status.add", ticket.StatusAddReq{Name: name, Category: cat, Desc: desc, After: after}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintf(w, "状態 %s を足しました(分類 %s。設定 statuses: %s)\n", name, cat, res.Source)
	return nil
}

func ticketStatusRm(args []string, w, stderr io.Writer) error {
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) < 1 {
		return usage("ticket status rm <状態> [--to <状態>]")
	}
	name, to := args[0], ""
	for i := 1; i < len(args); i++ {
		if args[i] != "--to" {
			return usage("ticket status rm: 不明な引数 %s", args[i])
		}
		v, err := argv(args, i)
		if err != nil {
			return err
		}
		to = v
		i++
	}
	if err := eventActorResolve(db); err != nil { // 移したチケットの履歴に agent の名前を残す(tb ticket move と同じ)
		return err
	}
	var res ticket.StatusDefResult
	if err := callAppWarn(db, "tb ticket status rm", stderr, "ticket.status.rm", ticket.StatusRmReq{Name: name, To: to}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	if res.HadTickets {
		fmt.Fprintf(w, "状態 %s のチケット %d 件を %s へ移しました\n", name, res.Moved, to)
	}
	fmt.Fprintf(w, "状態 %s を外しました(設定 statuses: %s)\n", name, res.Source)
	return nil
}
