// Package activity は Claude の session の活動の記録の操作(tb touch / tb activity。ADR 0008)。
package activity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/activity"
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// touchTimeoutMS は touch_impl の SQL_TIMEOUT(ロックされていても 1 秒で諦める)。
const touchTimeoutMS = 1000

// Touch は hook からの活動の記録(tb touch)。何も出さず、失敗は touch-errors.log に「時刻 event 理由コード」の 1 行だけを書く。
// hook の度に呼ばれるので操作(Server)を組み立てず、関数として呼ぶ。
func Touch(args []string) {
	reason := touch(args)
	if reason == "" {
		return
	}
	log := os.Getenv("TB_TOUCH_LOG")
	if log == "" {
		log = filepath.Join(filepath.Dir(env.TBDB()), "touch-errors.log")
	}
	_ = os.MkdirAll(filepath.Dir(log), 0o700)
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s event=%s reason=%s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), activity.LogEvent(args), reason)
}

func touch(args []string) (reason string) {
	defer func() {
		if recover() != nil {
			reason = "unknown"
		}
	}()
	t, reason := activity.ParseTouch(args)
	if reason != "" {
		return reason
	}
	var opt store.Options
	opt.BusyTimeoutMS = touchTimeoutMS
	opt.NoSession = true // 見張りなしで書く(行を別の herdr セッションから移すことがある)
	if d := os.Getenv("TB_SCHEMA_DIR"); d != "" {
		opt.Schema = os.DirFS(d)
	}
	session := env.SessionName()
	db, err := store.Open(env.TBDB(), session, &opt)
	if err != nil {
		return activity.Reason(err)
	}
	defer db.Close()
	if err := activity.Upsert(db.DB, session, t); err != nil {
		return activity.Reason(err)
	}
	return ""
}

// ListReq は activity.list の入力。StuckMin は滞留とみなす分(数字。空なら 10)。
type ListReq struct {
	StuckMin string `api:"stuck_min,optional"`
	JSON     bool   `api:"json,optional"`
}

// ListResult は tb activity の出力。
type ListResult struct {
	Text string `api:"text" json:"text"`
}

// Ops は活動の記録の操作。
func Ops(env *app.Env) []app.Op {
	return []app.Op{app.Query(env, "activity.list", func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} },
		func(_ context.Context, in ListReq) (ListResult, error) {
			stuck := in.StuckMin
			if stuck == "" {
				stuck = "10"
			}
			if !activity.ValidMinutes(stuck) {
				return ListResult{}, define.Errorf(define.CodeInvalidArgument, "分は数字: %s", stuck)
			}
			db, done, err := env.DB()
			if err != nil {
				return ListResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			var b strings.Builder
			err = activity.List(db.DB, db.Session, stuck, in.JSON, func(cols []string, vals []any) error {
				if in.JSON {
					s, err := rowfmt.RowJSON(cols, vals)
					if err != nil {
						return err
					}
					b.WriteString(s + "\n")
					return nil
				}
				parts := make([]string, len(vals))
				for i, v := range vals {
					if v == nil {
						parts[i] = "-" // sqlite3 -nullvalue -
					} else {
						parts[i] = tickets.Text(v)
					}
				}
				b.WriteString(strings.Join(parts, "\t") + "\n")
				return nil
			})
			if err != nil {
				return ListResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			return ListResult{Text: b.String()}, nil
		},
		define.Doc("Claude の session の活動(tb activity。working の推定と滞留つき)"),
		define.Authz("cmd:activity:list"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/activity"),
	)}
}
