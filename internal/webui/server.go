package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// server は HTTP の入口: ログインまわり(初回設定・ログイン・再設定・ログアウト)、
// 同梱のスクリプトと、pages.go の pages の画面。知らないパスは 404。

const CSP = "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'unsafe-inline'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// NotifyPollSec は data.NOTIFY_POLL_SEC と同じ。
const NotifyPollSec = 15

// Server は 1 プロセスで全 herdr セッションの画面を返す(/s/<セッション名>/)。
type Server struct {
	Auth *Auth
	Port int
	Log  io.Writer

	mu     sync.Mutex
	static map[string][]byte
}

// rq はリクエストごとの文脈(reqctx.py)。name は URL の /s/<名前> の名前。
type rq struct {
	w      http.ResponseWriter
	r      *http.Request
	name   string
	status int
	v      *sv // このリクエストのセッションの DB(開くのは初めて使うとき)
}

// data はこのリクエストのセッションの読み取り(data.py の request_db の中の q)。
func (q *rq) data() *sv {
	if q.v == nil || q.v.sess != q.name {
		q.v.close()
		q.v = newSV(q.name)
	}
	return q.v
}

func (q *rq) base() string { return "/s/" + q.name }

func (s *Server) logf(format string, a ...any) { fmt.Fprintf(s.Log, format, a...) }

// readStatic は webui/static/ のファイル(名前は呼び出し側で固定)。一度読んだら覚える。
func (s *Server) readStatic(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.static[name]; ok {
		return b, nil
	}
	b, err := os.ReadFile(filepath.Join(Root(), "static", name))
	if err != nil {
		return nil, err
	}
	if s.static == nil {
		s.static = map[string][]byte{}
	}
	s.static[name] = b
	return b, nil
}

func jsonString(v string) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func (s *Server) css() string { b, _ := s.readStatic("page.css"); return string(b) }

// ---- 応答

// absAttr は HTML のサーバー内の絶対パス(href="/…" など)。直後が / か \ のもの(// 始まりなど)は付け替えない。
var absAttr = regexp.MustCompile(`(\s(?:href|action|src)=")/`)

func withBase(html, base string) string {
	var b strings.Builder
	last := 0
	for _, m := range absAttr.FindAllStringSubmatchIndex(html, -1) {
		end := m[1]
		if end < len(html) && (html[end] == '/' || html[end] == '\\') {
			continue
		}
		b.WriteString(html[last:m[3]])
		b.WriteString(base + "/")
		last = end
	}
	b.WriteString(html[last:])
	return b.String()
}

func (s *Server) send(q *rq, code int, body []byte, ctype string, extra ...[2]string) {
	h := q.w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", CSP)
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	for _, kv := range extra {
		h.Add(kv[0], kv[1])
	}
	q.status = code
	q.w.WriteHeader(code)
	if q.r.Method != http.MethodHead {
		_, _ = q.w.Write(body)
	}
}

func (s *Server) html(q *rq, code int, html string, extra ...[2]string) {
	s.send(q, code, []byte(withBase(html, q.base())), "text/html; charset=utf-8", extra...)
}

func (s *Server) text(q *rq, code int, msg string, extra ...[2]string) {
	s.send(q, code, []byte(msg+"\n"), "text/plain; charset=utf-8", extra...)
}

// redirect の loc はこのサーバー内の絶対パス(SafeNext 済みなど)。BASE を付ける。
func (s *Server) redirect(q *rq, loc string, extra ...[2]string) {
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		loc = q.base() + loc
	}
	s.send(q, http.StatusFound, nil, "text/plain", append([][2]string{{"Location", loc}}, extra...)...)
}

func cookie(value string, maxAge int) [2]string {
	// ログインは全セッションで共通なので Path=/(どの /s/<セッション名>/ にも付く)
	return [2]string{"Set-Cookie", fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Strict; Max-Age=%d", Cookie, value, maxAge)}
}

// ---- 画面の部品(views.py / handler.py と同じ HTML)

func instLabel(name string) string {
	cls := "inst"
	if name != "default" {
		cls = "inst inst-warn"
	}
	return `<span class="` + cls + `" title="herdr セッション">` + E(name) + `</span>`
}

func titleSuffix(name string) string {
	if name == "" {
		return ""
	}
	return " [" + name + "]"
}

func errHTML(err string) string {
	if err == "" {
		return ""
	}
	return "<p class=err>" + E(err) + "</p>"
}

// plainPage はログイン・初回設定用(ナビも自動更新もなし)。
func (s *Server) plainPage(q *rq, title, body string) string {
	return "<!doctype html><html lang=ja><head><meta charset=utf-8><meta name=referrer content=same-origin>" +
		`<meta name=viewport content="width=device-width,initial-scale=1">` +
		"<title>" + E(title) + " - herdr" + E(titleSuffix(q.name)) + "</title><style>" + s.css() + "</style></head><body>" +
		"<main style='max-width:420px'>" + instLabel(q.name) + "<h1>" + E(title) + "</h1>" + body + "</main></body></html>"
}

type field struct{ name, label, typ, ac string }

func formHTML(action, csrf string, fields []field, submit, errMsg, note, next string) string {
	var rows strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&rows, `<p><label>%s<br><input type=%s name=%s required autocomplete=%s style="width:100%%"></label></p>`, E(f.label), f.typ, f.name, f.ac)
	}
	nx := ""
	if next != "" {
		nx = `<input type=hidden name=next value="` + E(next) + `">`
	}
	head := ""
	if note != "" || errMsg != "" {
		head = errHTML(errMsg) + "<p class=note>" + E(note) + "</p>"
	}
	return head + `<form method=post action="` + action + `"><input type=hidden name=csrf value="` + E(csrf) + `">` + nx + rows.String() +
		"<button type=submit>" + E(submit) + "</button></form>"
}

const forgot = `<p class=note><a href="/reset">パスワードを忘れた</a></p>`

var (
	setupFields = []field{{"username", "ユーザー名", "text", "username"},
		{"password", fmt.Sprintf("パスワード(%d 文字以上)", MinPassword), "password", "new-password"},
		{"confirm", "パスワード(確認)", "password", "new-password"}}
	loginFields = []field{{"username", "ユーザー名", "text", "username"}, {"password", "パスワード", "password", "current-password"}}
	resetFields = []field{{"password", fmt.Sprintf("新しいパスワード(%d 文字以上)", MinPassword), "password", "new-password"},
		{"confirm", "新しいパスワード(確認)", "password", "new-password"}}
	resetDeny = map[string]string{"missing": "再設定は許可されていません。", "expired": "再設定の許可の期限(10 分)が切れています。",
		"invalid": "再設定の許可ファイルが不正です(通常ファイルで、自分の所有のものだけ有効)。いったん削除して作り直してください。"}
)

const loginFail = "ユーザー名またはパスワードが違います"

func (s *Server) setupPage(q *rq, errMsg string) string {
	note := ""
	if errMsg == "" {
		note = "最初に 1 回だけ行う設定です。設定後、この画面は閉じます。"
	}
	return s.plainPage(q, "初回設定", formHTML("/setup", s.Auth.FormToken("anon"), setupFields, "設定する", errMsg, note, ""))
}

func (s *Server) loginPage(q *rq, next, errMsg string) string {
	return s.plainPage(q, "ログイン", formHTML("/login", s.Auth.FormToken("anon"), loginFields, "ログイン", errMsg, "", next)+forgot)
}

// shQuote は shlex.quote。
var shSafe = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shSafe.MatchString(s) && isASCII(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// resetPage は state が ok なら新パスワードの設定画面、そうでなければ許可コマンドの案内。
func (s *Server) resetPage(q *rq, state, errMsg string) string {
	if state == "ok" {
		return s.plainPage(q, "パスワードの再設定", formHTML("/reset", s.Auth.FormToken("anon"), resetFields, "再設定する", errMsg,
			"許可の有効期間内に設定してください。成功すると全員のログインが無効になります。", ""))
	}
	cmd := "touch " + shQuote(s.Auth.AllowPath)
	return s.plainPage(q, "パスワードの再設定", errHTML(resetDeny[state])+
		"<p>次のコマンドを手元のシェルで実行して再設定を許可してください(10 分間有効)。実行後にこの画面を再読み込みします。</p>"+
		"<pre>"+E(cmd)+"</pre>"+`<p class=note><a href="/login">ログインへ戻る</a></p>`)
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func runeCut(s string, n int) string {
	i := 0
	for p := range s {
		if i == n {
			return s[:p]
		}
		i++
	}
	return s
}

// passwordError は新しいパスワードの検査(初回設定・再設定で共通)。問題なければ ""。
func passwordError(pw, conf string) string {
	switch {
	case runeLen(pw) < MinPassword:
		return fmt.Sprintf("パスワードは %d 文字以上にしてください", MinPassword)
	case runeLen(pw) > MaxField:
		return "パスワードが長すぎます"
	case pw != conf:
		return "確認入力が一致しません"
	}
	return ""
}

// ---- リクエスト

func (s *Server) origins() []string {
	return []string{fmt.Sprintf("http://127.0.0.1:%d", s.Port), fmt.Sprintf("http://localhost:%d", s.Port)}
}

// hostOK は DNS リバインディング対策: Host が自分自身(127.0.0.1 / localhost とこのポート)であること。
func (s *Server) hostOK(r *http.Request) bool {
	h := strings.ToLower(r.Host)
	return h == fmt.Sprintf("127.0.0.1:%d", s.Port) || h == fmt.Sprintf("localhost:%d", s.Port)
}

func (s *Server) originOK(r *http.Request) bool {
	if o, ok := r.Header["Origin"]; ok {
		for _, b := range s.origins() {
			if len(o) > 0 && o[0] == b {
				return true
			}
		}
		return false
	}
	if ref, ok := r.Header["Referer"]; ok && len(ref) > 0 {
		for _, b := range s.origins() {
			if ref[0] == b || strings.HasPrefix(ref[0], b+"/") {
				return true
			}
		}
	}
	return false
}

func sessionToken(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Cookie"), ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		if k == Cookie {
			return v
		}
	}
	return ""
}

// body は application/x-www-form-urlencoded の本文(8192 バイトまで)。大きすぎ・長さが不正なら nil。
func body(r *http.Request) map[string]string {
	n := int64(0)
	if v := r.Header.Get("Content-Length"); v != "" {
		var err error
		if n, err = strconv.ParseInt(v, 10, 64); err != nil {
			n = -1
		}
	}
	if n < 0 || n > MaxBody {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, n))
	vals, _ := url.ParseQuery(strings.ToValidUTF8(string(raw), "�"))
	out := map[string]string{}
	for k, v := range vals {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

var reSessPath = regexp.MustCompile(`^/s/([^/]+)(/.*)?$`)

// ServeHTTP は /s/<セッション名>/… を、そのセッションの画面として扱う。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, query, _ := strings.Cut(r.RequestURI, "?")
	query, _, _ = strings.Cut(query, "#")
	raw, _, _ = strings.Cut(raw, "#")
	q := &rq{w: w, r: r, name: SessionName()}
	defer func() { q.v.close() }() // q.v は途中で作られるので、閉じるときの値を見る
	defer func() {
		if q.status == 0 {
			q.status = 200
		}
		// クエリ・Cookie・本文はログに残さない(メソッド・パス・状態だけ)
		s.logf("%s %s %s %d\n", time.Now().Format("02/Jan/2006 15:04:05"), r.Method, raw, q.status)
	}()
	if !s.hostOK(r) {
		s.text(q, 403, "Forbidden")
		return
	}
	if !strings.HasPrefix(raw, "/") { // 絶対 URI(http://…)など
		s.text(q, 400, "Bad Request")
		return
	}
	if raw == "/" { // このプロセスの herdr セッションへ
		s.send(q, 302, nil, "text/plain", [2]string{"Location", "/s/" + SessionName() + "/"})
		return
	}
	m := reSessPath.FindStringSubmatch(raw)
	if m == nil {
		s.text(q, 404, "Not Found")
		return
	}
	if !SessionRE.MatchString(m[1]) || !IsKnownSession(m[1]) {
		s.text(q, 404, "Not Found(その herdr セッションはありません。/ からこのプロセスのセッションへ)")
		return
	}
	q.name = m[1]
	qs := ""
	if query != "" {
		qs = "?" + query
	}
	if m[2] == "" { // /s/<名前> → /s/<名前>/
		s.send(q, 302, nil, "text/plain", [2]string{"Location", q.base() + "/" + qs})
		return
	}
	s.dispatch(q, m[2], query)
}

var (
	postPaths = map[string]bool{"/login": true, "/setup": true, "/logout": true, "/reset": true, "/resolve": true, "/agent-op": true, "/agent-new": true}
	reJobPost = regexp.MustCompile(`^/jobs/[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

func (s *Server) dispatch(q *rq, path, query string) {
	if q.r.Method == http.MethodPost && (postPaths[path] || reJobPost.MatchString(path)) {
		s.post(q, path)
		return
	}
	if q.r.Method != http.MethodGet && q.r.Method != http.MethodHead {
		s.text(q, 405, "Method Not Allowed(定期実行の設定・報告の対応済み・agent の操作以外は読み取り専用です)", [2]string{"Allow", "GET, HEAD"})
		return
	}
	s.get(q, path, query)
}

func (s *Server) notFoundPlain(q *rq) {
	s.html(q, 404, s.plainPage(q, "見つかりません", "<p>そのページはありません。</p>"))
}

func (s *Server) get(q *rq, path, query string) {
	a := s.Auth
	vals, _ := url.ParseQuery(query)
	switch path {
	case "/setup":
		if a.Configured() {
			s.notFoundPlain(q)
			return
		}
		s.html(q, 200, s.setupPage(q, ""))
		return
	case "/login":
		if !a.Configured() {
			s.redirect(q, "/setup")
			return
		}
		nx := "/"
		if v, ok := vals["next"]; ok && len(v) > 0 {
			nx = v[0]
		}
		nx = SafeNext(nx)
		if a.SessionHash(sessionToken(q.r)) != "" {
			s.redirect(q, nx)
			return
		}
		s.html(q, 200, s.loginPage(q, nx, ""))
		return
	case "/reset":
		if !a.Configured() {
			s.redirect(q, "/setup")
			return
		}
		st := a.ResetState()
		if st == "expired" || st == "invalid" {
			s.logf("reset: denied (%s)\n", st)
		}
		s.html(q, 200, s.resetPage(q, st, ""))
		return
	case "/logout":
		s.text(q, 405, "Method Not Allowed", [2]string{"Allow", "POST"})
		return
	case "/api/notifications": // スクリプト用。未ログインはリダイレクトせず 401
		if !a.Configured() || a.SessionHash(sessionToken(q.r)) == "" {
			s.send(q, 401, []byte(`{"error":"login required"}`), "application/json; charset=utf-8")
			return
		}
		var data string
		if !s.safe(q, func() { data = notificationsJSON(q.data().urgentItems()) }) {
			return
		}
		s.send(q, 200, []byte(data), "application/json; charset=utf-8")
		return
	}
	if !a.Configured() {
		s.redirect(q, "/setup")
		return
	}
	sh := a.SessionHash(sessionToken(q.r))
	if sh == "" {
		nx := path
		if query != "" {
			nx += "?" + query
		}
		s.redirect(q, "/login?"+url.Values{"next": {SafeNext(nx)}}.Encode())
		return
	}
	logout := `<form method=post action="/logout"><input type=hidden name=csrf value="` + E(a.FormToken("s:"+sh)) + `">` +
		"<button type=submit>ログアウト</button></form>"
	const js = "text/javascript; charset=utf-8"
	switch path {
	case "/notify.js":
		b, err := s.readStatic("notify.js.tmpl")
		if err != nil {
			s.text(q, 404, "Not Found")
			return
		}
		base, _ := jsonString(q.base())
		out := strings.Replace(string(b), "__BASE__", base, 1)
		out = strings.Replace(out, "__POLL__", strconv.Itoa(NotifyPollSec*1000), 1)
		s.send(q, 200, []byte(out), js)
	case "/refresh.js", "/filter.js", "/docs.js":
		s.staticFile(q, path[1:], js)
	case "/static/mermaid.min.js":
		s.staticFile(q, "mermaid.min.js", js)
	default:
		s.safe(q, func() { s.route(q, path, query, logout, a.FormToken("s:"+sh)) })
	}
}

// safe は fn の panic を 500 にする(1 画面の失敗でサーバーを落とさない。中身は出さない)。fn が最後まで動けば true。
func (s *Server) safe(q *rq, fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("internal error: %T\n", r)
			if q.status == 0 {
				s.text(q, 500, "Internal Server Error")
			}
			ok = false
		}
	}()
	fn()
	return true
}

// route は views.route(と handler.py の /jobs/<job>)。
func (s *Server) route(q *rq, path, query, logout, csrf string) {
	if p, err := url.PathUnescape(path); err == nil {
		path = p
	}
	if path != "/" && strings.HasSuffix(path, "/") {
		path = strings.TrimRight(path, "/")
	}
	if name, ok := strings.CutPrefix(path, "/jobs/"); ok && name != "" && !strings.Contains(name, "/") { // 設定フォーム(自動更新しない)
		if !JobRE.MatchString(name) || !contains(q.data().jobNames(), name) {
			s.html(q, 404, s.page(q, "見つかりません", "<p>そのページはありません。</p>", "", logout, pageOpts{}))
			return
		}
		saved := pyParseQS(query)["saved"]
		title, body := s.pageJob(q, name, csrf, "", len(saved) == 1 && saved[0] == "1")
		s.html(q, 200, s.page(q, title, body, "/jobs/"+name, logout, pageOpts{norefresh: true}))
		return
	}
	opts := &pageOpts{}
	if title, body, ok, handled := s.routeDetail(q, path, query, csrf, opts); handled { // 見つからなければ 404 になる画面
		if !ok {
			s.html(q, 404, s.page(q, "見つかりません", "<p>そのページはありません。</p>", "", logout, pageOpts{}))
			return
		}
		s.html(q, 200, s.page(q, title, body, path, logout, *opts))
		return
	}
	if fn, ok := pages[path]; ok {
		title, body := fn(s, q, pageCtx{csrf: csrf, opts: opts, qs: pyParseQS(query)})
		s.html(q, 200, s.page(q, title, body, path, logout, *opts))
		return
	}
	s.html(q, 404, s.page(q, "見つかりません", "<p>そのページはありません。</p>", "", logout, pageOpts{}))
}

func (s *Server) staticFile(q *rq, name, ctype string) {
	b, err := s.readStatic(name)
	if err != nil {
		s.text(q, 404, "Not Found")
		return
	}
	s.send(q, 200, b, ctype)
}

var reDetail = regexp.MustCompile(`^/(agents|tasks)/([^/]+)$`)

// routeDetail は、対象が無ければ 404 を返す画面(/docs/view、/tasks/<id>、/agents/<key>)。handled=false ならここでは扱わない。
func (s *Server) routeDetail(q *rq, path, query, csrf string, opts *pageOpts) (title, body string, ok, handled bool) {
	c := pageCtx{csrf: csrf, opts: opts, qs: pyParseQS(query)}
	if path == "/docs/view" {
		title, body, ok = s.pageDoc(q, c)
		return title, body, ok, true
	}
	if m := reDetail.FindStringSubmatch(path); m != nil {
		key := pyUnquote(m[2]) // views.route と同じく、もう一度 unquote する
		if m[1] == "agents" {
			title, body, ok = s.pageAgent(q, key)
		} else {
			title, body, ok = s.pageTask(q, key, c)
		}
		return title, body, ok, true
	}
	return "", "", false, false
}

func (s *Server) post(q *rq, path string) {
	a := s.Auth
	if !s.originOK(q.r) {
		s.text(q, 403, "Forbidden: Origin/Referer が不正です")
		return
	}
	form := body(q.r)
	if form == nil {
		s.text(q, 413, "Payload Too Large")
		return
	}
	tok := sessionToken(q.r)
	sh := a.SessionHash(tok)
	authed := strings.HasPrefix(path, "/jobs/") || path == "/resolve" || path == "/agent-op" || path == "/agent-new"
	if authed && sh == "" {
		s.redirect(q, "/login")
		return
	}
	ctx := "anon"
	if (path == "/logout" || authed) && sh != "" {
		ctx = "s:" + sh
	}
	if !a.CheckFormToken(form["csrf"], ctx) {
		if path == "/logout" && sh == "" {
			s.redirect(q, "/login")
			return
		}
		s.text(q, 403, "Forbidden: フォームの有効期限切れです。画面を開き直してください")
		return
	}
	if path == "/resolve" { // 報告を対応済みにする(書き込みは tb resolve)
		s.safe(q, func() { s.postResolve(q, form) })
		return
	}
	if name, ok := strings.CutPrefix(path, "/jobs/"); ok { // 定期実行のスケジュール(書き込みは tb job)
		s.safe(q, func() { s.postJob(q, path, name, form, a.FormToken("s:"+sh)) })
		return
	}
	if path == "/agent-op" { // agent の停止・復活と、承認待ちの操作の承認・却下(書き込みは tb が行う。Web UI からは人の操作)
		s.safe(q, func() { s.postAgentOp(q, form) })
		return
	}
	if path == "/agent-new" {
		s.safe(q, func() { s.postAgentNew(q, form, a.FormToken("s:"+sh)) })
		return
	}
	switch path {
	case "/logout":
		a.DestroySession(tok)
		s.redirect(q, "/login", cookie("", 0))
	case "/setup":
		if a.Configured() {
			s.notFoundPlain(q)
			return
		}
		user, pw, conf := strings.TrimSpace(form["username"]), form["password"], form["confirm"]
		errMsg := ""
		if user == "" || runeLen(user) > 64 || Clean(user) != user {
			errMsg = "ユーザー名は 1〜64 文字で入力してください"
		} else {
			errMsg = passwordError(pw, conf)
		}
		if errMsg != "" {
			s.html(q, 400, s.setupPage(q, errMsg))
			return
		}
		ok, err := a.Setup(user, pw)
		if err != nil {
			s.internal(q, err)
			return
		}
		if !ok {
			s.notFoundPlain(q)
			return
		}
		s.logf("setup: 認証情報を作成しました\n")
		t, err := a.CreateSession()
		if err != nil {
			s.internal(q, err)
			return
		}
		s.redirect(q, "/", cookie(t, SessionTTL()))
	case "/reset":
		if !a.Configured() {
			s.redirect(q, "/setup")
			return
		}
		st := a.ResetState()
		errMsg := "denied"
		if st == "ok" {
			errMsg = passwordError(form["password"], form["confirm"])
		}
		if errMsg == "" {
			if st = a.ResetPassword(form["password"]); st == "done" {
				s.logf("reset: ok(全セッションを無効化)\n")
				s.redirect(q, "/login")
				return
			}
			errMsg = "denied"
		}
		time.Sleep(FailDelay())
		if errMsg == "denied" {
			s.logf("reset: denied (%s)\n", st)
			s.html(q, 403, s.resetPage(q, st, ""))
			return
		}
		s.html(q, 400, s.resetPage(q, st, errMsg))
	case "/login":
		if !a.Configured() {
			s.redirect(q, "/setup")
			return
		}
		nx := "/"
		if v, ok := form["next"]; ok {
			nx = v
		}
		nx = SafeNext(nx)
		if wait := a.LockedFor(); wait > 0 {
			s.logf("login: locked\n")
			s.html(q, 429, s.plainPage(q, "ログイン", errHTML(fmt.Sprintf("失敗が続いたため一時的にロックしています。約 %d 秒後にやり直してください", wait))),
				[2]string{"Retry-After", strconv.Itoa(wait)})
			return
		}
		if a.Attempt(runeCut(form["username"], MaxField), runeCut(form["password"], MaxField)) {
			s.logf("login: ok\n")
			t, err := a.CreateSession()
			if err != nil {
				s.internal(q, err)
				return
			}
			s.redirect(q, nx, cookie(t, SessionTTL()))
			return
		}
		s.logf("login: failed\n")
		time.Sleep(FailDelay())
		msg := loginFail
		if a.LockedFor() > 0 {
			msg += "(ロックしました)"
		}
		s.html(q, 401, s.loginPage(q, nx, msg))
	}
}

// internal は 500(中身は出さない)。
func (s *Server) internal(q *rq, err error) {
	s.logf("internal error: %T\n", err)
	s.text(q, 500, "Internal Server Error")
}

// ---- 起動

func startPort() (int, error) {
	v := os.Getenv("TB_UI_GATEWAY_PORT")
	if v == "" {
		v = strconv.Itoa(DefPort)
	}
	n, err := strconv.Atoi(v)
	if err != nil || strings.TrimLeft(v, "0123456789") != "" || n < 1 || n > 65535 {
		return 0, fmt.Errorf("herdr-webui: TB_UI_GATEWAY_PORT は 1〜65535 の数字: %s", v)
	}
	return n, nil
}

// bind は start から空いているポートを探す(ほかのアプリが使っていればずらす)。127.0.0.1 だけ。
func bind(start int) (net.Listener, int, error) {
	end := min(start+Shift, 65535)
	var last error = errors.New("使用中")
	for p := start; p <= end; p++ {
		addr := net.JoinHostPort(Host, strconv.Itoa(p))
		// macOS では SO_REUSEADDR の bind が待ち受け中のポートにも通るので、先に接続を試す
		if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			c.Close()
			last = errors.New("使用中")
			continue
		}
		l, err := net.Listen("tcp", addr)
		if err == nil {
			return l, p, nil
		}
		last = err
	}
	return nil, 0, fmt.Errorf("herdr-webui: ポート %d〜%d がすべて使えません: %v", start, end, last)
}

// Serve は herdrctl webui serve の本体。終了コードを返す。
func Serve(stdout, stderr io.Writer) int {
	ApplySession() // checkout の herdr-session ファイルでセッションを固定していれば、herdr にも渡す(/ の行き先)
	if g := ReadGateway(true); g != nil {
		fmt.Fprintf(stderr, "herdr-webui: 既にポート %d で動いています(pid %d): http://%s:%d/\n", g.Port, g.PID, Host, g.Port)
		return 1
	}
	runDir := RunDir()
	if err := os.MkdirAll(runDir, 0o777); err != nil {
		fmt.Fprintln(stderr, "herdr-webui: "+err.Error())
		return 1
	}
	a, err := NewAuth(AuthDir())
	if err != nil {
		fmt.Fprintln(stderr, "herdr-webui: "+err.Error())
		return 1
	}
	start, err := startPort()
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	l, port, err := bind(start)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	s := &Server{Auth: a, Port: port, Log: stderr}
	if err := WriteGateway(port); err != nil {
		fmt.Fprintf(stdout, "herdr-webui: 待ち受けたポートを記録できません: %T\n", err)
	}
	portFile := filepath.Join(runDir, "port")
	_ = os.WriteFile(portFile, []byte(strconv.Itoa(port)+"\n"), 0o666)
	u := fmt.Sprintf("http://%s:%d/s/%s", Host, port, SessionName())
	fmt.Fprintf(stdout, "herdr-webui: listening on http://%s:%d/(全 herdr セッション。/s/<セッション名>/)\n", Host, port)
	if a.Configured() {
		fmt.Fprintf(stdout, "herdr-webui: 開く %s/(ログインは全セッション共通)\n", u)
	} else {
		fmt.Fprintf(stdout, "herdr-webui: 初回設定(ユーザー名とパスワードを作る。全セッション共通) %s/setup\n", u)
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second, ErrorLog: nil}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	err = srv.Serve(l)
	ClearGateway()
	os.Remove(portFile)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "herdr-webui: "+err.Error())
		return 1
	}
	return 0
}
