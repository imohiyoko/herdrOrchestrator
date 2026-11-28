package webui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	appconfig "github.com/imohiyoko/herdrorchestrator/internal/app/config"
	"github.com/imohiyoko/herdrorchestrator/internal/app/job"
	"github.com/imohiyoko/herdrorchestrator/internal/sched"
)

// jobs.go は scheduler のジョブ定義と、Web UI から書く上書き設定(DB の sched-settings:<job>)。

var (
	reHM         = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9](?::[0-5][0-9])?$`)
	dayTokens    = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	dayPresets   = [][2]string{{"daily", "毎日"}, {"weekdays", "平日"}, {"weekends", "土日"}}
	intervalUnit = []struct {
		u, label string
		hi       int
	}{{"min", "分", 1440}, {"sec", "秒", 86400}}
)

const confMaxDB = 262144

func schedRoot() string { return filepath.Join(HerdrRoot(), "scheduler") }
func schedJobs() string { return cfg("WEBUI_SCHED_JOBS", filepath.Join(schedRoot(), "jobs")) }
func schedSettings() string {
	return cfg("WEBUI_SCHED_SETTINGS", filepath.Join(schedRoot(), "settings"))
}

// filesOK はファイル(jobs/*.conf・settings/*.settings)も読むか(scheduler と同じく SCHED_FILE_FALLBACK=1 のときだけ)。
func filesOK() bool { return os.Getenv("SCHED_FILE_FALLBACK") == "1" }

func (v *sv) dbJobs() map[string]bool {
	out := map[string]bool{}
	for _, r := range appconfig.JobNames(v.qChecked, v.sess) {
		if n, ok := r["name"].(string); ok && JobRE.MatchString(n) {
			out[n] = true
		}
	}
	return out
}

// dbBody は DB の本文(このセッション > 共通)。無ければ false。
func (v *sv) dbBody(key string) (string, bool) {
	rows := appconfig.Body(v.qChecked, key, v.sess)
	if len(rows) == 0 || rows[0]["body"] == nil {
		return "", false
	}
	return runesUpTo(pyStr(rows[0]["body"]), confMaxDB+1), true
}

func runesUpTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func fileJobs() map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(schedJobs())
	if err != nil {
		return out
	}
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".conf"); ok && JobRE.MatchString(n) {
			out[n] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jobNames はジョブ名。DB(このセッション > 共通)。SCHED_FILE_FALLBACK=1 なら jobs/*.conf との和。
func (v *sv) jobNames() []string {
	m := v.dbJobs()
	if filesOK() {
		for k := range fileJobs() {
			m[k] = true
		}
	}
	return sortedKeys(m)
}

// unimportedJobs は DB に取り込んでいない jobs/*.conf のジョブ名。
func (v *sv) unimportedJobs() []string {
	db := v.dbJobs()
	m := map[string]bool{}
	for k := range fileJobs() {
		if !db[k] {
			m[k] = true
		}
	}
	return sortedKeys(m)
}

// jobInfo は scheduler の show と同じ「実際に効く設定(定義 + 上書き設定)」。SCHED_* の環境変数の上書きは見ない。
type jobInfo struct {
	kind, target, start, end, days, tz, settingsSource string
	intervalSec, intervalMin                           int // intervalMin は 60 で割り切れないとき -1
	enabled, overridden                                bool
	at                                                 string // 1 回だけのジョブの日時(AT。繰り返しなら空)
	manual                                             bool   // MANUAL=1(呼ばれたときだけ)
}

func (v *sv) jobInfo(name string) (*jobInfo, string) {
	e := sched.Env{Root: schedRoot(), JobsDir: schedJobs(), SettingsDir: schedSettings(), DB: TBDB(), Session: v.sess,
		FilesOK: filesOK(), NoEnvOverride: true}
	j, err := e.Load(name)
	if err == nil {
		_, err = sched.DaysToNums(j.Name, j.Days)
	}
	if err != nil {
		msg := err.Error()
		var d *sched.Die
		if errors.As(err, &d) {
			msg = d.Msg
		}
		ls := pySplitlines(pyStrip("error: " + msg))
		return nil, Clean(ls[len(ls)-1])
	}
	kind := "prompt"
	if j.Command != "" {
		kind = "command"
	}
	return &jobInfo{kind: kind, target: j.Target, start: j.Start, end: j.End, days: j.Days, tz: j.TZ, settingsSource: j.SettingsSrc,
		intervalSec: j.IntervalSec, intervalMin: j.IntervalMin, enabled: j.Enabled == "1", overridden: j.SettingsSrc != "none", at: j.At, manual: j.Manual}, ""
}

func intervalText(info *jobInfo) string {
	if info.intervalSec%60 == 0 {
		return strconv.Itoa(info.intervalSec/60) + " 分"
	}
	return strconv.Itoa(info.intervalSec) + " 秒"
}

// jobLast は (最後の記録, 最後の実行(ran / failed / sent))。
func jobLast(name string) (map[string]any, map[string]any) {
	recs := parseJSONL(tailLines(filepath.Join(SchedLogs(), name+".log"), 200))
	var last, ran map[string]any
	for _, r := range recs {
		last = r
		if a, ok := r["action"].(string); ok && (a == "ran" || a == "failed" || a == "sent") {
			ran = r
		}
	}
	return last, ran
}

// jobCall はジョブの操作(job.set / job.unset / job.rm)を人として呼ぶ。断られたら画面に出す理由(実行できたら "")。
func (v *sv) jobCall(op string, in any, out *job.Result) string {
	return callEdit(v.sess, op, in, out, &out.Pending)
}

// jobRunTimeout は Web UI の「今すぐ流す」の上限(app/job の runTimeout と同じ)。
const jobRunTimeout = 15 * time.Minute

// startTBJobRun は tb job run <job> を裏で動かし、終わるのを待たずに戻る(ジョブは長くかかりうる)。
// 結果は scheduler のログ(ジョブ画面の「最近の実行」)に残る。Web UI のログには始めたことと終わりの終了コードを書く。
func (s *Server) startTBJobRun(sess, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), jobRunTimeout)
	c := jobTBCmd(ctx, sess, []string{"job", "run", name})
	se := &capBuf{max: 4096} // scheduler に届く前に tb が断ったとき(herdrctl が無いなど)の理由。ジョブのログには残らない
	c.Stderr = se
	if err := c.Start(); err != nil {
		cancel()
		return err
	}
	go func() {
		defer cancel()
		err := c.Wait()
		why := ""
		if err != nil {
			m, _ := mask(strings.ToValidUTF8(se.String(), "\uFFFD"))
			why = " " + oneLine(m, 300)
		}
		s.logf("jobs: run %s done (%v)%s\n", name, err, why)
	}()
	return nil
}

// jobTBCmd は tb job を Web UI と同じ jobs/・settings/・ログ・DB を見る形で組む(人の操作として。HERDR_PANE_ID は外す)。
func jobTBCmd(ctx context.Context, sess string, args []string) *exec.Cmd {
	c := exec.CommandContext(ctx, "bash", append([]string{tbPath()}, args...)...)
	// tb job が見る jobs/・settings/ を、Web UI が見ているものに揃える
	jobs, _ := filepath.Abs(schedJobs())
	sets, _ := filepath.Abs(schedSettings())
	schedDir := ""
	if filepath.Base(jobs) == "jobs" && filepath.Base(sets) == "settings" && filepath.Dir(jobs) == filepath.Dir(sets) {
		schedDir = filepath.Dir(jobs)
	}
	var env []string
	for _, kv := range childEnv(sess) {
		if k, _, _ := strings.Cut(kv, "="); k == "HERDR_PANE_ID" || k == "TB_DB" || k == "LOG_DIR" || (k == "TB_SCHED_DIR" && schedDir != "") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "TB_DB="+TBDB())
	if logs, err := filepath.Abs(SchedLogs()); err == nil { // tb job rm が消すログを、Web UI が読んでいるものに揃える
		env = append(env, "LOG_DIR="+logs)
	}
	if schedDir != "" {
		env = append(env, "TB_SCHED_DIR="+schedDir)
	}
	c.Env = env
	return c
}

type form = map[string]string

// settingsBody はフォームを検証して settings の本文にする。(本文, エラー文)。
func settingsBody(f form) (string, string) {
	iv, ok := f["interval"]
	if !ok {
		iv = f["interval_min"]
	}
	iv = pyStrip(iv)
	unit, ok := f["interval_unit"]
	if !ok {
		unit = "min"
	}
	label, hi := "", 0
	for _, u := range intervalUnit {
		if u.u == unit {
			label, hi = u.label, u.hi
		}
	}
	if hi == 0 {
		return "", "間隔の単位が不正です"
	}
	n, err := strconv.Atoi(iv)
	if !isDecimal(iv) || err != nil || n < 1 || n > hi {
		return "", fmt.Sprintf("間隔は 1〜%d %sの整数で入力してください", hi, label)
	}
	start, end := pyStrip(f["start"]), pyStrip(f["end"])
	if !reHM.MatchString(start) || !reHM.MatchString(end) {
		return "", "開始・終了は HH:MM か HH:MM:SS で入力してください"
	}
	s, _ := sched.HMS(start, false)
	e, _ := sched.HMS(end, true)
	if s > e {
		return "", "開始は終了より前にしてください(日またぎは未対応)"
	}
	days := f["days"]
	if days == "custom" {
		var picked []string
		for _, t := range dayTokens {
			if f["day_"+t] == "1" {
				picked = append(picked, strings.ToUpper(t[:1])+t[1:])
			}
		}
		if len(picked) == 0 {
			return "", "曜日を 1 つ以上選んでください"
		}
		days = strings.Join(picked, " ")
	} else if !isPreset(days) {
		return "", "曜日の指定が不正です"
	}
	enabled := "0"
	if f["enabled"] == "1" {
		enabled = "1"
	}
	key := "INTERVAL_MIN"
	if unit == "sec" {
		key = "INTERVAL_SEC"
	}
	return "# Web UI が書く上書き設定(" + time.Now().Format("2006-01-02T15:04:05-0700") + ")\n" +
		key + "=" + strconv.Itoa(n) + "\nSTART=" + start + "\nEND=" + end + "\nDAYS=" + days + "\nENABLED=" + enabled + "\n", ""
}

func isPreset(days string) bool {
	for _, p := range dayPresets {
		if p[0] == days {
			return true
		}
	}
	return false
}

// saveJobSettings はフォームを検証して上書き設定を DB(sched-settings:<job>。このセッションの行)に tb job で書く。エラー文か ""。
func (v *sv) saveJobSettings(name string, f form) string {
	if f["action"] == "reset" {
		if _, ok := v.dbBody("sched-settings:" + name); ok {
			e := v.jobCall("job.unset", job.UnsetReq{Name: name, Settings: true}, &job.Result{})
			// 共通の行(*)は、このセッションからは消さない(ほかのセッションにも効くため)
			if _, still := v.dbBody("sched-settings:" + name); e != "" || still {
				return "上書き設定を戻せませんでした(共通の行は tb job unset " + name + " --settings --shared で人が消します)"
			}
		}
		if e := removeSettingsFile(name); e != "" {
			return e
		}
		return v.verifyApplied(name, f)
	}
	body, e := settingsBody(f)
	if e != "" {
		return e
	}
	var r job.Result
	if e := v.jobCall("job.set", job.SetReq{Name: name, Body: strings.TrimRight(body, "\n"), Settings: true, How: "set"}, &r); e != "" {
		return "scheduler が設定を受け付けませんでした: " + runesUpTo(strings.TrimPrefix(e, "断りました: "), 300)
	}
	if r.Rejected != "" {
		return "scheduler が設定を受け付けませんでした: scheduler がこの本文を受け付けません: " + runesUpTo(Clean(r.Rejected), 300)
	}
	if e := removeSettingsFile(name); e != "" {
		return e
	}
	return v.verifyApplied(name, f)
}

// removeSettingsFile は settings/<job>.settings(DB に移す前の置き場所)が残っていれば消す。
func removeSettingsFile(name string) string {
	p := filepath.Join(schedSettings(), name+".settings")
	if st, err := os.Lstat(p); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return "settings/" + name + ".settings がシンボリックリンクです。手で確認してください"
		}
		if err := os.Remove(p); err != nil {
			return "設定を書き込めませんでした"
		}
	}
	return ""
}

// verifyApplied は scheduler が読む設定が、いま書いた値になったか。
func (v *sv) verifyApplied(name string, f form) string {
	info, err := v.jobInfo(name)
	if err != "" {
		return "保存しましたが、scheduler の読む設定を確かめられませんでした: " + err
	}
	where := map[string]string{"db": "DB の sched-settings:" + name, "file": "settings/" + name + ".settings", "none": "conf の値"}[info.settingsSource]
	if where == "" {
		where = "?"
	}
	if f["action"] == "reset" {
		if info.overridden {
			return "conf の値に戻りませんでした(scheduler は " + where + " を読んでいます)"
		}
		return ""
	}
	body, _ := settingsBody(f)
	want := map[string]string{}
	for _, ln := range pySplitlines(body) {
		if k, val, ok := strings.Cut(ln, "="); ok && !strings.HasPrefix(ln, "#") {
			want[k] = val
		}
	}
	iv, _ := strconv.Atoi(want["INTERVAL_SEC"])
	if iv == 0 {
		m, _ := strconv.Atoi(want["INTERVAL_MIN"])
		iv = m * 60
	}
	en := "0"
	if info.enabled {
		en = "1"
	}
	if info.intervalSec != iv || info.start != want["START"] || info.end != want["END"] || info.days != want["DAYS"] || en != want["ENABLED"] {
		return "保存しましたが、scheduler の読む設定に反映されていません(scheduler は " + where + " を読んでいます)"
	}
	return ""
}

// oneLine は改行と連続した空白を 1 つの空白にし、n 字(rune)で切る(ログの 1 行用)。
func oneLine(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// capBuf は最初の max バイトだけ持ち、残りは読み捨てる io.Writer(子の標準エラーで Web UI のメモリが増えないように)。
type capBuf struct {
	bytes.Buffer
	max int
}

func (b *capBuf) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
