package reminder

import (
	"context"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/reminder"
)

// DeadlinesReq は reminder.deadlines の入力(なし)。
type DeadlinesReq struct{}

// DeadlineLine は期限ファイルの 1 行と、取り込みでの扱い。
// State は invalid(日付が正しくない)・have(取り込み済み。Ticket がそのチケット)・new(まだ無い)。
type DeadlineLine struct {
	Date   string `api:"date" json:"date"`
	Label  string `api:"label" json:"label"`
	State  string `api:"state" json:"state"`
	Ticket string `api:"ticket,optional" json:"ticket,omitempty"`
}

// DeadlinesResult は期限ファイル(TB_REMINDERS_DEADLINES)の中身。Missing ならファイルが無い。
// NoDue は DB に期日の列が無い(スキーマが古い)。
type DeadlinesResult struct {
	File    string         `api:"file" json:"file"`
	Missing bool           `api:"missing,optional" json:"missing,omitempty"`
	NoDue   bool           `api:"no_due,optional" json:"no_due,omitempty"`
	Lines   []DeadlineLine `api:"lines" json:"lines"`
}

func deadlinesOp(env *app.Env) app.Op {
	return app.Query(env, "reminder.deadlines", func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} },
		func(_ context.Context, _ DeadlinesReq) (DeadlinesResult, error) {
			return withDB(env, func(db *store.DB) (DeadlinesResult, error) { return Deadlines(db), nil })
		},
		define.Doc("期限ファイルの行と、チケットとして取り込み済みか(tb deadlines-import が読む)"),
		define.Authz("cmd:reminder:deadlines-import"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/deadlines"),
	)
}

// Deadlines は期限ファイルの各行を、日付が正しいか・取り込み済みかと合わせて返す。
func Deadlines(db *store.DB) DeadlinesResult {
	var hasDue int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name='due'`).Scan(&hasDue)
	env := reminder.LoadEnv()
	res := DeadlinesResult{File: env.Deadlines, NoDue: hasDue == 0}
	if res.NoDue {
		return res
	}
	lines, ok := env.Lines()
	if !ok {
		res.Missing = true
		return res
	}
	for _, l := range lines {
		d := DeadlineLine{Date: l.Date, Label: l.Label, State: "new"}
		var valid int
		_ = db.QueryRow(`SELECT date(?1) IS ?1`, l.Date).Scan(&valid)
		switch {
		case valid != 1:
			d.State = "invalid"
		default:
			if have := reminder.DeadlineTicket(db.DB, l.Date, l.Label); have != "" {
				d.State, d.Ticket = "have", have
			}
		}
		res.Lines = append(res.Lines, d)
	}
	return res
}
