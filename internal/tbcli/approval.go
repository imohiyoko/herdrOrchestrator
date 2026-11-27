package tbcli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	appapproval "github.com/imohiyoko/herdrorchestrator/internal/app/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

// tb approval(taskboard/lib/approvals.sh)。古い名前 tb approval <操作> / allow / ask / approve / reject も同じに動かす。

const approvalUsage = `tb approval check <操作> [--by A] [--to 宛先 --text 本文 [--kind K]]   1 行目 allow|ask|deny、2 行目 rule:(終了コード 0=allow 20=ask 4=deny)
tb approval allow <操作のパターン>    今後は許可する(人だけ。設定 policy に * <操作> <対象> = allow の行を書く)
tb approval ask <操作のパターン>      承認制に戻す(人だけ。設定 policy に * <操作> <対象> = ask の行を書く)
tb approval list [--mine] [--json]    承認待ちの一覧(Q-<n>。送信・agent の操作・ask で止まったコマンド。優先度の高い順、同じなら古い順。--mine は自分が決められるものだけ)
tb approval history [--mine|--by A] [--limit N] [--json]   allow / ask / deny の記録(新しい順。判定・状態・依頼者・決めた人・当たった行)
tb approval approve Q-<n> [--always]  承認する(送信は届け、agent の操作は実行する。コマンドは依頼した agent に知らせ、依頼者が run で実行する。
                                      deny の記録 Q-<n> は人だけが例外として承認できる。前の O-<n> / L-<n> も受ける)
tb approval run Q-<n>                 承認された自分の依頼を 1 回だけ実行する(依頼した agent だけ。2 回目は実行しない)
tb approval reject Q-<n>              却下する(依頼した agent に知らせる)
tb approval priority Q-<n> high|normal|low   優先度を変える(依頼した agent と人。積むときは TB_PRIORITY で付けられる)
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
		case "check", "list", "history", "run", "allow", "ask", "approve", "reject", "priority":
			sub, args = args[0], args[1:]
		case "help", "-h", "--help":
			fmt.Fprint(w, approvalUsage)
			return nil
		default:
			sub = "check" // 古い形: tb approval <操作>
		}
	}
	// 承認待ちは 1 か所で見て決める(ADR 0007 の段 4。API の approval.approve / reject)。L-<n> は tb agent-approve / agent-reject に渡す
	// (その入口の門も通す。policy で cmd:agent:approve だけを締めた行を、こちらから迂回させない)
	if (sub == "approve" || sub == "reject") && (len(args) == 0 || !strings.HasPrefix(args[0], "L-")) {
		return approvalDecide(sub, args, w, stderr)
	}
	if sub == "priority" {
		return requestPriority(args, w, stderr)
	}
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
	case "history":
		return approvalHistory(db, args, w)
	case "run":
		return approvalRun(db, args, w, stderr)
	case "check":
		return approvalCheck(db, args, w, stderr)
	case "allow", "ask":
		if len(args) != 1 {
			return usage("%s <操作のパターン>", sub)
		}
		return setApproval(db, sub, args[0], w, stderr)
	}
	return usage("approval: 不明なサブコマンド %s", sub)
}

// approvalDecide は tb approval approve|reject Q-<n>(前の O-<n> も): API の approval.approve / reject を呼ぶ。
func approvalDecide(sub string, args []string, w, stderr io.Writer) error {
	id := ""
	if len(args) > 0 {
		id = args[0]
	}
	rest := args[min(len(args), 1):]
	switch {
	case reQID.MatchString(id):
	case strings.HasPrefix(id, "Q-"):
		return usage("%s Q-<n>", sub)
	case requests.ReOID.MatchString(id): // 前の名前(tb approve O-<n>)
		if sub == "reject" && len(rest) > 0 {
			return usage("reject O-<n>")
		}
	case sub == "approve":
		return usage("approve O-<n> [--always]")
	default:
		return usage("reject O-<n>")
	}
	if len(rest) > 1 || (len(rest) == 1 && (sub == "reject" || rest[0] != "--always")) {
		if sub == "reject" {
			return usage("reject Q-<n>")
		}
		return usage("approve Q-<n> [--always]")
	}
	always := len(rest) == 1
	if _, err := callerName(); err != nil {
		return die("herdr agent list に失敗したため、実行者を特定できません")
	}
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	var r appapproval.Result
	if err := callAppIO(db, "tb approval "+sub, w, stderr, "approval."+sub, appapproval.DecideReq{ID: id, Always: always}, &r); err != nil {
		return err
	}
	if r.Exit != 0 {
		return &exit{r.Exit, ""}
	}
	return nil
}

// setApproval は tb approval allow|ask <パターン>(と、承認の --always が書く行): API の approval.allow / ask を呼ぶ。
func setApproval(db *store.DB, val, pat string, w, stderr io.Writer) error {
	if err := validAction(pat); err != nil {
		return err
	}
	var r appapproval.Result
	return callAppIO(db, "tb "+val, w, stderr, "approval."+val, appapproval.PatternReq{Pattern: pat}, &r)
}

func cutHash(s string) string { return requests.CutHash(s) }

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

// flush は書きかけの標準出力を先に出す(標準エラーとの順を bash と同じにする)。
func flush(w io.Writer) {
	if f, ok := w.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}
