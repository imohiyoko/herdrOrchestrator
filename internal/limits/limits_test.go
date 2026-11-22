package limits

import "testing"

func TestNorm(t *testing.T) {
	ok := map[[2]string]string{
		{"max_running_agents", "007"}:              "7",
		{"max_working_agents", "0"}:                "0",
		{"incident_roles", "fixer,fixer verifier"}: "fixer verifier",
		{"watchers", ""}:                           "",
		{"human_handoff", "on"}:                    "on",
	}
	for in, want := range ok {
		if got, err := Norm(in[0], in[1]); err != nil || got != want {
			t.Errorf("Norm(%q) = %q %v", in, got, err)
		}
	}
	for _, in := range [][2]string{{"max_running_agents", "0"}, {"analyst_max", "x"}, {"incident_roles", "bogus"}, {"incident_roles", " "}, {"watchers", "a/b"}, {"human_handoff", "yes"}, {"nope", "1"}} {
		if _, err := Norm(in[0], in[1]); err == nil {
			t.Errorf("Norm(%q) accepted", in)
		}
	}
}

func TestRawEnv(t *testing.T) {
	t.Setenv("TB_LIMIT_HUMAN_HANDOFF", "on")
	s := Set{conf: map[string]string{"human_handoff": "off"}}
	if v, src, _ := s.Raw("human_handoff"); v != "on" || src != "env" {
		t.Errorf("env = %q %q", v, src)
	}
	t.Setenv("TB_LIMIT_HUMAN_HANDOFF", "bad") // 通らない値は無視して設定に落ちる
	if v, src, _ := s.Raw("human_handoff"); v != "off" || src != "conf" {
		t.Errorf("bad env = %q %q", v, src)
	}
	if got := (Set{}).Get("max_running_agents"); got != "30" {
		t.Errorf("default = %q", got)
	}
}
