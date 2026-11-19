"""repoindex の CLI。書き込みの経路はここだけ(読み取りの口 serve は書けない)。

標準出力に要約の本文を出すのは、人が明示的に読む query get / query search だけ。
ログ・報告には識別子・件数・理由の符号だけを出す。
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys

from . import read, rollout, scan, server, store

NAME_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")
BOUNDARIES = {"auth", "authz", "external", "data"}


def _err(msg, code=1):
    print(f"repoindex: {msg}", file=sys.stderr)
    return code


def _repo_id(con, name):
    r = con.execute("SELECT id FROM repos WHERE name = ?", (name,)).fetchone()
    if not r:
        raise store.StoreError(f"リポジトリが登録されていません: {name}")
    return r[0]


def cmd_init(a):
    con = store.connect_rw()
    v = con.execute("PRAGMA user_version").fetchone()[0]
    con.close()
    print(f"ok: {store.db_path()} (schema {v})")
    return 0


def cmd_status(a):
    d = store.data_dir()
    print(f"data_dir: {d}")
    try:
        store.check_location(d)
        print("location: ok(git の作業ツリーの外)")
    except store.StoreError as e:
        print(f"location: NG {e}")
        return 1
    probs = store.check_permissions(d)
    print("permissions: " + ("ok" if not probs else "NG " + "; ".join(probs)))
    if not os.path.exists(store.db_path()):
        print("db: なし(repoindex init で作る)")
        return 1 if probs else 0
    try:
        con = store.connect_ro()
    except store.StoreError as e:
        print(f"db: {e}")
        return 1
    for r in read.freshness(con)["repos"]:
        print(f"repo {r['repo']}: commit={r['index_commit'] or '-'} fresh={r['fresh']} stale={r['stale']} missing={r['missing']}")
    con.close()
    return 1 if probs else 0


def cmd_repo_add(a):
    if not NAME_RE.fullmatch(a.name):
        return _err("名前は英数字で始まり、英数字と . _ - の 64 字以内")
    path = os.path.abspath(a.path)
    r = subprocess.run(["git", "-C", path, "rev-parse", "--show-toplevel"], capture_output=True, text=True)
    if r.returncode != 0:
        return _err(f"git のリポジトリではありません(自分の権限で clone 済みのものだけを登録する): {path}")
    top = r.stdout.strip()
    if store._inside(store.data_dir(), top):
        return _err("データの置き場所がこのリポジトリの中にあります")
    con = store.connect_rw()
    rollout.check_repo_add(con, a.name, a.granularity)
    with con:
        con.execute("INSERT INTO repos(name, path, granularity) VALUES (?, ?, ?) ON CONFLICT(name) DO UPDATE SET"
                    " path = excluded.path, granularity = excluded.granularity,"
                    " updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')", (a.name, top, a.granularity))
    con.close()
    print(f"ok: {a.name} -> {top}(granularity={a.granularity})")
    return 0


def cmd_repo_remove(a):
    con = store.connect_rw()
    rid = _repo_id(con, a.name)
    with con:
        n = con.execute("DELETE FROM summaries WHERE repo_id = ?", (rid,)).rowcount
        con.execute("DELETE FROM repos WHERE id = ?", (rid,))
    con.close()
    print(f"ok: {a.name} を外した(要約 {n} 件を削除)")
    return 0


def cmd_repo_list(a):
    con = store.connect_rw()
    for name, path, head, gran in con.execute("SELECT name, path, head_commit, granularity FROM repos ORDER BY name"):
        print(f"{name}\t{path}\t{head or '-'}\t{gran}")
    con.close()
    return 0


def put_summary(con, repo, kind, path, body, commit, symbol="", file_hash=None, model="", boundaries=""):
    """要約を 1 件書く(同じ repo/kind/path/symbol があれば置き換えて fresh に戻す)。本文はログに出さない。"""
    if kind not in ("dir", "file", "symbol"):
        raise store.StoreError(f"kind は dir / file / symbol: {kind}")
    if kind == "symbol" and not symbol:
        raise store.StoreError("kind=symbol には symbol が要る")
    bs = [b for b in (boundaries or "").split(",") if b]
    bad = [b for b in bs if b not in BOUNDARIES]
    if bad:
        raise store.StoreError(f"不明な境界: {','.join(bad)}(使えるのは {','.join(sorted(BOUNDARIES))})")
    if not body.strip():
        raise store.StoreError("本文が空です")
    rid = _repo_id(con, repo)
    path = read._norm_path(path)
    scan.check(path, body)  # 落ちたら scan.Rejected。本文は保存しない(呼び出し側が理由の符号だけを記録する)
    con.execute(
        """INSERT INTO summaries(repo_id, kind, path, symbol, body, commit_id, file_hash, fmt_version, model, state, boundaries)
           VALUES (?,?,?,?,?,?,?,?,?, 'fresh', ?)
           ON CONFLICT(repo_id, kind, path, symbol) DO UPDATE SET body=excluded.body, commit_id=excluded.commit_id,
             file_hash=excluded.file_hash, fmt_version=excluded.fmt_version, model=excluded.model, state='fresh',
             boundaries=excluded.boundaries, updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')""",
        (rid, kind, path, symbol or "", body, commit, file_hash, read.FMT_VERSION, model,
         ",".join(sorted(set(bs)))))


def record_rejection(con, repo, kind, path, symbol, reasons, commit=None, attempts=1, final=False):
    """落ちた要約の記録。本文は受け取らない(理由の符号と回数だけ)。"""
    con.execute("INSERT INTO rejections(repo_id, kind, path, symbol, reasons, attempts, final, commit_id)"
                " VALUES (?,?,?,?,?,?,?,?)",
                (_repo_id(con, repo), kind, read._norm_path(path), symbol or "", ",".join(reasons), attempts,
                 1 if final else 0, commit))


def cmd_put(a):
    body = sys.stdin.read()
    con = store.connect_rw()
    try:
        with con:
            put_summary(con, a.repo, a.kind, a.path, body, a.commit, a.symbol or "", a.file_hash, a.model or "",
                        a.boundaries or "")
    except scan.Rejected as e:
        with con:
            record_rejection(con, a.repo, a.kind, a.path, a.symbol, e.reasons, a.commit, final=True)
        con.close()
        return _err(f"保存しなかった({','.join(e.reasons)})", 3)
    con.close()
    print(f"ok: {a.repo} {a.kind} {read._norm_path(a.path)}{' ' + a.symbol if a.symbol else ''}")
    return 0


def cmd_rejections(a):
    """落ちた要約の一覧(本文は持っていないので出ない)。"""
    con = store.connect_rw()
    sql = ("SELECT x.created_at, r.name, x.kind, x.path, x.symbol, x.reasons, x.attempts, x.final"
           " FROM rejections x JOIN repos r ON r.id = x.repo_id")
    args = []
    if a.repo:
        sql += " WHERE r.name = ?"
        args.append(a.repo)
    sql += " ORDER BY x.id DESC LIMIT ?"
    args.append(a.limit)
    for row in con.execute(sql, args):
        print("\t".join(str(c) for c in row[:5]) + f"\t{row[5]}\tattempts={row[6]}{' final' if row[7] else ''}")
    con.close()
    return 0


def cmd_scan(a):
    """標準入力の本文を検査だけする(保存しない)。--path を付けるとパスの除外も見る。結果は理由の符号だけ。"""
    try:
        scan.check(a.path or "summary.txt", sys.stdin.read())
    except scan.Rejected as e:
        print("rejected: " + ",".join(e.reasons))
        return 3
    print("ok")
    return 0


def cmd_update(a):
    from . import update
    if a.all:
        con = store.connect_rw()
        names = [r[0] for r in con.execute("SELECT name FROM repos ORDER BY name")]
        con.close()
    elif a.name:
        names = [a.name]
    else:
        return _err("update <名前> か update --all", 2)
    if a.check:  # 変更があるか(0 = ある、1 = ない)。scheduler の GATE に使う
        return 0 if any(update.run(n, check_only=True) == 0 for n in names) else 1
    rc = 0
    for n in names:
        try:
            rc = max(rc, update.run(n, dry_run=a.dry_run))
        except store.StoreError as e:
            rc = max(rc, _err(f"{n}: {e}"))
    return rc


def cmd_stage(a):
    con = store.connect_rw()
    try:
        if a.op == "show":
            print(json.dumps({"stage": rollout.current(con), "history": rollout.history(con)}, ensure_ascii=False, indent=1))
            return 0
        if a.stage is None or not a.by:
            return _err("stage set <0-4> --by <人> --measures '<JSON>' | --measures-file F [--note T]", 2)
        raw = a.measures
        if a.measures_file:
            with open(a.measures_file, encoding="utf-8") as f:
                raw = f.read()
        try:
            m = json.loads(raw or "")
        except ValueError:
            return _err("--measures は JSON のオブジェクト", 2)
        before, removed = rollout.set_stage(con, a.stage, a.by, m, a.note or "")
        print(f"ok: 段階 {before} -> {a.stage}" + ("(定期更新の登録を外した)" if removed else ""))
        return 0
    finally:
        con.close()


def cmd_measures(a):
    con = store.connect_rw()
    print(json.dumps(rollout.measures(con, a.repo), ensure_ascii=False, indent=1))
    con.close()
    return 0


def cmd_job(a):
    con = store.connect_rw()
    try:
        if a.op == "register":
            ok, out = rollout.job_register(con)
            print(f"ok: {rollout.job_conf_path()} を書いた")
            if not ok:
                print(f"scheduler への登録はできなかった(launchd が無い環境など): {out}")
            return 0
        if a.op == "remove":
            print("ok: 外した" if rollout.job_remove(con) else "ok: 登録はもともと無い")
            return 0
        print(json.dumps(rollout.job_status(con), ensure_ascii=False, indent=1))
        return 0
    finally:
        con.close()


def cmd_purge(a):
    """元に戻す: データのディレクトリごと消す。ほかの部品は何も依存していない。"""
    d = store.data_dir()
    if not a.yes:
        return _err(f"{d} を丸ごと消します。--yes を付けて実行する", 2)
    if not os.path.exists(d):
        print("ok: もともと無い")
        return 0
    if not os.path.exists(os.path.join(d, store.DB_NAME)):
        return _err(f"{d} に {store.DB_NAME} が無いので消しません(別のディレクトリのおそれ)")
    con = store.connect_rw()
    if rollout.job_remove(con, reason="purge"):  # データが無くなった後に定期更新が動き続けないように
        print("定期更新の登録を外した")
    con.close()
    shutil.rmtree(d)
    print(f"ok: {d} を消した")
    return 0


def cmd_query(a):
    con = store.connect_ro()
    try:
        if a.op == "search":
            res = read.search(con, " ".join(a.args), a.repo, a.limit)
        elif a.op == "get":
            if not a.args:
                return _err("query get <path> [symbol]", 2)
            res = read.get(con, a.repo, a.args[0], a.args[1] if len(a.args) > 1 else None)
        elif a.op == "context":
            res = read.context(con, a.repo, a.args or None, a.range)
        elif a.op == "stale":
            res = read.stale(con, a.repo, a.limit)
        else:
            res = read.freshness(con, a.repo)
    finally:
        con.close()
    print(json.dumps(res, ensure_ascii=False, indent=1))
    return 0


def build_parser():
    p = argparse.ArgumentParser(prog="repoindex", description="リポジトリの要約の索引(任意のモジュール)")
    sp = p.add_subparsers(dest="cmd", required=True)
    sp.add_parser("init", help="DB を作る・版を上げる").set_defaults(fn=cmd_init)
    sp.add_parser("status", help="置き場所・権限・件数を確かめる(読み取りのみ)").set_defaults(fn=cmd_status)
    s = sp.add_parser("repo-add", help="索引に入れるリポジトリを登録する(人が選ぶ)")
    s.add_argument("name"); s.add_argument("path")
    s.add_argument("--granularity", choices=["dir", "file"], default="dir",
                   help="dir = ディレクトリだけ(既定)/ file = ファイルも")
    s.set_defaults(fn=cmd_repo_add)
    s = sp.add_parser("repo-remove", help="リポジトリとその要約をすべて消す")
    s.add_argument("name"); s.set_defaults(fn=cmd_repo_remove)
    sp.add_parser("repo-list").set_defaults(fn=cmd_repo_list)
    s = sp.add_parser("put", help="要約を 1 件書く(本文は標準入力)")
    s.add_argument("--repo", required=True); s.add_argument("--kind", required=True, choices=["dir", "file", "symbol"])
    s.add_argument("--path", required=True); s.add_argument("--commit", required=True)
    s.add_argument("--symbol"); s.add_argument("--file-hash"); s.add_argument("--model"); s.add_argument("--boundaries")
    s.set_defaults(fn=cmd_put)
    s = sp.add_parser("update", help="git の差分で要約を更新する(REPOINDEX_SUMMARIZER が要る)")
    s.add_argument("name", nargs="?"); s.add_argument("--all", action="store_true")
    s.add_argument("--dry-run", action="store_true", help="stale / missing の印だけ付け、要約は作らない")
    s.add_argument("--check", action="store_true", help="変更があるかだけ(0 = ある、1 = ない)。何も書かない")
    s.set_defaults(fn=cmd_update)
    s = sp.add_parser("rejections", help="安全の検査で落ちた要約(理由の符号だけ)")
    s.add_argument("--repo"); s.add_argument("--limit", type=int, default=50); s.set_defaults(fn=cmd_rejections)
    s = sp.add_parser("scan", help="標準入力の本文を検査だけする(保存しない)")
    s.add_argument("--path"); s.set_defaults(fn=cmd_scan)
    s = sp.add_parser("stage", help="段階的な導入: show / set(人が判断し、測った値と一緒に残す)")
    s.add_argument("op", choices=["show", "set"]); s.add_argument("stage", nargs="?", type=int)
    s.add_argument("--by"); s.add_argument("--measures"); s.add_argument("--measures-file"); s.add_argument("--note")
    s.set_defaults(fn=cmd_stage)
    s = sp.add_parser("measures", help="索引が数えられる値(更新の回数・時間・検査で落ちた数・作り直し・上限)")
    s.add_argument("--repo"); s.set_defaults(fn=cmd_measures)
    s = sp.add_parser("job", help="定期更新: register(承認済みテンプレートだけ。段階 3 から)/ remove / status")
    s.add_argument("op", choices=["register", "remove", "status"]); s.set_defaults(fn=cmd_job)
    s = sp.add_parser("purge", help="データのディレクトリごと消す(元に戻す)")
    s.add_argument("--yes", action="store_true"); s.set_defaults(fn=cmd_purge)
    s = sp.add_parser("query", help="読み取り(人が確かめる用。serve と同じ中身)")
    s.add_argument("op", choices=["search", "get", "stale", "freshness", "context"])
    s.add_argument("args", nargs="*"); s.add_argument("--range")
    s.add_argument("--repo"); s.add_argument("--limit", type=int); s.set_defaults(fn=cmd_query)
    sp.add_parser("serve", help="読み取り専用のツール口(MCP stdio)").set_defaults(fn=lambda a: server.main())
    return p


def main(argv=None):
    a = build_parser().parse_args(argv)
    try:
        return a.fn(a)
    except store.StoreError as e:
        return _err(str(e))
