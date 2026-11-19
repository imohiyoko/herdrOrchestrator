package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	schemafs "github.com/imohiyoko/herdrorchestrator/taskboard/schema"
)

func open(t *testing.T, path, sess string, opt *Options) *DB {
	t.Helper()
	db, err := Open(path, sess, opt)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestFreshDBIsAtLatest(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "sub", "tasks.db"), "s1", nil)
	v, err := db.UserVersion()
	if err != nil || v != Latest() || v == 0 {
		t.Fatalf("user_version = %d, %v; want %d", v, err, Latest())
	}
	if jm, _ := db.JournalMode(); jm != "wal" {
		t.Errorf("journal_mode = %q, want wal", jm)
	}
}

func TestUpToDateDBIsNotTouched(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tasks.db")
	db := open(t, p, "s1", nil)
	var before int
	db.QueryRow("PRAGMA schema_version").Scan(&before)
	db.Close()
	db2 := open(t, p, "s1", nil)
	var after int
	db2.QueryRow("PRAGMA schema_version").Scan(&after)
	if before != after {
		t.Errorf("schema_version %d → %d", before, after)
	}
}

func TestSessionViewsAndGuards(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tasks.db")
	a := open(t, p, "a", nil)
	ins := func(db *DB, id, sess string) error {
		_, err := db.Exec(`INSERT INTO main.tasks (id, project, title, created_at, updated_at, herdr_session) VALUES (?, 'p', 't', 't', 't', ?)`, id, sess)
		return err
	}
	if err := ins(a, "T-a", "a"); err != nil {
		t.Fatalf("insert own session: %v", err)
	}
	if err := ins(a, "T-x", "b"); err == nil || !strings.Contains(err.Error(), "別の herdr セッション") {
		t.Fatalf("insert other session: err = %v, want guard", err)
	}
	b := open(t, p, "b", nil)
	if err := ins(b, "T-b", "b"); err != nil {
		t.Fatalf("insert b: %v", err)
	}
	count := func(db *DB, q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(a, "SELECT COUNT(*) FROM tasks"); n != 1 {
		t.Errorf("a sees %d tasks, want 1", n)
	}
	if n := count(a, "SELECT COUNT(*) FROM main.tasks"); n != 2 {
		t.Errorf("main.tasks = %d, want 2", n)
	}
	if _, err := a.Exec(`UPDATE main.tasks SET title='x' WHERE id='T-b'`); err == nil {
		t.Error("update of other session's row was not stopped")
	}
	if _, err := a.Exec(`DELETE FROM main.tasks WHERE id='T-b'`); err == nil {
		t.Error("delete of other session's row was not stopped")
	}
}

func TestFailedMigrationKeepsVersion(t *testing.T) {
	fsys := fstest.MapFS{}
	ms, _ := Migrations(schemafs.FS)
	for _, m := range ms {
		fsys[m.Name] = &fstest.MapFile{Data: []byte(m.SQL)}
	}
	fsys["999_broken.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE broken (;\n")}
	p := filepath.Join(t.TempDir(), "tasks.db")
	open(t, p, "s", nil).Close()
	if _, err := Open(p, "s", &Options{Schema: fsys}); err == nil || !strings.Contains(err.Error(), "999_broken.sql") {
		t.Fatalf("err = %v, want migration failure", err)
	}
	db := open(t, p, "s", nil)
	if v, _ := db.UserVersion(); v != Latest() {
		t.Errorf("user_version = %d, want %d", v, Latest())
	}
}

func TestAlreadyMigratedIsNotRunTwice(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "tasks.db"), "s", nil)
	if _, err := db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	dup := Migration{Version: 999, Name: "999_dup.sql",
		SQL: "INSERT INTO main.tasks (id, project, title, created_at, updated_at, herdr_session) VALUES ('T-dup','p','t','t','t','s');"}
	if err := db.applyMigration(dup); err == nil {
		t.Fatal("guard did not stop a migration that already ran")
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM main.tasks WHERE id='T-dup'").Scan(&n)
	if n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
	if v, _ := db.UserVersion(); v != 999 {
		t.Errorf("user_version = %d, want 999", v)
	}
}

func TestNoSchemaFilesCreatesNoDB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none", "tasks.db")
	if _, err := Open(p, "s", &Options{Schema: fstest.MapFS{}}); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Errorf("directory was created: %v", err)
	}
}

// bash の tb と Go の store が同じスキーマ(sqlite_master)を作ることを確かめる(契約テスト)。
// TB_PARITY=1 のときだけ流す(CI の ubuntu。bash・sqlite3・jq が要る)。
func TestSameSchemaAsBashTB(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getenv("TB_PARITY") != "1" {
		t.Skip("TB_PARITY=1 のときだけ(bash の tb と突き合わせる)")
	}
	for _, c := range []string{"bash", "sqlite3", "jq"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skipf("%s が無い", c)
		}
	}
	dir := t.TempDir()
	bashDB, goDB := filepath.Join(dir, "bash.db"), filepath.Join(dir, "go.db")
	cmd := exec.Command("bash", "../../taskboard/bin/tb", "list")
	cmd.Env = append(os.Environ(), "TB_DB="+bashDB, "HERDR_BIN=false", "HERDR_SESSION=s1", "HERDR_SOCKET_PATH=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tb list: %v\n%s", err, out)
	}
	open(t, goDB, "s1", nil).Close()
	dump := func(p string) string {
		out, err := exec.Command("sqlite3", p, "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name; PRAGMA user_version;").Output()
		if err != nil {
			t.Fatalf("sqlite3 %s: %v", p, err)
		}
		return string(out)
	}
	if b, g := dump(bashDB), dump(goDB); b != g {
		t.Errorf("schema differs\n--- bash\n%s\n--- go\n%s", b, g)
	}
}

func TestSessionTablesListIsNotEmpty(t *testing.T) {
	ts := SessionTables()
	if len(ts) == 0 || ts[0] == "" {
		t.Fatalf("SessionTables() = %v", ts)
	}
	for _, x := range ts {
		if strings.ContainsAny(x, " #") {
			t.Errorf("bad entry %q", x)
		}
	}
}
