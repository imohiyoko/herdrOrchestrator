package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ReviveReq は agent.revive の入力。
type ReviveReq struct {
	Name   string `api:"name,path"`
	Fresh  bool   `api:"fresh,optional"`  // 前の会話に戻らず新しい会話にする
	Model  string `api:"model,optional"`  // 空なら台帳(と役割の既定)
	Prompt string `api:"prompt,optional"` // 最初の指示(4000 字まで)
	// Pane は置き場所(空なら専用の workspace)。人だけが指定できる(agent セットが自分の workspace に戻す)
	Pane *PanePlacement `api:"pane,optional"`
}

// LifeResult は agent.revive / agent.restart の結果。
type LifeResult struct {
	Name string `api:"name" json:"name"`
	// Out は tb の文言(「<名前> revived (…)」など)。途中の警告は Env.Warn に書く。
	Out     string       `api:"out" json:"out"`
	Pane    string       `api:"pane,optional" json:"pane,omitempty"` // 起動した pane(revive のとき)
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *LifeResult) SetPending(p *app.Pending) { r.Pending = p }

// jsonArgs は agent_ops.args の JSON(キーの順を保つ。HTML の文字を逃がさない。tb の jsonC と同じ)。
func jsonArgs(kv ...any) string {
	enc := func(v any) []byte {
		var x bytes.Buffer
		e := json.NewEncoder(&x)
		e.SetEscapeHTML(false)
		_ = e.Encode(v)
		return bytes.TrimRight(x.Bytes(), "\n")
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(enc(kv[i]))
		b.WriteByte(':')
		b.Write(enc(kv[i+1]))
	}
	b.WriteByte('}')
	return b.String()
}

func reviveArgs(in ReviveReq) string {
	f := 0
	if in.Fresh {
		f = 1
	}
	kv := []any{"fresh", f}
	if in.Model != "" {
		kv = append(kv, "model", in.Model)
	}
	if in.Prompt != "" {
		kv = append(kv, "prompt", in.Prompt)
	}
	return jsonArgs(kv...)
}

// revivePre は承認待ちに積む前の確かめ: 入力、台帳の kind でできるか(12)、動作中の上限(4)。
func revivePre(env *app.Env, who string, in ReviveReq) error {
	if err := validName(in.Name); err != nil {
		return err
	}
	if in.Model != "" && !ModelRE.MatchString(in.Model) {
		return define.Errorf(define.CodeInvalidArgument, "agent-revive: --model に使えない文字があります: %s", in.Model)
	}
	if utf8.RuneCountInString(in.Prompt) > 4000 {
		return define.Errorf(define.CodeInvalidArgument, "agent-revive: --prompt は 4000 字まで")
	}
	if err := paneErr("agent-revive", who, in.Pane); err != nil {
		return err
	}
	_, err := withDB(env, func(db *store.DB) (struct{}, error) {
		if row := RegRowOf(db, in.Name); row.Ok {
			m, sess := in.Model, ""
			if m == "" {
				m = row.Model
			}
			if !in.Fresh {
				sess = row.Sess
			}
			if r := CheckLaunch(row.Kind, m, sess); r != nil {
				return struct{}{}, r.Err()
			}
		}
		var b bytes.Buffer
		if RefuseRunning(db, 1, &b) {
			return struct{}{}, limitErr(&b)
		}
		return struct{}{}, nil
	})
	return err
}

func reviveOp(env *app.Env) app.Op {
	return app.Command(env, "agent.revive",
		opGate("revive", func(in ReviveReq) string { return in.Name }, reviveArgs, func(who string, in ReviveReq) error { return revivePre(env, who, in) }),
		func(ctx context.Context, in ReviveReq) (LifeResult, error) {
			who := by(ctx)
			return withDB(env, func(db *store.DB) (LifeResult, error) {
				r := ReviveAt(ctx, db, in.Name, in.Fresh, in.Model, in.Prompt, who, in.Pane.place(), env.Warner())
				agents.RecordOp(db.DB, db.Session, "revive", in.Name, reviveArgs(in), who, r.Code, r.Out)
				if err := r.Err(); err != nil {
					return LifeResult{}, err
				}
				return LifeResult{Name: in.Name, Out: r.Out, Pane: r.Pane}, nil
			})
		},
		define.Doc("止まっている agent を戻す(tb agent-revive)"),
		define.Authz("cmd:agent:revive", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/{name}/revive"),
	)
}

// RestartReq は agent.restart の入力。
type RestartReq struct {
	Name   string `api:"name,path"`
	Prompt string `api:"prompt,optional"` // 再開メモ(未完了のタスクがあるときは必須。4000 字まで)
}

func restartArgs(in RestartReq) string {
	kv := []any{"restart", 1}
	if in.Prompt != "" {
		kv = append(kv, "prompt", in.Prompt)
	}
	return jsonArgs(kv...)
}

// restartGate は再起動の門: 承認待ちにはしない。agent からは、自分・指揮官でなく、
// policy の agent:stop と agent:revive が両方 allow のときだけ(だめなら却下の行を残して 4)。上限を既に超えていれば 4。
func restartGate(_ context.Context, env *app.Env, who string, v any) app.Decision {
	in := v.(RestartReq)
	if err := validName(in.Name); err != nil {
		return app.Decision{Err: err}
	}
	if utf8.RuneCountInString(in.Prompt) > 4000 {
		return app.Decision{Err: define.Errorf(define.CodeInvalidArgument, "agent-restart: --prompt は 4000 字まで")}
	}
	_, err := withDB(env, func(db *store.DB) (struct{}, error) {
		if who != "human" {
			why := ""
			if who == in.Name {
				why = "自分自身は再起動できません"
			} else {
				for _, n := range message.ConductorNames(db.DB, db.Session) {
					if n == in.Name {
						why = "指揮官(" + in.Name + ")は agent から再起動できません(人が実行します)"
					}
				}
			}
			if why == "" {
				j := approval.NewJudge(db.DB, db.Session)
				s, _ := j.Of("agent:stop:"+in.Name, who)
				r, _ := j.Of("agent:revive:"+in.Name, who)
				if s != "allow" || r != "allow" {
					why = "policy で agent:stop と agent:revive(" + in.Name + ")の両方が allow ではありません(stop=" + s + " revive=" + r + ")。人が実行するか、人が許可してください"
				}
			}
			if why != "" {
				ts := env.NowString()
				_, _ = db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at, decided_at) VALUES (?, 'revive', ?, ?, ?, 'rejected', ?, ?, ?)`,
					db.Session, in.Name, restartArgs(in), who, "restart refused: "+why, ts, ts)
				return struct{}{}, &define.Error{Code: define.CodePermissionDenied, Message: why, Reasons: []string{ReasonOpDenied}}
			}
		}
		var b bytes.Buffer
		if RefuseRunning(db, 0, &b) { // 動作中が既に上限を超えているときは触らない(再起動で数は増えない)
			return struct{}{}, limitErr(&b)
		}
		return struct{}{}, nil
	})
	return app.Decision{Err: err}
}

func restartOp(env *app.Env) app.Op {
	return app.Command(env, "agent.restart", restartGate,
		func(ctx context.Context, in RestartReq) (LifeResult, error) {
			who := by(ctx)
			return withDB(env, func(db *store.DB) (LifeResult, error) {
				r := RestartContext(ctx, db, tickets.LoadStatuses(db.DB, db.Session), in.Name, in.Prompt, who, env.Warner())
				agents.RecordOp(db.DB, db.Session, "revive", in.Name, restartArgs(in), who, r.Code, "restart: "+r.Out)
				if err := r.Err(); err != nil {
					return LifeResult{}, err
				}
				return LifeResult{Name: in.Name, Out: r.Out}, nil
			})
		},
		define.Doc("動いている agent を同じ会話で再起動する(tb agent-restart)"),
		define.Authz("cmd:agent:restart", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/{name}/restart"),
	)
}
