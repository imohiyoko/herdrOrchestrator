package conv

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/util"
)

// Err は tb の終了: Code 2 は使い方(usage)、1 は die。Msg が空なら何も言わずにその終了コードで終わる。
type Err struct {
	Code int
	Msg  string
}

func (e *Err) Error() string { return e.Msg }

func usage(f string, a ...any) error { return &Err{2, fmt.Sprintf(f, a...)} }
func die(f string, a ...any) error   { return &Err{1, fmt.Sprintf(f, a...)} }
func quiet(code int) error           { return &Err{code, ""} }

// requireHuman はコマンドの中の人だけの確かめ(bash の require_human)。段階は権限段階の表(ADR 0006)と設定 policy の行で決まる。
func requireHuman(what string) error {
	var d *perms.Denied
	if err := perms.RequireHuman(what, nil); errors.As(err, &d) {
		return &Err{d.Code, d.Msg}
	} else if err != nil {
		return err
	}
	return nil
}

// Env は 1 回の実行に要るもの。
type Env struct {
	DB         *sql.DB
	Sess       string
	W, Err     io.Writer
	In         io.Reader
	StdinIsTTY bool
}

func (e *Env) say(f string, a ...any) { fmt.Fprintf(e.W, f+"\n", a...) }

// errf は標準エラーに書く。先に標準出力を流しておき、bash と同じ順に並ぶようにする。
func (e *Env) errf(f string, a ...any) {
	if fl, ok := e.W.(interface{ Flush() error }); ok {
		_ = fl.Flush()
	}
	fmt.Fprintf(e.Err, f, a...)
}

func (e *Env) errln(s string) { e.errf("%s\n", s) }

func needName(what, v string) error {
	if !ValidName(v) {
		return usage("%s は英小文字で始まる 40 文字までの英小文字・数字・-: %s", what, v)
	}
	return nil
}

func needFile(what, p string) error {
	if !Readable(p) {
		return usage("%s のファイルを読めません: %s", what, p)
	}
	return nil
}

// args は need_arg の付いた「--名前 値」と、値を取らない名前を読む。unknown は知らない引数のときの誤り。
func args(a []string, vals, bools []string, unknown func(string) error) (map[string]string, error) {
	m := map[string]string{}
	for i := 0; i < len(a); i++ {
		switch {
		case has(bools, a[i]):
			m[a[i]] = "1"
		case has(vals, a[i]):
			if i+1 >= len(a) {
				return nil, usage("%s に値が必要です", a[i])
			}
			m[a[i]] = a[i+1]
			i++
		default:
			return nil, unknown(a[i])
		}
	}
	return m, nil
}

// routesTable は conv_routes_file: 今効いている振り分け表(無ければ nil)。
func (e *Env) routesBody() (string, bool) {
	b, _, ok := config.Lookup(e.DB, e.Sess, "conv-routes")
	return b, ok
}

// Route は cmd_conv-route。
func (e *Env) Route(a []string) error {
	var ev, cp, dp, sv, dl string
	json, stdin := false, false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--event", "--counterpart", "--depth", "--severity", "--deadline":
			if i+1 >= len(a) {
				return usage("%s に値が必要です", a[i])
			}
			v := a[i+1]
			switch a[i] {
			case "--event":
				ev = v
			case "--counterpart":
				cp = v
			case "--depth":
				dp = v
			case "--severity":
				sv = v
			case "--deadline":
				dl = v
			}
			i++
		case "--stdin":
			stdin = true
		case "--json":
			json = true
		default:
			return usage("conv-route: 不明な引数 %s(依頼は --event --counterpart --depth --severity --deadline の 5 つの名前だけ。本文は渡さない)", a[i])
		}
	}
	if stdin {
		b, _ := io.ReadAll(e.In)
		m, ok, msg := stdinObject(b)
		if !ok {
			return usage("%s", msg)
		}
		ev, cp, dp, sv, dl = m["event"], m["counterpart"], m["depth"], m["severity"], m["deadline"]
	}
	for _, x := range [][2]string{{"event", ev}, {"counterpart", cp}, {"depth", dp}, {"severity", sv}, {"deadline", dl}} {
		if x[1] == "" {
			return usage("conv-route: --%s が要ります", x[0])
		}
		if err := needName("--"+x[0], x[1]); err != nil {
			return err
		}
	}
	var r Result
	ver := "0"
	if body, ok := e.routesBody(); ok {
		t, why := Load(body)
		if t == nil {
			return die("振り分け表が読めません(conv-routes): %s", why)
		}
		r, ver = t.Route([5]string{ev, cp, dp, sv, dl}), t.Ver
	} else {
		r = Result{Reason: "no-table"}
	}
	q := util.JQString
	switch {
	case r.Routed && json:
		e.say(`{"status":"routed","kind":%s,"counterpart":%s,"channel":%s,"timing":%s,"severity":%s,"needs_human":false,"table_version":%s,"rule_line":%d}`,
			q(r.Kind), q(r.Counterpart), q(r.Channel), q(r.Timing), q(r.Sv), ver, r.Line)
	case r.Routed:
		e.say("routed\tkind=%s\tcounterpart=%s\tchannel=%s\ttiming=%s\tseverity=%s\tversion=%s\tline=%d", r.Kind, r.Counterpart, r.Channel, r.Timing, r.Sv, ver, r.Line)
	case json:
		e.say(`{"status":"unregistered","kind":null,"counterpart":%s,"channel":null,"timing":null,"severity":%s,"needs_human":true,"reason":%s,"table_version":%s}`,
			q(cp), q(sv), q(r.Reason), ver)
	default:
		e.say("unregistered\treason=%s\tneeds_human=yes\tversion=%s", r.Reason, ver)
	}
	return nil
}

// RoutesSet は cmd_conv-routes-set(人だけ)。
func (e *Env) RoutesSet(a []string) error {
	shared, approve, dry := false, false, false
	for _, x := range a {
		switch x {
		case "--shared":
			shared = true
		case "--approve-looser":
			approve = true
		case "--dry-run":
			dry = true
		default:
			return usage("conv-routes-set [--shared] [--approve-looser] [--dry-run] < 新しい表")
		}
	}
	if e.StdinIsTTY {
		return usage("conv-routes-set: 新しい表は標準入力から渡す(例: tb conv-routes-set < conv-routes.conf)")
	}
	if !dry {
		if err := requireHuman("tb conv-routes-set"); err != nil {
			return err
		}
	}
	info, _ := config.Info("conv-routes")
	envFile := os.Getenv(info.Env)
	if shared && envFile != "" {
		return die("TB_CONV_ROUTES でファイルを明示しているので --shared は使えません")
	}
	b, _ := io.ReadAll(e.In)
	body := strings.TrimRight(string(b), "\n")
	var old *Table
	if cur, ok := e.routesBody(); ok {
		t, why := Load(cur)
		if t == nil {
			return die("振り分け表を書けません: 今の表: %s", why)
		}
		old = t
	}
	nw, why := Load(body)
	if nw == nil {
		return die("振り分け表を書けません: %s", why)
	}
	vOld := "0"
	if old != nil {
		vOld = old.Ver
	}
	if !numGreater(nw.Ver, vOld) {
		return die("version を今の表(%s)より大きくしてください(新しい表は %s)", vOld, nw.Ver)
	}
	lo, ch, stc, big := Compare(old, nw)
	if big {
		e.say("組み合わせが多すぎて比べられません。緩める変更として扱います")
		lo, ch, stc = 1, 0, 0
	} else {
		e.say("version %s -> %s: 緩める(未登録 → 振り分け)=%d 振り分け先の変更=%d 厳しくする(振り分け → 未登録)=%d", vOld, nw.Ver, lo, ch, stc)
	}
	how := "conv-routes-set:stricter"
	if lo+ch > 0 {
		if !approve {
			e.errln("tb: 振り分けを緩める・変える変更です。人が内容を確かめたうえで --approve-looser を付けて実行してください")
			return quiet(20)
		}
		how = "conv-routes-set:looser-approved"
	}
	if dry {
		e.say("(dry-run: 何も書いていません)")
		return nil
	}
	if envFile != "" {
		if err := writeFile(envFile, body); err != nil {
			return err
		}
	} else if err := config.DBPut(e.DB, e.Sess, "conv-routes", body, how, shared, ""); err != nil {
		return err
	}
	_, src, _ := config.Lookup(e.DB, e.Sess, "conv-routes")
	e.say("conv-routes: %s(%s)", src, how)
	return nil
}

func numGreater(a, b string) bool {
	x, _ := strconv.ParseFloat(a, 64)
	y, _ := strconv.ParseFloat(b, 64)
	return x > y
}

// writeFile は config_put のファイルの書き方(同じディレクトリの一時ファイルから置き換え、今の権限を保つ)。
func writeFile(f, body string) error {
	if st, err := os.Lstat(f); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return die("%s がシンボリックリンクです: %s", filepath.Base(f), f)
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), filepath.Base(f)+".")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(f); err == nil {
		mode = st.Mode().Perm()
	}
	if body != "" {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f)
}

var reRefBad = regexp.MustCompile("[[:space:]\"'<>`]")

func cat(w io.Writer, p string) {
	b, _ := os.ReadFile(p)
	w.Write(b)
}

// Draft は cmd_conv-draft: 下書きの指示を組み立てて出す。
func (e *Env) Draft(a []string) error {
	var kind, route, note, ref, max string
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--kind", "--route", "--note", "--ref", "--max-chars", "--request":
			if i+1 >= len(a) {
				return usage("%s に値が必要です", a[i])
			}
			v := a[i+1]
			switch a[i] {
			case "--kind":
				kind = v
			case "--route":
				route = v
			case "--note":
				note = v
			case "--ref":
				ref = v
			case "--max-chars":
				max = v
			case "--request":
				if err := needFile("--request", v); err != nil {
					return err
				}
				max = strconv.Itoa(FileChars(v))
			}
			i++
		default:
			return usage("conv-draft (--route <tb conv-route --json の出力のファイル|-> | --kind <種別>) [--note 一般化した状況] [--ref 参照] [--max-chars N | --request <依頼のファイル>]")
		}
	}
	if kind == "" && route == "" {
		return usage("conv-draft: --route か --kind が要ります")
	}
	if kind != "" && route != "" {
		return usage("conv-draft: --route と --kind は一緒に使えません")
	}
	if note != "" {
		if Strlen(note) > NoteMax {
			return usage("conv-draft: --note は %d 文字まで", NoteMax)
		}
		if m := Generalized("--note", note); m != "" {
			return usage("conv-draft: %s", m)
		}
	}
	if ref != "" && (len(ref) > 200 || reRefBad.MatchString(ref)) {
		return usage("conv-draft: --ref は 200 文字までで、空白・引用符・<> を含まない")
	}
	if max != "" && !regexp.MustCompile(`^[1-9][0-9]{0,5}$`).MatchString(max) {
		return usage("conv-draft: --max-chars は 1 以上の整数")
	}
	cp, ch, tm, sv := "-", "-", "-", "-"
	needs, team, why := false, false, ""
	if route != "" {
		var b []byte
		if route == "-" {
			b, _ = io.ReadAll(e.In)
		} else {
			if err := needFile("--route", route); err != nil {
				return err
			}
			b, _ = os.ReadFile(route)
		}
		m, ok := routeObject(b)
		if !ok {
			return usage("conv-draft --route: tb conv-route --json の出力を渡す")
		}
		for _, f := range []string{"kind", "counterpart", "channel", "timing", "severity"} {
			i := m.str(f)
			if i != "" && !ValidName(i) {
				return usage("conv-draft --route: %s の値が不正です", f)
			}
			d := i
			if d == "" {
				d = "-"
			}
			switch f {
			case "kind":
				kind = i
			case "counterpart":
				cp = d
			case "channel":
				ch = d
			case "timing":
				tm = d
			case "severity":
				sv = d
			}
		}
		if m.status == "unregistered" {
			needs = true
			why = "振り分け表に無い依頼です(" + m.reason + ")"
			kind = ""
		}
	} else if err := needName("--kind", kind); err != nil {
		return err
	}
	if kind != "" {
		switch {
		case !TeamsOn():
			why = "チームの層は止めてあります(TB_CONV_TEAMS=off)"
		case HasTeam(kind):
			team = true
		default:
			why = "種別 " + kind + " のチームはありません"
		}
	}
	base := filepath.Join(Dir(), "_base.md")
	if st, err := os.Stat(base); err != nil || !st.Mode().IsRegular() {
		return die("共通の土台がありません: %s", base)
	}
	e.say("# 下書きの指示(tb conv-draft)")
	if team {
		e.say("チーム: %s", kind)
	} else {
		if why == "" {
			why = "種別が決まっていません"
		}
		e.say("チーム: なし。共通の土台だけで下書きします — %s", why)
	}
	if needs {
		e.say("要: 人の判断(振り分けが決まっていません。誰に・いつ・どこで送るかは人が決めます)")
	}
	e.say("")
	cat(e.W, base)
	if team {
		e.say("")
		e.say("## チーム: %s", kind)
		e.say("")
		cat(e.W, filepath.Join(Dir(), "kinds", kind, "prompt.md"))
		pats, _ := ReadPatterns(PatternsFile(kind))
		n := 0
		for _, p := range pats {
			if !p.Adopted() {
				continue
			}
			if n == 0 {
				e.say("")
				e.say("## 採用済みのパターン(人が採用したもの)")
				e.say("")
			}
			n++
			c := "末尾"
			if p[6] == "first" {
				c = "先頭"
			}
			e.say("- %s: 場面=%s / 意図=%s / 終わり方=%s / 結論=%s / 避ける=%s", p[0], p[3], p[4], p[5], c, p[7])
		}
	}
	e.say("")
	e.say("## 今回の依頼")
	e.say("")
	e.say("- 種別: %s", or(kind, "-"))
	e.say("- 相手の区分: %s / 経路の区分: %s / 時機: %s / 重大度: %s", cp, ch, tm, sv)
	e.say("- 状況(一般化したもの): %s", or(note, "-"))
	e.say("- 参照: %s(元の文面は参照先で読む。ここには写さない)", or(ref, "-"))
	if max != "" {
		e.say("- 長さ: %s 文字以内(依頼より長くしない)", max)
	} else {
		e.say("- 長さ: 依頼より長くしない")
	}
	return nil
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Voice は cmd_conv-voice: 役の指示と下書きだけを出す(ほかの役の出力・依頼の本文は入れない)。
func (e *Env) Voice(a []string) error {
	if len(a) < 1 {
		return usage("conv-voice <役> --draft <下書きのファイル> [--request <依頼のファイル> | --request-chars N]")
	}
	role, draft, rc := a[0], "", ""
	a = a[1:]
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--draft", "--request", "--request-chars":
			if i+1 >= len(a) {
				return usage("%s に値が必要です", a[i])
			}
			v := a[i+1]
			switch a[i] {
			case "--draft":
				draft = v
			case "--request":
				if err := needFile("--request", v); err != nil {
					return err
				}
				rc = strconv.Itoa(FileChars(v))
			case "--request-chars":
				if !regexp.MustCompile(`^[0-9]{1,6}$`).MatchString(v) {
					return usage("--request-chars は整数")
				}
				rc = v
			}
			i++
		default:
			return usage("conv-voice: 不明な引数 %s", a[i])
		}
	}
	vf := VoiceFile(role)
	if vf == "" {
		vs := ""
		for _, v := range Voices() {
			vs += v + " "
		}
		return usage("conv-voice: 声の役がありません: %s(あるのは: %s)", role, vs)
	}
	if draft == "" {
		return usage("conv-voice: --draft が要ります")
	}
	if err := needFile("--draft", draft); err != nil {
		return err
	}
	if role == "brevity" && rc == "" {
		return usage("conv-voice brevity: --request か --request-chars が要ります(依頼より長いかを必ず言うため)")
	}
	dc := FileChars(draft)
	e.say("# 声の役: %s(tb conv-voice)", role)
	e.say("")
	cat(e.W, vf)
	e.say("")
	e.say("## 出力の形")
	e.say("")
	e.say("- 1 行 1 点、行頭は「- 」。多くても %d 点。直す点が無ければ「- なし」", VoiceMax)
	if role == "brevity" {
		r, _ := strconv.Atoi(rc)
		if dc > r {
			e.say("- 1 行目に必ず「長さ: 依頼より長い」と書く(下書き %d 文字 / 依頼 %s 文字)", dc, rc)
		} else {
			e.say("- 1 行目に必ず「長さ: 依頼より長くない」と書く(下書き %d 文字 / 依頼 %s 文字)", dc, rc)
		}
	}
	e.say("- 下書きを書き直さない。ほかの役の意見は見ていない前提で、自分の観点だけを書く")
	e.say("")
	e.say("## 下書き")
	e.say("")
	cat(e.W, draft)
	return nil
}

var reLen = regexp.MustCompile(`^長さ(:|：)[[:space:]]*(依頼より長い|依頼より長くない)$`)

// VoiceCheck は cmd_conv-voice-check: 役の出力の形を確かめ、整えたものを出す。0=正しい / 1=形が違う。
func (e *Env) VoiceCheck(a []string) error {
	if len(a) < 1 {
		return usage("conv-voice-check <役> [--draft <下書き> --request <依頼>] < 役の出力")
	}
	role, draft, req := a[0], "", ""
	a = a[1:]
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--draft", "--request":
			if i+1 >= len(a) {
				return usage("%s に値が必要です", a[i])
			}
			if err := needFile(a[i], a[i+1]); err != nil {
				return err
			}
			if a[i] == "--draft" {
				draft = a[i+1]
			} else {
				req = a[i+1]
			}
			i++
		default:
			return usage("conv-voice-check: 不明な引数 %s", a[i])
		}
	}
	if VoiceFile(role) == "" {
		return usage("conv-voice-check: 声の役がありません: %s", role)
	}
	if (draft == "") != (req == "") {
		return usage("conv-voice-check: --draft と --request は両方を渡す")
	}
	b, _ := io.ReadAll(e.In)
	ls := strings.Split(string(b), "\n")
	if len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	var out []string
	l, first := "", true
	for _, line := range ls {
		line = config.Trim(line)
		if line == "" {
			continue
		}
		if first && role == "brevity" {
			if m := reLen.FindStringSubmatch(line); m != nil {
				l, first = m[2], false
				continue
			}
		}
		first = false
		if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "・") {
			e.errln("tb: 行頭が「- 」ではない行があります")
			return quiet(1)
		}
		line = strings.TrimPrefix(line, "- ")
		line = config.Trim(strings.TrimPrefix(line, "・"))
		if line == "" {
			continue
		}
		out = append(out, "- "+line)
	}
	if len(out) < 1 {
		e.errln("tb: 点がありません(直す点が無ければ「- なし」)")
		return quiet(1)
	}
	if len(out) > VoiceMax {
		e.errf("tb: 点が多すぎます(%d 点。多くても %d 点)\n", len(out), VoiceMax)
		return quiet(1)
	}
	if role == "brevity" {
		if l == "" {
			e.errln("tb: brevity は 1 行目に「長さ: 依頼より長い」か「長さ: 依頼より長くない」を書きます")
			return quiet(1)
		}
		if draft != "" {
			exp := "依頼より長くない"
			if FileChars(draft) > FileChars(req) {
				exp = "依頼より長い"
			}
			if l != exp {
				e.errf("tb: 長さの判定が実際と違います(実際は「%s」)\n", exp)
				return quiet(1)
			}
		}
		e.say("長さ: %s", l)
	}
	for _, o := range out {
		e.say("%s", o)
	}
	return nil
}

// Length は cmd_conv-length: 0=依頼以下 / 1=依頼より長い。
func (e *Env) Length(a []string) error {
	const u = "conv-length --draft <下書き> --request <依頼>"
	m, err := args(a, []string{"--draft", "--request"}, nil, func(string) error { return usage(u) })
	if err != nil {
		return err
	}
	draft, req := m["--draft"], m["--request"]
	if draft == "" || req == "" {
		return usage(u)
	}
	if err := needFile("--draft", draft); err != nil {
		return err
	}
	if err := needFile("--request", req); err != nil {
		return err
	}
	d, r := FileChars(draft), FileChars(req)
	if d > r {
		e.say("draft=%d\trequest=%d\tlonger=yes", d, r)
		return quiet(1)
	}
	e.say("draft=%d\trequest=%d\tlonger=no", d, r)
	return nil
}

// KindsCmd は cmd_conv-kinds。
func (e *Env) KindsCmd(a []string) error {
	json := false
	if len(a) > 0 && a[0] == "--json" {
		json, a = true, a[1:]
	}
	if len(a) != 0 {
		return usage("conv-kinds [--json]")
	}
	var rows []string
	for _, k := range Kinds() {
		t := "no"
		if HasTeam(k) {
			t = "yes"
		}
		pats, errs := ReadPatterns(PatternsFile(k))
		ad, ca := 0, 0
		for _, p := range pats {
			if p.Adopted() {
				ad++
			} else {
				ca++
			}
		}
		if json {
			rows = append(rows, fmt.Sprintf(`{"kind":%s,"team":%t,"adopted":%d,"candidate":%d,"errors":%d}`, util.JQString(k), t == "yes", ad, ca, len(errs)))
		} else {
			rows = append(rows, fmt.Sprintf("%s\tteam=%s\tadopted=%d\tcandidate=%d\terrors=%d", k, t, ad, ca, len(errs)))
		}
	}
	if json {
		e.say("[%s]", strings.Join(rows, ","))
		return nil
	}
	for _, r := range rows {
		e.say("%s", r)
	}
	return nil
}

// Patterns は cmd_conv-patterns。
func (e *Env) Patterns(a []string) error {
	const u = "conv-patterns <種別> [--all]"
	if len(a) < 1 {
		return usage(u)
	}
	kind, all := a[0], false
	a = a[1:]
	if len(a) > 0 && a[0] == "--all" {
		all, a = true, a[1:]
	}
	if len(a) != 0 {
		return usage(u)
	}
	if err := needName("種別", kind); err != nil {
		return err
	}
	if st, err := os.Stat(filepath.Join(Dir(), "kinds", kind)); err != nil || !st.IsDir() {
		return die("種別がありません: %s", kind)
	}
	pats, errs := ReadPatterns(PatternsFile(kind))
	for _, x := range errs {
		e.errf("tb: patterns.conf %s\n", x)
	}
	for _, p := range pats {
		if all || p.Adopted() {
			e.say("%s", strings.Join(p[:], "\t"))
		}
	}
	return nil
}

var reRouteKind = regexp.MustCompile(`^.*->.*kind=([a-z][a-z0-9-]*)`)

// Lint は cmd_conv-lint: 0=問題なし / 1=問題あり。
func (e *Env) Lint(a []string) error {
	if len(a) != 0 {
		return usage("conv-lint")
	}
	bad := false
	if st, err := os.Stat(filepath.Join(Dir(), "_base.md")); err != nil || !st.Mode().IsRegular() {
		e.say("_base.md: ありません")
		bad = true
	}
	for _, v := range []string{"direct", "empathetic", "receiver", "brevity"} {
		if VoiceFile(v) == "" {
			e.say("voices/%s.md: ありません", v)
			bad = true
		}
	}
	for _, k := range Kinds() {
		if !HasTeam(k) {
			e.say("kinds/%s: prompt.md がありません(この種別は共通の土台だけで下書きされます)", k)
			bad = true
		}
		_, errs := ReadPatterns(PatternsFile(k))
		for _, x := range errs {
			e.say("kinds/%s/patterns.conf %s", k, x)
			bad = true
		}
	}
	if body, ok := e.routesBody(); ok {
		t, why := Load(body)
		if t == nil {
			e.say("conv-routes: %s", why)
			bad = true
		} else {
			_, src, _ := config.Lookup(e.DB, e.Sess, "conv-routes")
			e.say("conv-routes: version %s、%d 行(%s)", t.Ver, t.Rules(), src)
			seen := map[string]bool{}
			var ks []string
			if body != "" {
				for _, l := range strings.Split(body, "\n") {
					if m := reRouteKind.FindStringSubmatch(l); m != nil && !seen[m[1]] {
						seen[m[1]] = true
						ks = append(ks, m[1])
					}
				}
			}
			sort.Strings(ks)
			for _, k := range ks {
				if !HasTeam(k) {
					e.say("conv-routes: 種別 %s のチームはありません(共通の土台だけで下書きされます)", k)
				}
			}
		}
	} else {
		e.say("conv-routes: ありません(すべての依頼が未登録になり、人の判断に回ります)")
	}
	if bad {
		return quiet(1)
	}
	e.say("ok")
	return nil
}
