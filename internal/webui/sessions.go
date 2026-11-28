package webui

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	appconfig "github.com/imohiyoko/herdrorchestrator/internal/app/config"
	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

// sessions は URL の /s/<名前>/ に書ける herdr セッションの一覧。
// DB に行のあるセッション(agents / tasks / config)・herdr のセッションのディレクトリ・default・このプロセスのセッション。

const (
	sessTTL = 5 * time.Second
	sessMax = 200
)

var (
	sessMu    sync.Mutex
	sessAt    time.Time
	sessNames []string
)

func herdrSessionsDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "herdr", "sessions")
}

func sessionsFromDB(out map[string]bool) {
	p := TBDB()
	if !isFile(p) {
		return
	}
	db, err := store.OpenRO(p, 2000)
	if err != nil {
		return
	}
	defer db.Close()
	appconfig.Sessions(db, sessMax, out)
}

func collectSessions() []string {
	set := map[string]bool{"default": true, SessionName(): true}
	sessionsFromDB(set)
	if es, err := os.ReadDir(herdrSessionsDir()); err == nil {
		for _, e := range es {
			if st, err := os.Stat(filepath.Join(herdrSessionsDir(), e.Name())); err == nil && st.IsDir() {
				set[e.Name()] = true
			}
		}
	}
	var out []string
	for n := range set {
		if SessionRE.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if len(out) > sessMax {
		out = out[:sessMax]
	}
	return out
}

// KnownSessions は名前の一覧(名前順)。数秒だけ覚える。
func KnownSessions() []string {
	sessMu.Lock()
	if sessNames != nil && time.Since(sessAt) < sessTTL {
		defer sessMu.Unlock()
		return sessNames
	}
	sessMu.Unlock()
	names := collectSessions()
	sessMu.Lock()
	sessAt, sessNames = time.Now(), names
	sessMu.Unlock()
	return names
}

func ForgetSessions() {
	sessMu.Lock()
	sessNames = nil
	sessMu.Unlock()
}

func IsKnownSession(name string) bool {
	if !SessionRE.MatchString(name) {
		return false
	}
	for _, n := range KnownSessions() {
		if n == name {
			return true
		}
	}
	return false
}
