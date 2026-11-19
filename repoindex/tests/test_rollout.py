"""段階的な導入と、承認済みテンプレートだけからの定期更新の登録(#110 / #107)。scheduler は偽物。"""
import json
import os
import shutil
import sys

from helpers import MODULE, Case

from ri import rollout, store

FAKE_TOOL = """#!/bin/bash
echo "$(basename "$0") $*" >> "$(dirname "$0")/../calls.log"
exit ${FAKE_RC:-0}
"""


class StageTest(Case):
    STAGE = None

    def setUp(self):
        super().setUp()
        os.makedirs(os.path.join(self.sched, "bin"))
        for t in ("gen-plist.sh", "install.sh"):
            p = os.path.join(self.sched, "bin", t)
            with open(p, "w") as f:
                f.write(FAKE_TOOL)
            os.chmod(p, 0o755)
        self.write("app/a.py", "x = 1\n")
        self.commit()

    def stage(self, n, measures='{"baseline_minutes": 12}', ok=True):
        return self.ri("stage", "set", str(n), "--by", "imohiyoko", "--measures", measures, ok=ok)

    def sched_calls(self):
        p = os.path.join(self.sched, "calls.log")
        return open(p).read().splitlines() if os.path.exists(p) else []

    def test_stage_0_blocks_everything(self):
        r = self.ri("repo-add", "demo", self.repo, ok=False)
        self.assertIn("段階 1 から", r.stderr)

    def test_stage_change_needs_person_and_measures(self):
        self.assertNotEqual(self.stage(1, measures="{}", ok=False).returncode, 0)
        self.assertNotEqual(self.stage(1, measures="not json", ok=False).returncode, 0)
        self.assertNotEqual(self.ri("stage", "set", "1", "--measures", '{"a":1}', ok=False).returncode, 0)
        self.stage(1)
        hist = json.loads(self.ri("stage", "show").stdout)
        self.assertEqual(hist["stage"], 1)
        self.assertEqual(hist["history"][0]["decided_by"], "imohiyoko")
        self.assertEqual(hist["history"][0]["measures"], {"baseline_minutes": 12})

    def test_stage_1_limits(self):
        self.stage(1)
        self.assertIn("ディレクトリ単位だけ",
                      self.ri("repo-add", "demo", self.repo, "--granularity", "file", ok=False).stderr)
        self.ri("repo-add", "demo", self.repo)
        self.assertIn("1 つまで", self.ri("repo-add", "other", self.repo, ok=False).stderr)
        # 読み取りの口は段階 2 から
        r = self.ri("serve", input=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                                                "params": {"name": "repoindex_freshness", "arguments": {}}}) + "\n")
        out = json.loads(r.stdout)["result"]
        self.assertTrue(out["isError"])
        self.assertIn("段階 2 から", out["content"][0]["text"])
        self.stage(2)
        r = self.ri("serve", input=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                                                "params": {"name": "repoindex_freshness", "arguments": {}}}) + "\n")
        self.assertNotIn("isError", json.loads(r.stdout)["result"])

    def test_measures_from_runs(self):
        self.stage(1)
        script = os.path.join(self.tmp, "s.py")
        with open(script, "w") as f:
            f.write("import sys; sys.stdin.read(); print('A summary.')\n")
        os.makedirs(self.data, exist_ok=True)
        with open(os.path.join(self.data, "settings.conf"), "w") as f:  # scheduler から動くときの設定
            f.write(f"REPOINDEX_SUMMARIZER={sys.executable} {script}\nPATH=/evil\n")
        self.ri("repo-add", "demo", self.repo)
        self.ri("update", "demo")
        m = json.loads(self.ri("measures").stdout)
        self.assertEqual((m["stage"], m["runs"], m["summarized"], m["rejected_attempts"], m["hit_attempt_limit"]),
                         (1, 1, 1, 0, 0))
        self.assertIn("stale_relied_on", m["human_measured"])

    def test_job_register_only_from_stage_3_and_approved_template(self):
        self.stage(2)
        self.assertIn("段階 3 から", self.ri("job", "register", ok=False).stderr)
        self.assertFalse(rollout.job_registered())
        self.stage(3)
        self.ri("job", "register")
        conf = os.path.join(self.sched, "jobs", "repoindex-update.conf")
        text = open(conf).read()
        self.assertIn(os.path.join(MODULE, "bin", "repoindex") + " update --all", text)
        self.assertIn("--check", text)
        self.assertNotIn("__COMMAND__", text)
        self.assertEqual(self.sched_calls(), ["gen-plist.sh repoindex-update", "install.sh install repoindex-update"])
        st = json.loads(self.ri("job", "status").stdout)
        self.assertTrue(st["registered"] and st["matches_template"] and st["template_approved"])
        self.assertEqual(st["log"][0]["action"], "register")
        # 登録がテンプレートと違う(書き換えられた)なら上書きしない
        with open(conf, "a") as f:
            f.write("COMMAND='curl evil'\n")
        self.assertIn("上書きしない", self.ri("job", "register", ok=False).stderr)
        self.assertFalse(json.loads(self.ri("job", "status").stdout)["matches_template"])
        # 1 コマンドで外す
        self.ri("job", "remove")
        self.assertFalse(os.path.exists(conf))
        self.assertIn("install.sh uninstall repoindex-update", self.sched_calls())

    def test_tampered_template_is_refused(self):
        self.stage(3)
        jobs = os.path.join(self.tmp, "jobs")
        shutil.copytree(rollout.JOBS_DIR, jobs)
        with open(os.path.join(jobs, rollout.TEMPLATE), "a") as f:
            f.write("INTERVAL_MIN=1\n")
        old = rollout.JOBS_DIR
        rollout.JOBS_DIR = jobs
        try:
            con = store.connect_rw()
            with self.assertRaises(store.StoreError):
                rollout.job_register(con)
            self.assertEqual(con.execute("SELECT action FROM job_log").fetchone()[0], "refused")
            con.close()
        finally:
            rollout.JOBS_DIR = old
        self.assertFalse(rollout.job_registered())

    def test_reverting_below_3_removes_the_job(self):
        self.stage(3)
        self.ri("job", "register")
        r = self.stage(2)
        self.assertIn("定期更新の登録を外した", r.stdout)
        self.assertFalse(rollout.job_registered())

    def test_purge_removes_the_job(self):
        self.stage(3)
        self.ri("job", "register")
        self.ri("purge", "--yes")
        self.assertFalse(rollout.job_registered())

    def test_installer_missing_still_writes_conf(self):
        self.stage(3)
        os.environ["FAKE_RC"] = "1"
        self.env["FAKE_RC"] = "1"
        r = self.ri("job", "register")
        self.assertIn("できなかった", r.stdout)
        con = store.connect_rw()
        self.assertIn("conf only", con.execute("SELECT detail FROM job_log ORDER BY id DESC").fetchone()[0])
        con.close()
