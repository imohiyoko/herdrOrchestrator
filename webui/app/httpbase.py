"""Web UI の HTTP 応答で共通のもの: 安全のためのヘッダと、中身を残さないアクセスログ。"""

import sys
from urllib.parse import urlsplit

CSP = ("default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'unsafe-inline'; "
       "base-uri 'none'; form-action 'self'; frame-ancestors 'none'")


def send(h, code, data, ctype, extra=(), csp=CSP):
    """h は BaseHTTPRequestHandler。data は bytes。HEAD では本文を書かない。"""
    h.send_response(code)
    h.send_header("Content-Type", ctype)
    h.send_header("Content-Length", str(len(data)))
    h.send_header("Cache-Control", "no-store")
    h.send_header("Referrer-Policy", "same-origin")
    h.send_header("X-Content-Type-Options", "nosniff")
    h.send_header("X-Frame-Options", "DENY")
    h.send_header("Content-Security-Policy", csp)
    h.send_header("Cross-Origin-Resource-Policy", "same-origin")
    for k, v in extra:
        h.send_header(k, v)
    h.end_headers()
    if h.command != "HEAD":
        h.wfile.write(data)


def log_request_line(h, args):
    """クエリ・Cookie・本文はログに残さない(メソッド・パス・状態だけ)。"""
    try:
        line = h.requestline.split(" ")
        path = urlsplit(line[1]).path if len(line) > 1 else "?"
        sys.stderr.write(f"{h.log_date_time_string()} {line[0]} {path} {args[1] if len(args) > 1 else ''}\n")
    except Exception:
        pass


def host_ok(h, port):
    """DNS リバインディング対策: Host が自分自身(127.0.0.1 / localhost とこのポート)であること。"""
    return (h.headers.get("Host") or "").lower() in (f"127.0.0.1:{port}", f"localhost:{port}")


def origins(port):
    return (f"http://127.0.0.1:{port}", f"http://localhost:{port}")
