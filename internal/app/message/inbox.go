package message

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
)

// ReadReq は message.read の入力(tb inbox-read I-<n>。読んだ印を付ける)。
type ReadReq struct {
	ID string `api:"id,path"` // I-<n>
}

// ReadResult は印を付けた受信。Marked は今回付けた(既に読んでいれば false)。
type ReadResult struct {
	ID     string `api:"id" json:"id"`
	Marked bool   `api:"marked" json:"marked"`
}

func readOp(env *app.Env) app.Op {
	return app.Command(env, "message.read", app.Entry("cmd:message:read", "tb inbox-read"),
		func(ctx context.Context, in ReadReq) (ReadResult, error) {
			p, _ := define.PrincipalFrom(ctx)
			who := app.Who(p)
			if env.Reporter != nil { // tb の入口: herdr から引いた pane の持ち主
				n, _, _ := env.Reporter(who)
				who = n
			}
			if who == "" {
				return ReadResult{}, &define.Error{Code: define.CodeUnauthenticated, Message: "受信者を特定できません", Reasons: []string{app.ReasonNoSender}}
			}
			num := strings.TrimPrefix(in.ID, "I-")
			iid, err := strconv.ParseInt(num, 10, 64)
			if err != nil || num == "" || strings.Trim(num, "0123456789") != "" {
				return ReadResult{}, define.Errorf(define.CodeNotFound, "inbox に見つかりません: I-%s", num)
			}
			db, done, err := env.DB()
			if err != nil {
				return ReadResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			var one int
			if err := db.QueryRow(`SELECT 1 FROM inbox WHERE id=? AND recipient=?`, iid, who).Scan(&one); err == sql.ErrNoRows {
				return ReadResult{}, define.Errorf(define.CodeNotFound, "inbox に見つかりません: I-%s", num)
			} else if err != nil {
				return ReadResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			res, err := db.Exec(`UPDATE main.inbox SET read_at=? WHERE herdr_session=? AND id=? AND recipient=? AND read_at IS NULL`, env.NowString(), db.Session, iid, who)
			if err != nil {
				return ReadResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			n, _ := res.RowsAffected()
			return ReadResult{ID: "I-" + strconv.FormatInt(iid, 10), Marked: n == 1}, nil
		},
		define.Doc("受信に読んだ印を付ける(tb inbox-read。宛先の agent 自身だけ)"),
		define.Authz("cmd:message:read", define.Resource("inbox", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/inbox/{id}/read"),
	)
}
