package tbcli

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// モデルの切り替え(taskboard/lib/models.sh の cmd_model-set / model_set_precheck / model_set_run / do_model_set)。
// 承認待ち(op=revive、args.model_set=1)の承認は API の agent.approve(--always だけは tb agent-approve)。

const modelCacheNote = "注意: モデルを変えると、その会話のプロンプトキャッシュは使えなくなります(切り替えた直後の 1 回は、会話の全体を読み直す分の費用がかかります)"

var reModelSel = regexp.MustCompile(`^[A-Za-z0-9*?._+-]{1,64}$`)

// 実行の本体は app/agent(承認後の実行を API でもするため。ADR 0008)。ここは tbcli の名前の包み。
func liveStatus(a herdr.Agent) string { return agent.LiveStatus(a) }

func modelSetPrecheck(db *store.DB, live []herdr.Agent, name, model string, reset bool) (int, string) {
	return agent.ModelSetPrecheck(db, live, name, model, reset)
}

func modelSetRun(db *store.DB, name, args, by string, stderr io.Writer) opResult {
	r := agent.ModelSetRun(db, name, args, by, stderr)
	return opResult{code: r.Code, out: r.Out}
}

func cmdModelSet(args []string, w, stderr io.Writer) error {
	u := "model-set <名前 | @役割 | --all> <モデル> [--dry-run] | model-set <名前 | @役割 | --all> --reset [--dry-run]"
	if len(args) < 2 {
		return usage("%s", u)
	}
	sel, model, reset, dry := args[0], "", false, false
	for _, a := range args[1:] {
		switch {
		case a == "--reset":
			reset = true
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "-"):
			return usage("model-set: 不明な引数 %s", a)
		default:
			if model != "" {
				return usage("%s", u)
			}
			model = a
		}
	}
	if reset && model != "" {
		return usage("model-set: --reset とモデル名は同時に指定できません")
	}
	if !reset && model == "" {
		return usage("%s", u)
	}
	if model != "" && !modelRE.MatchString(model) {
		return usage("model-set: モデル名に使えない文字があります: %s", model)
	}
	all := false
	switch {
	case sel == "--all":
		all = true
	case strings.HasPrefix(sel, "@"):
		if !reModelSel.MatchString(sel[1:]) {
			return usage("model-set: @<役割> に使えない文字があります: %s", sel)
		}
	default:
		if err := validAgentName(sel); err != nil {
			return err
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requester()
	if err != nil {
		return err
	}
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return &exit{11, "herdr agent list に失敗しました"}
	}
	// 対象を集める(このセッションの台帳と、今動いているもの)
	var names []string
	_ = qrows(db.DB, `SELECT name FROM agents`, nil, func(_ []string, v []any) error {
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
				return &exit{11, "台帳を読めず、" + n + " の役割を確かめられません"}
			}
		}
	case all:
		targets = names
	default:
		targets = []string{sel}
	}
	if len(targets) == 0 {
		return die("対象の agent がいません: %s", sel)
	}
	conductors := message.ConductorNames(db.DB, db.Session)
	changed, skipped, unsup, failed, pending := 0, 0, 0, 0, 0
	for _, n := range targets {
		if n == by {
			fmt.Fprintln(w, n+" skipped (自分自身は切り替えません)")
			skipped++
			continue
		}
		if slices.Contains(conductors, n) {
			fmt.Fprintln(w, n+" skipped (指揮官は切り替えません。人が tb agent-revive "+n+" --model M で行う)")
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
		opArgs := jsonC(kv...)
		// 先に、承認に回す前に分かる「対応していない」「今は切り替えない」を見る(--dry-run の計画もここで出す)
		r, line := modelSetPrecheck(db, live, n, model, reset)
		if r != 0 {
			fmt.Fprintln(w, n+" "+line)
			if r == 12 {
				unsup++
			} else {
				skipped++
			}
			continue
		}
		// --all は承認待ちにだけ積む(自動では実行しない)。agent の pane からは agent:model:<名前> が allow のときだけ実行
		hold, why := false, ""
		if all {
			hold, why = true, "--all は人の承認が要ります"
		} else if by != "human" {
			v, rule := approval.NewJudge(db.DB, db.Session).Of("agent:model:"+n, by)
			if v == "deny" {
				fmt.Fprintln(w, n+" denied(認可: "+rule+")")
				skipped++
				continue
			}
			if v != "allow" {
				hold, why = true, "認可: "+rule
			}
		}
		if dry {
			if hold {
				fmt.Fprintln(w, n+" would hold for approval ("+line+" / "+why+")")
			} else {
				fmt.Fprintln(w, n+" would "+line)
			}
			continue
		}
		if hold {
			res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?, 'revive', ?, ?, ?, 'pending', ?, ?)`,
				db.Session, n, opArgs, by, why, nowTS())
			if err != nil {
				return err
			}
			lid, _ := res.LastInsertId()
			q := pendingQ(db.DB, "agent_ops", lid)
			fmt.Fprintf(w, "%s pending L-%d %s (人の承認待ち: tb approval approve %s)\n", n, lid, q, q)
			pending++
			continue
		}
		res := modelSetRun(db, n, opArgs, by, stderr)
		recordOp(db, "revive", n, opArgs, by, res.code, "model-set: "+res.out)
		fmt.Fprintln(w, n+" "+res.out)
		switch res.code {
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
		fmt.Fprintln(w, "(dry-run: 何も変えていません)")
	}
	if changed > 0 && !reset {
		fmt.Fprintln(w, modelCacheNote)
	}
	switch {
	case failed > 0:
		return &exit{11, ""}
	case pending > 0:
		return &exit{20, ""}
	case unsup > 0:
		return &exit{12, ""}
	case changed == 0 && skipped > 0 && len(targets) == 1:
		return &exit{1, ""}
	}
	return nil
}
