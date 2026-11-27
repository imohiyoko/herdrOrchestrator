// Package alert はアラートと解析役のアプリケーション API(ADR 0008。tb の alert / alert-*)。
// アラートはチケット(project alert)と alert_details に置き、ここでは互換ビュー alerts を読み書きする(020。ADR 0003)。
// 閉じる(closed)のは人だけ(alert.close)。解析役の起動は app/agent と同じ部品を使う。
package alert

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/approval"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/transport"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
	"github.com/imohiyoko/herdrorchestrator/internal/message"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/requests"
)

// Severities は緊急度(解析役が決める)。
const Severities = "critical high medium low"

// RaiseReq は alert.raise の入力(tb alert)。Key は同じアラートとみなす指紋の元(空なら Title)。
type RaiseReq struct {
	Title string `api:"title"`
	Body  string `api:"body,optional"`
	Key   string `api:"key,optional"`
}

// RaiseResult は登録の結果。State は new か duplicate(同じ指紋の未解決があれば件数を増やすだけ)、Status は今の状態。
type RaiseResult struct {
	ID     string `api:"id" json:"id"`
	State  string `api:"state" json:"state"`
	Status string `api:"status" json:"status"`
}

// IDReq は A-<n> だけを受ける入力。
type IDReq struct {
	ID     string `api:"id,path"`
	Always bool   `api:"always,optional"` // alert.approve: 今後は承認なし(policy に * alert:analyze * = allow を書く)
}

// StatusResult は 1 件の状態。Always は --always の行を書けた。
type StatusResult struct {
	ID      string       `api:"id" json:"id"`
	Status  string       `api:"status" json:"status"`
	Always  bool         `api:"always,optional" json:"always,omitempty"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *StatusResult) SetPending(p *app.Pending) { r.Pending = p }

// ResultReq は alert.result の入力(解析役が緊急度と結果を返す)。Report は報告の本文、ReportPath はそのファイル。
type ResultReq struct {
	ID         string `api:"id,path"`
	Severity   string `api:"severity"`
	Summary    string `api:"summary"`
	Report     string `api:"report,optional"`
	ReportPath string `api:"report_path,optional"`
}

// DispatchReq は alert.dispatch の入力(無し)。
type DispatchReq struct{}

// DispatchResult は承認待ち・上限待ち・解析中のアラートの一覧(古い順)。
type DispatchResult struct {
	Alerts []StatusResult `api:"alerts" json:"alerts"`
}

// Ops はアラートの操作。
func Ops(env *app.Env) []app.Op {
	raise := app.Command(env, "alert.raise", app.Entry("cmd:alert:add", "tb alert"),
		func(ctx context.Context, in RaiseReq) (RaiseResult, error) {
			return withDB(env, func(db *store.DB) (RaiseResult, error) { return raise(env, db, who(ctx), in) })
		},
		define.Doc("アラートを登録する(tb alert。緊急度は解析役が決める)"),
		define.Authz("cmd:alert:add"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/alerts"),
	)
	dispatch := app.Command(env, "alert.dispatch", app.Entry("cmd:alert:dispatch", "tb alert-dispatch"),
		func(_ context.Context, _ DispatchReq) (DispatchResult, error) {
			return withDB(env, func(db *store.DB) (DispatchResult, error) {
				DispatchAll(db)
				var r DispatchResult
				err := store.Each(db.DB, `SELECT 'A-'||id, status FROM alerts WHERE status IN ('awaiting-approval','queued','analyzing') ORDER BY id`, nil, func(_ []string, v []any) error {
					r.Alerts = append(r.Alerts, StatusResult{ID: text(v[0]), Status: text(v[1])})
					return nil
				})
				return r, ierr(err)
			})
		},
		define.Doc("承認待ち・上限待ちのアラートを進める(tb alert-dispatch)"),
		define.Authz("cmd:alert:dispatch"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/alerts/dispatch"),
	)
	approve := app.Command(env, "alert.approve", app.Cmd("cmd:alert:approve", "tb alert-approve"),
		func(ctx context.Context, in IDReq) (StatusResult, error) {
			id, err := aid(in.ID)
			if err != nil {
				return StatusResult{}, err
			}
			return withDB(env, func(db *store.DB) (StatusResult, error) {
				if Status(db, id) != "awaiting-approval" {
					return StatusResult{}, define.Errorf(define.CodeFailedPrecondition, "A-%d は承認待ちではありません", id)
				}
				setStatus(db, id, "approved")
				r := StatusResult{ID: "A-" + strconv.FormatInt(id, 10)}
				check := func(old, new string) error {
					return perms.RequirePolicyWriteAs(db.DB, db.Session, who(ctx), old, new, "tb approval allow")
				}
				if in.Always && requests.SetApprovalChecked(db, "allow", "alert:analyze", io.Discard, io.Discard, check) == nil {
					r.Always = true
				}
				_ = DispatchOne(db, id)
				r.Status = Status(db, id)
				return r, nil
			})
		},
		define.Doc("人が解析役の起動を承認する(tb alert-approve)"),
		define.Authz("cmd:alert:approve", define.Resource("alert", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/alerts/{id}/approve"),
	)
	result := app.Command(env, "alert.result", app.Entry("cmd:alert:result", "tb alert-result"),
		func(ctx context.Context, in ResultReq) (StatusResult, error) {
			return withDB(env, func(db *store.DB) (StatusResult, error) { return result(env, db, who(ctx), in) })
		},
		define.Doc("解析役が緊急度と結果を返す(tb alert-result。担当の解析役か人だけ)"),
		define.Authz("cmd:alert:result", define.Resource("alert", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/alerts/{id}/result"),
	)
	closeOp := app.Command(env, "alert.close", app.Cmd("cmd:alert:close", "tb alert-close"),
		func(ctx context.Context, in IDReq) (StatusResult, error) {
			id, err := aid(in.ID)
			if err != nil {
				return StatusResult{}, err
			}
			return withDB(env, func(db *store.DB) (StatusResult, error) {
				if _, err := db.Exec(`UPDATE main.alerts SET status='closed', closed_at=? WHERE herdr_session=? AND id=?`, env.NowString(), db.Session, id); err != nil {
					return StatusResult{}, ierr(err)
				}
				return StatusResult{ID: "A-" + strconv.FormatInt(id, 10), Status: "closed"}, nil
			})
		},
		define.Doc("人がアラートを閉じる(tb alert-close)"),
		define.Authz("cmd:alert:close", define.Resource("alert", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/alerts/{id}/close"),
	)
	return []app.Op{raise, dispatch, approve, result, closeOp}
}

func who(ctx context.Context) string {
	p, _ := define.PrincipalFrom(ctx)
	return app.Who(p)
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

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// aid は A-<n> の n(大きすぎる番号は無い番号として -1)。
func aid(s string) (int64, error) {
	if len(s) < 3 || s[:2] != "A-" || strings.Trim(s[2:], "0123456789") != "" {
		return 0, define.Errorf(define.CodeInvalidArgument, "アラート ID は A-<数字>: %s", s)
	}
	n, err := strconv.ParseInt(s[2:], 10, 64)
	if err != nil {
		n = -1
	}
	return n, nil
}

// Status は A-<id> の今の状態(無ければ空)。
func Status(db *store.DB, id int64) string {
	var s sql.NullString
	_ = db.QueryRow(`SELECT status FROM alerts WHERE id=?`, id).Scan(&s)
	return s.String
}

func setStatus(db *store.DB, id int64, st string) {
	_, _ = db.Exec(`UPDATE main.alerts SET status=? WHERE herdr_session=? AND id=?`, st, db.Session, id)
}

func queued(db *store.DB, id int64, note string) {
	_, _ = db.Exec(`UPDATE main.alerts SET status='queued', note=? WHERE herdr_session=? AND id=?`, note, db.Session, id)
}

// row は A-<id> の列(使うものだけ)。
type row struct {
	ok                                                        bool
	title, body, source, severity, summary, path, analyst, st string
}

func rowOf(db *store.DB, id int64) row {
	var r row
	var t, b, src, sev, sum, p, an, st sql.NullString
	if db.QueryRow(`SELECT title, body, source, severity, result_summary, result_path, analyst, status FROM alerts WHERE id=?`, id).
		Scan(&t, &b, &src, &sev, &sum, &p, &an, &st) != nil {
		return r
	}
	return row{true, t.String, b.String, src.String, sev.String, sum.String, p.String, an.String, st.String}
}

// nullable は jq の \(.x)(null は "null")。
func nullable(s string, ok bool) string {
	if !ok {
		return "null"
	}
	return s
}

func emit(db *store.DB, typ string, id int64) {
	o := rowOf(db, id)
	if !o.ok {
		return
	}
	e := notify.Event{Type: typ, Ref: "A-" + strconv.FormatInt(id, 10)}
	if typ == "alert_raised" {
		e.A, e.B, e.C = o.title, o.source, o.body
	} else {
		e.Sev, e.A, e.B, e.C = o.severity, o.title, o.summary, o.path
	}
	notify.New(db.DB, db.Session).Emit(e)
}

func raise(env *app.Env, db *store.DB, who string, in RaiseReq) (RaiseResult, error) {
	if strings.TrimFunc(in.Title, config.IsSpace) == "" {
		return RaiseResult{}, define.Errorf(define.CodeInvalidArgument, "alert: --title が必要です")
	}
	src, spane := "cli", ""
	if env.Reporter != nil { // tb の入口: pane の中なら herdr から引いた報告者
		n, p, _ := env.Reporter(who)
		if n == "" {
			return RaiseResult{}, &define.Error{Code: define.CodeUnauthenticated, Message: "報告者を特定できません", Reasons: []string{app.ReasonNoSender}}
		}
		src, spane = n, p
	} else if !requests.IsHuman(who) && who != "" {
		src = who
	}
	key := in.Key
	if key == "" {
		key = in.Title
	}
	sum := sha256.Sum256([]byte(key))
	fp := hex.EncodeToString(sum[:])[:16]
	ts := env.NowString()
	var dup sql.NullInt64
	_ = db.QueryRow(`SELECT id FROM alerts WHERE fingerprint=? AND status NOT IN ('analyzed','closed') ORDER BY id DESC LIMIT 1`, fp).Scan(&dup)
	var id int64
	state := "new"
	if dup.Valid {
		if _, err := db.Exec(`UPDATE main.alerts SET count=count+1, last_seen=? WHERE herdr_session=? AND id=?`, ts, db.Session, dup.Int64); err != nil {
			return RaiseResult{}, ierr(err)
		}
		id, state = dup.Int64, "duplicate"
	} else {
		// ビュー経由では last_insert_rowid() が使えないので、同じトランザクションで振った番号を読む
		tx, err := db.Begin()
		if err != nil {
			return RaiseResult{}, ierr(err)
		}
		_, err = tx.Exec(`INSERT INTO main.alerts (herdr_session, fingerprint, title, body, source, source_pane, status, count, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, NULLIF(?,''), 'new', 1, ?, ?)`, db.Session, fp, in.Title, in.Body, src, spane, ts, ts)
		if err == nil {
			err = tx.QueryRow(`SELECT MAX(alert_no) FROM main.alert_details`).Scan(&id)
		}
		if err != nil {
			_ = tx.Rollback()
			return RaiseResult{}, ierr(err)
		}
		if err := tx.Commit(); err != nil {
			return RaiseResult{}, ierr(err)
		}
	}
	if state == "new" {
		emit(db, "alert_raised", id)
		_ = DispatchOne(db, id)
	}
	return RaiseResult{ID: "A-" + strconv.FormatInt(id, 10), State: state, Status: Status(db, id)}, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// DispatchOne は alert_dispatch_one: 解析役を起動する。新規で policy の alert:analyze が allow でなければ awaiting-approval。上限なら queued。
// 戻り値は tb の終了コード(起動・依頼の失敗は 10 / 11。呼ぶ側はどれも使わない)。
func DispatchOne(db *store.DB, id int64) int {
	st := Status(db, id)
	if st != "new" && st != "queued" && st != "approved" {
		return 0
	}
	if st == "new" {
		if v, _ := approval.NewJudge(db.DB, db.Session).Of("alert:analyze", ""); v != "allow" {
			setStatus(db, id, "awaiting-approval")
			return 0
		}
	}
	var busy int64
	_ = db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status='analyzing'`).Scan(&busy)
	amax := envOr("TB_ANALYST_MAX", "2")
	ls := limits.Load(db.DB, db.Session)
	if v, _, ok := ls.Raw("analyst_max"); ok { // limits で設定されていれば TB_ANALYST_MAX より優先
		amax = v
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(amax), 10, 64); err == nil && busy >= n { // unlimited は数でないので待たせない
		setStatus(db, id, "queued")
		return 0
	}
	// 動作中の agent の総数(max_running_agents)。上限なら作らず queued に戻す
	if agent.RefuseRunning(db, 1, io.Discard) {
		queued(db, id, "max_running_agents に達しているため待機")
		return 0
	}
	return startAnalyst(db, id)
}

// startAnalyst は start_analyst: analyst-A<n> を起動して依頼を送る。
func startAnalyst(db *store.DB, id int64) int {
	ids := strconv.FormatInt(id, 10)
	name := "analyst-A" + ids
	d := envOr("TB_ANALYST_CWD", os.Getenv("TB_ROOT")+"/..")
	if d == "" || !agent.IsDir(d) {
		queued(db, id, "TB_ANALYST_CWD が無い")
		return 11
	}
	cwd := agent.PhysDir(d)
	model := os.Getenv("TB_ANALYST_MODEL")
	var extra []string
	if model != "" {
		extra = []string{"--model", model}
	}
	// 起動のやり直しの間隔は TB_ANALYST_RETRY_SEC(プロセスの環境は書き換えない。Web UI では並行して呼ばれる)
	sp, ok := transport.SpawnAt(env.TBDB(), agent.KindDefault, name, cwd, extra, transport.Place{RetryEnv: "TB_ANALYST_RETRY_SEC"})
	if !ok {
		queued(db, id, sp.Err)
		return 11
	}
	_ = agent.Register(db, name, cwd, model, "", "tb", "unknown", "", agent.KindDefault)
	_, _ = db.Exec(`UPDATE main.alerts SET status='analyzing', analyst=?, analysis_started_at=?, note=NULL WHERE herdr_session=? AND id=?`, name, requests.Now(), db.Session, id)
	prompt := "[from: tb / alert / A-" + ids + " / task] " + config.Msg(db.DB, db.Session, "alert.analyze",
		"あなたはアラート A-{id} を解析する agent です。まず tb alerts A-{id} で全文を読み、原因の仮説・根拠・影響範囲・推奨対応をまとめ、緊急度を決めて、tb alert-result A-{id} --severity critical|high|medium|low --summary '<要約>' --report <報告ファイル> で返してください。", "id="+ids)
	// 起動直後は idle になるまで少し待つ(送るのは idle/done のときだけ)
	for i := 1; i <= 5; i++ {
		if transport.Send(db.DB, db.Session, name, prompt, "A-"+ids, "alert", io.Discard) == 0 {
			return 0
		}
		time.Sleep(time.Duration(agent.SecEnv("TB_ANALYST_RETRY_SEC", 3) * float64(time.Second)))
	}
	_, _ = db.Exec(`UPDATE main.alerts SET note='解析役に依頼を送れていない(tb alert-dispatch で再送)' WHERE herdr_session=? AND id=?`, db.Session, id)
	return 10
}

// DispatchAll は待っているアラート(new / queued / approved)を古い順に進める。
func DispatchAll(db *store.DB) {
	var ids []int64
	_ = store.Each(db.DB, `SELECT id FROM alerts WHERE status IN ('new','queued','approved') ORDER BY id`, nil, func(_ []string, vals []any) error {
		if n, ok := vals[0].(int64); ok {
			ids = append(ids, n)
		}
		return nil
	})
	for _, id := range ids {
		_ = DispatchOne(db, id)
	}
}

func result(env *app.Env, db *store.DB, who string, in ResultReq) (StatusResult, error) {
	id, err := aid(in.ID)
	if err != nil {
		return StatusResult{}, err
	}
	sev := in.Severity
	if sev == "" || strings.Contains(sev, " ") || !strings.Contains(" "+Severities+" ", " "+sev+" ") {
		return StatusResult{}, define.Errorf(define.CodeInvalidArgument, "alert-result: --severity は %s のどれか(緊急度は解析役が決める)", Severities)
	}
	if strings.TrimFunc(in.Summary, config.IsSpace) == "" {
		return StatusResult{}, define.Errorf(define.CodeInvalidArgument, "alert-result: --summary が必要です")
	}
	o := rowOf(db, id)
	if !o.ok {
		return StatusResult{}, define.Errorf(define.CodeNotFound, "アラートが見つかりません: A-%d", id)
	}
	by := "human"
	if env.Reporter != nil { // tb の入口: pane の中なら herdr から引いた名前
		n, _, _ := env.Reporter(who)
		if n == "" {
			return StatusResult{}, &define.Error{Code: define.CodeUnauthenticated, Message: "報告者を特定できません", Reasons: []string{app.ReasonNoSender}}
		}
		by = n
	} else if who == "" {
		return StatusResult{}, define.Errorf(define.CodePermissionDenied, "alert-result: 呼び手を決められません")
	} else if !requests.IsHuman(who) {
		by = who
	}
	if by != "human" && by != o.analyst {
		return StatusResult{}, define.Errorf(define.CodeFailedPrecondition, "A-%d の解析役ではありません(%s)", id, by)
	}
	if o.st == "closed" {
		return StatusResult{}, define.Errorf(define.CodeFailedPrecondition, "A-%d は閉じています", id)
	}
	ts := env.NowString()
	if _, err := db.Exec(`UPDATE main.alerts SET status='analyzed', severity=?, result_summary=?, result_report=NULLIF(?,''),
		result_path=NULLIF(?,''), result_by=?, analyzed_at=? WHERE herdr_session=? AND id=?`, sev, in.Summary, in.Report, in.ReportPath, by, ts, db.Session, id); err != nil {
		return StatusResult{}, ierr(err)
	}
	emit(db, "alert_result", id)
	deliver(db, id)
	// 解析役は結果を返したら用済み: 止めてよい印を付ける(停止は tb agent-stop)
	if by != "human" {
		_, _ = db.Exec(`UPDATE main.agents SET killable='yes', killable_reason=?, killable_by='tb', killable_at=?,
			killable_session=session_id WHERE herdr_session=? AND name=?`, fmt.Sprintf("A-%d の解析結果を返した", id), ts, db.Session, by)
	}
	DispatchAll(db) // 枠が空いたので待ちを進める
	return StatusResult{ID: "A-" + strconv.FormatInt(id, 10), Status: "analyzed"}, nil
}

// deliver は alert_deliver: 解析結果を指揮官へ(使用中なら tb digest が回収する)。
func deliver(db *store.DB, id int64) {
	var sev, title, sum, del sql.NullString
	if db.QueryRow(`SELECT severity, title, result_summary, result_delivered_at FROM alerts WHERE id=?`, id).Scan(&sev, &title, &sum, &del) != nil || del.String != "" {
		return
	}
	ids := strconv.FormatInt(id, 10)
	target := message.TargetFor(db.DB, db.Session, "*")
	msg := "[alert A-" + ids + " analyzed / " + nullable(sev.String, sev.Valid) + "] " + util.Oneline(nullable(title.String, title.Valid), 80) + " — " +
		util.Oneline(nullable(sum.String, sum.Valid), 300) + " (全文: tb alerts A-" + ids + ")"
	if transport.Send(db.DB, db.Session, target, msg, "A-"+ids, "alert", io.Discard) != 0 {
		return
	}
	_, _ = db.Exec(`UPDATE main.alerts SET result_delivered_at=? WHERE herdr_session=? AND id=?`, requests.Now(), db.Session, id)
}
