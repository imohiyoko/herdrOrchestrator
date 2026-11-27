// Package config は設定(config 表と、環境変数で明示したファイル)を書く操作のアプリケーション API(ADR 0008。tb のサービス config)。
// 書けるのは人だけが既定(commands.tbl の cmd:config:set / unset)。policy を変える agent はさらに、変わる行ごとに policy:<操作> が要る。
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	kconfig "github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// SetReq は config.set の入力。Shared なら共通の行(*)に書く。How は記録に残す理由(tb の set / edit。空なら set)。
type SetReq struct {
	Key    string `api:"key,path"`
	Body   string `api:"body"`
	Shared bool   `api:"shared,optional"`
	How    string `api:"how,optional"`
}

// UnsetReq は config.unset の入力(DB の行を消す)。
type UnsetReq struct {
	Key    string `api:"key,path"`
	Shared bool   `api:"shared,optional"`
}

// Result は書いたあとに読まれる設定の出どころ(db:<セッション> / db:* / file:<パス> / none)。
type Result struct {
	Key     string       `api:"key" json:"key"`
	Source  string       `api:"source" json:"source"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *Result) SetPending(p *app.Pending) { r.Pending = p }

// Validators は config_validate_<キー>。検査はそのキーを持つサービスのパッケージに置く。
func Validators(st tickets.StatusSet) map[string]kconfig.Validator {
	return map[string]kconfig.Validator{
		"messages":      kconfig.ValidateMessages,
		"statuses":      tickets.ValidateStatuses,
		"labels":        tickets.ValidateLabels,
		"system-labels": tickets.ValidateSystemLabels,
		"notify-rules":  notify.ValidateRules,
		"nudge":         message.ValidateNudge(st),
		"protect":       agents.ValidateProtect,
		"role-models":   agents.ValidateRoleModels,
		"org":           approval.ValidateOrg,
		"policy":        perms.ValidatePolicy,
	}
}

// Ops は設定の操作。
func Ops(env *app.Env) []app.Op {
	set := app.Command(env, "config.set", howGate("config"),
		func(ctx context.Context, in SetReq) (Result, error) {
			p, _ := define.PrincipalFrom(ctx)
			return withDB(env, func(db *store.DB) (Result, error) { return set(env, db, app.Who(p), in) })
		},
		define.Doc("設定を書く(tb config set / edit。検査を通らない本文は書かない。人だけが既定)"),
		define.Authz("cmd:config:set", define.Resource("config", "key")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("PUT /v1/config/{key}"),
	)
	unset := app.Command(env, "config.unset", app.Cmd("cmd:config:unset", "tb config unset"),
		func(ctx context.Context, in UnsetReq) (Result, error) {
			p, _ := define.PrincipalFrom(ctx)
			return withDB(env, func(db *store.DB) (Result, error) { return unset(db, app.Who(p), in) })
		},
		define.Doc("設定の DB の行を消す(tb config unset。人だけが既定)"),
		define.Authz("cmd:config:unset", define.Resource("config", "key")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("DELETE /v1/config/{key}"),
	)
	return []app.Op{set, unset}
}

// howGate は set / edit の門: 記録の理由 How が edit なら cmd:<サービス>:edit、ほかは cmd:<サービス>:set の行で判定する
// (tb config edit / tb job edit の入口と同じ行)。
func howGate(svc string) app.Gate {
	return func(ctx context.Context, env *app.Env, who string, in any) app.Decision {
		verb := "set"
		if h, ok := in.(interface{ HowOf() string }); ok && h.HowOf() == "edit" {
			verb = "edit"
		}
		return app.Cmd("cmd:"+svc+":"+verb, "tb "+svc+" "+verb)(ctx, env, who, in)
	}
}

// HowOf は記録の理由(門が見る)。
func (r SetReq) HowOf() string { return r.How }

func withDB(env *app.Env, f func(*store.DB) (Result, error)) (Result, error) {
	db, done, err := env.DB()
	if err != nil {
		return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	defer done()
	return f(db)
}

func refuse(format string, a ...any) error {
	return define.Errorf(define.CodeFailedPrecondition, format, a...)
}

// KeyOK は使える設定のキーか。
func KeyOK(k string) error {
	if _, ok := kconfig.Info(k); !ok {
		return define.Errorf(define.CodeInvalidArgument, "不明な設定のキー: %s(使えるのは: %s)", k, strings.Join(kconfig.Keys, " "))
	}
	return nil
}

// Precheck は config set / edit で本文を読む前に断るもの(tb は本文を読む前に呼ぶ。config.set の中でも同じ確かめをする)。
func Precheck(key string, shared bool) error {
	_, err := common(key, shared, false)
	return err
}

// common は set / unset に共通の確かめ(キー・書けないキー・--shared とファイル)。ファイルを明示していればそのパス。
func common(key string, shared, unset bool) (string, error) {
	if err := KeyOK(key); err != nil {
		return "", err
	}
	if key == "conv-routes" && !unset {
		return "", refuse("conv-routes は tb conv-routes-set < 新しい表 で書きます(緩める変更は --approve-looser が要ります)")
	}
	if (key == "approvals" || key == "grants") && !unset {
		return "", refuse("%s はもう読みません(ADR 0007)。権限は設定 policy に書きます(残っている古い設定は tb policy import で移します)", key)
	}
	info, _ := kconfig.Info(key)
	envFile := os.Getenv(info.Env)
	if shared && envFile != "" {
		return "", refuse("%s でファイルを明示しているので --shared は使えません", info.Env)
	}
	return envFile, nil
}

func source(db *store.DB, key string) string {
	_, s, _ := kconfig.Lookup(db.DB, db.Session, key)
	return s
}

// policyCheck は who が policy を before から after に変えてよいか(人は常に通る)。
func policyCheck(db *store.DB, who, before, after, what string) error {
	err := perms.RequirePolicyWriteAs(db.DB, db.Session, who, before, after, what)
	var d *perms.Denied
	if !errors.As(err, &d) {
		return err
	}
	e := &define.Error{Code: define.CodePermissionDenied, Message: d.Msg}
	if d.Code == 20 {
		e.Reasons = []string{app.ReasonApprovalRequired}
	}
	return e
}

// policyBefore は設定 policy を書く前の本文。共通の行(--shared)はその行そのもの(ほかのセッションにも効くため)。
// それ以外は今このセッションに効いている本文(書いたあとに効くものとの差分を確かめる)。
// 読めなければ誤り(空として扱うと、消える行を確かめずに済んでしまう)。
func policyBefore(db *store.DB, shared bool) (string, error) {
	if shared {
		b, err := kconfig.DBRowAt(db.DB, db.Session, "policy", true)
		if err != nil {
			return "", refuse("設定 policy を読めないため書きません(db:%v)", err)
		}
		return b, nil
	}
	cur, _, _, re := kconfig.LookupStrict(db.DB, db.Session, "policy")
	if re != "" {
		return "", refuse("設定 policy を読めないため書きません(%s)", re)
	}
	return cur, nil
}

func set(env *app.Env, db *store.DB, who string, in SetReq) (Result, error) {
	envFile, err := common(in.Key, in.Shared, false)
	if err != nil {
		return Result{}, err
	}
	how := in.How
	switch how {
	case "":
		how = "set"
	case "set", "edit":
	default:
		return Result{}, define.Errorf(define.CodeInvalidArgument, "how は set か edit: %s", how)
	}
	if v := Validators(tickets.LoadStatuses(db.DB, db.Session))[in.Key]; v != nil {
		if msg := v(in.Body); msg != "" {
			fmt.Fprint(env.Warner(), msg)
			if env.Warn == nil { // 警告の出し先が無い入口(Web UI など)には、検査の文言を理由に含める
				return Result{}, refuse("%s の本文に問題があるため書きませんでした: %s", in.Key, strings.TrimSpace(msg))
			}
			return Result{}, refuse("%s の本文に問題があるため書きませんでした", in.Key)
		}
	}
	if in.Key == "policy" && who != "human" {
		before, err := policyBefore(db, in.Shared && envFile == "")
		if err != nil {
			return Result{}, err
		}
		if err := policyCheck(db, who, before, in.Body, "tb config "+how+" policy"); err != nil {
			return Result{}, err
		}
	}
	if envFile != "" {
		if err := kconfig.WriteFile(envFile, in.Body); err != nil {
			var sl *kconfig.ErrSymlink
			if errors.As(err, &sl) {
				return Result{}, refuse("%s", sl.Error())
			}
			return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
		}
	} else if err := kconfig.DBPut(db.DB, db.Session, in.Key, in.Body, how, in.Shared, who); err != nil {
		return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	return Result{Key: in.Key, Source: source(db, in.Key)}, nil
}

func unset(db *store.DB, who string, in UnsetReq) (Result, error) {
	envFile, err := common(in.Key, in.Shared, true)
	if err != nil {
		return Result{}, err
	}
	if envFile != "" {
		info, _ := kconfig.Info(in.Key)
		return Result{}, refuse("%s でファイルを明示しています(DB の行は使っていません)", info.Env)
	}
	if in.Key == "policy" && who != "human" {
		before, err := policyBefore(db, in.Shared)
		if err != nil {
			return Result{}, err
		}
		after, re := kconfig.LookupWithout(db.DB, db.Session, in.Key, in.Shared)
		if re != "" {
			return Result{}, refuse("設定 policy を読めないため消しません(%s)", re)
		}
		if err := policyCheck(db, who, before, after, "tb config unset policy"); err != nil {
			return Result{}, err
		}
	}
	if err := kconfig.UnsetBy(db.DB, db.Session, in.Key, in.Shared, who); err != nil {
		return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	return Result{Key: in.Key, Source: source(db, in.Key)}, nil
}
