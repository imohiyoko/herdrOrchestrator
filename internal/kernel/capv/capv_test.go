package capv

import "testing"

func TestParse(t *testing.T) {
	for in, want := range map[string]struct {
		n       int
		lim, ok bool
	}{
		"3": {3, true, true}, " 0 ": {0, true, true}, "unlimited": {0, false, true}, " Unlimited ": {0, false, true},
		"UNLIMITED": {0, false, true}, "lots": {0, true, false}, "": {0, true, false},
	} {
		if n, lim, ok := Parse(in); n != want.n || lim != want.lim || ok != want.ok {
			t.Errorf("Parse(%q) = %d %v %v", in, n, lim, ok)
		}
	}
	if Int("unlimited", 5) != NoLimit || Int("x", 5) != 5 || Int("7", 5) != 7 {
		t.Error("Int")
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("CAPV_T", "")
	if Env("CAPV_T", 9) != 9 {
		t.Error("empty → default")
	}
	for v, want := range map[string]int{"unlimited": NoLimit, "12": 12, "-1": 9, "x": 9, "0": 0} {
		t.Setenv("CAPV_T", v)
		if got := Env("CAPV_T", 9); got != want {
			t.Errorf("Env(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestExceedsShow(t *testing.T) {
	if Exceeds(1<<30, 0, false) || !Exceeds(3, 2, true) || Exceeds(2, 2, true) {
		t.Error("Exceeds")
	}
	if Show(0, false) != "無制限" || Show(5, true) != "5" {
		t.Error("Show")
	}
}
