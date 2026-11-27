// Package recipe はレシピの登録・実行・実行の依頼の取り消しのアプリケーション API(ADR 0008。tb recipe set / rm / run / runner / cancel)。
// 書式の読み取りと部品は internal/recipe。登録と削除は既定で人だけ(commands.tbl の inner の行)。
// 実行は run.go。部品はアプリケーション API の中身(agent.Stop、ticket.Show など)を直接呼ぶ。
package recipe

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/recipe"
)

var reRid = regexp.MustCompile(`^R-([0-9]{1,18})$`)

// SetReq は recipe.set の入力。
type SetReq struct {
	Name string `api:"name,path"`
	Body string `api:"body"`
}

// SetResult は登録の結果。Added は新しく足した(false なら書き換えた)。Decl は本文で宣言した risk(Risk より低ければ Risk に上げた)。
type SetResult struct {
	Name    string       `api:"name" json:"name"`
	Added   bool         `api:"added" json:"added"`
	Risk    string       `api:"risk" json:"risk"`
	Steps   int          `api:"steps" json:"steps"`
	Decl    string       `api:"decl,optional" json:"decl,omitempty"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *SetResult) SetPending(p *app.Pending) { r.Pending = p }

// NameReq は名前だけを受ける入力。
type NameReq struct {
	Name string `api:"name,path"`
}

// RmResult は消したレシピ。
type RmResult struct {
	Name    string       `api:"name" json:"name"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *RmResult) SetPending(p *app.Pending) { r.Pending = p }

// CancelReq は recipe.cancel の入力(R-<n>)。
type CancelReq struct {
	ID string `api:"id,path"`
}

// CancelResult は取り消した依頼。
type CancelResult struct {
	ID string `api:"id" json:"id"`
}

// Ops はレシピの操作。
func Ops(env *app.Env) []app.Op {
	set := app.Command(env, "recipe.set", app.Cmd("cmd:recipe:set", "tb recipe-set"),
		func(ctx context.Context, in SetReq) (SetResult, error) {
			return withDB(env, func(db *store.DB) (SetResult, error) { return set(env, db, who(ctx), in) })
		},
		define.Doc("レシピを登録する・書き換える(tb recipe set。書式の誤りは登録しない。既定は人だけ)"),
		define.Authz("cmd:recipe:set", define.Resource("recipe", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("PUT /v1/recipes/{name}"),
	)
	rm := app.Command(env, "recipe.rm", app.Cmd("cmd:recipe:rm", "tb recipe-rm"),
		func(ctx context.Context, in NameReq) (RmResult, error) {
			return withDB(env, func(db *store.DB) (RmResult, error) { return rm(env, db, who(ctx), in.Name) })
		},
		define.Doc("レシピを消す(tb recipe rm。既定は人だけ)"),
		define.Authz("cmd:recipe:rm", define.Resource("recipe", "name")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("DELETE /v1/recipes/{name}"),
	)
	cancel := app.Command(env, "recipe.cancel", app.Entry("cmd:recipe:cancel", "tb recipe-cancel"),
		func(ctx context.Context, in CancelReq) (CancelResult, error) {
			return withDB(env, func(db *store.DB) (CancelResult, error) { return Cancel(env, db, who(ctx), in.ID) })
		},
		define.Doc("待ち行列の実行の依頼を取り消す(tb recipe cancel。人と、頼んだ agent 自身だけ)"),
		define.Authz("cmd:recipe:cancel", define.Resource("recipe_run", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/recipe-runs/{id}/cancel"),
	)
	return append([]app.Op{set, rm, cancel}, runOps(env)...)
}

func who(ctx context.Context) string {
	p, _ := define.PrincipalFrom(ctx)
	return app.Who(p)
}

// changer は履歴の「変えた者」(人と system:* は human)。決められない主体は断る。
func changer(who string) (string, error) {
	switch {
	case who == "":
		return "", define.Errorf(define.CodePermissionDenied, "呼び手を決められません")
	case who == "human" || strings.HasPrefix(who, "system:"):
		return "human", nil
	}
	return who, nil
}

func withDB[T any](env *app.Env, f func(*store.DB) (T, error)) (T, error) {
	var zero T
	db, done, err := env.DB()
	if err != nil {
		return zero, ierr(err)
	}
	defer done()
	return f(db)
}

func ierr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*define.Error); ok {
		return err
	}
	return define.Errorf(define.CodeInternal, "%s", err.Error())
}

func validName(n string) error {
	if !recipe.NameRE.MatchString(n) {
		return define.Errorf(define.CodeInvalidArgument, "レシピ名は英小文字で始まる英小文字・数字・- の 32 字以内(: は使えない): %s", n)
	}
	return nil
}

func set(env *app.Env, db *store.DB, who string, in SetReq) (SetResult, error) {
	if err := validName(in.Name); err != nil {
		return SetResult{}, err
	}
	by, err := changer(who)
	if err != nil {
		return SetResult{}, err
	}
	body := in.Body
	r, why := recipe.Parse(body)
	if why != "" {
		return SetResult{}, define.Errorf(define.CodeInvalidArgument, "recipe-set: %s", strings.ReplaceAll(why, "\n", " / "))
	}
	ts := env.NowString()
	tx, err := db.Begin()
	if err != nil {
		return SetResult{}, ierr(err)
	}
	// 前の本文は同じトランザクションで読む(同時に足しても履歴の add / update と old_body が上書きした行と合う)
	var old string
	exists := tx.QueryRow(`SELECT body FROM recipes WHERE name=?`, in.Name).Scan(&old) == nil
	how := "add"
	var oldv any
	if exists {
		how, oldv = "update", old
	}
	_, err = tx.Exec(`INSERT INTO main.recipes (herdr_session, name, body, risk, steps, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(herdr_session, name) DO UPDATE SET body=excluded.body, risk=excluded.risk, steps=excluded.steps,
        updated_by=excluded.updated_by, updated_at=excluded.updated_at`, db.Session, in.Name, body, r.Risk, len(r.Steps), by, ts)
	if err == nil {
		_, err = tx.Exec(`INSERT INTO main.recipe_changes (herdr_session, name, how, old_body, new_body, risk, changed_by, changed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			db.Session, in.Name, how, oldv, body, r.Risk, by, ts)
	}
	if err != nil {
		_ = tx.Rollback()
		return SetResult{}, ierr(err)
	}
	if err := tx.Commit(); err != nil {
		return SetResult{}, ierr(err)
	}
	return SetResult{Name: in.Name, Added: !exists, Risk: r.Risk, Steps: len(r.Steps), Decl: r.Decl}, nil
}

func rm(env *app.Env, db *store.DB, who, name string) (RmResult, error) {
	if err := validName(name); err != nil {
		return RmResult{}, err
	}
	by, err := changer(who)
	if err != nil {
		return RmResult{}, err
	}
	var old string
	if db.QueryRow(`SELECT body FROM recipes WHERE name=?`, name).Scan(&old) != nil {
		return RmResult{}, define.Errorf(define.CodeNotFound, "レシピが登録されていません: %s", name)
	}
	tx, err := db.Begin()
	if err != nil {
		return RmResult{}, ierr(err)
	}
	_, err = tx.Exec(`DELETE FROM main.recipes WHERE herdr_session=? AND name=?`, db.Session, name)
	if err == nil {
		_, err = tx.Exec(`INSERT INTO main.recipe_changes (herdr_session, name, how, old_body, new_body, risk, changed_by, changed_at) VALUES (?, ?, 'rm', ?, NULL, NULL, ?, ?)`,
			db.Session, name, old, by, env.NowString())
	}
	if err != nil {
		_ = tx.Rollback()
		return RmResult{}, ierr(err)
	}
	if err := tx.Commit(); err != nil {
		return RmResult{}, ierr(err)
	}
	return RmResult{Name: name}, nil
}

// Cancel は recipe-cancel: 待ち行列の queued を取り消す。人と、頼んだ agent 自身だけ。
func Cancel(env *app.Env, db *store.DB, who, rid string) (CancelResult, error) {
	m := reRid.FindStringSubmatch(rid)
	if m == nil {
		return CancelResult{}, define.Errorf(define.CodeInvalidArgument, "recipe-cancel R-<n>")
	}
	idS := m[1]
	id, perr := strconv.ParseInt(idS, 10, 64)
	var requester, status string
	if perr != nil || db.QueryRow(`SELECT requester, status FROM recipe_runs WHERE id=?`, id).Scan(&requester, &status) != nil {
		return CancelResult{}, define.Errorf(define.CodeNotFound, "実行の依頼が見つかりません: R-%s", idS)
	}
	by, err := changer(who)
	if err != nil {
		return CancelResult{}, err
	}
	if by != "human" && by != requester {
		return CancelResult{}, define.Errorf(define.CodePermissionDenied, "R-%s を取り消せるのは人と、頼んだ agent(%s)だけです", idS, requester)
	}
	if status != "queued" {
		return CancelResult{}, define.Errorf(define.CodeFailedPrecondition, "R-%s は待ち行列にありません(%s)", idS, status)
	}
	label := by
	if by == "human" {
		label = "人"
	}
	RunClose(env, db, id, "cancelled", label+"が取り消し", "queued", by)
	var cur string
	_ = db.QueryRow(`SELECT status FROM recipe_runs WHERE id=?`, id).Scan(&cur)
	if cur != "cancelled" {
		return CancelResult{}, define.Errorf(define.CodeConflict, "R-%s は待ち行列にありません(ほかで決まりました)", idS)
	}
	return CancelResult{ID: "R-" + idS}, nil
}

// RunClose は _runner_close: 実行の依頼の状態を from から st に変え(変わったときだけ)、recipe_end を残す。actor が空なら依頼者。
func RunClose(env *app.Env, db *store.DB, id int64, st, why, from, actor string) {
	var task, requester string
	var eid int64
	if db.QueryRow(`SELECT task_id, requester, event_id FROM recipe_runs WHERE id=?`, id).Scan(&task, &requester, &eid) != nil {
		return
	}
	ts := env.NowString()
	res, err := db.Exec(`UPDATE main.recipe_runs SET status=?, result=?, finished_at=? WHERE herdr_session=? AND id=? AND status=?`, st, why, ts, db.Session, id, from)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
	nw := st + ": " + why
	if st == "done" {
		nw = "done"
	}
	if actor == "" {
		actor = requester
	}
	_, _ = db.Exec(`INSERT INTO main.task_events (herdr_session, task_id, kind, old, new, actor, ref, created_at) VALUES (?,?,'recipe_end',?,?,?,?,?)`,
		db.Session, task, fmt.Sprintf("R-%d", id), nw, actor, "E-"+strconv.FormatInt(eid, 10), ts)
}
