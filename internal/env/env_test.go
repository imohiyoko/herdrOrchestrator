package env

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionName(t *testing.T) {
	f := filepath.Join(t.TempDir(), "herdr-session")
	os.WriteFile(f, []byte("  fromfile # comment\nignored\n"), 0o600)
	cases := []struct {
		sock, sess, file, want string
	}{
		{"/x/herdr/sessions/work/herdr.sock", "env", f, "work"},
		{"/x/herdr/herdr.sock", "env", f, "default"},
		{"/elsewhere.sock", "env", f, "env"},
		{"", "", f, "fromfile"},
		{"", "", "", "default"},
	}
	for _, c := range cases {
		t.Setenv("HERDR_SOCKET_PATH", c.sock)
		t.Setenv("HERDR_SESSION", c.sess)
		t.Setenv("HERDR_SESSION_FILE", c.file)
		if got := SessionName(); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

func TestTBDB(t *testing.T) {
	t.Setenv("TB_DB", "/tmp/x.db")
	if TBDB() != "/tmp/x.db" {
		t.Error("TB_DB not used")
	}
	t.Setenv("TB_DB", "")
	t.Setenv("XDG_STATE_HOME", "/st")
	if got, want := TBDB(), filepath.Join("/st", "herdr", "tasks.db"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
