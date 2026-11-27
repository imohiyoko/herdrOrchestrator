package tbcli

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	apprecipe "github.com/imohiyoko/herdrorchestrator/internal/app/recipe"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/recipe"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// レシピの実行(taskboard/lib/recipes.sh の recipe-run / recipe-runner / recipe-queue / recipe-cancel。ADR 0005 の段 3)。
// 記録はチケットの履歴 task_events(recipe_run / recipe_step / recipe_end)。actor は依頼者。

// partExec は実行できる部品(ここに無い部品は判定まで行い、実行しない)。
const partExec = " agent-status wait-idle task-show agent-stop agent-revive agent-restart agent-force-stop comment-resolve "

// recipeRun は 1 回の実行の状態(bash の RUN_* / REQ_BY / JUDGE_*)。
type recipeRun struct {
	db             *store.DB
	st             tickets.StatusSet
	by             string // 依頼者(REQ_BY)
	task, eid      string // RUN_TASK / RUN_EID
	target, via    string
	rules          []string // JUDGE_RULES
	stop           string   // JUDGE_STOP
	rc             int      // JUDGE_RC
	outside        bool
	execStop       string
	stdout, stderr io.Writer
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

// event は recipe_event: 履歴に 1 行足して id を返す。
func (r *recipeRun) event(kind, old, nw, ref string) string {
	var o any
	if old != "" {
		o = old
	}
	res, err := r.db.Exec(`INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		r.db.Session, r.task, kind, o, nw, r.by, ref, nowTS())
	if err != nil {
		return ""
	}
	id, _ := res.LastInsertId()
	return strconv.FormatInt(id, 10)
}

// requesterOf は recipe_requester: 定期ジョブ(SCHED_JOB)から人の端末で呼ばれたら、ジョブの持ち主(SCHED_OWNER)の権限にする。
// 持ち主を確かめられなければ ok=false(理由は stderr)。
func (r *recipeRun) requesterOf() (bool, error) {
	r.via = ""
	by, err := requester()
	if err != nil {
		return false, err
	}
	r.by = by
	job := os.Getenv("SCHED_JOB")
	if by != "human" || job == "" {
		return true, nil
	}
	owner := os.Getenv("SCHED_OWNER")
	if owner == "" {
		fmt.Fprintf(r.stderr, "tb: recipe-run: 定期ジョブ(%s)からは、ジョブに持ち主(OWNER=<agent 名>)があるときだけ実行します(人の権限では実行しない)\n", job)
		return false, nil
	}
	if recipe.ValueOK("agent", owner) != "" {
		fmt.Fprintf(r.stderr, "tb: recipe-run: ジョブ %s の OWNER が agent 名ではありません: %s\n", job, owner)
		return false, nil
	}
	r.by, r.via = owner, "sched:"+job
	return true, nil
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
					return die("%s", err.Error())
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

// subCmd は (cmd_xxx) 2>&1: ほかのコマンドの出力(と tb: の理由)をまとめて取る。末尾の改行は落とす。
func subCmd(f cmdFunc, args []string) (string, bool) {
	var b bytes.Buffer
	err := f(args, &b, &b)
	if err != nil {
		var e *exit
		if !errors.As(err, &e) {
			e = &exit{exitErr, err.Error()}
		}
		if e.msg != "" {
			fmt.Fprintf(&b, "tb: %s\n", e.msg)
			if e.code == exitUsage {
				fmt.Fprintln(&b, "(tb help で使い方)")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), err == nil
}

func agentStatusOf(name string) string {
	out, err := herdr.Call("agent", "get", name)
	if err != nil {
		return ""
	}
	m := firstJSONObj(out)
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
			time.Sleep(time.Duration(secEnv("TB_RECIPE_POLL_SEC", 2) * float64(time.Second)))
		}
	case "task-show":
		out, ok := subCmd(cmdShow, []string{val["task"]})
		if !ok {
			return out, 1
		}
		return out, 0
	case "comment-resolve":
		out, ok := subCmd(cmdResolve, []string{val["comment"]})
		if !ok {
			return out, 1
		}
		return out, 0
	case "agent-stop", "agent-force-stop":
		force := part == "agent-force-stop"
		f := 0
		if force {
			f = 1
		}
		args := jv.Compact(jv.Obj{{K: "force", V: jv.Number(strconv.Itoa(f))}, {K: "recipe", V: label}})
		res := doAgentStop(r.db, r.st, t, force, label)
		recordOp(r.db, "stop", t, args, r.by, res.code, label+": "+res.out)
		return res.out, res.code
	case "agent-revive":
		args := withPrompt(jv.Obj{{K: "fresh", V: jv.Number("0")}, {K: "recipe", V: label}})
		if limitsRefuseRunning(r.db, 1, io.Discard) != nil {
			out := "動作中の agent が上限のため戻しません"
			recordOp(r.db, "revive", t, args, r.by, 4, label+": "+out)
			return out, 4
		}
		res := doAgentRevive(r.db, t, false, "", val["prompt"], label, r.stderr)
		recordOp(r.db, "revive", t, args, r.by, res.code, label+": "+res.out)
		return res.out, res.code
	case "agent-restart":
		args := withPrompt(jv.Obj{{K: "restart", V: jv.Number("1")}, {K: "recipe", V: label}})
		if limitsRefuseRunning(r.db, 0, io.Discard) != nil {
			out := "動作中の agent が上限を超えているため触りません"
			recordOp(r.db, "revive", t, args, r.by, 4, label+": "+out)
			return out, 4
		}
		res := doAgentRestart(r.db, r.st, t, val["prompt"], label, r.stderr)
		recordOp(r.db, "revive", t, args, r.by, res.code, "restart: "+label+": "+res.out)
		return res.out, res.code
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

// cmdRecipeRun は tb recipe-run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]。
// 終了コード 0=済んだか待ち行列に置いた / 1=途中の部品が失敗 / 2=使い方 / 4=実行しない / 20=人の承認が要る
func cmdRecipeRun(args []string, w, stderr io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage("recipe-run <名前> [--target A] [--task T-..] [--dry-run] [<名前>=<値> ...]")
	}
	name := args[0]
	if err := validRecipeName(name); err != nil {
		return err
	}
	r := &recipeRun{stdout: w, stderr: stderr}
	dry := false
	var given []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--target" || a == "--task":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			i++
			if a == "--target" {
				r.target = v
			} else {
				if err := needID(v); err != nil {
					return err
				}
				r.task = v
			}
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "-"):
			return usage("recipe-run: 不明な引数 %s", a)
		default:
			given = append(given, a)
		}
	}
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	r.db, r.st = db, st
	if r.task != "" {
		if err := requireTask(db, r.task); err != nil {
			return err
		}
	}
	ok, err := r.requesterOf()
	if err != nil {
		return err
	}
	if !ok {
		if !dry {
			r.eid = r.event("recipe_run", "", name+" target="+or(r.target, "-")+" via=sched:"+os.Getenv("SCHED_JOB"), "recipe:"+name+":"+or(r.target, "-"))
			r.event("recipe_end", "", "定期ジョブの持ち主を確かめられないため実行しません", "E-"+r.eid)
		}
		return &exit{4, ""}
	}
	if r.target == "" {
		if r.by == "human" {
			return usage("recipe-run: 人の端末からは --target <agent 名> が要ります(既定の対象は依頼者自身)")
		}
		r.target = r.by
	}
	if why := recipe.ValueOK("agent", r.target); why != "" {
		return usage("recipe-run: --target: %s: %s", why, r.target)
	}
	op := "recipe:" + name + ":" + r.target
	end := func(code int, why string) error {
		if !dry {
			r.event("recipe_end", "", or(why, "done"), "E-"+r.eid)
		}
		if why != "" {
			fmt.Fprintln(stderr, "tb: recipe-run: "+why)
		}
		if code == 0 {
			return nil
		}
		return &exit{code, ""}
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
	for _, kv := range given {
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
		return err
	}
	if dry {
		if r.stop == "" {
			return nil
		}
		fmt.Fprintln(stderr, "tb: recipe-run: "+r.stop)
		return &exit{r.rc, ""}
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
		res, err := db.Exec(`INSERT INTO main.recipe_runs (herdr_session, name, body_sha, target, requester, via, task_id, plan, event_id, status, created_at)
			VALUES (?,?,?,?,?,?,?,?,?, 'queued', ?)`, db.Session, name, sha256Hex(body), r.target, r.by, via, r.task, planJSON(plan), atoi(r.eid), nowTS())
		if err != nil {
			return err
		}
		rid, _ := res.LastInsertId()
		r.event("recipe_step", "0 queue", fmt.Sprintf("R-%d queued(pane の外の実行役が実行します)", rid), "E-"+r.eid)
		fmt.Fprintf(w, "R-%d queued (外の実行役 tb recipe-runner が実行します。状態は tb recipe-queue)\n", rid)
		return nil
	}
	if !r.exec(plan, "recipe:E-"+r.eid, w) {
		return end(1, r.execStop)
	}
	return end(0, "")
}

var (
	reRunnerMax = regexp.MustCompile(`^[0-9]{1,3}$`)
	reSec6      = regexp.MustCompile(`^[0-9]{1,6}$`)
	reRid       = regexp.MustCompile(`^R-([0-9]{1,18})$`)
)

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

// runnerClose は _runner_close: 状態を from から st に変え(変わったときだけ)、recipe_end を残す。actor が空なら依頼者。
func runnerClose(db *store.DB, id int64, st, why, from, actor string) {
	apprecipe.RunClose(&app.Env{Now: now}, db, id, st, why, from, actor)
}

func int64Rows(db *store.DB, q string, args ...any) []int64 {
	var out []int64
	_ = qrows(db.DB, q, args, func(_ []string, v []any) error {
		if n, ok := v[0].(int64); ok {
			out = append(out, n)
		}
		return nil
	})
	return out
}

// cmdRecipeRunner は tb recipe-runner [--max N]: 待ち行列の queued を古い順に取り出し、判定し直してから実行する(人の端末からだけ)。
func cmdRecipeRunner(args []string, w, stderr io.Writer) error {
	max := 1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--max":
			v, err := needVal(args, i)
			if err != nil {
				return err
			}
			if !reRunnerMax.MatchString(v) || atoi(v) < 1 {
				return usage("recipe-runner: --max は 1〜999: %s", v)
			}
			max = atoi(v)
			i++
		default:
			return usage("recipe-runner: 不明な引数 %s", args[i])
		}
	}
	if err := requireHuman("tb recipe-runner"); err != nil {
		return err
	}
	ttl := envOr("TB_RECIPE_QUEUE_TTL_SEC", "900")
	if !reSec6.MatchString(ttl) {
		return die("TB_RECIPE_QUEUE_TTL_SEC は秒の整数: %s", ttl)
	}
	runMax := envOr("TB_RECIPE_RUNNING_MAX_SEC", "3600")
	if !reSec6.MatchString(runMax) {
		return die("TB_RECIPE_RUNNING_MAX_SEC は秒の整数: %s", runMax)
	}
	db, st, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	ttlN, runN := atoi(ttl), atoi(runMax)
	// 実行役が途中で落ちた(running のまま長く残った)ものは failed にする
	for _, id := range int64Rows(db, `SELECT id FROM recipe_runs WHERE status='running' AND (julianday('now') - julianday(started_at)) * 86400 > ? ORDER BY id`, runN) {
		runnerClose(db, id, "failed", fmt.Sprintf("実行役が %s 秒たっても終わっていません(途中で止まった可能性。済んだ step は記録を見てください)", runMax), "running", "")
	}
	fresh := fmt.Sprintf("(julianday('now') - julianday(created_at)) * 86400 <= %d", ttlN)
	for n := 0; n < max; {
		for _, id := range int64Rows(db, `SELECT id FROM recipe_runs WHERE status='queued' AND NOT (`+fresh+`) ORDER BY id`) {
			runnerClose(db, id, "expired", fmt.Sprintf("置いてから %s 秒を過ぎたため実行しません", ttl), "queued", "")
		}
		ids := int64Rows(db, `SELECT id FROM recipe_runs WHERE status='queued' AND `+fresh+` ORDER BY id LIMIT 1`)
		if len(ids) == 0 {
			break
		}
		id := ids[0]
		res, err := db.Exec(`UPDATE main.recipe_runs SET status='running', started_at=? WHERE herdr_session=? AND id=? AND status='queued' AND `+fresh, nowTS(), db.Session, id)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c != 1 {
			continue
		}
		n++
		row, _ := runRowOf(db, id)
		if err := runnerOne(db, st, row, stderr); err != nil {
			return err
		}
		var cur string
		_ = db.QueryRow(`SELECT status FROM recipe_runs WHERE id=?`, id).Scan(&cur)
		fmt.Fprintf(w, "R-%d %s\n", id, cur)
	}
	return nil
}

// runnerOne は _runner_one: 1 件を判定し直してから実行する。
func runnerOne(db *store.DB, st tickets.StatusSet, row runRow, stderr io.Writer) error {
	id := row.id
	stop := func(why string) { runnerClose(db, id, "stopped", why, "running", "") }
	r := &recipeRun{db: db, st: st, by: row.requester, target: row.target, task: row.task, eid: row.eid, stdout: io.Discard, stderr: stderr}
	if recipe.ValueOK("agent", row.requester) != "" {
		stop("依頼者が agent 名ではありません(人の依頼は待ち行列では実行しない)")
		return nil
	}
	if recipe.ValueOK("agent", row.target) != "" {
		stop("対象が agent 名ではありません")
		return nil
	}
	body, has := recipeBody(db, row.name)
	if !has {
		stop("レシピ " + row.name + " の登録が消えたため実行しません")
		return nil
	}
	if sha256Hex(body) != row.bodySha {
		stop("レシピ " + row.name + " の本文が変わったため実行しません(もう一度頼みます)")
		return nil
	}
	if _, perr := recipe.Parse(body); perr != "" {
		stop("レシピ " + row.name + " は今のコードでは検査を通りません")
		return nil
	}
	plan, err := planOf(row.plan)
	if err != nil {
		stop(err.Error())
		return nil
	}
	if v, rule := approval.NewJudge(db.DB, db.Session).Of("recipe:"+row.name+":"+row.target, row.requester); v != "allow" {
		stop("recipe:" + row.name + ":" + row.target + " が実行の時点で allow ではありません(" + rule + ")")
		return nil
	}
	if err := r.judge(plan, io.Discard); err != nil {
		return err
	}
	if r.stop != "" {
		stop("実行の時点の判定で止めました: " + strings.TrimPrefix(r.stop, "step "))
		return nil
	}
	if r.exec(plan, fmt.Sprintf("recipe:R-%d", id), io.Discard) {
		runnerClose(db, id, "done", "", "running", "")
	} else {
		runnerClose(db, id, "failed", r.execStop, "running", "")
	}
	return nil
}

// cmdRecipeQueue は tb recipe-queue [--all] [--json]。
func cmdRecipeQueue(args []string, w, stderr io.Writer) error {
	where := "status IN ('queued','running')"
	json := false
	for _, a := range args {
		switch a {
		case "--all":
			where = "1=1"
		case "--json":
			json = true
		default:
			return usage("recipe-queue: 不明な引数 %s", a)
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if json {
		rows, err := sqlObjs(db.DB, []string{"plan"}, `SELECT 'R-'||id AS id, status, name, target, requester, via, task_id, plan, 'E-'||event_id AS event, created_at, started_at, finished_at, result FROM recipe_runs WHERE `+where+` ORDER BY recipe_runs.id`)
		if err != nil {
			return err
		}
		compactRows(w, rows)
		return nil
	}
	for _, r := range strRows(db, `SELECT 'R-'||id, status, name, target, requester, created_at, COALESCE(replace(result, char(10), ' '),'-') FROM recipe_runs WHERE `+where+` ORDER BY recipe_runs.id`) {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return nil
}

// cmdRecipeCancel は tb recipe-cancel R-<n>: 待ち行列の queued を取り消す。人と、頼んだ agent 自身だけ。
func cmdRecipeCancel(args []string, w, stderr io.Writer) error {
	if len(args) != 1 || !reRid.MatchString(args[0]) {
		return usage("recipe-cancel R-<n>")
	}
	idS := reRid.FindStringSubmatch(args[0])[1]
	db, _, err := openWrite(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	var r apprecipe.CancelResult
	if err := callAppEnv(appEnv(db, stderr), by, "recipe.cancel", apprecipe.CancelReq{ID: args[0]}, &r); err != nil {
		return err
	}
	fmt.Fprintf(w, "R-%s cancelled\n", idS)
	return nil
}
