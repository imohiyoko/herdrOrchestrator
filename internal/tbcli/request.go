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
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	keyHex := hex.EncodeToString(key)
	res, err := db.Exec(`UPDATE main.requests SET status='approved', decided_by=?, decided_at=?, run_key=? WHERE id=? AND status='pending'`,
		ap.name, nowTS(), keyHash(keyHex), r.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("%s は承認待ちではありません(ほかで決まりました)", r.qid())
	}
	fmt.Fprintf(w, "%s approved by %s: %s として実行します: tb %s\n", r.qid(), ap.name, r.requester, strings.Join(pl.Args, " "))
	flush(w)
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
	_, _ = db.Exec(`UPDATE main.requests SET status=?, result=? WHERE id=?`, st, result, r.id)
	if code != 0 {
		return &exit{code, ""}
	}
	return nil
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
