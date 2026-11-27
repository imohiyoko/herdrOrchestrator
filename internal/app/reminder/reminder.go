// Package reminder はリマインダーの書き込みのアプリケーション API(ADR 0008。tb reminder publish / scan)。
// 材料集めと差分は internal/reminder、期限のイベントは internal/tickets。list / check(書かない)と nudge は tb に残る。
package reminder

import (
	"context"
	"strconv"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/reminder"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// PublishReq は reminder.publish の入力。Today は今日(空なら今日の日付。tb は TB_TODAY を確かめて渡す)。
type PublishReq struct {
	Today string `api:"today,optional"`
}

// PublishResult はまとめ。Text は人向けの本文(秘密らしい値は伏せた)、Items / Removed は --json の項目(JSON の配列)。
type PublishResult struct {
	Full    bool   `api:"full" json:"full"`
	Text    string `api:"text" json:"text"`
	Items   string `api:"items" json:"items"`
	Removed string `api:"removed" json:"removed"`
}

// ScanReq は reminder.scan の入力(tb reminder scan / due-scan)。
type ScanReq struct {
	Project string `api:"project,optional"`
	Within  int    `api:"within,optional"` // 期限間近とみなす日数(0〜999。既定 1)
	Today   string `api:"today,optional"`
}

// ScanResult は履歴に残した件数。
type ScanResult struct {
	Overdue int64 `api:"overdue" json:"overdue"`
	DueSoon int64 `api:"due_soon" json:"due_soon"`
}

// Ops はリマインダーの操作。
func Ops(env *app.Env) []app.Op {
	publish := app.Command(env, "reminder.publish", app.Entry("cmd:reminder:publish", "tb reminder publish"),
		func(_ context.Context, in PublishReq) (PublishResult, error) {
			return withDB(env, func(db *store.DB) (PublishResult, error) { return Publish(db, in) })
		},
		define.Doc("まとめを出し、前回の記録を更新する(tb reminder publish。1 日 1 回は全件)"),
		define.Authz("cmd:reminder:publish"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/reminders/publish"),
	)
	scan := app.Command(env, "reminder.scan", app.Entry("cmd:ticket:due-scan", "tb reminder scan"),
		func(_ context.Context, in ScanReq) (ScanResult, error) {
			return withDB(env, func(db *store.DB) (ScanResult, error) { return Scan(db, in) })
		},
		define.Doc("期限切れ・期限間近のタスクに履歴のイベントを 1 日 1 回ずつ残す(tb reminder scan)"),
		define.Authz("cmd:ticket:due-scan"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/reminders/scan"),
	)
	return []app.Op{publish, scan}
}

func withDB[T any](env *app.Env, f func(*store.DB) (T, error)) (T, error) {
	var zero T
	db, done, err := env.DB()
	if err != nil {
		return zero, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	defer done()
	return f(db)
}

func day(s string) (string, error) {
	if s == "" {
		return time.Now().Format("2006-01-02"), nil
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return "", define.Errorf(define.CodeInvalidArgument, "期日は YYYY-MM-DD の日付: %s", s)
	}
	return s, nil
}

// Publish は reminders --publish: まとめを作り、前回の記録(notify_seen の reminders:*)を書く。
func Publish(db *store.DB, in PublishReq) (PublishResult, error) {
	env := reminder.LoadEnv()
	if !env.Valid() {
		return PublishResult{}, define.Errorf(define.CodeInvalidArgument, "TB_REMINDERS_STALE_HOURS / TB_REMINDERS_DUE_DAYS / TB_DIGEST_QUEUED_SEC は数字")
	}
	td, err := day(in.Today)
	if err != nil {
		return PublishResult{}, err
	}
	st := tickets.LoadStatuses(db.DB, db.Session)
	r := &reminder.R{DB: db.DB, Session: db.Session, St: st, Env: env, Today: td}
	items, err := r.Collect(reminder.Now())
	if err != nil {
		return PublishResult{}, define.Errorf(define.CodeInternal, "reminders: 材料を読めませんでした")
	}
	q := notify.New(db.DB, db.Session)
	prev := reminder.ParseState(q.SeenGet("reminders:state"))
	fullDay := q.SeenGet("reminders:full-day")
	full := fullDay != td && len(items) > 0
	d := reminder.MakeDiff(items, prev, td, fullDay, full)
	// 秘密らしい値: 題に紛れ込んだものを伏せる(継続の項目は本文に出ないが、--json と Web UI の行には出るので全項目の題も調べる)
	text := d.Redact(d.Text(reminder.Now()))
	ts := reminder.Now()
	last := `{"at":` + util.JQString(ts) + `,"full":` + strconv.FormatBool(d.Full) + `,"removed":` + d.RemovedJSON() +
		`,"text":` + util.JQString(text) + `,"items":` + d.ItemsJSON() + "}"
	kv := [][2]string{{"reminders:state", d.StateJSON()}, {"reminders:last", last}}
	if full {
		kv = append(kv, [2]string{"reminders:full-day", td})
	}
	if err := seenTx(db, ts, kv); err != nil {
		return PublishResult{}, define.Errorf(define.CodeInternal, "reminders: 記録を書けませんでした")
	}
	return PublishResult{Full: d.Full, Text: text, Items: d.ItemsJSON(), Removed: d.RemovedJSON()}, nil
}

// seenTx は notify_seen の行を 1 つのトランザクションで書く。
func seenTx(db *store.DB, ts string, kv [][2]string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range kv {
		if _, err := tx.Exec(`INSERT INTO main.notify_seen (herdr_session, key, value, updated_at) VALUES (?, ?, ?, ?)
    ON CONFLICT(herdr_session, key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, db.Session, p[0], p[1], ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Scan は due-scan: 期限のイベントを残し、通知の規則を回す。
func Scan(db *store.DB, in ScanReq) (ScanResult, error) {
	if in.Within < 0 || in.Within > 999 {
		return ScanResult{}, define.Errorf(define.CodeInvalidArgument, "due-scan: --within は 0〜999 の日数: %d", in.Within)
	}
	td, err := day(in.Today)
	if err != nil {
		return ScanResult{}, err
	}
	st := tickets.LoadStatuses(db.DB, db.Session)
	over, soon, err := tickets.DueScan(db.DB, db.Session, st.SQLList("done", "cancelled"), td, in.Within, in.Project)
	if err != nil {
		return ScanResult{}, define.Errorf(define.CodeInternal, "due-scan: 履歴に書けませんでした")
	}
	q := notify.New(db.DB, db.Session)
	q.ActiveSQL = st.SQLList("active", "review")
	_ = q.RunRules()
	return ScanResult{Overdue: over, DueSoon: soon}, nil
}
