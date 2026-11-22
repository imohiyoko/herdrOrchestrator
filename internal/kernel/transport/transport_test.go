package transport

import "testing"

func TestHasError(t *testing.T) {
	for in, want := range map[string]bool{
		`{"error":{"code":"x"}}`: true,
		`{"result":{}}`:          false,
		`not json`:               false,
		``:                       false,
		`{"error":1} {"ok":1}`:   false, // jq -e は最後の値で決まる
		`{"ok":1} {"error":1}`:   true,
		`[1]`:                    false,
	} {
		if got := hasError([]byte(in)); got != want {
			t.Errorf("hasError(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestJQText(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{{nil, ""}, {false, ""}, {"idle", "idle"}, {float64(3), "3"}, {true, "true"}} {
		if got := jqText(c.in); got != c.want {
			t.Errorf("jqText(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
