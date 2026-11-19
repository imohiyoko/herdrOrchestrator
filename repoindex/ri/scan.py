"""取り込みの安全(#108): 要約する前のパスの除外と、保存する前の本文の機械的な検査。

- 除外: テストデータ・fixture・seed・ダンプ・環境ファイル・鍵や証明書・credential / secret を名に含むもの・
  表計算や CSV の書き出し・factory・vendor / 生成物。要約の入力にしない(保存もしない)。
- 検査: 要約の本文を保存する前に、決まったパターン(トークン・鍵・長い乱数列・口座のような数字・メールアドレス・
  絶対パス)で調べる。追加のパターンは <データの場所>/scan-patterns.conf(`名前 = 正規表現`、1 行 1 つ)。
  外部のスキャナを足すなら REPOINDEX_SCANNER(本文を標準入力に渡し、終了コード 0 以外なら落とす)。
- 落ちた本文は保存しない。残すのは理由の符号だけ(本文・一致した文字列はどこにも出さない)。
- 名前(人名・顧客名)はパターンでは見つけにくいので、パスでの除外を主にする。
"""
import fnmatch
import math
import os
import re
import shlex
import subprocess

from . import store

# パスでの除外(小文字にして照合。パスのどこか 1 つの部分、またはファイル名に一致すれば除外)
EXCLUDE_DIRS = {
    "fixtures", "fixture", "testdata", "test_data", "test-data", "seeds", "seed", "dumps", "dump",
    "factories", "factory", "vendor", "node_modules", "third_party", "generated", "dist", "build",
    "secrets", "credentials", ".git",
}
EXCLUDE_FILES = [
    ".env", ".env.*", "*.env", ".envrc", "*.pem", "*.key", "*.p12", "*.pfx", "*.crt", "*.cer", "*.der", "*.jks",
    "*.keystore", "id_rsa*", "id_ecdsa*", "id_ed25519*", "*.ppk", "*credential*", "*secret*", ".npmrc", ".pypirc", ".netrc", "*.csv", "*.tsv", "*.xls", "*.xlsx", "*.ods", "*.sqlite", "*.sqlite3",
    "*.db", "*.dump", "*.sql.gz", "*.bak", "*_factory.*", "*factories.*", "*.min.js", "*.lock", "package-lock.json",
]

# 本文の検査(理由の符号, 正規表現)
PATTERNS = [
    ("private_key", re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----")),
    ("aws_access_key", re.compile(r"\b(?:AKIA|ASIA|AGPA|AIDA|AROA)[0-9A-Z]{16}\b")),
    ("github_token", re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})\b")),
    ("slack_token", re.compile(r"\bxox[abposr]-[A-Za-z0-9-]{10,}")),
    ("google_api_key", re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b")),
    ("stripe_key", re.compile(r"\b[sr]k_(?:live|test)_[0-9A-Za-z]{16,}\b")),
    ("jwt", re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}")),
    ("credential_assignment", re.compile(
        r"(?i)\b(?:api[_-]?key|secret|token|passw(?:or)?d|pwd|access[_-]?key|client[_-]?secret)\b\s*[:=]\s*['\"]?[^\s'\"]{6,}")),
    ("url_credentials", re.compile(r"\b[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@")),
    ("email", re.compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b")),
    ("account_number", re.compile(r"(?<![\w.])\d(?:[ -]?\d){11,18}(?![\w.])")),
    ("absolute_path", re.compile(r"(?:/Users/|/home/|/root/|[A-Za-z]:\\Users\\)[^\s/\\]+")),
]
LONG_TOKEN = re.compile(r"[A-Za-z0-9+/=_-]{32,}")


def _entropy(s):
    n = len(s)
    return -sum(c / n * math.log2(c / n) for c in (s.count(ch) for ch in set(s)))


def _random_like(tok):
    # 長くて文字の種類が混ざり、ばらつき(エントロピー)が大きいもの。識別子のような snake_case 長語は通す
    if "_" in tok and tok.replace("_", "").isalpha():
        return False
    kinds = sum(bool(re.search(p, tok)) for p in (r"[a-z]", r"[A-Z]", r"[0-9]"))
    return kinds >= 2 and _entropy(tok) >= 3.5


def _conf_file(name):
    return os.path.join(store.data_dir(), name)


def extra_patterns():
    """<データの場所>/scan-patterns.conf の追加パターン。壊れた行は StoreError(検査を弱めたまま進めない)。"""
    p = _conf_file("scan-patterns.conf")
    out = []
    if not os.path.exists(p):
        return out
    with open(p, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            name, sep, rx = line.partition("=")
            name, rx = name.strip(), rx.strip()
            if not sep or not re.fullmatch(r"[a-z0-9_]{1,40}", name):
                raise store.StoreError(f"scan-patterns.conf {n} 行目: `名前 = 正規表現` の形にする")
            try:
                out.append((f"custom_{name}", re.compile(rx)))
            except re.error:
                raise store.StoreError(f"scan-patterns.conf {n} 行目: 正規表現が不正")
    return out


def extra_excludes():
    """<データの場所>/exclude.conf の追加の除外(glob。1 行 1 つ。リポジトリ内の相対パスに照合)。"""
    p = _conf_file("exclude.conf")
    if not os.path.exists(p):
        return []
    with open(p, encoding="utf-8") as f:
        return [x.strip() for x in f if x.strip() and not x.strip().startswith("#")]


def excluded(path, extra=None):
    """除外するパスなら理由の符号、そうでなければ None。"""
    p = os.path.normpath(str(path)).lstrip("/").lower()
    parts = p.split("/")
    for d in parts[:-1]:
        if d in EXCLUDE_DIRS:
            return "excluded_dir"
    name = parts[-1]
    if name in EXCLUDE_DIRS:
        return "excluded_dir"
    if any(fnmatch.fnmatchcase(name, g) for g in EXCLUDE_FILES):
        return "excluded_file"
    for g in (extra if extra is not None else extra_excludes()):
        if fnmatch.fnmatchcase(p, g.lower()) or fnmatch.fnmatchcase(name, g.lower()):
            return "excluded_custom"
    return None


def scan_text(text, patterns=None):
    """本文の検査。落ちた理由の符号の一覧(空なら通過)。一致した文字列は返さない。"""
    text = str(text)
    reasons = []
    for code, rx in PATTERNS + (patterns if patterns is not None else extra_patterns()):
        if rx.search(text):
            reasons.append(code)
    if any(_random_like(t) for t in LONG_TOKEN.findall(text)):
        reasons.append("high_entropy")
    cmd = os.environ.get("REPOINDEX_SCANNER")
    if cmd:
        try:
            r = subprocess.run(shlex.split(cmd), input=text, capture_output=True, text=True, timeout=60)
            if r.returncode != 0:
                reasons.append("external_scanner")
        except (OSError, subprocess.TimeoutExpired, ValueError):
            reasons.append("external_scanner_failed")  # 調べられないときは落とす(安全な側)
    return sorted(set(reasons))


class Rejected(Exception):
    def __init__(self, reasons):
        super().__init__(",".join(reasons))
        self.reasons = reasons


def check(path, text):
    """保存してよいか。だめなら Rejected(理由の符号だけを持つ)。"""
    r = excluded(path)
    if r:
        raise Rejected([r])
    reasons = scan_text(text)
    if reasons:
        raise Rejected(reasons)
