package tbcli

// 承認待ちを 1 か所に(ADR 0007 の段 4。表 requests、ID は Q-<n>)。
// 送信(outbox)と agent の操作(agent_ops)は、元の表に pending の行ができるとトリガーがここに足す(状態の正は元の行)。
// ask で止まったコマンドは、ここに中身(引数と標準入力)ごと積み、承認されたら承認した側がそのコマンドを実行する。

import (
	"bytes"
	"database/sql"
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
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
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
	err := db.QueryRow(`SELECT id, source, source_id, requester, op, target, priority, payload, status, decided_by FROM requests WHERE id=?`, id).
		Scan(&r.id, &r.source, &r.sourceID, &r.requester, &r.op, &r.target, &r.priority, &r.payload, &r.status, &r.decidedBy)
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
func queueCmd(op, target, what string) (string, error) {
	p, err := envPriority()
	if err != nil {
		return "", err
	}
	var stdin []byte
	if stdinData != nil {
		stdin = *stdinData
	} else if !stdinIsTTY() {
		if stdin, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); err != nil {
			return "", err
		}
	}
	payload, _ := json.Marshal(map[string]any{"args": runArgs, "stdin": string(stdin)})
	db, session, done, err := permDB()
	if err != nil {
		return "", err
	}
	defer done()
	who := "human"
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		if who, err = identity.PaneOwnerName(pane); err != nil {
			return "", err
		}
	} else if identity.System != "" {
		who = identity.System
	}
	res, err := db.Exec(`INSERT INTO main.requests (herdr_session, source, requester, op, target, priority, detail, payload, created_at)
	  VALUES (?, 'cmd', ?, ?, ?, ?, ?, ?, ?)`, session, who, op, target, p, "tb "+strings.Join(runArgs, " "), string(payload), nowTS())
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
	ap, err := approverCheck(approval.NewJudge(db.DB, db.Session), "tb approval "+sub, r.act(), r.requester)
	if err != nil {
		return err
	}
	if r.status != "pending" {
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
		return nil
	}
	var pl struct {
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
	}
	if !r.payload.Valid || json.Unmarshal([]byte(r.payload.String), &pl) != nil || len(pl.Args) == 0 {
		return die("%s の中身を読めません", r.qid())
	}
	res, err := db.Exec(`UPDATE main.requests SET status='approved', decided_by=?, decided_at=? WHERE id=? AND status='pending'`, ap.name, nowTS(), r.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("%s は承認待ちではありません(ほかで決まりました)", r.qid())
	}
	fmt.Fprintf(w, "%s approved by %s: tb %s\n", r.qid(), ap.name, strings.Join(pl.Args, " "))
	flush(w)
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := exec.Command(self, append([]string{"tb"}, pl.Args...)...)
	cmd.Env = append(os.Environ(), "TB_APPROVED_REQUEST="+r.qid())
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
	_, _ = db.Exec(`UPDATE main.requests SET status=?, result=? WHERE id=?`, st, result, r.id)
	if code != 0 {
		return &exit{code, ""}
	}
	return nil
}

// approvedRun は TB_APPROVED_REQUEST の確かめ(tb approval approve が立てた子の実行)。承認した本人が、承認したコマンドを 1 回だけ実行できる。
func approvedRun(args []string) error {
	q := os.Getenv("TB_APPROVED_REQUEST")
	if q == "" {
		return nil
	}
	os.Unsetenv("TB_APPROVED_REQUEST")
	db, _, done, err := permDB()
	if err != nil {
		return err
	}
	defer done()
	r, ok, err := loadRequest(db, qidNum(q))
	if err != nil || !ok || r.source != "cmd" || r.status != "approved" {
		return die("承認済みの依頼ではありません: %s", q)
	}
	who := "human"
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		if who, err = identity.PaneOwnerName(pane); err != nil || who == "" {
			who = "human"
		}
	}
	var pl struct {
		Args []string `json:"args"`
	}
	_ = json.Unmarshal([]byte(r.payload.String), &pl)
	if r.decidedBy.String != who || strings.Join(pl.Args, "\x00") != strings.Join(args, "\x00") {
		return die("承認済みの依頼と、実行する人かコマンドが違います: %s", q)
	}
	res, err := db.Exec(`UPDATE main.requests SET status='running' WHERE id=? AND status='approved'`, r.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("承認済みの依頼はもう実行しました: %s", q)
	}
	perms.Approved = true
	return nil
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
