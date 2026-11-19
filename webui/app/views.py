"""HTML の組み立てと各ページ。"""

import json
import os
import re
import shlex
from datetime import datetime, timedelta, timezone
from urllib.parse import quote, unquote, urlencode

from .config import JOB_RE, REFRESH_SEC, SCHED_LOGS, STALL_MIN, TASK_STALE_HOURS
from .sanitize import clean, e
from . import graph
from .herdr import agent_key, agent_session, get_agents, read_agent, workspace_label
from .data import badge_count, has_resolution, parse_ts, NOTIFY_POLL_SEC, QUEUED_WARN_SEC, outbox_rows, activity_for, agent_ops, agent_registry, stopped_agents, db_conn, needs_human, parse_jsonl, q, stalled, tail_lines, tasks_for_agent, urgent_items, LEDGER_MAX, LEDGER_RESULTS, approval_history, ledger_kinds, ledger_rows, ledger_summary
from .actions import NOTE_MAX
from .jobview import agent_state, command_summary, job_about, job_defs, job_summary, mask, next_runs, prompt_file, send_task_decision
from .jobs import DAY_PRESETS, DAY_TOKENS, INTERVAL_UNITS, interval_sec, interval_text, job_info, job_last, job_names
from . import docs, perms, reqctx, sessions
from .markdown import render as render_md


CSS = """
body{font:14px/1.5 -apple-system,system-ui,sans-serif;margin:0;color:#1f2328;background:#f6f8fa}
nav{background:#24292f;padding:8px 16px;display:flex;gap:16px;align-items:center}
nav form{margin:0 0 0 auto}nav button{cursor:pointer}
nav a{color:#fff;text-decoration:none;padding:2px 6px;border-radius:4px}
nav a.on{background:#0969da}
.badge{background:#cf222e;color:#fff;border-radius:10px;padding:0 7px;font-size:12px;margin-left:4px}
.badge.zero{background:#6e7781}
main{padding:16px;max-width:1200px;margin:auto}
h1{font-size:20px}h2{font-size:16px;margin-top:24px}
table{border-collapse:collapse;width:100%;background:#fff}
th,td{border:1px solid #d0d7de;padding:4px 8px;text-align:left;vertical-align:top}
th{background:#eaeef2}
pre{background:#fff;border:1px solid #d0d7de;padding:8px;overflow:auto;white-space:pre-wrap;word-break:break-all}
code{background:#eaeef2;padding:1px 4px;border-radius:3px}
.st-working{color:#9a6700}.st-blocked,.warn{color:#cf222e;font-weight:bold}.st-idle{color:#6e7781}.st-done{color:#1a7f37}
.note{color:#6e7781;font-size:12px}
pre.mermaid{background:#fff}
.inst{padding:2px 10px;border-radius:4px;font-weight:bold;background:#d0d7de;color:#1f2328}.inst-warn{background:#8a5a00;color:#fff;border:1px solid #fff}
.doc{background:#fff;border:1px solid #d0d7de;padding:8px 24px;overflow-wrap:anywhere}
.doc table{width:auto}.doc pre{background:#f6f8fa}.doc blockquote{border-left:4px solid #d0d7de;margin:0;padding:0 12px;color:#57606a}
.card{background:#f6f8fa;border:1px solid #d0d7de;border-radius:4px;padding:4px 6px;margin:0 0 6px}
.doc h2.md{font-size:20px;border-bottom:1px solid #d0d7de}.doc ul,.doc ol{padding-left:1.5em}.err{background:#ffebe9;border:1px solid #cf222e;padding:6px 10px}
"""
NAV = [("/", "概要"), ("/notifications", "緊急通知"), ("/agents", "agent"), ("/tasks", "タスク"), ("/board", "ボード"),
       ("/inbox", "報告・承認待ち"), ("/outbox", "送信"), ("/docs", "ドキュメント"), ("/jobs", "定期実行"), ("/ledger", "送信履歴"), ("/perms", "権限"),
       ("/sessions", "セッション")]


def inst_label():
    """この画面の herdr セッション名(default 以外は色の帯)。"""
    name = reqctx.name()
    return f'<span class="inst{"" if name == "default" else " inst-warn"}" title="herdr セッション">{e(name)}</span>'


def title_suffix():
    name = reqctx.name()
    return f" [{name}]" if name else ""


def page(title, body, path, badge, logout="", refresh=True, scripts=()):
    links = []
    for href, label in NAV:
        on = path == href or (href != "/" and path.startswith(href + "/"))
        b = ""
        if href == "/inbox":
            b = f'<span class="badge{"" if badge else " zero"}">{badge}</span>'
        elif href == "/notifications":
            n = len(urgent_items())
            b = f'<span id=nbadge class="badge{"" if n else " zero"}">{n}</span>'
        links.append(f'<a href="{href}"{" class=on" if on else ""}>{label}{b}</a>')
    return (
        "<!doctype html><html lang=ja><head><meta charset=utf-8>"
        # 自動更新は /refresh.js(見ていないタブでは止める)。スクリプトが動かない環境だけ meta refresh で更新する
        + (f'<meta name=herdr-refresh content="{REFRESH_SEC}"><script src="/refresh.js" defer></script>'
           f'<noscript><meta http-equiv="refresh" content="{REFRESH_SEC}"></noscript>' if refresh else "") +
        '<meta name=viewport content="width=device-width,initial-scale=1">'
        '<meta name=referrer content=same-origin>'
        f"<title>{e(title)} - herdr{e(title_suffix())}</title><style>{CSS}</style>"
        '<script src="/notify.js" defer></script>'
        + "".join(f'<script src="{e(src)}" defer></script>' for src in scripts) + "</head><body>"
        f"<nav>{inst_label()}{''.join(links)}{logout}</nav><main><h1>{e(title)}</h1>{body}"
        f'<p class=note>{f"自動更新 {REFRESH_SEC} 秒(見ていないタブでは止まる)" if refresh else "入力欄があるので自動更新なし(再読み込みで更新)"} / 変更できるのは定期実行の設定・報告の「対応済み」・agent の停止 / 復活 / 作成だけ(権限と送信履歴は読み取りのみ) / 機密を含みうる画面です / Chrome 通知は「緊急通知」で設定</p></main></body></html>'
    )


def table(headers, rows):
    """rows の各セルは HTML 済み(呼び出し側で e() を通す)。"""
    if not rows:
        return "<p class=note>(なし)</p>"
    h = "".join(f"<th>{x}</th>" for x in headers)
    b = "".join("<tr>" + "".join(f"<td>{c}</td>" for c in r) + "</tr>" for r in rows)
    return f"<table><tr>{h}</tr>{b}</table>"


def state_html(s):
    s = s or "unknown"
    return f'<span class="st-{e(s)}">{e(s)}</span>'


def alink(a, frag=""):
    k = agent_key(a)
    return f'<a href="/agents/{quote(k, safe="")}{frag}">{e(k)}</a>'


def tlink(tid):
    return f'<a href="/tasks/{quote(str(tid), safe="")}">{e(tid)}</a>'


def err_html(err):
    return f'<p class=err>{e(err)}</p>' if err else ""


def cur_task_title(a, tasks):
    if tasks:
        return e(tasks[0]["title"])
    return e(a.get("terminal_title_stripped") or a.get("terminal_title") or "")


def page_overview(_qs):
    agents, err = get_agents()
    comments, proxies, blocked = needs_human()
    counts = {}
    for a in agents:
        counts[a.get("agent_status") or "unknown"] = counts.get(a.get("agent_status") or "unknown", 0) + 1
    stall = []
    for a in agents:
        act = activity_for(a)
        if stalled(a, act):
            stall.append((a, act))
    body = err_html(err)
    body += "<h2>承認待ち</h2>" + table(
        ["種別", "件数", ""],
        [["報告 (question/blocked)", len(comments), '<a href="/inbox">見る</a>'],
         ["代行依頼 (pending-human)", len(proxies), '<a href="/inbox">見る</a>'],
         ["blocked の agent", len(blocked), ", ".join(alink(a) for a in blocked)]])
    body += "<h2>agent の状態</h2>" + (
        " / ".join(f'{state_html(k)}: {v}' for k, v in sorted(counts.items())) or "<p class=note>(なし)</p>")
    body += f"<h2>滞留 (working のまま {STALL_MIN} 分超)</h2>" + table(
        ["agent", "最終入力"], [[alink(a), e(act.get("last_prompt_at"))] for a, act in stall])
    done = q("SELECT id,project,title,owner_agent,updated_at FROM tasks WHERE status='done' "
             "ORDER BY updated_at DESC LIMIT 5")
    body += "<h2>直近の完了</h2>" + table(
        ["タスク", "PJ", "題", "担当", "更新"],
        [[tlink(t["id"]), e(t["project"]), e(t["title"]), e(t["owner_agent"]), e(t["updated_at"])] for t in done])
    return "概要", body


KILL_LABEL = {"yes": "止めてよい", "no": "止めない", "unknown": "未判定", "stale": "判定が古い"}
OP_MSG = {"done": "実行しました。", "pending": "人の承認待ちに積みました。", "refused": "前提を満たさないため実行しませんでした(下の「直近の操作」に理由)。",
          "error": "実行に失敗しました(下の「直近の操作」か、手元の tb agent-ops を確認してください)。"}


def op_form(ctx, op, name, label, confirm=False):
    """agent 一覧の操作ボタン(POST /agent-op。書き込みは tb が行う)。confirm なら確認のチェックが要る。"""
    chk = f'<label class=note><input type=checkbox name=confirm value=1 required>本当に{e(label)}</label> ' if confirm else ""
    return (f'<form method=post action="/agent-op" style="display:inline"><input type=hidden name=csrf value="{e(ctx.get("csrf", ""))}">'
            f'<input type=hidden name=op value="{e(op)}"><input type=hidden name=name value="{e(name)}">{chk}'
            f'<button type=submit>{e(label)}</button></form>')


def decide_form(ctx, lid):
    tok = e(ctx.get("csrf", ""))
    return "".join(
        f'<form method=post action="/agent-op" style="display:inline"><input type=hidden name=csrf value="{tok}">'
        f'<input type=hidden name=op value="{op}"><input type=hidden name=id value="{int(lid)}"><button type=submit>{label}</button></form> '
        for op, label in (("approve", "承認して実行"), ("reject", "却下")))


def killable_html(r):
    if not r:
        return '<span class=note>未判定</span>'
    k = r.get("killable_now") or "unknown"
    cls = {"yes": "st-done", "no": "st-idle", "stale": "warn"}.get(k, "note")
    why = f'<br><span class=note>{e(r.get("killable_reason"))}({e(r.get("killable_by"))})</span>' if r.get("killable_reason") else ""
    return f'<span class={cls}>{e(KILL_LABEL.get(k, k))}</span>{why}'


def stop_cell(ctx, name, r):
    if not ctx.get("csrf") or not name:
        return ""
    if r and r.get("killable_now") == "yes" and not r.get("open_tasks"):
        return op_form(ctx, "stop", name, "停止")
    return op_form(ctx, "force-stop", name, "強制停止", confirm=True)


def reg_role(reg, name):
    """台帳の行の role から役割(台帳も役割ファイルも、このセッションのもの)。"""
    return graph.role_of(name, (reg.get(name) or {}).get("role"))


def role_groups_html(agents, reg):
    """動いている agent をロールごとにまとめた一覧。"""
    nodes = [{"agent": a, "role": reg_role(reg, a.get("name") or "")} for a in agents]
    rows = [[e(role) if role else "(役割なし)", ", ".join(f'{alink(n["agent"])} {state_html(n["agent"].get("agent_status"))}' for n in ns)]
            for role, ns in graph.by_role(nodes)]
    return table(["ロール", "agent"], rows)


def page_agent_graph(qs, ctx=None):
    """関係グラフ(主体 -動詞→ 対象)。ロールごとの枠にまとめて mermaid で描き、表でも出す(JS なしでも読める)。"""
    ctx = {} if ctx is None else ctx
    agents, err = get_agents()
    reg = agent_registry()
    es = graph.edges(q(graph.edge_sql()))
    nodes, seen = [], set()
    for a in agents:
        n = agent_key(a)   # 名前が無ければ pane_id(一覧・リンクと同じ)
        if n and n not in seen:
            seen.add(n)
            nodes.append({"name": n, "role": reg_role(reg, a.get("name") or ""), "agent": a})
    for x in [v for ed in es for v in (ed["from"], ed["to"])]:
        if x not in seen:   # 関係の端に居るが今は動いていない名前(止まった agent・cli など)
            seen.add(x)
            nodes.append({"name": x, "role": reg_role(reg, x), "agent": None})
    ctx["scripts"] = ["/static/mermaid.min.js", "/docs.js"]
    body = err_html(err) + (
        '<p class=note>直近 7 日の「誰が誰に何をしたか」(spawn と指揮は今の状態)。1 体が複数の上流を持つこともあります。'
        '点線は routes.conf で拒否された送信の試みです。手元では tb agents --graph で同じものを出せます。 <a href="/agents">agent 一覧へ</a></p>')
    body += f"<pre class=mermaid>{e(graph.mermaid(nodes, es))}</pre>"
    body += "<h2>関係</h2>" + table(
        ["主体", "動詞", "対象", "件数", "最後", "判定"],
        [[e(ed["from"]), e(graph.VERB_LABEL.get(ed["verb"], ed["verb"])), e(ed["to"]), e(ed["n"]), e(ed["last_at"]),
          '<span class=warn>拒否</span>' if ed["status"] == "denied" else "ok"] for ed in es])
    body += "<h2>ロール</h2>" + table(
        ["ロール", "agent"],
        [[e(role) if role else "(役割なし)", ", ".join(alink(n["agent"]) if n["agent"] else e(n["name"]) for n in ns)]
         for role, ns in graph.by_role(nodes)])
    return "agent の関係", body


def page_agents(qs, ctx=None):
    ctx = {} if ctx is None else ctx
    agents, err = get_agents()
    reg = agent_registry()
    rows = []
    for a in agents:
        act = activity_for(a)
        tasks = tasks_for_agent(a)
        warn = ""
        if a.get("agent_status") == "blocked":
            warn = "blocked(人の対応待ち)"
        elif stalled(a, act):
            warn = f"滞留 ({STALL_MIN} 分超)"
        r = reg.get(a.get("name") or "")
        rows.append([
            alink(a), state_html(a.get("agent_status")), e(a.get("workspace_id")), cur_task_title(a, tasks),
            f'入力 {e(act.get("last_prompt_at") if act else "-")}<br>停止 {e(act.get("last_stop_at") if act else "-")}',
            f'<span class=warn>{e(warn)}</span>' if warn else "", killable_html(r), stop_cell(ctx, a.get("name"), r)])
    st = (qs.get("op") or [""])[0]
    body = (f"<p class=note>{e(OP_MSG[st])}</p>" if st in OP_MSG else "") + err_html(err) + table(
        ["名前", "状態", "workspace", "いま何を", "最終入力 / 停止", "警告", "止めてよいか", "操作"], rows)
    body += ('<h2 id="roles">ロール別</h2>' + role_groups_html(agents, reg) +
             '<p class=note>誰が誰に何をしたか(spawn・指揮・依頼・報告・解析)は <a href="/agent-graph">関係グラフ</a> で見られます。</p>')
    body += ('<p class=note>「止めてよいか」は判定役(roles/sweeper.md)か人が付けます。止めてよい印があり、未完了タスクが無いものは「停止」、'
             'それ以外は確認つきの「強制停止」。止めても台帳に会話が残り、下から同じ会話で復活できます。 '
             '<a href="/agent-new">新しい agent を作る</a></p>')
    pend = agent_ops("pending", 50)
    body += '<h2 id="ops">承認待ちの操作(agent からの依頼)</h2>' + table(
        ["id", "依頼者", "操作", "対象", "内容", "時刻", ""],
        [[f"L-{o['id']}", e(o["requested_by"]), e(o["op"]), e(o["target"]), f'<code>{e(o.get("args"))}</code>',
          e(o["created_at"]), decide_form(ctx, o["id"]) if ctx.get("csrf") else ""] for o in pend])
    stopped = stopped_agents()
    body += "<h2>停止中の agent</h2>" + table(
        ["名前", "止めた時刻", "会話 (session)", "cwd", "モデル / 役割", "操作"],
        [[e(r["name"]), e(r.get("stopped_at")), f'<code>{e(r.get("session_id"))}</code>' if r.get("session_id") else "-",
          e(r.get("cwd")), f'{e(r.get("model") or "-")} / {e(r.get("role") or "-")}',
          ((op_form(ctx, "revive", r["name"], "同じ会話で復活") if r.get("session_id") else "") + " " +
           op_form(ctx, "revive-fresh", r["name"], "新しい会話で復活")) if ctx.get("csrf") else ""] for r in stopped])
    body += "<h2>直近の操作</h2>" + table(
        ["id", "操作", "対象", "依頼者", "結果", "内容", "時刻"],
        [[f"L-{o['id']}", e(o["op"]), e(o["target"]), e(o["requested_by"]), e(o["status"]), e(o.get("result")),
          e(o.get("decided_at") or o["created_at"])] for o in agent_ops(None, 15)])
    return "agent 一覧", body


def page_agent_new(csrf, error="", form=None):
    """新しい agent を作るフォーム(入力欄があるので自動更新しない)。"""
    f = form or {}
    roles = graph.role_names()   # 選べる役割は tb と同じ(DB の役割の定義と roles/ のファイル)
    opts = '<option value="">(名前と役割の対応(map)で決める)</option>' + "".join(
        f'<option value="{e(r)}"{" selected" if f.get("role") == r else ""}>{e(r)}</option>' for r in roles)
    kopts = "".join(f'<option value="{v}"{" selected" if f.get("killable", "unknown") == v else ""}>{lab}</option>'
                    for v, lab in (("unknown", "判定役に任せる"), ("yes", "止めてよい(使い捨て)"), ("no", "止めない(常駐)")))
    body = err_html(error) + (
        f'<form method=post action="/agent-new"><input type=hidden name=csrf value="{e(csrf)}">'
        f'<p><label>名前(英小文字で始まる英小文字・数字・_・-、32 字以内)<br><input name=name required maxlength=32 pattern="[a-z][a-z0-9_-]{{0,31}}" value="{e(f.get("name", ""))}"></label></p>'
        f'<p><label>作業ディレクトリ(空ならこのリポジトリ)<br><input name=cwd size=60 maxlength=1024 value="{e(f.get("cwd", ""))}"></label></p>'
        f'<p><label>モデル(空なら Claude Code の既定。例: haiku / sonnet)<br><input name=model maxlength=64 value="{e(f.get("model", ""))}"></label></p>'
        f'<p><label>役割<br><select name=role>{opts}</select></label></p>'
        f'<p><label>止めてよいか<br><select name=killable>{kopts}</select></label></p>'
        f'<p><label>最初の指示(任意)<br><textarea name=prompt rows=4 cols=60 maxlength=4000>{e(f.get("prompt", ""))}</textarea></label></p>'
        '<button type=submit>作成する</button></form>'
        '<p class=note>専用の workspace を作り、そこで claude を起動します(tb agent-new)。役割は起動時に SessionStart hook が渡します。</p>')
    return "新しい agent", body


def find_agent(key):
    agents, err = get_agents()
    for a in agents:
        if agent_key(a) == key or a.get("pane_id") == key:
            return a, err
    return None, err


def page_agent(key):
    a, err = find_agent(key)
    if a is None:
        return None
    act = activity_for(a)
    tasks = tasks_for_agent(a)
    sid = agent_session(a) or (tasks[0]["session_id"] if tasks and tasks[0].get("session_id") else None)
    info = [
        ("状態", state_html(a.get("agent_status")) + (' <span class=warn>滞留</span>' if stalled(a, act) else "")),
        ("場所", f'workspace {e(workspace_label(a.get("workspace_id")) or "-")} ({e(a.get("workspace_id"))})'
                 f' / tab {e(a.get("tab_id"))} / pane {e(a.get("pane_id"))}'),
        ("cwd", e(a.get("foreground_cwd") or a.get("cwd"))),
        ("端末タイトル", e(a.get("terminal_title_stripped") or a.get("terminal_title"))),
        ("最終入力", e(act.get("last_prompt_at") if act else "-")),
        ("最終停止", e(act.get("last_stop_at") if act else "-")),
        ("session_id", f"<code>{e(sid)}</code>" if sid else "-"),
        ("止めてよいか", killable_html(agent_registry().get(a.get("name") or "")) + ' <a href="/agents">停止・復活は agent 一覧から</a>'),
    ]
    body = err_html(err) + "<table>" + "".join(f"<tr><th>{k}</th><td>{v}</td></tr>" for k, v in info) + "</table>"
    attach = "herdr agent attach " + shlex.quote(str(agent_key(a)))
    if a.get("agent_status") == "blocked":
        body += '<h2 id="approval">承認待ちの内容</h2>'
        dlg, derr = read_agent(a, 40, "visible")
        body += err_html(derr)
        if dlg is not None:
            body += f"<pre>{e(dlg)}</pre>"
        body += ('<p class=note>要約・加工なしの端末表示です。承認の入力はブラウザ上では行えません。'
                 '下のコマンドで手元の herdr から開いて、人が操作してください。</p>')
    body += ('<h2>手元で開く</h2><p><input type=text readonly size=60 value="' + e(attach) +
             '"> <span class=note>欄をクリックして全選択(Cmd+A)→ コピーして手元の端末で実行</span></p>'
             '<p class=warn>別のターミナルで claude --resume を実行しないでください。動いている agent と会話が枝分かれします。</p>')
    body += "<h2>担当タスク</h2>" + table(
        ["タスク", "PJ", "題", "状態", "更新"],
        [[tlink(t["id"]), e(t["project"]), e(t["title"]), e(t["status"]), e(t["updated_at"])] for t in tasks])
    out, rerr = read_agent(a)
    body += "<h2>直近の出力 (agent read)</h2>" + err_html(rerr)
    if out is not None:
        body += f"<pre>{e(out)}</pre>"
    return f"agent: {clean(agent_key(a))}", body


TASK_STATUSES = ["todo", "in_progress", "review", "done", "blocked"]


TASK_SORTS = {"": ("優先順(未対応の報告 → blocked → in_progress → review → todo → done)", None),
              "project": ("PJ ごと・更新が新しい順", "project, updated_at DESC"),
              "oldest": ("更新が古い順", "updated_at ASC"),
              "created": ("作成が新しい順", "created_at DESC"), "id": ("タスク ID 順", "id ASC"),
              "due": ("期日が近い順(期日なしは最後)", "due IS NULL, due, updated_at DESC"),
              "priority": ("優先度が高い順(同じなら更新が新しい順)",
                           "CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END, updated_at DESC")}
# チケットの構造(スキーマ 014)。tb の TYPES / PRIORITIES と同じ
TASK_TYPES = ["epic", "story", "task", "bug", "subtask"]
TASK_PRIORITIES = ["highest", "high", "medium", "low", "lowest"]
LABEL_RE = re.compile(r"[^\s,\"']{1,40}")
# 期日(スキーマ 015)。「今日」は Web UI を動かす端末の地方時の日付(tb と同じ)
DUE_SOON_DAYS = 3        # 「期限間近」の既定の日数(今日〜N 日後)
DUE_DAYS_MAX = 365
DUE_VIEWS = {"overdue": "期限切れ", "soon": "期限間近"}
# 既定の並び: 人が見るべきものを先に(300 件で切れても blocked などが残る)。未対応の報告があるものは state に関わらず先頭
STATUS_RANK = ("CASE status WHEN 'blocked' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'review' THEN 3 WHEN 'todo' THEN 4 ELSE 5 END")
OWNER_RE = re.compile(r"[A-Za-z0-9._-]{1,64}")
TASK_LIST_MAX = 300
TITLE_MAX = 80
REPORT_MAX = 1000


def _head(text, n):
    """先頭 n 文字だけ(エスケープ前)。"""
    s = clean("" if text is None else text)
    return s[:n] + ("…" if len(s) > n else "")


def task_stale(t, now=None):
    """in_progress のまま TASK_STALE_HOURS を超えて更新がない(経過時間 or None)。"""
    if t.get("status") != "in_progress":
        return None
    u = parse_ts(t.get("updated_at"))
    if not u:
        return None
    now = now or datetime.now(timezone.utc)
    h = (now - u).total_seconds() / 3600
    return int(h) if h > TASK_STALE_HOURS else None


def today_str():
    return datetime.now().strftime("%Y-%m-%d")


def due_state(t, today=None, days=DUE_SOON_DAYS):
    """期日の状態: "overdue"(今日より前)/ "soon"(今日〜days 日後)/ None。期日なし・done は None。"""
    d = t.get("due")
    if not d or t.get("status") == "done" or not isinstance(d, str):
        return None
    today = today or today_str()
    if d < today:
        return "overdue"
    end = (datetime.strptime(today, "%Y-%m-%d") + timedelta(days=days)).strftime("%Y-%m-%d")
    return "soon" if d <= end else None


def due_html(t, today=None):
    d = t.get("due")
    if not d:
        return "-"
    st = due_state(t, today)
    if st == "overdue":
        return f'<span class=err>{e(d)} 期限切れ</span>'
    if st == "soon":
        return f'<span class=warn>{e(d)} 間近</span>'
    return e(d)


def report_counts(task_id=None):
    """{task_id: {kind: 件数, "open": 人が未対応の question / blocked の件数}}。task_id を渡すとそのタスク 1 件だけ集計する。
    open は resolved_at 列がある DB だけ(無ければキーを作らない)。完了済みのタスクの未解決も数える。"""
    out = {}
    w, a = ("", []) if task_id is None else (" WHERE task_id=?", [task_id])
    for r in q("SELECT task_id, kind, COUNT(*) AS n FROM comments" + w + " GROUP BY task_id, kind", a):
        out.setdefault(r["task_id"], {})[r["kind"]] = r["n"]
    if has_resolution():
        w2 = " AND task_id=?" if task_id is not None else ""
        for r in q("SELECT task_id, COUNT(*) AS n FROM comments WHERE kind IN ('question','blocked') AND resolved_at IS NULL" + w2 +
                   " GROUP BY task_id", a):
            out.setdefault(r["task_id"], {})["open"] = r["n"]
    return out


def counts_label(c):
    if not c:
        return '<span class=note>(報告なし)</span>'
    out = e(" / ".join(f"{k} {c[k]}" for k in ("done", "progress", "question", "blocked") if c.get(k)))
    if c.get("open"):
        out += f' <span class=warn>(未対応 {int(c["open"])}・完了済みのタスクを含む)</span>'
    return out


def page_tasks(qs, ctx=None):
    """タスク一覧(読み取りのみ)。担当・PJ・状態・未報告・停滞で絞り込み、並び替え、状態の件数サマリ。"""
    if ctx is not None:
        ctx["norefresh"] = True   # 入力欄があるので自動更新しない
    proj, st, owner = _one(qs, "project")[:100], _one(qs, "status"), _one(qs, "owner")
    unrep, stale = _one(qs, "unreported") == "1", _one(qs, "stale") == "1"
    sort = _one(qs, "sort")
    ttype, prio, label = _one(qs, "type"), _one(qs, "priority"), _one(qs, "label")
    dview, days_s = _one(qs, "due"), _one(qs, "days")
    tf, hd = has_ticket_fields(), has_due()
    errs = []
    days = DUE_SOON_DAYS
    if dview and dview not in DUE_VIEWS:
        errs.append("期日の絞り込みは overdue(期限切れ)/ soon(期限間近)のどちらかです"); dview = ""
    if days_s:
        if days_s.isdigit() and len(days_s) <= 3 and int(days_s) <= DUE_DAYS_MAX:
            days = int(days_s)
        else:
            errs.append(f"日数は 0〜{DUE_DAYS_MAX} の整数です"); days_s = ""
    if not hd:
        dview = days_s = ""
        if sort == "due":
            sort = ""
    if ttype and ttype not in TASK_TYPES:
        errs.append("種別は " + " / ".join(TASK_TYPES) + " のどれかです"); ttype = ""
    if prio and prio not in TASK_PRIORITIES:
        errs.append("優先度は " + " / ".join(TASK_PRIORITIES) + " のどれかです"); prio = ""
    if label and not LABEL_RE.fullmatch(label):
        errs.append("ラベルは 40 文字までで、空白・カンマ・引用符を含みません"); label = ""
    if not tf:
        ttype = prio = label = ""
        if sort == "priority":
            sort = ""
    if st and st not in TASK_STATUSES:
        errs.append("状態は todo / in_progress / review / done / blocked のどれかです"); st = ""
    if owner and not OWNER_RE.fullmatch(owner):
        errs.append("担当は英数字と . _ - だけです"); owner = ""
    if sort not in TASK_SORTS:
        sort = ""
    base_w, base_p = [], []   # 状態以外の絞り込み(状態の件数サマリにも使う)
    if proj:
        base_w.append("project=?"); base_p.append(proj)
    if owner:
        base_w.append("owner_agent=?"); base_p.append(owner)
    if unrep:
        base_w.append("reported_at IS NULL")
    if ttype:
        base_w.append("type=?"); base_p.append(ttype)
    if prio:
        base_w.append("priority=?"); base_p.append(prio)
    if label:
        base_w.append("EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?)"); base_p.append(label)
    today = today_str()
    if dview == "overdue":   # 期日なし・done は出さない(tb list --overdue と同じ)
        base_w += ["due IS NOT NULL", "due<?", "status!='done'"]; base_p.append(today)
    elif dview == "soon":
        end = (datetime.strptime(today, "%Y-%m-%d") + timedelta(days=days)).strftime("%Y-%m-%d")
        base_w += ["due IS NOT NULL", "due>=?", "due<=?", "status!='done'"]; base_p += [today, end]
    where, params = list(base_w), list(base_p)
    if st:
        where.append("status=?"); params.append(st)
    # 停滞は in_progress で最終更新が古いもの。時刻は ISO(UTC)の文字列比較で絞る
    if stale:
        cutoff = (datetime.now(timezone.utc) - timedelta(hours=TASK_STALE_HOURS)).strftime("%Y-%m-%dT%H:%M:%SZ")
        where += ["status='in_progress'", "updated_at<?"]; params.append(cutoff)
    wh = (" WHERE " + " AND ".join(where)) if where else ""
    if TASK_SORTS[sort][1]:
        order = TASK_SORTS[sort][1]
    else:   # 優先順。resolved_at 列が無い DB では未対応の報告の優先だけ使えない
        open_q = ("(EXISTS (SELECT 1 FROM comments c WHERE c.task_id=tasks.id AND c.kind IN ('question','blocked') AND c.resolved_at IS NULL"
                  " AND tasks.status!='done')) DESC, ") if has_resolution() else ""
        order = open_q + STATUS_RANK + ", project, updated_at DESC"
    rows = q("SELECT * FROM tasks" + wh + " ORDER BY " + order + " LIMIT " + str(TASK_LIST_MAX), params)
    total = (q("SELECT COUNT(*) AS n FROM tasks" + wh, params) or [{"n": len(rows)}])[0]["n"]
    summ = {r["status"]: r["n"] for r in q(
        "SELECT status, COUNT(*) AS n FROM tasks" + (" WHERE " + " AND ".join(base_w) if base_w else "") + " GROUP BY status", base_p)}
    projects = [r["project"] for r in q("SELECT DISTINCT project FROM tasks ORDER BY project")]
    owners = [r["owner_agent"] for r in q("SELECT DISTINCT owner_agent FROM tasks WHERE owner_agent IS NOT NULL ORDER BY owner_agent")]
    kids = {r["parent_id"]: r["n"] for r in q("SELECT parent_id, COUNT(*) AS n FROM tasks WHERE parent_id IS NOT NULL GROUP BY parent_id")}
    labels = [r["value"] for r in q("SELECT DISTINCT j.value FROM tasks, json_each(tasks.labels) j ORDER BY j.value")] if tf else []
    rc = report_counts()

    def href(**kw):
        d = {"project": proj, "status": st, "owner": owner, "unreported": "1" if unrep else "", "stale": "1" if stale else "", "sort": sort,
             "type": ttype, "priority": prio, "label": label, "due": dview, "days": days_s}
        d.update(kw)
        d = {k: v for k, v in d.items() if v}
        return "/tasks" + ("?" + urlencode(d) if d else "")
    cells = [f'<a href="{e(href(status=s))}">{e(s)} {summ.get(s, 0)}</a>' for s in TASK_STATUSES]
    body = err_html("。".join(errs)) * bool(errs)
    if hd:
        n_over = q("SELECT COUNT(*) AS n FROM tasks WHERE due IS NOT NULL AND due<? AND status!='done'", (today,))[0]["n"]
        soon_end = (datetime.strptime(today, "%Y-%m-%d") + timedelta(days=DUE_SOON_DAYS)).strftime("%Y-%m-%d")
        n_soon = q("SELECT COUNT(*) AS n FROM tasks WHERE due IS NOT NULL AND due>=? AND due<=? AND status!='done'", (today, soon_end))[0]["n"]
        if n_over or n_soon:
            body += (f'<p><a href="/tasks?due=overdue&amp;sort=due"><span class={"err" if n_over else "note"}>期限切れ {n_over}</span></a> / '
                     f'<a href="/tasks?due=soon&amp;sort=due">期限間近({DUE_SOON_DAYS} 日以内){n_soon}</a>'
                     f' <span class=note>(今日 = {e(today)}、done は数えない)</span></p>')
    if not db_conn_ok():
        body += '<p class=err>タスク DB がありません(または開けません)</p>'
    body += ("<h2>状態の件数</h2><p>" + " / ".join(cells) + f' / <a href="{e(href(status=""))}">全部 {sum(summ.values())}</a></p>'
             '<p class=note>件数は PJ・担当・未報告の絞り込みを反映し、状態と「停滞だけ」は反映しません。</p>')
    popts = '<option value="">(すべて)</option>' + "".join(f'<option value="{e(p_)}"{" selected" if p_ == proj else ""}>{e(p_)}</option>' for p_ in projects)
    oopts = '<option value="">(すべて)</option>' + "".join(f'<option value="{e(o)}"{" selected" if o == owner else ""}>{e(o)}</option>' for o in owners)
    sopts = '<option value="">(すべて)</option>' + "".join(f'<option value="{s_}"{" selected" if s_ == st else ""}>{s_}</option>' for s_ in TASK_STATUSES)
    ropts = "".join(f'<option value="{k}"{" selected" if k == sort else ""}>{e(v[0])}</option>' for k, v in TASK_SORTS.items()
                    if (tf or k != "priority") and (hd or k != "due"))
    topts = '<option value="">(すべて)</option>' + "".join(f'<option value="{x}"{" selected" if x == ttype else ""}>{x}</option>' for x in TASK_TYPES)
    pyopts = '<option value="">(すべて)</option>' + "".join(f'<option value="{x}"{" selected" if x == prio else ""}>{x}</option>' for x in TASK_PRIORITIES)
    lopts = '<option value="">(すべて)</option>' + "".join(f'<option value="{e(x)}"{" selected" if x == label else ""}>{e(x)}</option>' for x in labels)
    tsel = (f'<label>種別 <select name=type>{topts}</select></label> <label>優先度 <select name=priority>{pyopts}</select></label> '
            f'<label>ラベル <select name=label>{lopts}</select></label> ') if tf else ""
    if hd:
        dopts = '<option value="">(すべて)</option>' + "".join(f'<option value="{k}"{" selected" if k == dview else ""}>{e(v)}</option>' for k, v in DUE_VIEWS.items())
        tsel += (f'<label>期日 <select name=due>{dopts}</select></label> '
                 f'<label>間近の日数 <input name=days size=3 value="{e(days_s)}" placeholder="{DUE_SOON_DAYS}"></label> ')
    body += ('<form method=get action="/tasks"><p>'
             f'<label>PJ <select name=project>{popts}</select></label> <label>担当 <select name=owner>{oopts}</select></label> '
             f'<label>状態 <select name=status>{sopts}</select></label> ' + tsel +
             f'<label><input type=checkbox name=unreported value=1{" checked" if unrep else ""}> 未報告だけ</label> '
             f'<label><input type=checkbox name=stale value=1{" checked" if stale else ""}> 停滞だけ({TASK_STALE_HOURS} 時間超の in_progress)</label> '
             f'<label>並び <select name=sort>{ropts}</select></label> '
             '<button type=submit>絞り込む</button> <a href="/tasks">解除</a></p></form>')
    now = datetime.now(timezone.utc)
    out = []
    for t in rows:
        sh = task_stale(t, now)
        mark = f'<span class=warn>停滞 {sh} 時間</span>' if sh is not None else ""
        par = tlink(t["parent_id"]) if t.get("parent_id") else ""
        kid = f'子 {kids[t["id"]]}' if kids.get(t["id"]) else ""
        out.append([tlink(t["id"]), e(t["project"]), e(t.get("type") or "-"), priority_html(t.get("priority")),
                    due_html(t, today), e(_head(t["title"], TITLE_MAX)), labels_html(t.get("labels"), href), e(t["status"]), e(t["owner_agent"]),
                    " ".join(x for x in (par, e(kid)) if x) or "-", e(t["session_id"]), counts_label(rc.get(t["id"])),
                    str(len(safe_json_list(t["deliverables"]))), "○" if t["reported_at"] else "-",
                    e(t["created_at"]), e(t["updated_at"]), mark])
    if total > len(rows):
        body += (f'<p class=err>{total} 件のうち {len(rows)} 件だけ表示しています(上限 {TASK_LIST_MAX} 件)。'
                 '既定の並びは未対応の報告・blocked・in_progress を先に出します。PJ・担当・状態などで絞り込むと残りを見られます。</p>')
    body += table(["タスク", "PJ", "種別", "優先度", "期日", "題(先頭 80 文字)", "ラベル", "状態", "担当", "親 / 子", "session_id",
                   "報告(種別ごとの件数)", "成果物", "報告済み", "作成", "更新", "目印"], out)
    body += (f'<p class=note>最大 {TASK_LIST_MAX} 件。「報告済み」は tb report で指揮官に届いたもの。未対応は人がまだ「対応済み」にしていない question / blocked。'
             "ラベルを押すとそのラベルで絞り込みます。期日は tb add / update --due で付けます(期日なしは期限切れになりません)。題は先頭だけ、本文は出しません(詳細画面で報告の先頭を見られます)。</p>")
    return "タスク", body


def has_due():
    """tasks に期日の列があるか(スキーマ 016 より前の DB では無い)。"""
    return any(r.get("name") == "due" for r in q("PRAGMA table_info(tasks)"))


def has_ticket_fields():
    """tasks に種別・優先度・ラベルの列があるか(スキーマ 014 より前の DB では無い。tb を一度動かせば足される)。"""
    return any(r.get("name") == "labels" for r in q("PRAGMA table_info(tasks)"))


def priority_html(p):
    if not p:
        return "-"
    return f'<span class=warn>{e(p)}</span>' if p in ("highest", "high") else e(p)


def labels_html(raw, href=None):
    labs = [x for x in safe_json_list(raw) if isinstance(x, str)]
    if not labs:
        return "-"
    if href is None:
        return " ".join(f"<code>{e(x)}</code>" for x in labs)
    return " ".join(f'<a href="{e(href(label=x))}"><code>{e(x)}</code></a>' for x in labs)


REF_URL_RE = re.compile(r"https?://[^\s\"'<>]{1,500}")


def refs_html(raw):
    """外部リンク(Slack・PR など)。http(s) の URL だけをリンクにし、それ以外は文字として出す。"""
    out = []
    for r in safe_json_list(raw):
        if not isinstance(r, dict):
            continue
        kind, url = str(r.get("kind") or "link"), str(r.get("url") or "")
        if REF_URL_RE.fullmatch(url):
            out.append(f'<code>{e(kind)}</code> <a href="{e(url)}" rel="noopener noreferrer" target="_blank">{e(_head(url, TITLE_MAX))}</a>')
        else:
            out.append(f"<code>{e(kind)}</code> {e(_head(url, TITLE_MAX))}")
    return "<br>".join(out) or "-"


def has_sprints():
    """sprints 表と tasks.sprint_id があるか(スキーマ 016 より前の DB では無い。tb を一度動かせば足される)。"""
    return (bool(q("SELECT name FROM sqlite_master WHERE name='sprints'"))
            and any(r.get("name") == "sprint_id" for r in q("PRAGMA table_info(tasks)")))


SPRINT_RE = re.compile(r"S-[0-9]{1,9}")
BOARD_COLS = ["todo", "in_progress", "review", "blocked", "done"]


def page_board(qs, ctx=None):
    """スプリントのボード(読み取りのみ)。状態ごとの列に、優先度順でカードを並べる。書き込みは手元の tb で行う。"""
    if ctx is not None:
        ctx["norefresh"] = True   # 入力欄があるので自動更新しない
    if not has_sprints():
        return "ボード", ('<p class=note>スプリントの表がまだありません。手元で <code>tb sprints</code> を一度実行すると作られます。</p>')
    proj, sid, label = _one(qs, "project")[:100], _one(qs, "sprint"), _one(qs, "label")
    errs = []
    if sid and not SPRINT_RE.fullmatch(sid):
        errs.append("スプリントは S-<番号> です"); sid = ""
    if label and not LABEL_RE.fullmatch(label):
        errs.append("ラベルは 40 文字までで、空白・カンマ・引用符を含みません"); label = ""
    sprints = q("SELECT s.*, COUNT(t.id) AS n, SUM(t.status='done') AS done_n, COALESCE(SUM(t.points),0) AS pts,"
                " COALESCE(SUM(CASE WHEN t.status='done' THEN t.points END),0) AS done_pts"
                " FROM sprints s LEFT JOIN tasks t ON t.sprint_id=s.id" + (" WHERE s.project=?" if proj else "") +
                " GROUP BY s.id ORDER BY CASE s.state WHEN 'active' THEN 1 WHEN 'planned' THEN 2 ELSE 3 END, s.project, s.id DESC",
                (proj,) if proj else ())
    cur = None
    if sid:
        cur = next((x for x in sprints if "S-" + str(x["id"]) == sid), None)
        if cur is None:
            errs.append(f"スプリント {sid} が見つかりません"); sid = ""
    if cur is None:
        cur = next((x for x in sprints if x["state"] == "active"), None)
    body = err_html("。".join(errs)) * bool(errs)
    projects = [r["project"] for r in q("SELECT DISTINCT project FROM sprints ORDER BY project")]
    popts = '<option value="">(すべて)</option>' + "".join(f'<option value="{e(p_)}"{" selected" if p_ == proj else ""}>{e(p_)}</option>' for p_ in projects)
    sopts = "".join(f'<option value="S-{int(x["id"])}"{" selected" if cur and x["id"] == cur["id"] else ""}>'
                    f'S-{int(x["id"])} {e(x["project"])} / {e(x["name"])} [{e(x["state"])}]</option>' for x in sprints)
    body += ('<form method=get action="/board"><p>'
             f'<label>PJ <select name=project>{popts}</select></label> '
             f'<label>スプリント <select name=sprint><option value="">(active なもの)</option>{sopts}</select></label> '
             f'<label>ラベル <input type=text name=label value="{e(label)}" maxlength=40 size=12></label> '
             '<button type=submit>表示</button> <a href="/board">解除</a></p></form>')
    if cur is None:
        body += "<p>表示するスプリントがありません(active なものが無いか、まだ作っていません)。手元で <code>tb sprint-add</code> / <code>tb sprint-start</code>。</p>"
    else:
        cid = int(cur["id"])
        body += (f'<h2>S-{cid} {e(cur["name"])} <span class=note>[{e(cur["state"])}] {e(cur.get("start_date") or "-")} 〜 {e(cur.get("end_date") or "-")}'
                 f'・{e(cur["project"])}</span></h2>')
        if cur.get("goal"):
            body += f"<p>目標: {e(cur['goal'])}</p>"
        body += (f'<p>完了 {int(cur["done_n"] or 0)} / {int(cur["n"] or 0)} 件・ポイント {int(cur["done_pts"] or 0)} / {int(cur["pts"] or 0)}</p>')
        rows = q("SELECT * FROM tasks WHERE sprint_id=? AND (?='' OR EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE value=?))"
                 " ORDER BY CASE priority WHEN 'highest' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 ELSE 5 END, id"
                 " LIMIT 300", (cid, label, label))

        def card(t):
            pts = f' <span class=note>{int(t["points"])}pt</span>' if t.get("points") is not None else ""
            return (f'<div class=card>{tlink(t["id"])} {priority_html(t.get("priority"))} <span class=note>{e(t.get("type") or "")}</span>{pts}<br>'
                    f'{e(_head(t["title"], TITLE_MAX))}<br><span class=note>{e(t.get("owner_agent") or "-")}</span> '
                    f'{labels_html(t.get("labels"), lambda label: "/board?" + urlencode({"sprint": "S-" + str(cid), "label": label}))}</div>')
        cols = [[card(t) for t in rows if t["status"] == c] for c in BOARD_COLS]
        body += table([f"{c} ({len(cs)})" for c, cs in zip(BOARD_COLS, cols)], [["".join(cs) or "-" for cs in cols]])
        body += ("<p class=note>読み取りのみ。カードの移動・スプリントへの出し入れは手元で <code>tb update &lt;id&gt; --status …</code> / "
                 "<code>--sprint S-n</code>、閉じるのは <code>tb sprint-close S-n [--carry S-m]</code>。</p>")
    body += "<h2>スプリント一覧</h2>" + table(["スプリント", "PJ", "名前", "状態", "期間", "完了 / 件数", "ポイント"],
        [[f'<a href="/board?sprint=S-{int(x["id"])}">S-{int(x["id"])}</a>', e(x["project"]), e(x["name"]), e(x["state"]),
          f'{e(x.get("start_date") or "-")} 〜 {e(x.get("end_date") or "-")}', f'{int(x["done_n"] or 0)} / {int(x["n"] or 0)}',
          f'{int(x["done_pts"] or 0)} / {int(x["pts"] or 0)}'] for x in sprints])
    return "ボード", body


def db_conn_ok():
    c = db_conn()
    if c is None:
        return False
    c.close()
    return True


def safe_json_list(s):
    try:
        v = json.loads(s or "[]")
        return v if isinstance(v, list) else []
    except ValueError:
        return []


RESOLVE_MSG = {"done": "対応済みにしました。", "already": "すでに対応済みでした。",
               "error": "対応済みにできませんでした(tb の実行に失敗)。"}


def resolvable(c):
    return c.get("kind") in ("question", "blocked") and "resolved_at" in c and not c.get("resolved_at")


def resolve_form(c, back, ctx):
    """「対応済みにする」。ログイン済みのブラウザからの POST だけを受ける(書き込みは tb resolve が行う)。"""
    ctx["norefresh"] = True  # 入力欄があるので自動更新しない
    return (f'<form method=post action="/resolve"><input type=hidden name=csrf value="{e(ctx.get("csrf", ""))}">'
            f'<input type=hidden name=id value="{int(c["id"])}"><input type=hidden name=back value="{e(back)}">'
            f'<input type=text name=note maxlength={NOTE_MAX} placeholder="メモ(任意)" size=18> '
            '<button type=submit>対応済みにする</button></form>')


def resolved_label(c):
    if not c.get("resolved_at"):
        return ""
    return "対応済み " + e(c["resolved_at"]) + (f'<br>{e(c["resolved_note"])}' if c.get("resolved_note") else "")


def resolve_cell(c, back, ctx):
    return resolve_form(c, back, ctx) if resolvable(c) else resolved_label(c)


def resolve_notice(qs):
    st = (qs.get("resolved") or [""])[0]
    return f"<p class=note>{e(RESOLVE_MSG[st])}</p>" if st in RESOLVE_MSG else ""


def page_task(tid, qs=None, ctx=None):
    ctx = {} if ctx is None else ctx
    rows = q("SELECT * FROM tasks WHERE id=?", (tid,))
    if not rows:
        return None
    t = rows[0]
    info = [(k, e(t.get(k))) for k in ["id", "project", "title", "status", "owner_agent", "session_id", "account",
                                        "parent_id", "reported_at", "created_at", "updated_at"]]
    if t.get("parent_id"):
        info[7] = ("parent_id", tlink(t["parent_id"]))
    if "labels" in t:
        info[3:3] = [("種別", e(t.get("type"))), ("優先度", priority_html(t.get("priority"))),
                     ("ラベル", labels_html(t.get("labels"), lambda label: "/tasks?" + urlencode({"label": label})))]
        info.append(("説明", f'<pre>{e(_head(t.get("description"), REPORT_MAX))}</pre>' if t.get("description") else "-"))
    if "refs" in t:
        info.append(("外部リンク", refs_html(t.get("refs"))))
    if "due" in t:
        info.append(("期日", due_html(t)))
    if "sprint_id" in t:
        info.append(("スプリント", f'<a href="/board?sprint=S-{int(t["sprint_id"])}">S-{int(t["sprint_id"])}</a>' if t.get("sprint_id") is not None else "バックログ"))
        info.append(("ポイント", e(t.get("points")) if t.get("points") is not None else "-"))
    info.append(("summary", e(_head(t.get("summary"), REPORT_MAX))))
    info.append(("note", e(_head(t.get("note"), REPORT_MAX))))
    sh = task_stale(t)
    if sh is not None:
        info.append(("目印", f'<span class=warn>停滞: in_progress のまま {sh} 時間更新がありません</span>'))
    info.append(("成果物", "<br>".join(doc_link(r, raw) for r, raw in docs.task_refs(t["id"], t["deliverables"])) or "-"))
    body = resolve_notice(qs or {}) + "<table>" + "".join(f"<tr><th>{k}</th><td>{v}</td></tr>" for k, v in info) + "</table>"
    kids = q("SELECT id, title, status, owner_agent, updated_at FROM tasks WHERE parent_id=? ORDER BY id", (tid,))
    if kids:
        body += "<h2>子タスク</h2>" + table(["タスク", "題", "状態", "担当", "更新"],
            [[tlink(k["id"]), e(_head(k["title"], TITLE_MAX)), e(k["status"]), e(k["owner_agent"]), e(k["updated_at"])] for k in kids])
    if "labels" in t:
        lk = q("SELECT CASE WHEN l.from_id=? THEN l.link_type WHEN l.link_type='blocks' THEN 'blocked by'"
               " WHEN l.link_type='duplicates' THEN 'duplicated by' ELSE 'relates' END AS rel,"
               " x.id, x.title, x.status FROM task_links l JOIN tasks x ON x.id = CASE WHEN l.from_id=? THEN l.to_id ELSE l.from_id END"
               " WHERE l.from_id=? OR l.to_id=? ORDER BY l.id", (tid, tid, tid, tid))
        if lk:
            body += "<h2>リンク</h2>" + table(["関係", "タスク", "題", "状態"],
                [[e(x["rel"]), tlink(x["id"]), e(_head(x["title"], TITLE_MAX)), e(x["status"])] for x in lk])
    cm = q("SELECT * FROM comments WHERE task_id=? ORDER BY id DESC", (tid,))
    back = "/tasks/" + quote(str(tid), safe="")
    body += "<h2>報告</h2>" + counts_label(report_counts(tid).get(tid)) + table(
        ["id", "種別", "本文(先頭)", "成果物", "from", "配送", "時刻", "対応"],
        [[f"C-{c['id']}", e(c["kind"]), f'<pre>{e(_head(c["text"], REPORT_MAX))}</pre>',
          "<br>".join(doc_link(r, raw) for r, raw in docs.comment_refs(c["id"], c["deliverables"])), e(c["from_name"]),
          "済" if c["delivered_at"] else "未", e(c["created_at"]), resolve_cell(c, back, ctx)] for c in cm])
    body += (f"<p class=note>本文は先頭 {REPORT_MAX} 文字だけです(…は続きがあるしるし)。全文は手元で <code>tb comments {e(clean(str(tid)))}</code> か、"
             "成果物のパスで読んでください。</p>")
    px = q("SELECT * FROM proxy_requests WHERE task_id=? ORDER BY id DESC", (tid,))
    body += "<h2>代行依頼</h2>" + table(
        ["id", "状態", "effect", "コマンド", "理由", "結果"],
        [[e(p["id"]), e(p["status"]), e(p["effect"]), f'<pre>{e(p["cmd"])}</pre>', e(p["why"]), e(p["result"])]
         for p in px])
    return f"タスク {clean(t['id'])}", body


def page_inbox(qs, ctx=None):
    ctx = {} if ctx is None else ctx
    comments, proxies, blocked = needs_human()
    ids = {c["id"] for c in comments}
    body = resolve_notice(qs) + "<h2>承認待ち(どこで・どう承認するか)</h2>"
    appr = [it for it in urgent_items() if it.get("kind") in APPROVAL_KINDS]
    body += table(["種別", "何が・中身・どう対応するか", "時刻"],
                  [[f'<span class=warn>{e(it["label"])}</span>', summary3(it), e(it["ts"])] for it in appr])
    body += ("<p class=note>「手元で tb …」は、人の端末(agent の pane ではない)で実行するコマンドです。"
             "agent 操作の承認だけは Web UI の agent 一覧からもできます。</p>")
    how = {it["id"]: it.get("how") for it in urgent_items() if it["id"].startswith("c:")}
    body += "<h2>要対応の報告 (needs-human)</h2>"
    body += table(["id", "種別", "タスク", "本文", "from", "時刻", "どう対応するか", "操作"],
                  [[f"C-{c['id']}", f'<span class=warn>{e(c["kind"])}</span>', tlink(c["task_id"]),
                    f'<pre>{e(c["text"])}</pre>', e(c["from_name"]), e(c["created_at"]),
                    f'<span class=note>{e(how.get("c:" + str(c["id"])) or "")}</span>',
                    resolve_cell(c, "/inbox", ctx)] for c in comments])
    body += "<h3>代行依頼 (pending-human)</h3>" + table(
        ["id", "タスク", "effect", "コマンド", "理由"],
        [[e(p["id"]), tlink(p["task_id"]), e(p["effect"]), f'<pre>{e(p["cmd"])}</pre>', e(p["why"])] for p in proxies])
    body += "<h3>blocked の agent</h3>" + table(
        ["agent", "workspace", ""],
        [[alink(a, "#approval"), e(a.get("workspace_id")), f'<a href="/agents/{quote(agent_key(a), safe="")}#approval">承認待ちの内容を見る</a>']
         for a in blocked])
    rest = [c for c in q("SELECT * FROM comments ORDER BY id DESC LIMIT 60") if c["id"] not in ids][:50]
    body += "<h2>直近の報告</h2>" + table(
        ["id", "種別", "タスク", "本文", "from", "時刻", "状態"],
        [[f"C-{c['id']}", e(c["kind"]), tlink(c["task_id"]), f'<pre>{e(c["text"])}</pre>', e(c["from_name"]),
          e(c["created_at"]), resolved_label(c)] for c in rest])
    return "報告・承認待ち", body


def page_jobs(_qs):
    names = job_names()
    try:
        logged = sorted(n[:-4] for n in os.listdir(SCHED_LOGS()) if n.endswith(".log") and JOB_RE.fullmatch(n[:-4]))
    except OSError:
        logged = []
    rows = []
    for n in sorted(set(names) | set(logged)):
        info, err = job_info(n) if n in names else (None, "jobs/ に conf がない")
        last, lr = job_last(n)
        if info:
            sched = (f'{e(interval_text(info))}おき / {e(info["start"])}-{e(info["end"])} / {e(info["days"])}'
                     + (" <span class=note>(Web UI で変更済み)</span>" if info.get("overridden") else ""))
            st = "有効" if info.get("enabled") else '<span class=warn>一時停止</span>'
            if info["kind"] == "prompt":
                what = f'<b>agent</b> <code>{e(info["target"])}</code> に送る'
            else:
                cs = command_summary((job_defs(n) or {}).get("COMMAND", ""))
                what = "<b>コマンド</b> " + (" ".join(f"<code>{e(c)}</code>" for c, _ in cs) if cs else "を実行")
            summ = job_summary(n, info)
            if summ:
                what += f'<br><span class=note>{e(summ)}</span>'
        else:
            sched, st, what = f'<span class=warn>{e(err)}</span>', "-", "-"
        rows.append([f'<a href="/jobs/{quote(n)}">{e(n)}</a>' if n in names else e(n), what, sched, st,
                     (f'{e(last.get("ts"))} {e(last.get("action"))} {e(last.get("reason"))}' if last else "-"),
                     (f'{e(lr.get("ts"))} / {e(lr.get("action"))}' + (f' (exit {e(lr.get("exit"))})' if "exit" in lr else "")
                      if lr else "-")])
    note = ("<p class=note>「何をする」の 2 行目は conf の先頭のコメント(無ければコマンドから推した説明)です。job 名を開くと、型(agent に送る / コマンドを実行)・宛先と今の状態・送られる本文やコマンド・次回の予定・承認の扱いを読めます。"
            "スケジュール(間隔・時間帯・曜日・一時停止)だけ変更できます(次の分から反映)。</p>")
    return "定期実行", note + table(["job", "何をする(宛先)", "間隔 / 時間帯 / 曜日", "状態", "最終記録", "最後の実行"], rows)


def _fold(text, kind_label, limit_chars=600, limit_lines=12):
    """長い本文は折りたたむ(全文は details の中)。text は無害化・マスク済みの文字列。"""
    body = f"<pre>{e(text)}</pre>"
    if len(text) > limit_chars or text.count("\n") >= limit_lines:
        return f"<details><summary>{e(kind_label)}を開く({len(text)} 文字)</summary>{body}</details>"
    return body


def _job_overview(name, info):
    """型・宛先・本文・いつ・直近の結果・副作用・承認の扱い(読み取りのみ。conf は実行しない)。"""
    defs = job_defs(name) or {}
    cx = defs.get("_complex", set())
    inexact = '<p class=warn>シェルで展開・解釈される記述です(表示は不完全。実際に実行・送信される内容は、展開後の結果になります)。conf を直接確認してください。</p>'
    rows = []
    is_prompt = info["kind"] == "prompt"
    if defs.get("_truncated"):
        rows.append(("注意", '<span class=warn>conf が 64 KiB を超えているため、後半は読んでいません(表示は不完全)</span>'))
    about = job_about(name)
    if about:
        rows.append(("説明", "<br>".join(e(x) for x in about) + '<br><span class=note>(conf の先頭のコメント)</span>'))
    rows.append(("型", "<b>PROMPT 型</b>: agent にプロンプトを送る" if is_prompt else "<b>COMMAND 型</b>: コマンドを実行する(agent は介さない)"))
    warn = []
    if is_prompt:
        tgt = info["target"]
        st = agent_state(tgt)
        if st.get("err"):
            a_html = f'<code>{e(tgt)}</code> <span class=warn>状態を取得できません: {e(st["err"])}</span>'
        elif st["count"] == 0:
            a_html = f'<code>{e(tgt)}</code> <span class=warn>herdr に見つかりません(送れず failed になります)</span>'
            warn.append("宛先が存在しません")
        elif st["count"] > 1:
            a_html = f'<code>{e(tgt)}</code> <span class=warn>同じ名前が {st["count"]} 体あり、一意に決まりません(failed になります)</span>'
        else:
            s = st["status"]
            cls = {"working": "st-working", "blocked": "st-blocked", "idle": "st-idle", "done": "st-done"}.get(s, "")
            a_html = (f'<code>{e(tgt)}</code> 今: <span class="{cls}">{e(s or "不明")}</span>'
                      + (f" / workspace {e(st['workspace'])}" if st["workspace"] else "")
                      + (f" / herdr セッション {e(st['registry'].get('herdr_session'))}" if st["registry"].get("herdr_session") else "")
                      + (f" / 役割 {e(st['registry'].get('role'))}" if st["registry"].get("role") else ""))
            if s not in ("idle", "done"):
                a_html += ' <span class=note>(idle / done 以外のときは送らず skipped になります)</span>'
        reg = st.get("registry") or {}
        if reg.get("state") == "stopped":
            a_html += ' <span class=warn>tb の台帳では停止中です</span>'
        rows.append(("誰に送られるか", a_html))
        skip = defs.get("SKIP_IF", "")
        rows.append(("送る条件", "宛先が idle / done のときだけ送ります。" + (f"さらに状態が <code>{e(skip)}</code> のときは送りません(SKIP_IF)。" if skip else "")))
        text, note, src = None, "", ""
        if defs.get("PROMPT_FILE"):
            if "PROMPT_FILE" in cx:
                body, note = None, "パスにシェルで展開される記述($ や引用符など)が含まれるため、どのファイルか確定できず、中身は表示しません"
            else:
                body, note = prompt_file(defs["PROMPT_FILE"])
            src = f'ファイル <code>{e(defs["PROMPT_FILE"])}</code>(展開前の記述そのまま)'
            text = body
        elif "PROMPT" in defs:
            text, src = defs["PROMPT"], "conf の PROMPT(展開前の記述そのまま)" + (inexact if "PROMPT" in cx else "")
        if text is None:
            rows.append(("何が送られるか", f'{src or "本文の定義が見つかりません"} <span class=note>{e(note)}</span>' + (inexact if "PROMPT_FILE" in cx else "")))
        else:
            masked, nmask = mask(clean(text))
            rows.append(("何が送られるか", f"{src}" + (f' <span class=note>{e(note)}</span>' if note else "") + _fold(masked, "本文")
                         + (f'<p class=note>トークンらしい文字列 {nmask} 件をマスクしました</p>' if nmask else "")))
    else:
        cmd = defs.get("COMMAND", "")
        cs = command_summary(cmd)
        if cs:
            rows.append(("中身(推定)", "<br>".join(f"<code>{e(c)}</code>: {e(d)}" for c, d in cs)
                         + '<br><span class=note>(コマンドの中の tb のサブコマンドから推した説明。実際の動きは下の本文)</span>'))
        masked, nmask = mask(clean(cmd))
        rows.append(("何が実行されるか", "<code>bash -c</code> でそのまま実行(展開前の記述そのまま。タイムアウト "
                     + e(defs.get("COMMAND_TIMEOUT") or "既定") + " 秒)" + (inexact if "COMMAND" in cx else "") + _fold(masked, "コマンド")
                     + (f'<p class=note>トークンらしい文字列 {nmask} 件をマスクしました</p>' if nmask else "")))
    nxt, nnote = next_runs(info)
    rows.append(("いつ", f'{e(interval_text(info))}おき / {e(info["start"])}-{e(info["end"])} / {e(info["days"])} / TZ {e(info["tz"])}'
                 + ("" if info.get("enabled") else ' <span class=warn>一時停止中</span>')))
    rows.append(("次回の予定(目安)", " → ".join(e(x) for x in nxt) + (' <span class=note>(ジョブの TZ の時刻。間隔・時間帯・曜日からの推定で、実際の起動と数秒ずれることがあります)</span>' if nxt else f'<span class=note>{e(nnote)}</span>')))
    last, lr = job_last(name)
    recs = parse_jsonl(tail_lines(os.path.join(SCHED_LOGS(), name + ".log"), 200))
    counts = {}
    for r in recs:
        counts[r.get("action")] = counts.get(r.get("action"), 0) + 1
    rows.append(("直近の結果", (f'{e(lr.get("ts"))} {e(lr.get("action"))}' + (f' (exit {e(lr.get("exit"))})' if "exit" in lr else "") if lr else "記録なし")
                 + (f' / 最後の記録: {e(last.get("ts"))} {e(last.get("action"))} {e(last.get("reason"))}' if last else "")
                 + (f'<br><span class=note>直近 {len(recs)} 件: ' + ", ".join(f"{e(k)} {v}" for k, v in sorted(counts.items(), key=lambda x: str(x[0]))) + "</span>" if recs else "")))
    if is_prompt:
        rows.append(("送信の記録", f'<a href="/ledger?kind=scheduler&amp;target={quote(info["target"])}">送信履歴(scheduler → {e(info["target"])})</a>'))
    rows.append(("副作用", ("宛先 agent が受け取って動きます。その agent が Slack・Jira など外部へ書けるかは、webui からは分かりません(不明)。"
                          if is_prompt else "コマンドが何を書くかは、本文から推測しません(不明)。上のコマンド本文を確認してください。")))
    d = send_task_decision(info["target"]) if is_prompt else None
    appr = ("scheduler からの送信(<code>tb system-send</code>)は approvals.conf を見ず、人の承認待ちにはなりません。"
            "上の条件を満たせば自動で送られます。" if is_prompt else "COMMAND 型の実行は approvals.conf の対象外です(承認待ちにならず、時刻になれば実行されます)。")
    if d:
        appr += f' <span class=note>参考: 通常の <code>tb send</code> の <code>send:task:{e(info["target"])}</code> は現在 {e(d)}(この job には適用されません)。</span>'
    rows.append(("承認の扱い", appr))
    html_rows = "".join(f"<tr><th style='text-align:left;vertical-align:top;white-space:nowrap'>{e(k)}</th><td>{v}</td></tr>" for k, v in rows)
    return (f'<h2>このジョブが行うこと</h2><table>{html_rows}</table>'
            '<p class=note>conf の記述を実行せずに読んでいます(<code>$変数</code> などは展開せず、そのまま表示)。'
            'トークンらしい文字列はマスクしますが完全ではありません。機密を含みうる画面です。</p>')


def page_job(name, csrf, error="", saved=False):
    """1 ジョブの設定フォーム(自動更新しない)。"""
    info, err = job_info(name)
    if not info:
        return "定期実行: " + name, err_html(err or error) + '<p><a href="/jobs">一覧へ</a></p>'
    days = info["days"]
    preset = days if days in dict(DAY_PRESETS) else ""
    opts = "".join(f'<option value={k}{" selected" if preset == k else ""}>{e(v)}</option>' for k, v in DAY_PRESETS)
    opts += f'<option value=custom{" selected" if not preset else ""}>曜日を指定</option>'
    toks = set(days.lower().replace(",", " ").split()) if not preset else set()
    checks = "".join(f'<label><input type=checkbox name=day_{t} value=1{" checked" if t in toks else ""}> {t.capitalize()}</label> '
                     for t in DAY_TOKENS)
    hid = f'<input type=hidden name=csrf value="{e(csrf)}">'
    sec = interval_sec(info)
    unit = "min" if sec % 60 == 0 else "sec"
    iv = sec // 60 if unit == "min" else sec
    units = "".join(f'<option value={u}{" selected" if u == unit else ""}>{e(lbl)}</option>' for u, lbl, _ in INTERVAL_UNITS)
    form = (
        f'<form method=post action="/jobs/{quote(name)}">{hid}<input type=hidden name=action value=save>'
        f'<p><label>間隔<br><input type=number name=interval min=1 max=86400 required value="{iv}"></label> '
        f'<select name=interval_unit>{units}</select> <span class=note>分は 1440 まで、秒は 86400 まで</span></p>'
        f'<p><label>開始 <input type=time name=start step=1 required value="{e(info["start"])}"></label> '
        f'<label>終了 <input type=time name=end step=1 required value="{e(info["end"])}"></label>'
        f' <span class=note>両端を含む(秒を省いた終了はその分の 59 秒まで)。日またぎは不可。TZ は {e(info["tz"])}</span></p>'
        f'<p><label>曜日 <select name=days>{opts}</select></label> {checks}</p>'
        f'<p><label><input type=checkbox name=enabled value=1{" checked" if info["enabled"] else ""}> 有効(外すと一時停止)</label></p>'
        '<button type=submit>保存</button></form>'
        f'<form method=post action="/jobs/{quote(name)}" style="margin-top:12px">{hid}<input type=hidden name=action value=reset>'
        '<button type=submit>conf の値に戻す</button></form>')
    what = f'agent <code>{e(info["target"])}</code> にプロンプトを送る' if info["kind"] == "prompt" else "コマンドを実行する"
    msg = '<p class=note>保存しました。次の分から反映されます。</p>' if saved else ""
    return "定期実行: " + name, (
        err_html(error) + msg + f"<p>{what}ジョブ。{'Web UI で変更済み。' if info.get('overridden') else ''}</p>" + _job_overview(name, info) + "<h2>スケジュールの変更</h2>" + form +
        "<p class=note>変更できるのはスケジュールだけです(宛先・本文・コマンドは jobs/*.conf を人が編集)。"
        f'保存先: <code>scheduler/settings/{e(name)}.settings</code></p><p><a href="/jobs">一覧へ</a></p>')


DATE_RE = re.compile(r"\d{4}-\d{2}-\d{2}", re.ASCII)
TARGET_RE = re.compile(r"[A-Za-z0-9._-]{1,64}")


def _one(qs, name):
    return ((qs.get(name) or [""])[0]).strip()


def page_ledger(qs, ctx=None):
    """送信履歴(tb の messages 表。読み取り専用)。種別ごとの件数サマリ + 直近 100 件。failed は既定で畳む。"""
    if ctx is not None:
        ctx["norefresh"] = True   # 絞り込みの入力欄があるので自動更新しない
    target, d_from, d_to = _one(qs, "target"), _one(qs, "from"), _one(qs, "to")
    kind, result = _one(qs, "kind"), _one(qs, "result")
    errs = []
    if target and not TARGET_RE.fullmatch(target):
        errs.append("宛先は英数字と . _ - だけです"); target = ""
    for nm, v in (("開始日", d_from), ("終了日", d_to)):
        if v and not DATE_RE.fullmatch(v):
            errs.append(f"{nm}は YYYY-MM-DD の形です")
            if nm == "開始日":
                d_from = ""
            else:
                d_to = ""
    kinds = ledger_kinds()
    if kind and kind not in kinds:
        errs.append("その種別は履歴にありません"); kind = ""
    if result == "all":
        result = ""
        show_all = True
    else:
        show_all = False
    if result and result not in LEDGER_RESULTS:
        errs.append("結果は sent / busy / failed / all のどれかです"); result = ""

    def link(**ch):
        d = {"target": target, "from": d_from, "to": d_to, "kind": kind, "result": result or ("all" if show_all else "")}
        d.update(ch)
        return "/ledger?" + e(urlencode({k: v for k, v in d.items() if v}))

    summ = ledger_summary(target, d_from, d_to)
    kinds_seen = sorted({r["kind"] for r in summ})
    sm = {(r["kind"], r["result"]): r["n"] for r in summ}
    rows = []
    for k in kinds_seen:
        cells = []
        for rs in LEDGER_RESULTS:
            n = sm.get((k, rs), 0)
            txt = f'<span class=warn>{n}</span>' if rs == "failed" and n else str(n)
            cells.append(f'<a href="{link(kind=k, result=rs)}">{txt}</a>' if n else "0")
        rows.append([f'<a href="{link(kind=k, result="")}">{e(k)}</a>'] + cells + [str(sum(sm.get((k, rs), 0) for rs in LEDGER_RESULTS))])
    failed_total = sum(n for (k, rs), n in sm.items() if rs == "failed" and (not kind or k == kind))
    body = "<h2>種別ごとの件数</h2>" + table(["種別", "sent", "busy", "failed", "計"], rows)
    kopts = '<option value="">(すべて)</option>' + "".join(
        f'<option value="{e(k)}"{" selected" if k == kind else ""}>{e(k)}</option>' for k in kinds)
    ropts = "".join(f'<option value="{v}"{" selected" if (result or ("all" if show_all else "")) == v else ""}>{lbl}</option>'
                    for v, lbl in (("", "failed 以外(既定)"), ("all", "すべて"), ("sent", "sent"), ("busy", "busy"), ("failed", "failed")))
    form = ('<form method=get action="/ledger"><p>'
            f'<label>種別 <select name=kind>{kopts}</select></label> '
            f'<label>結果 <select name=result>{ropts}</select></label> '
            f'<label>宛先 <input type=text name=target value="{e(target)}" size=16></label> '
            f'<label>開始日 <input type=date name=from value="{e(d_from)}"></label> '
            f'<label>終了日 <input type=date name=to value="{e(d_to)}"></label> '
            '<button type=submit>絞り込む</button> <a href="/ledger">解除</a></p></form>')
    recs = ledger_rows(target, d_from, d_to, kind, result, include_failed=show_all)
    out = []
    for r in recs:
        ref = r["ref"]
        if r["kind"] == "send" and re.fullmatch(r"O-\d+", ref):
            ref_html = f'<a href="/outbox">{e(ref)}</a>'
        else:
            ref_html = e(ref)
        res = f'<span class=warn>{e(r["result"])}</span>' if r["result"] == "failed" else e(r["result"])
        out.append([e(r["created_at"]), e(r["kind"]), ref_html, e(r["target"]), res, e(r["first"])])
    note = ""
    if not result and not show_all and failed_total:
        note = f'<p class=note>failed {failed_total} 件は畳んでいます。<a href="{link(result="failed")}">failed だけを見る</a> / <a href="{link(result="all")}">すべて見る</a></p>'
    body += "<h2>履歴</h2>" + err_html("。".join(errs)) * bool(errs) + form + note + table(["時刻(UTC)", "種別", "参照", "宛先", "結果", "本文(1 行目)"], out)
    body += (f"<p class=note>tb の messages 表(送ろうとしたものすべて)を新しい順に最大 {LEDGER_MAX} 件。時刻と日付は UTC。"
             "send の O-n は送信の依頼の状態(承認待ちなど)を見る <a href=\"/outbox\">送信</a> 画面に対応します。本文は 1 行目だけ(全文は表示しません)。</p>")
    return "送信履歴", body


def _catalog_section(qs, rules, err, catalog, cerr, cbad):
    """権限カタログ(操作の定義)と approvals.conf を突き合わせた一覧。読み取りのみ。"""
    if cerr:
        return "<h2>操作ごとの現在の権限</h2>" + err_html(cerr)
    if err:
        return "<h2>操作ごとの現在の権限</h2>" + err_html("approvals.conf を全部は読めていないので、現在の値は出しません")
    f_kind, f_state = _one(qs, "kind"), _one(qs, "state")
    f_fb, f_al = _one(qs, "fallback") == "1", _one(qs, "allowed") == "1"
    if f_kind not in perms.KINDS:
        f_kind = ""
    if f_state not in perms.STATES:
        f_state = ""
    rows_all = perms.catalog_rows(rules, catalog)
    rows = []
    for r in rows_all:
        c = r["entry"]
        if f_kind and c["kind"] != f_kind or f_state and c["state"] != f_state:
            continue
        if f_fb and r["explicit"] or f_al and r["value"] != "allow":
            continue
        rows.append(r)
    kopts = '<option value="">(すべて)</option>' + "".join(
        f'<option value="{k}"{" selected" if k == f_kind else ""}>{k}</option>' for k in perms.KINDS)
    sopts = '<option value="">(すべて)</option>' + "".join(
        f'<option value="{k}"{" selected" if k == f_state else ""}>{k}</option>' for k in perms.STATES)
    form = ('<form method=get action="/perms"><p>'
            f'<label>種別 <select name=kind>{kopts}</select></label> <label>状態 <select name=state>{sopts}</select></label> '
            f'<label><input type=checkbox name=fallback value=1{" checked" if f_fb else ""}> 既定に落ちているものだけ</label> '
            f'<label><input type=checkbox name=allowed value=1{" checked" if f_al else ""}> allow になっているものだけ</label> '
            '<button type=submit>絞り込む</button> <a href="/perms">解除</a></p></form>')
    out = []
    for r in rows:
        c = r["entry"]
        dim = c["state"] in ("reserved", "unwired")
        cell = (lambda x: f'<span class=note>{x}</span>' if dim else x)
        if r["value"] == "unknown":
            val = '<b class=warn>判定不能</b>'
            via = f'{r["rule"]["no"]} 行目(<code>[</code> を含む)'
            how = "-"
        else:
            val = '<b class=warn>allow</b>' if r["value"] == "allow" else "ask"
            if r["rule"] is None:
                via, how = "なし(既定の ask)", "既定に落ちる"
            elif r["explicit"]:
                via, how = f'{r["rule"]["no"]} 行目 <code>{e(r["rule"]["pattern"])}</code>', "明示"
            else:
                via, how = f'{r["rule"]["no"]} 行目 <code>* = ask</code>(既定)', "既定に落ちる"
        warns = []
        if c["state"] == "unwired":
            warns.append("承認判定に渡らない操作です。approvals.conf の行は効きません(代行依頼は別の仕組み)")
        if r["risky"]:
            warns.append(f'リスク {e(c["risk"])} / allow 候補 {e(c["allow_ok"])} の操作が allow になっています')
        if r["risky_exc"]:
            warns.append("allow にしない操作に、宛先を絞った allow の例外があります")
        exc = " ".join(f'<code>{e(x["pattern"])} = {e(x["value"])}</code>' for x in r["exceptions"][:4])
        risk = f'<b class=warn>{e(c["risk"])}</b>' if c["risk"] in ("high", "human-only") else e(c["risk"])
        out.append([cell(f'<code>{e(c["pattern"])}</code>'), cell(e(c["kind"])), cell(e(c["desc"])), val, cell(how), cell(via),
                    cell(risk), cell(e(c["side"])), cell(e(c["allow_ok"])), cell(e(c["state"])),
                    (exc or "") + ("<br>" if exc and warns else "") + "".join(f'<span class=warn>{w}</span>' for w in warns)])
    note = (f"<p class=note>権限カタログ(<code>taskboard/config/approvals-catalog.conf</code>)の {len(catalog)} 件のうち {len(rows)} 件。"
            "現在の値は、パターンの <code>*</code> を例の名前に置いて approvals.conf を上から当てた結果です"
            "(宛先を絞った規則は「例外」の欄に出ます)。reserved(将来用で未実装)と unwired(操作名はあるが承認判定には渡らない)は薄く表示。"
            "カタログは操作の説明で、判定には使いません。")
    if cbad:
        note += f" 形式が不正でスキップした行が {cbad} 件あります。"
    note += "</p>"
    return ("<h2>操作ごとの現在の権限(カタログ × approvals.conf)</h2>" + form +
            table(["操作", "種別", "説明", "現在の値", "規則の有無", "決まる行", "リスク", "副作用", "allow 候補", "状態", "例外・警告"], out) + note)


def page_perms(qs, ctx=None):
    """権限(approvals.conf の読み取り)。変更操作は置かない。コマンド行を組み立てて表示するだけ。"""
    if ctx is not None:
        ctx["norefresh"] = True
    rules, err = perms.load_rules()
    op = _one(qs, "op")
    body = err_html(err) if err else ""
    # 「この操作はどの行で決まるか」
    form = ('<form method=get action="/perms"><p><label>操作名 '
            f'<input type=text name=op value="{e(op)}" size=36 placeholder="send:task:worker1"></label> '
            '<button type=submit>どの行で決まるか調べる</button></p></form>')
    res = ""
    hit = None
    if op:
        if not perms.ACTION_RE.fullmatch(op):
            res = err_html("操作名に使えない文字があります(英数字と . _ : * ? - だけ、128 文字まで)")
        elif err:
            res = err_html("approvals.conf を全部は読めていないので、ask とも allow とも判定しません")
        else:
            val, hit = perms.decide(rules, op)
            if val == "unknown":
                res = (f'<p><code>{e(op)}</code> → <b class=warn>判定不能</b>({hit["no"]} 行目 <code>{e(hit["pattern"])}</code> に <code>[</code> があり、'
                       'bash と照合規則が違いうるため、この画面ではここから下を判定しません。手元で <code>tb approval</code> を使ってください)</p>')
            elif hit:
                res = (f'<p><code>{e(op)}</code> → <b class="{"warn" if val == "allow" else ""}">{e(val)}</b>'
                       f'(<b>{hit["no"]} 行目</b>: <code>{e(hit["pattern"])} = {e(hit["value"])}</code> で決まる)</p>')
            else:
                res = f'<p><code>{e(op)}</code> → <b>ask</b>(どの行にも一致しないので既定の ask)</p>'
    body += "<h2>この操作はどの行で決まるか</h2>" + form + res + (
        '<p class=note>tb の <code>tb approval &lt;操作&gt;</code> と同じ規則(上から見て最初に一致した行)をここで再現した結果です。'
        '実際に tb を呼んだり、何かを書き換えたりはしません。リスク評価(<code>tb approval &lt;操作&gt; --to 宛先 --text 本文</code>)は'
        '本文が要るので、ここでは出しません。</p>')
    # 権限カタログ × approvals.conf(操作ごとの現在の値)
    catalog, cerr, cbad = perms.load_catalog()
    body += _catalog_section(qs, rules, err, catalog, cerr, cbad)
    # 一覧
    rows = []
    last_chg = {}
    for h in approval_history(200):
        last_chg.setdefault(h["pattern"], h)
    for r in rules:
        notes = []
        w = perms.warning(r)
        if w:
            notes.append(f'<span class=warn>{e(w)}</span>')
        if catalog and not err and perms.rule_inert(r, catalog):
            notes.append('<span class=warn>この規則は何も効きません(当たる操作はカタログで「承認判定に渡らない」とされています)</span>')
        sh = perms.shadowed_by(rules, r)
        if sh:
            notes.append(f'<span class=warn>上の {sh["no"]} 行目(<code>{e(sh["pattern"])}</code>)が先に一致するため、この行は使われません</span>')
        chg = e(r["note"]) if r["note"] else ""
        hrow = last_chg.get(r["raw"])
        if hrow:
            chg += (" / " if chg else "") + f'履歴: {e(hrow["created_at"])} に {e(hrow["value"])}'
        v = f'<b class=warn>{e(r["value"])}</b>' if r["value"] == "allow" else e(r["value"])
        mark = ' style="background:#fff8c5"' if hit is r else ""
        ex = " ".join(f"<code>{e(x)}</code>" for x in perms.examples(rules, r)[:8]) or '<span class=note>(例なし)</span>'
        rows.append([str(r["no"]), f'<code{mark}>{e(r["pattern"])}</code>', v, chg or '<span class=note>(注記なし)</span>',
                     ex, "<br>".join(notes)])
    body += "<h2>ルール一覧(上から順に評価)</h2>" + table(["順位", "パターン", "値", "最後の変更", "この行で決まる操作の例", "警告"], rows)
    body += (f'<p class=note>元ファイル: <code>{e(perms.conf_path_label())}</code>。「最後の変更」は行末の注記と、tb が残した変更履歴(approval_changes)です。'
             'どの行にも一致しなければ ask。ワイルドカードは <code>*</code> と <code>?</code>。</p>')
    if catalog and not err:
        unk = perms.unknown_rules(rules, catalog)
        body += "<h2>未知の規則(カタログに無い)</h2>" + (table(
            ["順位", "パターン", "値"], [[str(r["no"]), f'<code>{e(r["pattern"])}</code>', e(r["value"])] for r in unk]) if unk else
            "<p class=note>(なし)カタログに無い規則は見つかりませんでした。</p>")
        body += ("<p class=note>approvals.conf にあるが、権限カタログのどの操作とも重ならない規則です。"
                 "操作名の書き間違いか、カタログへの追記漏れの可能性があります。</p>")
    # 変更履歴
    hist = approval_history(50)
    body += "<h2>変更履歴(直近 50 件)</h2>" + table(
        ["時刻(UTC)", "パターン", "値"],
        [[e(h["created_at"]), f'<code>{e(h["pattern"])}</code>', e(h["value"])] for h in hist])
    body += '<p class=note>tb allow / tb ask が残した履歴です。誰が変えたか・変更前の値は記録されません(tb にその欄がありません)。</p>'
    # コマンドの組み立て
    pat, val = _one(qs, "pat"), _one(qs, "val")
    cmd_html = ""
    if pat or val:
        cmd = perms.tb_command(val, pat)
        if cmd is None:
            cmd_html = err_html("パターンは英数字と . _ : * ? - だけ(128 文字まで)、値は allow か ask です")
        else:
            warns = []
            probe = {"raw": pat, "value": val}
            w = perms.warning(probe)
            if val == "allow" and pat == "*":
                w = "全体を allow にします。末尾の <code>* = ask</code> は書き換えられてしまいます(tb に止める仕組みがありません)"
                warns.append(w)
            elif w:
                warns.append(e(w))
            if val == "ask" and pat == "*":
                warns.append("既定の <code>* = ask</code> と同じ値です")
            if any(r["raw"] == pat for r in rules):
                warns.append("同じパターンの行が既にあるので、その行の値が書き換わります")
            else:
                warns.append("新しい行は一覧の先頭(コメントの直後)に入り、既存の行より先に一致します")
            cmd_html = (f'<p>人が<b>自分の端末で</b>実行するコマンド行です(この画面では実行しません):</p><pre>{e(cmd)}</pre>'
                        + "".join(f"<p class=warn>{x}</p>" for x in warns))
    pat_form = ('<form method=get action="/perms"><p>'
                f'<label>パターン <input type=text name=pat value="{e(pat)}" size=30 placeholder="send:task:worker1"></label> '
                f'<label>値 <select name=val><option value=allow{" selected" if val == "allow" else ""}>allow(承認なしで進めてよい)</option>'
                f'<option value=ask{" selected" if val == "ask" else ""}>ask(人の承認が要る)</option></select></label> '
                '<button type=submit>コマンド行を組み立てる</button></p></form>')
    body += ("<h2>変更したいとき</h2>" + pat_form + cmd_html +
             "<p class=note>権限の変更は、この画面ではできません(ボタンも POST もありません)。人が端末で <code>tb allow</code> / <code>tb ask</code> を実行します。"
             "広い allow(例: <code>agent:*</code>)は、agent が自分の承認権限を広げる経路になりえます。</p>")
    return "権限(approvals.conf)", body


def age_label(sec):
    if sec is None:
        return "?"
    if sec < 60:
        return f"{sec} 秒"
    if sec < 3600:
        return f"{sec // 60} 分"
    if sec < 86400:
        return f"{sec // 3600} 時間"
    return f"{sec // 86400} 日"


def page_outbox(_qs):
    """tb send の一覧(読み取り専用)。承認・却下・送信の操作は置かない(承認は手元の tb approve)。"""
    rows = []
    for o in outbox_rows():
        st = f'<span class=warn>{e(o["status"])}</span>' if o["stuck"] else e(o["status"])
        age = f'<span class=warn>{e(age_label(o["age_sec"]))}</span>' if o["stuck"] else e(age_label(o["age_sec"]))
        rows.append([f"O-{o['id']}", st, e(o["from_name"]), e(o["target"]), e(o["kind"]), e(o["task_id"]), age, e(o["text"])])
    body = table(["id", "状態", "from", "宛先", "種別", "タスク", "経過", "本文(先頭 80 文字)"], rows)
    body += (f"<p class=note>新しい順に最大 100 件。queued のまま {QUEUED_WARN_SEC} 秒を超えたものは赤で強調"
             "(scheduler の flush が止まっていないか見てください)。承認は手元で tb approve。この画面からは操作できません。</p>")
    return "送信 (outbox)", body


def summary3(it):
    """通知 1 件の 3 行: 何が(対象へのリンク) / 中身 / どう対応するか。"""
    return (f'<div><a href="{e(it["href"])}">{e(it["title"])}</a></div>'
            f'<div>{e(it.get("body") or "-")}</div>'
            f'<div class=note>対応: {e(it.get("how") or "リンク先で確かめる")}</div>')


APPROVAL_KINDS = ("send_pending", "agent_op_pending", "alert_approval", "proxy_pending", "agent_blocked")


def page_notifications(_qs):
    items = urgent_items()
    body = ("<p>10 分おきのまとめ送信を待たずに知らせるものです。開いている間、"
            f"{NOTIFY_POLL_SEC} 秒ごとに確認し、新しく出たものを Chrome のデスクトップ通知で出します。</p>"
            "<p><span id=nstate class=note>Chrome 通知: JavaScript が無効か、読み込み中です</span> "
            "<button type=button id=nenable hidden>Chrome 通知を有効にする</button></p>")
    body += table(["種別", "何が・中身・どう対応するか", "時刻"],
                  [[f'<span class=warn>{e(it["label"])}</span>', summary3(it), e(it["ts"])] for it in items])
    body += ("<p class=note>対象: 未完了タスクの question/blocked 報告、pending-human の代行依頼、blocked の agent、"
             f"working のまま {STALL_MIN} 分超の agent、最後の実行が失敗した定期実行。"
             "通知は Web UI のタブが 1 つ開いている間だけ届きます(タブを閉じると止まる)。</p>")
    return "緊急通知", body


def doc_href(ref, sub=""):
    return "/docs/view?" + urlencode({"ref": ref, **({"sub": sub} if sub else {})})


def doc_link(ref, raw):
    """成果物の文字列 → Web UI で開くリンク(URL ならそのまま外部リンク)。"""
    raw = str(raw)
    if docs.is_url(raw):
        return f'<a href="{e(raw.strip())}" rel="noopener noreferrer" target=_blank>{e(raw)}</a>'
    return f'<a href="{e(doc_href(ref))}">{e(raw)}</a>'


def _doc_name(raw):
    s = str(raw or "").rstrip("/")
    return os.path.basename(s) or s


def page_docs(qs, ctx=None):
    if ctx is not None:
        ctx["norefresh"] = True  # 絞り込みの入力欄があるので自動更新しない
    items = docs.recent_docs()
    kw = clean((qs.get("q") or [""])[0]).strip()[:100]
    if kw:
        low = kw.lower()
        items = [it for it in items if low in " ".join(str(it.get(k) or "") for k in ("raw", "task", "from", "title")).lower()]
    body = ("<p>agent が報告(<code>tb comment --deliverables</code>・タスクの成果物・アラートの解析レポート)で"
            "「ここにまとめました」と示したファイルを、新しい順に並べています。名前を押すとこの画面で読めます"
            "(Markdown は整形して表示)。</p>"
            f'<form method=get action="/docs"><input type=search name=q value="{e(kw)}" placeholder="パス・タスク・agent で絞り込み" size=36> '
            "<button type=submit>絞り込み</button></form>"
            "<h2>パスを指定して開く</h2>" + _open_form() +
            "<p class=note>開ける範囲: " + (", ".join(f"<code>{e(r)}</code>" for r in docs.allowed_roots()) or "(なし)") +
            "(agent の作業ディレクトリと <code>WEBUI_DOC_ROOTS</code>。ホームや / そのものは対象外)</p><h2>報告されたドキュメント</h2>")
    rows = []
    for it in items:
        name = _doc_name(it["raw"]) or (f"A-{it['ref'][2:]} の解析レポート" if it["kind"] == "alert" else "-")
        if docs.is_url(it["raw"]):
            link = doc_link(it["ref"], it["raw"])
        else:
            link = f'<a href="{e(doc_href(it["ref"]))}">{e(name)}</a>'
        ctx_cell = tlink(it["task"]) if it["task"] else e(it.get("title") or "")
        rows.append([e(it["ts"]), link, f'<span class=note>{e(it["raw"])}</span>', ctx_cell, e(it["from"]),
                     e(it["kind"]), e(docs.exists_label(it))])
    body += table(["時刻", "ドキュメント", "パス", "タスク / アラート", "from", "種別", "状態"], rows)
    body += ("<p class=note>一覧に出るのは tasks.db に成果物として登録されたものです。相対パスは報告した agent の作業ディレクトリが基準です。"
             "鍵・.env などの認証情報らしいものは表示しません。</p>")
    return "ドキュメント", body


def page_doc(qs, ctx):
    ctx["norefresh"] = True  # 読んでいる途中で再読み込みしない
    ref = (qs.get("ref") or [""])[0]
    given = (qs.get("path") or [""])[0]
    sub = (qs.get("sub") or [""])[0]
    raw_mode = (qs.get("raw") or [""])[0] == "1"
    head = "<p><a href=\"/docs\">← ドキュメント一覧</a></p>"
    if given and not ref:  # パスを直接指定(agent の作業ディレクトリ配下だけ)
        p, why = docs.open_path(given)
        if p is None:
            return "ドキュメント", head + err_html(why) + _open_form(given)
        base_q = {"path": given}
        ent = {"raw": given, "path": p, "report": None}
        title = _doc_name(given)
        meta = [("指定したパス", e(given)), ("場所", e(p))]
    else:
        ent = docs.lookup(ref)
        if ent is None:
            return None
        base_q = {"ref": ref}
        title = _doc_name(ent["raw"]) or f"{ref} の解析レポート"
        meta = [("登録された値", e(ent["raw"]) or "-"), ("場所", e(ent["path"]) if ent["path"] else "-"),
                ("タスク", tlink(ent["task"]) if ent["task"] else e(ent.get("title") or "-")),
                ("from", e(ent["from"])), ("時刻", e(ent["ts"])), ("参照", e(ref))]
        if docs.is_url(ent["raw"]):
            return title, head + f'<p>URL です: {doc_link(ref, ent["raw"])}</p>'
    path = ent["path"]
    if path and sub:
        p2 = docs.sub_path(path, sub)
        if p2 is None or not os.path.isdir(path):
            return None
        path, title = p2, sub
        meta.insert(2, ("フォルダ内", e(sub)))
        base_q = {**base_q, "sub": sub}
    info = "<table>" + "".join(f"<tr><th>{k}</th><td>{v}</td></tr>" for k, v in meta) + "</table>"
    if path is None:
        if ent.get("report"):
            return title, head + info + "<h2>解析レポート(DB に保存された本文)</h2>" + _doc_body(ent["report"], ".md", raw_mode, base_q, ctx)
        return title, head + info + err_html("ファイルの場所を決められません(相対パスで、報告した agent の作業ディレクトリが分からない)")
    kind, text, trunc, size = docs.read_text(path)
    if kind == "dir":
        files, more = docs.list_dir(path)
        top = {k: v for k, v in base_q.items() if k != "sub"}
        rows = [[f'<a href="/docs/view?{e(urlencode({**top, "sub": os.path.relpath(os.path.join(path, f), ent["path"])}))}">{e(f)}</a>']
                for f in files]
        return title, head + info + "<h2>フォルダの中身</h2>" + table(["ファイル"], rows) + (
            f"<p class=note>先頭 {docs.MAX_LIST} 件だけ表示しています</p>" if more else "")
    if kind == "missing":
        if ent.get("report"):
            return title, head + info + "<p class=note>ファイルは見つかりませんでした。DB に保存された本文を表示します。</p>" + \
                _doc_body(ent["report"], ".md", raw_mode, base_q, ctx)
        return title, head + info + err_html("ファイルが見つかりません(移動・削除された可能性があります)")
    if kind == "binary":
        return title, head + info + err_html(f"テキストではないので表示しません({size} バイト)")
    if kind in ("denied", "error"):
        return title, head + info + err_html(text)
    note = f"<p class=note>大きいので先頭 {docs.MAX_BYTES // 1024} KB だけ表示しています(全体 {size} バイト)</p>" if trunc else ""
    ext = os.path.splitext(path)[1].lower()
    return title, head + info + note + _doc_body(text, ext, raw_mode, base_q, ctx)


def _open_form(value=""):
    return ('<form method=get action="/docs/view"><input type=text name=path value="' + e(value) + '" '
            'placeholder="/絶対パス/まとめ.md(agent の作業ディレクトリ配下)" size=60> <button type=submit>開く</button></form>')


def _doc_body(text, ext, raw_mode, base_q, ctx):
    if ext in docs.MD_EXT or ext in (".mmd", ".mermaid"):
        if ext in (".mmd", ".mermaid"):
            text = "```mermaid\n" + text + "\n```"
        if raw_mode:
            sw = f'<p class=note><a href="/docs/view?{e(urlencode(base_q))}">整形して表示</a></p>'
            return sw + f"<pre>{e(text)}</pre>"
        html = render_md(text)
        if "<pre class=mermaid>" in html:  # 図は同梱の mermaid.js でブラウザが描く(JS が無ければコードのまま)
            ctx["scripts"] = ["/static/mermaid.min.js", "/docs.js"]
        sw = f'<p class=note><a href="/docs/view?{e(urlencode({**base_q, "raw": "1"}))}">元のテキストを表示</a></p>'
        return sw + f"<div class=doc>{html}</div>"
    return f"<pre>{e(text)}</pre>"


STATIC = {"/": page_overview, "/notifications": page_notifications, "/inbox": page_inbox,
          "/outbox": page_outbox, "/jobs": page_jobs}


def page_sessions(_qs, ctx):
    """この Web UI が返す herdr セッションの一覧と、それぞれの緊急通知の数(ログインは全セッション共通なので、ここで数える)。"""
    me = reqctx.name()
    rows = []
    for n in sessions.known():
        try:
            with reqctx.use(n, n):
                cell = f"<td>{len(urgent_items())}</td><td>{badge_count()}</td>"
        except Exception as ex:   # 1 セッションが読めなくても一覧は出す
            cell = f"<td colspan=2>読めない({e(type(ex).__name__)})</td>"
        # ページは /s/<この画面>/sessions。隣のセッションは相対パスで(/… はこの画面の下に付け替えられるため)
        link = f"<strong>{e(n)}</strong>(この画面)" if n == me else f'<a href="{e("../" + n + "/")}">{e(n)}</a>'
        rows.append(f"<tr><td>{link}</td>{cell}</tr>")
    body = ("<p>この Web UI は全 herdr セッションの画面を返します(ログインは共通)。行き先を選んでください。"
            "一覧は DB に行のあるセッションと、herdr のセッションのディレクトリ(" + e(sessions.herdr_sessions_dir()) + ")から作ります。</p>"
            "<table><tr><th>セッション</th><th>緊急通知</th><th>報告・承認待ち</th></tr>" + "".join(rows) + "</table>")
    return "セッション一覧", body


def route(path, qs, ctx=None):
    """(title, body) か None(404)。ctx: {"csrf": フォーム用トークン}。フォームを出したページは ctx["norefresh"] を立てる。"""
    ctx = {} if ctx is None else ctx
    if path != "/" and path.endswith("/"):
        path = path.rstrip("/")
    if path == "/inbox":
        return page_inbox(qs, ctx)
    if path == "/sessions":
        return page_sessions(qs, ctx)
    if path == "/agents":
        return page_agents(qs, ctx)
    if path == "/agent-graph":
        return page_agent_graph(qs, ctx)
    if path == "/agent-new":
        ctx["norefresh"] = True
        return page_agent_new(ctx.get("csrf", ""))
    if path == "/ledger":
        return page_ledger(qs, ctx)
    if path == "/perms":
        return page_perms(qs, ctx)
    if path == "/docs":
        return page_docs(qs, ctx)
    if path == "/docs/view":
        return page_doc(qs, ctx)
    if path == "/tasks":
        return page_tasks(qs, ctx)
    if path == "/board":
        return page_board(qs, ctx)
    if path in STATIC:
        t, b = STATIC[path](qs)
        return t, b
    m = re.fullmatch(r"/(agents|tasks)/([^/]+)", path)
    if m:
        key = unquote(m.group(2))
        r = page_agent(key) if m.group(1) == "agents" else page_task(key, qs, ctx)
        return r
    return None
