package tbcli

import (
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"

	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// agent の止める・戻す(taskboard/lib/agents.sh の authorize_op / op_entry / do_agent_stop / cmd_agent-stop)。
// 作る(agent-new)・戻す(agent-revive)・再起動(agent-restart)も。承認(agent-approve / agent-reject)と tb agent-spawn の子の作成はまだ bash。
// 承認待ち(L-<n>)の行は bash と同じ形で積むので、承認は bash の tb agent-approve がそのまま実行できる。

// opResult は do_agent_* の結果(OP_OUT と終了コード)。
type opResult struct {
	code int
	out  string
	note string // 最初の指示を送れなかった・確かめられなかった理由(FP_NOTE)
}

// authorizeOp は authorize_op: 人なら true。agent なら policy の agent:<op> <名前> が allow のとき true、
// deny なら却下の行を残して 4、それ以外(ask-*)は L-<n> を積んで「承認待ち」を出し、終了コード 20 を返す。
func authorizeOp(db *store.DB, op, target, args, by string, w io.Writer) (bool, error) {
	if by == "human" {
		return true, nil
	}
	j := approval.NewJudge(db.DB, db.Session)
	if op == "new" { // 作成は、名前から推定した役割ではなく要求された役割(--role)で @<役割> を判定する
		var a struct {
			Role string `json:"role"`
		}
		if json.Unmarshal([]byte(args), &a) == nil && a.Role != "" {
			j.Roles.Hint(target, a.Role)
		}
	}
	v, rule := j.Of("agent:"+op+":"+target, by)
	if v == "allow" {
		return true, nil
	}
	if v == "deny" {
		ts := nowTS()
		_, _ = db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at) VALUES (?,?,?,?,?, 'rejected', ?, ?, ?)`,
			db.Session, op, target, args, by, "認可: "+rule, ts, ts)
		return false, &exit{4, "agent:" + op + " " + target + " は実行できません(deny。" + rule + ")"}
	}
	res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?,?,?,?,?, 'pending', ?, ?)`,
		db.Session, op, target, args, by, "認可: "+rule, nowTS())
	if err != nil {
		return false, err
	}
	id, _ := res.LastInsertId()
	q := pendingQ(db.DB, "agent_ops", id)
	fmt.Fprintf(w, "L-%d pending %s (承認待ち: tb approval approve %s)\n", id, q, q)
	return false, &exit{20, ""}
}

// opEntry は op_entry: 承認を確かめて実行し、記録して結果を出す。
func opEntry(db *store.DB, op, target, args, by string, w io.Writer, run func() opResult) error {
	if ok, err := authorizeOp(db, op, target, args, by, w); !ok {
		return err
	}
	r := run()
	recordOp(db, op, target, args, by, r.code, r.out)
	if r.code != 0 {
		return &exit{r.code, r.out}
	}
	fmt.Fprintln(w, r.out)
	return nil
}

// killableOf は killable_of: protected|yes|no|unknown|stale。
func killableOf(db *store.DB, name string) string { return agents.Killable(db.DB, db.Session, name) }

// liveOf は live_row: herdr の一覧から名前が一致する 1 件。
func liveOf(live []herdr.Agent, name string) (herdr.Agent, bool) { return agents.LiveOf(live, name) }

// closePane は agent_close_pane: pane を閉じて台帳を stopped にする(session は残す)。閉じられなければ 11。
func closePane(db *store.DB, name, pane, sess string) *opResult {
	if !agents.ClosePane(db.DB, db.Session, name, pane, sess) {
		return &opResult{code: 11, out: "pane " + pane + " を閉じられません"}
	}
	return nil
}

// doAgentStop は do_agent_stop(中身は app/agent.Stop。承認後の実行・recipe から呼ぶ)。
func doAgentStop(db *store.DB, st tickets.StatusSet, name string, force bool, by string) opResult {
	r := agent.Stop(db, st, name, force, by)
	return opResult{code: r.Code, out: r.Out}
}

// cmdAgentStop は tb agent-stop <名前> [--force]。中身はアプリケーション API の agent.stop(ADR 0008)。
func cmdAgentStop(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-stop <名前> [--force]")
	}
	name, force := args[0], false
	if err := validAgentName(name); err != nil {
		return err
	}
	for _, a := range args[1:] {
		if a != "--force" {
			return usage("agent-stop: 不明な引数 %s", a)
		}
		force = true
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	var res agent.StopResult
	if err := callAppAs(db, app.PrincipalOf(by), "agent.stop", agent.StopReq{Name: name, Force: force}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.Out)
	return nil
}

// ---------- 戻す(revive) ----------

func cmdAgentRevive(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-revive <名前> [--fresh] [--model M] [--prompt P]")
	}
	name, fresh, model, prompt := args[0], false, "", ""
	if err := validAgentName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		switch a := args[i]; a {
		case "--fresh":
			fresh = true
		case "--model", "--prompt":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			if a == "--model" {
				model = args[i+1]
			} else {
				prompt = args[i+1]
			}
			i++
		default:
			return usage("agent-revive: 不明な引数 %s", a)
		}
	}
	if model != "" && !modelRE.MatchString(model) {
		return usage("agent-revive: --model に使えない文字があります: %s", model)
	}
	if utf8.RuneCountInString(prompt) > 4000 {
		return usage("agent-revive: --prompt は 4000 字まで")
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	var res agent.LifeResult
	if err := callAppW(db, app.PrincipalOf(by), stderr, "agent.revive", agent.ReviveReq{Name: name, Fresh: fresh, Model: model, Prompt: prompt}, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.Out)
	return nil
}

// ---------- 再起動(restart) ----------

func cmdAgentRestart(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-restart <名前> [--prompt P]")
	}
	name, prompt := args[0], ""
	if err := validAgentName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		if args[i] != "--prompt" {
			return usage("agent-restart: 不明な引数 %s(--force はありません)", args[i])
		}
		if i+1 >= len(args) {
			return usage("%s に値が必要です", args[i])
		}
		prompt = args[i+1]
		i++
	}
	if utf8.RuneCountInString(prompt) > 4000 {
		return usage("agent-restart: --prompt は 4000 字まで")
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	var res agent.LifeResult
	if err := callAppW(db, app.PrincipalOf(by), stderr, "agent.restart", agent.RestartReq{Name: name, Prompt: prompt}, &res); err != nil {
		return err
	}
	fmt.Fprintln(w, res.Out)
	return nil
}

// ---------- 作る(open / agent-new) ----------

func cmdAgentNew(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("agent-new <名前> [--cwd D] [--kind K] [--model M] [--role R] [--prompt P] [--killable yes|no]")
	}
	name := args[0]
	cwd, model, role, prompt, kill, kind := "", "", "", "", "unknown", kindDefault
	if err := validAgentName(name); err != nil {
		return err
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		var dst *string
		switch a {
		case "--cwd":
			dst = &cwd
		case "--kind":
			dst = &kind
		case "--model":
			dst = &model
		case "--role":
			dst = &role
		case "--prompt":
			dst = &prompt
		case "--killable":
			dst = &kill
		default:
			return usage("agent-new: 不明な引数 %s", a)
		}
		if i+1 >= len(args) {
			return usage("%s に値が必要です", a)
		}
		*dst = args[i+1]
		i++
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	// 入力・役割・上限の確かめと承認は agent.new が行う(承認待ちに積む前に、その kind でできない依頼は止める)
	in := agent.NewReq{Name: name, Cwd: agent.NormalizeCwd(cwd), Kind: kind, Model: model, Role: role, Prompt: prompt, Killable: kill}
	var res agent.NewResult
	if err := callAppW(db, app.PrincipalOf(by), stderr, "agent.new", in, &res); err != nil {
		return err
	}
	if res.Pending != nil {
		return pendingExit(w, res.Pending)
	}
	fmt.Fprintln(w, res.Out)
	return nil
}

// tb agent parent add|rm <子> <親>: 親(agent_parents)を足す・外す。子は親が全員止まったときだけ orphan になる。
func cmdAgentParentAdd(args []string, w, stderr io.Writer) error {
	return agentParent(true, args, w, stderr)
}

func cmdAgentParentRm(args []string, w, stderr io.Writer) error {
	return agentParent(false, args, w, stderr)
}

func agentParent(add bool, args []string, w, stderr io.Writer) error {
	verb, op := "rm", "agent.parent.rm"
	if add {
		verb, op = "add", "agent.parent.add"
	}
	if len(args) != 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		return usage("agent parent %s <子> <親>", verb)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	var res agent.ParentResult
	if err := callAppAs(db, app.PrincipalOf(by), op, agent.ParentReq{Name: args[0], Parent: args[1]}, &res); err != nil {
		return err
	}
	ps := strings.Join(res.Parents, " ")
	if ps == "" {
		ps = "(なし)"
	}
	note := ""
	if !res.Changed {
		note = "(変わりません)"
	}
	fmt.Fprintf(w, "%s の親: %s%s\n", res.Name, ps, note)
	return nil
}
