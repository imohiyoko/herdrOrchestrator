// Package job は scheduler のジョブ定義のアプリケーション API(ADR 0008。tb のサービス job)。
// 書けるのは人だけ(tb job set / rm と同じ。agent セットが schedule を登録するのに使う)。
package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/jobs"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
)

// SetReq は job.set の入力(このセッションの定義 sched-job:<Name> を書く)。
// Settings なら上書き設定 sched-settings:<Name>、Shared なら共通の行(*)に書く。
// How は記録に残す理由(tb の set / edit)。空なら agent セットの書き方: 今と同じ本文なら書かない。
type SetReq struct {
	Name     string `api:"name,path"`
	Body     string `api:"body"`
	Settings bool   `api:"settings,optional"`
	Shared   bool   `api:"shared,optional"`
	How      string `api:"how,optional"`
}

// UnsetReq は job.unset の入力(DB の行を消す。tb job unset)。
type UnsetReq struct {
	Name     string `api:"name,path"`
	Settings bool   `api:"settings,optional"`
	Shared   bool   `api:"shared,optional"`
}

// RmReq は job.rm の入力(定義と上書き設定の行を消す)。
// Strict なら消す行が無いときは断る(tb job rm)。RemoveLog なら、どのセッションにも定義が残らないときに scheduler のログも消す。
type RmReq struct {
	Name      string `api:"name,path"`
	Shared    bool   `api:"shared,optional"`
	Strict    bool   `api:"strict,optional"`
	RemoveLog bool   `api:"remove_log,optional"`
}

// Result は job.set / job.unset / job.rm の結果。Changed は書いた・消した(rm で行が無ければ false)。
// Source は操作のあとに読まれる定義の出どころ(db:<セッション> / db:* / ファイル / none)。
// Gone は rm で消した行の key、LogRemoved は消したログのファイル、LogFailed は消せなかったログのファイル(行は消してある)。
// Rejected は set で scheduler が本文を受け付けなかった理由(書いていない。How を渡したときだけ。空なら agent セットの書き方で断る)。
type Result struct {
	Name       string   `api:"name" json:"name"`
	Changed    bool     `api:"changed" json:"changed"`
	Source     string   `api:"source,optional" json:"source,omitempty"`
	Found      bool     `api:"found,optional" json:"found,omitempty"`
	Gone       []string `api:"gone,optional" json:"gone,omitempty"`
	LogRemoved string   `api:"log_removed,optional" json:"log_removed,omitempty"`
	LogFailed  string   `api:"log_failed,optional" json:"log_failed,omitempty"`
	Rejected   string   `api:"rejected,optional" json:"rejected,omitempty"`
	// Pending は承認待ちに積んだとき(agent の呼び出しで policy が ask のとき)。
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *Result) SetPending(p *app.Pending) { r.Pending = p }

// schedEnv は env の scheduler の置き場所(空のところは tb と同じ既定)。
func schedEnv(env *app.Env) jobs.Env {
	e := jobs.DefaultEnv()
	if env.SchedDir != "" {
		e.Dir = env.SchedDir
	}
	if env.SchedShow != "" {
		e.Show = env.SchedShow
	}
	return e
}

// logDir は scheduler がログを書く所。
func logDir(env *app.Env, e jobs.Env) string {
	if env.SchedLogs != "" {
		return env.SchedLogs
	}
	return jobs.LogDir(e)
}

func badName(name string) error {
	return define.Errorf(define.CodeInvalidArgument, "ジョブ名は英数字で始まり、英数字・_・- の 64 字以内(dispatch は予約名): %s", name)
}

func ierr(err error) error { return define.Errorf(define.CodeInternal, "%s", err.Error()) }

// Ops はジョブの操作。
func Ops(env *app.Env) []app.Op {
	set := app.Command(env, "job.set", app.Cmd("cmd:job:set", "tb job set"),
		func(_ context.Context, in SetReq) (Result, error) { return setJob(env, in) },
		define.Doc("scheduler のジョブ定義か上書き設定を書く(tb job set / edit。人だけ)"),
		define.Authz("cmd:job:set", define.Resource("job", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("PUT /v1/jobs/{name}"),
	)
	unset := app.Command(env, "job.unset", app.Cmd("cmd:job:unset", "tb job unset"),
		func(_ context.Context, in UnsetReq) (Result, error) { return unsetJob(env, in) },
		define.Doc("ジョブ定義か上書き設定の DB の行を消す(tb job unset。人だけ)"),
		define.Authz("cmd:job:unset", define.Resource("job", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/jobs/{name}/unset"),
	)
	rm := app.Command(env, "job.rm", app.Cmd("cmd:job:rm", "tb job rm"),
		func(_ context.Context, in RmReq) (Result, error) { return rmJob(env, in) },
		define.Doc("scheduler のジョブ定義と上書き設定を消す(tb job rm。人だけ)"),
		define.Authz("cmd:job:rm", define.Resource("job", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("DELETE /v1/jobs/{name}"),
	)
	return []app.Op{set, unset, rm, runOp(env)}
}

func setJob(env *app.Env, in SetReq) (Result, error) {
	if !jobs.ValidName(in.Name) {
		return Result{}, badName(in.Name)
	}
	if in.How == "" && in.Body == "" { // agent セットの書き方(前と同じく空の本文は断る)
		return Result{}, define.Errorf(define.CodeInvalidArgument, "本文が空です")
	}
	db, done, err := env.DB()
	if err != nil {
		return Result{}, ierr(err)
	}
	defer done()
	e := schedEnv(env)
	key := jobs.Key(in.Name, in.Settings)
	// scheduler が読めることを確かめてから書く(設定なら今の定義と組み合わせて)
	var conf, settings string
	if in.Settings {
		c, _, ok := e.Lookup(db.DB, db.Session, in.Name, false)
		if !ok {
			return Result{}, define.Errorf(define.CodeFailedPrecondition, "ジョブ %s の定義が DB にありません(先に tb job set %s か tb job import %s で置く)", in.Name, in.Name, in.Name)
		}
		conf, settings = c, in.Body
	} else {
		conf = in.Body
		settings, _, _ = e.Lookup(db.DB, db.Session, in.Name, true)
	}
	if conf == "" {
		if in.How == "" {
			return Result{}, define.Errorf(define.CodeInvalidArgument, "本文が空です")
		}
		return Result{}, define.Errorf(define.CodeFailedPrecondition, "本文が空です")
	}
	ok, last, err := e.Check(in.Name, conf, settings)
	if err != nil {
		if in.How == "" {
			return Result{}, define.Errorf(define.CodeInternal, "一時ディレクトリを作れません")
		}
		return Result{}, define.Errorf(define.CodeFailedPrecondition, "一時ディレクトリを作れません")
	}
	if !ok {
		if in.How == "" {
			return Result{}, define.Errorf(define.CodeInvalidArgument, "scheduler がこの本文を受け付けません: %s", last)
		}
		return Result{Name: in.Name, Rejected: last}, nil
	}
	how := in.How
	if how == "" {
		if cur, src, has := e.Lookup(db.DB, db.Session, in.Name, in.Settings); has && src == "db:"+db.Session && !in.Shared && strings.TrimRight(cur, "\n") == strings.TrimRight(in.Body, "\n") {
			return Result{Name: in.Name, Source: src}, nil
		}
		how = "set"
	}
	if err := config.DBPut(db.DB, db.Session, key, in.Body, how, in.Shared, ""); err != nil {
		return Result{}, ierr(err)
	}
	_, src, _ := e.Lookup(db.DB, db.Session, in.Name, in.Settings)
	return Result{Name: in.Name, Changed: true, Source: src}, nil
}

func unsetJob(env *app.Env, in UnsetReq) (Result, error) {
	if !jobs.ValidName(in.Name) {
		return Result{}, badName(in.Name)
	}
	db, done, err := env.DB()
	if err != nil {
		return Result{}, ierr(err)
	}
	defer done()
	key := jobs.Key(in.Name, in.Settings)
	which := "このセッションの行"
	if in.Shared {
		which = "共通の行"
	}
	if ok, err := config.HasRow(db.DB, db.Session, key, in.Shared); err != nil {
		return Result{}, ierr(err)
	} else if !ok {
		return Result{}, define.Errorf(define.CodeFailedPrecondition, "%s の DB の行はありません(%s)", key, which)
	}
	if err := config.Unset(db.DB, db.Session, key, in.Shared); err != nil {
		return Result{}, ierr(err)
	}
	_, src, _ := schedEnv(env).Lookup(db.DB, db.Session, in.Name, in.Settings)
	return Result{Name: in.Name, Changed: true, Source: src}, nil
}

func rmJob(env *app.Env, in RmReq) (Result, error) {
	if !jobs.ValidName(in.Name) {
		return Result{}, badName(in.Name)
	}
	db, done, err := env.DB()
	if err != nil {
		return Result{}, ierr(err)
	}
	defer done()
	// 定義と上書き設定は 1 つのトランザクションで消す(片方だけ残さない)
	tx, err := db.Begin()
	if err != nil {
		return Result{}, ierr(err)
	}
	defer tx.Rollback()
	r := Result{Name: in.Name}
	for _, st := range []bool{false, true} {
		key := jobs.Key(in.Name, st)
		ok, err := config.UnsetTx(tx, db.Session, key, in.Shared)
		if err != nil {
			return Result{}, ierr(err)
		}
		if ok {
			r.Gone = append(r.Gone, key)
		}
	}
	r.Changed = len(r.Gone) > 0
	if !r.Changed && in.Strict {
		which := "このセッション " + db.Session + " の行"
		if in.Shared {
			which = "共通の行"
		}
		return Result{}, define.Errorf(define.CodeFailedPrecondition, "ジョブ %s の DB の行はありません(%s)", in.Name, which)
	}
	if err := tx.Commit(); err != nil {
		return Result{}, ierr(err)
	}
	e := schedEnv(env)
	_, r.Source, r.Found = e.Lookup(db.DB, db.Session, in.Name, false)
	if !in.RemoveLog || !r.Changed {
		return r, nil
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM main.config WHERE key=?`, jobs.Key(in.Name, false)).Scan(&left); err != nil {
		return Result{}, ierr(err)
	}
	if left > 0 {
		return r, nil
	}
	log := filepath.Join(logDir(env, e), in.Name+".log")
	if jobs.IsRegular(log) && !jobs.IsLink(log) {
		if os.Remove(log) != nil {
			r.LogFailed = log
		} else {
			r.LogRemoved = log
		}
	}
	return r, nil
}
