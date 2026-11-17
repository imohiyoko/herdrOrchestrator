"""approvals.conf の読み取りと照合(読み取り専用。tb の approval_of と同じ「上から最初に一致した行」を再現する)。"""

import os
import re
from fnmatch import fnmatchcase
import shlex

from .config import CATALOG
from . import data
from .graph import name_matches, role_exists
from .sanitize import clean

# tb の valid_action と同じ文字種
ACTION_RE = re.compile(r"[A-Za-z0-9._:*?@][A-Za-z0-9._:*?@-]{0,127}")

# 一致の例に使う、tb が実際に出す操作名(宛先は仮の名前)
SAMPLE_OPS = ["send:task:worker1", "send:info:worker1", "send:answer:worker1", "role:change", "proxy:read-only",
              "proxy:write", "proxy:external", "alert:analyze", "setup:hooks", "setup:scheduler", "setup:webui",
              "setup:launchctl", "agent:new:worker1", "agent:stop:worker1", "agent:revive:worker1"]


MAX_BYTES = 262144


def load_rules():
    """[{no, pattern, value, note, line}]。読めなければ ([], エラー文)。"""
    text, _src = data.config_text("approvals")   # DB のこのセッションの行 → 共通の行 → checkout のファイル
    if text is None:
        return [], "approvals の設定がありません(DB にもファイルにも無い。すべての操作が ask として扱われます)"
    if len(text.encode("utf-8", "replace")) > MAX_BYTES:
        return [], f"approvals.conf が大きすぎて({MAX_BYTES // 1024} KiB 超)全部は読めません。一覧と判定は出しません(tb は全体を読むので、手元で tb approval を使ってください)"
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


def has_bracket(r):
    """bash の glob と fnmatch で結果が変わりうる行([...] と、拡張 glob の !(...) @(...) など)。"""
    return "[" in r["raw"] or "(" in r["raw"]


def matches(op, pat):
    """tb の approval_of と同じ照合。末尾が @<役割> の行は、操作の末尾の名前を役割で比べる。
    台帳を読めず役割を確かめられないときは None(tb と同じく、その行で ask にして下へ進まない)。"""
    head, _, last = pat.rpartition(":")
    if last.startswith("@"):
        if ":" not in op:
            return False
        oh, _, on = op.rpartition(":")
        if not fnmatchcase(oh, head):
            return False
        from . import data   # tb と同じく台帳の role を先に見る(循環 import を避けてここで読む)
        reg, ok = data.q_checked("SELECT role FROM agents WHERE name=?", (on,))
        if not ok:
            # 台帳を読めないときは map.conf に落とさない(tb の ROLE_STRICT と同じ)。名前そのものの役割ファイルだけは台帳なしで決まる
            if role_exists(on):
                return name_matches(on, last)
            return None
        # ロールは herdr セッション固有: 台帳もこのセッションの行だけ(app/data.py)。役割ファイルもこのセッションの場所
        return name_matches(on, last, reg[0]["role"] if reg else None)
    return fnmatchcase(op, pat)


def decide(rules, op):
    """(値, 決めた行 or None)。どれにも一致しなければ ask。
    [ を含む行に行き当たったら ("unknown", その行): bash の glob と fnmatch で結果が変わりうるので、それより下へは進まない。"""
    for r in rules:
        if has_bracket(r):
            return "unknown", r
        m = matches(op, r["raw"])
        if m is None:
            return "ask", r
        if m:
            return r["value"], r
    return "ask", None


def wildcard(pat):
    return any(c in pat for c in "*?[")


def warning(r):
    """行ごとの警告文(無ければ空)。"""
    pat, v = r["raw"], r["value"]
    if "[" in pat:
        return "[ を含むパターン: bash と照合規則が違いうるので、この画面ではこの行から下の判定をしません(手元で tb approval を使ってください)"
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
    if wildcard(r["raw"]) or "@" in r["raw"]:
        return None   # @<役割> は名前でなく役割の集合なので、文字列の一致では判定しない
    for o in rules[: r["no"] - 1]:
        if has_bracket(o) or "@" in o["raw"]:
            return None   # 判定できないので「隠れる」とは言わない
        if fnmatchcase(r["raw"], o["raw"]):
            return o
    return None


def examples(rules, r):
    if has_bracket(r):
        return []
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
    """承認ルールの出どころ(画面の表示用)。"""
    src = data.config_text("approvals")[1]
    if src.startswith(("file:", "example:")):
        p = src.split(":", 1)[1]
        return os.path.basename(os.path.dirname(p)) + "/" + os.path.basename(p)
    if src == "db:*":
        return "DB の設定 approvals(全セッション共通)"
    if src.startswith("db:"):
        return f"DB の設定 approvals(セッション {src[3:]})"
    return "approvals(未設定)"


# ---- 権限カタログ(taskboard/config/approvals-catalog.conf。操作の定義。判定には使わない)
KINDS = ("send", "agent", "alert", "proxy", "role", "gate", "setup")
SIDE = ("none", "local-write", "external-send", "external-write")
RISK = ("low", "mid", "high", "human-only")
ALLOWOK = ("yes", "no", "conditional")
STATES = ("active", "unwired", "reserved")   # unwired = 操作名はあるが承認判定には渡らない(approvals.conf の行は効かない)


def load_catalog():
    """([{pattern, kind, side, risk, allow_ok, state, desc}], エラー文, 不正な行の数)。書式はファイル先頭の説明どおり。"""
    try:
        with open(CATALOG, encoding="utf-8", errors="replace") as f:
            text = f.read(MAX_BYTES + 1)
    except OSError:
        return [], "権限カタログ(approvals-catalog.conf)を読めません", 0
    if len(text.encode("utf-8", "replace")) > MAX_BYTES:
        return [], "権限カタログが大きすぎて読めません", 0
    out, bad, seen = [], 0, set()
    for ln in text.splitlines():
        t = ln.strip()
        if not t or t.startswith("#"):
            continue
        p = [x.strip() for x in ln.split("|", 6)]
        if (len(p) != 7 or not ACTION_RE.fullmatch(p[0]) or p[1] not in KINDS or p[2] not in SIDE or p[3] not in RISK
                or p[4] not in ALLOWOK or p[5] not in STATES or not p[6]
                or p[0].split(":", 1)[0] != p[1]                      # 種別はパターンの先頭と同じ
                or (p[3] == "human-only" and p[4] != "no")             # human-only は allow 候補にしない
                or (p[4] == "conditional" and p[0].count(":") < 2)     # 宛先・対象の無い操作名は絞れない
                or p[0] in seen):                                      # 重複
            bad += 1
            continue
        seen.add(p[0])
        out.append({"pattern": p[0], "kind": p[1], "side": p[2], "risk": p[3], "allow_ok": p[4], "state": p[5],
                    "desc": clean(p[6])})
    return out, "", bad


def representative(pattern):
    """パターンの * と ? を具体的な名前に置いた 1 例(判定に使う)。"""
    return pattern.replace("*", "example").replace("?", "x")


def covers(cat_pattern, rule_pattern):
    """規則がカタログの項目に当たるか(glob を双方向に当てる)。"""
    return fnmatchcase(cat_pattern, rule_pattern) or fnmatchcase(rule_pattern, cat_pattern)


def rule_inert(rule, catalog):
    """規則が当たるカタログの項目がすべて unwired(承認判定に渡らない)なら True: その行は何も効かない。"""
    hit = [c for c in catalog if covers(c["pattern"], rule["raw"])]
    return bool(hit) and all(c["state"] == "unwired" for c in hit)


def unknown_rules(rules, catalog):
    """カタログのどれにも当たらない規則(末尾の既定 * は除く)。"""
    return [r for r in rules if r["raw"] != "*" and not any(covers(c["pattern"], r["raw"]) for c in catalog)]


def catalog_rows(rules, catalog):
    """カタログ × approvals.conf。[{entry, value(allow/ask/unknown), rule(決めた行 or None), explicit, exceptions, risky}]"""
    out = []
    for c in catalog:
        val, hit = decide(rules, representative(c["pattern"]))
        explicit = bool(hit) and hit["raw"] != "*"
        exc = [r for r in rules if r is not hit and r["raw"] != c["pattern"] and fnmatchcase(r["raw"], c["pattern"])
               and not has_bracket(r)]
        risky = val == "allow" and (c["risk"] in ("high", "human-only") or c["allow_ok"] == "no")
        risky_exc = any(r["value"] == "allow" for r in exc) and (c["risk"] == "human-only" or c["allow_ok"] == "no")
        out.append({"entry": c, "value": val, "rule": hit, "explicit": explicit, "exceptions": exc, "risky": risky,
                    "risky_exc": risky_exc})
    return out
