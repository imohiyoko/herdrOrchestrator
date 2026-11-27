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
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// 承認待ちの操作(L-<n>)の承認のうち、tb の中で扱うもの: スラッシュコマンド(slash_approve)、モデルの切り替え(tb model-set)と
// --always(今後は承認なし)。ほかの承認と却下は API(agent.approve / agent.reject。internal/app/agent/approve.go)。

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

// opClaim は op_claim: 同時に承認しても実行は 1 回。取れたら Q-<n> の記録に、誰が承認したかを残す。
func opClaim(db *store.DB, id int64, by string) error {
	res, err := db.Exec(`UPDATE main.agent_ops SET decided_at=? WHERE herdr_session=? AND id=? AND status='pending' AND decided_at IS NULL`, nowTS(), db.Session, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("L-%d はほかの承認者が処理中か、すでに決まっています(途中で止まった行なら、人が tb agent-reject L-%d で片付ける)", id, id)
	}
	recordDecision(db, "agent_ops", id, "approved", by)
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

func agentApproveImpl(args []string, w, stderr io.Writer) error {
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
	if err := opClaim(db, id, ap.name); err != nil {
		return err
	}
	if always {
		pat := "agent:" + aop + ":" + o.target
		if setApproval(db, "allow", pat, io.Discard, io.Discard) == nil {
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

// slashApprove は slash_approve(op=slash の承認待ち)。tb に残るのは --always のときだけ(ほかは API の agent.approve)。
// 送る中身は app/agent.SlashApproved と同じ。
func slashApprove(db *store.DB, o opRow, always bool, ap approver, w, stderr io.Writer) error {
	id, cmd := o.id, o.parsed().Command
	if always && o.target == "*" {
		return die("L-%d: --all の承認に --always は使えません", id)
	}
	if err := opClaim(db, id, ap.name); err != nil { // 同時に承認しても送るのは 1 回
		return err
	}
	if always {
		// 許可リストで送れないものに allow を書かない(送るかどうかは SlashApproved がもう一度確かめる)
		body, _, has := config.Lookup(db.DB, db.Session, "slash")
		if e, r, _ := slashLookup(body, has, cmd); r == 0 && e.Policy == "allow" {
			pat := "agent:slash:" + cmd + ":" + o.target
			if setApproval(db, "allow", pat, io.Discard, io.Discard) == nil {
				fmt.Fprintln(w, pat+" = allow(今後は承認なし)")
			}
		}
	}
	line, rc := agent.SlashApproved(db, agent.OpRow{ID: o.id, Op: o.op, Target: o.target, Args: o.args, By: o.by, Stat: o.stat, Result: o.result}, ap.label(), w)
	switch rc {
	case 0:
		if o.target != "*" {
			fmt.Fprintln(w, line)
		}
		return nil
	case 11:
		if o.target == "*" {
			return &exit{11, ""}
		}
		fmt.Fprintln(w, line)
		return &exit{11, ""}
	}
	return &exit{1, line}
}
