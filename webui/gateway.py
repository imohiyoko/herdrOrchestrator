#!/usr/bin/env python3
"""herdr Web UI の入口(gateway)。1 つのポートで受け、/s/<インスタンス名>/… をそのインスタンスへ中継する。標準ライブラリだけ。

- 各インスタンス(server.py)は今までどおり別プロセス・別ポート・別ログインで、自分の herdr セッションだけを見る。
  インスタンスはページを常に /s/<名前>/ の下に置く。入口の既定ポートは 8800、インスタンスは 8801〜8819。
- 行き先はホームのインスタンス一覧(app/registry.py)だけ。127.0.0.1 の、そこに書かれたポートにしかつながない。
- 入口自身はログインを持たない。/ はインスタンスの名前とポートの一覧だけを出す(中身は各インスタンスのログインの後ろ)。
- 127.0.0.1 だけで待ち受け、Host が自分自身のときだけ応える(DNS リバインディング対策)。
  中継するときは Host と、入口から来た Origin / Referer を、そのインスタンスのものに読み替える(インスタンス側の検査はそのまま効く)。
"""

import http.client
import os
import socket
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

from app import httpbase, registry
from app.config import HOST
from app.sanitize import e
from app.scripts import GATEWAY_JS

DEFAULT_PORT = 8800
MAX_BODY = 64 * 1024
TIMEOUT = 60
# 中継しないヘッダ(hop-by-hop)。Connection に並んだ名前も落とす
HOP = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer",
       "transfer-encoding", "upgrade"}

CSS = ("body{font-family:-apple-system,system-ui,sans-serif;margin:24px;color:#1f2328}table{border-collapse:collapse}"
       "th,td{border:1px solid #d0d7de;padding:4px 10px;text-align:left}.note{color:#57606a;font-size:13px}.bad{color:#cf222e}")
CSP = httpbase.CSP.replace("form-action 'self'", "form-action 'none'")   # 入口自身はフォームを出さない


def gateway_port():
    v = os.environ.get("TB_UI_GATEWAY_PORT") or str(DEFAULT_PORT)
    if not v.isdigit() or not 1 <= int(v) <= 65535:
        raise SystemExit(f"herdr-webui-gateway: TB_UI_GATEWAY_PORT は 1〜65535 の数字: {v}")
    return int(v)


def index_html():
    rows = []
    for x in registry.entries():
        up = registry.alive(x)
        link = f'<a href="{e(x["base"])}/">{e(x["name"])}</a>'
        cnt = f'<td data-api="{e(x["base"])}/api/notifications">…</td>' if up else "<td>-</td>"
        rows.append(f"<tr><td>{link}</td>{cnt}<td>{'動いている' if up else '<span class=bad>応答なし</span>'}</td>"
                    f"<td>{x['port']}</td></tr>")
    table = ("<table><tr><th>セッション</th><th>緊急通知</th><th>状態</th><th>ポート</th></tr>" + "".join(rows) + "</table>"
             if rows else f"<p class=note>(動いているインスタンスがありません。一覧の置き場所: {e(registry.REGISTRY_DIR())})</p>")
    return ("<!doctype html><html lang=ja><head><meta charset=utf-8><meta name=referrer content=same-origin>"
            '<meta name=viewport content="width=device-width,initial-scale=1"><title>herdr セッション一覧</title>'
            f'<style>{CSS}</style><script src="/gateway.js" defer></script></head><body><h1>herdr セッション一覧</h1>'
            "<p>herdr セッションごとの Web UI です。開くと、そのセッションのログインが求められます(ログインはセッションごとに別)。"
            "件数は緊急通知の数で、ログイン済みのセッションだけ出ます。</p>" + table + "</body></html>")


class Gateway(BaseHTTPRequestHandler):
    server_version = "herdr-webui-gateway"
    sys_version = ""
    protocol_version = "HTTP/1.0"   # 1 リクエスト 1 接続(中継を単純に保つ)
    port = 0

    def log_message(self, fmt, *args):
        httpbase.log_request_line(self, args)

    def _send(self, code, body, ctype="text/html; charset=utf-8", extra=()):
        httpbase.send(self, code, body.encode("utf-8") if isinstance(body, str) else body, ctype, extra, CSP)

    def _text(self, code, msg, extra=()):
        self._send(code, msg + "\n", "text/plain; charset=utf-8", extra)

    def _handle(self):
        if not httpbase.host_ok(self, self.port):
            return self._text(403, "Forbidden")
        u = urlsplit(self.path)
        if u.scheme or u.netloc:
            return self._text(400, "Bad Request")
        path = u.path
        if path in ("/", "/gateway.js"):
            if self.command not in ("GET", "HEAD"):
                return self._text(405, "Method Not Allowed", [("Allow", "GET, HEAD")])
            if path == "/":
                return self._send(200, index_html())
            return self._send(200, GATEWAY_JS, "text/javascript; charset=utf-8")
        m = registry.NAME_IN_BASE.fullmatch(path)
        if not m:
            return self._text(404, "Not Found")
        name = m.group(1).lower()
        if not m.group(2):   # /s/<名前> → /s/<名前>/
            return self._send(302, "", "text/plain", [("Location", f"/s/{name}/" + ("?" + u.query if u.query else ""))])
        if m.group(1) != name:  # インスタンスは小文字で置く(Cookie の Path も小文字)
            return self._send(302, "", "text/plain", [("Location", f"/s/{name}{m.group(2)}" + ("?" + u.query if u.query else ""))])
        ent = registry.read_entry(name)
        if not ent:
            return self._send(502, f"<p>セッション <strong>{e(name)}</strong> の Web UI は動いていません(一覧にありません)。<a href=\"/\">一覧へ</a></p>")
        if not registry.pid_ok(ent):   # 強制終了で残った古い行。そのポートを今は別のプロセスが使っているかもしれないので、つながない
            return self._send(502, f"<p>セッション <strong>{e(name)}</strong> の Web UI は止まっています(一覧に古い行が残っています)。<a href=\"/\">一覧へ</a></p>")
        self._proxy(ent)

    def _proxy(self, ent):
        if "chunked" in (self.headers.get("Transfer-Encoding") or "").lower():
            return self._text(411, "Length Required")
        try:
            n = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            n = -1
        if n < 0 or n > MAX_BODY:
            return self._text(413, "Payload Too Large")
        body = self.rfile.read(n) if n else None
        port = ent["port"]
        drop = HOP | {x.strip().lower() for x in (self.headers.get("Connection") or "").split(",") if x.strip()}
        hdrs = []
        for k, v in self.headers.items():
            lk = k.lower()
            if lk in drop or lk in ("host", "content-length", "forwarded") or lk.startswith("x-forwarded-"):
                continue
            if lk in ("origin", "referer"):
                v = self._rewrite_origin(v, port)
            hdrs.append((k, v))
        hdrs.append(("Host", f"127.0.0.1:{port}"))
        if body is not None:
            hdrs.append(("Content-Length", str(len(body))))
        try:
            c = http.client.HTTPConnection("127.0.0.1", port, timeout=TIMEOUT)
            c.putrequest(self.command, self.path, skip_host=True, skip_accept_encoding=True)
            for k, v in hdrs:
                c.putheader(k, v)
            c.endheaders(body)
            r = c.getresponse()
            data = r.read()
            rh = r.getheaders()
            status, reason = r.status, r.reason
            c.close()
        except (OSError, http.client.HTTPException) as ex:
            sys.stderr.write(f"gateway: {ent['name']} に中継できません: {type(ex).__name__}\n")
            return self._send(502, f"<p>セッション <strong>{e(ent['name'])}</strong> の Web UI に中継できません(応答なし)。<a href=\"/\">一覧へ</a></p>")
        self.send_response(status, reason)
        rdrop = HOP | {x.strip().lower() for k, v in rh if k.lower() == "connection" for x in v.split(",")}
        for k, v in rh:
            if k.lower() in rdrop or k.lower() in ("content-length", "server", "date"):
                continue
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(data)

    def _rewrite_origin(self, v, port):
        """入口から来た Origin / Referer だけ、そのインスタンスのものに読み替える。ほかの値はそのまま(インスタンスが拒む)。"""
        for o in httpbase.origins(self.port):
            if v == o or v.startswith(o + "/"):
                return o.rsplit(":", 1)[0] + f":{port}" + v[len(o):]
        return v

    do_GET = do_HEAD = do_POST = do_PUT = do_DELETE = do_PATCH = do_OPTIONS = _handle


def main():
    port = gateway_port()
    Gateway.port = port
    with socket.socket() as probe:   # macOS では SO_REUSEADDR の bind が待ち受け中のポートにも通るので、先に接続を試す
        probe.settimeout(0.5)
        if probe.connect_ex((HOST, port)) == 0:
            raise SystemExit(f"herdr-webui-gateway: ポート {port} で既に別のプロセスが待ち受けています")
    try:
        srv = ThreadingHTTPServer((HOST, port), Gateway)
    except OSError as ex:
        raise SystemExit(f"herdr-webui-gateway: ポート {port} で待ち受けられません: {ex}")
    print(f"herdr-webui-gateway: http://{HOST}:{port}/ (一覧: {registry.REGISTRY_DIR()})", flush=True)

    def stop(*_):
        threading.Thread(target=srv.shutdown, daemon=True).start()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        srv.serve_forever()
    finally:
        srv.server_close()


if __name__ == "__main__":
    main()
