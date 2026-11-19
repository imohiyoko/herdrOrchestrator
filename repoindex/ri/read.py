"""読み取りの操作(search / get / stale)。書き込みは一切しない。

返す要約は「手がかり」であって根拠ではない。どの結果にも鮮度(commit と stale かどうか)と、
確かめるべき元のソースの場所(repo / path / commit)を付ける。件数と長さには上限がある。
"""
import os
import re
import subprocess

from . import store

FMT_VERSION = 1          # 今の要約の書式の版。これより古い要約は stale として返す
MAX_RESULTS = 20         # 1 回に返す件数の上限
MAX_BODY = 1200          # 1 件の本文の上限(文字)
MAX_TOTAL = 8000         # 1 回に返す本文の合計の上限(文字)
NOTICE = "要約は手がかりで、根拠ではない。判断の前に source の path を commit の時点で読んで確かめる。"


def _limit(n, default=10):
    try:
        n = int(n if n is not None else default)
    except (TypeError, ValueError):
        n = default
    return max(1, min(n, MAX_RESULTS))


def _git(path, *args, input=None):
    try:
        return subprocess.run(["git", "-C", path, *args], capture_output=True, text=True, input=input, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        return None


class _Source:
    """結果を返すときに、今のソースと突き合わせる(リポジトリは読むだけ)。1 回の呼び出しの中でだけ覚えておく。"""

    def __init__(self):
        self.heads, self.blobs = {}, {}

    def head(self, repo_path):
        if repo_path not in self.heads:
            r = _git(repo_path, "rev-parse", "HEAD")
            self.heads[repo_path] = r.stdout.strip() if r and r.returncode == 0 else None
        return self.heads[repo_path]

    def blob(self, repo_path, rel):
        key = (repo_path, rel)
        if key not in self.blobs:
            r = _git(repo_path, "cat-file", "--batch-check", input=f"HEAD:{rel}\n")
            out = r.stdout.split() if r and r.returncode == 0 else []
            self.blobs[key] = out[0] if len(out) == 3 and out[1] == "blob" else None
        return self.blobs[key]

    def changed_since(self, repo_path, commit, rel):
        r = _git(repo_path, "diff", "--quiet", commit, "HEAD", "--", rel)
        return None if r is None or r.returncode not in (0, 1) else r.returncode == 1


def _stale_reasons(row, src):
    r = []
    if row["state"] != "fresh":
        r.append(row["state"])
    if row["fmt_version"] < FMT_VERSION:
        r.append("old_format")
    if src is None:
        return r
    head = src.head(row["repo_path"])
    if head is None:
        r.append("source_unavailable")
        return r
    if head == row["commit_id"]:
        return r
    if row["kind"] in ("file", "symbol") and row["file_hash"]:
        cur = src.blob(row["repo_path"], row["path"])
        if cur is None:
            r.append("file_missing")
        elif cur != row["file_hash"]:
            r.append("file_changed")
    else:
        ch = src.changed_since(row["repo_path"], row["commit_id"], row["path"])
        if ch is None:
            r.append("source_unavailable")
        elif ch:
            r.append("changed_since_summary")
    return r


def _item(row, budget, src=None):
    body = row["body"]
    cut = False
    room = max(0, min(MAX_BODY, budget))
    if len(body) > room:
        body, cut = body[:room], True
    reasons = _stale_reasons(row, src)
    it = {
        "repo": row["repo"], "kind": row["kind"], "path": row["path"],
        "freshness": {"commit": row["commit_id"], "state": row["state"], "stale": bool(reasons),
                      "reasons": reasons, "updated_at": row["updated_at"]},
        "source": {"repo": row["repo"], "path": row["path"], "commit": row["commit_id"]},
        "boundaries": [b for b in row["boundaries"].split(",") if b],
        "summary": body,
    }
    if row["symbol"]:
        it["symbol"] = row["symbol"]
    if cut:
        it["truncated"] = True
    return it, len(body)


_SELECT = """SELECT s.*, r.name AS repo, r.head_commit AS repo_head, r.path AS repo_path
             FROM summaries s JOIN repos r ON r.id = s.repo_id"""


def _pack(rows, limit):
    out, budget, more, src = [], MAX_TOTAL, False, _Source()
    for i, row in enumerate(rows):
        if i >= limit or budget <= 0:
            more = True
            break
        it, used = _item(row, budget, src)
        budget -= used
        out.append(it)
    return {"results": out, "more": more, "notice": NOTICE}


def _fts_query(q):
    # 利用者の文字列を FTS5 の構文として解釈させない: 語ごとに "..." で囲む
    words = [w.replace('"', '""') for w in q.split() if w]
    return " ".join(f'"{w}"' for w in words)


def search(con, query, repo=None, limit=None):
    q = _fts_query(str(query or ""))
    if not q:
        raise store.StoreError("query が空です")
    lim = _limit(limit)
    sql = _SELECT + " JOIN summaries_fts f ON f.rowid = s.id WHERE summaries_fts MATCH ?"
    args = [q]
    if repo:
        sql += " AND r.name = ?"
        args.append(repo)
    sql += " ORDER BY bm25(summaries_fts) LIMIT ?"
    args.append(lim + 1)
    return _pack(con.execute(sql, args).fetchall(), lim)


def _norm_path(p):
    p = os.path.normpath(str(p or ".")).lstrip("/")
    return p or "."


def get(con, repo, path, symbol=None, kind=None):
    """1 つのパス(とシンボル)の要約。無ければ missing を明示して返す(空の答えにしない)。"""
    if not repo:
        raise store.StoreError("repo を指定してください")
    p = _norm_path(path)
    sql = _SELECT + " WHERE r.name = ? AND s.path = ?"
    args = [repo, p]
    if symbol:
        sql += " AND s.symbol = ?"
        args.append(symbol)
    if kind:
        sql += " AND s.kind = ?"
        args.append(kind)
    sql += " ORDER BY CASE s.kind WHEN 'dir' THEN 0 WHEN 'file' THEN 1 ELSE 2 END, s.symbol LIMIT ?"
    args.append(MAX_RESULTS + 1)
    res = _pack(con.execute(sql, args).fetchall(), MAX_RESULTS)
    if not res["results"]:
        res["missing"] = [{"repo": repo, "path": p, "symbol": symbol or "",
                           "hint": "要約がない。元のソースを読む"}]
    return res


def stale(con, repo=None, limit=None):
    """鮮度が落ちた要約の一覧(本文なし)。"""
    lim = _limit(limit)
    sql = _SELECT + " WHERE (s.state != 'fresh' OR s.fmt_version < ?)"
    args = [FMT_VERSION]
    if repo:
        sql += " AND r.name = ?"
        args.append(repo)
    sql += " ORDER BY r.name, s.path, s.symbol LIMIT ?"
    args.append(lim + 1)
    rows = con.execute(sql, args).fetchall()
    out = []
    for row in rows[:lim]:
        it, _ = _item(row, 0)
        it.pop("summary")
        it.pop("truncated", None)
        out.append(it)
    return {"results": out, "more": len(rows) > lim}


def freshness(con, repo=None):
    """リポジトリごとの索引の鮮度(進めたコミットと件数)。"""
    sql = """SELECT r.name, r.head_commit, r.updated_at, r.path,
                    SUM(s.state='fresh') AS fresh, SUM(s.state='stale') AS stale, SUM(s.state='missing') AS missing
             FROM repos r LEFT JOIN summaries s ON s.repo_id = r.id"""
    args = []
    if repo:
        sql += " WHERE r.name = ?"
        args.append(repo)
    sql += " GROUP BY r.id ORDER BY r.name"
    src, out = _Source(), []
    for r in con.execute(sql, args):
        head = src.head(r[3])
        out.append({"repo": r[0], "index_commit": r[1], "repo_head": head, "updated_at": r[2],
                    "index_behind": head is None or r[1] != head,
                    "fresh": r[4] or 0, "stale": r[5] or 0, "missing": r[6] or 0})
    return {"repos": out}


# --- 変更のまわりを引く(#109) ---
MAX_CONTEXT_FILES = 10   # 1 回に見る変更ファイルの上限
MAX_NEIGHBORS = 5        # 1 ファイルあたりの呼び出し元・呼び先の上限
NEIGHBOR_BODY = 400      # まわりの要約 1 件の本文の上限(文字)
_RANGE_RE = re.compile(r"[0-9A-Za-z_./~^@{}-]+(?:\.\.\.?[0-9A-Za-z_./~^@{}-]+)?")
_GENERIC = {"index", "main", "__init__", "init", "utils", "util", "helpers", "common", "types", "test", "config", "mod", "lib"}


def _one(con, src, repo, path, kinds, budget):
    row = con.execute(_SELECT + f" WHERE r.name = ? AND s.path = ? AND s.kind IN ({','.join('?' * len(kinds))})"
                      " ORDER BY s.kind LIMIT 1", (repo, path, *kinds)).fetchone()
    if not row:
        return None, 0
    return _item(row, budget, src)


def _grep_callers(repo_path, path, limit):
    """普通のコード検索で呼び出し元を探す(ファイル名の語で git grep)。場所(path と行)だけを返し、行の中身は返さない。"""
    name = os.path.splitext(os.path.basename(path))[0]
    if len(name) < 3 or name.lower() in _GENERIC:
        return []
    r = _git(repo_path, "grep", "-z", "-n", "-I", "-w", "-F", "-e", name, "HEAD", "--")
    if r is None or r.returncode not in (0, 1):
        return []
    from . import scan
    out, seen = [], set()
    for line in r.stdout.split("\n"):
        parts = line.split("\0")
        if len(parts) < 3:
            continue
        f = parts[0].split(":", 1)[1] if parts[0].startswith("HEAD:") else parts[0]
        if f == path or f in seen or scan.excluded(f, extra=[]):
            continue
        seen.add(f)
        out.append({"path": f, "line": int(parts[1]) if parts[1].isdigit() else None})
        if len(out) >= limit:
            break
    return out


def context(con, repo, paths=None, rev_range=None):
    """変更のまわり: 変わったファイルとそのディレクトリの要約、呼び出し元・呼び先、関わる境界、影響の範囲。
    どの主張にも元ソースの場所(path / 行 / commit)を付ける。要約が無ければ missing を明示する。"""
    row = con.execute("SELECT id, path FROM repos WHERE name = ?", (repo or "",)).fetchone()
    if not row:
        raise store.StoreError(f"リポジトリが登録されていません: {repo}")
    rid, repo_path = row
    src = _Source()
    head = src.head(repo_path)
    if head is None:
        raise store.StoreError("元のリポジトリを読めません")
    if rev_range:
        if not _RANGE_RE.fullmatch(rev_range) or rev_range.startswith("-"):
            raise store.StoreError("range は <base>..<head> の形")
        r = _git(repo_path, "diff", "--name-only", "-z", rev_range, "--")
        if r is None or r.returncode != 0:
            raise store.StoreError("range の差分を取れません")
        paths = [p for p in r.stdout.split("\0") if p]
    paths = [_norm_path(p) for p in (paths or [])]
    if not paths:
        raise store.StoreError("paths か range を指定してください")
    more = len(paths) > MAX_CONTEXT_FILES
    paths = paths[:MAX_CONTEXT_FILES]

    budget = MAX_TOTAL
    files, missing, boundaries, radius = [], [], set(), {}

    def take(path, kinds, cap):
        nonlocal budget
        it, used = _one(con, src, repo, path, kinds, min(cap, budget))
        budget -= used
        return it

    def note_missing(path, kind):
        missing.append({"repo": repo, "path": path, "kind": kind, "hint": "要約がない。元のソースを読む"})

    for p in paths:
        ent = {"path": p, "source": {"repo": repo, "path": p, "commit": head}}
        f = take(p, ("file",), MAX_BODY)
        d = take(os.path.dirname(p) or ".", ("dir",), MAX_BODY)
        ent["file"], ent["dir"] = f, d
        if f is None:
            note_missing(p, "file")
        if d is None:
            note_missing(os.path.dirname(p) or ".", "dir")
        for it in (f, d):
            if it:
                boundaries.update(it["boundaries"])
        radius.setdefault(p, {"path": p, "why": ["changed"], "source": ent["source"]})

        callers, seen = [], set()
        for (s_,) in con.execute("SELECT src FROM relations WHERE repo_id = ? AND dst = ? ORDER BY src LIMIT ?",
                                 (rid, p, MAX_NEIGHBORS)):
            seen.add(s_)
            callers.append({"path": s_, "found_by": "index", "line": None})
        for c in _grep_callers(repo_path, p, MAX_NEIGHBORS):
            if c["path"] not in seen and len(callers) < MAX_NEIGHBORS:
                seen.add(c["path"])
                callers.append({**c, "found_by": "code_search"})
        callees = [{"path": d_, "found_by": "index", "line": None} for (d_,) in con.execute(
            "SELECT dst FROM relations WHERE repo_id = ? AND src = ? ORDER BY dst LIMIT ?", (rid, p, MAX_NEIGHBORS))]
        for group, why in ((callers, "caller"), (callees, "callee")):
            for n in group:
                n["source"] = {"repo": repo, "path": n.pop("path"), "line": n.pop("line"), "commit": head}
                n["summary"] = take(n["source"]["path"], ("file",), NEIGHBOR_BODY) or \
                    take(os.path.dirname(n["source"]["path"]) or ".", ("dir",), NEIGHBOR_BODY)
                if n["summary"] is None:
                    n["missing"] = True
                elif why == "callee":
                    boundaries.update(n["summary"]["boundaries"])
                r_ = radius.setdefault(n["source"]["path"], {"path": n["source"]["path"], "why": [], "source": n["source"]})
                if f"{why} of {p}" not in r_["why"]:
                    r_["why"].append(f"{why} of {p}")
        ent["callers"], ent["callees"] = callers, callees
        files.append(ent)

    return {"repo": repo, "head": head, "files": files, "missing": missing,
            "boundaries": sorted(boundaries), "blast_radius": list(radius.values()),
            "more": more, "notice": NOTICE}
