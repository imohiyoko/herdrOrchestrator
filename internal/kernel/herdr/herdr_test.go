package herdr

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// herdr が応答しなくても、呼び出しは上限(HERDR_CALL_TIMEOUT_SEC)で子プロセスを止めて失敗する。
func TestCallTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("疑似 herdr が sh")
	}
	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN", bin)
	t.Setenv("HERDR_CALL_TIMEOUT_SEC", "0.2")
	for name, f := range map[string]func() ([]byte, error){
		"Call":         func() ([]byte, error) { return Call("agent", "list") },
		"CallCombined": func() ([]byte, error) { return CallCombined("pane", "close", "p1") },
	} {
		t0 := time.Now()
		if _, err := f(); err == nil {
			t.Errorf("%s: 応答しない herdr で成功した", name)
		}
		if d := time.Since(t0); d > 5*time.Second {
			t.Errorf("%s: %v かかった", name, d)
		}
	}
}
