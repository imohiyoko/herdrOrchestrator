package tbcli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appmessage "github.com/imohiyoko/herdrorchestrator/internal/app/message"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

// tb message(taskboard/lib/send.sh の send / flush / outbox / inbox / inbox-read)。
// 古い名前 tb send / flush / outbox / inbox / inbox-read も同じに動かす。

const messageUsage = `tb message send <宛先 agent 名> --text T [--task T-..] [--kind task|info|answer] [--reply-to O-<n>] [--json]
tb message flush                     積まれた送信(outbox の queued)を届ける
tb message outbox [--status pending|queued|sent|denied] [--json]
tb message inbox [--unread] [--json]  自分宛てに保留された送信の一覧
tb message read I-<n> [--json]        1 件を全文で読み、既読にする
(古い名前 tb send / tb flush / tb outbox / tb inbox / tb inbox-read も同じに使える)
`

var (
	reIID = regexp.MustCompile(`^I-[0-9]+$`)
)

// cmdMessage は tb message <サブコマンド>。legacy は古い名前(send / flush / outbox / inbox / inbox-read)。
func cmdMessage(args []string, w, stderr io.Writer, legacy string) error {
	sub := legacy
	if sub == "" {
		if len(args) == 0 {
			fmt.Fprint(w, messageUsage)
			return &exit{exitUsage, ""}
		}
		sub, args = args[0], args[1:]
		switch sub {
		case "help", "-h", "--help":
			fmt.Fprint(w, messageUsage)
			return nil
		case "send", "flush", "outbox", "inbox":
		case "read":
			sub = "inbox-read"
		default:
			return usage("message: 不明なサブコマンド %s(send / flush / outbox / inbox / read)", sub)
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	switch sub {
	case "send":
		return send(db, args, w, stderr)
	case "flush":
		return flushOutbox(db, args, w, stderr)
	case "outbox":
		return outboxList(db, args, w)
	case "inbox":
		return inboxList(db, args, w, stderr)
	default:
		return inboxRead(db, args, w, stderr)
	}
}

// send は cmd_send。終了コード: 0 送信済み・保留 / 10 使用中 / 11 送信失敗 / 12 保留に失敗 / 4 拒否 / 20 承認待ち / 3 送信者が分からない
func send(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("send <宛先 agent 名> --text T [--task T-..] [--kind task|info|answer] [--reply-to O-<n>] [--json]")
	}
	target, text, tid, kind, js, reply := args[0], "", "", "task", false, ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--text" || a == "--task" || a == "--kind" || a == "--reply-to":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			v := args[i+1]
			i++
			switch a {
			case "--text":
				text = v
			case "--task":
				tid = v
			case "--kind":
				kind = v
			default:
				reply = v
			}
		case a == "--json":
			js = true
		case a == "--from" || strings.HasPrefix(a, "--from-") || a == "--name" || a == "--pane" || a == "--session":
			return usage("send: 送信者は herdr から自動で決まります。%s は指定できません", a)
		default:
			return usage("send: 不明な引数 %s", a)
		}
	}
	// 入力の確かめは herdr に問い合わせる前に(使い方の誤りは 2。API の message.send でも同じ確かめをする)
	spec := message.SendSpec{To: target, Text: text, Task: tid, Kind: kind, ReplyTo: reply}
	if err := message.CheckSend(db.DB, spec); err != nil {
		var r *message.SendRefused
		if errors.As(err, &r) {
			if r.Usage {
				return usage("%s", r.Msg)
			}
			return die("%s", r.Msg)
		}
		return err
	}
	l, err := label(db, stderr) // 送信者は herdr から(決められなければ理由を出して 3)
	if err != nil {
		return err
	}
	env := appEnv(db, stderr)
	env.Reporter = func(string) (string, string, string) { return l.Name, l.Pane, l.Session }
	by, err := callerPrincipal("tb message send")
	if err != nil {
		by = app.Agent(l.Name) // 送信者は決まっている(pane の持ち主)
	}
	var r appmessage.SendResult
	if err := callAppEnv(env, by, "message.send", appmessage.SendReq{To: target, Text: text, Task: tid, Kind: kind, ReplyTo: reply}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	oid := requests.Num(r.ID)
	rc, held, blocked := r.Code, r.Held, r.Blocked
	switch {
	case js:
		if err := outboxJSON(db, oid, held, blocked, w); err != nil {
			return err
		}
	case rc == 0 && held != "":
		fmt.Fprintf(w, "%s held for %s (%s)\n", r.ID, target, held)
	case rc == 0:
		fmt.Fprintf(w, "%s sent to %s\n", r.ID, target)
	case rc == 4 && blocked != "":
		fmt.Fprintf(w, "%s blocked by %s\n", r.ID, target)
	case rc == 4:
		fmt.Fprintf(w, "%s denied\n", r.ID)
	case rc == exitAsk:
		fmt.Fprintf(w, "%s pending-approval %s\n", r.ID, r.Q)
	default:
		fmt.Fprintf(w, "%s queued\n", r.ID)
	}
	if rc != 0 {
		return &exit{rc, ""}
	}
	return nil
}

// outboxJSON は send --json の 1 行(outbox の行。保留したら inbox、止めたら blocked を足す)。
func outboxJSON(db *store.DB, oid int64, held, blocked string, w io.Writer) error {
	var r appmessage.TextResult
	if err := callAppW(db, app.Human(), io.Discard, "message.outbox", appmessage.OutboxReq{ID: oid}, &r); err != nil {
		return err
	}
	if r.Text == "" {
		return nil
	}
	s := strings.TrimSuffix(strings.TrimSuffix(r.Text, "\n"), "}")
	if held != "" {
		s += `,"inbox":` + jqString(held)
	}
	if blocked != "" {
		s += `,"blocked":true`
	}
	_, err := fmt.Fprintln(w, s+"}")
	return err
}

// flushOutbox は cmd_flush: 積まれた送信を届ける。宛先ごとに古い順で、使用中の宛先は次回に回す。
// 停止中の宛先には送らず飛ばす(revive すれば次の flush で届く)。停止中かは、最初に 1 回 herdr の一覧と台帳を突き合わせて決める
func flushOutbox(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) != 0 {
		return usage("flush(引数なし)")
	}
	var r appmessage.FlushResult
	if err := callAppIO(db, "tb message flush", w, stderr, "message.flush", appmessage.FlushReq{}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	if r.Code != 0 {
		return &exit{r.Code, ""}
	}
	return nil
}

func outboxList(db *store.DB, args []string, w io.Writer) error {
	st, js := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--status":
			if i+1 >= len(args) {
				return usage("--status に値が必要です")
			}
			st = args[i+1]
			i++
		case "--json":
			js = true
		default:
			return usage("outbox: 不明な引数 %s", args[i])
		}
	}
	return msgRead(db, app.Human(), "message.outbox", appmessage.OutboxReq{Status: st, JSON: js}, w) // 値は API が確かめる
}

// msgRead は送信・受信の読むだけの操作を by として呼び、Text を出す。
func msgRead(db *store.DB, by port.Principal, op string, in any, w io.Writer) error {
	var r appmessage.TextResult
	if err := callAppW(db, by, io.Discard, op, in, &r); err != nil {
		return err
	}
	_, err := fmt.Fprint(w, r.Text)
	return err
}

// inboxList は cmd_inbox: 読み手は HERDR_PANE_ID + herdr agent list から決まる(自己申告にしない)。自分宛ての行だけを見る。
func inboxList(db *store.DB, args []string, w, stderr io.Writer) error {
	unread, js := false, false
	for _, a := range args {
		switch a {
		case "--unread":
			unread = true
		case "--json":
			js = true
		case "--name", "--agent", "--for", "--pane", "--session":
			return usage("inbox: 読み手は herdr から自動で決まります。%s は指定できません", a)
		default:
			return usage("inbox [--unread] [--json]")
		}
	}
	l, err := label(db, stderr)
	if err != nil {
		return err
	}
	return msgRead(db, app.PrincipalOf(l.Name), "message.inbox", appmessage.InboxReq{Unread: unread, JSON: js}, w)
}

// inboxRead は cmd_inbox-read: 1 件を全文で出し、既読にする(自分宛てでなければ「見つからない」。ほかの agent の行があるかも漏らさない)
func inboxRead(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) < 1 || !reIID.MatchString(args[0]) {
		return usage("inbox-read I-<n> [--json]")
	}
	num := strings.TrimPrefix(args[0], "I-")
	js := false
	if len(args) > 1 {
		switch args[1] {
		case "--json":
			js = true
		case "":
		default:
			return usage("inbox-read I-<n> [--json]")
		}
	}
	l, err := label(db, stderr)
	if err != nil {
		return err
	}
	// 読んだ印を付ける前の行を出す(--json の read_at は前と同じく印の前の値)
	var got appmessage.TextResult
	if err := callAppW(db, app.PrincipalOf(l.Name), io.Discard, "message.inbox.get", appmessage.InboxGetReq{ID: "I-" + num, JSON: js}, &got); err != nil {
		return err
	}
	env := appEnv(db, stderr)
	env.Reporter = func(string) (string, string, string) { return l.Name, l.Pane, l.Session }
	var rr appmessage.ReadResult
	if err := callAppEnv(env, app.Agent(l.Name), "message.read", appmessage.ReadReq{ID: "I-" + num}, &rr); err != nil {
		return err
	}
	_, err = fmt.Fprint(w, got.Text)
	return err
}
