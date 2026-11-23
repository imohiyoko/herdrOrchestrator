package webui

import "testing"

// [ を含む行(主体が人・system 以外)があると、判定は「判定不能」になる(bash の glob と照合規則が違いうる)。
func TestDecideBracketUnknown(t *testing.T) {
	r1 := &rule{no: 1, subject: "*", op: "send:task", target: "*", effect: "allow"}
	r2 := &rule{no: 2, subject: "*", op: "send:[ab]", target: "*", effect: "deny"}
	p := &perms{rules: []*rule{r1, r2}}
	if eff, hit := p.decide("send:task:w1"); eff != "unknown" || hit != r2 {
		t.Fatalf("decide = %q, %v", eff, hit)
	}
	p.rules = []*rule{r1}
	if eff, hit := p.decide("send:task:w1"); eff != "allow" || hit != r1 {
		t.Fatalf("decide = %q, %v", eff, hit)
	}
}
