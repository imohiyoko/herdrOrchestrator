package tbcli

import (
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
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
	reReply = regexp.MustCompile(`^O-[0-9]{1,18}$`)
	reIID   = regexp.MustCompile(`^I-[0-9]+$`)
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
	if !reTo.MatchString(target) {
		return usage("send: 宛先は agent 名(英数字と . _ -): %s", target)
	}
	if !slices.Contains(message.SendKinds, kind) {
		return usage("send: --kind は %s のどれか", strings.Join(message.SendKinds, " "))
	}
	if blank(text) {
		return usage("send: --text が必要です")
	}
	if reply != "" && !reReply.MatchString(reply) {
		return usage("send: --reply-to は O-<n>(返信する元の送信): %s", reply)
	}
	if tid != "" {
		if !reID.MatchString(tid) {
			return usage("タスク ID は T-<project>-NNN 形式: %s", tid)
		}
		if ok, err := tickets.Exists(db.DB, tid); err != nil {
			return err
		} else if !ok {
			return die("タスクが見つかりません: %s", tid)
		}
	}
	l, err := label(db, stderr)
	if err != nil {
		return err
	}
	if target == l.Name {
		return usage("send: 自分自身には送れません")
	}
	if !liveName(target) {
		return die("send: 宛先の agent が herdr にいません: %s(保存しません)", target)
	}
	j := approval.NewJudge(db.DB, db.Session)
	status, rc := "queued", 0
	// 受け付け時の判定(返信の主張があれば返信として見る。届ける直前に、確かめた結果でもう一度決める)
	d := message.Mode(j, l.Name, target, kind, reply != "")
	reason := d.Rule
	apRule := ""
	switch {
	case d.Mode == "block" && d.Gate:
		status = "denied" // 経路そのものを止めた(既定の拒否・旧 routes)
	case d.Mode == "block": // delivery の行で止める: 承認は求めず、下の Deliver が止めて送信者に返す
	default:
		v, rule := j.Of("send:"+kind+":"+target, l.Name)
		apRule = rule
		switch v {
		case "allow":
		case "deny":
			status = "denied"
		default:
			status = "pending"
		}
		reason += " / 認可: " + rule
		// リスク評価(第 1 段: 参考値を reason に残すだけ。status は変えない)
		reason += " / " + approval.Eval(j.Collect(l.Name, target, kind, text)).Oneline()
	}
	oid, err := insertOutbox(db, l.Name, l.Pane, l.Session, target, kind, tid, text, status, reason, reply)
	if err != nil {
		return err
	}
	id := "O-" + strconv.FormatInt(oid, 10)
	if reply != "" {
		var rcheck sql.NullString
		_ = db.QueryRow(`SELECT reply_check FROM outbox WHERE id=?`, oid).Scan(&rcheck)
		if rcheck.String != "ok" && status != "denied" {
			fmt.Fprintf(stderr, "tb: --reply-to %s を返信と認めませんでした(%s)。返信ではない送信として通常の規則(delivery)で扱います: %s\n", reply, rcheck.String, id)
		}
	}
	held, blocked := "", ""
	switch status {
	case "denied":
		fmt.Fprintf(stderr, "tb: 送信を拒否しました(%s): %s\n", reason, id)
		rc = 4
	case "pending":
		fmt.Fprintf(stderr, "tb: 承認待ちです(認可: %s)。人(か approve: を許された agent)が tb approval approve %s で届けます\n", apRule, pendingQ(db.DB, "outbox", oid))
		rc = exitAsk
	default:
		r := message.Deliver(j, oid, true, io.Discard, stderr)
		rc, held, blocked = r.Code, r.Held, r.Blocked
		notice := r.Notice
		// 受け付けてから届けるまでの間に、別の tb flush が同じ行を止めた・保留したなら、その結果を送信者に返す(行を読み直す)
		if rc == 0 && blocked+held == "" {
			var st2 sql.NullString
			_ = db.QueryRow(`SELECT status || '|' || (reason LIKE '% → blocked') FROM outbox WHERE id=?`, oid).Scan(&st2)
			switch {
			case st2.String == "denied|1":
				var r2 string
				_ = db.QueryRow(`SELECT reason FROM outbox WHERE id=?`, oid).Scan(&r2)
				if i := strings.LastIndex(r2, " / "); i >= 0 {
					r2 = r2[i+3:]
				}
				r2 = strings.TrimSuffix(r2, " → blocked")
				blocked, notice = id, message.BlockNotice(db.DB, db.Session, oid, target, kind, r2)
			case strings.HasPrefix(st2.String, "sent"):
				var h sql.NullString
				_ = db.QueryRow(`SELECT 'I-'||id FROM inbox WHERE outbox_id=?`, oid).Scan(&h)
				held = h.String
			}
		}
		if rc == 0 && blocked != "" {
			fmt.Fprintf(stderr, "tb: %s\n", notice)
			rc = 4
		}
	}
	switch {
	case js:
		if err := outboxJSON(db, oid, held, blocked, w); err != nil {
			return err
		}
	case rc == 0 && held != "":
		fmt.Fprintf(w, "%s held for %s (%s)\n", id, target, held)
	case rc == 0:
		fmt.Fprintf(w, "%s sent to %s\n", id, target)
	case rc == 4 && blocked != "":
		fmt.Fprintf(w, "%s blocked by %s\n", id, target)
	case rc == 4:
		fmt.Fprintf(w, "%s denied\n", id)
	case rc == exitAsk:
		fmt.Fprintf(w, "%s pending-approval %s\n", id, pendingQ(db.DB, "outbox", oid))
	default:
		fmt.Fprintf(w, "%s queued\n", id)
	}
	if rc != 0 {
		return &exit{rc, ""}
	}
	return nil
}

// liveName は herdr agent list にその名前の agent がいるか(herdr に届かなければ いない)。
func liveName(name string) bool {
	as, err := herdr.Agents()
	if err != nil {
		return false
	}
	for _, a := range as {
		if a.Name == name {
			return true
		}
	}
	return false
}

// insertOutbox は outbox に 1 行足す。--reply-to の主張は本文ではなく DB で確かめる(1 文で決めて書く: 同時に 2 つ来ても、ok になるのは 1 つ)。
// ok の条件: 元の送信があり、「この送信の宛先 → この送信の送信者」で、届いていて(sent)、まだ ok の返信が無い
func insertOutbox(db *store.DB, from, pane, sess, target, kind, tid, text, status, reason, reply string) (int64, error) {
	var rid any
	rcheck := "NULL"
	args := []any{db.Session, from, pane, sess, target, kind, tid, text, status, reason, nowTS()}
	if reply != "" {
		n, _ := strconv.ParseInt(strings.TrimPrefix(reply, "O-"), 10, 64)
		rid = n
		rcheck = `CASE
		  WHEN ?9='denied' THEN 'ルールで拒否された送信'
		  WHEN NOT EXISTS (SELECT 1 FROM outbox WHERE id=?12) THEN '元の送信 ' || ?13 || ' が見つからない'
		  WHEN NOT EXISTS (SELECT 1 FROM outbox WHERE id=?12 AND from_name=?5 AND target=?2)
		    THEN ?13 || ' は ' || ?5 || ' から ' || ?2 || ' への送信ではない'
		  WHEN (SELECT status FROM outbox WHERE id=?12) != 'sent' THEN ?13 || ' はまだ届いていない(' || (SELECT status FROM outbox WHERE id=?12) || ')'
		  WHEN EXISTS (SELECT 1 FROM outbox WHERE reply_to=?12 AND reply_check='ok')
		    THEN ?13 || ' には返信済み(O-' || (SELECT MIN(id) FROM outbox WHERE reply_to=?12 AND reply_check='ok') || ')'
		  ELSE 'ok' END`
	}
	args = append(args, rid)
	if reply != "" {
		args = append(args, reply)
	}
	res, err := db.Exec(`INSERT INTO main.outbox (herdr_session, from_name, from_pane, from_session, target, kind, task_id, text, status, reason, created_at, reply_to, reply_check)
	  VALUES (?1, ?2, ?3, ?4, ?5, ?6, NULLIF(?7,''), ?8, ?9, ?10, ?11, ?12, `+rcheck+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// outboxJSON は send --json の 1 行(outbox の行。保留したら inbox、止めたら blocked を足す)。
func outboxJSON(db *store.DB, oid int64, held, blocked string, w io.Writer) error {
	return store.Each(db.DB, `SELECT * FROM outbox WHERE id=?`, []any{oid}, func(cols []string, vals []any) error {
		prefixID(cols, vals, "id", "O-")
		s, err := rowJSON(cols, vals)
		if err != nil {
			return err
		}
		s = strings.TrimSuffix(s, "}")
		if held != "" {
			s += `,"inbox":` + jqString(held)
		}
		if blocked != "" {
			s += `,"blocked":true`
		}
		_, err = fmt.Fprintln(w, s+"}")
		return err
	})
}

// prefixID は jq の .<列> = "<接頭辞>\(.<列>)"(NULL は "null")。
func prefixID(cols []string, vals []any, col, prefix string) {
	for i, c := range cols {
		if c == col {
			if vals[i] == nil {
				vals[i] = prefix + "null"
			} else {
				vals[i] = prefix + tickets.Text(vals[i])
			}
		}
	}
}

// flushOutbox は cmd_flush: 積まれた送信を届ける。宛先ごとに古い順で、使用中の宛先は次回に回す。
// 停止中の宛先には送らず飛ばす(revive すれば次の flush で届く)。停止中かは、最初に 1 回 herdr の一覧と台帳を突き合わせて決める
func flushOutbox(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) != 0 {
		return usage("flush(引数なし)")
	}
	var one int
	if db.QueryRow(`SELECT 1 FROM outbox WHERE status='queued' LIMIT 1`).Scan(&one) != nil {
		return nil
	}
	// 台帳を herdr の一覧と突き合わせる(台帳の stopped が古いままのことがある)。herdr に届かなければ判定せず、今までどおり送ってみる
	synced := agents.Sync(db.DB, db.Session) == nil
	type row struct {
		id     int64
		target string
	}
	var rows []row
	if err := store.Each(db.DB, `SELECT id, target FROM outbox WHERE status='queued' ORDER BY id`, nil, func(_ []string, v []any) error {
		n, _ := v[0].(int64)
		rows = append(rows, row{n, tickets.Text(v[1])})
		return nil
	}); err != nil {
		return err
	}
	j := approval.NewJudge(db.DB, db.Session)
	busy, held := map[string]bool{}, map[string]bool{}
	rc := 0
	for _, r := range rows {
		if busy[r.target] || held[r.target] { // 同じ宛先の後続は順序を保つため送らない
			continue
		}
		if synced && targetStopped(db, r.target) {
			held[r.target] = true
			flush(w)
			fmt.Fprintf(stderr, "tb: 宛先 '%s' は停止中のため送りません(queued のまま。tb agent-revive %s のあとの flush で届きます)\n", r.target, r.target)
			continue
		}
		flush(w)
		res := message.Deliver(j, r.id, false, w, stderr) // 止めた(block)ものは 0。同じ宛先の後続も続けて判定する
		if res.Code != 0 {
			busy[r.target] = true
			rc = max(rc, res.Code)
		}
	}
	if rc != 0 {
		return &exit{rc, ""}
	}
	return nil
}

// targetStopped は outbox_target_stopped: 宛先が台帳で停止中か。
func targetStopped(db *store.DB, name string) bool {
	var one int
	return db.QueryRow(`SELECT 1 FROM agents WHERE name=? AND state='stopped' LIMIT 1`, name).Scan(&one) == nil
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
	where, qa := "1=1", []any{}
	if st != "" {
		if !slices.Contains([]string{"pending", "queued", "sent", "denied"}, st) {
			return usage("outbox: --status は pending|queued|sent|denied")
		}
		where, qa = "status=?", []any{st}
	}
	if js {
		return store.Each(db.DB, "SELECT * FROM outbox WHERE ("+where+") ORDER BY id DESC LIMIT 100", qa, func(cols []string, vals []any) error {
			prefixID(cols, vals, "id", "O-")
			return emitRow(w, true, cols, vals, "")
		})
	}
	return store.Each(db.DB, `SELECT 'O-'||id, status, from_name, target, kind, COALESCE(task_id,'-'), created_at,
	     substr(replace(replace(text, char(10), ' '), char(9), ' '),1,80) FROM outbox WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa, func(cols []string, vals []any) error {
		return emitRow(w, false, cols, vals, "-")
	})
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
	where := "recipient=?"
	if unread {
		where += " AND read_at IS NULL"
	}
	if js {
		return store.Each(db.DB, "SELECT * FROM inbox WHERE "+where+" ORDER BY id", []any{l.Name}, func(cols []string, vals []any) error {
			prefixID(cols, vals, "id", "I-")
			prefixID(cols, vals, "outbox_id", "O-")
			return emitRow(w, true, cols, vals, "")
		})
	}
	return store.Each(db.DB, `SELECT 'I-'||id, CASE WHEN read_at IS NULL THEN 'unread' ELSE 'read' END, from_name, kind, COALESCE(task_id,'-'), created_at,
	     substr(replace(replace(text, char(10), ' '), char(9), ' '),1,200) FROM inbox WHERE `+where+` ORDER BY id`, []any{l.Name}, func(cols []string, vals []any) error {
		return emitRow(w, false, cols, vals, "-")
	})
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
	iid, perr := strconv.ParseInt(num, 10, 64)
	var cols []string
	var vals []any
	if perr == nil {
		if err := store.Each(db.DB, `SELECT * FROM inbox WHERE id=? AND recipient=?`, []any{iid, l.Name}, func(c []string, v []any) error {
			if cols == nil {
				cols, vals = c, v
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if cols == nil {
		return die("inbox に見つかりません: I-%s", num)
	}
	if _, err := db.Exec(`UPDATE main.inbox SET read_at=? WHERE herdr_session=? AND id=? AND recipient=? AND read_at IS NULL`, nowTS(), db.Session, iid, l.Name); err != nil {
		return err
	}
	if js {
		prefixID(cols, vals, "id", "I-")
		prefixID(cols, vals, "outbox_id", "O-")
		return emitRow(w, true, cols, vals, "")
	}
	f := map[string]string{}
	for i, c := range cols {
		if vals[i] == nil {
			f[c] = "null"
		} else {
			f[c] = tickets.Text(vals[i])
		}
	}
	task := f["task_id"]
	if task == "null" {
		task = "-"
	}
	fmt.Fprintf(w, "I-%s  from: %s / %s / %s / %s  (O-%s)\n\n%s\n", f["id"], f["from_name"], f["kind"], task, f["created_at"], f["outbox_id"], f["text"])
	return nil
}
