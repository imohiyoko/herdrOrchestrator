package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeHerdr(t *testing.T, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("疑似 herdr は sh のスクリプト")
	}
	p := filepath.Join(t.TempDir(), "herdr")
	os.WriteFile(p, []byte("#!/bin/sh\nprintf '%s' '"+out+"'\n"), 0o755)
	t.Setenv("HERDR_BIN", p)
}

func TestRequireHuman(t *testing.T) {
	fakeHerdr(t, `{"result":{"agents":[{"pane_id":"w:p1","name":"worker1"},{"pane_id":"w:p2"}]}}`)
	t.Setenv("HERDR_PANE_ID", "")
	if err := RequireHuman("tb x"); err != nil {
		t.Errorf("human shell: %v", err)
	}
	t.Setenv("HERDR_PANE_ID", "w:p2")
	if err := RequireHuman("tb x"); err != nil {
		t.Errorf("pane without agent: %v", err)
	}
	t.Setenv("HERDR_PANE_ID", "w:p1")
	if err := RequireHuman("tb x"); err == nil || !strings.Contains(err.Error(), "agent 'worker1'") {
		t.Errorf("agent pane: %v", err)
	}
	fakeHerdr(t, `not json`)
	if err := RequireHuman("tb x"); err == nil || !strings.Contains(err.Error(), "herdr agent list に失敗") {
		t.Errorf("broken herdr: %v", err)
	}
}
