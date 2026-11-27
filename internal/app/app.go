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
	"strings"
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
	// Deliver は、今書いた報告(comments の id)を宛先(指揮官など)に届ける(tb の入口が入れる。best effort)。nil なら届けない。
	Deliver func(db *store.DB, commentID int64)
	// EventActor は、チケットの履歴(task_events)に残す「変えた者」を主体 who から決める。
	// 空を返すと、入口が作った履歴のトリガーをそのまま使う(tb の入口: pane から引いた名前か pane:<id>)。
	// nil なら主体から決める(agent は名前、それ以外は human。tb の event_actor_default と同じ)。
	EventActor func(who string) string
	// EntryChecked は、入口がコマンドの入口の段(commands.tbl の entry の行)を判定済み(tb の入口。perms.Gate)。
	// true なら Entry の門は判定も記録もしない。
	EntryChecked bool
	// ActorName は、記録に残す「した者」の名前(リンクの created_by など)を主体 who から決める。
	// nil なら主体から(agent は名前、それ以外は human)。tb の入口は pane から引いた名前(actor_name)を入れる。
	ActorName func(who string) string
	// Reporter は報告コメントの報告した者(名前・pane・セッション)を主体 who から決める。
	// nil なら主体から(agent は名前、それ以外は human。pane とセッションは空)。tb の入口は herdr から引いたもの(resolve_label)を入れる。
	Reporter func(who string) (name, pane, session string)
	// ImmediateKinds は書いたらすぐ届ける報告コメントの種類(空白区切り)。空なら "question blocked"(tb の TB_IMMEDIATE_KINDS)。
	ImmediateKinds string
	// SchedDir / SchedShow / SchedLogs は scheduler の置き場所(jobs/・settings/ の親、本文を確かめる show.sh、ログ)。
	// 空なら tb と同じ(TB_SCHED_DIR / TB_SCHED_SHOW / LOG_DIR、無ければ <TB_ROOT>/../scheduler)。Web UI は自分の見ている所を入れる。
	SchedDir, SchedShow, SchedLogs string
	// Out は操作の途中の出力(tb が標準出力に出す行)をそのまま流す先(tb の入口は標準出力)。nil なら結果に集めて返す。
	Out io.Writer
	// ChildEnv は承認したコマンドを依頼者として動かす子(herdrctl tb …)の環境。nil ならこのプロセスの環境。
	ChildEnv []string
	// SchedJob / SchedOwner は、定期ジョブ(scheduler)から呼ばれたときのジョブ名と持ち主(tb の SCHED_JOB / SCHED_OWNER)。
	// 人の呼び出しのレシピの実行は、ジョブの持ち主の権限で行う。入口だけが入れる(呼び手の入力では受けない)。
	SchedJob, SchedOwner string
	// TbCmd は、その子で tb を動かすコマンド(nil ならこのプロセスの herdrctl tb)。Web UI は checkout の tb を入れる。
	TbCmd []string
}

// ReporterOf は主体 who の報告した者(Env.Reporter があればそれ)。
func (e *Env) ReporterOf(who string) (name, pane, session string) {
	if e != nil && e.Reporter != nil {
		return e.Reporter(who)
	}
	return e.ActorOf(who), "", ""
}

// ActorOf は主体 who の「した者」の名前(Env.ActorName があればそれ)。
func (e *Env) ActorOf(who string) string {
	if e != nil && e.ActorName != nil {
		return e.ActorName(who)
	}
	if who == "" || who == humanID || strings.HasPrefix(who, sysPrefix) {
		return humanID
	}
	return who
}

// EventActorOf は主体 who の履歴の「変えた者」(Env.EventActor があればそれ)。空なら入口のトリガーをそのまま使う。
func (e *Env) EventActorOf(who string) string {
	if e != nil && e.EventActor != nil {
		return e.EventActor(who)
	}
	if who == "" || who == humanID || strings.HasPrefix(who, sysPrefix) {
		return humanID
	}
	return who
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
