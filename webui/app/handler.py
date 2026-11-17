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

from . import registry
from .httpbase import host_ok, log_request_line, origins, send
from .config import BASE, COOKIE, HOST, INSTANCE, herdr_env, JOB_RE, LEGACY_RUN_DIR, ROOT, RUN_DIR, cfg
from .sanitize import clean, e
from .actions import agent_decide, agent_new, agent_op, resolve_comment
from .data import NOTIFY_POLL_SEC, badge_count, urgent_items
from .jobs import job_names, save_job_settings
from .scripts import DOCS_JS, SESSIONS_JS, notify_js
from .views import CSS, err_html, inst_label, page, page_agent_new, page_job, route, title_suffix

NOTIFY_JS = notify_js(BASE, NOTIFY_POLL_SEC * 1000)


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
# gateway の後ろ(BASE=/s/<名前>)では、HTML のサーバー内の絶対パス(href="/…" など)を BASE の下に付け替える。
# 外部 URL(http:)・// 始まり・相対パスはそのまま。属性値は e() で " を逃がしているので、本文の中の文字列は当たらない
_ABS_ATTR = re.compile(r'(\s(?:href|action|src)=")/(?![/\\])')


def with_base(html):
    return _ABS_ATTR.sub(lambda m: m.group(1) + BASE + "/", html) if BASE else html


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
        if BASE and isinstance(body, str) and ctype.startswith("text/html"):
            body = with_base(body)
        send(self, code, body.encode("utf-8") if isinstance(body, str) else body, ctype, extra)

    def _text(self, code, msg, extra=()):
        self._send(code, msg + "\n", "text/plain; charset=utf-8", extra)

    def _redirect(self, loc, extra=()):
        # loc はこのサーバー内の絶対パス(safe_next 済みなど)。gateway の後ろでは BASE を付ける
        if BASE and loc.startswith("/") and not loc.startswith("//"):
            loc = BASE + loc
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
        return ("Set-Cookie", f"{COOKIE}={value}; Path={BASE}/; HttpOnly; SameSite=Strict; Max-Age={max_age}")

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
            return self._send(200, NOTIFY_JS, "text/javascript; charset=utf-8")
        if path == "/docs.js":
            return self._send(200, DOCS_JS, "text/javascript; charset=utf-8")
        if path == "/sessions.js":
            return self._send(200, SESSIONS_JS, "text/javascript; charset=utf-8")
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
        if not self._host_ok():
            return self._text(403, "Forbidden")
        u = urlsplit(self.path)
        path = u.path
        if BASE:  # gateway の後ろ: /s/<名前>/… だけを受け、BASE を外してから今までどおり扱う
            if path == BASE:
                return self._send(302, "", "text/plain", [("Location", BASE + "/" + ("?" + u.query if u.query else ""))])
            if not path.startswith(BASE + "/"):
                return self._text(404, "Not Found")
            path = path[len(BASE):]
            self.path = path + ("?" + u.query if u.query else "")
        if self.command == "POST" and (path in ("/login", "/setup", "/logout", "/reset", "/resolve", "/agent-op", "/agent-new")
                                       or re.fullmatch(r"/jobs/[A-Za-z0-9][A-Za-z0-9_-]{0,63}", path)):
            return self._post(path)
        if self.command not in ("GET", "HEAD"):
            return self._text(405, "Method Not Allowed(定期実行の設定・報告の対応済み・agent の操作以外は読み取り専用です)", [("Allow", "GET, HEAD")])
        self._get()

    do_GET = do_HEAD = do_POST = do_PUT = do_DELETE = do_PATCH = do_OPTIONS = _handle


def bind_server():
    """(server, port)。固定指定なら探索しない。全部埋まっていれば SystemExit。"""
    fixed = os.environ.get("TB_UI_PORT")
    try:
        if fixed:
            lo = hi = int(fixed)
            if not 1 <= lo <= 65535:
                raise SystemExit(f"herdr-webui: TB_UI_PORT は 1〜65535 の数字: {fixed}")
        else:
            lo, hi = (int(x) for x in cfg("HERDR_WEBUI_RANGE", "8800-8819").split("-"))
    except ValueError:
        raise SystemExit("herdr-webui: ポート指定が数値ではありません")
    last = None
    if fixed:
        # macOS では SO_REUSEADDR の bind が、0.0.0.0 で待ち受け中のポートにも通ってしまう。先に接続を試して検出する
        # (固定ポートのときだけ。bind の失敗検査は残す)
        with socket.socket() as probe:
            probe.settimeout(0.5)
            if probe.connect_ex((HOST, lo)) == 0:
                raise SystemExit(f"herdr-webui: instance={INSTANCE or 'default'} 固定ポート {lo}(TB_UI_PORT)で既に別のプロセスが待ち受けています。別のポートへは移りません")
    for p in range(lo, hi + 1):
        try:
            return ThreadingHTTPServer((HOST, p), Handler), p
        except OSError as ex:
            last = ex
    if fixed:
        # TB_UI_PORT は「このセッションのポート」の約束なので、別のポートへ黙って逃げない
        raise SystemExit(f"herdr-webui: instance={INSTANCE or 'default'} 固定ポート {lo}(TB_UI_PORT)を使えません。別のポートへは移りません: {last}")
    raise SystemExit(f"herdr-webui: ポート {lo}〜{hi} がすべて使用中です")


def main():
    herdr_env.apply_session()   # checkout の herdr-session ファイルでセッションを固定していれば、herdr にも渡す
    run_dir = RUN_DIR()
    # 旧い場所(run/)の auth.json の引き継ぎは、明示したとき(WEBUI_LEGACY_AUTH=1)だけ。
    # 既定で引き継ぐと、これから作る別インスタンスも default の認証で入れてしまい、別の認証情報を設定できない
    legacy = LEGACY_RUN_DIR() if INSTANCE and not os.environ.get("WEBUI_RUN_DIR") and os.environ.get("WEBUI_LEGACY_AUTH") == "1" else None
    Handler.auth = auth.Auth(run_dir, legacy_dir=legacy)
    if legacy and not Handler.auth.using_legacy() and not Handler.auth.configured():
        print("herdr-webui: WEBUI_LEGACY_AUTH=1 ですが、引き継げる旧い auth.json がありません(無い、または通常ファイル・自分の所有・group/other 書込なし を満たさない)。/setup で作ります", flush=True)
    if Handler.auth.using_legacy():
        print(f"herdr-webui: 旧い {Handler.auth.legacy_auth_path} を読み取りで使います(消しません)。"
              f"{run_dir}/auth.json に移す(コピーする)か、パスワード再設定で新しい場所に作ると、この表示は消えます", flush=True)
    srv, port = bind_server()
    Handler.port = port
    port_file = os.path.join(run_dir, "port")
    inst_file = os.path.join(run_dir, "instance")
    with open(port_file, "w") as f:
        f.write(f"{port}\n")
    with open(inst_file, "w") as f:
        f.write(f"{INSTANCE or 'default'}\n")
    reg_name = INSTANCE or "default"
    try:
        old = registry.read_entry(reg_name)
        if old and old.get("pid") != os.getpid() and old["port"] != port and registry.alive(old):
            print(f"herdr-webui: 同じ名前 {reg_name} のインスタンスがポート {old['port']} で動いています。一覧はこちら(ポート {port})に置き換えます"
                  "(別の checkout なら TB_INSTANCE を分けてください)", flush=True)
        registry.register(reg_name, port, BASE)
    except OSError as ex:
        print(f"herdr-webui: インスタンス一覧({registry.REGISTRY_DIR()})に書けません。セッション一覧・入口には出ません: {type(ex).__name__}", flush=True)
    print(f"herdr-webui: listening on http://{HOST}:{port}{BASE}/", flush=True)
    print(f"herdr-webui: instance={INSTANCE or 'default'} port={port}", flush=True)
    if BASE:
        print(f"herdr-webui: 入口(gateway)経由で開く: http://{HOST}:<入口のポート>{BASE}/", flush=True)
    if Handler.auth.configured():
        print(f"herdr-webui: ログイン http://{HOST}:{port}{BASE}/login", flush=True)
    else:
        print(f"herdr-webui: 初回設定(ユーザー名とパスワードを作る) http://{HOST}:{port}{BASE}/setup", flush=True)

    def stop(*_):
        threading.Thread(target=srv.shutdown, daemon=True).start()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        srv.serve_forever()
    finally:
        srv.server_close()
        registry.unregister(reg_name)
        for fn in (port_file, inst_file):
            try:
                os.remove(fn)
            except OSError:
                pass
