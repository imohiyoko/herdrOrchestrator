package message

import (
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/agents"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	// ReTo は宛先(agent 名。英数字と . _ -)。
	ReTo = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// ReReply は --reply-to(O-<n>)。
	ReReply  = regexp.MustCompile(`^O-[0-9]{1,18}$`)
	reTaskID = regexp.MustCompile(`^T-[A-Za-z0-9_]+-[0-9]{3,}$`)
)

// SendSpec は 1 通の送信(tb message send)。From / Pane / Session は herdr から決めた送信者。
type SendSpec struct {
	From, Pane, Session string
	To, Text, Task      string
	Kind                string
	ReplyTo             string // O-<n>(返信の主張。DB で確かめる)
}

// Sent は送信の結果。Code は tb の終了コード(0 送信済み・保留 / 10 使用中 / 11 送信失敗 / 12 保留に失敗 / 4 拒否 / 20 承認待ち)。
// Held は保留した inbox の I-<n>、Blocked は経路の規則で止めたときの O-<n>、Q は承認待ちの Q-<n>。
type Sent struct {
	OID     int64
	Code    int
	Held    string
	Blocked string
	Q       string
}

// LiveName は herdr agent list にその名前の agent がいるか(herdr に届かなければ いない)。
func LiveName(name string) bool {
	as, err := herdr.Agents()
	if err != nil {
		return false
	}
	for _, a := range as {
		if a.Name == name {
			return true
		}
	}
	return false
}

// SendRefused は送信を受け付けなかった理由(Usage なら使い方の誤り)。
type SendRefused struct {
	Msg   string
	Usage bool
}

func (e *SendRefused) Error() string { return e.Msg }

// CheckSend は送信の入力の確かめ(tb send の順と文言)。送信者が決まる前に見るもの。
func CheckSend(db *sql.DB, s SendSpec) error {
	u := func(f string, a ...any) error { return &SendRefused{Msg: fmt.Sprintf(f, a...), Usage: true} }
	if !ReTo.MatchString(s.To) {
		return u("send: 宛先は agent 名(英数字と . _ -): %s", s.To)
	}
	if !slices.Contains(SendKinds, s.Kind) {
		return u("send: --kind は %s のどれか", strings.Join(SendKinds, " "))
	}
	if strings.TrimFunc(s.Text, config.IsSpace) == "" {
		return u("send: --text が必要です")
	}
	if s.ReplyTo != "" && !ReReply.MatchString(s.ReplyTo) {
		return u("send: --reply-to は O-<n>(返信する元の送信): %s", s.ReplyTo)
	}
	if s.Task != "" {
		if !reTaskID.MatchString(s.Task) {
			return u("タスク ID は T-<project>-NNN 形式: %s", s.Task)
		}
		if ok, err := tickets.Exists(db, s.Task); err != nil {
			return err
		} else if !ok {
			return &SendRefused{Msg: "タスクが見つかりません: " + s.Task}
		}
	}
	return nil
}

// Send は送信者が決まったあとの送信(tb send の残り): 自分宛て・宛先の不在を断り、受け付けの判定をして outbox に積み、届ける。
// 途中の知らせ(「tb: …」)は stderr に書く。q は承認待ちに積んだ行の Q-<n> を返す(知らせも送る)。
func Send(db *store.DB, s SendSpec, now string, stderr io.Writer, q func(oid int64) string) (Sent, error) {
	if s.To == s.From {
		return Sent{}, &SendRefused{Msg: "send: 自分自身には送れません", Usage: true}
	}
	if !LiveName(s.To) {
		return Sent{}, &SendRefused{Msg: "send: 宛先の agent が herdr にいません: " + s.To + "(保存しません)"}
	}
	j := approval.NewJudge(db.DB, db.Session)
	status := "queued"
	// 受け付け時の判定(返信の主張があれば返信として見る。届ける直前に、確かめた結果でもう一度決める)
	d := Mode(j, s.From, s.To, s.Kind, s.ReplyTo != "")
	reason := d.Rule
	apRule := ""
	switch {
	case d.Mode == "block" && d.Gate:
		status = "denied" // 経路そのものを止めた(既定の拒否・旧 routes)
	case d.Mode == "block": // delivery の行で止める: 承認は求めず、下の Deliver が止めて送信者に返す
	default:
		v, rule := j.Of("send:"+s.Kind+":"+s.To, s.From)
		apRule = rule
		switch v {
		case "allow":
		case "deny":
			status = "denied"
		default:
			status = "pending"
		}
		reason += " / 認可: " + rule
		// リスク評価(第 1 段: 参考値を reason に残すだけ。status は変えない)
		reason += " / " + approval.Eval(j.Collect(s.From, s.To, s.Kind, s.Text)).Oneline()
	}
	oid, err := insertOutbox(db, s, status, reason, now)
	if err != nil {
		return Sent{}, err
	}
	r := Sent{OID: oid}
	id := "O-" + strconv.FormatInt(oid, 10)
	if s.ReplyTo != "" {
		var rcheck sql.NullString
		_ = db.QueryRow(`SELECT reply_check FROM outbox WHERE id=?`, oid).Scan(&rcheck)
		if rcheck.String != "ok" && status != "denied" {
			fmt.Fprintf(stderr, "tb: --reply-to %s を返信と認めませんでした(%s)。返信ではない送信として通常の規則(delivery)で扱います: %s\n", s.ReplyTo, rcheck.String, id)
		}
	}
	switch status {
	case "denied":
		fmt.Fprintf(stderr, "tb: 送信を拒否しました(%s): %s\n", reason, id)
		r.Code = 4
	case "pending":
		r.Q = q(oid)
		fmt.Fprintf(stderr, "tb: 承認待ちです(認可: %s)。人(か approve: を許された agent)が tb approval approve %s で届けます\n", apRule, r.Q)
		r.Code = 20
	default:
		dr := Deliver(j, oid, true, io.Discard, stderr)
		r.Code, r.Held, r.Blocked = dr.Code, dr.Held, dr.Blocked
		notice := dr.Notice
		// 受け付けてから届けるまでの間に、別の tb flush が同じ行を止めた・保留したなら、その結果を送信者に返す(行を読み直す)
		if r.Code == 0 && r.Blocked+r.Held == "" {
			var st2 sql.NullString
			_ = db.QueryRow(`SELECT status || '|' || (reason LIKE '% → blocked') FROM outbox WHERE id=?`, oid).Scan(&st2)
			switch {
			case st2.String == "denied|1":
				var r2 string
				_ = db.QueryRow(`SELECT reason FROM outbox WHERE id=?`, oid).Scan(&r2)
				if i := strings.LastIndex(r2, " / "); i >= 0 {
					r2 = r2[i+3:]
				}
				r2 = strings.TrimSuffix(r2, " → blocked")
				r.Blocked, notice = id, BlockNotice(db.DB, db.Session, oid, s.To, s.Kind, r2)
			case strings.HasPrefix(st2.String, "sent"):
				var h sql.NullString
				_ = db.QueryRow(`SELECT 'I-'||id FROM inbox WHERE outbox_id=?`, oid).Scan(&h)
				r.Held = h.String
			}
		}
		if r.Code == 0 && r.Blocked != "" {
			fmt.Fprintf(stderr, "tb: %s\n", notice)
			r.Code = 4
		}
	}
	return r, nil
}

// insertOutbox は outbox に 1 行足す。--reply-to の主張は本文ではなく DB で確かめる(1 文で決めて書く: 同時に 2 つ来ても、ok になるのは 1 つ)。
// ok の条件: 元の送信があり、「この送信の宛先 → この送信の送信者」で、届いていて(sent)、まだ ok の返信が無い
func insertOutbox(db *store.DB, s SendSpec, status, reason, now string) (int64, error) {
	var rid any
	rcheck := "NULL"
	args := []any{db.Session, s.From, s.Pane, s.Session, s.To, s.Kind, s.Task, s.Text, status, reason, now}
	if s.ReplyTo != "" {
		n, _ := strconv.ParseInt(strings.TrimPrefix(s.ReplyTo, "O-"), 10, 64)
		rid = n
		rcheck = `CASE
		  WHEN ?9='denied' THEN 'ルールで拒否された送信'
		  WHEN NOT EXISTS (SELECT 1 FROM outbox WHERE id=?12) THEN '元の送信 ' || ?13 || ' が見つからない'
		  WHEN NOT EXISTS (SELECT 1 FROM outbox WHERE id=?12 AND from_name=?5 AND target=?2)
		    THEN ?13 || ' は ' || ?5 || ' から ' || ?2 || ' への送信ではない'
		  WHEN (SELECT status FROM outbox WHERE id=?12) != 'sent' THEN ?13 || ' はまだ届いていない(' || (SELECT status FROM outbox WHERE id=?12) || ')'
		  WHEN EXISTS (SELECT 1 FROM outbox WHERE reply_to=?12 AND reply_check='ok')
		    THEN ?13 || ' には返信済み(O-' || (SELECT MIN(id) FROM outbox WHERE reply_to=?12 AND reply_check='ok') || ')'
		  ELSE 'ok' END`
	}
	args = append(args, rid)
	if s.ReplyTo != "" {
		args = append(args, s.ReplyTo)
	}
	res, err := db.Exec(`INSERT INTO main.outbox (herdr_session, from_name, from_pane, from_session, target, kind, task_id, text, status, reason, created_at, reply_to, reply_check)
	  VALUES (?1, ?2, ?3, ?4, ?5, ?6, NULLIF(?7,''), ?8, ?9, ?10, ?11, ?12, `+rcheck+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Flush は cmd_flush: 積まれた送信を届ける。宛先ごとに古い順で、使用中の宛先は次回に回す。
// 停止中の宛先には送らず飛ばす(revive すれば次の flush で届く)。停止中かは、最初に 1 回 herdr の一覧と台帳を突き合わせて決める。
// 戻り値は tb の終了コード(届けられなかった宛先の最大のコード。無ければ 0)。flush は w の書きかけを先に出す。
func Flush(db *store.DB, w, stderr io.Writer, flush func()) (int, error) {
	var one int
	if db.QueryRow(`SELECT 1 FROM outbox WHERE status='queued' LIMIT 1`).Scan(&one) != nil {
		return 0, nil
	}
	// 台帳を herdr の一覧と突き合わせる(台帳の stopped が古いままのことがある)。herdr に届かなければ判定せず、今までどおり送ってみる
	synced := agents.Sync(db.DB, db.Session) == nil
	type row struct {
		id     int64
		target string
	}
	var rows []row
	if err := store.Each(db.DB, `SELECT id, target FROM outbox WHERE status='queued' ORDER BY id`, nil, func(_ []string, v []any) error {
		n, _ := v[0].(int64)
		rows = append(rows, row{n, tickets.Text(v[1])})
		return nil
	}); err != nil {
		return 0, err
	}
	j := approval.NewJudge(db.DB, db.Session)
	busy, held := map[string]bool{}, map[string]bool{}
	rc := 0
	for _, r := range rows {
		if busy[r.target] || held[r.target] { // 同じ宛先の後続は順序を保つため送らない
			continue
		}
		if synced && targetStopped(db, r.target) {
			held[r.target] = true
			flush()
			fmt.Fprintf(stderr, "tb: 宛先 '%s' は停止中のため送りません(queued のまま。tb agent-revive %s のあとの flush で届きます)\n", r.target, r.target)
			continue
		}
		flush()
		res := Deliver(j, r.id, false, w, stderr) // 止めた(block)ものは 0。同じ宛先の後続も続けて判定する
		if res.Code != 0 {
			busy[r.target] = true
			rc = max(rc, res.Code)
		}
	}
	return rc, nil
}

// targetStopped は outbox_target_stopped: 宛先が台帳で停止中か。
func targetStopped(db *store.DB, name string) bool {
	var one int
	return db.QueryRow(`SELECT 1 FROM agents WHERE name=? AND state='stopped' LIMIT 1`, name).Scan(&one) == nil
}
