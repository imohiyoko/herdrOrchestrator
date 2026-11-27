package ticket

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ReportReq は ticket.report の入力(tb report <id> | report C-<n>)。Ref はチケットの ID か C-<n>(未配送の報告の再送)。
type ReportReq struct {
	Ref string `api:"ref,path"`
}

// ReportResult は ticket.report の結果。
// Reported は報告済みだった時刻(何もしていない)、Already はコメントが配送済みだった時刻、Code は送れなかったときの tb の終了コード(10 使用中 / 11 失敗)。
type ReportResult struct {
	ID       string `api:"id" json:"id"`
	Comment  string `api:"comment,optional" json:"comment,omitempty"` // C-<n>
	Target   string `api:"target,optional" json:"target,omitempty"`
	Reported string `api:"reported,optional" json:"reported,omitempty"`
	Already  string `api:"already,optional" json:"already,omitempty"`
	Code     int    `api:"code,optional" json:"code,omitempty"`
}

func reportOp(env *app.Env) app.Op {
	return app.Command(env, "ticket.report", app.Entry("cmd:message:report", "tb report"),
		func(_ context.Context, in ReportReq) (ReportResult, error) { return report(env, in) },
		define.Doc("完了の報告を指揮役へ届ける(tb report。C-<n> なら未配送の報告の再送)"),
		define.Authz("cmd:message:report", define.Resource("ticket", "ref")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets/{ref}/report"),
	)
}

func report(env *app.Env, in ReportReq) (ReportResult, error) {
	db, done, err := env.DB()
	if err != nil {
		return ReportResult{}, internal(err)
	}
	defer done()
	send := func(r ReportResult, ref string) (ReportResult, error) {
		d, err := message.DeliverComment(db, ref, env.NowString(), env.Warner())
		var nf *message.ErrCommentNotFound
		if errors.As(err, &nf) {
			return ReportResult{}, define.Errorf(define.CodeNotFound, "%s", nf.Error())
		}
		if err != nil {
			return ReportResult{}, internal(err)
		}
		r.Comment, r.Already, r.Code = "C-"+ref, d.Already, d.RC
		if r.Target == "" {
			r.Target = d.Target
		}
		return r, nil
	}
	if c, ok := strings.CutPrefix(in.Ref, "C-"); ok {
		if !reDigits.MatchString(c) {
			return ReportResult{}, define.Errorf(define.CodeInvalidArgument, "コメント ID は C-<数字>")
		}
		return send(ReportResult{}, c)
	}
	id := in.Ref
	if err := needID(id); err != nil {
		return ReportResult{}, err
	}
	var status, project string
	var reported sql.NullString
	err = db.QueryRow(`SELECT status, project, reported_at FROM tasks WHERE id=?`, id).Scan(&status, &project, &reported)
	if err == sql.ErrNoRows {
		return ReportResult{}, define.Errorf(define.CodeNotFound, "タスクが見つかりません: %s", id)
	}
	if err != nil {
		return ReportResult{}, internal(err)
	}
	st := tickets.LoadStatuses(db.DB, db.Session)
	if !st.Is(status, "done") {
		return ReportResult{}, define.Errorf(define.CodeFailedPrecondition, "done ではないタスクは report できません(%s): %s", status, id)
	}
	if reported.String != "" {
		return ReportResult{ID: id, Reported: reported.String}, nil
	}
	target := message.TargetFor(db.DB, db.Session, project)
	// 完了の報告も報告コメント(kind done の C-<n>)として届ける。未配送の done コメントがあればそれを送り直す
	var cid int64
	err = db.QueryRow(`SELECT id FROM comments WHERE task_id=? AND kind='done' AND delivered_at IS NULL ORDER BY id LIMIT 1`, id).Scan(&cid)
	if err == sql.ErrNoRows {
		res, err := db.Exec(`INSERT INTO main.comments (herdr_session, task_id, kind, text, deliverables, from_name, from_pane, from_session, created_at)
			SELECT ?, id, 'done', COALESCE(summary,''), deliverables, COALESCE(owner_agent,'-'), '-', COALESCE(session_id,'-'), ? FROM tasks WHERE id=?`, db.Session, env.NowString(), id)
		if err != nil {
			return ReportResult{}, internal(err)
		}
		cid, _ = res.LastInsertId()
	} else if err != nil {
		return ReportResult{}, internal(err)
	}
	return send(ReportResult{ID: id, Target: target}, strconv.FormatInt(cid, 10))
}
