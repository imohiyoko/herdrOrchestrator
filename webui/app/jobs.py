"""scheduler のジョブ定義と、Web UI から書く上書き設定(DB の sched-settings:<job>)。"""

import json
import os
import re
import subprocess
import time

from . import reqctx
from .actions import run_tb_input
from .config import JOB_RE, SCHED_JOBS, SCHED_LOGS, SCHED_ROOT, SCHED_SETTINGS, TB_DB
from .sanitize import clean
from .data import parse_jsonl, q, tail_lines


# HH:MM か HH:MM:SS(秒を省いた終了時刻は、scheduler がその分の 59 秒までを含める)
HM_RE = re.compile(r"([01][0-9]|2[0-3]):[0-5][0-9](:[0-5][0-9])?")
INTERVAL_UNITS = (("min", "分", 1440), ("sec", "秒", 86400))  # (値, 表示, 上限)
DAY_TOKENS = ("mon", "tue", "wed", "thu", "fri", "sat", "sun")
DAY_PRESETS = (("daily", "毎日"), ("weekdays", "平日"), ("weekends", "土日"))


CONF_MAX_DB = 262144
# ジョブ定義・上書き設定は DB の config 表(sched-job:<job> / sched-settings:<job>。tb sched-job)にも置ける。
# 引く順は scheduler と同じ: このセッションの行 > 共通の行(*)(SCHED_FILE_FALLBACK=1 のときだけ、その後にファイル jobs/<job>.conf・settings/<job>.settings)
_DB_JOBS_SQL = ("SELECT DISTINCT substr(key, 11) AS name FROM main.config WHERE key LIKE 'sched-job:%' "
                "AND herdr_session IN (?, '*')")
_DB_BODY_SQL = ("SELECT body FROM main.config WHERE key = ? AND herdr_session IN (?, '*') "
                "ORDER BY herdr_session = '*' LIMIT 1")


def _db_jobs():
    return {r["name"] for r in q(_DB_JOBS_SQL, (reqctx.session(),)) if JOB_RE.fullmatch(r["name"] or "")}


def db_body(key):
    """DB の本文(このセッション > 共通)。無ければ None。"""
    rows = q(_DB_BODY_SQL, (key, reqctx.session()))
    return rows[0]["body"][:CONF_MAX_DB + 1] if rows and rows[0]["body"] is not None else None


def files_ok():
    """ファイル(jobs/*.conf・settings/*.settings)も読むか。scheduler と同じく SCHED_FILE_FALLBACK=1 のときだけ(取り込み前の確認・テスト用)。"""
    return os.environ.get("SCHED_FILE_FALLBACK") == "1"


def _file_jobs():
    try:
        return {n[:-5] for n in os.listdir(SCHED_JOBS()) if n.endswith(".conf") and JOB_RE.fullmatch(n[:-5])}
    except OSError:
        return set()


def job_names():
    """ジョブ名。DB(このセッション > 共通)。SCHED_FILE_FALLBACK=1 なら jobs/*.conf との和(scheduler と同じ)。"""
    return sorted((_file_jobs() if files_ok() else set()) | _db_jobs())


def unimported_jobs():
    """DB に取り込んでいない jobs/*.conf のジョブ名(scheduler は既定では読まない = 動かない)。"""
    return sorted(_file_jobs() - _db_jobs())


def job_in_db(name):
    return db_body("sched-job:" + name) is not None


def job_info(name):
    """scheduler/bin/show.sh で、実際に効く設定(conf + settings)を得る。(dict, None) か (None, エラー文)。"""
    env = dict(reqctx.child_env(), JOBS_DIR=SCHED_JOBS(), SCHED_SETTINGS_DIR=SCHED_SETTINGS(), TB_DB=TB_DB())
    for k in ("SCHED_WINDOW", "SCHED_START", "SCHED_END", "SCHED_INTERVAL_MIN", "SCHED_INTERVAL_SEC"):
        env.pop(k, None)
    try:
        r = subprocess.run(["bash", os.path.join(SCHED_ROOT, "bin", "show.sh"), name], env=env,
                           capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.TimeoutExpired):
        return None, "設定を読めませんでした"
    if r.returncode != 0:
        return None, clean(r.stderr.strip().splitlines()[-1] if r.stderr.strip() else "設定を読めませんでした")
    try:
        return json.loads(r.stdout), None
    except ValueError:
        return None, "設定を読めませんでした"


def interval_sec(info):
    """show.sh の結果から間隔(秒)。interval_sec が無い古い形なら interval_min から"""
    v = info.get("interval_sec")
    return int(v) if v is not None else int(info.get("interval_min") or 0) * 60


def interval_text(info):
    """表示用: 60 で割り切れれば「N 分」、そうでなければ「N 秒」"""
    sec = interval_sec(info)
    return f"{sec // 60} 分" if sec % 60 == 0 else f"{sec} 秒"


def _sec_of(hms, end=False):
    h, m, *rest = hms.split(":")
    return int(h) * 3600 + int(m) * 60 + (int(rest[0]) if rest else (59 if end else 0))


def job_last(name):
    recs = parse_jsonl(tail_lines(os.path.join(SCHED_LOGS(), name + ".log"), 200))
    last = recs[-1] if recs else None
    ran = [r for r in recs if r.get("action") in ("ran", "failed", "sent")]
    return last, (ran[-1] if ran else None)


def save_job_settings(name, form):
    """フォームを検証して上書き設定を DB(sched-settings:<job>。このセッションの行)に tb sched-job で書く。エラー文か None。
    定義がファイル(jobs/<job>.conf)のジョブも DB に書く(scheduler は settings を DB > ファイルの順に読み、定義の置き場所は見ない)。
    残っている settings/<job>.settings は消す(置き場所を DB の 1 つにする。DB の行を消したときに古い値が効き出さないように)。
    書いたあと、scheduler の読む設定(show.sh)が書いた値になったかを確かめる。"""
    if form.get("action") == "reset":
        if db_body("sched-settings:" + name) is not None:
            rc, out = run_tb_input(["sched-job", "unset", name, "--settings"], "")
            # 共通の行(*)は、このセッションからは消さない(ほかのセッションにも効くため)。このセッションの行を消しても共通の行が残れば、それが効く
            if rc != 0 or db_body("sched-settings:" + name) is not None:
                return "上書き設定を戻せませんでした(共通の行は tb sched-job unset " + name + " --settings --shared で人が消します)"
        return _remove_settings_file(name) or _verify_applied(name, form)
    body, err = settings_body(form)
    if err:
        return err
    rc, out = run_tb_input(["sched-job", "set", name, "--settings"], body)
    if rc != 0:
        first = next((ln for ln in out.splitlines() if ln.startswith("tb: ")), "")
        return "scheduler が設定を受け付けませんでした" + (": " + clean(first[4:])[:300] if first else "")
    return _remove_settings_file(name) or _verify_applied(name, form)


def _remove_settings_file(name):
    """ファイルの settings(settings/<job>.settings。DB に移す前の置き場所)が残っていれば消す。エラー文か None。"""
    path = os.path.join(SCHED_SETTINGS(), name + ".settings")
    if os.path.islink(path):
        return "settings/" + name + ".settings がシンボリックリンクです。手で確認してください"
    if os.path.exists(path):
        os.unlink(path)
    return None


def _verify_applied(name, form):
    """scheduler が読む設定(show.sh)が、いま書いた値になったか。違えばエラー文(書いた場所と、scheduler が読んでいる場所を出す)。"""
    info, err = job_info(name)
    if err:
        return "保存しましたが、scheduler の読む設定を確かめられませんでした: " + err
    src = {"db": "DB の sched-settings:" + name, "file": "settings/" + name + ".settings", "none": "conf の値"}
    where = src.get(info.get("settings_source"), "?")
    if form.get("action") == "reset":
        if info.get("overridden"):
            return f"conf の値に戻りませんでした(scheduler は {where} を読んでいます)"
        return None
    body, _ = settings_body(form)
    want = dict(ln.split("=", 1) for ln in body.splitlines() if "=" in ln and not ln.startswith("#"))
    iv = int(want.get("INTERVAL_SEC") or int(want.get("INTERVAL_MIN", "0")) * 60)
    got = (interval_sec(info), info.get("start"), info.get("end"), info.get("days"), "1" if info.get("enabled") else "0")
    if got != (iv, want["START"], want["END"], want["DAYS"], want["ENABLED"]):
        return f"保存しましたが、scheduler の読む設定に反映されていません(scheduler は {where} を読んでいます)"
    return None


def settings_body(form):
    """フォームを検証して settings の本文にする。(本文, None) か (None, エラー文)。"""
    # 間隔は interval + interval_unit(min / sec)。古いフォームの interval_min も受け付ける
    iv = form.get("interval", form.get("interval_min", "")).strip()
    unit = form.get("interval_unit", "min")
    units = {u: (label, hi) for u, label, hi in INTERVAL_UNITS}
    if unit not in units:
        return None, "間隔の単位が不正です"
    label, hi = units[unit]
    if not iv.isdigit() or not 1 <= int(iv) <= hi:
        return None, f"間隔は 1〜{hi} {label}の整数で入力してください"
    start, end = form.get("start", "").strip(), form.get("end", "").strip()
    if not HM_RE.fullmatch(start) or not HM_RE.fullmatch(end):
        return None, "開始・終了は HH:MM か HH:MM:SS で入力してください"
    if _sec_of(start) > _sec_of(end, end=True):
        return None, "開始は終了より前にしてください(日またぎは未対応)"
    days = form.get("days", "")
    if days == "custom":
        picked = [t for t in DAY_TOKENS if form.get("day_" + t) == "1"]
        if not picked:
            return None, "曜日を 1 つ以上選んでください"
        days = " ".join(t.capitalize() for t in picked)
    elif days not in dict(DAY_PRESETS):
        return None, "曜日の指定が不正です"
    enabled = "1" if form.get("enabled") == "1" else "0"
    body = f"# Web UI が書く上書き設定({time.strftime('%Y-%m-%dT%H:%M:%S%z')})\n" \
           f"{'INTERVAL_SEC' if unit == 'sec' else 'INTERVAL_MIN'}={int(iv)}\nSTART={start}\nEND={end}\nDAYS={days}\nENABLED={enabled}\n"
    return body, None
