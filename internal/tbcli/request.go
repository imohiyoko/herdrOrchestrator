package tbcli

// 承認待ちを 1 か所に(ADR 0007 の段 4。表 requests、ID は Q-<n>)。
// 送信(outbox)と agent の操作(agent_ops)は、元の表に pending の行ができるとトリガーがここに足す(状態の正は元の行)。
// ask で止まったコマンドは、ここに中身(引数と標準入力)ごと積み、承認されたら依頼者の権限で機械的に実行する(承認した側の権限は使わない)。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	appapproval "github.com/imohiyoko/herdrorchestrator/internal/app/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

var reQID = requests.ReQID

// request は承認待ちの 1 行(requests.Request)。
type request = requests.Request

func loadRequest(db *sql.DB, id int64) (request, bool, error) { return requests.Load(db, id) }

func qidNum(s string) int64 { return requests.Num(s) }

// runArgs は今の tb の引数(承認待ちに積むコマンド)。
var runArgs []string

// envPriority は TB_PRIORITY(high / normal / low。無ければ normal)。
func envPriority() (int, error) {
	v := os.Getenv("TB_PRIORITY")
	if v == "" {
		return 1, nil
	}
	p, ok := appapproval.Priorities[v]
	if !ok {
		return 0, fmt.Errorf("TB_PRIORITY は high / normal / low のどれかです: %s", v)
	}
	return p, nil
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
	if words, ok := requests.ConvWords(runArgs); ok {
		// 会話(tb conv)の文面は DB・記録に書かない約束(README)。引数と標準入力は残さず、コマンドの語だけ残す(承認しても実行できない)
		pl = map[string]any{"args": []string{}, "redacted": true}
		detail = "tb " + strings.Join(words, " ") + "(会話の文面を含みうるため、引数と標準入力は残していません)"
	}
	payload, _ := json.Marshal(pl)
	id, err := requests.InsertCmd(db, requests.CmdRow{Session: session, Requester: who, Op: op, Target: target, Priority: p, Detail: detail,
		Payload: string(payload), Status: status, Tier: tier, Rule: rule, DecidedBy: decidedBy, DecidedAt: decidedAt, CreatedAt: nowTS()})
	if err != nil {
		return "", err
	}
	if tier == "ask" {
		appapproval.NotifyApprovers(db, session, id)
	}
	return "Q-" + strconv.FormatInt(id, 10), nil
}

// callerRead は呼び手の名前(callerName)の主体で読むだけの操作を呼び、Text を出す。
func callerRead(db *store.DB, op string, in any, w io.Writer) error {
	who, err := callerName()
	if err != nil {
		return die("herdr agent list に失敗したため、実行者を特定できません")
	}
	var r appapproval.TextResult
	if err := callAppW(db, app.PrincipalOf(who), io.Discard, op, in, &r); err != nil {
		return err
	}
	fmt.Fprint(w, r.Text)
	return nil
}

// approvalList は tb approval list(承認待ち Q-<n>。優先度の高い順、同じなら古い順)。
func approvalList(db *store.DB, args []string, w io.Writer) error {
	var in appapproval.ListReq
	for _, a := range args {
		switch a {
		case "--json":
			in.JSON = true
		case "--mine":
			in.Mine = true
		default:
			return usage("approval list: 不明な引数 %s", a)
		}
	}
	return callerRead(db, "approval.list", in, w)
}

// 送信(O-<n>)と agent の操作(L-<n>)の承認・却下は、どの入口(Q-<n> / O-<n> / L-<n> / tb agent-approve)から決めても、
// 決まったら 1 回だけ依頼者に知らせる(下の 4 つが入口。中身は approveImpl などのまま)。

func cmdAgentApprove(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("agent-approve L-<n> [--always]")
	}
	if _, err := validLid(args[0]); err != nil {
		return err
	}
	always := len(args) > 1 && args[1] == "--always"
	if len(args) > 1 && !always {
		return usage("agent-approve L-<n> [--always]")
	}
	return agentDecide("agent.approve", args[0], always, w, stderr) // 決まったことの知らせは API が送る
}

func cmdAgentReject(args []string, w, stderr io.Writer) error {
	if len(args) != 1 {
		return usage("agent-reject L-<n>")
	}
	if _, err := validLid(args[0]); err != nil {
		return err
	}
	return agentDecide("agent.reject", args[0], false, w, stderr)
}

func agentDecide(op, lid string, always bool, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	who, err := requester()
	if err != nil {
		return err
	}
	var res agent.DecideResult
	if err := callAppW(db, app.PrincipalOf(who), stderr, op, agent.DecideReq{ID: lid, Always: always}, &res); err != nil {
		return err
	}
	fmt.Fprintln(w, res.Out)
	return nil
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
	if !ok || r.Source != "cmd" || who != r.Requester {
		return die("%s を実行できるのは、依頼した agent だけです", args[0])
	}
	switch r.Status {
	case "approved":
	case "pending":
		return die("%s はまだ承認されていません", r.QID())
	case "running", "done", "failed":
		return die("%s はもう実行しました(%s。ワンタイムのため 2 回目は実行しません。もう一度要るなら、もう一度依頼してください)", r.QID(), r.Status)
	default:
		return die("%s は承認されていません(%s)", r.QID(), r.Status)
	}
	pl, err := r.Cmd()
	if err != nil {
		return reqErr(err)
	}
	fmt.Fprintf(w, "%s を実行します(%s が承認): tb %s\n", r.QID(), r.DecidedBy.String, strings.Join(pl.Args, " "))
	flush(w)
	code, err := requests.RunApproved(db, requests.IO{Out: w, Err: stderr}, r, pl)
	if err != nil {
		return reqErr(err)
	}
	if code != 0 {
		return &exit{code, ""}
	}
	return nil
}

// reqErr は requests の断りを tb の終了コードにする(使い方の誤りは 2、ほかは 1)。
func reqErr(err error) error {
	var r *requests.Refused
	if errors.As(err, &r) {
		if r.Usage {
			return usage("%s", r.Msg)
		}
		return die("%s", r.Msg)
	}
	return err
}

// approvalHistory は tb approval history: allow / ask / deny の記録(新しい順)。誰が依頼し、何が・誰が決め、どうなったか。
func approvalHistory(db *store.DB, args []string, w io.Writer) error {
	var in appapproval.HistoryReq
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--json":
			in.JSON = true
		case "--mine":
			in.Mine = true
		case "--by", "--limit":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			v := args[i+1]
			i++
			if a == "--by" {
				in.By = v
			} else {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 1000 {
					return usage("--limit は 1〜1000")
				}
				in.Limit = n
			}
		default:
			return usage("approval history: 不明な引数 %s", a)
		}
	}
	return callerRead(db, "approval.history", in, w)
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
	if err != nil || !ok || r.Source != "cmd" || r.Status != "approved" || key == "" {
		return false, die("承認済みの依頼ではありません: %s", q)
	}
	var pl struct {
		Args []string `json:"args"`
	}
	_ = json.Unmarshal([]byte(r.Payload.String), &pl)
	if strings.Join(pl.Args, "\x00") != strings.Join(args, "\x00") {
		return false, die("承認済みの依頼とコマンドが違います: %s", q)
	}
	claimed, err := requests.ClaimRun(db, r.ID, key)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, die("承認済みの依頼の鍵が合わないか、もう実行しました: %s", q)
	}
	// 依頼者として動く
	identity.System = ""
	os.Unsetenv("HERDR_PANE_ID")
	switch {
	case r.Requester == "human":
	case strings.HasPrefix(r.Requester, "system:"):
		identity.System = r.Requester
	default:
		as, err := herdr.Agents()
		if err != nil {
			return false, die("herdr agent list に失敗したため、依頼者 %s として実行できません", r.Requester)
		}
		pane := ""
		for _, a := range as {
			if a.Name == r.Requester {
				pane = a.PaneID
			}
		}
		if pane == "" {
			return false, die("依頼者 %s がもういないため実行できません(%s)", r.Requester, q)
		}
		os.Setenv("HERDR_PANE_ID", pane)
	}
	perms.Approved = true
	if r.Tier == "deny" {
		perms.ApprovedDeny = r.Op // 人が例外として承認した deny の操作だけ通す
	}
	return true, nil
}

// requestPriority は tb approval priority Q-<n> high|normal|low(依頼者か人だけ。approval.priority)。
func requestPriority(args []string, w, stderr io.Writer) error {
	if len(args) != 2 || !reQID.MatchString(args[0]) {
		return usage("approval priority Q-<n> high|normal|low")
	}
	if _, ok := appapproval.Priorities[args[1]]; !ok {
		return usage("優先度は high / normal / low のどれかです: %s", args[1])
	}
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var r appapproval.PriorityResult
	if err := callAppEnv(appEnv(db, stderr), by, "approval.priority", appapproval.PriorityReq{ID: args[0], Priority: args[1]}, &r); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s priority %s\n", r.ID, r.Priority)
	return nil
}
