// Package config は設定(config 表と conf ファイル)を引く・書く(ADR 0004 の土台の store)。
// 振る舞いは taskboard/lib/config.sh と同じにする。
package config

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Key は 1 つの設定のキーの情報(config_key_info)。
type Key struct {
	Env     string // 上書きの環境変数(ファイルのパス)
	File    string // checkout のファイル名
	Example bool   // .example も既定値として読む
}

// Keys は config.sh の CONFIG_KEYS と同じ順。
var Keys = []string{"projects", "policy", "approvals", "grants", "routes", "delivery", "limits", "notify", "notify-rules", "compact", "slash", "sched-watch", "spawn-policy", "role-models", "conv-routes", "conv-learn", "conv-stages", "protect", "org", "labels", "messages", "nudge", "statuses", "system-labels"}

var keyInfo = map[string]Key{
	"projects":      {"TB_CONFIG", "projects.conf", true},
	"policy":        {"TB_POLICY", "policy.conf", false},
	"approvals":     {"TB_APPROVALS", "approvals.conf", false}, // 古い権限の設定(ADR 0007 で読まない。tb policy import が移す)
	"grants":        {"TB_GRANTS", "grants.conf", false},
	"routes":        {"TB_ROUTES", "routes.conf", false},
	"delivery":      {"TB_DELIVERY", "delivery.conf", false},
	"notify":        {"TB_NOTIFY_CONF", "notify.conf", false},
	"notify-rules":  {"TB_NOTIFY_RULES", "notify-rules.conf", false},
	"limits":        {"TB_LIMITS", "limits.conf", true},
	"compact":       {"TB_COMPACT_CONF", "compact.conf", true},
	"slash":         {"TB_SLASH_CONF", "slash.conf", true},
	"sched-watch":   {"TB_SCHEDWATCH_CONF", "sched-watch.conf", true},
	"spawn-policy":  {"TB_SPAWN_POLICY", "spawn-policy.conf", true},
	"role-models":   {"TB_ROLE_MODELS", "role-models.conf", true},
	"conv-routes":   {"TB_CONV_ROUTES", "conv-routes.conf", false},
	"conv-learn":    {"TB_CONV_LEARN", "conv-learn.conf", true},
	"conv-stages":   {"TB_CONV_STAGES", "conv-stages.conf", true},
	"protect":       {"TB_PROTECT", "protect.conf", false},
	"org":           {"TB_ORG", "org.conf", false},
	"labels":        {"TB_LABELS", "labels.conf", false},
	"statuses":      {"TB_STATUSES", "statuses.conf", false},
	"system-labels": {"TB_SYSTEM_LABELS", "system-labels.conf", false},
	"nudge":         {"TB_NUDGE", "nudge.conf", false},
	"messages":      {"TB_MESSAGES", "messages.conf", false},
}

// Info はキーの情報。知らないキーなら ok=false。
func Info(key string) (Key, bool) {
	k, ok := keyInfo[key]
	return k, ok
}

// Dir は checkout の設定ファイルの置き場(bin/tb が渡す TB_CONFIG_DIR)。
func Dir() string { return os.Getenv("TB_CONFIG_DIR") }

// readBody はファイルを $(cat f) と同じく、末尾の改行を落として読む。
func readBody(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(b), "\n"), true
}

// unreachable はファイルがあるかどうかを確かめられない(stat が「無い」以外の理由で失敗した)。「ファイルが無い」とは分ける。
func unreachable(path string) bool {
	_, err := os.Stat(path)
	return err != nil && !errors.Is(err, fs.ErrNotExist)
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// DBRow は config_db_row と同じ: このセッションの行 > 共通の行(*)。shared は共通の行だったか。
// 本文は sqlite3 の出力を $(...) で受けたときと同じく、末尾の改行を落とす。
func DBRow(db *sql.DB, session, key string) (body string, shared, ok bool) {
	body, shared, ok, _ = dbRow(db, session, key)
	return
}

// noConfigTable は config そのものが無い(表がまだ作られていない)か。誤りの文面では見分けず、sqlite_master で確かめる。
func noConfigTable(db *sql.DB) bool {
	var n int
	return db.QueryRow(`SELECT count(*) FROM main.sqlite_master WHERE name='config'`).Scan(&n) == nil && n == 0
}

// dbRow は DBRow に加えて、問い合わせの失敗(行が無い・表がまだ無いは失敗にしない)を返す。
func dbRow(db *sql.DB, session, key string) (body string, shared, ok bool, err error) {
	var hs string
	err = db.QueryRow(`SELECT herdr_session, body FROM main.config WHERE key=? AND herdr_session IN (?, '*') ORDER BY herdr_session='*' LIMIT 1`, key, session).Scan(&hs, &body)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || noConfigTable(db) {
			err = nil
		}
		return "", false, false, err
	}
	return strings.TrimRight(body, "\n"), hs == "*", true, nil
}

// Lookup は config_lookup と同じ順に引く。src は file:<パス> / example:<パス> / db:<セッション> / db:* / none。
// 読み取りの失敗は「無い」と同じに扱う(従来どおり)。失敗を区別したいとき(権限の判定)は LookupStrict。
func Lookup(db *sql.DB, session, key string) (body, src string, ok bool) {
	body, src, ok, _ = LookupStrict(db, session, key)
	return
}

// LookupStrict は Lookup と同じ順に引き、読めなかった出どころ(ファイルの読み取り・DB の問い合わせの失敗)を readErr に返す
// (bash の CFG_ERR)。readErr が空でないときも、Lookup と同じく次の出どころへ進んだ結果を返す。
func LookupStrict(db *sql.DB, session, key string) (body, src string, ok bool, readErr string) {
	k, known := keyInfo[key]
	if !known {
		return "", "none", false, ""
	}
	read := func(p, s string) (string, string, bool, string) {
		b, ok := readBody(p)
		if !ok {
			readErr = strings.TrimSpace(readErr + " " + s + p)
		}
		return b, s + p, true, readErr
	}
	if f := os.Getenv(k.Env); f != "" {
		if isFile(f) {
			return read(f, "file:")
		}
		if k.Example && isFile(f+".example") {
			return read(f+".example", "example:")
		}
		if unreachable(f) {
			return "", "none", false, "stat:" + f
		}
		return "", "none", false, ""
	}
	if db != nil {
		b, shared, ok, err := dbRow(db, session, key)
		if err != nil {
			readErr = "db:" + err.Error()
		}
		if ok {
			if shared {
				return b, "db:*", true, readErr
			}
			return b, "db:" + session, true, readErr
		}
	}
	f := filepath.Join(Dir(), k.File)
	if Dir() == "" {
		f = k.File
	}
	if isFile(f) {
		return read(f, "file:")
	}
	if k.Example && isFile(f+".example") {
		return read(f+".example", "example:")
	}
	if unreachable(f) {
		readErr = strings.TrimSpace(readErr + " stat:" + f)
	}
	return "", "none", false, readErr
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func rowSession(session string, shared bool) string {
	if shared {
		return "*"
	}
	return session
}

// DBPut は config_db_put と同じ: 行を書き、履歴(config_changes)を 1 行足す。by が空なら human。
func DBPut(db *sql.DB, session, key, body, how string, shared bool, by string) error {
	if by == "" {
		by = "human"
	}
	hs, n := rowSession(session, shared), now()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO main.config (herdr_session, key, body, updated_by, updated_at) VALUES (?, ?, ?, ?, ?)
      ON CONFLICT (herdr_session, key) DO UPDATE SET body=excluded.body, updated_by=excluded.updated_by, updated_at=excluded.updated_at`, hs, key, body, by, n); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO main.config_changes (herdr_session, key, body, changed_by, how, changed_at) VALUES (?, ?, ?, ?, ?, ?)`, hs, key, body, by, how, n); err != nil {
		return err
	}
	return tx.Commit()
}

// DBRowAt は (このセッション|共通) の行そのものの本文(無ければ空)。引く順をたどらない。
// 行が無い・表がまだ無いは空。問い合わせの失敗は err(権限の確かめに使うので、空とは分ける)。
func DBRowAt(db *sql.DB, session, key string, shared bool) (string, error) {
	var body string
	err := db.QueryRow(`SELECT body FROM main.config WHERE herdr_session=? AND key=?`, rowSession(session, shared), key).Scan(&body)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || noConfigTable(db) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimRight(body, "\n"), nil
}

// LookupWithout は、このセッションの行(shared なら共通の行も)が無いとしたら効く本文(unset のあとに効くもの)。
// 環境変数で明示したファイルは見ない(そのときは unset できない)。
func LookupWithout(db *sql.DB, session, key string, shared bool) (body, readErr string) {
	k, known := keyInfo[key]
	if !known {
		return "", ""
	}
	if !shared && db != nil {
		b, sh, ok, err := dbRow(db, "\x00", key) // 名前にならないセッション: 共通の行だけを引く
		if err != nil {
			return "", "db:" + err.Error()
		}
		if ok && sh {
			return b, ""
		}
	}
	f := filepath.Join(Dir(), k.File)
	if Dir() == "" {
		f = k.File
	}
	for _, p := range []string{f, f + ".example"} {
		if p != f && !k.Example {
			break
		}
		if isFile(p) {
			b, ok := readBody(p)
			if !ok {
				return "", "file:" + p
			}
			return b, ""
		}
	}
	if unreachable(f) {
		return "", "stat:" + f
	}
	return "", ""
}

// HasRow は (このセッション|共通) の行がそのキーにあるか。
func HasRow(db *sql.DB, session, key string, shared bool) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM main.config WHERE herdr_session=? AND key=?`, rowSession(session, shared), key).Scan(&n)
	return n > 0, err
}

// Unset は config_unset と同じ: DB の行だけを消し、消したときだけ履歴を残す。
func Unset(db *sql.DB, session, key string, shared bool) error {
	return UnsetBy(db, session, key, shared, "")
}

// UnsetBy は Unset に、履歴に残す消した者 by(空なら human)を足したもの。
func UnsetBy(db *sql.DB, session, key string, shared bool, by string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := unsetTx(tx, session, key, shared, by); err != nil {
		return err
	}
	return tx.Commit()
}

// UnsetTx は Unset を呼び出し側のトランザクションの中で行う(いくつかのキーをまとめて消すとき)。消したか を返す。
func UnsetTx(tx *sql.Tx, session, key string, shared bool) (bool, error) {
	return unsetTx(tx, session, key, shared, "")
}

func unsetTx(tx *sql.Tx, session, key string, shared bool, by string) (bool, error) {
	if by == "" {
		by = "human"
	}
	hs := rowSession(session, shared)
	r, err := tx.Exec(`DELETE FROM main.config WHERE herdr_session=? AND key=?`, hs, key)
	if err != nil {
		return false, err
	}
	c, _ := r.RowsAffected()
	if c > 0 {
		if _, err := tx.Exec(`INSERT INTO main.config_changes (herdr_session, key, body, changed_by, how, changed_at) VALUES (?, ?, NULL, ?, 'unset', ?)`, hs, key, by, now()); err != nil {
			return false, err
		}
	}
	return c > 0, nil
}
