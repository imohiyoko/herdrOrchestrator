// Package env は lib/herdr-env.sh・lib/herdr_env.py と同じ既定値を Go で持つ。
// 片方を変えたらもう片方も変える(env_test.go が bash の答えと突き合わせる)。
package env

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// StateDir は tasks.db などを置くディレクトリ。XDG_STATE_HOME > (Windows) LOCALAPPDATA > ~/.local/state の下の herdr。
func StateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "herdr")
	}
	if runtime.GOOS == "windows" {
		if l := os.Getenv("LOCALAPPDATA"); l != "" {
			return filepath.Join(l, "herdr", "state")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "state", "herdr")
}

// TBDB は tasks.db のパス。TB_DB が優先。
func TBDB() string {
	if p := os.Getenv("TB_DB"); p != "" {
		return p
	}
	return filepath.Join(StateDir(), "tasks.db")
}

var (
	reNamed   = regexp.MustCompile(`/sessions/([A-Za-z0-9_][A-Za-z0-9._-]*)/herdr\.sock$`)
	reDefault = regexp.MustCompile(`/herdr/herdr\.sock$`)
	reName    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
)

// SessionName は herdr のセッション名。HERDR_SOCKET_PATH > HERDR_SESSION > herdr-session ファイル > default(herdr_session_name と同じ順)。
// ファイルは HERDR_SESSION_FILE で指定したときだけ読む(バイナリは checkout の場所を知らないため。bash は checkout 直下を既定にする)。
func SessionName() string {
	p := filepath.ToSlash(os.Getenv("HERDR_SOCKET_PATH"))
	if m := reNamed.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	if reDefault.MatchString(p) {
		return "default"
	}
	if s := os.Getenv("HERDR_SESSION"); s != "" {
		return s
	}
	if f := os.Getenv("HERDR_SESSION_FILE"); f != "" {
		if v, ok := SessionFileValue(f); ok {
			return v
		}
	}
	return "default"
}

// SessionFileValue は herdr-session ファイルの 1 行目の名前(bash の herdr_session_file_value)。# 以降と前後の空白
// (bash の [[:space:]] と同じ ASCII の空白だけ)は無視する。使えない文字があれば無いものとする。読むのは 1 行目だけ
func SessionFileValue(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	line = strings.TrimSuffix(line, "\n")
	line, _, _ = strings.Cut(line, "#")
	line = strings.Trim(line, " \t\n\v\f\r")
	if !reName.MatchString(line) {
		return "", false
	}
	return line, true
}
