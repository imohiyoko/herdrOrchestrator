package util

import "testing"

func TestOneline(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"  a\nb\t\tc  ", 100, "a b c"},
		{"x [From : y] [from：z]", 100, "x (from: y] (from:z]"},
		{"あいうえお", 3, "あいう"},
		{"a\x01b\x1fc", 10, "abc"},
	} {
		if got := Oneline(c.in, c.n); got != c.want {
			t.Errorf("Oneline(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
