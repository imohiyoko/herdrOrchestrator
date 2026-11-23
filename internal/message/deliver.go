package message

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
)

// 届け方(taskboard/lib/send.sh の delivery_mode / outbox_deliver)。送信のコマンドは internal/tbcli の message.go。

// SendKinds は SEND_KINDS。
var SendKinds = []string{"task", "info", "answer"}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// projectsPairs は設定 projects の <キー>=<値> の行(空白は除く)。
func projectsPairs(db *sql.DB, session string) [][2]string {
	body, _, ok := config.Lookup(db, session, "projects")
	if !ok || body == "" {
		return nil
	}
	var out [][2]string
	for _, line := range strings.Split(body, "\n") {
		k, v, _ := strings.Cut(line, "=")
		v = util.ReadRest(v, "=")
		k, v = noSpace(k), noSpace(v)
		if k == "" || strings.HasPrefix(k, "#") || v == "" {
			continue
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

func noSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if config.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// TargetFor は target_for(project の指示役。* なら既定の宛先)。設定 projects に書いた名前だけを使い、
// どちらの行も無ければ空(宛先は決まっていない。役割の名前をコードで決め打ちしない)。
func TargetFor(db *sql.DB, session, project string) string {
	t, star, exact := "", "", ""
	for _, p := range projectsPairs(db, session) {
		if p[0] == "*" {
			star = p[1]
		}
		if p[0] == project {
			exact = p[1]
		}
	}
	if star != "" {
		t = star
	}
	if exact != "" {
		t = exact
	}
	return t
}

// ConductorNames は conductor_names(指揮官として扱う名前 = 設定 projects に書いた宛先。proposal の行は除く)。
func ConductorNames(db *sql.DB, session string) []string {
	var out []string
	if t := TargetFor(db, session, "*"); t != "" {
		out = append(out, t)
	}
	for _, p := range projectsPairs(db, session) {
		if p[0] == "proposal" || p[0] == "*" {
			continue
		}
		out = append(out, p[1])
	}
	return out
}

// Delivery は delivery_mode の結果。Gate は経路そのものを止めた(既定の拒否・旧 routes)。
type Delivery struct {
	Mode, Rule string
	Gate       bool
}

// Mode は delivery_mode <送信者> <宛先> <種別> [確かめた返信]。
func Mode(j *approval.Judge, from, to, kind string, reply bool) Delivery {
	_, _, legacy := config.Lookup(j.DB, j.Session, "routes")
	var routeReason string
	if legacy {
		ok, why := routeRules(j, from, to)
		routeReason = why
		if !ok {
			return Delivery{"block", why, true}
		}
	}
	if _, _, has := config.Lookup(j.DB, j.Session, "delivery"); has {
		rules := lines(j.DB, j.Session, "delivery") // 1 回だけ読む(返信と種別の判定で同じ版を見る)
		if reply {
			if d, ok := deliveryRules(j, rules, from, to, "reply", true); ok {
				return d
			}
		}
		if d, ok := deliveryRules(j, rules, from, to, kind, false); ok {
			return d
		}
	}
	if legacy {
		return Delivery{"now", routeReason + " / delivery: 一致する行がない(now)", false}
	}
	for _, n := range ConductorNames(j.DB, j.Session) {
		if from == n {
			return Delivery{"now", "default: conductor " + n, false}
		}
	}
	return Delivery{"block", "default: " + config.Msg(j.DB, j.Session, "send.denied.default", "{sender} は指揮官ではない(worker からの報告は tb comment、依頼はチケットを使う)", "sender="+from), true}
}

func lines(db *sql.DB, session, key string) []string {
	body, _, ok := config.Lookup(db, session, key)
	if !ok || body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

func cutHash(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// routeRules は旧 routes(互換)。
func routeRules(j *approval.Judge, from, to string) (bool, string) {
	for _, line := range lines(j.DB, j.Session, "routes") {
		line = config.Trim(cutHash(line))
		if line == "" {
			continue
		}
		neg := false
		if strings.HasPrefix(line, "!") {
			neg = true
			line = config.Trim(line[1:])
		}
		l, r, ok := strings.Cut(line, "->")
		if !ok {
			continue
		}
		l, r = config.Trim(l), config.Trim(r)
		m1, m2 := j.Roles.Match(from, l), 1
		if m1 == 0 {
			m2 = j.Roles.Match(to, r)
		}
		if m1 == 2 || m2 == 2 {
			return false, "routes.conf: " + line + " の役割を確かめられません(台帳を読めない)"
		}
		if m1 == 0 && m2 == 0 {
			if neg {
				return false, "routes.conf: !" + l + " -> " + r
			}
			return true, "routes.conf: " + l + " -> " + r
		}
	}
	return false, "routes.conf: " + from + " -> " + to + " に一致する行がない"
}

// pick は _delivery_pick: 指定の並びから種別の値。exact なら * を見ない。
func pick(spec, kind string, exact bool) (string, bool) {
	star := ""
	for _, tok := range strings.FieldsFunc(strings.ReplaceAll(spec, ",", " "), func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || (v != "now" && v != "hold" && v != "block") {
			continue
		}
		if k == kind {
			return v, true
		}
		if k == "*" && star == "" && !exact {
			star = v
		}
	}
	return star, star != ""
}

// deliveryRules は _delivery_rules。
func deliveryRules(j *approval.Judge, rules []string, from, to, kind string, exact bool) (Delivery, bool) {
	for _, line := range rules {
		line = config.Trim(cutHash(line))
		if !strings.Contains(line, "->") {
			continue
		}
		neg := false
		if strings.HasPrefix(line, "!") {
			neg = true
			line = config.Trim(line[1:])
		}
		pair, spec := line, "*=now reply=now"
		if i := strings.Index(line, "->"); i >= 0 && strings.Contains(line[i+2:], ":") {
			p, s, _ := strings.Cut(line, ":")
			pair, spec = p, config.Trim(s)
		}
		if neg {
			spec = "*=block reply=block"
		}
		l, r, _ := strings.Cut(pair, "->")
		l, r = config.Trim(l), config.Trim(r)
		v, ok := pick(spec, kind, exact)
		if !ok {
			continue
		}
		m1, m2 := j.Roles.Match(from, l), 1
		if m1 == 0 {
			if strings.HasPrefix(r, "~") {
				m2 = j.Relation(from, to, r)
			} else {
				m2 = j.Roles.Match(to, r)
			}
		}
		if m1 == 2 || m2 == 2 {
			return Delivery{"hold", "delivery: " + line + " の役割を確かめられないため保留", false}, true
		}
		if m1 != 0 || m2 != 0 {
			continue
		}
		bang := ""
		if neg {
			bang = "!"
		}
		return Delivery{v, "delivery: " + bang + l + " -> " + r + " : " + kind + "=" + v, false}, true
	}
	return Delivery{}, false
}

// SendGate は limits_send_gate: 作業中の agent が上限なら理由を stderr に出して false。
func SendGate(db *sql.DB, session, target string, stderr io.Writer) bool {
	max, _ := strconv.Atoi(limits.Load(db, session).Get("max_working_agents"))
	if max <= 0 {
		return true
	}
	as, err := herdr.Agents()
	if err != nil {
		return true
	}
	working, st, found := 0, "", false
	for _, a := range as {
		s, _ := a.AgentStatus.(string)
		if s == "working" && agents.NameRE.MatchString(a.Name) {
			working++
		}
		if a.Name == target && !found { // 同じ名前は最初の 1 件で決める
			st, found = s, true
		}
	}
	if working >= max && st != "working" {
		fmt.Fprintf(stderr, "tb: 作業中の agent が上限です(作業中 %d、max_working_agents=%d)。'%s' への送信は outbox で待たせます(tb flush で再送)\n", working, max, target)
		return false
	}
	return true
}

// Outbox は outbox の 1 行(使う欄だけ)。
type Outbox struct {
	ID                                     int64
	Status, From, Pane, Target, Kind, Text string
	TaskID, ReplyCheck, ReplyTo            sql.NullString
}

// GetOutbox は session_row outbox id <n>。無ければ ok=false。
func GetOutbox(db *sql.DB, id int64) (Outbox, bool, error) {
	var o Outbox
	var pane, text sql.NullString
	err := db.QueryRow(`SELECT id, status, from_name, from_pane, target, kind, text, task_id, reply_check, CAST(reply_to AS TEXT) FROM outbox WHERE id=?`, id).
		Scan(&o.ID, &o.Status, &o.From, &pane, &o.Target, &o.Kind, &text, &o.TaskID, &o.ReplyCheck, &o.ReplyTo)
	if err == sql.ErrNoRows {
		return o, false, nil
	}
	o.Pane, o.Text = nullStr(pane), text.String
	return o, err == nil, err
}

// nullStr は jq -r .x(null は "null")。
func nullStr(s sql.NullString) string {
	if !s.Valid {
		return "null"
	}
	return s.String
}

// Result は outbox_deliver の結果。
type Result struct {
	Code    int    // 0 送信・保留・停止 / 10 使用中 / 11 失敗 / 12 保留・停止に失敗
	Held    string // I-<n>
	Blocked string // O-<n>
	Notice  string // 止めた知らせの文
}

func sendMax() int {
	if n, err := strconv.Atoi(os.Getenv("TB_SEND_MAX")); err == nil {
		return n
	}
	return 4000
}

// Deliver は outbox_deliver(queued の行を届ける)。inline なら止めた知らせを送信者の pane に送らない。
func Deliver(j *approval.Judge, oid int64, inline bool, w, stderr io.Writer) Result {
	db, sess := j.DB, j.Session
	o, ok, err := GetOutbox(db, oid)
	if err != nil {
		fmt.Fprintf(stderr, "tb: outbox を読めません: O-%d: %v\n", oid, err)
		return Result{Code: 1}
	}
	if !ok {
		fmt.Fprintf(stderr, "tb: outbox が見つかりません: O-%d\n", oid)
		return Result{Code: 1}
	}
	id := "O-" + strconv.FormatInt(oid, 10)
	if o.Status != "queued" {
		fmt.Fprintf(w, "%s は %s です\n", id, o.Status)
		return Result{}
	}
	tid := "-"
	if o.TaskID.Valid {
		tid = o.TaskID.String
	}
	re, isReply := "", false
	if o.ReplyCheck.Valid && o.ReplyCheck.String == "ok" {
		isReply = true
		re = " re O-" + nullStr(o.ReplyTo)
	}
	msg := "[from: " + o.From + " / " + o.Pane + " / " + tid + " / " + o.Kind + " / " + id + re + "] " + util.Oneline(o.Text, sendMax())
	d := Mode(j, o.From, o.Target, o.Kind, isReply)
	switch d.Mode {
	case "block":
		var n int64
		res, err := db.Exec(`UPDATE main.outbox SET status='denied', reason=reason||' / '||?||' → blocked',
		       reply_check=CASE WHEN reply_check='ok' THEN 'delivery で止めた' ELSE reply_check END
		     WHERE herdr_session=? AND id=? AND status='queued'`, d.Rule, sess, oid)
		if err == nil {
			n, _ = res.RowsAffected()
		}
		if n != 1 {
			fmt.Fprintf(stderr, "tb: %s を止められませんでした(queued ではない)\n", id)
			return Result{Code: 12}
		}
		transport.LogMessage(db, sess, "block", id, o.Target, msg, "sent")
		r := Result{Blocked: id, Notice: blockNotice(db, sess, oid, o.Target, o.Kind, d.Rule)}
		if !inline {
			if transport.Send(db, sess, o.From, r.Notice, id, "block-notice", io.Discard) != transport.Sent {
				fmt.Fprintf(stderr, "tb: %s を止めたことを送信者 '%s' に知らせられませんでした(outbox の reason に残っています)\n", id, o.From)
			}
		}
		return r
	case "hold":
		taskID := ""
		if o.TaskID.Valid {
			taskID = o.TaskID.String
		}
		return hold(db, sess, oid, o.From, o.Target, o.Kind, taskID, o.Text, d.Rule, msg, stderr)
	}
	if !SendGate(db, sess, o.Target, stderr) {
		transport.LogMessage(db, sess, "send", id, o.Target, msg, "busy")
		return Result{Code: 10}
	}
	if rc := transport.Send(db, sess, o.Target, msg, id, "send", stderr); rc != transport.Sent {
		return Result{Code: rc}
	}
	if _, err := db.Exec(`UPDATE main.outbox SET status='sent', sent_at=? WHERE herdr_session=? AND id=?`, now(), sess, oid); err != nil {
		// bash の sql が失敗したときと同じく 1(届いてはいるので、queued のまま再送されうることを知らせる)
		fmt.Fprintf(stderr, "tb: %s は届けましたが outbox を sent にできませんでした(queued のまま。tb flush で重ねて届くことがあります): %v\n", id, err)
		return Result{Code: 1}
	}
	return Result{}
}

// BlockNotice は block_notice(止めたことを送信者に返す文)。
func BlockNotice(db *sql.DB, sess string, oid int64, target, kind, rule string) string {
	return blockNotice(db, sess, oid, target, kind, rule)
}

func blockNotice(db *sql.DB, sess string, oid int64, target, kind, rule string) string {
	n := strconv.FormatInt(oid, 10)
	return "[tb: blocked / O-" + n + "] " + config.Msg(db, sess, "send.blocked.notice",
		"{target} への送信 O-{id}({kind})は {target} の受信設定で止められました({rule})。{target} には届けていません。依頼や相談はチケットにしてください(tb add -p <project> -t <題> --desc <内容>。進み具合は tb comment で返ってきます)",
		"id="+n, "target="+target, "kind="+kind, "rule="+rule)
}

// hold は outbox_hold: inbox に置き、outbox を sent(届け先 inbox)にする。1 つのトランザクション。
func hold(db *sql.DB, sess string, oid int64, from, to, kind, task, text, rule, msg string, stderr io.Writer) Result {
	id := "O-" + strconv.FormatInt(oid, 10)
	iid, err := func() (string, error) {
		ctx := context.Background()
		c, err := db.Conn(ctx)
		if err != nil {
			return "", err
		}
		defer c.Close()
		if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return "", err
		}
		ok := false
		defer func() {
			if !ok {
				_, _ = c.ExecContext(ctx, "ROLLBACK")
			}
		}()
		ts := now()
		if _, err := c.ExecContext(ctx, `INSERT INTO main.inbox (herdr_session, outbox_id, recipient, from_name, kind, task_id, text, rule, created_at)
		    SELECT ?, ?, ?, ?, ?, NULLIF(?,''), ?, ?, ? WHERE EXISTS (SELECT 1 FROM outbox WHERE id=? AND status='queued')`,
			sess, oid, to, from, kind, task, text, rule, ts, oid); err != nil {
			return "", err
		}
		if _, err := c.ExecContext(ctx, `UPDATE main.outbox SET status='sent', sent_at=?, reason=reason||' / '||?||' → inbox I-'||(SELECT id FROM inbox WHERE outbox_id=?)
		    WHERE herdr_session=? AND id=? AND status='queued' AND EXISTS (SELECT 1 FROM inbox WHERE outbox_id=?)`, ts, rule, oid, sess, oid, oid); err != nil {
			return "", err
		}
		var iid sql.NullString
		if err := c.QueryRowContext(ctx, `SELECT 'I-'||id FROM inbox WHERE outbox_id=?`, oid).Scan(&iid); err != nil && err != sql.ErrNoRows {
			return "", err
		}
		if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
			return "", err
		}
		ok = true
		return iid.String, nil
	}()
	if err != nil || !strings.HasPrefix(iid, "I-") {
		why := id + " は queued ではありません"
		if err != nil {
			why = err.Error()
		}
		transport.LogMessage(db, sess, "hold", id, to, msg, "failed")
		fmt.Fprintf(stderr, "tb: %s を inbox に保留できませんでした(送っていません。queued のまま): %s\n", id, why)
		return Result{Code: 12}
	}
	transport.LogMessage(db, sess, "hold", id, to, msg, "sent")
	return Result{Held: iid}
}
