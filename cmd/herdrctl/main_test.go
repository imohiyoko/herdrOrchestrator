package main

import (
	"bytes"
	"strings"
	"testing"
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
