package app

import (
	"strings"

	"github.com/imohiyoko/API/port"
)

// 主体の写し(ADR 0007 の主体 → port.Principal)。
//
//	human           → Type user、ID "human"
//	agent <名前>     → Type service_account、ID "agent:<名前>"
//	system:<名前>    → Type service_account、ID "system:<名前>"
const (
	humanID     = "human"
	agentPrefix = "agent:"
	sysPrefix   = "system:"
)

// Human は人(tb の人の端末、Web UI にログインした人)。
func Human() port.Principal { return port.Principal{Type: port.PrincipalUser, ID: humanID} }

// Agent は agent name。
func Agent(name string) port.Principal {
	return port.Principal{Type: port.PrincipalServiceAccount, ID: agentPrefix + name}
}

// System は system:<名前> の主体(scheduler など)。name は "system:" を含めても含めなくてもよい。
func System(name string) port.Principal {
	return port.Principal{Type: port.PrincipalServiceAccount, ID: sysPrefix + strings.TrimPrefix(name, sysPrefix)}
}

// PrincipalOf は perms の主体の書き方("human" / agent 名 / "system:<名前>")から主体を作る。
func PrincipalOf(who string) port.Principal {
	switch {
	case who == humanID:
		return Human()
	case strings.HasPrefix(who, sysPrefix):
		return System(who)
	}
	return Agent(who)
}

// Who は主体を perms の主体の書き方にする。知らない主体なら空。
func Who(p port.Principal) string {
	switch p.Type {
	case port.PrincipalUser:
		if p.ID == humanID {
			return humanID
		}
	case port.PrincipalServiceAccount:
		if n, ok := strings.CutPrefix(p.ID, agentPrefix); ok && n != "" && n != humanID && !strings.HasPrefix(n, sysPrefix) { // human は予約名(人と区別できない)
			return n
		}
		if strings.HasPrefix(p.ID, sysPrefix) && len(p.ID) > len(sysPrefix) {
			return p.ID
		}
	}
	return ""
}
