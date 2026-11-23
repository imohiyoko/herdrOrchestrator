"""画面に配るスクリプト(CSP の script-src 'self' のため、すべて別ファイルとして配る。インラインのスクリプトは無い)。

中身は webui/static/ のファイル(Go 版の Web UI と同じものを配る)。
外部由来の値は埋め込まない(埋め込むのは BASE と間隔だけ。BASE は json.dumps で JS の文字列にする)。
"""

import json
import os

from .config import ROOT


def _read(name):
    with open(os.path.join(ROOT, "static", name), encoding="utf-8") as f:
        return f.read()


DOCS_JS = _read("docs.js")   # /docs/view の ```mermaid を図にする
FILTER_JS = _read("filter.js")   # タスク一覧の絞り込み: 選んだらすぐ反映する
REFRESH_JS = _read("refresh.js")   # 画面の自動更新(見ていないタブでは止める)
_NOTIFY_TMPL = _read("notify.js.tmpl")   # 緊急通知のポーリングと Chrome 通知。__BASE__ と __POLL__ を埋める


def notify_js(base, poll_ms):
    """緊急通知のポーリングと Chrome 通知。base はこの画面の置き場所(/s/<セッション名>)。"""
    return _NOTIFY_TMPL.replace("__BASE__", json.dumps(base), 1).replace("__POLL__", str(int(poll_ms)), 1)
