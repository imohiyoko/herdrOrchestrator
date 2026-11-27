package agent

import (
	"context"
	"regexp"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

// 役割の定義(role:<名前> / role:@map)と台帳のロールの書き込み(tb role-write / role-edit / role-unset / role-set)。
// 人か、policy で role:change を許された agent だけが書く。自分の持つ役割と、role:change を許す役割を扱えるのは人だけ。

var reRoleItem = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RoleWriteReq は agent.role.write / agent.role.edit の入力(どちらで書いたかは操作で決まり、履歴の理由になる)。
type RoleWriteReq struct {
	Name   string `api:"name,path"`
	Body   string `api:"body"`
	Shared bool   `api:"shared,optional"`
}

// RoleUnsetReq は agent.role.unset の入力。
type RoleUnsetReq struct {
	Name   string `api:"name,path"`
	Shared bool   `api:"shared,optional"`
}

// RoleResult は役割の定義を書いた・消したあとの今の出どころ。Found=false なら定義は無くなった。
type RoleResult struct {
	Name    string       `api:"name" json:"name"`
	Src     string       `api:"src,optional" json:"src,omitempty"`
	Found   bool         `api:"found" json:"found"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *RoleResult) SetPending(p *app.Pending) { r.Pending = p }

// RoleSetReq は agent.role.set の入力(台帳のロールを変える。権限の変更でもある)。
type RoleSetReq struct {
	Name   string `api:"name,path"`
	Role   string `api:"role"` // カンマ区切りで 1〜4 個
	Reason string `api:"reason,optional"`
}

// RoleSetResult は変えたロール。Old は前のロール(無ければ空)、New は今効いているロール。
type RoleSetResult struct {
	Name    string       `api:"name" json:"name"`
	Old     string       `api:"old" json:"old"`
	New     string       `api:"new" json:"new"`
	By      string       `api:"by" json:"by"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *RoleSetResult) SetPending(p *app.Pending) { r.Pending = p }

func roleOps(env *app.Env) []app.Op {
	write := roleWriteOp(env, "write", "PUT /v1/roles/{name}")
	edit := roleWriteOp(env, "edit", "POST /v1/roles/{name}/edit")
	unset := app.Command(env, "agent.role.unset", app.Cmd("cmd:agent:role-unset", "tb role-unset"),
		func(ctx context.Context, in RoleUnsetReq) (RoleResult, error) {
			if roleWho(ctx) != "human" {
				return RoleResult{}, define.Errorf(define.CodePermissionDenied, "tb role-unset は人だけが実行できます")
			}
			key, err := roleKey(in.Name)
			if err != nil {
				return RoleResult{}, err
			}
			return withDB(env, func(db *store.DB) (RoleResult, error) {
				if err := config.Unset(db.DB, db.Session, key, in.Shared); err != nil {
					return RoleResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				return roleNow(db, in.Name), nil
			})
		},
		define.Doc("役割の定義の DB の行を消す(tb role-unset。人だけ)"),
		define.Authz("cmd:agent:role-unset", define.Resource("role", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("DELETE /v1/roles/{name}"),
	)
	set := app.Command(env, "agent.role.set", app.Entry("cmd:agent:role-set", "tb role-set"),
		func(ctx context.Context, in RoleSetReq) (RoleSetResult, error) {
			return withDB(env, func(db *store.DB) (RoleSetResult, error) { return roleSet(env, db, roleWho(ctx), in) })
		},
		define.Doc("台帳のロールを変える(tb role-set。人か、policy で role:change を許された agent。自分のロールは変えられない)"),
		define.Authz("cmd:agent:role-set", define.Resource("agent", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("PUT /v1/agents/{name}/role"),
	)
	return []app.Op{write, edit, unset, set}
}

// roleWriteOp は agent.role.write / agent.role.edit(tb role-write / role-edit。門の行は cmd:agent:role-<verb>)。
func roleWriteOp(env *app.Env, verb, route string) app.Op {
	how := "role-" + verb
	return app.Command(env, "agent.role."+verb, app.Entry("cmd:agent:"+how, "tb "+how),
		func(ctx context.Context, in RoleWriteReq) (RoleResult, error) {
			return withDB(env, func(db *store.DB) (RoleResult, error) { return roleWrite(db, roleWho(ctx), how, in) })
		},
		define.Doc("役割の定義を書く(tb "+how+"。人か、policy で role:change を許された agent)"),
		define.Authz("cmd:agent:"+how, define.Resource("role", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP(route),
	)
}

// roleWho は呼び手。人として扱うのは人だけ(system:* は agent と同じ確かめを受ける)。決められない主体は空。
func roleWho(ctx context.Context) string { return by(ctx) }

func roleKey(name string) (string, error) {
	key, ok := agents.KeyOf(name)
	if !ok {
		return "", define.Errorf(define.CodeInvalidArgument, "役割名は英数字・_ で始まり、英数字と . _ - の 64 字以内(または @map): %s", name)
	}
	return key, nil
}

func roleNow(db *store.DB, name string) RoleResult {
	_, src, ok := agents.NewRoles(db.DB, db.Session).LookupSrc(name)
	return RoleResult{Name: name, Src: src, Found: ok}
}

// HoldsRole は by が役割 role を持っているか(自分の持つ役割の定義は人だけが書き換える)。
func HoldsRole(j *approval.Judge, by, role string) bool { return j.Roles.Match(by, "@"+role) == 0 }

// GrantsRoleChange は役割 roles(カンマ区切り)を持つと policy の行で role:change が許されるようになるか。
// 役割の名前では決めず、役割の無いときと比べて policy の行だけで判定する(そういう役割を配る・書き換えるのは人だけ)。
func GrantsRoleChange(j *approval.Judge, roles string) bool {
	const probe = "role-probe.tb" // 台帳に無い名前。役割は Hint で与える
	defer j.Roles.Forget(probe)
	j.Roles.Hint(probe, "")
	if _, ok := j.GrantOf(probe, "role:change", "*"); ok {
		return false
	}
	j.Roles.Hint(probe, roles)
	_, ok := j.GrantOf(probe, "role:change", "*")
	return ok
}

// roleChangeCheck は agent が役割を変えてよいか。決めるのは policy の role:change の行だけ(役割の名前では決めない)。
// target は policy の対象、cmd は人に頼むときのコマンド。
func roleChangeCheck(db *store.DB, j *approval.Judge, who, target, cmd string) error {
	value, rule := "allow", ""
	if r, ok := j.GrantOf(who, "role:change", target); ok {
		rule = r
	} else {
		value, rule = j.Of("role:change", who)
	}
	if value == "deny" {
		return define.Errorf(define.CodePermissionDenied, "role:change は実行できません(deny。%s)(%s)", rule, who)
	}
	if value != "allow" {
		return &define.Error{Code: define.CodePermissionDenied, Reasons: []string{app.ReasonApprovalRequired}, // tb は文言を出して 20
			Message: "[role.change.needs-approval] " + config.Msg(db.DB, db.Session, "role.change.needs-approval",
				"role:change は人の承認が要ります({rule})。人が自分の端末で実行してください: {command}", "rule="+rule, "command="+cmd)}
	}
	return nil
}

func refuseWho(who string) error {
	if who == "" {
		return define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
	}
	return nil
}

// roleWrite は role_put: 役割の本文を DB に書く。
func roleWrite(db *store.DB, who, how string, in RoleWriteReq) (RoleResult, error) {
	key, err := roleKey(in.Name)
	if err != nil {
		return RoleResult{}, err
	}
	if err := refuseWho(who); err != nil {
		return RoleResult{}, err
	}
	name := in.Name
	j := approval.NewJudge(db.DB, db.Session)
	if who != "human" {
		if HoldsRole(j, who, name) {
			return RoleResult{}, define.Errorf(define.CodeFailedPrecondition, "自分の持つ役割(%s)を書き換えられるのは人だけです", name)
		}
		if name != "@map" && GrantsRoleChange(j, name) {
			return RoleResult{}, define.Errorf(define.CodeFailedPrecondition, "policy で role:change を許される役割(%s)を書き換えられるのは人だけです", name)
		}
		if name == "@map" {
			for _, line := range strings.Split(in.Body, "\n") {
				k, x, _ := strings.Cut(line, "=")
				x = util.ReadRest(x, "=")
				if pat := config.Trim(requests.CutHash(k)); pat != "" && util.Glob(pat, who) {
					return RoleResult{}, define.Errorf(define.CodeFailedPrecondition, "自分に当たる対応(%s)は書けません(自分のロールは変えられません)", pat)
				}
				if x = strings.ReplaceAll(config.Trim(requests.CutHash(x)), " ", ""); x != "" && GrantsRoleChange(j, x) {
					return RoleResult{}, define.Errorf(define.CodeFailedPrecondition, "policy で role:change を許される役割(%s)を付ける対応を書けるのは人だけです", x)
				}
			}
		}
		sh := ""
		if in.Shared {
			sh = " --shared"
		}
		if err := roleChangeCheck(db, j, who, name, "tb role-write "+name+sh+" < 本文"); err != nil {
			return RoleResult{}, err
		}
	}
	if strings.TrimFunc(in.Body, config.IsSpace) == "" {
		return RoleResult{}, define.Errorf(define.CodeFailedPrecondition, "本文が空です(消すときは人が tb role-unset %s で行を消す)", name)
	}
	if err := config.DBPut(db.DB, db.Session, key, in.Body, how, in.Shared, who); err != nil {
		return RoleResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	return roleNow(db, name), nil
}

// RoleItems は role-set のロールの並び(IFS=, read -ra: 末尾の空の欄は数えない)。1〜4 個でなければ使い方の誤り。
func RoleItems(role string) ([]string, error) {
	xs := strings.Split(role, ",")
	if xs[len(xs)-1] == "" {
		xs = xs[:len(xs)-1]
	}
	if len(xs) < 1 || len(xs) > 4 {
		return nil, define.Errorf(define.CodeInvalidArgument, "ロールはカンマ区切りで 1〜4 個: %s", role)
	}
	return xs, nil
}

// roleSet は台帳のロールを変える(読んだ台帳の値が変わっていないときだけ)。
func roleSet(env *app.Env, db *store.DB, who string, in RoleSetReq) (RoleSetResult, error) {
	name, role := in.Name, in.Role
	if !agents.NameRE.MatchString(name) {
		return RoleSetResult{}, define.Errorf(define.CodeInvalidArgument, "agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内: %s", name)
	}
	xs, err := RoleItems(role)
	if err != nil {
		return RoleSetResult{}, err
	}
	j := approval.NewJudge(db.DB, db.Session)
	for _, x := range xs {
		if !reRoleItem.MatchString(x) || x == "README" || !j.Roles.Exists(x) {
			return RoleSetResult{}, define.Errorf(define.CodeInvalidArgument, "ロールは定義のある役割名(tb role-list。カンマ区切りで複数可): %s", x)
		}
	}
	if err := refuseWho(who); err != nil {
		return RoleSetResult{}, err
	}
	if who != "human" {
		if who == name {
			return RoleSetResult{}, define.Errorf(define.CodeFailedPrecondition, "自分のロールは変えられません")
		}
		if GrantsRoleChange(j, strings.Join(xs, ",")) {
			return RoleSetResult{}, define.Errorf(define.CodeFailedPrecondition, "policy で role:change を許される役割(%s)を付けられるのは人だけです", strings.Join(xs, ","))
		}
		if err := roleChangeCheck(db, j, who, name, "tb role-set "+name+" "+role); err != nil {
			return RoleSetResult{}, err
		}
	}
	_ = agents.Sync(db.DB, db.Session)
	var one int
	if db.QueryRow(`SELECT 1 FROM agents WHERE name=?`, name).Scan(&one) != nil {
		if msg, ok := agents.OtherSession(db.DB, db.Session, name); ok {
			return RoleSetResult{}, define.Errorf(define.CodeNotFound, "%s(ロールはセッション固有)", msg)
		}
		return RoleSetResult{}, define.Errorf(define.CodeNotFound, "台帳に無い agent です: %s(tb agents で確かめる)", name)
	}
	r := agents.NewRoles(db.DB, db.Session)
	if _, src, ok := r.LookupSrc(name); ok {
		return RoleSetResult{}, define.Errorf(define.CodeFailedPrecondition, "役割 %s(%s)があるため、台帳のロールは効きません(その役割の定義を直す)", name, src)
	}
	// 旧ロールは読んだ台帳の値から決め、変更はその値が変わっていないときだけ行う
	var reg string
	if db.QueryRow(`SELECT COALESCE(role,'') FROM agents WHERE name=?`, name).Scan(&reg) != nil {
		return RoleSetResult{}, define.Errorf(define.CodeInternal, "台帳を読めません")
	}
	old := strings.Join(r.FilesForReg(name, reg), ",")
	n, err := roleSetTx(db, name, old, role, who, util.Oneline(in.Reason, 300), reg, env.NowString())
	if err != nil {
		return RoleSetResult{}, define.Errorf(define.CodeInternal, "ロールを変えられませんでした(DB)")
	}
	if n != 1 {
		return RoleSetResult{}, define.Errorf(define.CodeConflict, "同時に別のロール変更が入りました。もう一度実行してください")
	}
	nw := strings.Join(agents.NewRoles(db.DB, db.Session).FilesForLoose(name), ",")
	return RoleSetResult{Name: name, Old: old, New: nw, By: who}, nil
}

func roleSetTx(db *store.DB, name, old, role, by, why, reg, ts string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO main.role_changes (agent, old_role, new_role, changed_by, reason, created_at, herdr_session)
    SELECT ?, NULLIF(?,''), ?, ?, NULLIF(?,''), ?, ?
    WHERE (SELECT COALESCE(role,'') FROM agents WHERE name=?)=?`, name, old, role, by, why, ts, db.Session, name, reg); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE main.agents SET role=?, updated_at=? WHERE herdr_session=? AND name=? AND COALESCE(role,'')=?`, role, ts, db.Session, name, reg)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}
