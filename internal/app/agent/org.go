package agent

import (
	"context"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// OrgAgent は組織図の 1 体。State は台帳の状態(台帳にいない上司は "-")。
// Manager は上司(いなければ空、役割を確かめられず決まらなければ "?")。
type OrgAgent struct {
	Name    string `api:"name" json:"name"`
	State   string `api:"state" json:"state"`
	Manager string `api:"manager,optional" json:"manager,omitempty"`
}

// OrgResult は agent.org の結果。Set は設定 org があるか、Source はその出どころ(無ければ none)。
type OrgResult struct {
	Source string     `api:"source" json:"source"`
	Set    bool       `api:"set" json:"set"`
	Agents []OrgAgent `api:"agents" json:"agents"`
}

// Org は台帳の全員(と extra の名前)の上司を設定 org から引く(tb org と同じ)。
// 上司として書かれているが台帳にいない名前も、上司の上司までたどって足す。
func Org(db *store.DB, extra ...string) (OrgResult, error) {
	var res OrgResult
	idx := map[string]int{}
	add := func(n, state string) {
		if _, ok := idx[n]; ok || n == "" {
			return
		}
		idx[n] = len(res.Agents)
		res.Agents = append(res.Agents, OrgAgent{Name: n, State: state})
	}
	rows, err := db.Query(`SELECT name, COALESCE(state,'') FROM agents ORDER BY name`)
	if err != nil {
		return res, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			rows.Close()
			return res, define.Errorf(define.CodeInternal, "%s", err.Error())
		}
		add(n, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return res, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	for _, n := range extra {
		add(n, "-")
	}
	_, src, has := config.Lookup(db.DB, db.Session, "org")
	res.Set = has
	res.Source = "none"
	if !has {
		return res, nil
	}
	res.Source = src
	j := approval.NewJudge(db.DB, db.Session)
	for i := 0; i < len(res.Agents); i++ { // 足した上司もこのループで引く
		switch m, rc := j.ManagerOf(res.Agents[i].Name); rc {
		case 0:
			res.Agents[i].Manager = m
			add(m, "-")
		case 2:
			res.Agents[i].Manager = "?"
		}
	}
	return res, nil
}

// OrgReq は agent.org の入力(無し)。
type OrgReq struct{}

func orgOp(env *app.Env) app.Op {
	return app.Query(env, "agent.org",
		func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} },
		func(_ context.Context, _ OrgReq) (OrgResult, error) {
			return withDB(env, func(db *store.DB) (OrgResult, error) { return Org(db) })
		},
		define.Doc("組織図: 台帳の agent と、設定 org で決まる上司(tb org の読み取り)"),
		define.Authz("cmd:agent:org"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/org"),
	)
}
