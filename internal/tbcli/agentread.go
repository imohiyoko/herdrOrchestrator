package tbcli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// 読むだけの agent のコマンド(taskboard/lib/agents.sh の cmd_peek / cmd_org / cmd_can)。

// requester は requester: 叩いた agent の名前(pane から herdr で引く。自己申告にしない)。人の端末なら human。
func requester() (string, error) {
	n := ""
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		var err error
		if n, err = identity.PaneOwnerName(pane); err != nil {
			return "", die("herdr agent list に失敗したため、実行者を特定できません")
		}
	}
	if n == "human" {
		return "", die("agent 名 human は予約名です(人と区別できないため、この agent からの操作は受けません)")
	}
	if n == "" {
		n = "human"
	}
	return n, nil
}

// validAgentName は valid_agent_name。
func validAgentName(n string) error {
	if n == "human" || !agents.NameRE.MatchString(n) {
		return usage("agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内(human は予約名): %s", n)
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// cmdCan は tb can [<主体>] <操作> <対象>: 実際の判定と同じ規則で allow|ask|hold|deny を 1 つに決めて出す(読むだけ)。
// 終了コード 0=allow 20=ask 21=hold 4=deny。
func cmdCan(args []string, w, stderr io.Writer) error {
	if len(args) != 2 && len(args) != 3 {
		return usage("can [<主体>] <操作> <対象>(例: tb can w1 send:task lead1、主体を省くと自分: tb can agent:stop w1)")
	}
	if len(args) == 2 {
		by, err := requester()
		if err != nil {
			return err
		}
		args = append([]string{by}, args...)
	}
	who, op, target := args[0], args[1], args[2]
	if who != "human" {
		if err := validAgentName(who); err != nil {
			return err
		}
	}
	if err := validAgentName(target); err != nil {
		return err
	}
	if err := validAction(op); err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	j := approval.NewJudge(db.DB, db.Session)
	verdict := "allow"
	out := func(rules ...string) error {
		fmt.Fprintln(w, verdict)
		for _, r := range rules {
			fmt.Fprintf(w, "rule: %s\n", r)
		}
		switch verdict {
		case "allow":
			return nil
		case "ask":
			return &exit{exitAsk, ""}
		case "hold":
			return &exit{21, ""}
		}
		return &exit{4, ""}
	}
	if op == "role:change" && j.Roles.Exists(target) {
		verdict = "deny"
		return out("役割 " + target + " の定義があるため、台帳のロールは効かない(tb role-set は止まる)")
	}
	if who == "human" {
		if strings.HasPrefix(op, "send:") {
			verdict = "deny"
			return out("tb send は agent の pane から使う(人の端末には送信者がいない。人は herdr で直接話す)")
		}
		if op == "agent:stop" {
			if p, ok := j.Roles.ProtectReason(target); ok {
				verdict = "deny"
				return out("protect: " + p + "(人が設定 protect から外すまで止められない)")
			}
		}
		return out("人の端末からはそのまま実行できる")
	}
	if who == target && (strings.HasPrefix(op, "send:") || op == "role:change") {
		verdict = "deny"
		return out("自分自身には使えない")
	}
	if who == target && op == "agent:read" {
		return out("本人はそのまま読める(tb peek)")
	}
	switch {
	case op == "send:task" || op == "send:info" || op == "send:answer":
		d := message.Mode(j, who, target, op[len("send:"):], false)
		if d.Mode == "block" {
			verdict = "deny"
			return out(d.Rule)
		}
		v, rule := j.Of(op+":"+target, who)
		if v != "allow" {
			verdict = v
		} else if d.Mode == "hold" {
			verdict = "hold"
		}
		r := d.Rule
		if d.Mode == "hold" {
			r += "(届けず宛先の inbox に置く)"
		}
		return out(r, "認可: "+rule)
	case op == "agent:stop":
		if p, ok := j.Roles.ProtectReason(target); ok {
			verdict = "deny"
			return out("protect: " + p)
		}
		v, rule := j.Of(op+":"+target, who)
		verdict = v
		return out(rule)
	case op == "agent:flag" || op == "role:change":
		if r, ok := j.GrantOf(who, op, target); ok {
			return out(r)
		}
		if op == "agent:flag" {
			verdict = "deny"
			return out("policy に agent:flag を許す行がない")
		}
		v, rule := j.Of("role:change", who)
		verdict = v
		return out(rule)
	case strings.HasPrefix(op, "approve:"):
		if who == target {
			verdict = "deny"
			return out("自分の依頼は承認できない")
		}
		if r, ok := j.GrantOf(who, op, target); ok {
			return out(r + "(--always・--all 系・spawn-freeze 中は人だけ)")
		}
		verdict = "deny"
		return out("policy に " + op + " " + target + " を許す approve の行がない(承認は人だけ)")
	}
	v, rule := j.Of(op+":"+target, who)
	verdict = v
	return out(rule)
}

// cmdOrg は tb org [<agent 名> | --me] [--json]: 台帳の agent と設定 org の上司を木で出す(読むだけ)。
func cmdOrg(args []string, w, stderr io.Writer) error {
	json, who := false, ""
	for _, a := range args {
		switch {
		case a == "--json":
			json = true
		case a == "--me":
			if who != "" {
				return usage("org: agent 名か --me のどちらか 1 つ")
			}
			by, err := requester()
			if err != nil {
				return err
			}
			if by == "human" {
				return usage("org: --me は agent の pane から使う(人の端末には上司がいない。tb org <agent 名> で見る)")
			}
			who = by
		case strings.HasPrefix(a, "-"):
			return usage("org: 不明な引数 %s", a)
		default:
			if who != "" {
				return usage("org: agent 名は 1 つだけ")
			}
			who = a
		}
	}
	if who != "" {
		if err := validAgentName(who); err != nil {
			return err
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var names []string
	st := map[string]string{}
	if err := orgLedger(db, &names, st); err != nil {
		return err
	}
	if who != "" {
		if _, ok := st[who]; !ok {
			names = append(names, who)
			st[who] = "-"
		}
	}
	j := approval.NewJudge(db.DB, db.Session)
	_, src, has := config.Lookup(db.DB, db.Session, "org")
	mgr := map[string]string{}
	if has {
		for _, n := range names {
			switch m, rc := j.ManagerOf(n); rc {
			case 0:
				mgr[n] = m
			case 2:
				mgr[n] = "?"
			}
		}
	}
	// 上司として書かれているが台帳にいない名前も出す(上司の上司もたどる)
	for added := true; added; {
		added = false
		for _, n := range append([]string(nil), names...) {
			m := mgr[n]
			if m != "" && m != "?" {
				if _, ok := st[m]; !ok {
					names = append(names, m)
					st[m] = "-"
					added = true
					if x, rc := j.ManagerOf(m); rc == 0 {
						mgr[m] = x
					}
				}
			}
		}
	}
	if who != "" {
		chain, ok := j.Chain(who)
		if !ok {
			chain = []string{"?"}
		}
		var reports, peers []string
		for _, x := range names {
			if x == who {
				continue
			}
			if mgr[x] == who {
				reports = append(reports, x)
			}
			if m := mgr[who]; m != "" && m != "?" && mgr[x] == m {
				peers = append(peers, x)
			}
		}
		if json {
			var m any
			if mgr[who] != "" {
				m = mgr[who]
			}
			fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "name", V: who}, {K: "state", V: st[who]}, {K: "manager", V: m},
				{K: "superiors", V: strs(chain)}, {K: "reports", V: strs(reports)}, {K: "peers", V: strs(peers)}}))
			return nil
		}
		dash := func(xs []string) string {
			if len(xs) == 0 {
				return "-"
			}
			return strings.Join(xs, " ")
		}
		fmt.Fprintf(w, "agent: %s\n上司: %s\n上の全員: %s\n直属の部下: %s\n同じ上司: %s\n", who, dash(nonEmpty(mgr[who])),
			dash(chain), dash(reports), dash(peers))
		return nil
	}
	if json {
		if !has {
			src = "none"
		}
		as := make([]any, 0, len(names))
		for _, n := range names {
			var m any
			if mgr[n] != "" {
				m = mgr[n]
			}
			as = append(as, jv.Obj{{K: "name", V: n}, {K: "state", V: st[n]}, {K: "manager", V: m}})
		}
		fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "source", V: src}, {K: "agents", V: as}}))
		return nil
	}
	if !has {
		fmt.Fprintln(w, "組織図は未設定です(設定 org。人が tb config set org で書く。例: setup/examples/org.conf)。誰にも上司はいません")
		return nil
	}
	fmt.Fprintf(w, "組織図(設定 org: %s)\n", src)
	if len(names) == 0 {
		fmt.Fprintln(w, "(台帳に agent がいません)")
		return nil
	}
	names = sortUniq(names)
	seen := map[string]bool{}
	var print func(n, ind string)
	print = func(n, ind string) {
		seen[n] = true
		tag := ""
		switch st[n] {
		case "stopped":
			tag = "  (停止中)"
		case "-":
			tag = "  (台帳にいない)"
		}
		fmt.Fprintf(w, "%s%s%s\n", ind, n, tag)
		for _, c := range names {
			if mgr[c] == n && !seen[c] {
				print(c, ind+"  ")
			}
		}
	}
	for _, n := range names {
		if m := mgr[n]; (m == "" || m == "?") && !seen[n] {
			print(n, "")
		}
	}
	var unk, rest []string
	for _, n := range names {
		if mgr[n] == "?" {
			unk = append(unk, n)
		}
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	if len(unk) > 0 {
		fmt.Fprintf(w, "役割を確かめられず上司が決まらない: %s\n", strings.Join(unk, " "))
	}
	if len(rest) > 0 {
		fmt.Fprintf(w, "循環していて木にできない: %s\n", strings.Join(rest, " "))
	}
	return nil
}

func orgLedger(db *store.DB, names *[]string, st map[string]string) error {
	rows, err := db.Query(`SELECT name, COALESCE(state,'') FROM agents ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			return err
		}
		if n != "" {
			*names = append(*names, n)
			st[n] = s
		}
	}
	return rows.Err()
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func strs(xs []string) []any {
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		out = append(out, x)
	}
	return out
}

// sortUniq は sort -u(バイト順)。
func sortUniq(xs []string) []string {
	ys := append([]string(nil), xs...)
	sort.Strings(ys)
	out := ys[:0]
	for i, y := range ys {
		if i == 0 || y != ys[i-1] {
			out = append(out, y)
		}
	}
	return out
}

var rePeekLines = regexp.MustCompile(`^[0-9]{1,3}$`)

// cmdPeek は tb peek <agent 名> [--lines N]: agent の pane の直近の出力を読む。人と本人はそのまま、ほかの agent は
// approvals の agent:read:<対象> が allow のときだけ(終了コード 20)。読んだ記録は read_log。
func cmdPeek(args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("peek <agent 名> [--lines N]")
	}
	name, lines := args[0], 40
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--lines":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			v := args[i+1]
			n, _ := strconv.Atoi(v)
			if !rePeekLines.MatchString(v) || n < 1 || n > 200 {
				return usage("peek: --lines は 1〜200")
			}
			lines = n
			i++
		default:
			return usage("peek: 不明な引数 %s", args[i])
		}
	}
	if err := validAgentName(name); err != nil {
		return err
	}
	by, err := requester()
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	logRead := func(status, rule string) {
		var r any
		if rule != "" || status == "denied" {
			r = rule
		}
		_, _ = db.Exec(`INSERT INTO main.read_log (herdr_session, reader, target, lines, status, rule, created_at) VALUES (?,?,?,?,?,?,?)`,
			db.Session, by, name, lines, status, r, nowTS())
	}
	rule := ""
	if by != "human" && by != name {
		j := approval.NewJudge(db.DB, db.Session)
		var v string
		v, rule = j.Of("agent:read:"+name, by)
		if v != "allow" {
			logRead("denied", rule)
			fmt.Fprintf(stderr, "tb: %s は %s の中身を読めません(%s)。必要なら人が policy に '<主体> agent:read %s = allow'(対象は @<役割> や ~child なども書ける)を足す\n", by, name, rule, name)
			if v == "deny" {
				return &exit{4, ""}
			}
			return &exit{exitAsk, ""}
		}
	}
	as, err := herdr.Agents()
	if err != nil {
		logRead("failed", rule)
		return die("herdr agent list に失敗しました")
	}
	pane := ""
	for _, a := range as {
		if a.Name == name {
			pane = a.PaneID
			break
		}
	}
	if pane == "" {
		logRead("failed", rule)
		return die("%s は herdr で動いていません", name)
	}
	out, err := herdr.Call("agent", "read", pane, "--source", "recent", "--lines", strconv.Itoa(lines), "--format", "text")
	if err != nil {
		logRead("failed", rule)
		return die("herdr agent read に失敗しました(%s)", name)
	}
	logRead("ok", rule)
	fmt.Fprintf(w, "%s\n", strings.TrimRight(string(out), "\n"))
	return nil
}
