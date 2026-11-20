package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imohiyoko/herdrorchestrator/internal/kernel/store"
)

func TestRun(t *testing.T) {
	cases := []struct {
		args     []string
		code     int
		inStdout string
		inStderr string
	}{
		{[]string{"version"}, 0, "herdrctl dev", ""},
		{[]string{"--version"}, 0, "herdrctl dev", ""},
		{[]string{"help"}, 0, "usage: herdrctl", ""},
		{nil, 2, "", "usage: herdrctl"},
		{[]string{"nope"}, 2, "", "不明なコマンド: nope"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb); got != c.code {
			t.Errorf("run(%q) = %d, want %d", c.args, got, c.code)
		}
		if !strings.Contains(out.String(), c.inStdout) {
			t.Errorf("run(%q) stdout = %q, want %q", c.args, out.String(), c.inStdout)
		}
		if !strings.Contains(errb.String(), c.inStderr) {
			t.Errorf("run(%q) stderr = %q, want %q", c.args, errb.String(), c.inStderr)
		}
	}
}

func TestDB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tasks.db")
	t.Setenv("TB_DB", p)
	t.Setenv("HERDR_SESSION", "s1")
	status := func() store.Status {
		var out, errb bytes.Buffer
		if c := run([]string{"db", "status"}, &out, &errb); c != 0 {
			t.Fatalf("db status = %d: %s", c, errb.String())
		}
		var st store.Status
		if err := json.Unmarshal(out.Bytes(), &st); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		return st
	}
	if st := status(); st.Exists || st.Latest == 0 {
		t.Fatalf("before migrate: %+v", st)
	}
	var out, errb bytes.Buffer
	if c := run([]string{"db", "migrate"}, &out, &errb); c != 0 {
		t.Fatalf("db migrate = %d: %s", c, errb.String())
	}
	if st := status(); !st.Exists || st.UserVersion != st.Latest {
		t.Fatalf("after migrate: %+v", st)
	}
	if c := run([]string{"db"}, &out, &errb); c != 2 {
		t.Errorf("db without subcommand = %d, want 2", c)
	}
}
