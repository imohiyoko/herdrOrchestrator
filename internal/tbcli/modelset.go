package tbcli

import (
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// モデルの切り替え(taskboard/lib/models.sh の cmd_model-set / model_set_precheck / model_set_run / do_model_set)。
// 承認待ち(op=revive、args.model_set=1)の承認は API の agent.approve(--always だけは tb agent-approve)。

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
	in := agent.ModelSetReq{Select: sel, Model: model, Reset: reset, DryRun: dry}
	if err := agent.ValidModelSet(in); err != nil {
		return appErr(err)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	var r agent.ModelSetResult
	if err := callAppEnv(appEnv(db, stderr), by, "agent.model.set", in, &r); err != nil {
		return err
	}
	for _, l := range r.Lines {
		fmt.Fprintln(w, l)
	}
	if r.Code != 0 {
		return &exit{r.Code, ""}
	}
	return nil
}
