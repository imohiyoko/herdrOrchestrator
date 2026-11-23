package perms_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

func tier(tb *perms.Table, db *sql.DB, op, who string) (string, string) {
	t, r, _ := tb.Tier(db, "s", op, "", func() (string, error) { return who, nil })
	return t, r
}

func TestPathOfAndTier(t *testing.T) {
	tb := perms.Parse("a cmd:x:a allow allow entry\na.b cmd:x:ab deny ask-human inner\na.b.c cmd:x:abc allow allow entry\n")
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
	if tier, _ := tier(tb, nil, "cmd:x:ab", "w1"); tier != "deny" {
		t.Errorf("既定 = %s", tier)
	}
	if tier, _ := tier(tb, nil, "cmd:nope:x", "w1"); tier != "deny" {
		t.Errorf("表に無い操作 = %s", tier)
	}
}

func TestEnforceHumanPassesWithoutHerdr(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	if err := perms.Enforce("cmd:x:y", "deny", "", "tb x"); err != nil {
		t.Fatal(err)
	}
}

// 設定 policy を読めない(DB の問い合わせの失敗)ときは、設定が無いのとは分けて deny(fail-closed)。
// 行が無い・表がまだ無い DB は「設定が無い」なので既定のまま。
func TestTierFailsClosedOnUnreadableConfig(t *testing.T) {
	t.Setenv("TB_POLICY", "")
	t.Setenv("TB_CONFIG_DIR", t.TempDir())
	tb := perms.Parse("a cmd:x:a allow allow entry\n")
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "allow" {
		t.Errorf("表の無い DB = %s %s, want allow", tier, rule)
	}
	if _, err := db.Exec(`CREATE TABLE config (key TEXT, herdr_session TEXT, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "allow" {
		t.Errorf("行の無い DB = %s %s, want allow", tier, rule)
	}
	// config が参照する別の表が無いのは失敗(config そのものが無いときだけ「設定が無い」)
	if _, err := db.Exec(`ALTER TABLE config RENAME TO config_gone; CREATE VIEW config AS SELECT * FROM config_gone; DROP TABLE config_gone`); err != nil {
		t.Fatal(err)
	}
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "deny" {
		t.Errorf("config が参照する表が無い DB = %s %s, want deny", tier, rule)
	}
	if _, err := db.Exec(`DROP VIEW config; CREATE TABLE "config-gone" (key TEXT, herdr_session TEXT, body TEXT); CREATE VIEW config AS SELECT * FROM "config-gone"; DROP TABLE "config-gone"`); err != nil {
		t.Fatal(err)
	}
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "deny" {
		t.Errorf("config が参照する表 config-gone が無い DB = %s %s, want deny", tier, rule)
	}
	if _, err := db.Exec(`DROP VIEW config; CREATE TABLE "config gone" (key TEXT, herdr_session TEXT, body TEXT); CREATE VIEW config AS SELECT * FROM "config gone"; DROP TABLE "config gone"`); err != nil {
		t.Fatal(err)
	}
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "deny" {
		t.Errorf("config が参照する表 \"config gone\" が無い DB = %s %s, want deny", tier, rule)
	}
	db.Close()
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "deny" || !strings.Contains(rule, "読めない") {
		t.Errorf("問い合わせに失敗した DB = %s %s, want deny", tier, rule)
	}
}

// 設定の値は表の下限より緩められない(下限に丸め、根拠に書く)。
func TestTierClampsToFloor(t *testing.T) {
	f := filepath.Join(t.TempDir(), "approvals")
	if err := os.WriteFile(f, []byte("* cmd:x:* * = allow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TB_POLICY", f)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tb := perms.Parse("a cmd:x:a deny allow inner\nb cmd:x:b deny ask-human inner\nc cmd:x:c deny deny inner\n")
	for op, want := range map[string]string{"cmd:x:a": "allow", "cmd:x:b": "ask-human", "cmd:x:c": "deny"} {
		if tier, rule := tier(tb, db, op, "w1"); tier != want {
			t.Errorf("%s = %s %s, want %s", op, tier, rule, want)
		}
	}
	if _, rule := tier(tb, db, "cmd:x:b", "w1"); !strings.Contains(rule, "下限 ask-human より緩いので ask-human") {
		t.Errorf("丸めの根拠が無い: %s", rule)
	}
}

// 途中のディレクトリの中を見られず、設定ファイルがあるか確かめられないときも deny(本当に無いときは既定のまま)。
func TestTierFailsClosedOnUnsearchableDir(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("root はディレクトリの権限を無視する。Windows は chmod でディレクトリの中を見られなくできない")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tb := perms.Parse("a cmd:x:a allow allow entry\n")
	t.Setenv("TB_POLICY", filepath.Join(root, "none", "approvals"))
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "allow" {
		t.Errorf("無いファイル = %s %s, want allow", tier, rule)
	}
	if err := os.Chmod(filepath.Join(root, "a"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root, "a"), 0o755)
	t.Setenv("TB_POLICY", filepath.Join(root, "a", "b", "approvals"))
	if tier, rule := tier(tb, db, "cmd:x:a", "w1"); tier != "deny" {
		t.Errorf("上のディレクトリを見られない = %s %s, want deny", tier, rule)
	}
}

// policy: 当たる行のうちいちばん厳しい効果(行の順は関係ない)。当たる行が無ければ既定。
// 役割を確かめられない行は締める向きにだけ効く。人は判定しない。操作に当たる行が無ければ呼び出し元を聞かない。
func TestPolicyStrictestWins(t *testing.T) {
	f := filepath.Join(t.TempDir(), "policy")
	body := "w*  cmd:x:a * = allow\n" +
		"w1  cmd:x:a * = ask-human\n" +
		"*   cmd:x:b * = ask-any\n" +
		"w2  cmd:x:b * = allow\n" +
		"@lead cmd:x:c * = allow\n" +
		"@worker cmd:x:d * = deny\n" +
		"w1  cmd:x:e * = bogus\n"
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TB_POLICY", f)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tb := perms.Parse("a cmd:x:a allow allow entry\nb cmd:x:b allow allow entry\nc cmd:x:c deny allow inner\nd cmd:x:d allow allow entry\ne cmd:x:e deny allow inner\nz cmd:x:z allow allow entry\n")
	for _, c := range []struct{ op, who, want string }{
		{"cmd:x:a", "w1", "ask-human"}, // 広い allow と狭い ask-human → 厳しい方
		{"cmd:x:a", "w3", "allow"},
		{"cmd:x:b", "w2", "ask-any"}, // 狭い allow でも、広い ask-any が当たれば厳しい方
		{"cmd:x:c", "w1", "deny"},    // @役割を確かめられない allow は緩める向きに使わない(既定 deny のまま)
		{"cmd:x:d", "w1", "deny"},    // @役割を確かめられない deny は締める向きに効く
		{"cmd:x:e", "w1", "deny"},    // 知らない効果の行は無視(既定)
	} {
		if got, rule := tier(tb, db, c.op, c.who); got != c.want {
			t.Errorf("%s by %s = %s %s, want %s", c.op, c.who, got, rule, c.want)
		}
	}
	asked := false
	if got, _, _ := tb.Tier(db, "s", "cmd:x:z", "", func() (string, error) { asked = true; return "w1", nil }); got != "allow" || asked {
		t.Errorf("当たる行の無い操作 = %s(呼び出し元を聞いた: %v)", got, asked)
	}
	if got, _ := tier(tb, db, "cmd:x:a", "human"); got != "allow" {
		t.Errorf("人 = %s, want allow(既定のまま。Enforce が通す)", got)
	}
}

func TestValidatePolicy(t *testing.T) {
	if msg := perms.ValidatePolicy("# c\n\n@worker cmd:ticket:* * = ask-human\nw1 send:task lead1 = deny\n@lead agent:read ~child = allow\n"); msg != "" {
		t.Errorf("正しい本文が落ちた: %s", msg)
	}
	msg := perms.ValidatePolicy("w1 cmd:x = allow\nw1 cmd:x:y * = maybe\nhuman cmd:x:y * = deny\nno equals here\nw1 cmd:x:y lead1 = deny\nw1 agent:read ~cousin = allow\n")
	for _, want := range []string{"1 行目", "2 行目", "3 行目", "4 行目", "5 行目", "6 行目"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%s が無い: %s", want, msg)
		}
	}
}

func TestOpsOf(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"send:*", "send:*"},
		{"agent:stop", "agent:stop"},
		{"agent:*", "agent:new agent:stop agent:revive agent:spawn agent:compact agent:model agent:read agent:slash:*"},
		{"*", strings.Join(perms.InnerOps, " ")},
		{"*:task", "send:task"},
		{"cmd:ticket:list", ""},
		{"approve:send:task", ""},
		{"agent:flag", ""},
	} {
		if got := strings.Join(perms.OpsOf(c.in), " "); got != c.want {
			t.Errorf("OpsOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPolicyDiff(t *testing.T) {
	old := "a send:task * = allow\n# c\nb agent:stop * = deny"
	new := "b agent:stop   *   = deny   # moved\na send:task * = ask-any\nc x:y * = allow"
	var got []string
	for _, c := range perms.PolicyDiff(old, new) {
		got = append(got, c.Subject+"|"+c.Op+"|"+c.Line)
	}
	want := []string{"a|send:task|a send:task * = allow", "a|send:task|a send:task * = ask-any", "c|x:y|c x:y * = allow"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q", got)
	}
}
