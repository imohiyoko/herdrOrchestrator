package webui

import (
	"os"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
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
