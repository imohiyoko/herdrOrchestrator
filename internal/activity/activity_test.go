package activity

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTouch(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--event", "Stop", "--session-id", "s1"}, ""},
		{[]string{"--event", "UserPromptSubmit", "--session-id", "s1", "--pane-id", "w:p1", "--cwd", "/a b"}, ""},
		{[]string{"--event"}, "invalid-args"},
		{[]string{"--event", "Stop", "--prompt", "x"}, "invalid-args"},
		{[]string{"--event", "Bogus", "--session-id", "s1"}, "invalid-event"},
		{[]string{"--event", "Stop"}, "missing-session-id"},
		{[]string{"--event", "Stop", "--session-id", "a b"}, "invalid-session-id"},
		{[]string{"--event", "Stop", "--session-id", "s1", "--tab-id", "t;x"}, "invalid-field"},
		{[]string{"--event", "Stop", "--session-id", "s1", "--cwd", "a\nb"}, "invalid-cwd"},
		{[]string{"--event", "Stop", "--session-id", "s1", "--cwd", strings.Repeat("あ", 1024)}, ""},
		{[]string{"--event", "Stop", "--session-id", "s1", "--cwd", strings.Repeat("a", 1025)}, "invalid-cwd"},
	}
	for _, c := range cases {
		if _, got := ParseTouch(c.args); got != c.want {
			t.Errorf("ParseTouch(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestLogEvent(t *testing.T) {
	if got := LogEvent([]string{"--event", "Bogus"}); got != "-" {
		t.Errorf("got %q", got)
	}
	if got := LogEvent([]string{"--event", "Stop", "--session-id", "x"}); got != "Stop" {
		t.Errorf("got %q", got)
	}
}

func TestReason(t *testing.T) {
	for msg, want := range map[string]string{
		"database is locked (5) (SQLITE_BUSY)":     "db-locked",
		"attempt to write a readonly database (8)": "db-readonly",
		"unable to open database file":             "db-readonly",
		"disk I/O error":                           "db-error",
	} {
		if got := Reason(errors.New(msg)); got != want {
			t.Errorf("Reason(%q) = %q, want %q", msg, got, want)
		}
	}
}
