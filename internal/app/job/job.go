// Package job は scheduler のジョブ定義のアプリケーション API(ADR 0008。tb のサービス job)。
// 書けるのは人だけ(tb job set / rm と同じ。agent セットが schedule を登録するのに使う)。
package job

import (
	"context"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/jobs"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

// SetReq は job.set の入力(このセッションの定義 sched-job:<Name> を書く)。
type SetReq struct {
	Name string `api:"name,path"`
	Body string `api:"body"`
}

// RmReq は job.rm の入力(このセッションの定義と上書き設定を消す。ログは残す)。
type RmReq struct {
	Name string `api:"name,path"`
}

// Result は job.set / job.rm の結果。Changed は書いた・消した(rm で行が無ければ false)。
type Result struct {
	Name    string `api:"name" json:"name"`
	Changed bool   `api:"changed" json:"changed"`
}

// Ops はジョブの操作。
func Ops(env *app.Env) []app.Op {
	set := app.Command(env, "job.set", app.Cmd("cmd:job:set", "tb job set"),
		func(_ context.Context, in SetReq) (Result, error) {
			if !jobs.ValidName(in.Name) {
				return Result{}, define.Errorf(define.CodeInvalidArgument, "ジョブ名は英数字で始まり、英数字・_・- の 64 字以内(dispatch は予約名): %s", in.Name)
			}
			if in.Body == "" {
				return Result{}, define.Errorf(define.CodeInvalidArgument, "本文が空です")
			}
			db, done, err := env.DB()
			if err != nil {
				return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			e := jobs.DefaultEnv()
			settings, _, _ := e.Lookup(db.DB, db.Session, in.Name, true)
			ok, last, err := e.Check(in.Name, in.Body, settings)
			if err != nil {
				return Result{}, define.Errorf(define.CodeInternal, "一時ディレクトリを作れません")
			}
			if !ok {
				return Result{}, define.Errorf(define.CodeInvalidArgument, "scheduler がこの本文を受け付けません: %s", last)
			}
			if cur, src, has := e.Lookup(db.DB, db.Session, in.Name, false); has && src == "db:"+db.Session && strings.TrimRight(cur, "\n") == strings.TrimRight(in.Body, "\n") {
				return Result{Name: in.Name}, nil
			}
			if err := config.DBPut(db.DB, db.Session, jobs.Key(in.Name, false), in.Body, "set", false, ""); err != nil {
				return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			return Result{Name: in.Name, Changed: true}, nil
		},
		define.Doc("scheduler のジョブ定義を書く(tb job set。人だけ)"),
		define.Authz("cmd:job:set", define.Resource("job", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("PUT /v1/jobs/{name}"),
	)
	rm := app.Command(env, "job.rm", app.Cmd("cmd:job:rm", "tb job rm"),
		func(_ context.Context, in RmReq) (Result, error) {
			if !jobs.ValidName(in.Name) {
				return Result{}, define.Errorf(define.CodeInvalidArgument, "ジョブ名は英数字で始まり、英数字・_・- の 64 字以内(dispatch は予約名): %s", in.Name)
			}
			db, done, err := env.DB()
			if err != nil {
				return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			tx, err := db.Begin()
			if err != nil {
				return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer tx.Rollback()
			r := Result{Name: in.Name}
			for _, st := range []bool{false, true} {
				ok, err := config.UnsetTx(tx, db.Session, jobs.Key(in.Name, st), false)
				if err != nil {
					return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
				}
				r.Changed = r.Changed || ok
			}
			if err := tx.Commit(); err != nil {
				return Result{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			return r, nil
		},
		define.Doc("scheduler のジョブ定義と上書き設定を消す(tb job rm。人だけ。ログは残す)"),
		define.Authz("cmd:job:rm", define.Resource("job", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("DELETE /v1/jobs/{name}"),
	)
	return []app.Op{set, rm}
}
