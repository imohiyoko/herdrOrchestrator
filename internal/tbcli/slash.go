package tbcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// tb slash(taskboard/lib/slash.sh の cmd_slash): 許可リストにあるスラッシュコマンドを agent に送る。
// 承認待ち(L-<n>)の承認は tb agent-approve(bash の slash_approve)が行う。

var (
	reSlashName   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}(:[a-z0-9][a-z0-9_-]{0,31})?$`)
	reSlashArgs   = regexp.MustCompile(`^[A-Za-z0-9 ._:/=@,+-]{0,200}$`)
	reSlashTarget = regexp.MustCompile(`^@[A-Za-z0-9._*?+-]{1,64}$`)
)

const slashErrREDefault = `([Uu]nknown (slash )?command|[Ee]rror:|[Ff]ailed)`

// slashEntry は許可リストの 1 行(SL_ARGS / SL_POLICY / SL_CHECK / SL_OK / SL_NOTE)。
type slashEntry struct{ args, policy, check, ok, note string }

// slashFields は IFS='|' read -r n a p c o d(最後の欄に残りが入る)。
func slashFields(line string) [6]string {
	var f [6]string
	parts := strings.SplitN(line, "|", 6)
	copy(f[:], parts)
	return f
}

func slashSkip(line string) bool {
	t := config.Trim(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// slashLookup は slash_lookup: 0=見つかった 1=無い 3=書式の誤り(err に理由)。
func slashLookup(body string, has bool, cmd string) (e slashEntry, r int, why string) {
	if !has {
		return e, 1, ""
	}
	for _, line := range config.Lines(body) {
		if slashSkip(line) {
			continue
		}
		f := slashFields(line)
		n := config.Trim(f[0])
		if n != cmd {
			continue
		}
		a, p, c, o, d := config.Trim(f[1]), config.Trim(f[2]), config.Trim(f[3]), config.Trim(f[4]), config.Trim(cutHash(f[5]))
		if p == "" {
			p = "allow"
		}
		if c == "" {
			c = "busy"
		}
		why = ""
		if !reSlashName.MatchString(n) {
			why = "名前に使えない文字があります"
		}
		if !reSlashArgs.MatchString(a) {
			why = "固定の引数に使えない文字があります(英数字と空白 . _ : / = @ , + - の 200 字以内)"
		}
		if p != "allow" && p != "deny" {
			why = "方針は allow か deny: " + p
		}
		if c != "busy" && c != "screen" {
			why = "確認は busy か screen: " + c
		}
		if why != "" {
			return e, 3, why
		}
		return slashEntry{a, p, c, o, d}, 0, ""
	}
	return e, 1, ""
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
			fmt.Fprintf(w, "/%s\t%s\t%s\t%s\t%s\n", n, e.args, e.policy, e.check, e.note)
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

type slashTarget struct{ name, why string }

type slashCtx struct {
	db    *store.DB
	live  []herdr.Agent
	roles *agents.Roles
	cmd   string
	e     slashEntry
}

func (c *slashCtx) liveOf(name string) (herdr.Agent, bool) {
	for _, a := range c.live {
		if a.Name == name {
			return a, true
		}
	}
	return herdr.Agent{}, false
}

// resolve は slash_resolve: 宛先を名前と除く理由の並びにする。
func (c *slashCtx) resolve(all bool, self string, targets []string) []slashTarget {
	conductors := " " + strings.Join(message.ConductorNames(c.db.DB, c.db.Session), " ") + " "
	var names []string
	for _, a := range c.live {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	seen := " "
	var out []slashTarget
	emit := func(n string) {
		if strings.Contains(seen, " "+n+" ") {
			return
		}
		seen += n + " "
		if n == self || strings.Contains(conductors, " "+n+" ") {
			out = append(out, slashTarget{n, "skip(excluded)"})
			return
		}
		a, _ := c.liveOf(n)
		st, _ := a.AgentStatus.(string)
		if st != "idle" && st != "done" {
			if st == "" {
				st = "unknown"
			}
			out = append(out, slashTarget{n, "skip(" + st + ")"})
			return
		}
		var ck string
		_ = c.db.QueryRow(`SELECT COALESCE(kind,'claude') FROM agents WHERE name=?`, n).Scan(&ck)
		if ck == "" {
			out = append(out, slashTarget{n, "not-supported(kind unknown: not in the agents ledger)"})
			return
		}
		if !kindSupports(ck, "slash") {
			out = append(out, slashTarget{n, "not-supported(kind=" + ck + ")"})
			return
		}
		out = append(out, slashTarget{n, ""})
	}
	if all {
		for _, n := range names {
			emit(n)
		}
		return out
	}
	for _, t := range targets {
		if strings.HasPrefix(t, "@") {
			hit := false
			for _, n := range names {
				if c.roles.Match(n, t) == 0 {
					hit = true
					emit(n)
				}
			}
			if !hit {
				out = append(out, slashTarget{t, "skip(no agent with this role)"})
			}
			continue
		}
		found := false
		for _, n := range names {
			if n == t {
				found = true
				break
			}
		}
		if found {
			emit(t)
		} else {
			out = append(out, slashTarget{t, "skip(not found in herdr session '" + c.db.Session + "')"})
		}
	}
	return out
}

// screenCheck は slash_screen_check: 送った後の画面を見る。問題が無ければ空、あれば短い理由。
func screenCheck(pane, okRE string) string {
	sec := os.Getenv("TB_SLASH_SCREEN_SEC")
	if sec == "" {
		sec = "2"
	}
	if s, err := strconv.ParseFloat(sec, 64); err == nil && s > 0 {
		time.Sleep(time.Duration(s * float64(time.Second)))
	}
	lines := os.Getenv("TB_SLASH_SCREEN_LINES")
	if lines == "" {
		lines = "15"
	}
	raw, err := herdr.Call("agent", "read", pane, "--source", "recent", "--lines", lines, "--format", "text")
	if err != nil {
		return "画面を読めない"
	}
	out := strings.TrimRight(string(raw), "\n")
	errRE := os.Getenv("TB_SLASH_ERROR_RE")
	if errRE == "" {
		errRE = slashErrREDefault
	}
	ls := strings.Split(out, "\n")
	if re, err := regexp.Compile(errRE); err == nil {
		for _, l := range ls {
			if re.MatchString(l) {
				return "エラーの行: " + util.Oneline(l, 120)
			}
		}
	}
	if okRE != "" {
		re, err := regexp.Compile(okRE)
		hit := false
		if err == nil {
			for _, l := range ls {
				if re.MatchString(l) {
					hit = true
					break
				}
			}
		}
		if !hit {
			return "成功の印が画面に無い"
		}
	}
	return ""
}

// sendOne は slash_send_one: 1 行の結果と 0=送った 1=失敗 10=使用中で送らず。
// lid > 0 なら承認待ちの行(L-<lid>)に結果を書き、承認者の label を添える。
func (c *slashCtx) sendOne(name, by string, lid int64, label string) (string, int) {
	text := "/" + c.cmd
	if c.e.args != "" {
		text += " " + c.e.args
	}
	args := jsonC("command", c.cmd, "args", c.e.args)
	suffix := ""
	if lid > 0 {
		// 承認待ちの行は、承認時の許可リストで実際に送る引数に書き換える(承認待ちの間に固定の引数が変わりうる)
		_, _ = c.db.Exec(`UPDATE main.agent_ops SET args=? WHERE herdr_session=? AND id=?`, args, c.db.Session, lid)
		suffix = " / " + label + "が承認"
	}
	rec := func(rc int, result string) {
		if lid > 0 {
			recordOpID(c.db, lid, rc, result)
		} else {
			recordOp(c.db, "slash", name, args, by, rc, result)
		}
	}
	pane := name
	if a, ok := c.liveOf(name); ok && a.PaneID != "" {
		pane = a.PaneID
	}
	r := transport.Send(c.db.DB, c.db.Session, name, text, "slash:"+c.cmd+":"+name, "slash", io.Discard)
	if r == transport.Sent && c.e.check != "screen" && !transport.VerifyPrompt(name) {
		r = 1
	}
	msg := ""
	switch r {
	case 0:
		msg = "sent"
		if c.e.check == "screen" || c.e.ok != "" {
			if why := screenCheck(pane, c.e.ok); why != "" {
				r, msg = 1, "failed("+why+")"
			}
		}
	case 1:
		msg = "failed(処理の開始を確認できない。pane を見てください)"
	case 10:
		rec(1, util.Oneline(text+": 送らず(送る直前に宛先が使用中)", 200)+suffix)
		return name + " skip(busy)", 10
	default:
		msg, r = "failed(送信に失敗)", 1
	}
	rec(r, util.Oneline(text+": "+msg, 200)+suffix)
	return name + " " + msg, r
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
	if e.policy != "allow" {
		note := ""
		if e.note != "" {
			note = "(" + e.note + ")"
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
	c := &slashCtx{db: db, live: live, roles: j.Roles, cmd: cmd, e: e}
	if all {
		if !apply {
			fmt.Fprintln(w, "dry-run: --all は --apply で人の承認待ち(L-<n>)になり、承認後に次の宛先へ送ります")
			for _, t := range c.resolve(true, by, nil) {
				why := t.why
				if why == "" {
					why = "would-send(after approval)"
				}
				fmt.Fprintf(w, "%s %s\n", t.name, why)
			}
			return nil
		}
		id, err := insertPending(db, "*", jsonC("command", cmd, "args", e.args, "all", 1), by, "slash --all は常に人の承認")
		if err != nil {
			return die("agent_ops に書けません: %v", err)
		}
		fmt.Fprintf(w, "L-%d pending (/%s を --all で送る。人の承認待ち: tb agent-approve L-%d)\n", id, cmd, id)
		return &exit{20, ""}
	}
	rc := 0
	for _, t := range c.resolve(false, by, targets) {
		if t.name == "" {
			continue
		}
		if t.why != "" {
			fmt.Fprintf(w, "%s %s\n", t.name, t.why)
			continue
		}
		if by != "human" {
			if v, rule := j.Of("agent:slash:"+cmd+":"+t.name, by); v != "allow" {
				if v == "deny" {
					fmt.Fprintf(w, "%s denied(認可: %s)\n", t.name, rule)
					rc = 4
					continue
				}
				if !apply {
					fmt.Fprintf(w, "%s would-ask(approval: agent:slash:%s:%s is ask)\n", t.name, cmd, t.name)
					continue
				}
				id, err := insertPending(db, t.name, jsonC("command", cmd, "args", e.args), by, "認可: "+rule)
				if err != nil {
					return die("agent_ops に書けません: %v", err)
				}
				fmt.Fprintf(w, "%s pending L-%d (人の承認待ち: tb agent-approve L-%d)\n", t.name, id, id)
				if rc == 0 || rc == 20 {
					rc = 20
				}
				continue
			}
		}
		if !apply {
			fmt.Fprintf(w, "%s would-send(dry-run)\n", t.name)
			continue
		}
		line, r := c.sendOne(t.name, by, 0, "")
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
