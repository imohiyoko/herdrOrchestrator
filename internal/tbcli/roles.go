package tbcli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
)

// 役割の配布と定義(taskboard/lib/roles.sh の role-context / role-broadcast / role-show / role-list / role-write / role-edit /
// role-unset / role-set / role-log / roles)。

var (
	reRoleSrc = regexp.MustCompile(`^(startup|resume|clear|compact)$`)
	reRoleSID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// paneAgent は agent_name_by_pane(無ければ空。herdr に聞けなければ err)。
func paneAgent(pane string) (string, error) { return identity.PaneOwnerName(pane) }

// liveAgent は roles / role-broadcast が読む herdr agent list の 1 件。
type liveAgent struct{ pane, name, sess string }

func liveAgents() ([]liveAgent, error) {
	as, err := herdr.Agents()
	if err != nil {
		return nil, die("herdr agent list に失敗しました")
	}
	clean := strings.NewReplacer("\x1f", " ", "\n", " ")
	var out []liveAgent
	for _, a := range as {
		out = append(out, liveAgent{clean.Replace(a.PaneID), clean.Replace(a.Name), clean.Replace(a.AgentSession.Value)})
	}
	return out, nil
}

// cmdRoleContext は tb role-context --source S --session-id ID: SessionStart hook から呼ぶ。常に終了コード 0。
// 渡すときだけ stdout に hook の JSON を出す。
func cmdRoleContext(args []string, w, stderr io.Writer) error {
	src, sid := "", ""
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return nil
		}
		switch args[i] {
		case "--source":
			src = args[i+1]
		case "--session-id":
			sid = args[i+1]
		default:
			return nil
		}
	}
	pane := os.Getenv("HERDR_PANE_ID")
	if !reRoleSrc.MatchString(src) || !reRoleSID.MatchString(sid) || pane == "" {
		return nil
	}
	name, _ := paneAgent(pane)
	if name == "" {
		return nil
	}
	db, err := openDB()
	if err != nil {
		return nil
	}
	defer db.Close()
	ro, ok := agents.NewRoles(db.DB, db.Session).LoadRole(name)
	if !ok {
		return nil
	}
	if h, _ := agents.LastBriefedHash(db.DB, sid); src == "resume" && h == ro.Hash { // 同じ会話の再開で同じ版は渡し済み
		return nil
	}
	_ = agents.RecordBriefing(db.DB, db.Session, sid, name, ro.Label, ro.Hash, src, "hook")
	fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "hookSpecificOutput", V: jv.Obj{{K: "hookEventName", V: "SessionStart"}, {K: "additionalContext", V: ro.Text}}}}))
	return nil
}

// cmdRoleBroadcast は tb role-broadcast [<agent 名>...]: 動いている agent に役割の更新を知らせる(同じ版を渡し済みなら飛ばす)。
// 終了コード 0 / 10 使用中で送れなかった agent がある / 11 送信失敗がある。
func cmdRoleBroadcast(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	live, err := liveAgents()
	if err != nil {
		return err
	}
	only := " " + strings.Join(args, " ") + " "
	rc := 0
	for _, a := range live {
		if a.name == "" || (len(args) > 0 && !strings.Contains(only, " "+a.name+" ")) {
			continue
		}
		ro, ok := agents.NewRoles(db.DB, db.Session).LoadRole(a.name)
		if !ok {
			fmt.Fprintf(w, "skip %s (役割ファイルなし)\n", a.name)
			continue
		}
		if a.sess != "" {
			if h, _ := agents.LastBriefedHash(db.DB, a.sess); h == ro.Hash {
				fmt.Fprintf(w, "skip %s (渡し済み %s)\n", a.name, ro.Hash[:12])
				continue
			}
		}
		msg := fmt.Sprintf("[role-update / tb] あなた(%s)の役割が更新されました(版 %s、%s)。tb role-show で読み直し、以後はこれに従ってください", a.name, ro.Hash[:12], ro.Label)
		if r := transport.Send(db.DB, db.Session, a.name, msg, "role:"+a.name, "role", io.Discard); r != transport.Sent {
			fmt.Fprintf(w, "pending %s (宛先が使用中か送信失敗。あとで再実行)\n", a.name)
			if r > rc {
				rc = r
			}
			continue
		}
		if a.sess != "" {
			_ = agents.RecordBriefing(db.DB, db.Session, a.sess, a.name, ro.Label, ro.Hash, "broadcast", "broadcast")
		}
		fmt.Fprintf(w, "sent %s (%s)\n", a.name, ro.Hash[:12])
	}
	if rc != 0 {
		return &exit{rc, ""}
	}
	return nil
}

// cmdRoleShow は tb role-show [<agent 名>]: その agent に渡す役割の本文(省略時は自分の pane の agent)。
func cmdRoleShow(args []string, w, stderr io.Writer) error {
	if len(args) > 1 {
		return usage("role-show [<agent 名>]")
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	if name == "" {
		pane := os.Getenv("HERDR_PANE_ID")
		if pane == "" {
			return usage("role-show <agent 名>(agent の pane では省略できる)")
		}
		var err error
		if name, err = paneAgent(pane); err != nil {
			return &exit{exitErr, ""} // bash は name=$(...) の失敗で set -e のまま黙って終わる
		}
		if name == "" {
			return die("この pane の agent が分かりません")
		}
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	ro, ok := agents.NewRoles(db.DB, db.Session).LoadRole(name)
	if !ok {
		return die("%s に渡す役割がありません", name)
	}
	fmt.Fprintln(w, ro.Text)
	return nil
}

// cmdRoleList は tb role-list: 役割名と出どころ。_common と map も出す。
func cmdRoleList(args []string, w, stderr io.Writer) error {
	if len(args) != 0 {
		return usage("role-list")
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	r := agents.NewRoles(db.DB, db.Session)
	for _, n := range append([]string{"_common", "@map"}, r.List()...) {
		if _, src, ok := r.LookupSrc(n); ok {
			fmt.Fprintf(w, "%s\t%s\n", n, src)
		}
	}
	return nil
}

// roleWriteArgs は role_write_args: 役割名と --shared。
func roleWriteArgs(args []string) (name string, shared bool, err error) {
	for _, a := range args {
		switch {
		case a == "--shared":
			shared = true
		case strings.HasPrefix(a, "-"):
			return "", false, usage("不明な引数: %s", a)
		default:
			if name != "" {
				return "", false, usage("役割名は 1 つ")
			}
			name = a
		}
	}
	if name == "" {
		return "", false, usage("role-write|role-edit <役割名|@map> [--shared]")
	}
	if _, ok := agents.KeyOf(name); !ok {
		return "", false, usage("役割名は英数字・_ で始まり、英数字と . _ - の 64 字以内(または @map): %s", name)
	}
	return name, shared, nil
}

// requesterPrincipal は requester の呼び手(pane の中なら herdr から引いた agent、外なら人。役割・レシピの取り消し)。
func requesterPrincipal() (port.Principal, error) {
	by, err := requester()
	if err != nil {
		return port.Principal{}, err
	}
	if by == "human" {
		return app.Human(), nil
	}
	return app.Agent(by), nil
}

// rolePut は role_put: 役割の本文を書く(agent.role.write。人か、policy で role:change を許された agent)。
func rolePut(db *store.DB, name, body, how string, shared bool, w, stderr io.Writer) error {
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	var r agent.RoleResult
	if err := callAppEnv(appEnv(db, stderr), by, "agent."+strings.ReplaceAll(how, "-", "."), agent.RoleWriteReq{Name: name, Body: body, Shared: shared}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	fmt.Fprintf(w, "%s: %s。動いている agent に配るには: tb role-broadcast\n", name, r.Src)
	return nil
}

// cmdRoleWrite は tb role-write <役割名|@map> [--shared] < 本文。
func cmdRoleWrite(args []string, w, stderr io.Writer) error {
	name, shared, err := roleWriteArgs(args)
	if err != nil {
		return err
	}
	if stdinIsTTY() {
		return usage("role-write <役割名>: 本文は標準入力から渡す")
	}
	body, err := readStdin()
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	return rolePut(db, name, body, "role-write", shared, w, stderr)
}

// cmdRoleEdit は tb role-edit <役割名|@map> [--shared]: エディタで書き換える。
func cmdRoleEdit(args []string, w, stderr io.Writer) error {
	name, shared, err := roleWriteArgs(args)
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	cur, ok := agents.NewRoles(db.DB, db.Session).Lookup(name)
	body, err := editBody(cur, ok)
	if err != nil {
		return err
	}
	return rolePut(db, name, body, "role-edit", shared, w, stderr)
}

// cmdRoleUnset は tb role-unset <役割名|@map> [--shared]: DB の行を消す(人だけ)。
func cmdRoleUnset(args []string, w, stderr io.Writer) error {
	name, shared, err := roleWriteArgs(args)
	if err != nil {
		return err
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireHuman("tb role-unset"); err != nil {
		return err
	}
	by, err := requesterPrincipal()
	if err != nil {
		return err
	}
	var r agent.RoleResult
	if err := callAppEnv(appEnv(db, stderr), by, "agent.role.unset", agent.RoleUnsetReq{Name: name, Shared: shared}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	if r.Found {
		fmt.Fprintf(w, "%s: %s\n", name, r.Src)
	} else {
		fmt.Fprintf(w, "%s: なし\n", name)
	}
	return nil
}

// cmdRoleSet は tb role-set <agent 名> <ロール[,ロール...]> [--reason R]: 台帳のロールを変える(権限の変更でもある)。
func cmdRoleSet(args []string, w, stderr io.Writer) error {
	if len(args) < 2 {
		return usage("role-set <agent 名> <ロール[,ロール...]> [--reason R]")
	}
	name, role, why := args[0], args[1], ""
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--reason":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", args[i])
			}
			why = args[i+1]
			i++
		default:
			return usage("role-set: 不明な引数 %s", args[i])
		}
	}
	if !agents.NameRE.MatchString(name) {
		return usage("agent 名は英小文字で始まり、英小文字・数字・_・- の 32 字以内: %s", name)
	}
	if _, err := agent.RoleItems(role); err != nil {
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
	var r agent.RoleSetResult
	if err := callAppEnv(appEnv(db, stderr), by, "agent.role.set", agent.RoleSetReq{Name: name, Role: role, Reason: why}, &r); err != nil {
		return err
	}
	if r.Pending != nil {
		return pendingExit(w, r.Pending)
	}
	old := r.Old
	if old == "" {
		old = "-"
	}
	fmt.Fprintf(w, "%s role %s -> %s (by %s)。本人に配るには: tb role-broadcast %s\n", name, old, r.New, r.By, name)
	return nil
}

// cmdRoleLog は tb role-log [--json]: ロール変更の記録(このセッションのもの)。
func cmdRoleLog(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var r agent.OpsResult
	if err := callAppW(db, app.Human(), io.Discard, "agent.role.log", agent.RoleLogReq{JSON: len(args) > 0 && args[0] == "--json"}, &r); err != nil {
		return err
	}
	fmt.Fprint(w, r.Text)
	return nil
}

// cmdRoles は tb roles [--json]: agent ごとの役割と、渡した版が最新か。
func cmdRoles(args []string, w, stderr io.Writer) error {
	json := len(args) > 0 && args[0] == "--json"
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	live, err := liveAgents()
	if err != nil {
		return err
	}
	for _, a := range live {
		if a.name == "" {
			continue
		}
		last, at, state := "", "", "no-role"
		ro, ok := agents.NewRoles(db.DB, db.Session).LoadRole(a.name)
		if ok {
			if a.sess != "" {
				last, at = agents.LastBriefedHash(db.DB, a.sess)
			}
			switch {
			case last == "":
				state = "never"
			case last == ro.Hash:
				state = "current"
			default:
				state = "outdated"
			}
		}
		h := ro.Hash
		if len(h) > 12 {
			h = h[:12]
		}
		if json {
			var b any
			if at != "" {
				b = at
			}
			fmt.Fprintln(w, jv.Compact(jv.Obj{{K: "name", V: a.name}, {K: "pane", V: a.pane}, {K: "role_file", V: ro.Label},
				{K: "version", V: h}, {K: "state", V: state}, {K: "briefed_at", V: b}}))
		} else {
			if at == "" {
				at = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.name, ro.Label, h, state, at)
		}
	}
	return nil
}
