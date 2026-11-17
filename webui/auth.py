"""webui の認証: ID/パスワード、セッション、総当たり対策、CSRF トークン。標準ライブラリのみ。

- ログインは全 herdr セッションで共通。置き場はホームの 1 か所(config.AUTH_DIR、既定 ~/.local/state/herdr-webui/)。
  複数のプロセスが同じファイルを読み書きするので、書き換えは auth.lock の排他ロック(flock)の中で行う。
- パスワードは scrypt(ソルト 16 バイト、パラメータも記録)でハッシュ化し、auth.json(0600)に保存する。平文は保存・ログしない。
- セッションは 32 バイトの乱数。サーバー側にはその SHA-256 だけを sessions.json(0600)に期限付きで保存する。
- 総当たり対策の失敗回数とロックも lockout.json で共有する(旧版の複数プロセスや再起動をまたいでも回数を稼げない)。
- 失敗理由(ユーザー名違いか、パスワード違いか)は区別せず、ログにも残さない。
"""
import contextlib
import fcntl
import hashlib
import hmac
import json
import os
import secrets
import stat
import threading
import time
from urllib.parse import urlsplit

MIN_PASSWORD = 12
MAX_FIELD = 256
SCRYPT = {"n": 2 ** 14, "r": 8, "p": 1, "dklen": 32}
FORM_TOKEN_TTL = 1800
RESET_WINDOW = 600  # allow-reset の有効時間(10 分)


def _env_int(name, default):
    try:
        return int(os.environ.get(name) or default)
    except ValueError:
        return default


def session_ttl():
    return _env_int("WEBUI_SESSION_TTL", 12 * 3600)  # 12 時間


def lock_max():
    return _env_int("WEBUI_LOCK_MAX", 5)  # この回数、失敗が続いたらロック(全体でカウント)


def lock_sec():
    return _env_int("WEBUI_LOCK_SEC", 300)  # ロック時間(5 分)


def fail_delay():
    try:
        return float(os.environ.get("WEBUI_FAIL_DELAY") or 1.0)  # 失敗時の遅延(秒)
    except ValueError:
        return 1.0


def write_private_json(path, obj):
    """0600 で原子的に書く(他者に読める瞬間を作らない)。"""
    tmp = f"{path}.{secrets.token_hex(4)}.tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(obj, f)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.remove(tmp)
        except OSError:
            pass
        raise


def hash_password(password, salt=None, params=None):
    p = dict(params or SCRYPT)
    salt = salt or os.urandom(16)
    h = hashlib.scrypt(password.encode("utf-8"), salt=salt, n=p["n"], r=p["r"], p=p["p"],
                       dklen=p["dklen"], maxmem=128 * 1024 * 1024)
    return {"algo": "scrypt", **p, "salt": salt.hex(), "hash": h.hex()}


def verify_password(password, rec):
    try:
        p = {k: int(rec[k]) for k in ("n", "r", "p", "dklen")}
        calc = hash_password(password, bytes.fromhex(rec["salt"]), p)["hash"]
        return hmac.compare_digest(calc.encode(), str(rec["hash"]).encode())
    except (KeyError, ValueError, TypeError):
        return False


def safe_next(n):
    """同一オリジンのパスだけ許可。外部 URL・スキーム付き・// 始まりは "/" にする。"""
    if not isinstance(n, str) or not n.startswith("/") or n.startswith("//") or "\\" in n:
        return "/"
    if any(ord(c) < 32 or ord(c) == 127 for c in n):
        return "/"
    u = urlsplit(n)
    if u.scheme or u.netloc or u.path in ("/login", "/setup", "/logout"):
        return "/"
    return n


class Auth:
    def __init__(self, auth_dir, clock=time.time):
        self.clock = clock  # 時刻の注入点(テスト用)
        self.run_dir = auth_dir
        os.makedirs(auth_dir, mode=0o700, exist_ok=True)
        self.allow_path = os.path.join(os.path.abspath(auth_dir), "allow-reset")
        self.auth_path = os.path.join(auth_dir, "auth.json")
        self.sess_path = os.path.join(auth_dir, "sessions.json")
        self.lockout_path = os.path.join(auth_dir, "lockout.json")
        self.lock_path = os.path.join(auth_dir, "auth.lock")
        self.lock = threading.Lock()
        self.key = secrets.token_bytes(32)  # フォームトークンの署名鍵(プロセス内のみ。フォームは出したインスタンスへ戻る)

    @contextlib.contextmanager
    def _locked(self):
        """プロセス内(スレッド)とプロセス間(flock)の両方で排他する。"""
        with self.lock:
            fd = os.open(self.lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
            try:
                fcntl.flock(fd, fcntl.LOCK_EX)
                yield
            finally:
                os.close(fd)   # close でロックも外れる

    # ---- 初回設定
    def configured(self):
        return os.path.isfile(self.auth_path)

    def setup(self, username, password):
        """auth.json が無いときだけ作る。作れたら True(先に作られていたら False)。"""
        rec = {"version": 1, "username": username, "password": hash_password(password)}
        tmp = f"{self.auth_path}.{secrets.token_hex(4)}.tmp"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as f:
            json.dump(rec, f)
        try:
            os.link(tmp, self.auth_path)  # 既にあれば失敗する(排他的な作成。ほかのインスタンスと同時でも 1 つだけ)
            return True
        except FileExistsError:
            return False
        finally:
            os.remove(tmp)

    def _load(self):
        try:
            with open(self.auth_path) as f:
                return json.load(f)
        except (OSError, ValueError):
            return None

    # ---- ログイン
    def _read_lockout(self):
        try:
            with open(self.lockout_path) as f:
                d = json.load(f)
            fails, until = d.get("fails"), d.get("until")
            if isinstance(fails, int) and isinstance(until, (int, float)):
                return fails, float(until)
        except (OSError, ValueError, AttributeError):
            pass
        return 0, 0.0

    def _write_lockout(self, fails, until):
        write_private_json(self.lockout_path, {"fails": fails, "until": until})

    def locked_for(self):
        fails, until = self._read_lockout()
        left = until - time.time()
        if left <= 0 and until:
            with self._locked():
                _, u2 = self._read_lockout()
                if u2 == until:   # 期限切れのロックを解く(その間にほかで変わっていなければ)
                    self._write_lockout(0, 0.0)
        return max(0, int(left) + 1) if left > 0 else 0

    def attempt(self, username, password):
        """True/False。失敗の理由は区別しない(常に両方を検証する)。"""
        rec = self._load()
        ok = False
        if rec:
            u_ok = hmac.compare_digest(str(rec.get("username", "")).encode(), username.encode())
            p_ok = verify_password(password, rec.get("password") or {})
            ok = u_ok and p_ok
        with self._locked():
            fails, until = self._read_lockout()
            if ok:
                if fails or until:
                    self._write_lockout(0, 0.0)
            else:
                fails += 1
                self._write_lockout(fails, time.time() + lock_sec() if fails >= lock_max() else until)
        return ok

    # ---- セッション
    @staticmethod
    def _h(token):
        return hashlib.sha256(token.encode()).hexdigest()

    def _read_sessions(self):
        try:
            with open(self.sess_path) as f:
                d = json.load(f)
            return d if isinstance(d, dict) else {}
        except (OSError, ValueError):
            return {}

    def _write_sessions(self, d):
        now = time.time()
        write_private_json(self.sess_path, {k: v for k, v in d.items() if isinstance(v, (int, float)) and v > now})

    def create_session(self):
        tok = secrets.token_urlsafe(32)  # 32 バイトの乱数
        with self._locked():
            d = self._read_sessions()
            d[self._h(tok)] = time.time() + session_ttl()
            self._write_sessions(d)
        return tok

    def session_hash(self, tok):
        """有効なセッションならそのハッシュ、でなければ None。"""
        if not tok:
            return None
        h = self._h(tok)
        exp = self._read_sessions().get(h)
        if isinstance(exp, (int, float)) and exp > time.time():
            return h
        return None

    def destroy_session(self, tok):
        if not tok:
            return
        with self._locked():
            d = self._read_sessions()
            if d.pop(self._h(tok), None) is not None:
                self._write_sessions(d)

    # ---- CSRF(フォームの隠しトークン。署名付き・短命)
    def form_token(self, ctx):
        ts, nonce = int(time.time()), secrets.token_hex(8)
        sig = hmac.new(self.key, f"{ctx}|{ts}|{nonce}".encode(), hashlib.sha256).hexdigest()
        return f"{ts}.{nonce}.{sig}"

    def check_form_token(self, tok, ctx):
        try:
            ts_s, nonce, sig = (tok or "").split(".")
            ts = int(ts_s)
        except ValueError:
            return False
        if not (-5 <= time.time() - ts <= FORM_TOKEN_TTL):
            return False
        good = hmac.new(self.key, f"{ctx}|{ts}|{nonce}".encode(), hashlib.sha256).hexdigest()
        return hmac.compare_digest(good.encode(), sig.encode())

    # ---- パスワード再設定(allow-reset を人がシェルで作ったときだけ)
    def reset_state(self, now=None):
        """"ok" | "missing" | "expired" | "invalid"。毎回ファイルを調べる(キャッシュしない)。
        通常ファイルで自分のユーザーの所有のものだけ有効。シンボリックリンクは無効。"""
        now = self.clock() if now is None else now
        try:
            st = os.lstat(self.allow_path)  # リンクを辿らない
        except FileNotFoundError:
            return "missing"
        except OSError:
            return "invalid"
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid():
            return "invalid"
        age = now - st.st_mtime
        if age < -60:  # 未来の時刻は不正
            return "invalid"
        return "ok" if age <= RESET_WINDOW else "expired"

    def reset_password(self, password, now=None):
        """許可が有効なら、ハッシュ更新・許可ファイル削除・全セッション無効化・ロックアウト解除。
        返り値は reset_state の値(成功時は "done")。"""
        with self._locked():
            state = self.reset_state(now)
            if state != "ok":
                return state
            rec = self._load()
            if not rec:
                return "missing"
            try:
                os.remove(self.allow_path)  # 先に消す(消せなければ中止)
            except OSError:
                return "invalid"
            rec["password"] = hash_password(password)
            write_private_json(self.auth_path, rec)
            write_private_json(self.sess_path, {})
            self._write_lockout(0, 0.0)
            return "done"
