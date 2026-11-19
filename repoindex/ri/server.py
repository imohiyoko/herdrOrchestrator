"""読み取り専用のツール口(MCP の stdio サーバー。JSON-RPC 2.0 を 1 行 1 メッセージで読み書きする)。

agent ごとに 1 プロセスを stdio で起動する形。DB は読み取り専用で開くので、ここからは書けない。
書き込み(登録・更新・削除)の道具はここには無い。ツール名は他の文書検索ツールとぶつからないよう repoindex_ で始める。
ログ(stderr)には要約の本文を出さない。出すのはツール名・件数・理由の符号だけ。
"""
import json
import sys

from . import read, store

PROTOCOL = "2024-11-05"
TOOLS = [
    {"name": "repoindex_search",
     "description": "リポジトリの要約を全文検索する。結果は手がかりで、鮮度(commit)と元ソースの場所を必ず確かめる。",
     "inputSchema": {"type": "object", "properties": {
         "query": {"type": "string"}, "repo": {"type": "string"},
         "limit": {"type": "integer", "minimum": 1, "maximum": read.MAX_RESULTS}},
         "required": ["query"]}},
    {"name": "repoindex_get",
     "description": "ディレクトリ・ファイル・シンボルの要約を取る。無ければ missing を返す。",
     "inputSchema": {"type": "object", "properties": {
         "repo": {"type": "string"}, "path": {"type": "string"}, "symbol": {"type": "string"},
         "kind": {"type": "string", "enum": ["dir", "file", "symbol"]}},
         "required": ["repo", "path"]}},
    {"name": "repoindex_stale",
     "description": "鮮度が落ちた要約の一覧(本文なし)。",
     "inputSchema": {"type": "object", "properties": {
         "repo": {"type": "string"}, "limit": {"type": "integer", "minimum": 1, "maximum": read.MAX_RESULTS}}}},
    {"name": "repoindex_context",
     "description": "変更のまわり(変わったファイルとディレクトリの要約、呼び出し元・呼び先、関わる境界、影響の範囲)。"
                    "paths(変わったファイル)か range(base..head)を渡す。どの主張にも元ソースの場所と commit が付く。",
     "inputSchema": {"type": "object", "properties": {
         "repo": {"type": "string"}, "paths": {"type": "array", "items": {"type": "string"}},
         "range": {"type": "string"}}, "required": ["repo"]}},
    {"name": "repoindex_freshness",
     "description": "リポジトリごとの索引の鮮度(索引を進めたコミットと件数)。",
     "inputSchema": {"type": "object", "properties": {"repo": {"type": "string"}}}},
]


def _call(name, args):
    con = store.connect_ro()
    try:
        if name == "repoindex_search":
            return read.search(con, args.get("query"), args.get("repo"), args.get("limit"))
        if name == "repoindex_get":
            return read.get(con, args.get("repo"), args.get("path"), args.get("symbol"), args.get("kind"))
        if name == "repoindex_stale":
            return read.stale(con, args.get("repo"), args.get("limit"))
        if name == "repoindex_context":
            paths = args.get("paths")
            if paths is not None and not (isinstance(paths, list) and all(isinstance(x, str) for x in paths)):
                raise store.StoreError("paths は文字列の配列")
            return read.context(con, args.get("repo"), paths, args.get("range"))
        if name == "repoindex_freshness":
            return read.freshness(con, args.get("repo"))
        raise store.StoreError(f"不明なツール: {name}")
    finally:
        con.close()


def _log(msg):
    print(f"repoindex: {msg}", file=sys.stderr, flush=True)


def handle(msg):
    """1 メッセージを処理して返事(通知なら None)。"""
    mid, method = msg.get("id"), msg.get("method")
    if mid is None:
        return None  # 通知(notifications/initialized など)には返さない

    def ok(result):
        return {"jsonrpc": "2.0", "id": mid, "result": result}

    if method == "initialize":
        return ok({"protocolVersion": PROTOCOL, "capabilities": {"tools": {}},
                   "serverInfo": {"name": "repoindex", "version": "1"}})
    if method == "ping":
        return ok({})
    if method == "tools/list":
        return ok({"tools": TOOLS})
    if method == "tools/call":
        p = msg.get("params") or {}
        name, args = p.get("name"), p.get("arguments") or {}
        try:
            res = _call(name, args)
        except store.StoreError as e:
            _log(f"{name}: error")
            return ok({"content": [{"type": "text", "text": str(e)}], "isError": True})
        except Exception as e:  # 書き込みの試み(読み取り専用の接続)などもここ
            _log(f"{name}: error {type(e).__name__}")
            return ok({"content": [{"type": "text", "text": f"失敗しました({type(e).__name__})"}], "isError": True})
        _log(f"{name}: {len(res.get('results', res.get('repos', res.get('files', []))))} 件")
        return ok({"content": [{"type": "text", "text": json.dumps(res, ensure_ascii=False)}]})
    return {"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": f"method not found: {method}"}}


def main(stdin=sys.stdin, stdout=sys.stdout):
    for line in stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except ValueError:
            out = {"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}}
        else:
            out = handle(msg) if isinstance(msg, dict) else None
        if out is not None:
            stdout.write(json.dumps(out, ensure_ascii=False) + "\n")
            stdout.flush()
    return 0
