"""approvals.conf の読み取りと照合(読み取り専用。tb の approval_of と同じ「上から最初に一致した行」を再現する)。"""

import os
import re
from fnmatch import fnmatchcase
import shlex

from .config import APPROVALS
from .sanitize import clean

# tb の valid_action と同じ文字種
ACTION_RE = re.compile(r"[A-Za-z0-9._:*?][A-Za-z0-9._:*?-]{0,127}")

# 一致の例に使う、tb が実際に出す操作名(宛先は仮の名前)
SAMPLE_OPS = ["send:task:worker1", "send:info:worker1", "send:answer:worker1", "role:change", "proxy:read-only",
              "proxy:write", "proxy:external", "alert:analyze", "setup:hooks", "setup:scheduler", "setup:webui",
              "setup:launchctl", "agent:new:worker1", "agent:stop:worker1", "agent:revive:worker1"]


def load_rules():
    """[{no, pattern, value, note, line}]。読めなければ ([], エラー文)。"""
    path = APPROVALS()
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            text = f.read(262144)
    except OSError:
        return [], "approvals.conf を読めません(無ければ、すべての操作が ask として扱われます)"
    rules = []
    for ln in text.splitlines():
        body, _, note = ln.partition("#")
        body = body.strip()
        if "=" not in body:
            continue
        k, _, v = body.partition("=")
        k, v = k.strip(), v.strip()
        if v not in ("ask", "allow"):
            continue
        rules.append({"no": len(rules) + 1, "pattern": clean(k), "value": v, "note": clean(note.strip()), "raw": k})
    return rules, ""


def decide(rules, op):
    """(値, 決めた行 or None)。どれにも一致しなければ ask。"""
    for r in rules:
        if fnmatchcase(op, r["raw"]):
            return r["value"], r
    return "ask", None


def wildcard(pat):
    return any(c in pat for c in "*?[")


def warning(r):
    """行ごとの警告文(無ければ空)。"""
    pat, v = r["raw"], r["value"]
    if pat == "*" and v == "ask":
        return "全体の既定(どれにも一致しない操作は ask)。この行を allow にしたり消したりしないこと"
    if v != "allow" or not wildcard(pat):
        return ""
    head = re.split(r"[*?\[]", pat, 1)[0]
    if head.count(":") < 2:
        return "広い allow: 操作の種類全体を承認なしにします(agent が自分の権限を広げうる)"
    return "ワイルドカードの allow: 該当する宛先すべてが承認なしになります"


def shadowed_by(rules, r):
    """ワイルドカードを含まない行が、上の行で先に決まってしまうとき、その行(届かない行の検出)。"""
    if wildcard(r["raw"]):
        return None
    for o in rules[: r["no"] - 1]:
        if fnmatchcase(r["raw"], o["raw"]):
            return o
    return None


def examples(rules, r):
    ex = [op for op in SAMPLE_OPS if decide(rules, op)[1] is r]
    if not wildcard(r["raw"]) and decide(rules, r["raw"])[1] is r:
        ex.insert(0, r["raw"])
    return ex


def tb_command(val, pat):
    """表示するだけのコマンド行。検査に通らなければ None。"""
    if val not in ("allow", "ask") or not ACTION_RE.fullmatch(pat or ""):
        return None
    return f"tb {val} {shlex.quote(pat)}"


def conf_path_label():
    return os.path.basename(os.path.dirname(APPROVALS())) + "/" + os.path.basename(APPROVALS())
