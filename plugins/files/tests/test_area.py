"""Paths the model names — local or on a share, in any of the spellings
people use — and whether they fall inside the connection's area."""

import os
import sys
import unittest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, ".."))

import area  # noqa: E402


class ParseTests(unittest.TestCase):
    def test_local_paths_are_absolute_and_normalised(self):
        loc = area.parse("/srv/docs/../docs/specs/")
        self.assertEqual((loc.kind, loc.path, str(loc)), ("local", "/srv/docs/specs", "/srv/docs/specs"))
        with self.assertRaises(ValueError):
            area.parse("docs/specs")
        with self.assertRaises(ValueError):
            area.parse("")

    def test_share_paths_in_three_spellings_mean_the_same(self):
        for spelled in (r"\\nas.corp\docs\specs\plan.docx", "//nas.corp/docs/specs/plan.docx",
                        "smb://nas.corp/docs/specs/plan.docx", "SMB://NAS.corp/docs/specs/plan.docx"):
            loc = area.parse(spelled)
            self.assertEqual((loc.kind, loc.server, loc.share, loc.path), ("smb", "nas.corp", "docs", "specs/plan.docx"), spelled)
            self.assertEqual(str(loc), "smb://nas.corp/docs/specs/plan.docx")
        root = area.parse(r"\\nas.corp\docs")
        self.assertEqual((root.share, root.path, str(root)), ("docs", "", "smb://nas.corp/docs"))
        with self.assertRaises(ValueError):
            area.parse(r"\\nas.corp")

    def test_join_and_parent(self):
        loc = area.parse("smb://nas/docs/a")
        self.assertEqual(str(loc.join("b/c.txt")), "smb://nas/docs/a/b/c.txt")
        self.assertEqual(str(loc.parent()), "smb://nas/docs")
        self.assertEqual(str(area.parse("/srv/docs").join("x")), "/srv/docs/x")
        self.assertEqual(area.parse("/srv/docs/x").name, "x")


class AreaTests(unittest.TestCase):
    def test_roots_bound_what_is_inside(self):
        a = area.Area(["/srv/docs", r"\\nas\docs\specs"], [])
        self.assertTrue(a.contains(area.parse("/srv/docs")))
        self.assertTrue(a.contains(area.parse("/srv/docs/2026/plan.docx")))
        self.assertFalse(a.contains(area.parse("/srv/docs2/plan.docx")), "a prefix of the name is not the folder")
        self.assertFalse(a.contains(area.parse("/etc/passwd")))
        self.assertTrue(a.contains(area.parse("smb://nas/docs/specs/x.pdf")))
        self.assertFalse(a.contains(area.parse("smb://nas/docs/other.pdf")))
        self.assertFalse(a.contains(area.parse("smb://other/docs/specs/x.pdf")))

    def test_no_roots_means_every_local_path_but_no_share(self):
        a = area.Area([], [])
        self.assertTrue(a.contains(area.parse("/etc/hostname")))
        self.assertFalse(a.contains(area.parse("smb://nas/docs/x")),
                         "the password must not travel to a server the model named")
        self.assertEqual([str(r) for r in a.roots], [])
        self.assertEqual([str(r) for r in a.starting_points()], ["/"])

    def test_excludes_match_names_and_paths(self):
        a = area.Area(["/srv/docs"], ["*.bak", "~$*", ".git", "archive/2019", "*/tmp/*"])
        for p in ("/srv/docs/a.bak", "/srv/docs/x/~$plan.docx", "/srv/docs/.git", "/srv/docs/.git/HEAD",
                  "/srv/docs/archive/2019", "/srv/docs/archive/2019/x.doc", "/srv/docs/x/tmp/y"):
            self.assertTrue(a.excluded(area.parse(p)), p)
        for p in ("/srv/docs/a.docx", "/srv/docs/archive/2020/x.doc", "/srv/docs/tmp"):
            self.assertFalse(a.excluded(area.parse(p)), p)

    def test_bad_roots_are_named(self):
        with self.assertRaises(ValueError) as ctx:
            area.Area(["relative/path"], [])
        self.assertIn("relative/path", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
