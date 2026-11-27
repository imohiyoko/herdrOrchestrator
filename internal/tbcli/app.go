package tbcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/app/server"
	"github.com/imohiyoko/herdrorchestrator/internal/app/ticket"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// callApp はアプリケーション API の操作 op を、今の呼び出し元を主体にして呼ぶ(ADR 0008)。
// db は入口で開いた DB(操作もこれを使う)。what は止めたときの文言に入る tb の文面("tb resolve" など)。
// エラーは今の tb の終了コードと文言にする。
func callApp(db *store.DB, what, op string, in, out any) error {
	by, err := callerPrincipal(what)
	if err != nil {
		return err
	}
	return callAppAs(db, by, op, in, out)
}

// callAppEnv は入口で作った環境 env で、主体 by として操作 op を呼ぶ。
func callAppEnv(env *app.Env, by port.Principal, op string, in, out any) error {
	c, err := server.New(env)
	if err != nil {
		return err
	}
	return appErr(c.Call(context.Background(), by, op, in, out))
}

// callAppWarn は callApp に、操作の途中の警告の書き込み先(標準エラー)を足したもの。
func callAppWarn(db *store.DB, what string, stderr io.Writer, op string, in, out any) error {
	by, err := callerPrincipal(what)
	if err != nil {
		return err
	}
	return callAppW(db, by, stderr, op, in, out)
}

// callAppActor は callApp に、記録に残す「した者」の名前(actor_name で引いたもの)を足したもの。
func callAppActor(db *store.DB, what, actor, op string, in, out any) error {
	by, err := callerPrincipal(what)
	if err != nil {
		return err
	}
	env := appEnv(db, nil)
	env.ActorName = func(string) string { return actor }
	c, err := server.New(env)
	if err != nil {
		return err
	}
	return appErr(c.Call(context.Background(), by, op, in, out))
}

// callAppIO は callApp に、操作の途中の出力の先(標準出力 w と標準エラー stderr)を足したもの(承認して実行するコマンドの出力など)。
func callAppIO(db *store.DB, what string, w, stderr io.Writer, op string, in, out any) error {
	by, err := callerPrincipal(what)
	if err != nil {
		return err
	}
	env := appEnv(db, stderr)
	env.Out = w
	c, err := server.New(env)
	if err != nil {
		return err
	}
	return appErr(c.Call(context.Background(), by, op, in, out))
}

// callAppAs は主体 by(入口で確かめたもの)として操作 op を呼ぶ。
func callAppAs(db *store.DB, by port.Principal, op string, in, out any) error {
	return callAppW(db, by, nil, op, in, out)
}

// callAppW は callAppAs に、操作の途中の警告の書き込み先(標準エラー)を足したもの。
func callAppW(db *store.DB, by port.Principal, stderr io.Writer, op string, in, out any) error {
	c, err := server.New(appEnv(db, stderr))
	if err != nil {
		return err
	}
	return appErr(c.Call(context.Background(), by, op, in, out))
}

// appEnv は tb の入口が API に渡す環境(承認待ちの知らせと報告の届け先は tb のもの)。
func appEnv(db *store.DB, stderr io.Writer) *app.Env {
	// 履歴の「変えた者」は openWrite と eventActorResolve が作ったトリガーのまま(pane:<id> も含め今と同じ)。
	// コマンドの入口の段は Run の perms.Gate が判定済み。
	return &app.Env{DB: app.Shared(db), Now: now, PendingQ: pendingQ, Warn: stderr,
		EventActor: func(string) string { return "" }, EntryChecked: true,
		Deliver: func(db *store.DB, id int64) {
			_ = deliverComment(db, io.Discard, io.Discard, strconv.FormatInt(id, 10))
		}}
}

// pendingExit は承認待ちの結果を今の tb の形にする: agent の操作の待ち(L-<n>)は標準出力に 1 行で 20、
// コマンドの ask(Q-<n>)は説明を標準エラーに出して 20。
func pendingExit(w io.Writer, p *app.Pending) error {
	if p.Route == "L" {
		fmt.Fprintln(w, p.Message)
		return &exit{20, ""}
	}
	return &exit{20, p.Message}
}

// callerPrincipal は今の呼び出し元の主体(入口の確かめ。perms.EnforceTarget と同じ決め方)。
// pane の中なら pane の agent(agent がいなければ人)、pane の外なら system:<名前>(TB_SYSTEM)か人。
// 呼び出し元の確かめ(identity.Authenticate)は Run の入口で済んでいる。
func callerPrincipal(what string) (port.Principal, error) {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		if identity.System != "" {
			return app.System(identity.System), nil
		}
		return app.Human(), nil
	}
	n, err := identity.PaneOwnerName(pane)
	if err != nil {
		return port.Principal{}, &exit{exitErr, what + ": herdr agent list に失敗したため、人の端末からの実行か確かめられません"}
	}
	if n == "" {
		return app.Human(), nil
	}
	return app.Agent(n), nil
}

// appErr は操作のエラー(*define.Error)を tb の終了コードと文言にする。
// 承認が要るが積めなかった = 20、使い方の誤り = 2、主体を確かめられない = 3、ほか = 1。
func appErr(err error) error {
	if err == nil {
		return nil
	}
	var e *define.Error
	if !errors.As(err, &e) {
		return err
	}
	msg := e.Message
	if msg == "" {
		msg = string(e.Code)
	}
	for _, r := range e.Reasons {
		if code, ok := agent.ExitReasons[r]; ok {
			return &exit{code, msg}
		}
	}
	switch {
	case slices.Contains(e.Reasons, ticket.ReasonQuiet): // 理由は標準エラーに出し終えている
		return &exit{exitErr, ""}
	case e.Code == define.CodePermissionDenied && slices.Contains(e.Reasons, app.ReasonApprovalRequired):
		return &exit{20, msg}
	case e.Code == define.CodeInvalidArgument:
		return &exit{exitUsage, msg}
	case e.Code == define.CodeUnauthenticated:
		return &exit{3, msg}
	}
	return &exit{exitErr, msg}
}
