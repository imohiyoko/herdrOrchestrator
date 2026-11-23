// Package perms は tb の認可(主体 × 操作 × 対象 → allow / ask / deny。ADR 0006 / ADR 0007。taskboard/lib/perms.sh)。
// 既定は taskboard/perms/commands.tbl(コマンド)と ops.tbl(コマンドの中の操作)。どちらも埋め込み。人が変えるのは設定 policy の行だけ(<主体> <操作> <対象> = <効果>)。
// 当たる行のうち、いちばん厳しい効果を採る。人は常に通る。
package perms

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	tblperms "github.com/imohiyoko/herdrorchestrator/taskboard/perms"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// Tiers は効果(緩い順)。ask は承認待ち。承認できるのは人と、policy の approve:<操作> を許された agent。
var Tiers = []string{"allow", "ask", "deny"}

// legacyAsk は前の効果(承認できる人を効果で分けていた)。読むときは ask と同じ。書く(tb config set など)ときは断る。
var legacyAsk = map[string]bool{"ask-any": true, "ask-human": true}

// Rank は効果の順位(知らない値は deny)。
func Rank(t string) int {
	if legacyAsk[t] {
		t = "ask"
	}
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

// RelMatch は主体と対象の関係(~child / ~report など)の照合: 0 当たる / 1 当たらない / 2 確かめられない。
// 台帳と組織図を読むので機能の層が入れる(herdrctl tb の入口。bash は PERM_REL_MATCH)。nil なら関係は確かめられない。
var RelMatch func(db *sql.DB, session, a, b, rel string) int

// Matcher は行の主体・対象の照合に使う関数(nil なら既定の NameMatch / RelMatch)。
// 1 回の判定の間、役割を固定したいとき(approval.Judge)は自分の関数を渡す。
type Matcher struct {
	Name func(name, pat string) int
	Rel  func(a, b, rel string) int
}

func (m Matcher) name(db *sql.DB, session, name, pat string) int {
	if pat == "*" {
		return 0
	}
	if m.Name != nil {
		return m.Name(name, pat)
	}
	return nameMatch(db, session, name, pat)
}

// targetMatch は bash の perm_target_match。
func (m Matcher) targetMatch(db *sql.DB, session, who, target, t string) int {
	if t == "*" {
		return 0
	}
	if target == "" || target == anySubject {
		return 1 // 対象の無い操作と主体 * の行(policy:<操作>)に当たるのは * の行だけ(?* などの glob にも当てない)
	}
	switch {
	case t == "self":
		if who != "" && who == target {
			return 0
		}
		return 1
	case strings.HasPrefix(t, "~"):
		if who == "" {
			return 1
		}
		if m.Rel != nil {
			return m.Rel(who, target, t)
		}
		if RelMatch != nil {
			return RelMatch(db, session, who, target, t)
		}
		return 2
	}
	if t == target { // 対象そのものが名前のパターン(policy:<操作> の対象は行の主体。* や @役割 も書いたとおりに当てる)
		return 0
	}
	return m.name(db, session, target, t)
}

// Judgement は設定 policy の行を当てた結果(bash の PJ_*)。
type Judgement struct {
	Best  int    // 確かめられた行のうち、いちばん厳しい効果の順位(無ければ -1)
	BRule string // その行
	Unk   int    // 役割・関係を確かめられない行のいちばん厳しい順位(締める向きにだけ使う。無ければ -1)
	URule string
	Human bool // 呼び出し元が人(行は見ない)
}

var reSystemSubject = regexp.MustCompile(`^system:[a-z*][a-z0-9*-]*$`)

// Caller は Judge の主体に「今の呼び出し元」を渡す印(行の操作名が当たったときに初めて whoFn で引く)。
const Caller = "@caller"

// Judge は bash の perm_judge。主体 who は agent 名 / "human" / ""(主体の無い判定。主体 * の行だけ)/ Caller。
// 対象 target は名前 / ""(対象の無い操作。対象 * の行だけ)/ "*"(行の対象を見ない)。
// readErr は設定を読めなかったとき(呼ぶ側が deny にする)。
func Judge(db *sql.DB, session, who string, whoFn func() (string, error), op, target string, m Matcher) (j Judgement, readErr string, err error) {
	j = Judgement{Best: -1, Unk: -1}
	if who == "human" && identity.System != "" { // 人の端末の外から scheduler などが呼んだ
		who = identity.System
	}
	if who == "human" {
		j.Human = true
		return j, "", nil
	}
	sys := func() (Judgement, string, error) { // system: の行が当たらなければ人と同じ
		if j.Best < 0 && (strings.HasPrefix(who, "system:") || (who == Caller && identity.System != "")) {
			j.Human = true
		}
		return j, "", nil
	}
	if db == nil {
		return sys()
	}
	body, _, found, re := config.LookupStrict(db, session, "policy")
	if re != "" {
		return j, re, nil
	}
	if !found {
		return sys()
	}
	for _, line := range strings.Split(body, "\n") {
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
		if !util.Glob(o, op) || s == "human" {
			continue
		}
		if who == Caller {
			if who, err = whoFn(); err != nil {
				return j, "", err
			}
			if who == "human" && identity.System != "" {
				who = identity.System
			}
			if who == "human" {
				j.Human = true
				return j, "", nil
			}
		}
		if strings.HasPrefix(who, "system:") || strings.HasPrefix(s, "system:") { // system の主体には system: の行だけ。関係(~ / self)は持たない
			if !strings.HasPrefix(who, "system:") || !strings.HasPrefix(s, "system:") || !util.Glob(s, who) ||
				strings.HasPrefix(tg, "~") || tg == "self" {
				continue
			}
			if target != "*" && m.targetMatch(db, session, "", target, tg) != 0 {
				continue
			}
			if rk := Rank(v); rk > j.Best {
				j.Best, j.BRule = rk, "policy: "+s+" "+o+" "+tg+" = "+v
			}
			continue
		}
		mm := 0
		if who == "" {
			if s != "*" {
				continue
			}
		} else if mm = m.name(db, session, who, s); mm == 1 {
			continue
		}
		if target != "*" {
			switch m.targetMatch(db, session, who, target, tg) {
			case 1:
				continue
			case 2:
				mm = 2
			}
		}
		rk := Rank(v)
		if mm == 0 && rk > j.Best {
			j.Best, j.BRule = rk, "policy: "+s+" "+o+" "+tg+" = "+v
		} else if mm == 2 && rk > j.Unk {
			j.Unk, j.URule = rk, "policy: "+s+" "+o+" "+tg+" = "+v+"(役割・関係を確かめられない)"
		}
	}
	return sys()
}

// Resolve は既定(tier, rule)に判定の結果を重ねる(bash の perm_resolve)。floor が空なら下限なし。
func (j Judgement) Resolve(tier, rule, floor string) (string, string) {
	if j.Best >= 0 {
		tier, rule = Tiers[j.Best], j.BRule
	}
	if j.Unk > Rank(tier) {
		tier, rule = Tiers[j.Unk], j.URule
	}
	if legacyAsk[floor] {
		floor = "ask"
	}
	if floor != "" && Rank(tier) < Rank(floor) {
		rule += "(下限 " + floor + " より緩いので " + floor + ")"
		tier = floor
	}
	return tier, rule
}

// Granted は行で明示した許可か(bash の grant_of: 当たる行のいちばん厳しい効果が allow で、確かめられない行が締めていない)。
func (j Judgement) Granted() bool { return j.Best == 0 && j.Unk <= 0 }

// Tier は今の呼び出し元に効く効果と根拠(bash の perm_tier。コマンドの操作)。db が nil なら設定を見ない。
// who は呼び出し元(人なら "human")を返す。policy の行が操作に当たったときだけ呼ぶ(herdr に聞かずに済むように)。
// human が返ったら、判定はしない(Enforce が通す)。対象は今は無い(対象 * の行だけが当たる)。
func (t *Table) Tier(db *sql.DB, session, op, target string, who func() (string, error)) (tier, rule string, err error) {
	r, ok := t.byOp[op]
	if !ok {
		return "deny", "(表に無い操作: deny)", nil
	}
	tier, rule = r.Default, "既定 "+r.Default
	j, readErr, err := Judge(db, session, Caller, who, op, target, Matcher{})
	if err != nil {
		return "", "", err
	}
	// 設定を読めない(ファイルの読み取りや DB の問い合わせが失敗した)ときは、設定が無いのとは分けて deny(fail-closed)
	if readErr != "" {
		return "deny", "(設定 policy を読めない: " + readErr + ")", nil
	}
	if j.Human {
		if identity.System != "" {
			return "allow", "(" + identity.System + " に当たる system: の行が無いので、人と同じく通る)", nil
		}
		return tier, rule, nil
	}
	tier, rule = j.Resolve(tier, rule, r.Floor)
	return tier, rule, nil
}

// opRow は ops.tbl の 1 行(コマンドの中の操作の既定。上から最初に当たった行)。
type opRow struct{ pat, def string }

func parseOps(body string) []opRow {
	var out []opRow
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		out = append(out, opRow{f[0], f[1]})
	}
	return out
}

var stdOps = parseOps(tblperms.Ops)

// OpDefault はコマンドの中の操作の既定(bash の perm_op_default。当たらなければ deny)。
func OpDefault(op string) string {
	for _, r := range stdOps {
		if util.Glob(r.pat, op) {
			return r.def
		}
	}
	return "deny"
}

// OpsAllowDefaults は既定が allow の操作名のパターン(tb policy import の変換が使う)。
func OpsAllowDefaults() []string {
	var out []string
	for _, r := range stdOps {
		if r.def == "allow" {
			out = append(out, r.pat)
		}
	}
	return out
}

// SplitAct は古い形の操作名(send:task:w1 など)を操作名と対象に分ける(bash の perm_split_act。対象が無ければ空)。
func SplitAct(a string) (op, target string) {
	for _, p := range []string{"role:", "alert:", "proxy:", "setup:"} {
		if strings.HasPrefix(a, p) {
			return a, ""
		}
	}
	if rest, ok := strings.CutPrefix(a, "agent:slash:"); ok {
		if c, t, ok := strings.Cut(rest, ":"); ok {
			return "agent:slash:" + c, t
		}
	}
	first, rest, ok := strings.Cut(a, ":")
	if !ok {
		return a, ""
	}
	second, t, ok := strings.Cut(rest, ":")
	if !ok {
		return a, ""
	}
	return first + ":" + second, t
}

// Authorize はコマンドの中の操作の効果と根拠(bash の perm_authorize)。主体の書き方は Judge と同じ(人なら allow)。
func Authorize(db *sql.DB, session, who, op, target string, m Matcher) (tier, rule string) {
	if who == "human" && identity.System == "" {
		return "allow", "(人は常に通る)"
	}
	def := OpDefault(op)
	j, readErr, _ := Judge(db, session, who, nil, op, target, m)
	if readErr != "" {
		return "deny", "(設定 policy を読めない: " + readErr + ")"
	}
	if j.Human { // system: の行が当たらなかった system も
		return "allow", "(人は常に通る)"
	}
	return j.Resolve(def, "既定 "+def, "")
}

// Relations は policy の対象に書ける関係。
var Relations = map[string]bool{"~child": true, "~descendant": true, "~parent": true, "~sibling": true,
	"~report": true, "~subordinate": true, "~manager": true, "~superior": true, "~peer": true}

func valid(v string) bool {
	if legacyAsk[v] {
		return true
	}
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
func Enforce(op, tier, rule, what string) error { return EnforceTarget(op, "*", tier, rule, what) }

// Queue は ask で止めたコマンドを承認待ちに積む(herdrctl tb の入口が入れる)。積んだ承認待ちの ID(Q-<n>)を返す。nil なら積まない。
var Queue func(op, target, what string) (string, error)

// Approved は承認済みの依頼を実行しているとき(tb approval approve が立てる。ask は通す。deny は通さない)。
var Approved bool

// EnforceTarget は Enforce に操作の対象を足したもの(承認待ちのラベルに使う)。
func EnforceTarget(op, target, tier, rule, what string) error {
	if tier == "ask" && Approved {
		return nil
	}
	pane := identityPane()
	if tier == "allow" || (pane == "" && identity.System == "") {
		return nil
	}
	n := identity.System // scheduler など(system: の行で締められた)
	if pane != "" {
		var err error
		if n, err = identity.PaneOwnerName(pane); err != nil {
			return &Denied{1, what + ": herdr agent list に失敗したため、人の端末からの実行か確かめられません"}
		}
	}
	if n == "" {
		return nil
	}
	switch tier {
	case "ask":
		msg := what + " は承認が要ります(" + op + " = " + tier + "。" + rule + ")"
		if Queue != nil {
			id, err := Queue(op, target, what)
			if err != nil {
				return &Denied{20, msg + "。承認待ちに積めませんでした(" + err.Error() + ")。人が自分の端末で実行してください(agent '" + n + "')"}
			}
			return &Denied{20, msg + "。承認待ち " + id + " に積みました(承認されると承認した側が実行します。tb approval list)(agent '" + n + "')"}
		}
		return &Denied{20, msg + "。承認待ちに積めないため実行しません。人が自分の端末で実行してください(agent '" + n + "')"}
	}
	if strings.HasPrefix(rule, "policy:") {
		return &Denied{1, what + " は実行できません(" + op + " = deny。" + rule + ")(agent '" + n + "')"}
	}
	return &Denied{1, what + " は人が自分の端末で実行します(agent '" + n + "' の pane からは実行できません)"}
}

// HumanPane は今の呼び出しが pane の外(人の端末)か。pane があっても agent がいなければ人だが、それは Enforce が herdr に聞いて決める。
// system:<名前>(scheduler など)は pane の外でも policy の system: の行を見るので、ここでは人にしない。
func HumanPane() bool { return identityPane() == "" && identity.System == "" }

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
	tier, rule, err := std.Tier(db, session, op, "", callerFn(what))
	if err != nil {
		return err
	}
	return Enforce(op, tier, rule, what)
}

// callerFn は今の呼び出し元(pane の agent 名。人と pane の外は "human"。Judge が system:<名前> に置き換える)。
func callerFn(what string) func() (string, error) {
	return func() (string, error) {
		if identityPane() == "" {
			return "human", nil
		}
		n, err := identity.PaneOwnerName(identityPane())
		if err != nil {
			return "", &Denied{1, what + ": herdr agent list に失敗したため、呼び出し元を確かめられません"}
		}
		if n == "" {
			return "human", nil
		}
		return n, nil
	}
}

// PolicyChange は policy の本文の差分の 1 行(足した行か消した行。効果を変えた行は両方に出る)。
type PolicyChange struct{ Subject, Op, Line string }

func policyRowsOf(body string) (keys []string, rows map[string]PolicyChange) {
	rows = map[string]PolicyChange{}
	for _, line := range config.Lines(body) {
		l := config.Trim(config.StripComment(line))
		lhs, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		f := strings.Fields(lhs)
		if len(f) != 3 {
			continue
		}
		k := strings.Join(f, " ") + " = " + config.Trim(v)
		keys = append(keys, k)
		rows[k] = PolicyChange{f[0], f[1], k}
	}
	return keys, rows
}

// PolicyDiff は old から new への行の差分(足した行・消した行。並べ替えとコメントは差分にしない)。
func PolicyDiff(old, new string) []PolicyChange {
	ok, orows := policyRowsOf(old)
	nk, nrows := policyRowsOf(new)
	count := map[string]int{}
	for _, k := range ok {
		count[k]++
	}
	for _, k := range nk {
		count[k]--
	}
	var out []PolicyChange
	seen := map[string]bool{}
	for _, k := range append(ok, nk...) {
		if count[k] == 0 || seen[k] {
			continue
		}
		seen[k] = true
		if r, has := orows[k]; has {
			out = append(out, r)
		} else {
			out = append(out, nrows[k])
		}
	}
	return out
}

// RequirePolicyWrite は policy を old から new に変える前の確かめ(人は常に通る)。
// 変わる行ごとに、呼び出し元に操作 policy:<行の操作> 対象 <行の主体> が許されているかを見る(既定は deny)。
// 権限の種類を並べた表は持たない: 行の操作名をそのまま使うので、新しく足した操作も同じ書き方で絞れる。
// 例: worker1 policy:send:* worker2 = allow(worker1 は worker2 の send:* の行だけを変えてよい)。
func RequirePolicyWrite(old, new, what string, openDB func() (*sql.DB, string, func(), error)) error {
	if HumanPane() {
		return nil
	}
	ch := PolicyDiff(old, new)
	if len(ch) == 0 {
		return nil
	}
	if openDB == nil {
		openDB = OpenDB
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
	who, err := callerFn(what)()
	if err != nil {
		return err
	}
	for _, c := range ch {
		op, target := "policy:"+c.Op, c.Subject
		if target == "*" {
			target = anySubject // Judge は対象 * を「行の対象を見ない」に使うので、主体 * の行は別の印で渡す(当たるのは対象 * の行だけ)
		}
		tier, rule := Authorize(db, session, who, op, target, Matcher{})
		if err := EnforceTarget(op, c.Subject, tier, rule, what+"(変わる行: "+c.Line+")"); err != nil {
			var d *Denied
			if errors.As(err, &d) && d.Code == 1 && tier == "deny" && !strings.HasPrefix(rule, "policy:") {
				d.Msg = what + " は実行できません(変わる行: " + c.Line + ")。" + op + " を対象 " + c.Subject +
					" に許す行がありません(既定 deny。人が policy に書く: <agent> " + op + " " + c.Subject + " = allow)(agent '" + who + "')"
			}
			return err
		}
	}
	return nil
}

// anySubject は主体 * の行を変えるときの対象(名前にもパターンにもならない文字列)。
const anySubject = "\x00*"

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
		if legacyAsk[v] {
			fmt.Fprintf(&bad, "%d 行目: %s は ask にまとめました(承認できる agent は approve:<操作> の行で決める): %s\n", i+1, v, l)
			continue
		}
		if !valid(v) {
			fmt.Fprintf(&bad, "%d 行目: 効果は allow / ask / deny のどれかです: %s\n", i+1, v)
			continue
		}
		if f[0] == "human" {
			fmt.Fprintf(&bad, "%d 行目: 主体 human の行は効きません(人は常に通る)\n", i+1)
		} else if strings.HasPrefix(f[0], "system:") && !reSystemSubject.MatchString(f[0]) {
			fmt.Fprintf(&bad, "%d 行目: system の主体は system:<名前>(英小文字・数字・-、glob の *)です: %s\n", i+1, f[0])
		}
		if strings.HasPrefix(f[1], "cmd:") && f[2] != "*" {
			fmt.Fprintf(&bad, "%d 行目: コマンドの操作(%s)の対象は * と書きます: %s\n", i+1, f[1], f[2])
		}
		if strings.HasPrefix(f[2], "~") && !Relations[f[2]] {
			fmt.Fprintf(&bad, "%d 行目: 知らない関係です(~child ~descendant ~parent ~sibling ~report ~subordinate ~manager ~superior ~peer): %s\n", i+1, f[2])
		}
	}
	return bad.String()
}

// InnerOps は古い approvals が見ていたコマンドの中の操作の族(bash の POLICY_INNER_OPS と同じ)。
var InnerOps = []string{"send:task", "send:info", "send:answer", "agent:new", "agent:stop", "agent:revive", "agent:spawn",
	"agent:compact", "agent:model", "agent:read", "agent:slash:*", "role:change", "alert:analyze", "proxy:read-only",
	"proxy:write", "proxy:external", "setup:*", "recipe:*"}

func opReserved(op string) bool {
	pre := op
	if i := strings.IndexAny(op, "*?\\["); i >= 0 {
		pre = op[:i]
	} else {
		return strings.HasPrefix(op, "cmd:") || strings.HasPrefix(op, "approve:") || strings.HasPrefix(op, "policy:") || op == "agent:flag"
	}
	for _, r := range []string{"cmd:", "approve:", "policy:", "agent:flag"} {
		if strings.HasPrefix(r, pre) || strings.HasPrefix(pre, r) {
			return true
		}
	}
	return false
}

// OpsOf は古い形の操作名の glob を、policy に書く操作名にする。cmd:・approve:・agent:flag に当たりうる glob は
// 中の操作の族に絞る(そのまま書くと、前は当たらなかったコマンドや承認の委任まで許してしまう)。
func OpsOf(op string) []string {
	if !opReserved(op) {
		return []string{op}
	}
	var out []string
	for _, f := range InnerOps {
		if util.Glob(op, f) {
			out = append(out, f)
		}
	}
	return out
}

// SubjectOf は古い approvals の行を policy に書くときの主体。role:change は人事役(TB_ROLE_ADMIN、既定 hr)にだけ効いていた。
func SubjectOf(op string) string {
	if op == "role:change" {
		if a := os.Getenv("TB_ROLE_ADMIN"); a != "" {
			return "@" + a
		}
		return "@hr"
	}
	return "*"
}
