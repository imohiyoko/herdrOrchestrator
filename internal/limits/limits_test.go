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

func TestUnlimited(t *testing.T) {
	for _, k := range []string{"max_running_agents", "max_working_agents", "incident_max_open", "analyst_max", "spawn_max_per_parent", "spawn_max_total"} {
		for _, in := range []string{"unlimited", "Unlimited", " UNLIMITED "} {
			if got, err := Norm(k, in); err != nil || got != Unlimited {
				t.Errorf("Norm(%s, %q) = %q %v", k, in, got, err)
			}
		}
	}
	for _, k := range []string{"incident_roles", "human_handoff"} { // 数でないキーでは上限なしの意味にならない
		if got, err := Norm(k, "unlimited"); err == nil {
			t.Errorf("Norm(%s, unlimited) accepted: %q", k, got)
		}
	}
	t.Setenv("TB_LIMIT_MAX_RUNNING_AGENTS", "unlimited")
	if n, lim := (Set{}).Max("max_running_agents"); lim {
		t.Errorf("env unlimited = %d limited", n)
	}
	for _, k := range []string{"TB_LIMIT_MAX_RUNNING_AGENTS", "TB_LIMIT_INCIDENT_MAX_OPEN", "TB_LIMIT_ANALYST_MAX", "TB_LIMIT_MAX_WORKING_AGENTS", "TB_ANALYST_MAX"} {
		t.Setenv(k, "") // 手元の環境の値が conf より優先されないように
	}
	s := Set{conf: map[string]string{"incident_max_open": Unlimited, "analyst_max": "0", "max_working_agents": "0"}}
	if _, lim := s.Max("incident_max_open"); lim {
		t.Error("conf unlimited is limited")
	}
	if n, lim := s.Max("analyst_max"); !lim || n != 0 { // 0 は「許さない」のまま
		t.Errorf("analyst_max 0 = %d %v", n, lim)
	}
	if _, lim := s.Max("max_working_agents"); lim { // max_working_agents の 0 は前から無制限
		t.Error("max_working_agents 0 is limited")
	}
	if n, lim := s.Max("max_running_agents"); !lim || n != 30 { // 既定は変えない
		t.Errorf("default running = %d %v", n, lim)
	}
	t.Setenv("TB_ANALYST_MAX", "unlimited")
	if _, lim := (Set{}).Max("analyst_max"); lim {
		t.Error("TB_ANALYST_MAX=unlimited is limited")
	}
	if Exceeds(1<<30, 0, false) || !Exceeds(3, 2, true) || Exceeds(2, 2, true) || Show(0, false) != "無制限" || Show(5, true) != "5" {
		t.Error("Exceeds / Show")
	}
}
