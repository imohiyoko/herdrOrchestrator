"""git の差分での更新と鮮度(#107)。要約のコマンドは偽物(呼ばれた記録をファイルに残す)。"""
import json
import os
import sys

from helpers import Case, git

from ri import store

FAKE_SUMMARIZER = r'''
import os, sys
text = sys.stdin.read()
log = os.environ["FAKE_LOG"]
with open(log, "a") as f:
    f.write(f"{os.environ['REPOINDEX_KIND']} {os.environ['REPOINDEX_PATH']} {os.environ['REPOINDEX_ATTEMPT']}\n")
with open(log + ".inputs", "a") as f:
    f.write(text + "\n=====\n")
bad = int(os.environ.get("FAKE_BAD_ATTEMPTS", "0"))
if int(os.environ["REPOINDEX_ATTEMPT"]) <= bad:
    print("Uses key " + "AK" + "IA" + "IOSFODNN7" + "EXAMPLE")
else:
    if "login" in text:
        print("boundaries: auth")
    print(f"Summary of {os.environ['REPOINDEX_KIND']} {os.environ['REPOINDEX_PATH']}.")
'''


class UpdateTest(Case):
    def setUp(self):
        super().setUp()
        script = os.path.join(self.tmp, "summ.py")
        with open(script, "w") as f:
            f.write(FAKE_SUMMARIZER)
        self.log = os.path.join(self.tmp, "calls.log")
        self.env.update(REPOINDEX_SUMMARIZER=f"{sys.executable} {script}", FAKE_LOG=self.log, REPOINDEX_MODEL="fake-1")
        os.environ.update(self.env)
        self.write("app/main.py", "def main():\n    login()\n")
        self.write("app/util.py", "X = 1\n")
        self.write("lib/a.py", "A = 1\n")
        self.write("tests/fixtures/users.json", '{"name": "Alice"}\n')
        self.write(".env", "SECRET=1\n")
        self.c1 = self.commit()

    def calls(self):
        if not os.path.exists(self.log):
            return []
        with open(self.log) as f:
            lines = f.read().splitlines()
        os.remove(self.log)
        return lines

    def inputs(self):
        p = self.log + ".inputs"
        if not os.path.exists(p):
            return ""
        with open(p) as f:
            return f.read()

    def rows(self):
        con = store.connect_rw()
        r = {(k, p, s): (st, c) for k, p, s, st, c in
             con.execute("SELECT kind, path, symbol, state, commit_id FROM summaries")}
        con.close()
        return r

    def head(self):
        con = store.connect_rw()
        h = con.execute("SELECT head_commit FROM repos").fetchone()[0]
        con.close()
        return h

    def test_initial_dir_level(self):
        self.ri("repo-add", "demo", self.repo)
        self.ri("update", "demo")
        # ルートには除外したものしか無いので、ルートの要約は作らない
        self.assertEqual(sorted(self.calls()), ["dir app 1", "dir lib 1"])
        self.assertEqual(self.head(), self.c1)
        self.assertEqual(self.query("get", "--repo", "demo", "app")["results"][0]["boundaries"], ["auth"])

    def test_excluded_paths_never_reach_the_summarizer(self):
        self.ri("repo-add", "demo", self.repo, "--granularity", "file")
        self.ri("update", "demo")
        calls = self.calls()
        self.assertFalse(any("fixtures" in c or ".env" in c for c in calls), calls)
        inp = self.inputs()
        self.assertNotIn("Alice", inp)
        self.assertNotIn("SECRET=1", inp)
        self.assertNotIn(".env", inp)
        self.assertNotIn("users.json", inp)

    def test_only_changed_files_and_no_work_without_changes(self):
        self.ri("repo-add", "demo", self.repo, "--granularity", "file")
        self.ri("update", "demo")
        self.calls()
        r = self.ri("update", "demo")
        self.assertIn("変更なし", r.stdout)
        self.assertEqual(self.calls(), [])
        self.assertNotEqual(self.ri("update", "demo", "--check", ok=False).returncode, 0)
        self.write("app/util.py", "X = 2\n")
        c2 = self.commit()
        self.assertEqual(self.ri("update", "demo", "--check").returncode, 0)
        self.ri("update", "demo")
        self.assertEqual(sorted(self.calls()), ["dir app 1", "file app/util.py 1"])
        rows = self.rows()
        self.assertEqual(rows[("file", "app/util.py", "")], ("fresh", c2))
        self.assertEqual(rows[("file", "app/main.py", "")], ("fresh", self.c1))  # 変わっていないものはそのまま
        self.assertEqual(self.head(), c2)
        # 変わっていないファイルの要約は、古いコミットで作ったものでも stale ではない
        it = self.query("get", "--repo", "demo", "app/main.py")["results"][0]
        self.assertFalse(it["freshness"]["stale"], it)

    def test_deleted_and_renamed_become_missing(self):
        self.ri("repo-add", "demo", self.repo, "--granularity", "file")
        self.ri("update", "demo")
        git(self.repo, "mv", "app/util.py", "app/helpers.py")
        git(self.repo, "rm", "-q", "-r", "lib")
        self.commit()
        self.ri("update", "demo")
        rows = self.rows()
        self.assertEqual(rows[("file", "app/util.py", "")][0], "missing")
        self.assertEqual(rows[("dir", "lib", "")][0], "missing")
        self.assertEqual(rows[("file", "app/helpers.py", "")][0], "fresh")
        it = self.query("get", "--repo", "demo", "lib")["results"][0]
        self.assertTrue(it["freshness"]["stale"])
        self.assertIn("missing", it["freshness"]["reasons"])

    def test_stale_marker_before_update(self):
        self.ri("repo-add", "demo", self.repo, "--granularity", "file")
        self.ri("update", "demo")
        self.write("app/main.py", "def main():\n    pass\n")
        self.commit()
        # 更新の前でも、今のソースと違えば stale の印が付く
        it = self.query("get", "--repo", "demo", "app/main.py")["results"][0]
        self.assertTrue(it["freshness"]["stale"])
        self.assertIn("file_changed", it["freshness"]["reasons"])
        d = self.query("get", "--repo", "demo", "app")["results"][0]
        self.assertIn("changed_since_summary", d["freshness"]["reasons"])
        fr = self.query("freshness", "--repo", "demo")["repos"][0]
        self.assertTrue(fr["index_behind"])

    def test_without_summarizer_nothing_is_sent(self):
        env = dict(self.env)
        env.pop("REPOINDEX_SUMMARIZER")
        self.env = env
        self.ri("repo-add", "demo", self.repo)
        r = self.ri("update", "demo")
        self.assertIn("未設定", r.stdout)
        self.assertEqual(self.calls(), [])
        self.assertIsNone(self.head())

    def test_regenerate_then_succeed(self):
        self.env["FAKE_BAD_ATTEMPTS"] = "2"
        self.ri("repo-add", "demo", self.repo)
        r = self.ri("update", "demo")
        self.assertIn("作り直し 2 件", r.stdout)
        calls = self.calls()
        self.assertIn("dir app 3", calls)
        self.assertNotIn("dir app 4", calls)
        self.assertEqual(self.rows()[("dir", "app", "")][0], "fresh")
        # 作り直しの入力に、落ちた本文は含まれない
        self.assertNotIn("IOSFODNN7", self.inputs())

    def test_regenerate_limit_then_reject_and_tell_human(self):
        self.env.update(FAKE_BAD_ATTEMPTS="99", REPOINDEX_MAX_ATTEMPTS="2")
        self.ri("repo-add", "demo", self.repo)
        r = self.ri("update", "demo")
        self.assertIn("2 回とも保存できなかった(aws_access_key", r.stderr)
        self.assertNotIn("IOSFODNN7", r.stdout + r.stderr)
        self.assertEqual(self.rows(), {})
        con = store.connect_rw()
        self.assertEqual(con.execute("SELECT count(*) FROM rejections WHERE final = 1 AND attempts = 2").fetchone()[0], 2)
        con.close()
        # 同じコミットでは、もう作り直さない
        self.calls()
        self.ri("update", "demo")
        self.assertEqual(self.calls(), [])

    def test_remove_repo_removes_all_rows(self):
        self.ri("repo-add", "demo", self.repo, "--granularity", "file")
        self.ri("update", "demo")
        self.ri("repo-remove", "demo")
        self.assertEqual(self.rows(), {})

    def test_update_all(self):
        self.ri("repo-add", "demo", self.repo)
        self.ri("update", "--all")
        self.assertEqual(self.head(), self.c1)
        out = json.loads(self.ri("query", "stale").stdout)
        self.assertEqual(out["results"], [])
