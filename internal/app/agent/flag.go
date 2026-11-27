package agent

import (
	"context"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// 判定フラグ(止めてよいか)と、作成の凍結(tb agent-flag / spawn-freeze / spawn-unfreeze)。

// FlagReq は agent.flag の入力。Killable は yes / no / unknown(yes / no には Reason が要る)。
type FlagReq struct {
	Name     string `api:"name,path"`
	Killable string `api:"killable"`
	Reason   string `api:"reason,optional"`
}

// FlagResult は付けた判定。
type FlagResult struct {
	Name     string `api:"name" json:"name"`
	Killable string `api:"killable" json:"killable"`
}

// FreezeReq は agent.freeze / agent.unfreeze の入力(無し)。
type FreezeReq struct{}

// FreezeResult は凍結の今の値。
type FreezeResult struct {
	Frozen  bool         `api:"frozen" json:"frozen"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *FreezeResult) SetPending(p *app.Pending) { r.Pending = p }

// ValidFlag は agent-flag の入力の確かめ(tb は開く前に呼ぶ)。
func ValidFlag(kill, why string) error {
	if kill != "yes" && kill != "no" && kill != "unknown" {
		return define.Errorf(define.CodeInvalidArgument, "agent-flag: --killable は yes|no|unknown")
	}
	if kill != "unknown" && strings.Trim(why, " \t\n\v\f\r") == "" {
		return define.Errorf(define.CodeInvalidArgument, "agent-flag: yes / no には --reason が必要です")
	}
	return nil
}

func flagOps(env *app.Env) []app.Op {
	flag := app.Command(env, "agent.flag", app.Entry("cmd:agent:flag", "tb agent-flag"),
		func(ctx context.Context, in FlagReq) (FlagResult, error) {
			if err := validName(in.Name); err != nil {
				return FlagResult{}, err
			}
			if err := ValidFlag(in.Killable, in.Reason); err != nil {
				return FlagResult{}, err
			}
			who := by(ctx)
			return withDB(env, func(db *store.DB) (FlagResult, error) { return setFlag(env, db, who, in) })
		},
		define.Doc("agent に止めてよいかの判定を付ける(tb agent-flag。人と、policy で agent:flag を許された agent。自分には付けられない)"),
		define.Authz("cmd:agent:flag", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/{name}/flag"),
	)
	freeze := func(verb string, val bool) app.Op {
		return app.Command(env, "agent."+verb, app.Cmd("cmd:agent:spawn-"+verb, "tb spawn-"+verb),
			func(ctx context.Context, _ FreezeReq) (FreezeResult, error) {
				who := by(ctx)
				return withDB(env, func(db *store.DB) (FreezeResult, error) {
					v := "0"
					if val {
						v = "1"
					}
					if who == "" || strings.HasPrefix(who, "system:") {
						who = "human"
					}
					if _, err := db.Exec(`INSERT INTO main.tb_flags (herdr_session, key, value, set_by, set_at) VALUES (?, 'spawn_frozen', ?, ?, ?)
    ON CONFLICT(herdr_session, key) DO UPDATE SET value=excluded.value, set_by=excluded.set_by, set_at=excluded.set_at`, db.Session, v, who, env.NowString()); err != nil {
						return FreezeResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
					}
					return FreezeResult{Frozen: val}, nil
				})
			},
			define.Doc(map[string]string{"freeze": "agent の作成を凍結する(以後の agent-spawn はすべて人の承認待ち。tb spawn-freeze。既定は人だけ)", "unfreeze": "agent の作成の凍結を解く(tb spawn-unfreeze。既定は人だけ)"}[verb]),
			define.Authz("cmd:agent:spawn-"+verb),
			define.Expose(define.Internal, define.REST),
			define.HTTP("POST /v1/agents/spawn-"+verb),
		)
	}
	return []app.Op{flag, freeze("freeze", true), freeze("unfreeze", false)}
}

func setFlag(env *app.Env, db *store.DB, who string, in FlagReq) (FlagResult, error) {
	name := in.Name
	if who == "" {
		return FlagResult{}, define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
	}
	if who != "human" && !strings.HasPrefix(who, "system:") {
		if _, ok := approval.NewJudge(db.DB, db.Session).GrantOf(who, "agent:flag", name); !ok {
			return FlagResult{}, define.Errorf(define.CodePermissionDenied, "判定フラグを付けられるのは人と、policy で agent:flag を許された agent だけです(%s)", who)
		}
		if who == name {
			return FlagResult{}, define.Errorf(define.CodeFailedPrecondition, "自分自身の判定は付けられません")
		}
	} else {
		who = "human"
	}
	if err := agents.Sync(db.DB, db.Session); err != nil {
		return FlagResult{}, define.Errorf(define.CodeUnavailable, "herdr agent list に失敗しました")
	}
	var state string
	_ = db.QueryRow(`SELECT state FROM agents WHERE name=?`, name).Scan(&state)
	if state != "running" {
		return FlagResult{}, define.Errorf(define.CodeFailedPrecondition, "%s は動いていません", name)
	}
	ts := env.NowString()
	if _, err := db.Exec(`UPDATE main.agents SET killable=?, killable_reason=NULLIF(?,''), killable_by=?, killable_at=?, killable_session=session_id, updated_at=? WHERE herdr_session=? AND name=?`,
		in.Killable, util.Oneline(in.Reason, 300), who, ts, ts, db.Session, name); err != nil {
		return FlagResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	return FlagResult{Name: name, Killable: in.Killable}, nil
}
