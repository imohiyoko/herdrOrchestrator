package gh

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConf(t *testing.T, body string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "github.conf")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TB_GITHUB_CONF", f)
}

func fakeExe(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh-wrapper")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCommandKeysByOp(t *testing.T) {
	exe := fakeExe(t)
	writeConf(t, "gh = "+exe+" base   # 共通\ngh_write = "+exe+" w\ngh_approve = "+exe+" a\n")
	for op, want := range map[string]string{OpRead: "gh base", OpComment: "gh_write w", OpReview: "gh_write w", OpApprove: "gh_approve a", OpMerge: "gh_write w"} {
		argv, key, problem := Command(op, "o/r", "w1")
		if problem != "" {
			t.Fatalf("%s: %s", op, problem)
		}
		if got := key + " " + strings.Join(argv[1:], " "); got != want || argv[0] != exe {
			t.Errorf("%s: got %q (%s), want %q", op, got, argv[0], want)
		}
	}
}

func TestCommandPlaceholders(t *testing.T) {
	exe := fakeExe(t)
	writeConf(t, "gh = "+exe+" exec -e GH_TOKEN:{owner}/pr-approve --repo={repo} as-{actor}\n")
	argv, _, p := Command(OpApprove, "my-org/app", "rev1")
	if p != "" {
		t.Fatal(p)
	}
	if got := strings.Join(argv[1:], " "); got != "exec -e GH_TOKEN:my-org/pr-approve --repo=my-org/app as-rev1" {
		t.Fatalf("got %q", got)
	}
}

func TestCommandReadKeyOnlyForRead(t *testing.T) {
	exe := fakeExe(t)
	writeConf(t, "gh_read = "+exe+"\n")
	if _, _, p := Command(OpRead, "o/r", "w1"); p != "" {
		t.Fatalf("read: %s", p)
	}
	if _, _, p := Command(OpComment, "o/r", "w1"); p == "" {
		t.Fatal("comment must not fall back to gh_read")
	}
}

func TestCommandFailsClosed(t *testing.T) {
	t.Setenv("TB_GITHUB_CONF", filepath.Join(t.TempDir(), "missing.conf"))
	if _, _, p := Command(OpRead, "o/r", "w1"); p == "" {
		t.Fatal("missing conf must fail")
	}
	writeConf(t, "gh =\n")
	if _, _, p := Command(OpRead, "o/r", "w1"); p == "" {
		t.Fatal("empty value must fail")
	}
	writeConf(t, "gh = gh\n")
	if _, _, p := Command(OpRead, "o/r", "w1"); p == "" {
		t.Fatal("relative path must fail (no PATH lookup)")
	}
	if runtime.GOOS != "windows" {
		p := filepath.Join(t.TempDir(), "noexec")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		writeConf(t, "gh = "+p+"\n")
		if _, _, prob := Command(OpRead, "o/r", "w1"); prob == "" {
			t.Fatal("non-executable must fail")
		}
	}
}

func TestValidRepo(t *testing.T) {
	for r, want := range map[string]bool{"o/r": true, "my-org/repo.go": true, "o/r/x": false, "o": false, "-o/r": false, "o/..": false, "o/r;x": false} {
		if ValidRepo(r) != want {
			t.Errorf("%s: want %v", r, want)
		}
	}
}

func TestWriteChecksBeforeAuthorize(t *testing.T) {
	called := 0
	var out, errb bytes.Buffer
	s := &Svc{W: &out, Err: &errb, Stdin: strings.NewReader(""), Authorize: func(op, repo, what string) error { called++; return nil }}
	for _, args := range [][]string{
		{"review", "add", "o/r", "1", "--body", "x"},            // --comment / --request-changes が無い
		{"review", "approve", "o/r", "1", "--comment"},          // approve に --comment は無い
		{"comment", "add", "o/r", "1"},                          // 本文が無い
		{"comment", "add", "o/r", "1", "--body-file", "/etc/x"}, // ファイルは受けない
		{"comment", "add", "o/r", "1", "--body", "a", "--body", "b"},
		{"pr", "merge", "o/r", "1"}, // 方法が無い
		{"pr", "merge", "o/r", "1", "--squash", "--merge"},
		{"pr", "merge", "o/r", "1", "--squash", "--head", "abc"}, // SHA でない
	} {
		err := s.Run(args)
		if e, ok := err.(*Err); !ok || e.Code != 2 {
			t.Errorf("%v: want usage error, got %v", args, err)
		}
	}
	if called != 0 {
		t.Fatalf("usage errors must not reach Authorize (%d)", called)
	}
}
