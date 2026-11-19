"""git の差分での更新(#107)。

流れ: 索引を進めたコミット(repos.head_commit)と今の HEAD の差分を取る → 変わったファイルの要約と、その親ディレクトリの
要約を stale にする → 消えたものは missing → 変わったものだけ作り直す(保存の前に必ず安全の検査)→ 最後に head_commit を進める。

- 要約を作るのは外部のコマンド(REPOINDEX_SUMMARIZER)。リポジトリの中身をモデルに送るかどうか・どのモデルかは、
  この設定で人が明示的に決める。未設定なら何も送らず、stale / missing の印を付けるだけ(head_commit も進めない)。
  コマンドには標準入力で中身を渡し、環境変数 REPOINDEX_KIND(dir / file)・REPOINDEX_PATH・REPOINDEX_ATTEMPT を渡す。
  標準出力の 1 行目が `boundaries: auth,external` の形なら境界として読み、残りを本文にする。
  REPOINDEX_MODEL は記録用のモデル名(要約の行に残す)。ディレクトリだけ別のコマンドにするなら REPOINDEX_SUMMARIZER_DIR。
- 除外するパス(ri/scan.py)は、名前も中身も入力にしない。バイナリと大きすぎるファイルも入れない。
- 検査で落ちたら作り直す(上限 REPOINDEX_MAX_ATTEMPTS、既定 3)。作り直しの入力は最初と同じで、落ちた本文は含めない。
  検査は毎回最初から。上限に達したら、理由の符号だけを残して人に 1 行で知らせる。
- 変わったものが無ければ何もしない(要約のコマンドを 1 回も呼ばない)。
"""
import os
import shlex
import subprocess
import sys

from . import cli, read, scan, store

MAX_FILE_BYTES = 200_000     # これより大きいファイルは要約しない
DIR_INPUT_BYTES = 20_000     # ディレクトリの要約に渡す中身の合計の上限
DIR_HEAD_LINES = 40          # ディレクトリの要約に渡す、各ファイルの先頭の行数
SUMMARIZER_TIMEOUT = 300


def git(path, *args, input=None):
    r = subprocess.run(["git", "-C", path, *args], capture_output=True, text=True, input=input)
    if r.returncode != 0:
        raise store.StoreError(f"git {args[0]} が失敗しました({path})")
    return r.stdout


def head_of(path):
    return git(path, "rev-parse", "HEAD").strip()


def max_attempts():
    try:
        return max(1, min(int(os.environ.get("REPOINDEX_MAX_ATTEMPTS", "3")), 10))
    except ValueError:
        return 3


def _parent(p):
    d = os.path.dirname(p)
    return d or "."


def tracked_files(path, commit):
    """{相対パス: バイト数}(通常のファイルだけ。サブモジュールやリンクは外す)。"""
    out = {}
    for ent in git(path, "ls-tree", "-r", "-l", "-z", commit).split("\0"):
        if not ent:
            continue
        meta, _, rel = ent.partition("\t")
        mode, typ, _sha, size = meta.split()
        if typ == "blob" and mode in ("100644", "100755") and size.isdigit():
            out[rel] = int(size)
    return out


def eligible(rel, size, extra):
    """要約の入力にしてよいファイルか(除外パス・大きすぎるものは外す。バイナリは読むときに外す)。"""
    return not scan.excluded(rel, extra=extra) and size <= MAX_FILE_BYTES


def file_text(path, commit, rel):
    r = subprocess.run(["git", "-C", path, "show", f"{commit}:{rel}"], capture_output=True)
    if r.returncode != 0 or b"\0" in r.stdout:
        return None
    return r.stdout.decode("utf-8", errors="replace")


def blob_hash(path, commit, rel):
    r = subprocess.run(["git", "-C", path, "rev-parse", f"{commit}:{rel}"], capture_output=True, text=True)
    return r.stdout.strip() if r.returncode == 0 else None


def dir_input(path, commit, d, files):
    """ディレクトリの要約の入力: 直下のファイル名と、それぞれの先頭の数行(除外したものは名前も入れない)。"""
    parts, used = [f"directory: {d}", "files:"] + [f"- {os.path.basename(f)}" for f in files], 0
    for f in files:
        t = file_text(path, commit, f)
        if t is None:
            continue
        head = "\n".join(t.splitlines()[:DIR_HEAD_LINES])
        if used + len(head) > DIR_INPUT_BYTES:
            break
        used += len(head)
        parts += [f"--- {os.path.basename(f)}", head]
    return "\n".join(parts) + "\n"


def changes(path, old, new):
    """(変わった・増えたファイル, 消えたファイル)。名前の変更は「古い名前が消えて、新しい名前が増えた」。"""
    changed, gone = set(), set()
    out = git(path, "diff", "--name-status", "-z", "-M", old, new)
    toks = [t for t in out.split("\0") if t]
    i = 0
    while i < len(toks):
        st = toks[i]
        if st[0] in "RC":
            src, dst = toks[i + 1], toks[i + 2]
            if st[0] == "R":
                gone.add(src)
            changed.add(dst)
            i += 3
        else:
            (gone if st[0] == "D" else changed).add(toks[i + 1])
            i += 2
    return changed, gone


def _summarizer(kind):
    return (os.environ.get("REPOINDEX_SUMMARIZER_DIR") if kind == "dir" else None) or os.environ.get("REPOINDEX_SUMMARIZER")


def summarize(kind, rel, text, attempt):
    """要約のコマンドを 1 回呼ぶ。(本文, 境界) か、失敗なら None。本文はログに出さない。"""
    cmd = _summarizer(kind)
    env = dict(os.environ, REPOINDEX_KIND=kind, REPOINDEX_PATH=rel, REPOINDEX_ATTEMPT=str(attempt))
    try:
        r = subprocess.run(shlex.split(cmd), input=text, capture_output=True, text=True, env=env,
                           timeout=SUMMARIZER_TIMEOUT)
    except (OSError, subprocess.TimeoutExpired, ValueError):
        return None
    if r.returncode != 0 or not r.stdout.strip():
        return None
    lines = r.stdout.strip().splitlines()
    bounds, calls = "", []
    while lines and lines[0].lower().split(":", 1)[0] in ("boundaries", "calls") and ":" in lines[0]:
        key, val = lines[0].split(":", 1)
        items = [x.strip() for x in val.split(",") if x.strip()]
        if key.lower() == "boundaries":
            bounds = ",".join(b for b in items if b in cli.BOUNDARIES)
        else:
            calls = items
        lines = lines[1:]
    body = "\n".join(lines).strip()
    return (body, bounds, calls) if body else None


def _mark(con, rid, kind_filter, paths, state):
    n = 0
    for p in paths:
        if kind_filter == "file":  # ファイルの要約とそのシンボルの要約
            n += con.execute("UPDATE summaries SET state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')"
                             " WHERE repo_id = ? AND kind IN ('file','symbol') AND path = ? AND state != ?",
                             (state, rid, p, state)).rowcount
        else:
            n += con.execute("UPDATE summaries SET state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')"
                             " WHERE repo_id = ? AND kind = 'dir' AND path = ? AND state != ?",
                             (state, rid, p, state)).rowcount
    return n


def _set_relations(con, rid, src, dsts, commit):
    """src から出る関係を置き換える(索引が見た分だけ)。"""
    con.execute("DELETE FROM relations WHERE repo_id = ? AND src = ?", (rid, src))
    for d in sorted(set(dsts))[:50]:
        con.execute("INSERT OR IGNORE INTO relations(repo_id, src, dst, kind, commit_id) VALUES (?,?,?,'calls',?)",
                    (rid, src, d, commit))


def _final_rejected(con, rid, kind, rel, commit):
    return con.execute("SELECT 1 FROM rejections WHERE repo_id = ? AND kind = ? AND path = ? AND final = 1"
                       " AND commit_id = ? LIMIT 1", (rid, kind, rel, commit)).fetchone() is not None


def run(name, dry_run=False, check_only=False, out=sys.stdout, err=sys.stderr):
    """1 つのリポジトリを更新する。戻り値は終了コード(check_only では 0 = 変更あり、1 = 変更なし)。"""
    con = store.connect_rw()
    try:
        row = con.execute("SELECT id, path, head_commit, granularity FROM repos WHERE name = ?", (name,)).fetchone()
        if not row:
            raise store.StoreError(f"リポジトリが登録されていません: {name}")
        rid, path, old, gran = row
        new = head_of(path)
        extra = scan.extra_excludes()
        stale_rows = con.execute("SELECT kind, path FROM summaries WHERE repo_id = ? AND state = 'stale'"
                                 " AND kind IN ('dir','file')", (rid,)).fetchall()
        stale_rows = [(k, p) for k, p in stale_rows if not _final_rejected(con, rid, k, p, new)]

        if old == new and not stale_rows:
            if check_only:
                return 1
            print(f"{name}: 変更なし({new[:12]})", file=out)
            return 0

        all_files = tracked_files(path, new)
        files_ok = {f for f, size in all_files.items() if eligible(f, size, extra)}
        if old is None:
            changed, gone = set(all_files), set()
        else:
            try:
                changed, gone = changes(path, old, new)
            except store.StoreError:  # 記録したコミットが無い(履歴の書き換えなど)→ 全部を作り直す
                changed, gone = set(all_files), set()
                print(f"{name}: 記録したコミットが見つからない。全体を作り直す", file=err)

        # 作り直す対象: ディレクトリ(file のときはファイルも)。除外したものは対象にしない
        dirs_now = {}
        for f in files_ok:
            dirs_now.setdefault(_parent(f), []).append(f)
        targets = set()
        for f in changed:
            d = _parent(f)
            if d in dirs_now:
                targets.add(("dir", d))
            if gran == "file" and f in files_ok:
                targets.add(("file", f))
        for f in gone:
            if _parent(f) in dirs_now:
                targets.add(("dir", _parent(f)))
        targets |= {(k, p) for k, p in stale_rows if (k == "dir" and p in dirs_now) or (k == "file" and p in files_ok)}
        gone_dirs = {p for (p,) in con.execute("SELECT path FROM summaries WHERE repo_id = ? AND kind = 'dir'", (rid,))
                     if p not in dirs_now}

        if check_only:
            return 0 if (targets or gone or gone_dirs or old != new) else 1

        with con:
            for f in gone | {f for f in changed if f not in files_ok}:
                _set_relations(con, rid, f, [], new)
            for f in gone:
                con.execute("DELETE FROM relations WHERE repo_id = ? AND dst = ?", (rid, f))
            n_missing = _mark(con, rid, "file", gone, "missing") + _mark(con, rid, "dir", gone_dirs, "missing")
            n_missing += _mark(con, rid, "file", {f for f in changed if f not in files_ok}, "missing")
            n_stale = _mark(con, rid, "file", {p for k, p in targets if k == "file"}, "stale")
            n_stale += _mark(con, rid, "dir", {p for k, p in targets if k == "dir"}, "stale")

        if dry_run or not os.environ.get("REPOINDEX_SUMMARIZER"):
            why = "dry-run" if dry_run else "REPOINDEX_SUMMARIZER が未設定(中身をどこにも送らない)"
            print(f"{name}: 対象 {len(targets)} 件、stale {n_stale} 件、missing {n_missing} 件({why}。索引は進めない)", file=out)
            return 0

        done = rejected = regenerated = failed = 0
        model = os.environ.get("REPOINDEX_MODEL", "")
        for kind, rel in sorted(targets, key=lambda t: (t[0] != "file", t[1])):  # ファイルを先に
            text = file_text(path, new, rel) if kind == "file" else dir_input(path, new, rel, sorted(dirs_now[rel]))
            if text is None:
                continue
            reasons, ok = [], False
            limit = max_attempts()
            for attempt in range(1, limit + 1):
                got = summarize(kind, rel, text, attempt)
                if got is None:
                    reasons = ["summarizer_failed"]
                    continue
                body, bounds, calls = got
                try:
                    with con:
                        cli.put_summary(con, name, kind, rel, body, new,
                                        file_hash=blob_hash(path, new, rel) if kind == "file" else None,
                                        model=model, boundaries=bounds)
                        if kind == "file":
                            _set_relations(con, rid, rel, [c for c in calls if c in files_ok and c != rel], new)
                    ok = True
                    if attempt > 1:
                        regenerated += 1
                    break
                except scan.Rejected as e:
                    reasons = e.reasons
                    rejected += 1
            if ok:
                done += 1
                continue
            failed += 1
            with con:
                cli.record_rejection(con, name, kind, rel, "", reasons, new, attempts=limit, final=True)
            # 人に 1 行で知らせる(理由の符号だけ。本文は出さない)
            print(f"repoindex: {name} {kind} {rel} の要約は {limit} 回とも保存できなかった({','.join(reasons)})。人が確認する",
                  file=err)

        with con:
            con.execute("UPDATE repos SET head_commit = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')"
                        " WHERE id = ?", (new, rid))
        print(f"{name}: {(old or '-')[:12]} -> {new[:12]} 作成 {done} 件、作り直し {regenerated} 件、"
              f"検査で落ちた {rejected} 回、上限に達した {failed} 件、missing {n_missing} 件", file=out)
        return 0
    finally:
        con.close()
