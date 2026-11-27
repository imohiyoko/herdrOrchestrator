package herdr

import (
	"context"
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

// ctx の期限を過ぎたら、上限より前に子プロセスを止める。期限を過ぎた ctx では起動しない。
func TestCallContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("疑似 herdr が sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	mark := filepath.Join(dir, "ran")
	os.WriteFile(bin, []byte("#!/bin/sh\ntouch "+mark+"\nexec sleep 30\n"), 0o755)
	t.Setenv("HERDR_BIN", bin)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if _, err := CallContext(ctx, "agent", "list"); err == nil || time.Since(t0) > 5*time.Second {
		t.Fatalf("期限で止まらない: %v %v", err, time.Since(t0))
	}
	os.Remove(mark)
	if _, err := CallContext(ctx, "pane", "close", "p1"); err == nil {
		t.Fatal("期限を過ぎた ctx で成功した")
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("期限を過ぎた ctx で herdr を起動した")
	}
}
