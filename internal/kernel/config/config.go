// Package config は設定(config 表と conf ファイル)を引く・書く(ADR 0004 の土台の store)。
// 振る舞いは taskboard/lib/config.sh と同じにする。
package config

import (
	"database/sql"
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
var Keys = []string{"projects", "approvals", "grants", "routes", "delivery", "limits", "notify", "notify-rules", "compact", "slash", "sched-watch", "spawn-policy", "role-models", "conv-routes", "conv-learn", "conv-stages", "protect", "org", "labels", "messages", "nudge", "statuses", "system-labels"}

var keyInfo = map[string]Key{
	"projects":      {"TB_CONFIG", "projects.conf", true},
	"approvals":     {"TB_APPROVALS", "approvals.conf", true},
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

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// DBRow は config_db_row と同じ: このセッションの行 > 共通の行(*)。shared は共通の行だったか。
// 本文は sqlite3 の出力を $(...) で受けたときと同じく、末尾の改行を落とす。
func DBRow(db *sql.DB, session, key string) (body string, shared, ok bool) {
	var hs string
	err := db.QueryRow(`SELECT herdr_session, body FROM main.config WHERE key=? AND herdr_session IN (?, '*') ORDER BY herdr_session='*' LIMIT 1`, key, session).Scan(&hs, &body)
	if err != nil {
		return "", false, false
	}
	return strings.TrimRight(body, "\n"), hs == "*", true
}

// Lookup は config_lookup と同じ順に引く。src は file:<パス> / example:<パス> / db:<セッション> / db:* / none。
func Lookup(db *sql.DB, session, key string) (body, src string, ok bool) {
	k, known := keyInfo[key]
	if !known {
		return "", "none", false
	}
	if f := os.Getenv(k.Env); f != "" {
		if isFile(f) {
			b, _ := readBody(f)
			return b, "file:" + f, true
		}
		if k.Example && isFile(f+".example") {
			b, _ := readBody(f + ".example")
			return b, "example:" + f + ".example", true
		}
		return "", "none", false
	}
	if db != nil {
		if b, shared, ok := DBRow(db, session, key); ok {
			if shared {
				return b, "db:*", true
			}
			return b, "db:" + session, true
		}
	}
	f := filepath.Join(Dir(), k.File)
	if Dir() == "" {
		f = k.File
	}
	if isFile(f) {
		b, _ := readBody(f)
		return b, "file:" + f, true
	}
	if k.Example && isFile(f+".example") {
		b, _ := readBody(f + ".example")
		return b, "example:" + f + ".example", true
	}
	return "", "none", false
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

// HasRow は (このセッション|共通) の行がそのキーにあるか。
func HasRow(db *sql.DB, session, key string, shared bool) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM main.config WHERE herdr_session=? AND key=?`, rowSession(session, shared), key).Scan(&n)
	return n > 0, err
}

// Unset は config_unset と同じ: DB の行だけを消し、消したときだけ履歴を残す。
func Unset(db *sql.DB, session, key string, shared bool) error {
	hs := rowSession(session, shared)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.Exec(`DELETE FROM main.config WHERE herdr_session=? AND key=?`, hs, key)
	if err != nil {
		return err
	}
	if c, _ := r.RowsAffected(); c > 0 {
		if _, err := tx.Exec(`INSERT INTO main.config_changes (herdr_session, key, body, changed_by, how, changed_at) VALUES (?, ?, NULL, 'human', 'unset', ?)`, hs, key, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
