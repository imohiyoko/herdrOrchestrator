package tbcli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// 役割の配布と定義(taskboard/lib/roles.sh の role-context / role-broadcast / role-show / role-list / role-write / role-edit /
// role-unset / role-set / role-log / roles)。

var (
	reRoleSrc  = regexp.MustCompile(`^(startup|resume|clear|compact)$`)
	reRoleSID  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	reRoleItem = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func roleAdmin() string { return envOr("TB_ROLE_ADMIN", "hr") }

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

// roleAdminCheck は role_admin_check: 人事役(agent)が役割を変えてよいか。target は grants の対象、cmd は人に頼むときのコマンド。
// single なら target は 1 体の agent で、grants に当たれば人事役でなくてよい(tb role-set)。
func roleAdminCheck(db *store.DB, j *approval.Judge, by, target, cmd string, single bool, stderr io.Writer) error {
	value, rule := "", ""
	if r, ok := j.GrantOf(by, "role:change", target); single && ok {
		value, rule = "allow", r
	} else {
		if j.Roles.Match(by, "@"+roleAdmin()) != 0 {
			return die("ロールを変えられるのは人と人事役(@%s)だけです(%s)", roleAdmin(), by)
		}
		if r, ok := j.GrantOf(by, "role:change", target); ok {
			value, rule = "allow", r
		} else {
			value, rule = j.Of("role:change", "")
		}
	}
	if value != "allow" {
		fmt.Fprintf(stderr, "tb: [role.change.needs-approval] %s\n", config.Msg(db.DB, db.Session, "role.change.needs-approval",
			"role:change は人の承認が要ります({rule})。人が自分の端末で実行してください: {command}", "rule="+rule, "command="+cmd))
		return &exit{exitAsk, ""}
	}
	return nil
}

// rolePut は role_put: 役割の本文を DB に書く(人か、許された人事役)。
func rolePut(db *store.DB, name, body, how string, shared bool, w, stderr io.Writer) error {
	key, _ := agents.KeyOf(name)
	by, err := requester()
	if err != nil {
		return err
	}
	j := approval.NewJudge(db.DB, db.Session)
	if by != "human" {
		admin := roleAdmin()
		if name == admin {
			return die("人事役の役割(%s)を書き換えられるのは人だけです", admin)
		}
		if name == "@map" {
			for _, line := range strings.Split(body, "\n") {
				_, x, _ := strings.Cut(line, "=")
				x = util.ReadRest(x, "=")
				x = config.Trim(cutHash(x))
				if strings.Contains(","+strings.ReplaceAll(x, " ", "")+",", ","+admin+",") {
					return die("人事役(%s)を付ける対応を書けるのは人だけです", admin)
				}
			}
		}
		sh := ""
		if shared {
			sh = " --shared"
		}
		if err := roleAdminCheck(db, j, by, name, "tb role-write "+name+sh+" < 本文", false, stderr); err != nil {
			return err
		}
	}
	if blank(body) {
		return die("本文が空です(消すときは人が tb role-unset %s で行を消す)", name)
	}
	if err := config.DBPut(db.DB, db.Session, key, body, how, shared, by); err != nil {
		return err
	}
	_, src, _ := agents.NewRoles(db.DB, db.Session).LookupSrc(name)
	fmt.Fprintf(w, "%s: %s。動いている agent に配るには: tb role-broadcast\n", name, src)
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
	key, _ := agents.KeyOf(name)
	if err := config.Unset(db.DB, db.Session, key, shared); err != nil {
		return err
	}
	if _, src, ok := agents.NewRoles(db.DB, db.Session).LookupSrc(name); ok {
		fmt.Fprintf(w, "%s: %s\n", name, src)
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
	xs := strings.Split(role, ",") // IFS=, read -ra: 末尾の空の欄は数えない
	if xs[len(xs)-1] == "" {
		xs = xs[:len(xs)-1]
	}
	if len(xs) < 1 || len(xs) > 4 {
		return usage("ロールはカンマ区切りで 1〜4 個: %s", role)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	j := approval.NewJudge(db.DB, db.Session)
	for _, x := range xs {
		if !reRoleItem.MatchString(x) || x == "README" || !j.Roles.Exists(x) {
			return usage("ロールは定義のある役割名(tb role-list。カンマ区切りで複数可): %s", x)
		}
	}
	by, err := requester()
	if err != nil {
		return err
	}
	if by != "human" {
		admin := roleAdmin()
		if _, ok := j.GrantOf(by, "role:change", name); !ok && j.Roles.Match(by, "@"+admin) != 0 {
			return die("ロールを変えられるのは人、人事役(@%s)、grants で role:change を許された agent だけです(%s)", admin, by)
		}
		if by == name {
			return die("自分のロールは変えられません")
		}
		for _, x := range xs {
			if x == admin {
				return die("人事役のロール(%s)を付けられるのは人だけです", admin)
			}
		}
		if err := roleAdminCheck(db, j, by, name, "tb role-set "+name+" "+role, true, stderr); err != nil {
			return err
		}
	}
	_ = agents.Sync(db.DB, db.Session)
	var one int
	if db.QueryRow(`SELECT 1 FROM agents WHERE name=?`, name).Scan(&one) != nil {
		if msg, ok := otherSession(db, name); ok {
			return die("%s(ロールはセッション固有)", msg)
		}
		return die("台帳に無い agent です: %s(tb agents で確かめる)", name)
	}
	r := agents.NewRoles(db.DB, db.Session)
	if _, src, ok := r.LookupSrc(name); ok {
		return die("役割 %s(%s)があるため、台帳のロールは効きません(その役割を人事役の手順で直す)", name, src)
	}
	// 旧ロールは読んだ台帳の値から決め、変更はその値が変わっていないときだけ行う
	var reg string
	if db.QueryRow(`SELECT COALESCE(role,'') FROM agents WHERE name=?`, name).Scan(&reg) != nil {
		return die("台帳を読めません")
	}
	old := strings.Join(r.FilesForReg(name, reg), ",")
	n, err := roleSetTx(db, name, old, role, by, util.Oneline(why, 300), reg)
	if err != nil {
		return die("ロールを変えられませんでした(DB)")
	}
	if n != 1 {
		return die("同時に別のロール変更が入りました。もう一度実行してください")
	}
	nw := strings.Join(agents.NewRoles(db.DB, db.Session).FilesForLoose(name), ",")
	if old == "" {
		old = "-"
	}
	fmt.Fprintf(w, "%s role %s -> %s (by %s)。本人に配るには: tb role-broadcast %s\n", name, old, nw, by, name)
	return nil
}

func roleSetTx(db *store.DB, name, old, role, by, why, reg string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	if _, err := tx.Exec(`INSERT INTO main.role_changes (agent, old_role, new_role, changed_by, reason, created_at, herdr_session)
    SELECT ?, NULLIF(?,''), ?, ?, NULLIF(?,''), ?, ?
    WHERE (SELECT COALESCE(role,'') FROM agents WHERE name=?)=?`, name, old, role, by, why, ts, db.Session, name, reg); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE main.agents SET role=?, updated_at=? WHERE herdr_session=? AND name=? AND COALESCE(role,'')=?`, role, ts, db.Session, name, reg)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// otherSession は other_session: 名前が別の herdr セッションの agent なら、その旨の文面。
func otherSession(db *store.DB, name string) (string, bool) {
	var s string
	if db.QueryRow(`SELECT herdr_session FROM main.agents WHERE name=? AND herdr_session!=?
    AND NOT EXISTS (SELECT 1 FROM agents WHERE name=?) ORDER BY updated_at DESC LIMIT 1`, name, db.Session, name).Scan(&s) != nil || s == "" {
		return "", false
	}
	return name + " は herdr セッション '" + s + "' の agent です。HERDR_SESSION=" + s + " を付けて実行してください", true
}

// cmdRoleLog は tb role-log [--json]: ロール変更の記録(このセッションのもの)。
func cmdRoleLog(args []string, w, stderr io.Writer) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(args) > 0 && args[0] == "--json" {
		os, err := sqlObjs(db.DB, nil, `SELECT * FROM role_changes ORDER BY id DESC LIMIT 100`)
		if err != nil {
			return err
		}
		compactRows(w, os)
		return nil
	}
	return sqlTSV(db.DB, w, `SELECT created_at, agent, COALESCE(old_role,'-')||' -> '||new_role, changed_by, COALESCE(reason,'') FROM role_changes ORDER BY id DESC LIMIT 100`)
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
