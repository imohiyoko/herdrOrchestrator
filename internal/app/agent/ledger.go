package agent

// agent の台帳・絞る候補・操作の記録の読むだけの操作(tb agents / agent-shed / agent-ops)。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// LedgerReq は agent.ledger の入力。All なら止まって 7 日を過ぎたものも。
type LedgerReq struct {
	All bool `api:"all,optional"`
}

// LedgerResult は台帳の行(1 行 1 つの JSON。実効の止めてよいかの判定・herdr の状態・役割のファイルつき)。
// LiveErr は herdr の一覧を読めなかった(台帳の内容だけ)。
type LedgerResult struct {
	Rows    string `api:"rows" json:"rows"`
	LiveErr bool   `api:"live_err,optional" json:"live_err,omitempty"`
}

// ShedReq は agent.shed の入力。Count が 0 より大きければその数まで。
type ShedReq struct {
	Count int `api:"count,optional"`
}

// ShedRow は絞る候補の 1 行。
type ShedRow struct {
	Group    string `api:"group" json:"group"`
	Name     string `api:"name" json:"name"`
	Killable string `api:"killable" json:"killable"`
	Command  string `api:"command" json:"command"`
}

// ShedResult は絞る候補(何も止めない)。LiveErr は herdr の一覧を読めなかった(台帳の内容だけで選んだ)。
type ShedResult struct {
	Rows    []ShedRow `api:"rows" json:"rows"`
	LiveErr bool      `api:"live_err,optional" json:"live_err,omitempty"`
}

// OpsReq は agent.ops の入力。Status で絞る(空なら全部)。新しい順に 100 件。
type OpsReq struct {
	Status string `api:"status,optional"`
	JSON   bool   `api:"json,optional"`
}

// OpsResult は tb agent-ops の出力。
type OpsResult struct {
	Text string `api:"text" json:"text"`
}

func ledgerOps(env *app.Env) []app.Op {
	pass := func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} }
	ledger := app.Query(env, "agent.ledger", pass,
		func(ctx context.Context, in LedgerReq) (LedgerResult, error) {
			return withDB(env, func(db *store.DB) (LedgerResult, error) { return ledgerRows(ctx, db, in) })
		},
		define.Doc("台帳の agent の行(tb agents。開いているタスク・質問の数、最後の活動、herdr の状態、役割のファイルつき)"),
		define.Authz("cmd:agent:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/agents/ledger"),
	)
	shed := app.Query(env, "agent.shed", pass,
		func(ctx context.Context, in ShedReq) (ShedResult, error) {
			if in.Count < 0 {
				return ShedResult{}, define.Errorf(define.CodeInvalidArgument, "agent-shed: --count は 0 以上の整数")
			}
			return withDB(env, func(db *store.DB) (ShedResult, error) { return shedRows(ctx, db, in) })
		},
		define.Doc("絞る候補の一覧(tb agent-shed。読み取り専用: 何も止めない)"),
		define.Authz("cmd:agent:shed"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/agents/shed"),
	)
	ops := app.Query(env, "agent.ops", pass,
		func(_ context.Context, in OpsReq) (OpsResult, error) {
			return withDB(env, func(db *store.DB) (OpsResult, error) {
				s, err := opsText(db, in)
				if err != nil {
					return OpsResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				return OpsResult{Text: s}, nil
			})
		},
		define.Doc("agent の操作の記録 L-<n>(tb agent-ops。新しい順に 100 件)"),
		define.Authz("cmd:agent:ops"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/agent-ops"),
	)
	graph := app.Query(env, "agent.graph", pass,
		func(ctx context.Context, in GraphReq) (GraphResult, error) {
			return withDB(env, func(db *store.DB) (GraphResult, error) { return graphOf(ctx, db, in) })
		},
		define.Doc("agent の関係図(tb agents --graph。点と 主体 -動詞→ 対象 のエッジ、直近 7 日)"),
		define.Authz("cmd:agent:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/agents/graph"),
	)
	return []app.Op{ledger, graph, shed, ops, roleLogOp(env)}
}

func ledgerRows(ctx context.Context, db *store.DB, in LedgerReq) (LedgerResult, error) {
	rows, liveErr, err := ledgerObjs(ctx, db, in.All)
	if err != nil {
		return LedgerResult{}, err
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(jv.Compact(r) + "\n")
	}
	return LedgerResult{Rows: b.String(), LiveErr: liveErr}, nil
}

func scalar(v any) string {
	if v == nil {
		return ""
	}
	return jv.Scalar(v)
}

// ledgerObjs は台帳の行に実効の判定フラグと herdr の状態・役割のファイルを足したもの。
func ledgerObjs(ctx context.Context, db *store.DB, all bool) ([]jv.Obj, bool, error) {
	st := tickets.LoadStatuses(db.DB, db.Session)
	live, lerr := agents.SyncLiveContext(ctx, db.DB, db.Session)
	where := "1=1"
	if !all {
		where = "(a.state='running' OR a.stopped_at > strftime('%Y-%m-%dT%H:%M:%SZ','now','-7 days'))"
	}
	rows, err := rowfmt.Objs(db.DB, `SELECT a.name, a.state, a.pane_id, a.session_id, a.cwd, a.model, a.role, a.killable_reason, a.killable_by, a.killable_at,
      a.created_by, a.created_at, a.stopped_at, a.spawned_by, a.spawn_task,
      (SELECT COUNT(*) FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (`+st.SQLList("active", "review", "blocked")+`)) AS open_tasks,
      (SELECT COUNT(*) FROM comments c LEFT JOIN tasks t ON t.id=c.task_id WHERE c.from_name=a.name AND c.kind IN ('question','blocked')
         AND c.resolved_at IS NULL AND COALESCE(t.status,'') NOT IN (`+st.SQLList("done", "cancelled")+`)) AS open_questions,
      aa.last_prompt_at, aa.last_stop_at,
      CASE WHEN aa.last_stop_at IS NOT NULL AND (aa.last_prompt_at IS NULL OR aa.last_stop_at >= aa.last_prompt_at)
           THEN CAST((julianday('now') - julianday(aa.last_stop_at)) * 1440 AS INTEGER) END AS idle_min
    FROM agents a LEFT JOIN main.agent_activity aa ON aa.session_id=a.session_id
    WHERE `+where+` ORDER BY a.state, a.name`)
	if err != nil {
		return nil, lerr != nil, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	roles := agents.NewRoles(db.DB, db.Session)
	for i, r := range rows {
		name := scalar(r.Get("name"))
		a, _ := agents.LiveOf(live, name)
		var status, rfile, rname any
		if s := LiveStatus(a); s != "" {
			status = s
		}
		if fs := roles.FilesForLoose(name); len(fs) > 0 {
			var lab []string
			for _, f := range fs {
				lab = append(lab, f+".md")
			}
			rfile, rname = strings.Join(lab, ","), strings.Join(fs, ",")
		}
		rows[i] = r.Set("killable", agents.Killable(db.DB, db.Session, name)).Set("agent_status", status).Set("role_file", rfile).Set("role_name", rname)
	}
	return rows, lerr != nil, nil
}

// GraphReq は agent.graph の入力(点は agent.ledger と同じ範囲)。
type GraphReq struct {
	All bool `api:"all,optional"`
}

// GraphNode は関係図の点。Status・Role は無ければ空。State は台帳に無い名前なら "-"。
type GraphNode struct {
	Name   string `api:"name" json:"name"`
	State  string `api:"state" json:"state"`
	Status string `api:"status,optional" json:"status,omitempty"`
	Role   string `api:"role,optional" json:"role,omitempty"`
}

// GraphEdge は 主体 -動詞→ 対象(同じ動詞・端・状態はまとめて N 回、最後は LastAt)。
type GraphEdge struct {
	Verb   string  `api:"verb" json:"verb"`
	From   string  `api:"from" json:"from"`
	To     string  `api:"to" json:"to"`
	Status string  `api:"status" json:"status"`
	N      float64 `api:"n" json:"n"`
	LastAt string  `api:"last_at" json:"last_at"`
}

// GraphResult は agent.graph の結果。関係(taskboard/sql/agent_edges.sql)を読めなければエラー。
type GraphResult struct {
	Nodes   []GraphNode `api:"nodes" json:"nodes"`
	Edges   []GraphEdge `api:"edges" json:"edges"`
	LiveErr bool        `api:"live_err,optional" json:"live_err,omitempty"`
}

func graphOf(ctx context.Context, db *store.DB, in GraphReq) (GraphResult, error) {
	rows, liveErr, err := ledgerObjs(ctx, db, in.All)
	if err != nil {
		return GraphResult{}, err
	}
	res := GraphResult{LiveErr: liveErr}
	edges, err := agentEdges(db)
	if err != nil {
		// tb は前と同じ文言で落ちる(原因の err は出さない: 前も出していない)
		return GraphResult{}, define.Errorf(define.CodeFailedPrecondition, "関係を読めません(DB)")
	}
	have := map[string]bool{}
	for _, r := range rows {
		n := scalar(r.Get("name"))
		have[n] = true
		res.Nodes = append(res.Nodes, GraphNode{n, scalar(r.Get("state")), scalar(r.Get("agent_status")), scalar(r.Get("role_name"))})
	}
	// 関係の端に居るが一覧に無い名前(止まって 7 日を過ぎた agent・cli など)も点として足す
	var extra []string
	for _, e := range edges {
		for _, n := range []string{e.From, e.To} {
			if !have[n] && !slices.Contains(extra, n) {
				extra = append(extra, n)
			}
		}
	}
	sort.Strings(extra)
	roles := agents.NewRoles(db.DB, db.Session)
	for _, n := range extra {
		role, _ := roles.NameOf(n)
		res.Nodes = append(res.Nodes, GraphNode{Name: n, State: "-", Role: role})
	}
	res.Edges = edges
	return res, nil
}

// agentEdges は taskboard/sql/agent_edges.sql の行を 主体 -動詞→ 対象 のエッジにする。
func agentEdges(db *store.DB) ([]GraphEdge, error) {
	st := tickets.LoadStatuses(db.DB, db.Session)
	b, err := os.ReadFile(filepath.Join(os.Getenv("TB_ROOT"), "sql", "agent_edges.sql"))
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "--") {
			lines = append(lines, l)
		}
	}
	q := strings.ReplaceAll(strings.TrimRight(strings.Join(lines, "\n"), "\n"), "__HELD_STATUSES__", st.SQLList("active", "review", "blocked"))
	rows, err := rowfmt.Objs(db.DB, q)
	if err != nil {
		return nil, err
	}
	cond := map[string]string{}
	var es []GraphEdge
	for _, r := range rows {
		src, dst, proj := r.Get("src"), r.Get("dst"), r.Get("project")
		fill := func(v any) string {
			if v != nil {
				return scalar(v)
			}
			if proj == nil || proj == false {
				return ""
			}
			p := scalar(proj)
			if _, ok := cond[p]; !ok {
				cond[p] = message.TargetFor(db.DB, db.Session, p)
			}
			return cond[p]
		}
		s, d := fill(src), fill(dst)
		if s == "" || d == "" || s == d {
			continue
		}
		n, _ := jv.Num(r.Get("n"))
		es = append(es, GraphEdge{scalar(r.Get("verb")), s, d, scalar(r.Get("status")), n, scalar(r.Get("last_at"))})
	}
	key := func(e GraphEdge) []string { return []string{e.Verb, e.From, e.To, e.Status} }
	sort.SliceStable(es, func(i, j int) bool { return slices.Compare(key(es[i]), key(es[j])) < 0 })
	var out []GraphEdge
	for _, e := range es {
		if k := len(out) - 1; k >= 0 && slices.Equal(key(out[k]), key(e)) {
			out[k].N += e.N
			if e.LastAt > out[k].LastAt {
				out[k].LastAt = e.LastAt
			}
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// names は 1 列目の値の一覧(読み終えてから返す。1 本の接続のため)。
func names(db *store.DB, q string, a ...any) []string {
	var out []string
	_ = store.Each(db.DB, q, a, func(_ []string, v []any) error {
		out = append(out, fmt.Sprint(v[0]))
		return nil
	})
	return out
}

func isDigits(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

func shedRows(ctx context.Context, db *store.DB, in ShedReq) (ShedResult, error) {
	st := tickets.LoadStatuses(db.DB, db.Session)
	live, lerr := agents.SyncLiveContext(ctx, db.DB, db.Session)
	held, closed := st.SQLList("active", "review", "blocked"), st.SQLList("done", "cancelled")
	conductors := message.ConductorNames(db.DB, db.Session)
	var out []ShedRow
	seen := map[string]bool{}
	add := func(group, name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		k := agents.Killable(db.DB, db.Session, name)
		if k == "protected" {
			return
		}
		var open int
		_ = db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE owner_agent=? AND status IN (`+held+`)`, name).Scan(&open)
		cmd := "tb agent-stop " + name
		if k != "yes" || open != 0 {
			cmd += " --force"
		}
		out = append(out, ShedRow{group, name, k, cmd})
	}
	// 1. 完了報告済みで待機中
	for _, name := range names(db, `SELECT a.name FROM agents a WHERE a.state='running'
      AND EXISTS (SELECT 1 FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (`+closed+`))
      AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.owner_agent=a.name AND t.status IN (`+held+`)) ORDER BY a.name`) {
		if name == "" || slices.Contains(conductors, name) {
			continue
		}
		a, _ := agents.LiveOf(live, name)
		if s := LiveStatus(a); s == "idle" || s == "done" {
			add("done-idle", name)
		}
	}
	// 2. 止めてよいと判定されている(stale でない)
	for _, name := range names(db, `SELECT name FROM agents WHERE state='running' AND killable='yes' ORDER BY name`) {
		if name == "" || slices.Contains(conductors, name) || agents.Killable(db.DB, db.Session, name) != "yes" {
			continue
		}
		var one int
		if db.QueryRow(`SELECT 1 FROM tasks WHERE owner_agent=? AND status IN (`+held+`) LIMIT 1`, name).Scan(&one) == nil {
			continue
		}
		add("killable", name)
	}
	// 3. 解析役(解析中のものは除く)と、親が終わった子
	for _, name := range names(db, `SELECT name FROM agents WHERE state='running' AND name LIKE 'analyst-%' ORDER BY name`) {
		if name == "" {
			continue
		}
		if aid := strings.TrimPrefix(name, "analyst-A"); aid != name && isDigits(aid) {
			var s string
			_ = db.QueryRow(`SELECT status FROM alerts WHERE id=?`, aid).Scan(&s)
			if s == "analyzing" {
				continue
			}
		}
		add("analyst", name)
	}
	// 親(agent_parents)が 1 体以上いて全員が動いていない子と、立てた親(spawned_by)がいて任されたタスクが終わった子
	orphans := Orphans(db.DB)
	orphans = append(orphans, names(db, `SELECT a.name FROM agents a WHERE a.state='running' AND a.spawned_by IS NOT NULL
      AND EXISTS (SELECT 1 FROM tasks t WHERE t.id=a.spawn_task AND t.status IN (`+closed+`))`)...)
	slices.Sort(orphans)
	for _, name := range orphans {
		if name != "" {
			add("orphan", name)
		}
	}
	if in.Count > 0 && len(out) > in.Count {
		out = out[:in.Count]
	}
	return ShedResult{Rows: out, LiveErr: lerr != nil}, nil
}

func opsText(db *store.DB, in OpsReq) (string, error) {
	where, qa := "1=1", []any{}
	if in.Status != "" {
		where, qa = "status=?", []any{in.Status}
	}
	var b strings.Builder
	if in.JSON {
		rows, err := rowfmt.Objs(db.DB, `SELECT * FROM agent_ops WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa...)
		if err != nil {
			return "", err
		}
		for _, o := range rows {
			b.WriteString(jv.Compact(o.Set("id", "L-"+tickets.Text(o.Get("id")))) + "\n")
		}
		return b.String(), nil
	}
	err := store.Each(db.DB, `SELECT 'L-'||id, op, target, requested_by, status, created_at, COALESCE(result,'') FROM agent_ops WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa,
		func(_ []string, vals []any) error {
			s := make([]string, len(vals))
			for i, v := range vals {
				if v == nil {
					s[i] = "-"
				} else {
					s[i] = tickets.Text(v)
				}
			}
			b.WriteString(strings.Join(s, "\t") + "\n")
			return nil
		})
	return b.String(), err
}

// RoleLogReq は agent.role.log の入力。
type RoleLogReq struct {
	JSON bool `api:"json,optional"`
}

// RoleLogResult は tb role-log の出力。
type RoleLogResult struct {
	Text string `api:"text" json:"text"`
}

func roleLogOp(env *app.Env) app.Op {
	return app.Query(env, "agent.role.log", func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} },
		func(_ context.Context, in RoleLogReq) (RoleLogResult, error) {
			return withDB(env, func(db *store.DB) (RoleLogResult, error) {
				var b strings.Builder
				var err error
				if in.JSON {
					var rows []jv.Obj
					rows, err = rowfmt.Objs(db.DB, `SELECT * FROM role_changes ORDER BY id DESC LIMIT 100`)
					for _, o := range rows {
						b.WriteString(jv.Compact(o) + "\n")
					}
				} else {
					err = store.Each(db.DB, `SELECT created_at, agent, COALESCE(old_role,'-')||' -> '||new_role, changed_by, COALESCE(reason,'') FROM role_changes ORDER BY id DESC LIMIT 100`, nil,
						func(_ []string, vals []any) error {
							s := make([]string, len(vals))
							for i, v := range vals {
								s[i] = tickets.Text(v)
							}
							b.WriteString(strings.Join(s, "\t") + "\n")
							return nil
						})
				}
				if err != nil {
					return RoleLogResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				return RoleLogResult{Text: b.String()}, nil
			})
		},
		define.Doc("役割の変更の記録(tb role-log。新しい順に 100 件)"),
		define.Authz("cmd:agent:role-log"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/role-changes"),
	)
}
