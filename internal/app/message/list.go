package message

// 送信と受信の読むだけの操作(tb outbox / inbox / inbox-read の表示)。Text は tb の出力そのまま。

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/rowfmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"github.com/imohiyoko/herdrorchestrator/internal/tickets"
)

// OutboxReq は message.outbox の入力。ID があればその 1 件(JSON の 1 行)。Status は pending / queued / sent / denied。
type OutboxReq struct {
	ID     int64  `api:"id,optional"`
	Status string `api:"status,optional"`
	JSON   bool   `api:"json,optional"`
}

// InboxReq は message.inbox の入力。受信者は呼び手(自分宛ての行だけ)。
type InboxReq struct {
	Unread bool `api:"unread,optional"`
	JSON   bool `api:"json,optional"`
}

// InboxGetReq は message.inbox.get の入力(I-<n>。読んだ印は付けない。付けるのは message.read)。
type InboxGetReq struct {
	ID   string `api:"id,path"`
	JSON bool   `api:"json,optional"`
}

// TextResult は読むだけの操作の結果。Text は tb が標準出力に出す文字列。
type TextResult struct {
	Text string `api:"text" json:"text"`
}

func listOps(env *app.Env) []app.Op {
	pass := func(context.Context, *app.Env, string, any) app.Decision { return app.Decision{} }
	outbox := app.Query(env, "message.outbox", pass,
		func(_ context.Context, in OutboxReq) (TextResult, error) {
			return textOf(env, func(db *store.DB) (string, error) { return outboxText(db, in) })
		},
		define.Doc("送信の一覧(tb outbox。新しい順に 100 件)"),
		define.Authz("cmd:message:outbox"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/outbox"),
	)
	inbox := app.Query(env, "message.inbox", pass,
		func(ctx context.Context, in InboxReq) (TextResult, error) {
			who, err := recipient(ctx)
			if err != nil {
				return TextResult{}, err
			}
			return textOf(env, func(db *store.DB) (string, error) { return inboxText(db, who, in) })
		},
		define.Doc("呼び手宛ての受信の一覧(tb inbox)"),
		define.Authz("cmd:message:inbox"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/inbox"),
	)
	get := app.Query(env, "message.inbox.get", pass,
		func(ctx context.Context, in InboxGetReq) (TextResult, error) {
			who, err := recipient(ctx)
			if err != nil {
				return TextResult{}, err
			}
			return textOf(env, func(db *store.DB) (string, error) { return inboxGet(db, who, in) })
		},
		define.Doc("呼び手宛ての受信 1 件の全文(tb inbox-read の表示。読んだ印は付けない)"),
		define.Authz("cmd:message:read"),
		define.Expose(define.Internal, define.REST),
		define.HTTP("GET /v1/inbox/{id}"),
	)
	return []app.Op{outbox, inbox, get}
}

// recipient は呼び手の名前(受信者)。決められなければ Unauthenticated(message.read と同じ)。
func recipient(ctx context.Context) (string, error) {
	p, _ := define.PrincipalFrom(ctx)
	if who := app.Who(p); who != "" {
		return who, nil
	}
	return "", &define.Error{Code: define.CodeUnauthenticated, Message: "受信者を特定できません", Reasons: []string{app.ReasonNoSender}}
}

func textOf(env *app.Env, f func(*store.DB) (string, error)) (TextResult, error) {
	db, done, err := env.DB()
	if err != nil {
		return TextResult{}, define.Errorf(define.CodeInternal, "%s", err.Error())
	}
	defer done()
	s, err := f(db)
	if err != nil {
		if _, ok := err.(*define.Error); !ok {
			err = define.Errorf(define.CodeInternal, "%s", err.Error())
		}
		return TextResult{}, err
	}
	return TextResult{Text: s}, nil
}

// prefixID は jq の .<列> = "<接頭辞>\(.<列>)"(NULL は "null")。
func prefixID(cols []string, vals []any, col, prefix string) {
	for i, c := range cols {
		if c == col {
			if vals[i] == nil {
				vals[i] = prefix + "null"
			} else {
				vals[i] = prefix + tickets.Text(vals[i])
			}
		}
	}
}

func outboxText(db *store.DB, in OutboxReq) (string, error) {
	var b strings.Builder
	if in.ID != 0 {
		err := store.Each(db.DB, `SELECT * FROM outbox WHERE id=?`, []any{in.ID}, func(cols []string, vals []any) error {
			prefixID(cols, vals, "id", "O-")
			return rowfmt.EmitRow(&b, true, cols, vals, "")
		})
		return b.String(), err
	}
	where, qa := "1=1", []any{}
	if in.Status != "" {
		if !slices.Contains([]string{"pending", "queued", "sent", "denied"}, in.Status) {
			return "", define.Errorf(define.CodeInvalidArgument, "outbox: --status は pending|queued|sent|denied")
		}
		where, qa = "status=?", []any{in.Status}
	}
	if in.JSON {
		err := store.Each(db.DB, "SELECT * FROM outbox WHERE ("+where+") ORDER BY id DESC LIMIT 100", qa, func(cols []string, vals []any) error {
			prefixID(cols, vals, "id", "O-")
			return rowfmt.EmitRow(&b, true, cols, vals, "")
		})
		return b.String(), err
	}
	err := store.Each(db.DB, `SELECT 'O-'||id, status, from_name, target, kind, COALESCE(task_id,'-'), created_at,
	     substr(replace(replace(text, char(10), ' '), char(9), ' '),1,80) FROM outbox WHERE (`+where+`) ORDER BY id DESC LIMIT 100`, qa, func(cols []string, vals []any) error {
		return rowfmt.EmitRow(&b, false, cols, vals, "-")
	})
	return b.String(), err
}

func inboxText(db *store.DB, who string, in InboxReq) (string, error) {
	where := "recipient=?"
	if in.Unread {
		where += " AND read_at IS NULL"
	}
	var b strings.Builder
	if in.JSON {
		err := store.Each(db.DB, "SELECT * FROM inbox WHERE "+where+" ORDER BY id", []any{who}, func(cols []string, vals []any) error {
			prefixID(cols, vals, "id", "I-")
			prefixID(cols, vals, "outbox_id", "O-")
			return rowfmt.EmitRow(&b, true, cols, vals, "")
		})
		return b.String(), err
	}
	err := store.Each(db.DB, `SELECT 'I-'||id, CASE WHEN read_at IS NULL THEN 'unread' ELSE 'read' END, from_name, kind, COALESCE(task_id,'-'), created_at,
	     substr(replace(replace(text, char(10), ' '), char(9), ' '),1,200) FROM inbox WHERE `+where+` ORDER BY id`, []any{who}, func(cols []string, vals []any) error {
		return rowfmt.EmitRow(&b, false, cols, vals, "-")
	})
	return b.String(), err
}

// inboxGet は自分宛ての 1 件(自分宛てでなければ「見つからない」。ほかの agent の行があるかも漏らさない)。
func inboxGet(db *store.DB, who string, in InboxGetReq) (string, error) {
	num := strings.TrimPrefix(in.ID, "I-")
	iid, perr := strconv.ParseInt(num, 10, 64)
	if num == "" || strings.Trim(num, "0123456789") != "" { // message.read と同じ(+1 などは受けない)
		perr = strconv.ErrSyntax
	}
	var cols []string
	var vals []any
	if perr == nil {
		if err := store.Each(db.DB, `SELECT * FROM inbox WHERE id=? AND recipient=?`, []any{iid, who}, func(c []string, v []any) error {
			if cols == nil {
				cols, vals = c, v
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	if cols == nil {
		return "", define.Errorf(define.CodeNotFound, "inbox に見つかりません: I-%s", num)
	}
	var b strings.Builder
	if in.JSON {
		prefixID(cols, vals, "id", "I-")
		prefixID(cols, vals, "outbox_id", "O-")
		err := rowfmt.EmitRow(&b, true, cols, vals, "")
		return b.String(), err
	}
	f := map[string]string{}
	for i, c := range cols {
		if vals[i] == nil {
			f[c] = "null"
		} else {
			f[c] = tickets.Text(vals[i])
		}
	}
	task := f["task_id"]
	if task == "null" {
		task = "-"
	}
	fmt.Fprintf(&b, "I-%s  from: %s / %s / %s / %s  (O-%s)\n\n%s\n", f["id"], f["from_name"], f["kind"], task, f["created_at"], f["outbox_id"], f["text"])
	return b.String(), nil
}
