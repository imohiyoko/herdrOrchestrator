"""段階的な導入(#110)と、承認済みテンプレートだけからの定期更新の登録(#107)。

段階(人が決める。変更は測った値と一緒に stages に残る):
  0  測る値を決め、既存の道具だけで基準を測る        → 索引はまだ作らない
  1  スキーマ・安全の検査・そのテスト。小〜中のリポジトリ 1 つ、ディレクトリ単位だけ
  2  読み取りの口(serve)を使う。専用の読み取り役の agent はここで作る
  3  2 つ目の(大きな)リポジトリ。scheduler からの定期更新(承認済みテンプレートだけ)
  4  採用・拡大を決める(制限なし)

どの段階にも戻せる。3 より下に戻すと定期更新の登録を外す。データは repo-remove / purge で消す。
"""
import getpass
import hashlib
import json
import os
import shlex
import subprocess

from . import store

MODULE_ROOT = store.MODULE_ROOT
JOBS_DIR = os.path.join(MODULE_ROOT, "jobs")
JOB_NAME = "repoindex-update"
TEMPLATE = "update.conf.template"


def _user():
    try:
        return getpass.getuser()
    except Exception:
        return ""


def current(con):
    r = con.execute("SELECT stage FROM stages ORDER BY id DESC LIMIT 1").fetchone()
    return r[0] if r else 0


def require(con, need, what):
    st = current(con)
    if st < need:
        raise store.StoreError(f"{what} は段階 {need} から(今は段階 {st})。段階は人が `repoindex stage set` で進める")
    return st


def max_repos(stage):
    return {0: 0, 1: 1, 2: 1, 3: 2}.get(stage)  # 4 は制限なし(None)


def check_repo_add(con, name, granularity):
    st = require(con, 1, "repo-add")
    known = con.execute("SELECT 1 FROM repos WHERE name = ?", (name,)).fetchone()
    lim = max_repos(st)
    n = con.execute("SELECT count(*) FROM repos").fetchone()[0]
    if not known and lim is not None and n >= lim:
        raise store.StoreError(f"段階 {st} で入れられるリポジトリは {lim} つまで")
    if granularity == "file" and st < 2:
        raise store.StoreError("段階 1 はディレクトリ単位だけ(--granularity dir)")


def set_stage(con, stage, by, measures, note=""):
    if not (0 <= stage <= 4):
        raise store.StoreError("段階は 0〜4")
    if not by.strip():
        raise store.StoreError("--by に判断した人を書く")
    if not isinstance(measures, dict) or not measures:
        raise store.StoreError("--measures に測った値(JSON のオブジェクト。空でない)を渡す")
    before = current(con)
    with con:
        con.execute("INSERT INTO stages(stage, decided_by, os_user, measures, note) VALUES (?,?,?,?,?)",
                    (stage, by.strip(), _user(), json.dumps(measures, ensure_ascii=False, sort_keys=True), note))
    removed = False
    if stage < 3 <= before or (stage < 3 and job_registered()):
        removed = job_remove(con, reason=f"stage {before} -> {stage}")
    return before, removed


def history(con):
    return [{"stage": s, "decided_by": b, "os_user": u, "measures": json.loads(m), "note": n, "at": t}
            for s, b, u, m, n, t in con.execute(
                "SELECT stage, decided_by, os_user, measures, note, decided_at FROM stages ORDER BY id")]


def record_run(con, rid, old, new, **counts):
    cols = ["targets", "summarized", "regenerated", "rejected", "final_rejects", "missing", "seconds"]
    with con:
        con.execute(f"INSERT INTO runs(repo_id, from_commit, to_commit, {', '.join(cols)}) VALUES (?,?,?,{','.join('?' * len(cols))})",
                    (rid, old, new, *[counts.get(c, 0) for c in cols]))


def measures(con, repo=None):
    """測る値のうち、索引が自分で数えられるもの(人が測るもの: 答えるまでの時間・見落とし・要約の誤り率・stale を頼った回数)。"""
    where, args = "", []
    if repo:
        where, args = " WHERE r.name = ?", [repo]
    row = con.execute(f"""SELECT count(*), coalesce(sum(x.targets),0), coalesce(sum(x.summarized),0),
                            coalesce(sum(x.regenerated),0), coalesce(sum(x.rejected),0), coalesce(sum(x.final_rejects),0),
                            coalesce(avg(x.seconds),0), coalesce(max(x.seconds),0)
                          FROM runs x JOIN repos r ON r.id = x.repo_id{where}""", args).fetchone()
    st = con.execute(f"""SELECT coalesce(sum(s.state='fresh'),0), coalesce(sum(s.state!='fresh'),0)
                         FROM summaries s JOIN repos r ON r.id = s.repo_id{where}""", args).fetchone()
    attempts = row[2] + row[4]
    return {"stage": current(con), "runs": row[0], "targets": row[1], "summarized": row[2], "regenerated": row[3],
            "rejected_attempts": row[4], "hit_attempt_limit": row[5],
            "rejection_rate": round(row[4] / attempts, 3) if attempts else 0.0,
            "avg_update_seconds": round(row[6], 2), "max_update_seconds": round(row[7], 2),
            "summaries_fresh": st[0], "summaries_not_fresh": st[1],
            "human_measured": ["time_to_answer", "missed_items", "summary_error_rate", "stale_relied_on"]}


# --- 定期更新の登録(承認済みテンプレートだけ) ---

def scheduler_dir():
    return os.environ.get("REPOINDEX_SCHEDULER_DIR") or os.path.join(store.HERDR_ROOT, "scheduler")


def job_conf_path():
    return os.path.join(scheduler_dir(), "jobs", JOB_NAME + ".conf")


def _sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def allowlist():
    """jobs/allowlist.conf: `<テンプレート名> <sha256>`。人が一度だけ承認したもの(このファイルの変更は人のレビューを通す)。"""
    out = {}
    with open(os.path.join(JOBS_DIR, "allowlist.conf"), encoding="utf-8") as f:
        for line in f:
            line = line.split("#", 1)[0].split()
            if len(line) == 2:
                out[line[0]] = line[1]
    return out


def render():
    """テンプレートを確かめてから、このモジュールの場所を埋めた中身を返す。(中身, テンプレートの hash)"""
    tpath = os.path.join(JOBS_DIR, TEMPLATE)
    h = _sha256(tpath)
    want = allowlist().get(TEMPLATE)
    if want != h:
        raise store.StoreError(f"{TEMPLATE} が承認済みの hash と合わない(書き換えられた)。登録しない。人が確かめる")
    bin_ = shlex.quote(os.path.join(MODULE_ROOT, "bin", "repoindex"))
    with open(tpath, encoding="utf-8") as f:
        text = f.read()
    text = text.replace("__COMMAND__", shlex.quote(f"{bin_} update --all"))
    text = text.replace("__GATE__", shlex.quote(f"{bin_} update --all --check"))
    return text, h


def job_registered():
    return os.path.exists(job_conf_path())


def _log(con, action, h, detail):
    with con:
        con.execute("INSERT INTO job_log(action, template, sha256, os_user, detail) VALUES (?,?,?,?,?)",
                    (action, TEMPLATE, h, _user(), detail))


def _sched(*args):
    """scheduler の道具を呼ぶ(gen-plist / install)。launchctl が無い環境では登録の手前で止まる。"""
    r = subprocess.run(["bash", *args], capture_output=True, text=True, timeout=120)
    return r.returncode, (r.stdout + r.stderr).strip()[-300:]


def job_register(con):
    """承認済みテンプレートから定期更新を登録する。段階 3 から。テンプレート以外のものは登録しない。"""
    try:
        require(con, 3, "定期更新の登録")
        text, h = render()
    except store.StoreError as e:
        _log(con, "refused", "", str(e))
        raise
    conf = job_conf_path()
    if os.path.exists(conf):
        with open(conf, encoding="utf-8") as f:
            if f.read() != text:
                _log(con, "refused", h, "既存の登録がテンプレートと違う")
                raise store.StoreError(f"{conf} がテンプレートと違う(書き換えられた)。上書きしない。人が確かめて job remove する")
    os.makedirs(os.path.dirname(conf), exist_ok=True)
    with open(conf, "w", encoding="utf-8") as f:
        f.write(text)
    sd = scheduler_dir()
    rc, out = _sched(os.path.join(sd, "bin", "gen-plist.sh"), JOB_NAME)
    if rc == 0:
        rc, out = _sched(os.path.join(sd, "bin", "install.sh"), "install", JOB_NAME)
    _log(con, "register", h, "installed" if rc == 0 else f"conf only (installer rc={rc})")
    return rc == 0, out


def job_remove(con, reason="manual"):
    """定期更新の登録を外す(1 コマンドで戻す)。"""
    conf = job_conf_path()
    if not os.path.exists(conf):
        return False
    sd = scheduler_dir()
    _sched(os.path.join(sd, "bin", "install.sh"), "uninstall", JOB_NAME)
    os.remove(conf)
    _log(con, "remove", "", reason)
    return True


def job_status(con):
    conf = job_conf_path()
    st = {"registered": os.path.exists(conf), "conf": conf, "matches_template": None}
    try:
        text, h = render()
        st["template_sha256"] = h
        st["template_approved"] = True
        if st["registered"]:
            with open(conf, encoding="utf-8") as f:
                st["matches_template"] = f.read() == text
    except store.StoreError:
        st["template_approved"] = False
    st["log"] = [{"action": a, "sha256": s, "os_user": u, "detail": d, "at": t} for a, s, u, d, t in con.execute(
        "SELECT action, sha256, os_user, detail, at FROM job_log ORDER BY id DESC LIMIT 10")]
    return st
