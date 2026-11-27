package recipe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/recipe"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// レシピの実行(recipe.run / recipe.runner。もとは taskboard/lib/recipes.sh の recipe-run / recipe-runner。ADR 0005 の段 3)。
// 記録はチケットの履歴 task_events(recipe_run / recipe_step / recipe_end)。actor は依頼者。

// partExec は実行できる部品(ここに無い部品は判定まで行い、実行しない)。
const partExec = " agent-status wait-idle task-show agent-stop agent-revive agent-restart agent-force-stop comment-resolve "

// RunReq は recipe.run の入力(tb recipe run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...])。
// 定期ジョブからの実行(SCHED_JOB / SCHED_OWNER)は入口が Env.SchedJob / SchedOwner に入れる(呼び手の入力では受けない)。
type RunReq struct {
	Name   string   `api:"name,path"`
	Target string   `api:"target,optional"`
	Task   string   `api:"task,optional"`
	DryRun bool     `api:"dry_run,optional"`
	Args   []string `api:"args,optional"` // <名前>=<値>
}

// RunResult は実行の結果。Out / Err は tb が標準出力・標準エラーに出す行(Env.Out があれば Out はそちらへ流し、ここは空)。
// Code は tb の終了コード(0 = 済んだか待ち行列に置いた / 1 = 途中の部品が失敗 / 2 = 使い方 / 4 = 実行しない / 20 = 人の承認が要る)。
type RunResult struct {
	Out     string       `api:"out,optional" json:"out,omitempty"`
	Err     string       `api:"err,optional" json:"err,omitempty"`
	Code    int          `api:"code" json:"code"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *RunResult) SetPending(p *app.Pending) { r.Pending = p }

// RunnerReq は recipe.runner の入力(tb recipe runner [--max N])。Max は 1〜999(0 なら 1)。
type RunnerReq struct {
	Max int `api:"max,optional"`
}

// RunnerResult は実行した依頼ごとの 1 行(R-<n> <状態>)と、途中の警告(tb の標準エラー)。
type RunnerResult struct {
	Lines   []string     `api:"lines" json:"lines"`
	Err     string       `api:"err,optional" json:"err,omitempty"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *RunnerResult) SetPending(p *app.Pending) { r.Pending = p }

func runOps(env *app.Env) []app.Op {
	run := app.Command(env, "recipe.run", app.Entry("cmd:recipe:run", "tb recipe-run"),
		func(ctx context.Context, in RunReq) (RunResult, error) {
			by := who(ctx)
			switch {
			case by == "":
				return RunResult{}, define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
			case strings.HasPrefix(by, "system:"): // scheduler などは人の端末と同じ(tb の requester と同じ。定期ジョブの持ち主は Env.SchedOwner)
				by = "human"
			}
			if err := validName(in.Name); err != nil {
				return RunResult{}, err
			}
			if in.Task != "" && !ticket.ReID.MatchString(in.Task) {
				return RunResult{}, define.Errorf(define.CodeInvalidArgument, "タスク ID は T-<project>-NNN 形式: %s", in.Task)
			}
			return withDB(env, func(db *store.DB) (RunResult, error) { return runRecipe(ctx, env, db, by, in) })
		},
		define.Doc("登録したレシピを実行する(tb recipe run。レシピと部品ごとに依頼者の権限で判定してから実行する。pane の外の部品は待ち行列に置く)"),
		define.Authz("cmd:recipe:run", define.Resource("recipe", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/recipes/{name}/run"),
	)
	runner := app.Command(env, "recipe.runner", app.Cmd("cmd:recipe:runner", "tb recipe-runner"),
		func(ctx context.Context, in RunnerReq) (RunnerResult, error) {
			if in.Max == 0 {
				in.Max = 1
			}
			if in.Max < 1 || in.Max > 999 {
				return RunnerResult{}, define.Errorf(define.CodeInvalidArgument, "recipe-runner: --max は 1〜999: %d", in.Max)
			}
			return withDB(env, func(db *store.DB) (RunnerResult, error) { return runner(ctx, env, db, in.Max) })
		},
		define.Doc("待ち行列の実行の依頼を古い順に取り出し、判定し直してから実行する(tb recipe runner。既定は人だけ)"),
		define.Authz("cmd:recipe:runner"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/recipe-runs/run"),
	)
	return []app.Op{run, runner}
}

// recipeRun は 1 回の実行の状態(bash の RUN_* / REQ_BY / JUDGE_*)。
type recipeRun struct {
	ctx       context.Context
	env       *app.Env
	db        *store.DB
	st        tickets.StatusSet
	by        string // 依頼者(REQ_BY)
	task, eid string // RUN_TASK / RUN_EID
	target    string
	via       string
	rules     []string // JUDGE_RULES
	stop      string   // JUDGE_STOP
	rc        int      // JUDGE_RC
	outside   bool
	execStop  string
	errw      io.Writer
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// event は recipe_event: 履歴に 1 行足して id を返す。
func (r *recipeRun) event(kind, old, nw, ref string) string {
	var o any
	if old != "" {
		o = old
	}
	res, err := r.db.Exec(`INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		r.db.Session, r.task, kind, o, nw, r.by, ref, r.env.NowString())
	if err != nil {
		return ""
	}
	id, _ := res.LastInsertId()
	return strconv.FormatInt(id, 10)
}

// planStep は plan の 1 件(引数は書いた順)。
type planStep struct {
	part string
	args jv.Obj
}

func planJSON(plan []planStep) string {
	var a []any
	for _, s := range plan {
		a = append(a, jv.Obj{{K: "part", V: s.part}, {K: "args", V: s.args}})
	}
	if a == nil {
		return "[]"
	}
	return jv.Compact(a)
}

func planOf(s string) ([]planStep, error) {
	vs, err := jv.DecodeAll([]byte(s))
	if err != nil || len(vs) != 1 {
		return nil, fmt.Errorf("plan を読めません")
	}
	arr, _ := vs[0].([]any)
	var out []planStep
	for _, x := range arr {
		o, _ := x.(jv.Obj)
		p, _ := o.Get("part").(string)
		args, _ := o.Get("args").(jv.Obj)
		out = append(out, planStep{p, args})
	}
	return out, nil
}

func (s planStep) kvs() []string {
	var out []string
	for _, kv := range s.args {
		out = append(out, kv.K+"="+jv.Scalar(kv.V))
	}
	return out
}

// judge は recipe_judge: 部品ごとに依頼者の権限で判定し、1 行ずつ出す。止まる step があれば stop と rc(4 / 20)。
func (r *recipeRun) judge(plan []planStep, w io.Writer) error {
	r.stop, r.rc, r.rules, r.outside = "", 0, nil, false
	j := approval.NewJudge(r.db.DB, r.db.Session)
	for i, s := range plan {
		n := i + 1
		verdict, rule := "allow", ""
		p, ok := recipe.Lookup(s.part)
		var val map[string]string
		why := ""
		if !ok {
			why = "部品の一覧にありません: " + s.part
		} else {
			val, why = recipe.CheckArgs(p, s.kvs())
		}
		switch {
		case why != "":
			verdict, rule, r.rc = "deny", why, 4
		case r.agentsBad(p, val) != "":
			verdict, rule, r.rc = "deny", r.agentsBad(p, val), 4
		case p.HumanOnly() && r.by != "human":
			verdict, rule, r.rc = "deny", "人だけの部品(コードで固定)", 4
		case !strings.Contains(partExec, " "+s.part+" "):
			verdict, rule, r.rc = "deny", "この部品の実行はまだありません(判定だけ)", 4
		default:
			// agent から指揮官を止める・戻すことはしない(tb agent-restart と同じ)
			if t := val["target"]; r.by != "human" && t != "" && (strings.Contains(p.Ops, "agent:stop") || strings.Contains(p.Ops, "agent:revive")) {
				for _, c := range message.ConductorNames(r.db.DB, r.db.Session) {
					if c != "" && c == t {
						verdict, rule, r.rc = "deny", "指揮官("+t+")は agent から止めない・戻さない(人が実行します)", 4
					}
				}
			}
			if verdict == "allow" {
				ops, err := recipe.Ops(p, val)
				if err != nil {
					return define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				for _, k := range ops {
					if r.by == "human" {
						rule = joinRule(rule, k+": 人の端末")
						continue
					}
					v, ru := j.Of(k, r.by)
					rule = joinRule(rule, k+": "+ru)
					if v == "deny" {
						verdict, r.rc = "deny", 4
					} else if v != "allow" {
						if verdict != "deny" {
							verdict = "ask"
						}
						if r.rc != 4 {
							r.rc = 20
						}
					}
				}
				if rule == "" {
					rule = "判定の操作名なし(読み取り)"
				}
			}
			if p.Runner == "outside" {
				r.outside = true
			}
		}
		r.rules = append(r.rules, verdict+" "+rule)
		fmt.Fprintf(w, "step %d %s: %s(%s)\n", n, s.part, verdict, rule)
		if verdict != "allow" {
			r.stop = fmt.Sprintf("step %d (%s) で止めました: %s(%s)", n, s.part, verdict, rule)
			return nil
		}
	}
	return nil
}

func joinRule(a, b string) string {
	if a == "" {
		return b
	}
	return a + " / " + b
}

// agentsBad は recipe_agents_ok: agent 型の値が依頼者か対象か。
func (r *recipeRun) agentsBad(p recipe.Part, val map[string]string) string {
	for _, spec := range strings.Fields(p.Args) {
		nm, t, _ := strings.Cut(spec, ":")
		if t != "agent" {
			continue
		}
		k := strings.TrimSuffix(nm, "?")
		v, has := val[k]
		if !has || v == r.by || v == r.target {
			continue
		}
		return p.Name + " の " + k + "(" + v + ")は依頼者でも対象でもありません"
	}
	return ""
}

// errText は部品の中で呼んだ操作の失敗を、tb のコマンドを呼んだときと同じ「tb: <理由>」の行にする。
func errText(out string, err error) string {
	msg := err.Error()
	if e, ok := err.(*define.Error); ok {
		msg = or(e.Message, string(e.Code))
	}
	if out != "" {
		out += "\n"
	}
	return out + "tb: " + msg
}

func agentStatusOf(name string) string {
	out, err := herdr.Call("agent", "get", name)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.NewDecoder(bytes.NewReader(out)).Decode(&m) != nil {
		return ""
	}
	res, _ := m["result"].(map[string]any)
	a, _ := res["agent"].(map[string]any)
	v := a["agent_status"]
	if v == nil || v == false {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return jv.Compact(v)
}

// partRun は part_run: 部品を実行する。出力と終了コード(0 = 成功)。label は実行の記録に残す名前(recipe:E-<n> など)。
func (r *recipeRun) partRun(part string, val map[string]string, label string) (string, int) {
	t := val["target"]
	withPrompt := func(o jv.Obj) string {
		if p := val["prompt"]; p != "" {
			o = append(o, jv.KV{K: "prompt", V: p})
		}
		return jv.Compact(o)
	}
	record := func(op, args string, rc int, result string) {
		agents.RecordOp(r.db.DB, r.db.Session, op, t, args, r.by, rc, result)
	}
	switch part {
	case "agent-status":
		st := agentStatusOf(t)
		if st == "" {
			return t + " の状態を引けません", 1
		}
		return t + " " + st, 0
	case "wait-idle":
		t0 := time.Now()
		limit, _ := strconv.Atoi(val["timeout"])
		for {
			st := agentStatusOf(t)
			if st == "idle" || st == "done" {
				return t + " " + st, 0
			}
			if int(time.Since(t0).Seconds()) >= limit {
				return t + " は " + val["timeout"] + " 秒で idle になりませんでした(" + or(st, "状態不明") + ")", 1
			}
			select {
			case <-r.ctx.Done(): // 呼び手が待つのをやめた(Web UI の接続が切れたなど)
				return t + " を待つのをやめました(呼び出しが取り消された)", 1
			case <-time.After(time.Duration(agent.SecEnv("TB_RECIPE_POLL_SEC", 2) * float64(time.Second))):
			}
		}
	case "task-show":
		out, err := ticket.Show(r.db, val["task"])
		if err != nil {
			return errText("", err), 1
		}
		return out, 0
	case "comment-resolve":
		ref := strings.TrimPrefix(val["comment"], "C-")
		res, err := ticket.ResolveComment(r.env, ticket.ResolveReq{ID: ref})
		if err != nil {
			return errText("", err), 1
		}
		if res.Already {
			return fmt.Sprintf("C-%s はすでに対応済みです(%s)", ref, res.ResolvedAt), 0
		}
		return fmt.Sprintf("C-%s resolved", ref), 0
	case "agent-stop", "agent-force-stop":
		force := part == "agent-force-stop"
		f := 0
		if force {
			f = 1
		}
		args := jv.Compact(jv.Obj{{K: "force", V: jv.Number(strconv.Itoa(f))}, {K: "recipe", V: label}})
		res := agent.StopContext(r.ctx, r.db, r.st, t, force, label)
		record("stop", args, res.Code, label+": "+res.Out)
		return res.Out, res.Code
	case "agent-revive":
		args := withPrompt(jv.Obj{{K: "fresh", V: jv.Number("0")}, {K: "recipe", V: label}})
		if agent.RefuseRunning(r.db, 1, io.Discard) {
			out := "動作中の agent が上限のため戻しません"
			record("revive", args, 4, label+": "+out)
			return out, 4
		}
		res := agent.ReviveContext(r.ctx, r.db, t, false, "", val["prompt"], label, r.errw)
		record("revive", args, res.Code, label+": "+res.Out)
		return res.Out, res.Code
	case "agent-restart":
		args := withPrompt(jv.Obj{{K: "restart", V: jv.Number("1")}, {K: "recipe", V: label}})
		if agent.RefuseRunning(r.db, 0, io.Discard) {
			out := "動作中の agent が上限を超えているため触りません"
			record("revive", args, 4, label+": "+out)
			return out, 4
		}
		res := agent.RestartContext(r.ctx, r.db, r.st, t, val["prompt"], label, r.errw)
		record("revive", args, res.Code, "restart: "+label+": "+res.Out)
		return res.Out, res.Code
	}
	return "この部品の実行はまだありません: " + part, 1
}

// exec は recipe_exec: 並びどおりに実行する。途中で失敗したら残りは続行しない(巻き戻さない)。
func (r *recipeRun) exec(plan []planStep, label string, w io.Writer) bool {
	r.execStop = ""
	for i, s := range plan {
		n := i + 1
		p, ok := recipe.Lookup(s.part)
		var val map[string]string
		why := ""
		if !ok {
			why = "部品の一覧にありません: " + s.part
		} else {
			val, why = recipe.CheckArgs(p, s.kvs())
		}
		if why != "" {
			r.execStop = fmt.Sprintf("step %d の引数を確かめ直せません: %s", n, why)
			return false
		}
		out, rc := r.partRun(s.part, val, label)
		res := "ok"
		if rc != 0 {
			res = "failed"
		}
		rule := ""
		if i < len(r.rules) {
			rule = r.rules[i]
		}
		r.event("recipe_step", fmt.Sprintf("%d %s", n, s.part), rule+" / "+res+": "+util.Oneline(out, 300), "E-"+r.eid)
		fmt.Fprintln(w, out)
		if rc != 0 {
			r.execStop = fmt.Sprintf("step %d (%s) が失敗しました: %s。step %d 以降は実行していません(済んだ step は巻き戻しません)", n, s.part, util.Oneline(out, 300), n+1)
			return false
		}
	}
	return true
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// recipeBody はこのセッションのレシピの本文($(...) と同じく末尾の改行は落とす)。無ければ ok=false。
func recipeBody(db *store.DB, name string) (string, bool) {
	var b sql.NullString
	if db.QueryRow(`SELECT body FROM recipes WHERE name=?`, name).Scan(&b) != nil {
		return "", false
	}
	body := strings.TrimRight(b.String, "\n")
	return body, body != ""
}

// runRecipe は recipe-run の中身。
func runRecipe(ctx context.Context, env *app.Env, db *store.DB, by string, in RunReq) (RunResult, error) {
	var outBuf, errBuf bytes.Buffer
	w := io.Writer(&outBuf)
	if env.Out != nil {
		w = env.Out
	}
	var warn io.Writer = &errBuf // 部品の途中の警告(agent の戻しなど)。入口に Warn があればすぐ出す
	if env.Warn != nil {
		warn = env.Warn
	}
	r := &recipeRun{ctx: ctx, env: env, db: db, st: tickets.LoadStatuses(db.DB, db.Session), by: by, target: in.Target, task: in.Task, errw: warn}
	name, dry := in.Name, in.DryRun
	result := func(code int) RunResult { return RunResult{Out: outBuf.String(), Err: errBuf.String(), Code: code} }
	if r.task != "" {
		ok, err := tickets.Exists(db.DB, r.task)
		if err != nil {
			return RunResult{}, ierr(err)
		}
		if !ok {
			return RunResult{}, define.Errorf(define.CodeNotFound, "タスクが見つかりません: %s", r.task)
		}
	}
	// 定期ジョブ(SCHED_JOB)から人の端末で呼ばれたら、ジョブの持ち主(SCHED_OWNER)の権限にする
	if job, owner := env.SchedJob, env.SchedOwner; by == "human" && job != "" {
		why := ""
		switch {
		case owner == "":
			why = fmt.Sprintf("定期ジョブ(%s)からは、ジョブに持ち主(OWNER=<agent 名>)があるときだけ実行します(人の権限では実行しない)", job)
		case recipe.ValueOK("agent", owner) != "":
			why = fmt.Sprintf("ジョブ %s の OWNER が agent 名ではありません: %s", job, owner)
		}
		if why != "" {
			fmt.Fprintln(&errBuf, "tb: recipe-run: "+why)
			if !dry {
				r.eid = r.event("recipe_run", "", name+" target="+or(r.target, "-")+" via=sched:"+job, "recipe:"+name+":"+or(r.target, "-"))
				r.event("recipe_end", "", "定期ジョブの持ち主を確かめられないため実行しません", "E-"+r.eid)
			}
			return result(4), nil
		}
		r.by, r.via = owner, "sched:"+job
	}
	if r.target == "" {
		if r.by == "human" {
			return RunResult{}, define.Errorf(define.CodeInvalidArgument, "recipe-run: 人の端末からは --target <agent 名> が要ります(既定の対象は依頼者自身)")
		}
		r.target = r.by
	}
	if why := recipe.ValueOK("agent", r.target); why != "" {
		return RunResult{}, define.Errorf(define.CodeInvalidArgument, "recipe-run: --target: %s: %s", why, r.target)
	}
	op := "recipe:" + name + ":" + r.target
	end := func(code int, why string) (RunResult, error) {
		if !dry {
			r.event("recipe_end", "", or(why, "done"), "E-"+r.eid)
		}
		if why != "" {
			fmt.Fprintln(&errBuf, "tb: recipe-run: "+why)
		}
		return result(code), nil
	}
	if !dry {
		via := ""
		if r.via != "" {
			via = " via=" + r.via
		}
		r.eid = r.event("recipe_run", "", name+" target="+r.target+via, op)
	}
	body, has := recipeBody(db, name)
	if !has {
		return end(4, "登録されていないレシピは実行しません: "+name+"(登録は人が tb recipe-set)")
	}
	rc, perr := recipe.Parse(body)
	if perr != "" {
		first, _, _ := strings.Cut(perr, "\n")
		return end(4, "レシピ "+name+" は今のコードでは検査を通らないため実行しません: "+first)
	}
	// 引数: 宣言したものだけ。宣言の型で確かめる
	bound := map[string]string{}
	for _, kv := range in.Args {
		k, v, okk := strings.Cut(kv, "=")
		if !okk {
			return end(2, "引数は <名前>=<値>: "+kv)
		}
		t, decl := rc.ArgT[k]
		if !decl {
			return end(2, "レシピ "+name+" に引数 "+k+" はありません(宣言: "+or(strings.Join(rc.ArgN, " "), "なし")+")")
		}
		if _, dup := bound[k]; dup {
			return end(2, "引数 "+k+" が 2 回あります")
		}
		if why := recipe.ValueOK(t, v); why != "" {
			return end(2, "引数 "+k+": "+why)
		}
		bound[k] = v
	}
	for _, k := range rc.ArgN {
		if strings.HasSuffix(k, "?") {
			continue
		}
		if _, okk := bound[k]; !okk {
			return end(2, "引数 "+k+" がありません")
		}
	}
	// 並び(plan)を作る: 変数を値に置き換える。省略された引数は、その部品の引数も省く
	var plan []planStep
	for _, step := range rc.Steps {
		tok := strings.FieldsFunc(step, func(c rune) bool { return c == ' ' || c == '\t' || c == '\n' })
		if len(tok) == 0 {
			continue
		}
		ps := planStep{part: tok[0], args: jv.Obj{}}
		for _, kv := range tok[1:] {
			k, v, cut := strings.Cut(kv, "=")
			if !cut {
				v = kv
			}
			switch {
			case v == "$requester":
				if r.by == "human" {
					return end(4, "人の端末からは $requester を使うレシピ("+name+")は実行できません。agent の pane から実行します")
				}
				v = r.by
			case v == "$target":
				v = r.target
			case strings.HasPrefix(v, "$"):
				bv, okk := bound[v[1:]]
				if !okk {
					continue
				}
				v = bv
			}
			ps.args = ps.args.Set(k, v)
		}
		plan = append(plan, ps)
	}
	// 判定 1: レシピの操作名。人の端末は承認なし。agent は policy(recipe:<名前> <対象>)
	verdict, rule := "allow", "人の端末"
	if r.by != "human" {
		verdict, rule = approval.NewJudge(db.DB, db.Session).Of(op, r.by)
	}
	fmt.Fprintf(w, "recipe %s target=%s risk=%s: %s(%s)\n", name, r.target, rc.Risk, verdict, rule)
	if verdict == "deny" {
		if !dry {
			r.event("recipe_step", "0 "+op, verdict+" "+rule, "E-"+r.eid)
		}
		return end(4, op+" は実行できません(deny。"+rule+")")
	}
	if verdict != "allow" {
		if !dry {
			r.event("recipe_step", "0 "+op, verdict+" "+rule, "E-"+r.eid)
		}
		return end(20, op+" は人の承認が要ります("+rule+")。人が自分の端末で実行するか、人が allow にします")
	}
	// 判定 2: 部品ごと。全部を判定してから実行する
	if err := r.judge(plan, w); err != nil {
		return RunResult{}, err
	}
	if dry {
		if r.stop == "" {
			return result(0), nil
		}
		fmt.Fprintln(&errBuf, "tb: recipe-run: "+r.stop)
		return result(r.rc), nil
	}
	if r.stop != "" {
		for i, ru := range r.rules {
			r.event("recipe_step", fmt.Sprintf("%d %s", i+1, plan[i].part), ru+" / 未実行", "E-"+r.eid)
		}
		return end(r.rc, r.stop+"。どの部品も実行していません")
	}
	// pane の外で行う部品があれば、検査済みの並びだけを待ち行列に置く(人の端末からの依頼はその場で実行する)
	if r.outside && r.by != "human" {
		var via any
		if r.via != "" {
			via = r.via
		}
		eid, _ := strconv.ParseInt(r.eid, 10, 64)
		res, err := db.Exec(`INSERT INTO main.recipe_runs (herdr_session, name, body_sha, target, requester, via, task_id, plan, event_id, status, created_at)
			VALUES (?,?,?,?,?,?,?,?,?, 'queued', ?)`, db.Session, name, sha256Hex(body), r.target, r.by, via, r.task, planJSON(plan), eid, env.NowString())
		if err != nil {
			return RunResult{}, ierr(err)
		}
		rid, _ := res.LastInsertId()
		r.event("recipe_step", "0 queue", fmt.Sprintf("R-%d queued(pane の外の実行役が実行します)", rid), "E-"+r.eid)
		fmt.Fprintf(w, "R-%d queued (外の実行役 tb recipe-runner が実行します。状態は tb recipe-queue)\n", rid)
		return result(0), nil
	}
	if !r.exec(plan, "recipe:E-"+r.eid, w) {
		return end(1, r.execStop)
	}
	return end(0, "")
}

var reSec6 = regexp.MustCompile(`^[0-9]{1,6}$`)

// runRow は recipe_runs の 1 行。
type runRow struct {
	id                                                        int64
	name, bodySha, target, requester, task, plan, status, eid string
}

func runRowOf(db *store.DB, id int64) (runRow, bool) {
	r := runRow{id: id}
	var eid int64
	if db.QueryRow(`SELECT name, body_sha, target, requester, task_id, plan, status, event_id FROM recipe_runs WHERE id=?`, id).
		Scan(&r.name, &r.bodySha, &r.target, &r.requester, &r.task, &r.plan, &r.status, &eid) != nil {
		return r, false
	}
	r.eid = strconv.FormatInt(eid, 10)
	return r, true
}

func int64Rows(db *store.DB, q string, args ...any) []int64 {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// runner は recipe-runner の中身: 待ち行列の queued を古い順に max 件まで取り出し、判定し直してから実行する。
func runner(ctx context.Context, env *app.Env, db *store.DB, max int) (RunnerResult, error) {
	ttl := envOr("TB_RECIPE_QUEUE_TTL_SEC", "900")
	if !reSec6.MatchString(ttl) {
		return RunnerResult{}, define.Errorf(define.CodeFailedPrecondition, "TB_RECIPE_QUEUE_TTL_SEC は秒の整数: %s", ttl)
	}
	runMax := envOr("TB_RECIPE_RUNNING_MAX_SEC", "3600")
	if !reSec6.MatchString(runMax) {
		return RunnerResult{}, define.Errorf(define.CodeFailedPrecondition, "TB_RECIPE_RUNNING_MAX_SEC は秒の整数: %s", runMax)
	}
	ttlN, _ := strconv.Atoi(ttl)
	runN, _ := strconv.Atoi(runMax)
	var out RunnerResult
	var errBuf bytes.Buffer
	st := tickets.LoadStatuses(db.DB, db.Session)
	// 実行役が途中で落ちた(running のまま長く残った)ものは failed にする
	for _, id := range int64Rows(db, `SELECT id FROM recipe_runs WHERE status='running' AND (julianday('now') - julianday(started_at)) * 86400 > ? ORDER BY id`, runN) {
		RunClose(env, db, id, "failed", fmt.Sprintf("実行役が %s 秒たっても終わっていません(途中で止まった可能性。済んだ step は記録を見てください)", runMax), "running", "")
	}
	fresh := fmt.Sprintf("(julianday('now') - julianday(created_at)) * 86400 <= %d", ttlN)
	for n := 0; n < max; {
		for _, id := range int64Rows(db, `SELECT id FROM recipe_runs WHERE status='queued' AND NOT (`+fresh+`) ORDER BY id`) {
			RunClose(env, db, id, "expired", fmt.Sprintf("置いてから %s 秒を過ぎたため実行しません", ttl), "queued", "")
		}
		ids := int64Rows(db, `SELECT id FROM recipe_runs WHERE status='queued' AND `+fresh+` ORDER BY id LIMIT 1`)
		if len(ids) == 0 {
			break
		}
		id := ids[0]
		res, err := db.Exec(`UPDATE main.recipe_runs SET status='running', started_at=? WHERE herdr_session=? AND id=? AND status='queued' AND `+fresh, env.NowString(), db.Session, id)
		if err != nil { // 済んだ分の行は返す
			fmt.Fprintf(&errBuf, "tb: recipe-runner: R-%d を取り出せません: %v\n", id, err)
			break
		}
		if c, _ := res.RowsAffected(); c != 1 {
			continue
		}
		n++
		row, _ := runRowOf(db, id)
		runnerOne(ctx, env, db, st, row, &errBuf)
		var cur string
		_ = db.QueryRow(`SELECT status FROM recipe_runs WHERE id=?`, id).Scan(&cur)
		out.Lines = append(out.Lines, fmt.Sprintf("R-%d %s", id, cur))
	}
	out.Err = errBuf.String()
	return out, nil
}

// runnerOne は _runner_one: 1 件を判定し直してから実行する。
// 判定の途中の失敗(部品の操作名を作れないなど)も、その依頼を止めて次へ進む(済んだ依頼の行を失わない)。
func runnerOne(ctx context.Context, env *app.Env, db *store.DB, st tickets.StatusSet, row runRow, errw io.Writer) {
	id := row.id
	stop := func(why string) { RunClose(env, db, id, "stopped", why, "running", "") }
	r := &recipeRun{ctx: ctx, env: env, db: db, st: st, by: row.requester, target: row.target, task: row.task, eid: row.eid, errw: errw}
	if recipe.ValueOK("agent", row.requester) != "" {
		stop("依頼者が agent 名ではありません(人の依頼は待ち行列では実行しない)")
		return
	}
	if recipe.ValueOK("agent", row.target) != "" {
		stop("対象が agent 名ではありません")
		return
	}
	body, has := recipeBody(db, row.name)
	if !has {
		stop("レシピ " + row.name + " の登録が消えたため実行しません")
		return
	}
	if sha256Hex(body) != row.bodySha {
		stop("レシピ " + row.name + " の本文が変わったため実行しません(もう一度頼みます)")
		return
	}
	if _, perr := recipe.Parse(body); perr != "" {
		stop("レシピ " + row.name + " は今のコードでは検査を通りません")
		return
	}
	plan, err := planOf(row.plan)
	if err != nil {
		stop(err.Error())
		return
	}
	if v, rule := approval.NewJudge(db.DB, db.Session).Of("recipe:"+row.name+":"+row.target, row.requester); v != "allow" {
		stop("recipe:" + row.name + ":" + row.target + " が実行の時点で allow ではありません(" + rule + ")")
		return
	}
	if err := r.judge(plan, io.Discard); err != nil {
		stop("実行の時点の判定に失敗しました: " + errText("", err))
		return
	}
	if r.stop != "" {
		stop("実行の時点の判定で止めました: " + strings.TrimPrefix(r.stop, "step "))
		return
	}
	if r.exec(plan, fmt.Sprintf("recipe:R-%d", id), io.Discard) {
		RunClose(env, db, id, "done", "", "running", "")
	} else {
		RunClose(env, db, id, "failed", r.execStop, "running", "")
	}
}
