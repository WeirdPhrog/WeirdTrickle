import unittest

from scripts.release_version import validate_version


class ReleaseVersionTests(unittest.TestCase):
    def test_stable_version_and_matching_tag(self):
        for version in ["1.0.0", "1.0.1", "1.10.0", "2.0.0"]:
            with self.subTest(version=version):
                self.assertEqual(validate_version(version, f"refs/tags/v{version}"), version)

    def test_branch_builds_use_same_version(self):
        self.assertEqual(validate_version("1.0.0", "refs/heads/develop"), "1.0.0")

    def test_invalid_versions_and_mismatched_tags_fail(self):
        for version in ["2026.10.16.20", "01.0.0", "1.0", "v1.0.0", "1.0.0\nextra", "../1.0.0"]:
            with self.subTest(version=version), self.assertRaises(ValueError):
                validate_version(version)
        for ref in ["refs/tags/v1.0.1", "refs/tags/1.0.0", "refs/tags/v2026.10.16.20"]:
            with self.subTest(ref=ref), self.assertRaises(ValueError):
                validate_version("1.0.0", ref)
