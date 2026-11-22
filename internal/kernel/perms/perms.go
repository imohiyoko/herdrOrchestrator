// Package perms は tb の全コマンドの権限段階(allow / ask-any / ask-human / only-human。ADR 0006。taskboard/lib/perms.sh)。
// 表は taskboard/perms/commands.tbl(埋め込み)。人が変えるのは設定 approvals の cmd: の行だけ。
package perms

import (
	"database/sql"
	"os"
	"strings"

	tblperms "github.com/imohiyoko/herdrorchestrator/taskboard/perms"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// Tiers は段階(緩い順)。
var Tiers = []string{"allow", "ask-any", "ask-human", "only-human"}

// Rank は段階の順位(知らない値は only-human)。
func Rank(t string) int {
	for i, x := range Tiers {
		if x == t {
			return i
		}
	}
	return len(Tiers) - 1
}

// Row は表の 1 行。
type Row struct {
	Path, Op, Default, Floor, Where string
}

// Table は読み込んだ表。
type Table struct {
	Rows   []Row
	byPath map[string]Row
	byOp   map[string]Row
}

// Parse は commands.tbl の本文を読む(壊れた行は飛ばす。bash の perm_load と同じ)。
func Parse(body string) *Table {
	t := &Table{byPath: map[string]Row{}, byOp: map[string]Row{}}
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) != 5 {
			continue
		}
		r := Row{f[0], f[1], f[2], f[3], f[4]}
		t.Rows = append(t.Rows, r)
		t.byPath[r.Path] = r
		t.byOp[r.Op] = r
	}
	return t
}

var std = Parse(tblperms.Commands)

// Std は埋め込みの表。
func Std() *Table { return std }

// PathOf は長いほうの道(3 語 → 2 語 → 1 語)で当たった行。
func (t *Table) PathOf(words []string) (Row, bool) {
	for n := 3; n >= 1; n-- {
		if len(words) < n {
			continue
		}
		if r, ok := t.byPath[strings.Join(words[:n], ".")]; ok {
			return r, true
		}
	}
	return Row{}, false
}

// Op は操作名の行。
func (t *Table) Op(op string) (Row, bool) { r, ok := t.byOp[op]; return r, ok }

// Tier は今効いている段階と根拠(bash の perm_tier)。db が nil なら設定を見ない。
func (t *Table) Tier(db *sql.DB, session, op string) (tier, rule string) {
	r, ok := t.byOp[op]
	if !ok {
		return "only-human", "(表に無い操作: only-human)"
	}
	tier, rule = r.Default, "(既定 "+r.Default+")"
	if r.Floor == "only-human" { // 設定では変わらない(設定を読まない)
		return "only-human", "(下限 only-human)"
	}
	if db != nil {
		body, _, ok, readErr := config.LookupStrict(db, session, "approvals")
		// 設定を読めない(ファイルの読み取りや DB の問い合わせが失敗した)ときは、設定が無いのとは分けて only-human(fail-closed)
		if readErr != "" {
			return "only-human", "(設定 approvals を読めない: " + readErr + ")"
		}
		if ok {
			for _, line := range strings.Split(body, "\n") {
				if i := strings.IndexByte(line, '#'); i >= 0 {
					line = line[:i]
				}
				line = config.Trim(line)
				if !strings.HasPrefix(line, "cmd:") || !strings.Contains(line, "=") {
					continue
				}
				k, v, _ := strings.Cut(line, "=")
				k, v = config.Trim(k), config.Trim(v)
				if v == "ask" {
					v = "ask-any"
				}
				if !valid(v) || !util.Glob(k, op) {
					continue
				}
				tier, rule = v, "approvals: "+k+" = "+v
				break
			}
		}
	}
	if Rank(tier) < Rank(r.Floor) {
		rule += "(下限 " + r.Floor + " より緩いので " + r.Floor + ")"
		tier = r.Floor
	}
	return tier, rule
}

func valid(v string) bool {
	for _, x := range Tiers {
		if x == v {
			return true
		}
	}
	return false
}

// Denied は止めた理由と終了コード(1 = 人だけ・確かめられない、20 = 承認が要る)。
type Denied struct {
	Code int
	Msg  string
}

func (d *Denied) Error() string { return d.Msg }

// Enforce は段階で止めるか決める(bash の perm_enforce)。人の端末(pane なし・pane に agent がいない)なら常に通す。
func Enforce(op, tier, rule, what string) error {
	pane := identityPane()
	if tier == "allow" || pane == "" {
		return nil
	}
	n, err := identity.PaneOwnerName(pane)
	if err != nil {
		return &Denied{1, what + ": herdr agent list に失敗したため、人の端末からの実行か確かめられません"}
	}
	if n == "" {
		return nil
	}
	switch tier {
	case "ask-any", "ask-human":
		return &Denied{20, what + " は承認が要ります(" + op + " = " + tier + "。" + rule + ")。承認待ちの仕組みはまだ無いため実行しません。人が自分の端末で実行してください(agent '" + n + "')"}
	}
	return &Denied{1, what + " は人が自分の端末で実行します(agent '" + n + "' の pane からは実行できません)"}
}

// HumanPane は今の呼び出しが pane の外(人の端末)か。pane があっても agent がいなければ人だが、それは Enforce が herdr に聞いて決める。
func HumanPane() bool { return identityPane() == "" }

// Gate は入口(bin/tb の main と herdrctl の Run)。場所が entry の行だけ確かめる。表に無いコマンドは only-human。
// openDB は、設定 approvals を読むときだけ呼ぶ(人の端末では DB を開かない)。
func Gate(args []string, openDB func() (*sql.DB, string, func(), error)) error {
	if len(args) == 0 {
		return nil
	}
	r, ok := std.PathOf(args)
	if !ok {
		return Enforce("cmd:?:"+args[0], "only-human", "(表に無いコマンド)", "tb "+args[0])
	}
	if r.Where != "entry" || HumanPane() {
		return nil
	}
	return check(r.Op, "tb "+strings.ReplaceAll(r.Path, ".", " "), openDB)
}

// RequireHuman はコマンドの中の人だけの確かめ(bash の require_human)。what は "tb <語...>"。表に無い文面は only-human(従来と同じ)。
func RequireHuman(what string, openDB func() (*sql.DB, string, func(), error)) error {
	if HumanPane() {
		return nil
	}
	r, ok := std.PathOf(strings.Fields(strings.TrimPrefix(what, "tb ")))
	if !ok {
		return Enforce("cmd:?:"+strings.TrimPrefix(what, "tb "), "only-human", "(表に無い文面)", what)
	}
	return check(r.Op, what, openDB)
}

// OpenDB は openDB を渡さない呼び出し(サービスのパッケージの requireHuman)が設定 approvals を読むための DB(herdrctl tb の入口が入れる)。
var OpenDB func() (*sql.DB, string, func(), error)

func check(op, what string, openDB func() (*sql.DB, string, func(), error)) error {
	if openDB == nil {
		openDB = OpenDB
	}
	if r, ok := std.byOp[op]; !ok || r.Floor == "only-human" {
		openDB = nil // 設定を読んでも段階は変わらない(DB を開かない)
	}
	var db *sql.DB
	session := ""
	if openDB != nil {
		d, s, done, err := openDB()
		if err != nil {
			return err
		}
		defer done()
		db, session = d, s
	}
	tier, rule := std.Tier(db, session, op)
	return Enforce(op, tier, rule, what)
}

func identityPane() string { return getenv("HERDR_PANE_ID") }

var getenv = os.Getenv
