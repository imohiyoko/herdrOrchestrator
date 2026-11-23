// Package webui は Web UI(herdrctl webui serve。webui/bin/serve.sh から動かす)。
// 以前の Python 版(webui/server.py と webui/app/)から移したもので、画面・ファイルの形は Python 版と同じ。
// 境界は webui/tests/test_webui.py、部品と画面の HTML はこのパッケージのテストが確かめる。
package webui

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/env"
)

// 定数。
const (
	Host    = "127.0.0.1"
	Cookie  = "herdr_webui_session"
	MaxBody = 8192
	// MaxEditBody は本文を編集するフォーム(policy・ジョブの定義)の上限。本文は 256 KiB 文字までで、URL エンコードで 9 倍まで膨らむ
	MaxEditBody = 4 << 20
	DefPort     = 8800
	Shift       = 19
	RefreshS    = 30
)

var (
	SessionRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
	JobRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

func cfg(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Root は webui/ のディレクトリ(static/ を読む)。WEBUI_ROOT(webui/bin/serve.sh が渡す)> 実行時のカレントの webui。
func Root() string { return cfg("WEBUI_ROOT", "webui") }

// HerdrRoot は checkout の直下。
func HerdrRoot() string { return filepath.Dir(Root()) }

// StateDir は checkout をまたいで共有するもの(既定 ~/.local/state/herdr-webui/)。
func StateDir() string {
	if v := os.Getenv("WEBUI_STATE_DIR"); v != "" {
		return v
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "herdr-webui")
}

// AuthDir はログイン情報の置き場(全セッションで共通)。
func AuthDir() string { return cfg("WEBUI_AUTH_DIR", StateDir()) }

// RunDir は待ち受けたポートの port ファイルの置き場。
func RunDir() string {
	if v := os.Getenv("WEBUI_RUN_DIR"); v != "" {
		return v
	}
	return cfg("WEBUI_RUN_BASE", filepath.Join(Root(), "run"))
}

// TBDB は WEBUI_TB_DB → TB_DB → 既定。
func TBDB() string { return cfg("WEBUI_TB_DB", env.TBDB()) }

var (
	reSockNamed   = regexp.MustCompile(`/sessions/([A-Za-z0-9_][A-Za-z0-9._-]*)/herdr\.sock$`)
	reSockDefault = regexp.MustCompile(`/herdr/herdr\.sock$`)
)

func sessionFile() string {
	return cfg("HERDR_SESSION_FILE", filepath.Join(HerdrRoot(), "herdr-session"))
}

// SessionName は herdr_env.session_name: HERDR_SOCKET_PATH > HERDR_SESSION > herdr-session ファイル > default。
func SessionName() string {
	p := filepath.ToSlash(os.Getenv("HERDR_SOCKET_PATH"))
	if m := reSockNamed.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	if reSockDefault.MatchString(p) {
		return "default"
	}
	if s := os.Getenv("HERDR_SESSION"); s != "" {
		return s
	}
	if v, ok := env.SessionFileValue(sessionFile()); ok {
		return v
	}
	return "default"
}

// ApplySession は herdr_env.apply_session: herdr-session ファイルで決まったときだけ HERDR_SESSION に入れる(子プロセスに渡すため)。
func ApplySession() {
	p := filepath.ToSlash(os.Getenv("HERDR_SOCKET_PATH"))
	if reSockNamed.MatchString(p) || reSockDefault.MatchString(p) || os.Getenv("HERDR_SESSION") != "" {
		return
	}
	if v, ok := env.SessionFileValue(sessionFile()); ok {
		os.Setenv("HERDR_SESSION", v)
		os.Setenv("HERDR_SESSION_FROM_FILE", v)
	}
}
