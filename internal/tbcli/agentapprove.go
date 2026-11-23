package tbcli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// 承認待ちの操作(L-<n>)の承認と却下(taskboard/lib/agents.sh の cmd_agent-approve / cmd_agent-reject と
// lib/slash.sh の slash_approve)。承認できるのは人か、grants の approve:agent:… で許された agent。

var reLid = regexp.MustCompile(`^L-[0-9]+$`)

// opRow は agent_ops の 1 行。
type opRow struct {
	id                         int64
	op, target, args, by, stat string
	result                     string
}

// opArgs は agent_ops.args のうち使う項目。
type opArgs struct {
	Cwd      string `json:"cwd"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	Prompt   string `json:"prompt"`
	Killable string `json:"killable"`
	Kind     string `json:"kind"`
	Force    int    `json:"force"`
	Fresh    int    `json:"fresh"`
	ModelSet int    `json:"model_set"`
	All      int    `json:"all"`
	Command  string `json:"command"`
	Spawn    struct {
		Parent string `json:"parent"`
		Task   string `json:"task"`
	} `json:"spawn"`
}

func (o opRow) parsed() opArgs {
	var a opArgs
	_ = json.Unmarshal([]byte(o.args), &a)
	return a
}

func validLid(s string) (int64, error) {
	if !reLid.MatchString(s) {
		return 0, usage("操作 ID は L-<数字>: %s", s)
	}
	n, _ := strconv.ParseInt(s[2:], 10, 64)
	return n, nil
}

func opRowOf(db *store.DB, id int64) (opRow, bool) {
	o := opRow{id: id}
	var args, result sql.NullString
	if db.QueryRow(`SELECT op, target, args, requested_by, status, result FROM agent_ops WHERE id=?`, id).Scan(&o.op, &o.target, &args, &o.by, &o.stat, &result) != nil {
		return o, false
	}
	o.args, o.result = args.String, result.String
	if o.args == "" {
		o.args = "{}"
	}
	return o, true
}

// agentOpAction は agent_op_action: 承認するときに grants で見る元の操作名(空 = 人だけ)。
func agentOpAction(db *store.DB, o opRow) string {
	a := o.parsed()
	switch o.op {
	case "slash":
		if o.target == "*" { // --all は人だけ
			return ""
		}
		return "agent:slash:" + a.Command + ":" + o.target
	case "new":
		if a.Spawn.Parent != "" {
			if spawnFrozen(db) { // spawn-freeze の間は、spawn の承認は人だけ
				return ""
			}
			return "agent:spawn:" + o.target
		}
		return "agent:new:" + o.target
	case "revive":
		if a.ModelSet == 1 {
			if a.All == 1 || strings.HasPrefix(o.result, "--all") { // tb model-set --all は人だけ
				return ""
			}
			return "agent:model:" + o.target
		}
		return "agent:revive:" + o.target
	}
	return "agent:" + o.op + ":" + o.target
}

// opClaim は op_claim: 同時に承認しても実行は 1 回。
func opClaim(db *store.DB, id int64) error {
	res, err := db.Exec(`UPDATE main.agent_ops SET decided_at=? WHERE herdr_session=? AND id=? AND status='pending' AND decided_at IS NULL`, nowTS(), db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("L-%d はほかの承認者が処理中か、すでに決まっています(途中で止まった行なら、人が tb agent-reject L-%d で片付ける)", id, id)
	}
	return nil
}

// runOp は run_op: op と args から実行する(承認後の実行)。
func runOp(db *store.DB, st tickets.StatusSet, o opRow, stderr io.Writer) opResult {
	a := o.parsed()
	switch o.op {
	case "new":
		kill, kind := or(a.Killable, "unknown"), or(a.Kind, kindDefault)
		return doAgentNew(db, o.target, a.Cwd, a.Model, a.Role, a.Prompt, kill, o.by, a.Spawn.Parent, a.Spawn.Task, kind, stderr)
	case "stop":
		return doAgentStop(db, st, o.target, a.Force == 1, o.by)
	case "revive":
		if a.ModelSet == 1 { // tb model-set(承認後)
			return modelSetRun(db, o.target, o.args, o.by, stderr)
		}
		return doAgentRevive(db, o.target, a.Fresh == 1, a.Model, a.Prompt, o.by, stderr)
	}
	return opResult{code: 1, out: "不明な操作: " + o.op}
}

func cmdAgentApprove(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("agent-approve L-<n> [--always]")
	}
	id, err := validLid(args[0])
	if err != nil {
		return err
	}
	always := len(args) > 1 && args[1] == "--always"
	db, st, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	j := approval.NewJudge(db.DB, db.Session)
	o, ok := opRowOf(db, id)
	if !ok {
		if _, err := approverCheck(j, "tb agent-approve", "", ""); err != nil {
			return err
		}
		return die("操作が見つかりません: L-%d", id)
	}
	ap, err := approverCheck(j, "tb agent-approve", agentOpAction(db, o), o.by)
	if err != nil {
		return err
	}
	if ap.name != "human" && always {
		return die("tb agent-approve: --always(今後は承認なしにする)は人だけが使えます")
	}
	if o.stat != "pending" {
		return die("L-%d は承認待ちではありません(%s)", id, o.stat)
	}
	if o.op == "slash" {
		return slashApprove(db, o, always, ap, w, stderr)
	}
	a := o.parsed()
	aop := o.op
	if a.ModelSet == 1 {
		aop = "model" // tb model-set の承認待ち(記録は op=revive)
	}
	if o.op == "new" && a.Spawn.Parent != "" {
		if !spawnRecheck(db, st, id, a.Spawn.Parent, a.Role, a.Spawn.Task, stderr) {
			return &exit{1, ""}
		}
	} else if o.op == "new" || aop == "revive" {
		if err := limitsRefuseRunning(db, 1, stderr); err != nil { // 承認待ちの間に上限へ達していたら、承認待ちのまま残す
			return err
		}
	}
	if o.op == "new" { // --always の規則を書く前に止める
		if why, bad := roleConflict(agents.NewRoles(db.DB, db.Session), o.target, a.Role); bad {
			return die("L-%d: %s(tb agent-reject L-%d で却下)", id, why, id)
		}
	}
	if err := opClaim(db, id); err != nil {
		return err
	}
	if always {
		pat := "agent:" + aop + ":" + o.target
		if setApproval(db, "allow", pat, io.Discard) == nil {
			fmt.Fprintln(w, pat+" = allow(今後は承認なし)")
		}
	}
	r := runOp(db, st, o, stderr)
	recordOpID(db, id, r.code, r.out+" / "+ap.label()+"が承認")
	if r.code != 0 {
		return &exit{r.code, fmt.Sprintf("L-%d: %s", id, r.out)}
	}
	fmt.Fprintf(w, "L-%d: %s\n", id, r.out)
	return nil
}

// slashApprove は slash_approve(op=slash の承認待ち)。
func slashApprove(db *store.DB, o opRow, always bool, ap approver, w, stderr io.Writer) error {
	id, cmd := o.id, o.parsed().Command
	if always && o.target == "*" {
		return die("L-%d: --all の承認に --always は使えません", id)
	}
	if err := opClaim(db, id); err != nil { // 同時に承認しても送るのは 1 回
		return err
	}
	// 承認待ちの間に許可リストが変わっていたら、今の行に従う(外された・deny になったら送らない)
	body, _, has := config.Lookup(db.DB, db.Session, "slash")
	e, r, _ := slashLookup(body, has, cmd)
	if r != 0 || e.policy != "allow" {
		recordOpID(db, id, 1, "/"+cmd+" は許可リストに無いか deny になったため送らない / "+ap.label()+"が承認")
		return &exit{1, fmt.Sprintf("L-%d: /%s は許可リストに無いか deny です。送りません", id, cmd)}
	}
	if always {
		pat := "agent:slash:" + cmd + ":" + o.target
		if setApproval(db, "allow", pat, io.Discard) == nil {
			fmt.Fprintln(w, pat+" = allow(今後は承認なし)")
		}
	}
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return die("herdr agent list に失敗しました")
	}
	c := &slashCtx{db: db, live: live, roles: agents.NewRoles(db.DB, db.Session), cmd: cmd, e: e}
	if o.target == "*" {
		// 承認の行には要約を、送った宛先ごとの行は別に残す
		rc, n := 0, 0
		for _, t := range c.resolve(true, o.by, nil) {
			if t.name == "" {
				continue
			}
			if t.why != "" {
				fmt.Fprintln(w, t.name+" "+t.why)
				continue
			}
			line, r := c.sendOne(t.name, o.by, 0, "")
			fmt.Fprintln(w, line)
			if r == 0 {
				n++
			}
			if r == 1 {
				rc = 11
			}
		}
		res := 0
		if rc != 0 {
			res = 1
		}
		recordOpID(db, id, res, fmt.Sprintf("/%s を --all で %d 体に送った / 人が承認", cmd, n))
		if rc != 0 {
			return &exit{rc, ""}
		}
		return nil
	}
	out, rr := "", 0
	for _, t := range c.resolve(false, o.by, []string{o.target}) {
		if t.name == "" {
			continue
		}
		if t.why != "" {
			recordOpID(db, id, 1, t.why+" / "+ap.label()+"が承認")
			return &exit{1, fmt.Sprintf("L-%d: %s %s", id, t.name, t.why)}
		}
		out, rr = c.sendOne(t.name, o.by, id, ap.label())
	}
	fmt.Fprintf(w, "L-%d: %s\n", id, out)
	if rr != 0 {
		return &exit{11, ""}
	}
	return nil
}

func cmdAgentReject(args []string, w, stderr io.Writer) error {
	if len(args) != 1 {
		return usage("agent-reject L-<n>")
	}
	id, err := validLid(args[0])
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	j := approval.NewJudge(db.DB, db.Session)
	o, ok := opRowOf(db, id)
	if !ok {
		if _, err := approverCheck(j, "tb agent-reject", "", ""); err != nil {
			return err
		}
		return die("L-%d は承認待ちではありません", id)
	}
	ap, err := approverCheck(j, "tb agent-reject", agentOpAction(db, o), o.by)
	if err != nil {
		return err
	}
	if o.stat != "pending" {
		return die("L-%d は承認待ちではありません", id)
	}
	// agent は処理中(印のある)の行を却下できない。人は、途中で止まって印だけ残った行も片付けられる
	free := " AND decided_at IS NULL"
	if ap.name == "human" {
		free = ""
	}
	res, err := db.Exec(`UPDATE main.agent_ops SET status='rejected', result=COALESCE(result,'')||?, decided_at=? WHERE herdr_session=? AND id=? AND status='pending'`+free,
		" / "+ap.label()+"が却下", nowTS(), db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("L-%d はほかの承認者が処理中か、すでに決まっています", id)
	}
	fmt.Fprintf(w, "L-%d rejected\n", id)
	return nil
}
