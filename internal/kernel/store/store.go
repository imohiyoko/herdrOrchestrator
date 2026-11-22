// Package store は tasks.db を開き、スキーマを最新にし、このセッションの行だけを見る接続を返す(ADR 0002 の store)。
// 振る舞いは taskboard/lib/db.sh と同じにする。DB を直接触るのはこのパッケージだけにする。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	schemafs "github.com/imohiyoko/herdrorchestrator/taskboard/schema"
	tbsql "github.com/imohiyoko/herdrorchestrator/taskboard/sql"

	_ "modernc.org/sqlite"
)

// BusyTimeoutMS は tb の SQL_TIMEOUT の既定と同じ。
const BusyTimeoutMS = 5000

// Migration は 1 本分のスキーマ。
type Migration struct {
	Version int
	Name    string
	SQL     string
}

var reMigration = regexp.MustCompile(`^(\d{3})_.*\.sql$`)

// Migrations は fsys の NNN_*.sql を番号順に返す。
func Migrations(fsys fs.FS) ([]Migration, error) {
	ents, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var ms []Migration
	for _, e := range ents {
		m := reMigration.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		n, _ := strconv.Atoi(m[1])
		ms = append(ms, Migration{Version: n, Name: e.Name(), SQL: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	return ms, nil
}

// Options で Open の既定を差し替える(テスト用)。
type Options struct {
	Schema        fs.FS // 既定は埋め込みの taskboard/schema
	BusyTimeoutMS int   // 既定は BusyTimeoutMS(tb の SQL_TIMEOUT)
	NoSession     bool  // 一時ビューと見張りを作らない(db.sh の sql_all と同じ。tb touch が使う)
}

// DB は 1 本の接続。一時ビューと見張りのトリガーは接続ごとなので、接続は 1 本に固定する。
type DB struct {
	*sql.DB
	Path    string
	Session string
}

// Quote は SQL の文字列リテラルにする(db.sh の q)。
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Open は path の DB を開き、スキーマを最新にし、session の行だけを見る一時ビューと見張りを作る。
func Open(path, session string, opt *Options) (*DB, error) {
	schema := fs.FS(schemafs.FS)
	timeout := BusyTimeoutMS
	if opt != nil && opt.Schema != nil {
		schema = opt.Schema
	}
	if opt != nil && opt.BusyTimeoutMS > 0 {
		timeout = opt.BusyTimeoutMS
	}
	ms, err := Migrations(schema)
	if err != nil {
		return nil, err
	}
	// スキーマが無ければ DB に触る前に止める(開いただけで空の DB ファイルができる)
	if len(ms) == 0 {
		return nil, errors.New("store: スキーマのファイルが見つかりません")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout("+strconv.Itoa(timeout)+")")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	s := &DB{DB: db, Path: path, Session: session}
	if err := s.migrate(ms); err != nil {
		db.Close()
		return nil, err
	}
	if opt != nil && opt.NoSession {
		return s, nil
	}
	if err := s.setupSession(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// UserVersion は PRAGMA user_version。
func (s *DB) UserVersion() (int, error) {
	var v int
	err := s.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

// JournalMode は PRAGMA journal_mode。
func (s *DB) JournalMode() (string, error) {
	var m string
	err := s.QueryRow("PRAGMA journal_mode").Scan(&m)
	return m, err
}

func (s *DB) migrate(ms []Migration) error {
	v, err := s.UserVersion()
	if err != nil {
		return err
	}
	// WAL にする(DB ファイルに残る)。切り替えられなくても動作は変わらないので、失敗は無視する
	if jm, err := s.JournalMode(); err == nil && jm != "wal" {
		_, _ = s.Exec("PRAGMA journal_mode=WAL")
	}
	for _, m := range ms {
		if m.Version <= v {
			continue
		}
		if err := s.applyMigration(m); err != nil {
			// 別のプロセスが先に済ませて版が進んでいれば良しとする
			nv, verr := s.UserVersion()
			if verr != nil || nv < m.Version {
				return fmt.Errorf("store: 移行 %s に失敗: %w", m.Name, err)
			}
		}
		v = m.Version
	}
	return nil
}

// migration_sql と同じ: ロックを取った直後に版を確かめ、先に済んでいたら CHECK 違反で何も流さずに止まる
func (s *DB) applyMigration(m Migration) error {
	body := strings.ReplaceAll(m.SQL, "@SESSION@", Quote(s.Session))
	script := fmt.Sprintf("BEGIN IMMEDIATE;\n"+
		"CREATE TEMP TABLE _tb_migrate_guard (v INTEGER CHECK (v < %d)); INSERT INTO _tb_migrate_guard SELECT user_version FROM pragma_user_version;\n"+
		"%s\nPRAGMA user_version=%d; COMMIT;", m.Version, body, m.Version)
	_, err := s.Exec(script)
	// 途中で止まったら、開いたままのトランザクションと見張りの表を片づける
	_, _ = s.Exec("ROLLBACK")
	_, _ = s.Exec("DROP TABLE IF EXISTS temp._tb_migrate_guard")
	return err
}

// SessionTables は session_tables の一覧。
func SessionTables() []string { return nameList(tbsql.SessionTables) }

// SessionViews は session_views の一覧(main にビューとしてある互換ビュー)。
func SessionViews() []string { return nameList(tbsql.SessionViews) }

func nameList(body string) []string {
	var ts []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		ts = append(ts, l)
	}
	return ts
}

// db.sh の session_setup と同じ一時ビューと見張りを作る。この DB に herdr_session 列つきで今ある表だけ。
func (s *DB) setupSession() error {
	q := Quote(s.Session)
	const m = "tb: 別の herdr セッションの行は書き換えられません"
	for _, t := range SessionTables() {
		var ok int
		err := s.QueryRow(`SELECT COUNT(*) FROM sqlite_master m WHERE m.type='table' AND m.name=?
			AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session')`, t).Scan(&ok)
		if err != nil {
			return err
		}
		if ok == 0 {
			continue
		}
		cond := "IS " + q
		if t == "config" || t == "config_changes" {
			cond = "IN (" + q + ", '*')"
		}
		stmts := []string{
			fmt.Sprintf("CREATE TEMP VIEW %s AS SELECT * FROM main.%s WHERE herdr_session=%s", t, t, q),
			fmt.Sprintf("CREATE TEMP TRIGGER _tb_guard_%s_i BEFORE INSERT ON main.%s WHEN NOT (NEW.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END", t, t, cond, m, t),
			fmt.Sprintf("CREATE TEMP TRIGGER _tb_guard_%s_u BEFORE UPDATE ON main.%s WHEN NOT (OLD.herdr_session %s AND NEW.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END", t, t, cond, cond, m, t),
			fmt.Sprintf("CREATE TEMP TRIGGER _tb_guard_%s_d BEFORE DELETE ON main.%s WHEN NOT (OLD.herdr_session %s) BEGIN SELECT RAISE(ABORT,'%s(%s)'); END", t, t, cond, m, t),
		}
		for _, st := range stmts {
			if _, err := s.Exec(st); err != nil {
				return fmt.Errorf("store: セッションの準備(%s): %w", t, err)
			}
		}
	}
	// 互換ビュー(020 の alerts / proposals): 読みの一時ビューだけ。書き込みの見張りは振り分け先の表に付いている
	for _, v := range SessionViews() {
		var ok int
		err := s.QueryRow(`SELECT COUNT(*) FROM sqlite_master m WHERE m.type='view' AND m.name=?
			AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='herdr_session')`, v).Scan(&ok)
		if err != nil {
			return err
		}
		if ok == 0 {
			continue
		}
		if _, err := s.Exec(fmt.Sprintf("CREATE TEMP VIEW %s AS SELECT * FROM main.%s WHERE herdr_session=%s", v, v, q)); err != nil {
			return fmt.Errorf("store: セッションの準備(%s): %w", v, err)
		}
	}
	return nil
}

// Latest は埋め込みのスキーマの最新の版。
func Latest() int {
	ms, err := Migrations(schemafs.FS)
	if err != nil || len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].Version
}

// Status は DB を変えずに読んだ状態(herdrctl db status)。
type Status struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	UserVersion int    `json:"user_version"`
	Latest      int    `json:"latest"`
	JournalMode string `json:"journal_mode,omitempty"`
}

// Inspect は path の DB を読み取り専用で開いて版を返す。無ければ作らない。
func Inspect(path string) (Status, error) {
	st := Status{Path: path, Latest: Latest()}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	st.Exists = true
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout("+strconv.Itoa(BusyTimeoutMS)+")")
	if err != nil {
		return st, err
	}
	defer db.Close()
	if err := db.QueryRow("PRAGMA user_version").Scan(&st.UserVersion); err != nil {
		return st, err
	}
	err = db.QueryRow("PRAGMA journal_mode").Scan(&st.JournalMode)
	return st, err
}
