package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
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
	// Parent は親(台帳の spawned_by)。親が止まると子は orphan になる(tb agent-shed)。人だけが指定できる
	Parent string `api:"parent,optional"`
	// Pane は置き場所(空なら専用の workspace)。人だけが指定できる(agent セットが 1 つの workspace に並べる)
	Pane *PanePlacement `api:"pane,optional"`
}

// PanePlacement は agent の置き場所: 既存の pane を分けて置く(Split)か、名前を付けた新しい workspace(Workspace)。
type PanePlacement struct {
	Workspace string `api:"workspace,optional"` // 新しい workspace の label(空なら agent の名前)
	Split     string `api:"split,optional"`     // この pane を分けて置く(herdr の pane id)
	Direction string `api:"direction,optional"` // right|down(空は right)
}

// NewResult は agent.new の結果。
type NewResult struct {
	Name string `api:"name" json:"name"`
	// Out は tb の文言(「<名前> started (…)」)。途中の警告は Env.Warn に書く。
	Out       string       `api:"out" json:"out"`
	Pane      string       `api:"pane,optional" json:"pane,omitempty"`           // 起動した pane
	Workspace string       `api:"workspace,optional" json:"workspace,omitempty"` // 作った workspace(Split のときは空)
	Pending   *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *NewResult) SetPending(p *app.Pending) { r.Pending = p }

var (
	rePaneID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	reLabel  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func (in NewReq) place() transport.Place {
	if in.Pane == nil {
		return transport.Place{}
	}
	return transport.Place{Label: in.Pane.Workspace, Split: in.Pane.Split, Direction: in.Pane.Direction}
}

// placeErr は親と置き場所の確かめ(人だけ。親は台帳にいる別の agent。pane の id と向き)。
func placeErr(db *store.DB, who string, in NewReq) error {
	if in.Parent == "" && in.Pane == nil {
		return nil
	}
	if who != "human" {
		return define.Errorf(define.CodePermissionDenied, "agent-new: 親と置き場所(pane)を指定できるのは人だけです(%s)", who)
	}
	if in.Parent != "" {
		if err := validName(in.Parent); err != nil {
			return err
		}
		if in.Parent == in.Name {
			return usageErr("agent-new: 自分自身を親にはできません: %s", in.Name)
		}
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM agents WHERE name=?`, in.Parent).Scan(&n)
		if n == 0 {
			return define.Errorf(define.CodeFailedPrecondition, "agent-new: 親 %s が台帳にいません", in.Parent)
		}
	}
	if p := in.Pane; p != nil {
		switch {
		case p.Split != "" && p.Workspace != "":
			return usageErr("agent-new: pane を分ける(split)と新しい workspace は同時に指定できません")
		case p.Split != "" && !rePaneID.MatchString(p.Split):
			return usageErr("agent-new: pane の id に使えない文字があります: %s", p.Split)
		case p.Workspace != "" && !reLabel.MatchString(p.Workspace):
			return usageErr("agent-new: workspace の名前は英数字で始まり、英数字・.・_・- の 64 字以内: %s", p.Workspace)
		case p.Direction != "" && p.Direction != "right" && p.Direction != "down":
			return usageErr("agent-new: 分ける向きは right|down: %s", p.Direction)
		case p.Direction != "" && p.Split == "":
			return usageErr("agent-new: 向きは pane を分けるときだけ指定できます")
		}
	}
	return nil
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
	if !isAbs(cwd) {
		cwd = envOr("PWD", ".") + "/" + cwd
	}
	if IsDir(cwd) {
		cwd = PhysDir(cwd)
	}
	return cwd
}

// isAbs は絶対パスか(tb は / で始まるパス。Go のテストは Windows でも流れるので、その OS の絶対パスも認める)。
func isAbs(p string) bool { return strings.HasPrefix(p, "/") || filepath.IsAbs(p) }

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
	kv = append(kv, "killable", in.kill())
	if in.Parent != "" {
		kv = append(kv, "parent", in.Parent)
	}
	if p := in.Pane; p != nil {
		kv = append(kv, "pane", map[string]string{"workspace": p.Workspace, "split": p.Split, "direction": p.Direction})
	}
	return jsonArgs(kv...)
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
	if in.Cwd != "" && !isAbs(in.Cwd) {
		return usageErr("agent-new: --cwd は絶対パスにしてください: %s", in.Cwd)
	}
	_, err := withDB(env, func(db *store.DB) (struct{}, error) {
		if err := placeErr(db, who, in); err != nil {
			return struct{}{}, err
		}
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
		func(ctx context.Context, in NewReq) (NewResult, error) {
			who := by(ctx)
			return withDB(env, func(db *store.DB) (NewResult, error) {
				r := NewWith(ctx, db, NewSpec{Name: in.Name, Cwd: in.cwd(), Model: in.Model, Role: in.Role, Prompt: in.Prompt, Kill: in.kill(),
					By: who, SpawnedBy: in.Parent, Kind: in.kind(), At: in.place()}, env.Warner())
				agents.RecordOp(db.DB, db.Session, "new", in.Name, newArgs(in), who, r.Code, r.Out)
				if err := r.Err(); err != nil {
					return NewResult{}, err
				}
				return NewResult{Name: in.Name, Out: r.Out, Pane: r.Pane, Workspace: r.WS}, nil
			})
		},
		define.Doc("agent を作る(tb agent-new)"),
		define.Authz("cmd:agent:new", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents"),
	)
}
