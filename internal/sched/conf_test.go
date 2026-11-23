package sched

import "testing"

func TestParseConf(t *testing.T) {
	ok := []struct {
		body string
		want map[string]string
	}{
		{"TARGET=a\nPROMPT='x $y `z`'\n", map[string]string{"TARGET": "a", "PROMPT": "x $y `z`"}},
		{`PROMPT="a \"q\" \$ \\ \n"`, map[string]string{"PROMPT": `a "q" $ \ \n`}},
		{"TARGET=a PROMPT=x\\ y # note\n# comment\n", map[string]string{"TARGET": "a", "PROMPT": "x y"}},
		{"PROMPT_RULES='\nmin=00 | a\n'\n", map[string]string{"PROMPT_RULES": "\nmin=00 | a\n"}},
		{"SKIP_IF=\"working blocked\"\nDAYS=a#b", map[string]string{"SKIP_IF": "working blocked", "DAYS": "a#b"}},
		{"PROMPT=ab'c d'\"e\"", map[string]string{"PROMPT": "abc de"}},
		{"TARGET=", map[string]string{"TARGET": ""}},
	}
	for _, c := range ok {
		got, err := ParseConf(c.body)
		if err != nil {
			t.Errorf("%q: %v", c.body, err)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%q: %s=%q, want %q", c.body, k, got[k], v)
			}
		}
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v", c.body, got)
		}
	}
	bad := []string{
		`PROMPT="hi $HOME"`, "PROMPT=$(date)", "PROMPT=`date`", "PROMPT=~/x", "TARGET=a; rm -rf x", "echo hi",
		"export TARGET=a", "FOO=1", "PROMPT='open", `PROMPT="open`, "TARGET=a b", "PROMPT=a|b",
	}
	for _, b := range bad {
		if _, err := ParseConf(b); err == nil {
			t.Errorf("%q: want error", b)
		}
	}
}

func TestParseConfLine(t *testing.T) {
	_, err := ParseConf("TARGET=a\nPROMPT='x\ny'\nFOO=1\n")
	ce, ok := err.(*ConfError)
	if !ok || ce.Line != 4 {
		t.Fatalf("got %v", err)
	}
}
