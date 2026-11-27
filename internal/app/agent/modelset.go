package agent

import (
	"encoding/json"
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// モデルの切り替えの実行(tb model-set と、その承認待ち L-<n> の承認後。ADR 0008)。
// 承認待ちは op=revive、args.model_set=1 で積まれる。

// LiveStatus は herdr の agent_status(文字でなければ空)。
func LiveStatus(a herdr.Agent) string {
	s, _ := a.AgentStatus.(string)
	return s
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ModelSetPrecheck は承認の前に分かる結果。0 なら計画、3(skipped)か 12(not supported)なら理由。
func ModelSetPrecheck(db *store.DB, live []herdr.Agent, name, model string, reset bool) (int, string) {
	reg := RegRowOf(db, name)
	a, isLive := agents.LiveOf(live, name)
	if !reg.Ok && !isLive {
		if msg, other := agents.OtherSession(db.DB, db.Session, name); other {
			return 3, "skipped (" + msg + ")"
		}
		return 3, "skipped (台帳にありません)"
	}
	kind := reg.Kind
	if !reg.Ok {
		kind = KindDefault
	}
	old := reg.Model
	if reset {
		if old == "" {
			return 3, "skipped (台帳にモデルがありません)"
		}
		return 0, "reset (" + old + " -> 役割の既定 " + RoleModelFor(db, name, "") + ")"
	}
	if kind != "claude" {
		return 12, "not supported (kind=" + kind + " はモデルの切り替えに対応していません)"
	}
	if !ModelRE.MatchString(model) {
		return 12, "not supported (kind=" + kind + " のモデル名として使えません: " + model + ")"
	}
	if old == model {
		return 3, "skipped (すでに " + model + ")"
	}
	if isLive {
		if st := LiveStatus(a); st != "idle" && st != "done" {
			return 3, "skipped (" + orStr(st, "状態不明") + "。idle か done のときだけ切り替えます)"
		}
		return 0, "change (" + orStr(old, "既定") + " -> " + model + "、止めて同じ会話で再開)"
	}
	return 0, "change (" + orStr(old, "既定") + " -> " + model + "、停止中なので台帳だけ)"
}

// ModelSet は 1 体を切り替える。0=changed 3=skipped 11=herdr の失敗 12=not supported
func ModelSet(db *store.DB, name, model, by string, stderr io.Writer) Result {
	live, err := agents.SyncLive(db.DB, db.Session) // 承認後の実行でも、今の状態で判断する
	if err != nil {
		return Result{Code: 11, Out: "herdr agent list に失敗しました"}
	}
	reg := RegRowOf(db, name)
	a, isLive := agents.LiveOf(live, name)
	if !reg.Ok && !isLive {
		if msg, other := agents.OtherSession(db.DB, db.Session, name); other {
			return Result{Code: 3, Out: msg}
		}
		return Result{Code: 3, Out: name + " は台帳にありません"}
	}
	kind := reg.Kind
	if !reg.Ok {
		kind = KindDefault
	}
	if kind != "claude" {
		return Result{Code: 12, Out: "not supported (kind=" + kind + " はモデルの切り替えに対応していません)"}
	}
	if !ModelRE.MatchString(model) {
		return Result{Code: 12, Out: "not supported (kind=" + kind + " のモデル名として使えません: " + model + ")"}
	}
	old := reg.Model
	if !isLive { // 止まっている: 台帳の値だけを変える(次の tb agent-revive から効く)
		_, _ = db.Exec(`UPDATE main.agents SET model=?, updated_at=? WHERE herdr_session=? AND name=?`, model, nowTS(), db.Session, name)
		return Result{Code: 0, Out: "changed (" + orStr(old, "既定") + " -> " + model + "。停止中のため、次の tb agent-revive " + name + " から効きます)"}
	}
	if st := LiveStatus(a); st != "idle" && st != "done" {
		return Result{Code: 3, Out: "skipped (" + orStr(st, "状態不明") + "。idle か done のときだけ切り替えます)"}
	}
	if old == model {
		return Result{Code: 3, Out: "skipped (すでに " + model + ")"}
	}
	pane, sess := a.PaneID, a.AgentSession.Value
	if sess == "" {
		sess = reg.Sess
	}
	switch {
	case pane == "":
		return Result{Code: 3, Out: "skipped (pane が分かりません)"}
	case sess == "":
		return Result{Code: 3, Out: "skipped (session id が台帳にありません。同じ会話に戻れないので止めません)"}
	case reg.Cwd == "" || !IsDir(reg.Cwd):
		return Result{Code: 3, Out: "skipped (作業ディレクトリがありません: " + orStr(reg.Cwd, "未記録") + "。同じ会話に戻れないので止めません)"}
	}
	if !agents.ClosePane(db.DB, db.Session, name, pane, sess) {
		return Result{Code: 11, Out: "pane " + pane + " を閉じられません"}
	}
	_, _ = agents.SyncLive(db.DB, db.Session)
	if rev := Revive(db, name, false, model, "", by, stderr); rev.Code != 0 {
		return Result{Code: 11, Out: name + " を停止しましたが、" + model + " での復活に失敗: " + rev.Out + "。停止のままです。戻すなら: tb agent-revive " + name + " --model " + model}
	}
	return Result{Code: 0, Out: "changed (" + orStr(old, "既定") + " -> " + model + "、同じ会話で再開)"}
}

// ModelSetRun は承認後にも使う実行の本体(args は agent_ops.args: reset / model)。
func ModelSetRun(db *store.DB, name, args, by string, stderr io.Writer) Result {
	var a struct {
		Reset int    `json:"reset"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	if a.Reset == 1 {
		_, _ = db.Exec(`UPDATE main.agents SET model=NULL, updated_at=? WHERE herdr_session=? AND name=?`, nowTS(), db.Session, name)
		d := RoleModelFor(db, name, "")
		use := "agent の CLI の既定"
		if d != "" {
			use = "役割の既定 " + d
		}
		return Result{Code: 0, Out: "reset (台帳のモデルを消しました。次の起動・復活から " + use + " を使います)"}
	}
	return ModelSet(db, name, a.Model, by, stderr)
}
