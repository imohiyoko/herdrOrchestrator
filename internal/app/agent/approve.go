package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// 承認待ちの agent の操作(L-<n>)の承認と却下(tb agent approve / reject)。承認できるのは人か、
// policy の approve:agent:… で許された agent(依頼者本人は不可)。
// API で扱うのは作成・停止・再開(spawn を含む)・スラッシュコマンド(op=slash)・モデルの切り替え(tb model-set)。
// --always(今後は承認なし。policy を書く)だけは tb の入口が扱う(段 3 で policy の書き込みと一緒に移す)。

var reLid = regexp.MustCompile(`^L-[0-9]+$`)

// DecideReq は agent.approve / agent.reject の入力。
type DecideReq struct {
	ID string `api:"id,path"` // L-<n>
}

// DecideResult は agent.approve / agent.reject の結果。
type DecideResult struct {
	ID  string `api:"id" json:"id"`
	Out string `api:"out" json:"out"` // 承認: 実行の結果の文言、却下: 「L-<n> rejected」
}

// OpRow は agent_ops の 1 行。
type OpRow struct {
	ID                         int64
	Op, Target, Args, By, Stat string
	Result                     string
}

// OpArgs は agent_ops.args のうち使う項目。
type OpArgs struct {
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

// Parsed は args を読む(読めなければ空)。
func (o OpRow) Parsed() OpArgs {
	var a OpArgs
	_ = json.Unmarshal([]byte(o.Args), &a)
	return a
}

// OpRowOf は L-<id> の行(無ければ false)。
func OpRowOf(db *store.DB, id int64) (OpRow, bool) {
	o := OpRow{ID: id}
	var args, result sql.NullString
	if db.QueryRow(`SELECT op, target, args, requested_by, status, result FROM agent_ops WHERE id=?`, id).Scan(&o.Op, &o.Target, &args, &o.By, &o.Stat, &result) != nil {
		return o, false
	}
	o.Args, o.Result = args.String, result.String
	if o.Args == "" {
		o.Args = "{}"
	}
	return o, true
}

// OpAction は agent_op_action: 承認するときに grants で見る元の操作名(空 = 人だけ)。
func OpAction(db *store.DB, o OpRow) string {
	a := o.Parsed()
	switch o.Op {
	case "slash":
		if o.Target == "*" { // --all は人だけ
			return ""
		}
		return "agent:slash:" + a.Command + ":" + o.Target
	case "new":
		if a.Spawn.Parent != "" {
			if SpawnFrozen(db) { // spawn-freeze の間は、spawn の承認は人だけ
				return ""
			}
			return "agent:spawn:" + o.Target
		}
		return "agent:new:" + o.Target
	case "revive":
		if a.ModelSet == 1 {
			if a.All == 1 || strings.HasPrefix(o.Result, "--all") { // tb model-set --all は人だけ
				return ""
			}
			return "agent:model:" + o.Target
		}
		return "agent:revive:" + o.Target
	}
	return "agent:" + o.Op + ":" + o.Target
}

// Approver は承認・却下する主体(人か agent の名前)と根拠の policy の行。
type Approver struct{ Name, Rule string }

// Label は記録に残す「人」/「agent <名前>(<行>)」。
func (a Approver) Label() string {
	if a.Name == "human" {
		return "人"
	}
	return "agent " + a.Name + "(" + a.Rule + ")"
}

// CheckApprover は approver_check: who(人か agent の名前)が、依頼者 by の操作 act を決めてよいか。act が空なら人だけ。
func CheckApprover(j *approval.Judge, cmd, who, act, by string) (Approver, error) {
	deny := func(format string, a ...any) error {
		return &define.Error{Code: define.CodePermissionDenied, Message: fmt.Sprintf(format, a...)}
	}
	switch {
	case who == "human":
		return Approver{Name: "human"}, nil
	case strings.HasPrefix(who, "system:") || who == "":
		return Approver{}, deny("%s: この承認待ちは人が自分の端末で実行します(%s からは承認できません)", cmd, who)
	case act == "":
		return Approver{}, deny("%s: この承認待ちは人が自分の端末で実行します(agent '%s' の pane からは承認できません)", cmd, who)
	case who == by:
		return Approver{}, deny("%s: 自分の依頼は承認できません(agent '%s')。人が自分の端末で実行します", cmd, who)
	}
	rule, ok := j.GrantOf(who, "approve:"+act, by)
	if !ok {
		return Approver{}, deny("%s は人が自分の端末で実行します(agent '%s' には %s からの %s を承認する許可がありません。policy に '<承認者> approve:<操作> <依頼者> = allow' の行が要ります)", cmd, who, by, act)
	}
	return Approver{who, rule}, nil
}

// RecordOpID は承認後の実行の結果を L-<id> の行に残す。
func RecordOpID(db *store.DB, now string, id int64, rc int, result string) {
	st := "done"
	if rc != 0 {
		st = "failed"
	}
	_, _ = db.Exec(`UPDATE main.agent_ops SET status=?, result=?, decided_at=? WHERE herdr_session=? AND id=?`, st, result, now, db.Session, id)
}

// RecordDecision は承認待ち(Q-<n>)の記録に、決めた結果と誰が決めたかを残す(Q-<n> が無ければ何もしない)。
func RecordDecision(db *store.DB, now, source string, sourceID int64, status, by string) {
	_, _ = db.Exec(`UPDATE main.requests SET status=?, decided_by=?, decided_at=? WHERE herdr_session=? AND source=? AND source_id=?`,
		status, by, now, db.Session, source, sourceID)
}

// OpClaim は op_claim: 同時に承認しても実行は 1 回。取れたら Q-<n> の記録に、誰が承認したかを残す。
func OpClaim(db *store.DB, now string, id int64, by string) error {
	res, err := db.Exec(`UPDATE main.agent_ops SET decided_at=? WHERE herdr_session=? AND id=? AND status='pending' AND decided_at IS NULL`, now, db.Session, id)
	if err != nil {
		return define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return define.Errorf(define.CodeFailedPrecondition, "L-%d はほかの承認者が処理中か、すでに決まっています(途中で止まった行なら、人が tb agent-reject L-%d で片付ける)", id, id)
	}
	RecordDecision(db, now, "agent_ops", id, "approved", by)
	return nil
}

func decideOp(env *app.Env, approve bool) app.Op {
	name, sub, cmd, verb := "agent.reject", "reject", "tb agent-reject", "reject"
	if approve {
		name, sub, cmd, verb = "agent.approve", "approve", "tb agent-approve", "approve"
	}
	return app.Command(env, name,
		func(_ context.Context, _ *app.Env, _ string, v any) app.Decision {
			if id := v.(DecideReq).ID; !reLid.MatchString(id) {
				return app.Decision{Err: usageErr("操作 ID は L-<数字>: %s", id)}
			}
			return app.Decision{}
		},
		func(ctx context.Context, in DecideReq) (DecideResult, error) {
			who := by(ctx)
			id, _ := strconv.ParseInt(in.ID[2:], 10, 64)
			return withDB(env, func(db *store.DB) (DecideResult, error) {
				j := approval.NewJudge(db.DB, db.Session)
				o, ok := OpRowOf(db, id)
				if !ok {
					if _, err := CheckApprover(j, cmd, who, "", ""); err != nil {
						return DecideResult{}, err
					}
					if approve {
						return DecideResult{}, define.Errorf(define.CodeNotFound, "操作が見つかりません: L-%d", id)
					}
					return DecideResult{}, define.Errorf(define.CodeNotFound, "L-%d は承認待ちではありません", id)
				}
				ap, err := CheckApprover(j, cmd, who, OpAction(db, o), o.By)
				if err != nil {
					return DecideResult{}, err
				}
				if o.Stat != "pending" {
					if approve {
						return DecideResult{}, define.Errorf(define.CodeFailedPrecondition, "L-%d は承認待ちではありません(%s)", id, o.Stat)
					}
					return DecideResult{}, define.Errorf(define.CodeFailedPrecondition, "L-%d は承認待ちではありません", id)
				}
				var r DecideResult
				if approve {
					r, err = approveRow(env, db, o, ap)
				} else {
					r, err = rejectRow(env, db, o, ap)
				}
				if err == nil {
					NotifyDecided(db, env.NowString(), id, sub, who)
				}
				return r, err
			})
		},
		define.Doc("承認待ちの agent の操作 L-<n> を"+map[bool]string{true: "承認して実行する", false: "却下する"}[approve]+"(tb agent "+verb+")"),
		define.Authz("cmd:agent:"+verb, define.Resource("agent_op", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agent-ops/{id}/"+verb),
	)
}

func approveRow(env *app.Env, db *store.DB, o OpRow, ap Approver) (DecideResult, error) {
	id := o.ID
	if o.Op == "slash" { // スラッシュコマンド: 印を取ってから、今の許可リストで送る
		if err := OpClaim(db, env.NowString(), id, ap.Name); err != nil {
			return DecideResult{}, err
		}
		var b bytes.Buffer
		line, rc := SlashApproved(db, o, ap.Label(), &b)
		out := strings.TrimRight(b.String()+line, "\n")
		if err := (Result{Code: rc, Out: out}).Err(); err != nil {
			return DecideResult{}, err
		}
		return DecideResult{ID: "L-" + strconv.FormatInt(id, 10), Out: out}, nil
	}
	st := tickets.LoadStatuses(db.DB, db.Session)
	a := o.Parsed()
	if o.Op == "new" && a.Spawn.Parent != "" {
		if why := SpawnRecheck(db, st, id, a.Spawn.Parent, a.Role, a.Spawn.Task); why != "" {
			return DecideResult{}, define.Errorf(define.CodeFailedPrecondition, "L-%d は承認できません(規則に合わなくなりました): %s。L-%d は承認待ちのままです(tb agent-reject L-%d で却下)", id, why, id, id)
		}
	} else if o.Op == "new" || (o.Op == "revive" && a.ModelSet != 1) {
		var b bytes.Buffer
		if RefuseRunning(db, 1, &b) { // 承認待ちの間に上限へ達していたら、承認待ちのまま残す
			return DecideResult{}, limitErr(&b)
		}
	}
	if o.Op == "new" {
		if why, bad := RoleConflict(agents.NewRoles(db.DB, db.Session), o.Target, a.Role); bad {
			return DecideResult{}, define.Errorf(define.CodeFailedPrecondition, "L-%d: %s(tb agent-reject L-%d で却下)", id, why, id)
		}
	}
	if err := OpClaim(db, env.NowString(), id, ap.Name); err != nil {
		return DecideResult{}, err
	}
	r := runOp(db, st, o, env.Warner())
	RecordOpID(db, env.NowString(), id, r.Code, r.Out+" / "+ap.Label()+"が承認")
	if err := r.Err(); err != nil {
		if e, ok := err.(*define.Error); ok {
			e.Message = fmt.Sprintf("L-%d: %s", id, r.Out)
		}
		return DecideResult{}, err
	}
	return DecideResult{ID: "L-" + strconv.FormatInt(id, 10), Out: fmt.Sprintf("L-%d: %s", id, r.Out)}, nil
}

// runOp は run_op: op と args から実行する(承認後の実行。作成・停止・再開)。
func runOp(db *store.DB, st tickets.StatusSet, o OpRow, stderr io.Writer) Result {
	a := o.Parsed()
	switch o.Op {
	case "new":
		kill, kind := a.Killable, a.Kind
		if kill == "" {
			kill = "unknown"
		}
		if kind == "" {
			kind = KindDefault
		}
		return New(db, o.Target, a.Cwd, a.Model, a.Role, a.Prompt, kill, o.By, a.Spawn.Parent, a.Spawn.Task, kind, stderr)
	case "stop":
		return Stop(db, st, o.Target, a.Force == 1, o.By)
	case "revive":
		if a.ModelSet == 1 { // tb model-set(承認後)
			return ModelSetRun(db, o.Target, o.Args, o.By, stderr)
		}
		return Revive(db, o.Target, a.Fresh == 1, a.Model, a.Prompt, o.By, stderr)
	}
	return Result{Code: 1, Out: "不明な操作: " + o.Op}
}

func rejectRow(env *app.Env, db *store.DB, o OpRow, ap Approver) (DecideResult, error) {
	id := o.ID
	// agent は処理中(印のある)の行を却下できない。人は、途中で止まって印だけ残った行も片付けられる
	free := " AND decided_at IS NULL"
	if ap.Name == "human" {
		free = ""
	}
	res, err := db.Exec(`UPDATE main.agent_ops SET status='rejected', result=COALESCE(result,'')||?, decided_at=? WHERE herdr_session=? AND id=? AND status='pending'`+free,
		" / "+ap.Label()+"が却下", env.NowString(), db.Session, id)
	if err != nil {
		return DecideResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return DecideResult{}, define.Errorf(define.CodeFailedPrecondition, "L-%d はほかの承認者が処理中か、すでに決まっています", id)
	}
	RecordDecision(db, env.NowString(), "agent_ops", id, "rejected", ap.Name)
	return DecideResult{ID: "L-" + strconv.FormatInt(id, 10), Out: fmt.Sprintf("L-%d rejected", id)}, nil
}

// NotifyDecided は、決まった agent の操作 L-<sid> の承認待ち(Q-<n>)を、依頼した agent の pane に 1 通知らせる
// (人・system: の依頼者には送らない。記録は messages の kind=approval、ref=Q-<n>)。who は決めた主体。
func NotifyDecided(db *store.DB, now string, sid int64, sub, who string) {
	var qid int64
	var requester, op, target string
	if db.QueryRow(`SELECT id, requester, op, target FROM requests WHERE source='agent_ops' AND source_id=?`, sid).Scan(&qid, &requester, &op, &target) != nil {
		return // 承認待ちの記録が無い(古い行)。知らせる相手が分からない
	}
	if requester == "human" || requester == "" || strings.HasPrefix(requester, "system:") {
		return
	}
	state := "却下されました(実行していません)"
	if sub == "approve" {
		var st string
		_ = db.QueryRow(`SELECT status FROM agent_ops WHERE id=?`, sid).Scan(&st)
		state = "承認されましたが、実行に失敗しました(" + st + ")"
		if st == "done" {
			state = "承認され、実行されました"
		}
	}
	q := "Q-" + strconv.FormatInt(qid, 10)
	msg := "[tb approval / " + q + " / " + who + "] " + fmt.Sprintf("%s(agent の操作 %s %s)は %s に%s", q, op, target, who, state)
	if transport.Send(db.DB, db.Session, requester, msg, q, "approval", io.Discard) != transport.Sent {
		return
	}
	_, _ = db.Exec(`UPDATE main.requests SET notified_at=? WHERE id=?`, now, qid)
}
