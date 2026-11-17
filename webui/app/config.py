"""設定: 定数と、環境変数で上書きできるパス。"""

import os
import re
import sys


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))   # webui/
HERDR_ROOT = os.path.dirname(ROOT)
sys.path.insert(0, os.path.join(HERDR_ROOT, "lib"))
import herdr_env  # noqa: E402  部品で共通の既定値(lib/herdr-env.sh と同じ)

HOST = "127.0.0.1"
RUN_BASE = lambda: os.environ.get("WEBUI_RUN_BASE") or os.path.join(ROOT, "run")  # noqa: E731  run/ そのもの(/docs で開かない)
SESSION_RE = re.compile(r"[A-Za-z0-9_][A-Za-z0-9._-]{0,63}")   # herdr セッション名(lib/herdr-env.sh と同じ)

# 1 つのプロセスが全 herdr セッションの画面を返す。ページは /s/<セッション名>/ の下に置き、セッションはリクエストごとに決める(app/reqctx.py)。
# ログインは全セッションで共通(AUTH_DIR の 1 組と、Path=/ の Cookie 1 つ)。
COOKIE = "herdr_webui_session"
REFRESH_SEC = 8
CACHE_TTL = 3.0
TASK_STALE_HOURS = 24  # in_progress のまま最終更新からこの時間を超えたタスクに「停滞」の目印を付ける
STALL_MIN = 30  # working のまま最終入力から何分超で滞留警告にするか


def cfg(name, default):
    return os.environ.get(name) or default


HERDR_BIN = herdr_env.herdr_bin
TB_DB = lambda: cfg("WEBUI_TB_DB", herdr_env.tb_db())  # noqa: E731  WEBUI_TB_DB → TB_DB → 既定
SCHED_LOGS = lambda: cfg("WEBUI_SCHED_LOGS", os.path.join(HERDR_ROOT, "scheduler", "logs"))  # noqa: E731
CATALOG = os.path.join(HERDR_ROOT, "taskboard", "config", "approvals-catalog.conf")  # 固定(env で差し替えない)
# run ディレクトリ(待ち受けたポートの port ファイル)。WEBUI_RUN_DIR を指定すればそのまま(テスト用)
RUN_DIR = lambda: os.environ.get("WEBUI_RUN_DIR") or RUN_BASE()  # noqa: E731
SCHED_ROOT = os.path.join(HERDR_ROOT, "scheduler")
SCHED_JOBS = lambda: cfg("WEBUI_SCHED_JOBS", os.path.join(SCHED_ROOT, "jobs"))  # noqa: E731
SCHED_SETTINGS = lambda: cfg("WEBUI_SCHED_SETTINGS", os.path.join(SCHED_ROOT, "settings"))  # noqa: E731

JOB_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}")  # ジョブ名(scheduler/jobs/<名前>.conf)

# checkout をまたいで共有するものはホームに置く(既定 ~/.local/state/herdr-webui/)
STATE_DIR = lambda: os.environ.get("WEBUI_STATE_DIR") or os.path.join(  # noqa: E731
    os.environ.get("XDG_STATE_HOME") or os.path.join(os.path.expanduser("~"), ".local", "state"), "herdr-webui")
# ログイン情報(auth.json・sessions.json・lockout.json・allow-reset)。全セッションで共通
AUTH_DIR = lambda: os.environ.get("WEBUI_AUTH_DIR") or STATE_DIR()  # noqa: E731
