package sched

import (
	"database/sql"

	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
)

// Die は lib.sh の die(error: … を出して終了コード 2)。
type Die struct{ Msg string }

func (d *Die) Error() string { return d.Msg }

func die(format string, a ...any) error { return &Die{fmt.Sprintf(format, a...)} }

// Env は置き場所(lib.sh の SCHED_ROOT / JOBS_DIR / SETTINGS_DIR / SCHED_DB)。
type Env struct {
	Root, JobsDir, SettingsDir, DB, Session string
	FilesOK                                 bool
	NoEnvOverride                           bool              // SCHED_WINDOW などの環境変数の上書きを見ない(Web UI が設定そのものを見せるとき)
	Bodies                                  map[string]string // DB より先に引く本文(キー → 本文)。sched lint が標準入力の定義を読ませる
}

// DefaultEnv は lib.sh と同じ既定。SCHED_ROOT は scheduler のディレクトリ(scheduler/bin の show.sh などが渡す)。
func DefaultEnv() Env {
	root := os.Getenv("SCHED_ROOT")
	if root == "" {
		root = "."
	}
	e := Env{Root: root, JobsDir: os.Getenv("JOBS_DIR"), SettingsDir: os.Getenv("SCHED_SETTINGS_DIR"), DB: env.TBDB(),
		Session: env.SessionName(), FilesOK: os.Getenv("SCHED_FILE_FALLBACK") == "1"}
	if e.JobsDir == "" {
		e.JobsDir = filepath.Join(root, "jobs")
	}
	if e.SettingsDir == "" {
		e.SettingsDir = filepath.Join(root, "settings")
	}
	return e
}

// dbBody は sched_db_body: DB のこのセッションの行 > 共通の行(*)。DB が無い・読めない・本文が NULL なら無い。
func (e Env) dbBody(key string) (string, bool) {
	if b, ok := e.Bodies[key]; ok {
		return strings.TrimRight(b, "\n"), true // 空でも渡された本文として読む(ファイルへ戻らない)
	}
	if st, err := os.Stat(e.DB); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	db, err := sql.Open("sqlite", e.DB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return "", false
	}
	defer db.Close()
	var b sql.NullString
	err = db.QueryRow(`SELECT 'x' || body FROM config WHERE key=? AND herdr_session IN (?, '*') ORDER BY herdr_session='*' LIMIT 1`, key, e.Session).Scan(&b)
	if err != nil || !b.Valid || b.String == "" {
		return "", false
	}
	return strings.TrimRight(strings.TrimPrefix(b.String, "x"), "\n"), true
}

func (e Env) missingMsg(job string) string {
	m := fmt.Sprintf("ジョブ定義が DB に無い: sched-job:%s(セッション %s)", job, e.Session)
	if _, err := os.Stat(e.DB); err != nil {
		m += "。DB が無い: " + e.DB
	}
	if isRegular(filepath.Join(e.JobsDir, job+".conf")) {
		m += "。jobs/" + job + ".conf は DB に取り込むまで使われない: taskboard/bin/tb job import " + job
	}
	return m
}

func isRegular(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isLink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}
func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Rule は PROMPT_RULES の 1 行(条件の語と本文)。Var は PROMPT_VARS の 1 行。
type Rule struct {
	Cond []string
	Body string
}
type Var struct{ Name, Cmd string }

// Job は load_job の結果(既定を埋め、検証したもの)。
type Job struct {
	Name, Src, Conf, SettingsSrc                                    string
	Target, Prompt, PromptFile, PromptCmd, Command, Gate            string
	Start, End, Window, Days, SkipIf, TZ, Enabled, Watcher, Owner   string
	IntervalSec, IntervalMin                                        int // IntervalMin は 60 で割り切れないとき -1
	CommandTimeout, GateTimeout, PromptCmdTimeout, PromptVarTimeout int
	WinStart, WinEnd                                                int
	Rules                                                           []Rule
	Vars                                                            []Var
	DayNums                                                         []int
	// At は 1 回だけ動かす時刻(AT=YYYY-MM-DD HH:MM[:SS]。TZ の時刻)。あれば WINDOW / START / END / INTERVAL_* / DAYS は書かない
	At string
	// Manual は MANUAL=1: 時刻では動かず、呼ばれたときだけ動く(tb job run / POST /v1/jobs/{name}/run)
	Manual bool
}

var (
	reJobName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	reWindow   = regexp.MustCompile(`^([0-9:]+)-([0-9:]+)$`)
	reDigits   = regexp.MustCompile(`^[0-9]+$`)
	reHMS      = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})(:([0-9]{2}))?$`)
	reAt       = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2}(:[0-9]{2})?$`)
	reSetLine  = regexp.MustCompile(`^([A-Z_]+)=(.*)$`)
	reSetMin   = regexp.MustCompile(`^[0-9]{1,4}$`)
	reSetSec   = regexp.MustCompile(`^[0-9]{1,5}$`)
	reSetTime  = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}(:[0-9]{2})?$`)
	reSetDays  = regexp.MustCompile(`^[A-Za-z][A-Za-z ,]{0,39}$`)
	re01       = regexp.MustCompile(`^[01]$`)
	reOwner    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	reRuleTok  = regexp.MustCompile(`^([a-z]+)=(.+)$`)
	reStep     = regexp.MustCompile(`^\*/([0-9]{1,2})$`)
	reRange    = regexp.MustCompile(`^([0-9]{1,2})-([0-9]{1,2})$`)
	reNum2     = regexp.MustCompile(`^[0-9]{1,2}$`)
	reVarName  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	reservedVr = map[string]bool{"job": true, "target": true, "at": true, "date": true, "time": true, "hour": true, "min": true, "sec": true, "dow": true}
)

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimLeft(s, "0")); return n }

// HMS は hms_to_sec。end なら秒を省いた HH:MM はその分の 59 秒まで。
func HMS(s string, end bool) (int, error) {
	m := reHMS.FindStringSubmatch(s)
	if m == nil {
		return 0, die("時刻の形式が不正: %s (HH:MM か HH:MM:SS)", s)
	}
	h, mi, sec := atoi(m[1]), atoi(m[2]), 0
	if m[3] != "" {
		sec = atoi(m[4])
	} else if end {
		sec = 59
	}
	if h >= 24 || mi >= 60 || sec >= 60 {
		return 0, die("時刻が範囲外: %s", s)
	}
	return h*3600 + mi*60 + sec, nil
}

// intervalOf は interval_of(<場所> <分> <秒>)。両方空なら -1
func intervalOf(job, where, min, sec string) (int, error) {
	if min != "" && sec != "" {
		return 0, die("%s: %s に INTERVAL_MIN と INTERVAL_SEC を同時に書かない", job, where)
	}
	if min != "" {
		if !reDigits.MatchString(min) || atoi(min) <= 0 {
			return 0, die("%s: INTERVAL_MIN は正の整数", job)
		}
		return atoi(min) * 60, nil
	}
	if sec != "" {
		if !reDigits.MatchString(sec) || atoi(sec) <= 0 {
			return 0, die("%s: INTERVAL_SEC は正の整数", job)
		}
		return atoi(sec), nil
	}
	return -1, nil
}

// DaysToNums は days_to_nums(ISO 曜日番号。1=Mon..7=Sun)。
func DaysToNums(job, days string) ([]int, error) {
	var out []int
	s := strings.Map(func(r rune) rune {
		if r == ',' {
			return ' '
		}
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, days)
	for _, t := range strings.Fields(s) {
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
		default:
			return nil, die("%s: DAYS の指定が不正: %s", job, t)
		}
	}
	return out, nil
}

// bodyLines は bash の while IFS= read -r line(本文の後ろに改行を 1 つ足したヒアドキュメント)と同じ行に分ける。
func bodyLines(b string) []string {
	return strings.Split(strings.TrimSuffix(b+"\n", "\n"), "\n")
}

func trimLeftSpace(s string) string  { return strings.TrimLeft(s, " \t\n\v\f\r") }
func trimRightSpace(s string) string { return strings.TrimRight(s, " \t\n\v\f\r") }

type settings struct{ min, sec, start, end, days, enabled, src string }

// loadSettings は load_settings。
func (e Env) loadSettings(job string) (settings, error) {
	s := settings{src: "none"}
	f := filepath.Join(e.SettingsDir, job+".settings")
	body, ok := e.dbBody("sched-settings:" + job)
	if ok {
		s.src, f = "db", "DB の sched-settings:"+job
	} else {
		if !e.FilesOK {
			return s, nil
		}
		if !exists(f) && !isLink(f) {
			return s, nil
		}
		if !isRegular(f) || isLink(f) {
			return s, die("%s: settings が通常ファイルではない: %s", job, f)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return s, die("%s: settings を読めない: %s", job, f)
		}
		body, s.src = strings.TrimRight(string(b), "\n"), "file"
	}
	for _, line := range bodyLines(body) {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := reSetLine.FindStringSubmatch(line)
		if m == nil {
			return s, die("%s: settings の行が不正: %s", job, f)
		}
		k, v := m[1], m[2]
		switch k {
		case "INTERVAL_MIN":
			if !reSetMin.MatchString(v) {
				return s, die("%s: settings の INTERVAL_MIN が不正", job)
			}
			s.min = v
		case "INTERVAL_SEC":
			if !reSetSec.MatchString(v) {
				return s, die("%s: settings の INTERVAL_SEC が不正", job)
			}
			s.sec = v
		case "START", "END":
			if !reSetTime.MatchString(v) {
				return s, die("%s: settings の %s が不正", job, k)
			}
			if k == "START" {
				s.start = v
			} else {
				s.end = v
			}
		case "DAYS":
			if !reSetDays.MatchString(v) {
				return s, die("%s: settings の DAYS が不正", job)
			}
			s.days = v
		case "ENABLED":
			if !re01.MatchString(v) {
				return s, die("%s: settings の ENABLED は 0 か 1", job)
			}
			s.enabled = v
		default:
			return s, die("%s: settings に使えないキー: %s", job, k)
		}
	}
	if (s.start != "" || s.end != "") && (s.start == "" || s.end == "") {
		return s, die("%s: settings の START と END は両方書く", job)
	}
	return s, nil
}

func positive(job, key, v string) (int, error) {
	if !reDigits.MatchString(v) || atoi(v) <= 0 {
		return 0, die("%s: %s は正の整数(秒)", job, key)
	}
	return atoi(v), nil
}

// Load は load_job: 定義(sched-job:<job>)< 上書き設定(sched-settings:<job>)< 環境変数 SCHED_* の順で読み、既定を埋めて検証する。
func (e Env) Load(name string) (*Job, error) {
	if !reJobName.MatchString(name) {
		return nil, die("ジョブ名が不正: '%s'", name)
	}
	j := &Job{Name: name, Conf: filepath.Join(e.JobsDir, name+".conf")}
	body, ok := e.dbBody("sched-job:" + name)
	if ok {
		j.Src, j.Conf = "db", "db:sched-job:"+name
	} else {
		if !e.FilesOK {
			return nil, die("%s", e.missingMsg(name))
		}
		j.Src = "file"
		if !isRegular(j.Conf) {
			return nil, die("ジョブ定義がない: %s", j.Conf)
		}
		b, err := os.ReadFile(j.Conf)
		if err != nil {
			return nil, die("ジョブ定義を読めない: %s", j.Conf)
		}
		body = string(b)
	}
	c, err := ParseConf(body)
	if err != nil {
		return nil, die("%s: ジョブ定義の書き方が不正(%s)。KEY=値 だけを書き、展開($ や `)は使わない", name, err)
	}
	switch c["MANUAL"] {
	case "", "0":
	case "1":
		if c["AT"] != "" {
			return nil, die("%s: MANUAL=1(呼ばれたときだけ)と AT は同時に書かない", name)
		}
		for _, k := range []string{"WINDOW", "START", "END", "INTERVAL_MIN", "INTERVAL_SEC", "DAYS"} {
			if c[k] != "" {
				return nil, die("%s: MANUAL=1(呼ばれたときだけ)と %s(繰り返し)は同時に書かない", name, k)
			}
		}
		j.Manual = true
	default:
		return nil, die("%s: MANUAL は 0 か 1", name)
	}
	if at := c["AT"]; at != "" {
		if !reAt.MatchString(at) {
			return nil, die("%s: AT は YYYY-MM-DD HH:MM[:SS](TZ の時刻)", name)
		}
		if _, err := ParseAt(at, time.UTC); err != nil {
			return nil, die("%s: AT の日時が不正: %s", name, at)
		}
		for _, k := range []string{"WINDOW", "START", "END", "INTERVAL_MIN", "INTERVAL_SEC", "DAYS"} {
			if c[k] != "" {
				return nil, die("%s: AT(1 回だけ)と %s(繰り返し)は同時に書かない", name, k)
			}
		}
		j.At = strings.Replace(at, "T", " ", 1)
	}
	start, end := c["START"], c["END"]
	if w := c["WINDOW"]; w != "" && start+end == "" {
		m := reWindow.FindStringSubmatch(w)
		if m == nil {
			return nil, die("%s: WINDOW は HH:MM-HH:MM", name)
		}
		start, end = m[1], m[2]
	}
	s, err := e.loadSettings(name)
	if err != nil {
		return nil, err
	}
	j.SettingsSrc = s.src
	if (j.At != "" || j.Manual) && s.min+s.sec+s.start+s.end+s.days != "" {
		return nil, die("%s: AT・MANUAL のジョブの settings に書けるのは ENABLED だけ", name)
	}
	if s.start != "" {
		start, end = s.start, s.end
	}
	iv, err := intervalOf(name, "conf", c["INTERVAL_MIN"], c["INTERVAL_SEC"])
	if err != nil {
		return nil, err
	}
	if v, err := intervalOf(name, "settings", s.min, s.sec); err != nil {
		return nil, err
	} else if v >= 0 {
		iv = v
	}
	j.Days = firstNonEmpty(s.days, c["DAYS"])
	j.Enabled = firstNonEmpty(s.enabled, c["ENABLED"], "1")
	if !re01.MatchString(j.Enabled) {
		return nil, die("%s: ENABLED は 0 か 1", name)
	}
	j.Watcher = c["WATCHER"]
	if j.Watcher != "" && !re01.MatchString(j.Watcher) {
		return nil, die("%s: WATCHER は 0 か 1", name)
	}
	j.Command, j.Owner = c["COMMAND"], c["OWNER"]
	if j.Owner != "" {
		if j.Command == "" {
			return nil, die("%s: OWNER はコマンド型(COMMAND)のジョブにだけ書ける", name)
		}
		if !reOwner.MatchString(j.Owner) || j.Owner == "human" {
			return nil, die("%s: OWNER は agent 名(英小文字で始まる 32 字以内。human は不可): %s", name, j.Owner)
		}
	}
	getenv := os.Getenv
	if e.NoEnvOverride {
		getenv = func(string) string { return "" }
	}
	if j.At != "" || j.Manual {
		getenv = func(string) string { return "" } // 繰り返しの上書き(SCHED_WINDOW など)は AT・MANUAL のジョブに効かない
		start, end, iv = "00:00", "23:59", 60
	}
	if w := getenv("SCHED_WINDOW"); w != "" {
		m := reWindow.FindStringSubmatch(w)
		if m == nil {
			return nil, die("SCHED_WINDOW は HH:MM-HH:MM")
		}
		start, end = m[1], m[2]
	}
	start, end = firstNonEmpty(getenv("SCHED_START"), start), firstNonEmpty(getenv("SCHED_END"), end)
	if v, err := intervalOf(name, "SCHED_*", getenv("SCHED_INTERVAL_MIN"), getenv("SCHED_INTERVAL_SEC")); err != nil {
		return nil, err
	} else if v >= 0 {
		iv = v
	}
	if start == "" || end == "" {
		return nil, die("%s: 開始・終了時刻が未設定(WINDOW または START と END)", name)
	}
	j.Start, j.End, j.Window = start, end, start+"-"+end
	j.Days = firstNonEmpty(j.Days, "daily")
	j.SkipIf = firstNonEmpty(c["SKIP_IF"], "working blocked")
	j.TZ = firstNonEmpty(c["TZ"], "Asia/Tokyo")
	if j.CommandTimeout, err = positive(name, "COMMAND_TIMEOUT", firstNonEmpty(c["COMMAND_TIMEOUT"], "60")); err != nil {
		return nil, err
	}
	j.Gate = c["GATE"]
	if j.GateTimeout, err = positive(name, "GATE_TIMEOUT", firstNonEmpty(c["GATE_TIMEOUT"], "10")); err != nil {
		return nil, err
	}
	rules, vars := c["PROMPT_RULES"], c["PROMPT_VARS"]
	j.PromptCmd = c["PROMPT_CMD"]
	if j.PromptCmdTimeout, err = positive(name, "PROMPT_CMD_TIMEOUT", firstNonEmpty(c["PROMPT_CMD_TIMEOUT"], "10")); err != nil {
		return nil, err
	}
	if j.PromptVarTimeout, err = positive(name, "PROMPT_VAR_TIMEOUT", firstNonEmpty(c["PROMPT_VAR_TIMEOUT"], "30")); err != nil {
		return nil, err
	}
	j.Target, j.Prompt, j.PromptFile = c["TARGET"], c["PROMPT"], c["PROMPT_FILE"]
	if j.Command != "" {
		if j.Target+j.Prompt+j.PromptFile+rules+j.PromptCmd+vars != "" {
			return nil, die("%s: COMMAND は TARGET/PROMPT/PROMPT_FILE/PROMPT_RULES/PROMPT_CMD/PROMPT_VARS と同時に指定できない", name)
		}
	} else if j.Target == "" {
		return nil, die("%s: TARGET が未設定(または COMMAND を指定)", name)
	}
	if j.Command+j.Prompt+j.PromptFile+rules+j.PromptCmd == "" {
		return nil, die("%s: PROMPT か PROMPT_FILE(または PROMPT_RULES / PROMPT_CMD)が必要", name)
	}
	if j.Rules, err = parseRules(name, rules); err != nil {
		return nil, err
	}
	if j.Vars, err = parseVars(name, vars); err != nil {
		return nil, err
	}
	if iv < 0 {
		return nil, die("%s: 間隔が未設定(INTERVAL_MIN か INTERVAL_SEC)", name)
	}
	j.IntervalSec, j.IntervalMin = iv, -1
	if iv%60 == 0 {
		j.IntervalMin = iv / 60
	}
	m := reWindow.FindStringSubmatch(j.Window)
	if m == nil {
		return nil, die("%s: WINDOW は HH:MM-HH:MM", name)
	}
	if j.WinStart, err = HMS(m[1], false); err != nil {
		return nil, err
	}
	if j.WinEnd, err = HMS(m[2], true); err != nil {
		return nil, err
	}
	if j.WinStart > j.WinEnd {
		return nil, die("%s: 日をまたぐ WINDOW は未対応", name)
	}
	return j, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ruleSpecOK は rule_spec_ok。
func ruleSpecOK(job, key, spec string) bool {
	max := 0
	switch key {
	case "min", "sec":
		max = 59
	case "hour":
		max = 23
	case "days":
		_, err := DaysToNums(job, spec)
		return err == nil
	default:
		return false
	}
	if spec == "" || strings.HasPrefix(spec, ",") || strings.HasSuffix(spec, ",") || strings.Contains(spec, ",,") {
		return false
	}
	for _, item := range strings.Split(spec, ",") {
		switch {
		case item == "*":
		case reStep.MatchString(item):
			if atoi(reStep.FindStringSubmatch(item)[1]) <= 0 {
				return false
			}
		case reRange.MatchString(item):
			m := reRange.FindStringSubmatch(item)
			if a, b := atoi(m[1]), atoi(m[2]); a > b || b > max {
				return false
			}
		case reNum2.MatchString(item):
			if atoi(item) > max {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// parseRules は parse_prompt_rules。
func parseRules(job, body string) ([]Rule, error) {
	if body == "" {
		return nil, nil
	}
	var out []Rule
	for _, line := range bodyLines(body) {
		line = trimLeftSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cond, b, ok := strings.Cut(line, "|")
		if !ok {
			return nil, die("%s: PROMPT_RULES の行に | が無い(<条件> | <本文>): %s", job, line)
		}
		b = trimLeftSpace(b)
		if b == "" {
			return nil, die("%s: PROMPT_RULES の本文が空: %s", job, line)
		}
		toks := strings.Fields(cond)
		if len(toks) == 0 {
			return nil, die("%s: PROMPT_RULES の条件が空(常に合わせるなら *): %s", job, line)
		}
		for _, t := range toks {
			if t == "*" {
				continue
			}
			m := reRuleTok.FindStringSubmatch(t)
			if m == nil {
				return nil, die("%s: PROMPT_RULES の条件が不正: %s", job, t)
			}
			if !ruleSpecOK(job, m[1], m[2]) {
				return nil, die("%s: PROMPT_RULES の条件が不正: %s(key は min / hour / sec / days)", job, t)
			}
		}
		out = append(out, Rule{Cond: toks, Body: b})
	}
	if len(out) == 0 {
		return nil, die("%s: PROMPT_RULES にルールが無い", job)
	}
	return out, nil
}

// parseVars は parse_prompt_vars。
func parseVars(job, body string) ([]Var, error) {
	if body == "" {
		return nil, nil
	}
	var out []Var
	seen := map[string]bool{}
	for _, line := range bodyLines(body) {
		line = trimLeftSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, cmd, ok := strings.Cut(line, "|")
		if !ok {
			return nil, die("%s: PROMPT_VARS の行に | が無い(<名前> | <コマンド>): %s", job, line)
		}
		name, cmd = trimRightSpace(name), trimLeftSpace(cmd)
		if !reVarName.MatchString(name) {
			return nil, die("%s: PROMPT_VARS の名前が不正(英小文字・数字・_): %s", job, name)
		}
		if reservedVr[name] {
			return nil, die("%s: PROMPT_VARS の名前 %s は組み込みと重なる", job, name)
		}
		if seen[name] {
			return nil, die("%s: PROMPT_VARS の名前が重複: %s", job, name)
		}
		if cmd == "" {
			return nil, die("%s: PROMPT_VARS のコマンドが空: %s", job, name)
		}
		seen[name] = true
		out = append(out, Var{name, cmd})
	}
	if len(out) == 0 {
		return nil, die("%s: PROMPT_VARS に定義が無い", job)
	}
	return out, nil
}

// ParseAt は AT の値(YYYY-MM-DD HH:MM[:SS]。T 区切りも可)を loc の時刻として読む。
func ParseAt(at string, loc *time.Location) (time.Time, error) {
	v := strings.Replace(at, "T", " ", 1)
	if len(v) == len("2006-01-02 15:04") {
		v += ":00"
	}
	return time.ParseInLocation("2006-01-02 15:04:05", v, loc)
}
