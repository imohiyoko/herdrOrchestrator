package digest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app/agent"
	"github.com/imohiyoko/herdrorchestrator/internal/jobs"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/jv"
	"github.com/imohiyoko/herdrorchestrator/internal/limits"
)

// scheduler の見張り(taskboard/lib/schedwatch.sh。tb digest の観測)。
// scheduler のログ(scheduler/logs/<job>.log、1 行 1 JSON)と、ジョブの実効設定(scheduler/bin/show.sh)を読むだけ。
//   schedoff   : ENABLED=0 のまま disabled_min 分以上、skipped_disabled が続いているジョブ
//   schedstale : 見張り系ジョブで、有効で窓の中なのに、ran が watch_stale_min 分以上無い
//   schedfail  : 直近 fail_window_min 分の実行が fail_min_runs 回以上あり、failed の割合が fail_rate_pct % 以上(exit 10 の待ちは数えない)
//   schedwait  : 直近の記録が exit 10 の待ちだけになってから wait_max_min 分以上たった
// 同じ観測は、状態が変わるまで 1 回だけ(observations の key。条件が外れたら記録を消す)。

// swCfg は sw_cfg: 環境変数 TB_SCHEDWATCH_<キー大文字> > sched-watch.conf > .example > 既定。
func (d *run) swCfg(key, def string) string {
	if v := os.Getenv("TB_SCHEDWATCH_" + strings.ToUpper(key)); v != "" {
		return v
	}
	if body, _, ok := config.Lookup(d.db.DB, d.db.Session, "sched-watch"); ok {
		for _, line := range strings.Split(body, "\n") {
			line = config.Trim(strings.SplitN(line, "#", 2)[0])
			k, v, has := strings.Cut(line, "=")
			if !has {
				continue
			}
			if config.Trim(k) == key {
				return config.Trim(v)
			}
		}
	}
	return def
}

// swSum は sw_summarize の結果(無い値は nil)。
type swSum struct {
	n                                  int
	lastAction                         any
	lastIsDisabled                     bool
	streakStart, lastRan, enabledSince *int64
	waitStart                          *int64
	streakCount, ranW, failedW, waitW  int
	waitCount                          int
}

var reTsOff = regexp.MustCompile(`^(.{19})([+-])([0-9]{2}):?([0-9]{2})$`)

// swEpoch は jq の ep: "...Z" か "...+HH:MM"(+HHMM)の時刻を epoch に。読めなければ ok=false(jq は全体が失敗する)。
func swEpoch(t string) (int64, bool) {
	const lay = "2006-01-02T15:04:05"
	if strings.HasSuffix(t, "Z") {
		tm, err := time.Parse(lay, strings.TrimSuffix(t, "Z"))
		if err != nil {
			return 0, false
		}
		return tm.Unix(), true
	}
	m := reTsOff.FindStringSubmatch(t)
	if m == nil {
		return 0, false
	}
	tm, err := time.Parse(lay, m[1])
	if err != nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[3])
	mi, _ := strconv.Atoi(m[4])
	off := int64(h*3600 + mi*60)
	if m[2] == "-" {
		off = -off
	}
	return tm.Unix() - off, true
}

// swSummarize は sw_summarize: ログ 1 ファイルを集計する($2=現在 $3=窓(秒))。dry_run の行は数えない。
func swSummarize(path string, now, win int64) (swSum, bool) {
	var s swSum
	b, err := os.ReadFile(path)
	if err != nil {
		return s, false
	}
	type ev struct {
		e int64
		a string
		w bool
	}
	var L []ev
	for _, line := range strings.Split(string(b), "\n") {
		vs, err := jv.DecodeAll([]byte(line))
		if err != nil || len(vs) != 1 {
			continue // fromjson? は読めない行を飛ばす
		}
		o, ok := vs[0].(jv.Obj)
		if !ok {
			continue
		}
		if dr := o.Get("dry_run"); dr != nil && dr != false {
			continue
		}
		ts, ok1 := o.Get("ts").(string)
		act, ok2 := o.Get("action").(string)
		if !ok1 || !ok2 {
			continue
		}
		e, ok := swEpoch(ts)
		if !ok {
			return s, false
		}
		w := false
		if act == "failed" {
			if x, ok := jv.Num(o.Get("exit")); ok && x == 10 {
				w = true
			}
		}
		L = append(L, ev{e, act, w})
	}
	sort.SliceStable(L, func(i, j int) bool { return L[i].e < L[j].e })
	maxOf := func(f func(ev) bool) *int64 {
		var r *int64
		for _, x := range L {
			if f(x) && (r == nil || x.e > *r) {
				v := x.e
				r = &v
			}
		}
		return r
	}
	minOf := func(f func(ev) bool) *int64 {
		var r *int64
		for _, x := range L {
			if f(x) && (r == nil || x.e < *r) {
				v := x.e
				r = &v
			}
		}
		return r
	}
	count := func(f func(ev) bool) int {
		n := 0
		for _, x := range L {
			if f(x) {
				n++
			}
		}
		return n
	}
	or1 := func(p *int64) int64 {
		if p == nil {
			return -1
		}
		return *p
	}
	lastd := maxOf(func(x ev) bool { return x.a == "skipped_disabled" })
	lastnd := maxOf(func(x ev) bool { return x.a != "skipped_disabled" })
	lastrun := maxOf(func(x ev) bool { return (x.a == "ran" || x.a == "sent" || x.a == "failed") && !x.w })
	var waitfrom *int64
	for _, p := range []*int64{lastrun, lastd} {
		if p != nil && (waitfrom == nil || *p > *waitfrom) {
			waitfrom = p
		}
	}
	s.n = len(L)
	if len(L) > 0 {
		s.lastAction = L[len(L)-1].a
		s.lastIsDisabled = L[len(L)-1].a == "skipped_disabled"
	}
	inStreak := func(x ev) bool { return x.a == "skipped_disabled" && x.e > or1(lastnd) }
	s.streakStart, s.streakCount = minOf(inStreak), count(inStreak)
	s.lastRan = maxOf(func(x ev) bool { return x.a == "ran" || x.a == "sent" })
	s.enabledSince = minOf(func(x ev) bool { return x.a != "skipped_disabled" && x.e > or1(lastd) })
	s.ranW = count(func(x ev) bool { return (x.a == "ran" || x.a == "sent") && x.e >= now-win })
	s.failedW = count(func(x ev) bool { return x.a == "failed" && !x.w && x.e >= now-win })
	s.waitW = count(func(x ev) bool { return x.w && x.e >= now-win })
	inWait := func(x ev) bool { return x.w && x.e > or1(waitfrom) }
	s.waitStart, s.waitCount = minOf(inWait), count(inWait)
	return s, true
}

// swDayOK は sw_day_ok: DAYS に今日の曜日(ISO 1..7)が入るか。
func swDayOK(days string, dnum int) bool {
	days = strings.Map(func(r rune) rune {
		switch {
		case r == ',':
			return ' '
		case r >= 'A' && r <= 'Z':
			return r + 32
		}
		return r
	}, days)
	var out []int
	for _, t := range strings.Fields(days) {
		switch {
		case t == "daily" || t == "everyday":
			out = append(out, 1, 2, 3, 4, 5, 6, 7)
		case t == "weekdays":
			out = append(out, 1, 2, 3, 4, 5)
		case t == "weekends":
			out = append(out, 6, 7)
		case strings.HasPrefix(t, "mon"):
			out = append(out, 1)
		case strings.HasPrefix(t, "tue"):
			out = append(out, 2)
		case strings.HasPrefix(t, "wed"):
			out = append(out, 3)
		case strings.HasPrefix(t, "thu"):
			out = append(out, 4)
		case strings.HasPrefix(t, "fri"):
			out = append(out, 5)
		case strings.HasPrefix(t, "sat"):
			out = append(out, 6)
		case strings.HasPrefix(t, "sun"):
			out = append(out, 7)
		}
	}
	for _, x := range out {
		if x == dnum {
			return true
		}
	}
	return false
}

var reHMS = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})(:([0-9]{2}))?$`)

// swHMS は sw_hms: "HH:MM[:SS]" → 0 時からの秒。終端で秒が無ければ :59。読めなければ -1。
func swHMS(s string, end bool) int64 {
	m := reHMS.FindStringSubmatch(s)
	if m == nil {
		return -1
	}
	sec := int64(0)
	if m[3] != "" {
		sec = int64(atoi(m[4]))
	} else if end {
		sec = 59
	}
	return int64(atoi(m[1]))*3600 + int64(atoi(m[2]))*60 + sec
}

var reCommandLine = regexp.MustCompile(`^[[:space:]]*COMMAND=(.*)$`)

// swCommandValues は sw_command_value: conf の COMMAND= の値(引用符の内側、無ければ最初の " #" の前まで)。
func (d *run) swCommandValues(e jobs.Env, job string) []string {
	body, _, ok := e.Lookup(d.db.DB, d.db.Session, job, false)
	if !ok {
		return nil
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		m := reCommandLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := m[1]
		if v != "" && (v[0] == '\'' || v[0] == '"') {
			q := v[:1]
			v = v[1:]
			if i := strings.Index(v, q); i >= 0 {
				v = v[:i]
			}
		} else {
			if i := strings.Index(v, " #"); i >= 0 {
				v = v[:i]
			}
			if i := strings.Index(v, "\t#"); i >= 0 {
				v = v[:i]
			}
		}
		out = append(out, v)
	}
	return out
}

func isWordByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}

// grepWord は grep -qwF: 語の境目で一致するか。
func grepWord(line, w string) bool {
	if w == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(line[i:], w)
		if j < 0 {
			return false
		}
		s, e := i+j, i+j+len(w)
		if (s == 0 || !isWordByte(line[s-1])) && (e == len(line) || !isWordByte(line[e])) {
			return true
		}
		i = s + 1
	}
}

// swShow は show.sh <job> の JSON(読めなければ nil)。dbPath と session はジョブを集めた DB とセッション(Web UI では呼び手の環境と違いうる)。
func swShow(e jobs.Env, logs, job, dbPath, session string) jv.Obj {
	c := exec.Command("bash", e.Show, job)
	c.Env = append(os.Environ(), "TB_DB="+dbPath, "HERDR_SOCKET_PATH=", "HERDR_SESSION="+session, "JOBS_DIR="+filepath.Join(e.Dir, "jobs"), "LOG_DIR="+logs, "SCHED_SETTINGS_DIR="+filepath.Join(e.Dir, "settings"))
	out, err := c.Output()
	if err != nil {
		return nil
	}
	vs, err := jv.DecodeAll(out)
	if err != nil || len(vs) == 0 {
		return nil
	}
	o, _ := vs[0].(jv.Obj)
	if len(vs) != 1 {
		return nil
	}
	return o
}

func swStr(o jv.Obj, k string) string { return jqStr(o.Get(k)) }

// collectSched は collect_sched_obs。
func (d *run) collectSched() error {
	if d.swCfg("enabled", "1") != "1" {
		return nil
	}
	// 置き場所は Env.SchedDir / SchedShow / SchedLogs(Web UI は自分の見ている所を入れる)。空なら tb と同じ既定
	e := jobs.DefaultEnv()
	if d.env.SchedDir != "" {
		e.Dir = d.env.SchedDir
	}
	if d.env.SchedShow != "" {
		e.Show = d.env.SchedShow
	}
	logs := filepath.Join(e.Dir, "logs")
	if d.env.SchedLogs != "" {
		logs = d.env.SchedLogs
	}
	if !agent.IsDir(logs) || !jobs.IsRegular(e.Show) {
		return nil
	}
	keys := []string{"disabled_min", "watch_stale_min", "fail_window_min", "fail_min_runs", "fail_rate_pct", "wait_max_min"}
	defs := []string{"30", "20", "60", "5", "50", "180"}
	vals := make([]int64, len(keys))
	for i, k := range keys {
		v := d.swCfg(k, defs[i])
		if !reDigits.MatchString(v) {
			return define.Errorf(define.CodeInvalidArgument, "sched-watch.conf の disabled_min / watch_stale_min / fail_window_min / fail_min_runs / fail_rate_pct / wait_max_min は 0 以上の整数")
		}
		vals[i], _ = strconv.ParseInt(v, 10, 64)
	}
	offMin, staleMin, failWin, failRuns, failPct, waitMax := vals[0], vals[1], vals[2], vals[3], vals[4], vals[5]
	now := time.Now().Unix()
	if v := os.Getenv("TB_SCHEDWATCH_NOW"); v != "" {
		now, _ = strconv.ParseInt(v, 10, 64)
	}
	watchers := strings.Fields(limits.Load(d.db.DB, d.db.Session).Get("watchers"))
	// ジョブ = DB の sched-job:<job>(SCHED_FILE_FALLBACK=1 なら jobs/*.conf との和。scheduler と同じ)
	set := map[string]bool{}
	for _, r := range strRows(d.db, `SELECT DISTINCT substr(key, 11) FROM main.config WHERE key LIKE 'sched-job:%' AND herdr_session IN (?, '*')`, d.db.Session) {
		for _, l := range strings.Split(r[0], "\n") {
			set[l] = true
		}
	}
	if jobs.FilesOK() {
		ms, _ := filepath.Glob(filepath.Join(e.Dir, "jobs", "*.conf"))
		for _, m := range ms {
			if jobs.IsRegular(m) {
				set[strings.TrimSuffix(filepath.Base(m), ".conf")] = true
			}
		}
	}
	var names []string
	for n := range set {
		if reJobName.MatchString(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var cur []string
	for _, job := range names {
		cfg := swShow(e, logs, job, d.db.Path, d.db.Session)
		if cfg == nil {
			continue
		}
		log := filepath.Join(logs, job+".log")
		if !jobs.IsRegular(log) {
			continue
		}
		sum, ok := swSummarize(log, now, failWin*60)
		if !ok {
			continue
		}
		isWatch := false
		switch swStr(cfg, "watcher") {
		case "1":
			isWatch = true
		case "0":
		default:
			cmds := d.swCommandValues(e, job)
			for _, w := range watchers {
				if swStr(cfg, "target") == w {
					isWatch = true
				}
				for _, c := range cmds {
					if grepWord(c, w) {
						isWatch = true
					}
				}
			}
		}
		if swStr(cfg, "enabled") == "false" {
			// 1. ENABLED=0 のまま続いている(ログの最後が skipped_disabled のときだけ)
			if !sum.lastIsDisabled || sum.streakStart == nil {
				continue
			}
			start := *sum.streakStart
			if now-start < offMin*60 {
				continue
			}
			key := fmt.Sprintf("schedwatch:off:%s:%d", job, start)
			cur = append(cur, key)
			if !d.seen(key) {
				extra := ""
				if isWatch {
					extra = "。見張り系のジョブで、見回りが動いていない"
				}
				d.add(d.dt, "obs", "sched", key, "", "schedoff", "scheduler 観測 / "+job,
					fmt.Sprintf("ジョブ %s が ENABLED=0 のまま %d 分(skipped_disabled %d 回、その間の実行 0 回)%s。有効にするのは人(scheduler の settings か conf)", job, (now-start)/60, sum.streakCount, extra))
			}
			continue
		}
		// 2. 見張り系: 有効で窓の中なのに ran が無い
		if isWatch {
			tz := "Asia/Tokyo"
			if v := cfg.Get("tz"); v != nil && v != false {
				tz = jv.Scalar(v)
			}
			loc, err := time.LoadLocation(tz)
			if err != nil {
				loc = time.UTC
			}
			t := time.Unix(now, 0).In(loc)
			dnum := int(t.Weekday())
			if dnum == 0 {
				dnum = 7
			}
			sec := int64(t.Hour()*3600 + t.Minute()*60 + t.Second())
			ws, we := swHMS(swStr(cfg, "start"), false), swHMS(swStr(cfg, "end"), true)
			if swDayOK(swStr(cfg, "days"), dnum) && ws >= 0 && we >= 0 && sec >= ws+staleMin*60 && sec <= we {
				ref := sum.lastRan
				if ref == nil {
					ref = sum.enabledSince
				}
				if ref != nil && now-*ref >= staleMin*60 {
					key := fmt.Sprintf("schedwatch:stale:%s:%d", job, *ref)
					cur = append(cur, key)
					if !d.seen(key) {
						how := "有効になってから ran が 1 回も無く、"
						if sum.lastRan != nil && *sum.lastRan == *ref {
							how = "最後の ran から"
						}
						d.add(d.dt, "obs", "sched", key, "", "schedstale", "scheduler 観測 / "+job,
							fmt.Sprintf("見張り系ジョブ %s の ran が %d 分無い(%s。直近の記録は %s。scheduler/logs/%s.log を確認)", job, (now-*ref)/60, how, jqStr(sum.lastAction), job))
					}
				}
			}
		}
		// 3. 失敗が多い(exit 10 の待ちは分子にも分母にも入れない。回数は本文に並べる)
		failed := int64(sum.failedW)
		rans := int64(sum.ranW) + failed
		waits := sum.waitW
		if rans > 0 && rans >= failRuns && failed*100 >= failPct*rans {
			key := "schedwatch:fail:" + job
			cur = append(cur, key)
			if !d.seen(key) {
				extra := ""
				if waits > 0 {
					extra = fmt.Sprintf("。ほかに宛先の作業中で待った回(exit 10)が %d 回あり、これは数えていない", waits)
				}
				d.add(d.dt, "obs", "sched", key, "", "schedfail", "scheduler 観測 / "+job,
					fmt.Sprintf("ジョブ %s の失敗が多い(直近 %d 分で %d 回実行して failed %d 回 = %d%%。しきい値 %d%%%s。scheduler/logs/%s.log を確認)", job, failWin, rans, failed, failed*100/rans, failPct, extra, job))
			}
		}
		// 4. 宛先の作業中(exit 10)で待ったままの時間が長い
		if waitMax > 0 && sum.waitStart != nil && now-*sum.waitStart >= waitMax*60 {
			ws := *sum.waitStart
			key := fmt.Sprintf("schedwatch:wait:%s:%d", job, ws)
			cur = append(cur, key)
			if !d.seen(key) {
				d.add(d.dt, "obs", "sched", key, "", "schedwait", "scheduler 観測 / "+job,
					fmt.Sprintf("ジョブ %s が、宛先の作業中(exit 10)で %d 分送れていない(その間 %d 回とも待ち)。宛先が止まっていないか確認。scheduler/logs/%s.log を確認", job, (now-ws)/60, sum.waitCount, job))
			}
		}
	}
	// 条件が外れたものは記録を消す(再び当てはまったら、また送る)
	d.prune(`DELETE FROM main.observations WHERE herdr_session=? AND kind IN ('schedoff','schedstale','schedfail','schedwait') AND key NOT IN (`+sqlQuoteList(cur)+`)`, d.db.Session)
	return nil
}

var reJobName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
