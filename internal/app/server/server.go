// Package server はアプリケーション API の全サービスの操作を 1 つの imohiyoko/API の Server にまとめ、
// 入口(tb の CLI・Web UI など)がプロセス内から呼ぶ口(Client.Call)を置く(ADR 0008)。
package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	appalert "github.com/imohiyoko/herdrorchestrator/internal/app/alert"
	"github.com/imohiyoko/herdrorchestrator/internal/app/approval"
	appconfig "github.com/imohiyoko/herdrorchestrator/internal/app/config"
	"github.com/imohiyoko/herdrorchestrator/internal/app/job"
	appmessage "github.com/imohiyoko/herdrorchestrator/internal/app/message"
	appnotify "github.com/imohiyoko/herdrorchestrator/internal/app/notify"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
)

// Ops は全サービスの操作(サービスを足したらここに足す)。
func Ops(env *app.Env) []app.Op {
	var ops []app.Op
	ops = append(ops, ticket.Ops(env)...)
	ops = append(ops, agent.Ops(env)...)
	ops = append(ops, job.Ops(env)...)
	ops = append(ops, approval.Ops(env)...)
	ops = append(ops, appconfig.Ops(env)...)
	ops = append(ops, appalert.Ops(env)...)
	ops = append(ops, appnotify.Ops(env)...)
	ops = append(ops, appmessage.Ops(env)...)
	return ops
}

// Client は入口が操作を呼ぶ口。
type Client struct {
	Server *runtime.Server
	tokens *app.Tokens
}

// New は env で全サービスの操作を組み立てる。
// 公開範囲は internal だけ(人と agent・system の両方を受け、エラーの文言を見せる)。外へ出す口は作らない。
func New(env *app.Env) (*Client, error) {
	reg, err := app.Registry(Ops(env)...)
	if err != nil {
		return nil, err
	}
	tokens := &app.Tokens{}
	srv, err := runtime.Build(reg, runtime.Config{
		Authenticator: tokens,
		Authorizer:    app.Authorizer{Env: env},
		Audiences: map[string]runtime.AudienceConfig{
			define.Internal: {Principals: []string{port.PrincipalUser, port.PrincipalServiceAccount}, Disclosure: runtime.DiscloseDetail},
		},
		// 失敗は *define.Error で入口に返す(文言は入口が出す)。ログは出さない
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, err
	}
	return &Client{Server: srv, tokens: tokens}, nil
}

// Call は主体 by として操作 op を呼び、結果を out(出力の型のポインター)に読み直す。
// by は入口が確かめた主体(tb は呼び出し元の確かめ、Web UI はログイン)。エラーは *define.Error。
func (c *Client) Call(ctx context.Context, by port.Principal, op string, in, out any) error {
	tok, revoke := c.tokens.Issue(by)
	defer revoke()
	res, err := c.Server.Invoke(ctx, define.Internal, op, tok, in)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return define.Errorf(define.CodeInternal, "%s: 結果を読めません: %v", op, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return define.Errorf(define.CodeInternal, "%s: 結果を読めません: %v", op, err)
	}
	return nil
}
