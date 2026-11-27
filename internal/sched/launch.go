package sched

import (
	"fmt"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// jobs / dispatch / plist / launchd は scheduler/bin の jobs.sh / dispatch.sh / gen-plist.sh / install.sh の中身。

// dbJobs は sched_db_jobs: DB にあるジョブ名(このセッションか共通 * の行)。DB が無い・読めなければ空。
func (e Env) dbJobs() []string {
	if !isRegular(e.DB) {
		return nil
	}
	db, err := store.OpenRO(e.DB, 5000)
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT substr(key, 11) FROM config WHERE key LIKE 'sched-job:%' AND herdr_session IN (?, '*')`, e.Session)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil && reJobName.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

// JobNames は sched_job_names: DB のジョブ名(SCHED_FILE_FALLBACK=1 なら jobs/*.conf との和)。名前順で重複なし。
func (e Env) JobNames() []string {
	set := map[string]bool{}
	for _, j := range e.dbJobs() {
		set[j] = true
	}
	if e.FilesOK {
		fs, _ := filepath.Glob(filepath.Join(e.JobsDir, "*.conf"))
		for _, f := range fs {
			if j := strings.TrimSuffix(filepath.Base(f), ".conf"); isRegular(f) && reJobName.MatchString(j) {
				set[j] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for j := range set {
		out = append(out, j)
	}
	sort.Strings(out)
	return out
}

// Dispatch は dispatch.sh: 指定したジョブの tick を並べて動かす。ジョブごとに別のプロセス(herdrctl sched tick)にする
// (tick は TZ や SCHED_* を環境変数に置くので、1 プロセスの中で混ぜない)。1 つでも失敗があれば 1。
func Dispatch(jobs []string, stdout, stderr io.Writer) int {
	if len(jobs) == 0 {
		fmt.Fprintln(stderr, "error: 使い方: dispatch.sh <job> [<job> ...] | --all")
		return 2
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "error: herdrctl の場所が分からない: "+err.Error())
		return 1
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	rc := 0
	for _, j := range jobs {
		c := exec.Command(self, "sched", "tick", j)
		c.Stdout, c.Stderr = stdout, stderr
		if err := c.Start(); err != nil {
			fmt.Fprintln(stderr, "error: "+err.Error())
			rc = 1
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Wait() != nil {
				mu.Lock()
				rc = 1
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return rc
}

var (
	reInstance  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	rePlistSafe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
)

// labelPrefix は herdr_label_prefix(TB_INSTANCE 未設定なら local.herdr-)。
func labelPrefix() (string, error) {
	inst := os.Getenv("TB_INSTANCE")
	if inst == "" {
		return "local.herdr-", nil
	}
	if !reInstance.MatchString(inst) {
		return "", die("TB_INSTANCE は英数字で始まり、英数字と . _ - の 64 字以内: %s", inst)
	}
	return "local.herdr-" + inst + "-", nil
}

// GenPlist は gen-plist.sh。args は <job> か --dispatch <job> [<job> ...]。
func GenPlist(e Env, logDir string, args []string, stdout io.Writer) error {
	var job, prog, what string
	var progArgs []string
	if len(args) > 0 && args[0] == "--dispatch" {
		args = args[1:]
		if len(args) == 0 {
			return die("使い方: gen-plist.sh --dispatch <job> [<job> ...] | --dispatch --all")
		}
		all := len(args) == 1 && args[0] == "--all"
		for _, j := range args {
			if all {
				break
			}
			if _, err := e.Load(j); err != nil {
				return err
			}
		}
		job, prog, progArgs = "dispatch", filepath.Join(e.Root, "bin", "dispatch.sh"), args
		what = "ジョブ " + strings.Join(args, " ")
		if all {
			what = "DB のすべてのジョブ(毎分読み直す)"
		}
	} else {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		j, err := e.Load(name)
		if err != nil {
			return err
		}
		if j.Name == "dispatch" {
			return die("ジョブ名 dispatch は予約済み(--dispatch のラベルと重なる)")
		}
		job, prog, progArgs = j.Name, filepath.Join(e.Root, "bin", "tick.sh"), []string{j.Name}
		what = fmt.Sprintf("間隔 %s / %s / %s", j.intervalLabel(), j.Window, j.Days)
		if j.At != "" {
			what = "1 回だけ " + j.At
		}
		if j.Manual {
			what = "呼ばれたときだけ(MANUAL=1。毎分の判定では動かない)"
		}
	}
	prefix, err := labelPrefix()
	if err != nil {
		return err
	}
	label := prefix + "sched-" + job
	sess := ""
	if s := os.Getenv("HERDR_SESSION"); s != "" {
		if !rePlistSafe.MatchString(s) {
			return die("HERDR_SESSION は英数字と . _ - だけ: %s", s)
		}
		sess = "    <key>HERDR_SESSION</key>\n    <string>" + s + "</string>\n"
	}
	outDir := os.Getenv("OUT_DIR")
	if outDir == "" {
		outDir = filepath.Join(e.Root, "generated")
	}
	if err := os.MkdirAll(outDir, 0o777); err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0o777); err != nil {
		return err
	}
	out := filepath.Join(outDir, label+".plist")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>` + prog + `</string>
`)
	for _, a := range progArgs {
		b.WriteString("    <string>" + a + "</string>\n")
	}
	b.WriteString(`  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
` + sess + `  </dict>
  <key>StartCalendarInterval</key>
  <dict/>
  <key>StandardOutPath</key>
  <string>` + filepath.Join(logDir, "launchd-"+job+".out") + `</string>
  <key>StandardErrorPath</key>
  <string>` + filepath.Join(logDir, "launchd-"+job+".err") + `</string>
</dict>
</plist>
`)
	if err := os.WriteFile(out, []byte(b.String()), 0o666); err != nil {
		return err
	}
	if p, err := exec.LookPath("plutil"); err == nil { // macOS 以外(テスト)では lint を省く
		if o, err := exec.Command(p, "-lint", out).CombinedOutput(); err != nil {
			return fmt.Errorf("plutil -lint: %s", strings.TrimSpace(string(o)))
		}
	}
	fmt.Fprintf(stdout, "%s (毎分起動。%s は tick.sh が判定)\n", out, what)
	return nil
}

// Launchd は install.sh: {install|uninstall|status} <job> | --dispatch。
func Launchd(e Env, args []string, stdout, stderr io.Writer) (int, error) {
	cmd, target := "", ""
	if len(args) > 0 {
		cmd = args[0]
	}
	if len(args) > 1 {
		target = args[1]
	}
	job := "dispatch"
	if target != "--dispatch" {
		j, err := e.Load(target)
		if err != nil {
			return 0, err
		}
		job = j.Name
	}
	prefix, err := labelPrefix()
	if err != nil {
		return 0, err
	}
	label := prefix + "sched-" + job
	src := filepath.Join(e.Root, "generated", label+".plist")
	dst := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", label+".plist")
	domain := "gui/" + strconv.Itoa(os.Getuid())
	launchctl := func(quiet bool, a ...string) error {
		c := exec.Command("launchctl", a...)
		if !quiet {
			c.Stdout, c.Stderr = stdout, stderr
		}
		return c.Run()
	}
	switch cmd {
	case "install":
		if !isRegular(src) {
			hint := job
			if job == "dispatch" {
				hint = "--dispatch <job> ..."
			}
			return 0, die("%s がない。先に bin/gen-plist.sh %s", src, hint)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return 1, err
		}
		if err := os.WriteFile(dst, b, 0o666); err != nil {
			return 1, err
		}
		_ = launchctl(true, "bootout", domain+"/"+label) // 再登録を許す
		if err := launchctl(false, "bootstrap", domain, dst); err != nil {
			return exitOf(err), nil
		}
		fmt.Fprintln(stdout, "installed: "+label)
	case "uninstall":
		if launchctl(true, "bootout", domain+"/"+label) != nil {
			fmt.Fprintln(stdout, "(未登録だった)")
		}
		os.Remove(dst)
		fmt.Fprintln(stdout, "uninstalled: "+label)
	case "status":
		o, _ := exec.Command("launchctl", "print", domain+"/"+label).CombinedOutput()
		lines := strings.SplitAfter(string(o), "\n")
		if len(lines) > 30 {
			lines = lines[:30]
		}
		fmt.Fprint(stdout, strings.Join(lines, ""))
	default:
		return 0, die("使い方: install.sh {install|uninstall|status} <job> | --dispatch")
	}
	return 0, nil
}

func exitOf(err error) int {
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() > 0 {
		return ee.ExitCode()
	}
	return 1
}
