package tbcli

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// 承認待ちの agent の操作(L-<n>)の行を読む所。承認と却下は API(agent.approve / agent.reject。internal/app/agent/approve.go)。

var reLid = regexp.MustCompile(`^L-[0-9]+$`)

// opRow は agent_ops の 1 行。
type opRow struct {
	id                         int64
	op, target, args, by, stat string
	result                     string
}

// opArgs は agent_ops.args のうち使う項目。
type opArgs struct {
	Cwd      string `json:"cwd"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	Prompt   string `json:"prompt"`
	Killable string `json:"killable"`
	Kind     string `json:"kind"`
	Force    int    `json:"force"`
	Fresh    int    `json:"fresh"`
	ModelSet int    `json:"model_set"`
	All      int    `json:"all"`
	Command  string `json:"command"`
	Spawn    struct {
		Parent string `json:"parent"`
		Task   string `json:"task"`
	} `json:"spawn"`
}

func (o opRow) parsed() opArgs {
	var a opArgs
	_ = json.Unmarshal([]byte(o.args), &a)
	return a
}

func validLid(s string) (int64, error) {
	if !reLid.MatchString(s) {
		return 0, usage("操作 ID は L-<数字>: %s", s)
	}
	n, _ := strconv.ParseInt(s[2:], 10, 64)
	return n, nil
}

func opRowOf(db *store.DB, id int64) (opRow, bool) {
	o := opRow{id: id}
	var args, result sql.NullString
	if db.QueryRow(`SELECT op, target, args, requested_by, status, result FROM agent_ops WHERE id=?`, id).Scan(&o.op, &o.target, &args, &o.by, &o.stat, &result) != nil {
		return o, false
	}
	o.args, o.result = args.String, result.String
	if o.args == "" {
		o.args = "{}"
	}
	return o, true
}

// agentOpAction は agent_op_action: 承認するときに grants で見る元の操作名(空 = 人だけ)。
func agentOpAction(db *store.DB, o opRow) string {
	a := o.parsed()
	switch o.op {
	case "slash":
		if o.target == "*" { // --all は人だけ
			return ""
		}
		return "agent:slash:" + a.Command + ":" + o.target
	case "new":
		if a.Spawn.Parent != "" {
			if spawnFrozen(db) { // spawn-freeze の間は、spawn の承認は人だけ
				return ""
			}
			return "agent:spawn:" + o.target
		}
		return "agent:new:" + o.target
	case "revive":
		if a.ModelSet == 1 {
			if a.All == 1 || strings.HasPrefix(o.result, "--all") { // tb model-set --all は人だけ
				return ""
			}
			return "agent:model:" + o.target
		}
		return "agent:revive:" + o.target
	}
	return "agent:" + o.op + ":" + o.target
}
