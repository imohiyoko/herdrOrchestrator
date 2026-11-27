package requests

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
)

// IO は決める操作の出力の先と、承認したコマンドを動かす子の環境。
// Out は tb の標準出力に出す行、Err は標準エラー(「tb: …」)。ChildEnv が nil なら os.Environ()。
// Tb は tb を動かすコマンド(nil ならこのプロセスの herdrctl tb)。
type IO struct {
	Out, Err io.Writer
	ChildEnv []string
	Tb       []string
}

func (o IO) flush() {
	if f, ok := o.Out.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

// Decide は tb approval approve|reject <Q-n | O-n>: who(perms の主体の書き方)として決める。
// agent の操作(L-<n> と、その Q-<n>)は扱わない(app/agent の agent.approve / reject)。agentOps が true なら Q-<n> が agent の操作。
// code は tb の終了コード(0 以外でも err は nil: 送信の 11 / 12、実行したコマンドの終了コード)。
func Decide(db *store.DB, o IO, who, id string, approve, always bool) (code int, agentOps int64, err error) {
	sub := "reject"
	if approve {
		sub = "approve"
	}
	if ReOID.MatchString(id) {
		return decideOutbox(db, o, who, Num(id), approve, always)
	}
	if !ReQID.MatchString(id) {
		return 0, 0, usage("%s Q-<n>", sub)
	}
	r, ok, err := Load(db.DB, Num(id))
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		if _, err := CheckApprover(approval.NewJudge(nil, ""), "tb approval "+sub, who, "", ""); err != nil {
			return 0, 0, err
		}
		return 0, 0, refuse("承認待ちが見つかりません: %s", id)
	}
	switch r.Source {
	case "outbox":
		if !approve && always {
			return 0, 0, usage("reject Q-<n>")
		}
		return decideOutbox(db, o, who, r.SourceID.Int64, approve, always)
	case "agent_ops":
		return 0, r.SourceID.Int64, nil
	}
	if always {
		return 0, 0, usage("%s Q-<n>(コマンドの承認待ちに --always はありません)", sub)
	}
	return decideCmd(db, o, who, r, approve)
}

func decideOutbox(db *store.DB, o IO, who string, oid int64, approve, always bool) (int, int64, error) {
	var code int
	var err error
	if approve {
		code, err = approveOutbox(db, o, who, oid, always)
	} else {
		err = rejectOutbox(db, o, who, oid)
	}
	if err == nil {
		DecidedNotice(db, "outbox", oid, approve, who)
	}
	return code, 0, err
}

func approveOutbox(db *store.DB, o IO, who string, oid int64, always bool) (int, error) {
	j := approval.NewJudge(db.DB, db.Session)
	ob, ok, err := message.GetOutbox(db.DB, oid)
	if err != nil {
		return 0, err
	}
	id := "O-" + strconv.FormatInt(oid, 10)
	if !ok { // agent には、あるかどうかも先に言わない
		if _, err := CheckApprover(j, "tb approve", who, "", ""); err != nil {
			return 0, err
		}
		return 0, refuse("outbox が見つかりません: %s", id)
	}
	act := "send:" + ob.Kind + ":" + ob.Target
	ap, err := CheckApprover(j, "tb approve", who, act, ob.From)
	if err != nil {
		return 0, err
	}
	if always && (ap.Name != "human" || who != "human") { // scheduler など(system:<名前>)も policy は書かない(tb の require_human と同じ)
		return 0, refuse("tb approve: --always(今後は承認なしにする)は人だけが使えます")
	}
	if ob.Status != "pending" {
		return 0, refuse("%s は承認待ちではありません(%s)", id, ob.Status)
	}
	res, err := db.Exec(`UPDATE main.outbox SET status='queued', reason=reason||? WHERE herdr_session=? AND id=? AND status='pending'`,
		" / "+ap.Label()+"が承認 "+Now(), db.Session, oid)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, refuse("%s は承認待ちではありません(ほかで決まりました)", id)
	}
	RecordDecision(db, "outbox", oid, "approved", ap.Name)
	if always {
		if err := SetApproval(db, "allow", act, io.Discard, o.Err); err != nil {
			return 0, err
		}
		fmt.Fprintf(o.Out, "%s = allow(今後は承認なし)\n", act)
	}
	o.flush()
	r := message.Deliver(j, oid, false, io.Discard, o.Err)
	switch r.Code {
	case 0:
		switch {
		case r.Blocked != "":
			fmt.Fprintf(o.Out, "%s approved but blocked by %s\n", id, ob.Target)
		case r.Held != "":
			fmt.Fprintf(o.Out, "%s approved and held for %s (%s)\n", id, ob.Target, r.Held)
		default:
			fmt.Fprintf(o.Out, "%s approved and sent to %s\n", id, ob.Target)
		}
	case 12:
		fmt.Fprintf(o.Out, "%s approved but holding failed (queued)\n", id)
		return 12, nil
	case 11:
		o.flush()
		fmt.Fprintf(o.Err, "tb: %s は承認済みですが宛先 '%s' に届けられません(上の理由を見てください。queued のまま。直したら tb flush)\n", id, ob.Target)
		fmt.Fprintf(o.Out, "%s approved but delivery failed (queued)\n", id)
		return 11, nil
	default:
		fmt.Fprintf(o.Out, "%s approved (宛先使用中のため queued。tb flush で届く)\n", id)
	}
	return 0, nil
}

func rejectOutbox(db *store.DB, o IO, who string, oid int64) error {
	id := "O-" + strconv.FormatInt(oid, 10)
	j := approval.NewJudge(db.DB, db.Session)
	ob, ok, err := message.GetOutbox(db.DB, oid)
	if err != nil {
		return err
	}
	if !ok {
		if _, err := CheckApprover(j, "tb reject", who, "", ""); err != nil {
			return err
		}
		return refuse("%s は承認待ちではありません", id)
	}
	ap, err := CheckApprover(j, "tb reject", who, "send:"+ob.Kind+":"+ob.Target, ob.From)
	if err != nil {
		return err
	}
	if ob.Status != "pending" {
		return refuse("%s は承認待ちではありません", id)
	}
	lbl := ap.Label()
	res, err := db.Exec(`UPDATE main.outbox SET status='denied', reason=reason||?,
	       reply_check=CASE WHEN reply_check='ok' THEN ? ELSE reply_check END WHERE herdr_session=? AND id=? AND status='pending'`,
		" / "+lbl+"が却下 "+Now(), lbl+"が却下", db.Session, oid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return refuse("%s は承認待ちではありません(ほかで決まりました)", id)
	}
	RecordDecision(db, "outbox", oid, "rejected", ap.Name)
	fmt.Fprintf(o.Out, "%s rejected\n", id)
	return nil
}

// DecidedNotice は、決まった送信・agent の操作の承認待ちを、依頼した agent に知らせる(決まらなかったら呼ばない)。
// 文面は元の行の今の状態から作る(届いた・使用中で未送・経路で止めた・inbox に置いた、を言い分ける)。who は決めた者。
func DecidedNotice(db *store.DB, source string, sid int64, approve bool, who string) {
	var qid int64
	if db.QueryRow(`SELECT id FROM requests WHERE source=? AND source_id=?`, source, sid).Scan(&qid) != nil {
		return // 承認待ちの記録が無い(古い行)。知らせる相手が分からない
	}
	r, ok, lerr := Load(db.DB, qid)
	if lerr != nil || !ok {
		return
	}
	what, state := "agent の操作", ""
	if source == "outbox" {
		what = "送信"
		var st, reason string
		_ = db.QueryRow(`SELECT status, COALESCE(reason, '') FROM outbox WHERE id=?`, sid).Scan(&st, &reason)
		switch {
		case !approve:
			state = "却下されました(送っていません)"
		case st == "sent" && strings.Contains(reason, "→ inbox I-"):
			state = "承認され、宛先の inbox に置かれました(宛先が受け取るまで待ちます)"
		case st == "sent":
			state = "承認され、届けました"
		case st == "denied":
			return // 承認のあと送信の経路(delivery)で止めた。送信者には止めた知らせ(tb: blocked)が届くので、重ねて送らない
		default:
			state = "承認されましたが、まだ届けていません(宛先が使用中か届けられませんでした。queued のまま、届けられるようになれば tb flush で届きます)"
		}
	} else {
		var st string
		_ = db.QueryRow(`SELECT status FROM agent_ops WHERE id=?`, sid).Scan(&st)
		switch {
		case !approve:
			state = "却下されました(実行していません)"
		case st == "done":
			state = "承認され、実行されました"
		default:
			state = "承認されましたが、実行に失敗しました(" + st + ")"
		}
	}
	NotifyRequester(db, r, who, fmt.Sprintf("%s(%s %s %s)は %s に%s", r.QID(), what, r.Op, r.Target, who, state))
}

// decideCmd は ask で止まったコマンド(source=cmd)を決める。
func decideCmd(db *store.DB, o IO, who string, r Request, approve bool) (int, int64, error) {
	sub := "reject"
	if approve {
		sub = "approve"
	}
	// deny を開けるのは人だけ(approve: の権限を持つ agent にも開けさせない)
	act := r.Act()
	if r.Status == "denied" {
		act = ""
	}
	ap, err := CheckApprover(approval.NewJudge(db.DB, db.Session), "tb approval "+sub, who, act, r.Requester)
	if err != nil {
		return 0, 0, err
	}
	from := "pending"
	switch {
	case r.Status == "pending":
	case r.Status == "denied" && approve:
		from = "denied"
	case r.Status == "denied":
		return 0, 0, refuse("%s は policy が拒否した記録です(もう却下されています)", r.QID())
	default:
		return 0, 0, refuse("%s は承認待ちではありません(%s)", r.QID(), r.Status)
	}
	if !approve {
		res, err := db.Exec(`UPDATE main.requests SET status='rejected', decided_by=?, decided_at=? WHERE id=? AND status='pending'`, ap.Name, Now(), r.ID)
		if err != nil {
			return 0, 0, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return 0, 0, refuse("%s は承認待ちではありません(ほかで決まりました)", r.QID())
		}
		fmt.Fprintf(o.Out, "%s rejected\n", r.QID())
		NotifyRequester(db, r, ap.Name, r.QID()+" は "+ap.Name+" に却下されました(実行していません): "+r.DetailLine())
		return 0, 0, nil
	}
	pl, err := r.Cmd()
	if err != nil {
		return 0, 0, err
	}
	if HandBack(r.Requester) {
		// 依頼者が herdr にいなければ承認しない(知らせが届かず、run できるのも依頼者だけなので、承認しても使われない)
		alive, err := AgentAlive(r.Requester)
		if err != nil {
			return 0, 0, refuse("herdr agent list に失敗したため、依頼者 %s がいるか確かめられません(%s は承認していません)", r.Requester, r.QID())
		}
		if !alive {
			if from == "denied" {
				return 0, 0, refuse("依頼者 %s が herdr にいないため承認しません(%s は deny の記録のまま。%s を復活させてから承認してください)", r.Requester, r.QID(), r.Requester)
			}
			return 0, 0, refuse("依頼者 %s が herdr にいないため承認しません(%s は承認待ちのまま。tb approval reject %s で却下するか、%s を復活させてから承認してください)", r.Requester, r.QID(), r.QID(), r.Requester)
		}
	}
	res, err := db.Exec(`UPDATE main.requests SET status='approved', decided_by=?, decided_at=? WHERE id=? AND status=?`, ap.Name, Now(), r.ID, from)
	if err != nil {
		return 0, 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, 0, refuse("%s は承認待ちではありません(ほかで決まりました)", r.QID())
	}
	r.Status = "approved"
	if HandBack(r.Requester) {
		// 依頼した agent に知らせ、依頼者が自分で 1 回だけ実行する(tb approval run)
		fmt.Fprintf(o.Out, "%s approved by %s: 依頼者 %s に知らせました(%s が tb approval run %s で 1 回だけ実行します): tb %s\n",
			r.QID(), ap.Name, r.Requester, r.Requester, r.QID(), strings.Join(pl.Args, " "))
		if !NotifyRequester(db, r, ap.Name, r.QID()+" は "+ap.Name+" に承認されました。tb approval run "+r.QID()+" で 1 回だけ実行できます(実行するコマンド: tb "+strings.Join(pl.Args, " ")+")") {
			fmt.Fprintf(o.Err, "tb: 依頼者 %s に知らせを届けられませんでした(使用中か不在。%s は承認済みのまま。依頼者は tb approval history --mine で見られます)\n", r.Requester, r.QID())
		}
		return 0, 0, nil
	}
	// 知らせる相手がいない依頼者(scheduler などの system:<名前>、人)は、承認した側がその場で依頼者として実行する
	fmt.Fprintf(o.Out, "%s approved by %s: %s として実行します: tb %s\n", r.QID(), ap.Name, r.Requester, strings.Join(pl.Args, " "))
	o.flush()
	code, err := RunApproved(db, o, r, pl)
	return code, 0, err
}

// KeyHash は実行の鍵の記録(鍵そのものは残さない)。
func KeyHash(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:])
}

// RunApproved は承認済みの依頼を子として 1 回だけ実行する(子は鍵が合うときだけ、依頼者として動く。結果を Q-<n> に残す)。
// 戻り値は子の終了コード。
func RunApproved(db *store.DB, o IO, r Request, pl CmdPayload) (int, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return 0, err
	}
	keyHex := hex.EncodeToString(key)
	res, err := db.Exec(`UPDATE main.requests SET run_key=? WHERE id=? AND status='approved' AND run_key IS NULL`, KeyHash(keyHex), r.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, refuse("%s はもう実行しています", r.QID())
	}
	tb := o.Tb
	if len(tb) == 0 {
		self, err := os.Executable()
		if err != nil {
			return 0, err
		}
		tb = []string{self, "tb"}
	}
	var out bytes.Buffer
	cmd := exec.Command(tb[0], append(append([]string{}, tb[1:]...), pl.Args...)...)
	// 承認した側の pane・呼び出し元は渡さない(子は依頼者として動く。承認した側の権限は使わない)
	base := o.ChildEnv
	if base == nil {
		base = os.Environ()
	}
	var env []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k != "HERDR_PANE_ID" && k != "TB_SYSTEM" && k != "TB_APPROVED_REQUEST" && k != "TB_APPROVED_KEY" {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "TB_APPROVED_REQUEST="+r.QID(), "TB_APPROVED_KEY="+keyHex)
	cmd.Stdin = strings.NewReader(pl.Stdin)
	cmd.Stdout = io.MultiWriter(o.Out, &out)
	cmd.Stderr = io.MultiWriter(o.Err, &out)
	runErr := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		code = ee.ExitCode()
	} else if runErr != nil {
		code = 1
	}
	st := "done"
	if code != 0 {
		st = "failed"
	}
	result := fmt.Sprintf("exit %d", code)
	if s := strings.TrimSpace(out.String()); s != "" {
		if len(s) > 2000 {
			s = s[len(s)-2000:]
		}
		result += "\n" + s
	}
	_, _ = db.Exec(`UPDATE main.requests SET status=?, result=?, run_key=NULL WHERE id=?`, st, result, r.ID)
	return code, nil
}
