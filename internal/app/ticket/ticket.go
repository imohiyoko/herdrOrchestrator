// Package ticket はチケットのアプリケーション API(ADR 0008。tb のサービス ticket)。
package ticket

import (
	"context"
	"database/sql"
	"regexp"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// ResolveReq は ticket.resolve の入力。
type ResolveReq struct {
	// ID は報告の番号(C- を除いた数字。tb の文言に出すので、書かれたとおりの文字列で持つ)。
	ID   string `api:"id,path"`
	Note string `api:"note,optional"`
}

// ResolveResult は ticket.resolve の結果。
type ResolveResult struct {
	ID string `api:"id" json:"id"`
	// Already は、すでに対応済みだった(何も変えていない)。ResolvedAt はそのときの時刻。
	Already    bool   `api:"already" json:"already"`
	ResolvedAt string `api:"resolved_at,optional" json:"resolved_at,omitempty"`
	// Pending は承認待ちに積んだときだけ(そのとき ID ほかは空)。
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *ResolveResult) SetPending(p *app.Pending) { r.Pending = p }

var reDigits = regexp.MustCompile(`^[0-9]+$`)

// noteMax はメモの上限(文字数。tb resolve の -m と同じ)。
const noteMax = 500

// Ops はチケットの操作。
func Ops(env *app.Env) []app.Op {
	resolve := app.Command(env, "ticket.resolve",
		app.Cmd("cmd:ticket:resolve", "tb resolve"),
		func(ctx context.Context, in ResolveReq) (ResolveResult, error) { return resolveComment(env, in) },
		define.Doc("question / blocked の報告を対応済みにする(tb resolve)"),
		define.Authz("cmd:ticket:resolve", define.Resource("comment", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/comments/{id}/resolve"),
	)
	return append([]app.Op{resolve, getOp(env), showOp(env), countOp(env), moveOp(env), rmOp(env)}, append(append(stateOps(env), linkOps(env)...), append(append(editOps(env), commentOp(env), reopenOp(env), reportOp(env)), statusOps(env)...)...)...)
}

// ResolveComment は ticket.resolve の中身(門は通さない。レシピの部品 comment-resolve のように、呼び手が判定済みのとき)。
func ResolveComment(env *app.Env, in ResolveReq) (ResolveResult, error) {
	return resolveComment(env, in)
}

func resolveComment(env *app.Env, in ResolveReq) (ResolveResult, error) {
	ref := in.ID
	if !reDigits.MatchString(ref) {
		return ResolveResult{}, define.Errorf(define.CodeInvalidArgument, "resolve: ID は C-<数字>: %s", ref)
	}
	if len([]rune(in.Note)) > noteMax {
		return ResolveResult{}, define.Errorf(define.CodeInvalidArgument, "resolve: メモは %d 字まで", noteMax)
	}
	db, done, err := env.DB()
	if err != nil {
		return ResolveResult{}, internal(err)
	}
	defer done()
	var kind string
	var at sql.NullString
	err = db.QueryRow(`SELECT kind, resolved_at FROM comments WHERE id=`+ref).Scan(&kind, &at)
	if err == sql.ErrNoRows {
		return ResolveResult{}, define.Errorf(define.CodeNotFound, "報告が見つかりません: C-%s", ref)
	}
	if err != nil {
		return ResolveResult{}, internal(err)
	}
	if kind != "question" && kind != "blocked" {
		return ResolveResult{}, define.Errorf(define.CodeFailedPrecondition, "対応済みにできるのは question / blocked だけです(C-%s は %s)", ref, kind)
	}
	if at.String != "" {
		return ResolveResult{ID: ref, Already: true, ResolvedAt: at.String}, nil
	}
	if _, err := db.Exec(`UPDATE main.comments SET resolved_at=?, resolved_note=NULLIF(?,'') WHERE herdr_session=? AND id=`+ref+` AND resolved_at IS NULL`, env.NowString(), in.Note, db.Session); err != nil {
		return ResolveResult{}, internal(err)
	}
	return ResolveResult{ID: ref}, nil
}

// internal は DB などの失敗を、文言を保ったまま internal のエラーにする(tb は今と同じく文言を出して 1 で終わる)。
func internal(err error) error { return define.Errorf(define.CodeInternal, "%s", err.Error()) }

// ReopenReq は ticket.reopen の入力(ticket.resolve の逆)。
type ReopenReq struct {
	ID   string `api:"id,path"` // 報告の番号(C- を除いた数字)
	Note string `api:"note,optional"`
}

// ReopenResult は ticket.reopen の結果。NotResolved は対応済みではなかった(何も変えていない)。
type ReopenResult struct {
	ID          string       `api:"id" json:"id"`
	NotResolved bool         `api:"not_resolved" json:"not_resolved"`
	Pending     *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter の実装。
func (r *ReopenResult) SetPending(p *app.Pending) { r.Pending = p }

func reopenOp(env *app.Env) app.Op {
	return app.Command(env, "ticket.reopen",
		app.Cmd("cmd:ticket:reopen", "tb reopen"),
		func(ctx context.Context, in ReopenReq) (ReopenResult, error) { return reopenComment(env, who(ctx), in) },
		define.Doc("対応済みの question / blocked の報告を未対応に戻す(tb reopen)"),
		define.Authz("cmd:ticket:reopen", define.Resource("comment", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/comments/{id}/reopen"),
	)
}

func reopenComment(env *app.Env, who string, in ReopenReq) (ReopenResult, error) {
	ref := in.ID
	if !reDigits.MatchString(ref) {
		return ReopenResult{}, define.Errorf(define.CodeInvalidArgument, "reopen: ID は C-<数字>: %s", ref)
	}
	if len([]rune(in.Note)) > noteMax {
		return ReopenResult{}, define.Errorf(define.CodeInvalidArgument, "reopen: 理由は %d 字まで", noteMax)
	}
	db, done, err := env.DB()
	if err != nil {
		return ReopenResult{}, internal(err)
	}
	defer done()
	var kind, tid string
	var at sql.NullString
	err = db.QueryRow(`SELECT kind, resolved_at, task_id FROM comments WHERE id=`+ref).Scan(&kind, &at, &tid)
	if err == sql.ErrNoRows {
		return ReopenResult{}, define.Errorf(define.CodeNotFound, "報告が見つかりません: C-%s", ref)
	}
	if err != nil {
		return ReopenResult{}, internal(err)
	}
	if kind != "question" && kind != "blocked" {
		return ReopenResult{}, define.Errorf(define.CodeFailedPrecondition, "未対応に戻せるのは question / blocked だけです(C-%s は %s)", ref, kind)
	}
	if at.String == "" {
		return ReopenResult{ID: ref, NotResolved: true}, nil
	}
	if err := tickets.ReopenComment(db, ref, tid, in.Note, env.ActorOf(who), env.NowString()); err != nil {
		return ReopenResult{}, internal(err)
	}
	return ReopenResult{ID: ref}, nil
}
