"""設定 policy の読み取りと照合(読み取り専用。tb の perm_authorize と同じ「当たる行のいちばん厳しい効果、無ければ既定の表」を再現する。ADR 0007)。"""

import os
import re
from fnmatch import fnmatchcase
import shlex

from .config import CATALOG, OPS_TBL
from . import data
from .graph import name_matches, role_exists
from .sanitize import clean

# tb の valid_action と同じ文字種
ACTION_RE = re.compile(r"[A-Za-z0-9._:*?@][A-Za-z0-9._:*?@-]{0,127}")
EFFECTS = ("allow", "ask-any", "ask-human", "deny")   # 緩い順

# 一致の例に使う、tb が実際に出す操作名(宛先は仮の名前)
SAMPLE_OPS = ["send:task:worker1", "send:info:worker1", "send:answer:worker1", "role:change", "proxy:read-only",
              "proxy:write", "proxy:external", "alert:analyze", "setup:hooks", "setup:scheduler", "setup:webui",
              "setup:launchctl", "agent:new:worker1", "agent:stop:worker1", "agent:revive:worker1"]


MAX_BYTES = 262144


def rank(eff):
    return EFFECTS.index(eff) if eff in EFFECTS else len(EFFECTS) - 1


def load_ops():
    """既定の表 perms/ops.tbl。[(パターン, 既定)]。読めなければ空(どの操作も deny)。"""
    try:
        with open(OPS_TBL, encoding="utf-8", errors="replace") as f:
            text = f.read(MAX_BYTES)
    except OSError:
        return []
    out = []
    for ln in text.splitlines():
        f = ln.split("#", 1)[0].split()
        if len(f) == 2:
            out.append((f[0], f[1]))
    return out


def op_default(op):
    for pat, d in load_ops():
        if fnmatchcase(op, pat):
            return d
    return "deny"


def split_act(a):
    """tb の perm_split_act: 古い形の操作名(send:task:w1)を (操作名, 対象) に。対象が無ければ空。"""
    if a.startswith(("role:", "alert:", "proxy:", "setup:")):
        return a, ""
    if a.startswith("agent:slash:"):
        rest = a[len("agent:slash:"):]
        if ":" in rest:
            c, t = rest.split(":", 1)
            return "agent:slash:" + c, t
    parts = a.split(":", 2)
    if len(parts) < 3:
        return a, ""
    return parts[0] + ":" + parts[1], parts[2]


def load_rules():
    """[{no, subject, op, target, effect, note, raw, pattern}]。読めなければ ([], エラー文)。"""
    text, _src = data.config_text("policy")   # DB のこのセッションの行 → 共通の行 → checkout のファイル
    if text is None:
        return [], ""   # 設定が無ければ、すべて既定の表で決まる
    if len(text.encode("utf-8", "replace")) > MAX_BYTES:
        return [], f"policy が大きすぎて({MAX_BYTES // 1024} KiB 超)全部は読めません。一覧と判定は出しません(tb は全体を読むので、手元で tb approval check を使ってください)"
    rules = []
    for ln in text.splitlines():
        body, _, note = ln.partition("#")
        body = body.strip()
        if "=" not in body:
            continue
        lhs, _, v = body.partition("=")
        f, v = lhs.split(), v.strip()
        if len(f) != 3 or v not in EFFECTS:
            continue
        raw = " ".join(f)
        rules.append({"no": len(rules) + 1, "subject": f[0], "op": f[1], "target": f[2], "effect": v, "value": v,
                      "note": clean(note.strip()), "raw": raw, "pattern": clean(raw)})
    return rules, ""


def has_bracket(r):
    """bash の glob と fnmatch で結果が変わりうる行([...] と、拡張 glob の !(...) @(...) など)。"""
    return any(c in r["op"] + r["target"] + r["subject"] for c in "[(")


def _role_match(name, pat):
    """@<役割> の照合。台帳を読めず役割を確かめられないときは None(tb の ROLE_STRICT と同じく、締める向きにだけ使う)。"""
    reg, ok = data.q_checked("SELECT role FROM agents WHERE name=?", (name,))
    if not ok:
        if role_exists(name):
            return name_matches(name, pat)
        return None
    # ロールは herdr セッション固有: 台帳もこのセッションの行だけ(app/data.py)。役割ファイルもこのセッションの場所
    return name_matches(name, pat, reg[0]["role"] if reg else None)


def target_matches(target, t, who=None):
    """True / False / None(確かめられない: 関係 ~ と、読めない役割)。"""
    if t == "*":
        return True
    if not target:
        return False   # 対象の無い操作に当たるのは * の行だけ
    if t == "self":
        return bool(who) and who == target
    if t.startswith("~"):
        return None if who else False   # 関係(親子・組織図)はこの画面では確かめない
    if t.startswith("@"):
        return _role_match(target, t)
    return fnmatchcase(target, t)


def decide(rules, act, who=None):
    """(効果, 決めた行 or None)。主体を渡さなければ主体 * の行だけを見る(tb approval check の --by なしと同じ)。
    当たる行が無ければ既定の表。[ を含む行が当たりうるときは ("unknown", その行)(bash の glob と fnmatch で結果が変わりうる)。"""
    op, target = split_act(act)
    best, unk = None, None
    for r in rules:
        if r["subject"] == "human" or r["subject"].startswith("system:"):
            continue
        if has_bracket(r):
            return "unknown", r
        if not fnmatchcase(op, r["op"]):
            continue
        sm = True
        if who is None:
            if r["subject"] != "*":
                continue
        elif r["subject"].startswith("@"):
            sm = _role_match(who, r["subject"])
        else:
            sm = fnmatchcase(who, r["subject"])
        if sm is False:
            continue
        tm = target_matches(target, r["target"], who)
        if tm is False:
            continue
        if sm and tm:
            if best is None or rank(r["effect"]) > rank(best["effect"]):
                best = r
        elif unk is None or rank(r["effect"]) > rank(unk["effect"]):
            unk = r
    eff, hit = (best["effect"], best) if best else (op_default(op), None)
    if unk and rank(unk["effect"]) > rank(eff):
        eff, hit = unk["effect"], unk
    return eff, hit


def wildcard(pat):
    return any(c in pat for c in "*?[")


def warning(r):
    """行ごとの警告文(無ければ空)。"""
    if has_bracket(r):
        return "[ を含むパターン: bash と照合規則が違いうるので、この画面ではこの行に当たりうる判定をしません(手元で tb approval check を使ってください)"
    if r["effect"] != "allow":
        return ""
    if r["op"] == "*" or r["op"].endswith(":*") and r["op"].count(":") < 2:
        return "広い allow: 操作の種類全体を承認なしにします(agent が自分の権限を広げうる)"
    if r["subject"] == "*" and wildcard(r["target"]):
        return "ワイルドカードの allow: どの agent からも、該当する対象すべてが承認なしになります"
    return ""


def stricter_over(rules, r):
    """allow などの行に、もっと厳しい行が重なる(厳しい方が勝つので、重なる相手ではこの行は効かない)。その行。"""
    for o in rules:
        if o is r or rank(o["effect"]) <= rank(r["effect"]) or has_bracket(o):
            continue
        if (fnmatchcase(r["op"], o["op"]) and o["subject"] in ("*", r["subject"])
                and (o["target"] == "*" or o["target"] == r["target"])):
            return o
    return None


def examples(rules, r):
    if has_bracket(r):
        return []
    return [op for op in SAMPLE_OPS if decide(rules, op)[1] is r]


def tb_command(val, pat):
    """表示するだけのコマンド行。検査に通らなければ None。"""
    if val not in ("allow", "ask") or not ACTION_RE.fullmatch(pat or ""):
        return None
    return f"tb approval {val} {shlex.quote(pat)}"


def policy_row(val, pat):
    """tb approval allow|ask <パターン> が書く policy の行。"""
    op, target = split_act(pat)
    return f"* {op} {target or '*'} = {'allow' if val == 'allow' else 'ask-any'}"


def conf_path_label():
    """policy の出どころ(画面の表示用)。"""
    src = data.config_text("policy")[1]
    if src.startswith(("file:", "example:")):
        p = src.split(":", 1)[1]
        return os.path.basename(os.path.dirname(p)) + "/" + os.path.basename(p)
    if src == "db:*":
        return "DB の設定 policy(全セッション共通)"
    if src.startswith("db:"):
        return f"DB の設定 policy(セッション {src[3:]})"
    return "policy(未設定。すべて既定の表 taskboard/perms/ops.tbl で決まる)"


# ---- 権限カタログ(taskboard/config/approvals-catalog.conf。操作の定義。判定には使わない)
KINDS = ("send", "agent", "alert", "proxy", "role", "gate", "setup", "recipe")
SIDE = ("none", "local-write", "external-send", "external-write")
RISK = ("low", "mid", "high", "human-only")
ALLOWOK = ("yes", "no", "conditional")
STATES = ("active", "unwired", "reserved")   # unwired = 操作名はあるが承認判定には渡らない(policy の行は効かない)


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


def covers(cat_pattern, rule):
    """規則がカタログの項目に当たるか(操作名と対象の glob を双方向に当てる)。"""
    cop, ct = split_act(cat_pattern)
    if not (fnmatchcase(cop, rule["op"]) or fnmatchcase(rule["op"], cop)):
        return False
    t = rule["target"]
    return t == "*" or not ct or ct == "*" or fnmatchcase(ct, t) or fnmatchcase(t, ct) or t.startswith(("@", "~")) or t == "self"


def rule_inert(rule, catalog):
    """規則が当たるカタログの項目がすべて unwired(承認判定に渡らない)なら True: その行は何も効かない。"""
    hit = [c for c in catalog if covers(c["pattern"], rule)]
    return bool(hit) and all(c["state"] == "unwired" for c in hit)


def unknown_rules(rules, catalog):
    """カタログのどれにも当たらない規則(操作 * は除く。cmd: はコマンドの権限でカタログの外)。"""
    return [r for r in rules if r["op"] != "*" and not r["op"].startswith("cmd:")
            and not any(covers(c["pattern"], r) for c in catalog)]


def catalog_rows(rules, catalog):
    """カタログ × policy。[{entry, value(効果 / unknown), rule(決めた行 or None), explicit, exceptions, risky}]"""
    out = []
    for c in catalog:
        val, hit = decide(rules, representative(c["pattern"]))
        explicit = hit is not None
        exc = [r for r in rules if r is not hit and covers(c["pattern"], r) and not has_bracket(r)
               and (r["subject"] != "*" or r["target"] != "*")]
        risky = val == "allow" and (c["risk"] in ("high", "human-only") or c["allow_ok"] == "no")
        risky_exc = any(r["effect"] == "allow" for r in exc) and (c["risk"] == "human-only" or c["allow_ok"] == "no")
        out.append({"entry": c, "value": val, "rule": hit, "explicit": explicit, "exceptions": exc, "risky": risky,
                    "risky_exc": risky_exc})
    return out
