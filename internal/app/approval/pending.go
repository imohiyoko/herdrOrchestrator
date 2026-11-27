package approval

import (
	"context"
	"database/sql"
	"io"
	"strconv"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	japproval "github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/herdr"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

// Priorities は承認待ちの優先度の名前 → 値。
var Priorities = map[string]int{"low": 0, "normal": 1, "high": 2}

// PriorityName は優先度の値 → 名前(知らない値は normal)。
func PriorityName(n int) string {
	for k, v := range Priorities {
		if v == n {
			return k
		}
	}
	return "normal"
}

// PendingQ は app.Env.PendingQ の中身: 今積んだ送信・agent の操作の承認待ち(source の sid)の Q-<n> を返す。
// priority が normal 以外ならその優先度を付け、決められる agent にも知らせる(同じ Q-<n> は 1 度だけ)。
func PendingQ(priority int) func(db *sql.DB, source string, sid int64) string {
	return func(db *sql.DB, source string, sid int64) string {
		if priority != 1 {
			_, _ = db.Exec(`UPDATE main.requests SET priority=? WHERE source=? AND source_id=?`, priority, source, sid)
		}
		var id int64
		var session string
		if db.QueryRow(`SELECT id, herdr_session FROM requests WHERE source=? AND source_id=?`, source, sid).Scan(&id, &session) != nil {
			return ""
		}
		NotifyApprovers(db, session, id)
		return "Q-" + strconv.FormatInt(id, 10)
	}
}

// decideAct は承認の権限の行(approve:<act> <依頼者>)に使う操作名。空なら人だけが決められる。
// 送信とコマンドは r.Act()。agent の操作は承認の入口と同じ agent.OpAction で決める。deny の記録は人だけ。
func decideAct(db *store.DB, r requests.Request) string {
	if r.Status == "denied" {
		return ""
	}
	if r.Source == "agent_ops" {
		o, ok := agent.OpRowOf(db, r.SourceID.Int64)
		if !ok {
			return ""
		}
		return agent.OpAction(db, o)
	}
	return r.Act()
}

// CanDecide は who が承認待ち r を決められるか(自分の依頼は決められない。人は常に決められる)。
func CanDecide(db *store.DB, j *japproval.Judge, r requests.Request, who string) bool {
	if who == "human" {
		return true
	}
	if who == "" || who == r.Requester {
		return false
	}
	act := decideAct(db, r)
	if act == "" {
		return false
	}
	_, ok := j.GrantOf(who, "approve:"+act, r.Requester)
	return ok
}

// NotifyApprovers は承認待ち Q-<n> ができたことを、それを決められる agent の pane に 1 通ずつ知らせる(送信の経路は通さない)。
// 同じ Q-<n> を 2 度知らせない(messages の kind=approval-pending, ref=Q-<n> で見る)。使用中・不在の agent には送らない(tb approval list --mine で見られる)。
// 本文は依頼者・要る権限・対象だけ(送る文面やコマンドの引数は載せない)。
func NotifyApprovers(sdb *sql.DB, session string, qid int64) {
	db := &store.DB{DB: sdb, Session: session}
	r, ok, err := requests.Load(sdb, qid)
	if err != nil || !ok {
		return
	}
	var n int
	if sdb.QueryRow(`SELECT count(*) FROM messages WHERE kind='approval-pending' AND ref=?`, r.QID()).Scan(&n) != nil || n > 0 {
		return
	}
	as, err := herdr.Agents()
	if err != nil {
		return
	}
	j := japproval.NewJudge(sdb, session)
	for _, a := range as {
		if a.Name == "" || !CanDecide(db, j, r, a.Name) {
			continue
		}
		msg := "[tb approval / " + r.QID() + "] " + r.Requester + " からの承認待ちです(" + r.Op + " " + r.Target + ")。tb approval approve " + r.QID() +
			" か tb approval reject " + r.QID() + " で決めてください(自分が決められる一覧: tb approval list --mine)"
		transport.Send(sdb, session, a.Name, msg, r.QID(), "approval-pending", io.Discard)
	}
}

// PriorityReq は approval.priority の入力(tb approval priority Q-<n> high|normal|low)。
type PriorityReq struct {
	ID       string `api:"id,path"`
	Priority string `api:"priority"`
}

// PriorityResult は付けた優先度。
type PriorityResult struct {
	ID       string `api:"id" json:"id"`
	Priority string `api:"priority" json:"priority"`
}

func priorityOp(env *app.Env) app.Op {
	return app.Command(env, "approval.priority", app.Entry("cmd:approval:priority", "tb approval priority"),
		func(ctx context.Context, in PriorityReq) (PriorityResult, error) {
			p, ok := Priorities[in.Priority]
			if !ok {
				return PriorityResult{}, define.Errorf(define.CodeInvalidArgument, "優先度は high / normal / low のどれかです: %s", in.Priority)
			}
			if !requests.ReQID.MatchString(in.ID) {
				return PriorityResult{}, define.Errorf(define.CodeInvalidArgument, "approval priority Q-<n> high|normal|low")
			}
			pr, _ := define.PrincipalFrom(ctx)
			who := app.Who(pr)
			db, done, err := env.DB()
			if err != nil {
				return PriorityResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			r, found, err := requests.Load(db.DB, requests.Num(in.ID))
			if err != nil {
				return PriorityResult{}, define.Errorf(define.CodeInternal, "requests を読めません: %v", err)
			}
			if !found || who == "" || (who != "human" && who != r.Requester) {
				return PriorityResult{}, define.Errorf(define.CodePermissionDenied, "%s の優先度を変えられるのは、依頼した agent と人だけです", in.ID)
			}
			if _, err := db.Exec(`UPDATE main.requests SET priority=? WHERE id=?`, p, r.ID); err != nil {
				return PriorityResult{}, define.Errorf(define.CodeInternal, "requests に書けません: %v", err)
			}
			return PriorityResult{ID: r.QID(), Priority: PriorityName(p)}, nil
		},
		define.Doc("承認待ち Q-<n> の優先度を変える(tb approval priority。依頼した agent と人だけ)"),
		define.Authz("cmd:approval:priority", define.Resource("request", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/requests/{id}/priority"),
	)
}
