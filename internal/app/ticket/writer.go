package ticket

import (
	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// writer はチケットを書く操作が使うもの(DB・状態の定義・今の時刻)。
type writer struct {
	db  *store.DB
	st  tickets.StatusSet
	now string
}

// change は DB を開き、履歴のトリガーを主体で作り直して f を呼び、通知の規則を流す。
// 結果の状態は分類 cat の代表の状態。
func change(env *app.Env, who, id string, f func(*writer) error, cat string) (StateResult, error) {
	db, done, err := env.DB()
	if err != nil {
		return StateResult{}, internal(err)
	}
	defer done()
	w := &writer{db: db, st: tickets.LoadStatuses(db.DB, db.Session), now: env.NowString()}
	if err := events(env, db, who); err != nil {
		return StateResult{}, internal(err)
	}
	if err := f(w); err != nil {
		return StateResult{}, refused(err)
	}
	notify.TicketRules(db.DB, db.Session, w.st)
	return StateResult{ID: id, Status: w.st.Main(cat)}, nil
}
