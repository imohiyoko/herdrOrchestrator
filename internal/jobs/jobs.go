// Package jobs は scheduler のジョブ定義と上書き設定を DB(config 表)に置く(tb job / tb sched-job)。
// 振る舞いは taskboard/lib/schedjobs.sh と同じにする。キーは sched-job:<job> と sched-settings:<job>。
package jobs

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

var reJob = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidName は sj_valid_job と同じ(dispatch は予約名)。
func ValidName(j string) bool {
	return reJob.MatchString(j) && utf8.RuneCountInString(j) <= 64 && j != "dispatch"
}

// NameChars はジョブ名の文字だけを確かめる(import の飛ばす判定。長さは見ない)。
func NameChars(j string) bool { return reJob.MatchString(j) && j != "dispatch" }

// Key は sj_key。
func Key(job string, settings bool) string {
	if settings {
		return "sched-settings:" + job
	}
	return "sched-job:" + job
}

// Env は置き場所(bin/tb が渡す TB_ROOT から決める。TB_SCHED_DIR / TB_SCHED_SHOW で差し替える)。
type Env struct {
	Dir  string // scheduler のディレクトリ(jobs/・settings/)
	Show string // scheduler/bin/show.sh
}

// DefaultEnv は schedjobs.sh の SJ_DIR / SJ_SHOW と同じ。
func DefaultEnv() Env {
	root := os.Getenv("TB_ROOT")
	e := Env{Dir: os.Getenv("TB_SCHED_DIR"), Show: os.Getenv("TB_SCHED_SHOW")}
	if e.Dir == "" {
		e.Dir = filepath.Join(root, "..", "scheduler")
	}
	if e.Show == "" {
		e.Show = filepath.Join(root, "..", "scheduler", "bin", "show.sh")
	}
	return e
}

// JobFile / SettingsFile はファイルの置き場所。
func (e Env) JobFile(j string) string      { return filepath.Join(e.Dir, "jobs", j+".conf") }
func (e Env) SettingsFile(j string) string { return filepath.Join(e.Dir, "settings", j+".settings") }

// FilesOK は SCHED_FILE_FALLBACK=1 のときだけファイルも読む。
func FilesOK() bool { return os.Getenv("SCHED_FILE_FALLBACK") == "1" }

// IsRegular は [[ -f f ]](symlink は辿る)。
func IsRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// IsLink は [[ -L f ]]。
func IsLink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// ReadBody は $(cat f)(末尾の改行を落とす)。読めなければ err(空のファイルとは区別する。bash は cat の失敗で止まる)。
func ReadBody(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// Lookup は sj_lookup と同じ: DB のこのセッションの行 > 共通の行 > (SCHED_FILE_FALLBACK=1 のときだけ)ファイル。
// src は db:<セッション> / db:* / file:<パス> / none。
func (e Env) Lookup(db *sql.DB, session, job string, settings bool) (body, src string, ok bool) {
	if b, shared, ok := config.DBRow(db, session, Key(job, settings)); ok {
		if shared {
			return b, "db:*", true
		}
		return b, "db:" + session, true
	}
	if !FilesOK() {
		return "", "none", false
	}
	f := e.JobFile(job)
	if settings {
		f = e.SettingsFile(job)
	}
	if !IsRegular(f) || IsLink(f) {
		return "", "none", false
	}
	b, err := ReadBody(f)
	if err != nil { // 読めないファイルは無いものとする(空の本文として使わない)
		return "", "none", false
	}
	return b, "file:" + f, true
}

// FileJobs は jobs/*.conf のジョブ名(名前の順)。
func (e Env) FileJobs() []string {
	ms, _ := filepath.Glob(filepath.Join(e.Dir, "jobs", "*.conf"))
	sort.Strings(ms)
	var out []string
	for _, m := range ms {
		if IsRegular(m) {
			out = append(out, strings.TrimSuffix(filepath.Base(m), ".conf"))
		}
	}
	return out
}

// Names は DB とファイルのジョブ名の和(sj_list と同じく、名前に使えない文字のものは除き、並べて重ねを除く)。
func (e Env) Names(db *sql.DB, session string) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT substr(key, 11) FROM main.config WHERE key LIKE 'sched-job:%' AND herdr_session IN (?, '*')`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		for _, l := range strings.Split(n, "\n") { // grep は行ごと
			set[l] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, n := range e.FileJobs() {
		set[n] = true
	}
	var out []string
	for n := range set {
		if reJob.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Check は sj_check と同じ: scheduler の show.sh を一時のディレクトリで動かし、読めるかを確かめる(DB は見せない)。
// 読めなければ show.sh の出力の最後の行を返す。
func (e Env) Check(job, conf, settings string) (ok bool, last string, err error) {
	tmp, err := os.MkdirTemp("", "tb-sj-")
	if err != nil {
		return false, "", err
	}
	defer os.RemoveAll(tmp)
	for _, d := range []string{"jobs", "settings", "logs"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o700); err != nil {
			return false, "", err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "jobs", job+".conf"), []byte(conf+"\n"), 0o600); err != nil {
		return false, "", err
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(tmp, "settings", job+".settings"), []byte(settings+"\n"), 0o600); err != nil {
			return false, "", err
		}
	}
	cmd := exec.Command("bash", e.Show, job)
	drop := map[string]bool{"SCHED_WINDOW": true, "SCHED_START": true, "SCHED_END": true, "SCHED_INTERVAL_MIN": true, "SCHED_INTERVAL_SEC": true,
		"SCHED_FILE_FALLBACK": true, "TB_DB": true, "JOBS_DIR": true, "LOG_DIR": true, "SCHED_SETTINGS_DIR": true}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "SCHED_FILE_FALLBACK=1", "TB_DB="+filepath.Join(tmp, "none.db"), "JOBS_DIR="+filepath.Join(tmp, "jobs"),
		"LOG_DIR="+filepath.Join(tmp, "logs"), "SCHED_SETTINGS_DIR="+filepath.Join(tmp, "settings"))
	out, rerr := cmd.CombinedOutput()
	if rerr == nil {
		return true, "", nil
	}
	s := strings.TrimRight(string(out), "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return false, s, nil
}

// History は sj_history / config_history と同じ列(最新の 50 件)。
func History(db *sql.DB, session, key string, fn func(cols []string, vals []any) error) error {
	return store.Each(db, `SELECT id, herdr_session, how, changed_by, changed_at, CASE WHEN body IS NULL THEN '(消した)' ELSE length(body) || ' 文字' END
    FROM main.config_changes WHERE key=? AND herdr_session IN (?, '*') ORDER BY id DESC LIMIT 50`, []any{key, session}, fn)
}

// LogDir は scheduler がログを書く所(tick と同じく LOG_DIR > <scheduler>/logs)。
func LogDir(e Env) string {
	if d := os.Getenv("LOG_DIR"); d != "" {
		return d
	}
	return filepath.Join(e.Dir, "logs")
}
