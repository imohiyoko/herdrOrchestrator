package agent

import (
	"context"
	"database/sql"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// 親(agent_parents)。子 1 体に親が何体でも付く。spawned_by は「立てた親」として残し(上限・深さ・記録)、今の親はこの表で持つ。
// 親を足す・外すのは人か、policy の agent:parent:<子> を allow された agent だけ(承認待ちには積まない)。

// ParentReq は agent.parent.add / agent.parent.rm の入力。
type ParentReq struct {
	Name   string `api:"name,path"` // 子
	Parent string `api:"parent"`
}

// ParentResult は agent.parent.add / rm の結果。Parents は変えたあとの親(名前の順)。
type ParentResult struct {
	Name    string   `api:"name" json:"name"`
	Parents []string `api:"parents" json:"parents"`
	// Changed は表を変えた(足す: もう親だった、外す: 親ではなかった、なら false)。
	Changed bool `api:"changed" json:"changed"`
}

// setParents は作成のときの親(spawned_by)を、子の親の表の最初の 1 行にする(同じ名前の古い行は消す)。
func setParents(db *store.DB, child, parent, by string) {
	_, _ = db.Exec(`DELETE FROM main.agent_parents WHERE herdr_session=? AND child=?`, db.Session, child)
	if parent != "" {
		_, _ = db.Exec(`INSERT OR IGNORE INTO main.agent_parents (herdr_session, child, parent, added_by, added_at) VALUES (?,?,?,?,?)`,
			db.Session, child, parent, by, nowString())
	}
}

func nowString() string { return (*app.Env)(nil).NowString() }

// Parents は子の親(名前の順。表が無い古い DB なら空)。
func Parents(db *sql.DB, child string) []string {
	rows, err := db.Query(`SELECT parent FROM agent_parents WHERE child=? ORDER BY parent`, child)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// Orphans は動いている子のうち、親が 1 体以上いて全員が動いていないもの(名前の順)。tb agent shed の orphan。
func Orphans(db *sql.DB) []string {
	rows, err := db.Query(`SELECT a.name FROM agents a WHERE a.state='running'
      AND EXISTS (SELECT 1 FROM agent_parents ap WHERE ap.child=a.name)
      AND NOT EXISTS (SELECT 1 FROM agent_parents ap JOIN agents p ON p.name=ap.parent WHERE ap.child=a.name AND p.state='running')
      ORDER BY a.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// ancestor は a が b の祖先(b から親を辿って a に着く)か。
func ancestor(db *sql.DB, a, b string) bool {
	seen := map[string]bool{}
	q := []string{b}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		if seen[n] {
			continue
		}
		seen[n] = true
		for _, p := range Parents(db, n) {
			if p == a {
				return true
			}
			q = append(q, p)
		}
	}
	return false
}

func parentGate(op string) app.Gate {
	return func(_ context.Context, env *app.Env, who string, v any) app.Decision {
		in := v.(ParentReq)
		for _, n := range []string{in.Name, in.Parent} {
			if err := validName(n); err != nil {
				return app.Decision{Err: err}
			}
		}
		if in.Name == in.Parent {
			return app.Decision{Err: usageErr("%s: 自分を親にはできません: %s", op, in.Name)}
		}
		if who == "human" {
			return app.Decision{}
		}
		db, done, err := env.DB()
		if err != nil {
			return app.Decision{Err: define.Errorf(define.CodeInternal, "%s", err.Error())}
		}
		defer done()
		if val, rule := approval.NewJudge(db.DB, db.Session).Of("agent:parent:"+in.Name, who); val != "allow" {
			return app.Decision{Err: &define.Error{Code: define.CodePermissionDenied, Reasons: []string{ReasonOpDenied},
				Message: op + ": " + in.Name + " の親を変えられるのは人か、policy で agent:parent を allow された agent だけです(" + val + "。" + rule + ")"}}
		}
		return app.Decision{}
	}
}

func parentOp(env *app.Env, add bool) app.Op {
	name, verb, doc := "agent.parent.rm", "rm", "agent の親を外す(tb agent parent rm)"
	if add {
		name, verb, doc = "agent.parent.add", "add", "agent に親を足す(tb agent parent add)"
	}
	return app.Command(env, name, parentGate("agent parent "+verb),
		func(ctx context.Context, in ParentReq) (ParentResult, error) {
			who := by(ctx)
			return withDB(env, func(db *store.DB) (ParentResult, error) {
				r := ParentResult{Name: in.Name}
				var st string
				if db.QueryRow(`SELECT state FROM agents WHERE name=?`, in.Name).Scan(&st) != nil {
					return r, define.Errorf(define.CodeNotFound, "agent が台帳にいません: %s", in.Name)
				}
				var res sql.Result
				var err error
				if add {
					if db.QueryRow(`SELECT state FROM agents WHERE name=?`, in.Parent).Scan(&st) != nil {
						return r, define.Errorf(define.CodeNotFound, "親が台帳にいません: %s", in.Parent)
					}
					if ancestor(db.DB, in.Name, in.Parent) {
						return r, define.Errorf(define.CodeFailedPrecondition, "%s は %s の祖先です(親子が輪になります)", in.Name, in.Parent)
					}
					res, err = db.Exec(`INSERT OR IGNORE INTO main.agent_parents (herdr_session, child, parent, added_by, added_at) VALUES (?,?,?,?,?)`,
						db.Session, in.Name, in.Parent, who, env.NowString())
				} else {
					res, err = db.Exec(`DELETE FROM main.agent_parents WHERE herdr_session=? AND child=? AND parent=?`, db.Session, in.Name, in.Parent)
				}
				if err != nil {
					return r, define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				n, _ := res.RowsAffected()
				r.Changed, r.Parents = n == 1, Parents(db.DB, in.Name)
				if r.Parents == nil {
					r.Parents = []string{}
				}
				return r, nil
			})
		},
		define.Doc(doc),
		define.Authz("cmd:agent:parent", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/{name}/parents/"+verb),
	)
}
