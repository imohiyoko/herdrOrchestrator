// Package handoff は人への依頼文の操作(tb handoff write。ADR 0008)。
package handoff

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/handoff"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// WriteReq は handoff.write の入力。
type WriteReq struct {
	Task string `api:"task,path"`
}

// WriteResult は書いたファイル。
type WriteResult struct {
	Path string `api:"path" json:"path"`
}

// Ops は依頼文の操作。
func Ops(env *app.Env) []app.Op {
	return []app.Op{app.Command(env, "handoff.write", app.Entry("cmd:handoff:write", "tb handoff"),
		func(_ context.Context, in WriteReq) (WriteResult, error) { return write(env, in.Task) },
		define.Doc("人が貼る依頼文(目的・必要なアクセス・期限・返し先)を data/handoff/<task-id>.md に書くだけ。何も送らない(human_handoff が on のときだけ)"),
		define.Authz("cmd:handoff:write", define.Resource("ticket", "task")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/tickets/{task}/handoff"),
	)}
}

// refused は tb の終了コード 4 の断り。
func refused(msg string) error {
	return &define.Error{Code: define.CodeFailedPrecondition, Message: msg, Reasons: []string{agent.ReasonOpDenied}}
}

func write(env *app.Env, tid string) (WriteResult, error) {
	if !ticket.ReID.MatchString(tid) {
		return WriteResult{}, define.Errorf(define.CodeInvalidArgument, "タスク ID は T-<project>-NNN 形式: %s", tid)
	}
	db, done, err := env.DB()
	if err != nil {
		return WriteResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	defer done()
	if limits.Load(db.DB, db.Session).Get("human_handoff") != "on" {
		return WriteResult{}, refused("handoff は使えません(human_handoff が off です。使うのは人が tb limits set human_handoff on)。何も書きません")
	}
	title, summary, ok, err := tickets.TitleSummary(db.DB, tid)
	if err != nil {
		return WriteResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	if !ok {
		return WriteResult{}, define.Errorf(define.CodeNotFound, "タスクが見つかりません: %s", tid)
	}
	// bash は jq -r の出力を $(...) で受けるので、末尾の改行は落ちる
	text := handoff.Text(tid, strings.TrimRight(title, "\n"), strings.TrimRight(summary, "\n"))
	if hits := handoff.SecretScan(text); len(hits) > 0 {
		return WriteResult{}, refused("文面に秘密らしい文字列が含まれるため、書きません(検出: " + strings.Join(hits, " ") + " )。" +
			" タスクの題名・要約から取り除いてから、もう一度 tb handoff write " + tid + " を実行してください(本文は表示しません)")
	}
	dir := os.Getenv("TB_HANDOFF_DIR")
	if dir == "" {
		dir = filepath.Join(os.Getenv("TB_ROOT"), "data", "handoff")
	}
	f := filepath.Join(dir, tid+".md")
	if _, err := os.Lstat(f); err == nil {
		return WriteResult{}, define.Errorf(define.CodeFailedPrecondition, "もう あります: %s(人が編集している可能性があるので上書きしません。消してからやり直してください)", f)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return WriteResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	if err := os.WriteFile(f, []byte(text+"\n"), 0o644); err != nil {
		return WriteResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	return WriteResult{Path: f}, nil
}
