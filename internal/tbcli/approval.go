package tbcli

import (
	"fmt"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// tb approval(taskboard/lib/approvals.sh)。古い名前 tb approval <操作> / allow / ask / approve / reject も同じに動かす。

const approvalUsage = `tb approval check <操作> [--by A] [--to 宛先 --text 本文 [--kind K]]   1 行目 allow|ask|deny、2 行目 rule:(終了コード 0=allow 20=ask 4=deny)
tb approval allow <操作のパターン>    今後は許可する(人だけ。設定 policy に * <操作> <対象> = allow の行を書く)
tb approval ask <操作のパターン>      承認制に戻す(人だけ。設定 policy に * <操作> <対象> = ask-any の行を書く)
tb approval list [--json]             承認待ちの一覧(送信 O-<n> と agent の操作 L-<n>。古い順)
tb approval approve O-<n>|L-<n> [--always]  承認する(O- は送信を届ける。L- は tb agent-approve と同じ)
tb approval reject O-<n>|L-<n>        却下する(L- は tb agent-reject と同じ)
(古い名前 tb approval <操作> / tb allow / tb ask / tb approve / tb reject も同じに使える)
`

const exitAsk = 20

var (
	reAction = regexp.MustCompile(`^[A-Za-z0-9._:*?@-]{1,128}$`)
	reTo     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reOID    = regexp.MustCompile(`^O-[0-9]+$`)
)

// inWords は bash の [[ " $LIST " == *" $v "* ]](空白で区切った一覧に含まれるか。"task info" のように続いた語も当たる)。
func inWords(list []string, v string) bool {
	return strings.Contains(" "+strings.Join(list, " ")+" ", " "+v+" ")
}

func validAction(a string) error {
	if !reAction.MatchString(a) {
		return usage("操作名に使えない文字があります: %s", a)
	}
	return nil
}

// cmdApproval は tb approval <サブコマンド>。legacy は古い名前(allow / ask / approve / reject)。
func cmdApproval(args []string, w, stderr io.Writer, legacy string) error {
	sub := legacy
	if sub == "" {
		if len(args) == 0 {
			fmt.Fprint(w, approvalUsage)
			return &exit{exitUsage, ""}
		}
		switch args[0] {
		case "check", "list", "allow", "ask", "approve", "reject":
			sub, args = args[0], args[1:]
		case "help", "-h", "--help":
			fmt.Fprint(w, approvalUsage)
			return nil
		default:
			sub = "check" // 古い形: tb approval <操作>
		}
	}
	// 承認待ちは 1 か所で見て決める(ADR 0007 の段 4)。L-<n> は tb agent-approve / agent-reject に渡す
	// (その入口の門も通す。policy で cmd:agent:approve だけを締めた行を、こちらから迂回させない)
	if (sub == "approve" || sub == "reject") && len(args) > 0 && strings.HasPrefix(args[0], "L-") {
		name := "agent-" + sub
		if err := permErr(perms.Gate(append([]string{name}, args...), permDB)); err != nil {
			return err
		}
		if sub == "approve" {
			return cmdAgentApprove(args, w, stderr)
		}
		return cmdAgentReject(args, w, stderr)
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	switch sub {
	case "list":
		return approvalList(db, args, w)
	case "check":
		return approvalCheck(db, args, w, stderr)
	case "allow", "ask":
		if len(args) != 1 {
			return usage("%s <操作のパターン>", sub)
		}
		return setApproval(db, sub, args[0], w, stderr)
	case "approve":
		return approve(db, args, w, stderr)
	default:
		return reject(db, args, w)
	}
}

func approvalCheck(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) < 1 {
		return usage("approval <操作>(例: send:task:worker1, role:change)[--by agent][--to 宛先 --text 本文 [--kind K] [--task T-..]]")
	}
	act, to, text, kind, by, haveText := args[0], "", "", "task", "", false
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--by", "--to", "--text", "--kind", "--task":
			if i+1 >= len(args) {
				return usage("%s に値が必要です", a)
			}
			v := args[i+1]
			i++
			switch a {
			case "--by":
				by = v
			case "--to":
				to = v
			case "--text":
				text, haveText = v, true
			case "--kind":
				kind = v
			}
		default:
			return usage("approval: 不明な引数 %s", a)
		}
	}
	if err := validAction(act); err != nil {
		return err
	}
	if by != "" && !agents.NameRE.MatchString(by) {
		return usage("approval: --by は agent 名: %s", by)
	}
	risk := to != "" && haveText
	if risk {
		if !reTo.MatchString(to) {
			return usage("approval: --to は agent 名(英数字と . _ -)")
		}
		if !inWords(message.SendKinds, kind) {
			return usage("approval: --kind は %s のどれか", strings.Join(message.SendKinds, " "))
		}
	} else if to != "" || haveText {
		return usage("approval: リスク評価には --to と --text の両方が要ります")
	}
	j := approval.NewJudge(db.DB, db.Session)
	v, rule := j.Of(act, by)
	fmt.Fprintf(w, "%s\nrule: %s\n", v, rule)
	if risk {
		sender := ""
		if os.Getenv("HERDR_PANE_ID") != "" {
			if l, _, ok := identity.ResolveLabel(func(code, def string) string { return config.Msg(db.DB, db.Session, code, def) }); ok {
				sender = l.Name
			} else {
				sender = "?"
			}
		}
		fmt.Fprint(w, approval.Eval(j.Collect(sender, to, kind, text)).Format())
	}
	switch v {
	case "allow":
		return nil
	case "deny":
		return &exit{4, ""}
	}
	return &exit{exitAsk, ""}
}

func nowTS() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// setApproval は set_approval: 設定 policy に「* <操作名> <対象> = allow|ask-any」の行を書く
// (主体・操作名・対象が同じ行があれば書き換え、無ければ末尾に足す)。書いたあとに、もっと厳しい行が当たっていないか確かめる。
func setApproval(db *store.DB, val, pat string, w io.Writer, stderr io.Writer) error {
	if err := validAction(pat); err != nil {
		return err
	}
	if err := requireHuman("tb " + val); err != nil {
		return err
	}
	eff := "ask-any"
	if val == "allow" {
		eff = "allow"
	}
	op, target := perms.SplitAct(pat)
	if target == "" {
		target = "*"
	}
	ops := perms.OpsOf(op)
	if len(ops) == 0 {
		return die("tb %s: %s はコマンドの中の操作に当たりません(コマンド cmd: や承認の委任 approve: は policy に直接書く: tb config edit policy)", val, pat)
	}
	cur, _, has, readErr := config.LookupStrict(db.DB, db.Session, "policy")
	if readErr != "" {
		return die("設定 policy を読めないため書きません(%s)", readErr)
	}
	var lines []string
	if has && cur != "" {
		lines = strings.Split(cur, "\n")
	}
	ts := nowTS()
	var b strings.Builder
	done := map[string]bool{}
	for _, line := range lines {
		l := config.Trim(cutHash(line))
		lhs, _, isRule := strings.Cut(l, "=")
		f := strings.Fields(lhs)
		hit := ""
		if isRule && len(f) == 3 && f[2] == target {
			for _, o := range ops {
				if f[0] == perms.SubjectOf(o) && f[1] == o {
					hit = o
				}
			}
		}
		if hit == "" {
			b.WriteString(line + "\n")
			continue
		}
		if done[hit] {
			continue
		}
		b.WriteString(perms.SubjectOf(hit) + " " + hit + " " + target + " = " + eff + "   # " + ts + " に人が変更(tb approval " + val + ")\n")
		done[hit] = true
	}
	body := strings.TrimSuffix(b.String(), "\n")
	for _, o := range ops {
		if done[o] {
			continue
		}
		row := perms.SubjectOf(o) + " " + o + " " + target + " = " + eff + "   # " + ts + " に人が追加(tb approval " + val + ")"
		if body == "" {
			body = row
		} else {
			body += "\n" + row
		}
	}
	if bad := perms.ValidatePolicy(body); bad != "" {
		fmt.Fprint(stderr, bad)
		return die("policy の本文に問題があるため書きませんでした")
	}
	info, _ := config.Info("policy")
	if f := os.Getenv(info.Env); f != "" {
		if err := writeConfigFile(f, body); err != nil {
			return err
		}
	} else if err := config.DBPut(db.DB, db.Session, "policy", body, val, false, ""); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO main.approval_changes (herdr_session, pattern, value, created_at) VALUES (?, ?, ?, ?)`, db.Session, pat, val, nowTS()); err != nil {
		return err
	}
	for _, o := range ops {
		fmt.Fprintf(w, "%s %s %s = %s\n", perms.SubjectOf(o), o, target, eff)
	}
	if val == "allow" && !strings.ContainsAny(pat, "*?") && perms.SubjectOf(op) == "*" {
		if tier, rule := approval.NewJudge(db.DB, db.Session).Authorize(pat, ""); tier != "allow" {
			fmt.Fprintf(stderr, "tb: 注意: もっと厳しい行が当たるため、%s はまだ %s です(%s)\n", pat, tier, rule)
		}
	}
	return nil
}

func isPosixSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r'
}

func cutHash(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// approver は approver_check の結果(human か agent の名前と根拠)。
type approver struct{ name, rule string }

func (a approver) label() string {
	if a.name == "human" {
		return "人"
	}
	return "agent " + a.name + "(" + a.rule + ")"
}

// approverCheck は approver_check <コマンド名> <元の操作名(空 = 人だけ)> <依頼者>。
func approverCheck(j *approval.Judge, cmd, act, by string) (approver, error) {
	who := ""
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		n, err := identity.PaneOwnerName(pane)
		if err != nil {
			return approver{}, die("herdr agent list に失敗したため、実行者を特定できません")
		}
		who = n
	}
	if who == "human" {
		return approver{}, die("agent 名 human は予約名です(人と区別できないため、この agent からの操作は受けません)")
	}
	if who == "" {
		return approver{name: "human"}, nil
	}
	if act == "" {
		return approver{}, die("%s: この承認待ちは人が自分の端末で実行します(agent '%s' の pane からは承認できません)", cmd, who)
	}
	if who == by {
		return approver{}, die("%s: 自分の依頼は承認できません(agent '%s')。人が自分の端末で実行します", cmd, who)
	}
	if agents.NameRE.MatchString(by) {
		if tier, r := j.Authorize(act, by); tier == "ask-human" {
			return approver{}, die("%s は人が自分の端末で実行します(%s からの %s は ask-human のため、人だけが承認できます。%s)", cmd, by, act, r)
		}
	}
	rule, ok := j.GrantOf(who, "approve:"+act, by)
	if !ok {
		return approver{}, die("%s は人が自分の端末で実行します(agent '%s' には %s からの %s を承認する許可がありません。policy に '<承認者> approve:<操作> <依頼者> = allow' の行が要ります)", cmd, who, by, act)
	}
	return approver{who, rule}, nil
}

func oidOf(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "O-"), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func approve(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) < 1 || !reOID.MatchString(args[0]) {
		return usage("approve O-<n> [--always]")
	}
	oid := oidOf(args[0])
	always := len(args) > 1 && args[1] == "--always"
	j := approval.NewJudge(db.DB, db.Session)
	o, ok, err := message.GetOutbox(db.DB, oid)
	if err != nil {
		return err
	}
	id := "O-" + strconv.FormatInt(oid, 10)
	if !ok { // agent には、あるかどうかも先に言わない
		if _, err := approverCheck(j, "tb approve", "", ""); err != nil {
			return err
		}
		return die("outbox が見つかりません: %s", id)
	}
	act := "send:" + o.Kind + ":" + o.Target
	ap, err := approverCheck(j, "tb approve", act, o.From)
	if err != nil {
		return err
	}
	if ap.name != "human" && always {
		return die("tb approve: --always(今後は承認なしにする)は人だけが使えます")
	}
	if o.Status != "pending" {
		return die("%s は承認待ちではありません(%s)", id, o.Status)
	}
	res, err := db.Exec(`UPDATE main.outbox SET status='queued', reason=reason||? WHERE herdr_session=? AND id=? AND status='pending'`,
		" / "+ap.label()+"が承認 "+nowTS(), db.Session, oid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("%s は承認待ちではありません(ほかで決まりました)", id)
	}
	if always {
		if err := setApproval(db, "allow", act, io.Discard, stderr); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s = allow(今後は承認なし)\n", act)
	}
	flush(w)
	r := message.Deliver(j, oid, false, io.Discard, stderr)
	switch r.Code {
	case 0:
		switch {
		case r.Blocked != "":
			fmt.Fprintf(w, "%s approved but blocked by %s\n", id, o.Target)
		case r.Held != "":
			fmt.Fprintf(w, "%s approved and held for %s (%s)\n", id, o.Target, r.Held)
		default:
			fmt.Fprintf(w, "%s approved and sent to %s\n", id, o.Target)
		}
	case 12:
		fmt.Fprintf(w, "%s approved but holding failed (queued)\n", id)
		return &exit{12, ""}
	case 11:
		flush(w)
		fmt.Fprintf(stderr, "tb: %s は承認済みですが宛先 '%s' に届けられません(上の理由を見てください。queued のまま。直したら tb flush)\n", id, o.Target)
		fmt.Fprintf(w, "%s approved but delivery failed (queued)\n", id)
		return &exit{11, ""}
	default:
		fmt.Fprintf(w, "%s approved (宛先使用中のため queued。tb flush で届く)\n", id)
	}
	return nil
}

func reject(db *store.DB, args []string, w io.Writer) error {
	if len(args) != 1 || !reOID.MatchString(args[0]) {
		return usage("reject O-<n>")
	}
	oid := oidOf(args[0])
	id := "O-" + strconv.FormatInt(oid, 10)
	j := approval.NewJudge(db.DB, db.Session)
	o, ok, err := message.GetOutbox(db.DB, oid)
	if err != nil {
		return err
	}
	if !ok {
		if _, err := approverCheck(j, "tb reject", "", ""); err != nil {
			return err
		}
		return die("%s は承認待ちではありません", id)
	}
	ap, err := approverCheck(j, "tb reject", "send:"+o.Kind+":"+o.Target, o.From)
	if err != nil {
		return err
	}
	if o.Status != "pending" {
		return die("%s は承認待ちではありません", id)
	}
	lbl := ap.label()
	res, err := db.Exec(`UPDATE main.outbox SET status='denied', reason=reason||?,
	       reply_check=CASE WHEN reply_check='ok' THEN ? ELSE reply_check END WHERE herdr_session=? AND id=? AND status='pending'`,
		" / "+lbl+"が却下 "+nowTS(), lbl+"が却下", db.Session, oid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return die("%s は承認待ちではありません(ほかで決まりました)", id)
	}
	fmt.Fprintf(w, "%s rejected\n", id)
	return nil
}

// flush は書きかけの標準出力を先に出す(標準エラーとの順を bash と同じにする)。
func flush(w io.Writer) {
	if f, ok := w.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

// approvalList は tb approval list(承認待ち: 送信 O-<n> と agent の操作 L-<n>。古い順)。
func approvalList(db *store.DB, args []string, w io.Writer) error {
	json := false
	for _, a := range args {
		if a != "--json" {
			return usage("approval list: 不明な引数 %s", a)
		}
		json = true
	}
	q := `SELECT 'O-'||id AS id, 'send:'||kind AS op, from_name AS requested_by, target, created_at, text AS detail FROM outbox WHERE status='pending'
    UNION ALL SELECT 'L-'||id, 'agent:'||op, requested_by, target, created_at, args FROM agent_ops WHERE status='pending'
    ORDER BY created_at, id`
	if json {
		rows, err := sqlObjs(db.DB, nil, q)
		if err != nil {
			return err
		}
		compactRows(w, rows)
		return nil
	}
	return qrows(db.DB, `SELECT id, op, requested_by, target, created_at FROM (`+q+`)`, nil, func(_ []string, vals []any) error {
		s := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				s[i] = "-"
			} else {
				s[i] = tickets.Text(v)
			}
		}
		_, err := fmt.Fprintln(w, strings.Join(s, "\t"))
		return err
	})
}
