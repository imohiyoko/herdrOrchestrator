// Package notify は外への通知(Slack)のアプリケーション API(ADR 0008。tb notify の書き込み)。
// 中身(イベントの列・送信・規則)は internal/notify。ここは入口の確かめと、設定 notify の書き換え。
// 通知を入れる・止める・試す・送り切る・手でイベントを積むのは既定で人だけ(commands.tbl の inner の行。policy で緩められる)。
// ack は notifier agent か人。
package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

var (
	reHash12 = regexp.MustCompile(`^[0-9a-f]{12}$`)
	reMsgID  = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,80}$`)
	reNum    = regexp.MustCompile(`^[0-9]+$`)
)

// SwitchReq は notify.on / notify.off の入力(無し)。
type SwitchReq struct{}

// SwitchResult は書いた後の enabled。
type SwitchResult struct {
	Enabled string       `api:"enabled" json:"enabled"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *SwitchResult) SetPending(p *app.Pending) { r.Pending = p }

// TestResult は notify.test で積んだイベント。
type TestResult struct {
	ID      string       `api:"id" json:"id"`
	Status  string       `api:"status" json:"status"`
	Text    string       `api:"text" json:"text"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *TestResult) SetPending(p *app.Pending) { r.Pending = p }

// FlushReq は notify.flush の入力。Today は期限のイベントを見る日(空なら今日。YYYY-MM-DD でなければ見ない)。
type FlushReq struct {
	Today string `api:"today,optional"`
}

// FlushResult は送り切った結果。Off なら通知は止まっていて、何もしなかった。
type FlushResult struct {
	Off     bool         `api:"off" json:"off"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *FlushResult) SetPending(p *app.Pending) { r.Pending = p }

// AckReq は notify.ack の入力(notifier agent が投稿した本文の hash と投稿の ID を返す)。
type AckReq struct {
	ID    string `api:"id,path"` // N-<n>
	Hash  string `api:"hash"`
	MsgID string `api:"msg_id,optional"`
}

// AckResult は ack の結果。Status は acked か mismatch(本文の hash が送った本文と合わない。tb は 1 で終わる)。
type AckResult struct {
	ID      string       `api:"id" json:"id"`
	Status  string       `api:"status" json:"status"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *AckResult) SetPending(p *app.Pending) { r.Pending = p }

// EventReq は notify.event の入力(人が手でインシデントのイベントを積む)。
type EventReq struct {
	Type     string `api:"type"` // incident_opened | incident_closed
	Ref      string `api:"ref"`
	Title    string `api:"title,optional"`
	Severity string `api:"severity,optional"`
}

// EventResult は積んだイベント。
type EventResult struct {
	Type    string       `api:"type" json:"type"`
	Ref     string       `api:"ref" json:"ref"`
	Pending *app.Pending `api:"pending,optional" json:"pending,omitempty"`
}

// SetPending は app.PendingSetter。
func (r *EventResult) SetPending(p *app.Pending) { r.Pending = p }

// Ops は通知の操作。
func Ops(env *app.Env) []app.Op {
	sw := func(val string) app.Op {
		return app.Command(env, "notify."+val, app.Cmd("cmd:notify:"+val, "tb notify "+val),
			func(ctx context.Context, _ SwitchReq) (SwitchResult, error) {
				return withDB(env, func(db *store.DB) (SwitchResult, error) {
					if err := WriteConf(db, "enabled", val, who(ctx)); err != nil {
						return SwitchResult{}, err
					}
					return SwitchResult{Enabled: val}, nil
				})
			},
			define.Doc(map[string]string{"on": "外への通知を入れる(tb notify on。既定は人だけ)", "off": "外への通知を止める(tb notify off。既定は人だけ)"}[val]),
			define.Authz("cmd:notify:"+val),
			define.Expose(define.Internal, define.REST),
			define.HTTP("POST /v1/notify/"+val),
		)
	}
	test := app.Command(env, "notify.test", app.Cmd("cmd:notify:test", "tb notify test"),
		func(ctx context.Context, _ SwitchReq) (TestResult, error) {
			return withDB(env, func(db *store.DB) (TestResult, error) {
				q := queue(db)
				id, _ := q.Enqueue(notify.Event{Type: "test", Ref: fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), os.Getpid()) /* 長く動くプロセスから続けて呼んでも重ならない */, Force: true})
				if id == 0 {
					return TestResult{}, define.Errorf(define.CodeInternal, "test: イベントを作れませんでした")
				}
				if field(db, id, "slack_status") == "pending" {
					q.SlackSend(id)
				}
				return TestResult{ID: "N-" + strconv.FormatInt(id, 10), Status: field(db, id, "slack_status"), Text: field(db, id, "text")}, nil
			})
		},
		define.Doc("試しの通知を 1 件積んで送る(tb notify test。既定は人だけ)"),
		define.Authz("cmd:notify:test"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/notify/test"),
	)
	flush := app.Command(env, "notify.flush", app.Cmd("cmd:notify:flush", "tb notify flush"),
		func(ctx context.Context, in FlushReq) (FlushResult, error) {
			return withDB(env, func(db *store.DB) (FlushResult, error) {
				st := tickets.LoadStatuses(db.DB, db.Session)
				q := notify.New(db.DB, db.Session)
				q.ActiveSQL = st.SQLList("active", "review")
				if !q.C.On() {
					return FlushResult{Off: true}, nil
				}
				_ = q.Periodic()
				td := in.Today
				if td == "" {
					td = time.Now().Format("2006-01-02")
				}
				if _, err := time.Parse("2006-01-02", td); err == nil { // 日付でなければ期限のイベントは見ない
					_, _, _ = tickets.DueScan(db.DB, db.Session, st.SQLList("done", "cancelled"), td, 1, "") // 期限のイベント(tb due-scan。1 日 1 回ずつ)
				}
				_ = q.RunRules() // 通知の規則の取りこぼし
				q.FlushSlack()
				return FlushResult{}, nil
			})
		},
		define.Doc("溜まった通知を送り切る(tb notify flush。既定は人だけ)"),
		define.Authz("cmd:notify:flush"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/notify/flush"),
	)
	ack := app.Command(env, "notify.ack", app.Entry("cmd:notify:ack", "tb notify ack"),
		func(ctx context.Context, in AckReq) (AckResult, error) {
			return withDB(env, func(db *store.DB) (AckResult, error) { return Ack(env, db, who(ctx), in) })
		},
		define.Doc("notifier agent が投稿を返す(tb notify ack。本文の hash を確かめる。notifier agent か人だけ)"),
		define.Authz("cmd:notify:ack", define.Resource("notify_event", "id")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/notify/events/{id}/ack"),
	)
	event := app.Command(env, "notify.event", app.Cmd("cmd:notify:event", "tb notify event"),
		func(ctx context.Context, in EventReq) (EventResult, error) {
			if in.Type != "incident_opened" && in.Type != "incident_closed" {
				return EventResult{}, define.Errorf(define.CodeInvalidArgument, "notify event: 種別は incident_opened|incident_closed")
			}
			if strings.TrimSpace(in.Ref) == "" {
				return EventResult{}, define.Errorf(define.CodeInvalidArgument, "notify event: ID が必要です")
			}
			return withDB(env, func(db *store.DB) (EventResult, error) {
				queue(db).Emit(notify.Event{Type: in.Type, Ref: in.Ref, Sev: in.Severity, A: in.Title})
				return EventResult{Type: in.Type, Ref: in.Ref}, nil
			})
		},
		define.Doc("人がインシデントのイベントを積む(tb notify event。既定は人だけ)"),
		define.Authz("cmd:notify:event"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("POST /v1/notify/events"),
	)
	return append([]app.Op{sw("on"), sw("off"), test, flush, ack, event}, readOps(env)...)
}

func who(ctx context.Context) string {
	p, _ := define.PrincipalFrom(ctx)
	return app.Who(p)
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

func queue(db *store.DB) *notify.Q { return notify.New(db.DB, db.Session) }

// field はこのセッションのイベントの 1 列(無ければ空)。
func field(db *store.DB, id int64, col string) string {
	var v any
	_ = db.QueryRow("SELECT "+col+" FROM notify_events WHERE id=?", id).Scan(&v)
	return strings.TrimRight(tickets.Text(v), "\n")
}

// WriteConf は notify_write_conf: 設定 notify のそのキーの行を置き換える(無ければ足す)。
// 環境変数で設定のファイルを指していれば、そのファイルに書く。
func WriteConf(db *store.DB, k, v, by string) error {
	var out []string
	found := false
	if body, _, ok := config.Lookup(db.DB, db.Session, "notify"); ok && body != "" {
		for _, line := range config.Lines(body) {
			if !strings.HasPrefix(line, "#") && strings.Contains(line, "=") && config.Trim(strings.SplitN(line, "=", 2)[0]) == k {
				if !found {
					out = append(out, k+" = "+v)
					found = true
				}
			} else {
				out = append(out, line)
			}
		}
	}
	if !found {
		out = append(out, k+" = "+v)
	}
	body := strings.TrimRight(strings.Join(out, "\n"), "\n")
	info, _ := config.Info("notify")
	var err error
	if f := os.Getenv(info.Env); f != "" {
		err = config.WriteFile(f, body)
	} else {
		err = config.DBPut(db.DB, db.Session, "notify", body, "notify", false, by)
	}
	if err != nil {
		var sl *config.ErrSymlink
		if errors.As(err, &sl) {
			return define.Errorf(define.CodeFailedPrecondition, "%s", sl.Error())
		}
		return define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	return nil
}

// Ack は notify ack の中身。who が notifier agent でも人でもなければ断る(Env.Reporter があればその名前で確かめる)。
func Ack(env *app.Env, db *store.DB, who string, in AckReq) (AckResult, error) {
	idS := strings.TrimPrefix(in.ID, "N-")
	if !reNum.MatchString(idS) {
		return AckResult{}, define.Errorf(define.CodeInvalidArgument, "イベント ID は N-<数字>: %s", in.ID)
	}
	if !reHash12.MatchString(in.Hash) {
		return AckResult{}, define.Errorf(define.CodeInvalidArgument, "ack: hash は 12 桁の 16 進(tb notify hash で出す)")
	}
	if in.MsgID != "" && !reMsgID.MatchString(in.MsgID) {
		return AckResult{}, define.Errorf(define.CodeInvalidArgument, "ack: --id は英数字と . _ : / - の 80 字まで")
	}
	q := queue(db)
	c := q.C
	name := ""
	if env.Reporter != nil { // tb の入口: pane の中なら herdr から引いた名前
		n, _, _ := env.Reporter(who)
		if n == "" {
			return AckResult{}, &define.Error{Code: define.CodeUnauthenticated, Message: "報告者を特定できません", Reasons: []string{app.ReasonNoSender}}
		}
		name = n
	} else if who == "" {
		return AckResult{}, define.Errorf(define.CodePermissionDenied, "ack: 呼び手を決められません")
	} else if who != "human" && !strings.HasPrefix(who, "system:") {
		name = who
	}
	if name != "" && name != c.Get("notifier_agent") {
		return AckResult{}, define.Errorf(define.CodePermissionDenied, "ack は notifier agent(%s)か人だけが使えます(%s)", c.Get("notifier_agent"), name)
	}
	id, perr := strconv.ParseInt(idS, 10, 64)
	var stt, exp string
	if perr != nil || db.QueryRow("SELECT slack_status, hash FROM notify_events WHERE id=?", id).Scan(&stt, &exp) != nil {
		return AckResult{}, define.Errorf(define.CodeNotFound, "イベントが見つかりません: N-%s", idS)
	}
	switch stt {
	case "awaiting-ack", "failed", "acked", "mismatch":
	default:
		return AckResult{}, define.Errorf(define.CodeFailedPrecondition, "N-%s は ack を待っていません(%s)", idS, stt)
	}
	if c.Flag("hash_check") && in.Hash != exp {
		_, err := db.Exec("UPDATE main.notify_events SET slack_status='mismatch', note='投稿された本文の hash が一致しない' WHERE herdr_session=? AND id=?", db.Session, id)
		q.Emit(notify.Event{Type: "warning", Ref: "N-" + idS + "-mismatch", A: "N-" + idS + " の投稿内容が送った本文と一致しません。Slack 側を確かめてください"}) // 記録に失敗しても人には知らせる
		if err != nil {
			return AckResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
		}
		return AckResult{ID: "N-" + idS, Status: "mismatch"}, nil
	}
	if _, err := db.Exec("UPDATE main.notify_events SET slack_status='acked', slack_msg_id=NULLIF(?,''), note=NULL WHERE herdr_session=? AND id=?", in.MsgID, db.Session, id); err != nil {
		return AckResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	return AckResult{ID: "N-" + idS, Status: "acked"}, nil
}
