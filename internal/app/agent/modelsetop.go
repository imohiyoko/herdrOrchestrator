package agent

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// ModelCacheNote はモデルを切り替えたあとの注意。
const ModelCacheNote = "注意: モデルを変えると、その会話のプロンプトキャッシュは使えなくなります(切り替えた直後の 1 回は、会話の全体を読み直す分の費用がかかります)"

var reModelSel = regexp.MustCompile(`^[A-Za-z0-9*?._+-]{1,64}$`)

// ModelSetReq は agent.model.set の入力(tb model-set)。Select は agent 名、@<役割> か --all。
type ModelSetReq struct {
	Select string `api:"select"`
	Model  string `api:"model,optional"`
	Reset  bool   `api:"reset,optional"`
	DryRun bool   `api:"dry_run,optional"`
}

// ModelSetResult は宛先ごとの 1 行(tb の出力のまま)と終了コード
// (0 / 1 = 1 体だけで切り替えなかった / 11 = 失敗があった / 12 = 対応していない / 20 = 承認待ちを積んだ)。
type ModelSetResult struct {
	Lines   []string     `api:"lines" json:"lines"`
	Code    int          `api:"code" json:"code"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *ModelSetResult) SetPending(p *app.Pending) { r.Pending = p }

// ValidModelSet は agent.model.set の入力の確かめ(tb は開く前に呼ぶ)。
func ValidModelSet(in ModelSetReq) error {
	if in.Reset && in.Model != "" {
		return define.Errorf(define.CodeInvalidArgument, "model-set: --reset とモデル名は同時に指定できません")
	}
	if !in.Reset && in.Model == "" {
		return define.Errorf(define.CodeInvalidArgument, "model-set <名前 | @役割 | --all> <モデル> [--dry-run] | model-set <名前 | @役割 | --all> --reset [--dry-run]")
	}
	if in.Model != "" && !ModelRE.MatchString(in.Model) {
		return define.Errorf(define.CodeInvalidArgument, "model-set: モデル名に使えない文字があります: %s", in.Model)
	}
	switch sel := in.Select; {
	case sel == "--all":
	case strings.HasPrefix(sel, "@"):
		if !reModelSel.MatchString(sel[1:]) {
			return define.Errorf(define.CodeInvalidArgument, "model-set: @<役割> に使えない文字があります: %s", sel)
		}
	default:
		return validName(sel)
	}
	return nil
}

func modelSetOp(env *app.Env) app.Op {
	return app.Command(env, "agent.model.set", app.Entry("cmd:agent:model-set", "tb model-set"),
		func(ctx context.Context, in ModelSetReq) (ModelSetResult, error) {
			if err := ValidModelSet(in); err != nil {
				return ModelSetResult{}, err
			}
			who := by(ctx)
			if who == "" {
				return ModelSetResult{}, define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
			}
			return withDB(env, func(db *store.DB) (ModelSetResult, error) { return modelSetAll(env, db, who, in) })
		},
		define.Doc("agent のモデルを切り替える(tb model-set。agent からは agent:model:<名前> の判定、--all は常に人の承認)"),
		define.Authz("cmd:agent:model-set"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/agents/model-set"),
	)
}

func modelSetAll(env *app.Env, db *store.DB, who string, in ModelSetReq) (ModelSetResult, error) {
	sel, model, reset, dry := in.Select, in.Model, in.Reset, in.DryRun
	all := sel == "--all"
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return ModelSetResult{}, &define.Error{Code: define.CodeUnavailable, Message: "herdr agent list に失敗しました", Reasons: []string{ReasonHerdrUnavailable}}
	}
	// 対象を集める(このセッションの台帳と、今動いているもの)
	var names []string
	_ = store.Each(db.DB, `SELECT name FROM agents`, nil, func(_ []string, v []any) error {
		names = append(names, fmt.Sprint(v[0]))
		return nil
	})
	for _, a := range live {
		names = append(names, a.Name)
	}
	names = slices.DeleteFunc(names, func(n string) bool { return !agents.NameRE.MatchString(n) })
	sort.Strings(names)
	names = slices.Compact(names)
	var targets []string
	switch {
	case strings.HasPrefix(sel, "@"):
		roles := agents.NewRoles(db.DB, db.Session)
		for _, n := range names {
			switch roles.Match(n, sel) {
			case 0:
				targets = append(targets, n)
			case 2:
				return ModelSetResult{}, &define.Error{Code: define.CodeUnavailable, Message: "台帳を読めず、" + n + " の役割を確かめられません", Reasons: []string{ReasonHerdrUnavailable}}
			}
		}
	case all:
		targets = names
	default:
		targets = []string{sel}
	}
	if len(targets) == 0 {
		return ModelSetResult{}, define.Errorf(define.CodeNotFound, "対象の agent がいません: %s", sel)
	}
	var out ModelSetResult
	add := func(s string) { out.Lines = append(out.Lines, s) }
	conductors := message.ConductorNames(db.DB, db.Session)
	changed, skipped, unsup, failed, pending := 0, 0, 0, 0, 0
	for _, n := range targets {
		if n == who {
			add(n + " skipped (自分自身は切り替えません)")
			skipped++
			continue
		}
		if slices.Contains(conductors, n) {
			add(n + " skipped (指揮官は切り替えません。人が tb agent-revive " + n + " --model M で行う)")
			skipped++
			continue
		}
		kv := []any{"model_set", 1}
		if reset {
			kv = append(kv, "reset", 1)
		} else {
			kv = append(kv, "model", model)
		}
		if all { // --all の承認待ちは人だけが決める(tb agent-approve)
			kv = append(kv, "all", 1)
		}
		opArgs := jsonArgs(kv...)
		// 先に、承認に回す前に分かる「対応していない」「今は切り替えない」を見る(--dry-run の計画もここで出す)
		r, line := ModelSetPrecheck(db, live, n, model, reset)
		if r != 0 {
			add(n + " " + line)
			if r == 12 {
				unsup++
			} else {
				skipped++
			}
			continue
		}
		// --all は承認待ちにだけ積む(自動では実行しない)。agent からは agent:model:<名前> が allow のときだけ実行
		hold, why := false, ""
		if all {
			hold, why = true, "--all は人の承認が要ります"
		} else if who != "human" {
			v, rule := approval.NewJudge(db.DB, db.Session).Of("agent:model:"+n, who)
			if v == "deny" {
				add(n + " denied(認可: " + rule + ")")
				skipped++
				continue
			}
			if v != "allow" {
				hold, why = true, "認可: "+rule
			}
		}
		if dry {
			if hold {
				add(n + " would hold for approval (" + line + " / " + why + ")")
			} else {
				add(n + " would " + line)
			}
			continue
		}
		if hold {
			res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?, 'revive', ?, ?, ?, 'pending', ?, ?)`,
				db.Session, n, opArgs, who, why, env.NowString())
			if err != nil { // 切り替え済みの宛先の行は残す
				add(n + " failed(agent_ops に書けません: " + err.Error() + ")")
				failed++
				continue
			}
			lid, _ := res.LastInsertId()
			q := env.QOf(db.DB, "agent_ops", lid)
			add(fmt.Sprintf("%s pending L-%d %s (人の承認待ち: tb approval approve %s)", n, lid, q, q))
			pending++
			continue
		}
		res := ModelSetRun(db, n, opArgs, who, env.Warner())
		agents.RecordOp(db.DB, db.Session, "revive", n, opArgs, who, res.Code, "model-set: "+res.Out)
		add(n + " " + res.Out)
		switch res.Code {
		case 0:
			changed++
		case 3:
			skipped++
		case 12:
			unsup++
		default:
			failed++
		}
	}
	if dry {
		add("(dry-run: 何も変えていません)")
	}
	if changed > 0 && !reset {
		add(ModelCacheNote)
	}
	switch {
	case failed > 0:
		out.Code = 11
	case pending > 0:
		out.Code = 20
	case unsup > 0:
		out.Code = 12
	case changed == 0 && skipped > 0 && len(targets) == 1:
		out.Code = 1
	}
	return out, nil
}
