package jobs

import "testing"

func TestValidName(t *testing.T) {
	for n, want := range map[string]bool{"j1": true, "a_b-c": true, "-a": false, "a b": false, "dispatch": false, "": false} {
		if got := ValidName(n); got != want {
			t.Errorf("ValidName(%q) = %v", n, got)
		}
	}
	if Key("j", true) != "sched-settings:j" || Key("j", false) != "sched-job:j" {
		t.Error("Key")
	}
}
