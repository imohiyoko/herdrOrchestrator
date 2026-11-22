package approval

import (
	"database/sql"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// 承認の判定(taskboard/lib/approvals.sh の grant_of / org_* / approval_of)。

// orgDepth は ORG_DEPTH(上へたどる段数の上限)。
const orgDepth = 16

// Judge は 1 回の判定(役割は判定の間 1 回だけ引いて固定する。bash の role_pin)。
type Judge struct {
	DB      *sql.DB
	Session string
	Roles   *agents.Roles
}

// NewJudge は今のセッションの判定。
func NewJudge(db *sql.DB, session string) *Judge {
	return &Judge{DB: db, Session: session, Roles: agents.NewRoles(db, session)}
}

// text は config_text の本文を行に(設定が無ければ nil)。
func (j *Judge) text(key string) ([]string, bool) {
	body, _, ok := config.Lookup(j.DB, j.Session, key)
	if !ok {
		return nil, false
	}
	if body == "" {
		return nil, true
	}
	return strings.Split(body, "\n"), true
}

func trim(s string) string { return config.Trim(s) }

func cutHash(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// parentOf は grant_parent_of(spawned_by。読めなければ ok=false)。
func (j *Judge) parentOf(name string) (string, bool) {
	var p string
	err := j.DB.QueryRow(`SELECT COALESCE(spawned_by,'') FROM agents WHERE name=?`, name).Scan(&p)
	if err == sql.ErrNoRows {
		return "", true
	}
	return p, err == nil
}

// Relation は grant_relation: 0 当たる / 1 当たらない / 2 台帳を読めず確かめられない。
func (j *Judge) Relation(a, b, rel string) int {
	yes := func(c bool) int {
		if c {
			return 0
		}
		return 1
	}
	switch rel {
	case "~child":
		p, ok := j.parentOf(b)
		if !ok {
			return 2
		}
		return yes(p != "" && p == a)
	case "~parent":
		p, ok := j.parentOf(a)
		if !ok {
			return 2
		}
		return yes(p != "" && p == b)
	case "~sibling":
		p, ok := j.parentOf(a)
		if !ok {
			return 2
		}
		x, ok := j.parentOf(b)
		if !ok {
			return 2
		}
		return yes(p != "" && p == x && a != b)
	case "~descendant":
		x := b
		for i := 0; i < orgDepth; i++ {
			var ok bool
			if x, ok = j.parentOf(x); !ok {
				return 2
			}
			if x == "" {
				return 1
			}
			if x == a {
				return 0
			}
		}
		return 1
	case "~report", "~subordinate", "~manager", "~superior", "~peer":
		return j.OrgRelation(a, b, rel)
	}
	return 1
}

// ManagerOf は org_manager_of: 上司の名前(rc 0)。上司がいなければ 1、役割や親を確かめられない行に先に当たったら 2。
func (j *Judge) ManagerOf(who string) (string, int) {
	lines, ok := j.text("org")
	if !ok {
		return "", 1
	}
	for _, line := range lines {
		line = trim(cutHash(line))
		l, r, ok := strings.Cut(line, "->")
		if !ok {
			continue
		}
		l, r = trim(l), trim(r)
		if l == "" || r == "" || strings.ContainsFunc(r, config.IsSpace) {
			continue
		}
		switch j.Roles.Match(who, l) {
		case 2:
			return "", 2
		case 1:
			continue
		}
		if r == "~parent" {
			p, ok := j.parentOf(who)
			if !ok {
				return "", 2
			}
			if p == "" {
				continue
			}
			r = p
		}
		if r == who {
			continue
		}
		return r, 0
	}
	return "", 1
}

// Chain は org_chain: 上司を上へ順に(循環したら止める)。役割を確かめられなければ ok=false。
func (j *Judge) Chain(who string) ([]string, bool) {
	seen := map[string]bool{who: true}
	var out []string
	x := who
	for i := 0; i < orgDepth; i++ {
		m, rc := j.ManagerOf(x)
		if rc != 0 {
			return out, rc != 2
		}
		if seen[m] {
			return out, true
		}
		seen[m] = true
		out = append(out, m)
		x = m
	}
	return out, true
}

// OrgRelation は org_relation: 0 当たる / 1 当たらない / 2 確かめられない。
func (j *Judge) OrgRelation(a, b, rel string) int {
	if a == b {
		return 1
	}
	yes := func(c bool) int {
		if c {
			return 0
		}
		return 1
	}
	in := func(xs []string, s string) bool {
		for _, x := range xs {
			if x == s {
				return true
			}
		}
		return false
	}
	switch rel {
	case "~report":
		x, rc := j.ManagerOf(b)
		if rc != 0 {
			return rc
		}
		return yes(x == a)
	case "~manager":
		x, rc := j.ManagerOf(a)
		if rc != 0 {
			return rc
		}
		return yes(x == b)
	case "~subordinate":
		c, ok := j.Chain(b)
		if !ok {
			return 2
		}
		return yes(in(c, a))
	case "~superior":
		c, ok := j.Chain(a)
		if !ok {
			return 2
		}
		return yes(in(c, b))
	case "~peer":
		x, rc := j.ManagerOf(a)
		if rc != 0 {
			return rc
		}
		y, rc := j.ManagerOf(b)
		if rc == 2 {
			return 2
		}
		if rc != 0 {
			return 1
		}
		return yes(x == y)
	}
	return 1
}

// GrantOf は grant_of: grants.conf の行に当たれば、その行の書き方("grants.conf: <主体> <操作> <対象>")と true。
func (j *Judge) GrantOf(who, op, target string) (string, bool) {
	if who == "" || who == "human" {
		return "", false
	}
	lines, ok := j.text("grants")
	if !ok {
		return "", false
	}
	for _, line := range lines {
		f := strings.FieldsFunc(trim(cutHash(line)), func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }) // read の既定の IFS
		if len(f) != 3 {
			continue
		}
		s, o, t := f[0], f[1], f[2]
		if !util.Glob(o, op) || j.Roles.Match(who, s) != 0 {
			continue
		}
		if strings.HasPrefix(t, "~") {
			if j.Relation(who, target, t) != 0 {
				continue
			}
		} else if j.Roles.Match(target, t) != 0 {
			continue
		}
		return "grants.conf: " + s + " " + o + " " + t, true
	}
	return "", false
}

// lastPart / initPart は ${x##*:} / ${x%:*}。
func lastPart(s string) string {
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}
func initPart(s string) string {
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return s[:i]
	}
	return s
}

// Of は approval_of: 値(ask|allow)と根拠の行。by(実行する agent)を渡すと先に grants.conf を見る(操作の末尾を対象とみなす)。
func (j *Judge) Of(act, by string) (value, rule string) {
	if by != "" && strings.Contains(act, ":") {
		if r, ok := j.GrantOf(by, initPart(act), lastPart(act)); ok {
			return "allow", r
		}
	}
	lines, ok := j.text("approvals")
	if ok {
		for _, line := range lines {
			line = trim(cutHash(line))
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = trim(k), trim(v)
			if v != "ask" && v != "allow" {
				continue
			}
			if strings.HasPrefix(lastPart(k), "@") {
				if !strings.Contains(act, ":") || !util.Glob(initPart(k), initPart(act)) {
					continue
				}
				switch j.Roles.Match(lastPart(act), lastPart(k)) {
				case 0:
					return v, k + " = " + v
				case 2:
					return "ask", k + "(台帳を読めず役割を確かめられないため ask)"
				}
				continue
			}
			if util.Glob(k, act) {
				return v, k + " = " + v
			}
		}
	}
	return "ask", "(一致なし: 既定 ask)"
}
