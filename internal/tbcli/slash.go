package tbcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

// tb slash(taskboard/lib/slash.sh の cmd_slash): 許可リストにあるスラッシュコマンドを agent に送る。
// 承認待ち(L-<n>)の承認は tb agent-approve(bash の slash_approve)が行う。

// 許可リストの読み取り・宛先の解決・送信は app/agent(承認後の送信を API でもするため。ADR 0008)。
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
	in := agent.SlashReq{Command: cmd, Targets: targets, All: all, Apply: apply}
	if err := agent.ValidSlash(in); err != nil {
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
	var r agent.SlashResult
	if err := callAppEnv(appEnv(db, stderr), by, "agent.slash", in, &r); err != nil {
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
