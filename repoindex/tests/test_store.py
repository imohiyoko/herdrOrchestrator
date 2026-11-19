"""置き場所・権限・読み取り専用・同時実行・出力の上限・ツール口(#106)。"""
import json
import os
import sqlite3
import stat
import subprocess
import sys

from helpers import BIN, MODULE, Case, git

from ri import read, store


def mode(p):
    return stat.S_IMODE(os.stat(p).st_mode)


class StoreTest(Case):
    def setup_repo(self):
        self.write("app/main.py", "print(1)\n")
        head = self.commit()
        self.ri("repo-add", "demo", self.repo)
        return head

    def test_permissions(self):
        self.ri("init")
        self.assertEqual(mode(self.data), 0o700)
        self.assertEqual(mode(os.path.join(self.data, "index.db")), 0o600)
        for suffix in ("-wal", "-shm"):
            p = os.path.join(self.data, "index.db" + suffix)
            if os.path.exists(p):
                self.assertEqual(mode(p) & 0o077, 0, p)
        self.assertIn("permissions: ok", self.ri("status").stdout)

    def test_status_reports_loose_permissions(self):
        self.ri("init")
        os.chmod(self.data, 0o755)
        r = self.ri("status", ok=False)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("permissions: NG", r.stdout)

    def test_location_inside_checkout_refused(self):
        env = dict(self.env, REPOINDEX_DIR=os.path.join(os.path.dirname(MODULE), "data", "ri"))
        r = subprocess.run([sys.executable, BIN, "init"], capture_output=True, text=True, env=env)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("checkout", r.stderr)
        self.assertFalse(os.path.exists(env["REPOINDEX_DIR"]))

    def test_location_inside_git_worktree_refused(self):
        env = dict(self.env, REPOINDEX_DIR=os.path.join(self.repo, ".cache", "ri"))
        r = subprocess.run([sys.executable, BIN, "init"], capture_output=True, text=True, env=env)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("作業ツリー", r.stderr)

    def test_read_only_connection_cannot_write(self):
        self.setup_repo()
        con = store.connect_ro()
        with self.assertRaises(sqlite3.OperationalError):
            con.execute("INSERT INTO repos(name, path) VALUES ('x', '/x')")
        with self.assertRaises(sqlite3.OperationalError):
            con.execute("DELETE FROM summaries")
        con.close()

    def test_reader_without_db_creates_nothing(self):
        with self.assertRaises(store.StoreError):
            store.connect_ro()
        self.assertFalse(os.path.exists(self.data))

    def test_put_get_and_missing(self):
        head = self.setup_repo()
        self.ri("put", "--repo", "demo", "--kind", "file", "--path", "app/main.py", "--commit", head,
                "--boundaries", "external", input="Entry point; prints a number.")
        res = self.query("get", "--repo", "demo", "app/main.py")
        it = res["results"][0]
        self.assertEqual(it["freshness"]["commit"], head)
        self.assertFalse(it["freshness"]["stale"])
        self.assertEqual(it["source"], {"repo": "demo", "path": "app/main.py", "commit": head})
        self.assertEqual(it["boundaries"], ["external"])
        self.assertIn("notice", res)
        miss = self.query("get", "--repo", "demo", "app/none.py")
        self.assertEqual(miss["results"], [])
        self.assertEqual(miss["missing"][0]["path"], "app/none.py")

    def test_search_quotes_fts_syntax(self):
        head = self.setup_repo()
        self.ri("put", "--repo", "demo", "--kind", "dir", "--path", "app", "--commit", head, input="handles login tokens")
        self.assertEqual(len(self.query("search", "--repo", "demo", "login")["results"]), 1)
        # FTS5 の演算子を含む文字列でも構文エラーにならない
        self.query("search", "--repo", "demo", 'login" OR body:*')

    def test_output_limits(self):
        head = self.setup_repo()
        con = store.connect_rw()
        from ri import cli
        with con:
            for i in range(40):
                cli.put_summary(con, "demo", "file", f"f{i}.py", "word " * 1000, head)
        con.close()
        ro = store.connect_ro()
        res = read.search(ro, "word", limit=999)
        self.assertLessEqual(len(res["results"]), read.MAX_RESULTS)
        self.assertTrue(res["more"])
        self.assertTrue(all(len(r["summary"]) <= read.MAX_BODY for r in res["results"]))
        self.assertLessEqual(sum(len(r["summary"]) for r in res["results"]), read.MAX_TOTAL)
        self.assertLessEqual(len(read.stale(ro, limit=999)["results"]), read.MAX_RESULTS)
        ro.close()

    def test_reader_and_writer_at_the_same_time(self):
        head = self.setup_repo()
        self.ri("put", "--repo", "demo", "--kind", "dir", "--path", "app", "--commit", head, input="alpha")
        w = store.connect_rw()
        w.execute("BEGIN IMMEDIATE")
        w.execute("UPDATE summaries SET body = 'beta'")
        # 書き込み側がトランザクションを持ったままでも、読み取り側は前の版を読める(WAL)
        ro = store.connect_ro()
        self.assertEqual(read.get(ro, "demo", "app")["results"][0]["summary"], "alpha")
        w.commit()
        self.assertEqual(read.get(ro, "demo", "app")["results"][0]["summary"], "beta")
        ro.close()
        w.close()

    def test_repo_remove_deletes_all_rows(self):
        head = self.setup_repo()
        for p in ("a", "b"):
            self.ri("put", "--repo", "demo", "--kind", "dir", "--path", p, "--commit", head, input="x")
        self.ri("repo-remove", "demo")
        con = store.connect_rw()
        self.assertEqual(con.execute("SELECT count(*) FROM summaries").fetchone()[0], 0)
        self.assertEqual(con.execute("SELECT count(*) FROM summaries_fts").fetchone()[0], 0)
        con.close()

    def test_repo_add_requires_git_repo(self):
        os.makedirs(os.path.join(self.tmp, "plain"))
        r = self.ri("repo-add", "x", os.path.join(self.tmp, "plain"), ok=False)
        self.assertNotEqual(r.returncode, 0)

    def test_purge(self):
        self.ri("init")
        self.assertNotEqual(self.ri("purge", ok=False).returncode, 0)
        self.assertTrue(os.path.exists(self.data))
        self.ri("purge", "--yes")
        self.assertFalse(os.path.exists(self.data))


class ServerTest(Case):
    def rpc(self, *msgs):
        lines = "".join(json.dumps(m) + "\n" for m in msgs)
        r = subprocess.run([sys.executable, BIN, "serve"], input=lines, capture_output=True, text=True, env=self.env)
        return [json.loads(x) for x in r.stdout.splitlines()], r.stderr

    def test_tools_are_read_only(self):
        out, _ = self.rpc({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
                          {"jsonrpc": "2.0", "method": "notifications/initialized"},
                          {"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        self.assertEqual(len(out), 2)
        names = {t["name"] for t in out[1]["result"]["tools"]}
        self.assertEqual(names, {"repoindex_search", "repoindex_get", "repoindex_stale", "repoindex_freshness",
                                 "repoindex_context"})

    def test_call_and_log_has_no_body(self):
        self.write("x.py", "1\n")
        head = self.commit()
        self.ri("repo-add", "demo", self.repo)
        self.ri("put", "--repo", "demo", "--kind", "file", "--path", "x.py", "--commit", head, input="SECRETLESS-BODY-TEXT")
        out, err = self.rpc({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                             "params": {"name": "repoindex_get", "arguments": {"repo": "demo", "path": "x.py"}}},
                            {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                             "params": {"name": "repoindex_put", "arguments": {}}})
        body = json.loads(out[0]["result"]["content"][0]["text"])
        self.assertEqual(body["results"][0]["summary"], "SECRETLESS-BODY-TEXT")
        self.assertTrue(out[1]["result"]["isError"])
        self.assertNotIn("SECRETLESS-BODY-TEXT", err)

    def test_missing_db_is_an_error_not_a_crash(self):
        out, _ = self.rpc({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                           "params": {"name": "repoindex_freshness", "arguments": {}}})
        self.assertTrue(out[0]["result"]["isError"])
        self.assertFalse(os.path.exists(self.data))


class OptInTest(Case):
    def test_core_does_not_reference_the_module(self):
        root = os.path.dirname(MODULE)
        r = subprocess.run(["git", "-C", root, "grep", "-l", "-i", "repoindex", "--", ".",
                            ":!repoindex", ":!tests/run-all.sh", ":!README.md"], capture_output=True, text=True)
        self.assertEqual(r.stdout.strip(), "", "core から repoindex を呼んでいる")
