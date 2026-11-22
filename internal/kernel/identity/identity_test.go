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

func TestResolveLabel(t *testing.T) {
	def := func(code, d string) string { return d }
	fakeHerdr(t, `{"result":{"agents":[{"pane_id":"w:p1","name":"worker1","agent_session":{"value":"s"}},{"pane_id":"w:p2","name":"x"},{"pane_id":"w:p3","name":"a b","agent_session":{"value":"s"}}]}}`)
	t.Setenv("HERDR_PANE_ID", "w:p1")
	if l, msg, ok := ResolveLabel(def); !ok || l != (Label{"worker1", "w:p1", "s"}) {
		t.Errorf("ok pane: %v %q %v", l, msg, ok)
	}
	for pane, want := range map[string]string{
		"":     "tb: [identity.no-pane] ",
		"w:p2": "の agent に session がありません",
		"w:p3": "報告者名/pane に使えない文字があります",
		"w:p9": "pane w:p9 に該当する agent がありません",
	} {
		t.Setenv("HERDR_PANE_ID", pane)
		if _, msg, ok := ResolveLabel(def); ok || !strings.Contains(msg, want) {
			t.Errorf("pane %q: %q %v", pane, msg, ok)
		}
	}
	fakeHerdr(t, `not json`) // 読めない一覧は「該当なし」(bash の jq の失敗と同じ)
	t.Setenv("HERDR_PANE_ID", "w:p1")
	if _, msg, ok := ResolveLabel(func(code, d string) string { return code + "!" }); ok || !strings.Contains(msg, "該当する agent がありません") {
		t.Errorf("broken list: %q", msg)
	}
	t.Setenv("HERDR_PANE_ID", "")
	if _, msg, _ := ResolveLabel(func(code, d string) string { return "上書き" }); msg != "tb: [identity.no-pane] 上書き" {
		t.Errorf("message override: %q", msg)
	}
}
