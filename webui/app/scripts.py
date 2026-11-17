"""画面に配るスクリプト(CSP の script-src 'self' のため、すべて別ファイルとして配る。インラインのスクリプトは無い)。

外部由来の値は埋め込まない(埋め込むのは BASE と間隔だけ。BASE は json.dumps で JS の文字列にする)。
"""

import json

DOCS_JS = """(function () {
  "use strict";
  // /docs/view の ```mermaid を図にする。securityLevel strict(ラベル内の HTML・クリックの JS は無効)
  if (!window.mermaid) return;
  try {
    window.mermaid.initialize({startOnLoad: false, securityLevel: "strict"});
    window.mermaid.run({querySelector: "pre.mermaid"}).catch(function () {});
  } catch (x) {}
})();
"""


# 画面の自動更新。<meta name=herdr-refresh content=秒> があるページだけ。
# 見ていないタブ(document.hidden)では再読み込みしない: 再読み込みのたびにサーバーが画面を作り直し herdr を呼ぶため。
# 見えるようになった時点で期限を過ぎていれば、すぐに読み直す。読み直しは GET(POST の結果画面を再送しない)
REFRESH_JS = """(function () {
  "use strict";
  var m = document.querySelector('meta[name="herdr-refresh"]');
  var sec = m ? parseInt(m.getAttribute("content"), 10) : 0;
  if (!(sec > 0)) return;
  var due = Date.now() + sec * 1000, timer = null;
  function arm() {
    clearTimeout(timer);
    if (document.hidden) return;
    timer = setTimeout(function () { if (!document.hidden) location.replace(location.href); }, Math.max(0, due - Date.now()));
  }
  document.addEventListener("visibilitychange", arm);
  arm();
})();
"""


_NOTIFY_TMPL = """(function () {
  "use strict";
  // BASE: /s/<セッション名>(同じオリジンに全セッションの画面が並ぶので、保存キーと通知のタグも分ける)
  var BASE = %s, POLL = %d, KEY = "herdr-webui-notified" + (BASE ? ":" + BASE : "");
  var badge = document.getElementById("nbadge");
  var state = document.getElementById("nstate");
  var btn = document.getElementById("nenable");
  function supported() { return "Notification" in window; }
  function load() {
    try { var v = JSON.parse(localStorage.getItem(KEY)); return v && typeof v === "object" ? v : null; }
    catch (x) { return null; }
  }
  function save(v) { try { localStorage.setItem(KEY, JSON.stringify(v)); } catch (x) {} }
  function showState() {
    if (!state) return;
    var t;
    if (!supported()) t = "このブラウザはデスクトップ通知に対応していません";
    else if (Notification.permission === "granted") t = "Chrome 通知: 有効";
    else if (Notification.permission === "denied") t = "Chrome 通知: ブロック中(アドレスバー左のサイト情報から通知を許可してください)";
    else t = "Chrome 通知: まだ許可していません";
    state.textContent = t;
    if (btn) btn.hidden = !supported() || Notification.permission !== "default";
  }
  function safeHref(h) { return typeof h === "string" && h.charAt(0) === "/" && h.charAt(1) !== "/" && h.charAt(1) !== "\\\\"; }
  function notify(it) {
    var n = new Notification("[herdr] " + it.label + ": " + it.title, {body: (it.body || "-") + "\\n対応: " + (it.how || "Web UI で確かめる"), tag: "herdr-" + (BASE ? BASE + "-" : "") + it.id});
    n.onclick = function () { window.focus(); if (safeHref(it.href)) location.href = BASE + it.href; n.close(); };
  }
  if (btn) btn.addEventListener("click", function () {
    Notification.requestPermission().then(function (p) {
      showState();
      if (p === "granted") new Notification("[herdr] 通知を有効にしました", {body: "新しい緊急通知をここに出します", tag: "herdr-enabled"});
    });
  });
  function poll() {
    fetch(BASE + "/api/notifications", {credentials: "same-origin", cache: "no-store", headers: {"Accept": "application/json"}})
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .then(function (d) {
        var items = Array.isArray(d.items) ? d.items : [];
        if (badge) { badge.textContent = String(items.length); badge.className = items.length ? "badge" : "badge zero"; }
        var seen = load(), first = seen === null, now = {};
        var ok = supported() && Notification.permission === "granted";
        items.forEach(function (it) {
          now[it.id] = 1;
          if (!first && !seen[it.id] && ok) notify(it);
        });
        save(now);
      })
      .catch(function () {});
  }
  showState();
  poll();
  setInterval(poll, POLL);
})();
"""


def notify_js(base, poll_ms):
    """緊急通知のポーリングと Chrome 通知。base はこの画面の置き場所(/s/<セッション名>)。"""
    return _NOTIFY_TMPL % (json.dumps(base), int(poll_ms))
