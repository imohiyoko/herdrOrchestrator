package conv

// 会話チームの学習(taskboard/lib/convlearn.sh): 下書きと人が送った文の組、候補の台帳、採用。
// 置き場は DB でも checkout でもなく、この機械だけの TB_CONV_STATE_DIR(既定 $HERDR_STATE_DIR/conv、権限 700)。
// 文字列の一般化と検査(sanitize / scan / ratio)は bash と同じく convtext.py を呼ぶ。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/identity"
)

var (
	reDirection = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	rePair      = regexp.MustCompile(`^pair-[0-9]{14}-[0-9a-f]{4}$`)
	reUint      = regexp.MustCompile(`^[0-9]+$`)
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// StateDir は CONV_STATE。
func StateDir() string { return envOr("TB_CONV_STATE_DIR", env.StateDir()+"/conv") }

// herdrRoot は lib/herdr-env.sh の HERDR_ROOT(taskboard の 1 つ上)。
func herdrRoot() string { return filepath.Clean(filepath.Join(os.Getenv("TB_ROOT"), "..")) }

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// bashSpace は [[:space:]]。
func bashSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r'
}

func trim(s string) string { return strings.TrimFunc(s, bashSpace) }
func blank(s string) bool  { return trim(s) == "" }

func hex4() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// usageLine は usage を $(...) の中で呼んだとき: 文を出してその場は続ける(呼んだ側が die する)。
func (e *Env) usageLine(f string, a ...any) {
	e.errf("tb: %s\n(tb help で使い方)\n", fmt.Sprintf(f, a...))
}

func needVal(a []string, i int) error {
	if i+1 >= len(a) {
		return usage("%s に値が必要です", a[i])
	}
	return nil
}

// py は conv_py: python3 -I $TB_ROOT/lib/convtext.py。出力と終了コード。
func py(stdin string, args ...string) (string, int, error) {
	if _, err := exec.LookPath("python3"); err != nil {
		return "", 0, die("python3 が必要です(会話の学習の文字列処理)")
	}
	c := exec.Command("python3", append([]string{"-I", filepath.Join(os.Getenv("TB_ROOT"), "lib", "convtext.py")}, args...)...)
	c.Stdin = strings.NewReader(stdin)
	c.Stderr = os.Stderr
	out, err := c.Output()
	rc := 0
	if x, ok := err.(*exec.ExitError); ok {
		rc = x.ExitCode()
	} else if err != nil {
		rc = 127
	}
	return string(out), rc, nil
}

// confGet は conv_learn_get / conv_stage_get: 設定の「キー = 値」(# 以降は注釈。最初に出たもの)。
func (e *Env) confGet(key, k string) (string, bool) {
	body, _, ok := config.Lookup(e.DB, e.Sess, key)
	if !ok || body == "" {
		return "", false
	}
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		if trim(line[:eq]) == k {
			return trim(line[eq+1:]), true
		}
	}
	return "", false
}

func (e *Env) threshold() int {
	v, ok := e.confGet("conv-learn", "threshold")
	if !ok || !reUint.MatchString(v) {
		v = "3"
	}
	t, err := strconv.Atoi(v)
	if err != nil || t < 2 {
		t = 2
	}
	return t
}

func (e *Env) deny() string {
	d, _ := e.confGet("conv-learn", "deny")
	return strings.ReplaceAll(d, ", ", ",")
}

func (e *Env) sanitize(s string, month bool) (string, error) {
	a := []string{"sanitize"}
	if month {
		a = append(a, "--keep-month")
	}
	out, _, err := py(s+"\n", append(a, "--deny", e.deny())...)
	return strings.TrimRight(out, "\n"), err
}

// clean は conv_clean(台帳とパターンに残す文)。
func (e *Env) clean(s string) string {
	out, _ := e.sanitize(s, false)
	return out
}

func kindsDir() string { return filepath.Join(Dir(), "kinds") }

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// validKind は valid_conv_kind: conversation/kinds/ があればそこにある種別だけ。
func validKind(k string) error {
	if !ValidName(k) {
		return usage("種別は英小文字で始まる 40 字までの英小文字・数字・-: %s", k)
	}
	if d := kindsDir(); isDir(d) && !isDir(filepath.Join(d, k)) {
		return usage("未登録の種別です(conversation/kinds/%s がありません): %s", k, k)
	}
	return nil
}

func under(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }

// stateInit は conv_state_init: 置き場を用意する。checkout の中には作らない。
func stateInit() error {
	d := StateDir()
	if l, err := os.Lstat(d); err == nil && l.Mode()&os.ModeSymlink != 0 {
		return die("会話の記録の置き場がシンボリックリンクです: %s", d)
	}
	if !strings.HasPrefix(d, "/") {
		wd, _ := os.Getwd()
		d = wd + "/" + d
	}
	hr := herdrRoot()
	root, err := filepath.EvalSymlinks(hr)
	if err == nil {
		root, err = filepath.Abs(root)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "cd: %s: No such file or directory\n", hr)
		return quiet(1)
	}
	outside := "会話の記録は checkout の外に置きます(今の置き場: " + d + ")"
	if under(d, hr) || under(d, root) {
		return die("%s", outside)
	}
	for _, s := range []string{"pairs", "ledger"} {
		if err := os.MkdirAll(filepath.Join(d, s), 0o700); err != nil {
			return die("会話の記録の置き場を作れません: %s", d)
		}
	}
	for _, s := range []string{"", "pairs", "ledger"} {
		_ = os.Chmod(filepath.Join(d, s), 0o700)
	}
	if real, err := filepath.EvalSymlinks(d); err == nil && under(real, root) {
		return die("%s", outside)
	}
	return nil
}

// pairFind は conv_pair_find: 組の meta.json のパス(無ければ "")。ID が不正なら usage の文を出して ""。
func (e *Env) pairFind(id string) string {
	if !rePair.MatchString(id) {
		e.usageLine("組の ID は pair-<日時 14 桁>-<16 進 4 桁>: %s", id)
		return ""
	}
	fs, _ := filepath.Glob(filepath.Join(StateDir(), "pairs", "*", id, "meta.json"))
	sort.Strings(fs)
	for _, f := range fs {
		if isFile(f) {
			return f
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(src); err == nil {
		mode = st.Mode().Perm() & 0o700
	}
	return os.WriteFile(dst, b, mode)
}

// PairAdd は cmd_conv-pair-add。
func (e *Env) PairAdd(a []string) error {
	var kind, df, ff, sit, intent, po, pa string
	posted := false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--kind", "--draft-file", "--final-file", "--situation", "--intent", "--points-offered", "--points-accepted":
			if err := needVal(a, i); err != nil {
				return err
			}
			v := a[i+1]
			switch a[i] {
			case "--kind":
				kind = v
			case "--draft-file":
				df = v
			case "--final-file":
				ff = v
			case "--situation":
				sit = v
			case "--intent":
				intent = v
			case "--points-offered":
				po = v
			case "--points-accepted":
				pa = v
			}
			i++
		case "--posted":
			posted = true
		default:
			return usage("conv-pair-add: 不明な引数 %s", a[i])
		}
	}
	if kind == "" || ff == "" || df == "" {
		return usage("conv-pair-add --kind K --draft-file F --final-file F --situation S --intent I --posted")
	}
	if err := validKind(kind); err != nil {
		return err
	}
	if !posted {
		return usage("conv-pair-add: 人が送ったあとにだけ記録します(--posted を付ける)")
	}
	if blank(sit) || blank(intent) {
		return usage("conv-pair-add: --situation と --intent(一般化した状況と意図)が要ります")
	}
	if !Readable(df) {
		return usage("conv-pair-add: 下書きのファイルを読めません: %s", df)
	}
	if st, err := os.Stat(ff); !Readable(ff) || err != nil || st.Size() == 0 {
		return usage("conv-pair-add: 送った文のファイルが無いか空です: %s", ff)
	}
	if po != "" || pa != "" {
		ok := reUint.MatchString(po) && reUint.MatchString(pa)
		if ok {
			x, _ := strconv.ParseInt(pa, 10, 64)
			y, _ := strconv.ParseInt(po, 10, 64)
			ok = x <= y
		}
		if !ok {
			return usage("conv-pair-add: --points-offered と --points-accepted は 0 以上の整数で、受け入れた数 <= 示された数")
		}
	}
	if err := requireHuman("tb conv-pair-add"); err != nil {
		return err
	}
	if err := stateInit(); err != nil {
		return err
	}
	id := "pair-" + time.Now().UTC().Format("20060102150405") + "-" + hex4()
	d := filepath.Join(StateDir(), "pairs", kind, id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return die("組の置き場を作れません: %s", d)
	}
	if copyFile(df, filepath.Join(d, "draft.txt")) != nil || copyFile(ff, filepath.Join(d, "final.txt")) != nil {
		return die("組を書けません: %s", d)
	}
	out, _, err := py("", "ratio", filepath.Join(d, "draft.txt"), filepath.Join(d, "final.txt"))
	if err != nil {
		return err
	}
	f := strings.Fields(strings.SplitN(out, "\n", 2)[0])
	if len(f) < 3 {
		return die("編集の割合を測れません")
	}
	ratio, dl, fl := f[0], f[1], f[2]
	s1, err := e.sanitize(sit, true)
	if err != nil {
		return err
	}
	s2, _ := e.sanitize(intent, true)
	pts := func(s string) any {
		if s == "" {
			return nil
		}
		n, _ := strconv.ParseInt(s, 10, 64)
		return int(n)
	}
	m := obj{{K: "id", V: id}, {K: "kind", V: kind}, {K: "situation", V: s1}, {K: "intent", V: s2}, {K: "recorded_at", V: now()}, {K: "posted_by", V: "human"},
		{K: "edit_ratio", V: jnum(ratio)}, {K: "draft_chars", V: jnum(dl)}, {K: "final_chars", V: jnum(fl)}, {K: "human_written", V: dl == "0"},
		{K: "points_offered", V: pts(po)}, {K: "points_accepted", V: pts(pa)}}
	if err := os.WriteFile(filepath.Join(d, "meta.json"), []byte(pretty(m)+"\n"), 0o600); err != nil {
		return die("組の記録を書けません: %s", d)
	}
	e.say("%s\t%s", id, ratio)
	return nil
}

// pairsJSON は conv_pairs_json: 組の meta の配列(記録の古い順)。
func pairsJSON(kind string) ([]any, error) {
	if kind == "" {
		kind = "*"
	}
	fs, _ := filepath.Glob(filepath.Join(StateDir(), "pairs", kind, "pair-*", "meta.json"))
	sort.Strings(fs)
	if len(fs) == 0 || !isFile(fs[0]) {
		return []any{}, nil
	}
	var all []byte
	for _, f := range fs {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		all = append(all, b...)
		all = append(all, '\n')
	}
	vs, err := decodeAll(all)
	if err != nil {
		return nil, err
	}
	field := func(v any, k string) any {
		if o, ok := v.(obj); ok {
			return o.Get(k)
		}
		return nil
	}
	sort.SliceStable(vs, func(i, j int) bool {
		if c := jqLess(field(vs[i], "recorded_at"), field(vs[j], "recorded_at")); c != 0 {
			return c < 0
		}
		return jqLess(field(vs[i], "id"), field(vs[j], "id")) < 0
	})
	if vs == nil {
		vs = []any{}
	}
	return vs, nil
}

func jqFail(err error) error {
	fmt.Fprintf(os.Stderr, "jq: error (at <stdin>:0): %s\n", err)
	return quiet(2)
}

// Pairs は cmd_conv-pairs。
func (e *Env) Pairs(a []string) error {
	kind, json := "", false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--kind":
			if err := needVal(a, i); err != nil {
				return err
			}
			kind = a[i+1]
			i++
		case "--json":
			json = true
		default:
			return usage("conv-pairs [--kind K] [--json]")
		}
	}
	if kind != "" {
		if err := validKind(kind); err != nil {
			return err
		}
	}
	vs, err := pairsJSON(kind)
	if err != nil {
		return jqFail(err)
	}
	if json {
		e.say("%s", pretty(vs))
		return nil
	}
	for _, v := range vs {
		o, _ := v.(obj)
		e.say("%s", tsv(o.Get("id"), o.Get("kind"), o.Get("recorded_at"), scalar(o.Get("edit_ratio")), o.Get("situation")))
	}
	return nil
}

// PairShow は cmd_conv-pair-show: 記録と本文(この機械の中だけで読む)。
func (e *Env) PairShow(a []string) error {
	if len(a) != 1 {
		return usage("conv-pair-show <pair-id>")
	}
	m := e.pairFind(a[0])
	if m == "" {
		return die("組が見つかりません: %s", a[0])
	}
	b, _ := os.ReadFile(m)
	vs, err := decodeAll(b)
	if err != nil {
		return jqFail(err)
	}
	for _, v := range vs {
		e.say("%s", pretty(v))
	}
	d := filepath.Dir(m)
	e.say("--- 下書き")
	cat(e.W, filepath.Join(d, "draft.txt"))
	e.say("")
	e.say("--- 送った文")
	cat(e.W, filepath.Join(d, "final.txt"))
	e.say("")
	return nil
}

// ---------- 候補の台帳 ----------
// 台帳の列(タブ区切り): direction state permissive situation intent ending conclusion avoid desc created updated pattern_id
// 証拠の列: direction evidence source situation_key added_at added_by

func ledgerFile(k string) string   { return filepath.Join(StateDir(), "ledger", k+".tsv") }
func evidenceFile(k string) string { return filepath.Join(StateDir(), "ledger", k+".evidence.tsv") }

func fileLines(f string) []string {
	b, err := os.ReadFile(f)
	if err != nil || len(b) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.Split(s, "\n")
}

// ledgerRow は conv_ledger_row: 1 列目が direction の最初の行(無ければ nil)。
func ledgerRow(k, dir string) []string {
	for _, l := range fileLines(ledgerFile(k)) {
		f := strings.Split(l, "\t")
		if f[0] == dir {
			return f
		}
	}
	return nil
}

// ledgerPut は conv_ledger_put: 同じ direction の行を置き換え、無ければ足す。
func ledgerPut(k string, row []string) error {
	f := ledgerFile(k)
	var b bytes.Buffer
	for _, l := range fileLines(f) {
		if strings.Split(l, "\t")[0] != row[0] {
			b.WriteString(l + "\n")
		}
	}
	b.WriteString(strings.Join(row, "\t") + "\n")
	tmp, err := os.CreateTemp(filepath.Dir(f), filepath.Base(f)+".")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b.Bytes())
	tmp.Close()
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), f)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// setCols は awk の $n=v(足りない列は空で埋める)。n は 1 から。
func setCols(row []string, kv ...any) []string {
	r := append([]string(nil), row...)
	for i := 0; i+1 < len(kv); i += 2 {
		n := kv[i].(int)
		for len(r) < n {
			r = append(r, "")
		}
		r[n-1] = kv[i+1].(string)
	}
	return r
}

type counts struct{ counted, he, hw, os, ag int }

// countsOf は conv_counts: 別々の状況の数(agent-generated は数えない。同じ状況のキーは 1 回)と証拠の種類ごとの数。
func countsOf(k, dir string) counts {
	var c counts
	seen := map[string]bool{}
	for _, l := range fileLines(evidenceFile(k)) {
		f := strings.Split(l, "\t")
		if f[0] != dir {
			continue
		}
		g := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		switch g(1) {
		case "human-edit":
			c.he++
		case "human-written":
			c.hw++
		case "other-source":
			c.os++
		case "agent-generated":
			c.ag++
		}
		if g(1) != "agent-generated" && !seen[g(3)] {
			seen[g(3)] = true
			c.counted++
		}
	}
	return c
}

func col(r []string, i int) string {
	if i < len(r) {
		return r[i]
	}
	return ""
}

// refresh は conv_refresh: candidate で件数がしきい値以上なら proposed、proposed で下回れば candidate。
func (e *Env) refresh(k, dir string) error {
	row := ledgerRow(k, dir)
	if row == nil {
		return nil
	}
	st := col(row, 1)
	c, th := countsOf(k, dir).counted, e.threshold()
	switch {
	case st == "candidate" && c >= th:
		return ledgerPut(k, setCols(row, 2, "proposed", 11, now()))
	case st == "proposed" && c < th:
		return ledgerPut(k, setCols(row, 2, "candidate", 11, now()))
	}
	return nil
}

// metaStr は jq -r .<k>(読めない meta なら空。キーが無ければ null)。
func metaStr(m obj, k string) string {
	if m == nil {
		return ""
	}
	return scalar(m.Get(k))
}

func readMeta(f string) obj {
	b, _ := os.ReadFile(f)
	vs, err := decodeAll(b)
	if err != nil || len(vs) == 0 {
		return nil
	}
	o, _ := vs[len(vs)-1].(obj)
	return o
}

// CandidateAdd は cmd_conv-candidate-add。
func (e *Env) CandidateAdd(a []string) error {
	var kind, dir, desc, ev, pair, ref, sit, intent, ending, concl, avoid string
	perm := false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--kind", "--direction", "--desc", "--evidence", "--pair", "--ref", "--situation", "--intent", "--ending", "--conclusion", "--avoid":
			if err := needVal(a, i); err != nil {
				return err
			}
			p := map[string]*string{"--kind": &kind, "--direction": &dir, "--desc": &desc, "--evidence": &ev, "--pair": &pair, "--ref": &ref,
				"--situation": &sit, "--intent": &intent, "--ending": &ending, "--conclusion": &concl, "--avoid": &avoid}
			*p[a[i]] = a[i+1]
			i++
		case "--permissive":
			perm = true
		default:
			return usage("conv-candidate-add: 不明な引数 %s", a[i])
		}
	}
	if kind == "" || dir == "" || ev == "" || blank(desc) {
		return usage("conv-candidate-add --kind K --direction D --desc 説明 --evidence E (--pair ID | --ref 説明)")
	}
	if err := validKind(kind); err != nil {
		return err
	}
	if !reDirection.MatchString(dir) {
		return usage("--direction は変更の向きの短い名前(英小文字・数字・-、例 conclusion-first): %s", dir)
	}
	evs := strings.Join(Evidence, " ")
	if !strings.Contains(" "+evs+" ", " "+ev+" ") {
		return usage("--evidence は %s のどれか: %s", evs, ev)
	}
	if concl != "" && concl != "first" && concl != "last" {
		return usage("--conclusion は first か last: %s", concl)
	}
	var src, skey string
	switch ev {
	case "human-edit", "human-written":
		if pair == "" || ref != "" {
			return usage("conv-candidate-add: --evidence %s には --pair(人が送った組)が要ります", ev)
		}
		mf := e.pairFind(pair)
		if mf == "" {
			return die("組が見つかりません: %s", pair)
		}
		m := readMeta(mf)
		if k := metaStr(m, "kind"); k != kind {
			return die("組 %s は種別 %s のものです", pair, k)
		}
		if metaStr(m, "posted_by") != "human" {
			return die("組 %s は人が送った記録ではありません", pair)
		}
		hw := m.Get("human_written")
		if ev == "human-edit" {
			r, _ := num(m.Get("edit_ratio"))
			gt := r > 0
			if _, isNum := num(m.Get("edit_ratio")); !isNum {
				gt = jqLess(m.Get("edit_ratio"), 0) > 0
			}
			if !(gt && !truthy(hw)) {
				return die("組 %s には人の手直しがありません(下書きのまま、または一から書いたもの。一から書いたものは human-written)", pair)
			}
		} else if hw != true {
			return die("組 %s は下書きを元にしています(human-written は人が一から書いた組だけ)", pair)
		}
		src, skey = pair, metaStr(m, "situation")
	case "other-source":
		if ref == "" || pair != "" {
			return usage("conv-candidate-add: --evidence other-source には --ref(出どころの一般化した説明)が要ります")
		}
		src = "ref:" + e.clean(ref)
		skey = src
	case "agent-generated":
		src = pair
		if src == "" {
			src = "ref:" + e.clean(or(ref, "agent"))
		}
		skey = src
	default:
		// " human-edit human-written " の部分に一致した値(bash の *" $ev "* と同じく通す)。どの枝にも入らない
	}
	if err := stateInit(); err != nil {
		return err
	}
	by := "human"
	if p := os.Getenv("HERDR_PANE_ID"); p != "" {
		n, _ := identity.PaneOwnerName(p)
		by = or(n, "human")
	}
	row := ledgerRow(kind, dir)
	if row == nil {
		p := "0"
		if perm {
			p = "1"
		}
		t := now()
		row = []string{dir, "candidate", p, e.clean(sit), e.clean(intent), e.clean(ending), concl, e.clean(avoid), e.clean(desc), t, t, "-"}
		if err := ledgerPut(kind, row); err != nil {
			return err
		}
	} else if perm {
		if err := ledgerPut(kind, setCols(row, 3, "1", 11, now())); err != nil {
			return err
		}
	}
	ef := evidenceFile(kind)
	dup := false
	for _, l := range fileLines(ef) {
		f := strings.Split(l, "\t")
		if f[0] == dir && col(f, 2) == src {
			dup = true
		}
	}
	if dup {
		e.errf("同じ証拠は記録済みです(%s %s)\n", dir, src)
	} else {
		fh, err := os.OpenFile(ef, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		fmt.Fprintf(fh, "%s\t%s\t%s\t%s\t%s\t%s\n", dir, ev, src, skey, now(), by)
		fh.Close()
	}
	if err := e.refresh(kind, dir); err != nil {
		return err
	}
	e.say("%s\t%s\t%d/%d", dir, col(ledgerRow(kind, dir), 1), countsOf(kind, dir).counted, e.threshold())
	return nil
}

func truthy(v any) bool { return v != nil && v != false }

// ledgerObjs は cmd_conv-ledger の 1 行ずつの object。
func (e *Env) ledgerObjs(kind string) []any {
	th := e.threshold()
	fs, _ := filepath.Glob(filepath.Join(StateDir(), "ledger", "*.tsv"))
	sort.Strings(fs)
	out := []any{}
	for _, f := range fs {
		if !isFile(f) || strings.HasSuffix(f, ".evidence.tsv") {
			continue
		}
		k := strings.TrimSuffix(filepath.Base(f), ".tsv")
		if kind != "" && k != kind {
			continue
		}
		for _, l := range fileLines(f) {
			F := strings.Split(l, "\t")
			c := countsOf(k, F[0])
			var pid any = col(F, 11)
			if pid == "-" {
				pid = nil
			}
			out = append(out, obj{{K: "kind", V: k}, {K: "direction", V: F[0]}, {K: "state", V: col(F, 1)}, {K: "permissive", V: col(F, 2) == "1"},
				{K: "counted", V: c.counted}, {K: "threshold", V: th},
				{K: "evidence", V: obj{{K: "human-edit", V: c.he}, {K: "human-written", V: c.hw}, {K: "other-source", V: c.os}, {K: "agent-generated", V: c.ag}}},
				{K: "situation", V: col(F, 3)}, {K: "intent", V: col(F, 4)}, {K: "ending", V: col(F, 5)}, {K: "conclusion", V: col(F, 6)}, {K: "avoid", V: col(F, 7)},
				{K: "desc", V: col(F, 8)}, {K: "updated_at", V: col(F, 10)}, {K: "pattern_id", V: pid}})
		}
	}
	return out
}

// Ledger は cmd_conv-ledger。
func (e *Env) Ledger(a []string) error {
	kind, json := "", false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--kind":
			if err := needVal(a, i); err != nil {
				return err
			}
			kind = a[i+1]
			i++
		case "--json":
			json = true
		default:
			return usage("conv-ledger [--kind K] [--json]")
		}
	}
	if kind != "" {
		if err := validKind(kind); err != nil {
			return err
		}
	}
	rows := e.ledgerObjs(kind)
	if json {
		e.say("%s", pretty(rows))
		return nil
	}
	for _, r := range rows {
		o := r.(obj)
		ev := o.Get("evidence").(obj)
		p := "-"
		if o.Get("permissive") == true {
			p = "permissive"
		}
		e.say("%s", tsv(o.Get("kind"), o.Get("direction"), o.Get("state"), fmt.Sprintf("%d/%d", o.Get("counted"), o.Get("threshold")),
			ev.Get("human-edit"), ev.Get("human-written"), ev.Get("other-source"), ev.Get("agent-generated"), p, o.Get("desc")))
	}
	return nil
}

// CandidateAdopt は cmd_conv-candidate-adopt(人だけ。proposed だけを採用できる)。
func (e *Env) CandidateAdopt(a []string) error {
	if len(a) < 2 {
		return usage("conv-candidate-adopt <kind> <direction> [--approve-permissive] [--situation S --intent I --ending E --conclusion first|last --avoid A]")
	}
	kind, dir := a[0], a[1]
	a = a[2:]
	ap := false
	set := map[string]string{}
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--approve-permissive":
			ap = true
		case "--situation", "--intent", "--ending", "--conclusion", "--avoid":
			if err := needVal(a, i); err != nil {
				return err
			}
			set[a[i]] = a[i+1]
			i++
		default:
			return usage("conv-candidate-adopt: 不明な引数 %s", a[i])
		}
	}
	if err := validKind(kind); err != nil {
		return err
	}
	if err := requireHuman("tb conv-candidate-adopt"); err != nil {
		return err
	}
	if err := e.refresh(kind, dir); err != nil {
		return err
	}
	row := ledgerRow(kind, dir)
	if row == nil {
		return die("候補がありません: %s %s", kind, dir)
	}
	st, perm := col(row, 1), col(row, 2)
	s0, i0, e0, c0, a0 := col(row, 3), col(row, 4), col(row, 5), col(row, 6), col(row, 7)
	cn, th := countsOf(kind, dir), e.threshold()
	switch st {
	case "proposed":
	case "candidate":
		return die("まだ候補です: 別々の状況での人の手直しが %d 件(しきい値 %d)。1 回の手直しでは採用しません", cn.counted, th)
	case "adopted":
		return die("採用済みです: %s %s", kind, dir)
	default:
		return die("状態が %s の候補は採用できません: %s %s", st, kind, dir)
	}
	if p, _ := strconv.Atoi(perm); p != 0 && !ap {
		return die("許す方向の変更です。採用するなら --approve-permissive を付けて明示してください")
	}
	if v, ok := set["--situation"]; ok {
		s0 = e.clean(v)
	}
	if v, ok := set["--intent"]; ok {
		i0 = e.clean(v)
	}
	if v, ok := set["--ending"]; ok {
		e0 = e.clean(v)
	}
	if v, ok := set["--conclusion"]; ok {
		c0 = v
	}
	if v, ok := set["--avoid"]; ok {
		a0 = e.clean(v)
	}
	if c0 != "first" && c0 != "last" {
		return die("conclusion(first か last)が決まっていません。--conclusion で渡してください")
	}
	if blank(s0) {
		return die("situation が空です。--situation で渡してください")
	}
	if blank(i0) {
		return die("intent が空です。--intent で渡してください")
	}
	all := s0 + " | " + i0 + " | " + e0 + " | " + a0
	if lt := strings.Index(all, "<"); lt >= 0 && strings.Contains(all[lt:], ">") {
		return die("置き換えた印(<name> <n> など)が残っています。一般化した言い方に直して --situation などで渡してください: %s", all)
	}
	bad, rc, err := py(all+"\n", "scan", "--deny", e.deny())
	if err != nil {
		return err
	}
	if rc != 0 {
		return die("パターンに残せないものがあります: %s", strings.ReplaceAll(strings.TrimRight(bad, "\n"), "\n", " ")+" ")
	}
	pf := PatternsFile(kind)
	if !isDir(filepath.Dir(pf)) {
		return die("種別の置き場がありません: %s", filepath.Dir(pf))
	}
	if l, err := os.Lstat(pf); err == nil && l.Mode()&os.ModeSymlink != 0 {
		return die("patterns.conf がシンボリックリンクです: %s", pf)
	}
	ev := "other-source"
	if cn.he > 0 {
		ev = "human-edit"
	} else if cn.hw > 0 {
		ev = "human-written"
	}
	pid := "P-" + firstN(dir, 20)
	if isFile(pf) && ereMatch(`^[[:space:]]*`+pid+`[[:space:]]*\|`, pf) {
		pid = "P-" + firstN(dir, 15) + "-" + hex4()
	}
	if blank(e0) {
		e0 = "-"
	}
	if blank(a0) {
		a0 = "-"
	}
	line := strings.Join([]string{pid, "adopted", ev, s0, i0, e0, c0, a0}, " | ")
	cur, _ := os.ReadFile(pf)
	tmp, err := os.CreateTemp("", "conv-pat.")
	if err != nil {
		return err
	}
	tmp.Write(cur)
	tmp.WriteString(line + "\n")
	tmp.Close()
	pats, errs := ReadPatterns(tmp.Name())
	os.Remove(tmp.Name())
	okp := false
	for _, p := range pats {
		if p[0] == pid {
			okp = true
		}
	}
	if !okp {
		return die("patterns.conf の検査を通りません: %s %s", line, strings.Join(errs, " "))
	}
	fh, err := os.OpenFile(pf, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if len(cur) > 0 && cur[len(cur)-1] != '\n' {
		fh.WriteString("\n")
	}
	fh.WriteString(line + "\n")
	fh.Close()
	if err := ledgerPut(kind, setCols(row, 2, "adopted", 4, s0, 5, i0, 6, e0, 7, c0, 8, a0, 11, now(), 12, pid)); err != nil {
		return err
	}
	e.say("%s adopted → %s", pid, pf)
	return nil
}

func firstN(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// ereMatch は grep -Eq(bash と同じ ERE で、ファイルのどこかの行に一致するか)。
func ereMatch(re, f string) bool {
	return exec.Command("grep", "-Eq", "--", re, f).Run() == nil
}

// CandidateReject は cmd_conv-candidate-reject(人だけ。証拠は残し、状態だけ rejected にする)。
func (e *Env) CandidateReject(a []string) error {
	if len(a) != 2 {
		return usage("conv-candidate-reject <kind> <direction>")
	}
	if err := validKind(a[0]); err != nil {
		return err
	}
	if err := requireHuman("tb conv-candidate-reject"); err != nil {
		return err
	}
	row := ledgerRow(a[0], a[1])
	if row == nil {
		return die("候補がありません: %s %s", a[0], a[1])
	}
	if col(row, 1) == "adopted" {
		return die("採用済みです。外すときは patterns.conf の行を人が消してください")
	}
	if err := ledgerPut(a[0], setCols(row, 2, "rejected", 11, now())); err != nil {
		return err
	}
	e.say("%s rejected", a[1])
	return nil
}

// Scan は cmd_conv-scan: 0=残せないものは無い / 1=ある(種類<TAB>該当)。
func (e *Env) Scan(a []string) error {
	if len(a) != 0 {
		return usage("conv-scan < 文")
	}
	b, _ := io.ReadAll(e.In)
	out, rc, err := py(string(b), "scan", "--deny", e.deny())
	if err != nil {
		return err
	}
	io.WriteString(e.W, out)
	if rc != 0 {
		return quiet(rc)
	}
	return nil
}

// LearnPurge は cmd_conv-learn-purge(人だけ)。patterns.conf の採用済みの行と段階の記録は消さない。
func (e *Env) LearnPurge(a []string) error {
	kind, yes := "", false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--kind":
			if err := needVal(a, i); err != nil {
				return err
			}
			kind = a[i+1]
			i++
		case "--yes":
			yes = true
		default:
			return usage("conv-learn-purge [--kind K] --yes")
		}
	}
	if !yes {
		return usage("conv-learn-purge: 組と台帳を消します。よければ --yes を付ける")
	}
	if kind != "" && !ValidName(kind) {
		return usage("種別の名前が不正です: %s", kind)
	}
	if err := requireHuman("tb conv-learn-purge"); err != nil {
		return err
	}
	s := StateDir()
	if kind != "" {
		os.RemoveAll(filepath.Join(s, "pairs", kind))
		os.Remove(ledgerFile(kind))
		os.Remove(evidenceFile(kind))
	} else {
		os.RemoveAll(filepath.Join(s, "pairs"))
		os.RemoveAll(filepath.Join(s, "ledger"))
	}
	e.say("消しました: %s(採用済みのパターンと段階の記録は残っています)", or(kind, "全種別"))
	return nil
}
