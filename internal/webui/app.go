package webui

import (
	"errors"
	"os"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/app/server"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// appClient はセッション sess の DB(TBDB)でアプリケーション API を呼ぶ口(ADR 0008。runTB の置き換え)。
// DB は操作ごとに書き込みで開いて閉じる。スキーマの置き場は tb と同じく TB_SCHEMA_DIR で変えられる(テスト用)。
func appClient(sess string) (*server.Client, error) {
	var opt *store.Options
	if d := os.Getenv("TB_SCHEMA_DIR"); d != "" {
		opt = &store.Options{Schema: os.DirFS(d)}
	}
	return server.New(&app.Env{DB: app.OpenStore(TBDB(), sess, opt)})
}

// appState は操作の結果 → 画面に出す結果(rcState と同じ分け方)。
// 0 実行済み / 承認待ち / 前提違い・入力の誤りで断った / それ以外は失敗。
func appState(err error, pending *app.Pending) string {
	if err == nil {
		if pending != nil {
			return "pending"
		}
		return "done"
	}
	var e *define.Error
	if !errors.As(err, &e) {
		return "error"
	}
	for _, r := range e.Reasons { // tb の終了コードが 1・2 以外(4・10・11・12)の失敗は、前と同じく error
		if _, ok := agent.ExitReasons[r]; ok {
			return "error"
		}
	}
	if e.Code == define.CodeFailedPrecondition || e.Code == define.CodeInvalidArgument {
		return "refused"
	}
	return "error"
}
