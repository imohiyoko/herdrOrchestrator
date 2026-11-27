package server

import (
	"context"
	"testing"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// ticket.count: 既定は未着手の分類、project・label・担当なしで絞る。ほかのセッションは数えない。
func TestTicketCount(t *testing.T) {
	env, dbp := fixture(t, "")
	db, err := store.Open(dbp, "default", &store.Options{NoSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO main.tasks (id, project, title, status, owner_agent, labels, created_at, updated_at, herdr_session) VALUES
		('T-web-001', 'web', 'a', 'todo', NULL, '["bug"]', 't', 't', 'default'),
		('T-web-002', 'web', 'b', 'todo', 'w1', '["bug"]', 't', 't', 'default'),
		('T-web-003', 'web', 'c', 'in_progress', 'w1', '[]', 't', 't', 'default'),
		('T-web-004', 'web', 'd', 'done', NULL, '["bug"]', 't', 't', 'default'),
		('T-api-001', 'api', 'e', 'todo', NULL, '[]', 't', 't', 'default'),
		('T-web-005', 'web', 'f', 'todo', NULL, '["bug"]', 't', 't', 'other')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	c, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		in   ticket.CountReq
		want int
	}{
		{ticket.CountReq{}, 3},
		{ticket.CountReq{Project: "web"}, 2},
		{ticket.CountReq{Project: "web", Unowned: true}, 1},
		{ticket.CountReq{Label: "bug"}, 2},
		{ticket.CountReq{Category: "todo,active"}, 4},
		{ticket.CountReq{Category: "done"}, 1},
		{ticket.CountReq{Project: "nope"}, 0},
	} {
		var r ticket.CountResult
		if err := c.Call(context.Background(), app.Agent("w1"), "ticket.count", x.in, &r); err != nil || r.Count != x.want {
			t.Errorf("%+v: %d %v (want %d)", x.in, r.Count, err, x.want)
		}
	}
	for _, in := range []ticket.CountReq{{Project: "a b"}, {Category: "doing"}} {
		var r ticket.CountResult
		if err := c.Call(context.Background(), app.Human(), "ticket.count", in, &r); err == nil {
			t.Errorf("%+v: 断るはず", in)
		}
	}
}
