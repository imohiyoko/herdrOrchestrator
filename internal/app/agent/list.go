package agent

import (
	"context"
	"database/sql"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
)

// ListReq は agent.list の入力。Names(カンマ区切り)が空ならこのセッションの台帳の全員。
type ListReq struct {
	Names string `api:"names,optional"`
}

func (in ListReq) names() []string {
	if in.Names == "" {
		return nil
	}
	return strings.Split(in.Names, ",")
}

// AgentInfo は台帳の 1 体(と herdr で動いているか)。
type AgentInfo struct {
	Name   string `api:"name" json:"name"`
	State  string `api:"state" json:"state"`                      // 台帳の状態: running / stopped(台帳に無ければ空)
	Live   bool   `api:"live" json:"live"`                        // herdr の agent 一覧にいる
	Pane   string `api:"pane,optional" json:"pane,omitempty"`     // herdr の pane(動いていれば今の pane)
	Parent string `api:"parent,optional" json:"parent,omitempty"` // 台帳の spawned_by
	Role   string `api:"role,optional" json:"role,omitempty"`     // 台帳の役割(--role)
	Kind   string `api:"kind,optional" json:"kind,omitempty"`
}

// ListResult は agent.list の結果。Running と Max は動作中の数と上限 max_running_agents(上限なしは -1)。
type ListResult struct {
	Agents  []AgentInfo `api:"agents" json:"agents"`
	Running int         `api:"running" json:"running"`
	Max     int         `api:"max" json:"max"`
}

// List は台帳を herdr と突き合わせて読む(SyncLive のあと)。names に台帳に無い名前があれば State が空の行で返す。
func List(ctx context.Context, db *store.DB, names []string) (ListResult, error) {
	var res ListResult
	live, err := agents.SyncLiveContext(ctx, db.DB, db.Session)
	if err != nil {
		return res, &define.Error{Code: define.CodeUnavailable, Message: "herdr agent list に失敗しました", Reasons: []string{ReasonHerdrUnavailable}}
	}
	row := func(name string) (AgentInfo, bool) {
		a := AgentInfo{Name: name}
		var pane, parent, role, kind sql.NullString
		if db.QueryRow(`SELECT state, pane_id, spawned_by, role, kind FROM agents WHERE name=?`, name).Scan(&a.State, &pane, &parent, &role, &kind) != nil {
			return a, false
		}
		a.Pane, a.Parent, a.Role, a.Kind = pane.String, parent.String, role.String, kind.String
		return a, true
	}
	if len(names) == 0 {
		rows, err := db.Query(`SELECT name FROM agents ORDER BY name`)
		if err != nil {
			return res, define.Errorf(define.CodeInternal, "%s", err.Error())
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		rows.Close()
	}
	for _, n := range names {
		a, _ := row(n)
		if l, ok := agents.LiveOf(live, n); ok {
			a.Live, a.Pane = true, l.PaneID
		}
		res.Agents = append(res.Agents, a)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM agents WHERE state='running'`).Scan(&res.Running)
	res.Max = -1
	if max, lim := limits.Load(db.DB, db.Session).Max("max_running_agents"); lim {
		res.Max = max
	}
	return res, nil
}

func listOp(env *app.Env) app.Op {
	return app.Query(env, "agent.list",
		func(_ context.Context, _ *app.Env, _ string, v any) app.Decision {
			for _, n := range v.(ListReq).names() {
				if err := validName(n); err != nil {
					return app.Decision{Err: err}
				}
			}
			return app.Decision{}
		},
		func(ctx context.Context, in ListReq) (ListResult, error) {
			return withDB(env, func(db *store.DB) (ListResult, error) { return List(ctx, db, in.names()) })
		},
		define.Doc("台帳の agent と、herdr で動いているか(tb agent list の読み取り)"),
		define.Authz("cmd:agent:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/agents"),
	)
}
