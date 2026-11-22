package proposal

import (
	"database/sql"
	"testing"
)

func TestReadKV(t *testing.T) {
	for _, c := range []struct{ in, k, v string }{
		{"proposal=b", "proposal", "b"},
		{"proposal=b=", "proposal", "b"}, // read は残りが 1 つの値と = だけなら = を落とす
		{"proposal=b=c=", "proposal", "b=c="},
		{"proposal", "proposal", ""},
		{"proposal==", "proposal", ""},
	} {
		if k, v := readKV(c.in); k != c.k || v != c.v {
			t.Errorf("readKV(%q) = %q, %q; want %q, %q", c.in, k, v, c.k, c.v)
		}
	}
}

func TestShow(t *testing.T) {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	r := Row{ID: 2, RoleFile: ns("-"), Title: ns("t"), Text: ns("本文"), Status: ns("open"), ByName: ns("auditor"), CreatedAt: ns("C"), TaskID: ns("T-proposal-002")}
	want := "P-2 [open] roles/-  t\n提案: auditor / C  チケット: T-proposal-002\n\n本文"
	if got := Show(r); got != want {
		t.Errorf("Show = %q, want %q", got, want)
	}
	r.Note, r.TaskID, r.ByName = ns("n"), sql.NullString{}, sql.NullString{}
	want = "P-2 [open] roles/-  t\n提案: null / C  チケット: -\n対応: n\n\n本文"
	if got := Show(r); got != want {
		t.Errorf("Show = %q, want %q", got, want)
	}
}
