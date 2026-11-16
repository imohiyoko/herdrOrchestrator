"""設定: 定数と、環境変数で上書きできるパス。"""

import os
import re
import sys


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))   # webui/
HERDR_ROOT = os.path.dirname(ROOT)
sys.path.insert(0, os.path.join(HERDR_ROOT, "lib"))
import herdr_env  # noqa: E402  部品で共通の既定値(lib/herdr-env.sh と同じ)

HOST = "127.0.0.1"
INSTANCE_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")   # lib/herdr-env.sh の TB_INSTANCE と同じ


def _instance():
    inst = os.environ.get("TB_INSTANCE") or ""
    if inst and not INSTANCE_RE.fullmatch(inst):
        raise SystemExit("herdr-webui: TB_INSTANCE は英数字で始まり、英数字と . _ - の 64 字以内")
    return inst


# 複数の herdr セッションを別ポート・別プロセスで動かす。Cookie は host 単位でポートでは分かれないので、名前をインスタンスごとに変える。
# 未設定(空)なら従来のまま(Cookie 名・run ディレクトリ・表示とも変わらない)。
INSTANCE = _instance()
COOKIE = "herdr_webui_session" + (f"_{INSTANCE}" if INSTANCE else "")
REFRESH_SEC = 8
CACHE_TTL = 3.0
TASK_STALE_HOURS = 24  # in_progress のまま最終更新からこの時間を超えたタスクに「停滞」の目印を付ける
STALL_MIN = 30  # working のまま最終入力から何分超で滞留警告にするか


def cfg(name, default):
    return os.environ.get(name) or default


HERDR_BIN = herdr_env.herdr_bin
TB_DB = lambda: cfg("WEBUI_TB_DB", herdr_env.tb_db())  # noqa: E731  WEBUI_TB_DB → TB_DB → 既定
SCHED_LOGS = lambda: cfg("WEBUI_SCHED_LOGS", os.path.join(HERDR_ROOT, "scheduler", "logs"))  # noqa: E731
APPROVALS = lambda: cfg("WEBUI_APPROVALS", os.environ.get("TB_APPROVALS") or os.path.join(HERDR_ROOT, "taskboard", "config", "approvals.conf"))  # noqa: E731  tb と同じ解決順(TB_APPROVALS)
CATALOG = os.path.join(HERDR_ROOT, "taskboard", "config", "approvals-catalog.conf")  # 固定(env で差し替えない)
# run ディレクトリ(ログイン情報・port)。WEBUI_RUN_DIR を指定すればそのまま。既定は run/、TB_INSTANCE があれば run/instance-<名前>/(同じ checkout で複数動かしても衝突しない)。
# WEBUI_RUN_BASE は既定の置き場(run/)の差し替え(テスト用)
RUN_DIR = lambda: os.environ.get("WEBUI_RUN_DIR") or os.path.join(  # noqa: E731
    os.environ.get("WEBUI_RUN_BASE") or os.path.join(ROOT, "run"), *([f"instance-{INSTANCE}"] if INSTANCE else []))
SCHED_ROOT = os.path.join(HERDR_ROOT, "scheduler")
SCHED_JOBS = lambda: cfg("WEBUI_SCHED_JOBS", os.path.join(SCHED_ROOT, "jobs"))  # noqa: E731
SCHED_SETTINGS = lambda: cfg("WEBUI_SCHED_SETTINGS", os.path.join(SCHED_ROOT, "settings"))  # noqa: E731

JOB_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}")  # ジョブ名(scheduler/jobs/<名前>.conf)
