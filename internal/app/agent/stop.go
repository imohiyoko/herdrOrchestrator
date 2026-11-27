package agent

import (
	"context"
	"fmt"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// StopReq は agent.stop の入力。
type StopReq struct {
	Name  string `api:"name,path"`
	Force bool   `api:"force,optional"` // 判定(killable・未完了タスク)を越えて止める。人だけ
}

// StopResult は agent.stop の結果。
type StopResult struct {
	Name string `api:"name" json:"name"`
	// Out は tb の文言(「<名前> stopped (session …)」)。
	Out     string       `api:"out" json:"out"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *StopResult) SetPending(p *app.Pending) { r.Pending = p }

func stopArgs(in StopReq) string {
	f := 0
	if in.Force {
		f = 1
	}
	return fmt.Sprintf(`{"force":%d}`, f)
}

func stopOp(env *app.Env) app.Op {
	return app.Command(env, "agent.stop",
		opGate("stop", func(in StopReq) string { return in.Name }, stopArgs, func(who string, in StopReq) error {
			if err := validName(in.Name); err != nil {
				return err
			}
			if in.Force && who != "human" {
				return define.Errorf(define.CodeFailedPrecondition, "--force は人が自分の端末で使います(agent '%s' の pane からは使えません)", who)
			}
			return nil
		}),
		func(ctx context.Context, in StopReq) (StopResult, error) {
			who := by(ctx)
			return withDB(env, func(db *store.DB) (StopResult, error) {
				r := Stop(db, tickets.LoadStatuses(db.DB, db.Session), in.Name, in.Force, who)
				agents.RecordOp(db.DB, db.Session, "stop", in.Name, stopArgs(in), who, r.Code, r.Out)
				if err := r.Err(); err != nil {
					return StopResult{}, err
				}
				return StopResult{Name: in.Name, Out: r.Out}, nil
			})
		},
		define.Doc("agent を止める(tb agent-stop)"),
		define.Authz("cmd:agent:stop", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/{name}/stop"),
	)
}

// Stop は do_agent_stop: 門を通ったあとの停止(承認後の実行・recipe からも呼ぶ)。by は依頼した主体。
func Stop(db *store.DB, st tickets.StatusSet, name string, force bool, by string) Result {
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return Result{Code: 11, Out: "herdr agent list に失敗しました"}
	}
	a, ok := agents.LiveOf(live, name)
	if !ok {
		if msg, other := agents.OtherSession(db.DB, db.Session, name); other {
			return Result{Code: 1, Out: msg}
		}
		return Result{Code: 1, Out: name + " は動いていません"}
	}
	if by == name {
		return Result{Code: 1, Out: "自分自身は止められません"}
	}
	pane, sess := a.PaneID, a.AgentSession.Value
	if pane == "" {
		return Result{Code: 1, Out: name + " の pane が分かりません"}
	}
	if k, ok := agents.NewRoles(db.DB, db.Session).ProtectReason(name); ok { // 保護は --force でも越えない
		return Result{Code: 1, Out: name + " は保護されています(" + k + ")。止めるなら、人が tb config edit protect で外してから"}
	}
	if !force {
		k := agents.Killable(db.DB, db.Session, name)
		var open string
		_ = db.QueryRow(`SELECT COALESCE(group_concat(id, ' '),'') FROM tasks WHERE owner_agent=? AND status IN (`+st.SQLList("active", "review", "blocked")+`)`, name).Scan(&open)
		if k != "yes" || open != "" {
			extra := ""
			if open != "" {
				extra = "、未完了タスク " + open
			}
			return Result{Code: 1, Out: name + " は止めてよいと判定されていません(killable=" + k + extra + ")。判定役に付けてもらうか(tb agent-flag)、人が --force で止めます"}
		}
	}
	if !agents.ClosePane(db.DB, db.Session, name, pane, sess) {
		return Result{Code: 11, Out: "pane " + pane + " を閉じられません"}
	}
	_ = db.QueryRow(`SELECT COALESCE(session_id,'') FROM agents WHERE name=?`, name).Scan(&sess)
	out := name + " stopped"
	if sess != "" {
		out += " (session " + sess + "。tb agent-revive " + name + " で同じ会話から再開できます)"
	}
	return Result{Code: 0, Out: out}
}
