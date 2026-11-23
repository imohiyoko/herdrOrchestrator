// Package perms は tb の認可(主体 × 操作 × 対象 → allow / ask-any / ask-human / deny。ADR 0006 / ADR 0007。taskboard/lib/perms.sh)。
// 既定は taskboard/perms/commands.tbl(埋め込み)。人が変えるのは設定 policy の行だけ(<主体> <操作> <対象> = <効果>)。
// 当たる行のうち、いちばん厳しい効果を採る。人は常に通る。
package perms

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	tblperms "github.com/imohiyoko/herdrorchestrator/taskboard/perms"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// Tiers は効果(緩い順)。
var Tiers = []string{"allow", "ask-any", "ask-human", "deny"}

// Rank は効果の順位(知らない値は deny)。
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

// NameMatch は名前とパターン(名前の glob か @<役割>)の照合: 0 一致 / 1 不一致 / 2 確かめられない。
// 役割は機能の層が入れる(herdrctl tb の入口。bash は PERM_NAME_MATCH)。nil なら @<役割> は確かめられない。
var NameMatch func(db *sql.DB, session, name, pat string) int

func nameMatch(db *sql.DB, session, name, pat string) int {
	if pat == "*" {
		return 0
	}
	if NameMatch != nil {
		return NameMatch(db, session, name, pat)
	}
	if strings.HasPrefix(pat, "@") {
		return 2
	}
	if util.Glob(pat, name) {
		return 0
	}
	return 1
}

// Tier は今の呼び出し元に効く効果と根拠(bash の perm_tier)。db が nil なら設定を見ない。
// who は呼び出し元(人なら "human")を返す。policy の行が操作に当たったときだけ呼ぶ(herdr に聞かずに済むように)。
// human が返ったら、判定はしない(Enforce が通す)。
func (t *Table) Tier(db *sql.DB, session, op, target string, who func() (string, error)) (tier, rule string, err error) {
	r, ok := t.byOp[op]
	if !ok {
		return "deny", "(表に無い操作: deny)", nil
	}
	tier, rule = r.Default, "(既定 "+r.Default+")"
	if target == "" {
		target = "*"
	}
	best, unk := -1, -1
	var brule, urule string
	if db != nil {
		body, _, found, readErr := config.LookupStrict(db, session, "policy")
		// 設定を読めない(ファイルの読み取りや DB の問い合わせが失敗した)ときは、設定が無いのとは分けて deny(fail-closed)
		if readErr != "" {
			return "deny", "(設定 policy を読めない: " + readErr + ")", nil
		}
		name := ""
		for _, line := range strings.Split(body, "\n") {
			if !found {
				break
			}
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			line = config.Trim(line)
			lhs, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			v = config.Trim(v)
			f := strings.Fields(lhs)
			if len(f) != 3 || !valid(v) {
				continue
			}
			s, o, tg := f[0], f[1], f[2]
			if !util.Glob(o, op) || s == "human" || strings.HasPrefix(s, "system:") {
				continue
			}
			if name == "" {
				if name, err = who(); err != nil {
					return "", "", err
				}
			}
			if name == "human" {
				return tier, rule, nil
			}
			m := nameMatch(db, session, name, s)
			if m == 1 {
				continue
			}
			if target != "*" {
				switch nameMatch(db, session, target, tg) {
				case 1:
					continue
				case 2:
					m = 2
				}
			}
			rk := Rank(v)
			if m == 0 && rk > best {
				best, brule = rk, "policy: "+s+" "+o+" "+tg+" = "+v
			} else if m == 2 && rk > unk {
				unk, urule = rk, "policy: "+s+" "+o+" "+tg+" = "+v+"(役割を確かめられない)"
			}
		}
	}
	if best >= 0 {
		tier, rule = Tiers[best], brule
	}
	if unk > Rank(tier) {
		tier, rule = Tiers[unk], urule
	}
	if Rank(tier) < Rank(r.Floor) {
		rule += "(下限 " + r.Floor + " より緩いので " + r.Floor + ")"
		tier = r.Floor
	}
	return tier, rule, nil
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

// Enforce は効果で止めるか決める(bash の perm_enforce)。人の端末(pane なし・pane に agent がいない)なら常に通す。
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
	if strings.HasPrefix(rule, "policy:") {
		return &Denied{1, what + " は実行できません(" + op + " = deny。" + rule + ")(agent '" + n + "')"}
	}
	return &Denied{1, what + " は人が自分の端末で実行します(agent '" + n + "' の pane からは実行できません)"}
}

// HumanPane は今の呼び出しが pane の外(人の端末)か。pane があっても agent がいなければ人だが、それは Enforce が herdr に聞いて決める。
func HumanPane() bool { return identityPane() == "" }

// Gate は入口(bin/tb の main と herdrctl の Run)。場所が entry の行だけ確かめる。表に無いコマンドは deny。
// openDB は、設定 policy を読むときだけ呼ぶ(人の端末では DB を開かない)。
func Gate(args []string, openDB func() (*sql.DB, string, func(), error)) error {
	if len(args) == 0 {
		return nil
	}
	if HumanPane() {
		return nil
	}
	r, ok := std.PathOf(args)
	if !ok {
		return Enforce("cmd:?:"+args[0], "deny", "(表に無いコマンド)", "tb "+args[0])
	}
	if r.Where != "entry" || HumanPane() {
		return nil
	}
	return check(r.Op, "tb "+strings.ReplaceAll(r.Path, ".", " "), openDB)
}

// RequireHuman はコマンドの中の確かめ(bash の require_human。既定は人だけ)。what は "tb <語...>"。表に無い文面は deny(従来と同じ)。
func RequireHuman(what string, openDB func() (*sql.DB, string, func(), error)) error {
	if HumanPane() {
		return nil
	}
	r, ok := std.PathOf(strings.Fields(strings.TrimPrefix(what, "tb ")))
	if !ok {
		return Enforce("cmd:?:"+strings.TrimPrefix(what, "tb "), "deny", "(表に無い文面)", what)
	}
	return check(r.Op, what, openDB)
}

// OpenDB は openDB を渡さない呼び出し(サービスのパッケージの requireHuman)が設定 policy を読むための DB(herdrctl tb の入口が入れる)。
var OpenDB func() (*sql.DB, string, func(), error)

func check(op, what string, openDB func() (*sql.DB, string, func(), error)) error {
	if openDB == nil {
		openDB = OpenDB
	}
	if _, ok := std.byOp[op]; !ok {
		openDB = nil // 表に無い操作は設定を読んでも deny(DB を開かない)
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
	who := func() (string, error) {
		n, err := identity.PaneOwnerName(identityPane())
		if err != nil {
			return "", &Denied{1, what + ": herdr agent list に失敗したため、呼び出し元を確かめられません"}
		}
		if n == "" {
			return "human", nil
		}
		return n, nil
	}
	tier, rule, err := std.Tier(db, session, op, "*", who)
	if err != nil {
		return err
	}
	return Enforce(op, tier, rule, what)
}

func identityPane() string { return getenv("HERDR_PANE_ID") }

var getenv = os.Getenv

// ValidatePolicy は config_validate_policy(tb config set / edit の前の検査)。
func ValidatePolicy(body string) string {
	var bad strings.Builder
	for i, line := range config.Lines(body) {
		l := config.Trim(config.StripComment(line))
		if l == "" {
			continue
		}
		lhs, v, ok := strings.Cut(l, "=")
		if !ok {
			fmt.Fprintf(&bad, "%d 行目: <主体> <操作> <対象> = <効果> の形ではありません: %s\n", i+1, l)
			continue
		}
		v = config.Trim(v)
		f := strings.Fields(lhs)
		if len(f) != 3 {
			fmt.Fprintf(&bad, "%d 行目: 主体・操作・対象の 3 つを空白で区切って書きます: %s\n", i+1, l)
			continue
		}
		if !valid(v) {
			fmt.Fprintf(&bad, "%d 行目: 効果は allow / ask-any / ask-human / deny のどれかです: %s\n", i+1, v)
			continue
		}
		if f[0] == "human" || strings.HasPrefix(f[0], "system:") {
			fmt.Fprintf(&bad, "%d 行目: 主体 %s の行はまだ効きません(人は常に通る。system は ADR 0007 の段 1 から)\n", i+1, f[0])
		}
	}
	return bad.String()
}
