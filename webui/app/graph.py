"""agent の関係グラフ(主体 -動詞→ 対象)とロールの解決。tb agents --graph と同じ SQL(taskboard/sql/agent_edges.sql)を読む。

1 体が複数の上流(spawn の親・複数 project の指揮官・送信者など)を持つメッシュをそのまま表す。読み取りだけ。
ロールは taskboard/lib/roles.sh の role_file_for と同じ順(roles/<名前>.md > 台帳の role > roles/map.conf)。
"""

import os
import re
from fnmatch import fnmatchcase

from .config import HERDR_ROOT

DEFAULT_TARGET = "conductor"   # taskboard/lib/core.sh の DEFAULT_TARGET
EDGES_SQL = os.path.join(HERDR_ROOT, "taskboard", "sql", "agent_edges.sql")
NAME_RE = re.compile(r"[A-Za-z0-9._-]{1,64}")
ROLE_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")
VERB_LABEL = {"spawn": "spawn", "lead": "指揮", "report": "報告", "analyze": "解析",
              "role": "ロール変更", "read": "中身を読む", "send:task": "依頼", "send:info": "連絡", "send:answer": "回答"}


def roles_dir():
    return os.environ.get("TB_ROLES") or os.path.join(HERDR_ROOT, "roles")


def projects_conf():
    return os.environ.get("TB_CONFIG") or os.path.join(HERDR_ROOT, "taskboard", "config", "projects.conf")


def _read_lines(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read().splitlines()
    except OSError:
        return []


def role_of(name, reg_role=None, rdir=None):
    """役割名(.md を除く)。無ければ None。"""
    rdir = rdir or roles_dir()
    if not name or not NAME_RE.fullmatch(name) or name.startswith("_") or name == "README":
        return None
    if os.path.isfile(os.path.join(rdir, name + ".md")):
        return name
    if reg_role and ROLE_RE.fullmatch(reg_role) and os.path.isfile(os.path.join(rdir, reg_role + ".md")):
        return reg_role
    for line in _read_lines(os.path.join(rdir, "map.conf")):
        k, sep, v = line.partition("=")
        pat, v = k.split("#", 1)[0].strip(), v.split("#", 1)[0].strip()
        if not sep or not pat or not v or not re.fullmatch(r"[A-Za-z0-9._-]+", v):
            continue
        if fnmatchcase(name, pat):
            return v if os.path.isfile(os.path.join(rdir, v + ".md")) else None
    return None


def name_matches(name, pat, reg_role=None):
    """規則のパターンと名前を比べる(tb の name_matches と同じ)。@<役割の glob> なら役割で比べる。"""
    if pat.startswith("@"):
        r = role_of(name, reg_role)
        return bool(r) and fnmatchcase(r, pat[1:])
    return fnmatchcase(name, pat)


def target_for(project, path=None):
    """project → 指揮官(core.sh の target_for と同じ。後に書いた行が勝つ)。"""
    t = DEFAULT_TARGET
    for line in _read_lines(path or projects_conf()):
        k, sep, v = line.partition("=")
        k, v = re.sub(r"\s", "", k), re.sub(r"\s", "", v)
        if sep and k and not k.startswith("#") and v and k == project:
            t = v
    return t


def edge_sql():
    return "\n".join(ln for ln in _read_lines(EDGES_SQL) if not ln.startswith("--"))


def edges(rows):
    """agent_edges.sql の行 → [{verb, from, to, status, n, last_at}](tb の agent_edges と同じまとめ方)。"""
    acc = {}
    for r in rows:
        src = r.get("src") or (target_for(r["project"]) if r.get("project") else None)
        dst = r.get("dst") or (target_for(r["project"]) if r.get("project") else None)
        if not src or not dst or src == dst:
            continue
        k = (r["verb"], src, dst, r.get("status") or "ok")
        e = acc.setdefault(k, {"verb": k[0], "from": src, "to": dst, "status": k[3], "n": 0, "last_at": ""})
        e["n"] += int(r.get("n") or 0)
        e["last_at"] = max(e["last_at"], r.get("last_at") or "")
    return sorted(acc.values(), key=lambda e: (e["from"], e["to"], e["verb"]))


def by_role(nodes):
    """[(役割 or None, [node])]。役割名の順、役割なしは最後。"""
    groups = {}
    for n in nodes:
        groups.setdefault(n.get("role"), []).append(n)
    return sorted(groups.items(), key=lambda kv: (kv[0] is None, kv[0] or ""))


def _safe(s):
    return re.sub(r"[^A-Za-z0-9._:@-]", "_", str(s or ""))[:64] or "_"


def mermaid(nodes, es):
    """flowchart の元。ロールごとに subgraph、エッジには動詞(拒否された試みは点線)。ラベルは安全な文字だけにする。"""
    ids = {n["name"]: f"n{i}" for i, n in enumerate(nodes)}
    out = ["flowchart LR"]
    for gi, (role, ns) in enumerate(by_role(nodes)):
        out.append(f'  subgraph g{gi}["{_safe(role) if role else "役割なし"}"]')
        out += [f'    {ids[n["name"]]}["{_safe(n["name"])}"]' for n in ns]
        out.append("  end")
    for e in es:
        if e["from"] not in ids or e["to"] not in ids:
            continue
        label = VERB_LABEL.get(e["verb"], _safe(e["verb"])) + (f" x{e['n']}" if e["n"] > 1 else "")
        arrow = f"-. {label} 拒否 .->" if e["status"] == "denied" else f"-- {label} -->"
        out.append(f'  {ids[e["from"]]} {arrow} {ids[e["to"]]}')
    return "\n".join(out)
