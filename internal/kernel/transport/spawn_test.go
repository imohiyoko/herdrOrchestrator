package transport

import "testing"

// spawn_error_of と同じ結果になるか(transport.sh)。
func TestSpawnErrorOf(t *testing.T) {
	for _, c := range []struct{ out, code, msg string }{
		{`{"error":{"code":"pane_busy","message":"busy\tnow"}}`, "pane_busy", "busy now"},
		{`{"error":{"code":"a/b!"}}`, "a_b_", ""},
		{"plain failure\nsecond", "exit 3", "plain failure"},
		{`{"result":{}}`, "exit 3", `{"result":{}}`},
	} {
		code, msg := spawnErrorOf([]byte(c.out), 3)
		if code != c.code || msg != c.msg {
			t.Errorf("%q: got %q %q, want %q %q", c.out, code, msg, c.code, c.msg)
		}
	}
}
