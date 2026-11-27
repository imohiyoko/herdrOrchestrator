package agent

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// スラッシュコマンドを agent に送る部品(tb slash と、その承認待ち L-<n> の承認後。ADR 0008)。

var (
	ReSlashName   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}(:[a-z0-9][a-z0-9_-]{0,31})?$`)
	reSlashArgs   = regexp.MustCompile(`^[A-Za-z0-9 ._:/=@,+-]{0,200}$`)
	ReSlashTarget = regexp.MustCompile(`^@[A-Za-z0-9._*?+-]{1,64}$`)
)

const slashErrREDefault = `([Uu]nknown (slash )?command|[Ee]rror:|[Ff]ailed)`

// SlashEntry は許可リストの 1 行(SL_ARGS / SL_POLICY / SL_CHECK / SL_OK / SL_NOTE)。
type SlashEntry struct{ Args, Policy, Check, OK, Note string }

// slashFields は IFS='|' read -r n a p c o d(最後の欄に残りが入る)。
func SlashFields(line string) [6]string {
	var f [6]string
	parts := strings.SplitN(line, "|", 6)
	copy(f[:], parts)
	return f
}

func SlashSkip(line string) bool {
	t := config.Trim(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// SlashLookup は許可リスト(設定 slash)から 1 行を引く: 0=見つかった 1=無い 3=書式の誤り(err に理由)。
func SlashLookup(body string, has bool, cmd string) (e SlashEntry, r int, why string) {
	if !has {
		return e, 1, ""
	}
	for _, line := range config.Lines(body) {
		if SlashSkip(line) {
			continue
		}
		f := SlashFields(line)
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
		if !ReSlashName.MatchString(n) {
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
		return SlashEntry{a, p, c, o, d}, 0, ""
	}
	return e, 1, ""
}

// SlashTarget は宛先 1 つ(Why が空なら送る。空でなければ送らない理由)。
type SlashTarget struct{ Name, Why string }

// SlashCtx はスラッシュコマンドを送る文脈(DB・herdr の一覧・役割・コマンド・許可リストの行)。
type SlashCtx struct {
	DB    *store.DB
	Live  []herdr.Agent
	Roles *agents.Roles
	Cmd   string
	E     SlashEntry
}

func (c *SlashCtx) LiveOf(name string) (herdr.Agent, bool) {
	for _, a := range c.Live {
		if a.Name == name {
			return a, true
		}
	}
	return herdr.Agent{}, false
}

// Resolve は 宛先を名前と除く理由の並びにする。
func (c *SlashCtx) Resolve(all bool, self string, targets []string) []SlashTarget {
	conductors := " " + strings.Join(message.ConductorNames(c.DB.DB, c.DB.Session), " ") + " "
	var names []string
	for _, a := range c.Live {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	seen := " "
	var out []SlashTarget
	emit := func(n string) {
		if strings.Contains(seen, " "+n+" ") {
			return
		}
		seen += n + " "
		if n == self || strings.Contains(conductors, " "+n+" ") {
			out = append(out, SlashTarget{n, "skip(excluded)"})
			return
		}
		a, _ := c.LiveOf(n)
		st, _ := a.AgentStatus.(string)
		if st != "idle" && st != "done" {
			if st == "" {
				st = "unknown"
			}
			out = append(out, SlashTarget{n, "skip(" + st + ")"})
			return
		}
		var ck string
		_ = c.DB.QueryRow(`SELECT COALESCE(kind,'claude') FROM agents WHERE name=?`, n).Scan(&ck)
		if ck == "" {
			out = append(out, SlashTarget{n, "not-supported(kind unknown: not in the agents ledger)"})
			return
		}
		if !KindSupports(ck, "slash") {
			out = append(out, SlashTarget{n, "not-supported(kind=" + ck + ")"})
			return
		}
		out = append(out, SlashTarget{n, ""})
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
				if c.Roles.Match(n, t) == 0 {
					hit = true
					emit(n)
				}
			}
			if !hit {
				out = append(out, SlashTarget{t, "skip(no agent with this role)"})
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
			out = append(out, SlashTarget{t, "skip(not found in herdr session '" + c.DB.Session + "')"})
		}
	}
	return out
}

// ScreenCheck は 送った後の画面を見る。問題が無ければ空、あれば短い理由。
func ScreenCheck(pane, okRE string) string {
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

// SendOne は 1 行の結果と 0=送った 1=失敗 10=使用中で送らず。
// lid > 0 なら承認待ちの行(L-<lid>)に結果を書き、承認者の label を添える。
func (c *SlashCtx) SendOne(name, by string, lid int64, label string) (string, int) {
	text := "/" + c.Cmd
	if c.E.Args != "" {
		text += " " + c.E.Args
	}
	args := jsonArgs("command", c.Cmd, "args", c.E.Args)
	suffix := ""
	if lid > 0 {
		// 承認待ちの行は、承認時の許可リストで実際に送る引数に書き換える(承認待ちの間に固定の引数が変わりうる)
		_, _ = c.DB.Exec(`UPDATE main.agent_ops SET args=? WHERE herdr_session=? AND id=?`, args, c.DB.Session, lid)
		suffix = " / " + label + "が承認"
	}
	rec := func(rc int, result string) {
		if lid > 0 {
			RecordOpID(c.DB, nowTS(), lid, rc, result)
		} else {
			agents.RecordOp(c.DB.DB, c.DB.Session, "slash", name, args, by, rc, result)
		}
	}
	pane := name
	if a, ok := c.LiveOf(name); ok && a.PaneID != "" {
		pane = a.PaneID
	}
	r := transport.Send(c.DB.DB, c.DB.Session, name, text, "slash:"+c.Cmd+":"+name, "slash", io.Discard)
	if r == transport.Sent && c.E.Check != "screen" && !transport.VerifyPrompt(name) {
		r = 1
	}
	msg := ""
	switch r {
	case 0:
		msg = "sent"
		if c.E.Check == "screen" || c.E.OK != "" {
			if why := ScreenCheck(pane, c.E.OK); why != "" {
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

func cutHash(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// SlashApproved は承認されたスラッシュコマンドの承認待ち o を送る(承認の印は呼ぶ側が取ったあと)。
// 承認待ちの間に許可リストが変わっていたら今の行に従う。--all(宛先 *)は送った宛先ごとの行を別に残し、w に 1 行ずつ書く。
// 戻り値は (要約の 1 行, 0=送った 1=送らなかった・失敗 11=届けられなかった)。結果は L-<n> の行に残す。
func SlashApproved(db *store.DB, o OpRow, label string, w io.Writer) (string, int) {
	id, cmd := o.ID, o.Parsed().Command
	body, _, has := config.Lookup(db.DB, db.Session, "slash")
	e, r, _ := SlashLookup(body, has, cmd)
	if r != 0 || e.Policy != "allow" {
		RecordOpID(db, nowTS(), id, 1, "/"+cmd+" は許可リストに無いか deny になったため送らない / "+label+"が承認")
		return fmt.Sprintf("L-%d: /%s は許可リストに無いか deny です。送りません", id, cmd), 1
	}
	live, err := agents.SyncLive(db.DB, db.Session)
	if err != nil {
		RecordOpID(db, nowTS(), id, 1, "herdr agent list に失敗したため送らない / "+label+"が承認")
		return fmt.Sprintf("L-%d: herdr agent list に失敗しました", id), 11
	}
	c := &SlashCtx{DB: db, Live: live, Roles: agents.NewRoles(db.DB, db.Session), Cmd: cmd, E: e}
	if o.Target == "*" {
		rc, n := 0, 0
		for _, t := range c.Resolve(true, o.By, nil) {
			if t.Name == "" {
				continue
			}
			if t.Why != "" {
				fmt.Fprintln(w, t.Name+" "+t.Why)
				continue
			}
			line, r := c.SendOne(t.Name, o.By, 0, "")
			fmt.Fprintln(w, line)
			if r == 0 {
				n++
			}
			if r == 1 {
				rc = 11
			}
		}
		res := 0
		if rc != 0 {
			res = 1
		}
		sum := fmt.Sprintf("/%s を --all で %d 体に送った / 人が承認", cmd, n)
		RecordOpID(db, nowTS(), id, res, sum)
		return fmt.Sprintf("L-%d: %s", id, sum), rc
	}
	out, rr := "", 0
	for _, t := range c.Resolve(false, o.By, []string{o.Target}) {
		if t.Name == "" {
			continue
		}
		if t.Why != "" {
			RecordOpID(db, nowTS(), id, 1, t.Why+" / "+label+"が承認")
			return fmt.Sprintf("L-%d: %s %s", id, t.Name, t.Why), 1
		}
		out, rr = c.SendOne(t.Name, o.By, id, label)
	}
	if rr != 0 {
		rr = 11
	}
	return fmt.Sprintf("L-%d: %s", id, out), rr
}
