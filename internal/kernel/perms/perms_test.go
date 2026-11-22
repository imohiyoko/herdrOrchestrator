package perms_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
	"github.com/imohiyoko/herdrorchestrator/internal/tbcli"

	_ "modernc.org/sqlite"
)

// herdrctl に移したコマンドは、どれも表に 1 語の行がある(コマンドを足したら表に行を足さないと落ちる)。
func TestEveryGoCommandHasARow(t *testing.T) {
	tb := perms.Std()
	for _, c := range tbcli.Commands {
		if _, ok := tb.PathOf([]string{c}); !ok {
			t.Errorf("tb %s: taskboard/perms/commands.tbl に行がありません", c)
		}
	}
}

// Go の requireHuman の文面は、表の inner の行に当たる(文字列の連結は * とみなす)。
func TestEveryGoRequireHumanLabelHasAnInnerRow(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "*", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("internal のソースが見つかりません: %v", err)
	}
	re := regexp.MustCompile(`requireHuman\("tb ([^"]*)"( \+)?`)
	var inner []string
	for _, r := range perms.Std().Rows {
		if r.Where == "inner" {
			inner = append(inner, r.Path)
		}
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "identity.RequireHuman(") {
			t.Errorf("%s: identity.RequireHuman を直接呼ぶと権限段階の表を通らない。perms.RequireHuman(tbcli は requireHuman)を使う", filepath.Base(f))
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			n++
			pat := strings.ReplaceAll(strings.TrimSpace(m[1]), " ", ".")
			if m[2] != "" {
				pat += "*"
			}
			hit := false
			for _, p := range inner {
				if util.Glob(pat, p) {
					hit = true
				}
			}
			if !hit {
				t.Errorf("%s: requireHuman(\"tb %s\") に当たる inner の行がありません", filepath.Base(f), m[1])
			}
		}
	}
	if n == 0 {
		t.Fatal("requireHuman の呼び出しを 1 つも見つけられません(正規表現が古い)")
	}
}

func TestRowsAreConsistent(t *testing.T) {
	seen := map[string]perms.Row{}
	for _, r := range perms.Std().Rows {
		if perms.Rank(r.Default) < perms.Rank(r.Floor) {
			t.Errorf("%s: 既定 %s が下限 %s より緩い", r.Path, r.Default, r.Floor)
		}
		if s, ok := seen[r.Op]; ok && (s.Default != r.Default || s.Floor != r.Floor) {
			t.Errorf("%s: %s と既定・下限が違う", r.Op, s.Path)
		}
		seen[r.Op] = r
	}
}

func TestPathOfAndTier(t *testing.T) {
	tb := perms.Parse("a cmd:x:a allow allow entry\na.b cmd:x:ab only-human ask-human inner\na.b.c cmd:x:abc allow allow entry\n")
	for _, c := range []struct {
		in   []string
		path string
	}{{[]string{"a"}, "a"}, {[]string{"a", "b"}, "a.b"}, {[]string{"a", "b", "c"}, "a.b.c"}, {[]string{"a", "z", "c"}, "a"}, {[]string{"a", "b", "z"}, "a.b"}} {
		r, ok := tb.PathOf(c.in)
		if !ok || r.Path != c.path {
			t.Errorf("PathOf(%v) = %q, want %q", c.in, r.Path, c.path)
		}
	}
	if _, ok := tb.PathOf([]string{"zz"}); ok {
		t.Error("表に無い語が当たった")
	}
	if tier, _ := tb.Tier(nil, "", "cmd:x:ab"); tier != "only-human" {
		t.Errorf("既定 = %s", tier)
	}
	if tier, _ := tb.Tier(nil, "", "cmd:nope:x"); tier != "only-human" {
		t.Errorf("表に無い操作 = %s", tier)
	}
}

func TestEnforceHumanPassesWithoutHerdr(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	if err := perms.Enforce("cmd:x:y", "only-human", "", "tb x"); err != nil {
		t.Fatal(err)
	}
}

// 設定 approvals を読めない(DB の問い合わせの失敗)ときは、設定が無いのとは分けて only-human(fail-closed)。
// 行が無い・表がまだ無い DB は「設定が無い」なので既定のまま。
func TestTierFailsClosedOnUnreadableConfig(t *testing.T) {
	t.Setenv("TB_APPROVALS", "")
	t.Setenv("TB_CONFIG_DIR", t.TempDir())
	tb := perms.Parse("a cmd:x:a allow allow entry\n")
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if tier, rule := tb.Tier(db, "s", "cmd:x:a"); tier != "allow" {
		t.Errorf("表の無い DB = %s %s, want allow", tier, rule)
	}
	if _, err := db.Exec(`CREATE TABLE config (key TEXT, herdr_session TEXT, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	if tier, rule := tb.Tier(db, "s", "cmd:x:a"); tier != "allow" {
		t.Errorf("行の無い DB = %s %s, want allow", tier, rule)
	}
	// config が参照する別の表が無いのは失敗(config そのものが無いときだけ「設定が無い」)
	if _, err := db.Exec(`ALTER TABLE config RENAME TO config_gone; CREATE VIEW config AS SELECT * FROM config_gone; DROP TABLE config_gone`); err != nil {
		t.Fatal(err)
	}
	if tier, rule := tb.Tier(db, "s", "cmd:x:a"); tier != "only-human" {
		t.Errorf("config が参照する表が無い DB = %s %s, want only-human", tier, rule)
	}
	if _, err := db.Exec(`DROP VIEW config; CREATE TABLE "config-gone" (key TEXT, herdr_session TEXT, body TEXT); CREATE VIEW config AS SELECT * FROM "config-gone"; DROP TABLE "config-gone"`); err != nil {
		t.Fatal(err)
	}
	if tier, rule := tb.Tier(db, "s", "cmd:x:a"); tier != "only-human" {
		t.Errorf("config が参照する表 config-gone が無い DB = %s %s, want only-human", tier, rule)
	}
	db.Close()
	if tier, rule := tb.Tier(db, "s", "cmd:x:a"); tier != "only-human" || !strings.Contains(rule, "読めない") {
		t.Errorf("問い合わせに失敗した DB = %s %s, want only-human", tier, rule)
	}
}

// 設定の値は表の下限より緩められない(下限に丸め、根拠に書く)。下限 only-human の操作は設定を読まない。
func TestTierClampsToFloor(t *testing.T) {
	f := filepath.Join(t.TempDir(), "approvals")
	if err := os.WriteFile(f, []byte("cmd:x:* = allow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TB_APPROVALS", f)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tb := perms.Parse("a cmd:x:a only-human allow inner\nb cmd:x:b only-human ask-human inner\nc cmd:x:c only-human only-human inner\n")
	for op, want := range map[string]string{"cmd:x:a": "allow", "cmd:x:b": "ask-human", "cmd:x:c": "only-human"} {
		if tier, rule := tb.Tier(db, "s", op); tier != want {
			t.Errorf("%s = %s %s, want %s", op, tier, rule, want)
		}
	}
	if _, rule := tb.Tier(db, "s", "cmd:x:b"); !strings.Contains(rule, "下限 ask-human より緩いので ask-human") {
		t.Errorf("丸めの根拠が無い: %s", rule)
	}
}
