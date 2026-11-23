package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	dir := t.TempDir()
	root, _ := filepath.Abs("../../webui")
	t.Setenv("WEBUI_ROOT", root)
	t.Setenv("WEBUI_STATE_DIR", dir)
	t.Setenv("WEBUI_AUTH_DIR", filepath.Join(dir, "auth"))
	t.Setenv("WEBUI_FAIL_DELAY", "0")
	t.Setenv("TB_DB", filepath.Join(dir, "none.db"))
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_SESSION", "default")
	t.Setenv("XDG_CONFIG_HOME", dir)
	ForgetSessions()
	a, err := NewAuth(filepath.Join(dir, "auth"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Auth: a, Log: io.Discard}
	ts := httptest.NewServer(s)
	u, _ := url.Parse(ts.URL)
	s.Port = mustAtoi(t, u.Port())
	t.Cleanup(ts.Close)
	return ts, s
}

func mustAtoi(t *testing.T, s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type resp struct {
	code int
	loc  string
	body string
	h    http.Header
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func do(t *testing.T, ts *httptest.Server, method, path, cookie string, form url.Values, hdr ...string) resp {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, _ := http.NewRequest(method, ts.URL+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", ts.URL)
	}
	if cookie != "" {
		r.Header.Set("Cookie", Cookie+"="+cookie)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			r.Host = hdr[i+1]
		} else if hdr[i+1] == "" {
			r.Header.Del(hdr[i])
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w, err := noRedirect.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Body.Close()
	b, _ := io.ReadAll(w.Body)
	return resp{w.StatusCode, w.Header.Get("Location"), string(b), w.Header}
}

var reCSRF = regexp.MustCompile(`name=csrf value="([^"]+)"`)

func csrf(t *testing.T, body string) string {
	t.Helper()
	m := reCSRF.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("csrf が無い: %s", body)
	}
	return m[1]
}

func cookieOf(t *testing.T, r resp) string {
	t.Helper()
	c := r.h.Get("Set-Cookie")
	v, _, _ := strings.Cut(strings.TrimPrefix(c, Cookie+"="), ";")
	if v == "" {
		t.Fatalf("Set-Cookie が無い: %v", r.h)
	}
	return v
}

func TestLoginFlow(t *testing.T) {
	ts, s := testServer(t)
	const B = "/s/default"
	if r := do(t, ts, "GET", "/", "", nil); r.code != 302 || r.loc != B+"/" {
		t.Fatalf("/ → %d %s", r.code, r.loc)
	}
	if r := do(t, ts, "GET", "/s/nope/", "", nil); r.code != 404 {
		t.Fatalf("知らないセッション %d", r.code)
	}
	if r := do(t, ts, "GET", B+"/", "", nil, "Host", "evil.example"); r.code != 403 {
		t.Fatalf("Host 違い %d", r.code)
	}
	if r := do(t, ts, "GET", B+"/tasks", "", nil); r.code != 302 || r.loc != B+"/setup" {
		t.Fatalf("未設定 → setup: %d %s", r.code, r.loc)
	}
	r := do(t, ts, "GET", B+"/setup", "", nil)
	if r.code != 200 || !strings.Contains(r.body, `action="`+B+`/setup"`) || r.h.Get("Content-Security-Policy") != CSP {
		t.Fatalf("setup 画面 %d %v", r.code, r.h)
	}
	tok := csrf(t, r.body)
	if r := do(t, ts, "POST", B+"/setup", "", url.Values{"csrf": {tok}, "username": {"alice"}, "password": {"short"}, "confirm": {"short"}}); r.code != 400 || !strings.Contains(r.body, "12 文字以上") {
		t.Fatalf("短いパスワード %d", r.code)
	}
	if r := do(t, ts, "POST", B+"/setup", "", url.Values{"csrf": {"bad"}, "username": {"alice"}}); r.code != 403 {
		t.Fatalf("csrf 不正 %d", r.code)
	}
	if r := do(t, ts, "POST", B+"/setup", "", url.Values{"csrf": {tok}}, "Origin", "http://evil"); r.code != 403 {
		t.Fatalf("Origin 不正 %d", r.code)
	}
	if r := do(t, ts, "POST", B+"/setup", "", url.Values{"csrf": {tok}, "x": {strings.Repeat("a", 9000)}}); r.code != 413 {
		t.Fatalf("大きすぎる本文 %d", r.code)
	}
	r = do(t, ts, "POST", B+"/setup", "", url.Values{"csrf": {tok}, "username": {"alice"}, "password": {"correcthorse12"}, "confirm": {"correcthorse12"}})
	if r.code != 302 || r.loc != B+"/" {
		t.Fatalf("setup %d %s", r.code, r.loc)
	}
	ck := cookieOf(t, r)
	if !strings.Contains(r.h.Get("Set-Cookie"), "HttpOnly; SameSite=Strict; Max-Age=43200") {
		t.Fatalf("cookie %s", r.h.Get("Set-Cookie"))
	}
	if r := do(t, ts, "GET", B+"/setup", "", nil); r.code != 404 {
		t.Fatalf("設定後の setup %d", r.code)
	}
	if r := do(t, ts, "GET", B+"/refresh.js", ck, nil); r.code != 200 || r.h.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("refresh.js %d", r.code)
	}
	if r := do(t, ts, "GET", B+"/notify.js", ck, nil); !strings.Contains(r.body, `var BASE = "/s/default"`) || !strings.Contains(r.body, "15000") {
		t.Fatalf("notify.js: %.300s", r.body)
	}
	if r := do(t, ts, "GET", B+"/", ck, nil); r.code != 501 || !strings.Contains(r.body, `action="`+B+`/logout"`) {
		t.Fatalf("未移行の画面 %d", r.code)
	}
	if r := do(t, ts, "GET", B+"/api/notifications", "", nil); r.code != 401 {
		t.Fatalf("api 未ログイン %d", r.code)
	}
	if r := do(t, ts, "HEAD", B+"/refresh.js", ck, nil); r.code != 200 || r.body != "" {
		t.Fatalf("HEAD %d %q", r.code, r.body)
	}
	if r := do(t, ts, "PUT", B+"/x", ck, nil); r.code != 405 || r.h.Get("Allow") != "GET, HEAD" {
		t.Fatalf("PUT %d", r.code)
	}

	// 未ログインはログインへ(next 付き)
	if r := do(t, ts, "GET", B+"/tasks?a=1", "", nil); r.code != 302 || r.loc != B+"/login?next=%2Ftasks%3Fa%3D1" {
		t.Fatalf("login へ %d %s", r.code, r.loc)
	}
	r = do(t, ts, "GET", B+"/login?next=/tasks", "", nil)
	tok = csrf(t, r.body)
	if !strings.Contains(r.body, `name=next value="/tasks"`) {
		t.Fatal("next が無い")
	}
	if r := do(t, ts, "POST", B+"/login", "", url.Values{"csrf": {tok}, "username": {"alice"}, "password": {"wrong"}}); r.code != 401 || !strings.Contains(r.body, loginFail) {
		t.Fatalf("失敗 %d", r.code)
	}
	r = do(t, ts, "POST", B+"/login", "", url.Values{"csrf": {tok}, "username": {"alice"}, "password": {"correcthorse12"}, "next": {"//evil"}})
	if r.code != 302 || r.loc != B+"/" {
		t.Fatalf("ログイン %d %s", r.code, r.loc)
	}
	ck2 := cookieOf(t, r)
	if r := do(t, ts, "GET", B+"/login", ck2, nil); r.code != 302 {
		t.Fatalf("ログイン済みの /login %d", r.code)
	}

	// ログアウト: そのセッションのトークンでだけ
	r = do(t, ts, "GET", B+"/tasks", ck2, nil)
	lt := csrf(t, r.body)
	if r := do(t, ts, "POST", B+"/logout", ck, url.Values{"csrf": {lt}}); r.code != 403 {
		t.Fatalf("別セッションのトークン %d", r.code)
	}
	r = do(t, ts, "POST", B+"/logout", ck2, url.Values{"csrf": {lt}})
	if r.code != 302 || r.loc != B+"/login" || !strings.Contains(r.h.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout %d %s", r.code, r.loc)
	}
	if s.Auth.SessionHash(ck2) != "" || s.Auth.SessionHash(ck) == "" {
		t.Fatal("logout は自分のセッションだけ消す")
	}
	if r := do(t, ts, "GET", B+"/logout", "", nil); r.code != 405 || r.h.Get("Allow") != "POST" {
		t.Fatalf("GET /logout %d", r.code)
	}
}

func TestLockout(t *testing.T) {
	ts, s := testServer(t)
	t.Setenv("WEBUI_LOCK_MAX", "2")
	if ok, err := s.Auth.Setup("alice", "correcthorse12"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	tok := s.Auth.FormToken("anon")
	bad := url.Values{"csrf": {tok}, "username": {"alice"}, "password": {"x"}}
	do(t, ts, "POST", "/s/default/login", "", bad)
	if r := do(t, ts, "POST", "/s/default/login", "", bad); r.code != 401 || !strings.Contains(r.body, "(ロックしました)") {
		t.Fatalf("2 回目 %d", r.code)
	}
	r := do(t, ts, "POST", "/s/default/login", "", url.Values{"csrf": {tok}, "username": {"alice"}, "password": {"correcthorse12"}})
	if r.code != 429 || r.h.Get("Retry-After") == "" {
		t.Fatalf("ロック中 %d", r.code)
	}
}

func TestReset(t *testing.T) {
	ts, s := testServer(t)
	if _, err := s.Auth.Setup("alice", "correcthorse12"); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Auth.CreateSession()
	if r := do(t, ts, "GET", "/s/default/reset", "", nil); r.code != 200 || !strings.Contains(r.body, "touch ") || !strings.Contains(r.body, "許可されていません") {
		t.Fatalf("許可なし %d", r.code)
	}
	tok := s.Auth.FormToken("anon")
	pw := url.Values{"csrf": {tok}, "password": {"newpassword123"}, "confirm": {"newpassword123"}}
	if r := do(t, ts, "POST", "/s/default/reset", "", pw); r.code != 403 {
		t.Fatalf("許可なしの再設定 %d", r.code)
	}
	if err := os.WriteFile(s.Auth.AllowPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := do(t, ts, "GET", "/s/default/reset", "", nil); !strings.Contains(r.body, `action="/s/default/reset"`) {
		t.Fatal("許可ありの画面")
	}
	if r := do(t, ts, "POST", "/s/default/reset", "", pw); r.code != 302 || r.loc != "/s/default/login" {
		t.Fatalf("再設定 %d", r.code)
	}
	if s.Auth.SessionHash(old) != "" || !s.Auth.Attempt("alice", "newpassword123") || isFile(s.Auth.AllowPath) {
		t.Fatal("再設定の後始末")
	}
}

func TestWithBase(t *testing.T) {
	in := `<a href="/x"> <a href="//e"> <img src="/\y"> <form action="/p"> <a href="http://a/"> <a href="rel">`
	want := `<a href="/s/n/x"> <a href="//e"> <img src="/\y"> <form action="/s/n/p"> <a href="http://a/"> <a href="rel">`
	if got := withBase(in, "/s/n"); got != want {
		t.Fatalf("%s", got)
	}
}

func TestShQuote(t *testing.T) {
	for in, want := range map[string]string{"/a/b-c": "/a/b-c", "/a b": "'/a b'", "it's": `'it'"'"'s'`, "/日本": "'/日本'"} {
		if got := shQuote(in); got != want {
			t.Errorf("%s → %s", in, got)
		}
	}
}

// TestPythonCompat は Python 版(webui/auth.py)と Go 版でログイン情報のファイルを入れ替えられること。
func TestPythonCompat(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("python3 が無い")
	}
	webui, _ := filepath.Abs("../../webui")
	run := func(code string, dir string) string {
		c := exec.Command(py, "-c", "import sys; sys.path.insert(0, sys.argv[1]); import auth; a = auth.Auth(sys.argv[2])\n"+code, webui, dir)
		c.Env = append(os.Environ(), "WEBUI_FAIL_DELAY=0")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", code, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// Go で作って Python で確かめる
	d1 := t.TempDir()
	a, _ := NewAuth(d1)
	a.Setup("alice", "correcthorse12")
	tok, _ := a.CreateSession()
	if got := run("print(a.attempt('alice', 'correcthorse12'), bool(a.session_hash('"+tok+"')))", d1); got != "True True" {
		t.Fatalf("Python から: %s", got)
	}
	// Python で作って Go で確かめる
	d2 := t.TempDir()
	ptok := run("a.setup('bob', 'batterystaple12'); print(a.create_session())", d2)
	b, _ := NewAuth(d2)
	if !b.Attempt("bob", "batterystaple12") || b.Attempt("bob", "nope") || b.SessionHash(ptok) == "" {
		t.Fatal("Go から")
	}
}
