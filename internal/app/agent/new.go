package agent

import (
	"bytes"
	"context"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// NewReq は agent.new の入力(tb agent-new)。
type NewReq struct {
	Name string `api:"name"`
	// Cwd は作業ディレクトリ(絶対パス。空なら既定の場所)。入口が NormalizeCwd で絶対パスにして渡す。
	Cwd      string `api:"cwd,optional"`
	Kind     string `api:"kind,optional"`     // 空なら claude
	Model    string `api:"model,optional"`    // 空なら役割の既定
	Role     string `api:"role,optional"`     // 役割(カンマ区切りで 1〜4 個)
	Prompt   string `api:"prompt,optional"`   // 最初の指示(4000 字まで)
	Killable string `api:"killable,optional"` // yes|no(空は unknown)
}

// NormalizeCwd は agent-new の --cwd を絶対パスにする: 空なら既定の場所、~ は HOME、相対パスは PWD から。
// 承認する側のカレントで解決されないよう、依頼する側(入口)で行う。存在の検査は実行時。
func NormalizeCwd(cwd string) string {
	if cwd == "" {
		cwd = DefaultCwd()
	}
	if strings.HasPrefix(cwd, "~") {
		cwd = os.Getenv("HOME") + cwd[1:]
	}
	if !strings.HasPrefix(cwd, "/") {
		cwd = envOr("PWD", ".") + "/" + cwd
	}
	if IsDir(cwd) {
		cwd = PhysDir(cwd)
	}
	return cwd
}

func (in NewReq) kind() string {
	if in.Kind == "" {
		return KindDefault
	}
	return in.Kind
}

func (in NewReq) kill() string {
	if in.Killable == "" {
		return "unknown"
	}
	return in.Killable
}

func (in NewReq) cwd() string {
	if in.Cwd == "" {
		return NormalizeCwd("")
	}
	return in.Cwd
}

func newArgs(in NewReq) string {
	kv := []any{"cwd", in.cwd()}
	if k := in.kind(); k != "claude" {
		kv = append(kv, "kind", k)
	}
	for _, p := range [][2]string{{"model", in.Model}, {"role", in.Role}, {"prompt", in.Prompt}} {
		if p[1] != "" {
			kv = append(kv, p[0], p[1])
		}
	}
	return jsonArgs(append(kv, "killable", in.kill())...)
}

func usageErr(format string, a ...any) error {
	return define.Errorf(define.CodeInvalidArgument, format, a...)
}

// newPre は承認待ちに積む前の確かめ(tb agent-new と同じ順と文言): 入力、kind でできるか(12)、役割、
// 止めてよいかの指定と、それを付けてよいか(人か、policy で agent:flag を許された agent)、動作中の上限(4)。
func newPre(env *app.Env, who string, in NewReq) error {
	if err := validName(in.Name); err != nil {
		return err
	}
	kind := in.kind()
	if !KindValid(kind) {
		return usageErr("agent-new: --kind は %s のどれか: %s", strings.Join(KindAll, " "), kind)
	}
	if in.Model != "" && !ModelRE.MatchString(in.Model) {
		return usageErr("agent-new: --model に使えない文字があります: %s", in.Model)
	}
	if r := CheckLaunch(kind, in.Model, ""); r != nil {
		return r.Err()
	}
	if in.Cwd != "" && !strings.HasPrefix(in.Cwd, "/") {
		return usageErr("agent-new: --cwd は絶対パスにしてください: %s", in.Cwd)
	}
	_, err := withDB(env, func(db *store.DB) (struct{}, error) {
		if in.Role != "" {
			rs := strings.Split(in.Role, ",")
			if len(rs) > 1 && rs[len(rs)-1] == "" { // IFS=, read -ra は末尾の空の欄を数えない
				rs = rs[:len(rs)-1]
			}
			if len(rs) < 1 || len(rs) > 4 {
				return struct{}{}, usageErr("agent-new: --role はカンマ区切りで 1〜4 個: %s", in.Role)
			}
			roles := agents.NewRoles(db.DB, db.Session)
			for _, x := range rs {
				if !RoleNameRE.MatchString(x) || x == "README" || !roles.Exists(x) {
					return struct{}{}, usageErr("agent-new: --role は定義のある役割名(tb role-list。カンマ区切りで複数可): %s", x)
				}
			}
			// roles/<名前>.md は台帳の役割より優先される。承認(要求された役割)と作成後の役割がずれないよう拒否する
			if _, bad := RoleConflict(roles, in.Name, in.Role); bad {
				return struct{}{}, usageErr("agent-new: 役割 %s の定義があるため、この名前の役割は %s に固定です(--role %s は効きません)", in.Name, in.Name, in.Role)
			}
		}
		kill := in.kill()
		if kill != "yes" && kill != "no" && kill != "unknown" {
			return struct{}{}, usageErr("agent-new: --killable は yes|no")
		}
		if kill != "unknown" && who != "human" { // 判定フラグを付けられるのは agent-flag と同じく人か、policy で agent:flag を許された agent だけ
			// 作る前の agent は台帳に無い。policy の @<役割> の対象は、要求された役割(--role)で比べる
			j := approval.NewJudge(db.DB, db.Session)
			if in.Role != "" {
				j.Roles.Hint(in.Name, in.Role)
			}
			if _, ok := j.GrantOf(who, "agent:flag", in.Name); !ok {
				return struct{}{}, define.Errorf(define.CodeFailedPrecondition, "agent-new --killable を付けられるのは人と、policy で agent:flag を許された agent だけです(%s)", who)
			}
		}
		if utf8.RuneCountInString(in.Prompt) > 4000 {
			return struct{}{}, usageErr("agent-new: --prompt は 4000 字まで")
		}
		var b bytes.Buffer
		if RefuseRunning(db, 1, &b) { // 動作中が上限なら、承認待ちにも積まず拒否(max_running_agents)
			return struct{}{}, limitErr(&b)
		}
		return struct{}{}, nil
	})
	return err
}

func newOp(env *app.Env) app.Op {
	return app.Command(env, "agent.new",
		opGateRole("new", func(in NewReq) string { return in.Name }, newArgs,
			func(who string, in NewReq) error { return newPre(env, who, in) },
			func(in NewReq) string { return in.Role }),
		func(ctx context.Context, in NewReq) (LifeResult, error) {
			who := by(ctx)
			return withDB(env, func(db *store.DB) (LifeResult, error) {
				r := NewContext(ctx, db, in.Name, in.cwd(), in.Model, in.Role, in.Prompt, in.kill(), who, "", "", in.kind(), env.Warner())
				agents.RecordOp(db.DB, db.Session, "new", in.Name, newArgs(in), who, r.Code, r.Out)
				if err := r.Err(); err != nil {
					return LifeResult{}, err
				}
				return LifeResult{Name: in.Name, Out: r.Out}, nil
			})
		},
		define.Doc("agent を作る(tb agent-new)"),
		define.Authz("cmd:agent:new", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents"),
	)
}
