package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

func TestLookupOrder(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"), "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("TB_CONFIG_DIR", dir)
	t.Setenv("TB_LIMITS", "")
	// どこにも無い → .example(limits は既定値として読む)
	os.WriteFile(filepath.Join(dir, "limits.conf.example"), []byte("a=1\n\n"), 0o600)
	if b, src, ok := Lookup(db.DB, "s1", "limits"); !ok || b != "a=1" || src != "example:"+filepath.Join(dir, "limits.conf.example") {
		t.Errorf("example = %q %q %v", b, src, ok)
	}
	// 共通の行 > ファイル、このセッションの行 > 共通の行
	if err := DBPut(db.DB, "s1", "limits", "b=2", "set", true, ""); err != nil {
		t.Fatal(err)
	}
	if b, src, _ := Lookup(db.DB, "s1", "limits"); b != "b=2" || src != "db:*" {
		t.Errorf("shared = %q %q", b, src)
	}
	DBPut(db.DB, "s1", "limits", "c=3", "set", false, "")
	if b, src, _ := Lookup(db.DB, "s1", "limits"); b != "c=3" || src != "db:s1" {
		t.Errorf("session = %q %q", b, src)
	}
	// 環境変数のファイルがいちばん先
	f := filepath.Join(dir, "x.conf")
	os.WriteFile(f, []byte("d=4\n"), 0o600)
	t.Setenv("TB_LIMITS", f)
	if b, src, _ := Lookup(db.DB, "s1", "limits"); b != "d=4" || src != "file:"+f {
		t.Errorf("env = %q %q", b, src)
	}
	t.Setenv("TB_LIMITS", "")
	// unset は行を消し、履歴を残す
	if err := Unset(db.DB, "s1", "limits", false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := HasRow(db.DB, "s1", "limits", false); ok {
		t.Error("row still there")
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM main.config_changes WHERE key='limits'").Scan(&n)
	if n != 3 {
		t.Errorf("history rows = %d", n)
	}
	if _, _, ok := Lookup(db.DB, "s1", "no-such-key"); ok {
		t.Error("unknown key found")
	}
}
