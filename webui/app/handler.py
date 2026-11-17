"""HTTP ハンドラ・ログイン・起動。"""

import json
import os
import re
import shlex
import signal
import socket
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlencode, urlsplit

import auth

from . import registry, reqctx, sessions
from .httpbase import host_ok, log_request_line, origins, send
from .config import AUTH_DIR, COOKIE, HOST, herdr_env, JOB_RE, ROOT, RUN_DIR, SESSION_RE
from .sanitize import clean, e
from .actions import agent_decide, agent_new, agent_op, resolve_comment
from .data import NOTIFY_POLL_SEC, badge_count, urgent_items
from .jobs import job_names, save_job_settings
from .scripts import DOCS_JS, notify_js
from .views import CSS, err_html, inst_label, page, page_agent_new, page_job, route, title_suffix


def plain_page(title, body):
    """ログイン・初回設定用(ナビも自動更新もなし)。"""
    return ("<!doctype html><html lang=ja><head><meta charset=utf-8><meta name=referrer content=same-origin>"
            '<meta name=viewport content="width=device-width,initial-scale=1">'
            f"<title>{e(title)} - herdr{e(title_suffix())}</title><style>{CSS}</style></head><body>"
            f"<main style='max-width:420px'>{inst_label()}<h1>{e(title)}</h1>{body}</main></body></html>")


def form_html(action, csrf, fields, submit, error="", note="", next_=""):
    rows = "".join(
        f'<p><label>{e(label)}<br><input type={typ} name={name} required autocomplete={ac} style="width:100%"></label></p>'
        for name, label, typ, ac in fields)
    nx = f'<input type=hidden name=next value="{e(next_)}">' if next_ else ""
    return (f'{err_html(error)}<p class=note>{e(note)}</p>' if note or error else "") + (
        f'<form method=post action="{action}"><input type=hidden name=csrf value="{e(csrf)}">{nx}{rows}'
        f'<button type=submit>{e(submit)}</button></form>')


FORGOT = '<p class=note><a href="/reset">パスワードを忘れた</a></p>'
SETUP_FIELDS = [("username", "ユーザー名", "text", "username"),
                ("password", f"パスワード({auth.MIN_PASSWORD} 文字以上)", "password", "new-password"),
                ("confirm", "パスワード(確認)", "password", "new-password")]
LOGIN_FIELDS = [("username", "ユーザー名", "text", "username"),
                ("password", "パスワード", "password", "current-password")]


def setup_page(a, error=""):
    return plain_page("初回設定", form_html("/setup", a.form_token("anon"), SETUP_FIELDS, "設定する", error=error,
                                         note="" if error else "最初に 1 回だけ行う設定です。設定後、この画面は閉じます。"))


def login_page(a, next_, error=""):
    return plain_page("ログイン", form_html("/login", a.form_token("anon"), LOGIN_FIELDS, "ログイン", error=error, next_=next_) + FORGOT)


def password_error(pw, conf):
    """新しいパスワードの検査(初回設定・再設定で共通)。問題なければ None。"""
    if len(pw) < auth.MIN_PASSWORD:
        return f"パスワードは {auth.MIN_PASSWORD} 文字以上にしてください"
    if len(pw) > auth.MAX_FIELD:
        return "パスワードが長すぎます"
    if pw != conf:
        return "確認入力が一致しません"
    return None
RESET_DENY = {"missing": "再設定は許可されていません。", "expired": "再設定の許可の期限(10 分)が切れています。",
              "invalid": "再設定の許可ファイルが不正です(通常ファイルで、自分の所有のものだけ有効)。いったん削除して作り直してください。"}


def reset_page(a, state, error=""):
    """state が ok なら新パスワードの設定画面、そうでなければ許可コマンドの案内。"""
    if state == "ok":
        return plain_page("パスワードの再設定", form_html(
            "/reset", a.form_token("anon"),
            [("password", f"新しいパスワード({auth.MIN_PASSWORD} 文字以上)", "password", "new-password"),
             ("confirm", "新しいパスワード(確認)", "password", "new-password")], "再設定する", error=error,
            note="許可の有効期間内に設定してください。成功すると全員のログインが無効になります。"))
    cmd = "touch " + shlex.quote(a.allow_path)
    return plain_page("パスワードの再設定", err_html(RESET_DENY.get(state, "")) +
                      "<p>次のコマンドを手元のシェルで実行して再設定を許可してください(10 分間有効)。実行後にこの画面を再読み込みします。</p>"
                      f"<pre>{e(cmd)}</pre>" + '<p class=note><a href="/login">ログインへ戻る</a></p>')


LOGIN_FAIL = "ユーザー名またはパスワードが違います"
# ページは BASE(/s/<名前>)の下に置くので、HTML のサーバー内の絶対パス(href="/…" など)を BASE の下に付け替える。
# 外部 URL(http:)・// 始まり・相対パスはそのまま。属性値は e() で " を逃がしているので、本文の中の文字列は当たらない
_ABS_ATTR = re.compile(r'(\s(?:href|action|src)=")/(?![/\\])')


def with_base(html):
    base = reqctx.base()
    return _ABS_ATTR.sub(lambda m: m.group(1) + base + "/", html)


class Handler(BaseHTTPRequestHandler):
    server_version = "herdr-webui"
    sys_version = ""
    auth = None
    port = 0

    def log_message(self, fmt, *args):
        log_request_line(self, args)

    _static_cache = {}

    def _send_static(self, name, ctype):
        """webui/static/ の同梱ファイル(名前は呼び出し側で固定)。ログイン後だけ。"""
        data = self._static_cache.get(name)
        if data is None:
            try:
                with open(os.path.join(ROOT, "static", name), "rb") as f:
                    data = self._static_cache[name] = f.read()
            except OSError:
                return self._text(404, "Not Found")
        return self._send(200, data, ctype)

    def _send(self, code, body, ctype="text/html; charset=utf-8", extra=()):
        if isinstance(body, str) and ctype.startswith("text/html"):
            body = with_base(body)
        send(self, code, body.encode("utf-8") if isinstance(body, str) else body, ctype, extra)

    def _text(self, code, msg, extra=()):
        self._send(code, msg + "\n", "text/plain; charset=utf-8", extra)

    def _redirect(self, loc, extra=()):
        # loc はこのサーバー内の絶対パス(safe_next 済みなど)。BASE を付ける
        if loc.startswith("/") and not loc.startswith("//"):
            loc = reqctx.base() + loc
        self._send(302, "", "text/plain", [("Location", loc), *extra])

    def _origins(self):
        return origins(self.port)

    def _host_ok(self):
        return host_ok(self, self.port)

    def _origin_ok(self):
        o, ref = self.headers.get("Origin"), self.headers.get("Referer")
        if o is not None:
            return o in self._origins()
        if ref is not None:
            return any(ref == b or ref.startswith(b + "/") for b in self._origins())
        return False

    def _session_token(self):
        for part in (self.headers.get("Cookie") or "").split(";"):
            k, _, v = part.strip().partition("=")
            if k == COOKIE:
                return v
        return None

    def _session(self):
        return self.auth.session_hash(self._session_token())

    def _cookie(self, value, max_age):
        # ログインは全セッションで共通なので Path=/(どの /s/<セッション名>/ にも付く)
        return ("Set-Cookie", f"{COOKIE}={value}; Path=/; HttpOnly; SameSite=Strict; Max-Age={max_age}")

    def _body(self):
        try:
            n = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            n = -1
        if n < 0 or n > 8192:
            return None
        raw = self.rfile.read(n).decode("utf-8", "replace")
        return {k: v[0] for k, v in parse_qs(raw, keep_blank_values=True).items()}

    # ---- GET
    def _get(self):
        u = urlsplit(self.path)
        path = u.path
        qs = parse_qs(u.query)
        a = self.auth
        if path == "/setup":
            if a.configured():
                return self._send(404, plain_page("見つかりません", "<p>そのページはありません。</p>"))
            return self._send(200, setup_page(a))
        if path == "/login":
            if not a.configured():
                return self._redirect("/setup")
            nx = auth.safe_next((qs.get("next") or ["/"])[0])
            if self._session():
                return self._redirect(nx)
            return self._send(200, login_page(a, nx))
        if path == "/reset":
            if not a.configured():
                return self._redirect("/setup")
            st = a.reset_state()
            if st in ("expired", "invalid"):
                sys.stderr.write(f"reset: denied ({st})\n")
            return self._send(200, reset_page(a, st))
        if path == "/logout":
            return self._text(405, "Method Not Allowed", [("Allow", "POST")])
        if path == "/api/notifications":  # スクリプト用。未ログインはリダイレクトせず 401
            if not a.configured() or not self._session():
                return self._send(401, '{"error":"login required"}', "application/json; charset=utf-8")
            try:
                data = json.dumps({"items": urgent_items(), "poll_sec": NOTIFY_POLL_SEC}, ensure_ascii=False)
            except Exception as ex:
                sys.stderr.write(f"internal error: {type(ex).__name__}\n")
                return self._text(500, "Internal Server Error")
            return self._send(200, data, "application/json; charset=utf-8")
        if not a.configured():
            return self._redirect("/setup")
        sh = self._session()
        if not sh:
            nx = u.path + (("?" + u.query) if u.query else "")
            return self._redirect("/login?" + urlencode({"next": auth.safe_next(nx)}))
        logout = (f'<form method=post action="/logout"><input type=hidden name=csrf value="{e(a.form_token("s:" + sh))}">'
                  '<button type=submit>ログアウト</button></form>')
        if path == "/notify.js":
            return self._send(200, notify_js(reqctx.base(), NOTIFY_POLL_SEC * 1000), "text/javascript; charset=utf-8")
        if path == "/docs.js":
            return self._send(200, DOCS_JS, "text/javascript; charset=utf-8")
        if path == "/static/mermaid.min.js":
            return self._send_static("mermaid.min.js", "text/javascript; charset=utf-8")
        try:
            m = re.fullmatch(r"/jobs/([^/]+)/?", unquote(path))
            if m:  # 設定フォーム(自動更新しない)
                name = m.group(1)
                if not JOB_RE.fullmatch(name) or name not in job_names():
                    return self._send(404, page("見つかりません", "<p>そのページはありません。</p>", "", badge_count(), logout))
                title, body = page_job(name, a.form_token("s:" + sh), saved=qs.get("saved") == ["1"])
                return self._send(200, page(title, body, "/jobs/" + name, badge_count(), logout, refresh=False))
            ctx = {"csrf": a.form_token("s:" + sh)}
            r = route(unquote(path), qs, ctx)
            if r is None:
                return self._send(404, page("見つかりません", "<p>そのページはありません。</p>", "", badge_count(), logout))
            title, body = r
            self._send(200, page(title, body, path, badge_count(), logout, refresh=not ctx.get("norefresh"),
                                 scripts=ctx.get("scripts", ())))
        except Exception as ex:  # 1 ページの失敗でサーバーを落とさない(例外の内容は出さない)
            sys.stderr.write(f"internal error: {type(ex).__name__}\n")
            self._text(500, "Internal Server Error")

    # ---- POST(/login /setup /logout /reset と、定期実行の設定 /jobs/<job>、報告の対応済み /resolve、agent の操作 /agent-op /agent-new だけ)
    def _post(self, path):
        a = self.auth
        if not self._origin_ok():
            return self._text(403, "Forbidden: Origin/Referer が不正です")
        form = self._body()
        if form is None:
            return self._text(413, "Payload Too Large")
        sh = self._session()
        job = path.startswith("/jobs/")
        authed = job or path in ("/resolve", "/agent-op", "/agent-new")
        if authed and not sh:
            return self._redirect("/login")
        ctx = "s:" + sh if (path == "/logout" or authed) and sh else "anon"
        if not a.check_form_token(form.get("csrf"), ctx):
            if path == "/logout" and not sh:
                return self._redirect("/login")
            return self._text(403, "Forbidden: フォームの有効期限切れです。画面を開き直してください")
        if job:
            name = path[len("/jobs/"):]
            if not JOB_RE.fullmatch(name) or name not in job_names():
                return self._text(404, "Not Found")
            try:
                err = save_job_settings(name, form)
            except OSError:
                err = "設定を書き込めませんでした"
            if err:
                title, body = page_job(name, a.form_token("s:" + sh), error=err)
                return self._send(400, page(title, body, path, badge_count(), refresh=False))
            sys.stderr.write(f"jobs: settings {'reset' if form.get('action') == 'reset' else 'saved'} {name}\n")
            return self._redirect(f"/jobs/{name}?saved=1")
        if path == "/resolve":  # 報告を対応済みにする(書き込みは tb resolve)
            cid, back = form.get("id", ""), form.get("back", "/inbox")
            if not re.fullmatch(r"[0-9]{1,9}", cid):
                return self._text(400, "Bad Request")
            if not re.fullmatch(r"/inbox|/tasks/[A-Za-z0-9_%-]{1,200}", back):
                back = "/inbox"
            st = resolve_comment(int(cid), form.get("note", ""))
            sys.stderr.write(f"resolve: C-{cid} {st}\n")
            return self._redirect(back + "?" + urlencode({"resolved": st}))
        if path == "/agent-op":  # agent の停止・復活と、承認待ちの操作の承認・却下(書き込みは tb が行う。Web UI からは人の操作)
            op = form.get("op", "")
            if op in ("approve", "reject"):
                lid = form.get("id", "")
                if not re.fullmatch(r"[0-9]{1,9}", lid):
                    return self._text(400, "Bad Request")
                st = agent_decide(int(lid), op == "approve")
                sys.stderr.write(f"agent-op: {op} L-{lid} {st}\n")
            elif op in ("stop", "force-stop", "revive", "revive-fresh"):
                name = form.get("name", "")
                if not re.fullmatch(r"[a-z][a-z0-9_-]{0,31}", name):
                    return self._text(400, "Bad Request")
                if op == "force-stop" and form.get("confirm") != "1":
                    return self._redirect("/agents?" + urlencode({"op": "refused"}))
                st = agent_op(op, name)
                sys.stderr.write(f"agent-op: {op} {name} {st}\n")
            else:
                return self._text(400, "Bad Request")
            return self._redirect("/agents?" + urlencode({"op": st}))
        if path == "/agent-new":
            st, err = agent_new(form)
            sys.stderr.write(f"agent-new: {st}\n")
            if st in ("done", "pending"):
                return self._redirect("/agents?" + urlencode({"op": st}))
            title, body = page_agent_new(a.form_token("s:" + sh), error=err, form=form)
            return self._send(400, page(title, body, "/agents", badge_count(), refresh=False))
        if path == "/logout":
            a.destroy_session(self._session_token())
            return self._redirect("/login", [self._cookie("", 0)])
        if path == "/setup":
            if a.configured():
                return self._send(404, plain_page("見つかりません", "<p>そのページはありません。</p>"))
            user, pw, conf = form.get("username", "").strip(), form.get("password", ""), form.get("confirm", "")
            err = ("ユーザー名は 1〜64 文字で入力してください" if not user or len(user) > 64 or clean(user) != user
                   else password_error(pw, conf))
            if err:
                return self._send(400, setup_page(a, err))
            if not a.setup(user, pw):
                return self._send(404, plain_page("見つかりません", "<p>そのページはありません。</p>"))
            sys.stderr.write("setup: 認証情報を作成しました\n")
            tok = a.create_session()
            return self._redirect("/", [self._cookie(tok, auth.session_ttl())])
        if path == "/reset":
            if not a.configured():
                return self._redirect("/setup")
            st = a.reset_state()
            pw, conf = form.get("password", ""), form.get("confirm", "")
            err = "denied" if st != "ok" else password_error(pw, conf)
            if not err:
                st = a.reset_password(pw)
                if st == "done":
                    sys.stderr.write("reset: ok(全セッションを無効化)\n")
                    return self._redirect("/login")
                err = "denied"
            time.sleep(auth.fail_delay())  # 失敗の遅延
            if err == "denied":
                sys.stderr.write(f"reset: denied ({st})\n")
                return self._send(403, reset_page(a, st))
            return self._send(400, reset_page(a, st, err))
        # /login
        if not a.configured():
            return self._redirect("/setup")
        nx = auth.safe_next(form.get("next", "/"))
        wait = a.locked_for()
        if wait:
            sys.stderr.write("login: locked\n")
            return self._send(429, plain_page("ログイン", err_html(f"失敗が続いたため一時的にロックしています。約 {wait} 秒後にやり直してください")),
                              extra=[("Retry-After", str(wait))])
        if a.attempt(form.get("username", "")[:auth.MAX_FIELD], form.get("password", "")[:auth.MAX_FIELD]):
            sys.stderr.write("login: ok\n")
            tok = a.create_session()
            return self._redirect(nx, [self._cookie(tok, auth.session_ttl())])
        sys.stderr.write("login: failed\n")
        time.sleep(auth.fail_delay())  # 失敗の遅延
        wait = a.locked_for()
        return self._send(401, login_page(a, nx, LOGIN_FAIL + ("(ロックしました)" if wait else "")))

    def _handle(self):
        """/s/<セッション名>/… を、そのセッションの画面として扱う(セッションはリクエストごと。app/reqctx.py)。"""
        if not self._host_ok():
            return self._text(403, "Forbidden")
        u = urlsplit(self.path)
        if u.scheme or u.netloc:
            return self._text(400, "Bad Request")
        q = "?" + u.query if u.query else ""
        if u.path == "/":   # このプロセスの herdr セッションへ
            return self._send(302, "", "text/plain", [("Location", f"/s/{herdr_env.session_name()}/")])
        m = re.fullmatch(r"/s/([^/]+)(/.*)?", u.path)
        if not m:
            return self._text(404, "Not Found")
        name = m.group(1)
        if not SESSION_RE.fullmatch(name) or not sessions.is_known(name):
            return self._text(404, "Not Found(その herdr セッションはありません。/ からこのプロセスのセッションへ)")
        with reqctx.use(name, name):
            if not m.group(2):   # /s/<名前> → /s/<名前>/
                return self._send(302, "", "text/plain", [("Location", reqctx.base() + "/" + q)])
            self._dispatch(m.group(2), q)

    def _dispatch(self, path, q):
        self.path = path + q
        if self.command == "POST" and (path in ("/login", "/setup", "/logout", "/reset", "/resolve", "/agent-op", "/agent-new")
                                       or re.fullmatch(r"/jobs/[A-Za-z0-9][A-Za-z0-9_-]{0,63}", path)):
            return self._post(path)
        if self.command not in ("GET", "HEAD"):
            return self._text(405, "Method Not Allowed(定期実行の設定・報告の対応済み・agent の操作以外は読み取り専用です)", [("Allow", "GET, HEAD")])
        self._get()

    do_GET = do_HEAD = do_POST = do_PUT = do_DELETE = do_PATCH = do_OPTIONS = _handle


DEFAULT_PORT = 8800
SHIFT = 19   # 既定のポートが埋まっていれば、+1 ずつ最大この数だけずらす


def start_port():
    v = os.environ.get("TB_UI_GATEWAY_PORT") or str(DEFAULT_PORT)
    if not v.isdigit() or not 1 <= int(v) <= 65535:
        raise SystemExit(f"herdr-webui: TB_UI_GATEWAY_PORT は 1〜65535 の数字: {v}")
    return int(v)


def bind_server(start):
    """(server, port)。start から空いているポートを探す(ほかのアプリが使っていればずらす)。127.0.0.1 だけ。"""
    last = None
    for p in range(start, min(start + SHIFT, 65535) + 1):
        with socket.socket() as probe:   # macOS では SO_REUSEADDR の bind が待ち受け中のポートにも通るので、先に接続を試す
            probe.settimeout(0.5)
            if probe.connect_ex((HOST, p)) == 0:
                last = "使用中"
                continue
        try:
            return ThreadingHTTPServer((HOST, p), Handler), p
        except OSError as ex:
            last = ex
    raise SystemExit(f"herdr-webui: ポート {start}〜{min(start + SHIFT, 65535)} がすべて使えません: {last}")


def main():
    herdr_env.apply_session()   # checkout の herdr-session ファイルでセッションを固定していれば、herdr にも渡す(/ の行き先)
    running = registry.read_gateway()
    if running:
        raise SystemExit(f"herdr-webui: 既にポート {running['port']} で動いています(pid {running['pid']}): http://{HOST}:{running['port']}/")
    run_dir = RUN_DIR()
    os.makedirs(run_dir, exist_ok=True)
    Handler.auth = auth.Auth(AUTH_DIR())
    srv, port = bind_server(start_port())
    Handler.port = port
    try:
        registry.write_gateway(port)
    except OSError as ex:
        print(f"herdr-webui: 待ち受けたポートを記録できません: {type(ex).__name__}", flush=True)
    port_file = os.path.join(run_dir, "port")
    with open(port_file, "w") as f:
        f.write(f"{port}\n")
    url = f"http://{HOST}:{port}/s/{herdr_env.session_name()}"
    print(f"herdr-webui: listening on http://{HOST}:{port}/(全 herdr セッション。/s/<セッション名>/)", flush=True)
    if Handler.auth.configured():
        print(f"herdr-webui: 開く {url}/(ログインは全セッション共通)", flush=True)
    else:
        print(f"herdr-webui: 初回設定(ユーザー名とパスワードを作る。全セッション共通) {url}/setup", flush=True)

    def stop(*_):
        threading.Thread(target=srv.shutdown, daemon=True).start()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        srv.serve_forever()
    finally:
        srv.server_close()
        registry.clear_gateway()
        try:
            os.remove(port_file)
        except OSError:
            pass
