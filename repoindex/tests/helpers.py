"""テストの共通部品。データは一時ディレクトリ(REPOINDEX_DIR)、リポジトリは一時の git リポジトリ。"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
MODULE = os.path.dirname(HERE)
BIN = os.path.join(MODULE, "bin", "repoindex")
sys.path.insert(0, MODULE)


def git(cwd, *args):
    return subprocess.run(["git", "-C", cwd, *args], check=True, capture_output=True, text=True).stdout.strip()


class Case(unittest.TestCase):
    STAGE = 4

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="repoindex-test-")
        self.data = os.path.join(self.tmp, "state", "herdr-repoindex")
        self.sched = os.path.join(self.tmp, "scheduler")  # 本物の scheduler / launchd には触らない
        self.env = dict(os.environ, REPOINDEX_DIR=self.data, REPOINDEX_SCHEDULER_DIR=self.sched,
                        GIT_CONFIG_GLOBAL="/dev/null",
                        GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.invalid",
                        GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@example.invalid")
        self._old = os.environ.copy()
        os.environ.clear()
        os.environ.update(self.env)
        self.repo = os.path.join(self.tmp, "src")
        os.makedirs(self.repo)
        git(self.repo, "init", "-q", "-b", "main")
        if self.STAGE is not None:  # 段階のゲート(#110)を越えておく。段階を試すテストは STAGE = None
            self.ri("stage", "set", str(self.STAGE), "--by", "test", "--measures", '{"test": 1}')

    def tearDown(self):
        os.environ.clear()
        os.environ.update(self._old)
        shutil.rmtree(self.tmp, ignore_errors=True)

    def write(self, rel, text):
        p = os.path.join(self.repo, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w", encoding="utf-8") as f:
            f.write(text)

    def commit(self, msg="c"):
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "--allow-empty", "-m", msg)
        return git(self.repo, "rev-parse", "HEAD")

    def ri(self, *args, input=None, ok=True):
        r = subprocess.run([sys.executable, BIN, *args], input=input, capture_output=True, text=True, env=self.env)
        if ok and r.returncode != 0:
            self.fail(f"repoindex {' '.join(args)} -> {r.returncode}\n{r.stdout}\n{r.stderr}")
        return r

    def query(self, *args):
        return json.loads(self.ri("query", *args).stdout)
