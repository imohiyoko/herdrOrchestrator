package agent

import (
	"context"
	"io"
	"regexp"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
)

// 作る(agent-new)の中身(tb の agents.sh の do_agent_new)。tb agent-spawn の子の作成と承認後の実行からも呼ぶ。

var RoleNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RoleConflict は role_conflict: 作成後の役割が、要求された(承認した)役割とずれるなら理由。
func RoleConflict(r *agents.Roles, name, role string) (string, bool) {
	if role == "" {
		return "", false
	}
	if role != name && r.Exists(name) {
		return "役割 " + name + " の定義があるため、" + name + " の役割は " + role + " になりません", true
	}
	for _, x := range strings.Split(role, ",") {
		x = config.Trim(x)
		if !r.Exists(x) {
			return "役割 " + x + " の定義が無いため、" + name + " の役割は " + role + " になりません", true
		}
	}
	return "", false
}

// New は do_agent_new(sby / stask は tb agent-spawn の子のときだけ)。
func New(db *store.DB, name, cwd, model, role, prompt, kill, by, sby, stask, kind string, stderr io.Writer) Result {
	return NewContext(context.Background(), db, name, cwd, model, role, prompt, kill, by, sby, stask, kind, stderr)
}

// NewContext は New に ctx を足したもの。ctx の期限を過ぎたら、起動する前に時間切れ(11)にする。
func NewContext(ctx context.Context, db *store.DB, name, cwd, model, role, prompt, kill, by, sby, stask, kind string, stderr io.Writer) Result {
	if r := CheckLaunch(kind, model, ""); r != nil {
		return *r
	}
	// 承認待ちの間に roles/<名前>.md が作られていたら、承認した役割と作成後の役割がずれるので作らない
	if why, bad := RoleConflict(agents.NewRoles(db.DB, db.Session), name, role); bad {
		return Result{Code: 1, Out: why}
	}
	live, err := agents.SyncLiveContext(ctx, db.DB, db.Session)
	if err != nil {
		return Result{Code: 11, Out: "herdr agent list に失敗しました"}
	}
	if _, ok := agents.LiveOf(live, name); ok {
		return Result{Code: 1, Out: name + " はすでに動いています"}
	}
	var state string
	_ = db.QueryRow(`SELECT state FROM agents WHERE name=?`, name).Scan(&state)
	if state == "stopped" {
		return Result{Code: 1, Out: "停止中の " + name + " が台帳にあります。tb agent-revive " + name + " で再開するか、別の名前にしてください"}
	}
	if !IsDir(cwd) {
		return Result{Code: 1, Out: "ディレクトリがありません: " + cwd}
	}
	cwd = PhysDir(cwd)
	// 先に台帳へ載せる(SessionStart hook が --role の役割を引けるように)
	why := ""
	if kill != "unknown" {
		why = "作成時に指定"
	}
	if err := Register(db, name, cwd, model, role, by, kill, why, kind); err != nil {
		return Result{Code: 1, Out: err.Error()}
	}
	if ctx.Err() != nil {
		_, _ = db.Exec(`DELETE FROM main.agents WHERE herdr_session=? AND name=?`, db.Session, name)
		return Result{Code: 11, Out: name + " を起動する前に時間切れになりました(起動していません)"}
	}
	sp, ok := transport.Spawn(db.Path, kind, name, cwd, LaunchArgs(kind, "", ModelWithRoleDefault(db, model, kind, name, role)))
	setPane := func() {
		_, _ = db.Exec(`UPDATE main.agents SET pane_id=?, spawned_by=NULLIF(?,''), spawn_task=NULLIF(?,'') WHERE herdr_session=? AND name=?`, sp.Pane, sby, stask, db.Session, name)
	}
	if !ok {
		if sp.Kept { // 人の操作待ち: pane は残っているので台帳にも残す(最初の指示は送らない)
			setPane()
			out := name + ": " + sp.Err
			if prompt != "" {
				out += "。最初の指示は未送信(答えたあと tb send などで送る)"
			}
			return Result{Code: 11, Out: out}
		}
		_, _ = db.Exec(`DELETE FROM main.agents WHERE herdr_session=? AND name=?`, db.Session, name)
		return Result{Code: 11, Out: name + " を起動できません: " + sp.Err}
	}
	setPane()
	out := name + " started (pane " + sp.Pane + ", cwd " + cwd
	if model != "" {
		out += ", model " + model
	}
	if role != "" {
		out += ", role " + role
	}
	out += ")"
	if prompt != "" {
		if note := FirstPrompt(db, name, prompt, by, stderr); note != "" {
			out += " / " + note
		}
	}
	return Result{Code: 0, Out: out}
}
