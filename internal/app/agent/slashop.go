package agent

import (
	"context"
	"fmt"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// SlashReq は agent.slash の入力(tb slash)。Apply が false なら送らずに何をするかだけを返す。
type SlashReq struct {
	Command string   `api:"command"`
	Targets []string `api:"targets,optional"` // agent 名か @<役割>
	All     bool     `api:"all,optional"`     // 動いている全員(常に人の承認待ち)
	Apply   bool     `api:"apply,optional"`
}

// SlashResult は宛先ごとの 1 行(tb の出力のまま)と終了コード(0 / 4 = deny があった / 11 = 送れなかった / 20 = 承認待ちを積んだ)。
type SlashResult struct {
	Lines   []string     `api:"lines" json:"lines"`
	Code    int          `api:"code" json:"code"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *SlashResult) SetPending(p *app.Pending) { r.Pending = p }

// ValidSlash は agent.slash の入力の確かめ(tb は開く前に呼ぶ)。
func ValidSlash(in SlashReq) error {
	if in.Command == "" {
		return define.Errorf(define.CodeInvalidArgument, "slash <コマンド名> <agent 名|@役割>... [--all] [--apply] / slash --list")
	}
	if !ReSlashName.MatchString(in.Command) {
		return define.Errorf(define.CodeInvalidArgument, "slash: コマンド名に使えない文字があります: %s", in.Command)
	}
	if in.All && len(in.Targets) > 0 {
		return define.Errorf(define.CodeInvalidArgument, "slash: --all と宛先は一緒に指定できません")
	}
	if !in.All && len(in.Targets) == 0 {
		return define.Errorf(define.CodeInvalidArgument, "slash: 宛先(agent 名・@役割・--all)が必要です")
	}
	for _, t := range in.Targets {
		if !agents.NameRE.MatchString(t) && !ReSlashTarget.MatchString(t) {
			return define.Errorf(define.CodeInvalidArgument, "slash: 宛先は agent 名か @<役割>: %s", t)
		}
	}
	return nil
}

func slashOp(env *app.Env) app.Op {
	return app.Command(env, "agent.slash", app.Entry("cmd:agent:slash", "tb slash"),
		func(ctx context.Context, in SlashReq) (SlashResult, error) {
			if err := ValidSlash(in); err != nil {
				return SlashResult{}, err
			}
			who := by(ctx)
			if who == "" {
				return SlashResult{}, define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
			}
			return withDB(env, func(db *store.DB) (SlashResult, error) { return slashRun(env, db, who, in) })
		},
		define.Doc("許可リストにあるスラッシュコマンドを agent に送る(tb slash。agent からは agent:slash:<コマンド>:<宛先> の判定、--all は常に人の承認)"),
		define.Authz("cmd:agent:slash"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/slash"),
	)
}

func slashPending(env *app.Env, db *store.DB, target, args, who, result string) (int64, string, error) {
	res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?, 'slash', ?, ?, ?, 'pending', ?, ?)`,
		db.Session, target, args, who, result, env.NowString())
	if err != nil {
		return 0, "", define.Errorf(define.CodeInternal, "agent_ops に書けません: %v", err)
	}
	id, _ := res.LastInsertId()
	return id, env.QOf(db.DB, "agent_ops", id), nil
}

func slashRun(env *app.Env, db *store.DB, who string, in SlashReq) (SlashResult, error) {
	cmd := in.Command
	body, _, has := config.Lookup(db.DB, db.Session, "slash")
	e, r, why := SlashLookup(body, has, cmd)
	switch r {
	case 0:
	case 3:
		return SlashResult{}, define.Errorf(define.CodeFailedPrecondition, "slash: 許可リストの /%s の行が正しくありません: %s", cmd, why)
	default:
		return SlashResult{}, define.Errorf(define.CodeFailedPrecondition, "slash: /%s は許可リスト(config/slash.conf)にありません。送りません", cmd)
	}
	if e.Policy != "allow" {
		note := ""
		if e.Note != "" {
			note = "(" + e.Note + ")"
		}
		return SlashResult{}, define.Errorf(define.CodeFailedPrecondition, "slash: /%s は許可リストで deny(既定で拒否)です。送りません%s", cmd, note)
	}
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return SlashResult{}, define.Errorf(define.CodeUnavailable, "herdr agent list に失敗しました")
	}
	j := approval.NewJudge(db.DB, db.Session)
	c := &SlashCtx{DB: db, Live: live, Roles: j.Roles, Cmd: cmd, E: e}
	var out SlashResult
	add := func(format string, a ...any) { out.Lines = append(out.Lines, fmt.Sprintf(format, a...)) }
	if in.All {
		if !in.Apply {
			add("dry-run: --all は --apply で人の承認待ち(L-<n>)になり、承認後に次の宛先へ送ります")
			for _, t := range c.Resolve(true, who, nil) {
				why := t.Why
				if why == "" {
					why = "would-send(after approval)"
				}
				add("%s %s", t.Name, why)
			}
			return out, nil
		}
		id, q, err := slashPending(env, db, "*", jsonArgs("command", cmd, "args", e.Args, "all", 1), who, "slash --all は常に人の承認")
		if err != nil {
			return SlashResult{}, err
		}
		add("L-%d pending %s (/%s を --all で送る。人の承認待ち: tb approval approve %s)", id, q, cmd, q)
		out.Code = 20
		return out, nil
	}
	for _, t := range c.Resolve(false, who, in.Targets) {
		if t.Name == "" {
			continue
		}
		if t.Why != "" {
			add("%s %s", t.Name, t.Why)
			continue
		}
		if who != "human" {
			if v, rule := j.Of("agent:slash:"+cmd+":"+t.Name, who); v != "allow" {
				if v == "deny" {
					add("%s denied(認可: %s)", t.Name, rule)
					out.Code = 4
					continue
				}
				if !in.Apply {
					add("%s would-ask(approval: agent:slash:%s:%s is ask)", t.Name, cmd, t.Name)
					continue
				}
				id, q, err := slashPending(env, db, t.Name, jsonArgs("command", cmd, "args", e.Args), who, "認可: "+rule)
				if err != nil { // 送り済みの宛先の行は残す
					add("%s failed(%s)", t.Name, err.Error())
					out.Code = 11
					continue
				}
				add("%s pending L-%d %s (承認待ち: tb approval approve %s)", t.Name, id, q, q)
				if out.Code == 0 || out.Code == 20 {
					out.Code = 20
				}
				continue
			}
		}
		if !in.Apply {
			add("%s would-send(dry-run)", t.Name)
			continue
		}
		line, r := c.SendOne(t.Name, who, 0, "")
		add("%s", line)
		if r == 1 {
			out.Code = 11
		}
	}
	return out, nil
}
