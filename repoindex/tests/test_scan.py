"""取り込みの安全(#108)。偽の秘密はソースに丸ごと書かず、つなげて作る(このファイル自体が検出されないように)。"""
import glob
import os

from helpers import Case

from ri import scan, store

FAKE = {
    "github_token": "gh" + "p_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8",
    "aws_access_key": "AK" + "IA" + "IOSFODNN7" + "EXAMPLE",
    "private_key": "-----BEGIN " + "RSA PRIVATE KEY-----",
    "email": "alice" + "@" + "example.com",
    "slack_token": "xo" + "xb-" + "1234567890-abcdefghij",
    "jwt": "ey" + "JhbGciOiJIUzI1NiJ9.ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
    "credential_assignment": "pass" + "word = hunter2hunter2",
    "url_credentials": "postgres://" + "admin:s3cr3t" + "@db.internal/app",
    "account_number": "4111 1111 " + "1111 1111",
    "absolute_path": "/Users/" + "alice/projects/app",
    "high_entropy": "Zq8" + "xV2mN7pL4kR9tW1yB6cH3jF5gD0sA8eQ",
}
CLEAN = [
    "Entry point for the HTTP server. Reads config from the environment and starts the router.",
    "This directory holds the authentication middleware (session cookies) and calls the user store.",
    "Public API: create_order(customer_id, items) validates items and writes to the orders table.",
    "Uses repo-relative paths like app/auth/session.py and the function verify_session_cookie_signature.",
]


class ScanTest(Case):
    def test_fake_secrets_are_rejected(self):
        for code, text in FAKE.items():
            with self.subTest(code=code):
                reasons = scan.scan_text(f"This module uses {text} when it starts.", patterns=[])
                self.assertIn(code, reasons)

    def test_clean_text_passes(self):
        for text in CLEAN:
            with self.subTest(text=text[:20]):
                self.assertEqual(scan.scan_text(text, patterns=[]), [])

    def test_excluded_paths(self):
        for p in ("tests/fixtures/users.json", "spec/factories/user.rb", "db/seeds/data.sql", ".env", ".env.production",
                  "config/secrets.yml", "deploy/server.pem", "keys/id_rsa", "exports/customers.csv", "report.xlsx",
                  "vendor/lib/x.go", "node_modules/a/index.js", "aws_credentials", "dumps/prod.sql", "testdata/a.txt"):
            with self.subTest(path=p):
                self.assertIsNotNone(scan.excluded(p, extra=[]))
        for p in ("app/auth/session.py", "src/main.go", "README.md", "config/routes.rb", "app/auth/token_store.py"):
            with self.subTest(path=p):
                self.assertIsNone(scan.excluded(p, extra=[]))

    def test_custom_excludes_and_patterns(self):
        os.makedirs(self.data, mode=0o700)
        with open(os.path.join(self.data, "exclude.conf"), "w") as f:
            f.write("# comment\ninternal/customers/*\n")
        with open(os.path.join(self.data, "scan-patterns.conf"), "w") as f:
            f.write("ticket = \\bCUST-[0-9]{5}\\b\n")
        self.assertEqual(scan.excluded("internal/customers/a.py"), "excluded_custom")
        self.assertIn("custom_ticket", scan.scan_text("mentions CUST-12345 here"))

    def test_broken_pattern_file_stops(self):
        os.makedirs(self.data, mode=0o700)
        with open(os.path.join(self.data, "scan-patterns.conf"), "w") as f:
            f.write("bad line without equals\n")
        with self.assertRaises(store.StoreError):
            scan.scan_text("x")

    def test_external_scanner(self):
        os.environ["REPOINDEX_SCANNER"] = "sh -c '! grep -q forbidden-word'"
        self.assertIn("external_scanner", scan.scan_text("has forbidden-word", patterns=[]))
        self.assertEqual(scan.scan_text("fine", patterns=[]), [])
        os.environ["REPOINDEX_SCANNER"] = "/no/such/scanner"
        self.assertIn("external_scanner_failed", scan.scan_text("fine", patterns=[]))


class PutRejectTest(Case):
    def setUp(self):
        super().setUp()
        self.write("app/main.py", "print(1)\n")
        self.head = self.commit()
        self.ri("repo-add", "demo", self.repo)

    def db_bytes(self):
        out = b""
        for p in glob.glob(os.path.join(self.data, "index.db*")):
            with open(p, "rb") as f:
                out += f.read()
        return out

    def test_rejected_text_is_never_persisted(self):
        for code, text in FAKE.items():
            r = self.ri("put", "--repo", "demo", "--kind", "file", "--path", "app/main.py", "--commit", self.head,
                        input=f"Starts with {text}.", ok=False)
            self.assertEqual(r.returncode, 3, code)
            self.assertNotIn(text, r.stdout + r.stderr, code)  # 一致した文字列は出さない
            self.assertIn(code, r.stderr)                         # 理由の符号は出す
            self.assertNotIn(text.encode(), self.db_bytes(), code)
        con = store.connect_rw()
        self.assertEqual(con.execute("SELECT count(*) FROM summaries").fetchone()[0], 0)
        n, final = con.execute("SELECT count(*), min(final) FROM rejections").fetchone()
        self.assertEqual((n, final), (len(FAKE), 1))
        cols = [c[1] for c in con.execute("PRAGMA table_info(rejections)")]
        self.assertNotIn("body", cols)
        con.close()
        listing = self.ri("rejections", "--repo", "demo").stdout
        self.assertIn("aws_access_key", listing)
        for text in FAKE.values():
            self.assertNotIn(text, listing)

    def test_excluded_path_is_not_stored(self):
        r = self.ri("put", "--repo", "demo", "--kind", "file", "--path", "tests/fixtures/users.json",
                    "--commit", self.head, input="sample users", ok=False)
        self.assertEqual(r.returncode, 3)
        self.assertIn("excluded_dir", r.stderr)

    def test_clean_text_is_stored(self):
        self.ri("put", "--repo", "demo", "--kind", "file", "--path", "app/main.py", "--commit", self.head, input=CLEAN[0])
        self.assertEqual(len(self.query("get", "--repo", "demo", "app/main.py")["results"]), 1)

    def test_scan_command_does_not_store(self):
        r = self.ri("scan", input=FAKE["github_token"], ok=False)
        self.assertEqual(r.returncode, 3)
        self.assertIn("github_token", r.stdout)
        self.assertEqual(self.ri("scan", input=CLEAN[1]).stdout.strip(), "ok")

    def test_repo_remove_removes_rejections(self):
        self.ri("put", "--repo", "demo", "--kind", "file", "--path", "app/main.py", "--commit", self.head,
                input=FAKE["email"], ok=False)
        self.ri("repo-remove", "demo")
        con = store.connect_rw()
        self.assertEqual(con.execute("SELECT count(*) FROM rejections").fetchone()[0], 0)
        con.close()
