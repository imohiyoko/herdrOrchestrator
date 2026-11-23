"""定期実行ジョブの「何が・誰に・いつ・承認は」を読み取りで見せる(編集機能は無い)。

- conf は実行しない(bash として source しない)。PROMPT / PROMPT_FILE / PROMPT_RULES / PROMPT_CMD / PROMPT_VARS / COMMAND / SKIP_IF / COMMAND_TIMEOUT の記述を、そのまま(展開せず)読む。
- PROMPT_FILE の中身は、scheduler/ か scheduler/jobs の配下の通常ファイルで、docs.denied() に当たらないものだけ読む。
- 本文は表示前に、トークンらしい文字列をマスクする(完全ではない。機密を含みうる画面です)。
"""

import os
import re
from datetime import datetime, timedelta

from . import perms
from .config import SCHED_JOBS, SCHED_ROOT
from .jobs import db_body, files_ok
from .docs import denied
from .sanitize import clean

CONF_MAX = 64 * 1024
FILE_MAX = 64 * 1024
KEYS = ("PROMPT", "PROMPT_FILE", "PROMPT_RULES", "PROMPT_CMD", "PROMPT_VARS", "COMMAND", "SKIP_IF", "COMMAND_TIMEOUT")

# トークンらしい文字列のマスク(完全ではない。既存の画面は本文を出すだけなので、ここで足す)
_MASKS = (
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|\Z)", re.S),
    re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9_-]{20,})\b"),
    re.compile(r"(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{12,}"),
    re.compile(r"(?i)((?:token|secret|passw(?:or)?d|api[_-]?key|authorization)[\"']?\s*[=:]\s*)[\"']?[^\s\"']{6,}"),
)


def mask(text):
    """(マスク後の文字列, マスクした数)。"""
    n = 0
    out = text
    for i, rx in enumerate(_MASKS):
        if i == 2:
            out, k = rx.subn(lambda m: m.group(1) + " [マスク]", out)
        elif i == 3:
            out, k = rx.subn(lambda m: m.group(1) + "[マスク]", out)
        else:
            out, k = rx.subn("[マスク]", out)
        n += k
    return out, n


def _is_comment(rest):
    """閉じ引用符の直後の残り。空白のあとの # だけがコメント(`'x'#v2` は引用符の連結で、# は値の一部)。"""
    return rest[:1] in (" ", "\t") and rest.lstrip().startswith("#")


def _value(text, i):
    """text[i:] から shell の代入の右辺を、展開せずにそのまま取り出す。(値, 解釈が不完全か, 値の終わりの位置)。
    不完全 = bash が展開・解釈する記述($ ` ; & | ( ) < > \\ や、引用符の連結)を含む。その場合、値は行末までの記述をそのまま返す。"""
    n = len(text)
    eol = text.find("\n", i)
    line = text[i:eol if eol >= 0 else n]
    if i >= n or not line.strip():
        return "", False, (eol if eol >= 0 else n)
    c = text[i]
    if c == "'":
        j = text.find("'", i + 1)
        if j < 0:
            return text[i + 1:], True, n
        rest = text[j + 1:eol if eol >= 0 and eol > j else (n if eol < 0 else j + 1)]
        return text[i + 1:j], bool(rest) and not _is_comment(rest), j + 1
    if c == '"':
        j = i + 1
        while j < n:
            if text[j] == "\\":
                j += 2
                continue
            if text[j] == '"':
                break
            j += 1
        val = text[i + 1:j]
        rest = text[j + 1:eol if eol >= 0 and eol > j else (n if eol < 0 else j + 1)]
        return val, bool(re.search(r"[$`\\]", val)) or (bool(rest) and not _is_comment(rest)), min(j + 1, n)
    # 引用符なし: 行末まで(bash は空白で値を切るが、$(...) などはその中の空白で切れないので、行全体を記述として見せる)
    raw = re.split(r"\s#", line, maxsplit=1)[0].strip()
    end = eol if eol >= 0 else n
    while text[i:end].endswith("\\") and end < n:   # 行末の \ は次の行へ続く(続きの行は独立した代入ではない)
        nxt = text.find("\n", end + 1)
        end = nxt if nxt >= 0 else n
    return raw, bool(re.search(r"[$`;&|()<>\\ \t'\"*?~{]", raw)), end


def job_text(name, limit):
    """ジョブ定義の本文(DB の sched-job:<job>。SCHED_FILE_FALLBACK=1 なら、無いときに jobs/<名>.conf)。limit 文字まで。読めなければ None。"""
    body = db_body("sched-job:" + name)
    if body is not None:
        return body[:limit]
    if not files_ok():
        return None
    path = os.path.join(SCHED_JOBS(), name + ".conf")
    try:
        if os.path.islink(path) or not os.path.isfile(path):
            return None
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read(limit)
    except OSError:
        return None


def job_defs(name):
    """jobs/<名>.conf の PROMPT 等の記述(展開しない)。{キー: 文字列}。読めなければ None。"""
    text = job_text(name, CONF_MAX + 1)
    if text is None:
        return None
    out, complex_ = {}, set()
    rx, pos = re.compile(r"(?m)^[ \t]*(?:export[ \t]+)?(" + "|".join(KEYS) + r")="), 0
    while True:
        m = rx.search(text, pos)
        if not m:
            break
        out[m.group(1)], inexact, pos = _value(text, m.end())   # 後ろの代入が勝つ(source と同じ)
        pos = max(pos, m.end())   # 値(引用符内・継続行)の内側は、独立した代入として読まない
        (complex_.add if inexact else complex_.discard)(m.group(1))
    out["_complex"] = complex_
    out["_truncated"] = len(text) > CONF_MAX
    return out


# conf の先頭コメントのうち、どの job にも付く注意書き(説明ではない)は省く
_BOILER = re.compile(r"^(注意: COMMAND は bash -c|HERDR_SESSION は|先頭を / にしないこと|xargs は空入力では)")


def job_about(name):
    """conf の先頭のコメント(最初の代入より前の # 行)を説明として返す。[行]。無ければ []。"""
    text = job_text(name, CONF_MAX)
    if text is None:
        return []
    block = []
    for line in text.splitlines():
        t = line.strip()
        if not t:
            if block:
                break   # 空行で説明は終わり
            continue
        if not t.startswith("#"):
            break
        block.append(t.lstrip("#").strip())
    # 行をまたぐ秘密(秘密鍵のブロックなど)も伏せるよう、ブロック全体をマスクしてから行に分ける
    masked = mask(clean("\n".join(block)))[0]
    out = [t.strip() for t in masked.splitlines() if t.strip() and not _BOILER.match(t.strip())]
    return out[:12]


# COMMAND 型でよく使う tb のサブコマンドの説明(conf に説明が無いときの補い)
TB_CMDS = (
    (r"\btb\s+notify\s+flush\b", "tb notify flush", "通知のキューを進める(承認待ち・定期まとめを Slack などへ送る。返事の無いものは再送)"),
    (r"\btb\s+(?:message\s+)?flush\b", "tb message flush", "届かなかった送信(宛先が作業中など)を再送する"),
    (r"\btb\s+digest\b", "tb digest", "worker から指揮官への報告をまとめて送る"),
    (r"\btb\s+pr(?:-|\s+)watch\b", "tb pr watch", "許可したリポジトリの PR の動き(新規・approve・変更依頼・マージ)を通知に積む"),
    (r"\btb\s+compact-check\b", "tb compact-check", "文脈が大きくなった待機中の agent に /compact を送る"),
    (r"\btb\s+pending\b", "tb pending", "未報告のタスクと未配送の報告(C-<n>)を一覧する(再送は続く tb report が行う)"),
    (r"\btb\s+system-send\b", "tb system-send", "agent に本文を送る(scheduler からの送信)"),
    (r"\btb\s+review(?:-|\s+)pack\b", "tb review pack", "レビュー用の集計を作る"),
    (r"\btb\s+pr(?:-|\s+)queue\s+scan\b", "tb pr queue scan", "origin のブランチを走査し、本文があるものを PR 作成キューに積む"),
    (r"\btb\s+pr(?:-|\s+)queue\s+run\b", "tb pr queue run", "承認済みの PR 作成キューを、ガードを通して draft PR にする"),
)


def command_summary(cmd):
    """COMMAND から tb のサブコマンドを見つけて [(コマンド, 説明)]。見つからなければ []。"""
    out, seen = [], set()
    for rx, label, desc in TB_CMDS:
        if re.search(rx, cmd or "") and label not in seen:
            seen.add(label)
            out.append((label, desc))
    return out


def job_summary(name, info):
    """一覧用の 1 行: conf の先頭コメントの 1 行目。無ければ COMMAND から推した説明。"""
    about = job_about(name)
    if about:
        return about[0]
    if info and info.get("kind") != "prompt":
        cs = command_summary((job_defs(name) or {}).get("COMMAND", ""))
        if cs:
            return " / ".join(d for _, d in cs)
    return ""


def prompt_file(path):
    """PROMPT_FILE の中身。(本文 or None, 注記)。相対パス・範囲外・認証情報らしい場所は読まない。"""
    if not path:
        return None, ""
    if not os.path.isabs(path):
        return None, "相対パスなので、どこから読まれるか webui には分からず、中身は表示しません"
    real = os.path.realpath(path)
    # 範囲は scheduler/ 配下と jobs の置き場だけ(taskboard/data や config は読まない)
    roots = [os.path.realpath(SCHED_ROOT), os.path.realpath(SCHED_JOBS())]
    if not any(real == r or real.startswith(r + os.sep) for r in roots):
        return None, "scheduler/(と jobs の置き場)の外なので、中身は表示しません"
    if denied(real):
        return None, "認証情報などの置き場なので、中身は表示しません"
    try:
        if not os.path.isfile(real):
            return None, "ファイルがありません(tick は failed になります)"
        with open(real, encoding="utf-8", errors="replace") as f:
            body = f.read(FILE_MAX + 1)
    except OSError:
        return None, "読めません(tick は failed になります)"
    if len(body) > FILE_MAX:
        return body[:FILE_MAX], f"先頭 {FILE_MAX // 1024} KiB だけ表示しています"
    return body, ""


def _hms(s, end=False):
    p = s.split(":")
    return int(p[0]) * 3600 + int(p[1]) * 60 + (int(p[2]) if len(p) > 2 else (59 if end else 0))


def _day_nums(days):
    out = set()
    for t in days.lower().replace(",", " ").split():
        if t in ("daily", "everyday"):
            out |= {1, 2, 3, 4, 5, 6, 7}
        elif t == "weekdays":
            out |= {1, 2, 3, 4, 5}
        elif t == "weekends":
            out |= {6, 7}
        else:
            for i, p in enumerate(("mon", "tue", "wed", "thu", "fri", "sat", "sun"), 1):
                if t.startswith(p):
                    out.add(i)
    return out


def next_runs(info, now=None, n=3):
    """次回の実行予定(ジョブの TZ の時刻文字列)。(リスト, 注記)。一時停止・TZ 不明なら ([], 理由)。"""
    if not info.get("enabled"):
        return [], "一時停止中のため、次回の予定はありません"
    try:
        from zoneinfo import ZoneInfo
        tz = ZoneInfo(info["tz"])
        now = (now or datetime.now(tz)).astimezone(tz)
        iv = int(info["interval_sec"]) if info.get("interval_sec") else int(info.get("interval_min") or 0) * 60
        start, end = _hms(info["start"]), _hms(info["end"], end=True)
        nums = _day_nums(info["days"])
    except Exception:  # noqa: BLE001  zoneinfo が使えない・値が不正なら「不明」にする
        return [], "次回の予定を計算できません(不明)"
    if iv <= 0 or not nums:
        return [], "次回の予定を計算できません(不明)"
    out = []
    for off in range(0, 9):
        d = (now + timedelta(days=off)).date()
        if d.isoweekday() not in nums:
            continue
        sod = now.hour * 3600 + now.minute * 60 + now.second
        k = 0 if off else max(0, (sod - start) // iv + 1)
        t = start + k * iv
        while t <= end and len(out) < n:
            out.append(f"{d.isoformat()} {t // 3600:02d}:{t % 3600 // 60:02d}:{t % 60:02d}")
            t += iv
        if len(out) >= n:
            break
    return out, ("" if out else "1 週間以内に実行予定がありません")


def send_task_decision(target):
    """参考: 通常の tb send(send:task:<宛先>)の判定(allow / ask / deny / unknown。主体 * の行と既定の表)。policy の読み取りだけ。読めなければ None。"""
    rules, err = perms.load_rules()
    if err or not target:
        return None
    return perms.decide(rules, f"send:task:{clean(target)}")[0]


def agent_state(target):
    """宛先 agent の今の状態。dict(found, count, status, pane, workspace, session, registry) 。herdr に聞けなければ err。"""
    from .data import agent_registry
    from .herdr import agent_key, agent_session, get_agents, workspace_label
    agents, err = get_agents()
    if err:
        return {"err": clean(err)}
    hit = [a for a in agents if a.get("name") == target]
    reg = {}
    try:
        reg = agent_registry().get(target) or {}
    except Exception:  # noqa: BLE001  台帳が無い・古い DB でも画面は出す
        reg = {}
    out = {"count": len(hit), "registry": reg}
    if len(hit) == 1:
        a = hit[0]
        out.update(status=a.get("agent_status") or "", pane=a.get("pane_id") or "", session=agent_session(a) or "",
                   workspace=workspace_label(a.get("workspace_id")) or a.get("workspace_id") or "", key=agent_key(a))
    return out
