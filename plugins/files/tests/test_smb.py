"""The share store against a real SMB server — Samba from
uitest/e2e/files-stand.sh, which runs with SMB1 off and signing required. Skipped
without the stand's env file (FILES_STAND_ENV, default /tmp/stand/files.env)."""

import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, ".."))

import area  # noqa: E402
import extract  # noqa: E402
import store  # noqa: E402


def stand() -> dict:
    path = os.environ.get("FILES_STAND_ENV", "/tmp/stand/files.env")
    if not os.path.exists(path):
        return {}
    out = {}
    with open(path) as f:
        for line in f:
            k, _, v = line.strip().partition("=")
            if k:
                out[k] = v
    return out


@unittest.skipUnless(stand().get("FILES_SMB_SERVER"), "no files stand (uitest/e2e/files-stand.sh up)")
class SmbStoreTests(unittest.TestCase):
    def setUp(self):
        env = stand()
        self.store = store.Smb(lambda: {"username": env["FILES_SMB_USER"], "password": env["FILES_SMB_PASS"]})
        self.root = area.parse(f"smb://{env['FILES_SMB_SERVER']}/{env['FILES_SMB_SHARE']}")

    def tearDown(self):
        self.store.close()

    def test_lists_walks_and_reads_a_hardened_share(self):
        self.assertEqual([e.name for e in self.store.list(self.root)], ["docs", "private"])
        files = sorted(str(loc) for loc, _ in self.store.walk(self.root.join("docs"), lambda l, e: False))
        self.assertIn(str(self.root) + "/docs/2026/contracts/contract.pdf", files)
        self.assertIn(str(self.root) + "/docs/old/kickoff-2018.ppt", files)
        text = extract.text_of("contract.pdf", self.store.read(self.root.join("docs/2026/contracts/contract.pdf")))
        self.assertIn("77-АБ/2026", text)
        entry = self.store.stat(self.root.join("docs/regulations.docx"))
        self.assertEqual((entry.is_dir, entry.size), (False, 6432))
        one = list(self.store.walk(self.root.join("docs/regulations.docx"), lambda l, e: False))
        self.assertEqual([str(l) for l, _ in one], [str(self.root) + "/docs/regulations.docx"], "a file root walks to itself")
        conn = self.store._conns[self.root.server]
        self.assertTrue(conn.is_using_smb2, "negotiated SMB2 without an SMB1 hello")
        self.assertTrue(conn.is_signing_active, "the server requires signing, and the session signs")

    def test_a_missing_path_and_a_wrong_password_are_told_apart(self):
        with self.assertRaises(store.Missing):
            self.store.stat(self.root.join("docs/nope.txt"))
        env = stand()
        wrong = store.Smb(lambda: {"username": env["FILES_SMB_USER"], "password": "not-it"})
        with self.assertRaises(ConnectionError) as ctx:
            wrong.list(self.root)
        self.assertIn("отказал во входе", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
