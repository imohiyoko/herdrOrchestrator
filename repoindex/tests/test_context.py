"""変更のまわりを引く(#109)。要約のコマンドは偽物。"""
import json
import os
import subprocess
import sys

from helpers import BIN, Case

from ri import read, store

FAKE = r'''
import os, sys
text = sys.stdin.read()
p = os.environ["REPOINDEX_PATH"]
if p == "app/handler.py":
    print("calls: app/session.py, tests/fixtures/x.json, no/such.py")
if "session" in p:
    print("boundaries: auth")
if p == "app/session.py":
    print("calls: app/db.py")
if p == "app/db.py":
    print("boundaries: data")
print("Summary of " + p)
'''


class ContextTest(Case):
    def setUp(self):
        super().setUp()
        script = os.path.join(self.tmp, "summ.py")
        with open(script, "w") as f:
            f.write(FAKE)
        self.env.update(REPOINDEX_SUMMARIZER=f"{sys.executable} {script}")
        os.environ.update(self.env)
        self.write("app/session.py", "def verify(): pass\n")
        self.write("app/db.py", "def query(): pass\n")
        self.write("app/handler.py", "from app import session\nsession.verify()\n")
        self.write("web/routes.py", "import session\n")
        self.write("tests/fixtures/x.json", "session\n")
        self.c1 = self.commit()
        self.ri("repo-add", "demo", self.repo, "--granularity", "file")
        self.ri("update", "demo")

    def ctx(self, *args):
        return self.query("context", "--repo", "demo", *args)

    def test_context_of_a_change(self):
        res = self.ctx("app/session.py")
        ent = res["files"][0]
        self.assertEqual(ent["file"]["summary"], "Summary of app/session.py")
        self.assertEqual(ent["dir"]["path"], "app")
        callers = {c["source"]["path"]: c for c in ent["callers"]}
        # 索引の関係(handler)と、普通のコード検索(routes)。除外パス(fixtures)は出さない
        self.assertEqual(callers["app/handler.py"]["found_by"], "index")
        self.assertEqual(callers["web/routes.py"]["found_by"], "code_search")
        self.assertEqual(callers["web/routes.py"]["source"]["line"], 1)
        self.assertNotIn("tests/fixtures/x.json", callers)
        self.assertEqual(callers["web/routes.py"]["summary"]["path"], "web/routes.py")
        self.assertEqual([c["source"]["path"] for c in ent["callees"]], ["app/db.py"])
        self.assertEqual(res["boundaries"], ["auth", "data"])
        radius = {r["path"]: r["why"] for r in res["blast_radius"]}
        self.assertEqual(radius["app/session.py"], ["changed"])
        self.assertIn("caller of app/session.py", radius["app/handler.py"])
        self.assertIn("callee of app/session.py", radius["app/db.py"])
        for r in res["blast_radius"]:
            self.assertEqual(r["source"]["commit"], self.c1)
        self.assertIn("notice", res)

    def test_relations_only_to_known_files(self):
        res = self.ctx("app/handler.py")
        self.assertEqual([c["source"]["path"] for c in res["files"][0]["callees"]], ["app/session.py"])

    def test_missing_is_explicit(self):
        self.write("app/new.py", "x = 1\n")
        self.commit()
        res = self.ctx("app/new.py")
        self.assertIsNone(res["files"][0]["file"])
        self.assertIn({"repo": "demo", "path": "app/new.py", "kind": "file", "hint": "要約がない。元のソースを読む"},
                      res["missing"])
        # ディレクトリの要約は古い。stale の印が付く
        self.assertTrue(res["files"][0]["dir"]["freshness"]["stale"])

    def test_range(self):
        self.write("app/db.py", "def query(): return 1\n")
        self.commit()
        res = self.ctx("--range", "HEAD~1..HEAD")
        self.assertEqual([f["path"] for f in res["files"]], ["app/db.py"])
        self.assertIn("file_changed", res["files"][0]["file"]["freshness"]["reasons"])
        con = store.connect_ro()
        for bad in ("--output=/tmp/x", "a b", "HEAD;rm"):
            with self.assertRaises(store.StoreError):
                read.context(con, "demo", rev_range=bad)
        con.close()

    def test_limits(self):
        res = self.ctx(*[f"f{i}.py" for i in range(read.MAX_CONTEXT_FILES + 3)])
        self.assertEqual(len(res["files"]), read.MAX_CONTEXT_FILES)
        self.assertTrue(res["more"])
        total = 0
        for f in res["files"]:
            for it in (f["file"], f["dir"]):
                if it:
                    total += len(it["summary"])
        self.assertLessEqual(total, read.MAX_TOTAL)

    def test_tool_call(self):
        msg = {"jsonrpc": "2.0", "id": 1, "method": "tools/call",
               "params": {"name": "repoindex_context", "arguments": {"repo": "demo", "paths": ["app/db.py"]}}}
        r = subprocess.run([sys.executable, BIN, "serve"], input=json.dumps(msg) + "\n", capture_output=True, text=True,
                           env=self.env)
        out = json.loads(r.stdout)
        body = json.loads(out["result"]["content"][0]["text"])
        self.assertEqual(body["files"][0]["path"], "app/db.py")
        self.assertNotIn("Summary of", r.stderr)
        bad = dict(msg, params={"name": "repoindex_context", "arguments": {"repo": "demo", "paths": "app/db.py"}})
        r = subprocess.run([sys.executable, BIN, "serve"], input=json.dumps(bad) + "\n", capture_output=True, text=True,
                           env=self.env)
        self.assertTrue(json.loads(r.stdout)["result"]["isError"])

    def test_deleted_file_drops_relations(self):
        os.remove(os.path.join(self.repo, "app/db.py"))
        self.commit()
        self.ri("update", "demo")
        res = self.ctx("app/session.py")
        self.assertEqual(res["files"][0]["callees"], [])
