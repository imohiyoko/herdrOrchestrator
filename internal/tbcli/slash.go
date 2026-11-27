package tbcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// tb slash(taskboard/lib/slash.sh の cmd_slash): 許可リストにあるスラッシュコマンドを agent に送る。
// 承認待ち(L-<n>)の承認は tb agent-approve(bash の slash_approve)が行う。

// 許可リストの読み取り・宛先の解決・送信は app/agent(承認後の送信を API でもするため。ADR 0008)。
var (
	reSlashName   = agent.ReSlashName
	reSlashTarget = agent.ReSlashTarget
)

func slashFields(line string) [6]string { return agent.SlashFields(line) }
func slashSkip(line string) bool        { return agent.SlashSkip(line) }
func slashLookup(body string, has bool, cmd string) (agent.SlashEntry, int, string) {
	return agent.SlashLookup(body, has, cmd)
}

func slashList(w io.Writer, body string, has bool) {
	if !has {
		fmt.Fprintln(w, "tb slash: 許可リストがありません(config/slash.conf)")
		return
	}
	fmt.Fprint(w, "name\targs\tpolicy\tcheck\tnote\n")
	for _, line := range config.Lines(body) {
		if slashSkip(line) {
			continue
		}
		n := config.Trim(slashFields(line)[0])
		if e, r, why := slashLookup(body, has, n); r == 0 {
			fmt.Fprintf(w, "/%s\t%s\t%s\t%s\t%s\n", n, e.Args, e.Policy, e.Check, e.Note)
		} else {
			fmt.Fprintf(w, "/%s\t-\tinvalid\t-\t%s\n", n, why)
		}
	}
}

// jsonC は jq -nc の出力(キーの順はそのまま。HTML の文字は逃がさない)。kv は キー, 値, キー, 値, ...
func jsonC(kv ...any) string {
	enc := func(v any) []byte {
		var x bytes.Buffer
		e := json.NewEncoder(&x)
		e.SetEscapeHTML(false)
		_ = e.Encode(v)
		return bytes.TrimRight(x.Bytes(), "\n")
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(enc(kv[i]))
		b.WriteByte(':')
		b.Write(enc(kv[i+1]))
	}
	b.WriteByte('}')
	return b.String()
}

func insertPending(db *store.DB, target, args, by, result string) (int64, error) {
	res, err := db.Exec(`INSERT INTO main.agent_ops (herdr_session, op, target, args, requested_by, status, result, created_at) VALUES (?, 'slash', ?, ?, ?, 'pending', ?, ?)`,
		db.Session, target, args, by, result, nowTS())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func cmdSlash(args []string, w, stderr io.Writer) error {
	cmd := ""
	apply, all, list := false, false, false
	var targets []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--list":
			if len(args)-i != 1 || cmd != "" {
				return usage("slash --list は単独で")
			}
			list = true
		case a == "--apply":
			apply = true
		case a == "--dry-run":
			apply = false
		case a == "--all":
			all = true
		case strings.HasPrefix(a, "-"):
			return usage("slash: 不明な引数 %s", a)
		case cmd == "":
			cmd = strings.TrimPrefix(a, "/")
		default:
			targets = append(targets, a)
		}
	}
	if list {
		db, _, err := openCmd(stderr)
		if err != nil {
			return err
		}
		defer db.Close()
		body, _, has := config.Lookup(db.DB, db.Session, "slash")
		slashList(w, body, has)
		return nil
	}
	if cmd == "" {
		return usage("slash <コマンド名> <agent 名|@役割>... [--all] [--apply] / slash --list")
	}
	if !reSlashName.MatchString(cmd) {
		return usage("slash: コマンド名に使えない文字があります: %s", cmd)
	}
	if all && len(targets) > 0 {
		return usage("slash: --all と宛先は一緒に指定できません")
	}
	if !all && len(targets) == 0 {
		return usage("slash: 宛先(agent 名・@役割・--all)が必要です")
	}
	for _, t := range targets {
		if !agents.NameRE.MatchString(t) && !reSlashTarget.MatchString(t) {
			return usage("slash: 宛先は agent 名か @<役割>: %s", t)
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	body, _, has := config.Lookup(db.DB, db.Session, "slash")
	e, r, why := slashLookup(body, has, cmd)
	switch r {
	case 0:
	case 3:
		return die("slash: 許可リストの /%s の行が正しくありません: %s", cmd, why)
	default:
		return die("slash: /%s は許可リスト(config/slash.conf)にありません。送りません", cmd)
	}
	if e.Policy != "allow" {
		note := ""
		if e.Note != "" {
			note = "(" + e.Note + ")"
		}
		return die("slash: /%s は許可リストで deny(既定で拒否)です。送りません%s", cmd, note)
	}
	by, err := requester()
	if err != nil {
		return err
	}
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		return die("herdr agent list に失敗しました")
	}
	j := approval.NewJudge(db.DB, db.Session)
	c := &agent.SlashCtx{DB: db, Live: live, Roles: j.Roles, Cmd: cmd, E: e}
	if all {
		if !apply {
			fmt.Fprintln(w, "dry-run: --all は --apply で人の承認待ち(L-<n>)になり、承認後に次の宛先へ送ります")
			for _, t := range c.Resolve(true, by, nil) {
				why := t.Why
				if why == "" {
					why = "would-send(after approval)"
				}
				fmt.Fprintf(w, "%s %s\n", t.Name, why)
			}
			return nil
		}
		id, err := insertPending(db, "*", jsonC("command", cmd, "args", e.Args, "all", 1), by, "slash --all は常に人の承認")
		if err != nil {
			return die("agent_ops に書けません: %v", err)
		}
		q := pendingQ(db.DB, "agent_ops", id)
		fmt.Fprintf(w, "L-%d pending %s (/%s を --all で送る。人の承認待ち: tb approval approve %s)\n", id, q, cmd, q)
		return &exit{20, ""}
	}
	rc := 0
	for _, t := range c.Resolve(false, by, targets) {
		if t.Name == "" {
			continue
		}
		if t.Why != "" {
			fmt.Fprintf(w, "%s %s\n", t.Name, t.Why)
			continue
		}
		if by != "human" {
			if v, rule := j.Of("agent:slash:"+cmd+":"+t.Name, by); v != "allow" {
				if v == "deny" {
					fmt.Fprintf(w, "%s denied(認可: %s)\n", t.Name, rule)
					rc = 4
					continue
				}
				if !apply {
					fmt.Fprintf(w, "%s would-ask(approval: agent:slash:%s:%s is ask)\n", t.Name, cmd, t.Name)
					continue
				}
				id, err := insertPending(db, t.Name, jsonC("command", cmd, "args", e.Args), by, "認可: "+rule)
				if err != nil {
					return die("agent_ops に書けません: %v", err)
				}
				q := pendingQ(db.DB, "agent_ops", id)
				fmt.Fprintf(w, "%s pending L-%d %s (承認待ち: tb approval approve %s)\n", t.Name, id, q, q)
				if rc == 0 || rc == 20 {
					rc = 20
				}
				continue
			}
		}
		if !apply {
			fmt.Fprintf(w, "%s would-send(dry-run)\n", t.Name)
			continue
		}
		line, r := c.SendOne(t.Name, by, 0, "")
		fmt.Fprintln(w, line)
		if r == 1 {
			rc = 11
		}
	}
	if rc != 0 {
		return &exit{rc, ""}
	}
	return nil
}
