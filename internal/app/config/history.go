package config

import (
	"context"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// HistoryReq は config.history の入力。
type HistoryReq struct {
	Key string `api:"key,path"`
}

// HistoryResult は tb config history の出力(新しい順に 50 件。このセッションと全セッション共通の行)。
type HistoryResult struct {
	Text string `api:"text" json:"text"`
}

func historyOp(env *app.Env) app.Op {
	return app.Query(env, "config.history",
		func(_ context.Context, _ *app.Env, _ string, v any) app.Decision {
			return app.Decision{Err: KeyOK(v.(HistoryReq).Key)}
		},
		func(_ context.Context, in HistoryReq) (HistoryResult, error) {
			db, done, err := env.DB()
			if err != nil {
				return HistoryResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			defer done()
			s, err := historyText(db, in.Key)
			if err != nil {
				return HistoryResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
			}
			return HistoryResult{Text: s}, nil
		},
		define.Doc("設定のキーの変更の記録(tb config history。新しい順に 50 件)"),
		define.Authz("cmd:config:history", define.Resource("config", "key")),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/config/{key}/history"),
	)
}

func historyText(db *store.DB, key string) (string, error) {
	var b strings.Builder
	err := store.Each(db.DB, `SELECT id, herdr_session, how, changed_by, changed_at, CASE WHEN body IS NULL THEN '(消した)' ELSE length(body) || ' 文字' END
    FROM main.config_changes WHERE key=? AND herdr_session IN (?, '*') ORDER BY id DESC LIMIT 50`, []any{key, db.Session},
		func(cols []string, vals []any) error { return rowfmt.EmitRow(&b, false, cols, vals, "") })
	return b.String(), err
}
