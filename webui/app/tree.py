"""agent 一覧の論理ツリー(指揮系統)。tb agents --tree(taskboard/lib/agents.sh の AGENT_TREE_JQ)と同じ規則で親を決める。

親の優先: spawned_by(tb agent-spawn の親) > 役割が conductor なら最上位 > 未完了タスクの project の指揮官(projects.conf)
> 役割が worker なら既定の指揮官(conductor) > 最上位。親が一覧に居ない・循環するときは最上位(人の直下)。
読み取りだけ(台帳・tasks・roles/・projects.conf)。
"""

import os
import re
from fnmatch import fnmatchcase

from .config import HERDR_ROOT

DEFAULT_TARGET = "conductor"   # taskboard/lib/core.sh の DEFAULT_TARGET
OPEN_TASK_STATUSES = ("in_progress", "review", "blocked")
NAME_RE = re.compile(r"[A-Za-z0-9._-]{1,64}")
ROLE_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")


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
    """taskboard/lib/roles.sh の role_file_for と同じ順で役割名(.md を除く)を返す。無ければ None。"""
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


def project_targets(path=None):
    """projects.conf の project → 指揮官。後に書いたものが勝つ(core.sh の target_for と同じ)。"""
    out = {}
    for line in _read_lines(path or projects_conf()):
        k, sep, v = line.partition("=")
        k, v = re.sub(r"\s", "", k), re.sub(r"\s", "", v)
        if sep and k and not k.startswith("#") and v:
            out[k] = v
    return out


def build(nodes):
    """nodes: [{name, role, spawned_by, task_project, ...}]。各 node に parent / parent_via を足して返す。"""
    names = {n["name"] for n in nodes if n.get("name")}
    targets = project_targets()
    dflt = targets.get("", DEFAULT_TARGET)
    for n in nodes:
        name = n.get("name") or ""
        lead = targets.get(n.get("task_project") or "", DEFAULT_TARGET) if n.get("task_project") else ""
        sb = n.get("spawned_by") or ""
        if name and sb and sb in names and sb != name:
            n["parent"], n["parent_via"] = sb, "spawn"
        elif n.get("role") == "conductor":
            n["parent"], n["parent_via"] = None, None
        elif name and lead and lead in names and lead != name:
            n["parent"], n["parent_via"] = lead, "project"
        elif n.get("role") == "worker" and dflt in names and dflt != name:
            n["parent"], n["parent_via"] = dflt, "default"
        else:
            n["parent"], n["parent_via"] = None, None
    parent = {n["name"]: n["parent"] for n in nodes if n.get("name")}
    for n in nodes:   # 循環は最上位へ
        p, seen = n["parent"], {n.get("name")}
        while p is not None and p not in seen:
            seen.add(p)
            p = parent.get(p)
        if p is not None:
            n["parent"], n["parent_via"] = None, None
    return nodes


def children(nodes):
    """parent → [node](一覧の順を保つ)。最上位は None。"""
    out = {}
    for n in nodes:
        out.setdefault(n.get("parent"), []).append(n)
    return out
