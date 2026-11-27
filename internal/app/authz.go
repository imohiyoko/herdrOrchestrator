package app

import (
	"context"
	"database/sql"

	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
)

// Authorizer は入口の段の判定(port.Authorizer。ADR 0008 の決定 2 の 1)。
// 操作の define.Authz の操作名は commands.tbl の cmd:<サービス>:<動詞>。
//   - 場所が entry の行: policy の効果が deny なら拒否、allow と ask は通す(ask は Guard が承認待ちにする)
//   - 場所が inner の行: ここでは通し、Guard が 3 つの効果で判定する(承認待ち・記録・文言を tb と同じにするため)
//   - 表に無い操作: 拒否(fail-closed)
//
// 主体は CheckRequest のものだけを使い、pane や identity.System を見ない。
type Authorizer struct{ Env *Env }

// Check は port.Authorizer の実装。
func (a Authorizer) Check(_ context.Context, req port.CheckRequest) (port.Decision, error) {
	r, ok := perms.RowOf(req.Action)
	if !ok {
		return port.Decision{Reasons: []string{"unknown_operation"}}, nil
	}
	who := Who(req.Principal)
	if who == "" {
		return port.Decision{Reasons: []string{"principal_not_allowed"}}, nil
	}
	if r.Where != "entry" || who == humanID {
		return port.Decision{Allowed: true}, nil
	}
	tier, _, err := tierFor(a.Env, who, req.Action, "")
	if err != nil {
		return port.Decision{}, err
	}
	if tier == "deny" {
		return port.Decision{Reasons: []string{"policy_deny"}}, nil
	}
	return port.Decision{Allowed: true}, nil
}

// tierFor は主体 who のコマンドの操作 op の効果(人なら DB を開かない)。
func tierFor(env *Env, who, op, target string) (tier, rule string, err error) {
	if who == humanID {
		tier, rule = perms.TierFor(nil, "", who, op, target)
		return tier, rule, nil
	}
	var db *sql.DB
	session := ""
	if env != nil && env.DB != nil {
		d, done, err := env.DB()
		if err != nil {
			return "", "", err
		}
		defer done()
		db, session = d.DB, d.Session
	}
	tier, rule = perms.TierFor(db, session, who, op, target)
	return tier, rule, nil
}
