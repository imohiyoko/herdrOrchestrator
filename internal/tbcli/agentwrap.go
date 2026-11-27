package tbcli

import (
	"io"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// agent の起動の部品は app/agent にある(ADR 0008 の段 1)。ここは tbcli のほかのコマンドが使う名前の包み。

var (
	kindAll = agent.KindAll
	modelRE = agent.ModelRE
)

const (
	kindDefault          = agent.KindDefault
	restartDefaultPrompt = agent.RestartDefaultPrompt
)

func kindValid(k string) bool                                   { return agent.KindValid(k) }
func kindSupports(kind, item string) bool                       { return agent.KindSupports(kind, item) }
func kindRequire(kind, item, note string) (string, bool)        { return agent.KindRequire(kind, item, note) }
func kindLaunchArgs(kind, sess, model string) []string          { return agent.LaunchArgs(kind, sess, model) }
func roleModelFor(db *store.DB, name, reg string) string        { return agent.RoleModelFor(db, name, reg) }
func agentCwd() string                                          { return agent.DefaultCwd() }
func isDir(d string) bool                                       { return agent.IsDir(d) }
func limitsCounts(db *store.DB) (running, working int, ok bool) { return agent.Counts(db) }

func modelWithRoleDefault(db *store.DB, model, kind, name, role string) string {
	return agent.ModelWithRoleDefault(db, model, kind, name, role)
}

func firstPrompt(db *store.DB, name, prompt, by string, stderr io.Writer) string {
	return agent.FirstPrompt(db, name, prompt, by, stderr)
}

// kindCheckLaunch は kind_check_launch: 要求された起動に対応していなければ 12。
func kindCheckLaunch(kind, model, sess string) *opResult {
	if r := agent.CheckLaunch(kind, model, sess); r != nil {
		return &opResult{code: r.Code, out: r.Out}
	}
	return nil
}

// limitsRefuseRunning は limits_refuse_running <add>: 上限を超えるなら理由を stderr に出して 4。
func limitsRefuseRunning(db *store.DB, add int, stderr io.Writer) error {
	if agent.RefuseRunning(db, add, stderr) {
		return &exit{4, ""}
	}
	return nil
}

type regRow struct {
	ok                     bool
	cwd, model, sess, kind string
}

func agentRegRow(db *store.DB, name string) regRow {
	r := agent.RegRowOf(db, name)
	return regRow{r.Ok, r.Cwd, r.Model, r.Sess, r.Kind}
}

func doAgentRevive(db *store.DB, name string, fresh bool, model, prompt, by string, stderr io.Writer) opResult {
	r := agent.Revive(db, name, fresh, model, prompt, by, stderr)
	return opResult{code: r.Code, out: r.Out, note: r.Note}
}

func doAgentRestart(db *store.DB, st tickets.StatusSet, name, prompt, by string, stderr io.Writer) opResult {
	r := agent.Restart(db, st, name, prompt, by, stderr)
	return opResult{code: r.Code, out: r.Out, note: r.Note}
}

func secEnv(k string, def float64) float64 { return agent.SecEnv(k, def) }

var reRoleName = agent.RoleNameRE

func roleConflict(r *agents.Roles, name, role string) (string, bool) {
	return agent.RoleConflict(r, name, role)
}

func doAgentNew(db *store.DB, name, cwd, model, role, prompt, kill, by, sby, stask, kind string, stderr io.Writer) opResult {
	r := agent.New(db, name, cwd, model, role, prompt, kill, by, sby, stask, kind, stderr)
	return opResult{code: r.Code, out: r.Out, note: r.Note}
}
