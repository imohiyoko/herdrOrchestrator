package tbcli

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	"github.com/imohiyoko/herdrorchestrator/internal/app/job"
	"github.com/imohiyoko/herdrorchestrator/internal/jobs"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/perms"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

const jobUsage = `tb job list   ジョブの一覧と、定義・上書き設定の出どころ(db:<セッション> / db:* / file:<パス> / none)
tb job show <job> [--settings]   今の本文(出どころは標準エラーに 1 行)
tb job set <job> [--settings] [--shared] < 本文   書く(人だけ。scheduler が読めることを確かめてから)
tb job edit <job> [--settings] [--shared]   $VISUAL / $EDITOR で書き換える(人だけ)
tb job unset <job> [--settings] [--shared]   DB の行を消す(人だけ)
tb job import [--dry-run] [--shared] [<job> ...]   jobs/*.conf(と settings/*.settings)を DB に取り込み、ファイルを退かす(人だけ)
tb job history <job> [--settings]   変更の履歴
tb job rm <job> [--shared] [--keep-log]   ジョブを消す: 定義と上書き設定の DB の行(人だけ。どのセッションにも残らなければ logs/<job>.log も消す)
tb job run <job>   時刻に関係なく今すぐ 1 回動かす(既定は人だけ。ENABLED・GATE・SKIP_IF は効く。API は POST /v1/jobs/{name}/run)
(古い名前 tb sched-job も同じに使える。tb sched-job だけなら list)
`

// requireHuman はコマンドの中の人だけの確かめ(bash の require_human)。段階は権限段階の表(ADR 0006)と設定 approvals の cmd: の行で決まる。
func requireHuman(what string) error {
	return permErr(perms.RequireHuman(what, permDB))
}

// requirePolicyWrite は設定 policy を old から new に変える前の確かめ(変わる行ごとに policy:<操作> を見る)。
func requirePolicyWrite(old, new, what string) error {
	return permErr(perms.RequirePolicyWrite(old, new, what, permDB))
}

// permErr は perms の断りを tb の終了コード(1 か 20)にする。
func permErr(err error) error {
	var d *perms.Denied
	if errors.As(err, &d) {
		return &exit{d.Code, d.Msg}
	}
	return err
}

// permDB は権限段階が設定 approvals を読むときだけ開く DB。
func permDB() (*sql.DB, string, func(), error) {
	db, err := openDB()
	if err != nil {
		return nil, "", nil, err
	}
	return db.DB, db.Session, func() { db.Close() }, nil
}

// cmdJob は tb job <サブコマンド>。legacy(tb sched-job)ならサブコマンドが無いときは list。
func cmdJob(args []string, w, stderr io.Writer, legacy bool) error {
	db, _, err := openCmd(stderr)
	if err != nil {
		return err
	}
	defer db.Close()
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if sub == "" && legacy {
		sub = "list"
	}
	e := jobs.DefaultEnv()
	switch sub {
	case "list":
		return jobList(db, e, args, w)
	case "show":
		return jobShow(db, e, args, w, stderr)
	case "set", "edit", "unset":
		return jobChange(db, e, sub, args, w, stderr)
	case "import":
		return jobImport(db, e, args, w, stderr)
	case "history":
		return jobHistory(db, args, w)
	case "run":
		if !legacy {
			return jobRun(db, args, w, stderr)
		}
	case "rm":
		if !legacy { // 新しい動詞は新しい名前だけ(tb sched-job には足さない)
			return jobRm(db, e, args, w)
		}
	case "", "-h", "--help", "help":
		if !legacy {
			fmt.Fprint(w, jobUsage)
			if sub == "" {
				return &exit{exitUsage, ""}
			}
			return nil
		}
	}
	if legacy {
		return usage("sched-job [list | show <job> [--settings] | set <job> [--settings] [--shared] < 本文 | edit <job> [--settings] [--shared] | unset <job> [--settings] [--shared] | import [--dry-run] [--shared] [<job> ...] | history <job> [--settings]]")
	}
	return usage("job: 不明なサブコマンド %s(list / show / set / edit / unset / rm / import / history)", sub)
}

func validJob(j string) error {
	if !jobs.ValidName(j) {
		return usage("ジョブ名は英数字で始まり、英数字・_・- の 64 字以内(dispatch は予約名): %s", j)
	}
	return nil
}

type jobArgs struct {
	job              string
	settings, shared bool
}

func parseJobArgs(args []string) (jobArgs, error) {
	var a jobArgs
	for _, s := range args {
		switch {
		case s == "--settings":
			a.settings = true
		case s == "--shared":
			a.shared = true
		case strings.HasPrefix(s, "-"):
			return a, usage("sched-job: 不明な引数 %s", s)
		default:
			if a.job != "" {
				return a, usage("sched-job: ジョブは 1 つ")
			}
			a.job = s
		}
	}
	if a.job == "" {
		return a, usage("sched-job: ジョブ名が必要です")
	}
	return a, validJob(a.job)
}

func jobList(db *store.DB, e jobs.Env, args []string, w io.Writer) error {
	if len(args) != 0 {
		return usage("sched-job list")
	}
	names, err := e.Names(db.DB, db.Session)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "セッション: %s\n", db.Session)
	for _, j := range names {
		_, js, _ := e.Lookup(db.DB, db.Session, j, false)
		_, ss, _ := e.Lookup(db.DB, db.Session, j, true)
		if js == "none" && jobs.IsRegular(e.JobFile(j)) {
			js = "未取り込み:" + e.JobFile(j) + "(tb job import " + j + ")"
		}
		fmt.Fprintf(w, "%s\t%s\tsettings=%s\n", j, js, ss)
	}
	return nil
}

func jobShow(db *store.DB, e jobs.Env, args []string, w, stderr io.Writer) error {
	a, err := parseJobArgs(args)
	if err != nil {
		return err
	}
	key := jobs.Key(a.job, a.settings)
	body, src, ok := e.Lookup(db.DB, db.Session, a.job, a.settings)
	if !ok {
		hint := ""
		if !a.settings && jobs.IsRegular(e.JobFile(a.job)) {
			hint = "(jobs/" + a.job + ".conf は tb job import " + a.job + " で取り込む)"
		}
		return die("%s は DB にありません%s", key, hint)
	}
	fmt.Fprintf(stderr, "# %s: %s\n", key, src)
	if body != "" {
		fmt.Fprintln(w, body)
	}
	return nil
}

// stdinIsTTY は [[ -t 0 ]]。
func stdinIsTTY() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// stdinData は読んだ標準入力(承認待ちに積むとき、読んだあとでも中身を残せるように)。
var stdinData *[]byte

func readStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	stdinData = &b
	return strings.TrimRight(string(b), "\n"), err
}

// editBody は $VISUAL / $EDITOR(既定 vi)で本文を書き換える。
func editBody(cur string, has bool) (string, error) {
	f, err := os.CreateTemp("", "tb-edit-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if has {
		fmt.Fprintln(f, cur)
	}
	f.Close()
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	cmd := exec.Command(ed, f.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", die("編集を中止しました")
	}
	b, err := jobs.ReadBody(f.Name())
	if err != nil {
		return "", die("編集した本文を読めません: %s", f.Name())
	}
	return b, nil
}

func jobChange(db *store.DB, e jobs.Env, sub string, args []string, w, stderr io.Writer) error {
	a, err := parseJobArgs(args)
	if err != nil {
		return err
	}
	if err := requireHuman("tb job " + sub); err != nil {
		return err
	}
	key := jobs.Key(a.job, a.settings)
	var r job.Result
	var body string
	switch sub {
	case "unset":
		if err := callApp(db, "tb job unset", "job.unset", job.UnsetReq{Name: a.job, Settings: a.settings, Shared: a.shared}, &r); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s: %s\n", key, r.Source)
		return nil
	case "set":
		if stdinIsTTY() {
			return usage("sched-job set <job>: 本文は標準入力から渡す(例: tb job set %s < jobs/%s.conf)", a.job, a.job)
		}
		if body, err = readStdin(); err != nil {
			return err
		}
	case "edit":
		cur, _, has := e.Lookup(db.DB, db.Session, a.job, a.settings)
		if body, err = editBody(cur, has); err != nil {
			return err
		}
	}
	// scheduler が読めることを確かめてから書く(API の job.set)
	if err := callApp(db, "tb job "+sub, "job.set", job.SetReq{Name: a.job, Body: body, Settings: a.settings, Shared: a.shared, How: sub}, &r); err != nil {
		return err
	}
	if r.Rejected != "" {
		fmt.Fprintf(stderr, "tb: scheduler がこの本文を受け付けません: %s\n", r.Rejected)
		return die("%s は書きませんでした", key)
	}
	fmt.Fprintf(w, "%s: %s\n", key, r.Source)
	return nil
}

func jobImport(db *store.DB, e jobs.Env, args []string, w, stderr io.Writer) error {
	dry, shared := false, false
	var want []string
	for _, s := range args {
		switch {
		case s == "--dry-run":
			dry = true
		case s == "--shared":
			shared = true
		case strings.HasPrefix(s, "-"):
			return usage("sched-job import [--dry-run] [--shared] [<job> ...]")
		default:
			if err := validJob(s); err != nil {
				return err
			}
			want = append(want, s)
		}
	}
	if err := requireHuman("tb job import"); err != nil {
		return err
	}
	if len(want) == 0 {
		want = e.FileJobs()
	}
	dest, n := "", 0
	for _, j := range want {
		if !jobs.NameChars(j) {
			fmt.Fprintf(w, "%s: ジョブ名に使えない文字があるので飛ばします\n", j)
			continue
		}
		f, sf := e.JobFile(j), e.SettingsFile(j)
		if !jobs.IsRegular(f) || jobs.IsLink(f) {
			fmt.Fprintf(w, "%s: jobs/%s.conf がありません(通常のファイルのみ)\n", j, j)
			continue
		}
		if ok, err := config.HasRow(db.DB, db.Session, "sched-job:"+j, shared); err != nil {
			return err
		} else if ok {
			fmt.Fprintf(w, "%s: DB に行が既にあるので飛ばします(jobs/%s.conf はそのまま)\n", j, j)
			continue
		}
		if jobs.IsLink(sf) {
			fmt.Fprintf(w, "%s: settings/%s.settings がシンボリックリンクなので飛ばします\n", j, j)
			continue
		}
		conf, err := jobs.ReadBody(f)
		if err != nil {
			return die("読めません: %s", f)
		}
		settings, sfExists := "", jobs.IsRegular(sf)
		if sfExists {
			if settings, err = jobs.ReadBody(sf); err != nil {
				return die("読めません: %s", sf) // 読めない上書き設定(ENABLED=0 など)を落として取り込まない
			}
		}
		ok, last, err := e.Check(j, conf, settings)
		if err != nil {
			return die("一時ディレクトリを作れません")
		}
		if !ok {
			fmt.Fprintf(stderr, "tb: scheduler がこの本文を受け付けません: %s\n", last)
			fmt.Fprintf(w, "%s: scheduler が読めない定義なので飛ばします\n", j)
			continue
		}
		n++
		if dry {
			also := ""
			if settings != "" {
				also = " と settings/" + j + ".settings"
			}
			fmt.Fprintf(w, "%s: jobs/%s.conf%s を取り込みます\n", j, j, also)
			continue
		}
		if dest == "" {
			dest = filepath.Join(env.StateDir(), "sched-imported", time.Now().Format("20060102-150405"))
			if os.MkdirAll(filepath.Join(dest, "jobs"), 0o755) != nil || os.MkdirAll(filepath.Join(dest, "settings"), 0o755) != nil {
				return die("退かす先を作れません: %s", dest)
			}
		}
		if err := config.DBPut(db.DB, db.Session, "sched-job:"+j, conf, "import", shared, ""); err != nil {
			return err
		}
		if settings != "" || sfExists {
			if err := config.DBPut(db.DB, db.Session, "sched-settings:"+j, settings, "import", shared, ""); err != nil {
				return err
			}
		}
		if moveFile(f, filepath.Join(dest, "jobs")) != nil {
			return die("%s を退かせません(DB には取り込み済み)", f)
		}
		if sfExists && moveFile(sf, filepath.Join(dest, "settings")) != nil {
			return die("%s を退かせません(DB には取り込み済み)", sf)
		}
		fmt.Fprintf(w, "%s: 取り込み、ファイルを %s/ へ退かしました\n", j, dest)
	}
	if dry {
		fmt.Fprintln(w, "(dry-run: 何も書いていません)")
	}
	if n == 0 {
		fmt.Fprintln(w, "取り込むジョブはありません")
	}
	return nil
}

// moveFile は mv f dir/(別のファイルシステムならコピーして消す)。
func moveFile(f, dir string) error {
	to := filepath.Join(dir, filepath.Base(f))
	if os.Rename(f, to) == nil {
		return nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	st, err := os.Stat(f)
	if err != nil {
		return err
	}
	if err := os.WriteFile(to, b, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Remove(f)
}

func jobHistory(db *store.DB, args []string, w io.Writer) error {
	a, err := parseJobArgs(args)
	if err != nil {
		return err
	}
	return jobs.History(db.DB, db.Session, jobs.Key(a.job, a.settings), func(cols []string, vals []any) error {
		return emitRow(w, false, cols, vals, "")
	})
}

// jobRm は tb job rm: ジョブの定義(sched-job:<job>)と上書き設定(sched-settings:<job>)の行を消す(API の job.rm)。
// どのセッションにも行が残らなければ、scheduler のログ(logs/<job>.log)も消す(--keep-log で残す)。履歴(config_changes)は残す。
func jobRm(db *store.DB, e jobs.Env, args []string, w io.Writer) error {
	keepLog := false
	var rest []string
	for _, s := range args {
		if s == "--keep-log" {
			keepLog = true
		} else {
			rest = append(rest, s)
		}
	}
	a, err := parseJobArgs(rest)
	if err != nil {
		return err
	}
	if a.settings {
		return usage("job rm: --settings は使えません(上書き設定だけ消すなら tb job unset %s --settings)", a.job)
	}
	if err := requireHuman("tb job rm"); err != nil {
		return err
	}
	which := "このセッション " + db.Session + " の行"
	if a.shared {
		which = "共通の行"
	}
	var r job.Result
	if err := callApp(db, "tb job rm", "job.rm", job.RmReq{Name: a.job, Shared: a.shared, Strict: true, RemoveLog: !keepLog}, &r); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s: %s を消しました(%s)\n", a.job, strings.Join(r.Gone, " と "), which)
	if r.Found {
		hint := ""
		if strings.HasPrefix(r.Source, "db:") && !a.shared {
			hint = "(共通の行は tb job rm " + a.job + " --shared で消す)"
		}
		fmt.Fprintf(w, "%s: まだ %s の定義で動きます%s\n", a.job, r.Source, hint)
	} else if jobs.IsRegular(e.JobFile(a.job)) {
		fmt.Fprintf(w, "%s: jobs/%s.conf は残しています(SCHED_FILE_FALLBACK=1 のときだけ読まれる)\n", a.job, a.job)
	}
	if r.LogFailed != "" {
		return die("%s を消せません(DB の行は消しました)", r.LogFailed)
	}
	if r.LogRemoved != "" {
		fmt.Fprintf(w, "%s: ログ %s を消しました\n", a.job, r.LogRemoved)
	}
	return nil
}

// jobRun は tb job run <job>: API の job.run(時刻に関係なく今すぐ 1 回)。動かなかった回(skipped_* / failed)は 1。
func jobRun(db *store.DB, args []string, w, stderr io.Writer) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return usage("job run <job>")
	}
	who, err := requester()
	if err != nil {
		return err
	}
	var r job.RunResult
	if err := callAppW(db, app.PrincipalOf(who), stderr, "job.run", job.RunReq{Name: args[0]}, &r); err != nil {
		return err
	}
	line := r.Name + ": " + r.Action
	if r.Reason != "" {
		line += "(" + r.Reason + ")"
	} else if r.Exit != nil {
		line += fmt.Sprintf("(exit %d)", *r.Exit)
	}
	fmt.Fprintln(w, line)
	if r.Stdout != "" {
		fmt.Fprintln(w, r.Stdout)
	}
	if !r.OK {
		return &exit{1, ""}
	}
	return nil
}
