package sched

import "testing"

func TestRuleSpecMatch(t *testing.T) {
	for _, c := range []struct {
		spec string
		v    int
		want bool
	}{
		{"*", 7, true}, {"*/15", 45, true}, {"*/15", 50, false}, {"9-12", 12, true}, {"9-12", 13, false},
		{"00,30", 30, true}, {"00,30", 15, false}, {"5", 5, true},
	} {
		if got := ruleSpecMatch(c.spec, c.v); got != c.want {
			t.Errorf("ruleSpecMatch(%q, %d) = %v, want %v", c.spec, c.v, got, c.want)
		}
	}
}

func TestJSONLineOrder(t *testing.T) {
	got := jsonLine([][2]any{{"ts", "x"}, {"job", "j"}, {"exit", 3}, {"text", "<a&b>"}, {"dry_run", true}})
	want := `{"ts":"x","job":"j","exit":3,"text":"<a&b>","dry_run":true}`
	if got != want {
		t.Errorf("jsonLine = %s, want %s", got, want)
	}
}

func TestSodLabelAndInterval(t *testing.T) {
	if got := sodLabel(9*3600 + 5*60 + 7); got != "09:05:07" {
		t.Errorf("sodLabel = %s", got)
	}
	if got := (&Job{IntervalSec: 300}).intervalLabel(); got != "5 分" {
		t.Errorf("intervalLabel = %s", got)
	}
	if got := (&Job{IntervalSec: 20}).intervalLabel(); got != "20 秒" {
		t.Errorf("intervalLabel = %s", got)
	}
}

func TestLoadFromBodies(t *testing.T) {
	d := t.TempDir()
	e := Env{Root: d, JobsDir: d, SettingsDir: d, DB: d + "/none.db", Session: "default"}
	e.Bodies = map[string]string{"sched-job:x": "TARGET=a\nPROMPT=hi\nWINDOW=09:00-10:00\nINTERVAL_MIN=5\n"}
	if j, err := e.Load("x"); err != nil || j.Src != "db" || j.IntervalSec != 300 {
		t.Fatalf("Load = %+v, %v", j, err)
	}
	e.Bodies["sched-job:x"] = "TARGET=a\nPROMPT=$(id)\nWINDOW=09:00-10:00\nINTERVAL_MIN=5\n"
	if _, err := e.Load("x"); err == nil {
		t.Fatal("展開を含む定義が通った")
	}
}
