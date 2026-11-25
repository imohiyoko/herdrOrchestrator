package tbcli

// 承認待ちを 1 か所に(ADR 0007 の段 4。表 requests、ID は Q-<n>)。
// 送信(outbox)と agent の操作(agent_ops)は、元の表に pending の行ができるとトリガーがここに足す(状態の正は元の行)。
// ask で止まったコマンドは、ここに中身(引数と標準入力)ごと積み、承認されたら依頼者の権限で機械的に実行する(承認した側の権限は使わない)。

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
)

var reQID = regexp.MustCompile(`^Q-[0-9]+$`)

// runArgs は今の tb の引数(承認待ちに積むコマンド)。
var runArgs []string

// reqPending は承認待ちの行(元の行の状態を見る)。
const reqPending = `((r.source='cmd' AND r.status='pending')
  OR (r.source='outbox' AND EXISTS (SELECT 1 FROM outbox o WHERE o.id=r.source_id AND o.status='pending'))
  OR (r.source='agent_ops' AND EXISTS (SELECT 1 FROM agent_ops a WHERE a.id=r.source_id AND a.status='pending')))`

var reqPriorities = map[string]int{"low": 0, "normal": 1, "high": 2}

func priorityName(n int) string {
	for k, v := range reqPriorities {
		if v == n {
			return k
		}
	}
	return "normal"
}

// envPriority は TB_PRIORITY(high / normal / low。無ければ normal)。
func envPriority() (int, error) {
	v := os.Getenv("TB_PRIORITY")
	if v == "" {
		return 1, nil
	}
	p, ok := reqPriorities[v]
	if !ok {
		return 0, fmt.Errorf("TB_PRIORITY は high / normal / low のどれかです: %s", v)
	}
	return p, nil
}

type request struct {
	id                    int64
	source                string
	sourceID              sql.NullInt64
	requester, op, target string
	priority              int
	payload               sql.NullString
	status                string
	decidedBy             sql.NullString
	tier                  string
}

func (r request) qid() string { return "Q-" + strconv.FormatInt(r.id, 10) }

// act は承認の権限の行(approve:<act> <依頼者>)に使う操作名(対象が * なら操作名だけ)。
func (r request) act() string {
	if r.target == "" || r.target == "*" {
		return r.op
	}
	return r.op + ":" + r.target
}

func loadRequest(db *sql.DB, id int64) (request, bool, error) {
	var r request
	err := db.QueryRow(`SELECT id, source, source_id, requester, op, target, priority, payload, status, decided_by, tier FROM requests WHERE id=?`, id).
		Scan(&r.id, &r.source, &r.sourceID, &r.requester, &r.op, &r.target, &r.priority, &r.payload, &r.status, &r.decidedBy, &r.tier)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func qidNum(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "Q-"), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// qidOf は元の行(outbox / agent_ops)の承認待ちの ID(無ければ空)。
func qidOf(db *sql.DB, source string, sid int64) string {
	var id int64
	if db.QueryRow(`SELECT id FROM requests WHERE source=? AND source_id=?`, source, sid).Scan(&id) != nil {
		return ""
	}
	return "Q-" + strconv.FormatInt(id, 10)
}

// setSourcePriority は TB_PRIORITY を、今積んだ送信・agent の操作の承認待ちに付ける。
func setSourcePriority(db *sql.DB, source string, sid int64) {
	if p, err := envPriority(); err == nil && p != 1 {
		_, _ = db.Exec(`UPDATE main.requests SET priority=? WHERE source=? AND source_id=?`, p, source, sid)
	}
}

// pendingQ は今積んだ送信・agent の操作の承認待ちの ID(TB_PRIORITY があれば優先度も付ける)。
func pendingQ(db *sql.DB, source string, sid int64) string {
	setSourcePriority(db, source, sid)
	return qidOf(db, source, sid)
}

// queueCmd は perms.Queue: ask で止まったコマンドを、引数と標準入力ごと積む。
func queueCmd(op, target, rule, what string) (string, error) {
	return insertCmd("ask", op, target, rule, true)
}

// recordCmd は perms.Record: agent の呼び出しの allow / deny を決まった行として残す(allow は標準入力を読まない)。
func recordCmd(tier, op, target, rule, what string) string {
	id, err := insertCmd(tier, op, target, rule, tier == "deny")
	if err != nil {
		if tier == "deny" { // deny は止まるので、残せなかったことだけ伝える
			fmt.Fprintf(os.Stderr, "tb: 判定の記録を残せませんでした(%s)\n", err)
		}
		return ""
	}
	return id
}

// callerName は今の呼び出し元(pane の agent 名。人は human、scheduler などは system:<名前>)。
// 1 回の tb の中では pane の持ち主は変わらないので、herdr に聞くのは pane ごとに 1 回だけにする(allow の記録で毎回聞かない)。
var callerCache = map[string]string{}

func callerName() (string, error) {
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		if n, ok := callerCache[pane]; ok {
			return n, nil
		}
		n, err := identity.PaneOwnerName(pane)
		if err != nil {
			return "", err
		}
		if n == "" {
			n = "human"
		}
		callerCache[pane] = n
		return n, nil
	}
	if identity.System != "" {
		return identity.System, nil
	}
	return "human", nil
}

// insertCmd は requests にコマンドの 1 行を足す。ask は pending、allow は allowed(policy が許可)、deny は denied(policy が拒否)。
func insertCmd(tier, op, target, rule string, withStdin bool) (string, error) {
	p, err := envPriority()
	if err != nil {
		if tier != "ask" {
			p = 1
		} else {
			return "", err
		}
	}
	var stdin []byte
	if withStdin {
		if stdinData != nil {
			stdin = *stdinData
		} else if !stdinIsTTY() {
			if stdin, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); err != nil {
				return "", err
			}
		}
	}
	who, err := callerName()
	if err != nil {
		return "", err
	}
	if who == "human" && tier != "ask" {
		return "", nil // 人の端末は記録しない(人は常に通る)
	}
	if tier == "allow" && strings.HasPrefix(who, "system:") {
		return "", nil // scheduler などの定期の呼び出しの allow は残さない(数が多く、誰が依頼したかの記録にならない。ask / deny は残す)
	}
	db, session, done, err := permDB()
	if err != nil {
		return "", err
	}
	defer done()
	status, decidedBy, decidedAt := "pending", sql.NullString{}, sql.NullString{}
	switch tier {
	case "allow":
		status = "allowed"
	case "deny":
		status = "denied"
	}
	if tier != "ask" {
		decidedBy = sql.NullString{String: "policy", Valid: true}
		decidedAt = sql.NullString{String: nowTS(), Valid: true}
	}
	pl := map[string]any{"args": runArgs}
	if withStdin {
		pl["stdin"] = string(stdin)
	}
	detail := "tb " + strings.Join(runArgs, " ")
	if words, ok := convWords(runArgs); ok {
		// 会話(tb conv)の文面は DB・記録に書かない約束(README)。引数と標準入力は残さず、コマンドの語だけ残す(承認しても実行できない)
		pl = map[string]any{"args": []string{}, "redacted": true}
		detail = "tb " + strings.Join(words, " ") + "(会話の文面を含みうるため、引数と標準入力は残していません)"
	}
	payload, _ := json.Marshal(pl)
	res, err := db.Exec(`INSERT INTO main.requests (herdr_session, source, requester, op, target, priority, detail, payload, status, tier, rule, decided_by, decided_at, created_at)
	  VALUES (?, 'cmd', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, session, who, op, target, p, detail, string(payload),
		status, tier, rule, decidedBy, decidedAt, nowTS())
	if err != nil {
		return "", err
	}
	id, _ := res.LastInsertId()
	return "Q-" + strconv.FormatInt(id, 10), nil
}

// approvalList は tb approval list(承認待ち Q-<n>。優先度の高い順、同じなら古い順)。
func approvalList(db *store.DB, args []string, w io.Writer) error {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return usage("approval list: 不明な引数 %s", a)
		}
		asJSON = true
	}
	q := `SELECT 'Q-'||r.id AS id, CASE r.priority WHEN 2 THEN 'high' WHEN 0 THEN 'low' ELSE 'normal' END AS priority,
	  r.op, r.requester AS requested_by, r.target, r.created_at, r.detail
	  FROM requests r WHERE ` + reqPending + ` ORDER BY r.priority DESC, r.id`
	if asJSON {
		rows, err := sqlObjs(db.DB, nil, q)
		if err != nil {
			return err
		}
		compactRows(w, rows)
		return nil
	}
	return qrows(db.DB, `SELECT id, priority, op, requested_by, target, created_at FROM (`+q+`)`, nil, func(_ []string, vals []any) error {
		s := make([]string, len(vals))
		for i, v := range vals {
			s[i] = fmt.Sprint(v)
		}
		_, err := fmt.Fprintln(w, strings.Join(s, "\t"))
		return err
	})
}

// requestDecide は tb approval approve|reject Q-<n>: 元の表の承認・却下に渡すか、積んだコマンドを決める。
func requestDecide(sub string, args []string, w, stderr io.Writer) error {
	if len(args) < 1 || !reQID.MatchString(args[0]) {
		return usage("%s Q-<n>", sub)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	r, ok, err := loadRequest(db.DB, qidNum(args[0]))
	if err != nil {
		db.Close()
		return err
	}
	if !ok {
		db.Close()
		if _, err := approverCheck(approval.NewJudge(nil, ""), "tb approval "+sub, "", ""); err != nil {
			return err
		}
		return die("承認待ちが見つかりません: %s", args[0])
	}
	rest := args[1:]
	switch r.source {
	case "outbox":
		defer db.Close()
		a := append([]string{"O-" + strconv.FormatInt(r.sourceID.Int64, 10)}, rest...)
		if sub == "approve" {
			return approve(db, a, w, stderr)
		}
		if len(rest) > 0 {
			return usage("reject Q-<n>")
		}
		return reject(db, a, w)
	case "agent_ops":
		db.Close()
		a := append([]string{"L-" + strconv.FormatInt(r.sourceID.Int64, 10)}, rest...)
		if err := permErr(perms.Gate(append([]string{"agent-" + sub}, a...), permDB)); err != nil {
			return err
		}
		if sub == "approve" {
			return cmdAgentApprove(a, w, stderr)
		}
		return cmdAgentReject(a, w, stderr)
	}
	defer db.Close()
	if len(rest) > 0 {
		return usage("%s Q-<n>(コマンドの承認待ちに --always はありません)", sub)
	}
	// deny を開けるのは人だけ(approve: の権限を持つ agent にも開けさせない)
	act := r.act()
	if r.status == "denied" {
		act = ""
	}
	ap, err := approverCheck(approval.NewJudge(db.DB, db.Session), "tb approval "+sub, act, r.requester)
	if err != nil {
		return err
	}
	from := "pending"
	switch {
	case r.status == "pending":
	case r.status == "denied" && sub == "approve":
		from = "denied"
	case r.status == "denied":
		return die("%s は policy が拒否した記録です(もう却下されています)", r.qid())
	default:
		return die("%s は承認待ちではありません(%s)", r.qid(), r.status)
	}
	if sub == "reject" {
		res, err := db.Exec(`UPDATE main.requests SET status='rejected', decided_by=?, decided_at=? WHERE id=? AND status='pending'`, ap.name, nowTS(), r.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return die("%s は承認待ちではありません(ほかで決まりました)", r.qid())
		}
		fmt.Fprintf(w, "%s rejected\n", r.qid())
		notifyRequester(db, r, ap, r.qid()+" は "+ap.name+" に却下されました(実行していません): "+r.detailLine())
		return nil
	}
	pl, err := r.cmdPayload()
	if err != nil {
		return err
	}
	if handBack(r.requester) {
		// 依頼者が herdr にいなければ承認しない(知らせが届かず、run できるのも依頼者だけなので、承認しても使われない)
		alive, err := agentAlive(r.requester)
		if err != nil {
			return die("herdr agent list に失敗したため、依頼者 %s がいるか確かめられません(%s は承認していません)", r.requester, r.qid())
		}
		if !alive {
			if from == "denied" {
				return die("依頼者 %s が herdr にいないため承認しません(%s は deny の記録のまま。%s を復活させてから承認してください)", r.requester, r.qid(), r.requester)
			}
			return die("依頼者 %s が herdr にいないため承認しません(%s は承認待ちのまま。tb approval reject %s で却下するか、%s を復活させてから承認してください)", r.requester, r.qid(), r.qid(), r.requester)
		}
	}
	res, err := db.Exec(`UPDATE main.requests SET status='approved', decided_by=?, decided_at=? WHERE id=? AND status=?`, ap.name, nowTS(), r.id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("%s は承認待ちではありません(ほかで決まりました)", r.qid())
	}
	r.status = "approved"
	if handBack(r.requester) {
		// 依頼した agent に知らせ、依頼者が自分で 1 回だけ実行する(tb approval run)
		fmt.Fprintf(w, "%s approved by %s: 依頼者 %s に知らせました(%s が tb approval run %s で 1 回だけ実行します): tb %s\n",
			r.qid(), ap.name, r.requester, r.requester, r.qid(), strings.Join(pl.Args, " "))
		if !notifyRequester(db, r, ap, r.qid()+" は "+ap.name+" に承認されました。tb approval run "+r.qid()+" で 1 回だけ実行できます(実行するコマンド: tb "+strings.Join(pl.Args, " ")+")") {
			fmt.Fprintf(stderr, "tb: 依頼者 %s に知らせを届けられませんでした(使用中か不在。%s は承認済みのまま。依頼者は tb approval history --mine で見られます)\n", r.requester, r.qid())
		}
		return nil
	}
	// 知らせる相手がいない依頼者(scheduler などの system:<名前>、人)は、承認した側がその場で依頼者として実行する
	fmt.Fprintf(w, "%s approved by %s: %s として実行します: tb %s\n", r.qid(), ap.name, r.requester, strings.Join(pl.Args, " "))
	flush(w)
	return runApproved(db, r, pl, w, stderr)
}

// 送信(O-<n>)と agent の操作(L-<n>)の承認・却下は、どの入口(Q-<n> / O-<n> / L-<n> / tb agent-approve)から決めても、
// 決まったら 1 回だけ依頼者に知らせる(下の 4 つが入口。中身は approveImpl などのまま)。

func approve(db *store.DB, args []string, w, stderr io.Writer) error {
	err := approveImpl(db, args, w, stderr)
	decidedNotice(db, "outbox", args, "approve", err)
	return err
}

func reject(db *store.DB, args []string, w io.Writer) error {
	err := rejectImpl(db, args, w)
	decidedNotice(db, "outbox", args, "reject", err)
	return err
}

func cmdAgentApprove(args []string, w, stderr io.Writer) error {
	err := agentApproveImpl(args, w, stderr)
	agentDecidedNotice(args, "approve", err)
	return err
}

func cmdAgentReject(args []string, w, stderr io.Writer) error {
	err := agentRejectImpl(args, w, stderr)
	agentDecidedNotice(args, "reject", err)
	return err
}

func agentDecidedNotice(args []string, sub string, err error) {
	db, _, oerr := openCmd(io.Discard)
	if oerr != nil {
		return
	}
	defer db.Close()
	decidedNotice(db, "agent_ops", args, sub, err)
}

// decidedNotice は、この呼び出しで決まった送信・agent の操作の承認待ちを、依頼した agent に知らせる(決まらなかったら送らない)。
// 文面は元の行の今の状態から作る(届いた・使用中で未送・経路で止めた・inbox に置いた、を言い分ける)。
func decidedNotice(db *store.DB, source string, args []string, sub string, err error) {
	var ex *exit
	if err != nil && !(source == "outbox" && sub == "approve" && errors.As(err, &ex) && (ex.code == 11 || ex.code == 12)) {
		return // 承認・却下そのものが通らなかった(送信の承認の 11 / 12 は、承認済みで届けるところだけ失敗)
	}
	if len(args) == 0 {
		return
	}
	sid, perr := strconv.ParseInt(strings.TrimLeft(args[0], "OL-"), 10, 64)
	if perr != nil {
		return
	}
	var qid int64
	if db.QueryRow(`SELECT id FROM requests WHERE source=? AND source_id=?`, source, sid).Scan(&qid) != nil {
		return // 承認待ちの記録が無い(古い行)。知らせる相手が分からない
	}
	r, ok, lerr := loadRequest(db.DB, qid)
	if lerr != nil || !ok {
		return
	}
	who, cerr := callerName()
	if cerr != nil {
		who = "?"
	}
	what, state := "agent の操作", ""
	if source == "outbox" {
		what = "送信"
		var st, reason string
		_ = db.QueryRow(`SELECT status, COALESCE(reason, '') FROM outbox WHERE id=?`, sid).Scan(&st, &reason)
		switch {
		case sub == "reject":
			state = "却下されました(送っていません)"
		case st == "sent" && strings.Contains(reason, "→ inbox I-"):
			state = "承認され、宛先の inbox に置かれました(宛先が受け取るまで待ちます)"
		case st == "sent":
			state = "承認され、届けました"
		case st == "denied":
			return // 承認のあと送信の経路(delivery)で止めた。送信者には止めた知らせ(tb: blocked)が届くので、重ねて送らない
		default:
			state = "承認されましたが、まだ届けていません(宛先が使用中か届けられませんでした。queued のまま、届けられるようになれば tb flush で届きます)"
		}
	} else {
		var st string
		_ = db.QueryRow(`SELECT status FROM agent_ops WHERE id=?`, sid).Scan(&st)
		switch {
		case sub == "reject":
			state = "却下されました(実行していません)"
		case st == "done":
			state = "承認され、実行されました"
		default:
			state = "承認されましたが、実行に失敗しました(" + st + ")"
		}
	}
	notifyRequester(db, r, approver{name: who}, fmt.Sprintf("%s(%s %s %s)は %s に%s", r.qid(), what, r.op, r.target, who, state))
}

type cmdPayload struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

// convWords は tb conv の呼び出しなら、文面を含まないコマンドの語(conv <サブコマンド> / conv-<名前>)を返す。
func convWords(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}
	if strings.HasPrefix(args[0], "conv-") {
		return args[:1], true
	}
	if args[0] == "conv" {
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			return args[:2], true
		}
		return args[:1], true
	}
	return nil, false
}

func (r request) cmdPayload() (cmdPayload, error) {
	var pl cmdPayload
	var red struct {
		Redacted bool `json:"redacted"`
	}
	if r.payload.Valid && json.Unmarshal([]byte(r.payload.String), &red) == nil && red.Redacted {
		return pl, die("%s は会話の文面を含みうるため中身を残していません(承認しても実行できません。人が自分の端末で実行するか、却下してください)", r.qid())
	}
	if !r.payload.Valid || json.Unmarshal([]byte(r.payload.String), &pl) != nil || len(pl.Args) == 0 {
		return pl, die("%s の中身を読めません", r.qid())
	}
	return pl, nil
}

// detailLine は知らせの文面に入れる依頼の中身(1 行、長ければ切る)。
func (r request) detailLine() string {
	var d string
	if pl, err := r.cmdPayload(); err == nil {
		d = "tb " + strings.Join(pl.Args, " ")
	} else {
		d = r.op
	}
	d = strings.ReplaceAll(d, "\n", " ")
	if len([]rune(d)) > 200 {
		d = string([]rune(d)[:200]) + "…"
	}
	return d
}

// agentAlive は名前の agent が herdr にいるか。
func agentAlive(name string) (bool, error) {
	as, err := herdr.Agents()
	if err != nil {
		return false, err
	}
	for _, a := range as {
		if a.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// handBack は承認の結果を依頼者に返して、依頼者が自分で実行する形にするか(pane のある agent だけ)。
func handBack(requester string) bool {
	return requester != "human" && requester != "" && !strings.HasPrefix(requester, "system:")
}

// notifyRequester は決まったことを依頼した agent の pane に直接 1 通送る(tb からの知らせ。agent 同士の送信の経路 routes / delivery は通さない)。
// 記録は messages(kind=approval、ref=Q-<n>)。宛先が使用中・不在なら送らずに false(依頼者は tb approval history --mine で見られる)。
// 人・system: の依頼者には送らない。
func notifyRequester(db *store.DB, r request, ap approver, text string) bool {
	if !handBack(r.requester) {
		return true
	}
	msg := "[tb approval / " + r.qid() + " / " + ap.name + "] " + text
	if transport.Send(db.DB, db.Session, r.requester, msg, r.qid(), "approval", io.Discard) != transport.Sent {
		return false
	}
	_, _ = db.Exec(`UPDATE main.requests SET notified_at=? WHERE id=?`, nowTS(), r.id)
	return true
}

// approvalRun は tb approval run Q-<n>: 承認された自分の依頼を、自分で 1 回だけ実行する(依頼した agent だけ)。
func approvalRun(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) != 1 || !reQID.MatchString(args[0]) {
		return usage("approval run Q-<n>")
	}
	r, ok, err := loadRequest(db.DB, qidNum(args[0]))
	if err != nil {
		return err
	}
	who, err := callerName()
	if err != nil {
		return die("herdr agent list に失敗したため、実行者を特定できません")
	}
	if !ok || r.source != "cmd" || who != r.requester {
		return die("%s を実行できるのは、依頼した agent だけです", args[0])
	}
	switch r.status {
	case "approved":
	case "pending":
		return die("%s はまだ承認されていません", r.qid())
	case "running", "done", "failed":
		return die("%s はもう実行しました(%s。ワンタイムのため 2 回目は実行しません。もう一度要るなら、もう一度依頼してください)", r.qid(), r.status)
	default:
		return die("%s は承認されていません(%s)", r.qid(), r.status)
	}
	pl, err := r.cmdPayload()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s を実行します(%s が承認): tb %s\n", r.qid(), r.decidedBy.String, strings.Join(pl.Args, " "))
	flush(w)
	return runApproved(db, r, pl, w, stderr)
}

// runApproved は承認済みの依頼を子として 1 回だけ実行する(子は鍵が合うときだけ、依頼者として動く。結果を Q-<n> に残す)。
func runApproved(db *store.DB, r request, pl cmdPayload, w, stderr io.Writer) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	keyHex := hex.EncodeToString(key)
	res, err := db.Exec(`UPDATE main.requests SET run_key=? WHERE id=? AND status='approved' AND run_key IS NULL`, keyHash(keyHex), r.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("%s はもう実行しています", r.qid())
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := exec.Command(self, append([]string{"tb"}, pl.Args...)...)
	// 承認した側の pane・呼び出し元は渡さない(子は依頼者として動く。承認した側の権限は使わない)
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "HERDR_PANE_ID" && k != "TB_SYSTEM" && k != "TB_APPROVED_REQUEST" && k != "TB_APPROVED_KEY" {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "TB_APPROVED_REQUEST="+r.qid(), "TB_APPROVED_KEY="+keyHex)
	cmd.Stdin = strings.NewReader(pl.Stdin)
	cmd.Stdout = io.MultiWriter(w, &out)
	cmd.Stderr = io.MultiWriter(stderr, &out)
	runErr := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		code = ee.ExitCode()
	} else if runErr != nil {
		code = 1
	}
	st := "done"
	if code != 0 {
		st = "failed"
	}
	result := fmt.Sprintf("exit %d", code)
	if o := strings.TrimSpace(out.String()); o != "" {
		if len(o) > 2000 {
			o = o[len(o)-2000:]
		}
		result += "\n" + o
	}
	_, _ = db.Exec(`UPDATE main.requests SET status=?, result=?, run_key=NULL WHERE id=?`, st, result, r.id)
	if code != 0 {
		return &exit{code, ""}
	}
	return nil
}

// approvalHistory は tb approval history: allow / ask / deny の記録(新しい順)。誰が依頼し、何が・誰が決め、どうなったか。
func approvalHistory(db *store.DB, args []string, w io.Writer) error {
	asJSON, by, limit := false, "", 50
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--json":
			asJSON = true
		case "--mine":
			who, err := callerName()
			if err != nil {
				return die("herdr agent list に失敗したため、実行者を特定できません")
			}
			by = who
		case "--by", "--limit":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			v := args[i+1]
			i++
			if a == "--by" {
				by = v
			} else {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 1000 {
					return usage("--limit は 1〜1000")
				}
				limit = n
			}
		default:
			return usage("approval history: 不明な引数 %s", a)
		}
	}
	// 送信・agent の操作は元の行の状態を見せる
	q := `SELECT 'Q-'||r.id AS id, r.tier, CASE r.source
	    WHEN 'outbox' THEN COALESCE((SELECT o.status FROM outbox o WHERE o.id=r.source_id), r.status)
	    WHEN 'agent_ops' THEN COALESCE((SELECT a.status FROM agent_ops a WHERE a.id=r.source_id), r.status)
	    ELSE r.status END AS status,
	  r.requester AS requested_by, r.op, r.target, COALESCE(r.decided_by, '') AS decided_by, COALESCE(r.rule, '') AS rule,
	  r.created_at, COALESCE(r.decided_at, '') AS decided_at, r.detail, COALESCE(r.result, '') AS result
	  FROM requests r WHERE r.herdr_session=? AND (?='' OR r.requester=?) ORDER BY r.id DESC LIMIT ?`
	// 実行の結果(出力の末尾)は、依頼者本人と人にだけ見せる(ほかの agent の出力を読ませない)
	viewer, err := callerName()
	if err != nil {
		return die("herdr agent list に失敗したため、実行者を特定できません")
	}
	q = strings.Replace(q, `COALESCE(r.result, '') AS result`, `CASE WHEN ?='human' OR r.requester=? THEN COALESCE(r.result, '') ELSE '' END AS result`, 1)
	qa := []any{viewer, viewer, db.Session, by, by, limit}
	if asJSON {
		rows, err := sqlObjs(db.DB, nil, q, qa...)
		if err != nil {
			return err
		}
		compactRows(w, rows)
		return nil
	}
	return qrows(db.DB, `SELECT id, tier, status, requested_by, op, target, decided_by, created_at FROM (`+q+`)`, qa, func(_ []string, vals []any) error {
		s := make([]string, len(vals))
		for i, v := range vals {
			s[i] = fmt.Sprint(v)
			if s[i] == "" {
				s[i] = "-"
			}
		}
		_, err := fmt.Fprintln(w, strings.Join(s, "\t"))
		return err
	})
}

func keyHash(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:])
}

// approvedRun は tb approval approve が立てた子(承認したコマンドの機械的な実行)の確かめ。
// 鍵が合い、承認済みでまだ実行していないときだけ、依頼者として 1 回だけ動く(呼び出し元は依頼者、ask は通す、deny は通さない)。
// 承認した側の権限は使わない(承認する権限しか持たない承認者でも、依頼者の権限で実行される)。
func approvedRun(args []string) (bool, error) {
	q, key := os.Getenv("TB_APPROVED_REQUEST"), os.Getenv("TB_APPROVED_KEY")
	if q == "" && key == "" {
		return false, nil
	}
	os.Unsetenv("TB_APPROVED_REQUEST")
	os.Unsetenv("TB_APPROVED_KEY")
	db, _, done, err := permDB()
	if err != nil {
		return false, err
	}
	defer done()
	r, ok, err := loadRequest(db, qidNum(q))
	if err != nil || !ok || r.source != "cmd" || r.status != "approved" || key == "" {
		return false, die("承認済みの依頼ではありません: %s", q)
	}
	var pl struct {
		Args []string `json:"args"`
	}
	_ = json.Unmarshal([]byte(r.payload.String), &pl)
	if strings.Join(pl.Args, "\x00") != strings.Join(args, "\x00") {
		return false, die("承認済みの依頼とコマンドが違います: %s", q)
	}
	res, err := db.Exec(`UPDATE main.requests SET status='running', run_key=NULL WHERE id=? AND status='approved' AND run_key=?`, r.id, keyHash(key))
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, die("承認済みの依頼の鍵が合わないか、もう実行しました: %s", q)
	}
	// 依頼者として動く
	identity.System = ""
	os.Unsetenv("HERDR_PANE_ID")
	switch {
	case r.requester == "human":
	case strings.HasPrefix(r.requester, "system:"):
		identity.System = r.requester
	default:
		as, err := herdr.Agents()
		if err != nil {
			return false, die("herdr agent list に失敗したため、依頼者 %s として実行できません", r.requester)
		}
		pane := ""
		for _, a := range as {
			if a.Name == r.requester {
				pane = a.PaneID
			}
		}
		if pane == "" {
			return false, die("依頼者 %s がもういないため実行できません(%s)", r.requester, q)
		}
		os.Setenv("HERDR_PANE_ID", pane)
	}
	perms.Approved = true
	if r.tier == "deny" {
		perms.ApprovedDeny = r.op // 人が例外として承認した deny の操作だけ通す
	}
	return true, nil
}

// requestPriority は tb approval priority Q-<n> high|normal|low(依頼者か人だけ)。
func requestPriority(args []string, w, stderr io.Writer) error {
	if len(args) != 2 || !reQID.MatchString(args[0]) {
		return usage("approval priority Q-<n> high|normal|low")
	}
	p, ok := reqPriorities[args[1]]
	if !ok {
		return usage("優先度は high / normal / low のどれかです: %s", args[1])
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	r, found, err := loadRequest(db.DB, qidNum(args[0]))
	if err != nil {
		return err
	}
	who := "human"
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		if who, err = identity.PaneOwnerName(pane); err != nil {
			return die("herdr agent list に失敗したため、実行者を特定できません")
		}
		if who == "" {
			who = "human"
		}
	}
	if !found || (who != "human" && who != r.requester) {
		return die("%s の優先度を変えられるのは、依頼した agent と人だけです", args[0])
	}
	if _, err := db.Exec(`UPDATE main.requests SET priority=? WHERE id=?`, p, r.id); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s priority %s\n", r.qid(), priorityName(p))
	return nil
}
