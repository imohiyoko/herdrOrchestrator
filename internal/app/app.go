// Package app はアプリケーション API の土台(ADR 0008)。操作は imohiyoko/API の define で定義し、
// 入口(tb の CLI・Web UI・scheduler・agent セット)は runtime.Server.Invoke で呼ぶ。
//
// ここに置くもの:
//   - 主体(port.Principal)と herdr の主体("human" / agent 名 / "system:<名前>")の写し(principal.go)
//   - 入口が確かめた主体をプロセス内の短命のトークンにする Authenticator(tokens.go)
//   - 入口の段(commands.tbl の場所が entry の cmd: の行)を判定する Authorizer(authz.go)
//   - 操作の中の判定と ask を扱う門 Guard と、承認待ちの結果 Pending(guard.go)
//
// app とその下のパッケージは、出力・終了コード・環境変数(HERDR_PANE_ID など)を扱わない。
package app

import (
	"database/sql"
	"io"
	"strconv"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// Env は操作が使う環境。入口が作って渡す(テストでは一時 DB を入れる)。
type Env struct {
	// DB は書き込みに使える DB を返す。返した関数で閉じる。操作ごとに呼ぶ。
	DB func() (*store.DB, func(), error)
	// Now は今の時刻(UTC の ISO 8601、秒まで)。nil なら時計。
	Now func() string
	// Warn は操作の途中の警告(「tb: …」の 1 行ずつ)の書き込み先。tb の入口は標準エラー。nil なら捨てる。
	Warn io.Writer
	// PendingQ は、今積んだ承認待ち(source の source_id)の Q-<n> を返し、決められる agent に知らせる(tb の入口が入れる)。
	// nil なら Q-<n> を引くだけ。
	PendingQ func(db *sql.DB, source string, sourceID int64) string
}

// Warner は Env.Warn(無ければ捨てる)。
func (e *Env) Warner() io.Writer {
	if e != nil && e.Warn != nil {
		return e.Warn
	}
	return io.Discard
}

// QOf は承認待ちの行(source の sourceID)の Q-<n>(Env.PendingQ があればそれを使う)。無ければ空。
func (e *Env) QOf(db *sql.DB, source string, sourceID int64) string {
	if e != nil && e.PendingQ != nil {
		return e.PendingQ(db, source, sourceID)
	}
	var id int64
	if db.QueryRow(`SELECT id FROM requests WHERE source=? AND source_id=?`, source, sourceID).Scan(&id) != nil {
		return ""
	}
	return "Q-" + strconv.FormatInt(id, 10)
}

// NowString は Env.Now(無ければ時計)。
func (e *Env) NowString() string {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// OpenStore は path の DB を session で開く Env.DB(Web UI などの入口用)。
func OpenStore(path, session string, opt *store.Options) func() (*store.DB, func(), error) {
	return func() (*store.DB, func(), error) {
		db, err := store.Open(path, session, opt)
		if err != nil {
			return nil, nil, err
		}
		return db, func() { db.Close() }, nil
	}
}

// Shared は開いてある DB をそのまま使う Env.DB(閉じるのは呼んだ側。tb の入口用)。
func Shared(db *store.DB) func() (*store.DB, func(), error) {
	return func() (*store.DB, func(), error) { return db, func() {}, nil }
}
