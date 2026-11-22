package conv

// 会話チームの段階的な自動化(taskboard/lib/convstage.sh)。段階の今の値は stage-log.tsv の最後の記録(無い種別は 0)。
// 設定(しきい値・上限・一覧)は tb config の conv-stages。

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var visOrder = []string{"private", "team", "org", "external"}

func stageLog() string    { return filepath.Join(StateDir(), "stage-log.tsv") }
func incidentLog() string { return filepath.Join(StateDir(), "incidents.tsv") }

func (e *Env) stageGet(k, d string) string {
	if v, ok := e.confGet("conv-stages", k); ok {
		return v
	}
	return d
}

var reStageNum = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// stageNum は conv_stage_num: 数として読めなければ既定値。書いたままの文字列と数。
func (e *Env) stageNum(k, d string) (string, float64) {
	v := e.stageGet(k, d)
	if !reStageNum.MatchString(v) {
		v = d
	}
	f, _ := strconv.ParseFloat(v, 64)
	return v, f
}

func (e *Env) inList(k, v string) bool {
	l, ok := e.confGet("conv-stages", k)
	if !ok {
		return false
	}
	return strings.Contains(","+strings.ReplaceAll(l, " ", "")+",", ","+v+",")
}

func (e *Env) isHigh(k string) bool { return e.inList("high", k) }

// stage3OK は conv_stage3_ok: cp が空なら種別の行が 1 つでもあれば true。
func (e *Env) stage3OK(kind, cp string) bool {
	v, ok := e.confGet("conv-stages", "stage3")
	if !ok {
		return false
	}
	for _, x := range strings.Fields(strings.ReplaceAll(v, ",", " ")) {
		k, _, _ := strings.Cut(x, ":")
		if cp == "" && k == kind {
			return true
		}
		if x == kind+":"+cp {
			return true
		}
	}
	return false
}

// lastCol は awk '$2==k {s=$n} END {print s}'。
func lastCol(f, kind string, n int) string {
	s := ""
	for _, l := range fileLines(f) {
		c := strings.Split(l, "\t")
		if col(c, 1) == kind {
			s = col(c, n)
		}
	}
	return s
}

func stageOf(kind string) string { return or(lastCol(stageLog(), kind, 3), "0") }
func stageSince(kind string) string {
	return lastCol(stageLog(), kind, 0)
}

// days は jq の ((now - (t | fromdateiso8601)) / 86400 | floor)。
func days(t any) float64 {
	s, _ := t.(string)
	tm, err := time.Parse("2006-01-02T15:04:05Z", s)
	if err != nil {
		return 0
	}
	return math.Floor(float64(time.Now().UnixNano()-tm.UnixNano()) / 1e9 / 86400)
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// metrics は conv_metrics: 測った値(組の meta と台帳と事故の記録から。本文は読まない)。
func (e *Env) metrics(kind string) (obj, error) {
	winS, win := e.stageNum("window-days", "28")
	adopted := 0
	for _, r := range e.ledgerObjs(kind) {
		if r.(obj).Get("state") == "adopted" {
			adopted++
		}
	}
	inc := ""
	if isFile(incidentLog()) {
		inc = lastCol(incidentLog(), kind, 0)
	}
	since := stageSince(kind)
	ps, err := pairsJSON(kind)
	if err != nil {
		return nil, err
	}
	var w []obj
	var off, acc float64
	for _, p := range ps {
		o, _ := p.(obj)
		if days(o.Get("recorded_at")) < win {
			w = append(w, o)
		}
		x, _ := num(o.Get("points_offered"))
		y, _ := num(o.Get("points_accepted"))
		off += x
		acc += y
	}
	st, _ := strconv.ParseFloat(stageOf(kind), 64)
	var ratio any
	if len(w) > 0 {
		s := 0.0
		for _, o := range w {
			x, _ := num(o.Get("edit_ratio"))
			s += x
		}
		ratio = round3(s / float64(len(w)))
	}
	var lastInc any
	quiet := 0.0
	if inc != "" {
		lastInc, quiet = inc, days(inc)
	} else if len(ps) > 0 {
		quiet = days(ps[0].(obj).Get("recorded_at"))
	}
	var das any
	if since != "" {
		das = days(since)
	}
	var ra any
	if off != 0 {
		ra = round3(acc / off)
	}
	return obj{{K: "stage", V: st}, {K: "pairs", V: len(ps)}, {K: "window_days", V: jnum(winS)}, {K: "window_samples", V: len(w)}, {K: "window_edit_ratio", V: ratio},
		{K: "role_points_offered", V: off}, {K: "role_points_accepted", V: acc}, {K: "adopted_patterns", V: adopted}, {K: "last_incident", V: lastInc},
		{K: "quiet_days", V: quiet}, {K: "days_at_stage", V: das}, {K: "role_acceptance", V: ra}}, nil
}

func mnum(m obj, k string) float64 {
	f, _ := num(m.Get(k))
	return f
}

// stageCheck は conv_stage_check: 段階 to に上げる条件のうち満たさないもの(満たせば空)。
func (e *Env) stageCheck(kind string, to int, m obj) []string {
	var un []string
	j := func(k string) string { return scalar(m.Get(k)) }
	switch to {
	case 1:
		vs, v := e.stageNum("min-pairs.1", "20")
		if mnum(m, "pairs") < v {
			un = append(un, fmt.Sprintf("記録した組が %s 件(min-pairs.1 = %s)", j("pairs"), vs))
		}
	case 2:
		if ds := e.stageGet("draft-save", "unverified"); ds != "available" {
			un = append(un, fmt.Sprintf("下書き保存がメッセージの道具にあると確かめていません(draft-save = %s)", ds))
		}
		vs, v := e.stageNum("min-samples.2", "20")
		if mnum(m, "window_samples") < v {
			un = append(un, fmt.Sprintf("直近 %s 日の組が %s 件(min-samples.2 = %s)", j("window_days"), j("window_samples"), vs))
		}
		vs, v = e.stageNum("max-edit-ratio.2", "0.15")
		if r := m.Get("window_edit_ratio"); r == nil || mnum(m, "window_edit_ratio") > v {
			un = append(un, fmt.Sprintf("直近の編集の割合が %s(max-edit-ratio.2 = %s)", j("window_edit_ratio"), vs))
		}
		vs, v = e.stageNum("min-adopted.2", "3")
		if mnum(m, "adopted_patterns") < v {
			un = append(un, fmt.Sprintf("採用したパターンが %s 件(min-adopted.2 = %s)", j("adopted_patterns"), vs))
		}
		vs, v = e.stageNum("quiet-days.2", "21")
		if mnum(m, "quiet_days") < v {
			un = append(un, fmt.Sprintf("事故の無い日数が %s(quiet-days.2 = %s)", j("quiet_days"), vs))
		}
	case 3:
		if !e.stage3OK(kind, "") {
			un = append(un, "stage3 = の一覧にこの種別がありません")
		}
		vs, v := e.stageNum("quiet-days.3", "42")
		das := m.Get("days_at_stage")
		if das == nil {
			das = 0
		}
		if d, _ := num(das); !(mnum(m, "stage") == 2 && d >= v) {
			un = append(un, fmt.Sprintf("段階 2 の日数が %s(段階 %s。quiet-days.3 = %s)", scalar(das), j("stage"), vs))
		}
		if mnum(m, "quiet_days") < v {
			un = append(un, fmt.Sprintf("事故の無い日数が %s(quiet-days.3 = %s)", j("quiet_days"), vs))
		}
	}
	return un
}

func appendLine(f, line string) error {
	fh, err := os.OpenFile(f, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fh.WriteString(line + "\n")
	return err
}

func (e *Env) stageRecord(kind, from, to, how, reason string, m obj) error {
	if err := stateInit(); err != nil {
		return err
	}
	return appendLine(stageLog(), strings.Join([]string{now(), kind, from, to, how, "human", e.clean(reason), compact(m)}, "\t"))
}

func dirNames(glob string) []string {
	fs, _ := filepath.Glob(glob)
	var out []string
	for _, f := range fs {
		if isDir(f) {
			out = append(out, filepath.Base(f))
		}
	}
	return out
}

// Stage は cmd_conv-stage: 種別ごとの段階と測った値、次の段階の条件。
func (e *Env) Stage(a []string) error {
	kind, json := "", false
	for _, x := range a {
		switch {
		case x == "--json":
			json = true
		case strings.HasPrefix(x, "-"):
			return usage("conv-stage [<kind>] [--json]")
		default:
			kind = x
		}
	}
	var kinds []string
	if kind != "" {
		if err := validKind(kind); err != nil {
			return err
		}
		kinds = []string{kind}
	} else {
		var all []string
		all = append(all, dirNames(filepath.Join(kindsDir(), "*"))...)
		all = append(all, dirNames(filepath.Join(StateDir(), "pairs", "*"))...)
		for _, l := range fileLines(stageLog()) {
			all = append(all, col(strings.Split(l, "\t"), 1))
		}
		seen := map[string]bool{}
		for _, k := range all {
			if ValidName(k) && !seen[k] {
				seen[k] = true
				kinds = append(kinds, k)
			}
		}
		sort.Strings(kinds)
	}
	out := []any{}
	for _, k := range kinds {
		m, err := e.metrics(k)
		if err != nil {
			return jqFail(err)
		}
		s := int(mnum(m, "stage"))
		cap := 3
		if e.isHigh(k) {
			cap = 1
		}
		var next any
		if s < cap {
			un := e.stageCheck(k, s+1, m)
			if len(un) == 0 {
				next = obj{{K: "ok", V: true}, {K: "unmet", V: []any{}}}
			} else {
				l := make([]any, len(un))
				for i, u := range un {
					l[i] = u
				}
				next = obj{{K: "ok", V: false}, {K: "unmet", V: l}}
			}
		}
		o := obj{{K: "kind", V: k}, {K: "max_stage", V: cap}}
		for _, x := range m {
			o = o.Set(x.K, x.V)
		}
		out = append(out, o.Set("next", next))
	}
	if len(out) == 0 {
		if json {
			e.say("[]")
		}
		return nil
	}
	if json {
		e.say("%s", pretty(out))
		return nil
	}
	for _, x := range out {
		o := x.(obj)
		ed := "-"
		if r := o.Get("window_edit_ratio"); r != nil && r != false {
			ed = scalar(r)
		}
		nx := "next -"
		if n, ok := o.Get("next").(obj); ok {
			if n.Get("ok") == true {
				nx = "next ok"
			} else {
				var us []string
				for _, u := range n.Get("unmet").([]any) {
					us = append(us, u.(string))
				}
				nx = "next: " + strings.Join(us, "; ")
			}
		}
		e.say("%s", tsv(o.Get("kind"), "stage "+scalar(o.Get("stage"))+"/"+scalar(o.Get("max_stage")), "pairs "+scalar(o.Get("pairs")), "edit "+ed,
			"adopted "+scalar(o.Get("adopted_patterns")), "quiet "+scalar(o.Get("quiet_days"))+"d", nx))
	}
	return nil
}

// StageSet は cmd_conv-stage-set(人だけ)。上げるのは 1 段ずつ、条件を満たすときだけ(--override で越えたことも記録する)。
func (e *Env) StageSet(a []string) error {
	const u = "conv-stage-set <kind> <0-3> --reason R [--override]"
	if len(a) < 2 {
		return usage(u)
	}
	kind, to := a[0], a[1]
	a = a[2:]
	reason, ov := "", false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--reason":
			if err := needVal(a, i); err != nil {
				return err
			}
			reason = a[i+1]
			i++
		case "--override":
			ov = true
		default:
			return usage("conv-stage-set: 不明な引数 %s", a[i])
		}
	}
	if err := validKind(kind); err != nil {
		return err
	}
	if !regexp.MustCompile(`^[0-3]$`).MatchString(to) {
		return usage("段階は 0〜3: %s", to)
	}
	if blank(reason) {
		return usage("conv-stage-set: --reason(人が決めた理由)が要ります")
	}
	if err := requireHuman("tb conv-stage-set"); err != nil {
		return err
	}
	fromS := stageOf(kind)
	from, _ := strconv.Atoi(fromS)
	m, err := e.metrics(kind)
	if err != nil {
		return jqFail(err)
	}
	t, _ := strconv.Atoi(to)
	if t == from {
		e.say("%s は既に段階 %s です", kind, to)
		return nil
	}
	how := "lower"
	if t > from {
		if t != from+1 {
			return die("上げるのは 1 段ずつです(今 %s)", fromS)
		}
		refuse := func(msg string) error {
			e.errln("tb: " + msg)
			return quiet(4)
		}
		if t >= 2 && e.isHigh(kind) {
			return refuse(kind + " は high(問題の指摘・断り・組織の外)の種別なので、段階 1 より上にしません(conv-stages の high =)")
		}
		if t == 2 && e.stageGet("draft-save", "unverified") != "available" {
			return refuse("下書き保存がメッセージの道具にあると確かめるまで段階 2 にしません(conv-stages の draft-save = available)")
		}
		if t == 3 && !e.stage3OK(kind, "") {
			return refuse("stage3 = の一覧にこの種別がありません(種別:相手の区分 を人が書く)")
		}
		un := e.stageCheck(kind, t, m)
		switch {
		case len(un) == 0:
			how = "raise"
		case ov:
			how = "override"
		default:
			e.errf("tb: 段階 %s に上げる条件を満たしていません(人が判断して上げるなら --override):\n%s\n", to, strings.Join(un, "\n"))
			return quiet(4)
		}
	}
	if err := e.stageRecord(kind, fromS, to, how, reason, m); err != nil {
		return err
	}
	e.say("%s: 段階 %s → %s (%s)", kind, fromS, to, how)
	return nil
}

// StageReset は cmd_conv-stage-reset: すべての種別を段階 to(既定 1)以下に戻す。データは消さない。
func (e *Env) StageReset(a []string) error {
	to, reason := "1", ""
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--to", "--reason":
			if err := needVal(a, i); err != nil {
				return err
			}
			if a[i] == "--to" {
				to = a[i+1]
			} else {
				reason = a[i+1]
			}
			i++
		default:
			return usage("conv-stage-reset [--to 0|1] --reason R")
		}
	}
	if to != "0" && to != "1" {
		return usage("conv-stage-reset: --to は 0 か 1")
	}
	if blank(reason) {
		return usage("conv-stage-reset: --reason が要ります")
	}
	if err := requireHuman("tb conv-stage-reset"); err != nil {
		return err
	}
	if !isFile(stageLog()) {
		e.say("段階を上げた種別はありません")
		return nil
	}
	seen := map[string]bool{}
	var ks []string
	for _, l := range fileLines(stageLog()) {
		k := col(strings.Split(l, "\t"), 1)
		if !seen[k] {
			seen[k] = true
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	n := 0
	t, _ := strconv.Atoi(to)
	for _, k := range ks {
		s := stageOf(k)
		if si, _ := strconv.Atoi(s); si <= t {
			continue
		}
		m, err := e.metrics(k)
		if err != nil {
			return jqFail(err)
		}
		if err := e.stageRecord(k, s, to, "reset", reason, m); err != nil {
			return err
		}
		n++
		e.say("%s: 段階 %s → %s", k, s, to)
	}
	if n == 0 {
		e.say("段階 %s より上の種別はありません", to)
	}
	return nil
}

// StageLogCmd は cmd_conv-stage-log。
func (e *Env) StageLogCmd(a []string) error {
	kind, json := "", false
	for _, x := range a {
		switch {
		case x == "--json":
			json = true
		case strings.HasPrefix(x, "-"):
			return usage("conv-stage-log [<kind>] [--json]")
		default:
			kind = x
		}
	}
	if !isFile(stageLog()) {
		if json {
			e.say("[]")
		}
		return nil
	}
	out := []any{}
	for _, l := range fileLines(stageLog()) {
		c := strings.Split(l, "\t")
		if kind != "" && col(c, 1) != kind {
			continue
		}
		if !json {
			e.say("%s\t%s\t%s→%s\t%s\t%s\t%s", col(c, 0), col(c, 1), col(c, 2), col(c, 3), col(c, 4), col(c, 5), col(c, 6))
			continue
		}
		mv, err := decodeAll([]byte(col(c, 7)))
		if err != nil || len(mv) != 1 {
			return jqFail(fmt.Errorf("%s (while parsing '%s')", "metrics", col(c, 7)))
		}
		n := func(s string) any {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil
			}
			return f
		}
		out = append(out, obj{{K: "at", V: col(c, 0)}, {K: "kind", V: col(c, 1)}, {K: "from", V: n(col(c, 2))}, {K: "to", V: n(col(c, 3))}, {K: "how", V: col(c, 4)},
			{K: "by", V: col(c, 5)}, {K: "reason", V: col(c, 6)}, {K: "metrics", V: mv[0]}})
	}
	if json {
		e.say("%s", pretty(out))
	}
	return nil
}

// Incident は cmd_conv-incident(人だけ)。記録するだけで段階は変えない。
func (e *Env) Incident(a []string) error {
	const u = "conv-incident <kind> --note N"
	if len(a) < 1 {
		return usage(u)
	}
	kind, note := a[0], ""
	a = a[1:]
	for i := 0; i < len(a); i++ {
		if a[i] != "--note" {
			return usage(u)
		}
		if err := needVal(a, i); err != nil {
			return err
		}
		note = a[i+1]
		i++
	}
	if err := validKind(kind); err != nil {
		return err
	}
	if blank(note) {
		return usage("conv-incident: --note が要ります")
	}
	if err := requireHuman("tb conv-incident"); err != nil {
		return err
	}
	if err := stateInit(); err != nil {
		return err
	}
	if err := appendLine(incidentLog(), now()+"\t"+kind+"\t"+e.clean(note)); err != nil {
		return err
	}
	e.say("%s: 事故を記録しました(段階は %s のまま。下げるなら tb conv-stage-set %s 1 --reason ...)", kind, stageOf(kind), kind)
	return nil
}

func visRank(v string) int {
	for i, x := range visOrder {
		if x == v {
			return i + 1
		}
	}
	return 0
}

// Gate は cmd_conv-gate: 決まった表だけで判定する(読み取りのみ)。0=allow 20=ask 4=deny。
func (e *Env) Gate(a []string) error {
	if len(a) < 1 {
		return usage("conv-gate <kind> --action draft|review|save-draft|post [--counterpart C] [--severity S] [--source-vis V --dest-vis V]")
	}
	kind := a[0]
	a = a[1:]
	act, cp, sev, sv, dv := "", "", "normal", "", ""
	for i := 0; i < len(a); i++ {
		p := map[string]*string{"--action": &act, "--counterpart": &cp, "--severity": &sev, "--source-vis": &sv, "--dest-vis": &dv}[a[i]]
		if p == nil {
			return usage("conv-gate: 不明な引数 %s", a[i])
		}
		if err := needVal(a, i); err != nil {
			return err
		}
		*p = a[i+1]
		i++
	}
	if err := validKind(kind); err != nil {
		return err
	}
	if !regexp.MustCompile(`^(draft|review|save-draft|post)$`).MatchString(act) {
		return usage("conv-gate: --action は draft|review|save-draft|post")
	}
	if !regexp.MustCompile(`^(low|normal|high)$`).MatchString(sev) {
		return usage("conv-gate: --severity は low|normal|high")
	}
	if cp != "" && !ValidName(cp) {
		return usage("conv-gate: --counterpart は相手の区分の名前(英小文字・数字・-)")
	}
	vis := strings.Join(visOrder, " ")
	if sv != "" && visRank(sv) == 0 {
		return usage("conv-gate: --source-vis は %s のどれか", vis)
	}
	if dv != "" && visRank(dv) == 0 {
		return usage("conv-gate: --dest-vis は %s のどれか", vis)
	}
	stS := stageOf(kind)
	st, _ := strconv.Atoi(stS)
	cap, why := 3, ""
	if e.isHigh(kind) {
		cap, why = 1, "high の種別"
	}
	if sev == "high" {
		cap, why = 1, "severity high"
	}
	if dv == "external" {
		cap, why = 1, "組織の外へ出る"
	}
	eff := st
	if cap < st {
		eff = cap
	}
	gate := func(v, r string) {
		w := ""
		if why != "" {
			w = ", " + why
		}
		e.say("%s\nreason: %s\nstage: %d (設定 %s, 上限 %d%s)", v, r, eff, stS, cap, w)
	}
	flow := func() bool {
		if sv == "" || dv == "" {
			gate("deny", "情報の流れを確かめられません(--source-vis と --dest-vis が要る)")
			return false
		}
		if visRank(dv) > visRank(sv) {
			gate("deny", "元の情報("+sv+")より広い範囲("+dv+")へ出すことになります")
			return false
		}
		return true
	}
	switch act {
	case "draft":
		gate("allow", "下書きはどの段階でも作れます(送るのは人)")
	case "review":
		if eff < 1 {
			gate("deny", "段階 0 では声の役割を使いません")
			return quiet(4)
		}
		gate("allow", "声の役割の指摘(段階 1 以上)")
	case "save-draft":
		if eff < 2 {
			gate("deny", "下書き保存は段階 2 以上の種別だけ")
			return quiet(4)
		}
		if e.stageGet("draft-save", "unverified") != "available" {
			gate("deny", "下書き保存がメッセージの道具にあると確かめていません(draft-save)")
			return quiet(4)
		}
		if !flow() {
			return quiet(4)
		}
		gate("allow", "下書きとして保存するだけ(送らない)")
	case "post":
		if eff < 3 {
			gate("deny", "送るのは人です(段階 3 の種別ではない)")
			return quiet(4)
		}
		if cp == "" || !e.stage3OK(kind, cp) {
			gate("deny", "stage3 = の一覧に "+kind+":"+or(cp, "?")+" がありません。送るのは人です")
			return quiet(4)
		}
		if !flow() {
			return quiet(4)
		}
		gate("ask", "段階 3: この 1 通を人が承認すれば送れます(承認なしには送らない)")
		return quiet(20)
	}
	return nil
}
