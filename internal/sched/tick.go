package sched

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// tick は scheduler/bin/tick.sh の中身: この回(今の分、または NOW)に実行する回を判定し、実行して logs/<job>.log に 1 行ずつ記録する。
// 判定の順・記録の JSON(キーの順も)・理由の文言は 元の bash 版の tick.sh と同じ。環境変数も同じ(NOW / DRY_RUN / FAKE_STATE / LOG_DIR / HERDR_BIN / TB_BIN)。

// TickEnv は tick が使う置き場所とコマンド(lib.sh の LOG_DIR / HERDR と、送信に使う tb)。
type TickEnv struct {
	Env
	LogDir, Herdr, TB string
	Dry               bool
	Now, FakeState    string
	// Force は今すぐ 1 回(tick --now。tb job run・POST /v1/jobs/{name}/run)。時刻・間隔・曜日・AT・MANUAL を見ない。ENABLED・GATE・SKIP_IF・ロックは同じ
	Force bool
}

// DefaultTickEnv は lib.sh / tick.sh と同じ既定。
func DefaultTickEnv() TickEnv {
	e := DefaultEnv()
	t := TickEnv{Env: e, LogDir: os.Getenv("LOG_DIR"), Herdr: os.Getenv("HERDR_BIN"), TB: os.Getenv("TB_BIN"),
		Dry: os.Getenv("DRY_RUN") == "1", Now: os.Getenv("NOW"), FakeState: os.Getenv("FAKE_STATE")}
	if t.LogDir == "" {
		t.LogDir = filepath.Join(e.Root, "logs")
	}
	if t.Herdr == "" {
		t.Herdr = "herdr"
	}
	if t.TB == "" {
		t.TB = filepath.Join(e.Root, "..", "taskboard", "bin", "tb")
	}
	return t
}

type ticker struct {
	TickEnv
	j      *Job
	loc    *time.Location
	stdout io.Writer
	stderr io.Writer

	at, pane, state, psrc, dryText string
	exitc                          *int
	outS, errS                     string
	hour, min, sec, dow, schedAt   string
	varVals                        map[string]string

	mu       sync.Mutex
	lock     string
	haveLock bool
	tmps     map[string]bool
}

func tsNow(loc *time.Location) string { return time.Now().In(loc).Format("2006-01-02T15:04:05-0700") }

// jsonLine は jq -nc の 1 行(キーの順はこの並び)。
func jsonLine(kv [][2]any) string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range kv {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := marshal(p[0])
		v, _ := marshal(p[1])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.String()
}

func (t *ticker) emit(action, reason string) {
	kv := [][2]any{{"ts", tsNow(t.loc)}, {"job", t.j.Name}, {"action", action}, {"reason", reason}, {"at", t.at}}
	add := func(k, v string) {
		if v != "" {
			kv = append(kv, [2]any{k, v})
		}
	}
	add("target", t.j.Target)
	add("pane", t.pane)
	add("state", t.state)
	add("prompt", t.psrc)
	if t.exitc != nil {
		kv = append(kv, [2]any{"exit", *t.exitc}, [2]any{"stdout", t.outS}, [2]any{"stderr", t.errS})
	}
	if t.Force {
		kv = append(kv, [2]any{"now", true}) // tick --now(呼ばれて動いた回)
	}
	if t.Dry {
		add("text", t.dryText)
		kv = append(kv, [2]any{"dry_run", true})
	}
	t.writeLine(t.j.Name, jsonLine(kv))
}

func (t *ticker) writeLine(job, line string) {
	if t.Dry {
		fmt.Fprintln(t.stdout, line)
		return
	}
	f, err := os.OpenFile(filepath.Join(t.LogDir, job+".log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		fmt.Fprintf(t.stderr, "error: ログに書けない: %v\n", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func (t *ticker) fail(reason string) { t.emit("failed", reason) }

// Tick は tick.sh。戻り値は終了コード(die は 2)。
func Tick(te TickEnv, name string, stdout, stderr io.Writer) int {
	// 定義が DB に無いジョブは動かさず、failed を記録する(同じ理由が最後の記録なら書き足さない)
	if reJobName.MatchString(name) && !te.FilesOK {
		if _, ok := te.dbBody("sched-job:" + name); !ok {
			why := te.missingMsg(name)
			line := jsonLine([][2]any{{"ts", tsNow(time.Local)}, {"job", name}, {"action", "failed"}, {"reason", why}})
			if te.Dry {
				fmt.Fprintln(stdout, line)
			} else {
				_ = os.MkdirAll(te.LogDir, 0o777)
				if lastFailedReason(filepath.Join(te.LogDir, name+".log")) != why {
					t := &ticker{TickEnv: te, stdout: stdout, stderr: stderr}
					t.writeLine(name, line)
				}
			}
			return finish(die("%s", why), stderr)
		}
	}
	j, err := te.Load(name)
	if err != nil {
		return finish(err, stderr)
	}
	loc, lerr := time.LoadLocation(j.TZ)
	if lerr != nil {
		loc = time.FixedZone(j.TZ, 0) // date と同じく、知らない TZ は UTC として扱う
	}
	os.Setenv("TZ", j.TZ) // GATE / COMMAND / PROMPT_* のコマンドにも渡す(lib.sh の export TZ)
	if !te.Dry {
		_ = os.MkdirAll(te.LogDir, 0o777)
	}
	t := &ticker{TickEnv: te, j: j, loc: loc, stdout: stdout, stderr: stderr, varVals: map[string]string{},
		lock: filepath.Join(te.LogDir, j.Name+".lock.d"), tmps: map[string]bool{}}
	stop := t.trapSignals()
	defer stop()
	defer t.cleanup()
	rc, err := t.run()
	if err != nil {
		return finish(err, stderr)
	}
	return rc
}

func lastFailedReason(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSuffix(string(b), "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	var v struct{ Action, Reason any }
	if json.Unmarshal([]byte(s), &v) != nil || v.Action != "failed" {
		return ""
	}
	r, _ := v.Reason.(string)
	return r
}

// trapSignals は tick.sh の trap cleanup EXIT(止められてもロックと一時ファイルを残さない)。
func (t *ticker) trapSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case s := <-ch:
			t.cleanup()
			os.Exit(128 + int(s.(syscall.Signal)))
		case <-done:
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

func (t *ticker) cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.haveLock {
		os.RemoveAll(t.lock)
		t.haveLock = false
	}
	for f := range t.tmps {
		os.Remove(f)
	}
	t.tmps = map[string]bool{}
}

func (t *ticker) tempFile() (*os.File, error) {
	f, err := os.CreateTemp("", "sched-tick.")
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.tmps[f.Name()] = true
	t.mu.Unlock()
	return f, nil
}

func (t *ticker) dropTemp(f *os.File) {
	f.Close()
	os.Remove(f.Name())
	t.mu.Lock()
	delete(t.tmps, f.Name())
	t.mu.Unlock()
}

var (
	reNowSec = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2})`)
	reNowMin = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2})$`)
)

func sodLabel(s int) string { return fmt.Sprintf("%02d:%02d:%02d", s/3600, s%3600/60, s%60) }

func (j *Job) intervalLabel() string {
	if j.IntervalSec%60 == 0 {
		return fmt.Sprintf("%d 分", j.IntervalSec/60)
	}
	return fmt.Sprintf("%d 秒", j.IntervalSec)
}

func (t *ticker) fmtAt(e int64) string {
	return time.Unix(e, 0).In(t.loc).Format("2006-01-02 15:04:05")
}

func (t *ticker) run() (int, error) {
	j := t.j
	var slotLo, slotHi, nowE int64
	faked := t.Now != ""
	if faked {
		n := strings.Replace(t.Now, "T", " ", 1)
		if m := reNowSec.FindStringSubmatch(n); m != nil {
			tm, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], t.loc)
			if err != nil {
				return 0, die("NOW を解釈できない: %s", t.Now)
			}
			nowE = tm.Unix()
			slotLo, slotHi = nowE, nowE
		} else if m := reNowMin.FindStringSubmatch(n); m != nil {
			tm, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+":00", t.loc)
			if err != nil {
				return 0, die("NOW を解釈できない: %s", t.Now)
			}
			nowE = tm.Unix()
			slotLo, slotHi = nowE, nowE+59
		} else {
			return 0, die("NOW を解釈できない: %s (YYYY-MM-DD HH:MM[:SS])", t.Now)
		}
	} else {
		nowE = time.Now().Unix()
		slotLo = nowE - nowE%60
		slotHi = slotLo + 59
	}
	lt := time.Unix(slotLo, 0).In(t.loc)
	dow := int(lt.Weekday())
	if dow == 0 {
		dow = 7
	}
	sodLo := lt.Hour()*3600 + lt.Minute()*60 + lt.Second()
	sodHi := sodLo + int(slotHi-slotLo)
	t.at = lt.Format("2006-01-02 15:04:05")

	// 対象外の回: DRY_RUN では理由を出し、通常は記録せずに終わる
	quiet := func(action, reason string) (int, error) {
		if t.Dry {
			t.emit(action, reason)
		}
		return 0, nil
	}
	var due []int64
	if t.Force {
		return t.fire([]int64{nowE}, nowE, slotHi, faked)
	}
	if j.Manual {
		return quiet("skipped_manual", "MANUAL=1 runs only on request (tb job run)")
	}
	if j.At != "" {
		at, err := ParseAt(j.At, t.loc)
		if err != nil {
			return 0, die("%s: AT の日時が不正: %s", j.Name, j.At)
		}
		switch e := at.Unix(); {
		case e < slotLo:
			return quiet("skipped_at", fmt.Sprintf("AT %s has passed", j.At))
		case e > slotHi:
			return quiet("skipped_at", fmt.Sprintf("waiting for AT %s", j.At))
		default:
			due = []int64{e}
		}
		return t.fire(due, nowE, slotHi, faked)
	}
	nums, err := DaysToNums(j.Name, j.Days)
	if err != nil {
		fmt.Fprintln(t.stderr, "error: "+err.Error()) // tick.sh ではコマンド置換の中の die なので、曜日が合わない扱いになる
		nums = nil
	}
	if !containsInt(nums, dow) {
		return quiet("skipped_window", fmt.Sprintf("day %d not in DAYS (%s)", dow, j.Days))
	}
	lo, hi := sodLo, sodHi
	if lo < j.WinStart {
		lo = j.WinStart
	}
	if hi > j.WinEnd {
		hi = j.WinEnd
	}
	if lo > hi {
		return quiet("skipped_window", fmt.Sprintf("%s outside %s", sodLabel(sodLo), j.Window))
	}
	iv := j.IntervalSec
	first := j.WinStart + (lo-j.WinStart+iv-1)/iv*iv
	for p := first; p <= hi; p += iv {
		due = append(due, slotLo+int64(p-sodLo))
	}
	if len(due) == 0 {
		return quiet("skipped_interval", fmt.Sprintf("%s not on the %s-grid from %s", sodLabel(sodLo), j.intervalLabel(), j.Start))
	}
	return t.fire(due, nowE, slotHi, faked)
}

// fire は回の時刻 due を順に実行する(一時停止なら記録だけ)。
func (t *ticker) fire(due []int64, nowE, slotHi int64, faked bool) (int, error) {
	j := t.j
	if j.Enabled != "1" {
		t.at = t.fmtAt(due[0])
		t.emit("skipped_disabled", "paused (ENABLED=0)")
		return 0, nil
	}
	// 実際に起動された回: 過ぎてしまった回は最後の 1 回にまとめる
	if !faked {
		var past int64 = -1
		var future []int64
		for _, p := range due {
			if p <= nowE {
				past = p
			} else {
				future = append(future, p)
			}
		}
		due = due[:0]
		if past >= 0 {
			due = append(due, past)
		}
		due = append(due, future...)
	}

	rc := 0
	for n, p := range due {
		if !faked {
			cur := time.Now().Unix()
			if n > 0 && cur > slotHi {
				break // 前の回が長引いて分をまたいだら、残りは次の起動に任せる
			}
			if p > cur {
				time.Sleep(time.Duration(p-cur) * time.Second)
			}
		}
		pt := time.Unix(p, 0).In(t.loc)
		t.at = pt.Format("2006-01-02 15:04:05")
		t.pane, t.state, t.exitc, t.outS, t.errS, t.psrc = "", "", nil, "", "", ""
		t.hour, t.min, t.sec = pt.Format("15"), pt.Format("04"), pt.Format("05")
		d := int(pt.Weekday())
		if d == 0 {
			d = 7
		}
		t.dow = strconv.Itoa(d)
		t.schedAt = t.at
		for k, v := range map[string]string{"TB_SYSTEM": "scheduler", "SCHED_JOB": j.Name, "SCHED_AT": t.at, "SCHED_HOUR": t.hour,
			"SCHED_MIN": t.min, "SCHED_SEC": t.sec, "SCHED_DOW": t.dow, "SCHED_OWNER": j.Owner} {
			os.Setenv(k, v)
		}
		if !t.runGate() {
			continue
		}
		var ok bool
		if j.Command != "" {
			ok = t.runCommand()
		} else {
			ok = t.runPrompt()
		}
		if !ok {
			rc = 1
		}
	}
	return rc, nil
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// shell は /bin/bash -c <cmd> を時間制限つきで実行する(stdin は /dev/null)。時間切れはプロセスグループごと止める。
// 戻り値は終了コード(起動できなければ 127、シグナルで止まれば 128+番号)と、時間切れかどうか。
func shell(cmd string, timeout int, stdout, stderr *os.File) (int, bool) {
	c := exec.Command("/bin/bash", "-c", cmd)
	if stdout != nil {
		c.Stdout = stdout
	}
	if stderr != nil {
		c.Stderr = stderr
	}
	setGroup(c)
	if err := c.Start(); err != nil {
		return 127, false
	}
	var mu sync.Mutex
	timedOut := false
	timer := time.AfterFunc(time.Duration(timeout)*time.Second, func() {
		mu.Lock()
		timedOut = true
		mu.Unlock()
		killGroup(c)
	})
	err := c.Wait()
	timer.Stop()
	mu.Lock()
	to := timedOut
	mu.Unlock()
	return exitCode(c, err), to
}

func exitCode(c *exec.Cmd, err error) int {
	if c.ProcessState == nil {
		if err != nil {
			return 127
		}
		return 0
	}
	if sig, ok := signaled(c.ProcessState); ok {
		return 128 + sig
	}
	return c.ProcessState.ExitCode()
}

// head は先頭 n バイトを読み、NUL を除く(head -c n | tr -d '\000')。
func head(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, int64(n)))
	return string(bytes.ReplaceAll(b, []byte{0}, nil))
}

// runCapture は run_capture: stdout(NUL を除き上限まで。末尾の改行は落とす)と終了コード・時間切れ。stderr は捨てる。
func (t *ticker) runCapture(cmd string, timeout, limit int) (string, int, bool) {
	f, err := t.tempFile()
	if err != nil {
		return "", 126, false
	}
	defer t.dropTemp(f)
	rc, to := shell(cmd, timeout, f, nil)
	out := strings.TrimRight(head(f.Name(), limit), "\n")
	if to {
		return out, 124, true
	}
	return out, rc, false
}

// runCommand は COMMAND 型: agent の状態を見ずにコマンドを実行する。false は失敗(記録済み)。
func (t *ticker) runCommand() bool {
	j := t.j
	if t.Dry {
		t.emit("ran", fmt.Sprintf("dry-run: would run command (timeout %ds)", j.CommandTimeout))
		return true
	}
	if os.Mkdir(t.lock, 0o777) != nil {
		lb, _ := os.ReadFile(filepath.Join(t.lock, "pid"))
		lpid := strings.TrimRight(string(lb), "\n")
		if lpid != "" && alive(lpid) {
			t.emit("skipped_locked", fmt.Sprintf("previous run still active (pid %s)", lpid))
			return true
		}
		os.RemoveAll(t.lock) // 異常終了で残った stale lock
		if os.Mkdir(t.lock, 0o777) != nil {
			t.emit("skipped_locked", "lock race")
			return true
		}
	}
	t.mu.Lock()
	t.haveLock = true
	t.mu.Unlock()
	_ = os.WriteFile(filepath.Join(t.lock, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o666)
	outf, err1 := t.tempFile()
	errf, err2 := t.tempFile()
	if err1 != nil || err2 != nil {
		t.cleanup()
		t.fail("could not start (mktemp)")
		return false
	}
	rc, to := shell(j.Command, j.CommandTimeout, outf, errf)
	t.outS, t.errS = strings.TrimRight(head(outf.Name(), 500), "\n"), strings.TrimRight(head(errf.Name(), 500), "\n")
	t.cleanup()
	if to {
		x := 124
		t.exitc = &x
		t.fail(fmt.Sprintf("timeout after %ds", j.CommandTimeout))
		return false
	}
	t.exitc = &rc
	if rc == 0 {
		t.emit("ran", "exit 0")
		return true
	}
	t.fail(fmt.Sprintf("exit %d", rc))
	return false
}

// alive は kill -0 <pid>(数でなければ生きていない扱い)。
func alive(pid string) bool {
	n, err := strconv.Atoi(pid)
	if err != nil || n <= 0 {
		return false
	}
	return pidAlive(n)
}

// runGate は GATE: 終了コード 0 のときだけ true。0 以外・時間切れ・起動失敗は skipped_gate を記録する。出力は捨てる。
func (t *ticker) runGate() bool {
	j := t.j
	if j.Gate == "" {
		return true
	}
	rc, to := shell(j.Gate, j.GateTimeout, nil, nil)
	switch {
	case to:
		t.emit("skipped_gate", fmt.Sprintf("gate timeout after %ds", j.GateTimeout))
	case rc == 0:
		return true
	case rc == 126 || rc == 127:
		t.emit("skipped_gate", fmt.Sprintf("gate could not start (exit %d)", rc))
	default:
		t.emit("skipped_gate", fmt.Sprintf("gate exit %d", rc))
	}
	return false
}

// ruleSpecMatch は rule_spec_match。
func ruleSpecMatch(spec string, v int) bool {
	for _, item := range strings.Split(spec, ",") {
		switch {
		case item == "*":
			return true
		case strings.HasPrefix(item, "*/"):
			if n := atoi(item[2:]); n > 0 && v%n == 0 {
				return true
			}
		case strings.Contains(item, "-"):
			a, b, _ := strings.Cut(item, "-")
			if v >= atoi(a) && v <= atoi(b) {
				return true
			}
		default:
			if v == atoi(item) {
				return true
			}
		}
	}
	return false
}

func (t *ticker) ruleMatch(r Rule) bool {
	for _, tok := range r.Cond {
		if tok == "*" {
			continue
		}
		k, v, _ := strings.Cut(tok, "=")
		switch k {
		case "hour":
			if !ruleSpecMatch(v, atoi(t.hour)) {
				return false
			}
		case "min":
			if !ruleSpecMatch(v, atoi(t.min)) {
				return false
			}
		case "sec":
			if !ruleSpecMatch(v, atoi(t.sec)) {
				return false
			}
		case "days":
			nums, _ := DaysToNums(t.j.Name, v)
			if !containsInt(nums, atoi(t.dow)) {
				return false
			}
		}
	}
	return true
}

// selectPrompt は select_prompt: PROMPT_CMD → PROMPT_RULES(最初に合った行)→ PROMPT_FILE / PROMPT。
// 0=本文あり / 1=どれも無い / 2=失敗(記録済み)
func (t *ticker) selectPrompt() (string, int) {
	j := t.j
	t.psrc = ""
	if j.PromptCmd != "" {
		out, rc, to := t.runCapture(j.PromptCmd, j.PromptCmdTimeout, 65536)
		switch {
		case to:
			t.fail(fmt.Sprintf("PROMPT_CMD timeout after %ds", j.PromptCmdTimeout))
			return "", 2
		case rc == 0:
			if out != "" {
				t.psrc = "cmd"
				return out, 0
			}
		case rc == 126 || rc == 127:
			t.fail(fmt.Sprintf("PROMPT_CMD could not start (exit %d)", rc))
			return "", 2
		default:
			t.fail(fmt.Sprintf("PROMPT_CMD exit %d", rc))
			return "", 2
		}
	}
	f := ""
	for i, r := range j.Rules {
		if !t.ruleMatch(r) {
			continue
		}
		t.psrc = fmt.Sprintf("rule:%d", i+1)
		if !strings.HasPrefix(r.Body, "file:") {
			return r.Body, 0
		}
		f = strings.TrimPrefix(r.Body, "file:")
		break
	}
	if t.psrc == "" {
		switch {
		case j.PromptFile != "":
			f, t.psrc = j.PromptFile, "default"
		case j.Prompt != "":
			t.psrc = "default"
			return j.Prompt, 0
		default:
			return "", 1
		}
	}
	if !readable(f) {
		what := t.psrc + " の file:"
		if t.psrc == "default" {
			what = "PROMPT_FILE"
		}
		t.fail(fmt.Sprintf("%sを読めない: %s", what, f))
		return "", 2
	}
	b, _ := os.ReadFile(f)
	return strings.TrimRight(string(bytes.ReplaceAll(b, []byte{0}, nil)), "\n"), 0
}

var rePlaceholder = regexp.MustCompile(`\{\{([a-z][a-z0-9_]*)\}\}`)

// expandPrompt は expand_prompt: {{名前}} を差し込む(1 回だけ。値の中の {{…}} は展開しない)。0=差し込み済み / 1=送らない(記録済み)/ 2=失敗(記録済み)
func (t *ticker) expandPrompt(text string) (string, int) {
	if !strings.Contains(text, "{{") {
		return text, 0
	}
	j := t.j
	var out strings.Builder
	rest := text
	for {
		loc := rePlaceholder.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		m, name := rest[loc[0]:loc[1]], rest[loc[2]:loc[3]]
		out.WriteString(rest[:loc[0]])
		rest = rest[loc[1]:]
		var val string
		switch name {
		case "job":
			val = j.Name
		case "target":
			val = j.Target
		case "at":
			val = t.schedAt
		case "date":
			val, _, _ = strings.Cut(t.schedAt, " ")
		case "time":
			if _, after, ok := strings.Cut(t.schedAt, " "); ok {
				val = after
			} else {
				val = t.schedAt
			}
		case "hour":
			val = t.hour
		case "min":
			val = t.min
		case "sec":
			val = t.sec
		case "dow":
			val = t.dow
		default:
			var cmd string
			found := false
			for _, v := range j.Vars {
				if v.Name == name {
					cmd, found = v.Cmd, true
					break
				}
			}
			if !found {
				out.WriteString(m) // 知らない名前はそのまま
				continue
			}
			if cached, ok := t.varVals[name]; ok {
				val = cached
				break
			}
			o, rc, to := t.runCapture(cmd, j.PromptVarTimeout, 16384)
			switch {
			case to:
				t.fail(fmt.Sprintf("PROMPT_VARS %s: timeout after %ds", name, j.PromptVarTimeout))
				return "", 2
			case rc == 0:
			case rc == 1:
				t.emit("skipped_prompt", fmt.Sprintf("PROMPT_VARS %s: exit 1 (nothing to send)", name))
				return "", 1
			case rc == 126 || rc == 127:
				t.fail(fmt.Sprintf("PROMPT_VARS %s: could not start (exit %d)", name, rc))
				return "", 2
			default:
				t.fail(fmt.Sprintf("PROMPT_VARS %s: exit %d", name, rc))
				return "", 2
			}
			val = o
			t.varVals[name] = val
		}
		out.WriteString(val)
	}
	return out.String() + rest, 0
}

func cut300(s string) string {
	r := []rune(s)
	if len(r) > 300 {
		return string(r[:300])
	}
	return s
}

func badLead(s string) bool { return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "-") }

// jqRaw は jq -r の "\(x)"(文字列はそのまま、null は null、ほかは JSON)。
func jqRaw(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := marshal(v)
	return string(b)
}

// agentState は herdr agent list から TARGET の pane と状態を引く。失敗は記録して false。
func (t *ticker) agentState() bool {
	out, err := exec.Command(t.Herdr, "agent", "list").CombinedOutput()
	if err != nil {
		msg := strings.TrimRight(string(out), "\n")
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			msg += err.Error()
		}
		t.fail("herdr agent list 失敗: " + cut300(msg))
		return false
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.fail("agent list の解析失敗: " + cut300(err.Error()))
		return false
	}
	// jq の [.result.agents[]? | select(.name==$n)] と同じく、形が違うところは無いものとして扱う
	var hit []map[string]any
	if m, ok := v.(map[string]any); ok {
		if r, ok := m["result"].(map[string]any); ok {
			if as, ok := r["agents"].([]any); ok {
				for _, x := range as {
					if a, ok := x.(map[string]any); ok && a["name"] == t.j.Target {
						hit = append(hit, a)
					}
				}
			}
		}
	}
	if len(hit) != 1 {
		t.fail(fmt.Sprintf("TARGET '%s' が一意に解決できない (count=%d)", t.j.Target, len(hit)))
		return false
	}
	t.pane, t.state = jqRaw(hit[0]["pane_id"]), jqRaw(hit[0]["agent_status"])
	return true
}

// runPrompt はプロンプト型: agent が idle / done のときだけ本文を送る。false は失敗(記録済み)。
func (t *ticker) runPrompt() bool {
	j := t.j
	text, sel := t.selectPrompt()
	switch sel {
	case 1:
		t.emit("skipped_prompt", "no prompt for this run (PROMPT_CMD empty / no PROMPT_RULES matched / no PROMPT)")
		return true
	case 2:
		return false
	}
	if text == "" {
		t.fail("本文が空")
		return false
	}
	if badLead(text) {
		t.fail("本文の先頭が / か - (スラッシュコマンド/オプションと解釈される)")
		return false
	}
	if t.Dry {
		t.state = t.FakeState
	} else if !t.agentState() {
		return false
	}
	for _, s := range strings.Fields(j.SkipIf) {
		if t.state == s {
			t.emit("skipped_state", fmt.Sprintf("state=%s matches SKIP_IF", t.state))
			return true
		}
	}
	if !t.Dry || t.state != "" {
		if t.state != "idle" && t.state != "done" {
			t.emit("skipped_state", fmt.Sprintf("state=%s is not idle/done", t.state))
			return true
		}
	}
	text, ex := t.expandPrompt(text)
	switch ex {
	case 1:
		return true
	case 2:
		return false
	}
	if text == "" {
		t.fail("差し込み後の本文が空")
		return false
	}
	if badLead(text) {
		t.fail("差し込み後の本文の先頭が / か - (スラッシュコマンド/オプションと解釈される)")
		return false
	}
	if t.Dry {
		t.dryText = text
		if t.state == "" {
			t.emit("sent", "dry-run: state not checked, would send")
		} else {
			t.emit("sent", "dry-run: would send to "+j.Target)
		}
		t.dryText = ""
		return true
	}
	// 送信は tb を通す(送ったものは taskboard の messages に kind=scheduler で残る)
	c := exec.Command("bash", t.TB, "system-send", j.Target, "--text", text, "--job", j.Name)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HERDR_PANE_ID=") && !strings.HasPrefix(kv, "HERDR_BIN=") {
			env = append(env, kv)
		}
	}
	c.Env = append(env, "HERDR_BIN="+t.Herdr)
	out, err := c.CombinedOutput()
	rc := exitCode(c, err)
	switch rc {
	case 0:
		t.emit("sent", "prompt submitted (via tb)")
	case 10:
		t.emit("skipped_state", "target became busy before sending (tb)")
	default:
		t.fail(fmt.Sprintf("tb system-send 失敗(exit %d): %s", rc, cut300(strings.TrimRight(string(out), "\n"))))
		return false
	}
	return true
}
