package message

// 報告コメントの配送(tb の deliver_comment)。tb の入口(comment / report / digest)とアプリケーション API が使う。

import (
	"database/sql"
	"io"
	"regexp"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var reSpawnedBy = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// JoinDeliverables は tickets.JoinDeliverables。
func JoinDeliverables(s string) (string, bool) { return tickets.JoinDeliverables(s) }

// CommentDelivery は報告コメントを配送した結果。
type CommentDelivery struct {
	// Already は配送済みだったときの配送の時刻(何もしていない)。
	Already string
	// Target は届けた宛先。RC は transport.Send の結果(0 届けた / 10 宛先が使用中 / 11 送れなかった)。
	Target string
	RC     int
}

// ErrCommentNotFound はコメントが無いとき(文言は tb と同じ)。
type ErrCommentNotFound struct{ Ref string }

func (e *ErrCommentNotFound) Error() string {
	return "コメントが見つかりません: C-" + e.Ref
}

// DeliverComment は報告コメント C-<ref>(ref は数字だけ。呼ぶ側で確かめる)を指揮役へ届ける。warn は送信の警告の書き込み先。
// 届いたら配送済みの印とタスクの報告済みを一緒に書く(片方だけ残ると、次の tb report が同じ報告を作り直して送る)。
func DeliverComment(db *store.DB, ref, now string, warn io.Writer) (CommentDelivery, error) {
	var tid, kind, text, dels, name, pane string
	var delivered sql.NullString
	err := db.QueryRow(`SELECT task_id, kind, text, deliverables, from_name, from_pane, delivered_at FROM comments WHERE id=`+ref).
		Scan(&tid, &kind, &text, &dels, &name, &pane, &delivered)
	if err == sql.ErrNoRows {
		return CommentDelivery{}, &ErrCommentNotFound{Ref: ref}
	}
	if err != nil {
		return CommentDelivery{}, err
	}
	if delivered.String != "" {
		return CommentDelivery{Already: delivered.String}, nil
	}
	var project string
	_ = db.QueryRow(`SELECT project FROM tasks WHERE id=?`, tid).Scan(&project)
	target := TargetFor(db.DB, db.Session, project)
	// tb agent-spawn で立てた子なら、指揮官が親にも伝えられるよう親の名前を付ける
	var sby string
	_ = db.QueryRow(`SELECT COALESCE(spawned_by,'') FROM agents WHERE name=?`, name).Scan(&sby)
	if reSpawnedBy.MatchString(sby) {
		sby = "spawned_by: " + sby + " "
	} else {
		sby = ""
	}
	d := ""
	if kind == "done" {
		d, _ = JoinDeliverables(dels)
	}
	msg := "[from: " + name + " / " + pane + " / " + tid + " / " + kind + "] " + sby + util.Oneline(text, 200)
	if d != "" {
		msg += " / 成果物: " + util.Oneline(d, 300)
	}
	msg += " (詳細: tb comments " + tid + ")"
	if rc := transport.Send(db.DB, db.Session, target, msg, "C-"+ref, "comment", warn); rc != transport.Sent {
		return CommentDelivery{Target: target, RC: rc}, nil
	}
	if err := MarkCommentDelivered(db, ref, now); err != nil {
		return CommentDelivery{}, err
	}
	return CommentDelivery{Target: target}, nil
}

// MarkCommentDelivered は配送の記録: delivered_at を書き、comment_marks_reported(done の報告コメントが届いたら、
// そのタスク(done のもの)を報告済みにする)を同じトランザクションで行う。ref は数字だけ。
func MarkCommentDelivered(db *store.DB, ref, ts string) error {
	st := tickets.LoadStatuses(db.DB, db.Session)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE main.comments SET delivered_at=? WHERE herdr_session=? AND id=?`, ts, db.Session, ref); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE main.tasks SET reported_at=?1, updated_at=?1
		WHERE herdr_session=?2 AND status IN (`+st.SQLList("done")+`) AND reported_at IS NULL
		AND id=(SELECT task_id FROM comments WHERE id=?3 AND kind='done')`, ts, db.Session, ref); err != nil {
		return err
	}
	return tx.Commit()
}
